package v1alpha1_test

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/contextpublication"
)

func TestManagedOutputRealSchemaTruthfulBoundsAndImmutability(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := schemaContext("capture")
	inv := apiv1.ManagedInvocation(c.Spec.Source.UID, c.Spec.Actor.PodUID)
	c.Spec.Identity.Kind, c.Spec.Identity.Key = "transcript-excerpt", apiv1.ManagedOutputKey(inv, 0)
	c.Spec.Origin, c.Spec.Completeness = "ManagedOutputAssertion", "PartialManagedOutput"
	c.Spec.Attribution.Provider = "claude"
	c.Spec.ManagedOutput = &coord.ManagedOutputAssertion{Invocation: inv, ConversationID: "11111111-1111-4111-8111-111111111111"}
	c.Name = contextpublication.Name(c.Spec.Identity)
	if !contextpublication.ValidSpec(c.Spec) {
		t.Fatal("valid managed store assertion refused")
	}
	if err := kube.Create(ctx, c); err != nil {
		t.Fatal("valid managed schema refused", err)
	}
	changed := c.DeepCopy()
	changed.Spec.ManagedOutput.ConversationID = "22222222-2222-4222-8222-222222222222"
	if err := kube.Update(ctx, changed); !apierrors.IsInvalid(err) {
		t.Fatal("immutable conversation changed", err)
	}
	for i, mutate := range []func(*coord.PublishedContext){func(c *coord.PublishedContext) { c.Spec.ManagedOutput.Ordinal = 4 }, func(c *coord.PublishedContext) { c.Spec.Completeness = "Complete" }, func(c *coord.PublishedContext) { c.Spec.ManagedOutput = nil }, func(c *coord.PublishedContext) { c.Spec.Identity.Key = "wrong-key" }, func(c *coord.PublishedContext) { c.Spec.Origin = "ExplicitPublished" }, func(c *coord.PublishedContext) { c.Spec.Attribution.Provider = "opencode" }} {
		bad := c.DeepCopy()
		bad.ResourceVersion = ""
		bad.UID = ""
		bad.Name = "managed-invalid-" + string(rune('a'+i))
		mutate(bad)
		if err := kube.Create(ctx, bad); !apierrors.IsInvalid(err) {
			t.Fatal("untruthful managed schema accepted", i, err)
		}
	}
}
