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
	"unicode/utf8"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
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

func validCallerPolicy() *v1alpha1.CallerPolicy {
	return &v1alpha1.CallerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name(), Namespace: ns},
		Spec: v1alpha1.CallerPolicySpec{
			ServiceAccount: "dev-env-system/dev-env-meatball", Mode: "task", Profile: "dev",
			Models:        []string{"claude-opus-5-5", "gpt-6-astra", "claude-fable-5-2"},
			MaxConcurrent: 2, MaxTimeout: "4h", MaxWorkers: 4,
		},
	}
}

func TestCallerPolicyValidation(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "gpt-6-astra", "claude-fable-5-2"} {
		t.Run("accept "+model, func(t *testing.T) {
			p := validCallerPolicy()
			p.Spec.Models = []string{model}
			p.Spec.MaxWorkers, p.Spec.MaxConcurrent = 1, 1
			p.Spec.MaxTimeout = strings.Repeat("0", 30) + "1s" // 32-character bound, positive.
			if err := k8s.Create(ctx(t), p); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := k8s.Create(ctx(t), validCallerPolicy()); err != nil {
		t.Fatalf("accept all models and upper bounds: %v", err)
	}
	cases := []struct {
		name, field string
		mutate      func(*v1alpha1.CallerPolicySpec)
	}{
		{"other account", "serviceAccount", func(s *v1alpha1.CallerPolicySpec) { s.ServiceAccount = "dev-env-system/other" }},
		{"long account", "serviceAccount", func(s *v1alpha1.CallerPolicySpec) { s.ServiceAccount = strings.Repeat("a", 129) }},
		{"non-task", "mode", func(s *v1alpha1.CallerPolicySpec) { s.Mode = "local" }},
		{"wrong profile", "profile", func(s *v1alpha1.CallerPolicySpec) { s.Profile = "ops" }},
		{"long profile", "profile", func(s *v1alpha1.CallerPolicySpec) { s.Profile = "workbench" }},
		{"no models", "models", func(s *v1alpha1.CallerPolicySpec) { s.Models = []string{} }},
		{"wrong model", "models", func(s *v1alpha1.CallerPolicySpec) { s.Models = []string{"claude-sonnet-5-5"} }},
		{"long model", "models", func(s *v1alpha1.CallerPolicySpec) { s.Models = []string{strings.Repeat("a", 65)} }},
		{"too many models", "models", func(s *v1alpha1.CallerPolicySpec) { s.Models = append(s.Models, "claude-sonnet-5-5") }},
		{"duplicate models", "models", func(s *v1alpha1.CallerPolicySpec) { s.Models = []string{"gpt-6-astra", "gpt-6-astra"} }},
		{"zero concurrent", "maxConcurrent", func(s *v1alpha1.CallerPolicySpec) { s.MaxConcurrent = 0 }},
		{"three concurrent", "maxConcurrent", func(s *v1alpha1.CallerPolicySpec) { s.MaxConcurrent = 3 }},
		{"zero workers", "maxWorkers", func(s *v1alpha1.CallerPolicySpec) { s.MaxWorkers = 0 }},
		{"five workers", "maxWorkers", func(s *v1alpha1.CallerPolicySpec) { s.MaxWorkers = 5 }},
		{"too long timeout", "maxTimeout", func(s *v1alpha1.CallerPolicySpec) { s.MaxTimeout = "4h1ns" }},
		{"zero timeout", "maxTimeout", func(s *v1alpha1.CallerPolicySpec) { s.MaxTimeout = "0s" }},
		{"negative timeout", "maxTimeout", func(s *v1alpha1.CallerPolicySpec) { s.MaxTimeout = "-1s" }},
		{"invalid timeout", "maxTimeout", func(s *v1alpha1.CallerPolicySpec) { s.MaxTimeout = "four hours" }},
		{"long timeout string", "maxTimeout", func(s *v1alpha1.CallerPolicySpec) { s.MaxTimeout = strings.Repeat("0", 31) + "1s" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validCallerPolicy()
			tc.mutate(&p.Spec)
			wantInvalid(t, k8s.Create(ctx(t), p), tc.field)
		})
	}
	for _, field := range []string{"serviceAccount", "mode", "profile", "models", "maxConcurrent", "maxTimeout", "maxWorkers"} {
		t.Run("required "+field, func(t *testing.T) {
			p := validCallerPolicy()
			p.APIVersion, p.Kind = v1alpha1.GroupVersion.String(), "CallerPolicy"
			u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(p)
			if err != nil {
				t.Fatal(err)
			}
			unstructured.RemoveNestedField(u, "spec", field)
			wantInvalid(t, k8s.Create(ctx(t), &unstructured.Unstructured{Object: u}), field)
		})
	}
	t.Run("required spec", func(t *testing.T) {
		p := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": v1alpha1.GroupVersion.String(), "kind": "CallerPolicy",
			"metadata": map[string]any{"name": name(), "namespace": ns},
		}}
		wantInvalid(t, k8s.Create(ctx(t), p), "spec")
	})
}

func TestCallerPolicyWorkerLimitValidation(t *testing.T) {
	for _, workers := range []int32{1, 4, 0, 5} {
		s := taskSession()
		s.Spec.Limits = &v1alpha1.SessionLimits{MaxWorkers: &workers}
		err := k8s.Create(ctx(t), s)
		if workers == 0 || workers == 5 {
			wantInvalid(t, err, "maxWorkers")
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		original := s.DeepCopy()
		if err := k8s.Update(ctx(t), s); err != nil {
			t.Fatalf("unchanged cap: %v", err)
		}
		*original.Spec.Limits.MaxWorkers = 2
		original.ResourceVersion = s.ResourceVersion
		wantInvalid(t, k8s.Update(ctx(t), original), "immutable after create")
	}
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
	s.Spec.Summon = remediationSummon()
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
	s.Spec.Summon = escalationSummon()
	return s
}

// remediationSummon is the snapshot a remediation carries (DESIGN-064 6.4).
func remediationSummon() *v1alpha1.SessionSummon {
	return &v1alpha1.SessionSummon{
		DispatchID:    "dispatch-rem",
		Priority:      v1alpha1.SummonPriorityUrgent,
		FallbackModel: "claude-opus-5",
		OnUnreported:  v1alpha1.SummonOnUnreportedEscalated,
		MaxHold:       "20m",
		Escalation: &v1alpha1.SessionSummonEscalation{
			Kind:         v1alpha1.SummonKindRemote,
			Lane:         v1alpha1.LaneEscalation,
			Repo:         "haynes-ops",
			Profile:      "ops",
			Model:        "claude-sonnet-5-5",
			Fallback:     "claude-opus-5",
			Effort:       "xhigh",
			OnUnreported: v1alpha1.SummonOnUnreportedFailed,
		},
	}
}

// escalationSummon is the snapshot an escalation carries. It has no maxHold
// and no nested escalation entry.
func escalationSummon() *v1alpha1.SessionSummon {
	return &v1alpha1.SessionSummon{
		DispatchID:    "dispatch-esc",
		Priority:      v1alpha1.SummonPriorityUrgent,
		FallbackModel: "claude-opus-5",
		OnUnreported:  v1alpha1.SummonOnUnreportedFailed,
	}
}

// cursorSession is a cursor task (DESIGN-058): a Cursor model id, no effort.
func cursorSession() *v1alpha1.AgentSession {
	s := taskSession()
	s.Spec.Agent = v1alpha1.AgentCursor
	s.Spec.Model = "grok-code-fast-1"
	s.Spec.Profile = "dev"
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
			s.Spec.Restore = "haynes-ops-1005-195501/20261005-2010-2"
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
		"a cursor task":                   cursorSession(),
		"a summoned remediation task":     summonedTask(),
		"a summoned escalation":           summonedRemote(),
		"a summoned remote session with a prompt": func() *v1alpha1.AgentSession {
			s := summonedRemote()
			s.Spec.Prompt = "Escalation: Tom will join this session."
			return s
		}(),
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
		{"a restore that is a path", func(s *v1alpha1.AgentSession) { s.Spec.Restore = "../20261008-0024" }, "spec.restore"},
		{"a restore without a stamp", func(s *v1alpha1.AgentSession) { s.Spec.Restore = "dev-env-1008-001530" }, "spec.restore"},
		{"a restore with a third part", func(s *v1alpha1.AgentSession) { s.Spec.Restore = "a/20261008-0024/x" }, "spec.restore"},

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
		{"a prompt in remote mode", func(s *v1alpha1.AgentSession) { s.Spec.Mode = v1alpha1.ModeRemote }, "only for a summoned session"},
		{"a prompt in local mode", func(s *v1alpha1.AgentSession) { s.Spec.Mode = v1alpha1.ModeLocal }, "not allowed in local mode"},
		{"summon without a caller", func(s *v1alpha1.AgentSession) { s.Spec.Summon = escalationSummon() }, "exactly when caller is"},
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
		{"a cursor session in local mode", func(s *v1alpha1.AgentSession) {
			s.Spec.Agent, s.Spec.Model, s.Spec.Mode, s.Spec.Prompt = v1alpha1.AgentCursor, "grok-code-fast-1", v1alpha1.ModeLocal, ""
		}, "a cursor session runs in task mode only"},
		{"a cursor session in remote mode", func(s *v1alpha1.AgentSession) {
			s.Spec.Agent, s.Spec.Model, s.Spec.Mode, s.Spec.Prompt = v1alpha1.AgentCursor, "grok-code-fast-1", v1alpha1.ModeRemote, ""
		}, "a cursor session runs in task mode only"},
		{"a cursor task with an effort", func(s *v1alpha1.AgentSession) {
			s.Spec.Agent, s.Spec.Model, s.Spec.Effort = v1alpha1.AgentCursor, "grok-code-fast-1", "high"
		}, "Cursor encodes effort in the model id"},
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
		{"a summoned session on profile workbench", func(s *v1alpha1.AgentSession) { s.Spec.Profile = "workbench" }, "never workbench"},
		{"a summoned session on codex", func(s *v1alpha1.AgentSession) { s.Spec.Agent = v1alpha1.AgentCodex }, "agent claude only"},
		{"a caller without summon", func(s *v1alpha1.AgentSession) { s.Spec.Summon = nil }, "exactly when caller is"},
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

// TestSummonSnapshotRules proves the spec.summon field rules from DESIGN-064 6.4.
func TestSummonSnapshotRules(t *testing.T) {
	upgrade := func() *v1alpha1.AgentSession {
		s := summonedRemote()
		s.Name = "wo-" + name()
		s.Spec.Caller = "upgrade-shepherd"
		s.Spec.Lane = v1alpha1.LaneUpgrade
		s.Spec.IdempotencyKey = ""
		s.Spec.Summon = &v1alpha1.SessionSummon{
			DispatchID:    "dispatch-upgrade",
			Priority:      v1alpha1.SummonPriorityNormal,
			FallbackModel: "claude-opus-5",
			OnUnreported:  v1alpha1.SummonOnUnreportedFailed,
			StaleAfter:    "180m",
		}
		return s
	}
	curation := func() *v1alpha1.AgentSession {
		s := summonedTask()
		s.Name = "wo-cigar-curate-" + name()
		s.Spec.Lane = v1alpha1.LaneCuration
		s.Spec.Summon = &v1alpha1.SessionSummon{
			DispatchID:    "dispatch-curation",
			Priority:      v1alpha1.SummonPriorityBulk,
			FallbackModel: "claude-opus-5",
			OnUnreported:  v1alpha1.SummonOnUnreportedFailed,
			MaxHold:       "12h",
		}
		return s
	}

	accepts := []struct {
		title string
		build func() *v1alpha1.AgentSession
	}{
		{"upgrade with staleAfter", upgrade},
		{"upgrade without staleAfter", func() *v1alpha1.AgentSession {
			s := upgrade()
			s.Spec.Summon.StaleAfter = ""
			return s
		}},
		{"upgrade staleAfter at 24h", func() *v1alpha1.AgentSession {
			s := upgrade()
			s.Spec.Summon.StaleAfter = "24h"
			return s
		}},
		{"curation with maxHold", curation},
		{"remediation maxHold at 24h", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.MaxHold = "24h"
			return s
		}},
		{"filing dispatch id", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.DispatchID = "filing:01234567-89ab-cdef-0123-456789abcdef"
			return s
		}},
		{"hex dispatch id", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.DispatchID = strings.Repeat("ab", 32)
			return s
		}},
	}
	for _, c := range accepts {
		t.Run("accept "+c.title, func(t *testing.T) {
			if err := k8s.Create(ctx(t), c.build()); err != nil {
				t.Fatal(err)
			}
		})
	}

	refuses := []struct {
		title string
		build func() *v1alpha1.AgentSession
		want  string
	}{
		{"staleAfter on a remediation", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.StaleAfter = "180m"
			return s
		}, "remote upgrade session only"},
		{"staleAfter on an escalation", func() *v1alpha1.AgentSession {
			s := summonedRemote()
			s.Spec.Summon.StaleAfter = "180m"
			return s
		}, "remote upgrade session only"},
		{"staleAfter on a task upgrade", func() *v1alpha1.AgentSession {
			s := upgrade()
			s.Spec.Mode = v1alpha1.ModeTask
			s.Spec.Prompt = "bump the chart"
			s.Spec.Limits = &v1alpha1.SessionLimits{Timeout: dur(time.Hour)}
			return s
		}, "remote upgrade session only"},
		{"staleAfter of zero", func() *v1alpha1.AgentSession {
			s := upgrade()
			s.Spec.Summon.StaleAfter = "0s"
			return s
		}, "positive Go duration"},
		{"staleAfter over 24h", func() *v1alpha1.AgentSession {
			s := upgrade()
			s.Spec.Summon.StaleAfter = "24h1s"
			return s
		}, "at most 24h"},
		{"maxHold on an upgrade", func() *v1alpha1.AgentSession {
			s := upgrade()
			s.Spec.Summon.MaxHold = "20m"
			return s
		}, "not allowed for upgrade or escalation"},
		{"remediation without maxHold", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.MaxHold = ""
			return s
		}, "required for remediation and curation"},
		{"curation without maxHold", func() *v1alpha1.AgentSession {
			s := curation()
			s.Spec.Summon.MaxHold = ""
			return s
		}, "required for remediation and curation"},
		{"maxHold over 24h", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.MaxHold = "24h1s"
			return s
		}, "at most 24h"},
		{"escalation copy on an escalation", func() *v1alpha1.AgentSession {
			s := summonedRemote()
			s.Spec.Summon.Escalation = remediationSummon().Escalation
			return s
		}, "present exactly for a remediation"},
		{"remediation without an escalation copy", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.Escalation = nil
			return s
		}, "present exactly for a remediation"},
		{"onUnreported failed on a remediation", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.OnUnreported = v1alpha1.SummonOnUnreportedFailed
			return s
		}, "escalated for remediation"},
		{"onUnreported escalated on an upgrade", func() *v1alpha1.AgentSession {
			s := upgrade()
			s.Spec.Summon.OnUnreported = v1alpha1.SummonOnUnreportedEscalated
			return s
		}, "failed for every other lane"},
		{"priority normal on a remediation", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.Priority = v1alpha1.SummonPriorityNormal
			return s
		}, "urgent for lanes remediation and escalation"},
		{"escalation fallback equal to the model", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.Escalation.Fallback = s.Spec.Summon.Escalation.Model
			return s
		}, "fallback that differs"},
		{"escalation copy with kind task", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.Escalation.Kind = v1alpha1.SummonKindTask
			return s
		}, "kind remote"},
		{"escalation copy with lane upgrade", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.Escalation.Lane = v1alpha1.LaneUpgrade
			return s
		}, "lane escalation"},
		{"escalation profile dev", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.Escalation.Profile = "dev"
			return s
		}, "profile"},
		{"escalation onUnreported escalated", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.Escalation.OnUnreported = v1alpha1.SummonOnUnreportedEscalated
			return s
		}, "onUnreported failed"},
		{"fallback model alias", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.FallbackModel = "opus"
			return s
		}, "fallbackModel"},
		{"empty dispatch id", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.DispatchID = ""
			return s
		}, "dispatchID"},
		{"dispatch id with a slash", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.DispatchID = "abc/def"
			return s
		}, "dispatchID"},
		{"dispatch id over 128 characters", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.Summon.DispatchID = strings.Repeat("a", 129)
			return s
		}, "dispatchID"},
	}
	for _, c := range refuses {
		t.Run("refuse "+c.title, func(t *testing.T) {
			wantInvalid(t, k8s.Create(ctx(t), c.build()), c.want)
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
		{"suspend a summoned session", summonedTask, func(s *v1alpha1.AgentSessionSpec) { s.OperatingMode = v1alpha1.OperatingModeSuspended }, ""},
		{"change the summon dispatch id", summonedTask, func(s *v1alpha1.AgentSessionSpec) { s.Summon.DispatchID = "other-id" }, "spec.summon"},
		{"remove summon", summonedTask, func(s *v1alpha1.AgentSessionSpec) { s.Summon = nil }, presence},
		{"add summon", taskSession, func(s *v1alpha1.AgentSessionSpec) { s.Summon = escalationSummon() }, presence},
		{"set the timers", remoteSession, func(s *v1alpha1.AgentSessionSpec) {
			s.Lifecycle = &v1alpha1.Lifecycle{IdleSuspendAfter: dur(24 * time.Hour)}
		}, ""},
		{"change repo", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Repo = "haynesnetwork" }, "spec.repo"},
		{"add a workspace", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"} }, presence},
		{"change workspace", func() *v1alpha1.AgentSession {
			s := remoteSession()
			s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"}
			return s
		}, func(s *v1alpha1.AgentSessionSpec) { s.Workspace.ID = "foreign" }, "spec.workspace"},
		{"remove workspace", func() *v1alpha1.AgentSession {
			s := remoteSession()
			s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"}
			return s
		}, func(s *v1alpha1.AgentSessionSpec) { s.Workspace = nil }, presence},
		{"change agent", remoteSession, func(s *v1alpha1.AgentSessionSpec) {
			s.Agent, s.Model = v1alpha1.AgentCodex, "gpt-6-astra"
		}, "spec.agent"},
		{"change mode", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Mode = v1alpha1.ModeLocal }, "spec.mode"},
		{"change model", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Model = "claude-opus-5" }, "spec.model"},
		{"change title", func() *v1alpha1.AgentSession {
			s := remoteSession()
			s.Spec.Title = "Main coordinator"
			return s
		}, func(s *v1alpha1.AgentSessionSpec) { s.Title = "Ops migration" }, ""},
		{"add title", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Title = "Main coordinator" }, ""},
		{"remove title", func() *v1alpha1.AgentSession {
			s := remoteSession()
			s.Spec.Title = "Main coordinator"
			return s
		}, func(s *v1alpha1.AgentSessionSpec) { s.Title = "" }, ""},
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
		{"change the restore", func() *v1alpha1.AgentSession {
			s := remoteSession()
			s.Spec.Restore = "dev-env-1008-001530/20261008-0024"
			return s
		}, func(s *v1alpha1.AgentSessionSpec) { s.Restore = "dev-env-1008-001530/20261008-0025" }, "spec.restore"},
		{"add a restore", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Restore = "dev-env-1008-001530/20261008-0024" }, presence},
		{"add an effort", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Effort = "high" }, presence},
		{"add a profile", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Profile = "dev" }, presence},
		{"add a tool", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Tools = []string{"blender"} }, presence},
		{"set a parent", remoteSession, func(s *v1alpha1.AgentSessionSpec) { s.Parent = "someone-else" }, presence},
		{"change coordinator instruction parent", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.CoordinatorInstructionParentUID = "parent-uid"
			return s
		}, func(s *v1alpha1.AgentSessionSpec) { s.CoordinatorInstructionParentUID = "other-uid" }, "coordinator instruction parent UID"},
		{"drop coordinator instruction parent", func() *v1alpha1.AgentSession {
			s := summonedTask()
			s.Spec.CoordinatorInstructionParentUID = "parent-uid"
			return s
		}, func(s *v1alpha1.AgentSessionSpec) { s.CoordinatorInstructionParentUID = "" }, presence},
		{"add coordinator instruction parent", summonedTask, func(s *v1alpha1.AgentSessionSpec) {
			s.CoordinatorInstructionParentUID = "parent-uid"
		}, presence},
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

