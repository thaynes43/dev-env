package v1alpha1_test

// The envtest suite for the AgentSession CRD: a real kube-apiserver with
// config/crd/ installed proves that the schema accepts what DESIGN-001 3.3 and
// 3.7 allow, rejects what they forbid (D-39), applies the defaults, and keeps
// spec and status apart. It runs through `make test` (KUBEBUILDER_ASSETS).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/testenv"
)

const ns = "default"

var (
	k8s     client.Client
	restCfg *rest.Config
	seq     atomic.Int64
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	env, err := testenv.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "envtest:", err)
		return 1
	}
	defer func() {
		if err := env.Stop(); err != nil {
			fmt.Fprintln(os.Stderr, "envtest stop:", err)
		}
	}()

	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		fmt.Fprintln(os.Stderr, "scheme:", err)
		return 1
	}
	k8s, err = client.New(env.Config, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintln(os.Stderr, "client:", err)
		return 1
	}
	restCfg = env.Config
	return m.Run()
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

// name returns a fresh session name, so the cases never collide.
func name() string {
	return fmt.Sprintf("s-%d", seq.Add(1))
}

func dur(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }

// taskSession is the smallest valid session: Tom's own task, on Claude.
func taskSession() *v1alpha1.AgentSession {
	return &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: name(), Namespace: ns},
		Spec: v1alpha1.AgentSessionSpec{
			Repo:   "haynes-ops",
			Agent:  v1alpha1.AgentClaude,
			Mode:   v1alpha1.ModeTask,
			Model:  "claude-opus-5-5",
			Prompt: "fix the typo in README.md",
		},
	}
}

// remoteSession is Tom's own remote session, driven from the phone.
func remoteSession() *v1alpha1.AgentSession {
	s := taskSession()
	s.Spec.Mode = v1alpha1.ModeRemote
	s.Spec.Prompt = ""
	return s
}

// summonedTask is alert-responder's remediation session (DESIGN-001 3.7).
func summonedTask() *v1alpha1.AgentSession {
	s := taskSession()
	s.Name = "rem-responder-" + name()
	s.Spec.Caller = "alert-responder"
	s.Spec.Lane = v1alpha1.LaneRemediation
	s.Spec.IdempotencyKey = "b7baaf6c"
	s.Spec.Profile = "ops"
	s.Spec.Parent = "upgrade-agent/alert-responder"
	s.Spec.Effort = "xhigh"
	s.Spec.Limits = &v1alpha1.SessionLimits{Timeout: dur(40 * time.Minute), MaxTurns: 120}
	return s
}

// summonedRemote is an escalation that Tom joins from the phone.
func summonedRemote() *v1alpha1.AgentSession {
	s := summonedTask()
	s.Name = "esc-responder-" + name()
	s.Spec.Mode = v1alpha1.ModeRemote
	s.Spec.Lane = v1alpha1.LaneEscalation
	s.Spec.Prompt = ""
	s.Spec.Limits = nil
	s.Spec.Model = "claude-fable-5-1"
	return s
}

func opencodeSession() *v1alpha1.AgentSession {
	s := taskSession()
	s.Spec.Agent = v1alpha1.AgentOpencode
	s.Spec.Model = "qwen3-coder-30b-a3b-instruct-q4_k_m"
	s.Spec.LLM = &v1alpha1.LLMSpec{Pool: "llm-coder"}
	s.Spec.Profile = "dev"
	return s
}

// wantInvalid fails unless err is the API server's 422 Invalid and its message
// contains want.
func wantInvalid(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("accepted, want rejected with %q", want)
	}
	if !apierrors.IsInvalid(err) {
		t.Fatalf("got %v, want an Invalid error containing %q", err, want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("got %q, want it to contain %q", err.Error(), want)
	}
}

