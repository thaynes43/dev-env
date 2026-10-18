package controller

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

type OwnedRootExec interface {
	Run(context.Context, string, string, string, []string, io.Reader, io.Writer, io.Writer) error
}
type OwnedRootProber struct{ Exec OwnedRootExec }

func (p OwnedRootProber) Observe(ctx context.Context, key client.ObjectKey, container string, b taskbudget.Binding) (hostexecutor.Observation, error) {
	var result hostexecutor.Observation
	if p.Exec == nil {
		return result, taskbudget.ErrUnavailable
	}
	var challenge [32]byte
	if _, err := rand.Read(challenge[:]); err != nil {
		return result, err
	}
	nonce := hex.EncodeToString(challenge[:])
	raw, _ := json.Marshal(b)
	var out cappedBuffer
	out.max = 8192
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if p.Exec.Run(bounded, key.Namespace, key.Name, container, []string{"/usr/local/bin/agentd", "owned-codex-host-probe", nonce, string(raw)}, nil, &out, io.Discard) != nil || out.err != nil {
		return result, taskbudget.ErrUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(out.Bytes()))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || d.Decode(new(any)) != io.EOF || result.Version != 1 || result.Challenge != nonce {
		return hostexecutor.Observation{}, taskbudget.ErrUnavailable
	}
	return result, nil
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
