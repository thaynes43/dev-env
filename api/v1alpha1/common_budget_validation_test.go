package v1alpha1_test

import (
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// Structural admissibility only. The API/controller independently verify the
// retained source bytes, budget binding, HOME and one-shot executor intent.
func TestCommonRulesAllowOnlyFiniteOrdinaryPrivateTaskForm(t *testing.T) {
	for _, scenario := range []string{"ready", "no-parent", "no-timeout", "too-long", "zero-timeout", "shared", "interactive", "wrong-profile"} {
		t.Run(scenario, func(t *testing.T) {
			s := taskSession()
			s.Spec.Model = "claude-haiku-4-5"
			s.Spec.Profile = "dev"
			s.Spec.Parent = "fixture/parent"
			s.Spec.Limits = &v1alpha1.SessionLimits{Timeout: dur(time.Minute)}
			digest := strings.Repeat("a", 64)
			s.Spec.CommonRules = &v1alpha1.CommonRuleBinding{Version: 1, CommonRevision: digest, ProjectDigest: digest, SnapshotDigest: digest, Snapshot: "{}"}
			switch scenario {
			case "no-parent":
				s.Spec.Parent = ""
			case "no-timeout":
				s.Spec.Limits = nil
			case "too-long":
				s.Spec.Limits.Timeout = dur(46 * time.Minute)
			case "zero-timeout":
				s.Spec.Limits.Timeout = dur(0)
			case "shared":
				s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"}
			case "interactive":
				s.Spec.Mode = v1alpha1.ModeLocal
				s.Spec.Prompt = ""
				s.Spec.Limits = nil
			case "wrong-profile":
				s.Spec.Profile = "ops"
			}
			err := k8s.Create(ctx(t), s)
			if scenario == "ready" {
				if err != nil {
					t.Fatal("finite private binding rejected", err)
				}
				s.Spec.CommonRules.Snapshot = "replacement"
				wantInvalid(t, k8s.Update(ctx(t), s), "immutable")
			} else {
				wantInvalid(t, err, "common rules require")
			}
		})
	}
}
