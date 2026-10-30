package agentd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// Fixed GitOps qualification path. Neither stdin nor the API selects this file.
const CodexRCTargetFile = "codex/rc-target.json"
const codexRCOnceFile = "codex-rc-once.json"

var errCodexRCRefused = errors.New("codex RC target qualification refused")

type codexRCDeps struct {
	process func(Settings, codexHostFS) (codexHostProcess, error)
	start   func(context.Context, Settings, int, codexRCThreadSelection, func(context.Context) error) (codexRCThreadReceipt, error)
	consume func(string, []byte, os.FileMode) error
	now     func() time.Time
}

func realCodexRCDeps() codexRCDeps {
	return codexRCDeps{inspectCodexHostProcess, startCodexRCThread, writeFileOnce, time.Now}
}

// CodexRCControl runs exactly one of two fixed operations. Preflight is passive
// filesystem/process qualification; only start opens the existing daemon socket.
// Both refuse without independently installed deployment-owned qualification.
func CodexRCControl(ctx context.Context, s Settings, sess protocol.Session, operation string, d apiv1.CodexRCDispatch) (apiv1.CodexRCReceipt, error) {
	return codexRCControl(ctx, s, sess, operation, d, realCodexRCDeps())
}
func codexRCControl(ctx context.Context, s Settings, sess protocol.Session, operation string, d apiv1.CodexRCDispatch, deps codexRCDeps) (apiv1.CodexRCReceipt, error) {
	rec := apiv1.CodexRCReceipt{RequestID: d.Request.RequestID, ClientSHA256: d.ClientSHA256, DispatchSHA256: apiv1.CodexRCHash(d), Target: d.Target, Model: d.Request.Model, Effort: d.Request.Effort, State: "refused", Reason: "qualification_refused", StartedAt: deps.now().UTC()}
	finish := func() (apiv1.CodexRCReceipt, error) { rec.FinishedAt = deps.now().UTC(); return rec, nil }
	if operation != "preflight" && operation != "start" {
		return rec, errCodexRCRefused
	}
	check := func(context.Context) error {
		now := deps.now()
		if ctx.Err() != nil || !codexRemoteSession(sess) || sess.Name != d.Target.SessionName || sess.SessionUID != d.Target.SessionUID || s.PodUID != d.Target.PodUID || !d.Request.Valid(now) || !d.Target.Valid(now) || !d.Target.Matches(d.Request) || len(d.ClientSHA256) != 64 {
			return errCodexRCRefused
		}
		// Config is outside HOME's mutable platform state; missing/unknown pairing
		// evidence refuses. No connected/code-issued inference or repair is made.
		raw, err := boundedHostFile(filepath.Join(s.ConfigDir, CodexRCTargetFile), apiv1.CodexRCMaxPacket, false)
		var configured apiv1.CodexRCTarget
		if err != nil || apiv1.DecodeCodexRC(raw, &configured, apiv1.CodexRCMaxPacket) != nil || apiv1.CodexRCHash(configured) != apiv1.CodexRCHash(d.Target) {
			return errCodexRCRefused
		}
		if noSymlinkComponents(s.StateDir) != nil || noSymlinkComponents(d.Target.Root) != nil || !strings.HasPrefix(d.Target.Root, s.Home+string(filepath.Separator)) {
			return errCodexRCRefused
		}
		state, err := os.Stat(s.StateDir)
		if err != nil || !state.IsDir() || state.Mode().Perm()&0o077 != 0 {
			return errCodexRCRefused
		}
		root, err := os.Stat(d.Target.Root)
		if err != nil || !root.IsDir() {
			return errCodexRCRefused
		}
		st, ok := root.Sys().(*syscall.Stat_t)
		if !ok || uint64(st.Dev) != d.Target.RootDevice || st.Ino != d.Target.RootInode || int(st.Uid) != os.Geteuid() || root.Mode().Perm()&0o022 != 0 {
			return errCodexRCRefused
		}
		raw, err = boundedHostFile(s.statePath("project-snapshot.json"), projectcatalog.MaxSnapshotBytes, true)
		if err != nil {
			return errCodexRCRefused
		}
		snapshot, err := projectcatalog.ParseSnapshot(raw)
		if err != nil || apiv1.CodexRCHash(snapshot) != d.Target.SnapshotSHA256 || snapshot.Project() != d.Target.Project || snapshot.Selected().Name != d.Target.Repository {
			return errCodexRCRefused
		}

		raw, err = boundedHostFile(s.statePath(privateProjectBootstrapFile), 256<<10, true)
		var preparation PrivateProjectReport
		if err != nil || projectcatalog.Digest(raw) != d.Target.PreparationSHA256 || decodeHostJSON(raw, &preparation) != nil || preparation.Version != 1 || preparation.State != "Ready" || preparation.CatalogRevision != snapshot.CatalogRevision() {
			return errCodexRCRefused
		}
		prepared := 0
		for _, repo := range preparation.Repositories {
			if repo.Repository == snapshot.Selected() && repo.State == "Ready" {
				prepared++
			}
		}
		if prepared != 1 {
			return errCodexRCRefused
		}
		p, err := deps.process(s, realCodexHostFS())
		if err != nil || p.State != "Live" || p.PID != d.Target.PID || p.Identity.BootID != d.Target.BootID || p.Identity.StartTicks != d.Target.StartTicks {
			return errCodexRCRefused
		}
		return nil
	}
	if check(ctx) != nil {
		return finish()
	}
	if operation == "preflight" {
		rec.State, rec.Reason = "qualified", "qualified"
		return finish()
	}
	// A single retained fence per target UID, no eviction or retry. Reuse the
	// existing hard-link+file/directory-fsync primitive, not a new journal store.
	consumed := false
	consume := func(ctx context.Context) error {
		if check(ctx) != nil {
			return errCodexRCRefused
		}
		if deps.consume(s.statePath(codexRCOnceFile), []byte(rec.DispatchSHA256+"\n"), 0600) != nil {
			return errCodexRCRefused
		}
		consumed = true
		return nil
	}
	bound, cancel := context.WithDeadline(ctx, d.Request.Deadline)
	defer cancel()
	native, err := deps.start(bound, s, d.Target.PID, codexRCThreadSelection{InstallationID: d.Target.InstallationID, EnvironmentID: d.Target.EnvironmentID, Cwd: d.Target.Root, Model: d.Request.Model, Effort: d.Request.Effort, Handoff: d.Request.Handoff, BeforeTurn: check}, consume)
	rec.ThreadID, rec.TurnID = native.ThreadID, native.TurnID
	if consumed {
		rec.State, rec.Reason = "unconfirmed", "create_unconfirmed"
	}
	if err == nil && consumed && native.ThreadID != "" && native.TurnID != "" {
		rec.State, rec.Reason = "confirmed", "thread_and_turn_acknowledged"
	}
	return finish()
}

// ReadCodexRCDispatch bounds stdin and rejects generic RPC/path overrides.
func ReadCodexRCDispatch(r io.Reader) (apiv1.CodexRCDispatch, error) {
	var d apiv1.CodexRCDispatch
	raw, err := io.ReadAll(io.LimitReader(r, apiv1.CodexRCMaxRequest+apiv1.CodexRCMaxPacket+1))
	if err != nil || apiv1.DecodeCodexRC(raw, &d, apiv1.CodexRCMaxRequest+apiv1.CodexRCMaxPacket) != nil {
		return d, errCodexRCRefused
	}
	return d, nil
}
