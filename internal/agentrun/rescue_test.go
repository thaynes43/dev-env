package agentrun

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const rescueSession = "haynes-ops-1006-210000"

func rescueAt(stamp string, repos ...apiv1.RescueRepo) apiv1.Rescue {
	return apiv1.Rescue{
		ID: rescueSession + "/" + stamp, Session: rescueSession,
		CreatedAt: time.Date(2026, 10, 6, 21, 4, 59, 0, time.UTC),
		Finished:  true, Complete: true, Repos: repos, Bytes: 3_500_000,
	}
}

func repoWith(repo string, refs ...string) apiv1.RescueRepo {
	return apiv1.RescueRepo{Repo: repo, Refs: refs, Bytes: 1000}
}

// serveRescues answers GET /v1/rescues with list, and a create like serveCreate.
func serveRescues(h *harness, list apiv1.RescueList) {
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == apiv1.RescuesPath:
			writeTestJSON(w, http.StatusOK, list)
		case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionsPath:
			req := decodeCreate(h.t, body)
			s := runningOn(testSession(name, ""), "talosw02")
			s.Repo, s.Model, s.Effort, s.Size = req.Repo, req.Model, req.Effort, req.Size
			writeTestJSON(w, http.StatusCreated, s)
		default:
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route")
		}
	}
}

func TestRescueListTable(t *testing.T) {
	h := newHarness(t)
	prune := time.Date(2026, 11, 5, 3, 0, 0, 0, time.UTC)
	live := rescueAt("20261006-2104", repoWith("haynes-ops", "refs/heads/main"), repoWith("dev-env"))
	live.SessionExists = true
	live.Bytes = 12 * 1024
	gone := rescueAt("20261001-0000")
	gone.Finished, gone.Complete, gone.Bytes, gone.PruneAfter = true, false, 0, &prune
	unfinished := rescueAt("20260930-0000", repoWith("haynes-ops"))
	unfinished.Finished, unfinished.Complete = false, false
	bad := rescueAt("20260929-0000")
	bad.Error = "manifest.json: unexpected end of JSON input, truncated at byte 12"
	serveRescues(h, apiv1.RescueList{Rescues: []apiv1.Rescue{live, gone, unfinished, bad}, Unrecognized: []string{"rescue/odd/file", "rescue/x"}})

	h.mustRun(ExitOK, "rescue", "list")
	contains(t, "stdout", h.stdout.String(),
		"ID", "CREATED", "REPOS", "SIZE", "STATE", "KEPT",
		rescueSession+"/20261006-2104  2026-10-06 21:04  haynes-ops,dev-env  12K   complete  ",
		"session exists",
		"incomplete", "until 2026-11-05",
		"unfinished",
		"error: manifest.json: unexpected end of JSON in",
		"unrecognized (never pruned): rescue/odd/file",
		"unrecognized (never pruned): rescue/x",
	)
	if strings.Contains(h.stdout.String(), "truncated at") {
		t.Errorf("the error was not cut to 40 characters:\n%s", h.stdout.String())
	}
	if !strings.Contains(h.stdout.String(), "  -\n") && !strings.Contains(h.stdout.String(), "  -  ") {
		t.Errorf("no dash for empty repos or kept:\n%s", h.stdout.String())
	}
	reqs := h.api.requests()
	if len(reqs) != 1 || len(reqs[0].query) != 0 {
		t.Errorf("requests %+v, want one GET with no query", reqs)
	}
}

func TestRescueListEmptyJSONAndSessionFilter(t *testing.T) {
	h := newHarness(t)
	serveRescues(h, apiv1.RescueList{})
	h.mustRun(ExitOK, "rescue", "list", "--session", "a b")
	if h.stdout.String() != "no rescues\n" {
		t.Errorf("stdout %q", h.stdout.String())
	}
	if q := h.api.requests()[0].query["session"]; len(q) != 1 || q[0] != "a b" {
		t.Errorf("session query %v", q)
	}

	h = newHarness(t)
	serveRescues(h, apiv1.RescueList{Rescues: []apiv1.Rescue{rescueAt("20261006-2104", repoWith("haynes-ops"))}})
	h.mustRun(ExitOK, "rescue", "list", "-o", "json")
	contains(t, "stdout", h.stdout.String(), "{\n  \"rescues\": [", `"id": "`+rescueSession+`/20261006-2104"`)
	h.mustRun(ExitUsage, "rescue", "list", "extra")
	h.mustRun(ExitUsage, "rescue", "list", "-o", "name")
}

