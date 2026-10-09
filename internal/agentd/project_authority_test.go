package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestProjectSyncRefusesMissingOrWrongAcceptedAuthorityBeforeGit(t *testing.T) {
	for _, mode := range []string{"missing callback", "wrong name", "wrong namespace", "missing UID", "missing revision", "wrong data", "read error"} {
		t.Run(mode, func(t *testing.T) {
			_, s, runner, catalog := projectFixture(t)
			options := projectSyncFixtureOptions()
			if mode == "missing callback" {
				options.AcceptedCatalog = nil
			} else {
				read := options.AcceptedCatalog.Read
				options.AcceptedCatalog.Read = func(ctx context.Context) (ProjectCatalogResource, error) {
					resource, err := read(ctx)
					switch mode {
					case "wrong name":
						resource.Name = "caller-catalog"
					case "wrong namespace":
						resource.Namespace = "caller-namespace"
					case "missing UID":
						resource.UID = ""
					case "missing revision":
						resource.ResourceVersion = ""
					case "wrong data":
						resource.Data = []byte(strings.Replace(projectTestCatalog, "Exact rules.", "Other rules.", 1))
					case "read error":
						err = errors.New("fixture API read unavailable")
					}
					return resource, err
				}
			}
			if _, err := SyncProjects(context.Background(), runner, s, catalog, options); err == nil {
				t.Fatal("unconfirmed authority admitted sync")
			}
			if runner.cloneCalls != 0 || exists(filepath.Join(s.Home, "codex", "sample")) {
				t.Fatal("authority refusal performed a Git/root write")
			}
		})
	}
}

func TestProjectSyncRereadsAuthorityUnderPrimaryLockAndPreservesPartialWork(t *testing.T) {
	for _, mode := range []string{"unchanged", "UID", "resourceVersion", "bytes", "aliased bytes", "read error"} {
		t.Run(mode, func(t *testing.T) {
			_, s, runner, catalog := projectFixture(t)
			options := projectSyncFixtureOptions()
			read := options.AcceptedCatalog.Read
			reads := 0
			buffer := []byte(projectTestCatalog)
			options.AcceptedCatalog.Read = func(ctx context.Context) (ProjectCatalogResource, error) {
				resource, err := read(ctx)
				reads++
				if mode == "aliased bytes" {
					resource.Data = buffer
				}
				if reads == 1 {
					return resource, err
				}
				key := sha256.Sum256([]byte(filepath.Join(s.ClonePath("demo"), ".git")))
				unlock, lockErr := workspaceLock(s, "git-"+hex.EncodeToString(key[:]))
				if lockErr == nil {
					unlock()
					t.Fatal("publication authority reread ran outside the primary Git lock")
				}
				if !errors.Is(lockErr, syscall.EWOULDBLOCK) {
					t.Fatalf("publication lock was uncertain: %v", lockErr)
				}
				switch mode {
				case "UID":
					resource.UID = "replacement-catalog"
				case "resourceVersion":
					resource.ResourceVersion = "2"
				case "bytes":
					resource.Data = []byte(strings.Replace(projectTestCatalog, "Exact rules.", "Other rules.", 1))
				case "aliased bytes":
					copy(buffer, []byte(strings.Replace(projectTestCatalog, "Exact rules.", "Other rules.", 1)))
				case "read error":
					err = errors.New("fixture final authority reread unavailable")
				}
				return resource, err
			}
			report, err := SyncProjects(context.Background(), runner, s, catalog, options)
			if err != nil || reads != 2 {
				t.Fatalf("bounded partial sync did not return its findings: %v, reads=%d", err, reads)
			}
			root := filepath.Join(s.Home, "codex", "sample")
			if !exists(filepath.Join(root, "demo", "README.md")) {
				t.Fatal("authority change destroyed prepared repository work")
			}
			if mode == "unchanged" {
				assertNoPreservedProject(t, report)
				if _, err := os.ReadFile(filepath.Join(root, "AGENTS.md")); err != nil {
					t.Fatal(err)
				}
				return
			}
			if exists(filepath.Join(root, "AGENTS.md")) || exists(filepath.Join(root, "CLAUDE.md")) || exists(filepath.Join(s.workspaceDir(), "projects", "sample.json")) {
				t.Fatal("unconfirmed accepted revision published wrappers or receipt")
			}
			if !slices.ContainsFunc(report.Findings, func(f ProjectFinding) bool { return f.Path == root && f.State == "preserved" }) {
				t.Fatal("changed authority did not report preserved partial result")
			}
		})
	}
}
