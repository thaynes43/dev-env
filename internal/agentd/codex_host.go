package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/codexauth"
)

// CodexHostOptions is explicit host configuration, never a Session launch or a
// model-authored request. Every operation is disabled unless Enabled is true.
type CodexHostOptions struct {
	Enabled          bool
	CatalogFile      string
	InstructionsFile string
	DeclaredShutdown bool
	TerminationGrace time.Duration
}

// CodexHostStatus contains fixed readiness metadata only. Native identifiers,
// account records, thread IDs and pairing challenges never enter this result.
type CodexHostStatus struct {
	Code       string `json:"code"`
	Process    string `json:"process,omitempty"`
	Connection string `json:"connection,omitempty"`
	Idle       bool   `json:"idle"`
}

type codexHostDeps struct {
	preflight func(Settings) error
	process   func(Settings) (codexHostProcess, error)
	native    func(context.Context, Settings, string, []string, io.Writer) ([]byte, error)
	remote    func(context.Context, Settings, bool, int) (codexHostRemote, error)
	sync      func(Settings, time.Time) error
	wait      func(context.Context)
}

func realCodexHostDeps() codexHostDeps {
	return codexHostDeps{codexHostPreflight, func(s Settings) (codexHostProcess, error) { return inspectCodexHostProcess(s, realCodexHostFS()) }, runCodexHostNative, probeCodexHostRemote, SyncCodexAccess, waitCodexHostTick}
}

// runCodexHostNative scrubs alternate authentication/backend/storage channels.
// Native output is bounded and discarded except for exact version probes or an
// explicit pair caller's private TTY. It is never sent to supervisor logs.
func runCodexHostNative(ctx context.Context, s Settings, bin string, args []string, caller io.Writer) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = s.Home
	unset := append(append([]string{}, codexUnset...), "CODEX_SQLITE_HOME", "CODEX_APP_SERVER_TRANSPORT", "CODEX_APP_SERVER_SOCKET", "CODEX_REMOTE_CONTROL_SOCKET", "CODEX_APP_SERVER_DAEMON_BACKEND")
	cmd.Env = agentEnv(os.Environ(), Launch{Unset: unset, Env: []string{"HOME=" + s.Home, "CODEX_HOME=" + s.CodexHome}})
	cmd.Stdin = nil
	cmd.Stderr = io.Discard
	output := &hostBoundedOutput{limit: 64 << 10, caller: caller}
	cmd.Stdout = output
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil || output.exceeded {
		return nil, errCodexHostUnknown
	}
	return output.data, nil
}

type hostBoundedOutput struct {
	data     []byte
	limit    int
	caller   io.Writer
	exceeded bool
}

func (w *hostBoundedOutput) Write(data []byte) (int, error) {
	if len(data) > w.limit-len(w.data) {
		w.exceeded = true
		return 0, errCodexHostUnknown
	}
	w.data = append(w.data, data...)
	if w.caller != nil {
		if _, err := w.caller.Write(data); err != nil {
			return 0, errCodexHostUnknown
		}
	}
	return len(data), nil
}
func hostVersion(ctx context.Context, s Settings, d codexHostDeps, bin string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := d.native(ctx, s, bin, []string{"--version"}, nil)
	if err != nil || strings.TrimSpace(string(out)) != "codex-cli "+managedCodexVersion {
		return errCodexHostUnknown
	}
	return nil
}
func sameHostProcess(a, b codexHostProcess) bool {
	return a.State == "Live" && b.State == "Live" && a.PID == b.PID && a.Identity == b.Identity
}

