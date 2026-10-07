package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const name = "haynes-ops-1006-210000"

// serveCreate answers a create with 201 and each GET of the session with the
// next of states, repeating the last.
func serveCreate(h *harness, states ...apiv1.Session) {
	var gets atomic.Int32
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionsPath:
			req := decodeCreate(h.t, body)
			s := testSession(name, "Pending")
			s.Repo, s.Model, s.Effort, s.Size = req.Repo, req.Model, req.Effort, req.Size
			w.Header().Set("Location", apiv1.SessionPath(name))
			writeTestJSON(w, http.StatusCreated, s)
		case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath(name):
			i := int(gets.Add(1)) - 1
			if i >= len(states) {
				i = len(states) - 1
			}
			writeTestJSON(w, http.StatusOK, states[i])
		default:
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route")
		}
	}
}

func TestCreateSendsTheTask(t *testing.T) {
	h := newHarness(t)
	serveCreate(h, pendingOn(testSession(name, ""), "Creating", "creating the pod"), runningOn(testSession(name, ""), "talosw02"))

	h.mustRun(ExitOK, "--repo", "haynes-ops", "-p", "fix the typo in README.md")

	reqs := h.api.requests()
	if len(reqs) != 3 {
		t.Fatalf("got %d requests, want a create and two polls", len(reqs))
	}
	if reqs[0].auth != "Bearer "+testToken {
		t.Errorf("Authorization = %q, want the token file's token", reqs[0].auth)
	}
	got := decodeCreate(t, reqs[0].body)
	want := apiv1.CreateSessionRequest{
		Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: DefaultClaudeModel, Effort: "xhigh",
		Prompt: "fix the typo in README.md", IdempotencyKey: "agent-run-testkey",
	}
	if g, w := mustJSON(t, got), mustJSON(t, want); g != w {
		t.Errorf("create body = %s, want %s", g, w)
	}
	contains(t, "stdout", h.stdout.String(),
		"created "+name+"\n",
		"claude claude-opus-5-5, effort xhigh, size M, repo haynes-ops",
		"Running on talosw02.",
		"agent-run show "+name)
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", h.stderr.String())
	}
	if len(h.sleeps) != 2 || h.sleeps[0] != pollInterval {
		t.Errorf("sleeps = %v, want two polls %s apart", h.sleeps, pollInterval)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// v1's spellings: a positional repository anywhere, `run` as the verb, and
// --prompt for -p.
func TestCreateTakesV1Forms(t *testing.T) {
	for _, args := range [][]string{
		{"haynes-ops", "-p", "task"},
		{"-p", "task", "haynes-ops"},
		{"run", "haynes-ops", "-p", "task"},
		{"run", "--repo=haynes-ops", "--prompt", "task"},
		{"--repo", "haynes-ops", "--prompt=task", "--wait", "0"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := newHarness(t)
			serveCreate(h, runningOn(testSession(name, ""), "talosw03"))
			h.mustRun(ExitOK, args...)
			req := decodeCreate(t, h.api.requests()[0].body)
			if req.Repo != "haynes-ops" || req.Prompt != "task" {
				t.Errorf("repo %q prompt %q, want haynes-ops and task", req.Repo, req.Prompt)
			}
		})
	}
}

func TestCreateFlags(t *testing.T) {
	h := newHarness(t)
	serveCreate(h, runningOn(testSession(name, ""), "talosw01"))
	h.mustRun(ExitOK, "--repo", "dev-env", "-p", "task", "--model", "claude-sonnet-5-5", "--effort", "ultracode",
		"--base", "origin/release", "--size", "l", "--profile", "dev", "--timeout", "40m", "--max-turns", "50",
		"--idempotency-key", "order-42", "--agent", "claude")
	got := decodeCreate(t, h.api.requests()[0].body)
	want := apiv1.CreateSessionRequest{
		Repo: "dev-env", Base: "origin/release", Agent: "claude", Mode: "task", Model: "claude-sonnet-5-5",
		Effort: "ultracode", Prompt: "task", Size: "L", Profile: "dev",
		Limits: &apiv1.Limits{Timeout: "40m", MaxTurns: 50}, IdempotencyKey: "order-42",
	}
	if g, w := mustJSON(t, got), mustJSON(t, want); g != w {
		t.Errorf("create body = %s, want %s", g, w)
	}
}

