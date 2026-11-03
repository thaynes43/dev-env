package agentrun

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The pod's agentd records a credential-free rejection marker when the provider
// refuses the applied generation while its local expiry is still ahead. auth
// status reads it, read-only. Every time is relative to testNow.

func rejectionDoc(gen uint64, observed time.Time) string {
	return fmt.Sprintf(`{"version":1,"generation":%d,"observedAt":%q}`, gen, observed.UTC().Format(time.RFC3339))
}

func rejectedHarness(t *testing.T, projGen int, marker string) *harness {
	t.Helper()
	h := newHarness(t)
	hermeticAuth(t, h)
	proj := h.write("claude.json", authClaudeDoc(projGen, testNow.Add(5*time.Hour)))
	// The projection was published an hour before any marker was written.
	earlier := time.Now().Add(-time.Hour)
	if err := os.Chtimes(proj, earlier, earlier); err != nil {
		t.Fatal(err)
	}
	h.vars["AGENTD_CLAUDE_LIVE_FILE"] = proj
	if marker != "" {
		h.vars["HOME"] = filepath.Join(h.dir, "home")
		h.write("home/.agentd/claude-rejected.json", marker)
		// agentd applied this generation an hour ago; its receipt sits beside the
		// marker and is the stable reference (the projection file's mtime is not:
		// a pod restart resets a Secret volume's times).
		writeReceipt(t, h, "home/.agentd", uint64(projGen), time.Now().Add(-time.Hour))
	}
	return h
}

func writeReceipt(t *testing.T, h *harness, dir string, gen uint64, at time.Time) {
	t.Helper()
	p := h.write(filepath.Join(dir, "claude-access-generation.json"), fmt.Sprintf(`{"generation":%d,"fingerprint":%q}`, gen, strings.Repeat("a", 64)))
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

func claudeRow(t *testing.T, h *harness) authRow {
	t.Helper()
	var rep authReport
	if err := json.Unmarshal(h.stdout.Bytes(), &rep); err != nil || len(rep.Logins) != 2 {
		t.Fatalf("json: %v\n%s", err, h.stdout.String())
	}
	return rep.Logins[0]
}

func TestAuthStatusRejectedWhileLocalAccessUnexpired(t *testing.T) {
	h := rejectedHarness(t, 3, rejectionDoc(3, testNow.Add(-2*time.Minute)))
	h.mustRun(1, "auth", "status")
	out := h.stdout.String()
	for _, want := range []string{"claude  Rejected", "generation 3", "5h0m0s left", "the provider refused generation 3 2m0s ago", "next generation is pending", "no sign-in is needed unless it stays unpublished"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "claude  Ready") || strings.Contains(out+h.stderr.String(), authCanary) {
		t.Errorf("wrong state or token printed:\n%s", out)
	}
	h.mustRun(1, "auth", "status", "-o", "json")
	if row := claudeRow(t, h); row.State != "Rejected" {
		t.Errorf("json row = %+v", row)
	}
}

func TestAuthStatusRejectedClearsWhenNextGenerationIsVisible(t *testing.T) {
	// Observed 59 s ago on generation 2; the keeper's generation 3 is now mounted.
	// Generation 3 has not been observed: Ready here means "no rejection seen",
	// not "proved accepted".
	h := rejectedHarness(t, 3, rejectionDoc(2, testNow.Add(-59*time.Second)))
	h.mustRun(ExitOK, "auth", "status")
	if out := h.stdout.String(); !strings.Contains(out, "claude  Ready") || strings.Contains(out, "Rejected") || strings.Contains(out, "refused") {
		t.Errorf("stdout:\n%s", out)
	}
}

// Age never turns an unresolved same-generation rejection into Ready.
func TestAuthStatusRejectedNeverAgesIntoReady(t *testing.T) {
	for _, age := range []time.Duration{11 * time.Minute, 6 * time.Hour, 7 * time.Hour, 24 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			h := rejectedHarness(t, 3, rejectionDoc(3, testNow.Add(-age)))
			h.mustRun(1, "auth", "status")
			out := h.stdout.String()
			if !strings.Contains(out, "claude  Rejected") || !strings.Contains(out, fmt.Sprintf("%s ago", age)) || !strings.Contains(out, "check the keeper") || strings.Contains(out, "claude  Ready") {
				t.Errorf("stdout:\n%s", out)
			}
		})
	}
}

