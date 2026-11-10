package inbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
)

func TestParentNotesPriorityAcrossPagesAndProjects(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	spec := validMessage()
	for n, kind := range []string{KindResult, KindBlocker, KindQuestion, KindResult, KindQuestion} {
		spec.Key, spec.Kind = fmt.Sprintf("note-%d", n), kind
		if n%2 == 0 {
			spec.Project = "other"
		} else {
			spec.Project = "sample"
		}
		if _, created, err := s.CreateParentNote(ctx, spec); err != nil || !created {
			t.Fatal("create parent note", err)
		}
	}
	// Neither an ordinary peer question nor another parent's note belongs to
	// this parent's list. Both remain in the same underlying store.
	peer := validMessage()
	peer.Kind = KindQuestion
	if _, _, err := s.Create(ctx, peer); err != nil {
		t.Fatal(err)
	}
	foreign := spec
	foreign.Key, foreign.Recipient.UID = "foreign", "other-parent-uid"
	if _, _, err := s.CreateParentNote(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int64{1, 2, 3, 100} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			paged := pagedFake(s)
			calls := 0
			paged.Live = interceptor.NewClient(paged.Live.(client.WithWatch), interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				calls++
				var o client.ListOptions
				o.ApplyOptions(opts)
				if o.Limit < 1 || o.Limit > limit {
					t.Fatal("unbounded list")
				}
				if !strings.Contains(o.LabelSelector.String(), ParentNoteLabel+"=true") || !strings.Contains(o.LabelSelector.String(), RecipientLabel+"="+recipientLabel(spec.Recipient)) {
					t.Fatal("selector lost the party")
				}
				return c.List(ctx, list, opts...)
			}})
			cursor := ""
			seen := map[string]bool{}
			resultsStarted := false
			// A fixed bound lets an incorrect cursor fail instead of spin.
			for page := 0; page < 8; page++ {
				before := calls
				items, next, err := paged.ListParentNotes(ctx, spec.Recipient, false, limit, cursor)
				if err != nil || calls-before > 2 || int64(len(items)) > limit {
					t.Fatal("unbounded or failed page", err)
				}
				for _, item := range items {
					if seen[item.Name] {
						t.Fatal("duplicate across pages")
					}
					seen[item.Name] = true
					if item.Spec.Kind == KindResult {
						resultsStarted = true
					} else if resultsStarted {
						t.Fatal("urgent note after a result")
					}
				}
				cursor = next
				if cursor == "" {
					break
				}
			}
			if cursor != "" || len(seen) != 5 {
				t.Fatal("pagination lost a note or failed to finish", len(seen))
			}
		})
	}
	items, _, err := s.ListParentNotes(ctx, spec.Sender, true, 100, "")
	if err != nil || len(items) != 6 {
		t.Fatal("sent box did not select exact sender", err)
	}
	items, _, err = s.List(ctx, peer.Project, &peer.Recipient, 100, "")
	if err != nil || len(items) != 1 || IsParentNote(&items[0]) {
		t.Fatal("peer list exposed notes", err)
	}
	items, _, err = s.ListSent(ctx, peer.Project, peer.Sender, 100, "")
	if err != nil || len(items) != 1 || IsParentNote(&items[0]) {
		t.Fatal("peer sent list exposed notes", err)
	}
}

func TestParentNotesExactPriorityBoundary(t *testing.T) {
	s := storeFixture(t)
	spec := validMessage()
	spec.Kind = KindBlocker
	if _, _, err := s.CreateParentNote(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	spec.Key, spec.Kind = "result", KindResult
	if _, _, err := s.CreateParentNote(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	s = pagedFake(s)
	items, next, err := s.ListParentNotes(context.Background(), spec.Recipient, false, 1, "")
	if err != nil || len(items) != 1 || items[0].Spec.Kind != KindBlocker || next != "1:" {
		t.Fatal("priority boundary lost next phase", err)
	}
	items, next, err = s.ListParentNotes(context.Background(), spec.Recipient, false, 1, next)
	if err != nil || len(items) != 1 || items[0].Spec.Kind != KindResult || next != "" {
		t.Fatal("result unreachable at exact boundary", err)
	}
}

func TestParentNotesStrictLabelsAndHiddenInvalidRecord(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	spec := validMessage()
	spec.Kind = KindQuestion
	m, _, err := s.CreateParentNote(ctx, spec)
	if err != nil || !IsParentNote(m) {
		t.Fatal("parent marker missing", err)
	}
	got, err := s.GetParentNote(ctx, m.Name, spec.Sender)
	if err != nil || got.UID != m.UID {
		t.Fatal("sender read", err)
	}
	m.Labels["unexpected"] = "metadata"
	if err := s.Client.Update(ctx, m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, m.Name); !errors.Is(err, ErrUnavailable) {
		t.Fatal("extra label accepted", err)
	}
	stranger := spec.Sender
	stranger.UID = "stranger-uid"
	if _, err := s.GetParentNote(ctx, m.Name, stranger); !errors.Is(err, ErrNotFound) {
		t.Fatal("hidden corrupt note distinguished from absence", err)
	}
}

func TestParentNotesSharePeerLimits(t *testing.T) {
	for _, cap := range []string{"sender", "project"} {
		t.Run(cap, func(t *testing.T) {
			s := storeFixture(t)
			if cap == "sender" {
				s.Limits.PerSession = 1
			} else {
				s.Limits.PerProject = 1
			}
			spec := validMessage()
			if _, _, err := s.Create(context.Background(), spec); err != nil {
				t.Fatal(err)
			}
			spec.Key, spec.Kind = "parent-note", KindBlocker
			if _, _, err := s.CreateParentNote(context.Background(), spec); !errors.Is(err, ErrLimit) {
				t.Fatal("peer message did not count toward note limit", err)
			}
		})
	}
}

func TestParentNotesBoundedQueries(t *testing.T) {
	s := storeFixture(t)
	for _, cursor := range []string{"x", "2:", "0:\n", "1:" + strings.Repeat("x", 4095)} {
		if _, _, err := s.ListParentNotes(context.Background(), validMessage().Recipient, false, 1, cursor); !errors.Is(err, ErrUnavailable) {
			t.Fatal("invalid cursor accepted")
		}
	}
	for _, limit := range []int64{0, 101} {
		if _, _, err := s.ListParentNotes(context.Background(), validMessage().Recipient, false, limit, ""); !errors.Is(err, ErrUnavailable) {
			t.Fatal("invalid limit accepted")
		}
	}
	for _, bad := range []coord.SessionIdentity{{}, {Namespace: "n", Name: "name", UID: ""}} {
		if _, _, err := s.ListParentNotes(context.Background(), bad, false, 1, ""); !errors.Is(err, ErrUnavailable) {
			t.Fatal("invalid party accepted")
		}
	}
}