func TestCreateAppliesDefaults(t *testing.T) {
	s := taskSession()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatalf("create: %v", err)
	}

	got := &v1alpha1.AgentSession{}
	if err := k8s.Get(ctx(t), client.ObjectKeyFromObject(s), got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Spec.Base != "origin/main" {
		t.Errorf("base = %q, want the default origin/main", got.Spec.Base)
	}
	if got.Spec.Size != v1alpha1.SizeM {
		t.Errorf("size = %q, want the default M (DESIGN-001 7.2)", got.Spec.Size)
	}
	if got.Spec.OperatingMode != v1alpha1.OperatingModeRunning {
		t.Errorf("operatingMode = %q, want the default Running", got.Spec.OperatingMode)
	}
	// The operator resolves these per mode and from the templates; the schema
	// leaves them empty (DESIGN-001 4.3, D-04).
	if got.Spec.Lifecycle != nil || got.Spec.Profile != "" || got.Spec.Tools != nil {
		t.Errorf("lifecycle, profile and tools should stay unset, got %+v, %q, %v",
			got.Spec.Lifecycle, got.Spec.Profile, got.Spec.Tools)
	}
	if got.Generation != 1 {
		t.Errorf("generation = %d, want 1", got.Generation)
	}
}

func TestCreateAccepts(t *testing.T) {
	cases := map[string]*v1alpha1.AgentSession{
		"Tom's task": taskSession(),
		"Tom's remote session with every optional field": func() *v1alpha1.AgentSession {
			s := remoteSession()
			s.Spec.Base = "origin/feature-x"
			s.Spec.Effort = "xhigh"
			s.Spec.Size = v1alpha1.SizeL
			s.Spec.Profile = "full"
			s.Spec.Tools = []string{"blender", "audio"}
			s.Spec.Parent = "haynes-ops-1005-195501"
			s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
			s.Spec.Lifecycle = &v1alpha1.Lifecycle{IdleSuspendAfter: dur(72 * time.Hour), ArchiveAfter: dur(168 * time.Hour)}
			return s
		}(),
		"a local Codex session": func() *v1alpha1.AgentSession {
			s := remoteSession()
			s.Spec.Agent = v1alpha1.AgentCodex
			s.Spec.Mode = v1alpha1.ModeLocal
			s.Spec.Model = "gpt-6-astra"
			s.Spec.Effort = "max"
			s.Spec.Size = v1alpha1.SizeS
			return s
		}(),
		"an opencode task on an LLM pool": opencodeSession(),
		"a summoned remediation task":     summonedTask(),
		"a summoned escalation":           summonedRemote(),
		"a summoned upgrade without an idempotency key": func() *v1alpha1.AgentSession {
			s := summonedRemote()
			s.Name = "wo-" + name()
			s.Spec.Caller = "upgrade-shepherd"
			s.Spec.Lane = v1alpha1.LaneUpgrade
			s.Spec.IdempotencyKey = ""
			return s
		}(),
		"a 63-character name": func() *v1alpha1.AgentSession {
			s := taskSession()
			s.Name = s.Name + "-" + strings.Repeat("a", 62-len(s.Name))
			return s
		}(),
		"a base that is a commit": func() *v1alpha1.AgentSession {
			s := taskSession()
			s.Spec.Base = "5e7ca8e"
			return s
		}(),
		"a repo whose name starts with a dot": func() *v1alpha1.AgentSession {
			s := taskSession()
			s.Spec.Repo = ".github"
			return s
		}(),
		"a task with a zero turn cap and a timeout": func() *v1alpha1.AgentSession {
			s := taskSession()
			s.Spec.Limits = &v1alpha1.SessionLimits{Timeout: dur(3 * time.Hour)}
			return s
		}(),
	}
	for title, s := range cases {
		t.Run(title, func(t *testing.T) {
			if len(s.Name) > 63 {
				t.Fatalf("test bug: name %q is %d characters", s.Name, len(s.Name))
			}
			if err := k8s.Create(ctx(t), s); err != nil {
				t.Fatalf("create %s: %v", s.Name, err)
			}
		})
	}
}

