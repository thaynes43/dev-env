package agentd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestBoundedSessionFileAcceptsProjectionRotationAndRefusesFIFO(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "version-one")
	link := filepath.Join(dir, "session.json")
	first := []byte(`{"name":"original"}`)
	if err := os.WriteFile(target, first, 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	got, err := readSessionFile(link, protocol.MaxSessionFileBytes)
	if err != nil || !bytes.Equal(got, first) {
		t.Fatal("projected regular file refused", err)
	}
	second := filepath.Join(dir, "version-two")
	if err := os.WriteFile(second, []byte("rotated"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	got, err = readSessionFile(link, protocol.MaxSessionFileBytes)
	if err != nil || string(got) != "rotated" {
		t.Fatal("stale cached projection", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(link, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSessionFile(link, protocol.MaxSessionFileBytes); err == nil {
		t.Fatal("FIFO session accepted")
	}
	oversized := filepath.Join(dir, "oversized")
	if err := os.WriteFile(oversized, []byte(strings.Repeat("x", protocol.MaxSessionFileBytes+1)), 0444); err != nil {
		t.Fatal(err)
	}
	if _, err := readSessionFile(oversized, protocol.MaxSessionFileBytes); err == nil {
		t.Fatal("oversized file accepted")
	}
	if _, err := LoadSession(envOf(map[string]string{protocol.SessionFileEnv: second, "AGENTD_ENABLE_RETAINED_COMMON_RULES": "true"})); err == nil {
		t.Fatal("caller-overridden input path accepted")
	}
}
