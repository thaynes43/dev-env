package agentd

import (
	"errors"
	"os"
	"path/filepath"
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
				case "/sys/fs/cgroup/memory.stat":
					return []byte("inactive_file 0\n"), nil
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

func TestCodexHostWorkingSetFixtures(t *testing.T) {
	for _, tc := range []struct {
		name, current, stat string
		want                int64
		unknown             bool
	}{
		{"subtract page cache", "3000000000", "anon 1000000000\ninactive_file 2000000000\nactive_file 0\n", 1000000000, false},
		{"floor at zero", "10", "inactive_file 20\n", 0, false},
		{"zero cache", "123", "inactive_file 0\n", 123, false},
		{"missing current", "", "inactive_file 0\n", 0, true},
		{"bad current", "garbage", "inactive_file 0\n", 0, true},
		{"missing stat", "123", "", 0, true},
		{"missing inactive", "123", "anon 12\n", 0, true},
		{"bad stat", "123", "inactive_file nope\n", 0, true},
		{"negative inactive", "123", "inactive_file -1\n", 0, true},
		{"overflow inactive", "123", "inactive_file 9223372036854775808\n", 0, true},
		{"duplicate inactive", "123", "inactive_file 1\ninactive_file 2\n", 0, true},
		{"oversized stat", "123", strings.Repeat("anon 0\n", 10000) + "inactive_file 0\n", 0, true},
		{"malformed line shape", "123", "inactive_file 0\nbroken\n", 0, true},
		{"malformed other line", "123", "inactive_file 1\nanon nope\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{"memory.current": tc.current, "memory.stat": tc.stat, "memory.max": "4294967296\n"}
			for name, value := range files {
				if value != "" {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
			m := sampleCodexHostMemory(now, func(path string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, filepath.Base(path))) })
			if tc.unknown {
				if m != nil {
					t.Fatalf("malformed/unreadable sample accepted: %+v", m)
				}
			} else if m == nil || m.CurrentBytes != tc.want || m.LimitBytes != 4294967296 || !m.ObservedAt.Equal(now) {
				t.Fatalf("working set %+v, want %d", m, tc.want)
			}
		})
	}
}
