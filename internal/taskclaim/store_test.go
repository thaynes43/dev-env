package taskclaim

import (
	"context"
	"errors"
	"slices"
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

// A same-work page with a continuation leaves claims unread. One of them could
// be the same work under another repository key, so the create is Unknown and
// nothing is stored (#223 review, finding 5).
func TestTaskClaimSameWorkLookupRefusesUnreadPage(t *testing.T) {
	store := storeFixture(t)
	backend := store.Live.(client.WithWatch)
	store.Live = interceptor.NewClient(backend, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if err := c.List(ctx, list, opts...); err != nil {
			return err
		}
		o := &client.ListOptions{}
		o.ApplyOptions(opts)
		if claims, ok := list.(*coord.TaskClaimList); ok && o.LabelSelector != nil && strings.Contains(o.LabelSelector.String(), WorkLabel+"=") {
			claims.Continue = "unread-page"
		}
		return nil
	}})
	if _, _, err := store.Create(context.Background(), validSpec()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("same-work lookup with an unread page passed", err)
	}
	var stored coord.TaskClaimList
	if err := backend.List(context.Background(), &stored); err != nil || len(stored.Items) != 0 {
		t.Fatal("unconfirmed same-work lookup stored a claim", err, len(stored.Items))
	}
}

// ListOwned must reject incomplete snapshots even when every returned record is
// a valid owned claim. Invalid padding would hide a missing size/Continue guard.
func TestListOwnedCompleteSnapshot(t *testing.T) {
	for _, mode := range []string{"complete", "truncated", "oversize", "unreadable", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			s := storeFixture(t)
			ctx := context.Background()
			first, _, err := s.Create(ctx, validSpec())
			if err != nil {
				t.Fatal(err)
			}
			legacy := first.DeepCopy()
			legacy.Labels = map[string]string{ProjectLabel: "sample"}
			if err := s.Client.Update(ctx, legacy); err != nil {
				t.Fatal(err)
			}
			spec := validSpec()
			spec.Identity.TaskKey = "issue:176"
			second, _, err := s.Create(ctx, spec)
			if err != nil {
				t.Fatal(err)
			}
			spec.Identity.TaskKey = "issue:177"
			spec.Owner.UID = "different-owner-uid"
			if _, _, err := s.Create(ctx, spec); err != nil {
				t.Fatal(err)
			}
			lists := 0
			s.Live = interceptor.NewClient(s.Client.(client.WithWatch), interceptor.Funcs{
				Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
					t.Error("ListOwned performed a Get")
					return errors.New("unexpected Get")
				},
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					lists++
					o := &client.ListOptions{}
					o.ApplyOptions(opts)
					if o.Limit != DefaultLimits.PerProject || o.Namespace != s.Namespace || o.Continue != "" || o.LabelSelector.String() != ProjectLabel+"=sample" {
						t.Fatal("wrong list bounds", o)
					}
					if err := c.List(ctx, list, opts...); err != nil {
						return err
					}
					claims := list.(*coord.TaskClaimList)
					slices.Reverse(claims.Items) // Backend order is not the output contract.
					switch mode {
					case "truncated":
						claims.Continue = "more"
					case "oversize":
						claims.Items = make([]coord.TaskClaim, int(DefaultLimits.PerProject)+1)
						for i := range claims.Items {
							claims.Items[i] = *first.DeepCopy()
						}
					case "unreadable":
						return errors.New("unavailable")
					case "invalid":
						claims.Items = append(claims.Items, coord.TaskClaim{})
					}
					return nil
				},
			})
			// The cached client must never be used by this read.
			s.Client = interceptor.NewClient(s.Client.(client.WithWatch), interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				t.Error("cached list")
				return errors.New("cached")
			}})
			got, err := s.ListOwned(ctx, "sample", first.Spec.Owner.UID)
			if lists != 1 {
				t.Fatal("list fan-out", lists)
			}
			if mode != "complete" {
				if !errors.Is(err, ErrUnavailable) || got != nil {
					t.Fatalf("partial snapshot accepted: count=%d err=%v", len(got), err)
				}
				return
			}
			if err != nil || len(got) != 2 {
				t.Fatal("missing valid/legacy owner", len(got), err)
			}
			want := []string{first.Name, second.Name}
			slices.Sort(want)
			if got[0].Name != want[0] || got[1].Name != want[1] {
				t.Fatal("unstable sort or foreign owner", got)
			}
		})
	}
}
