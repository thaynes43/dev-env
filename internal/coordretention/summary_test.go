package coordretention

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/contextpublication"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/taskclaim"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const retentionNS = "dev-env-system"

var (
	sender    = coord.SessionIdentity{Namespace: "dev-agents", Name: "sender", UID: "11111111-1111-4111-8111-111111111111"}
	recipient = coord.SessionIdentity{Namespace: "dev-agents", Name: "recipient", UID: "22222222-2222-4222-8222-222222222222"}
)

func actor() coord.ClaimActor {
	return coord.ClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env-agent", PodName: "sender", PodUID: "33333333-3333-4333-8333-333333333333"}
}
func provenance() coord.ClaimProvenance {
	return coord.ClaimProvenance{CatalogRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RulesRevision: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", GitHub: "thaynes43/dev-env", DefaultBranch: "main", InitialPinState: "Unknown"}
}
func messageSpec() coord.InboxMessageSpec {
	return coord.InboxMessageSpec{Project: "sample", Key: "retention-1", Sender: sender, Recipient: recipient, Actor: actor(), Provenance: provenance(), Repository: "repo", Provider: "claude", RequestedModel: "claude-opus-5-5", Text: "finite synthetic message"}
}
func claimSpec() coord.TaskClaimSpec {
	return coord.TaskClaimSpec{Identity: coord.TaskIdentity{Project: "sample", Repository: "repo", TaskKey: "issue:301"}, Owner: sender, Actor: actor(), Provenance: provenance()}
}
func contextSpec() coord.PublishedContextSpec {
	const text = "finite synthetic context"
	return coord.PublishedContextSpec{Identity: coord.ContextIdentity{SessionUID: sender.UID, Kind: "handoff", Key: "retention-301"}, Source: sender, SourceGeneration: 1, SourceParent: "dev-env-system/human", Project: "sample", Repository: "repo", Actor: actor(), Provenance: provenance(), Attribution: coord.ContextAttribution{Provider: "claude", RequestedModel: "claude-opus-5-5", ObservedModelState: "Unknown", UsageState: "Unknown", CostState: "Unknown"}, Origin: "ExplicitPublished", Completeness: "ExplicitExcerpt", StopState: "Unknown", Text: text, ContentSHA256: apiv1.ContextHash(text), ByteCount: int32(len(text))}
}

func validObjects(t *testing.T) map[string][]unstructured.Unstructured {
	t.Helper()
	claim, context, message := claimSpec(), contextSpec(), messageSpec()
	ack := coord.InboxReadAckSpec{Project: message.Project, MessageName: inbox.MessageName(message.Sender, message.Key), MessageUID: "44444444-4444-4444-8444-444444444444", Recipient: message.Recipient, Actor: actor()}
	if !taskclaim.ValidSpec(claim) || !contextpublication.ValidSpec(context) || !inbox.ValidMessage(message) || !inbox.ValidAck(ack) {
		t.Fatal("fixture must be accepted by public validators")
	}
	toUnstructured := func(v runtime.Object) unstructured.Unstructured {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(v)
		if err != nil {
			t.Fatal(err)
		}
		return unstructured.Unstructured{Object: m}
	}
	return map[string][]unstructured.Unstructured{
		"TaskClaimList":        {toUnstructured(&coord.TaskClaim{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: taskclaim.Name(claim.Identity), UID: types.UID("55555555-5555-4555-8555-555555555555"), ResourceVersion: "10"}, Spec: claim})},
		"PublishedContextList": {toUnstructured(&coord.PublishedContext{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: contextpublication.Name(context.Identity), UID: types.UID("66666666-6666-4666-8666-666666666666"), ResourceVersion: "20"}, Spec: context})},
		"InboxMessageList":     {toUnstructured(&coord.InboxMessage{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: inbox.MessageName(message.Sender, message.Key), UID: types.UID(ack.MessageUID), ResourceVersion: "30"}, Spec: message})},
		"InboxReadAckList":     {toUnstructured(&coord.InboxReadAck{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: inbox.AckName(ack.MessageUID, ack.Recipient.UID), UID: types.UID("77777777-7777-4777-8777-777777777777"), ResourceVersion: "40"}, Spec: ack})},
	}
}

type listCall struct {
	kind, namespace, cursor string
	limit                   int64
}
type fixtureReader struct {
	client.Reader
	objects map[string][]unstructured.Unstructured
	calls   []listCall
	err     error
	repeat  bool
}

