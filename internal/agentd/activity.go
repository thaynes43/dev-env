package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Idle detection (DESIGN-001 4.2, D-59). A session is idle while its agent is
// not working, no tmux client is attached, and nothing changes in its
// worktree. agentd does not decide idleness: it reports the agent's state and
// the newest sign of activity, and the operator's timers judge the window
// (D-09). The signals:
//
//   - Claude's own status in $CLAUDE_CONFIG_DIR/sessions/<pid>.json (busy,
//     idle, waiting) and when it last changed, for the CLI that run-agent
//     started;
//   - the clients attached to tmux session "agent": an attached client counts
//     as activity now;
//   - v1's wt_busy signals: the worktree's git files (HEAD, FETCH_HEAD,
//     ORIG_HEAD, COMMIT_EDITMSG, MERGE_HEAD, REBASE_HEAD) and every file's
//     mtime outside .git, node_modules and .claude;
//   - the task's log and events, as before.
//
// v1's other wt_busy signal, a process with its cwd in the worktree, is left
// out: in a session pod the agent's own CLI always has.

// Claude's statuses in its session file.
const (
	claudeBusy    = "busy"
	claudeIdle    = "idle"
	claudeWaiting = "waiting"
)

// worktreeScanEvery is how often the worktree's files are walked; between
// walks the last answer stands. A heartbeat comes every minute, and the
// shortest idle window is an hour.
const worktreeScanEvery = 5 * time.Minute

// worktreeScanCap bounds one walk. A worktree with more entries outside the
// pruned directories is treated as changed now, which keeps it from looking
// idle: the safe direction.
var worktreeScanCap = 200000

// gitActivityFiles are v1's wt_busy git signals, in the worktree's git dir.
var gitActivityFiles = []string{"HEAD", "FETCH_HEAD", "ORIG_HEAD", "COMMIT_EDITMSG", "MERGE_HEAD", "REBASE_HEAD"}

// prunedDirs are left out of the worktree walk, as v1 left them out.
var prunedDirs = map[string]bool{".git": true, "node_modules": true, ".claude": true}

// claudeSessionRecord is the part of the CLI's sessions/<pid>.json that idle
// detection reads (checked on CLI 2.1.292: status is busy, idle or waiting,
// and updatedAt is in milliseconds).
type claudeSessionRecord struct {
	Status    string `json:"status"`
	UpdatedAt int64  `json:"updatedAt"`
}

// claudeStatus reads the CLI's own status for the process pid: its state and
// when it last changed. ok is false when the CLI wrote no record (yet).
func claudeStatus(s Settings, pid int) (string, time.Time, bool) {
	if pid <= 0 {
		return "", time.Time{}, false
	}
	var rec claudeSessionRecord
	data, err := os.ReadFile(filepath.Join(s.ClaudeConfigDir, "sessions", strconv.Itoa(pid)+".json"))
	if err != nil || json.Unmarshal(data, &rec) != nil || rec.Status == "" {
		return "", time.Time{}, false
	}
	var at time.Time
	if rec.UpdatedAt > 0 {
		at = time.UnixMilli(rec.UpdatedAt).UTC()
	}
	return rec.Status, at, true
}

// tmuxClients counts the clients attached to the agent's tmux session. No
// session, or no tmux server, is no client.
func tmuxClients(ctx context.Context, r Runner, s Settings) int {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"list-clients", "-t", "=" + TmuxSession, "-F", "#{client_tty}"}})
	if err != nil {
		return 0
	}
	n := 0
	for _, l := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

type worktreeScan struct {
	at     time.Time
	newest time.Time
}

var worktreeScans = struct {
	sync.Mutex
	m map[string]worktreeScan
}{m: map[string]worktreeScan{}}

// worktreeActivity is the newest change in the worktree by v1's wt_busy
// signals, walked at most every worktreeScanEvery. A walk that fails or hits
// its cap answers now, so a worktree it cannot read never looks idle.
func worktreeActivity(worktree string, now time.Time) time.Time {
	if worktree == "" {
		return time.Time{}
	}
	worktreeScans.Lock()
	defer worktreeScans.Unlock()
	if c, ok := worktreeScans.m[worktree]; ok && now.Sub(c.at) < worktreeScanEvery && !now.Before(c.at) {
		return c.newest
	}
	newest, err := scanWorktree(worktree)
	if err != nil {
		newest = now
	}
	worktreeScans.m[worktree] = worktreeScan{at: now, newest: newest}
	return newest
}

var errScanCap = errors.New("too many files to walk")

// scanWorktree walks the worktree once: its git dir's signal files, then every
// file outside the pruned directories. A worktree that is gone has no activity.
func scanWorktree(worktree string) (time.Time, error) {
	if _, err := os.Stat(worktree); isNotExist(err) {
		return time.Time{}, nil
	}
	var newest time.Time
	see := func(t time.Time) {
		if t.After(newest) {
			newest = t
		}
	}
	if gd := gitDirOf(worktree); gd != "" {
		for _, f := range gitActivityFiles {
			if fi, err := os.Stat(filepath.Join(gd, f)); err == nil {
				see(fi.ModTime())
			}
		}
	}
	n := 0
	err := filepath.WalkDir(worktree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != worktree && prunedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if n++; n > worktreeScanCap {
			return errScanCap
		}
		if d.Name() == ".git" {
			// A linked worktree's .git file; its git dir is read above.
			return nil
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				see(fi.ModTime())
			}
		}
		return nil
	})
	return newest.UTC(), err
}

// gitDirOf is a worktree's own git dir: the directory a linked worktree's .git
// file names, or the .git directory of a main checkout.
func gitDirOf(worktree string) string {
	p := filepath.Join(worktree, ".git")
	fi, err := os.Stat(p)
	if err != nil {
		return ""
	}
	if fi.IsDir() {
		return p
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	gd, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	gd = strings.TrimSpace(gd)
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(worktree, gd)
	}
	return gd
}

// applyActivity refines the agent's state with Claude's own status, counts the
// attached clients, and sets LastActivity to the newest sign of activity
// (D-59). pid is the running CLI's, 0 when none runs; tui says it is this
// boot's TUI, the only run whose Claude status idle detection reads (a task
// is busy until it ends).
func applyActivity(ctx context.Context, r Runner, s Settings, st *protocol.Status, pid int, tui bool, now time.Time) {
	var newest time.Time
	see := func(t time.Time) {
		if t.After(newest) {
			newest = t
		}
	}
	if st.Agent.LastActivity != nil {
		see(*st.Agent.LastActivity)
	}
	if st.Agent.State == protocol.AgentBusy {
		if status, at, ok := claudeStatus(s, pid); ok && tui {
			switch status {
			case claudeIdle:
				st.Agent.State = protocol.AgentIdle
				see(at)
			case claudeWaiting:
				st.Agent.State = protocol.AgentWaiting
				see(at)
			default:
				// Busy, or a status this agentd does not know: working.
				see(now)
			}
		} else {
			see(now)
		}
	}
	st.Agent.Attached = tmuxClients(ctx, r, s)
	if st.Agent.Attached > 0 {
		see(now)
	}
	if st.Workspace != nil {
		see(worktreeActivity(st.Workspace.Worktree, now))
	}
	if !newest.IsZero() {
		t := newest.UTC()
		if t.After(now) {
			t = now.UTC()
		}
		st.Agent.LastActivity = &t
	}
}
