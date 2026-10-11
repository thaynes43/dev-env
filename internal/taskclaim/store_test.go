package taskclaim

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
)

func validSpec() coord.TaskClaimSpec {
	return coord.TaskClaimSpec{Identity: coord.TaskIdentity{Project: "sample", Repository: "repo", TaskKey: "issue:175"},
		Owner:      coord.SessionIdentity{Namespace: "dev-agents", Name: "owner", UID: "original-session-uid"},
		Actor:      coord.ClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env-agent", PodName: "owner", PodUID: "original-pod-uid"},
		Provenance: coord.ClaimProvenance{CatalogRevision: strings.Repeat("a", 64), RulesRevision: strings.Repeat("b", 64), GitHub: "thaynes43/dev-env", DefaultBranch: "main", InitialPinState: "Unknown"}}
}

func storeFixture(t *testing.T) KubeStore {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := coord.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		obj.SetUID(types.UID("persisted-claim-uid"))
		obj.SetCreationTimestamp(metav1.NewTime(time.Now()))
		return c.Create(ctx, obj, opts...)
	}}).Build()
	return KubeStore{Client: c, Live: c, Namespace: "private-coordination"}
}

func TestTaskClaimFullDigestIdentityAndBounds(t *testing.T) {
	s := validSpec()
	name := Name(s.Identity)
	if !ValidSpec(s) || !ValidName(name) || len(validation.IsDNS1123Subdomain(name)) != 0 || len(name) <= 63 {
		t.Fatal("full digest identity invalid", name)
	}
	for _, key := range []string{"issue:0", "issue:01", "issue:-1", "issue:12345678901234567890", "task:", "task:with space", "task:" + strings.Repeat("a", 129), "prose"} {
		bad := s
		bad.Identity.TaskKey = key
		if ValidSpec(bad) {
			t.Fatal("unbounded/noncanonical task accepted", key)
		}
	}
	for _, mutate := range []func(*coord.TaskClaimSpec){
		func(s *coord.TaskClaimSpec) { s.Owner.UID = "" }, func(s *coord.TaskClaimSpec) { s.Actor.Kind = "client" },
		func(s *coord.TaskClaimSpec) { s.Provenance.InitialPinState = "Verified" }, func(s *coord.TaskClaimSpec) { s.Provenance.RulesRevision = "rules text" },
		func(s *coord.TaskClaimSpec) { s.Provenance.ReportedHead = strings.Repeat("a", 40) }, func(s *coord.TaskClaimSpec) { s.Provenance.GitHub = "https://github.com/owner/repo" },
	} {
		bad := s
		mutate(&bad)
		if ValidSpec(bad) {
			t.Fatal("invalid attribution/provenance accepted", bad)
		}
	}
	changed := s
	changed.Provenance.CatalogRevision = strings.Repeat("c", 64)
	changed.Provenance.RulesRevision = strings.Repeat("d", 64)
	if Name(changed.Identity) != name {
		t.Fatal("revision changed logical identity")
	}
}

func TestTaskClaimUsesOnlyLiveReadsAndRefusesForeignObjects(t *testing.T) {
	store := storeFixture(t)
	store.Client = interceptor.NewClient(store.Client.(client.WithWatch), interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			t.Error("cached Get used")
			return errors.New("cached")
		},
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			t.Error("cached List used")
			return errors.New("cached")
		},
	})
	c, created, err := store.Create(context.Background(), validSpec())
	if err != nil || !created {
		t.Fatal("create", err)
	}
	if _, err := store.Get(context.Background(), c.Name); err != nil {
		t.Fatal(err)
	}
	if list, _, err := store.List(context.Background(), "sample", 100, ""); err != nil || len(list) != 1 {
		t.Fatal("list", err)
	}
	backend := store.Live.(client.Client)
	for _, mutate := range []func(*coord.TaskClaim){
		func(c *coord.TaskClaim) { c.Spec.Identity.TaskKey = "task:foreign" },
		func(c *coord.TaskClaim) {
			c.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "foreign", UID: "foreign"}}
		},
		func(c *coord.TaskClaim) { c.Annotations = map[string]string{"foreign": "private body"} },
	} {
		bad := c.DeepCopy()
		mutate(bad)
		if err := backend.Update(context.Background(), bad); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(context.Background(), c.Name); !errors.Is(err, ErrUnavailable) {
			t.Fatal("foreign object trusted", err)
		}
		if _, _, err := store.Create(context.Background(), validSpec()); !errors.Is(err, ErrUnavailable) {
			t.Fatal("foreign object adopted", err)
		}
		if err := backend.Get(context.Background(), client.ObjectKeyFromObject(c), bad); err != nil {
			t.Fatal(err)
		}
		restored := c.DeepCopy()
		restored.ResourceVersion = bad.ResourceVersion
		if err := backend.Update(context.Background(), restored); err != nil {
			t.Fatal(err)
		}
		c = restored
	}
}
