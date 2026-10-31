package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
)

// bigReply stores a maximum-size reply to q under key, written straight to the
// client so a test can seed many of them.
func bigReply(t *testing.T, s KubeStore, q *coord.InboxMessage, key string) *coord.InboxMessage {
	t.Helper()
	spec := validMessage()
	spec.Key, spec.Kind = key, KindReply
	spec.Sender, spec.Recipient = q.Spec.Recipient, q.Spec.Sender
	spec.Text = strings.Repeat("x", MaxTextBytes)
	ref := Ref(q)
	spec.InReplyTo = &ref
	m := &coord.InboxMessage{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: MessageName(spec.Sender, spec.Key), Labels: messageLabels(spec)}, Spec: spec}
	if err := s.Client.Create(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return m
}

// recordingLists notes the Go type of each list and the name of each Get that
// the store issues through its live reader.
type recordingLists struct {
	mu    sync.Mutex
	lists []string
	gets  []string
}

func recorded(s KubeStore) (KubeStore, *recordingLists) {
	rec := &recordingLists{}
	s.Live = interceptor.NewClient(s.Live.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			rec.mu.Lock()
			rec.lists = append(rec.lists, reflect.TypeOf(list).String())
			rec.mu.Unlock()
			return c.List(ctx, list, opts...)
		},
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			rec.mu.Lock()
			rec.gets = append(rec.gets, key.Name)
			rec.mu.Unlock()
			return c.Get(ctx, key, obj, opts...)
		},
	})
	return s, rec
}

// The reply index is a metadata-only scan. A full-object list would hold every
// reply's text (up to 16 KiB each, 4096 of them) in memory for one request.
func TestInboxReplyIndexHoldsNoText(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	q, _ := questionAndReply(t, s)
	const replies = 40
	for i := range replies {
		bigReply(t, s, q, fmt.Sprintf("big-%d", i))
	}
	rec, log := recorded(s)
	got, err := rec.ReplyIndex(ctx, "sample", nil, nil)
	if err != nil || len(got) != replies+1 {
		t.Fatal("reply index", len(got), err)
	}
	if len(log.lists) == 0 {
		t.Fatal("the index issued no list")
	}
	for _, kind := range log.lists {
		if kind != "*v1.PartialObjectMetadataList" {
			t.Fatalf("the index listed full objects (%s), which carry reply text", kind)
		}
	}
	// An entry has a fixed few string fields and nothing else.
	typ := reflect.TypeFor[ReplyEntry]()
	for f := range typ.Fields() {
		if f.Type.Kind() != reflect.String || f.Name == "Text" {
			t.Fatalf("index entry field %s %s is not a short string", f.Name, f.Type)
		}
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if limit := len(got) * 320; len(data) > limit {
		t.Fatalf("the index holds %d bytes for %d replies, past %d: it retains more than metadata", len(data), len(got), limit)
	}
	if strings.Contains(string(data), "xxxx") {
		t.Fatal("reply text in the index")
	}
}

func TestInboxRepliesInReadsOnlyMatchedReplies(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	q, r := questionAndReply(t, s)
	other := validMessage()
	other.Key, other.Kind = "other-question", KindQuestion
	q2, _, err := s.Create(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	bigReply(t, s, q2, "answer-other")
	for i := range 6 {
		bigReply(t, s, q2, fmt.Sprintf("noise-%d", i)) // pile on replies to other questions
	}

	index, err := s.ReplyIndex(ctx, "sample", nil, nil)
	if err != nil || len(index) != 8 {
		t.Fatal("index", len(index), err)
	}
	rec, log := recorded(s)
	got, err := rec.RepliesIn(ctx, q, index)
	if err != nil || len(got) != 1 || got[0].UID != r.UID || got[0].Spec.Text != r.Spec.Text {
		t.Fatal("replies in", err, got)
	}
	if len(log.gets) != 1 || log.gets[0] != r.Name || len(log.lists) != 0 {
		t.Fatalf("only the matched reply is read, by name: gets %v lists %v", log.gets, log.lists)
	}
	// Another question reads its own replies, oldest first.
	got, err = s.RepliesIn(ctx, q2, index)
	if err != nil || len(got) != 7 {
		t.Fatal("other question's replies", len(got), err)
	}
	for _, m := range got {
		if m.Spec.InReplyTo == nil || *m.Spec.InReplyTo != Ref(q2) {
			t.Fatal("a reply to another question", m.Name)
		}
	}

	// A question with no reply in the index has none, and reads nothing.
	third := validMessage()
	third.Key, third.Kind = "third-question", KindQuestion
	q3, _, err := s.Create(ctx, third)
	if err != nil {
		t.Fatal(err)
	}
	rec, log = recorded(s)
	if got, err := rec.RepliesIn(ctx, q3, index); err != nil || len(got) != 0 || len(log.gets) != 0 {
		t.Fatal("unanswered question", err, got, log.gets)
	}
}

func TestInboxRepliesInIsUnknownRatherThanAGuess(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	q, r := questionAndReply(t, s)
	index, err := s.ReplyIndex(ctx, "sample", nil, nil)
	if err != nil || len(index) != 1 {
		t.Fatal("index", err)
	}
	if _, err := s.RepliesIn(ctx, r, index); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a reply has replies", err)
	}
	// The reply is gone, or replaced, between the scan and the read.
	gone := append([]ReplyEntry(nil), index...)
	gone[0].Name = MessageName(r.Spec.Sender, "never-stored")
	if _, err := s.RepliesIn(ctx, q, gone); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a missing reply was not Unknown", err)
	}
	swapped := append([]ReplyEntry(nil), index...)
	swapped[0].UID = "another-uid"
	if _, err := s.RepliesIn(ctx, q, swapped); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a replaced reply was not Unknown", err)
	}
	// More than MaxReplies matches is Unknown without reading any.
	many := make([]ReplyEntry, MaxReplies+1)
	for i := range many {
		many[i] = index[0]
	}
	rec, log := recorded(s)
	if _, err := rec.RepliesIn(ctx, q, many); !errors.Is(err, ErrUnavailable) || len(log.gets) != 0 {
		t.Fatal("too many replies", err, log.gets)
	}
	// A matched reply from the wrong party is Unknown, as in Replies.
	spec := validMessage()
	spec.Kind, spec.Text = KindReply, "forged"
	spec.Recipient = q.Spec.Sender
	spec.InReplyTo = &coord.InboxMessageRef{Name: q.Name, UID: string(q.UID)}
	spec.Sender = coord.SessionIdentity{Namespace: "dev-agents", Name: "bystander", UID: "bystander-uid"}
	spec.Key = "forged-2"
	wrong := &coord.InboxMessage{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: MessageName(spec.Sender, spec.Key), Labels: messageLabels(spec)}, Spec: spec}
	if err := s.Client.Create(ctx, wrong); err != nil {
		t.Fatal(err)
	}
	index, err = s.ReplyIndex(ctx, "sample", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RepliesIn(ctx, q, index); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a reply from the wrong party was accepted", err)
	}
}