func TestCreateReadsThePromptFromAFileOrStdin(t *testing.T) {
	h := newHarness(t)
	serveCreate(h, runningOn(testSession(name, ""), "talosw02"))
	p := h.write("order.md", "line one\nline two\n")
	h.mustRun(ExitOK, "--repo", "haynes-ops", "--prompt-file", p, "--wait", "0")
	if got := decodeCreate(t, h.api.requests()[0].body).Prompt; got != "line one\nline two\n" {
		t.Errorf("prompt = %q, want the file's content", got)
	}

	h.env.Stdin = strings.NewReader("from stdin")
	h.mustRun(ExitOK, "--repo", "haynes-ops", "--prompt-file", "-", "--wait", "0")
	reqs := h.api.requests()
	if got := decodeCreate(t, reqs[len(reqs)-1].body).Prompt; got != "from stdin" {
		t.Errorf("prompt = %q, want stdin", got)
	}
}

func TestCreateEffortPerModel(t *testing.T) {
	for _, tc := range []struct {
		model, asked, want string
	}{
		{"claude-opus-5-5", "", "xhigh"},
		{"claude-opus-5-5", "max", "max"},
		{"claude-fable-5-1", "ultracode", "ultracode"},
		{"claude-opus-4-6", "", "high"},
		{"claude-sonnet-4-6[1m]", "max", "max"},
		{"claude-haiku-4-5", "", ""},
	} {
		t.Run(tc.model+" "+tc.asked, func(t *testing.T) {
			h := newHarness(t)
			serveCreate(h, runningOn(testSession(name, ""), "talosw02"))
			args := []string{"--repo", "haynes-ops", "-p", "task", "--model", tc.model, "--wait", "0"}
			if tc.asked != "" {
				args = append(args, "--effort", tc.asked)
			}
			h.mustRun(ExitOK, args...)
			if got := decodeCreate(t, h.api.requests()[0].body).Effort; got != tc.want {
				t.Errorf("effort = %q, want %q", got, tc.want)
			}
		})
	}
}

// Every refusal agent-run makes itself sends nothing and exits 2.
func TestCreateUsageErrors(t *testing.T) {
	big := strings.Repeat("x", protocol.MaxPromptBytes+1)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no task", []string{"--repo", "haynes-ops"}, `say what to run: -p "<task>"`},
		{"blank task", []string{"--repo", "haynes-ops", "-p", "  "}, "say what to run"},
		{"no repo", []string{"-p", "task"}, "say which repository: --repo <name>"},
		{"a path for a repo", []string{"--repo", "thaynes43/haynes-ops", "-p", "task"}, "a repository name under the GitHub owner"},
		{"two repos", []string{"--repo", "a", "b", "-p", "task"}, "two repositories: --repo a and b"},
		{"three words", []string{"a", "b", "-p", "task"}, "one repository"},
		{"a verb after flags", []string{"-p", "task", "list"}, `"list" is a command: put it first`},
		{"an alias", []string{"--repo", "r", "-p", "t", "--model", "opus"}, `--model "opus" is an alias`},
		{"an alias with 1m", []string{"--repo", "r", "-p", "t", "--model", "sonnet[1m]"}, `"sonnet[1m]" is an alias`},
		{"fable alias", []string{"--repo", "r", "-p", "t", "--model", "Fable"}, "is an alias"},
		{"a latest alias", []string{"--repo", "r", "-p", "t", "--model", "claude-opus-latest"}, "an alias that moves with each release"},
		{"no version", []string{"--repo", "r", "-p", "t", "--model", "claude-opus"}, `"claude-opus" is not a full Claude model id`},
		{"not claude", []string{"--repo", "r", "-p", "t", "--model", "gpt-6-astra"}, "not a full Claude model id"},
		{"effort on haiku", []string{"--repo", "r", "-p", "t", "--model", "claude-haiku-4-5", "--effort", "low"}, "model claude-haiku-4-5 has no effort control; leave --effort out"},
		{"xhigh on 4.6", []string{"--repo", "r", "-p", "t", "--model", "claude-opus-4-6", "--effort", "xhigh"}, "model claude-opus-4-6 takes low, medium, high, max"},
		{"ultracode on 4.6", []string{"--repo", "r", "-p", "t", "--model", "claude-opus-4-6", "--effort", "ultracode"}, "takes low, medium, high, max"},
		{"a made-up level", []string{"--repo", "r", "-p", "t", "--effort", "ultra"}, "takes low, medium, high, xhigh, max or ultracode"},
		{"unknown agent", []string{"--repo", "r", "-p", "t", "--agent", "gemini"}, `--agent is claude, codex or opencode, not "gemini"`},
		{"codex without a model", []string{"--repo", "r", "-p", "t", "--agent", "codex"}, "--agent codex needs --model"},
		{"bad size", []string{"--repo", "r", "-p", "t", "--size", "XL"}, `--size is S, M or L, not "XL"`},
		{"bad timeout", []string{"--repo", "r", "-p", "t", "--timeout", "forever"}, `--timeout "forever" is not a positive duration`},
		{"zero timeout", []string{"--repo", "r", "-p", "t", "--timeout", "0s"}, "not a positive duration"},
		{"negative turns", []string{"--repo", "r", "-p", "t", "--max-turns", "-1"}, "--max-turns is a positive number"},
		{"negative wait", []string{"--repo", "r", "-p", "t", "--wait", "-1s"}, "--wait is a duration of 0 or more"},
		{"too big", []string{"--repo", "r", "-p", big}, "more than 65536"},
		{"both prompt flags", []string{"--repo", "r", "-p", "t", "--prompt-file", "x"}, "with -p or --prompt-file, not both"},
		{"missing prompt file", []string{"--repo", "r", "--prompt-file", "/nonexistent/order.md"}, "--prompt-file: open /nonexistent/order.md"},
		{"--safe", []string{"--repo", "r", "-p", "t", "--safe"}, "--safe is gone in v2"},
		{"-p with --interactive", []string{"--repo", "r", "-p", "t", "--interactive"}, "cannot combine with --interactive or --local"},
		{"--interactive", []string{"--repo", "r", "--interactive"}, "arrive in plans 02 and 03"},
		{"--local", []string{"--repo", "r", "--local"}, "arrive in plans 02 and 03"},
		{"an unknown flag", []string{"--repo", "r", "-p", "t", "--tools", "blender"}, "flag provided but not defined: -tools"},
		{"a bad output", []string{"--repo", "r", "-p", "t", "-o", "yaml"}, "-o yaml is not a format it prints; it takes -o name or -o json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			serveCreate(h, runningOn(testSession(name, ""), "talosw02"))
			h.mustRun(ExitUsage, tc.args...)
			contains(t, "stderr", h.stderr.String(), tc.want)
			if n := len(h.api.requests()); n != 0 {
				t.Errorf("sent %d requests, want none", n)
			}
		})
	}
}

