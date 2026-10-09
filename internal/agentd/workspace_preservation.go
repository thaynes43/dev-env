package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// This runs under the task writer and common-Git locks, after genuine executor
// termination. It preserves incomplete preparation without inventing a clean
// pushed worktree, an empty home, or an owner for an unadmitted task.
func preserveWorkspacePreparation(ctx context.Context, r Runner, s Settings, sess protocol.Session, owner *taskOwner, proof *protocol.WorkspaceStopProof, now time.Time) (protocol.RescueReport, error) {
	if proof == nil || owner != nil && owner.Launched {
		return protocol.RescueReport{}, errors.New("absent preparation requires verified never-launched executor stop")
	}
	var a workspaceAdmission
	if err := readWorkspaceJSON(s.statePath(workspaceAdmissionFile), &a); err != nil {
		return protocol.RescueReport{}, fmt.Errorf("private admission proof: %w", err)
	}
	if err := admissionMatches(s, sess, a, proof.PodUID); err != nil {
		return protocol.RescueReport{}, err
	}
	if owner == nil && a.Phase != "Refused" || owner != nil && a.Phase != "OwnerAdmitted" {
		return protocol.RescueReport{}, errors.New("checking or ambiguous admission cannot prove absent task work")
	}
	for _, file := range []string{launchFile, resumeFile, pidFile} {
		if _, err := os.Lstat(s.statePath(file)); !errors.Is(err, os.ErrNotExist) {
			return protocol.RescueReport{}, errors.New("private launch or uncertain admission state cannot prove absent preparation")
		}
	}
	worktree, clone := s.WorktreePath(sess.Name), s.ClonePath(sess.Repo)
	if err := noSymlinkComponents(filepath.Dir(worktree)); err != nil {
		return protocol.RescueReport{}, err
	}
	if _, err := os.Lstat(worktree); !errors.Is(err, os.ErrNotExist) {
		return protocol.RescueReport{}, errors.New("partial, unreadable or existing task path must be preserved and refused")
	}
	// A staging clone can contain partial Git data. Do not delete it or
	// mistake the absence of the final clone name for a completed preparation.
	staging := filepath.Join(filepath.Dir(clone), "."+filepath.Base(clone)+".agentd-clone")
	if _, err := os.Lstat(staging); !errors.Is(err, os.ErrNotExist) {
		return protocol.RescueReport{}, errors.New("partial or uncertain staging clone is preserved and refused")
	}
	if err := noSymlinkComponents(filepath.Dir(clone)); err != nil {
		return protocol.RescueReport{}, err
	}
	rr := protocol.RepoRescue{Path: clone, Worktrees: []protocol.WorktreeRescue{{Path: worktree, Absent: true}}}
	if _, err := os.Lstat(clone); errors.Is(err, os.ErrNotExist) {
		rr.Absent = true
	} else if err != nil {
		return protocol.RescueReport{}, err
	} else {
		if err := validateSharedClone(ctx, r, s, sess.Repo); err != nil {
			return protocol.RescueReport{}, err
		}
		// Inspect Git registration only; never inspect or rescue peers' paths.
		list, err := s.git(ctx, r, clone, "worktree", "list", "--porcelain")
		if err != nil {
			return protocol.RescueReport{}, err
		}
		for _, line := range strings.Split(list, "\n") {
			if line == "worktree "+worktree {
				return protocol.RescueReport{}, errors.New("absent task path still has partial Git registration; preserved and refused")
			}
		}
		rr.UnpushedRefs, err = ownedTaskRefs(ctx, r, s, clone, sess.Name, false)
		if err != nil {
			return protocol.RescueReport{}, err
		}
	}
	if owner == nil && len(rr.UnpushedRefs) > 0 {
		return protocol.RescueReport{}, errors.New("task refs without a durable owner cannot be claimed or bundled")
	}
	p := &protocol.WorkspacePreservation{Version: workspaceVersion, Workspace: s.WorkspaceID, Task: sess.Name,
		SessionUID: sess.Workspace.SessionUID, SourcePodUID: proof.PodUID, Kind: "NoWorkAdmitted"}
	if owner != nil {
		p.OwnerGeneration = owner.Generation
	} else {
		p.NoOwner = true
	}
	rep := protocol.RescueReport{WorkspacePreservation: p, SourcePodUID: proof.PodUID, Session: sess.Name,
		Stamp: now.UTC().Format("20060102-1504"), StartedAt: now.UTC(), Repos: []protocol.RepoRescue{rr}, Agent: &protocol.AgentStop{}, OK: true}
	if len(rr.UnpushedRefs) > 0 {
		p.Kind = "OwnedRefsPreserved"
		rep.Repos[0].FullBundle = true
		writeBundles(ctx, r, s, &rep, now)
		if rep.Bundle == nil || rep.Bundle.Error != "" || rep.Repos[0].Bundle == nil || !rep.Repos[0].Bundle.Verified {
			return rep, errors.New("all owned refs require a verified full bundle before stop")
		}
	}
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	if owner != nil {
		owner.State, owner.StopReason, owner.StopProof = "stopped", "terminated-pod-rescue", proof
		owner.UpdatedAt, owner.StoppedAt = now.UTC(), now.UTC()
		if err := writeWorkspaceJSON(s.ownerPath(sess.Name), owner); err != nil {
			return rep, err
		}
	}
	// A generation-zero result does not create a shared owner. The controller
	// records its fresh proof before Pod cleanup; the next Pod claims normally.
	rep.FinishedAt = time.Now().UTC()
	return rep, nil
}