func TestInboxReplyIndexRefusesInvalidMetadata(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	q, _ := questionAndReply(t, s)
	for name, mutate := range map[string]func(*coord.InboxMessage){
		"annotation":    func(m *coord.InboxMessage) { m.Annotations = map[string]string{"x": "y"} },
		"finalizer":     func(m *coord.InboxMessage) { m.Finalizers = []string{"x"} },
		"missing label": func(m *coord.InboxMessage) { delete(m.Labels, InReplyToLabel) },
		"extra label":   func(m *coord.InboxMessage) { m.Labels["extra"] = "y" },
	} {
		fresh := storeFixture(t)
		bad := bigReply(t, fresh, q, "bad")
		mutate(bad)
		if err := fresh.Client.Update(ctx, bad); err != nil {
			t.Fatal(name, err)
		}
		if _, err := fresh.ReplyIndex(ctx, "sample", nil, nil); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s: index of invalid metadata was not Unknown: %v", name, err)
		}
	}
}

// D2: an API server that prunes inReplyTo but keeps kind is still an old
// schema. The reply would store with kind=reply and no correlation.
func TestInboxCreateRefusesAServerThatStripsOnlyInReplyTo(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	q, _ := questionAndReply(t, s)
	strip := func(field string) KubeStore {
		old := s
		old.Client = interceptor.NewClient(s.Client.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if !isDryRun(opts) {
				return c.Create(ctx, obj, opts...)
			}
			if err := c.Create(ctx, obj, opts...); err != nil {
				return err
			}
			if u, ok := obj.(*unstructured.Unstructured); ok {
				unstructured.RemoveNestedField(u.Object, "spec", field)
			}
			return nil
		}})
		return old
	}
	reply := validMessage()
	reply.Key, reply.Kind = "r-partial", KindReply
	reply.Sender, reply.Recipient = q.Spec.Recipient, q.Spec.Sender
	ref := Ref(q)
	reply.InReplyTo = &ref
	for _, field := range []string{"inReplyTo", "kind"} {
		if _, _, err := strip(field).Create(ctx, reply); !errors.Is(err, ErrSchemaNotUpgraded) {
			t.Fatalf("a server stripping only %s was not refused: %v", field, err)
		}
		if _, err := s.Get(ctx, MessageName(reply.Sender, reply.Key)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a reply was written after the %s strip: %v", field, err)
		}
	}
	// The same reply on the upgraded schema is created.
	if _, created, err := s.Create(ctx, reply); err != nil || !created {
		t.Fatal("reply on the upgraded schema", err)
	}
}
