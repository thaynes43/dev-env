package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func wireOwnedBinding(b hostexecutor.Binding) apiv1.TaskBudgetBinding {
	return apiv1.TaskBudgetBinding{TaskUID: b.TaskUID, HostID: b.HostID, PodUID: b.PodUID, Epoch: b.Epoch, Deadline: b.Deadline}
}

// The ORIGINAL PID1 stays alive with Stopped evidence while awaiting an explicit
// owner continuation. No new Pod, enrollment, budget or bootstrap intent exists.
func runOwnedHostSequence(ctx context.Context, s agentd.Settings, c hostexecutor.Config, gate *ownedBudgetGate, getenv func(string) string) error {
	for {
		current := c.Binding
		var next *hostexecutor.Binding
		var nextBaseline string
		if c.Evidence != nil {
			c.Evidence.ReportStopped = func(stopCtx context.Context, _ hostexecutor.Receipt) error {
				return gate.remote.StopOwned(stopCtx, wireOwnedBinding(current))
			}
			c.Evidence.AfterStopped = func(r hostexecutor.Receipt) error {
				if getenv("AGENTD_OWNED_ALLOW_CONTINUATION") != "true" || current.Epoch != 1 || ctx.Err() != nil {
					return nil
				}
				resumed, ok, err := awaitOwnedContinuation(ctx, gate.remote.ReadOwnedStatus, wireOwnedBinding(current))
				if err != nil || !ok {
					return err
				}
				// Only the exact owned socket is removed, after independently proven
				// original Wait/ECHILD/helper Wait AND acknowledged server stop.
				st, err := os.Lstat(c.SocketPath)
				if err == nil {
					if st.Mode()&os.ModeSocket == 0 {
						return apiv1.ErrTaskBudgetGate
					}
					if err = os.Remove(c.SocketPath); err != nil {
						return err
					}
				} else if !os.IsNotExist(err) {
					return err
				}
				if digest := getenv("AGENTD_OWNED_HOME_INPUTS_SHA"); digest != "" {
					if err = checkOwnedHomeInputs(s, digest); err != nil {
						return err
					}
				}
				nextBaseline, err = hostexecutor.NativeHomeBaseline(s.Home)
				if err != nil {
					return err
				}
				next = &hostexecutor.Binding{TaskUID: resumed.TaskUID, HostID: resumed.HostID, PodUID: resumed.PodUID, Epoch: resumed.Epoch, Deadline: resumed.Deadline}
				return nil
			}
		}
		r, err := hostexecutor.Run(ctx, c, gate)
		if err != nil {
			return err
		}
		if next == nil {
			return nil
		}
		if r.State != "Stopped" || !r.RootWaitObserved || !r.TreeReaped || c.Evidence == nil {
			return apiv1.ErrTaskBudgetGate
		}
		c.Binding = *next
		// First receipt/intent remain byte-for-byte intact. The only supported
		// continued epoch has its own one-use file under the same private state.
		c.ReceiptPath = filepath.Join(s.StateDir, fmt.Sprintf("owned-host-epoch-%d.json", next.Epoch))
		c.Evidence = &hostexecutor.EvidenceConfig{HomeUID: c.Evidence.HomeUID, Baseline: c.Evidence.Baseline, ResumeBaseline: nextBaseline, CheckContinuation: c.Evidence.CheckContinuation, ReportRefusal: c.Evidence.ReportRefusal, RequirePID1: c.Evidence.RequirePID1}
	}
}

func awaitOwnedContinuation(ctx context.Context, read func(context.Context, string) (apiv1.TaskBudgetStatus, error), b apiv1.TaskBudgetBinding) (apiv1.TaskBudgetBinding, bool, error) {
	bounded, cancel := context.WithDeadline(ctx, b.Deadline)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if bounded.Err() != nil {
			return apiv1.TaskBudgetBinding{}, false, nil
		}
		callCtx, done := context.WithTimeout(bounded, time.Second)
		st, err := read(callCtx, b.TaskUID)
		done()
		if err != nil {
			// A failed GET grants no authority. Keep the proven-stopped original guard
			// available until its original deadline; never retry a mutation here.
			select {
			case <-bounded.Done():
				return apiv1.TaskBudgetBinding{}, false, nil
			case <-ticker.C:
			}
			continue
		}
		n := st.Binding
		if !st.Observed || n.TaskUID != b.TaskUID || n.HostID != b.HostID || n.PodUID != b.PodUID || !n.Deadline.Equal(b.Deadline) {
			return apiv1.TaskBudgetBinding{}, false, apiv1.ErrTaskBudgetGate
		}
		if n.Epoch == b.Epoch+1 && b.Epoch == 1 && !st.Latched && !st.Admitted && st.StopConfirmed {
			return n, true, nil
		}
		if n.Epoch == b.Epoch && st.Latched && st.Reason != "NativeRootStopped" {
			return apiv1.TaskBudgetBinding{}, false, nil
		}
		if n.Epoch != b.Epoch || !st.Latched || st.Reason != "NativeRootStopped" {
			return apiv1.TaskBudgetBinding{}, false, apiv1.ErrTaskBudgetGate
		}
		select {
		case <-bounded.Done():
			return apiv1.TaskBudgetBinding{}, false, nil
		case <-ticker.C:
		}
	}
}