func TestCreateRejects(t *testing.T) {
	cases := []struct {
		title  string
		mutate func(*v1alpha1.AgentSession)
		want   string
	}{
		// metadata.name (it is the pod's hostname).
		{"a 64-character name", func(s *v1alpha1.AgentSession) { s.Name = strings.Repeat("a", 64) }, "at most 63 characters"},
		{"a name with a dot", func(s *v1alpha1.AgentSession) { s.Name = "haynes.ops" }, "must be a DNS label"},

		// repo and base.
		{"an empty repo", func(s *v1alpha1.AgentSession) { s.Spec.Repo = "" }, "spec.repo"},
		{"a repo with an owner", func(s *v1alpha1.AgentSession) { s.Spec.Repo = "thaynes43/haynes-ops" }, "spec.repo"},
		{"a repo that is a path", func(s *v1alpha1.AgentSession) { s.Spec.Repo = ".." }, "repo is a repository name, not a path"},
		{"a base that git would read as an option", func(s *v1alpha1.AgentSession) { s.Spec.Base = "--upload-pack=touch /tmp/x" }, "spec.base"},
		{"a base with a space", func(s *v1alpha1.AgentSession) { s.Spec.Base = "origin/main extra" }, "spec.base"},

		// Enums.
		{"an unknown agent", func(s *v1alpha1.AgentSession) { s.Spec.Agent = "gemini" }, "spec.agent: Unsupported value"},
		{"v1's both mode", func(s *v1alpha1.AgentSession) { s.Spec.Mode = "both" }, "spec.mode: Unsupported value"},
		{"an unknown size", func(s *v1alpha1.AgentSession) { s.Spec.Size = "XL" }, "spec.size: Unsupported value"},
		{"an unknown operating mode", func(s *v1alpha1.AgentSession) { s.Spec.OperatingMode = "Stopped" }, "spec.operatingMode: Unsupported value"},

		// Models and effort.
		{"a Claude alias", func(s *v1alpha1.AgentSession) { s.Spec.Model = "opus" }, "never an alias"},
		{"a Claude model of another vendor's form", func(s *v1alpha1.AgentSession) { s.Spec.Model = "gpt-6-astra" }, "never an alias"},
		{"a model with a space", func(s *v1alpha1.AgentSession) { s.Spec.Model = "claude opus" }, "spec.model"},
		{"an empty model", func(s *v1alpha1.AgentSession) { s.Spec.Model = "" }, "spec.model"},
		{"an effort in capitals", func(s *v1alpha1.AgentSession) { s.Spec.Effort = "XHigh" }, "spec.effort"},

		// Mode-bound fields.
		{"a task without a prompt", func(s *v1alpha1.AgentSession) { s.Spec.Prompt = "" }, "prompt is required in task mode"},
		{"a prompt in remote mode", func(s *v1alpha1.AgentSession) { s.Spec.Mode = v1alpha1.ModeRemote }, "not allowed in local or remote mode"},
		{"a prompt in local mode", func(s *v1alpha1.AgentSession) { s.Spec.Mode = v1alpha1.ModeLocal }, "not allowed in local or remote mode"},
		{"limits on a remote session", func(s *v1alpha1.AgentSession) {
			s.Spec.Mode, s.Spec.Prompt = v1alpha1.ModeRemote, ""
			s.Spec.Limits = &v1alpha1.SessionLimits{MaxTurns: 10}
		}, "limits apply to task mode only"},
		{"a negative turn cap", func(s *v1alpha1.AgentSession) { s.Spec.Limits = &v1alpha1.SessionLimits{MaxTurns: -1} }, "spec.limits.maxTurns"},
		{"a zero timeout", func(s *v1alpha1.AgentSession) { s.Spec.Limits = &v1alpha1.SessionLimits{Timeout: dur(0)} }, "must be a positive Go duration"},
		{"a negative timeout", func(s *v1alpha1.AgentSession) {
			s.Spec.Limits = &v1alpha1.SessionLimits{Timeout: dur(-5 * time.Minute)}
		}, "must be a positive Go duration"},
		{"a zero idle window", func(s *v1alpha1.AgentSession) {
			s.Spec.Lifecycle = &v1alpha1.Lifecycle{IdleSuspendAfter: dur(0)}
		}, "spec.lifecycle.idleSuspendAfter"},
		{"a negative archive window", func(s *v1alpha1.AgentSession) {
			s.Spec.Lifecycle = &v1alpha1.Lifecycle{ArchiveAfter: dur(-time.Hour)}
		}, "spec.lifecycle.archiveAfter"},

		// Agent-bound fields.
		{"an LLM pool for Claude", func(s *v1alpha1.AgentSession) { s.Spec.LLM = &v1alpha1.LLMSpec{Pool: "llm-coder"} }, "llm is required for agent opencode and not allowed"},
		{"opencode without an LLM pool", func(s *v1alpha1.AgentSession) {
			s.Spec.Agent, s.Spec.Model = v1alpha1.AgentOpencode, "qwen3-coder-30b-a3b-instruct-q4_k_m"
		}, "llm is required for agent opencode"},
		{"an LLM pool with an empty name", func(s *v1alpha1.AgentSession) {
			s.Spec.Agent, s.Spec.Model = v1alpha1.AgentOpencode, "qwen3-coder-30b-a3b-instruct-q4_k_m"
			s.Spec.LLM = &v1alpha1.LLMSpec{}
		}, "spec.llm.pool"},

		// Summoned-only fields (DESIGN-001 3.7).
		{"a lane without a caller", func(s *v1alpha1.AgentSession) { s.Spec.Lane = v1alpha1.LaneRemediation }, "caller and lane go together"},
		{"a caller without a lane", func(s *v1alpha1.AgentSession) {
			s.Spec.Caller, s.Spec.Profile = "alert-responder", "ops"
		}, "caller and lane go together"},
		{"an idempotency key on Tom's own session", func(s *v1alpha1.AgentSession) { s.Spec.IdempotencyKey = "b7baaf6c" }, "idempotencyKey is for summoned sessions only"},
		{"an unknown lane", func(s *v1alpha1.AgentSession) {
			s.Spec.Caller, s.Spec.Lane, s.Spec.Profile = "alert-responder", "triage", "ops"
		}, "spec.lane: Unsupported value"},

		// Lists.
		{"a tool name in capitals", func(s *v1alpha1.AgentSession) { s.Spec.Tools = []string{"Blender"} }, "spec.tools[0]"},
		{"a tool listed twice", func(s *v1alpha1.AgentSession) { s.Spec.Tools = []string{"audio", "audio"} }, "Duplicate value"},
		{"a profile in capitals", func(s *v1alpha1.AgentSession) { s.Spec.Profile = "Full" }, "spec.profile"},
	}
	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			s := taskSession()
			c.mutate(s)
			wantInvalid(t, k8s.Create(ctx(t), s), c.want)
		})
	}
}

