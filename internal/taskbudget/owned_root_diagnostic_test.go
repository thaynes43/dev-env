package taskbudget

import (
	"bytes"
	"context"
	"errors"
	"github.com/go-logr/logr"
	"log/slog"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"strings"
	"testing"
)

func TestOwnedRootDiagnosesConcurrentPodMetadataDriftWithoutWeakeningSnapshot(t *testing.T) {
	f := newRootPolicyFixture(t)
	var output bytes.Buffer
	ctx := log.IntoContext(context.Background(), logr.FromSlogHandler(slog.NewTextHandler(&output, nil)))
	f.probe.during = func() {
		f.pod.Annotations["fixture-metadata"] = "changed"
		if err := f.live.Update(ctx, f.pod); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.inspector.InspectNativeAdmission(ctx, f.ledger, f.ledger.Binding); !errors.Is(err, ErrUnavailable) {
		t.Fatal("concurrent mutation accepted", err)
	}
	if !strings.Contains(output.String(), "stage=pod-readback") || strings.Contains(output.String(), "fixture-metadata") {
		t.Fatal("wrong snapshot diagnostic", output.String())
	}
}
