package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestProductStatePreviews(t *testing.T) {
	dir := t.TempDir()
	if err := writeProductStates(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sessions-allowance-seen-varies", "sessions-allowance-measured", "sessions-allowance-stale", "sessions-allowance-unknown", "usage-allowance", "usage-allowance-stale", "usage-allowance-unknown", "usage-allowance-not-connected", "usage", "usage-empty", "usage-timed-out", "sessions-not-connected", "sessions-credential-unavailable", "sessions-unreachable", "sessions-timed-out", "sessions-refused", "sessions-contract-refused", "sessions-clock-ahead", "sessions-cancelled", "sessions-read-not-started", "sessions-empty", "sessions-stale", "sessions-current", "tasks-empty", "tasks-unavailable", "tasks-unknown", "tasks-not-connected"} {
		body, err := os.ReadFile(filepath.Join(dir, name+".html"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, "Fixture preview") || strings.Contains(text, "<script") {
			t.Errorf("%s lacks fixture label or contains script", name)
		}
		footer := regexp.MustCompile(`<footer class="statusbar">(.*?)</footer>`).FindString(text)
		success := strings.Contains(name, "allowance") || name == "usage" || name == "usage-empty" || name == "sessions-empty" || name == "sessions-stale" || name == "sessions-current" || name == "tasks-empty"
		const updated = `Updated <time datetime="2026-10-29T13:59:00Z">29 Oct 2026, 13:59 UTC</time>`
		if success {
			if !strings.Contains(footer, updated) {
				t.Errorf("%s lacks synthetic successful read completion: %s", name, footer)
			}
			if (strings.HasPrefix(name, "sessions-") || strings.HasPrefix(name, "tasks-")) && !strings.Contains(text, `<p class="list-foot">`+updated+`</p>`) {
				t.Errorf("%s list foot differs from successful read completion", name)
			}
		} else if strings.Contains(footer, "Updated") || strings.Contains(text, `<p class="list-foot">`) {
			t.Errorf("%s failure preview claims successful read freshness", name)
		}
		if strings.HasPrefix(name, "usage") && (strings.Contains(text, `class="rail"`) || strings.Contains(text, `class="list"`) || strings.Count(text, `?view=usage" aria-current="page"`) != 2) {
			t.Errorf("%s has the session browser or wrong current nav", name)
		}
		if strings.HasPrefix(name, "usage") && (strings.Count(text, "<h1") != 1 || !strings.Contains(text, `<h1 id="usage-title">Usage</h1>`)) {
			t.Errorf("%s lacks its single Usage h1", name)
		}
		if strings.Contains(text, ">Retry</a>") && strings.Count(text, ">Retry</a>") != 1 {
			t.Errorf("%s repeats its failure recovery block", name)
		}
		if name == "sessions-timed-out" && !strings.Contains(text, `<form class="launch"`) {
			t.Error("timeout preview lost the independent New agent form")
		}
		if name == "usage" && !strings.Contains(text, `?session=uid-synthetic-agent">synthetic-agent</a>`) {
			t.Error("Usage preview does not select its session by UID")
		}
		if name == "usage-empty" && (strings.Count(text, "No sessions in this read.") != 1 || strings.Count(text, "<dd>0</dd>") != 2 || strings.Contains(text, "<table>")) {
			t.Error("empty Usage preview lost zeros or has empty tables")
		}
		if strings.Contains(name, "timed-out") && (!strings.Contains(text, "timed out") || !strings.Contains(text, ">Retry</a>")) {
			t.Errorf("%s lacks timeout recovery", name)
		}
		if strings.Contains(name, "contract-refused") && !strings.Contains(text, "Page 2, row index 1") {
			t.Error("contract preview lost refusal position")
		}
	}
}

func TestAttentionStatePreviews(t *testing.T) {
	dir := t.TempDir()
	if err := writeProductStates(dir); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, reason, action string }{
		{"waiting-linked", "Waiting for your input", "Answer in Claude"},
		{"waiting-no-link", "Waiting for your input", "Remote Control link not reported"},
		{"waiting-not-reported", "Waiting for your input", "Remote Control state was not reported: answer from the session&#39;s terminal on the workbench."},
		{"waiting-state-not-reported", "Waiting for your input", "Remote Control state was not reported: answer from the session&#39;s terminal on the workbench."},
		{"waiting-unknown-state", "Waiting for your input", "Remote Control state future-state: answer from the session&#39;s terminal on the workbench."},
		{"waiting-login-required", "Waiting for your input", "Remote Control needs a login: answer from the session&#39;s terminal on the workbench."},
		{"waiting-unpaired", "Waiting for your input", "Remote Control is not paired: answer from the session&#39;s terminal on the workbench."},
		{"waiting-offline", "Waiting for your input", "Remote Control is offline: answer from the session&#39;s terminal on the workbench."},
		{"waiting-pairing", "Waiting for your input", "A pairing code was issued but pairing is not confirmed: answer from the session&#39;s terminal on the workbench."},
		{"waiting-errored", "Waiting for your input", "Remote Control reported an error: answer from the session&#39;s terminal on the workbench."},
		{"waiting-starting", "Waiting for your input", "Remote Control is starting: answer from the session&#39;s terminal on the workbench."},
		{"waiting-local", "Waiting for your input", "Answer in the session&#39;s terminal on the workbench."},
		{"task-failed", "Task failed", "its log is kept on the shared volume at logs/synthetic-attention.log"},
		{"task-timed-out", "Task timed out", "its log is kept on the shared volume at logs/synthetic-attention.log"},
		{"pod-failed", "Session pod failed", "agent-run show synthetic-attention"},
		{"agent-failed", "Agent stopped unexpectedly", "agent-run show synthetic-attention"},
		{"agent-interrupted", "Agent stopped unexpectedly", "agent-run show synthetic-attention"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(dir, "attention-"+tc.name+".html"))
			if err != nil {
				t.Fatal(err)
			}
			s := string(body)
			for _, want := range []string{"Fixture preview", `class="attention-panel"`, tc.reason, tc.action, "The portal cannot show this session&#39;s conversation."} {
				if !strings.Contains(s, want) {
					t.Errorf("missing %s", want)
				}
			}
			if strings.Count(s, "<h1") != 1 || strings.Contains(s, "<script") || strings.Contains(s, "<form") {
				t.Fatal("unsafe preview or duplicate heading")
			}
			if strings.HasPrefix(tc.name, "waiting") && !strings.Contains(s, "Waiting for you") {
				t.Fatal("waiting subtitle missing")
			}
			if tc.name == "waiting-linked" && (strings.Count(s, `href="https://claude.ai/code/session_fixture-247"`) != 1 || strings.Contains(s, ">Open in Claude<svg")) {
				t.Fatal("Answer link is missing or the header duplicates it")
			}
			lastReported := tc.name == "pod-failed" || tc.name == "agent-failed" || tc.name == "agent-interrupted"
			if strings.Contains(s, "Last reported 1h ago · 29 Oct 2026, 13:00 UTC") != lastReported {
				t.Fatal("failure observation lacks its Last reported label, or task end was relabeled")
			}
			savedLog := tc.name == "task-failed" || tc.name == "task-timed-out" || tc.name == "pod-failed"
			if savedLog && strings.Contains(s, "agent-run log synthetic-attention") {
				t.Fatal("non-running session offers a live log")
			}
			if !strings.HasPrefix(tc.name, "waiting") && !strings.Contains(s, "1h ago · 29 Oct 2026, 13:00 UTC") {
				t.Fatal("failure time missing")
			}
		})
	}
}

