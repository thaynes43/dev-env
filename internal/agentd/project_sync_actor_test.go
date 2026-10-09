package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func initializerFixture(t *testing.T) Settings {
	t.Helper()
	g := newGitFixture(t, "demo")
	s, _ := g.settings(t)
	s, _ = sharedSettings(t, s, "unused")
	if err := os.Remove(filepath.Join(s.workspaceDir(), "marker.json")); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestProjectSyncInitializerNeverConvertsUnmarkedData(t *testing.T) {
	for _, path := range []string{"metadata", "repos", "codex", "work", "symlink", "read-only", "matching", "foreign"} {
		t.Run(path, func(t *testing.T) {
			s := initializerFixture(t)
			marker := filepath.Join(s.workspaceDir(), "marker.json")
			var preserved string
			switch path {
			case "matching", "foreign":
				id := s.WorkspaceID
				if path == "foreign" {
					id = "other-workspace"
				}
				preserved = marker
				writeFile(t, marker, "{\"version\":1,\"id\":\""+id+"\"}")
			case "symlink":
				preserved = filepath.Join(s.Home, "private-history")
				writeFile(t, preserved, "keep private history")
				if err := os.Symlink(preserved, marker); err != nil {
					t.Fatal(err)
				}
			case "read-only":
				readWorkspaceMountInfo = func() ([]byte, error) {
					return []byte(strings.ReplaceAll(string(workspaceMountTable(s.Home)), " rw -", " ro -")), nil
				}
			default:
				root := filepath.Join(s.Home, path)
				if path == "metadata" {
					root = s.workspaceDir()
				}
				preserved = filepath.Join(root, "existing")
				writeFile(t, preserved, "existing provider or workspace bytes")
			}
			before, _ := os.ReadFile(preserved)
			err := PrepareProjectSyncStorage(s, true)
			if path == "matching" && err != nil || path != "matching" && err == nil {
				t.Fatalf("initializer %s: %v", path, err)
			}
			if preserved != "" {
				after, readErr := os.ReadFile(preserved)
				if readErr != nil || string(after) != string(before) {
					t.Fatal("initializer altered existing bytes")
				}
			}
			if path != "matching" && path != "foreign" && path != "symlink" && exists(marker) {
				t.Fatal("refused storage gained a marker")
			}
		})
	}
}

func TestProjectSyncInitializerLostACKRefusesAndLaterOnlyValidates(t *testing.T) {
	s := initializerFixture(t)
	if err := PrepareProjectSyncStorage(s, false); err == nil {
		t.Fatal("ordinary sync initialized unmarked storage")
	}
	write := createProjectWorkspaceMarker
	t.Cleanup(func() { createProjectWorkspaceMarker = write })
	createProjectWorkspaceMarker = func(path string, marker workspaceMarker) error {
		if err := write(path, marker); err != nil {
			return err
		}
		return errors.New("fixture lost durable write acknowledgment")
	}
	if err := PrepareProjectSyncStorage(s, true); err == nil {
		t.Fatal("lost marker ACK admitted this attempt")
	}
	marker := filepath.Join(s.workspaceDir(), "marker.json")
	fi, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	createProjectWorkspaceMarker = func(string, workspaceMarker) error { t.Fatal("matching marker was recreated"); return nil }
	writeFile(t, filepath.Join(s.ReposDir(), "late-work"), "keep work")
	if err := PrepareProjectSyncStorage(s, true); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(marker)
	if !fi.ModTime().Equal(after.ModTime()) {
		t.Fatal("fresh validation modified the existing marker")
	}
}

func TestProjectSyncGitIsPrivateStrictAndDoesNotRenderProviders(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, runner := g.settings(t)
	operation := filepath.Join(s.StateDir, "project-sync", "job-1")
	if err := os.MkdirAll(operation, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(s.Home, ".codex", "history.jsonl"), "private transcript")
	guarded, err := PrepareProjectSyncGit(runner, s, operation, "/creds/gh_token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guarded.Run(context.Background(), Cmd{Name: "claude", Args: []string{"--version"}}); err == nil {
		t.Fatal("management runner invoked a provider")
	}
	if _, err := guarded.Run(context.Background(), Cmd{Name: "git", Args: []string{"config", "--global", "--get", "user.name"}}); err != nil {
		t.Fatal(err)
	}
	gitRun(t, g.env, g.seed, "config", "core.askPass", "untrusted-shared-askpass")
	askPass, err := guarded.Run(context.Background(), Cmd{Name: "git", Args: []string{"-C", g.seed, "config", "--get", "core.askPass"}})
	if err != nil || strings.TrimSpace(askPass.Stdout) != "" {
		t.Fatal("repository configuration supplied a credential launcher")
	}
	hooks := filepath.Join(s.Home, "fixture-hooks")
	writeFile(t, filepath.Join(hooks, "post-checkout"), "#!/bin/sh\nprintf 'untrusted hook ran' > \"$HOME/hook-canary\"\n")
	if err := os.Chmod(filepath.Join(hooks, "post-checkout"), 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, g.env, g.seed, "config", "core.hooksPath", hooks)
	if _, err := guarded.Run(context.Background(), Cmd{Name: "git", Args: []string{"-C", g.seed, "checkout", "-b", "sync-fixture"}}); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(s.Home, "hook-canary")) {
		t.Fatal("Git-only preparation launched a repository hook")
	}
	if _, err := PrepareProjectSyncGit(runner, s, operation, "/creds/gh_token"); err == nil {
		t.Fatal("existing Git config was replaced")
	}
	if exists(filepath.Join(s.Home, ".gitconfig")) || exists(filepath.Join(s.Home, ".claude")) {
		t.Fatal("Git preparation rendered ordinary provider/home config")
	}
	data, err := os.ReadFile(filepath.Join(s.Home, ".codex", "history.jsonl"))
	if err != nil || string(data) != "private transcript" {
		t.Fatal("Git preparation altered provider history")
	}
}

