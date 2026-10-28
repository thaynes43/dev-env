package agentd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// interruptedVolume is a volume whose bootstrap stopped: agentd's state is there, the
// declared clone never was. Synthetic; no history, no secrets.
func interruptedVolume(t *testing.T, record *PrivateProjectReport) Settings {
	t.Helper()
	old := sharedIsMounted
	sharedIsMounted = func(string, string) error { return nil }
	t.Cleanup(func() { sharedIsMounted = old })
	s := testSettings(t, t.TempDir())
	doc, err := json.Marshal(cloneSession("demo-1006-170000"))
	if err != nil {
		t.Fatal(err)
	}
	orig := s.Getenv
	s.Getenv = func(k string) string {
		if k == protocol.SessionEnv {
			return string(doc)
		}
		if k == protocol.SessionFileEnv {
			return ""
		}
		return orig(k)
	}
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(s.StateDir, "boot.json"), "{}") // agent state: the volume is not empty
	if record != nil {
		writeJSON(t, filepath.Join(s.StateDir, privateProjectBootstrapFile), record)
	}
	return s
}

func cancelledBootstrap() *PrivateProjectReport {
	return &PrivateProjectReport{Version: 1, State: protocol.BootstrapFailed, Reason: protocol.ReasonCatalogUnconfirmed}
}

func rescueNoClone(t *testing.T, s Settings) protocol.RescueReport {
	t.Helper()
	rep, err := Rescue(context.Background(), &fakeRunner{}, s, "demo-1006-170000", rescueNow, RescueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestRescueReportsAnInterruptedBootstrapWithoutAClone(t *testing.T) {
	s := interruptedVolume(t, cancelledBootstrap())
	rep := rescueNoClone(t, s)
	p := rep.NoClone
	if rep.VolumeEmpty || len(rep.Repos) != 0 || !rep.OK || p == nil || p.Version != 1 || p.Repo != "demo" || p.ReposEntries != 0 || p.WorkEntries != 0 ||
		p.BootstrapState != protocol.BootstrapFailed || p.BootstrapReason != protocol.ReasonCatalogUnconfirmed || p.RepositoryState != "" {
		t.Fatalf("report %+v proof %+v", rep, p)
	}
	// The rescue only reads: the volume is as it was.
	if _, err := os.Stat(filepath.Join(s.StateDir, "boot.json")); err != nil {
		t.Error("agent state is gone")
	}
	if _, err := os.Stat(s.ReposDir()); err == nil {
		t.Error("the rescue created ~/repos")
	}

	// A record that lists the repository carries its state, step and reason.
	listed := cancelledBootstrap()
	listed.State, listed.Reason = protocol.BootstrapPartial, ""
	listed.Repositories = []PrivateProjectSource{{Repository: projectcatalog.Repository{Name: "demo"}, State: protocol.BootstrapPreserved, Step: protocol.StepClone, Reason: protocol.ReasonStopped}}
	if p := rescueNoClone(t, interruptedVolume(t, listed)).NoClone; p == nil || p.RepositoryState != protocol.BootstrapPreserved || p.RepositoryStep != protocol.StepClone || p.RepositoryReason != protocol.ReasonStopped {
		t.Fatalf("listed repository: %+v", p)
	}
}

// What the proof counts is exactly what it says: any entry, even a hidden clone staging
// directory or a stray file, shows up as a nonzero count and the operator refuses it.
func TestRescueCountsWhatIsLeftInReposAndWork(t *testing.T) {
	for name, path := range map[string]func(Settings) string{
		"hidden clone staging": func(s Settings) string { return filepath.Join(s.ReposDir(), ".demo.clone-123", "x") },
		"a stray file":         func(s Settings) string { return filepath.Join(s.ReposDir(), "note") },
		"a worktree directory": func(s Settings) string { return filepath.Join(s.WorkDir(), "demo-1006-170000", "x") },
	} {
		t.Run(name, func(t *testing.T) {
			s := interruptedVolume(t, cancelledBootstrap())
			writeFile(t, path(s), "x")
			p := rescueNoClone(t, s).NoClone
			if p == nil || p.ReposEntries+p.WorkEntries == 0 {
				t.Fatalf("the leftover was not counted: %+v", p)
			}
		})
	}
}

// Without the evidence there is no proof, and the operator keeps NotProven.
func TestRescueGivesNoProofWithoutTheEvidence(t *testing.T) {
	t.Run("no bootstrap record", func(t *testing.T) {
		if rep := rescueNoClone(t, interruptedVolume(t, nil)); rep.NoClone != nil {
			t.Fatalf("proof without a record: %+v", rep.NoClone)
		}
	})
	t.Run("an unknown record version", func(t *testing.T) {
		bad := cancelledBootstrap()
		bad.Version = 2
		if rep := rescueNoClone(t, interruptedVolume(t, bad)); rep.NoClone != nil {
			t.Fatalf("proof from version 2: %+v", rep.NoClone)
		}
	})
	t.Run("no session in the environment", func(t *testing.T) {
		s := interruptedVolume(t, cancelledBootstrap())
		s.Getenv = func(string) string { return "" }
		if rep := rescueNoClone(t, s); rep.NoClone != nil {
			t.Fatalf("proof without a session: %+v", rep.NoClone)
		}
	})
	t.Run("a clone exists", func(t *testing.T) {
		rig := newRescueRig(t)
		if rep := rig.rescue(t); rep.NoClone != nil || len(rep.Repos) != 1 {
			t.Fatalf("a rescue with a clone carries a proof: %+v", rep.NoClone)
		}
	})
}