func TestTaskFailureStatePreviews(t *testing.T) {
	dir := t.TempDir()
	if err := writeProductStates(dir); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, reason string }{
		{"not-connected", "The Tasks source is not connected."},
		{"credential-unavailable", "The Tasks credential is unavailable."},
		{"unreachable", "The Tasks source could not be reached."},
		{"timed-out", "The Tasks read timed out."},
		{"refused", "The Tasks source refused the read (HTTP 403)."},
		{"non-200", "The Tasks source refused the read (HTTP 503)."},
		{"media-or-size", "The returned task data could not be read."},
		{"contract-refused", "Page 2, row index 1"},
		{"clock-ahead", "A Tasks timestamp is more than 2 seconds ahead"},
		{"cancelled", "The Tasks read was cancelled."},
		{"unavailable", "reason is Unknown"},
	} {
		for _, attention := range []bool{false, true} {
			name, href := "tasks-"+tc.name, "/portal/projects/dev-env/environments/internal/tasks"
			if attention {
				name += "-attention"
				href += "?show=attention"
			}
			body, err := os.ReadFile(filepath.Join(dir, name+".html"))
			if err != nil {
				t.Fatal(err)
			}
			s := string(body)
			for _, want := range []string{"Fixture preview", tc.reason, "Observed:", `datetime="2026-10-29T14:00:00Z"`, "Last successful read in this process:", `href="` + href + `">Retry</a>`} {
				if !strings.Contains(s, want) {
					t.Errorf("%s lacks %s", name, want)
				}
			}
			if strings.Count(s, `class="read-failure"`) != 1 || strings.Count(s, ">Retry</a>") != 1 || strings.Contains(s, "Updated <time") || strings.Contains(s, "SECRET-") || strings.Contains(s, "<form") {
				t.Errorf("%s has unsafe failure markup", name)
			}
			if tc.name == "not-connected" || tc.name == "unavailable" {
				if !strings.Contains(s, "Last successful read in this process: Unknown") {
					t.Errorf("%s lacks Unknown last success", name)
				}
			} else if !strings.Contains(s, `datetime="2026-10-29T13:59:00Z"`) {
				t.Errorf("%s lacks last success", name)
			}
		}
	}
}

