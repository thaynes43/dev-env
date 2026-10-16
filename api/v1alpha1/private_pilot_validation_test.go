package v1alpha1_test

import (
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
)

func TestPrivatePilotSchemaDryRunBoundaries(t *testing.T) {
	f := testutil.NewPilot(t)
	for _, slot := range []int{0, 1} {
		s := f.Session(t, slot)
		s.Namespace = ns
		s.UID = ""
		s.ResourceVersion = ""
		s.Generation = 0
		if err := k8s.Create(ctx(t), s, client.DryRunAll); err != nil {
			t.Fatal("exact pilot schema refused", err)
		}
		var absent v1alpha1.AgentSession
		if err := k8s.Get(ctx(t), client.ObjectKeyFromObject(s), &absent); !apierrors.IsNotFound(err) {
			t.Fatal("dry-run persisted", err)
		}
	}
	changes := map[string]func(*v1alpha1.AgentSession){
		"missing-binding": func(s *v1alpha1.AgentSession) { s.Spec.PrivatePilot = nil },
		"wrong-common": func(s *v1alpha1.AgentSession) {
			s.Spec.PrivatePilot.CommonRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		},
		"bad-seal":   func(s *v1alpha1.AgentSession) { s.Spec.PrivatePilot.SealUID = "wrong" },
		"bad-policy": func(s *v1alpha1.AgentSession) { s.Spec.PrivatePilot.PolicyDigest = "wrong" },
		"third-slot": func(s *v1alpha1.AgentSession) { s.Spec.PrivatePilot.Slot = 2 },
		"canary-mix": func(s *v1alpha1.AgentSession) {
			s.Spec.ProjectCanary = testutil.New(t, "codex", "dev/dev-env").Session.Spec.ProjectCanary
		},
		"missing-common":    func(s *v1alpha1.AgentSession) { s.Spec.CommonRules = nil },
		"missing-directory": func(s *v1alpha1.AgentSession) { s.Spec.SessionDirectory = nil },
		"foreign-project":   func(s *v1alpha1.AgentSession) { s.Spec.SessionDirectory.Project = "foreign" },
		"foreign-parent":    func(s *v1alpha1.AgentSession) { s.Spec.Parent = "foreign/caller" },
		"foreign-repo":      func(s *v1alpha1.AgentSession) { s.Spec.Repo = "foreign" },
		"foreign-base":      func(s *v1alpha1.AgentSession) { s.Spec.Base = "other" },
		"wrong-model":       func(s *v1alpha1.AgentSession) { s.Spec.Model = "gpt-6-astra" },
		"wrong-effort":      func(s *v1alpha1.AgentSession) { s.Spec.Effort = "max" },
		"wrong-provider":    func(s *v1alpha1.AgentSession) { s.Spec.Agent = v1alpha1.AgentClaude; s.Spec.Model = "claude-opus-5-5" },
		"wrong-size":        func(s *v1alpha1.AgentSession) { s.Spec.Size = v1alpha1.SizeM },
		"wrong-profile":     func(s *v1alpha1.AgentSession) { s.Spec.Profile = "full" },
		"workspace":         func(s *v1alpha1.AgentSession) { s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "shared"} },
		"restore":           func(s *v1alpha1.AgentSession) { s.Spec.Restore = "original/20261016-1000" },
		"tools":             func(s *v1alpha1.AgentSession) { s.Spec.Tools = []string{"extra"} },
		"longer-task":       func(s *v1alpha1.AgentSession) { s.Spec.Limits.Timeout = dur(16 * time.Minute) },
		"shorter-task":      func(s *v1alpha1.AgentSession) { s.Spec.Limits.Timeout = dur(time.Minute) },
		"turn-override":     func(s *v1alpha1.AgentSession) { s.Spec.Limits.MaxTurns = 1 },
		"lifecycle":         func(s *v1alpha1.AgentSession) { s.Spec.Lifecycle = &v1alpha1.Lifecycle{} },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s := f.Session(t, 0)
			s.Namespace = ns
			s.UID = ""
			s.Generation = 0
			change(s)
			if err := k8s.Create(ctx(t), s, client.DryRunAll); !apierrors.IsInvalid(err) {
				t.Fatal("boundary not refused by schema", err)
			}
		})
	}
}
