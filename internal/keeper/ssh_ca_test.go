package keeper

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestValidateSSHCARejectsInvalidMaterial(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
		want    SSHCAValidation
	}{
		{"valid", func(*testing.T, string) {}, SSHCAValidation{Version: 1, PrivateKeyValid: true, PublicKeyValid: true, KeysMatch: true, Valid: true}},
		{"missing-private", func(t *testing.T, dir string) { removeCAFile(t, dir, "private-key") }, SSHCAValidation{Version: 1, FailureCode: SSHCAUnavailable}},
		{"invalid-private", func(t *testing.T, dir string) { replaceCAFile(t, dir, "private-key", []byte("PRIVATE_KEY_CANARY")) }, SSHCAValidation{Version: 1, FailureCode: SSHCAInvalidPrivateKey}},
		{"oversize-private", func(t *testing.T, dir string) {
			replaceCAFile(t, dir, "private-key", []byte(strings.Repeat("x", (16<<10)+1)))
		}, SSHCAValidation{Version: 1, FailureCode: SSHCAUnavailable}},
		{"wrong-private-type", func(t *testing.T, dir string) {
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			block, err := ssh.MarshalPrivateKey(key, "")
			if err != nil {
				t.Fatal(err)
			}
			replaceCAFile(t, dir, "private-key", pem.EncodeToMemory(block))
		}, SSHCAValidation{Version: 1, FailureCode: SSHCAInvalidPrivateKey}},
		{"encrypted-private", func(t *testing.T, dir string) {
			_, key, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			block, err := ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte("synthetic-passphrase"))
			if err != nil {
				t.Fatal(err)
			}
			replaceCAFile(t, dir, "private-key", pem.EncodeToMemory(block))
		}, SSHCAValidation{Version: 1, FailureCode: SSHCAInvalidPrivateKey}},
		{"missing-public", func(t *testing.T, dir string) { removeCAFile(t, dir, "public-key") }, SSHCAValidation{Version: 1, PrivateKeyValid: true, FailureCode: SSHCAUnavailable}},
		{"invalid-public", func(t *testing.T, dir string) { replaceCAFile(t, dir, "public-key", []byte("PUBLIC_KEY_CANARY")) }, SSHCAValidation{Version: 1, PrivateKeyValid: true, FailureCode: SSHCAInvalidPublicKey}},
		{"wrong-public-type", func(t *testing.T, dir string) {
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			public, err := ssh.NewPublicKey(key.Public())
			if err != nil {
				t.Fatal(err)
			}
			replaceCAFile(t, dir, "public-key", ssh.MarshalAuthorizedKey(public))
		}, SSHCAValidation{Version: 1, PrivateKeyValid: true, FailureCode: SSHCAInvalidPublicKey}},
		{"public-options", func(t *testing.T, dir string) { prefixPublicKey(t, dir, "restrict ") }, SSHCAValidation{Version: 1, PrivateKeyValid: true, FailureCode: SSHCAInvalidPublicKey}},
		{"multiple-public-keys", func(t *testing.T, dir string) {
			raw, err := os.ReadFile(filepath.Join(dir, "public-key"))
			if err != nil {
				t.Fatal(err)
			}
			replaceCAFile(t, dir, "public-key", append(raw, raw...))
		}, SSHCAValidation{Version: 1, PrivateKeyValid: true, FailureCode: SSHCAInvalidPublicKey}},
		{"mismatch", func(t *testing.T, dir string) {
			replaceCAFile(t, dir, "public-key", ssh.MarshalAuthorizedKey(testSSHKey(t).PublicKey()))
		}, SSHCAValidation{Version: 1, PrivateKeyValid: true, PublicKeyValid: true, FailureCode: SSHCAKeyMismatch}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := writeTestSSHCA(t)
			tc.prepare(t, dir)
			got := ValidateSSHCA(dir)
			if got != tc.want {
				t.Fatalf("result = %+v, want %+v", got, tc.want)
			}
			raw, err := json.Marshal(got)
			if err != nil || strings.Contains(string(raw), "CANARY") || strings.Contains(string(raw), dir) {
				t.Fatal("validation report exposes local data")
			}
		})
	}
	if got := ValidateSSHCA(filepath.Join(t.TempDir(), "absent")); got.FailureCode != SSHCAUnavailable || got.Valid {
		t.Fatal("missing CA directory accepted")
	}
}

func TestValidateSSHCAUsesProjectedGenerationWithoutSSHConfiguration(t *testing.T) {
	dir, _ := writeTestSSHCA(t)
	root := t.TempDir()
	if err := os.Symlink(dir, filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
	// The projection's generation is authoritative, not same-named root files.
	replaceCAFile(t, root, "private-key", []byte("STALE_PRIVATE_CANARY"))
	replaceCAFile(t, root, "public-key", []byte("STALE_PUBLIC_CANARY"))
	if result := ValidateSSHCA(root); !result.Valid {
		t.Fatalf("valid projected generation rejected: %+v", result)
	}
	if err := (&NativeSSH{CADir: root}).Ready(); err == nil {
		t.Fatal("full trust accepted without targets and host pins")
	}
}

func replaceCAFile(t *testing.T, dir, name string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func removeCAFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
}

func prefixPublicKey(t *testing.T, dir, prefix string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "public-key"))
	if err != nil {
		t.Fatal(err)
	}
	replaceCAFile(t, dir, "public-key", append([]byte(prefix), raw...))
}
