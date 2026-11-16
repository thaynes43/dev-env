package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestCodexRemoteStopUnnamedMatchesMain(t *testing.T) {
	oldNow := preflightStopNow
	preflightStopNow = func() time.Time { return rescueNow }
	t.Cleanup(func() { preflightStopNow = oldNow })
	for _, kind := range []string{"host", "claude", "task", "invalid-inline", "invalid-file", "no-document"} {
		for _, decision := range []string{"", "another-session-uid", "session-c1"} {
			if (kind == "host" || strings.HasPrefix(kind, "invalid") || kind == "no-document") && decision == "session-c1" {
				continue
			}
			if (strings.HasPrefix(kind, "invalid") || kind == "no-document") && decision != "" {
				continue
			}
			t.Run(kind+"/decision="+decision, func(t *testing.T) {
				r := newRemoteStopRig(t)
				r.install(t)
				sess := r.sess
				switch kind {
				case "claude":
					sess.Agent, sess.Model = protocol.AgentClaude, "claude-opus-5-5"
				case "task":
					sess.Mode, sess.Prompt = protocol.ModeTask, "synthetic"
				}
				raw, _ := json.Marshal(sess)
				values := map[string]string{protocol.SessionEnv: string(raw), protocol.CodexRemoteStopEnv: decision}
				switch kind {
				case "invalid-inline":
					values[protocol.SessionEnv] = "{invalid"
				case "invalid-file":
					path := filepath.Join(t.TempDir(), "session.json")
					writeFile(t, path, "{invalid")
					values[protocol.SessionFileEnv] = path
				case "no-document":
					delete(values, protocol.SessionEnv)
				}
				var reads []string
				decisionReads := 0
				r.s.Getenv = func(key string) string {
					if key == protocol.CodexRemoteStopEnv {
						decisionReads++
						return values[key]
					}
					reads = append(reads, key)
					return values[key]
				}
				// The legacy preflight path has a visible refusal marker and result. No
				// real pid file is supplied, and every Git command uses the fake runner.
				writeJSON(t, r.s.statePath(launchFile), Launch{BootID: "boot-c1", ConversationID: "synthetic", Preflight: &launchPreflight{}})
				writeFile(t, r.s.statePath(startPreflightFile), "invalid")
				writeFile(t, filepath.Join(r.s.ReposDir(), "demo", ".git", "config"), "fake")
				wantRunner := &fakeRunner{}
				want, wantErr := mainRescueBeforeRemoteStop(context.Background(), wantRunner, r.s, sess.Name, rescueNow, RescueOptions{StopAgent: true})
				wantReads := append([]string(nil), reads...)
				wantMarker, err := os.ReadFile(r.s.statePath(startStoppedFile))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(r.s.statePath(startStoppedFile)); err != nil {
					t.Fatal(err)
				}
				reads, decisionReads = nil, 0
				var log bytes.Buffer
				old := slog.Default()
				slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
				defer slog.SetDefault(old)
				gotRunner := &fakeRunner{}
				got, gotErr := Rescue(context.Background(), gotRunner, r.s, sess.Name, rescueNow, RescueOptions{StopAgent: true})
				gotMarker, err := os.ReadFile(r.s.statePath(startStoppedFile))
				if err != nil {
					t.Fatal(err)
				}
				got.FinishedAt, want.FinishedAt = time.Time{}, time.Time{}
				if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotErr, wantErr) || !reflect.DeepEqual(gotRunner.calls, wantRunner.calls) || !bytes.Equal(gotMarker, wantMarker) {
					t.Fatalf("main behavior differs: got=%+v err=%v calls=%v; main=%+v err=%v calls=%v", got, gotErr, gotRunner.calls, want, wantErr, wantRunner.calls)
				}
				if len(gotRunner.calls) == 0 || len(r.attempts) != 0 || r.procs[10].state != "S" {
					t.Fatalf("legacy path froze/signalled a process or skipped Git: %v %v", r.attempts, gotRunner.calls)
				}
				for _, path := range []string{r.s.statePath(codexRemoteStopFile), codexRemoteGatePath(r.s, codexRemoteStopFile), codexRemoteGatePath(r.s, codexRemoteLifecycleLock)} {
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Fatalf("legacy path wrote remote state %s: %v", filepath.Base(path), err)
					}
				}
				if decisionReads != 1 {
					t.Fatalf("decision read %d times", decisionReads)
				}
				if decision == "" && !reflect.DeepEqual(reads, wantReads) {
					t.Fatalf("added session parsing: got=%v main=%v", reads, wantReads)
				}
				if decision == "another-session-uid" && !strings.Contains(log.String(), "a stop decision for another session was ignored") {
					t.Fatalf("missing ignored-UID log: %s", log.String())
				}
			})
		}
	}
}

