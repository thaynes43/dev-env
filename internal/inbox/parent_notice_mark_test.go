package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
)

func TestParentNoticeMarkIsAnnotationsOnlyAndStaysReadable(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	spec := validMessage()
	spec.Kind = KindQuestion
	spec.Key = "note-mark"
	spec.Text = "NOTE-TEXT-MUST-NOT-BE-TYPED"
	m, created, err := s.CreateParentNote(ctx, spec)
	if err != nil || !created || m.ResourceVersion == "" {
		t.Fatal(err, created)
	}
	var raw []byte
	base := s.Client.(client.WithWatch)
	s.Client = interceptor.NewClient(base, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, _ ...client.PatchOption) error {
			data, err := patch.Data(obj)
			if err != nil {
				return err
			}
			raw = append([]byte(nil), data...)
			var body map[string]json.RawMessage
			if json.Unmarshal(data, &body) != nil || len(body) != 1 {
				return errors.New("patch is not metadata only")
			}
			var meta map[string]json.RawMessage
			if json.Unmarshal(body["metadata"], &meta) != nil || len(meta) != 2 || meta["annotations"] == nil || meta["resourceVersion"] == nil {
				return errors.New("patch is not annotations and resourceVersion")
			}
			var ann map[string]string
			if json.Unmarshal(meta["annotations"], &ann) != nil || len(ann) != 1 || ann[ParentNoticeAnnotation] == "" {
				return errors.New("annotation")
			}
			if !strings.Contains(string(meta["resourceVersion"]), m.ResourceVersion) {
				return errors.New("resourceVersion " + string(meta["resourceVersion"]))
			}
			obj.SetAnnotations(ann)
			return c.Update(ctx, obj)
		},
	})
	at := time.Date(2026, 11, 11, 4, 38, 0, 1, time.UTC)
	if err := s.MarkParentNotice(ctx, m, at); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), spec.Text) || strings.Contains(string(raw), spec.Key) || strings.Contains(string(raw), `"spec"`) {
		t.Fatalf("patch carried more than the mark: %s", raw)
	}
	got, err := s.GetParentNote(ctx, m.Name, spec.Recipient)
	if err != nil || got.Annotations[ParentNoticeAnnotation] != at.Format(time.RFC3339Nano) || got.Spec.Text != spec.Text {
		t.Fatalf("marked note is not readable: %v %+v", err, got)
	}
	got.Annotations["other"] = "x"
	if err := base.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetParentNote(ctx, m.Name, spec.Recipient); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a second annotation was accepted", err)
	}
}

func TestParentNoticeAnnotationOnAPeerMessageStaysInvalid(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	m, _, err := s.Create(ctx, validMessage())
	if err != nil {
		t.Fatal(err)
	}
	m.Annotations = map[string]string{ParentNoticeAnnotation: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := s.Client.Update(ctx, m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, m.Name); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a peer message accepted the parent-notice annotation", err)
	}
}

func TestNoticeParentsSkipsMarkedCallerAndPlain(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	spec := validMessage()
	spec.Kind = KindQuestion
	spec.Key = "open-note"
	if _, _, err := s.CreateParentNote(ctx, spec); err != nil {
		t.Fatal(err)
	}
	marked := spec
	marked.Key = "marked-note"
	m, _, err := s.CreateParentNote(ctx, marked)
	if err != nil {
		t.Fatal(err)
	}
	m.Annotations = map[string]string{ParentNoticeAnnotation: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := s.Client.Update(ctx, m); err != nil {
		t.Fatal(err)
	}
	plain := spec
	plain.Key = "plain-note"
	plain.Kind = ""
	if _, _, err := s.Create(ctx, plain); err != nil {
		t.Fatal(err)
	}
	reply := spec
	reply.Key = "reply-note"
	reply.Kind = KindReply
	reply.Sender, reply.Recipient = spec.Recipient, spec.Sender
	reply.InReplyTo = &coord.InboxMessageRef{Name: m.Name, UID: string(m.UID)}
	if _, _, err := s.CreateParentNote(ctx, reply); err != nil {
		t.Fatal(err)
	}
	parents, err := s.NoticeParents(ctx)
	if err != nil || len(parents) != 1 || parents[0] != spec.Recipient {
		t.Fatalf("parents %+v %v", parents, err)
	}
	notes, hit, err := s.NoticeNotes(ctx, spec.Recipient)
	if err != nil || hit || len(notes) != 2 {
		t.Fatalf("notes %d hit %v %v", len(notes), hit, err)
	}
}

// More than 512 notes to one parent, most already marked, are still listed.
// The old refusal counted marked notes and dropped every later pass.
func TestNoticeNotesServesMoreThan512(t *testing.T) {
	ctx := context.Background()
	s := pagedFake(storeFixture(t))
	spec := validMessage()
	spec.Kind = KindQuestion
	const total = 513
	var unmarked string
	for i := 0; i < total; i++ {
		note := spec
		note.Key = fmt.Sprintf("fan-%d", i)
		if i >= 256 {
			note.Sender.Name = "sender-b"
			note.Sender.UID = "sender-b-uid"
		}
		m, created, err := s.CreateParentNote(ctx, note)
		if err != nil || !created {
			t.Fatal(i, err, created)
		}
		if i == total-1 {
			unmarked = m.Name
			continue
		}
		m.Annotations = map[string]string{ParentNoticeAnnotation: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)}
		if err := s.Client.Update(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	notes, hit, err := s.NoticeNotes(ctx, spec.Recipient)
	if err != nil || hit || len(notes) != total {
		t.Fatalf("notes %d hit %v %v", len(notes), hit, err)
	}
	found := false
	marked := 0
	for i := range notes {
		if _, ok := notes[i].Annotations[ParentNoticeAnnotation]; ok {
			marked++
		}
		if notes[i].Name == unmarked {
			found = true
		}
	}
	if !found || marked != total-1 {
		t.Fatalf("unmarked found %v marked %d", found, marked)
	}
}

func TestNoticeNotesStopsAtThePageBudgetAndServesWhatItRead(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	spec := validMessage()
	spec.Kind = KindQuestion
	spec.Key = "budget-note"
	m, _, err := s.CreateParentNote(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	base := s.Live.(client.WithWatch)
	s.Live = interceptor.NewClient(base, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		calls++
		if calls > NoticePageBudget+1 {
			t.Fatal("read past the page budget")
		}
		page, ok := list.(*coord.InboxMessageList)
		if !ok {
			t.Fatalf("%T", list)
		}
		page.Items = []coord.InboxMessage{*m}
		page.Continue = "more"
		return nil
	}})
	notes, hit, err := s.NoticeNotes(ctx, spec.Recipient)
	if err != nil || !hit || len(notes) != NoticePageBudget || calls != NoticePageBudget {
		t.Fatalf("notes %d hit %v calls %d err %v", len(notes), hit, calls, err)
	}
}
