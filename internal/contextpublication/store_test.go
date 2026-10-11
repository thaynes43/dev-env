package contextpublication

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestPublicationTwoReplicasOneAtomicChunk(t *testing.T) {
	store := fixtureStore(t)
	second := KubeStore{Client: store.Client, Live: store.Live, Namespace: store.Namespace}
	stores := []KubeStore{store, second}
	created := make([]bool, 2)
	errorsSeen := make([]error, 2)
	uids := make([]types.UID, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range stores {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			p, won, err := stores[i].Create(context.Background(), sampleSpec())
			created[i], errorsSeen[i] = won, err
			if p != nil {
				uids[i] = p.UID
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if errorsSeen[0] != nil || errorsSeen[1] != nil || created[0] == created[1] || uids[0] == "" || uids[0] != uids[1] {
		t.Fatal("atomic two-replica result", created, errorsSeen)
	}
}

func sampleSpec() coord.PublishedContextSpec {
	return coord.PublishedContextSpec{Identity: coord.ContextIdentity{SessionUID: "source-uid", Kind: "handoff", Key: "task-174"}, Source: coord.SessionIdentity{Namespace: "dev-agents", Name: "source", UID: "source-uid"}, SourceGeneration: 1, SourceParent: "dev-env-system/human", Project: "sample", Repository: "repo", Actor: coord.ClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env-agent", PodName: "source", PodUID: "pod-uid"}, Provenance: coord.ClaimProvenance{CatalogRevision: strings.Repeat("a", 64), RulesRevision: strings.Repeat("b", 64), GitHub: "thaynes43/dev-env", DefaultBranch: "main", InitialPinState: "Unknown"}, Attribution: coord.ContextAttribution{Provider: "opencode", RequestedModel: "local/qwen-candidate", ObservedModelState: "Unknown", UsageState: "Unknown", CostState: "Unknown"}, Origin: "ExplicitPublished", Completeness: "ExplicitExcerpt", StopState: "Unknown", Text: "explicit synthetic excerpt", ContentSHA256: apiv1.ContextHash("explicit synthetic excerpt"), ByteCount: int32(len("explicit synthetic excerpt"))}
}
func fixtureStore(t *testing.T) KubeStore {
	t.Helper()
	scheme := runtime.NewScheme()
	if coord.AddToScheme(scheme) != nil {
		t.Fatal("scheme")
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		obj.SetUID(types.UID("publication-uid"))
		obj.SetCreationTimestamp(metav1.NewTime(time.Now()))
		return c.Create(ctx, obj, opts...)
	}}).Build()
	return KubeStore{Client: c, Live: c, Namespace: "private-coordination"}
}
func TestPublicationExactAtomicRetryAndLostAcknowledgement(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	spec := sampleSpec()
	store.Client = interceptor.NewClient(store.Client.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if err := c.Create(ctx, obj, opts...); err != nil {
			return err
		}
		return errors.New("lost acknowledgement")
	}, Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		t.Fatal("cached get")
		return nil
	}})
	p, created, err := store.Create(ctx, spec)
	if err != nil || created || p == nil {
		t.Fatal("uncertain ACK not confirmed", err)
	}
	second := KubeStore{Client: store.Live.(client.Client), Live: store.Live, Namespace: store.Namespace}
	retry, created, err := second.Create(ctx, spec)
	if err != nil || created || retry.UID != p.UID || !retry.CreationTimestamp.Equal(&p.CreationTimestamp) {
		t.Fatal("retry replaced original", err)
	}
	changed := spec
	changed.Text = "changed"
	changed.ContentSHA256 = apiv1.ContextHash(changed.Text)
	changed.ByteCount = int32(len(changed.Text))
	if Name(changed.Identity) != p.Name {
		t.Fatal("body changed identity")
	}
	if _, _, err = second.Create(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("body changed under same key", err)
	}
	changed = spec
	changed.Provenance.RulesRevision = strings.Repeat("c", 64)
	if _, _, err = second.Create(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("provenance changed", err)
	}
	if list, _, err := second.List(ctx, "sample", 20, ""); err != nil || len(list) != 1 {
		t.Fatal("immutable durable list", err)
	}
	second.Live = interceptor.NewClient(store.Live.(client.WithWatch), interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		return errors.New("uncached confirmation unavailable")
	}})
	if _, _, err = second.Create(ctx, spec); !errors.Is(err, ErrUnavailable) {
		t.Fatal("uncertain confirmation invented success", err)
	}
}
func TestPublicationUTF8BytesHashAndForeignResources(t *testing.T) {
	spec := sampleSpec()
	spec.Text = strings.Repeat("é", apiv1.MaxContextBytes/2)
	spec.ContentSHA256 = apiv1.ContextHash(spec.Text)
	spec.ByteCount = int32(len(spec.Text))
	if !ValidSpec(spec) {
		t.Fatal("64KiB multibyte rejected")
	}
	for _, mutate := range []func(*coord.PublishedContextSpec){func(s *coord.PublishedContextSpec) {
		s.Text += "é"
		s.ContentSHA256 = apiv1.ContextHash(s.Text)
		s.ByteCount = int32(len(s.Text))
	}, func(s *coord.PublishedContextSpec) {
		s.Text = string([]byte{255})
		s.ByteCount = 1
		s.ContentSHA256 = apiv1.ContextHash(s.Text)
	}, func(s *coord.PublishedContextSpec) { s.ContentSHA256 = strings.Repeat("d", 64) }, func(s *coord.PublishedContextSpec) { s.Source.UID = "replacement" }, func(s *coord.PublishedContextSpec) { s.StopState = "Stopped" }, func(s *coord.PublishedContextSpec) { s.Attribution.ObservedModelState = "Verified" }, func(s *coord.PublishedContextSpec) { s.Actor.Kind = "coordinator" }} {
		bad := spec
		mutate(&bad)
		if ValidSpec(bad) {
			t.Fatal("invalid content or proof accepted")
		}
	}
	store := fixtureStore(t)
	p, _, err := store.Create(context.Background(), sampleSpec())
	if err != nil {
		t.Fatal(err)
	}
	bad := p.DeepCopy()
	bad.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "source", UID: "source"}}
	if store.Client.Update(context.Background(), bad) != nil {
		t.Fatal("fixture")
	}
	if _, err := store.Get(context.Background(), p.Name); !errors.Is(err, ErrUnavailable) {
		t.Fatal("foreign owner reference adopted", err)
	}
	store = fixtureStore(t)
	store.Client = interceptor.NewClient(store.Client.(client.WithWatch), interceptor.Funcs{Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
		return apierrors.NewForbidden(schema.GroupResource{Group: coord.GroupVersion.Group, Resource: "publishedcontexts"}, "p", errors.New("denied"))
	}})
	if _, _, err := store.Create(context.Background(), sampleSpec()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("denial recovered", err)
	}
}
