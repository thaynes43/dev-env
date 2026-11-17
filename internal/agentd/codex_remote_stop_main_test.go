package agentd

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Pinned main implementation, copied verbatim except for its name from
// 16921a30c4a9da4d736ce29a9937fcbe7fc9d070:internal/agentd/rescue.go.
// This executable reference compares complete calls/results, including invalid
// session input, instead of assuming a reading of stopAgent proves equivalence.
func mainRescueBeforeRemoteStop(ctx context.Context, r Runner, s Settings, session string, now time.Time, opt RescueOptions) (protocol.RescueReport, error) {
	if s.WorkspaceID != "" {
		if opt.StrictHome {
			return protocol.RescueReport{}, errors.New("--strict-home checks a private HOME; this session works in a shared workspace")
		}
		return rescueSharedTask(ctx, r, s, session, now, opt)
	}
	if opt.WorkspaceStopProof != nil {
		return protocol.RescueReport{}, errors.New("workspace stop proof cannot authorize a private rescue")
	}
	if s.Getenv != nil {
		if sess, err := LoadSession(s.Getenv); err == nil && sess.Workspace != nil {
			return protocol.RescueReport{}, errors.New("shared session lacks its operator workspace binding; refusing private rescue")
		}
	}
	empty, err := volumeEmpty(s)
	if err != nil {
		return protocol.RescueReport{}, err
	}
	if empty {
		rep := protocol.RescueReport{Session: session, Stamp: now.UTC().Format("20060102-1504"), StartedAt: now.UTC(),
			Repos: []protocol.RepoRescue{}, OK: true, CleanAndPushed: true, VolumeEmpty: true}
		if opt.StopAgent {
			// Only reads agentd's pid file, which an empty volume lacks.
			rep.Agent = stopAgent(s, opt.StopGrace)
		}
		if opt.StrictHome {
			// Nothing on the volume holds .git, so the check passes.
			rep.StrictHome = &protocol.StrictHomeCheck{}
		}
		rep.FinishedAt = time.Now().UTC()
		return rep, nil
	}
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		return protocol.RescueReport{}, err
	}
	unlock, err := lockFile(s.statePath(rescueLockFile))
	if err != nil {
		return protocol.RescueReport{}, err
	}
	defer unlock()

	rep := protocol.RescueReport{Session: session, Stamp: now.UTC().Format("20060102-1504"), StartedAt: now.UTC(), Repos: []protocol.RepoRescue{}, OK: true, CleanAndPushed: true}
	if opt.StopAgent {
		rep.Agent = stopAgent(s, opt.StopGrace)
	}
	repos, err := clonesIn(s.ReposDir())
	if err != nil {
		return protocol.RescueReport{}, err
	}
	if opt.StrictHome {
		// Before anything is committed or bundled: a refusal leaves the
		// volume and the shared volume as they were.
		rep.StrictHome = strictHome(ctx, r, s, repos)
		if rep.StrictHome.Refused != "" {
			rep.OK, rep.CleanAndPushed = false, false
			rep.FinishedAt = time.Now().UTC()
			return rep, nil
		}
	}
	for _, repo := range repos {
		rr := rescueRepo(ctx, r, s, repo, rep.Stamp)
		if rr.Error != "" {
			rep.OK = false
		}
		if rr.Error != "" || !rr.Fetched || len(rr.UnpushedRefs) > 0 {
			rep.CleanAndPushed = false
		}
		for _, w := range rr.Worktrees {
			if w.Refused != "" {
				rep.OK = false
			}
			if w.Dirty || w.Refused != "" || w.RescueBranch != "" {
				rep.CleanAndPushed = false
			}
		}
		rep.Repos = append(rep.Repos, rr)
	}
	if len(repos) == 0 {
		rep.NoClone = noCloneProof(s)
	}
	writeBundles(ctx, r, s, &rep, now)
	rep.FinishedAt = time.Now().UTC()
	return rep, nil
}
