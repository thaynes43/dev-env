package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func ownedRootBootstrap(ctx context.Context, s agentd.Settings, g *ownedBudgetGate, getenv func(string) string) (hostexecutor.Binding, *hostexecutor.EvidenceConfig, error) {
	if os.Getpid() != 1 || hostexecutor.RequireProbeIsolation() != nil || hostexecutor.ProtectEvidenceProcess() != nil {
		return hostexecutor.Binding{}, nil, apiv1.ErrTaskBudgetGate
	}
	var spec apiv1.TaskBudgetSpec
	raw := getenv("AGENTD_OWNED_BUDGET_SPEC")
	if len(raw) > 8192 || json.Unmarshal([]byte(raw), &spec) != nil || spec.OverallSeconds < 1 || spec.OverallSeconds > 2700 || spec.EffortSeconds < 1 || spec.EffortSeconds > 3600 || spec.CheckpointSeconds < 1 || spec.CheckpointSeconds > 600 || spec.SuccessCondition == "" {
		return hostexecutor.Binding{}, nil, apiv1.ErrTaskBudgetGate
	}
	if validateOwnedRootHome(s.Home, getenv("AGENTD_OWNED_HOME_UID"), getenv("AGENTD_OWNED_HOME_BASELINE")) != nil {
		return hostexecutor.Binding{}, nil, apiv1.ErrTaskBudgetGate
	}
	// Separate irreversible bootstrap intent is written before the one API create.
	// It is not an authority receipt and is never removed/rearmed automatically.
	if err := writeOwnedBootstrapIntent(s.StateDir, getenv("AGENTD_OWNED_TASK_UID")); err != nil {
		return hostexecutor.Binding{}, nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	b, err := g.remote.BootstrapOwned(bounded, apiv1.CreateTaskBudgetRequest{TaskUID: getenv("AGENTD_OWNED_TASK_UID"), Spec: spec}, getenv("AGENTD_OWNED_HOST_ID"), getenv("DEV_ENV_POD_UID"))
	if err != nil {
		return hostexecutor.Binding{}, nil, err
	}
	binding := hostexecutor.Binding{TaskUID: b.TaskUID, Epoch: b.Epoch, Deadline: b.Deadline, HostID: b.HostID, PodUID: b.PodUID}
	evidence := &hostexecutor.EvidenceConfig{HomeUID: getenv("AGENTD_OWNED_HOME_UID"), Baseline: getenv("AGENTD_OWNED_HOME_BASELINE"), RequirePID1: true, ReportStopped: func(ctx context.Context, _ hostexecutor.Receipt) error { return g.remote.StopOwned(ctx, b) }}
	return binding, evidence, nil
}

// The protected, pinned command is the only producer consumed by operator exec.
// Neither a socket path nor a supervisor PID can be selected by its caller.
func ownedRootProbe(ctx context.Context, args []string, stdout io.Writer) int {
	if len(args) != 2 || !protectedProbeOutput() || hostexecutor.RequireProbeIsolation() != nil || hostexecutor.ProtectEvidenceProcess() != nil {
		return exitFailure
	}
	var binding hostexecutor.Binding
	if len(args[1]) > 4096 || json.Unmarshal([]byte(args[1]), &binding) != nil {
		return exitFailure
	}
	bounded, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	proof, err := hostexecutor.ProbeEvidence(bounded, "/home/dev/.agentd/"+hostexecutor.EvidenceSocketName, 1, binding, args[0])
	if err != nil {
		return exitFailure
	}
	if json.NewEncoder(stdout).Encode(proof) != nil {
		return exitFailure
	}
	return exitOK
}

// CRI exec must supply an inaccessible pipe from process creation onward.
// Hardening in main alone cannot revoke an FD raced open during exec startup.
func protectedProbeOutput() bool {
	var st unix.Stat_t
	return unix.Fstat(1, &st) == nil && safeProbeOutput(st)
}
func safeProbeOutput(st unix.Stat_t) bool {
	return st.Mode&unix.S_IFMT == unix.S_IFIFO && st.Uid == 0 && st.Mode&0777 == 0600
}

func writeOwnedBootstrapIntent(stateDir, task string) error {
	fd, err := unix.Openat2(unix.AT_FDCWD, stateDir, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&077 != 0 {
		return apiv1.ErrTaskBudgetGate
	}
	markerFD, err := unix.Openat(fd, "owned-budget-attempt", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	marker := os.NewFile(uintptr(markerFD), "owned-budget-attempt")
	_, err = marker.Write([]byte(task))
	if err == nil {
		err = marker.Sync()
	}
	_ = marker.Close()
	if err != nil {
		return err
	}
	return unix.Fsync(fd)
}

// Preflight consumes no intent or ledger. Evidence.start measures again after
// bootstrap so changes during the API request cannot inherit the earlier proof.
func validateOwnedRootHome(home, uid, expected string) error {
	if len(uid) < 1 || len(uid) > 128 || len(expected) != 64 {
		return apiv1.ErrTaskBudgetGate
	}
	actual, err := hostexecutor.NativeHomeBaseline(home)
	if err != nil || actual != expected {
		return apiv1.ErrTaskBudgetGate
	}
	return nil
}