// TestAgentPodUIDLength is the schema rule for status.agent.podUID: 64
// characters are accepted and 65 are refused.
func TestAgentPodUIDLength(t *testing.T) {
	s := remoteSession()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	s.Status.Phase = v1alpha1.PhaseRunning
	s.Status.Agent = &v1alpha1.AgentStatus{PodUID: strings.Repeat("a", 64)}
	if err := k8s.Status().Update(ctx(t), s); err != nil {
		t.Fatalf("64 characters: %v", err)
	}
	if got := get(t, s).Status.Agent; got == nil || got.PodUID != strings.Repeat("a", 64) {
		t.Fatalf("podUID %+v", got)
	}
	got := get(t, s)
	got.Status.Agent.PodUID = strings.Repeat("b", 65)
	wantInvalid(t, k8s.Status().Update(ctx(t), got), "status.agent.podUID")
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
		Rescue: &v1alpha1.RescueStatus{LastBundle: "rescue/" + s.Name + "/20261006-0130.bundle",
			Result: v1alpha1.RescueFailed, TransientFailures: 2, NextRetryAt: &now},
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
	// The transient retry's fields are kept, not pruned (#486).
	if r := get(t, s).Status.Rescue; r.TransientFailures != 2 || r.NextRetryAt == nil || !r.NextRetryAt.Equal(&now) {
		t.Errorf("rescue %+v, want the transient retry's fields kept", r)
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
		{"a negative transient failure count", func(st *v1alpha1.AgentSessionStatus) { st.Rescue.TransientFailures = -1 }, "status.rescue.transientFailures"},
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

func TestSharedAdmissionEvidenceIsDurableAndOneWay(t *testing.T) {
	s := taskSession()
	s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"}
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	s.Status.SharedAdmission = &v1alpha1.SharedAdmissionStatus{Version: 1, SessionUID: string(s.UID), State: "NeverStarted"}
	if err := k8s.Status().Update(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	got := get(t, s)
	if got.Status.SharedAdmission == nil || *got.Status.SharedAdmission != *s.Status.SharedAdmission {
		t.Fatal("admission evidence was pruned")
	}
	got.Status.SharedAdmission.State = "Started"
	if err := k8s.Status().Update(ctx(t), got); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*v1alpha1.AgentSessionStatus)
	}{
		{"remove marker", func(st *v1alpha1.AgentSessionStatus) { st.SharedAdmission = nil }},
		{"reverse Started", func(st *v1alpha1.AgentSessionStatus) { st.SharedAdmission.State = "NeverStarted" }},
		{"change Session UID", func(st *v1alpha1.AgentSessionStatus) { st.SharedAdmission.SessionUID = "other-session" }},
		{"unknown version", func(st *v1alpha1.AgentSessionStatus) { st.SharedAdmission.Version = 2 }},
		{"unknown state", func(st *v1alpha1.AgentSessionStatus) { st.SharedAdmission.State = "EmptyHome" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := get(t, s)
			tc.edit(&bad.Status)
			if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
				t.Fatalf("invalid admission transition accepted: %v", err)
			}
		})
	}
	got = get(t, s)
	if err := k8s.Status().Patch(ctx(t), got, client.RawPatch(types.JSONPatchType, []byte(`[{"op":"remove","path":"/status"}]`))); !apierrors.IsInvalid(err) {
		t.Fatalf("dropping the entire status erased recorded admission: %v", err)
	}
}