func TestCodexRemoteStopNamedDecisionReadOnce(t *testing.T) {
	r := newRemoteStopRig(t)
	r.install(t)
	getenv := r.s.Getenv
	reads := 0
	r.s.Getenv = func(key string) string {
		if key == protocol.CodexRemoteStopEnv {
			reads++
			if reads > 1 {
				return "another-session-uid"
			}
		}
		return getenv(key)
	}
	rep, err := Rescue(context.Background(), &fakeRunner{}, r.s, r.sess.Name, rescueNow, RescueOptions{StopAgent: true})
	if err != nil || !rep.OK || rep.Agent == nil || rep.Agent.Running || !rep.Agent.WasRunning || !r.quiesced() || r.procs[10].state != "T" || reads != 1 {
		t.Fatalf("named decision not used once: report=%+v err=%v reads=%d signals=%v", rep, err, reads, r.attempts)
	}
	if _, err := os.Stat(r.s.statePath(codexRemoteStopFile)); err != nil {
		t.Fatal("named stop lost its fence", err)
	}
}

func TestCodexRemoteStopUnnamedIgnoresLifecycleRefusals(t *testing.T) {
	for _, decision := range []string{"", "another-session-uid"} {
		t.Run(decision, func(t *testing.T) {
			r := newRemoteStopRig(t)
			raw, _ := json.Marshal(r.sess)
			r.s.Getenv = envOf(map[string]string{protocol.SessionEnv: string(raw), protocol.CodexRemoteStopEnv: decision})
			writeFile(t, r.s.statePath(codexRemoteStopFile), "invalid fence")
			r.s.RuntimeDir = "relative-unusable-runtime"
			release, err := codexRemoteLifecycle(r.s, false)
			if err != nil {
				t.Fatal("new lifecycle refusal on unnamed host", err)
			}
			release()
			// Run must reach main's bootstrap validation, before any boot writer.
			// This intentionally invalid configuration starts no process.
			r.s.PrivateProjectBootstrap = true
			wantBoot := ValidatePrivateProjectBootstrap(r.s)
			if wantBoot == nil {
				t.Fatal("fixture did not select bootstrap refusal")
			}
			d := &Daemon{S: r.s, Session: r.sess}
			if err := d.Run(context.Background()); !errors.Is(err, wantBoot) && (err == nil || err.Error() != wantBoot.Error()) {
				t.Fatalf("new agentd-start refusal: got=%v main=%v", err, wantBoot)
			}
			// Thread entry reaches main's invalid-input check despite the bad gate/fence.
			f := &threadFake{now: threadNow}
			got := startCodexThread(context.Background(), r.s, r.sess, CodexThreadRequest{}, f.deps())
			if got.Error == nil || got.Error.Code != "invalid_input" {
				t.Fatalf("new thread-start refusal: %+v", got)
			}
		})
	}
}

func TestCodexRemoteStopUnnamedEmptyHomeMatchesMain(t *testing.T) {
	r := newRemoteStopRig(t)
	r.install(t)
	if err := os.RemoveAll(r.s.StateDir); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r.sess)
	r.s.Getenv = envOf(map[string]string{protocol.SessionEnv: string(raw)})
	mainRunner, runner := &fakeRunner{}, &fakeRunner{}
	want, wantErr := mainRescueBeforeRemoteStop(context.Background(), mainRunner, r.s, r.sess.Name, rescueNow, RescueOptions{StopAgent: true})
	got, gotErr := Rescue(context.Background(), runner, r.s, r.sess.Name, rescueNow, RescueOptions{StopAgent: true})
	want.FinishedAt, got.FinishedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotErr, wantErr) || !reflect.DeepEqual(runner.calls, mainRunner.calls) || !got.VolumeEmpty || len(r.attempts) != 0 {
		t.Fatalf("empty HOME changed: got=%+v %v main=%+v %v signals=%v", got, gotErr, want, wantErr, r.attempts)
	}
	if empty, err := volumeEmpty(r.s); err != nil || !empty {
		t.Fatalf("legacy stop wrote HOME: %v %v", empty, err)
	}
	if entries, err := os.ReadDir(r.s.RuntimeDir); err != nil || len(entries) != 0 {
		t.Fatalf("legacy stop wrote runtime: %v %v", entries, err)
	}
}
