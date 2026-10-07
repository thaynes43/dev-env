package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const repoPath = "/home/dev/repos/haynes-ops"

// goodReport is agentd's report of a rescue that bundled a rescue branch and a
// stash entry, with the agent stopped.
func goodReport(session string) protocol.RescueReport {
	dir := protocol.RescueDir(session, "20261006-1730")
	return protocol.RescueReport{
		Session: session, Stamp: "20261006-1730", OK: true,
		Agent: &protocol.AgentStop{WasRunning: true},
		Repos: []protocol.RepoRescue{{
			Path: repoPath, Fetched: true,
			Worktrees: []protocol.WorktreeRescue{{Path: "/home/dev/work/" + session, Dirty: true, RescueBranch: "rescue/x-20261006-1730"}},
			UnpushedRefs: []protocol.Ref{
				{Name: "refs/heads/rescue/x-20261006-1730", Commit: "aaaa"},
				{Name: "stash@{0}", Commit: "bbbb"},
			},
			Bundle: &protocol.RepoBundle{File: dir + "/haynes-ops.bundle", Verified: true, SHA256: "ff", Size: 10, Base: "refs/remotes/origin/HEAD",
				Refs: []protocol.BundleRef{
					{Name: "refs/heads/rescue/x-20261006-1730", Source: "refs/heads/rescue/x-20261006-1730", Commit: "aaaa"},
					{Name: protocol.StashRefPrefix + "0", Source: "stash@{0}", Commit: "bbbb"},
				}},
		}},
		Bundle: &protocol.BundleReport{Dir: dir, Manifest: dir + "/" + protocol.ManifestFile},
	}
}

func cleanReport(session string) protocol.RescueReport {
	return protocol.RescueReport{
		Session: session, Stamp: "20261006-1730", OK: true, CleanAndPushed: true,
		Agent: &protocol.AgentStop{},
		Repos: []protocol.RepoRescue{{Path: repoPath, Fetched: true, Worktrees: []protocol.WorktreeRescue{{Path: "/home/dev/work/" + session}}}},
	}
}

func verdictFor(rep protocol.RescueReport) (*v1alpha1.RescueStatus, string) {
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "s1", Generation: 4}, Spec: v1alpha1.AgentSessionSpec{Repo: "haynes-ops"}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-1"}}
	return verdict(s, pod, rep, metav1.NewTime(time.Date(2026, 10, 6, 17, 31, 0, 0, time.UTC)))
}

func TestVerdictVerified(t *testing.T) {
	rec, reason := verdictFor(goodReport("s1"))
	if rec.Result != v1alpha1.RescueVerified || reason != "Verified" || rec.PodUID != "pod-1" || rec.Generation != 4 || rec.Superseded {
		t.Fatalf("record %+v, reason %s", rec, reason)
	}
	if rec.LastBundle != "rescue/s1/20261006-1730/manifest.json" || rec.Stamp != "20261006-1730" || rec.At == nil {
		t.Errorf("record %+v", rec)
	}
	if len(rec.UnpushedRefs) != 2 || rec.UnpushedRefs[1] != (v1alpha1.RescuedRef{Repo: repoPath, Name: "stash@{0}", Commit: "bbbb", Bundle: "rescue/s1/20261006-1730/haynes-ops.bundle"}) {
		t.Errorf("refs %+v", rec.UnpushedRefs)
	}
	if !strings.Contains(rec.Message, "bundled 2 refs") || !strings.Contains(rec.Message, "the agent was stopped") {
		t.Errorf("message %q", rec.Message)
	}
}

func TestVerdictCleanAndPushed(t *testing.T) {
	rec, reason := verdictFor(cleanReport("s1"))
	if rec.Result != v1alpha1.RescueCleanAndPushed || reason != "CleanAndPushed" || rec.LastBundle != "" {
		t.Fatalf("record %+v, reason %s", rec, reason)
	}
}