func TestCreateRejectsSummoned(t *testing.T) {
	cases := []struct {
		title  string
		mutate func(*v1alpha1.AgentSession)
		want   string
	}{
		{"a summoned local session", func(s *v1alpha1.AgentSession) {
			s.Spec.Mode, s.Spec.Prompt, s.Spec.Limits = v1alpha1.ModeLocal, "", nil
		}, "never local"},
		{"a summoned session on profile full", func(s *v1alpha1.AgentSession) { s.Spec.Profile = "full" }, "never full"},
		{"a summoned session with no profile", func(s *v1alpha1.AgentSession) { s.Spec.Profile = "" }, "names its profile"},
		{"an idempotency key with a slash", func(s *v1alpha1.AgentSession) { s.Spec.IdempotencyKey = "alert/b7baaf6c" }, "spec.idempotencyKey"},
		{"an idempotency key longer than a label value", func(s *v1alpha1.AgentSession) {
			s.Spec.IdempotencyKey = strings.Repeat("a", 64)
		}, "spec.idempotencyKey"},
		{"a caller that is not an object name", func(s *v1alpha1.AgentSession) { s.Spec.Caller = "Alert Responder" }, "spec.caller"},
	}
	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			s := summonedTask()
			c.mutate(s)
			wantInvalid(t, k8s.Create(ctx(t), s), c.want)
		})
	}
}

// TestCreateRejectsMissingFields sends objects a typed client cannot build: no
// spec, required fields left out, and a duration Go would not parse.
func TestCreateRejectsMissingFields(t *testing.T) {
	spec := func() map[string]any {
		return map[string]any{
			"repo":   "haynes-ops",
			"agent":  "claude",
			"mode":   "task",
			"model":  "claude-opus-5-5",
			"prompt": "fix the typo",
		}
	}
	cases := []struct {
		title string
		spec  map[string]any
		want  string
	}{
		{"no spec", nil, "spec: Required value"},
		{"no repo", func() map[string]any { s := spec(); delete(s, "repo"); return s }(), "spec.repo: Required value"},
		{"no agent", func() map[string]any { s := spec(); delete(s, "agent"); return s }(), "spec.agent: Required value"},
		{"no mode", func() map[string]any { s := spec(); delete(s, "mode"); return s }(), "spec.mode: Required value"},
		{"no model", func() map[string]any { s := spec(); delete(s, "model"); return s }(), "spec.model: Required value"},
		{"a timeout in days", func() map[string]any {
			s := spec()
			s["limits"] = map[string]any{"timeout": "3d"}
			return s
		}(), "spec.limits.timeout"},
	}
	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{}}
			u.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("AgentSession"))
			u.SetName(name())
			u.SetNamespace(ns)
			if c.spec != nil {
				u.Object["spec"] = c.spec
			}
			wantInvalid(t, k8s.Create(ctx(t), u), c.want)
		})
	}
}