func TestProjectSyncChecksActorUnderEveryAdministrationLock(t *testing.T) {
	for _, refuseAt := range []int{1, 2, 3} {
		t.Run(string(rune('0'+refuseAt)), func(t *testing.T) {
			_, s, runner, catalog := projectFixture(t)
			options := projectSyncFixtureOptions()
			checks := 0
			key := sha256.Sum256([]byte(filepath.Join(s.ClonePath("demo"), ".git")))
			options.BeforeMutation = func(context.Context) error {
				checks++
				unlock, err := workspaceLock(s, "git-"+hex.EncodeToString(key[:]))
				if err == nil {
					unlock()
					t.Fatal("actor identity was checked outside the repository lock")
				}
				if !errors.Is(err, syscall.EWOULDBLOCK) {
					t.Fatalf("unexpected lock refusal: %v", err)
				}
				if checks >= refuseAt {
					return errors.New("fixture actor deleted while waiting")
				}
				return nil
			}
			report, err := SyncProjects(context.Background(), runner, s, catalog, options)
			root := filepath.Join(s.Home, "codex", "sample")
			if err != nil || checks < refuseAt || len(report.Findings) == 0 || exists(filepath.Join(root, "AGENTS.md")) || exists(filepath.Join(root, "CLAUDE.md")) {
				t.Fatalf("revoked actor published rules: %v, checks=%d", err, checks)
			}
			if refuseAt == 1 && exists(root) || refuseAt <= 2 && runner.cloneCalls != 0 {
				t.Fatal("revoked actor performed the refused stage's shared writes")
			}
			if refuseAt == 3 && (!exists(filepath.Join(root, "demo", "README.md")) || runner.cloneCalls != 1) {
				t.Fatal("final publication refusal lost the previously prepared anchor")
			}
		})
	}
}