// Every way a report can fall short is Failed, with a reason that says which.
func TestVerdictFailures(t *testing.T) {
	cases := []struct {
		name, reason, msg string
		edit              func(*protocol.RescueReport)
	}{
		{"another session", "ReportMismatch", `for session "s2"`, func(r *protocol.RescueReport) { r.Session = "s2" }},
		{"agent not stopped", "AgentNotStopped", "does not say the agent was stopped", func(r *protocol.RescueReport) { r.Agent = nil }},
		{"agent still running", "AgentStillRunning", "still ran after the stop", func(r *protocol.RescueReport) { r.Agent.Running = true }},
		{"refused worktree", "WorktreeRefused", "merge or rebase", func(r *protocol.RescueReport) {
			r.OK = false
			r.Repos[0].Worktrees[0].Refused = "a merge or rebase is in progress (MERGE_HEAD)"
		}},
		{"fetch failed", "FetchFailed", "could not be fetched", func(r *protocol.RescueReport) {
			r.Repos[0].Fetched, r.Repos[0].FetchError = false, "git fetch: exit status 128"
		}},
		{"repo error", "RepoError", "worktree list", func(r *protocol.RescueReport) {
			r.OK = false
			r.Repos[0].Error = "worktree list: exit status 128"
		}},
		{"bundle failed", "BundleFailed", "git bundle create", func(r *protocol.RescueReport) {
			r.OK = false
			r.Repos[0].Bundle.Verified, r.Repos[0].Bundle.Error = false, "git bundle create: exit status 128"
		}},
		{"bundle not verified", "BundleFailed", "not verified", func(r *protocol.RescueReport) { r.Repos[0].Bundle.Verified = false }},
		{"a ref outside the bundle", "BundleFailed", "stash@{0} (bbbb) is in no verified bundle", func(r *protocol.RescueReport) {
			r.Repos[0].Bundle.Refs = r.Repos[0].Bundle.Refs[:1]
		}},
		{"a ref at another commit", "BundleFailed", "is in no verified bundle", func(r *protocol.RescueReport) {
			r.Repos[0].Bundle.Refs[0].Commit = "cccc"
		}},
		{"no bundle at all", "BundleFailed", "in no verified bundle", func(r *protocol.RescueReport) {
			r.Repos[0].Bundle, r.Bundle = nil, nil
		}},
		{"no manifest", "BundleFailed", "manifest: write failed", func(r *protocol.RescueReport) {
			r.OK = false
			r.Bundle.Manifest, r.Bundle.Error = "", "manifest: write failed"
		}},
		{"not ok for no named reason", "RescueFailed", "not ok", func(r *protocol.RescueReport) { r.OK = false }},
		{"no clones at all", "NotProven", "clone ~/repos/haynes-ops is not in the report", func(r *protocol.RescueReport) {
			*r = cleanReport("s1")
			r.Repos = nil
		}},
		{"only another clone", "NotProven", "clone ~/repos/haynes-ops is not in the report", func(r *protocol.RescueReport) {
			*r = cleanReport("s1")
			r.Repos[0].Path = "/home/dev/repos/other"
		}},
		{"nothing to bundle, nothing proven", "NotProven", "did not prove", func(r *protocol.RescueReport) {
			*r = cleanReport("s1")
			r.CleanAndPushed = false
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := goodReport("s1")
			tc.edit(&rep)
			rec, reason := verdictFor(rep)
			if rec.Result != v1alpha1.RescueFailed || reason != tc.reason || !strings.Contains(rec.Message, tc.msg) {
				t.Errorf("result %s, reason %s, message %q; want Failed, %s, %q", rec.Result, reason, rec.Message, tc.reason, tc.msg)
			}
			if rec.PodUID != "pod-1" || rec.Generation != 4 {
				t.Errorf("record %+v", rec)
			}
		})
	}
}

