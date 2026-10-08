package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func credentialFixture(t *testing.T) (Credentials, CredentialSpec, protocol.CredentialPayload) {
	t.Helper()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	c := Credentials{Grants: Grants{Dir: t.TempDir(), Server: "https://127.0.0.1:443", Namespace: "dev-agents", Now: func() time.Time { return now }}, PodUID: "pod-one"}
	s := CredentialSpec{Name: "grant-pve", GrantUID: "uid-one", PodUID: c.PodUID, Expires: now.Add(time.Hour)}
	p := protocol.CredentialPayload{Version: 1, Credential: "proxmox", TokenID: "dev-env@pve!grant-" + s.GrantUID, TokenSecret: "fixture-private-value"}
	return c, s, p
}

func TestCredentialsCoexistFenceAndExpire(t *testing.T) {
	c, s, p := credentialFixture(t)
	if err := c.Install(s, p); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.Grants.Dir, s.Name, credentialFile)
	for file, mode := range map[string]fs.FileMode{filepath.Dir(path): 0o700, path: 0o600} {
		fi, err := os.Lstat(file)
		if err != nil || fi.Mode().Perm() != mode {
			t.Fatal("credential is not private")
		}
	}
	kube := GrantSpec{Name: "grant-kube", Role: "dev-env-grant-nodes", Expires: s.Expires.Add(time.Hour)}
	if err := c.Grants.Install(kube, []byte("fixture-kube-token")); err != nil {
		t.Fatal(err)
	}
	if err := c.Grants.Use(kube.Name); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Use("proxmox"); err != nil || got != p {
		t.Fatal("kube rewrite removed or altered the credential")
	}
	if list, err := c.Grants.List(); err != nil || len(list) != 1 || list[0].Name != kube.Name {
		t.Fatal("credential appeared as a kube context")
	}
	list, err := c.List()
	if err != nil || len(list) != 1 || list[0].GrantUID != s.GrantUID {
		t.Fatal("credential metadata missing")
	}
	data, err := json.Marshal(list)
	if err != nil || bytes.Contains(data, []byte(p.TokenSecret)) || bytes.Contains(data, []byte(p.TokenID)) {
		t.Fatal("list disclosed private material")
	}
	if err := c.Install(s, p); err != nil {
		t.Fatal("same credential reinstall failed")
	}
	if err := c.Grants.Remove(s.Name); err == nil {
		t.Fatal("kube cleanup accepted a credential directory")
	}
	if err := c.Grants.Install(GrantSpec{Name: s.Name, Role: kube.Role, Expires: kube.Expires}, []byte("fixture-kube-token")); err == nil {
		t.Fatal("kube install overwrote a credential")
	}
	if err := c.Install(CredentialSpec{Name: kube.Name, GrantUID: s.GrantUID, PodUID: s.PodUID, Expires: s.Expires}, p); err == nil {
		t.Fatal("credential install overwrote a kube grant")
	}
	// Replace the AccessGrant, then replay stale cleanup for the old UID.
	s2, p2 := s, p
	s2.GrantUID, p2.TokenID, p2.TokenSecret = "uid-two", "dev-env@pve!grant-uid-two", "fixture-replacement-value"
	if err := c.Install(s2, p2); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(s.Name, s.GrantUID, s.PodUID); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Use("proxmox"); err != nil || got != p2 {
		t.Fatal("stale cleanup deleted replacement grant")
	}
	if err := c.Remove(s.Name, s2.GrantUID, "pod-other"); err == nil {
		t.Fatal("cleanup accepted wrong pod UID")
	}
	c.Grants.Now = func() time.Time { return s.Expires }
	if _, err := c.Use("proxmox"); !errors.Is(err, ErrNoCredential) {
		t.Fatal("expiry boundary allowed credential use")
	}
	if err := c.Expire(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("expiry retained private material")
	}
	if err := c.Remove(s.Name, s2.GrantUID, s.PodUID); err != nil {
		t.Fatal("cleanup must be idempotent")
	}
	if list, err := c.Grants.List(); err != nil || len(list) != 1 {
		t.Fatal("credential expiry damaged kube grants")
	}
}

