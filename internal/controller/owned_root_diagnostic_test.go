package controller

import (
	"bytes"
	"context"
	"errors"
	"github.com/go-logr/logr"
	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/taskbudget"
	"io"
	"log/slog"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"strings"
	"testing"
	"time"
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