func TestCreateDefaultModelFromTheEnvironment(t *testing.T) {
	h := newHarness(t)
	serveCreate(h, runningOn(testSession(name, ""), "talosw02"))
	h.vars[envDefaultModel] = "claude-sonnet-5-5"
	h.mustRun(ExitOK, "--repo", "r", "-p", "t", "--wait", "0")
	if got := decodeCreate(t, h.api.requests()[0].body).Model; got != "claude-sonnet-5-5" {
		t.Errorf("model = %q, want DEV_ENV_CLAUDE_MODEL's", got)
	}

	h.vars[envDefaultModel] = "opus"
	h.mustRun(ExitUsage, "--repo", "r", "-p", "t")
	contains(t, "stderr", h.stderr.String(), `DEV_ENV_CLAUDE_MODEL "opus" is an alias`)

	// --model wins over the environment.
	h.mustRun(ExitOK, "--repo", "r", "-p", "t", "--model", "claude-opus-5-5", "--wait", "0")
}

// A create whose answer is lost is sent again with the same key, so the API
// returns the session the first attempt created rather than a second one.
func TestCreateRetriesWithTheSameKey(t *testing.T) {
	h := newHarness(t)
	var posts atomic.Int32
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch posts.Add(1) {
		case 1:
			// The connection drops before an answer.
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		case 2:
			apiError(w, http.StatusServiceUnavailable, apiv1.CodeUnavailable, "create session: the Kubernetes API server is busy")
		default:
			writeTestJSON(w, http.StatusOK, runningOn(testSession(name, ""), "talosw02"))
		}
	}
	h.mustRun(ExitOK, "--repo", "haynes-ops", "-p", "task")
	reqs := h.api.requests()
	if len(reqs) != 3 {
		t.Fatalf("got %d requests, want 3", len(reqs))
	}
	for i, r := range reqs {
		if k := decodeCreate(t, r.body).IdempotencyKey; k != "agent-run-testkey" {
			t.Errorf("attempt %d key = %q, want the same key each time", i+1, k)
		}
	}
	if want := []time.Duration{time.Second, 2 * time.Second}; len(h.sleeps) != 2 || h.sleeps[0] != want[0] || h.sleeps[1] != want[1] {
		t.Errorf("sleeps = %v, want %v", h.sleeps, want)
	}
	// agent-run's own key found its own session: that is a create.
	contains(t, "stdout", h.stdout.String(), "created "+name)
}

func TestCreateWithAKeyThatAlreadyMadeASession(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeTestJSON(w, http.StatusOK, runningOn(testSession(name, ""), "talosw02"))
	}
	h.mustRun(ExitOK, "--repo", "haynes-ops", "-p", "task", "--idempotency-key", "order-42")
	contains(t, "stdout", h.stdout.String(), name+" already exists: idempotency key order-42 created it")
}

