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
