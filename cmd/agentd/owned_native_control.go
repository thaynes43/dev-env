package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"path/filepath"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Only the fixed operator exec adapter dispatches this supported owner route.
// Direct Pod exec is already a trusted operator capability, not model authority.
func ownedNativeControl(ctx context.Context, args []string, in io.Reader, out io.Writer, getenv func(string) string) int {
	if len(args) != 1 || getenv("AGENTD_OWNED_ROOT_BOOTSTRAP") != "true" || hostexecutor.RequireProbeIsolation() != nil || hostexecutor.ProtectEvidenceProcess() != nil {
		return exitFailure
	}
	var req apiv1.NativeControlRequest
	dec := json.NewDecoder(io.LimitReader(in, 16<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF || !req.Valid() || req.Operation == "host/stop" || args[0] != apiv1.NativeControlDigest(req) {
		return exitFailure
	}
	if req.Binding.TaskUID != getenv("AGENTD_OWNED_TASK_UID") || req.Binding.HostID != getenv("AGENTD_OWNED_HOST_ID") || req.Binding.PodUID != getenv("DEV_ENV_POD_UID") {
		return exitFailure
	}
	s, err := agentd.LoadSettings(getenv)
	if err != nil || s.Home != hostexecutor.OwnedRootHome {
		return exitFailure
	}
	deadline := time.Now().Add(apiv1.NativeControlExecutionTimeout)
	if req.Binding.Deadline.Before(deadline) {
		deadline = req.Binding.Deadline
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	gate, err := newOwnedBudgetGate(ownedOperatorURL, ownedTokenFile, ownedCAFile, s)
	if err != nil {
		return exitFailure
	}
	status, err := gate.remote.ReadOwnedStatus(bounded, req.Binding.TaskUID)
	if err != nil || !status.Observed || status.Latched || status.NativeStopRequested || !sameOwnedBinding(status.Binding, req.Binding) {
		return exitFailure
	}
	var challenge [32]byte
	if _, err = rand.Read(challenge[:]); err != nil {
		return exitFailure
	}
	b := hostexecutor.Binding{TaskUID: req.Binding.TaskUID, HostID: req.Binding.HostID, PodUID: req.Binding.PodUID, Epoch: req.Binding.Epoch, Deadline: req.Binding.Deadline}
	proof, err := hostexecutor.ProbeEvidence(bounded, filepath.Join(s.StateDir, hostexecutor.EvidenceSocketName), 1, b, hex.EncodeToString(challenge[:]))
	if err != nil || hostexecutor.EvidenceExit(proof, b, getenv("AGENTD_OWNED_HOME_UID"), getenv("AGENTD_OWNED_HOME_BASELINE")) != hostexecutor.EvidenceReady {
		return exitFailure
	}
	result, err := hostexecutor.ControlNative(bounded, filepath.Join(s.CodexHome, "owned-control.sock"), proof, req, func(ctx context.Context) error {
		return authorizeOwnedNativeControl(ctx, req, gate.remote.ConsumeNativeControl)
	})
	if err != nil {
		return hostexecutor.NativeControlRefusalExit(err)
	}
	if json.NewEncoder(out).Encode(apiv1.NativeControlResponse{Binding: req.Binding, ID: req.ID, Operation: req.Operation, Result: result}) != nil {
		return exitFailure
	}
	return exitOK
}

func authorizeOwnedNativeControl(ctx context.Context, r apiv1.NativeControlRequest, consume func(context.Context, apiv1.NativeControlRequest) error) error {
	if !r.Valid() || r.Operation == "host/stop" {
		return apiv1.ErrTaskBudgetGate
	}
	if r.Operation != "thread/read" {
		if err := consume(ctx, r); err != nil {
			return err
		}
	}
	return nil
}