// The scheduler's reason is printed as soon as two polls agree, not after the
// whole wait (plan 01's acceptance).
func TestCreateReportsTheSchedulersReasonAtOnce(t *testing.T) {
	h := newHarness(t)
	reason := "0/7 nodes are available: 3 Insufficient cpu, 4 node(s) had untolerated taint"
	serveCreate(h, pendingOn(testSession(name, ""), unschedulable, reason))
	h.mustRun(ExitOK, "--repo", "haynes-ops", "-p", "task")
	contains(t, "stdout", h.stdout.String(), "Pending: the scheduler cannot place it yet: "+reason+". It starts when room frees up.")
	if len(h.sleeps) != 2 {
		t.Errorf("polled %d times, want 2", len(h.sleeps))
	}
}

// One Unschedulable poll, such as a volume binding, is not reported.
func TestCreateWaitsOutABriefUnschedulable(t *testing.T) {
	h := newHarness(t)
	serveCreate(h,
		pendingOn(testSession(name, ""), unschedulable, "pod has unbound immediate PersistentVolumeClaims"),
		pendingOn(testSession(name, ""), "ContainerCreating", "ContainerCreating"),
		runningOn(testSession(name, ""), "talosw02"))
	h.mustRun(ExitOK, "--repo", "haynes-ops", "-p", "task")
	contains(t, "stdout", h.stdout.String(), "Running on talosw02.")
}

func TestCreateStopsWaitingAtTheDeadline(t *testing.T) {
	h := newHarness(t)
	serveCreate(h, pendingOn(testSession(name, ""), "ContainerCreating", "pulling the image"))
	h.mustRun(ExitOK, "--repo", "haynes-ops", "-p", "task", "--wait", "10s")
	contains(t, "stdout", h.stdout.String(), "Still starting: pulling the image.")
	if len(h.sleeps) != 5 {
		t.Errorf("polled %d times in 10s, want 5", len(h.sleeps))
	}
}

func TestCreateWithoutWaiting(t *testing.T) {
	h := newHarness(t)
	serveCreate(h, runningOn(testSession(name, ""), "talosw02"))
	h.mustRun(ExitOK, "--repo", "haynes-ops", "-p", "task", "--wait", "0")
	if n := len(h.api.requests()); n != 1 {
		t.Errorf("sent %d requests, want only the create", n)
	}
	contains(t, "stdout", h.stdout.String(), "Pending: not waiting for it to start (--wait 0).")
}

func TestCreateOfASessionThatFails(t *testing.T) {
	h := newHarness(t)
	failed := testSession(name, "Failed")
	failed.Conditions = []apiv1.Condition{{Type: conditionPodReady, Status: "False", Reason: "SessionInvalid", Message: "repo: no such repository"}}
	serveCreate(h, failed)
	h.mustRun(ExitFailed, "--repo", "haynes-ops", "-p", "task")
	contains(t, "stdout", h.stdout.String(), "Failed: repo: no such repository.")
	contains(t, "stderr", h.stderr.String(), name+" failed: repo: no such repository")
}

// A poll that fails after the create does not undo it: the session exists.
func TestCreateWhenAPollFails(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method == http.MethodPost {
			writeTestJSON(w, http.StatusCreated, testSession(name, "Pending"))
			return
		}
		apiError(w, http.StatusInternalServerError, apiv1.CodeInternal, "the operator failed this request")
	}
	h.mustRun(ExitOK, "--repo", "haynes-ops", "-p", "task")
	contains(t, "stdout", h.stdout.String(), "created "+name)
	contains(t, "stderr", h.stderr.String(), "could not follow "+name+" after it was created")
}

func TestCreateOutputs(t *testing.T) {
	h := newHarness(t)
	serveCreate(h, runningOn(testSession(name, ""), "talosw02"))
	h.mustRun(ExitOK, "--repo", "haynes-ops", "-p", "task", "-o", "name")
	if got := h.stdout.String(); got != name+"\n" {
		t.Errorf("-o name printed %q, want the name alone", got)
	}

	h.mustRun(ExitOK, "--repo", "haynes-ops", "-p", "task", "--output", "json")
	var s apiv1.Session
	if err := json.Unmarshal(h.stdout.Bytes(), &s); err != nil {
		t.Fatalf("-o json printed no session: %v\n%s", err, h.stdout.String())
	}
	if s.Name != name || s.Phase != "Running" || s.Node != "talosw02" {
		t.Errorf("-o json printed %+v, want the session as last seen", s)
	}
}
