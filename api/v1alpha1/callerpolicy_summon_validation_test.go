package v1alpha1_test

// The CallerPolicy summon cases for DESIGN-064 6.4. The front-door cases stay
// in TestCallerPolicyValidation. Each rule here has an accept and a refuse.

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func i32(n int32) *int32 { return &n }

func hasLane(entries []v1alpha1.CallerPolicySummonSession, lane v1alpha1.Lane) bool {
	for _, e := range entries {
		if e.Lane == lane {
			return true
		}
	}
	return false
}

func setAccount(p *v1alpha1.CallerPolicy, account string) {
	p.Spec.Summon.ServiceAccount = account
	p.Name = strings.ReplaceAll(account, "/", ".")
}

func summonPolicy(entries ...v1alpha1.CallerPolicySummonSession) *v1alpha1.CallerPolicy {
	id := strings.TrimPrefix(name(), "s-")
	p := &v1alpha1.CallerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "upgrade-agent.sa-" + id, Namespace: ns},
		Spec: v1alpha1.CallerPolicySpec{
			Summon: &v1alpha1.CallerPolicySummon{
				ServiceAccount: "upgrade-agent/sa-" + id,
				Limits: v1alpha1.CallerPolicySummonLimits{
					Concurrent:     1,
					CreatesPerHour: 6,
				},
				Sessions: entries,
			},
		},
	}
	if hasLane(entries, v1alpha1.LaneEscalation) {
		n := int32(2)
		p.Spec.Summon.Limits.Escalations = &n
	}
	return p
}

func summonEntry(kind v1alpha1.SummonKind, lane v1alpha1.Lane) v1alpha1.CallerPolicySummonSession {
	e := v1alpha1.CallerPolicySummonSession{
		Kind:         kind,
		Lane:         lane,
		Priority:     v1alpha1.SummonPriorityNormal,
		NamePrefixes: []string{"wo-"},
		Repo:         "haynes-ops",
		Profile:      "ops",
		Model: v1alpha1.CallerPolicySummonModel{
			Default:  "claude-opus-5-5",
			Fallback: "claude-opus-5",
		},
		Effort:       "xhigh",
		OnUnreported: v1alpha1.SummonOnUnreportedFailed,
		Brief: v1alpha1.CallerPolicySummonBrief{
			Template: "Follow the runbook in the worktree.",
			Fields:   []string{"reason"},
		},
	}
	switch lane {
	case v1alpha1.LaneRemediation:
		e.Priority = v1alpha1.SummonPriorityUrgent
		e.NamePrefixes = []string{"rem-responder-"}
		e.OnUnreported = v1alpha1.SummonOnUnreportedEscalated
		e.MaxHold = "20m"
	case v1alpha1.LaneEscalation:
		e.Priority = v1alpha1.SummonPriorityUrgent
		e.NamePrefixes = []string{"esc-responder-"}
		e.Model.Default = "claude-sonnet-5-5"
	case v1alpha1.LaneCuration:
		e.Priority = v1alpha1.SummonPriorityBulk
		e.NamePrefixes = []string{"wo-cigar-curate-"}
		e.MaxHold = "12h"
	}
	if kind == v1alpha1.SummonKindTask {
		e.Timeout = "40m"
		if lane == v1alpha1.LaneCuration {
			e.Timeout = "2h"
		} else {
			e.MaxTurns = i32(120)
		}
	}
	if kind == v1alpha1.SummonKindRemote && lane == v1alpha1.LaneUpgrade {
		e.StaleAfter = "180m"
	}
	return e
}

func pairPolicy() *v1alpha1.CallerPolicy {
	return summonPolicy(
		summonEntry(v1alpha1.SummonKindTask, v1alpha1.LaneRemediation),
		summonEntry(v1alpha1.SummonKindRemote, v1alpha1.LaneEscalation),
	)
}

func sixEntries() []v1alpha1.CallerPolicySummonSession {
	return []v1alpha1.CallerPolicySummonSession{
		summonEntry(v1alpha1.SummonKindTask, v1alpha1.LaneRemediation),
		summonEntry(v1alpha1.SummonKindRemote, v1alpha1.LaneEscalation),
		summonEntry(v1alpha1.SummonKindRemote, v1alpha1.LaneUpgrade),
		summonEntry(v1alpha1.SummonKindTask, v1alpha1.LaneCuration),
		summonEntry(v1alpha1.SummonKindTask, v1alpha1.LaneUpgrade),
		summonEntry(v1alpha1.SummonKindRemote, v1alpha1.LaneRemediation),
	}
}

