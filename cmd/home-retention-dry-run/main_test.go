package main

// Finite synthetic stdin/file fixtures only. No subprocess, network, HOME
// snapshot, mounts, credentials, deletion or runtime integration is exercised.
import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/homeretention"
)

func TestReadOnlyDefaultAndExplicitNow(t *testing.T) {
	input := `{"version":"home-retention.v1","records":[{"uid":"legacy-pvc"}]}`
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--now", "2026-11-01T12:00:00Z"}, strings.NewReader(input), &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	var report homeretention.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.DryRun || report.DeleteEnabled || len(report.ImmediateCandidates) != 0 || len(report.StoppedWorkQueue) != 1 || !report.StoppedWorkQueue[0].DecisionRequired {
		t.Fatalf("incorrect default report: %+v", report)
	}
	for _, args := range [][]string{nil, {"--now", "invalid"}, {"--now", "2026-11-01T12:00:00Z", "--delete"}, {"--now", "2026-11-01T12:00:00Z", "--enable"}, {"--now", "2026-11-01T12:00:00Z", "--warning-lead", "0s"}, {"--now", "2026-11-01T12:00:00Z", "--observation-max-age", "2h"}, {"--now", "2026-11-01T12:00:00Z", "extra"}} {
		stdout.Reset()
		stderr.Reset()
		if code := run(args, strings.NewReader(input), &stdout, &stderr); code != 2 || stdout.Len() != 0 {
			t.Fatalf("invalid invocation accepted: %v, code=%d output=%s", args, code, stdout.String())
		}
	}
}

func TestBoundedFilesAndStrictInput(t *testing.T) {
	dir := t.TempDir()
	inputPath, trustPath := filepath.Join(dir, "snapshot.json"), filepath.Join(dir, "trust.json")
	input := `{"version":"home-retention.v1","records":[]}`
	trust := `{"version":"home-retention.v1","keys":[]}`
	if err := os.WriteFile(inputPath, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trustPath, []byte(trust), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"--now", "2026-11-01T12:00:00Z", "--input", inputPath, "--trust", trustPath}
	if code := run(args, strings.NewReader("unused"), &stdout, &stderr); code != 0 {
		t.Fatalf("file fixture failed: %s", stderr.String())
	}
	for _, invalid := range []string{`{"version":"home-retention.v1","records":[],"delete":true}`, strings.Repeat(" ", homeretention.MaxSnapshotBytes+1)} {
		stdout.Reset()
		stderr.Reset()
		if code := run([]string{"--now", "2026-11-01T12:00:00Z"}, strings.NewReader(invalid), &stdout, &stderr); code != 2 || stdout.Len() != 0 {
			t.Fatal("invalid input emitted report")
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--help"}, strings.NewReader(""), &stdout, &stderr); code != 0 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "required evaluation time") {
		t.Fatal("help failed")
	}
	data, err := os.ReadFile(inputPath)
	if err != nil || string(data) != input {
		t.Fatal("input snapshot changed")
	}
}

func TestLegacyAuditModeNeverPromotesTrust(t *testing.T) {
	// The orphan has no clock; its PVC birth must not create one.
	input := `{"at":"2026-11-01T12:00:00Z","claims":[{"name":"home-fixture","namespace":"test","uid":"pvc-1","creationTimestamp":"2020-01-01T00:00:00Z"}],"sessions":[],"pods":[]}`
	var stdout, stderr bytes.Buffer
	args := []string{"--now", "2026-11-01T12:00:00Z", "--format", "legacy-snapshot"}
	if code := run(args, strings.NewReader(input), &stdout, &stderr); code != 0 {
		t.Fatalf("legacy audit failed: %s", stderr.String())
	}
	var report homeretention.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.DryRun || report.DeleteEnabled || len(report.ImmediateCandidates) != 0 || len(report.StoppedWorkQueue) != 1 || report.StoppedWorkQueue[0].Deadline != "" || report.StoppedWorkQueue[0].Provenance == nil {
		t.Fatalf("legacy input promoted or lost provenance: %+v", report)
	}
	for _, invalid := range [][]string{{"--format", "unsupported"}, {"--format", "legacy-snapshot", "--trust", "must-not-open"}, {"--legacy-dispositions", "must-not-open"}} {
		stdout.Reset()
		stderr.Reset()
		invalid = append([]string{"--now", "2026-11-01T12:00:00Z"}, invalid...)
		if code := run(invalid, strings.NewReader(input), &stdout, &stderr); code != 2 || stdout.Len() != 0 {
			t.Fatalf("invalid legacy invocation accepted: %v", invalid)
		}
	}
}
