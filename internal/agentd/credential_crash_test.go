package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// firstInstallCrash reproduces the persistent state at each first-write
// crash boundary, without timing a kill or running a load-dependent test.
func firstInstallCrash(t *testing.T, c Credentials, s CredentialSpec, p protocol.CredentialPayload, stage string) (string, installedCredential) {
	t.Helper()
	path := filepath.Join(c.Grants.Dir, s.Name)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	r := installedCredential{InstalledCredential: protocol.InstalledCredential{Name: s.Name, GrantUID: s.GrantUID, PodUID: s.PodUID, Credential: "proxmox", Expires: s.Expires, InstalledAt: c.Grants.now()}, Payload: p}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if stage != "legacy-complete" {
		owner, err := json.Marshal(ownerOf(r))
		if err != nil {
			t.Fatal(err)
		}
		dir, err := c.openGrant(s.Name)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = dir.Close() }()
		if err := writePrivateCredentialAt(dir, credentialOwnerFile, ".credential-owner-", owner); err != nil {
			t.Fatal(err)
		}
	}
	switch stage {
	case "empty":
		b = nil
	case "partial":
		end := bytes.Index(b, []byte(p.TokenSecret)) + len(p.TokenSecret)/2
		if end < 0 || end >= len(b) {
			t.Fatal("fixture did not reach the private write")
		}
		b = b[:end]
	case "marker-only":
		return path, r
	}
	if err := os.WriteFile(filepath.Join(path, ".credential-012345abcdef"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, r
}

func TestCredentialFirstInstallCrashRecovery(t *testing.T) {
	for _, stage := range []string{"complete", "partial", "empty", "marker-only", "legacy-complete"} {
		t.Run(stage, func(t *testing.T) {
			c, s, p := credentialFixture(t)
			path, _ := firstInstallCrash(t, c, s, p, stage)
			if _, err := c.Use("proxmox"); !errors.Is(err, ErrNoCredential) {
				t.Fatal("uncommitted credential was selectable")
			}
			if list, err := c.List(); err != nil || len(list) != 0 {
				t.Fatal("uncommitted credential appeared in installed list")
			}
			kube := GrantSpec{Name: "grant-kube", Role: "dev-env-grant-nodes", Expires: s.Expires.Add(time.Hour)}
			if err := c.Grants.Install(kube, []byte("fixture-kube-token")); err != nil {
				t.Fatal(err)
			}
			if err := c.Grants.Use(kube.Name); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("kube rewrite deleted pending credential files")
			}
			if err := c.Grants.Remove(s.Name); err == nil {
				t.Fatal("kube remove accepted a pending credential")
			}
			if err := c.Grants.Install(GrantSpec{Name: s.Name, Role: kube.Role, Expires: kube.Expires}, []byte("fixture-kube-token")); err == nil {
				t.Fatal("kube install overwrote a pending credential")
			}
			// A fresh client instance simulates the supervisor/exec restart;
			// the keeper retries the same value from its private journal.
			restarted := Credentials{Grants: c.Grants, PodUID: c.PodUID}
			if err := restarted.Install(s, p); err != nil {
				t.Fatal(err)
			}
			if got, err := restarted.Use("proxmox"); err != nil || got != p {
				t.Fatal("first-install retry did not install original credential")
			}
			entries, err := os.ReadDir(path)
			if err != nil || len(entries) != 1 || entries[0].Name() != credentialFile {
				t.Fatal("retry retained temporary private material or marker")
			}
			if err := restarted.Install(s, p); err != nil {
				t.Fatal("post-recovery reinstall failed")
			}
		})
	}
}

func TestCredentialFirstInstallCrashCleanup(t *testing.T) {
	for _, stage := range []string{"complete", "partial", "empty", "marker-only", "legacy-complete"} {
		for _, cleanup := range []string{"expiry", "remove"} {
			t.Run(stage+"/"+cleanup, func(t *testing.T) {
				c, s, p := credentialFixture(t)
				path, _ := firstInstallCrash(t, c, s, p, stage)
				if err := c.Remove(s.Name, "uid-stale", s.PodUID); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(path); err != nil {
					t.Fatal("stale cleanup removed pending credential")
				}
				if err := c.Remove(s.Name, s.GrantUID, "pod-other"); err == nil {
					t.Fatal("pending cleanup accepted wrong pod UID")
				}
				if cleanup == "expiry" {
					if err := c.Expire(); err != nil {
						t.Fatal(err)
					}
					if _, err := os.Lstat(path); err != nil {
						t.Fatal("expiry cleaned an unexpired pending grant")
					}
					c.Grants.Now = func() time.Time { return s.Expires }
					if err := c.Expire(); err != nil {
						t.Fatal(err)
					}
				} else if err := c.Remove(s.Name, s.GrantUID, s.PodUID); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
					t.Fatal("cleanup retained first-install private material")
				}
			})
		}
	}
}

func credentialSnapshot(t *testing.T, path string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string)
	for _, entry := range entries {
		b, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[entry.Name()] = string(b)
	}
	return out
}

