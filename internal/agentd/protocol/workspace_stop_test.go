package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceStopProofInputIsBoundedAndStrict(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	p := WorkspaceStopProof{Version: 1, Workspace: "w", Task: "task-a", SessionUID: "s", PodName: "task-a", PodUID: "pod", PodResourceVersion: "1", NodeName: "node", NodeUID: "n", LeaseResourceVersion: "2", LeaseRenewedAt: now.Add(-time.Second), VerifiedAt: now}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadWorkspaceStopProof(strings.NewReader(string(b)))
	if err != nil || got.Validate("w", "task-a", "s", "pod") != nil {
		t.Fatalf("proof=%+v,err=%v", got, err)
	}
	for _, bad := range []string{string(b) + ` {}`, strings.TrimSuffix(string(b), "}") + `,"unknown":true}`, strings.Repeat(" ", 16<<10) + string(b)} {
		if _, err := ReadWorkspaceStopProof(strings.NewReader(bad)); err == nil {
			t.Fatal("ambiguous or oversized stop proof accepted")
		}
	}
	got.LeaseRenewedAt = now.Add(time.Second)
	if got.Validate("w", "task-a", "s", "pod") == nil {
		t.Fatal("future lease accepted")
	}
	got.LeaseRenewedAt = now.Add(-41 * time.Second)
	if got.Validate("w", "task-a", "s", "pod") == nil {
		t.Fatal("stale lease accepted")
	}
}
