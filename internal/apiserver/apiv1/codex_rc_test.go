package apiv1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCodexRCRequestBounds(t *testing.T) {
	now := time.Now().UTC()
	r := CodexRCRequest{RequestID: "once", SessionUID: "session", PodUID: "pod", Project: "project", Repository: "repo", Model: "gpt-6.1-sol", Effort: "medium", Handoff: "handoff", Deadline: now.Add(20 * time.Second)}
	if !r.Valid(now) {
		t.Fatal("valid request refused")
	}
	for _, mode := range []string{"blank", "oversize", "control", "alias", "effort", "deadline", "past"} {
		t.Run(mode, func(t *testing.T) {
			bad := r
			switch mode {
			case "blank":
				bad.Handoff = "  "
			case "oversize":
				bad.Handoff = strings.Repeat("a", 4097)
			case "control":
				bad.Handoff = "\x1b[0m"
			case "alias":
				bad.Model = "sol"
			case "effort":
				bad.Effort = "none"
			case "deadline":
				bad.Deadline = now.Add(31 * time.Second)
			case "past":
				bad.Deadline = now
			}
			if bad.Valid(now) {
				t.Fatal("invalid request admitted")
			}
		})
	}
	before := CodexRCClientHash("caller", r)
	r.Handoff += "changed"
	if before == CodexRCClientHash("caller", r) {
		t.Fatal("handoff not bound")
	}
	if CodexRCClientHash("caller", r) == CodexRCClientHash("other", r) {
		t.Fatal("caller not bound")
	}
	for _, raw := range []string{`{"requestID":"a","requestID":"b"}`, `{"cwd":"/tmp"}`, `{} {}`, `null`, strings.Repeat(" ", CodexRCMaxRequest+1)} {
		var parsed CodexRCRequest
		if DecodeCodexRC([]byte(raw), &parsed, CodexRCMaxRequest) == nil && parsed.Valid(now) {
			t.Fatal("malformed request admitted")
		}
	}
	raw, _ := json.Marshal(r)
	var parsed CodexRCRequest
	if DecodeCodexRC(raw, &parsed, CodexRCMaxRequest) != nil {
		t.Fatal("valid JSON refused")
	}
}

func TestCodexRCTargetStructureAndFreshness(t *testing.T) {
	now := time.Now().UTC()
	target := CodexRCTarget{SessionName: "rc-host", SessionUID: "session", PodUID: "pod", Project: "project", Repository: "repo", SnapshotSHA256: strings.Repeat("a", 64), Root: "/home/dev/work/accepted", RootDevice: 1, RootInode: 2, PreparationSHA256: strings.Repeat("b", 64), PID: 123, BootID: "boot", StartTicks: 42, InstallationID: "installation", EnvironmentID: "environment", PairingSource: "supported-client", PairingSHA256: strings.Repeat("c", 64), VerifiedAt: now.Add(-time.Minute), ExpiresAt: now.Add(14 * time.Minute)}
	if !target.StructurallyValid() || !target.Valid(now) {
		t.Fatal("valid target refused")
	}
	for _, mode := range []string{"expired", "future", "missing-time", "equal-time", "reversed-time", "long-window", "root", "pairing"} {
		t.Run(mode, func(t *testing.T) {
			bad := target
			switch mode {
			case "expired":
				bad.VerifiedAt, bad.ExpiresAt = now.Add(-15*time.Minute), now
			case "future":
				bad.VerifiedAt, bad.ExpiresAt = now.Add(time.Minute), now.Add(2*time.Minute)
			case "missing-time":
				bad.VerifiedAt = time.Time{}
			case "equal-time":
				bad.ExpiresAt = bad.VerifiedAt
			case "reversed-time":
				bad.ExpiresAt = bad.VerifiedAt.Add(-time.Minute)
			case "long-window":
				bad.ExpiresAt = bad.VerifiedAt.Add(15*time.Minute + time.Nanosecond)
			case "root":
				bad.Root = "/"
			case "pairing":
				bad.PairingSource = "connected"
			}
			wantStructure := mode == "expired" || mode == "future"
			if bad.StructurallyValid() != wantStructure || bad.Valid(now) {
				t.Fatal("target structure/freshness fence changed")
			}
		})
	}
}