func sameCredentialSnapshot(t *testing.T, path string, before map[string]string) {
	t.Helper()
	after := credentialSnapshot(t, path)
	if len(after) != len(before) {
		t.Fatal("refused operation changed staging entries")
	}
	for file, content := range before {
		if after[file] != content {
			t.Fatal("refused operation changed private staging content")
		}
	}
}

func TestCredentialPendingOwnershipRefusesConflicts(t *testing.T) {
	for _, change := range []string{"grant-uid", "pod-uid", "expiry", "value"} {
		t.Run(change, func(t *testing.T) {
			c, s, p := credentialFixture(t)
			path, _ := firstInstallCrash(t, c, s, p, "partial")
			before := credentialSnapshot(t, path)
			otherSpec, otherPayload := s, p
			switch change {
			case "grant-uid":
				otherSpec.GrantUID = "uid-other"
				otherPayload.TokenID = "dev-env@pve!grant-uid-other"
			case "pod-uid":
				c.PodUID = "pod-other"
				otherSpec.PodUID = c.PodUID
			case "expiry":
				otherSpec.Expires = s.Expires.Add(time.Hour)
			case "value":
				otherPayload.TokenSecret = "fixture-changed-value"
			}
			if err := c.Install(otherSpec, otherPayload); err == nil {
				t.Fatal("pending owner conflict accepted")
			}
			sameCredentialSnapshot(t, path, before)
		})
	}
}

func TestCredentialPendingRejectsUnsafePaths(t *testing.T) {
	for _, change := range []string{"temp-symlink", "marker-symlink", "temp-permissions", "marker-permissions", "directory-permissions", "unrelated", "kube", "marker-uid-conflict", "complete-temp-uid-conflict"} {
		t.Run(change, func(t *testing.T) {
			c, s, p := credentialFixture(t)
			path, r := firstInstallCrash(t, c, s, p, "partial")
			tmp, marker := filepath.Join(path, ".credential-012345abcdef"), filepath.Join(path, credentialOwnerFile)
			switch change {
			case "temp-symlink", "marker-symlink":
				file := tmp
				if change == "marker-symlink" {
					file = marker
				}
				other := filepath.Join(t.TempDir(), "private")
				if err := os.Rename(file, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, file); err != nil {
					t.Fatal(err)
				}
			case "temp-permissions":
				if err := os.Chmod(tmp, 0o644); err != nil {
					t.Fatal(err)
				}
			case "marker-permissions":
				if err := os.Chmod(marker, 0o644); err != nil {
					t.Fatal(err)
				}
			case "directory-permissions":
				if err := os.Chmod(path, 0o755); err != nil {
					t.Fatal(err)
				}
			case "unrelated", "kube":
				file := "other"
				if change == "kube" {
					file = grantFile
				}
				if err := os.WriteFile(filepath.Join(path, file), []byte("unrelated"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "marker-uid-conflict":
				owner := ownerOf(r)
				owner.PodUID = "pod-other"
				b, err := json.Marshal(owner)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(marker, b, 0o600); err != nil {
					t.Fatal(err)
				}
			case "complete-temp-uid-conflict":
				r.GrantUID, r.Payload.TokenID = "uid-other", "dev-env@pve!grant-uid-other"
				b, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(tmp, b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := credentialSnapshot(t, path)
			if err := c.Install(s, p); err == nil {
				t.Fatal("unsafe first-install recovery accepted")
			}
			sameCredentialSnapshot(t, path, before)
			if _, err := c.Use("proxmox"); err == nil {
				t.Fatal("unsafe pending credential selected")
			}
			if change == "marker-uid-conflict" {
				if err := c.Remove(s.Name, s.GrantUID, s.PodUID); err != nil {
					t.Fatal(err)
				}
			} else if err := c.Remove(s.Name, s.GrantUID, s.PodUID); err == nil {
				t.Fatal("unsafe pending cleanup accepted")
			}
			sameCredentialSnapshot(t, path, before)
			c.Grants.Now = func() time.Time { return s.Expires }
			if err := c.Expire(); err == nil {
				t.Fatal("unsafe pending expiry accepted")
			}
			sameCredentialSnapshot(t, path, before)
		})
	}
}

func TestCredentialMarkerWriteCrashHasNoPrivateMaterial(t *testing.T) {
	for _, operation := range []string{"retry", "prune"} {
		t.Run(operation, func(t *testing.T) {
			c, s, p := credentialFixture(t)
			path := filepath.Join(c.Grants.Dir, s.Name)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, ".credential-owner-012345abcdef"), []byte(`{"version":`), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Use("proxmox"); !errors.Is(err, ErrNoCredential) {
				t.Fatal("partial marker was selectable")
			}
			if operation == "retry" {
				if err := c.Install(s, p); err != nil {
					t.Fatal(err)
				}
				if got, err := c.Use("proxmox"); err != nil || got != p {
					t.Fatal("marker-write retry failed")
				}
			} else {
				if err := c.Expire(); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
					t.Fatal("partial metadata-only staging retained")
				}
			}
		})
	}
}