// TestSpecImmutableExceptOperatingModeAndLifecycle: suspend, resume and timer
// changes are updates; everything else the caller asked for is fixed at create.
func TestSpecImmutableExceptOperatingModeAndLifecycle(t *testing.T) {
	// A changed value is refused at the field; an added or removed field by the
	// presence rule on spec. Both messages say "immutable after create".
	const (
		immutable = "immutable after create"
		presence  = "no spec field may be added or removed"
	)

	cases := []struct {
		title  string
		create func() *v1alpha1.AgentSession
		mutate func(*v1alpha1.AgentSessionSpec)
		want   string // empty: the update is accepted
	}{
		{"suspend", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.OperatingMode = v1alpha1.OperatingModeSuspended }, ""},
		{"set the timers", remoteSession, func(s *v1alpha1.AgentSessionSpec) {
			s.Lifecycle = &v1alpha1.Lifecycle{IdleSuspendAfter: dur(24 * time.Hour)}
		}, ""},
		{"change repo", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Repo = "haynesnetwork" }, "spec.repo"},
		{"change agent", remoteSession, func(s *v1alpha1.AgentSessionSpec) {
			s.Agent, s.Model = v1alpha1.AgentCodex, "gpt-6-astra"
		}, "spec.agent"},
		{"change mode", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Mode = v1alpha1.ModeLocal }, "spec.mode"},
		{"change model", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Model = "claude-opus-5" }, "spec.model"},
		{"change base", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Base = "origin/dev" }, "spec.base"},
		{"change size", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Size = v1alpha1.SizeL }, "spec.size"},
		{"change the prompt", taskSession, func(s *v1alpha1.AgentSessionSpec) { s.Prompt = "something else" }, "spec.prompt"},
		{"change the LLM pool", opencodeSession, func(s *v1alpha1.AgentSessionSpec) { s.LLM.Pool = "llm-big" }, "spec.llm"},
		{"change the profile", opencodeSession, func(s *v1alpha1.AgentSessionSpec) { s.Profile = "full" }, "spec.profile"},
		{"change the effort", summonedTask, func(s *v1alpha1.AgentSessionSpec) { s.Effort = "low" }, "spec.effort"},
		{"change the parent", summonedTask, func(s *v1alpha1.AgentSessionSpec) { s.Parent = "someone-else" }, "spec.parent"},
		{"change the caller", summonedTask, func(s *v1alpha1.AgentSessionSpec) { s.Caller = "upgrade-shepherd" }, "spec.caller"},
		{"change the lane", summonedTask, func(s *v1alpha1.AgentSessionSpec) { s.Lane = v1alpha1.LaneCuration }, "spec.lane"},
		{"change the idempotency key", summonedTask, func(s *v1alpha1.AgentSessionSpec) { s.IdempotencyKey = "c0ffee" }, "spec.idempotencyKey"},
		{"raise the turn cap", summonedTask, func(s *v1alpha1.AgentSessionSpec) { s.Limits.MaxTurns = 500 }, "spec.limits"},
		{"change the tools", func() *v1alpha1.AgentSession {
			s := remoteSession()
			s.Spec.Tools = []string{"blender"}
			return s
		}, func(s *v1alpha1.AgentSessionSpec) { s.Tools = []string{"audio"} }, "spec.tools"},
		{"reorder the tools", func() *v1alpha1.AgentSession {
			s := remoteSession()
			s.Spec.Tools = []string{"blender", "audio"}
			return s
		}, func(s *v1alpha1.AgentSessionSpec) { s.Tools = []string{"audio", "blender"} }, ""},
		{"add an effort", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Effort = "high" }, presence},
		{"add a profile", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Profile = "dev" }, presence},
		{"add a tool", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Tools = []string{"blender"} }, presence},
		{"set a parent", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Parent = "someone-else" }, presence},
		{"drop the idempotency key", summonedTask, func(s *v1alpha1.AgentSessionSpec) { s.IdempotencyKey = "" }, presence},
		{"drop the limits", summonedTask, func(s *v1alpha1.AgentSessionSpec) { s.Limits = nil }, presence},
		{"make Tom's session summoned", remoteSession, func(s *v1alpha1.AgentSessionSpec) {
			s.Caller, s.Lane, s.Profile = "alert-responder", v1alpha1.LaneEscalation, "ops"
		}, presence},
	}
	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			s := c.create()
			if err := k8s.Create(ctx(t), s); err != nil {
				t.Fatalf("create: %v", err)
			}
			c.mutate(&s.Spec)
			err := k8s.Update(ctx(t), s)
			if c.want == "" {
				if err != nil {
					t.Fatalf("update refused: %v", err)
				}
				return
			}
			wantInvalid(t, err, immutable)
			wantInvalid(t, err, c.want)
		})
	}

	t.Run("a spec change bumps the generation", func(t *testing.T) {
		s := remoteSession()
		if err := k8s.Create(ctx(t), s); err != nil {
			t.Fatalf("create: %v", err)
		}
		s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		if err := k8s.Update(ctx(t), s); err != nil {
			t.Fatalf("update: %v", err)
		}
		if s.Generation != 2 {
			t.Errorf("generation = %d after a spec change, want 2", s.Generation)
		}
	})
}