func TestSharedRetentionEvidenceSurvivesTheStatusSchema(t *testing.T) {
	s := taskSession()
	s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"}
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	now := metav1.NewTime(time.Now().Truncate(time.Second))
	p := &v1alpha1.SharedRescueProof{Version: 1, Workspace: s.Spec.Workspace.ID, Task: s.Name, Repo: s.Spec.Repo,
		SessionUID: string(s.UID), SourcePodUID: "executor", PrivateHomeUID: "original-home", WriterGeneration: 7, Kind: "TaskWorkPreserved",
		Manifest: "rescue/" + s.Name + "/20261009-1200/manifest.json"}
	s.Status.SharedPrivateHomeUID = p.PrivateHomeUID
	s.Status.Rescue = &v1alpha1.RescueStatus{SharedProof: p, Generation: s.Generation, SourcePodUID: p.SourcePodUID, PodUID: "hold",
		Result: v1alpha1.RescueVerified, Stamp: "20261009-1200", At: &now, LastBundle: p.Manifest}
	if err := k8s.Status().Update(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	got := get(t, s)
	if got.Status.SharedPrivateHomeUID != p.PrivateHomeUID || got.Status.Rescue.SharedProof == nil || *got.Status.Rescue.SharedProof != *p {
		t.Fatalf("retention evidence was pruned: %+v", got.Status)
	}
	for _, tc := range []struct {
		name string
		edit func(*v1alpha1.SharedRescueProof)
	}{
		{"unknown proof version", func(p *v1alpha1.SharedRescueProof) { p.Version = 2 }},
		{"unknown preservation result", func(p *v1alpha1.SharedRescueProof) { p.Kind = "EmptyHome" }},
		{"negative writer generation", func(p *v1alpha1.SharedRescueProof) { p.WriterGeneration = -1 }},
		{"unbounded manifest", func(p *v1alpha1.SharedRescueProof) { p.Manifest = strings.Repeat("a", 513) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := get(t, s)
			tc.edit(bad.Status.Rescue.SharedProof)
			if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
				t.Fatalf("unsupported evidence accepted: %v", err)
			}
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
		{"Task", 0, nil}, // a remote session has no task result (DESIGN-021)
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

// status.agent.projectBootstrap accepts every closed-set code agentd sends
// and refuses anything outside them, so names and codes are all it can hold
// (DESIGN-006, reason codes).
func TestProjectBootstrapStatusSchemaAcceptsOnlyNamesAndCodes(t *testing.T) {
	s := taskSession()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	pb := &v1alpha1.ProjectBootstrapStatus{State: "Failed", Reason: "TimedOut", CatalogRevision: strings.Repeat("a", 64)}
	for i, reason := range protocol.RepositoryReasons {
		step := protocol.BootstrapSteps[i%len(protocol.BootstrapSteps)]
		pb.Repositories = append(pb.Repositories, v1alpha1.ProjectBootstrapRepositoryStatus{Name: fmt.Sprintf("repo-%d", i), State: "Preserved", Step: step, Reason: reason})
	}
	pb.Repositories = append(pb.Repositories, v1alpha1.ProjectBootstrapRepositoryStatus{Name: strings.Repeat("r", protocol.MaxBootstrapNameLength), State: "Ready"})
	for i, reason := range protocol.RootReasons {
		cause := protocol.RepositoryReasons[i%len(protocol.RepositoryReasons)]
		pb.Projects = append(pb.Projects, v1alpha1.ProjectBootstrapProjectStatus{Name: fmt.Sprintf("project-%d", i), State: "Failed", Reason: reason, Repository: "repo-0", Cause: cause})
	}
	pb.Projects = append(pb.Projects, v1alpha1.ProjectBootstrapProjectStatus{Name: "ready", State: "Ready"})
	s.Status.Agent = &v1alpha1.AgentStatus{Status: "busy", ProjectBootstrap: pb}
	if err := k8s.Status().Update(ctx(t), s); err != nil {
		t.Fatal("every closed-set code refused: ", err)
	}
	for _, state := range protocol.BootstrapStates {
		got := get(t, s)
		got.Status.Agent.ProjectBootstrap.State = state
		if err := k8s.Status().Update(ctx(t), got); err != nil {
			t.Fatalf("state %s refused: %v", state, err)
		}
	}
	for _, reason := range protocol.CatalogReasons {
		got := get(t, s)
		got.Status.Agent.ProjectBootstrap.Reason = reason
		if err := k8s.Status().Update(ctx(t), got); err != nil {
			t.Fatalf("catalog reason %s refused: %v", reason, err)
		}
	}
	saved := get(t, s)
	if saved.Status.Agent.ProjectBootstrap == nil || len(saved.Status.Agent.ProjectBootstrap.Repositories) != len(pb.Repositories) || saved.Status.Agent.ProjectBootstrap.Projects[0] != pb.Projects[0] {
		t.Fatal("schema pruned the bootstrap report")
	}
	// A full catalog's report fits: the lists hold exactly the protocol's
	// bounds, which the API server's tests hold to the catalog's limits.
	full := get(t, s)
	repositories, projects := full.Status.Agent.ProjectBootstrap.Repositories, full.Status.Agent.ProjectBootstrap.Projects
	for i := len(repositories); i < protocol.MaxBootstrapRepositories; i++ {
		repositories = append(repositories, v1alpha1.ProjectBootstrapRepositoryStatus{Name: fmt.Sprintf("full-%d", i), State: "Ready"})
	}
	for i := len(projects); i < protocol.MaxBootstrapProjects; i++ {
		projects = append(projects, v1alpha1.ProjectBootstrapProjectStatus{Name: fmt.Sprintf("full-%d", i), State: "Ready"})
	}
	full.Status.Agent.ProjectBootstrap.Repositories, full.Status.Agent.ProjectBootstrap.Projects = repositories, projects
	if err := k8s.Status().Update(ctx(t), full); err != nil {
		t.Fatal("a report at the bounds refused: ", err)
	}
	rejects := []struct {
		title  string
		mutate func(*v1alpha1.ProjectBootstrapStatus)
		want   string
	}{
		{"an unknown state", func(p *v1alpha1.ProjectBootstrapStatus) { p.State = "Started" }, "projectBootstrap.state: Unsupported value"},
		{"no state", func(p *v1alpha1.ProjectBootstrapStatus) { p.State = "" }, "projectBootstrap.state"},
		{"an unknown catalog reason", func(p *v1alpha1.ProjectBootstrapStatus) { p.Reason = "fetch failed: /home/dev" }, "projectBootstrap.reason: Unsupported value"},
		{"a commit for a revision", func(p *v1alpha1.ProjectBootstrapStatus) { p.CatalogRevision = strings.Repeat("a", 40) }, "projectBootstrap.catalogRevision"},
		{"an uppercase revision", func(p *v1alpha1.ProjectBootstrapStatus) { p.CatalogRevision = strings.Repeat("A", 64) }, "projectBootstrap.catalogRevision"},
		{"a path for a repository name", func(p *v1alpha1.ProjectBootstrapStatus) { p.Repositories[0].Name = "owner/demo" }, "projectBootstrap.repositories[0].name"},
		{"a repository name past the bound", func(p *v1alpha1.ProjectBootstrapStatus) {
			p.Repositories[0].Name = strings.Repeat("r", protocol.MaxBootstrapNameLength+1)
		}, "projectBootstrap.repositories[0].name"},
		{"no repository name", func(p *v1alpha1.ProjectBootstrapStatus) { p.Repositories[0].Name = "" }, "projectBootstrap.repositories[0].name"},
		{"an unknown repository state", func(p *v1alpha1.ProjectBootstrapStatus) { p.Repositories[0].State = "Failed" }, "projectBootstrap.repositories[0].state: Unsupported value"},
		{"an unknown step", func(p *v1alpha1.ProjectBootstrapStatus) { p.Repositories[0].Step = "Checkout" }, "projectBootstrap.repositories[0].step: Unsupported value"},
		{"detail text for a reason", func(p *v1alpha1.ProjectBootstrapStatus) {
			p.Repositories[0].Reason = "tracked files differ from the index; preserved"
		}, "projectBootstrap.repositories[0].reason: Unsupported value"},
		{"a root reason on a repository", func(p *v1alpha1.ProjectBootstrapStatus) { p.Repositories[0].Reason = "AnchorUnproven" }, "projectBootstrap.repositories[0].reason: Unsupported value"},
		{"repositories past the bound", func(p *v1alpha1.ProjectBootstrapStatus) {
			p.Repositories = nil
			for i := 0; i <= protocol.MaxBootstrapRepositories; i++ {
				p.Repositories = append(p.Repositories, v1alpha1.ProjectBootstrapRepositoryStatus{Name: fmt.Sprintf("r-%d", i), State: "Ready"})
			}
		}, "projectBootstrap.repositories: Too many"},
		{"an unknown project state", func(p *v1alpha1.ProjectBootstrapStatus) { p.Projects[0].State = "Preserved" }, "projectBootstrap.projects[0].state: Unsupported value"},
		{"a repository reason on a project", func(p *v1alpha1.ProjectBootstrapStatus) { p.Projects[0].Reason = "UntrackedFiles" }, "projectBootstrap.projects[0].reason: Unsupported value"},
		{"an unknown cause", func(p *v1alpha1.ProjectBootstrapStatus) { p.Projects[0].Cause = "RootUnproven" }, "projectBootstrap.projects[0].cause: Unsupported value"},
		{"a path for a member repository", func(p *v1alpha1.ProjectBootstrapStatus) { p.Projects[0].Repository = "../demo" }, "projectBootstrap.projects[0].repository"},
		{"projects past the bound", func(p *v1alpha1.ProjectBootstrapStatus) {
			p.Projects = nil
			for i := 0; i <= protocol.MaxBootstrapProjects; i++ {
				p.Projects = append(p.Projects, v1alpha1.ProjectBootstrapProjectStatus{Name: fmt.Sprintf("p-%d", i), State: "Ready"})
			}
		}, "projectBootstrap.projects: Too many"},
	}
	for _, c := range rejects {
		t.Run("rejects "+c.title, func(t *testing.T) {
			got := get(t, s)
			c.mutate(got.Status.Agent.ProjectBootstrap)
			wantInvalid(t, k8s.Status().Update(ctx(t), got), c.want)
		})
	}
}

func TestDecisionAuthorityStatusSchemaIsBoundedAndRetainsProvenance(t *testing.T) {
	s := taskSession()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	valid := v1alpha1.DecisionAnswerStatus{Version: 1, Session: s.Name, SessionUID: string(s.UID), PodUID: "synthetic-pod-uid", DecisionID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", ThreadID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", WriterGeneration: 3, Digest: strings.Repeat("a", 64), Phase: "Reserved"}
	s.Status.DecisionAnswer = &valid
	if err := k8s.Status().Update(ctx(t), s); err != nil {
		t.Fatal("bounded reservation refused", err)
	}
	s.Status.DecisionAnswer.Phase = "Confirmed"
	if err := k8s.Status().Update(ctx(t), s); err != nil {
		t.Fatal("bounded confirmation refused", err)
	}
	var saved v1alpha1.AgentSession
	if err := k8s.Get(ctx(t), client.ObjectKeyFromObject(s), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status.DecisionAnswer == nil || *saved.Status.DecisionAnswer != *s.Status.DecisionAnswer {
		t.Fatal("schema pruned authoritative identity/digest")
	}
	for _, change := range []string{"version", "session", "session-uid", "pod-uid", "decision", "thread", "generation", "digest", "phase"} {
		t.Run(change, func(t *testing.T) {
			bad := saved.DeepCopy()
			switch change {
			case "version":
				bad.Status.DecisionAnswer.Version = 2
			case "session":
				bad.Status.DecisionAnswer.Session = strings.Repeat("s", 64)
			case "session-uid":
				bad.Status.DecisionAnswer.SessionUID = strings.Repeat("s", 129)
			case "pod-uid":
				bad.Status.DecisionAnswer.PodUID = ""
			case "decision":
				bad.Status.DecisionAnswer.DecisionID = "malformed"
			case "thread":
				bad.Status.DecisionAnswer.ThreadID = "malformed"
			case "generation":
				bad.Status.DecisionAnswer.WriterGeneration = 0
			case "digest":
				bad.Status.DecisionAnswer.Digest = strings.Repeat("x", 64)
			case "phase":
				bad.Status.DecisionAnswer.Phase = "LocallyApproved"
			}
			if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
				t.Fatal("schema accepted invalid provenance", change, err)
			}
		})
	}
}

func TestPrivateHistoryRetentionIsDurableBoundedAndCannotAssertPrivateStop(t *testing.T) {
	s := taskSession()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: "original-home", HomeUID: "original-home-uid", BindingGeneration: s.Generation, Admission: "Started", ExecutorAttempted: true, ExecutorIntent: "synthetic-intent", Provider: string(s.Spec.Agent), StopState: "Unknown", LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: "original-executor", PodName: s.Name, NodeName: "worker-a", Revision: "original-template", Generation: s.Generation, ConversationID: "synthetic-conversation"}}
	if err := k8s.Status().Update(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	got := get(t, s)
	if got.Status.PrivateHistory == nil || got.Status.PrivateHistory.LastExecutor.ConversationID != "synthetic-conversation" {
		t.Fatal("retention metadata pruned")
	}
	for _, tc := range []struct {
		name string
		edit func(*v1alpha1.AgentSessionStatus)
	}{
		{"erase latch", func(st *v1alpha1.AgentSessionStatus) { st.PrivateHistory = nil }},
		{"erase executor", func(st *v1alpha1.AgentSessionStatus) { st.PrivateHistory.LastExecutor = nil }},
		{"replace executor", func(st *v1alpha1.AgentSessionStatus) { st.PrivateHistory.LastExecutor.PodUID = "replacement" }},
		{"replace home", func(st *v1alpha1.AgentSessionStatus) { st.PrivateHistory.HomeUID = "replacement" }},
		{"erase home", func(st *v1alpha1.AgentSessionStatus) { st.PrivateHistory.HomeUID = "" }},
		{"change session", func(st *v1alpha1.AgentSessionStatus) { st.PrivateHistory.SessionUID = "replacement" }},
		{"reverse admission", func(st *v1alpha1.AgentSessionStatus) { st.PrivateHistory.Admission = "NeverStarted" }},
		{"replace executor intent", func(st *v1alpha1.AgentSessionStatus) { st.PrivateHistory.ExecutorIntent = "replacement" }},
		{"reverse executor attempt", func(st *v1alpha1.AgentSessionStatus) { st.PrivateHistory.ExecutorAttempted = false }},
		{"claim private stop", func(st *v1alpha1.AgentSessionStatus) { st.PrivateHistory.StopState = "Confirmed" }},
		{"mark an observed executor not created", func(st *v1alpha1.AgentSessionStatus) { st.PrivateHistory.ExecutorNotCreated = true }},
		{"unbounded conversation", func(st *v1alpha1.AgentSessionStatus) {
			st.PrivateHistory.LastExecutor.ConversationID = strings.Repeat("x", 257)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := get(t, s)
			tc.edit(&bad.Status)
			if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
				t.Fatalf("invalid retention change accepted: %v", err)
			}
		})
	}
	if err := k8s.Status().Patch(ctx(t), get(t, s), client.RawPatch(types.JSONPatchType, []byte(`[{"op":"remove","path":"/status"}]`))); !apierrors.IsInvalid(err) {
		t.Fatal("dropping status erased retention latch")
	}
}

func TestPrivateHistoryUnknownOriginalHomeCannotBeBackfilled(t *testing.T) {
	s := taskSession()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: "original-home", BindingGeneration: s.Generation, Admission: "Unknown", Provider: string(s.Spec.Agent), StopState: "Unknown"}
	if err := k8s.Status().Update(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	got := get(t, s)
	got.Status.PrivateHistory.HomeUID = "guessed-home"
	if err := k8s.Status().Update(ctx(t), got); !apierrors.IsInvalid(err) {
		t.Fatal("historical Unknown binding was silently backfilled")
	}
}

// history-04 (DESIGN-021): the operator's task result is a bounded enum beside
// the phase, never a free-form state.
func TestTaskResultSchema(t *testing.T) {
	s := taskSession()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	s.Status.Phase = v1alpha1.PhaseFailed
	s.Status.TaskResult = &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultSucceeded, Reason: "TaskSucceeded", Message: "the task exited 0 (success) after 2 turns"}
	if err := k8s.Status().Update(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	if got := get(t, s); got.Status.TaskResult == nil || got.Status.TaskResult.State != v1alpha1.TaskResultSucceeded || got.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatal("task result pruned or phase changed", got.Status.TaskResult)
	}
	for name, edit := range map[string]func(*v1alpha1.TaskResultStatus){
		"unknown state":   func(r *v1alpha1.TaskResultStatus) { r.State = "Completed" },
		"empty state":     func(r *v1alpha1.TaskResultStatus) { r.State = "" },
		"long reason":     func(r *v1alpha1.TaskResultStatus) { r.Reason = strings.Repeat("R", 129) },
		"unbounded words": func(r *v1alpha1.TaskResultStatus) { r.Message = strings.Repeat("x", 1025) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := get(t, s)
			edit(bad.Status.TaskResult)
			if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
				t.Fatalf("invalid task result accepted: %v", err)
			}
		})
	}
}

// history-01 (DESIGN-019): a known non-attempt mark needs a recorded attempt
// and no observed executor; it may be cleared to re-arm the same intent.
func TestPrivateHistoryExecutorNotCreatedMark(t *testing.T) {
	s := taskSession()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: "original-home", HomeUID: "original-home-uid",
		BindingGeneration: s.Generation, Admission: "Started", ExecutorNotCreated: true, Provider: string(s.Spec.Agent), StopState: "Unknown"}
	if err := k8s.Status().Update(ctx(t), s); !apierrors.IsInvalid(err) {
		t.Fatalf("non-attempt mark without an attempt accepted: %v", err)
	}
	s = get(t, s)
	s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: "original-home", HomeUID: "original-home-uid",
		BindingGeneration: s.Generation, Admission: "Started", ExecutorAttempted: true, ExecutorIntent: "original-intent", ExecutorNotCreated: true, Provider: string(s.Spec.Agent), StopState: "Unknown"}
	if err := k8s.Status().Update(ctx(t), s); err != nil {
		t.Fatal("known non-attempt refused:", err)
	}
	if got := get(t, s); !got.Status.PrivateHistory.ExecutorNotCreated {
		t.Fatal("non-attempt mark pruned")
	}
	rearm := get(t, s)
	rearm.Status.PrivateHistory.ExecutorNotCreated = false
	if err := k8s.Status().Update(ctx(t), rearm); err != nil {
		t.Fatal("re-arming the same intent refused:", err)
	}
	if got := get(t, s); got.Status.PrivateHistory.ExecutorNotCreated || !got.Status.PrivateHistory.ExecutorAttempted || got.Status.PrivateHistory.ExecutorIntent != "original-intent" {
		t.Fatal("re-arm changed the latch", got.Status.PrivateHistory)
	}
	newIntent := get(t, s)
	newIntent.Status.PrivateHistory.ExecutorIntent = "replacement"
	newIntent.Status.PrivateHistory.ExecutorNotCreated = true
	if err := k8s.Status().Update(ctx(t), newIntent); !apierrors.IsInvalid(err) {
		t.Fatalf("a non-attempt mark replaced the intent: %v", err)
	}
}

