package agentd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func TestDaemonProjectFailureRemainsObservableWithoutProviderOrGitLaunch(t *testing.T) {
	for _, failure := range []string{"clone-owner", "snapshot-bytes", "snapshot-mode", "rules-mode"} {
		t.Run(failure, func(t *testing.T) {
			r := newDaemonRig(t, nil)
			s, sess := sharedSettings(t, r.d.S, r.d.Session.Name)
			catalog, err := projectcatalog.Parse([]byte(projectTestCatalog))
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := catalog.Snapshot("sample", "demo")
			if err != nil {
				t.Fatal(err)
			}
			sess.ProjectSnapshot, err = json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			sess.Base, s.RemoteBase = snapshot.Selected().DefaultBranch, "https://github.com/fixture"
			if failure == "clone-owner" {
				s.RemoteBase = "https://github.com/another-owner"
			} else {
				if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if _, err := StoreProjectSnapshot(s, snapshot); err != nil {
					t.Fatal(err)
				}
				switch failure {
				case "snapshot-bytes":
					writeFile(t, filepath.Join(s.StateDir, "project-snapshot.json"), "{}\n")
				case "snapshot-mode":
					if err := os.Chmod(filepath.Join(s.StateDir, "project-snapshot.json"), 0o644); err != nil {
						t.Fatal(err)
					}
				case "rules-mode":
					if err := os.Chmod(filepath.Join(s.StateDir, "project-rules.md"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			r.d.S, r.d.Session = s, sess
			stop := r.start(t)
			waitFor(t, func() bool { status, count := r.beats.last(); return status.Boot == protocol.BootFailed && count >= 3 })
			if err := stop(); err != nil {
				t.Fatal("failed project admission exited instead of supervising", err)
			}
			var rec bootRecord
			if err := readJSONFile(s.statePath(bootFile), &rec); err != nil {
				t.Fatal(err)
			}
			if rec.Boot != protocol.BootFailed || rec.AgentError == "" || len(rec.Steps) != 1 || rec.Steps[0].Name != "project" || !r.d.writerRefused {
				t.Fatal("failed project admission lost its durable observational status")
			}
			if r.tmuxStarted() || exists(s.statePath(launchFile)) || exists(s.statePath(resumeFile)) || exists(s.WorktreePath(sess.Name)) {
				t.Fatal("failed project admission started a provider or prepared a task worktree")
			}
			for _, line := range r.tmux.lines() {
				if !strings.HasPrefix(line, "tmux list-clients ") && !strings.HasPrefix(line, "tmux display-message ") {
					t.Fatal("observational failed daemon ran a mutation", line)
				}
			}
		})
	}
}
