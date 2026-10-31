package agentd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func outcomeLaunch(t *testing.T) (Settings, Launch) {
	t.Helper()
	s, l := captureLaunch(t)
	sess := protocol.Session{SessionUID: l.SessionUID, Agent: l.Provider, Mode: protocol.ModeTask, Model: l.CaptureModel}
	if err := bindInitialUsage(&l, sess, l.PodUID); err != nil {
		t.Fatal(err)
	}
	if err := beginInitialUsage(l, s.StateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.WorkDir(), 0700); err != nil {
		t.Fatal(err)
	}
	return s, l
}

func TestTaskOutcomeFileAndExitSnapshot(t *testing.T) {
	for _, kind := range []string{"valid", "missing", "invalid", "oversized", "symlink", "fifo", "directory"} {
		t.Run(kind, func(t *testing.T) {
			s, l := outcomeLaunch(t)
			p := filepath.Join(s.WorkDir(), l.Session+".outcome.json")
			switch kind {
			case "valid":
				writeFile(t, p, `{"summary":"Ready for review","prURLs":["https://github.com/thaynes43/dev-env/pull/1"]}`)
			case "invalid":
				writeFile(t, p, `{"summary":"partial","headSHAs":["bad"]}`)
			case "oversized":
				writeFile(t, p, strings.Repeat("x", protocol.MaxTaskOutcomeBytes+1))
			case "symlink":
				if err := os.Symlink("/nonexistent", p); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(p, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			writeResult(s.StateDir, l, time.Now(), 0, false, streamResult{}, true)
			var tr taskResult
			if err := readJSONFile(s.statePath(resultFile), &tr); err != nil {
				t.Fatal(err)
			}
			if (tr.TaskOutcome != nil) != (kind == "valid") {
				t.Fatalf("%s: outcome=%+v", kind, tr.TaskOutcome)
			}
			if kind != "valid" {
				return
			}
			if tr.TaskOutcome.SessionUID != l.SessionUID || tr.TaskOutcome.PodUID != l.PodUID {
				t.Fatal("lost original identity")
			}
			writeFile(t, p, `{"summary":"later mutation"}`)
			if err := writeJSONFile(s.statePath(launchFile), l); err != nil {
				t.Fatal(err)
			}
			if err := writeJSONFile(s.statePath(bootFile), bootRecord{Boot: protocol.BootReady, BootID: l.BootID}); err != nil {
				t.Fatal(err)
			}
			s.PodUID = l.PodUID
			sess := protocol.Session{Name: l.Session, SessionUID: l.SessionUID, Mode: protocol.ModeTask}
			st := CollectStatus(context.Background(), &fakeRunner{}, s, sess, time.Now())
			if st.TaskOutcome == nil || st.TaskOutcome.Record.Summary != "Ready for review" {
				t.Fatal("exit snapshot changed", st.TaskOutcome)
			}
			s.PodUID = "replacement-pod"
			if CollectStatus(context.Background(), &fakeRunner{}, s, sess, time.Now()).TaskOutcome != nil {
				t.Fatal("replacement pod relayed original")
			}
		})
	}
}