func asUnstructured(t *testing.T, p *v1alpha1.CallerPolicy) *unstructured.Unstructured {
	t.Helper()
	p.APIVersion, p.Kind = v1alpha1.GroupVersion.String(), "CallerPolicy"
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(p)
	if err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: u}
}

func TestCallerPolicySummonValidation(t *testing.T) {
	t.Run("accept meatball front door", func(t *testing.T) {
		p := validCallerPolicy()
		p.Name = "meatball"
		if err := k8s.Create(ctx(t), p); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("accept alert-responder example", func(t *testing.T) {
		p := pairPolicy()
		setAccount(p, "upgrade-agent/alert-responder")
		if err := k8s.Create(ctx(t), p); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("accept curation without escalations", func(t *testing.T) {
		p := summonPolicy(summonEntry(v1alpha1.SummonKindTask, v1alpha1.LaneCuration))
		if p.Spec.Summon.Limits.Escalations != nil {
			t.Fatal("curation policy must omit escalations")
		}
		if err := k8s.Create(ctx(t), p); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("accept upgrade without staleAfter", func(t *testing.T) {
		e := summonEntry(v1alpha1.SummonKindRemote, v1alpha1.LaneUpgrade)
		e.StaleAfter = ""
		p := summonPolicy(e)
		if err := k8s.Create(ctx(t), p); err != nil {
			t.Fatal(err)
		}
		got := &v1alpha1.CallerPolicy{}
		if err := k8s.Get(ctx(t), client.ObjectKeyFromObject(p), got); err != nil {
			t.Fatal(err)
		}
		if got.Spec.Summon.Sessions[0].StaleAfter != "" {
			t.Fatalf("staleAfter = %q, want it omitted", got.Spec.Summon.Sessions[0].StaleAfter)
		}
	})

	t.Run("accept same lane with different kinds", func(t *testing.T) {
		p := summonPolicy(
			summonEntry(v1alpha1.SummonKindTask, v1alpha1.LaneUpgrade),
			summonEntry(v1alpha1.SummonKindRemote, v1alpha1.LaneUpgrade),
		)
		if err := k8s.Create(ctx(t), p); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("accept six entries", func(t *testing.T) {
		if err := k8s.Create(ctx(t), summonPolicy(sixEntries()...)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("accept urgent priority on an upgrade", func(t *testing.T) {
		e := summonEntry(v1alpha1.SummonKindRemote, v1alpha1.LaneUpgrade)
		e.Priority = v1alpha1.SummonPriorityUrgent
		if err := k8s.Create(ctx(t), summonPolicy(e)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("accept a namespace that only starts like a forbidden one", func(t *testing.T) {
		p := summonPolicy(summonEntry(v1alpha1.SummonKindRemote, v1alpha1.LaneUpgrade))
		setAccount(p, "dev-agentsfoo/worker")
		if err := k8s.Create(ctx(t), p); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("accept a 63-character namespace", func(t *testing.T) {
		p := summonPolicy(summonEntry(v1alpha1.SummonKindRemote, v1alpha1.LaneUpgrade))
		setAccount(p, strings.Repeat("a", 63)+"/worker")
		if err := k8s.Create(ctx(t), p); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("queued and queuedUrgent default to 4", func(t *testing.T) {
		p := summonPolicy(summonEntry(v1alpha1.SummonKindTask, v1alpha1.LaneCuration))
		if err := k8s.Create(ctx(t), p); err != nil {
			t.Fatal(err)
		}
		got := &v1alpha1.CallerPolicy{}
		if err := k8s.Get(ctx(t), client.ObjectKeyFromObject(p), got); err != nil {
			t.Fatal(err)
		}
		if got.Spec.Summon.Limits.Queued == nil || *got.Spec.Summon.Limits.Queued != 4 {
			t.Fatalf("queued = %v, want 4", got.Spec.Summon.Limits.Queued)
		}
		if got.Spec.Summon.Limits.QueuedUrgent == nil || *got.Spec.Summon.Limits.QueuedUrgent != 4 {
			t.Fatalf("queuedUrgent = %v, want 4", got.Spec.Summon.Limits.QueuedUrgent)
		}
		if got.Spec.Summon.Limits.Escalations != nil {
			t.Fatalf("escalations = %v, want it absent", got.Spec.Summon.Limits.Escalations)
		}
	})

	t.Run("queued zero is kept", func(t *testing.T) {
		p := summonPolicy(summonEntry(v1alpha1.SummonKindTask, v1alpha1.LaneCuration))
		p.Spec.Summon.Limits.Queued = i32(0)
		if err := k8s.Create(ctx(t), p); err != nil {
			t.Fatal(err)
		}
		got := &v1alpha1.CallerPolicy{}
		if err := k8s.Get(ctx(t), client.ObjectKeyFromObject(p), got); err != nil {
			t.Fatal(err)
		}
		if got.Spec.Summon.Limits.Queued == nil || *got.Spec.Summon.Limits.Queued != 0 {
			t.Fatalf("queued = %v, want 0", got.Spec.Summon.Limits.Queued)
		}
	})

	accepts := []struct {
		title  string
		mutate func(*v1alpha1.CallerPolicy)
	}{
		{"concurrent 1 and 4", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.Concurrent = 4 }},
		{"escalations 1 and 4", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.Escalations = i32(4) }},
		{"queued 16", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.Queued = i32(16) }},
		{"queuedUrgent 1", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.QueuedUrgent = i32(1) }},
		{"queuedUrgent 16", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.QueuedUrgent = i32(16) }},
		{"createsPerHour 1", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.CreatesPerHour = 1 }},
		{"createsPerHour 30", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.CreatesPerHour = 30 }},
		{"timeout 4h", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Timeout = "4h" }},
		{"maxTurns 0", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].MaxTurns = i32(0) }},
		{"maxTurns 500", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].MaxTurns = i32(500) }},
		{"maxTurns omitted", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].MaxTurns = nil }},
		{"maxHold 24h", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].MaxHold = "24h" }},
		{"staleAfter 24h", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions = []v1alpha1.CallerPolicySummonSession{
				summonEntry(v1alpha1.SummonKindRemote, v1alpha1.LaneUpgrade),
			}
			p.Spec.Summon.Sessions[0].StaleAfter = "24h"
			p.Spec.Summon.Limits.Escalations = nil
		}},
		{"four prefixes", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].NamePrefixes = []string{"rem-a-", "rem-b-", "rem-c-", "rem-d-"}
		}},
		{"template of 8192", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Brief.Template = strings.Repeat("a", 8192)
		}},
		{"eight brief fields", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Brief.Fields = []string{"a", "b", "c", "d", "e", "f", "g", "h"}
		}},
		{"brief fields omitted", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Brief.Fields = nil
		}},
		{"a 32-character field name", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Brief.Fields = []string{"a" + strings.Repeat("b", 31)}
		}},
		{"effort ultracode", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Effort = "ultracode" }},
		{"model with one numeric segment", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Model = v1alpha1.CallerPolicySummonModel{
				Default: "claude-opus-5", Fallback: "claude-opus-5-5",
			}
		}},
	}
	for _, c := range accepts {
		t.Run("accept "+c.title, func(t *testing.T) {
			p := pairPolicy()
			c.mutate(p)
			if err := k8s.Create(ctx(t), p); err != nil {
				t.Fatal(err)
			}
		})
	}

	refuses := []struct {
		title  string
		mutate func(*v1alpha1.CallerPolicy)
		want   string
	}{
		{"both shapes", func(p *v1alpha1.CallerPolicy) {
			front := validCallerPolicy()
			p.Spec.ServiceAccount = front.Spec.ServiceAccount
			p.Spec.Mode = front.Spec.Mode
			p.Spec.Profile = front.Spec.Profile
			p.Spec.Models = front.Spec.Models
			p.Spec.MaxConcurrent = front.Spec.MaxConcurrent
			p.Spec.MaxTimeout = front.Spec.MaxTimeout
			p.Spec.MaxWorkers = front.Spec.MaxWorkers
		}, "exactly one of the front-door shape"},
		{"name does not match the account", func(p *v1alpha1.CallerPolicy) { p.Name = "not-the-account" }, "slash replaced by a dot"},
		{"namespace dev-agents", func(p *v1alpha1.CallerPolicy) { setAccount(p, "dev-agents/alert-responder") }, "dev-agents, dev-env-system or dev-tools"},
		{"namespace dev-env-system", func(p *v1alpha1.CallerPolicy) { setAccount(p, "dev-env-system/alert-responder") }, "dev-agents, dev-env-system or dev-tools"},
		{"namespace dev-tools", func(p *v1alpha1.CallerPolicy) { setAccount(p, "dev-tools/alert-responder") }, "dev-agents, dev-env-system or dev-tools"},
		{"account without a slash", func(p *v1alpha1.CallerPolicy) { setAccount(p, "alert-responder") }, "serviceAccount"},
		{"account with a trailing hyphen", func(p *v1alpha1.CallerPolicy) { setAccount(p, "upgrade-agent-/worker") }, "serviceAccount"},
		{"64-character namespace", func(p *v1alpha1.CallerPolicy) { setAccount(p, strings.Repeat("a", 64)+"/worker") }, "serviceAccount"},
		{"account over 128 characters", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.ServiceAccount = strings.Repeat("a", 129)
			p.Name = "short-name"
		}, "serviceAccount"},
		{"concurrent 0", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.Concurrent = 0 }, "concurrent"},
		{"concurrent 5", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.Concurrent = 5 }, "concurrent"},
		{"escalations 0", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.Escalations = i32(0) }, "escalations"},
		{"escalations 5", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.Escalations = i32(5) }, "escalations"},
		{"escalations absent with an escalation entry", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.Escalations = nil }, "required exactly when"},
		{"queued -1", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.Queued = i32(-1) }, "queued"},
		{"queued 17", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.Queued = i32(17) }, "queued"},
		{"queuedUrgent 0", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.QueuedUrgent = i32(0) }, "queuedUrgent"},
		{"queuedUrgent 17", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.QueuedUrgent = i32(17) }, "queuedUrgent"},
		{"createsPerHour 0", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.CreatesPerHour = 0 }, "createsPerHour"},
		{"createsPerHour 31", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Limits.CreatesPerHour = 31 }, "createsPerHour"},
		{"no sessions", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions = nil
			p.Spec.Summon.Limits.Escalations = nil
		}, "sessions"},
		{"seven sessions", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions = append(sixEntries(), summonEntry(v1alpha1.SummonKindTask, v1alpha1.LaneRemediation))
		}, "at most 6"},
		{"duplicate kind and lane", func(p *v1alpha1.CallerPolicy) {
			a := summonEntry(v1alpha1.SummonKindRemote, v1alpha1.LaneUpgrade)
			b := summonEntry(v1alpha1.SummonKindRemote, v1alpha1.LaneUpgrade)
			b.NamePrefixes = []string{"wo-night-"}
			p.Spec.Summon.Sessions = []v1alpha1.CallerPolicySummonSession{a, b}
			p.Spec.Summon.Limits.Escalations = nil
		}, "(kind, lane) is unique"},
		{"kind local", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Kind = "local" }, "kind"},
		{"lane triage", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Lane = "triage" }, "lane"},
		{"priority high", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Priority = "high" }, "priority"},
		{"remediation priority normal", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Priority = v1alpha1.SummonPriorityNormal
		}, "priority urgent is required"},
		{"escalation priority bulk", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[1].Priority = v1alpha1.SummonPriorityBulk
		}, "priority urgent is required"},
		{"five prefixes", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].NamePrefixes = []string{"rem-a-", "rem-b-", "rem-c-", "rem-d-", "rem-e-"}
		}, "at most 4"},
		{"no prefixes", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].NamePrefixes = nil }, "namePrefixes"},
		{"prefix does not end in a dash", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].NamePrefixes = []string{"rem-responder"}
		}, "namePrefixes"},
		{"prefix in capitals", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].NamePrefixes = []string{"Rem-responder-"}
		}, "namePrefixes"},
		{"prefix of 33 characters", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].NamePrefixes = []string{"rem-" + strings.Repeat("a", 28) + "-"}
		}, "namePrefixes"},
		{"prefix for the wrong lane", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].NamePrefixes = []string{"wo-"}
		}, "namePrefixes are DNS prefixes"},
		{"mixed lane prefixes", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].NamePrefixes = []string{"rem-a-", "wo-b-"}
		}, "namePrefixes are DNS prefixes"},
		{"prefix esc-rem-", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[1].NamePrefixes = []string{"esc-rem-"}
		}, "esc-rem-"},
		{"duplicate prefixes", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].NamePrefixes = []string{"rem-a-", "rem-a-"}
		}, "Duplicate value"},
		{"repo path", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Repo = ".." }, "not a path"},
		{"repo dot", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Repo = "." }, "not a path"},
		{"repo with an owner", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Repo = "thaynes43/haynes-ops" }, "repo"},
		{"repo of 101 characters", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Repo = strings.Repeat("a", 101)
		}, "repo"},
		{"profile dev", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Profile = "dev" }, "profile"},
		{"profile full", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Profile = "full" }, "profile"},
		{"profile workbench", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Profile = "workbench" }, "profile"},
		{"model alias", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Model.Default = "opus" }, "model.default"},
		{"model without a numeric segment", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Model.Default = "claude-opus"
		}, "model.default"},
		{"model of another vendor", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Model.Default = "gpt-6-astra"
		}, "model.default"},
		{"model over 64 characters", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Model.Default = "claude-" + strings.Repeat("a", 55) + "-12"
		}, "model.default"},
		{"fallback equal to the default", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Model.Fallback = p.Spec.Summon.Sessions[0].Model.Default
		}, "must differ"},
		{"effort in capitals", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Effort = "XHigh" }, "effort"},
		{"effort with a hyphen", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Effort = "x-high" }, "effort"},
		{"effort of 33 characters", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Effort = strings.Repeat("a", 33)
		}, "effort"},
		{"task without timeout", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Timeout = "" }, "required for a task entry"},
		{"timeout zero", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Timeout = "0s" }, "positive Go duration"},
		{"timeout over 4h", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Timeout = "4h1s" }, "at most 4h"},
		{"timeout on a remote entry", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[1].Timeout = "40m" }, "not allowed for a remote entry"},
		{"maxTurns over 500", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].MaxTurns = i32(501) }, "maxTurns"},
		{"maxTurns negative", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].MaxTurns = i32(-1) }, "maxTurns"},
		{"maxTurns on a remote entry", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[1].MaxTurns = i32(1) }, "task entry only"},
		{"staleAfter on a remediation", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].StaleAfter = "180m" }, "remote upgrade entry only"},
		{"staleAfter on an escalation", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[1].StaleAfter = "180m" }, "remote upgrade entry only"},
		{"staleAfter zero", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions = []v1alpha1.CallerPolicySummonSession{summonEntry(v1alpha1.SummonKindRemote, v1alpha1.LaneUpgrade)}
			p.Spec.Summon.Sessions[0].StaleAfter = "0s"
			p.Spec.Summon.Limits.Escalations = nil
		}, "positive Go duration"},
		{"staleAfter over 24h", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions = []v1alpha1.CallerPolicySummonSession{summonEntry(v1alpha1.SummonKindRemote, v1alpha1.LaneUpgrade)}
			p.Spec.Summon.Sessions[0].StaleAfter = "24h1s"
			p.Spec.Summon.Limits.Escalations = nil
		}, "at most 24h"},
		{"staleAfter on a task upgrade", func(p *v1alpha1.CallerPolicy) {
			e := summonEntry(v1alpha1.SummonKindTask, v1alpha1.LaneUpgrade)
			e.StaleAfter = "180m"
			p.Spec.Summon.Sessions = []v1alpha1.CallerPolicySummonSession{e}
			p.Spec.Summon.Limits.Escalations = nil
		}, "remote upgrade entry only"},
		{"remediation without maxHold", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].MaxHold = "" }, "required for remediation and curation"},
		{"maxHold on an escalation", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[1].MaxHold = "20m" }, "not allowed for upgrade or escalation"},
		{"maxHold zero", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].MaxHold = "0s" }, "positive Go duration"},
		{"maxHold over 24h", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].MaxHold = "24h1s" }, "at most 24h"},
		{"remediation onUnreported failed", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].OnUnreported = v1alpha1.SummonOnUnreportedFailed
		}, "escalated for remediation"},
		{"escalation onUnreported escalated", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[1].OnUnreported = v1alpha1.SummonOnUnreportedEscalated
		}, "failed for every other lane"},
		{"empty template", func(p *v1alpha1.CallerPolicy) { p.Spec.Summon.Sessions[0].Brief.Template = "" }, "template"},
		{"template of 8193", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Brief.Template = strings.Repeat("a", 8193)
		}, "template"},
		{"nine brief fields", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Brief.Fields = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
		}, "at most 8"},
		{"brief field in capitals", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Brief.Fields = []string{"Alert"}
		}, "fields"},
		{"brief field starting with a digit", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Brief.Fields = []string{"1alert"}
		}, "fields"},
		{"brief field with a hyphen", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Brief.Fields = []string{"alert-id"}
		}, "fields"},
		{"brief field of 33 characters", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Brief.Fields = []string{"a" + strings.Repeat("b", 32)}
		}, "fields"},
		{"duplicate brief fields", func(p *v1alpha1.CallerPolicy) {
			p.Spec.Summon.Sessions[0].Brief.Fields = []string{"alert", "alert"}
		}, "Duplicate value"},
	}
	for _, c := range refuses {
		t.Run("refuse "+c.title, func(t *testing.T) {
			p := pairPolicy()
			c.mutate(p)
			wantInvalid(t, k8s.Create(ctx(t), p), c.want)
		})
	}

	t.Run("refuse remediation without an escalation entry", func(t *testing.T) {
		p := summonPolicy(summonEntry(v1alpha1.SummonKindTask, v1alpha1.LaneRemediation))
		p.Spec.Summon.Limits.Escalations = nil
		wantInvalid(t, k8s.Create(ctx(t), p), "requires an escalation entry")
	})

	t.Run("refuse escalations without an escalation entry", func(t *testing.T) {
		p := summonPolicy(summonEntry(v1alpha1.SummonKindTask, v1alpha1.LaneCuration))
		p.Spec.Summon.Limits.Escalations = i32(1)
		wantInvalid(t, k8s.Create(ctx(t), p), "required exactly when")
	})

	t.Run("refuse curation without maxHold", func(t *testing.T) {
		e := summonEntry(v1alpha1.SummonKindTask, v1alpha1.LaneCuration)
		e.MaxHold = ""
		wantInvalid(t, k8s.Create(ctx(t), summonPolicy(e)), "required for remediation and curation")
	})

	t.Run("refuse maxHold on an upgrade", func(t *testing.T) {
		e := summonEntry(v1alpha1.SummonKindRemote, v1alpha1.LaneUpgrade)
		e.MaxHold = "20m"
		wantInvalid(t, k8s.Create(ctx(t), summonPolicy(e)), "not allowed for upgrade or escalation")
	})

	t.Run("refuse an empty spec", func(t *testing.T) {
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": v1alpha1.GroupVersion.String(),
			"kind":       "CallerPolicy",
			"metadata":   map[string]any{"name": name(), "namespace": ns},
			"spec":       map[string]any{},
		}}
		wantInvalid(t, k8s.Create(ctx(t), u), "exactly one of the front-door shape")
	})

	t.Run("refuse summon plus one front-door field", func(t *testing.T) {
		u := asUnstructured(t, pairPolicy())
		if err := unstructured.SetNestedField(u.Object, "dev-env-system/dev-env-meatball", "spec", "serviceAccount"); err != nil {
			t.Fatal(err)
		}
		wantInvalid(t, k8s.Create(ctx(t), u), "exactly one of the front-door shape")
	})

	t.Run("refuse an explicit zero front-door concurrent", func(t *testing.T) {
		u := asUnstructured(t, validCallerPolicy())
		if err := unstructured.SetNestedField(u.Object, int64(0), "spec", "maxConcurrent"); err != nil {
			t.Fatal(err)
		}
		wantInvalid(t, k8s.Create(ctx(t), u), "maxConcurrent")
	})
}
