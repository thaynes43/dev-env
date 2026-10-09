package agentd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Shared rescue is bounded by the durable task owner, never by discovering
// every clone, worktree, branch or stash visible on the retained claim.
func rescueSharedTask(ctx context.Context, r Runner, s Settings, task string, now time.Time, opt RescueOptions) (protocol.RescueReport, error) {
	if s.Getenv == nil {
		return protocol.RescueReport{}, errors.New("shared rescue requires the operator session document")
	}
	sess, err := LoadSession(s.Getenv)
	if err != nil {
		return protocol.RescueReport{}, err
	}
	if sess.Name != task {
		return protocol.RescueReport{}, errors.New("shared rescue task does not match the operator session")
	}
	if err := workspacePreflight(s, sess); err != nil {
		return protocol.RescueReport{}, err
	}
	if !opt.StopAgent {
		return protocol.RescueReport{}, errors.New("shared rescue requires explicit stop-and-preserve")
	}
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		return protocol.RescueReport{}, err
	}
	// Rescue must acquire writer protection itself. A busy lock does not
	// identify its holder and cannot prove the daemon is protecting rescue.
	writer, lockErr := workspaceLock(s, "writer-"+task)
	if lockErr != nil {
		return protocol.RescueReport{}, lockErr
	}
	defer writer()
	unlock, err := workspaceAdminLock(s, sess.Repo)
	if err != nil {
		return protocol.RescueReport{}, err
	}
	defer unlock()
	var owner taskOwner
	if err := readWorkspaceJSON(s.ownerPath(task), &owner); err != nil {
		return protocol.RescueReport{}, fmt.Errorf("shared rescue owner: %w", err)
	}
	if err := ownerMatches(s, sess, owner, true); err != nil {
		return protocol.RescueReport{}, err
	}
	if owner.State != "owned" && owner.State != "stopped" {
		return protocol.RescueReport{}, errors.New("shared rescue owner has an uncertain state")
	}
	rep := protocol.RescueReport{Session: task, Stamp: now.UTC().Format("20060102-1504"), StartedAt: now.UTC(),
		Repos: []protocol.RepoRescue{}, Agent: &protocol.AgentStop{}, OK: true, CleanAndPushed: true}
	// The current runner cannot prove that setsid/reparented descendants
	// stopped. Neither a free flock nor the local PID file can replace a
	// controller-verified old Pod whose every container terminated for good.
	uncertain := owner.Launched
	for _, file := range []string{launchFile, resumeFile, pidFile} {
		if _, err := os.Lstat(s.statePath(file)); !errors.Is(err, os.ErrNotExist) {
			uncertain = true
		}
	}
	if uncertain {
		rep.OK, rep.CleanAndPushed, rep.Agent.Running = false, false, true
		rep.Repos = append(rep.Repos, protocol.RepoRescue{Path: owner.Clone, Error: "shared writers require verified container termination; local PID and process-group stop are insufficient"})
		rep.FinishedAt = time.Now().UTC()
		return rep, nil
	}
	if err := validateSharedClone(ctx, r, s, sess.Repo); err != nil {
		return protocol.RescueReport{}, err
	}
	if err := noSymlinkComponents(owner.Worktree); err != nil {
		return protocol.RescueReport{}, err
	}
	top, err := s.git(ctx, r, owner.Worktree, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(top) != owner.Worktree {
		return protocol.RescueReport{}, errors.New("owned rescue worktree has an unexpected root")
	}
	common, err := s.git(ctx, r, owner.Worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || filepath.Clean(common) != filepath.Join(owner.Clone, ".git") {
		return protocol.RescueReport{}, errors.New("owned rescue worktree belongs to a foreign clone")
	}
	rr := protocol.RepoRescue{Path: owner.Clone, Worktrees: []protocol.WorktreeRescue{}}
	fctx, cancel := context.WithTimeout(ctx, rescueFetchTimeout)
	_, err = s.git(fctx, r, owner.Clone, "fetch", "--prune", "--quiet", "origin")
	cancel()
	if err != nil {
		rr.FetchError = cmdDetail(err)
	} else {
		rr.Fetched = true
	}
	w := rescueWorktree(ctx, r, s, owner.Worktree, rep.Stamp)
	// A clean detached/changed branch may be covered by a peer ref. Anchor
	// this task's HEAD in its own namespace instead of adding that peer ref.
	if w.Refused == "" && w.RescueBranch == "" && w.Branch != "agent/"+task {
		w.RescueBranch, err = createRescueRef(ctx, r, s, owner.Worktree, rep.Stamp, w.Head)
		if err != nil {
			w.Refused = err.Error()
		}
	}
	rr.Worktrees = append(rr.Worktrees, w)
	rr.UnpushedRefs, err = ownedUnpushedRefs(ctx, r, s, owner.Clone, task)
	if err != nil {
		rr.Error = cmdDetail(err)
	}
	if rr.Error != "" || w.Refused != "" {
		rep.OK = false
	}
	if rr.Error != "" || !rr.Fetched || len(rr.UnpushedRefs) > 0 || w.Dirty || w.Refused != "" || w.RescueBranch != "" {
		rep.CleanAndPushed = false
	}
	rep.Repos = append(rep.Repos, rr)
	writeBundles(ctx, r, s, &rep, now)
	// Final Git/bundle writes happen before the durable stop receipt, with
	// the administration and task writer protection still held. A failure
	// keeps the owner unchanged and therefore refuses future takeover.
	if rep.OK && (rep.CleanAndPushed || rep.Bundle != nil && rep.Bundle.Error == "") {
		owner.State, owner.StopReason = "stopped", "no-agent-admitted"
		owner.UpdatedAt, owner.StoppedAt = now.UTC(), now.UTC()
		if err := writeWorkspaceJSON(s.ownerPath(task), owner); err != nil {
			return protocol.RescueReport{}, err
		}
	}
	rep.FinishedAt = time.Now().UTC()
	return rep, nil
}

func ownedUnpushedRefs(ctx context.Context, r Runner, s Settings, clone, task string) ([]protocol.Ref, error) {
	branch, wip := "refs/heads/agent/"+task, "refs/heads/rescue/"+task+"/"
	out, err := s.git(ctx, r, clone, "for-each-ref", "--format=%(objectname) %(refname)", branch, wip)
	if err != nil {
		return nil, err
	}
	var refs []protocol.Ref
	scan := bufio.NewScanner(strings.NewReader(out))
	for scan.Scan() {
		commit, name, ok := strings.Cut(scan.Text(), " ")
		if !ok || (name != branch && !strings.HasPrefix(name, wip)) {
			return nil, errors.New("Git returned a ref outside the task's ownership namespace")
		}
		extra, err := s.git(ctx, r, clone, "rev-list", "-n", "1", commit, "--not", "--remotes=origin")
		if err != nil {
			return nil, err
		}
		if extra != "" {
			refs = append(refs, protocol.Ref{Name: name, Commit: commit})
		}
	}
	return refs, scan.Err()
}
