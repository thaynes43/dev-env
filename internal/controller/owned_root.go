package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	kexec "k8s.io/client-go/util/exec"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

type OwnedRootExec interface {
	Run(context.Context, string, string, string, []string, io.Reader, io.Writer, io.Writer) error
}
type OwnedRootProber struct{ Exec OwnedRootExec }

// Observe trusts only the CRI process-exit status, never worker-accessible
// stdout/stderr. The immutable probe verifies the complete expected predicate.
func (p OwnedRootProber) Observe(ctx context.Context, key client.ObjectKey, container string, b taskbudget.Binding, homeUID, baseline string) (int, error) {
	if p.Exec == nil {
		return 0, taskbudget.ErrUnavailable
	}
	var challenge [32]byte
	if _, err := rand.Read(challenge[:]); err != nil {
		return 0, err
	}
	raw, _ := json.Marshal(b)
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err := p.Exec.Run(bounded, key.Namespace, key.Name, container, []string{"/usr/local/bin/agentd", "owned-codex-host-probe", hex.EncodeToString(challenge[:]), string(raw), homeUID, baseline}, nil, io.Discard, io.Discard)
	var status kexec.ExitError
	typed := errors.As(err, &status)
	if bounded.Err() != nil || !typed || !status.Exited() || !hostexecutor.ValidEvidenceExit(status.ExitStatus()) {
		exited, code := false, -1
		if typed {
			exited, code = status.Exited(), status.ExitStatus()
		}
		// Never log remote error text or output: both may contain worker bytes.
		log.FromContext(ctx).WithName("owned-root").Info("owned root process proof refused", "errorType", fmt.Sprintf("%T", err), "contextDone", bounded.Err() != nil, "typedExit", typed, "exited", exited, "exitCode", code, "probeStage", hostexecutor.ProbeRefusalStage(code))
		return 0, taskbudget.ErrUnavailable
	}
	return status.ExitStatus(), nil
}

type OwnedRootBudgetValidator struct {
	Root    *taskbudget.OwnedRootInspector
	Managed taskbudget.Validator
}

func (v OwnedRootBudgetValidator) Validate(ctx context.Context, l *taskbudget.Ledger, e apiv1.TaskBudgetEvent) error {
	if e.Evidence.Kind == "owned-root-stop" {
		if v.Root == nil {
			return taskbudget.ErrUnavailable
		}
		return v.Root.ValidateStop(ctx, l, e)
	}
	if v.Managed == nil {
		return taskbudget.ErrUnavailable
	}
	return v.Managed.Validate(ctx, l, e)
}
func (v OwnedRootBudgetValidator) ValidateOwnerDecision(ctx context.Context, l *taskbudget.Ledger, e apiv1.TaskBudgetExtension, a string) error {
	if v.Managed == nil {
		return taskbudget.ErrUnavailable
	}
	return v.Managed.ValidateOwnerDecision(ctx, l, e, a)
}