func TestRescueListRetriesAnUnavailableAPI(t *testing.T) {
	h := newHarness(t)
	calls := 0
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		calls++
		if calls == 1 {
			apiError(w, http.StatusServiceUnavailable, apiv1.CodeUnavailable, "no shelf")
			return
		}
		writeTestJSON(w, http.StatusOK, apiv1.RescueList{})
	}
	h.mustRun(ExitOK, "rescue", "list")
	if calls != 2 {
		t.Errorf("calls %d, want a retry", calls)
	}
}

func TestRescueUsage(t *testing.T) {
	h := newHarness(t)
	h.mustRun(ExitUsage, "rescue")
	contains(t, "stderr", h.stderr.String(), "agent-run rescue list", "agent-run rescue restore")
	h.stderr.Reset()
	h.mustRun(ExitUsage, "rescue", "frob")
	contains(t, "stderr", h.stderr.String(), "agent-run rescue restore", `unknown rescue command "frob"`)
	h.stdout.Reset()
	h.mustRun(ExitOK, "rescue", "-h")
	contains(t, "stdout", h.stdout.String(), "rescue restore starts a new session")
	h.stdout.Reset()
	h.mustRun(ExitOK, "help", "rescue")
	contains(t, "stdout", h.stdout.String(), "rescue list shows them")
	h.stdout.Reset()
	h.mustRun(ExitOK, "help")
	contains(t, "stdout", h.stdout.String(), "agent-run rescue list", "agent-run rescue restore")
	if n := len(h.api.requests()); n != 0 {
		t.Errorf("sent %d requests", n)
	}
}

func TestRescueRestoreDefaultsTheRepoAndHints(t *testing.T) {
	h := newHarness(t)
	id := rescueSession + "/20261006-2104"
	serveRescues(h, apiv1.RescueList{Rescues: []apiv1.Rescue{
		rescueAt("20261001-0000", repoWith("other")),
		rescueAt("20261006-2104", repoWith("haynes-ops",
			"refs/heads/main", "refs/heads/agent/"+rescueSession, "refs/heads/rescue/"+rescueSession+"-0123")),
	}})
	h.mustRun(ExitOK, "rescue", "restore", id, "--local", "--wait", "0")

	reqs := h.api.requests()
	if len(reqs) != 2 || reqs[0].method != http.MethodGet || reqs[0].query["session"][0] != rescueSession || reqs[1].method != http.MethodPost {
		t.Fatalf("requests %+v, want the rescue lookup, then the create", reqs)
	}
	got := decodeCreate(t, reqs[1].body)
	if got.Restore != id || got.Repo != "haynes-ops" || got.Mode != "local" || got.Base != "" {
		t.Errorf("create body %+v", got)
	}
	contains(t, "stdout", h.stdout.String(),
		"created "+name,
		"Its clone fetches rescue "+id+" into refs/rescued/ on its first boot.",
		"  uncommitted work: refs/rescued/heads/rescue/"+rescueSession+"-0123 (look and copy from it; never push a rescue branch, D-10)",
		"  the worktree starts at refs/rescued/heads/agent/"+rescueSession,
	)
}

func TestRescueRestoreWithABaseOrNoBranchPrintsNoBaseLine(t *testing.T) {
	id := rescueSession + "/20261006-2104"
	h := newHarness(t)
	serveRescues(h, apiv1.RescueList{Rescues: []apiv1.Rescue{rescueAt("20261006-2104", repoWith("haynes-ops", "refs/heads/agent/"+rescueSession))}})
	h.mustRun(ExitOK, "rescue", "restore", id, "-p", "continue", "--base", "origin/main", "--wait", "0")
	if got := decodeCreate(t, h.api.requests()[1].body); got.Base != "origin/main" || got.Mode != "task" || got.Prompt != "continue" || got.Restore != id {
		t.Errorf("create body %+v", got)
	}
	if strings.Contains(h.stdout.String(), "the worktree starts at") {
		t.Errorf("a base line with --base:\n%s", h.stdout.String())
	}

	h = newHarness(t)
	serveRescues(h, apiv1.RescueList{Rescues: []apiv1.Rescue{rescueAt("20261006-2104", repoWith("haynes-ops", "refs/heads/main"))}})
	h.mustRun(ExitOK, "rescue", "restore", id, "--local", "--wait", "0")
	if strings.Contains(h.stdout.String(), "the worktree starts at") || strings.Contains(h.stdout.String(), "uncommitted work") {
		t.Errorf("hints for refs the bundle lacks:\n%s", h.stdout.String())
	}
	contains(t, "stdout", h.stdout.String(), "Its clone fetches rescue")

	h = newHarness(t)
	serveRescues(h, apiv1.RescueList{Rescues: []apiv1.Rescue{rescueAt("20261006-2104", repoWith("haynes-ops", "refs/heads/agent/"+rescueSession))}})
	h.mustRun(ExitOK, "rescue", "restore", id, "--local", "--wait", "0", "-o", "name")
	if h.stdout.String() != name+"\n" {
		t.Errorf("-o name printed %q", h.stdout.String())
	}
}