// A marker that cannot be believed is "cannot tell", never Ready.
func TestAuthStatusRejectedUnbelievableMarkerIsCannotTell(t *testing.T) {
	cases := map[string]string{
		"above the projection's generation": rejectionDoc(4, testNow.Add(-time.Minute)),
		"in the future":                     rejectionDoc(3, testNow.Add(time.Hour)),
		"garbage":                           "not json",
		"empty":                             " ",
		"unknown field":                     `{"version":1,"generation":3,"observedAt":"2026-10-06T20:59:00Z","accessToken":"` + authCanary + `"}`,
		"wrong version":                     `{"version":2,"generation":3,"observedAt":"2026-10-06T20:59:00Z"}`,
		"no generation":                     `{"version":1,"generation":0,"observedAt":"2026-10-06T20:59:00Z"}`,
		"trailing data":                     rejectionDoc(3, testNow.Add(-time.Minute)) + `{}`,
		"oversize":                          strings.Repeat(" ", 8<<10) + rejectionDoc(3, testNow.Add(-time.Minute)),
	}
	for name, marker := range cases {
		t.Run(name, func(t *testing.T) {
			h := rejectedHarness(t, 3, marker)
			h.mustRun(2, "auth", "status")
			out := h.stdout.String()
			if !strings.Contains(out, "claude  Unknown") || !strings.Contains(out, "cannot tell") || strings.Contains(out, "claude  Ready") || strings.Contains(out+h.stderr.String(), authCanary) {
				t.Errorf("stdout:\n%s", out)
			}
			h.mustRun(2, "auth", "status", "-o", "json")
			if row := claudeRow(t, h); row.State != "Unknown" {
				t.Errorf("json row = %+v", row)
			}
		})
	}
	t.Run("unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a mode 0 file")
		}
		h := rejectedHarness(t, 3, rejectionDoc(3, testNow.Add(-time.Minute)))
		path := filepath.Join(h.dir, "home/.agentd/claude-rejected.json")
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		h.mustRun(2, "auth", "status")
		if out := h.stdout.String(); !strings.Contains(out, "claude  Unknown") || !strings.Contains(out, "cannot tell") {
			t.Errorf("stdout:\n%s", out)
		}
	})
	t.Run("a directory", func(t *testing.T) {
		h := rejectedHarness(t, 3, "")
		h.vars["HOME"] = filepath.Join(h.dir, "home")
		if err := os.MkdirAll(filepath.Join(h.dir, "home/.agentd/claude-rejected.json"), 0o700); err != nil {
			t.Fatal(err)
		}
		h.mustRun(2, "auth", "status")
		if out := h.stdout.String(); !strings.Contains(out, "claude  Unknown") {
			t.Errorf("stdout:\n%s", out)
		}
	})
	t.Run("last written before the applied generation (receipt)", func(t *testing.T) {
		h := rejectedHarness(t, 3, "not json")
		old := time.Now().Add(-48 * time.Hour)
		if err := os.Chtimes(filepath.Join(h.dir, "home/.agentd/claude-rejected.json"), old, old); err != nil {
			t.Fatal(err)
		}
		h.mustRun(ExitOK, "auth", "status")
		if out := h.stdout.String(); !strings.Contains(out, "claude  Ready") {
			t.Errorf("a file older than the generation it would describe changed the row:\n%s", out)
		}
	})
	// Finding 1: a pod restart resets the Secret volume's mtimes. The projection
	// file looking newer than a corrupt marker for the current generation must
	// not turn cannot-tell into Ready.
	t.Run("corrupt marker, projection mtime newer than the marker", func(t *testing.T) {
		h := rejectedHarness(t, 3, "not json")
		future := time.Now().Add(time.Hour)
		if err := os.Chtimes(h.vars["AGENTD_CLAUDE_LIVE_FILE"], future, future); err != nil {
			t.Fatal(err)
		}
		h.mustRun(2, "auth", "status")
		if out := h.stdout.String(); !strings.Contains(out, "claude  Unknown") || !strings.Contains(out, "cannot tell") {
			t.Errorf("failed open after a Secret mtime reset:\n%s", out)
		}
	})
	t.Run("corrupt marker, newer generation published but not yet applied", func(t *testing.T) {
		h := rejectedHarness(t, 4, "not json") // projection 4, receipt written for 4 by the helper
		writeReceipt(t, h, "home/.agentd", 3, time.Now().Add(-time.Hour))
		h.mustRun(ExitOK, "auth", "status")
		if out := h.stdout.String(); !strings.Contains(out, "claude  Ready") {
			t.Errorf("a marker agentd could only have written for generation 3 changed generation 4:\n%s", out)
		}
	})
	t.Run("marker with no receipt", func(t *testing.T) {
		h := rejectedHarness(t, 3, rejectionDoc(3, testNow.Add(-time.Minute)))
		if err := os.Remove(filepath.Join(h.dir, "home/.agentd/claude-access-generation.json")); err != nil {
			t.Fatal(err)
		}
		h.mustRun(2, "auth", "status")
		if out := h.stdout.String(); !strings.Contains(out, "claude  Unknown") {
			t.Errorf("stdout:\n%s", out)
		}
	})
	t.Run("valid marker, corrupt receipt", func(t *testing.T) {
		h := rejectedHarness(t, 3, rejectionDoc(3, testNow.Add(-time.Minute)))
		h.write("home/.agentd/claude-access-generation.json", "not json")
		h.mustRun(2, "auth", "status")
		if out := h.stdout.String(); !strings.Contains(out, "claude  Unknown") {
			t.Errorf("stdout:\n%s", out)
		}
	})
	// Finding 1: duplicate members are refused, as agentd's strict decoder does.
	t.Run("duplicate members", func(t *testing.T) {
		for name, doc := range map[string]string{
			"generation twice": `{"version":1,"generation":3,"generation":2,"observedAt":"2026-10-06T20:59:00Z"}`,
			"version twice":    `{"version":1,"version":1,"generation":3,"observedAt":"2026-10-06T20:59:00Z"}`,
			"observedAt twice": `{"version":1,"generation":3,"observedAt":"2026-10-06T20:59:00Z","observedAt":"2026-10-06T20:58:00Z"}`,
		} {
			h := rejectedHarness(t, 3, doc)
			h.mustRun(2, "auth", "status")
			if out := h.stdout.String(); !strings.Contains(out, "claude  Unknown") || strings.Contains(out, "claude  Ready") {
				t.Errorf("%s:\n%s", name, out)
			}
		}
	})
}