func TestAllowancePreviewsUseMappedStates(t *testing.T) {
	dir := t.TempDir()
	if err := writeProductStates(dir); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                                  string
		percent, stale, unknown, disconnected bool
	}{
		{"sessions-allowance-measured", true, false, false, false},
		{"sessions-allowance-stale", true, true, false, false},
		{"sessions-allowance-unknown", false, false, true, false},
		{"usage-allowance", true, false, false, false},
		{"usage-allowance-stale", true, true, false, false},
		{"usage-allowance-unknown", false, false, true, false},
		{"usage-allowance-not-connected", false, false, false, true},
		{"usage", false, false, false, true},
		{"usage-empty", false, false, false, true},
	} {
		body, err := os.ReadFile(filepath.Join(dir, tc.name+".html"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		pattern := `(?s)<div class="allowance-strip">.*?</ul></details></div>`
		if strings.HasPrefix(tc.name, "usage") {
			pattern = `(?s)<section class="allowance".*?Plan limits.*?</section>`
		}
		block := regexp.MustCompile(pattern).FindString(text)
		if block == "" {
			t.Fatalf("%s: no allowance", tc.name)
		}
		if strings.Contains(block, "62% left") != tc.percent || strings.Contains(block, "Stale") != tc.stale || strings.Contains(block, "Account binding unverified") != tc.unknown || strings.Contains(block, "Not connected") != tc.disconnected {
			t.Errorf("%s: wrong states %s", tc.name, block)
		}
		for _, ref := range []string{"anthropic-primary-window", "openai-primary-window", "anthropic-secondary-window", "openai-secondary-window"} {
			if strings.Contains(text, ref) {
				t.Errorf("%s displays opaque reference %q", tc.name, ref)
			}
		}
		if tc.percent && (!strings.Contains(block, "7 days") || !strings.Contains(block, "5 hours")) {
			t.Errorf("%s lacks duration labels", tc.name)
		}
		if tc.percent && strings.HasPrefix(tc.name, "sessions") && !strings.Contains(block, ">+1 more</a>") {
			t.Errorf("%s lacks window overflow", tc.name)
		}
		if tc.percent && strings.HasPrefix(tc.name, "usage") && strings.Count(block, `class="allowance-window"`) != 6 {
			t.Errorf("%s lacks full windows", tc.name)
		}
	}
}

func TestAllowancePreviewStatesAreUniformAndSeenVariesIsExplicit(t *testing.T) {
	dir := t.TempDir()
	if err := writeProductStates(dir); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"measured", "stale", "seen-varies"} {
		allowances, err := allowancePreview(state)
		if err != nil {
			t.Fatal(err)
		}
		for _, allowance := range allowances {
			want := "Measured"
			if state == "stale" {
				want = "Stale"
			}
			if string(allowance.Value) != want {
				t.Errorf("%s provider %s: %s, want %s", state, allowance.Provider, allowance.Value, want)
			}
		}
		body, err := os.ReadFile(filepath.Join(dir, "sessions-allowance-"+state+".html"))
		if err != nil {
			t.Fatal(err)
		}
		block := regexp.MustCompile(`(?s)<div class="allowance-strip">.*?</ul></details></div>`).FindString(string(body))
		if block == "" {
			t.Fatalf("%s: missing strip", state)
		}
		wantVaries, wantStale := 0, 0
		if state == "seen-varies" {
			wantVaries = 2
		}
		if state == "stale" {
			wantStale = 2
		}
		if strings.Count(block, "Seen varies · see Usage") != wantVaries || strings.Count(block, `>Stale</span>`) != wantStale {
			t.Errorf("%s: wrong per-provider seen or stale states: %s", state, block)
		}
	}
}