// TestStatusSubresource: status is written only through /status, spec only
// through the object, and the status schema holds too.
func TestStatusSubresource(t *testing.T) {
	s := remoteSession()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatalf("create: %v", err)
	}
	now := metav1.NewTime(time.Now().Truncate(time.Second))

	s.Status = v1alpha1.AgentSessionStatus{
		Phase:    v1alpha1.PhaseRunning,
		Revision: "2.0.3-7f3a9c",
		PodName:  s.Name,
		NodeName: "talosw02",
		Agent:    &v1alpha1.AgentStatus{Status: "busy", LastActivity: &now},
		RemoteControl: &v1alpha1.RemoteControlStatus{
			Name: s.Name, SessionID: "session_test", URL: "https://claude.ai/code/test", State: "registered",
		},
		Outcome: &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeRunning, At: &now},
		Usage:   &v1alpha1.UsageStatus{CostUSD: "0.42", InputTokens: 1200, OutputTokens: 300},
		Rescue:  &v1alpha1.RescueStatus{LastBundle: "rescue/" + s.Name + "/20261006-0130.bundle"},
		Conditions: []metav1.Condition{{
			Type: "Ready", Status: metav1.ConditionTrue, Reason: "PodReady", Message: "agent started",
			LastTransitionTime: now, ObservedGeneration: 1,
		}},
	}
	if err := k8s.Status().Update(ctx(t), s); err != nil {
		t.Fatalf("status update: %v", err)
	}
	if s.Generation != 1 {
		t.Errorf("generation = %d after a status update, want 1", s.Generation)
	}

	t.Run("an object update ignores status", func(t *testing.T) {
		got := get(t, s)
		got.Status.Phase = v1alpha1.PhaseFailed
		got.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		if err := k8s.Update(ctx(t), got); err != nil {
			t.Fatalf("update: %v", err)
		}
		got = get(t, s)
		if got.Status.Phase != v1alpha1.PhaseRunning {
			t.Errorf("phase = %q, want Running: the object endpoint must not write status", got.Status.Phase)
		}
		if got.Spec.OperatingMode != v1alpha1.OperatingModeSuspended || got.Generation != 2 {
			t.Errorf("operatingMode = %q, generation = %d, want Suspended and 2", got.Spec.OperatingMode, got.Generation)
		}
	})

	t.Run("a status update ignores spec", func(t *testing.T) {
		got := get(t, s)
		got.Spec.Repo = "haynesnetwork"
		got.Status.Phase = v1alpha1.PhaseSuspended
		if err := k8s.Status().Update(ctx(t), got); err != nil {
			t.Fatalf("status update: %v", err)
		}
		got = get(t, s)
		if got.Spec.Repo != "haynes-ops" {
			t.Errorf("repo = %q, want haynes-ops: the status endpoint must not write spec", got.Spec.Repo)
		}
		if got.Status.Phase != v1alpha1.PhaseSuspended {
			t.Errorf("phase = %q, want Suspended", got.Status.Phase)
		}
	})

	rejects := []struct {
		title  string
		mutate func(*v1alpha1.AgentSessionStatus)
		want   string
	}{
		{"an unknown phase", func(st *v1alpha1.AgentSessionStatus) { st.Phase = "Sleeping" }, "status.phase: Unsupported value"},
		{"an unknown outcome", func(st *v1alpha1.AgentSessionStatus) { st.Outcome.State = "maybe" }, "status.outcome.state: Unsupported value"},
		{"negative tokens", func(st *v1alpha1.AgentSessionStatus) { st.Usage.InputTokens = -1 }, "status.usage.inputTokens"},
		{"a cost in exponent form", func(st *v1alpha1.AgentSessionStatus) { st.Usage.CostUSD = "1e3" }, "status.usage.costUSD"},
		{"a condition without a reason", func(st *v1alpha1.AgentSessionStatus) { st.Conditions[0].Reason = "" }, "status.conditions[0].reason"},
		{"a condition status that is not True, False or Unknown", func(st *v1alpha1.AgentSessionStatus) {
			st.Conditions[0].Status = "Yes"
		}, "status.conditions[0].status: Unsupported value"},
		{"two conditions of one type", func(st *v1alpha1.AgentSessionStatus) {
			st.Conditions = append(st.Conditions, st.Conditions[0])
		}, "Duplicate value"},
	}
	for _, c := range rejects {
		t.Run("rejects "+c.title, func(t *testing.T) {
			got := get(t, s)
			c.mutate(&got.Status)
			wantInvalid(t, k8s.Status().Update(ctx(t), got), c.want)
		})
	}
}