// ObserveCodexHost is passive: it never installs auth, modifies config, starts,
// pairs or stops native processes. Failed version/RPC probes remain unknown.
func ObserveCodexHost(ctx context.Context, s Settings, o CodexHostOptions) (CodexHostStatus, error) {
	return observeCodexHost(ctx, s, o, realCodexHostDeps())
}
func observeCodexHost(ctx context.Context, s Settings, o CodexHostOptions, d codexHostDeps) (CodexHostStatus, error) {
	if !o.Enabled {
		return CodexHostStatus{Code: "Disabled"}, nil
	}
	unknown := CodexHostStatus{Code: "Unknown"}
	if d.preflight(s) != nil {
		return unknown, errCodexHostUnknown
	}
	first, err := d.process(s)
	if err != nil {
		return unknown, errCodexHostUnknown
	}
	if first.State == "Absent" {
		return CodexHostStatus{Code: "Absent", Process: "Absent"}, nil
	}
	if first.State != "Live" || hostVersion(ctx, s, d, s.CodexBin) != nil {
		return unknown, errCodexHostUnknown
	}
	managed, err := codexHostManagedBin(s)
	if err != nil || hostVersion(ctx, s, d, managed) != nil {
		return unknown, errCodexHostUnknown
	}
	if codexHostConfig(s, o, false) != nil {
		return unknown, errCodexHostUnknown
	}
	if _, err := codexHostSettings(s, false); err != nil {
		return unknown, errCodexHostUnknown
	}
	remote, err := d.remote(ctx, s, true, first.PID)
	if err != nil {
		return unknown, errCodexHostUnknown
	}
	final, err := d.process(s)
	if err != nil || !sameHostProcess(first, final) {
		return unknown, errCodexHostUnknown
	}
	status := CodexHostStatus{Code: "Observed", Process: "Live", Connection: remote.Connection, Idle: remote.Idle}
	if codexHostAccessReady(s, time.Now()) != nil {
		status.Code = "NeedsLogin"
	}
	return status, nil
}

type codexHostLifecycle struct {
	Version     int              `json:"version"`
	Phase       string           `json:"phase"`
	PodUID      string           `json:"podUID"`
	WorkspaceID string           `json:"workspaceID"`
	CLI         string           `json:"cli"`
	Process     codexHostProcess `json:"process"`
}

func hostLifecycle(s Settings) (codexHostLifecycle, error) {
	raw, err := boundedHostFile(s.statePath("codex-host-start-intent.json"), 16<<10, true)
	if err != nil {
		return codexHostLifecycle{}, err
	}
	var saved codexHostLifecycle
	if decodeHostJSON(raw, &saved) != nil || saved.Version != 1 || saved.CLI != managedCodexVersion || saved.WorkspaceID != s.WorkspaceID || saved.PodUID == "" {
		return codexHostLifecycle{}, errCodexHostUnknown
	}
	switch saved.Phase {
	case "Attempting":
	case "Confirmed", "Stopping", "Stopped":
		if saved.Process.State != "Live" || saved.Process.PID <= 0 || saved.Process.Identity.BootID == "" || saved.Process.Identity.StartTicks == 0 {
			return codexHostLifecycle{}, errCodexHostUnknown
		}
	default:
		return codexHostLifecycle{}, errCodexHostUnknown
	}
	return saved, nil
}
func saveHostLifecycle(s Settings, phase string, process codexHostProcess) error {
	data, err := json.Marshal(codexHostLifecycle{Version: 1, Phase: phase, PodUID: s.PodUID, WorkspaceID: s.WorkspaceID, CLI: managedCodexVersion, Process: process})
	if err != nil || writeCodexPrivateAtomic(s.statePath("codex-host-start-intent.json"), data) != nil {
		return errCodexHostUnknown
	}
	return nil
}

