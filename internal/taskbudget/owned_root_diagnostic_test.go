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

func TestOwnedRootStartupLogsOneTerminalRefusal(t *testing.T) {
	for _, mode := range []string{"cap", "context"} {
		t.Run(mode, func(t *testing.T) {
			f := newRootPolicyFixture(t)
			f.inspector.Reader = &startingRootReader{Reader: f.live, always: true}
			var output bytes.Buffer
			ctx, cancel := context.WithCancel(log.IntoContext(context.Background(), logr.FromSlogHandler(slog.NewTextHandler(&output, nil))))
			defer cancel()
			if mode == "context" {
				cancel()
			}
			if err := f.inspector.InspectNativeAdmission(ctx, f.ledger, f.ledger.Binding); !errors.Is(err, ErrUnavailable) {
				t.Fatal("startup granted authority", err)
			}
			if strings.Count(output.String(), "owned root observation refused") != 1 || !strings.Contains(output.String(), "stage=pod-startup-"+mode) {
				t.Fatal("terminal refusal missing or polling spam", output.String())
			}
		})
	}
}
