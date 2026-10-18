package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOwnedRootProbeOutputRejectsRaceableTransport(t *testing.T) {
	for _, st := range []unix.Stat_t{{Mode: unix.S_IFIFO | 0600, Uid: 1000}, {Mode: unix.S_IFIFO | 0620, Uid: 0}, {Mode: unix.S_IFREG | 0600, Uid: 0}, {Mode: unix.S_IFSOCK | 0600, Uid: 0}, {Mode: unix.S_IFCHR | 0600, Uid: 0}} {
		if safeProbeOutput(st) {
			t.Fatal("raceable output accepted", st)
		}
	}
	if !safeProbeOutput(unix.Stat_t{Mode: unix.S_IFIFO | 0600, Uid: 0}) {
		t.Fatal("protected output refused")
	}
}

func TestOwnedRootBootstrapIntentPersistsAndRefusesAliasOrReplay(t *testing.T) {
	state := t.TempDir()
	if os.Chmod(state, 0700) != nil {
		t.Fatal("private state")
	}
	if err := writeOwnedBootstrapIntent(state, "campaign"); err != nil {
		t.Fatal(err)
	}
	if err := writeOwnedBootstrapIntent(state, "campaign"); err == nil {
		t.Fatal("attempt rearmed")
	}
	raw, err := os.ReadFile(filepath.Join(state, "owned-budget-attempt"))
	if err != nil || string(raw) != "campaign" {
		t.Fatal("intent lost", err)
	}
	outside := t.TempDir()
	alias := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	if err := writeOwnedBootstrapIntent(alias, "campaign"); err == nil {
		t.Fatal("aliased state accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "owned-budget-attempt")); !os.IsNotExist(err) {
		t.Fatal("alias wrote outside original state")
	}
}