// DESIGN-025 T16 (record part): the terminal record names the recorded
// executor, never changes or goes, never coexists with the missed marker, and
// survives a rebuild of lastExecutor.
func TestPrivateHistoryExecutorTerminalRecordIsImmutableAndBound(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC))
	retained := func(s *v1alpha1.AgentSession) *v1alpha1.PrivateHistoryStatus {
		return &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: "original-home", HomeUID: "original-home-uid",
			BindingGeneration: s.Generation, Admission: "Started", ExecutorAttempted: true, ExecutorIntent: "synthetic-intent", Provider: string(s.Spec.Agent), StopState: "Unknown",
			LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: "original-executor", PodName: s.Name, NodeName: "worker-a", Revision: "original-template", Generation: s.Generation}}
	}
	terminal := func() *v1alpha1.PrivateExecutorTerminalStatus {
		return &v1alpha1.PrivateExecutorTerminalStatus{Version: 1, PodUID: "original-executor", Phase: "Failed", Reason: "DeadlineExceeded", PodResourceVersion: "42",
			NodeName: "worker-a", NodeUID: "node-uid", LeaseRenewedAt: at, ObservedAt: at, ClaudeConfigDir: ".claude", CodexHome: ".codex",
			Containers: []v1alpha1.PrivateExecutorContainerTermination{{Name: "agent", ExitCode: 0, Reason: "Completed", FinishedAt: at}}}
	}
	missed := func() *v1alpha1.PrivateExecutorMissedStatus {
		return &v1alpha1.PrivateExecutorMissedStatus{Version: 1, PodUID: "original-executor", ObservedAt: at}
	}

	t.Run("refused at creation", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			edit func(*v1alpha1.PrivateHistoryStatus)
		}{
			{"terminal without an executor", func(h *v1alpha1.PrivateHistoryStatus) { h.LastExecutor = nil; h.ExecutorTerminal = terminal() }},
			{"terminal for another UID", func(h *v1alpha1.PrivateHistoryStatus) {
				h.ExecutorTerminal = terminal()
				h.ExecutorTerminal.PodUID = "forged"
			}},
			{"missed without an executor", func(h *v1alpha1.PrivateHistoryStatus) { h.LastExecutor = nil; h.ExecutorMissed = missed() }},
			{"missed for another UID", func(h *v1alpha1.PrivateHistoryStatus) {
				h.ExecutorMissed = missed()
				h.ExecutorMissed.PodUID = "forged"
			}},
			{"both", func(h *v1alpha1.PrivateHistoryStatus) { h.ExecutorTerminal, h.ExecutorMissed = terminal(), missed() }},
			{"running phase", func(h *v1alpha1.PrivateHistoryStatus) {
				h.ExecutorTerminal = terminal()
				h.ExecutorTerminal.Phase = "Running"
			}},
			{"version 2", func(h *v1alpha1.PrivateHistoryStatus) {
				h.ExecutorTerminal = terminal()
				h.ExecutorTerminal.Version = 2
			}},
			{"no containers", func(h *v1alpha1.PrivateHistoryStatus) {
				h.ExecutorTerminal = terminal()
				h.ExecutorTerminal.Containers = nil
			}},
			{"too many containers", func(h *v1alpha1.PrivateHistoryStatus) {
				h.ExecutorTerminal = terminal()
				for i := 0; i < 16; i++ {
					h.ExecutorTerminal.Containers = append(h.ExecutorTerminal.Containers, v1alpha1.PrivateExecutorContainerTermination{Name: fmt.Sprintf("c%d", i), FinishedAt: at})
				}
			}},
			{"unbounded reason", func(h *v1alpha1.PrivateHistoryStatus) {
				h.ExecutorTerminal = terminal()
				h.ExecutorTerminal.Reason = strings.Repeat("R", 129)
			}},
			{"unbounded provider dir", func(h *v1alpha1.PrivateHistoryStatus) {
				h.ExecutorTerminal = terminal()
				h.ExecutorTerminal.CodexHome = strings.Repeat("d", 257)
			}},
			// DESIGN-025 25.6: only the kubelet's ContainerStatusUnknown
			// may go without a finish time.
			{"no finish time with another reason", func(h *v1alpha1.PrivateHistoryStatus) {
				h.ExecutorTerminal = terminal()
				h.ExecutorTerminal.Containers[0] = v1alpha1.PrivateExecutorContainerTermination{Name: "agent", ExitCode: 137, Reason: "Error"}
			}},
			{"no finish time and no reason", func(h *v1alpha1.PrivateHistoryStatus) {
				h.ExecutorTerminal = terminal()
				h.ExecutorTerminal.Containers[0] = v1alpha1.PrivateExecutorContainerTermination{Name: "agent", ExitCode: 137}
			}},
			{"no finish time with the reason in another case", func(h *v1alpha1.PrivateHistoryStatus) {
				h.ExecutorTerminal = terminal()
				h.ExecutorTerminal.Containers[0] = v1alpha1.PrivateExecutorContainerTermination{Name: "agent", ExitCode: 137, Reason: "containerstatusunknown"}
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s := taskSession()
				if err := k8s.Create(ctx(t), s); err != nil {
					t.Fatal(err)
				}
				s.Status.PrivateHistory = retained(s)
				tc.edit(s.Status.PrivateHistory)
				if err := k8s.Status().Update(ctx(t), s); !apierrors.IsInvalid(err) {
					t.Fatalf("invalid record accepted: %v", err)
				}
			})
		}
	})

	t.Run("terminal", func(t *testing.T) {
		s := taskSession()
		if err := k8s.Create(ctx(t), s); err != nil {
			t.Fatal(err)
		}
		s.Status.PrivateHistory = retained(s)
		if err := k8s.Status().Update(ctx(t), s); err != nil {
			t.Fatal(err)
		}
		withRecord := get(t, s)
		withRecord.Status.PrivateHistory.ExecutorTerminal = terminal()
		if err := k8s.Status().Update(ctx(t), withRecord); err != nil {
			t.Fatal("a valid terminal record refused:", err)
		}
		if got := get(t, s); !apiequality.Semantic.DeepEqual(got.Status.PrivateHistory.ExecutorTerminal, terminal()) {
			t.Fatalf("terminal record pruned or changed: %+v", got.Status.PrivateHistory.ExecutorTerminal)
		}
		// A rebuild of lastExecutor (a new node name, a later generation, a
		// conversation id) keeps the terminal record, and the write succeeds.
		rebuilt := get(t, s)
		rebuilt.Status.PrivateHistory.LastExecutor = &v1alpha1.PrivateExecutorStatus{PodUID: "original-executor", PodName: s.Name, NodeName: "worker-b", Revision: "original-template", Generation: s.Generation + 1, ConversationID: "later"}
		if err := k8s.Status().Update(ctx(t), rebuilt); err != nil {
			t.Fatal("a lastExecutor rebuild beside the terminal record refused:", err)
		}
		for _, tc := range []struct {
			name string
			edit func(*v1alpha1.PrivateHistoryStatus)
		}{
			{"remove", func(h *v1alpha1.PrivateHistoryStatus) { h.ExecutorTerminal = nil }},
			{"change phase", func(h *v1alpha1.PrivateHistoryStatus) { h.ExecutorTerminal.Phase = "Succeeded" }},
			{"change observedAt", func(h *v1alpha1.PrivateHistoryStatus) {
				h.ExecutorTerminal.ObservedAt = metav1.NewTime(at.Add(time.Hour))
			}},
			{"change a container", func(h *v1alpha1.PrivateHistoryStatus) { h.ExecutorTerminal.Containers[0].ExitCode = 1 }},
			{"change the UID", func(h *v1alpha1.PrivateHistoryStatus) { h.ExecutorTerminal.PodUID = "replacement" }},
			{"add missed", func(h *v1alpha1.PrivateHistoryStatus) { h.ExecutorMissed = missed() }},
			{"drop with status", func(h *v1alpha1.PrivateHistoryStatus) { *h = *retained(s) }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				bad := get(t, s)
				tc.edit(bad.Status.PrivateHistory)
				if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
					t.Fatalf("terminal record change accepted: %v", err)
				}
			})
		}
	})

	// DESIGN-025 25.6: a container the kubelet reported terminated as
	// ContainerStatusUnknown has no finish time, and the record keeps none.
	t.Run("container status unknown without a finish time", func(t *testing.T) {
		unknown := func() *v1alpha1.PrivateExecutorTerminalStatus {
			rec := terminal()
			rec.Containers = []v1alpha1.PrivateExecutorContainerTermination{{Name: "agent", ExitCode: 137, Reason: "ContainerStatusUnknown"},
				{Name: "sidecar", ExitCode: 0, Reason: "ContainerStatusUnknown", FinishedAt: at}}
			return rec
		}
		s := taskSession()
		if err := k8s.Create(ctx(t), s); err != nil {
			t.Fatal(err)
		}
		s.Status.PrivateHistory = retained(s)
		s.Status.PrivateHistory.ExecutorTerminal = unknown()
		if err := k8s.Status().Update(ctx(t), s); err != nil {
			t.Fatal("a ContainerStatusUnknown record without a finish time refused:", err)
		}
		got := get(t, s)
		if !apiequality.Semantic.DeepEqual(got.Status.PrivateHistory.ExecutorTerminal, unknown()) || !got.Status.PrivateHistory.ExecutorTerminal.Containers[0].FinishedAt.IsZero() {
			t.Fatalf("record pruned, changed or given a finish time: %+v", got.Status.PrivateHistory.ExecutorTerminal)
		}
		for _, tc := range []struct {
			name string
			edit func(*v1alpha1.PrivateHistoryStatus)
		}{
			{"add a finish time", func(h *v1alpha1.PrivateHistoryStatus) { h.ExecutorTerminal.Containers[0].FinishedAt = at }},
			{"change the reason", func(h *v1alpha1.PrivateHistoryStatus) { h.ExecutorTerminal.Containers[0].Reason = "Error" }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				bad := get(t, s)
				tc.edit(bad.Status.PrivateHistory)
				if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
					t.Fatalf("terminal record change accepted: %v", err)
				}
			})
		}
	})

	t.Run("missed", func(t *testing.T) {
		s := taskSession()
		if err := k8s.Create(ctx(t), s); err != nil {
			t.Fatal(err)
		}
		s.Status.PrivateHistory = retained(s)
		s.Status.PrivateHistory.ExecutorMissed = missed()
		if err := k8s.Status().Update(ctx(t), s); err != nil {
			t.Fatal("a valid missed marker refused:", err)
		}
		if got := get(t, s); !apiequality.Semantic.DeepEqual(got.Status.PrivateHistory.ExecutorMissed, missed()) {
			t.Fatalf("missed marker pruned or changed: %+v", got.Status.PrivateHistory.ExecutorMissed)
		}
		for _, tc := range []struct {
			name string
			edit func(*v1alpha1.PrivateHistoryStatus)
		}{
			{"remove", func(h *v1alpha1.PrivateHistoryStatus) { h.ExecutorMissed = nil }},
			{"change observedAt", func(h *v1alpha1.PrivateHistoryStatus) {
				h.ExecutorMissed.ObservedAt = metav1.NewTime(at.Add(time.Hour))
			}},
			{"add terminal", func(h *v1alpha1.PrivateHistoryStatus) { h.ExecutorTerminal = terminal() }},
			{"replace with terminal", func(h *v1alpha1.PrivateHistoryStatus) { h.ExecutorMissed, h.ExecutorTerminal = nil, terminal() }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				bad := get(t, s)
				tc.edit(bad.Status.PrivateHistory)
				if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
					t.Fatalf("missed marker change accepted: %v", err)
				}
			})
		}
	})
}

// DESIGN-025 section 4, T16's stop part: the stop intent names the recorded
// executor, and once set it never changes and is never removed. It sits
// beside the terminal record, which follows it.
func TestPrivateHistoryStopRequestIsImmutableAndBound(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC))
	retained := func(s *v1alpha1.AgentSession) *v1alpha1.PrivateHistoryStatus {
		return &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: "original-home", HomeUID: "original-home-uid",
			BindingGeneration: s.Generation, Admission: "Started", ExecutorAttempted: true, ExecutorIntent: "synthetic-intent", Provider: string(s.Spec.Agent), StopState: "Unknown",
			LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: "original-executor", PodName: s.Name, NodeName: "worker-a", Revision: "original-template", Generation: s.Generation}}
	}
	request := func() *v1alpha1.PrivateExecutorStopRequestStatus {
		return &v1alpha1.PrivateExecutorStopRequestStatus{Version: 1, PodUID: "original-executor", RequestedAt: at}
	}

	t.Run("refused at creation", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			edit func(*v1alpha1.PrivateHistoryStatus)
		}{
			{"without an executor", func(h *v1alpha1.PrivateHistoryStatus) { h.LastExecutor = nil; h.StopRequest = request() }},
			{"for another UID", func(h *v1alpha1.PrivateHistoryStatus) {
				h.StopRequest = request()
				h.StopRequest.PodUID = "forged"
			}},
			{"version 2", func(h *v1alpha1.PrivateHistoryStatus) {
				h.StopRequest = request()
				h.StopRequest.Version = 2
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s := taskSession()
				if err := k8s.Create(ctx(t), s); err != nil {
					t.Fatal(err)
				}
				s.Status.PrivateHistory = retained(s)
				tc.edit(s.Status.PrivateHistory)
				if err := k8s.Status().Update(ctx(t), s); !apierrors.IsInvalid(err) {
					t.Fatalf("invalid stop request accepted: %v", err)
				}
			})
		}
		// The bound on the intent's own UID: the API server names that
		// field, beside lastExecutor's.
		s := taskSession()
		if err := k8s.Create(ctx(t), s); err != nil {
			t.Fatal(err)
		}
		s.Status.PrivateHistory = retained(s)
		s.Status.PrivateHistory.LastExecutor.PodUID = strings.Repeat("u", 129)
		s.Status.PrivateHistory.StopRequest = request()
		s.Status.PrivateHistory.StopRequest.PodUID = s.Status.PrivateHistory.LastExecutor.PodUID
		if err := k8s.Status().Update(ctx(t), s); !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "stopRequest.podUID") {
			t.Fatalf("an unbounded stop request UID is not refused by its own bound: %v", err)
		}
	})

	t.Run("recorded, then the terminal record beside it", func(t *testing.T) {
		s := taskSession()
		if err := k8s.Create(ctx(t), s); err != nil {
			t.Fatal(err)
		}
		s.Status.PrivateHistory = retained(s)
		if err := k8s.Status().Update(ctx(t), s); err != nil {
			t.Fatal(err)
		}
		withIntent := get(t, s)
		withIntent.Status.PrivateHistory.StopRequest = request()
		if err := k8s.Status().Update(ctx(t), withIntent); err != nil {
			t.Fatal("a valid stop request refused:", err)
		}
		if got := get(t, s); !apiequality.Semantic.DeepEqual(got.Status.PrivateHistory.StopRequest, request()) {
			t.Fatalf("stop request pruned or changed: %+v", got.Status.PrivateHistory.StopRequest)
		}
		for _, tc := range []struct {
			name string
			edit func(*v1alpha1.PrivateHistoryStatus)
		}{
			{"remove", func(h *v1alpha1.PrivateHistoryStatus) { h.StopRequest = nil }},
			{"change requestedAt", func(h *v1alpha1.PrivateHistoryStatus) {
				h.StopRequest.RequestedAt = metav1.NewTime(at.Add(time.Hour))
			}},
			{"change the UID", func(h *v1alpha1.PrivateHistoryStatus) { h.StopRequest.PodUID = "replacement" }},
			{"drop with status", func(h *v1alpha1.PrivateHistoryStatus) { *h = *retained(s) }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				bad := get(t, s)
				tc.edit(bad.Status.PrivateHistory)
				if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
					t.Fatalf("stop request change accepted: %v", err)
				}
			})
		}
		// The kubelet ends the Pod; the terminal record follows the intent,
		// and a lastExecutor rebuild keeps both.
		ended := get(t, s)
		ended.Status.PrivateHistory.ExecutorTerminal = &v1alpha1.PrivateExecutorTerminalStatus{Version: 1, PodUID: "original-executor", Phase: "Failed", Reason: "DeadlineExceeded", PodResourceVersion: "42",
			NodeName: "worker-a", NodeUID: "node-uid", LeaseRenewedAt: at, ObservedAt: at,
			Containers: []v1alpha1.PrivateExecutorContainerTermination{{Name: "agent", ExitCode: 143, Reason: "Error", FinishedAt: at}}}
		ended.Status.PrivateHistory.LastExecutor.Generation = s.Generation + 1
		if err := k8s.Status().Update(ctx(t), ended); err != nil {
			t.Fatal("the terminal record beside a stop request refused:", err)
		}
		if got := get(t, s); !apiequality.Semantic.DeepEqual(got.Status.PrivateHistory.StopRequest, request()) || got.Status.PrivateHistory.ExecutorTerminal == nil {
			t.Fatalf("stop request or terminal record lost: %+v", got.Status.PrivateHistory)
		}
	})
}

