package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd"
)

func TestShelfCoordinationArchiveFlags(t *testing.T) {
	for _, args := range []struct {
		name                 string
		args                 []string
		history, coord, good bool
	}{
		{"default", nil, false, false, true},
		{"history", []string{"--history-archive"}, true, false, true},
		{"coordination", []string{"--coordination-archive"}, false, true, true},
		{"both history first", []string{"--history-archive", "--coordination-archive"}, true, true, true},
		{"both coordination first", []string{"--coordination-archive", "--history-archive"}, true, true, true},
		{"unknown", []string{"--other"}, false, false, false},
		{"value", []string{"--coordination-archive=true"}, false, false, false},
		{"positional", []string{"extra"}, false, false, false},
		{"duplicate", []string{"--coordination-archive", "--coordination-archive"}, false, false, false},
	} {
		t.Run(args.name, func(t *testing.T) {
			history, coord, good := shelfFlags(args.args)
			if history != args.history || coord != args.coord || good != args.good {
				t.Fatalf("shelfFlags(%q) = %v, %v, %v", args.args, history, coord, good)
			}
		})
	}
}

func TestShelfCoordinationArchiveRefusesBeforeReady(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "AGENTD_SHARED_DIR": "/dev/shm"}
	code, _, stderr := runArgs([]string{"shelf", "--coordination-archive"}, env)
	if code != exitFailure || !strings.Contains(stderr, "coordination archive volume") || strings.Contains(stderr, "shelf ready") {
		t.Fatalf("coordination archive unavailable: code=%d stderr=%q", code, stderr)
	}

}

func TestShelfCoordinationArchiveSuccessWaitsForCancellation(t *testing.T) {
	old := coordArchiveShelfCheck
	coordArchiveShelfCheck = func(agentd.Settings) error { return nil }
	t.Cleanup(func() { coordArchiveShelfCheck = old })

	home := t.TempDir()
	env := map[string]string{"HOME": home, "AGENTD_SHARED_DIR": "/dev/shm"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"shelf", "--coordination-archive"}, strings.NewReader(""), &stdout, &stderr, func(k string) string { return env[k] }, noRunner{})
	if code != exitOK || !strings.Contains(stderr.String(), "coordination archive mounted") || !strings.Contains(stderr.String(), "shelf ready") {
		t.Fatalf("canceled shelf: code=%d stderr=%q", code, stderr.String())
	}
}

func TestShelfBothArchivesRequireBothGuards(t *testing.T) {
	oldHistory := historyShelfCheck
	historyShelfCheck = func(agentd.Settings) error { return errors.New("history unavailable") }
	t.Cleanup(func() { historyShelfCheck = oldHistory })
	oldCoord := coordArchiveShelfCheck
	coordArchiveShelfCheck = func(agentd.Settings) error { return errors.New("coordination unavailable") }
	t.Cleanup(func() { coordArchiveShelfCheck = oldCoord })

	home := t.TempDir()
	env := map[string]string{"HOME": home, "AGENTD_SHARED_DIR": "/dev/shm"}
	for _, args := range [][]string{
		{"shelf", "--history-archive", "--coordination-archive"},
		{"shelf", "--coordination-archive", "--history-archive"},
	} {
		code, _, stderr := runArgs(args, env)
		if code != exitFailure || !strings.Contains(stderr, "history archive volume") {
			t.Fatalf("%q: code=%d stderr=%q", args, code, stderr)
		}
	}

	historyShelfCheck = func(agentd.Settings) error { return nil }
	for _, args := range [][]string{
		{"shelf", "--history-archive", "--coordination-archive"},
		{"shelf", "--coordination-archive", "--history-archive"},
	} {
		code, _, stderr := runArgs(args, env)
		if code != exitFailure || !strings.Contains(stderr, "coordination archive volume") {
			t.Fatalf("%q: code=%d stderr=%q", args, code, stderr)
		}
	}
}
