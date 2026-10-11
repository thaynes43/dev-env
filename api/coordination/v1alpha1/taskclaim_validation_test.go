package v1alpha1_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/taskclaim"
	"github.com/thaynes43/dev-env/internal/testenv"
)

var kube client.Client

func TestMain(m *testing.M) { os.Exit(run(m)) }
func run(m *testing.M) int {
	env, err := testenv.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() {
		if err := env.Stop(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()
	scheme := runtime.NewScheme()
	if err := coord.AddToScheme(scheme); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	kube, err = client.New(env.Config, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}

func schemaClaim(key string) *coord.TaskClaim {
	i := coord.TaskIdentity{Project: "sample", Repository: "repo", TaskKey: key}
	return &coord.TaskClaim{ObjectMeta: metav1.ObjectMeta{Name: taskclaim.Name(i), Namespace: "default"}, Spec: coord.TaskClaimSpec{
		Identity: i, Owner: coord.SessionIdentity{Namespace: "dev-agents", Name: "owner", UID: "original-session-uid"},
		Actor:      coord.ClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env-agent", PodName: "owner", PodUID: "original-pod-uid"},
		Provenance: coord.ClaimProvenance{CatalogRevision: strings.Repeat("a", 64), RulesRevision: strings.Repeat("b", 64), GitHub: "thaynes43/dev-env", DefaultBranch: "main", InitialPinState: "Unknown"}}}
}

func TestTaskClaimRealSchemaFullNameAtomicAndImmutable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := schemaClaim("issue:175")
	if err := kube.Create(ctx, c); err != nil {
		t.Fatal("full digest name rejected", err)
	}
	duplicate := schemaClaim("issue:175")
	duplicate.Spec.Owner.UID = "different-owner"
	if err := kube.Create(ctx, duplicate); !apierrors.IsAlreadyExists(err) {
		t.Fatal("duplicate identity not atomic", err)
	}
	for _, mutate := range []func(*coord.TaskClaim){
		func(c *coord.TaskClaim) { c.Spec.Owner.UID = "different-owner" }, func(c *coord.TaskClaim) { c.Spec.Identity.TaskKey = "issue:176" },
		func(c *coord.TaskClaim) { c.Spec.Actor.PodUID = "different-pod" }, func(c *coord.TaskClaim) { c.Spec.Provenance.CatalogRevision = strings.Repeat("c", 64) },
		func(c *coord.TaskClaim) { c.Spec.Provenance.RulesRevision = strings.Repeat("d", 64) }, func(c *coord.TaskClaim) {
			c.Spec.Provenance.ReportedHead = strings.Repeat("e", 40)
			c.Spec.Provenance.ReportedAt = &metav1.Time{Time: time.Now()}
		},
	} {
		bad := c.DeepCopy()
		mutate(bad)
		if err := kube.Update(ctx, bad); !apierrors.IsInvalid(err) {
			t.Fatal("immutable provenance/ownership changed", err)
		}
	}
	for n, mutate := range []func(*coord.TaskClaim){
		func(c *coord.TaskClaim) { c.Spec.Owner.UID = "" }, func(c *coord.TaskClaim) { c.Spec.Actor.Kind = "human" },
		func(c *coord.TaskClaim) { c.Spec.Identity.TaskKey = "issue:01" }, func(c *coord.TaskClaim) { c.Spec.Provenance.InitialPinState = "Verified" },
		func(c *coord.TaskClaim) { c.Spec.Provenance.ReportedHead = strings.Repeat("a", 40) }, func(c *coord.TaskClaim) { c.Spec.Provenance.DefaultBranch = strings.Repeat("a", 256) },
	} {
		bad := schemaClaim(fmt.Sprintf("task:invalid-%d", n))
		mutate(bad)
		if err := kube.Create(ctx, bad); !apierrors.IsInvalid(err) {
			t.Fatal("unbounded/invalid claim accepted", n, err)
		}
	}
}
