package controller

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

func TestOwnedRootRefusalDiagnosticCannotBecomeProofOrLeakOutput(t *testing.T) {
	b := taskbudget.Binding{TaskUID: "task", HostID: "host", PodUID: "pod", Epoch: 1, Deadline: time.Now().Add(time.Minute)}
	for _, tc := range []struct {
		name    string
		status  int
		failure error
		stage   string
	}{
		{"guard-liveness", hostexecutor.ProbeRefusedGuardLiveness, nil, "guard-liveness"},
		{"peer", hostexecutor.ProbeRefusedPeer, nil, "peer"},
		{"lost", 0, io.EOF, "unknown"},
		{"untrusted-error", 0, errors.New("NEVER_LOG_WORKER_BYTES"), "unknown"},
		{"generic-success", 0, nil, "unknown"},
		{"signal", 143, nil, "unknown"},
		{"unexited", 0, unexitedPhase{}, "unknown"},
		{"canceled", 0, context.Canceled, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := logr.FromSlogHandler(slog.NewTextHandler(&output, nil))
			ctx := log.IntoContext(context.Background(), logger)
			_, err := (OwnedRootProber{Exec: phaseExec{status: tc.status, failure: tc.failure, output: "NEVER_LOG_WORKER_BYTES"}}).Observe(ctx, client.ObjectKey{}, "owned-root", b, "home", strings.Repeat("a", 64))
			if !errors.Is(err, taskbudget.ErrUnavailable) {
				t.Fatal("refusal granted proof", err)
			}
			got := output.String()
			if strings.Contains(got, "NEVER_LOG_WORKER_BYTES") || !strings.Contains(got, "probeStage="+tc.stage) || !strings.Contains(got, "typedExit=") {
				t.Fatal("invalid diagnostic", got)
			}
		})
	}
}

// The refusal is always fail-closed, but diagnosis must distinguish the original
// caller abandoning admission from the independent immutable proof limit.
func TestOwnedRootRefusalIdentifiesContextOwner(t *testing.T) {
	b := taskbudget.Binding{TaskUID: "task", HostID: "host", PodUID: "pod", Epoch: 1, Deadline: time.Now().Add(time.Minute)}
	for _, owner := range []string{"caller-canceled", "caller-deadline", "probe-deadline"} {
		t.Run(owner, func(t *testing.T) {
			ctx := context.Background()
			var cancel context.CancelFunc
			switch owner {
			case "caller-canceled":
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "caller-deadline":
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			var output bytes.Buffer
			ctx = log.IntoContext(ctx, logr.FromSlogHandler(slog.NewTextHandler(&output, nil)))
			_, err := (OwnedRootProber{Exec: contextRefusalExec{}}).Observe(ctx, client.ObjectKey{}, "owned-root", b, "home", strings.Repeat("a", 64))
			if !errors.Is(err, taskbudget.ErrUnavailable) || !strings.Contains(output.String(), "contextOwner="+owner) || !strings.Contains(output.String(), "typedExit=false") {
				t.Fatalf("context refusal attribution lost: err=%v log=%s", err, output.String())
			}
		})
	}
}

type contextRefusalExec struct{}

func (contextRefusalExec) Run(ctx context.Context, _, _, _ string, _ []string, _ io.Reader, _, _ io.Writer) error {
	<-ctx.Done()
	return ctx.Err()
}