// T16, the expiry part (DESIGN-025 5.1): the HOME expiry record rests on the
// terminal record and the bound HOME, its phase moves only forward, its
// archive, executor, HOME and grace never change, and it can be removed (a
// cancel) only before Deleting.
func TestPrivateHomeExpiryRecordIsBoundAndForwardOnly(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC))
	due := metav1.NewTime(at.Add(720 * time.Hour))
	retained := func(s *v1alpha1.AgentSession) *v1alpha1.PrivateHistoryStatus {
		return &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: "original-home", HomeUID: "original-home-uid",
			BindingGeneration: s.Generation, Admission: "Started", ExecutorAttempted: true, ExecutorIntent: "synthetic-intent", Provider: string(s.Spec.Agent), StopState: "Unknown",
			LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: "original-executor", PodName: s.Name, NodeName: "worker-a", Revision: "original-template", Generation: s.Generation},
			ExecutorTerminal: &v1alpha1.PrivateExecutorTerminalStatus{Version: 1, PodUID: "original-executor", Phase: "Failed", Reason: "DeadlineExceeded", PodResourceVersion: "42",
				NodeName: "worker-a", NodeUID: "node-uid", LeaseRenewedAt: at, ObservedAt: at,
				Containers: []v1alpha1.PrivateExecutorContainerTermination{{Name: "agent", ExitCode: 143, Reason: "Error", FinishedAt: at}}}}
	}
	record := func() *v1alpha1.PrivateHomeExpiryStatus {
		return &v1alpha1.PrivateHomeExpiryStatus{Version: 1, ExecutorUID: "original-executor", HomeUID: "original-home-uid", GraceStart: at, DueAt: due, Phase: v1alpha1.HomeExpiryWaiting}
	}
	archive := func() *v1alpha1.PrivateHomeArchiveStatus {
		return &v1alpha1.PrivateHomeArchiveStatus{Entry: "v1/_none/uid/0123456789abcdef", ContentSHA256: strings.Repeat("ab", 32), Bytes: 42, Files: 3, GitFiles: 2, ArchivedAt: at}
	}
	stored := func(t *testing.T, h *v1alpha1.PrivateHistoryStatus) *v1alpha1.AgentSession {
		t.Helper()
		s := taskSession()
		if err := k8s.Create(ctx(t), s); err != nil {
			t.Fatal(err)
		}
		s.Status.PrivateHistory = retained(s)
		if h != nil {
			s.Status.PrivateHistory.Expiry = h.Expiry
			s.Status.PrivateHistory.ExpiryClearedAt = h.ExpiryClearedAt
		}
		if err := k8s.Status().Update(ctx(t), s); err != nil {
			t.Fatal("a valid expiry record refused:", err)
		}
		return get(t, s)
	}

	t.Run("refused at creation", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			edit func(*v1alpha1.PrivateHistoryStatus)
		}{
			{"without a terminal record", func(h *v1alpha1.PrivateHistoryStatus) { h.ExecutorTerminal = nil; h.Expiry = record() }},
			{"for another executor", func(h *v1alpha1.PrivateHistoryStatus) { h.Expiry = record(); h.Expiry.ExecutorUID = "forged" }},
			{"for another HOME", func(h *v1alpha1.PrivateHistoryStatus) { h.Expiry = record(); h.Expiry.HomeUID = "replacement-home" }},
			{"version 2", func(h *v1alpha1.PrivateHistoryStatus) { h.Expiry = record(); h.Expiry.Version = 2 }},
			{"an unknown phase", func(h *v1alpha1.PrivateHistoryStatus) { h.Expiry = record(); h.Expiry.Phase = "Done" }},
			{"exporting without a rescue", func(h *v1alpha1.PrivateHistoryStatus) {
				h.Expiry = record()
				h.Expiry.Phase = v1alpha1.HomeExpiryExporting
			}},
			{"archived without an archive", func(h *v1alpha1.PrivateHistoryStatus) {
				h.Expiry = record()
				h.Expiry.Phase, h.Expiry.RescueStamp = v1alpha1.HomeExpiryArchived, "20261111-1200"
			}},
			{"a digest that is not one", func(h *v1alpha1.PrivateHistoryStatus) {
				h.Expiry = record()
				h.Expiry.Phase, h.Expiry.RescueStamp, h.Expiry.Archive = v1alpha1.HomeExpiryArchived, "20261111-1200", archive()
				h.Expiry.Archive.ContentSHA256 = "not-a-digest"
			}},
			{"deleting without deletingAt", func(h *v1alpha1.PrivateHistoryStatus) {
				h.Expiry = record()
				h.Expiry.Phase, h.Expiry.RescueStamp, h.Expiry.Archive = v1alpha1.HomeExpiryDeleting, "20261111-1200", archive()
			}},
			{"an unbounded refusal", func(h *v1alpha1.PrivateHistoryStatus) {
				h.Expiry = record()
				h.Expiry.Refusal, h.Expiry.RefusalAt = strings.Repeat("r", 65), &at
			}},
			{"a refusal without refusalAt", func(h *v1alpha1.PrivateHistoryStatus) { h.Expiry = record(); h.Expiry.Refusal = "unknown" }},
			{"refusalAt without a refusal", func(h *v1alpha1.PrivateHistoryStatus) { h.Expiry = record(); h.Expiry.RefusalAt = &at }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s := taskSession()
				if err := k8s.Create(ctx(t), s); err != nil {
					t.Fatal(err)
				}
				s.Status.PrivateHistory = retained(s)
				tc.edit(s.Status.PrivateHistory)
				if err := k8s.Status().Update(ctx(t), s); !apierrors.IsInvalid(err) {
					t.Fatalf("invalid expiry record accepted: %v", err)
				}
			})
		}
	})

	t.Run("moves forward to Archived, then the archive is fixed", func(t *testing.T) {
		s := stored(t, &v1alpha1.PrivateHistoryStatus{Expiry: record()})
		steps := []func(*v1alpha1.PrivateHomeExpiryStatus){
			func(e *v1alpha1.PrivateHomeExpiryStatus) {
				e.Phase, e.ReleaseNowAt, e.Refusal, e.RefusalAt = v1alpha1.HomeExpiryRescuing, &at, "shelf_unavailable", &at
			},
			func(e *v1alpha1.PrivateHomeExpiryStatus) { e.HoldIntent, e.Refusal, e.RefusalAt = "intent-1", "", nil },
			func(e *v1alpha1.PrivateHomeExpiryStatus) { e.HoldPodUID = "hold-1" },
			// A hold pod made again: a new intent, and its UID cleared.
			func(e *v1alpha1.PrivateHomeExpiryStatus) { e.HoldIntent, e.HoldPodUID = "intent-2", "" },
			func(e *v1alpha1.PrivateHomeExpiryStatus) { e.HoldPodUID = "hold-2" },
			func(e *v1alpha1.PrivateHomeExpiryStatus) {
				e.Phase, e.RescueStamp = v1alpha1.HomeExpiryExporting, "20261111-1200"
			},
			func(e *v1alpha1.PrivateHomeExpiryStatus) { e.Phase, e.Archive = v1alpha1.HomeExpiryArchived, archive() },
		}
		for i, step := range steps {
			next := get(t, s)
			step(next.Status.PrivateHistory.Expiry)
			if err := k8s.Status().Update(ctx(t), next); err != nil {
				t.Fatalf("step %d refused: %v", i, err)
			}
		}
		if got := get(t, s).Status.PrivateHistory.Expiry; got.Phase != v1alpha1.HomeExpiryArchived || got.Archive == nil || got.HoldPodUID != "hold-2" {
			t.Fatalf("the record was pruned or changed: %+v", got)
		}
		for _, tc := range []struct {
			name string
			edit func(*v1alpha1.PrivateHomeExpiryStatus)
		}{
			{"back to Exporting", func(e *v1alpha1.PrivateHomeExpiryStatus) { e.Phase = v1alpha1.HomeExpiryExporting }},
			{"back to Waiting", func(e *v1alpha1.PrivateHomeExpiryStatus) { e.Phase = v1alpha1.HomeExpiryWaiting }},
			{"another digest", func(e *v1alpha1.PrivateHomeExpiryStatus) { e.Archive.ContentSHA256 = strings.Repeat("cd", 32) }},
			{"the archive dropped", func(e *v1alpha1.PrivateHomeExpiryStatus) { e.Archive = nil }},
			{"another grace", func(e *v1alpha1.PrivateHomeExpiryStatus) { e.DueAt = metav1.NewTime(due.Add(time.Hour)) }},
			{"another start", func(e *v1alpha1.PrivateHomeExpiryStatus) { e.GraceStart = metav1.NewTime(at.Add(time.Hour)) }},
			{"releaseNowAt changed", func(e *v1alpha1.PrivateHomeExpiryStatus) { e.ReleaseNowAt = &due }},
			{"releaseNowAt dropped", func(e *v1alpha1.PrivateHomeExpiryStatus) { e.ReleaseNowAt = nil }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				bad := get(t, s)
				tc.edit(bad.Status.PrivateHistory.Expiry)
				if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
					t.Fatalf("expiry record change accepted: %v", err)
				}
			})
		}
		// A cancel before Deleting removes the record and records when.
		cancelled := get(t, s)
		cancelled.Status.PrivateHistory.Expiry, cancelled.Status.PrivateHistory.ExpiryClearedAt = nil, &due
		if err := k8s.Status().Update(ctx(t), cancelled); err != nil {
			t.Fatal("a cancel at Archived refused:", err)
		}
		// A record made after the cancel starts its own grace, at Waiting.
		again := get(t, s)
		again.Status.PrivateHistory.Expiry = record()
		again.Status.PrivateHistory.Expiry.GraceStart, again.Status.PrivateHistory.Expiry.DueAt = due, metav1.NewTime(due.Add(720*time.Hour))
		if err := k8s.Status().Update(ctx(t), again); err != nil {
			t.Fatal("a new record after a cancel refused:", err)
		}
		// Dropping the whole retention record drops the expiry with it: refused.
		dropped := get(t, s)
		dropped.Status.PrivateHistory = nil
		if err := k8s.Status().Update(ctx(t), dropped); !apierrors.IsInvalid(err) {
			t.Fatalf("the retention record and its expiry dropped: %v", err)
		}
	})

	// Section 14: an operator below H5 drops the record and expiryClearedAt
	// from its status writes. Removing a record needs a newer expiryClearedAt
	// in the same write, and expiryClearedAt never goes or moves back, so such
	// a write is refused, as for the terminal record, and a later record can
	// never take its grace from an old terminal observation.
	t.Run("a cancel records a newer expiryClearedAt, which never goes or moves back", func(t *testing.T) {
		later := metav1.NewTime(due.Add(time.Hour))
		s := stored(t, &v1alpha1.PrivateHistoryStatus{Expiry: record()})
		for _, tc := range []struct {
			name string
			edit func(*v1alpha1.PrivateHistoryStatus)
		}{
			{"the first record dropped without expiryClearedAt", func(h *v1alpha1.PrivateHistoryStatus) { h.Expiry = nil }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				bad := get(t, s)
				tc.edit(bad.Status.PrivateHistory)
				if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
					t.Fatalf("accepted: %v", err)
				}
			})
		}
		cancelled := get(t, s)
		cancelled.Status.PrivateHistory.Expiry, cancelled.Status.PrivateHistory.ExpiryClearedAt = nil, &due
		if err := k8s.Status().Update(ctx(t), cancelled); err != nil {
			t.Fatal("a cancel with expiryClearedAt refused:", err)
		}
		again := get(t, s)
		again.Status.PrivateHistory.Expiry = record()
		again.Status.PrivateHistory.Expiry.GraceStart, again.Status.PrivateHistory.Expiry.DueAt = due, metav1.NewTime(due.Add(720*time.Hour))
		if err := k8s.Status().Update(ctx(t), again); err != nil {
			t.Fatal("a new record after a cancel refused:", err)
		}
		for _, tc := range []struct {
			name string
			edit func(*v1alpha1.PrivateHistoryStatus)
		}{
			{"expiryClearedAt dropped", func(h *v1alpha1.PrivateHistoryStatus) { h.ExpiryClearedAt = nil }},
			{"expiryClearedAt moved back", func(h *v1alpha1.PrivateHistoryStatus) { h.ExpiryClearedAt = &at }},
			{"both dropped, as an older operator writes", func(h *v1alpha1.PrivateHistoryStatus) { h.Expiry, h.ExpiryClearedAt = nil, nil }},
			{"the record dropped, expiryClearedAt kept", func(h *v1alpha1.PrivateHistoryStatus) { h.Expiry = nil }},
			{"the record dropped, expiryClearedAt older", func(h *v1alpha1.PrivateHistoryStatus) { h.Expiry, h.ExpiryClearedAt = nil, &at }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				bad := get(t, s)
				tc.edit(bad.Status.PrivateHistory)
				if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
					t.Fatalf("accepted: %v", err)
				}
			})
		}
		// A heartbeat-like write that keeps both is accepted, and so is a
		// second cancel with a newer time.
		kept := get(t, s)
		kept.Status.PrivateHistory.Expiry.HoldIntent = "intent-1"
		if err := k8s.Status().Update(ctx(t), kept); err != nil {
			t.Fatal("a write that keeps the record and expiryClearedAt refused:", err)
		}
		second := get(t, s)
		second.Status.PrivateHistory.Expiry, second.Status.PrivateHistory.ExpiryClearedAt = nil, &later
		if err := k8s.Status().Update(ctx(t), second); err != nil {
			t.Fatal("a second cancel with a newer expiryClearedAt refused:", err)
		}
		if got := get(t, s).Status.PrivateHistory; got.Expiry != nil || got.ExpiryClearedAt == nil || !got.ExpiryClearedAt.Equal(&later) {
			t.Fatalf("the second cancel was not stored: %+v", got)
		}
	})

	t.Run("from Deleting on it is never removed and only becomes Expired", func(t *testing.T) {
		deleting := record()
		deleting.Phase, deleting.RescueStamp, deleting.Archive, deleting.DeletingAt = v1alpha1.HomeExpiryDeleting, "20261111-1200", archive(), &due
		s := stored(t, &v1alpha1.PrivateHistoryStatus{Expiry: deleting})
		for _, tc := range []struct {
			name string
			edit func(*v1alpha1.PrivateHistoryStatus)
		}{
			{"removed", func(h *v1alpha1.PrivateHistoryStatus) { h.Expiry = nil }},
			{"back to Archived", func(h *v1alpha1.PrivateHistoryStatus) { h.Expiry.Phase = v1alpha1.HomeExpiryArchived }},
			{"expired without homeDeletedAt", func(h *v1alpha1.PrivateHistoryStatus) { h.Expiry.Phase = v1alpha1.HomeExpiryExpired }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				bad := get(t, s)
				tc.edit(bad.Status.PrivateHistory)
				if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
					t.Fatalf("a Deleting record change accepted: %v", err)
				}
			})
		}
		expired := get(t, s)
		expired.Status.PrivateHistory.Expiry.Phase, expired.Status.PrivateHistory.Expiry.HomeDeletedAt = v1alpha1.HomeExpiryExpired, &due
		if err := k8s.Status().Update(ctx(t), expired); err != nil {
			t.Fatal("Deleting to Expired refused:", err)
		}
		for _, tc := range []struct {
			name string
			edit func(*v1alpha1.PrivateHistoryStatus)
		}{
			{"removed", func(h *v1alpha1.PrivateHistoryStatus) { h.Expiry = nil }},
			{"a refusal added", func(h *v1alpha1.PrivateHistoryStatus) { h.Expiry.Refusal, h.Expiry.RefusalAt = "unknown", &at }},
		} {
			t.Run("expired "+tc.name, func(t *testing.T) {
				bad := get(t, s)
				tc.edit(bad.Status.PrivateHistory)
				if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
					t.Fatalf("an Expired record change accepted: %v", err)
				}
			})
		}
	})
}