func TestCredentialPayloadStrictAndRedacted(t *testing.T) {
	_, s, p := credentialFixture(t)
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ReadCredentialPayload(bytes.NewReader(data), s.GrantUID); err != nil || got != p {
		t.Fatal("valid payload rejected")
	}
	for _, input := range []string{
		string(data) + " {}", string(data[:len(data)-1]) + `,"unexpected":"fixture-private-value"}`,
		strings.Replace(string(data), `"version":1`, `"version":1,"version":2`, 1),
		strings.Replace(string(data), `"version":1`, `"version":2`, 1),
		strings.Replace(string(data), "grant-uid-one", "grant-uid-other", 1),
		strings.Replace(string(data), "fixture-private-value", `fixture-private-value\nnext`, 1),
		strings.Repeat("fixture-private-value", maxCredentialBytes),
		`{"version":1,"credential":"proxmox","tokenID":"fixture-private-value","tokenSecret":!}`,
	} {
		if _, err := ReadCredentialPayload(strings.NewReader(input), s.GrantUID); err == nil || strings.Contains(err.Error(), p.TokenSecret) {
			t.Fatal("malformed input accepted or disclosed")
		}
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, p), p.TokenSecret) {
			t.Fatal("formatting disclosed the payload")
		}
		if strings.Contains(fmt.Sprintf(format, installedCredential{Payload: p}), p.TokenSecret) {
			t.Fatal("formatting disclosed store state")
		}
	}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("private formatting", "payload", p, "record", installedCredential{Payload: p})
	if strings.Contains(logs.String(), p.TokenSecret) {
		t.Fatal("structured logging disclosed private material")
	}
}

