package v1alpha1_test

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
	"github.com/thaynes43/dev-env/internal/sessiondirectory"
)

func TestDirectoryRealSchemaOriginalBoundedImmutableAndWitness(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := testutil.New(t, "codex", "dev/client")
	fixture.Record.CaptureOutput = true
	source := fixture.Session
	source.Spec.ProjectCanary.CaptureOutput = true
	source.Spec.ProjectCanary.PolicyDigest = fixture.Record.Digest()
	source.CreationTimestamp = metav1.NewTime(time.Date(2026, 10, 11, 19, 0, 0, 0, time.UTC))
	source.Spec.SessionDirectory = &v1alpha1.SessionDirectoryEnrollment{Version: 1, Project: "sample"}
	spec, err := sessiondirectory.Spec(source)
	if err != nil {
		t.Fatal(err)
	}
	d := &coord.SessionDirectory{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: sessiondirectory.Name(spec.Source)}, Spec: spec}
	if err := kube.Create(ctx, d); err != nil {
		t.Fatal("valid protected directory rejected", err)
	}
	for _, mutate := range []func(*coord.SessionDirectory){func(d *coord.SessionDirectory) { d.Spec.Source.UID = "replacement" }, func(d *coord.SessionDirectory) { d.Spec.Request.Model = "changed" }, func(d *coord.SessionDirectory) {
		d.Spec.Revisions.Rules = coord.DirectoryDigest(strings.Repeat("c", 64))
	}} {
		bad := d.DeepCopy()
		mutate(bad)
		if err := kube.Update(ctx, bad); !apierrors.IsInvalid(err) {
			t.Fatal("immutable accepted input changed", err)
		}
	}
	bad := d.DeepCopy()
	bad.Name = "oversized-directory"
	bad.ResourceVersion = ""
	bad.UID = ""
	bad.Spec.Request.Model = coord.DirectoryText(strings.Repeat("x", 257))
	if err := kube.Create(ctx, bad); !apierrors.IsInvalid(err) {
		t.Fatal("unbounded requested model admitted", err)
	}
	bad = d.DeepCopy()
	bad.Name = "untyped-local"
	bad.ResourceVersion = ""
	bad.UID = ""
	bad.Spec.Request.Provider = "local"
	if err := kube.Create(ctx, bad); !apierrors.IsInvalid(err) {
		t.Fatal("local inferred without explicit type", err)
	}
	d.Status = coord.SessionDirectoryStatus{SourceState: "Present", ObservedAt: source.CreationTimestamp, StopState: "Unknown", ActualModelState: "Unknown", UsageState: "Unknown", CostState: "Unknown", LastWitness: &coord.DirectoryWitness{SourceGeneration: 1, SourceResourceVersion: "1", ObservedAt: source.CreationTimestamp, PodName: "original", PodUID: "pod-original", PodPhase: "Running"}}
	if err := kube.Status().Update(ctx, d); err != nil {
		t.Fatal("bounded witness rejected", err)
	}
	for _, mutate := range []func(*coord.SessionDirectory){func(d *coord.SessionDirectory) { d.Status.LastWitness = nil }, func(d *coord.SessionDirectory) { d.Status.LastWitness.PodUID = "replacement" }, func(d *coord.SessionDirectory) { d.Status.StopState = "StopConfirmed" }, func(d *coord.SessionDirectory) { d.Status.ActualModelState = "Verified" }} {
		bad := d.DeepCopy()
		mutate(bad)
		if err := kube.Status().Update(ctx, bad); !apierrors.IsInvalid(err) {
			t.Fatal("witness erased/replaced or certainty invented", err)
		}
	}
	if err := v1alpha1.AddToScheme(kube.Scheme()); err != nil {
		t.Fatal(err)
	}
	source.Namespace = "default"
	source.Name = "prospective-directory"
	source.UID = ""
	source.ResourceVersion = ""
	source.CreationTimestamp = metav1.Time{}
	if err := kube.Create(ctx, source); err != nil {
		t.Fatal("enrolled managed canary rejected", err)
	}
	removed := source.DeepCopy()
	removed.Spec.SessionDirectory = nil
	if err := kube.Update(ctx, removed); !apierrors.IsInvalid(err) {
		t.Fatal("enrollment erased", err)
	}
	source.Status.SessionDirectory = &v1alpha1.SessionDirectoryReceipt{State: "Preparing", Name: "session-directory." + strings.Repeat("a", 64), SpecDigest: strings.Repeat("b", 64), UID: ""}
	if err := kube.Status().Update(ctx, source); err != nil {
		t.Fatal("pre-create causal receipt refused", err)
	}
	removed = source.DeepCopy()
	removed.Status.SessionDirectory = nil
	if err := kube.Status().Update(ctx, removed); !apierrors.IsInvalid(err) {
		t.Fatal("receipt erased", err)
	}
	removed = source.DeepCopy()
	removed.Status = v1alpha1.AgentSessionStatus{}
	if err := kube.Status().Update(ctx, removed); !apierrors.IsInvalid(err) {
		t.Fatal("receipt erased with status", err)
	}
}