// A clean rescue keeps the newest bundle's path from an earlier rescue, for a
// restore; status keeps at most 256 refs and counts the rest.
func TestVerdictKeepsTheLastBundleAndCapsTheRefs(t *testing.T) {
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "s1"}, Spec: v1alpha1.AgentSessionSpec{Repo: "haynes-ops"}}
	s.Status.Rescue = &v1alpha1.RescueStatus{LastBundle: "rescue/s1/20261005-0900/manifest.json"}
	rec, _ := verdict(s, &corev1.Pod{}, cleanReport("s1"), metav1.Now())
	if rec.LastBundle != "rescue/s1/20261005-0900/manifest.json" {
		t.Errorf("last bundle %q", rec.LastBundle)
	}
	rep := goodReport("s1")
	repo := &rep.Repos[0]
	repo.UnpushedRefs, repo.Bundle.Refs = nil, nil
	for i := range 300 {
		name, sha := fmt.Sprintf("refs/tags/t%d", i), fmt.Sprintf("%040x", i)
		repo.UnpushedRefs = append(repo.UnpushedRefs, protocol.Ref{Name: name, Commit: sha})
		repo.Bundle.Refs = append(repo.Bundle.Refs, protocol.BundleRef{Name: name, Source: name, Commit: sha})
	}
	rec, _ = verdict(s, &corev1.Pod{}, rep, metav1.Now())
	if rec.Result != v1alpha1.RescueVerified || len(rec.UnpushedRefs) != 256 || rec.OmittedRefs != 44 || !strings.Contains(rec.Message, "bundled 300 refs") {
		t.Errorf("result %s, %d refs, %d omitted, %q", rec.Result, len(rec.UnpushedRefs), rec.OmittedRefs, rec.Message)
	}
}

func TestParseRescueOutput(t *testing.T) {
	good, err := json.Marshal(goodReport("s1"))
	if err != nil {
		t.Fatal(err)
	}
	buf := func(b []byte) *cappedBuffer {
		c := &cappedBuffer{max: 1 << 20}
		_, _ = c.Write(b)
		return c
	}
	if rep, err := parseRescueOutput(buf(good), "", nil); err != nil || rep.Session != "s1" {
		t.Errorf("exit 0: %+v %v", rep, err)
	}
	// agentd exits 1 when the rescue is not OK, and prints the report.
	if rep, err := parseRescueOutput(buf(good), "", utilexec.CodeExitError{Err: errors.New("exit 1"), Code: 1}); err != nil || rep.Session != "s1" {
		t.Errorf("exit 1: %+v %v", rep, err)
	}
	for name, tc := range map[string]struct {
		out    *cappedBuffer
		stderr string
		err    error
		want   string
	}{
		"usage error":      {buf(nil), "agentd: usage: ctl status | ctl rescue [--stop-agent]", utilexec.CodeExitError{Err: errors.New("exit 2"), Code: 2}, "usage"},
		"exit 2 with json": {buf(good), "", utilexec.CodeExitError{Err: errors.New("exit 2"), Code: 2}, "exit 2"},
		"no connection":    {buf(nil), "", errors.New("container not found"), "container not found"},
		"garbage":          {buf([]byte("not json")), "", nil, "unreadable rescue report"},
		"no session":       {buf([]byte(`{"stamp":"x"}`)), "", nil, "names no session"},
		"too long":         {&cappedBuffer{max: 4, err: errors.New("more than 4 bytes")}, "", nil, "more than 4 bytes"},
	} {
		if _, err := parseRescueOutput(tc.out, tc.stderr, tc.err); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	c := &cappedBuffer{max: 4}
	if n, err := c.Write([]byte("12345")); n != 5 || err != nil || c.err == nil || c.Len() != 0 {
		t.Errorf("overflow: %d %v %v %d", n, err, c.err, c.Len())
	}
}

// The command matches agentd's CLI (cmd/agentd: ctl rescue [--stop-agent]).
func TestRescueCommand(t *testing.T) {
	if strings.Join(RescueCommand, " ") != "agentd ctl rescue --stop-agent" {
		t.Errorf("command %q", RescueCommand)
	}
}
