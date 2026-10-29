package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductStatePreviews(t *testing.T) {
	dir := t.TempDir()
	if err := writeProductStates(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"usage", "usage-empty", "usage-timed-out", "sessions-not-connected", "sessions-credential-unavailable", "sessions-unreachable", "sessions-timed-out", "sessions-refused", "sessions-contract-refused", "sessions-clock-ahead", "sessions-cancelled", "sessions-read-not-started", "sessions-empty", "sessions-stale", "sessions-current"} {
		body, err := os.ReadFile(filepath.Join(dir, name+".html"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, "Fixture preview") || strings.Contains(text, "<script") {
			t.Errorf("%s lacks fixture label or contains script", name)
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
		{"waiting-login-required", "Waiting for your input", "Remote Control is login required: answer from the session&#39;s terminal on the workbench."},
		{"waiting-unpaired", "Waiting for your input", "Remote Control is not paired: answer from the session&#39;s terminal on the workbench."},
		{"waiting-offline", "Waiting for your input", "Remote Control is offline: answer from the session&#39;s terminal on the workbench."},
		{"waiting-pairing", "Waiting for your input", "Remote Control is pairing code issued (pairing not confirmed): answer from the session&#39;s terminal on the workbench."},
		{"waiting-errored", "Waiting for your input", "Remote Control is error: answer from the session&#39;s terminal on the workbench."},
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
