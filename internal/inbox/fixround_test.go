package inbox

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
)

func isDryRun(opts []client.CreateOption) bool {
	var o client.CreateOptions
	o.ApplyOptions(opts)
	return slices.Contains(o.DryRun, metav1.DryRunAll)
}

// oldSchema stands in for an InboxMessage CRD from before kind and inReplyTo:
// its API server prunes both silently, in a dry-run as in a real create.
func oldSchema(s KubeStore, real *int) KubeStore {
	s.Client = interceptor.NewClient(s.Client.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if !isDryRun(opts) {
			*real++
			return c.Create(ctx, obj, opts...)
		}
		if err := c.Create(ctx, obj, opts...); err != nil {
			return err
		}
		if u, ok := obj.(*unstructured.Unstructured); ok {
			unstructured.RemoveNestedField(u.Object, "spec", "kind")
			unstructured.RemoveNestedField(u.Object, "spec", "inReplyTo")
		}
		return nil
	}})
	return s
}

func TestInboxCreateRefusesAnOldSchemaBeforeWriting(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	q, _ := questionAndReply(t, s)
	real := 0
	old := oldSchema(s, &real)

	question := validMessage()
	question.Key, question.Kind = "q-old", KindQuestion
	reply := validMessage()
	reply.Key, reply.Kind = "r-old", KindReply
	reply.Sender, reply.Recipient = question.Recipient, question.Sender
	ref := Ref(q)
	reply.InReplyTo = &ref
	for _, spec := range []coord.InboxMessageSpec{question, reply} {
		if _, _, err := old.Create(ctx, spec); !errors.Is(err, ErrSchemaNotUpgraded) {
			t.Fatalf("%s on an old schema: %v", spec.Kind, err)
		}
		if _, err := s.Get(ctx, MessageName(spec.Sender, spec.Key)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s was written: %v", spec.Kind, err)
		}
	}
	if real != 0 {
		t.Fatal("a real create followed the failed schema check", real)
	}
	// Only the newer fields are at risk: a plain message needs no check.
	plain := validMessage()
	plain.Key = "plain-old"
	if _, created, err := old.Create(ctx, plain); err != nil || !created {
		t.Fatal("plain message on an old schema", err)
	}
	// A retry of a stored question confirms it without a new schema check.
	if m, created, err := old.Create(ctx, q.Spec); err != nil || created || m.UID != q.UID {
		t.Fatal("retry of a stored question", err)
	}
	// A dry-run that fails writes nothing and is Unknown, not a schema verdict.
	failing := s
	failing.Client = interceptor.NewClient(s.Client.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if isDryRun(opts) {
			return apierrors.NewServiceUnavailable("synthetic")
		}
		return c.Create(ctx, obj, opts...)
	}})
	if _, _, err := failing.Create(ctx, question); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrSchemaNotUpgraded) {
		t.Fatal("a failed dry-run was not Unknown", err)
	}
	// With the schema upgraded, the same intent is created.
	if _, created, err := s.Create(ctx, question); err != nil || !created {
		t.Fatal("question on the upgraded schema", err)
	}
}

// pagedFake gives the fake client the Limit and Continue it ignores.
func pagedFake(s KubeStore) KubeStore {
	s.Live = interceptor.NewClient(s.Live.(client.WithWatch), interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		var o client.ListOptions
		o.ApplyOptions(opts)
		if err := c.List(ctx, list, &client.ListOptions{Namespace: o.Namespace, LabelSelector: o.LabelSelector}); err != nil {
			return err
		}
		var page func(start, end int)
		var total int
		var contp *string
		switch l := list.(type) {
		case *coord.InboxMessageList:
			total, contp = len(l.Items), &l.Continue
			page = func(start, end int) { l.Items = l.Items[start:end] }
		case *metav1.PartialObjectMetadataList:
			total, contp = len(l.Items), &l.Continue
			page = func(start, end int) { l.Items = l.Items[start:end] }
		default:
			return nil
		}
		if o.Limit == 0 {
			return nil
		}
		start, _ := strconv.Atoi(o.Continue)
		end := min(start+int(o.Limit), total)
		*contp = ""
		if end < total {
			*contp = strconv.Itoa(end)
		}
		page(start, end)
		return nil
	}})
	return s
}

func TestInboxReplyIndexCoversTheProjectLimit(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	q, _ := questionAndReply(t, s)
	const replies = 1200 // past the old 1000-reply scan, within the project limit
	for i := 0; i < replies; i++ {
		spec := validMessage()
		spec.Key, spec.Kind = fmt.Sprintf("bulk-%d", i), KindReply
		spec.Sender, spec.Recipient = q.Spec.Recipient, q.Spec.Sender
		ref := Ref(q)
		spec.InReplyTo = &ref
		m := &coord.InboxMessage{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: MessageName(spec.Sender, spec.Key), Labels: messageLabels(spec)}, Spec: spec}
		if err := s.Client.Create(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	paged := pagedFake(s)
	got, err := paged.ReplyIndex(ctx, "sample", nil, nil)
	if err != nil || len(got) != replies+1 {
		t.Fatalf("an index within the project limit: %d replies, %v", len(got), err)
	}
	// A limit configured below the index leaves it Unknown rather than cut short.
	paged.Limits.PerProject = 1000
	if _, err := paged.ReplyIndex(ctx, "sample", nil, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatal("an index past the project limit was not Unknown", err)
	}
}