// Start intent is durable before the single native invocation. Lost/failed ACK
// never licenses another automatic start. A later confirmed daemon can still be
// observed; recovery of an absent/uncertain attempted start is explicit review.
func startCodexHost(ctx context.Context, s Settings, o CodexHostOptions, d codexHostDeps) error {
	first, err := d.process(s)
	if err != nil || first.State != "Absent" {
		return errCodexHostUnknown
	}
	previous, err := hostLifecycle(s)
	if err == nil {
		if previous.Phase != "Stopped" {
			return errCodexHostUnknown
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errCodexHostUnknown
	}
	if hostVersion(ctx, s, d, s.CodexBin) != nil {
		return errCodexHostUnknown
	}
	// A fresh native installation bootstraps from the invoking package. Existing
	// managed packages must match before start; malformed/broken current refuses.
	managed, err := codexHostManagedBin(s)
	if err == nil {
		if hostVersion(ctx, s, d, managed) != nil {
			return errCodexHostUnknown
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errCodexHostUnknown
	} else {
		for _, root := range []string{"app-server-daemon", "standalone"} {
			if _, err := os.Lstat(filepath.Join(s.CodexHome, "packages", root, "current")); !errors.Is(err, os.ErrNotExist) {
				return errCodexHostUnknown
			}
		}
	}
	if d.sync(s, time.Now()) != nil {
		return ErrCodexAccess
	}
	if codexHostConfig(s, o, true) != nil {
		return errCodexHostUnknown
	}
	if _, err := codexHostSettings(s, true); err != nil {
		return errCodexHostUnknown
	}
	final, err := d.process(s)
	if err != nil || final.State != "Absent" {
		return errCodexHostUnknown
	}
	if saveHostLifecycle(s, "Attempting", codexHostProcess{}) != nil {
		return errCodexHostUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := d.native(ctx, s, s.CodexBin, []string{"remote-control", "start", "--json"}, nil); err != nil {
		return errCodexHostUnknown
	}
	expected, err := d.process(s)
	if err != nil || expected.State != "Live" {
		return errCodexHostUnknown
	}
	status, err := observeCodexHost(ctx, s, o, d)
	if err != nil || status.Process != "Live" {
		return errCodexHostUnknown
	}
	confirmed, err := d.process(s)
	if err != nil || !sameHostProcess(expected, confirmed) {
		return errCodexHostUnknown
	}
	return saveHostLifecycle(s, "Confirmed", confirmed)
}

// RunCodexHost owns only the private native host lifecycle/auth installation.
// It never enters Session daemon/task-writer/clone/render/sync code. Projection
// failure reports renewal; native cached auth is not claimed to be revoked.
func RunCodexHost(ctx context.Context, s Settings, o CodexHostOptions, report func(CodexHostStatus) error) error {
	return runCodexHost(ctx, s, o, report, realCodexHostDeps())
}
func waitCodexHostTick(ctx context.Context) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
func runCodexHost(ctx context.Context, s Settings, o CodexHostOptions, report func(CodexHostStatus) error, d codexHostDeps) error {
	if !o.Enabled {
		return report(CodexHostStatus{Code: "Disabled"})
	}
	if d.preflight(s) != nil {
		return errCodexHostUnknown
	}
	release, err := hostSupervisorLock(s)
	if err != nil {
		return err
	}
	defer release()
	first, err := d.process(s)
	// Exactly one initial absence decision may start native. Unknown first state,
	// failed/lost startup ACK and all later probes only monitor, never retry start.
	if err == nil && first.State == "Absent" {
		_ = startCodexHost(ctx, s, o, d)
	}
	for {
		status, err := observeCodexHost(ctx, s, o, d)
		if err != nil {
			status = CodexHostStatus{Code: "Unknown"}
		}
		if d.preflight(s) == nil && d.sync(s, time.Now()) != nil && err == nil {
			status.Code = "NeedsLogin"
		}
		// Broken supervisory stdout is not permission to kill native active chats.
		// Only pod shutdown ends this monitor; output never includes native responses.
		_ = report(status)
		d.wait(ctx)
		if ctx.Err() != nil {
			if o.DeclaredShutdown {
				return stopCodexHost(context.Background(), s, o, d)
			}
			return nil
		}
	}
}
func hostSupervisorLock(s Settings) (func(), error) {
	if os.MkdirAll(s.StateDir, 0o700) != nil {
		return nil, errCodexHostUnknown
	}
	f, err := os.OpenFile(s.statePath("codex-host-supervisor.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, errCodexHostUnknown
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 || syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		_ = f.Close()
		return nil, errCodexHostUnknown
	}
	return func() { _ = f.Close() }, nil
}

// PairCodexHost accepts only the explicitly supplied private owner TTY. Raw
// pairing output is never returned in status or included in an error. The
// caller checks TTY before entering this API; no implicit pairing exists.
func PairCodexHost(ctx context.Context, s Settings, o CodexHostOptions, caller *os.File) error {
	if !o.Enabled || caller == nil {
		return errCodexHostUnknown
	}
	if _, err := unix.IoctlGetTermios(int(caller.Fd()), unix.TCGETS); err != nil {
		return errCodexHostUnknown
	}
	return pairCodexHost(ctx, s, o, caller, realCodexHostDeps())
}
func pairCodexHost(ctx context.Context, s Settings, o CodexHostOptions, caller io.Writer, d codexHostDeps) error {
	if !o.Enabled || caller == nil {
		return errCodexHostUnknown
	}
	status, err := observeCodexHost(ctx, s, o, d)
	if err != nil || status.Process != "Live" {
		return errCodexHostUnknown
	}
	if codexHostAccessReady(s, time.Now()) != nil {
		return ErrCodexAccess
	}
	first, err := d.process(s)
	if err != nil || first.State != "Live" {
		return errCodexHostUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Pinned start_remote_control_pairing connects to the existing socket only;
	// unlike start, it never enables/replaces/starts a daemon. Unknown delivery is
	// returned once with a fixed error; there is no implicit retry.
	if _, err := d.native(ctx, s, s.CodexBin, []string{"remote-control", "pair", "--json"}, caller); err != nil {
		return errCodexHostUnknown
	}
	final, err := d.process(s)
	if err != nil || !sameHostProcess(first, final) {
		return errCodexHostUnknown
	}
	return nil
}
func codexHostAccessReady(s Settings, now time.Time) error {
	if s.CodexAccessFile == "" || privateCodexHome(s) != nil {
		return ErrCodexAccess
	}
	raw, err := readCodexAccessFile(s.CodexAccessFile)
	if err != nil {
		return ErrCodexAccess
	}
	access, err := codexauth.Decode(raw, now)
	if err != nil || !access.ExpiresAt.After(now.Add(5*time.Minute)) {
		return ErrCodexAccess
	}
	return nil
}

// StopCodexHost is an explicit declared host shutdown, never task stop proof.
func StopCodexHost(ctx context.Context, s Settings, o CodexHostOptions) error {
	if !o.Enabled || !o.DeclaredShutdown || codexHostPreflight(s) != nil {
		return errCodexHostUnknown
	}
	release, err := hostSupervisorLock(s)
	if err != nil {
		return err
	}
	defer release()
	return stopCodexHost(ctx, s, o, realCodexHostDeps())
}
func stopCodexHost(ctx context.Context, s Settings, o CodexHostOptions, d codexHostDeps) error {
	if !o.Enabled || !o.DeclaredShutdown {
		return errCodexHostUnknown
	}
	status, err := observeCodexHost(ctx, s, o, d)
	if err != nil || status.Process != "Live" || !status.Idle {
		return errCodexHostUnknown
	}
	before, err := d.process(s)
	if err != nil || before.State != "Live" {
		return errCodexHostUnknown
	}
	saved, err := hostLifecycle(s)
	if err != nil || saved.Phase != "Confirmed" || saved.PodUID != s.PodUID || !sameHostProcess(saved.Process, before) {
		return errCodexHostUnknown
	}
	grace, err := codexHostSettings(s, false)
	if err != nil {
		return errCodexHostUnknown
	}
	budget := time.Duration(grace)*time.Second + 15*time.Second
	if o.TerminationGrace <= budget {
		return errCodexHostUnknown
	}
	latest, err := d.process(s)
	if err != nil || !sameHostProcess(before, latest) {
		return errCodexHostUnknown
	}
	if saveHostLifecycle(s, "Stopping", before) != nil {
		return errCodexHostUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	_, err = d.native(ctx, s, s.CodexBin, []string{"remote-control", "stop", "--json"}, nil)
	if err != nil {
		return errCodexHostUnknown
	}
	final, err := d.process(s)
	if err != nil || final.State != "Absent" {
		return errCodexHostUnknown
	}
	// Only our confirmed start plus successful explicit native stop and exact
	// absence completes this lifecycle. PID staleness/free locks never do so.
	return saveHostLifecycle(s, "Stopped", before)
}