func TestAuthStatusRejectedPastThePendingWindowSaysCheckTheKeeper(t *testing.T) {
	h := rejectedHarness(t, 3, rejectionDoc(3, testNow.Add(-11*time.Minute)))
	h.mustRun(1, "auth", "status")
	out := h.stdout.String()
	if !strings.Contains(out, "claude  Rejected") || !strings.Contains(out, "no newer generation") || !strings.Contains(out, "check the keeper") {
		t.Errorf("stdout:\n%s", out)
	}
	if strings.Contains(out, "NeedsLogin") {
		t.Errorf("a keeper delay must not read as a login to renew:\n%s", out)
	}
}

func TestAuthStatusRejectedFileFlagAndNoHome(t *testing.T) {
	h := rejectedHarness(t, 3, "")
	path := h.write("m/claude-rejected.json", rejectionDoc(3, testNow.Add(-time.Minute)))
	writeReceipt(t, h, "m", 3, time.Now().Add(-time.Hour))
	h.mustRun(ExitOK, "auth", "status") // no HOME, no flag: not read
	if strings.Contains(h.stdout.String(), "Rejected") {
		t.Errorf("read a marker nobody pointed it at:\n%s", h.stdout.String())
	}
	h.mustRun(1, "auth", "status", "--rejected-file", path)
	if !strings.Contains(h.stdout.String(), "claude  Rejected") {
		t.Errorf("stdout:\n%s", h.stdout.String())
	}
}

func TestAuthStatusRejectedDoesNotMaskExpiryOrLapse(t *testing.T) {
	h := newHarness(t)
	hermeticAuth(t, h)
	h.vars["AGENTD_CLAUDE_LIVE_FILE"] = h.write("claude.json", authClaudeDoc(3, testNow.Add(-time.Minute)))
	h.vars["HOME"] = filepath.Join(h.dir, "home")
	h.write("home/.agentd/claude-rejected.json", rejectionDoc(3, testNow.Add(-time.Minute)))
	h.mustRun(1, "auth", "status")
	if out := h.stdout.String(); !strings.Contains(out, "claude  NeedsLogin") {
		t.Errorf("an expired access token must stay NeedsLogin:\n%s", out)
	}
}

func TestAuthStatusRejectedIsReadOnlyAndLocal(t *testing.T) {
	h := rejectedHarness(t, 3, rejectionDoc(3, testNow.Add(-time.Minute)))
	path := filepath.Join(h.dir, "home/.agentd/claude-rejected.json")
	before := statFile(t, path)
	h.mustRun(1, "auth", "status")
	if after := statFile(t, path); after != before {
		t.Errorf("auth status changed the marker: %s -> %s", before, after)
	}
	if h.api.requests() != nil {
		t.Error("auth status called the API")
	}
}

func statFile(t *testing.T, path string) string {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%d %d %s", fi.Size(), fi.Mode(), fi.ModTime().Format(time.RFC3339Nano))
}
