package v1alpha1_test

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/contextpublication"
)

func schemaContext(key string) *coord.PublishedContext {
	c := schemaClaim("task:context")
	s := coord.PublishedContextSpec{Identity: coord.ContextIdentity{SessionUID: c.Spec.Owner.UID, Kind: "memory", Key: key}, Source: c.Spec.Owner, SourceGeneration: 1, SourceParent: "dev-env-system/human", Project: "sample", Repository: "repo", Actor: c.Spec.Actor, Provenance: c.Spec.Provenance, Attribution: coord.ContextAttribution{Provider: "opencode", RequestedModel: "local/qwen-candidate", ObservedModelState: "Unknown", UsageState: "Unknown", CostState: "Unknown"}, Origin: "ExplicitPublished", Completeness: "ExplicitExcerpt", StopState: "Unknown", Text: "synthetic", ContentSHA256: apiv1.ContextHash("synthetic"), ByteCount: 9}
	return &coord.PublishedContext{ObjectMeta: metav1.ObjectMeta{Name: contextpublication.Name(s.Identity), Namespace: "default"}, Spec: s}
}
func TestPublishedContextRealSchemaImmutableAndBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := schemaContext("immutable")
	c.Spec.Text = strings.Repeat("é", apiv1.MaxContextBytes/2)
	c.Spec.ContentSHA256 = apiv1.ContextHash(c.Spec.Text)
	c.Spec.ByteCount = int32(len(c.Spec.Text))
	if err := kube.Create(ctx, c); err != nil {
		t.Fatal("valid private schema rejected", err)
	}
	for _, mutate := range []func(*coord.PublishedContext){func(c *coord.PublishedContext) { c.Spec.Text = "changed" }, func(c *coord.PublishedContext) { c.Spec.Source.UID = "replacement" }, func(c *coord.PublishedContext) { c.Spec.Actor.PodUID = "replacement" }, func(c *coord.PublishedContext) { c.Spec.Provenance.RulesRevision = strings.Repeat("c", 64) }, func(c *coord.PublishedContext) { c.Spec.Attribution.RequestedModel = "changed" }} {
		bad := c.DeepCopy()
		mutate(bad)
		if err := kube.Update(ctx, bad); !apierrors.IsInvalid(err) {
			t.Fatal("immutable publication changed", err)
		}
	}
	bad := schemaContext("oversized")
	bad.Spec.Text = strings.Repeat("x", apiv1.MaxContextBytes+1)
	if err := kube.Create(ctx, bad); !apierrors.IsInvalid(err) {
		t.Fatal("unbounded schema body accepted", err)
	}
	bad = schemaContext("false-proof")
	bad.Spec.Attribution.ObservedModelState = "Verified"
	if err := kube.Create(ctx, bad); !apierrors.IsInvalid(err) {
		t.Fatal("invented observed model accepted", err)
	}
	bad = schemaContext("source-uid")
	bad.Spec.Source.UID = "replacement"
	if err := kube.Create(ctx, bad); !apierrors.IsInvalid(err) {
		t.Fatal("source identity mismatch accepted", err)
	}
	// Character MaxLength alone cannot prove UTF-8 byte bounds: the store
	// independently refuses such an object even when its character count fits.
	bad = schemaContext("multibyte-oversized")
	bad.Spec.Text = strings.Repeat("é", apiv1.MaxContextBytes/2+1)
	bad.Spec.ContentSHA256 = apiv1.ContextHash(bad.Spec.Text)
	bad.Spec.ByteCount = int32(len(bad.Spec.Text))
	if contextpublication.ValidSpec(bad.Spec) {
		t.Fatal("store accepted oversized multibyte")
	}
}
