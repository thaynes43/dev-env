package agentd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestSampleCodexHostMemory(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name, current, limit string
		unreadable, valid    bool
	}{
		{"finite", "3000000000\n", "4294967296\n", false, true},
		{"zero use", "0", "4294967296", false, true},
		{"above limit", "5000000000", "4294967296", false, true},
		{"unlimited", "3", "max", false, false},
		{"missing", "3", "4", true, false},
		{"negative", "-1", "4", false, false},
		{"zero limit", "3", "0", false, false},
		{"overflow", "9223372036854775808", "4", false, false},
		{"oversize", strings.Repeat("1", 33), "4", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sampleCodexHostMemory(now, func(path string) ([]byte, error) {
				if tc.unreadable {
					return nil, errors.New("unavailable")
				}
				switch path {
				case "/sys/fs/cgroup/memory.current":
					return []byte(tc.current), nil
				case "/sys/fs/cgroup/memory.max":
					return []byte(tc.limit), nil
				default:
					t.Fatalf("unexpected read %s", path)
					return nil, nil
				}
			})
			if (m != nil) != tc.valid {
				t.Fatalf("sample %+v", m)
			}
			if m != nil && !m.ObservedAt.Equal(now) {
				t.Fatal("sample time lost")
			}
		})
	}
	if codexHostMemory(protocol.Session{Agent: "claude", Mode: "remote"}, now) != nil || codexHostMemory(protocol.Session{Agent: "codex", Mode: "task"}, now) != nil {
		t.Fatal("non-host measured")
	}
}
