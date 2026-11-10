package agentrun

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func coordinatorArgs(extra ...string) []string {
	args := []string{"coordinator", "start", "--item", "527", "--repo", "dev-env", "--model", "claude-opus-5-5", "--hours", "3", "--workers", "2"}
	return append(args, extra...)
}

func TestCoordinatorStartAndRepeat(t *testing.T) {
	for _, ended := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "ended"}[ended], func(t *testing.T) {
			h := newHarness(t)
			var saved *apiv1.Session
			posts := 0
			h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
				if r.Header.Get(apiv1.CallerPolicyLimitsHeader) != apiv1.CallerPolicyLimitsV1 || r.Header.Get(apiv1.TaskOutcomeHeader) != apiv1.TaskOutcomeV1 {
					t.Error("missing worker-cap or outcome negotiation")
				}
				switch {
				case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionsPath:
					if r.URL.Query().Get("mine") != "true" {
						t.Error("item lookup must be caller-scoped")
					}
					list := apiv1.SessionList{Sessions: []apiv1.Session{}}
					if saved != nil {
						list.Sessions = append(list.Sessions, *saved)
					}
					writeTestJSON(w, 200, list)
				case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionsPath:
					posts++
					var req apiv1.CreateSessionRequest
					if err := json.Unmarshal(body, &req); err != nil {
						t.Error(err)
					}
					var keys map[string]json.RawMessage
					_ = json.Unmarshal(body, &keys)
					if _, ok := keys["effort"]; ok {
						t.Error("request includes effort")
					}
					if req.IdempotencyKey != "coordinator-527" || req.Agent != "claude" || req.Model != "claude-opus-5-5" || req.Mode != "task" || req.Profile != "dev" || req.Size != "M" || req.Repo != "dev-env" || req.Project != "" || req.Limits == nil || req.Limits.Timeout != "3h" || req.Limits.MaxWorkers == nil || *req.Limits.MaxWorkers != 2 {
						t.Errorf("wrong coordinator request: %+v", req)
					}
					contains(t, "brief", req.Prompt, "opwork show 527 --history", "opwork claim 527", "3 hours", "2 workers")
					saved = &apiv1.Session{Name: "dev-env-1110-060000", Phase: "Pending", Mode: "task", IdempotencyKey: req.IdempotencyKey}
					writeTestJSON(w, 201, saved)
				case r.Method == http.MethodGet && saved != nil && r.URL.Path == apiv1.SessionPath(saved.Name):
					writeTestJSON(w, 200, saved)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
			}
			h.mustRun(ExitOK, coordinatorArgs()...)
			contains(t, "start", h.stdout.String(), saved.Name, "Pending", "527")
			if ended {
				saved.Phase = "Failed"
				saved.AgentStatus = &apiv1.AgentStatus{State: "exited"}
			}
			h.mustRun(ExitOK, coordinatorArgs("-o", "json")...)
			var got struct {
				Item    int64         `json:"item"`
				Session apiv1.Session `json:"session"`
			}
			dec := json.NewDecoder(&h.stdout)
			if dec.Decode(&got) != nil || dec.Decode(new(any)) != io.EOF || got.Item != 527 || !reflect.DeepEqual(got.Session, *saved) || posts != 1 {
				t.Fatalf("repeat did not return one existing session document: %+v, posts=%d", got, posts)
			}
			if len(h.ran) != 0 {
				t.Fatalf("launcher ran subprocesses: %v", h.ran)
			}
		})
	}
}

func TestCoordinatorStartRejectsBeforeConnecting(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		extra         []string
	}{
		{"missing model", "--model is required", []string{"--model", ""}},
		{"alias", "full model", []string{"--model", "opus"}},
		{"wrong model", "full model", []string{"--model", "gpt-6.1-sol"}},
		{"effort", "effort", []string{"--effort", "max"}},
		{"empty effort", "effort", []string{"--effort="}},
		{"hours high", "--hours must be from 1 to 4", []string{"--hours", "5"}},
		{"hours low", "--hours must be from 1 to 4", []string{"--hours", "0"}},
		{"workers high", "--workers must be from 1 to 4", []string{"--workers", "5"}},
		{"workers low", "--workers must be from 1 to 4", []string{"--workers", "-1"}},
		{"item", "--item", []string{"--item", "0"}},
		{"repo", "--repo", []string{"--repo", "owner/repo"}},
		{"missing repo", "--repo or --project", []string{"--repo", ""}},
		{"both", "--repo or --project", []string{"--project", "dev-env"}},
		{"codex repo", "--project", []string{"--model", "gpt-6-astra"}},
		{"run path", "--run", []string{"--run", "../other"}},
		{"run length", "63", []string{"--run", strings.Repeat("a", 63)}},
		{"profile", "profile", []string{"--profile", "workbench"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.vars[envDefaultModel] = "claude-opus-5-5"
			h.mustRun(ExitUsage, coordinatorArgs(tc.extra...)...)
			contains(t, "error", h.stderr.String(), tc.message)
			if len(h.api.requests()) != 0 || len(h.ran) != 0 {
				t.Fatal("invalid start connected")
			}
		})
	}
}