func (r *fixtureReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.err != nil {
		return r.err
	}
	o := client.ListOptions{}
	for _, opt := range opts {
		opt.ApplyToList(&o)
	}
	u := list.(*unstructured.UnstructuredList)
	kind := u.GroupVersionKind().Kind
	r.calls = append(r.calls, listCall{kind, o.Namespace, o.Continue, o.Limit})
	if o.Continue == "" {
		u.Items = append(u.Items, r.objects[kind]...)
		if len(r.objects[kind]) > 0 {
			u.SetContinue("next")
		}
	} else if r.repeat {
		u.SetContinue("next")
	}
	return nil
}

func TestSummariesRealSchemaFixturesAndConfiguredPaging(t *testing.T) {
	r := &fixtureReader{objects: validObjects(t)}
	out, err := Summaries(context.Background(), ListConfig{Reader: r, Namespace: retentionNS, Enabled: map[Kind]bool{TaskClaim: true, PublishedContext: true, InboxMessage: true, InboxReadAck: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Fatalf("summaries = %d", len(out))
	}
	for _, s := range out {
		if !s.Valid || s.Namespace != retentionNS || s.Name == "" || s.UID == "" || s.ResourceVersion == "" || s.Project != "sample" {
			t.Fatalf("invalid compact summary: %#v", s)
		}
	}
	if got := fmt.Sprint(out[0].Kind, ",", out[1].Kind, ",", out[2].Kind, ",", out[3].Kind); got != "InboxMessage,InboxReadAck,PublishedContext,TaskClaim" {
		t.Fatalf("stable order = %s", got)
	}
	if len(r.calls) != 8 {
		t.Fatalf("list calls = %#v", r.calls)
	}
	for _, call := range r.calls {
		want := int64(50)
		if call.kind == "PublishedContextList" || call.kind == "InboxMessageList" {
			want = 10
		}
		if call.namespace != retentionNS || call.limit != want {
			t.Fatalf("call = %#v, want namespace %q limit %d", call, retentionNS, want)
		}
	}
}

func TestSummariesRefusesReaderAndCursorFailures(t *testing.T) {
	for name, reader := range map[string]*fixtureReader{"list error": {err: errors.New("list failed")}, "repeated cursor": {objects: validObjects(t), repeat: true}} {
		t.Run(name, func(t *testing.T) {
			if _, err := Summaries(context.Background(), ListConfig{Reader: reader, Namespace: retentionNS, Enabled: map[Kind]bool{TaskClaim: true}}); err == nil {
				t.Fatal("Summaries error = nil")
			}
		})
	}
}

func TestSummariesRejectsMalformedScopeAndCancellation(t *testing.T) {
	r := &fixtureReader{objects: validObjects(t)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Summaries(ctx, ListConfig{Reader: r, Namespace: retentionNS, Enabled: map[Kind]bool{TaskClaim: true}}); err == nil {
		t.Fatal("cancelled Summaries error = nil")
	}
	objects := validObjects(t)
	item := objects["TaskClaimList"][0]
	item.SetName("wrong-name")
	r = &fixtureReader{objects: map[string][]unstructured.Unstructured{"TaskClaimList": {item}}}
	out, err := Summaries(context.Background(), ListConfig{Reader: r, Namespace: retentionNS, Enabled: map[Kind]bool{TaskClaim: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Valid {
		t.Fatalf("malformed identity accepted: %#v", out)
	}
}

func TestSummariesStableProjectNameUIDOrder(t *testing.T) {
	spec := claimSpec()
	var items []unstructured.Unstructured
	for i, project := range []string{"sample", "aaa", "sample", "sample"} {
		spec.Identity.Project = project
		spec.Identity.TaskKey = "task:b"
		if i == 2 {
			spec.Identity.TaskKey = "task:a"
		}
		obj := &coord.TaskClaim{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: taskclaim.Name(spec.Identity), UID: types.UID(fmt.Sprintf("%08d-1111-4111-8111-111111111111", 4-i)), ResourceVersion: "1"}, Spec: spec}
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, unstructured.Unstructured{Object: raw})
	}
	out, err := Summaries(context.Background(), ListConfig{Reader: &fixtureReader{objects: map[string][]unstructured.Unstructured{"TaskClaimList": items}}, Namespace: retentionNS, Enabled: map[Kind]bool{TaskClaim: true}})
	if err != nil || len(out) != 4 {
		t.Fatalf("summary order: %#v %v", out, err)
	}
	keys := make([]string, 0, len(out))
	for _, s := range out {
		if !s.Valid {
			t.Fatalf("invalid ordering fixture: %#v", s)
		}
		keys = append(keys, string(s.Kind)+"/"+s.Project+"/"+s.Name+"/"+s.UID)
	}
	if out[0].Project != "aaa" || !sort.StringsAreSorted(keys) {
		t.Fatalf("unstable ordering: %q", keys)
	}
}