// DESIGN-061 section 2 item 1: a task Session's clean-up keeps its verified
// unpushed-work bundle in place of a history archive. The bundle is set once,
// at Archived, names the record's rescue, and never changes; a record holds an
// archive or a bundle, never both; and Archived, Deleting and Expired accept
// either.
func TestPrivateHomeExpiryTaskBundle(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 11, 3, 17, 31, 21, 0, time.UTC))
	later := metav1.NewTime(at.Add(168 * time.Hour))
	const stamp = "20261110-1731"
	retained := func(s *v1alpha1.AgentSession) *v1alpha1.PrivateHistoryStatus {
		return &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: "original-home", HomeUID: "original-home-uid",
			BindingGeneration: s.Generation, Admission: "Started", ExecutorAttempted: true, ExecutorIntent: "synthetic-intent", Provider: string(s.Spec.Agent), StopState: "Unknown",
			LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: "original-executor", PodName: s.Name, NodeName: "worker-a", Revision: "original-template", Generation: s.Generation},
			ExecutorTerminal: &v1alpha1.PrivateExecutorTerminalStatus{Version: 1, PodUID: "original-executor", Phase: "Succeeded", Reason: "Completed", PodResourceVersion: "42",
				NodeName: "worker-a", NodeUID: "node-uid", LeaseRenewedAt: at, ObservedAt: at,
				Containers: []v1alpha1.PrivateExecutorContainerTermination{{Name: "agent", ExitCode: 0, Reason: "Completed", FinishedAt: at}}}}
	}
	rescuing := func() *v1alpha1.PrivateHomeExpiryStatus {
		return &v1alpha1.PrivateHomeExpiryStatus{Version: 1, ExecutorUID: "original-executor", HomeUID: "original-home-uid", GraceStart: at, DueAt: at,
			Phase: v1alpha1.HomeExpiryRescuing, HoldIntent: "intent-1", HoldPodUID: "hold-1"}
	}
	verified := func(name string) *v1alpha1.PrivateHomeBundleStatus {
		return &v1alpha1.PrivateHomeBundleStatus{Stamp: stamp, Result: string(v1alpha1.RescueVerified),
			Manifest: "rescue/" + name + "/" + stamp + "/manifest.json", ManifestSHA256: strings.Repeat("ab", 32), Bundles: 2, Bytes: 41 << 10, VerifiedAt: later}
	}
	clean := func() *v1alpha1.PrivateHomeBundleStatus {
		return &v1alpha1.PrivateHomeBundleStatus{Stamp: stamp, Result: string(v1alpha1.RescueCleanAndPushed), VerifiedAt: later}
	}
	archive := func() *v1alpha1.PrivateHomeArchiveStatus {
		return &v1alpha1.PrivateHomeArchiveStatus{Entry: "v1/_none/uid/0123456789abcdef", ContentSHA256: strings.Repeat("ab", 32), Bytes: 42, Files: 3, GitFiles: 2, ArchivedAt: at}
	}
	// stored creates a task Session whose record stands at Rescuing.
	stored := func(t *testing.T) *v1alpha1.AgentSession {
		t.Helper()
		s := taskSession()
		if err := k8s.Create(ctx(t), s); err != nil {
			t.Fatal(err)
		}
		s.Status.PrivateHistory = retained(s)
		s.Status.PrivateHistory.Expiry = rescuing()
		if err := k8s.Status().Update(ctx(t), s); err != nil {
			t.Fatal("a Rescuing record refused:", err)
		}
		return get(t, s)
	}
	saved := func(e *v1alpha1.PrivateHomeExpiryStatus, b *v1alpha1.PrivateHomeBundleStatus) {
		e.Phase, e.RescueStamp, e.Bundle = v1alpha1.HomeExpiryArchived, stamp, b
	}

	t.Run("a Verified bundle carries the record from Rescuing to Expired", func(t *testing.T) {
		s := stored(t)
		steps := []func(*v1alpha1.PrivateHomeExpiryStatus){
			// A task record skips Exporting: Rescuing to Archived in one write.
			func(e *v1alpha1.PrivateHomeExpiryStatus) { saved(e, verified(s.Name)) },
			// The hold pod goes; a refusal comes and goes beside the bundle.
			func(e *v1alpha1.PrivateHomeExpiryStatus) { e.Refusal, e.RefusalAt = "bundle_missing", &later },
			func(e *v1alpha1.PrivateHomeExpiryStatus) { e.Refusal, e.RefusalAt = "", nil },
			func(e *v1alpha1.PrivateHomeExpiryStatus) { e.Phase, e.DeletingAt = v1alpha1.HomeExpiryDeleting, &later },
			func(e *v1alpha1.PrivateHomeExpiryStatus) {
				e.Phase, e.HomeDeletedAt = v1alpha1.HomeExpiryExpired, &later
			},
		}
		for i, step := range steps {
			next := get(t, s)
			step(next.Status.PrivateHistory.Expiry)
			if err := k8s.Status().Update(ctx(t), next); err != nil {
				t.Fatalf("step %d refused: %v", i, err)
			}
		}
		got := get(t, s).Status.PrivateHistory.Expiry
		if got.Phase != v1alpha1.HomeExpiryExpired || got.Archive != nil || !apiequality.Semantic.DeepEqual(got.Bundle, verified(s.Name)) {
			t.Fatalf("the record was pruned or changed: %+v", got)
		}
	})

	t.Run("a CleanAndPushed bundle names no manifest and carries the record to Deleting", func(t *testing.T) {
		s := stored(t)
		next := get(t, s)
		saved(next.Status.PrivateHistory.Expiry, clean())
		if err := k8s.Status().Update(ctx(t), next); err != nil {
			t.Fatal("a CleanAndPushed bundle refused:", err)
		}
		deleting := get(t, s)
		deleting.Status.PrivateHistory.Expiry.Phase, deleting.Status.PrivateHistory.Expiry.DeletingAt = v1alpha1.HomeExpiryDeleting, &later
		if err := k8s.Status().Update(ctx(t), deleting); err != nil {
			t.Fatal("Deleting on a CleanAndPushed bundle refused:", err)
		}
	})

	t.Run("refused when written", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			edit func(e *v1alpha1.PrivateHomeExpiryStatus, name string)
		}{
			{"Archived with neither archive nor bundle", func(e *v1alpha1.PrivateHomeExpiryStatus, _ string) {
				e.Phase, e.RescueStamp = v1alpha1.HomeExpiryArchived, stamp
			}},
			{"both an archive and a bundle", func(e *v1alpha1.PrivateHomeExpiryStatus, name string) {
				saved(e, verified(name))
				e.Archive = archive()
			}},
			{"a bundle at Rescuing", func(e *v1alpha1.PrivateHomeExpiryStatus, name string) {
				e.RescueStamp, e.Bundle = stamp, verified(name)
			}},
			{"a bundle at Exporting", func(e *v1alpha1.PrivateHomeExpiryStatus, name string) {
				saved(e, verified(name))
				e.Phase = v1alpha1.HomeExpiryExporting
			}},
			{"a bundle of another rescue", func(e *v1alpha1.PrivateHomeExpiryStatus, name string) {
				saved(e, verified(name))
				e.RescueStamp = "20261110-1732"
			}},
			{"Verified without a manifest", func(e *v1alpha1.PrivateHomeExpiryStatus, name string) {
				saved(e, verified(name))
				e.Bundle.Manifest = ""
			}},
			{"Verified without a digest", func(e *v1alpha1.PrivateHomeExpiryStatus, name string) {
				saved(e, verified(name))
				e.Bundle.ManifestSHA256 = ""
			}},
			{"Verified without a bundle", func(e *v1alpha1.PrivateHomeExpiryStatus, name string) {
				saved(e, verified(name))
				e.Bundle.Bundles = 0
			}},
			{"CleanAndPushed with a manifest", func(e *v1alpha1.PrivateHomeExpiryStatus, name string) {
				saved(e, clean())
				e.Bundle.Manifest = verified(name).Manifest
			}},
			{"CleanAndPushed with a digest", func(e *v1alpha1.PrivateHomeExpiryStatus, _ string) {
				saved(e, clean())
				e.Bundle.ManifestSHA256 = strings.Repeat("ab", 32)
			}},
			{"CleanAndPushed with bundles", func(e *v1alpha1.PrivateHomeExpiryStatus, _ string) {
				saved(e, clean())
				e.Bundle.Bundles = 1
			}},
			{"CleanAndPushed with bytes", func(e *v1alpha1.PrivateHomeExpiryStatus, _ string) {
				saved(e, clean())
				e.Bundle.Bytes = 1
			}},
			{"a Failed result", func(e *v1alpha1.PrivateHomeExpiryStatus, name string) {
				saved(e, verified(name))
				e.Bundle.Result = string(v1alpha1.RescueFailed)
			}},
			{"a manifest outside rescue/", func(e *v1alpha1.PrivateHomeExpiryStatus, name string) {
				saved(e, verified(name))
				e.Bundle.Manifest = "logs/" + name + ".log"
			}},
			{"a manifest path that climbs out", func(e *v1alpha1.PrivateHomeExpiryStatus, _ string) {
				saved(e, verified(""))
				e.Bundle.Manifest = "rescue/../" + stamp + "/manifest.json"
			}},
			{"a digest that is not one", func(e *v1alpha1.PrivateHomeExpiryStatus, name string) {
				saved(e, verified(name))
				e.Bundle.ManifestSHA256 = strings.Repeat("AB", 32)
			}},
			{"no stamp", func(e *v1alpha1.PrivateHomeExpiryStatus, name string) {
				saved(e, verified(name))
				e.RescueStamp, e.Bundle.Stamp = stamp, ""
			}},
			// The bundle's stamp equals rescueStamp, which has its own
			// bound; a longer one fails either rule.
			{"a stamp over 64 characters", func(e *v1alpha1.PrivateHomeExpiryStatus, name string) {
				saved(e, verified(name))
				e.RescueStamp, e.Bundle.Stamp = strings.Repeat("1", 64), strings.Repeat("1", 65)
			}},
			{"a manifest of another rescue", func(e *v1alpha1.PrivateHomeExpiryStatus, name string) {
				saved(e, verified(name))
				e.Bundle.Manifest = "rescue/" + name + "/20261110-1800/manifest.json"
			}},
			{"negative bytes", func(e *v1alpha1.PrivateHomeExpiryStatus, name string) {
				saved(e, verified(name))
				e.Bundle.Bytes = -1
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s := stored(t)
				bad := get(t, s)
				tc.edit(bad.Status.PrivateHistory.Expiry, s.Name)
				if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
					t.Fatalf("invalid bundle accepted: %v", err)
				}
			})
		}
	})

	t.Run("never changes once set", func(t *testing.T) {
		s := stored(t)
		next := get(t, s)
		saved(next.Status.PrivateHistory.Expiry, verified(s.Name))
		if err := k8s.Status().Update(ctx(t), next); err != nil {
			t.Fatal("a Verified bundle refused:", err)
		}
		for _, tc := range []struct {
			name string
			edit func(*v1alpha1.PrivateHomeExpiryStatus)
		}{
			{"another digest", func(e *v1alpha1.PrivateHomeExpiryStatus) { e.Bundle.ManifestSHA256 = strings.Repeat("cd", 32) }},
			{"another count", func(e *v1alpha1.PrivateHomeExpiryStatus) { e.Bundle.Bundles = 3 }},
			{"other bytes", func(e *v1alpha1.PrivateHomeExpiryStatus) { e.Bundle.Bytes++ }},
			{"verified again", func(e *v1alpha1.PrivateHomeExpiryStatus) {
				e.Bundle.VerifiedAt = metav1.NewTime(later.Add(time.Minute))
			}},
			{"another manifest", func(e *v1alpha1.PrivateHomeExpiryStatus) {
				e.Bundle.Manifest = "rescue/" + s.Name + "/" + stamp + "-2/manifest.json"
			}},
			{"dropped", func(e *v1alpha1.PrivateHomeExpiryStatus) { e.Bundle = nil }},
			{"traded for an archive", func(e *v1alpha1.PrivateHomeExpiryStatus) { e.Bundle, e.Archive = nil, archive() }},
			{"traded for CleanAndPushed", func(e *v1alpha1.PrivateHomeExpiryStatus) { e.Bundle = clean() }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				bad := get(t, s)
				tc.edit(bad.Status.PrivateHistory.Expiry)
				if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
					t.Fatalf("bundle change accepted: %v", err)
				}
			})
		}
	})

	t.Run("an archive record never takes a bundle", func(t *testing.T) {
		s := stored(t)
		next := get(t, s)
		next.Status.PrivateHistory.Expiry.Phase, next.Status.PrivateHistory.Expiry.RescueStamp = v1alpha1.HomeExpiryExporting, stamp
		if err := k8s.Status().Update(ctx(t), next); err != nil {
			t.Fatal("Exporting refused:", err)
		}
		// A record that exported history goes on to its archive only.
		swapped := get(t, s)
		saved(swapped.Status.PrivateHistory.Expiry, verified(s.Name))
		if err := k8s.Status().Update(ctx(t), swapped); !apierrors.IsInvalid(err) {
			t.Fatalf("Exporting to Archived with a bundle accepted: %v", err)
		}
		archived := get(t, s)
		archived.Status.PrivateHistory.Expiry.Phase, archived.Status.PrivateHistory.Expiry.Archive = v1alpha1.HomeExpiryArchived, archive()
		if err := k8s.Status().Update(ctx(t), archived); err != nil {
			t.Fatal("an archive record refused:", err)
		}
		bad := get(t, s)
		bad.Status.PrivateHistory.Expiry.Bundle = verified(s.Name)
		if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
			t.Fatalf("a bundle beside an archive accepted: %v", err)
		}
	})
}

func schemaInstruction(key, mark string) v1alpha1.CoordinatorInstructionStatus {
	return v1alpha1.CoordinatorInstructionStatus{
		Key:              key,
		MessageName:      "message." + strings.Repeat(mark, 32) + "." + strings.Repeat("e", 32),
		MessageUID:       "message-" + key,
		SenderUID:        "sender-uid",
		SenderPodUID:     "sender-pod",
		TargetSessionUID: "target-session",
		TargetPodUID:     "target-pod",
		AttemptID:        "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		Project:          "sample",
		Repository:       "alias",
		TaskKey:          "task:coord-instruction",
		TextSHA256:       strings.Repeat(mark, 64),
		Nonce:            strings.Repeat("c", 64),
		DeliveredAt:      metav1.NewTime(time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC)),
	}
}