func TestCredentialExpiryContinuesPastMalformedEntry(t *testing.T) {
	c, s, p := credentialFixture(t)
	if err := c.Install(s, p); err != nil {
		t.Fatal(err)
	}
	bad, badPayload := s, p
	bad.Name, bad.GrantUID, badPayload.TokenID = "grant-a-bad", "uid-bad", "dev-env@pve!grant-uid-bad"
	if err := c.Install(bad, badPayload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Grants.Dir, bad.Name, credentialFile), []byte(`{"invalid":"fixture-private-value"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Grants.Dir, "grant-a-unsafe"), []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.Grants.Now = func() time.Time { return s.Expires }
	if err := c.Expire(); err == nil {
		t.Fatal("malformed entry was not reported")
	}
	if _, err := os.Lstat(filepath.Join(c.Grants.Dir, s.Name)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("malformed sibling prevented valid expiry cleanup")
	}
}

func TestCredentialInterruptedReplacementAndEncodedLimit(t *testing.T) {
	c, s, p := credentialFixture(t)
	tooLarge := p
	tooLarge.TokenSecret = strings.Repeat(`"`, maxTokenBytes)
	if err := c.Install(s, tooLarge); err == nil {
		t.Fatal("oversized encoded private document installed")
	}
	if entries, err := os.ReadDir(c.Grants.Dir); err != nil || len(entries) != 0 {
		t.Fatal("size rejection changed the store")
	}
	if err := c.Install(s, p); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(c.Grants.Dir, s.Name, ".credential-012345abcdef")
	if err := os.WriteFile(tmp, []byte("partial private fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Use("proxmox"); err != nil {
		t.Fatal("interrupted replacement hid valid document")
	}
	if err := c.Install(s, p); err != nil {
		t.Fatal("same credential retry failed")
	}
	if _, err := os.Lstat(tmp); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("retry retained stale temporary private file")
	}
	if err := os.WriteFile(tmp, []byte("partial private fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.Grants.Now = func() time.Time { return s.Expires }
	if err := c.Expire(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(c.Grants.Dir, s.Name)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("expiry retained interrupted private replacement")
	}
}

func TestCredentialsRejectAtomically(t *testing.T) {
	for _, mutate := range []string{"file-symlink", "dir-symlink", "file-permissions", "dir-permissions", "metadata", "pod-uid", "provider-uid", "unrelated", "expired-install", "changed-value", "changed-expiry", "root-symlink", "lock-symlink"} {
		t.Run(mutate, func(t *testing.T) {
			c, s, p := credentialFixture(t)
			if err := c.Install(s, p); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(c.Grants.Dir, s.Name, credentialFile)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mutate {
			case "file-symlink":
				other := filepath.Join(t.TempDir(), "private")
				if err := os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatal(err)
				}
			case "dir-symlink":
				other := filepath.Join(t.TempDir(), "private")
				if err := os.Rename(filepath.Dir(path), other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
			case "file-permissions":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "dir-permissions":
				if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
			case "metadata", "pod-uid", "provider-uid":
				change := append([]byte(nil), before...)
				if mutate == "metadata" {
					change = bytes.Replace(change, []byte(`"name":"grant-pve"`), []byte(`"name":"grant-other"`), 1)
				}
				if mutate == "pod-uid" {
					change = bytes.Replace(change, []byte(`"podUID":"pod-one"`), []byte(`"podUID":"pod-other"`), 1)
				}
				if mutate == "provider-uid" {
					change = bytes.Replace(change, []byte("!grant-uid-one"), []byte("!grant-uid-other"), 1)
				}
				if err := os.WriteFile(path, change, 0o600); err != nil {
					t.Fatal(err)
				}
				before = change
			case "unrelated":
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), "other"), []byte("unrelated"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "expired-install":
				s.Expires = c.Grants.now()
			case "changed-value":
				p.TokenSecret = "fixture-changed-private-value"
			case "changed-expiry":
				s.Expires = s.Expires.Add(time.Hour)
			case "root-symlink":
				root := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(c.Grants.Dir, root); err != nil {
					t.Fatal(err)
				}
				c.Grants.Dir = root
			case "lock-symlink":
				lock := filepath.Join(c.Grants.Dir, grantsLock)
				other := filepath.Join(t.TempDir(), "lock")
				if err := os.Rename(lock, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, lock); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.Install(s, p); err == nil || strings.Contains(err.Error(), p.TokenSecret) {
				t.Fatal("unsafe install accepted or disclosed")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected install changed existing credential")
			}
			if mutate != "expired-install" && mutate != "changed-value" && mutate != "changed-expiry" {
				if _, err := c.Use("proxmox"); err == nil {
					t.Fatal("unsafe installed entry selected")
				}
			}
		})
	}
}

type differentOwnerInfo struct{ fs.FileInfo }

func (i differentOwnerInfo) Sys() any { return &syscall.Stat_t{Uid: uint32(os.Geteuid()) + 1} }

func TestCredentialsRejectWrongFileOwner(t *testing.T) {
	c, s, p := credentialFixture(t)
	if err := c.Install(s, p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(c.Grants.Dir, s.Name, credentialFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkPrivate(differentOwnerInfo{fi}, false); err == nil {
		t.Fatal("wrong owner accepted")
	}
}

func TestDaemonRemovesExpiredCredentials(t *testing.T) {
	c, s, p := credentialFixture(t)
	if err := c.Install(s, p); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{S: Settings{Getenv: func(key string) string {
		if key == protocol.GrantsDirEnv {
			return c.Grants.Dir
		}
		if key == protocol.PodUIDEnv {
			return c.PodUID
		}
		return ""
	}}, Now: func() time.Time { return s.Expires }, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Interval: time.Hour, Poll: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.supervise(ctx) }()
	waitFor(t, func() bool {
		_, err := os.Lstat(filepath.Join(c.Grants.Dir, s.Name, credentialFile))
		return errors.Is(err, fs.ErrNotExist)
	})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("expiry supervisor did not stop")
	}
}