func TestCoordinatorStartProjectAndExplicitRun(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method == http.MethodGet {
			// A prior run must not hide this deliberate new run.
			writeTestJSON(w, 200, apiv1.SessionList{Sessions: []apiv1.Session{{Name: "prior", IdempotencyKey: "coordinator-527"}}})
			return
		}
		var req apiv1.CreateSessionRequest
		_ = json.Unmarshal(body, &req)
		if req.IdempotencyKey != "coordinator-527-retry-2" || req.Project != "dev-env" || req.Repo != "" || req.Agent != "codex" || req.Model != "gpt-6-astra" || strings.Contains(string(body), `"effort"`) {
			t.Errorf("wrong project request: %+v", req)
		}
		writeTestJSON(w, 201, apiv1.Session{Name: "second", Phase: "Pending", IdempotencyKey: req.IdempotencyKey})
	}
	h.mustRun(ExitOK, "coordinator", "start", "--item", "527", "--project", "dev-env", "--model", "gpt-6-astra", "--hours", "4", "--workers", "4", "--run", "retry-2")
}

func TestCoordinatorStatusRetainedOutcome(t *testing.T) {
	for _, selector := range [][]string{{"ended"}, {"--item", "527", "--run", "retry-2"}} {
		h := newHarness(t)
		s := apiv1.Session{Name: "ended", Phase: "Failed", IdempotencyKey: "coordinator-527-retry-2", TaskOutcome: &protocol.TaskOutcome{SessionUID: "session", PodUID: "pod", Record: protocol.TaskOutcomeRecord{Summary: "Ready for review", PRURLs: []string{"https://github.com/thaynes43/dev-env/pull/123"}, HeadSHAs: []string{strings.Repeat("a", 40)}}}}
		h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
			if r.Method != http.MethodGet {
				t.Error("status mutated a session")
			}
			if r.URL.Path == apiv1.SessionsPath {
				writeTestJSON(w, 200, apiv1.SessionList{Sessions: []apiv1.Session{s}})
			} else {
				writeTestJSON(w, 200, s)
			}
		}
		h.mustRun(ExitOK, append([]string{"coordinator", "status"}, selector...)...)
		contains(t, "status", h.stdout.String(), "ended", "Failed", "527", "Ready for review", s.TaskOutcome.Record.PRURLs[0], s.TaskOutcome.Record.HeadSHAs[0])
	}
}

func TestCoordinatorItemMissingOrAmbiguous(t *testing.T) {
	for _, count := range []int{0, 2} {
		h := newHarness(t)
		h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
			if r.Method != http.MethodGet || r.URL.Query().Get("mine") != "true" {
				t.Errorf("unexpected lookup: %s %s", r.Method, r.URL)
			}
			list := apiv1.SessionList{Sessions: []apiv1.Session{{Name: "unrelated", IdempotencyKey: "coordinator-527-other"}}}
			for _, name := range []string{"first", "second"}[:count] {
				list.Sessions = append(list.Sessions, apiv1.Session{Name: name, IdempotencyKey: "coordinator-527"})
			}
			writeTestJSON(w, 200, list)
		}
		want := ExitNotFound
		if count == 2 {
			want = ExitFailed
		}
		h.mustRun(want, "coordinator", "status", "--item", "527")
		if count == 2 {
			h.mustRun(ExitFailed, coordinatorArgs()...)
			contains(t, "ambiguity", h.stderr.String(), "first", "second", "name")
		}
	}
}

func TestCoordinatorStopUsesReap(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method != http.MethodDelete || r.URL.Path != apiv1.SessionPath("coordinator") {
			t.Errorf("not reap: %s %s", r.Method, r.URL)
		}
		writeTestJSON(w, 202, apiv1.Session{Name: "coordinator", Reaping: true})
	}
	h.mustRun(ExitOK, "coordinator", "stop", "coordinator")
	contains(t, "stop", h.stdout.String(), "reaping coordinator", "rescues")
}

func TestCoordinatorBriefAndShortHelp(t *testing.T) {
	h := newHarness(t)
	h.mustRun(ExitOK, "coordinator", "brief", "--item", "527", "--hours", "4", "--workers", "3")
	contains(t, "brief", h.stdout.String(), "opwork show 527 --history", "opwork claim 527", "4 hours", "3 workers", "claude-sonnet-5-5", "claude-opus-5-5", "gpt-6.1-sol", "grok-4.7-high", "grok-4.7-xhigh", "--project", "--profile dev", ".outcome.json", "blocked: <refusal>", "Never wait on Meatball")
	h.mustRun(ExitOK, "coordinator", "brief")
	if len(h.api.requests()) != 0 {
		t.Fatal("brief connected")
	}
	for _, verb := range []string{"start", "status", "stop"} {
		h.mustRun(ExitOK, "coordinator", verb, "--help")
		if words := len(strings.Fields(h.stdout.String())); words == 0 || words > 60 {
			t.Fatalf("%s help has %d words", verb, words)
		}
		h.mustRun(ExitOK, "help", "coordinator", verb)
	}
}