func TestRescueRestoreChoosesTheRepo(t *testing.T) {
	id := rescueSession + "/20261006-2104"
	two := apiv1.RescueList{Rescues: []apiv1.Rescue{rescueAt("20261006-2104", repoWith("haynes-ops"), repoWith("dev-env", "refs/heads/main"))}}

	h := newHarness(t)
	serveRescues(h, two)
	h.mustRun(ExitUsage, "rescue", "restore", id, "--local")
	contains(t, "stderr", h.stderr.String(), "holds several repos (haynes-ops, dev-env)", "--repo")
	assertNoCreate(t, h)

	h = newHarness(t)
	serveRescues(h, two)
	h.mustRun(ExitOK, "rescue", "restore", id, "--local", "--repo", "dev-env", "--wait", "0")
	if got := decodeCreate(t, h.api.requests()[1].body); got.Repo != "dev-env" {
		t.Errorf("repo %q", got.Repo)
	}

	h = newHarness(t)
	serveRescues(h, two)
	h.mustRun(ExitFailed, "rescue", "restore", id, "--local", "--repo", "libretto")
	contains(t, "stderr", h.stderr.String(), "holds no bundle for libretto", "haynes-ops, dev-env")
	assertNoCreate(t, h)
}

func TestRescueRestoreRefusals(t *testing.T) {
	id := rescueSession + "/20261006-2104"
	for _, tc := range []struct {
		name string
		list apiv1.RescueList
		code int
		want string
	}{
		{"not found", apiv1.RescueList{Rescues: []apiv1.Rescue{rescueAt("20260101-0000", repoWith("r"))}}, ExitNotFound,
			"no rescue " + id + "; agent-run rescue list shows them"},
		{"none at all", apiv1.RescueList{}, ExitNotFound, "no rescue " + id},
		{"incomplete", apiv1.RescueList{Rescues: []apiv1.Rescue{func() apiv1.Rescue {
			r := rescueAt("20261006-2104", repoWith("r"))
			r.Complete = false
			return r
		}()}}, ExitFailed, "only a complete rescue can be restored"},
		{"unfinished", apiv1.RescueList{Rescues: []apiv1.Rescue{func() apiv1.Rescue {
			r := rescueAt("20261006-2104", repoWith("r"))
			r.Complete, r.Finished = false, false
			return r
		}()}}, ExitFailed, "rescue " + id + " is unfinished: only a complete rescue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			serveRescues(h, tc.list)
			h.mustRun(tc.code, "rescue", "restore", id, "--local")
			contains(t, "stderr", h.stderr.String(), tc.want)
			assertNoCreate(t, h)
		})
	}
}

// A bad id, or a missing task, sends nothing at all.
func TestRescueRestoreUsageErrorsSendNothing(t *testing.T) {
	id := rescueSession + "/20261006-2104"
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no id", []string{"--local"}, "takes one rescue id"},
		{"bad id", []string{"nonsense", "--local"}, "rescue restore:"},
		{"path in id", []string{"../x/20261006-2104", "--local"}, "rescue restore:"},
		{"two ids", []string{id, id, "--local"}, "takes one rescue id"},
		{"no task", []string{id}, `say what to run`},
		{"repo with slash", []string{id, "--local", "--repo", "a/b"}, "a repository name under the GitHub owner"},
		{"-p with --local", []string{id, "--local", "-p", "x"}, "cannot combine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			serveRescues(h, apiv1.RescueList{})
			h.mustRun(ExitUsage, append([]string{"rescue", "restore"}, tc.args...)...)
			contains(t, "stderr", h.stderr.String(), tc.want)
			if n := len(h.api.requests()); n != 0 {
				t.Errorf("sent %d requests, want none", n)
			}
		})
	}
}

func assertNoCreate(t *testing.T, h *harness) {
	t.Helper()
	for _, r := range h.api.requests() {
		if r.method == http.MethodPost {
			t.Errorf("created a session despite the refusal: %+v", r)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0B", 1023: "1023B", 1024: "1.0K", 12 * 1024: "12K", 3_500_000: "3.3M", 5 << 30: "5.0G", 2048 << 30: "2.0T"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