// TestPrinterColumns reads the object the way `kubectl get` does, as a Table.
func TestPrinterColumns(t *testing.T) {
	s := summonedRemote()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatalf("create: %v", err)
	}
	s.Status.Phase = v1alpha1.PhaseRunning
	s.Status.NodeName = "talosw03"
	s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeRunning}
	if err := k8s.Status().Update(ctx(t), s); err != nil {
		t.Fatalf("status update: %v", err)
	}

	hc, err := rest.HTTPClientFor(restCfg)
	if err != nil {
		t.Fatalf("http client: %v", err)
	}
	url := fmt.Sprintf("%s/apis/%s/%s/namespaces/%s/agentsessions/%s",
		strings.TrimSuffix(restCfg.Host, "/"), v1alpha1.GroupVersion.Group, v1alpha1.GroupVersion.Version, ns, s.Name)
	req, err := http.NewRequestWithContext(ctx(t), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Accept", "application/json;as=Table;v=v1;g=meta.k8s.io")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("get table: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get table: HTTP %d", resp.StatusCode)
	}
	var table metav1.Table
	if err := json.NewDecoder(resp.Body).Decode(&table); err != nil {
		t.Fatalf("decode table: %v", err)
	}

	type col struct {
		name     string
		priority int32
		value    any
	}
	want := []col{
		{"Name", 0, s.Name},
		{"Repo", 0, "haynes-ops"},
		{"Agent", 0, "claude"},
		{"Mode", 0, "remote"},
		{"Phase", 0, "Running"},
		{"Node", 0, "talosw03"},
		{"Model", 1, "claude-fable-5-1"},
		{"Lane", 1, "escalation"},
		{"Outcome", 1, "running"},
		{"Age", 0, nil}, // a duration that depends on the clock
	}
	if len(table.ColumnDefinitions) != len(want) || len(table.Rows) != 1 || len(table.Rows[0].Cells) != len(want) {
		t.Fatalf("table has %d columns and %d rows, want %d columns and 1 row: %+v",
			len(table.ColumnDefinitions), len(table.Rows), len(want), table.ColumnDefinitions)
	}
	for i, w := range want {
		def := table.ColumnDefinitions[i]
		if def.Name != w.name || def.Priority != w.priority {
			t.Errorf("column %d = %s (priority %d), want %s (priority %d)", i, def.Name, def.Priority, w.name, w.priority)
		}
		if w.value != nil && table.Rows[0].Cells[i] != w.value {
			t.Errorf("column %s = %v, want %v", w.name, table.Rows[0].Cells[i], w.value)
		}
	}
}

func get(t *testing.T, s *v1alpha1.AgentSession) *v1alpha1.AgentSession {
	t.Helper()
	got := &v1alpha1.AgentSession{}
	if err := k8s.Get(ctx(t), client.ObjectKeyFromObject(s), got); err != nil {
		t.Fatalf("get: %v", err)
	}
	return got
}