func TestCoordinatorInstructionMapListTransitions(t *testing.T) {
	c := ctx(t)
	s := taskSession()
	if err := k8s.Create(c, s); err != nil {
		t.Fatal(err)
	}
	first := schemaInstruction("correction-1", "a")
	s.Status.CoordinatorInstructions = []v1alpha1.CoordinatorInstructionStatus{first}
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatalf("create delivery: %v", err)
	}
	got := get(t, s)
	second := schemaInstruction("correction-2", "b")
	got.Status.CoordinatorInstructions = append(got.Status.CoordinatorInstructions, second)
	if err := k8s.Status().Update(c, got); err != nil {
		t.Fatalf("append delivery: %v", err)
	}
	got = get(t, s)
	if len(got.Status.CoordinatorInstructions) != 2 || got.Status.CoordinatorInstructions[0].Key != "correction-1" || got.Status.CoordinatorInstructions[1].Key != "correction-2" {
		t.Fatalf("stored keys %+v", got.Status.CoordinatorInstructions)
	}
	consumed := got.Status.CoordinatorInstructions[0]
	consumed.Consumed = &v1alpha1.CoordinatorInstructionConsumptionStatus{
		AttemptID: consumed.AttemptID, SessionUID: consumed.TargetSessionUID, PodUID: consumed.TargetPodUID,
		MessageUID: consumed.MessageUID, Nonce: consumed.Nonce, TextSHA256: consumed.TextSHA256, At: consumed.DeliveredAt,
	}
	got.Status.CoordinatorInstructions[0] = consumed
	if err := k8s.Status().Update(c, got); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	stored := get(t, s)
	if stored.Status.CoordinatorInstructions[0].Consumed == nil || stored.Status.CoordinatorInstructions[1].Consumed != nil {
		t.Fatalf("consumption was not one record: %+v", stored.Status.CoordinatorInstructions)
	}
	for _, tc := range []struct {
		name, want string
		edit       func(*v1alpha1.AgentSession)
	}{
		{"immutable delivery change", "immutable", func(s *v1alpha1.AgentSession) {
			s.Status.CoordinatorInstructions[1].TextSHA256 = strings.Repeat("d", 64)
		}},
		{"consumed mutation", "immutable", func(s *v1alpha1.AgentSession) {
			s.Status.CoordinatorInstructions[0].Consumed.Nonce = strings.Repeat("d", 64)
		}},
		{"consumed removal", "immutable", func(s *v1alpha1.AgentSession) {
			s.Status.CoordinatorInstructions[0].Consumed = nil
		}},
		{"old key removal", "removed or replaced", func(s *v1alpha1.AgentSession) {
			s.Status.CoordinatorInstructions = s.Status.CoordinatorInstructions[1:]
		}},
		{"old key replacement", "removed or replaced", func(s *v1alpha1.AgentSession) {
			s.Status.CoordinatorInstructions[0].Key = "correction-9"
		}},
		{"whole list deletion", "removed or replaced", func(s *v1alpha1.AgentSession) {
			s.Status.CoordinatorInstructions = nil
		}},
		{"duplicate key", "uplicate", func(s *v1alpha1.AgentSession) {
			s.Status.CoordinatorInstructions = append(s.Status.CoordinatorInstructions, s.Status.CoordinatorInstructions[0])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := get(t, s)
			tc.edit(bad)
			wantInvalid(t, k8s.Status().Update(c, bad), tc.want)
		})
	}
	fresh := taskSession()
	if err := k8s.Create(c, fresh); err != nil {
		t.Fatal(err)
	}
	dup := schemaInstruction("correction-1", "a")
	fresh.Status.CoordinatorInstructions = []v1alpha1.CoordinatorInstructionStatus{dup, dup}
	wantInvalid(t, k8s.Status().Update(c, fresh), "uplicate")
	full := get(t, s)
	full.Status.CoordinatorInstructions = append(full.Status.CoordinatorInstructions, schemaInstruction("correction-3", "c"), schemaInstruction("correction-4", "d"))
	if err := k8s.Status().Update(c, full); err != nil {
		t.Fatalf("append to the bound: %v", err)
	}
	over := get(t, s)
	over.Status.CoordinatorInstructions = append(over.Status.CoordinatorInstructions, schemaInstruction("correction-5", "f"))
	wantInvalid(t, k8s.Status().Update(c, over), "4")
}

func TestCoordinatorInstructionEndedAttemptLedger(t *testing.T) {
	c := ctx(t)
	s := taskSession()
	if err := k8s.Create(c, s); err != nil {
		t.Fatal(err)
	}
	hexAttempt := strings.Repeat("ab", 32)
	s.Status.CoordinatorInstructionAttempt = &v1alpha1.CoordinatorInstructionAttemptStatus{
		SessionUID: "target-session", PodUID: "target-pod", AttemptID: hexAttempt, Phase: "active",
	}
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatalf("64-hex attempt: %v", err)
	}
	got := get(t, s)
	got.Status.CoordinatorInstructionAttempt.Phase = "ended"
	got.Status.CoordinatorInstructionEndedAttempts = []v1alpha1.CoordinatorInstructionEndedAttemptStatus{{
		AttemptID: hexAttempt, SessionUID: "target-session", PodUID: "target-pod",
	}}
	if err := k8s.Status().Update(c, got); err != nil {
		t.Fatalf("retain ended attempt: %v", err)
	}
	for _, tc := range []struct {
		name, want string
		edit       func(*v1alpha1.AgentSession)
	}{
		{"ledger removal", "removed or rewritten", func(s *v1alpha1.AgentSession) {
			s.Status.CoordinatorInstructionEndedAttempts = nil
		}},
		{"ledger item removal", "removed or rewritten", func(s *v1alpha1.AgentSession) {
			s.Status.CoordinatorInstructionEndedAttempts = s.Status.CoordinatorInstructionEndedAttempts[:0]
		}},
		{"ledger rewrite", "rewritten", func(s *v1alpha1.AgentSession) {
			s.Status.CoordinatorInstructionEndedAttempts[0].PodUID = "other-pod"
		}},
		{"attempt id rewrite", "rewritten", func(s *v1alpha1.AgentSession) {
			s.Status.CoordinatorInstructionEndedAttempts[0].AttemptID = strings.Repeat("cd", 32)
		}},
		{"session uid rewrite", "immutable", func(s *v1alpha1.AgentSession) {
			s.Status.CoordinatorInstructionEndedAttempts[0].SessionUID = "other-session"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := get(t, s)
			tc.edit(bad)
			wantInvalid(t, k8s.Status().Update(c, bad), tc.want)
		})
	}
	stored := get(t, s)
	for i := 0; i < 3; i++ {
		id := strings.Repeat(string(rune('c'+i)), 64)
		stored.Status.CoordinatorInstructionEndedAttempts = append(stored.Status.CoordinatorInstructionEndedAttempts, v1alpha1.CoordinatorInstructionEndedAttemptStatus{
			AttemptID: id, SessionUID: "target-session", PodUID: "target-pod",
		})
	}
	if err := k8s.Status().Update(c, stored); err != nil {
		t.Fatalf("fill ledger: %v", err)
	}
	over := get(t, s)
	over.Status.CoordinatorInstructionEndedAttempts = append(over.Status.CoordinatorInstructionEndedAttempts, v1alpha1.CoordinatorInstructionEndedAttemptStatus{
		AttemptID: strings.Repeat("f", 64), SessionUID: "target-session", PodUID: "target-pod",
	})
	wantInvalid(t, k8s.Status().Update(c, over), "4")
	dup := get(t, s)
	dup.Status.CoordinatorInstructionEndedAttempts = append(dup.Status.CoordinatorInstructionEndedAttempts, dup.Status.CoordinatorInstructionEndedAttempts[0])
	wantInvalid(t, k8s.Status().Update(c, dup), "uplicate")
	badID := get(t, s)
	badID.Status.CoordinatorInstructionAttempt.AttemptID = "not-an-attempt"
	wantInvalid(t, k8s.Status().Update(c, badID), "attemptID")
}

// TestCoordinatorInstructionOptInImmutableAndDefaultOff exercises the generated
// CRD's default-off storage and immutable enrollment through the API server.
func TestCoordinatorInstructionOptInImmutableAndDefaultOff(t *testing.T) {
	created := taskSession()
	if err := k8s.Create(ctx(t), created); err != nil {
		t.Fatal(err)
	}
	if get(t, created).Spec.CoordinatorInstructions {
		t.Fatal("omitted opt-in was stored as enabled")
	}
	legacy := get(t, created)
	legacy.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if err := k8s.Update(ctx(t), legacy); err != nil {
		t.Fatal(err)
	}
	if got := get(t, created); got.Spec.CoordinatorInstructions || got.Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		t.Fatalf("legacy update changed opt-in or mode: %+v", got.Spec)
	}
	enabled := get(t, created)
	enabled.Spec.CoordinatorInstructions = true
	wantInvalid(t, k8s.Update(ctx(t), enabled), "coordinator instruction opt-in is immutable")

	opted := taskSession()
	opted.Spec.CoordinatorInstructions = true
	if err := k8s.Create(ctx(t), opted); err != nil {
		t.Fatal(err)
	}
	if !get(t, opted).Spec.CoordinatorInstructions {
		t.Fatal("explicit opt-in was not stored")
	}
	cleared := get(t, opted)
	cleared.Spec.CoordinatorInstructions = false
	wantInvalid(t, k8s.Update(ctx(t), cleared), "coordinator instruction opt-in is immutable")
	kept := get(t, opted)
	kept.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if err := k8s.Update(ctx(t), kept); err != nil {
		t.Fatal(err)
	}
	if got := get(t, opted); !got.Spec.CoordinatorInstructions || got.Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		t.Fatalf("allowed update did not keep the opt-in: %+v", got.Spec)
	}
}

func TestInstructionPublisherImmutableAndDefaultOff(t *testing.T) {
	s := taskSession()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	if get(t, s).Spec.CoordinatorInstructionPublisher {
		t.Fatal("publisher default enabled")
	}
	changed := get(t, s)
	changed.Spec.CoordinatorInstructionPublisher = true
	wantInvalid(t, k8s.Update(ctx(t), changed), "coordinator instruction publisher selection is immutable")
	fresh := taskSession()
	fresh.Spec.CoordinatorInstructionPublisher = true
	if err := k8s.Create(ctx(t), fresh); err != nil {
		t.Fatal(err)
	}
	changed = get(t, fresh)
	changed.Spec.CoordinatorInstructionPublisher = false
	wantInvalid(t, k8s.Update(ctx(t), changed), "coordinator instruction publisher selection is immutable")
	kept := get(t, fresh)
	kept.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if err := k8s.Update(ctx(t), kept); err != nil {
		t.Fatal(err)
	}
	if !get(t, fresh).Spec.CoordinatorInstructionPublisher {
		t.Fatal("lifecycle update lost publisher selection")
	}
}

// DESIGN-064 6.6: MaxLength bounds the CEL input; bytes() also bounds UTF-8
// storage, since OpenAPI maxLength alone counts Unicode code points.
func TestSummonOutcomeNoteByteValidation(t *testing.T) {
	for _, tc := range []struct {
		name, note string
		valid      bool
	}{
		{"ascii-limit", strings.Repeat("a", 1024), true},
		{"ascii-over", strings.Repeat("a", 1025), false},
		{"utf8-limit", strings.Repeat("é", 512), true},
		{"utf8-over", strings.Repeat("é", 512) + "a", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := taskSession()
			if err := k8s.Create(ctx(t), s); err != nil {
				t.Fatal(err)
			}
			s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeDone, Note: tc.note}
			err := k8s.Status().Update(ctx(t), s)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				if got := get(t, s).Status.Outcome; got == nil || got.Note != tc.note {
					t.Fatal("note did not round trip")
				}
			} else {
				wantInvalid(t, err, "note")
			}
		})
	}
}

func TestSummonOutcomeStatusRoundTrip(t *testing.T) {
	s := summonedTask()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	at := metav1.NewTime(time.Date(2026, 11, 12, 5, 30, 0, 0, time.UTC))
	s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeEscalated, Note: "unreported: agent-failed", At: &at}
	s.Status.Summon = &v1alpha1.SummonStatus{
		Closed:     &v1alpha1.SummonClosedStatus{At: at, Source: "unreported", Reason: "agent-failed"},
		Escalation: &v1alpha1.SummonEscalationStatus{State: "Pending", Name: "esc-" + s.Name},
	}
	if err := k8s.Status().Update(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	got := get(t, s)
	if !apiequality.Semantic.DeepEqual(got.Status.Summon, s.Status.Summon) || !apiequality.Semantic.DeepEqual(got.Status.Outcome, s.Status.Outcome) {
		t.Fatal("CRD pruned the atomic terminal record")
	}
}

// OP777 (DESIGN-064 5.9): status.summon.admittedAt is set once and never
// changes, and the summon record that holds it cannot be removed. Each case
// is one status write on a session whose admittedAt is already set.
func TestSummonAdmittedAtIsSetOnce(t *testing.T) {
	admitted := metav1.NewTime(time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC))
	later := metav1.NewTime(admitted.Add(time.Minute))
	for _, tc := range []struct {
		name  string
		edit  func(*v1alpha1.AgentSessionStatus)
		valid bool
	}{
		{"unchanged", func(st *v1alpha1.AgentSessionStatus) {
			st.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeRunning}
		}, true},
		{"changed", func(st *v1alpha1.AgentSessionStatus) { st.Summon.AdmittedAt = &later }, false},
		{"removed", func(st *v1alpha1.AgentSessionStatus) { st.Summon.AdmittedAt = nil }, false},
		{"summon-removed", func(st *v1alpha1.AgentSessionStatus) { st.Summon = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := summonedTask()
			if err := k8s.Create(ctx(t), s); err != nil {
				t.Fatal(err)
			}
			// The first write sets it: an absent admittedAt may be set.
			s.Status.Summon = &v1alpha1.SummonStatus{AdmittedAt: &admitted}
			if err := k8s.Status().Update(ctx(t), s); err != nil {
				t.Fatalf("setting admittedAt the first time: %v", err)
			}
			s = get(t, s)
			tc.edit(&s.Status)
			err := k8s.Status().Update(ctx(t), s)
			switch {
			case tc.valid && err != nil:
				t.Fatal(err)
			case tc.valid:
				if got := get(t, s).Status.Summon; got == nil || got.AdmittedAt == nil || !got.AdmittedAt.Equal(&admitted) {
					t.Fatalf("admittedAt did not round trip: %+v", got)
				}
			case tc.name == "summon-removed":
				wantInvalid(t, err, "the summon record cannot be removed")
			default:
				wantInvalid(t, err, "admittedAt is set once and never changes")
			}
		})
	}
}

// OP777: status.summon.closed.released may go from absent or false to true,
// never back, and the closed record that holds it cannot be removed.
func TestSummonClosedReleasedIsNeverReversed(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC))
	for _, tc := range []struct {
		name          string
		first, second func(*v1alpha1.SummonStatus)
		want          string
	}{
		{"absent-to-true", func(*v1alpha1.SummonStatus) {}, func(st *v1alpha1.SummonStatus) { st.Closed.Released = true }, ""},
		{"true-to-false", func(st *v1alpha1.SummonStatus) { st.Closed.Released = true }, func(st *v1alpha1.SummonStatus) { st.Closed.Released = false }, "released is never reversed"},
		{"true-closed-removed", func(st *v1alpha1.SummonStatus) { st.Closed.Released = true }, func(st *v1alpha1.SummonStatus) { st.Closed = nil }, "the closed record cannot be removed"},
		{"true-stays-true", func(st *v1alpha1.SummonStatus) { st.Closed.Released = true }, func(st *v1alpha1.SummonStatus) { st.Closed.Reason = "agent-failed" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := summonedTask()
			if err := k8s.Create(ctx(t), s); err != nil {
				t.Fatal(err)
			}
			s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeFailed, Note: "unreported: never-started", At: &at}
			s.Status.Summon = &v1alpha1.SummonStatus{Closed: &v1alpha1.SummonClosedStatus{At: at, Source: "unreported", Reason: "never-started"}}
			tc.first(s.Status.Summon)
			if err := k8s.Status().Update(ctx(t), s); err != nil {
				t.Fatalf("first write: %v", err)
			}
			s = get(t, s)
			tc.second(s.Status.Summon)
			err := k8s.Status().Update(ctx(t), s)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if got := get(t, s).Status.Summon; got == nil || got.Closed == nil || !got.Closed.Released {
					t.Fatalf("released did not round trip: %+v", got)
				}
				return
			}
			wantInvalid(t, err, tc.want)
		})
	}
}

