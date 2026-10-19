package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	kexec "k8s.io/client-go/util/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

type OwnedNativeControl struct {
	Root *taskbudget.OwnedRootInspector
	Exec OwnedRootExec
}

type nativeOutput struct {
	bytes.Buffer
	err error
}

func (b *nativeOutput) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if len(p) > hostexecutor.NativeControlLimit-b.Len() {
		b.err = errors.New("native response exceeds bound")
		return 0, b.err
	}
	return b.Buffer.Write(p)
}

func (b *nativeOutput) response(r apiv1.NativeControlRequest) (apiv1.NativeControlResponse, error) {
	if b.err != nil {
		return apiv1.NativeControlResponse{}, taskbudget.ErrUnavailable
	}
	return apiv1.DecodeNativeControlResponse(b.Bytes(), r)
}

func (c OwnedNativeControl) Control(ctx context.Context, l *taskbudget.Ledger, r apiv1.NativeControlRequest) (apiv1.NativeControlResponse, error) {
	if c.Root == nil || c.Exec == nil {
		return apiv1.NativeControlResponse{}, taskbudget.ErrUnavailable
	}
	p, err := c.Root.ControlTarget(ctx, l)
	if err != nil {
		return apiv1.NativeControlResponse{}, err
	}
	deadline := time.Now().Add(apiv1.NativeControlTransportTimeout)
	if r.Binding.Deadline.Before(deadline) {
		deadline = r.Binding.Deadline
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	body, _ := json.Marshal(r)
	out := &nativeOutput{}
	// One immutable command, prompt on stdin, no retries. These content bytes
	// are NEVER authority for readiness/stop, budget admission or continuation.
	started := time.Now()
	err = c.Exec.Run(bounded, p.Namespace, p.Name, taskbudget.OwnedRootContainer, []string{"/usr/local/bin/agentd", "owned-native-control", apiv1.NativeControlDigest(r)}, bytes.NewReader(body), out, io.Discard)
	if err != nil || bounded.Err() != nil || out.err != nil {
		logNativeControlRefusal(ctx, bounded, r.Operation, "exec", err, started)
		return apiv1.NativeControlResponse{}, taskbudget.ErrUnavailable
	}
	response, err := out.response(r)
	if err != nil {
		logNativeControlRefusal(ctx, bounded, r.Operation, "response-envelope", nil, started)
	}
	return response, err
}

// Only fixed categories, operation enum, process status and context/timing are
// logged. Native output, stderr and remote error strings remain untrusted.
func logNativeControlRefusal(ctx, bounded context.Context, operation, category string, err error, started time.Time) {
	var status kexec.ExitError
	typed := errors.As(err, &status)
	exited, code := false, -1
	if typed {
		exited = status.Exited()
	}
	if exited {
		code = status.ExitStatus()
	}
	log.FromContext(ctx).WithName("native-owner").Info("native owner request unconfirmed", "operation", operation, "category", category, "elapsedMs", time.Since(started).Milliseconds(), "typedExit", typed, "exited", exited, "exitCode", code, "nativeStage", hostexecutor.NativeControlRefusalStage(code), "callerCanceled", errors.Is(ctx.Err(), context.Canceled), "callerDeadline", errors.Is(ctx.Err(), context.DeadlineExceeded), "execCanceled", errors.Is(bounded.Err(), context.Canceled), "execDeadline", errors.Is(bounded.Err(), context.DeadlineExceeded))
}
