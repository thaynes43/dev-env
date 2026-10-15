package coordretention

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type listCall struct {
	kind              string
	namespace, cursor string
	limit             int64
}
type pagingReader struct {
	client.Reader
	calls []listCall
}

func (r *pagingReader) List(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
	o := client.ListOptions{}
	for _, opt := range opts {
		opt.ApplyToList(&o)
	}
	u := list.(*unstructured.UnstructuredList)
	r.calls = append(r.calls, listCall{u.GroupVersionKind().Kind, o.Namespace, o.Continue, o.Limit})
	if o.Continue == "" {
		u.Items = []unstructured.Unstructured{{Object: map[string]any{"metadata": map[string]any{"name": "bad", "namespace": o.Namespace, "uid": "u", "resourceVersion": "1"}}}}
		u.SetContinue("next")
	} else {
		u.SetContinue("")
	}
	return nil
}

func TestSummariesUsesConfiguredPagesAndContinuation(t *testing.T) {
	r := &pagingReader{}
	out, err := Summaries(context.Background(), ListConfig{Reader: r, Namespace: "dev-env-system", Enabled: map[Kind]bool{TaskClaim: true, PublishedContext: true, InboxMessage: true, InboxReadAck: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Fatalf("summaries = %d", len(out))
	}
	if len(r.calls) != 8 {
		t.Fatalf("list calls = %#v", r.calls)
	}
	for _, call := range r.calls {
		if call.namespace != "dev-env-system" {
			t.Fatalf("namespace = %#v", call)
		}
		want := int64(50)
		if call.kind == "PublishedContextList" || call.kind == "InboxMessageList" {
			want = 10
		}
		if call.limit != want {
			t.Fatalf("limit = %#v, want %d", call, want)
		}
	}
}