// S1: nested transition rules must also survive removal of the optional
// status object. Deliberately omit privateHistory and every other root guard.
func TestSummonCannotEraseWholeStatus(t *testing.T) {
	for _, record := range []string{"admitted-only", "released-only"} {
		for _, patch := range []struct {
			name string
			kind types.PatchType
			body string
		}{
			{"remove", types.JSONPatchType, `[{"op":"remove","path":"/status"}]`},
			{"null", types.MergePatchType, `{"status":null}`},
		} {
			t.Run(record+"/"+patch.name, func(t *testing.T) {
				s := summonedTask()
				if err := k8s.Create(ctx(t), s); err != nil {
					t.Fatal(err)
				}
				at := metav1.NewTime(time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
				s.Status.Summon = &v1alpha1.SummonStatus{}
				if record == "admitted-only" {
					s.Status.Summon.AdmittedAt = &at
				} else {
					s.Status.Summon.Closed = &v1alpha1.SummonClosedStatus{At: at, Source: "unreported", Reason: "not-charged", Released: true}
				}
				if err := k8s.Status().Update(ctx(t), s); err != nil {
					t.Fatal(err)
				}
				before := get(t, s)
				err := k8s.Status().Patch(ctx(t), s, client.RawPatch(patch.kind, []byte(patch.body)))
				if !apierrors.IsInvalid(err) {
					t.Errorf("whole-status %s accepted: %v", patch.name, err)
				}
				after := get(t, s)
				if !apiequality.Semantic.DeepEqual(before.Status, after.Status) {
					t.Fatal("refused write changed stored summon state")
				}
			})
		}
	}
}

// OP780 (DESIGN-064 5.9, F3): the remote executor latch. Each rule accepts
// the write it allows and refuses the one it forbids; the record itself and
// every field a rule reads stay under their bounds.
func TestSummonExecutorLatchRules(t *testing.T) {
	attempt := func(st *v1alpha1.SummonStatus) {
		st.Executor = &v1alpha1.SummonExecutorStatus{Attempted: true, Intent: "intent-1"}
	}
	for _, tc := range []struct {
		name          string
		first, second func(*v1alpha1.SummonStatus)
		want          string
	}{
		// An attempt is never reversed.
		{"attempt-recorded", func(*v1alpha1.SummonStatus) {}, attempt, ""},
		{"attempt-kept", attempt, func(st *v1alpha1.SummonStatus) { st.Executor.PodUID = "pod-uid-1" }, ""},
		{"attempt-reversed", attempt, func(st *v1alpha1.SummonStatus) { st.Executor.Attempted = false }, "an executor attempt is never reversed"},
		// The intent never changes.
		{"intent-changed", attempt, func(st *v1alpha1.SummonStatus) { st.Executor.Intent = "intent-2" }, "the executor intent never changes"},
		// An attempt has one intent.
		{"attempt-without-intent", func(*v1alpha1.SummonStatus) {}, func(st *v1alpha1.SummonStatus) {
			st.Executor = &v1alpha1.SummonExecutorStatus{Attempted: true}
		}, "an executor attempt has one intent"},
		// podUID once set never changes or goes.
		{"pod-uid-changed", func(st *v1alpha1.SummonStatus) { attempt(st); st.Executor.PodUID = "pod-uid-1" },
			func(st *v1alpha1.SummonStatus) { st.Executor.PodUID = "pod-uid-2" }, "the recorded executor pod UID never changes or goes"},
		{"pod-uid-removed", func(st *v1alpha1.SummonStatus) { attempt(st); st.Executor.PodUID = "pod-uid-1" },
			func(st *v1alpha1.SummonStatus) { st.Executor.PodUID = "" }, "the recorded executor pod UID never changes or goes"},
		{"pod-uid-without-attempt", func(*v1alpha1.SummonStatus) {}, func(st *v1alpha1.SummonStatus) {
			st.Executor = &v1alpha1.SummonExecutorStatus{Intent: "intent-1", PodUID: "pod-uid-1"}
		}, "an executor pod is recorded only for an attempt"},
		// The known non-attempt: only an attempt with no recorded pod, and it
		// may be cleared again (the one re-arm).
		{"not-created-marked", attempt, func(st *v1alpha1.SummonStatus) { st.Executor.NotCreated = true }, ""},
		{"not-created-cleared", func(st *v1alpha1.SummonStatus) { attempt(st); st.Executor.NotCreated = true },
			func(st *v1alpha1.SummonStatus) { st.Executor.NotCreated = false }, ""},
		{"not-created-with-pod", func(st *v1alpha1.SummonStatus) { attempt(st); st.Executor.PodUID = "pod-uid-1" },
			func(st *v1alpha1.SummonStatus) { st.Executor.NotCreated = true }, "only an attempt with no recorded pod can be marked not created"},
		{"not-created-without-attempt", func(*v1alpha1.SummonStatus) {}, func(st *v1alpha1.SummonStatus) {
			st.Executor = &v1alpha1.SummonExecutorStatus{NotCreated: true}
		}, "only an attempt with no recorded pod can be marked not created"},
		// The latch is never removed.
		{"latch-removed", attempt, func(st *v1alpha1.SummonStatus) { st.Executor = nil }, "the executor latch cannot be removed"},
		// Bounds: every string a rule reads has a MaxLength.
		{"intent-too-long", func(*v1alpha1.SummonStatus) {}, func(st *v1alpha1.SummonStatus) {
			st.Executor = &v1alpha1.SummonExecutorStatus{Attempted: true, Intent: strings.Repeat("i", 129)}
		}, "Too long"},
		{"pod-uid-too-long", attempt, func(st *v1alpha1.SummonStatus) { st.Executor.PodUID = strings.Repeat("p", 129) }, "Too long"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := summonedRemote()
			if err := k8s.Create(ctx(t), s); err != nil {
				t.Fatal(err)
			}
			at := metav1.NewTime(time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC))
			s.Status.Summon = &v1alpha1.SummonStatus{AdmittedAt: &at}
			tc.first(s.Status.Summon)
			if err := k8s.Status().Update(ctx(t), s); err != nil {
				t.Fatalf("first write: %v", err)
			}
			s = get(t, s)
			tc.second(s.Status.Summon)
			want := s.Status.Summon.Executor.DeepCopy()
			err := k8s.Status().Update(ctx(t), s)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if got := get(t, s).Status.Summon; got == nil || !apiequality.Semantic.DeepEqual(got.Executor, want) {
					t.Fatalf("the executor latch did not round trip: %+v, want %+v", got, want)
				}
				return
			}
			wantInvalid(t, err, tc.want)
		})
	}
}

// TestTitleBounds is DESIGN-065 ruling 1: each bound accepts and refuses.
// Adding, changing and removing a title are accepted (rule 8.1); the bounds
// on an update are TestTitleBoundsOnUpdate.
func TestTitleBounds(t *testing.T) {
	title96 := strings.Repeat("é", 48)
	title97 := strings.Repeat("é", 47) + "€"
	if len(title96) != 96 || utf8.RuneCountInString(title96) != 48 {
		t.Fatalf("96-byte fixture is %d bytes and %d runes", len(title96), utf8.RuneCountInString(title96))
	}
	if len(title97) != 97 || utf8.RuneCountInString(title97) != 48 {
		t.Fatalf("97-byte fixture is %d bytes and %d runes", len(title97), utf8.RuneCountInString(title97))
	}
	accept := []struct{ name, title string }{
		{"absent", ""},
		{"role text", "Main coordinator"},
		{"accented letter", "Café"},
		{"leading digit", "1a"},
		{"letter then combining mark", "e\u0301"},
		{"hyphen later", "Main-coordinator"},
		{"48 characters", strings.Repeat("a", 48)},
		{"96 bytes", title96},
	}
	for _, c := range accept {
		t.Run("accept "+c.name, func(t *testing.T) {
			var sent any
			if c.name != "absent" {
				sent = c.title
			}
			got := createTitle(t, sent)
			if got.Spec.Title != c.title {
				t.Fatalf("stored %q", got.Spec.Title)
			}
		})
	}
	cjk144 := strings.Repeat("\u4e00", 48)
	euro48 := strings.Repeat("€", 48)
	if len(cjk144) != 144 || utf8.RuneCountInString(cjk144) != 48 || len(euro48) != 144 {
		t.Fatalf("144-byte fixtures are %d and %d bytes", len(cjk144), len(euro48))
	}
	refuse := []struct{ name, title, want string }{
		{"empty", "", "spec.title"},
		{"49 characters", strings.Repeat("a", 49), "spec.title"},
		{"97 bytes", title97, "at most 96 bytes"},
		{"144 bytes", cjk144, "at most 96 bytes"},
		{"48 euros", euro48, "at most 96 bytes"},
		{"bar", "|", "no bar"},
		{"bar in text", "a|b", "no bar"},
		{"control character", "a\u0007b", "no control"},
		{"tab", "a\tb", "letter or a digit"},
		{"newline", "a\nb", "letter or a digit"},
		{"no-break space", "a\u00a0b", "letter or a digit"},
		{"format character", "a\u200bb", "letter or a digit"},
		{"above U+FFFF", "a\U00010000", "nothing above U+FFFF"},
		{"leading hyphen", "-Main", "letter or a digit"},
		{"leading full stop", ".Main", "letter or a digit"},
		{"leading punctuation", "!Main", "letter or a digit"},
		{"leading symbol", "€Main", "letter or a digit"},
		{"leading space", " Main", "no leading or trailing space"},
		{"trailing space", "Main ", "no leading or trailing space"},
		{"double space", "Main  coordinator", "no leading or trailing space"},
	}
	for _, c := range refuse {
		t.Run("refuse "+c.name, func(t *testing.T) {
			wantInvalid(t, createTitleErr(t, c.title), c.want)
		})
	}

	s := remoteSession()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	s.Status.RemoteControl = &v1alpha1.RemoteControlStatus{Title: "Opus | Main coordinator"}
	if err := k8s.Status().Update(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	if got := get(t, s).Status.RemoteControl; got == nil || got.Title != "Opus | Main coordinator" {
		t.Fatalf("status title %+v", got)
	}
	got := get(t, s)
	got.Status.RemoteControl.Title = strings.Repeat("a", 128)
	if err := k8s.Status().Update(ctx(t), got); err != nil {
		t.Fatal(err)
	}
	got = get(t, s)
	got.Status.RemoteControl.Title = strings.Repeat("b", 129)
	wantInvalid(t, k8s.Status().Update(ctx(t), got), "status.remoteControl.title")
	got = get(t, s)
	got.Status.RemoteControl.Title = strings.Repeat("€", 43)
	if len(got.Status.RemoteControl.Title) != 129 {
		t.Fatalf("status byte fixture is %d bytes", len(got.Status.RemoteControl.Title))
	}
	wantInvalid(t, k8s.Status().Update(ctx(t), got), "at most 128 bytes")
}

// TestTitleBoundsOnUpdate is DESIGN-065 rule 8.1: every title bound still
// refuses an update, and no other spec field becomes changeable. Adding,
// changing and removing a valid title are TestSpecImmutableExceptOperatingModeAndLifecycle.
func TestTitleBoundsOnUpdate(t *testing.T) {
	title97 := strings.Repeat("é", 47) + "€"
	refuse := []struct {
		name string
		edit func(*v1alpha1.AgentSessionSpec)
		want string
	}{
		{"49 characters", func(s *v1alpha1.AgentSessionSpec) { s.Title = strings.Repeat("a", 49) }, "spec.title"},
		{"97 bytes", func(s *v1alpha1.AgentSessionSpec) { s.Title = title97 }, "at most 96 bytes"},
		{"bar", func(s *v1alpha1.AgentSessionSpec) { s.Title = "a|b" }, "no bar"},
		{"control character", func(s *v1alpha1.AgentSessionSpec) { s.Title = "a\u0007b" }, "no control"},
		{"leading space", func(s *v1alpha1.AgentSessionSpec) { s.Title = " Main" }, "no leading or trailing space"},
		{"change repo", func(s *v1alpha1.AgentSessionSpec) { s.Repo = "haynesnetwork" }, "spec.repo"},
		{"change model", func(s *v1alpha1.AgentSessionSpec) { s.Model = "claude-opus-5" }, "spec.model"},
	}
	for _, c := range refuse {
		t.Run(c.name, func(t *testing.T) {
			s := remoteSession()
			s.Spec.Title = "Main coordinator"
			if err := k8s.Create(ctx(t), s); err != nil {
				t.Fatal(err)
			}
			c.edit(&s.Spec)
			wantInvalid(t, k8s.Update(ctx(t), s), c.want)
		})
	}
}

// TestRemoteRenameStatusBounds is DESIGN-065 rule 8.8: providerTitle and
// rename accept their bounds and refuse past them, including the held
// reason quiet.
func TestRemoteRenameStatusBounds(t *testing.T) {
	s := remoteSession()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	at := metav1.NewTime(time.Date(2026, 11, 13, 14, 2, 0, 0, time.UTC))
	set := func(rc v1alpha1.RemoteControlStatus) error {
		t.Helper()
		got := get(t, s)
		got.Status.RemoteControl = rc.DeepCopy()
		return k8s.Status().Update(ctx(t), got)
	}
	ok := v1alpha1.RemoteControlStatus{
		ProviderTitle: strings.Repeat("a", 128),
		Rename:        &v1alpha1.RemoteRenameStatus{State: "Held", Reason: "quiet", Since: &at, Attempts: 0},
	}
	if err := set(ok); err != nil {
		t.Fatal(err)
	}
	for _, attempts := range []int32{0, 1, 2} {
		rc := ok
		rc.Rename = &v1alpha1.RemoteRenameStatus{State: "Applied", Reason: "confirmed", Attempts: attempts}
		if err := set(rc); err != nil {
			t.Fatalf("attempts %d: %v", attempts, err)
		}
	}
	long := ok
	long.ProviderTitle = strings.Repeat("a", 129)
	wantInvalid(t, set(long), "status.remoteControl.providerTitle")
	bytes129 := ok
	bytes129.ProviderTitle = strings.Repeat("€", 43)
	if len(bytes129.ProviderTitle) != 129 {
		t.Fatalf("byte fixture is %d", len(bytes129.ProviderTitle))
	}
	wantInvalid(t, set(bytes129), "at most 128 bytes")
	badState := ok
	badState.Rename = &v1alpha1.RemoteRenameStatus{State: "held", Reason: "quiet"}
	wantInvalid(t, set(badState), "status.remoteControl.rename.state")
	badReason := ok
	badReason.Rename = &v1alpha1.RemoteRenameStatus{State: "Held", Reason: "nope"}
	wantInvalid(t, set(badReason), "status.remoteControl.rename.reason")
	badAttempts := ok
	badAttempts.Rename = &v1alpha1.RemoteRenameStatus{State: "Held", Reason: "paced", Attempts: 3}
	wantInvalid(t, set(badAttempts), "status.remoteControl.rename.attempts")
}

func createTitle(t *testing.T, title any) *v1alpha1.AgentSession {
	t.Helper()
	n := name()
	if err := createTitleErrNamed(t, n, title); err != nil {
		t.Fatal(err)
	}
	got := &v1alpha1.AgentSession{}
	if err := k8s.Get(ctx(t), client.ObjectKey{Namespace: ns, Name: n}, got); err != nil {
		t.Fatal(err)
	}
	return got
}

func createTitleErr(t *testing.T, title any) error {
	t.Helper()
	return createTitleErrNamed(t, name(), title)
}

func createTitleErrNamed(t *testing.T, n string, title any) error {
	t.Helper()
	spec := map[string]any{
		"repo": "haynes-ops", "agent": "claude", "mode": "task",
		"model": "claude-opus-5-5", "prompt": "fix the typo",
	}
	if title != nil {
		spec["title"] = title
	}
	u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	u.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("AgentSession"))
	u.SetName(n)
	u.SetNamespace(ns)
	return k8s.Create(ctx(t), u)
}
