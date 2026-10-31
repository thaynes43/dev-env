package broker

import (
	"os"
	"testing"

	"github.com/google/cel-go/cel"
	"sigs.k8s.io/yaml"
)

// Evaluate the actual native and unchanged Kyverno caller predicates. This is
// not a Kyverno webhook/engine test; the issuer's RBAC is tested independently.
func TestAdminIssuerBothGuardCallerMatches(t *testing.T) {
	for _, file := range []string{"identity-guard.yaml", "exec-guard-UNCHANGED.yaml"} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/proposed-haynes-ops/admin-lease/" + file)
			if err != nil {
				t.Fatal(err)
			}
			// The native file has a second binding document; only the first policy is
			// relevant. YAMLToJSON reads that first document.
			var policy struct {
				Spec struct {
					MatchConditions []struct {
						Expression string `json:"expression"`
					} `json:"matchConditions"`
				} `json:"spec"`
			}
			if err := yaml.Unmarshal(raw, &policy); err != nil {
				t.Fatal(err)
			}
			if len(policy.Spec.MatchConditions) != 1 {
				t.Fatal("unexpected caller match", len(policy.Spec.MatchConditions))
			}
			e, err := cel.NewEnv(cel.Variable("request", cel.DynType))
			if err != nil {
				t.Fatal(err)
			}
			ast, issues := e.Compile(policy.Spec.MatchConditions[0].Expression)
			if issues.Err() != nil {
				t.Fatal(issues.Err())
			}
			program, err := e.Program(ast)
			if err != nil {
				t.Fatal(err)
			}
			for username, want := range map[string]bool{
				"system:serviceaccount:dev-agents:dev-env-agent":                true,
				"system:serviceaccount:dev-agents:grant-ordinary":               true,
				"system:serviceaccount:dev-agents:dev-env-workbench":            false,
				"system:serviceaccount:dev-env-admin-issuance:grant-approved":   false,
				"system:serviceaccount:dev-env-admin-issuance:grant-unapproved": false,
			} {
				got, _, err := program.Eval(map[string]any{"request": map[string]any{"userInfo": map[string]any{"username": username}}})
				if err != nil || got.Value() != want {
					t.Fatal("incorrect actual guard match", username, got, err)
				}
			}
		})
	}
}
