package agentd

import (
	"os"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// noCloneProof reads the volume for a rescue that found no clone. It returns a proof
// only when everything it needs is readable and present: the session's declared
// repository, an unreadable-free ~/repos and ~/work listing, and a version 1 bootstrap
// record. Anything missing or unreadable returns nil, which the operator reads as "not
// proven" (NotProven), exactly as before. It reads counts and closed-set codes only,
// never file contents beyond agentd's own bootstrap record, and never writes.
func noCloneProof(s Settings) *protocol.NoCloneProof {
	if s.Getenv == nil {
		return nil
	}
	sess, err := LoadSession(s.Getenv)
	if err != nil || sess.Repo == "" {
		return nil
	}
	repos, ok := dirEntries(s.ReposDir())
	if !ok {
		return nil
	}
	work, ok := dirEntries(s.WorkDir())
	if !ok {
		return nil
	}
	var record PrivateProjectReport
	if readWorkspaceJSON(s.statePath(privateProjectBootstrapFile), &record) != nil || record.Version != 1 {
		return nil
	}
	proof := &protocol.NoCloneProof{Version: 1, Repo: sess.Repo, ReposEntries: repos, WorkEntries: work,
		BootstrapState: record.State, BootstrapReason: record.Reason}
	for _, src := range record.Repositories {
		if src.Repository.Name == sess.Repo {
			proof.RepositoryState, proof.RepositoryStep, proof.RepositoryReason = src.State, src.Step, src.Reason
			break
		}
	}
	return proof
}

// dirEntries counts every entry directly in dir, hidden ones included. A directory
// that does not exist has none; any other error is unknown.
func dirEntries(dir string) (int, bool) {
	entries, err := os.ReadDir(dir)
	if isNotExist(err) {
		return 0, true
	}
	if err != nil {
		return 0, false
	}
	return len(entries), true
}
