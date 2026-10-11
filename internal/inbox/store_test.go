package inbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
)

func validMessage() coord.InboxMessageSpec {
	return coord.InboxMessageSpec{Project: "sample", Key: "logical-1", Sender: coord.SessionIdentity{Namespace: "dev-agents", Name: "sender", UID: "sender-uid"}, Recipient: coord.SessionIdentity{Namespace: "dev-agents", Name: "recipient", UID: "recipient-uid"}, Actor: coord.ClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env-agent", PodName: "sender", PodUID: "sender-pod-uid"}, Provenance: coord.ClaimProvenance{CatalogRevision: strings.Repeat("a", 64), RulesRevision: strings.Repeat("b", 64), GitHub: "thaynes43/dev-env", DefaultBranch: "main", InitialPinState: "Unknown"}, Repository: "repo", Provider: "claude", RequestedModel: "claude-opus-5-5", Text: "  exact UTF-8 text\n"}
}
func storeFixture(t *testing.T) KubeStore {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := coord.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		obj.SetUID(uuid.NewUUID())
		obj.SetCreationTimestamp(metav1.NewTime(time.Now()))
		return c.Create(ctx, obj, opts...)
	}}).Build()
	return KubeStore{Client: c, Live: c, Namespace: "private-coordination"}
}
func TestInboxIntentNamesBoundsAndOriginalAttribution(t *testing.T) {
	spec := validMessage()
	name := MessageName(spec.Sender, spec.Key)
	if !ValidMessage(spec) || !ValidMessageName(name) || len(validation.IsDNS1123Subdomain(name)) != 0 {
		t.Fatal("invalid full digest identity")
	}
	for _, text := range []string{strings.Repeat("a", MaxTextBytes), strings.Repeat("é", MaxTextBytes/2)} {
		if !ValidText(text) {
			t.Fatal("exact byte limit refused")
		}
	}
	for _, text := range []string{"", strings.Repeat("a", MaxTextBytes+1), strings.Repeat("é", MaxTextBytes/2+1), string([]byte{0xff})} {
		if ValidText(text) {
			t.Fatal("invalid byte bound accepted")
		}
	}
	changed := spec
	changed.Actor.PodUID = "resumed-pod-uid"
	changed.Provenance.ReportedHead = strings.Repeat("c", 40)
	changed.Provenance.ReportedAt = &metav1.Time{Time: time.Now()}
	if !SameIntent(spec, changed) {
		t.Fatal("resumed executor altered client intent")
	}
	changed = spec
	changed.Provenance.RulesRevision = strings.Repeat("c", 64)
	if SameIntent(spec, changed) || MessageName(changed.Sender, changed.Key) != name {
		t.Fatal("revision escaped immutable intent")
	}
	changed = spec
	changed.Text += "changed"
	if SameIntent(spec, changed) {
		t.Fatal("changed text confirmed")
	}
	local := spec
	local.Provider = "opencode"
	local.RequestedModel = "local/qwen:32b"
	if !ValidMessage(local) {
		t.Fatal("configured local model metadata requires no provider execution")
	}
}
func TestInboxStoreUncachedLostACKAndReconstruction(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	spec := validMessage()
	s.Client = interceptor.NewClient(s.Client.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if err := c.Create(ctx, obj, opts...); err != nil {
			return err
		}
		return errors.New("synthetic lost ACK")
	}, Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		t.Error("cached Get used")
		return errors.New("cached")
	}, List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
		t.Error("cached List used")
		return errors.New("cached")
	}})
	m, created, err := s.Create(ctx, spec)
	if err != nil || created || m.UID == "" {
		t.Fatal("lost ACK did not recover", err)
	}
	newStore := KubeStore{Client: s.Client, Live: s.Live, Namespace: s.Namespace}
	observed, err := newStore.Get(ctx, m.Name)
	if err != nil || observed.UID != m.UID {
		t.Fatal("reconstruction lost message", err)
	}
	items, _, err := newStore.List(ctx, spec.Project, &spec.Recipient, 10, "")
	if err != nil || len(items) != 1 || items[0].Spec.Text != spec.Text {
		t.Fatal("uncached list", err)
	}
	a, created, err := s.Ack(ctx, m, spec.Actor)
	if err != nil || created || a.Spec.Recipient != spec.Recipient {
		t.Fatal("ack lost ACK", err)
	}
	actor := spec.Actor
	actor.PodUID = "replacement-executor"
	retry, created, err := newStore.Ack(ctx, m, actor)
	if err != nil || created || retry.UID != a.UID || retry.Spec.Actor != a.Spec.Actor {
		t.Fatal("ack retry changed original attribution", err)
	}
	s.Live = interceptor.NewClient(s.Live.(client.WithWatch), interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		return errors.New("unavailable")
	}})
	if _, _, err := s.Create(ctx, spec); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unconfirmed result claimed success", err)
	}
}
func TestInboxRefusesForeignStorageMetadata(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	m, _, err := s.Create(ctx, validMessage())
	if err != nil {
		t.Fatal(err)
	}
	m.Annotations = map[string]string{"foreign": "metadata"}
	if err := s.Client.Update(ctx, m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, m.Name); !errors.Is(err, ErrUnavailable) {
		t.Fatal("foreign metadata accepted", err)
	}
}
