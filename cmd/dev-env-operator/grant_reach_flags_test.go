package main

import "testing"

const legacyFlagFixture = "dev-agents/dev-env-agent:11111111-1111-1111-1111-111111111111:legacy-claude:22222222-2222-2222-2222-222222222222:legacy-claude:33333333-3333-3333-3333-333333333333"

func TestPolicyReachLegacyFlags(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "dev-env-system")
	for _, tc := range []struct {
		name string
		args []string
		bad  bool
	}{
		{name: "default"},
		{name: "named", args: []string{"--grant-delegates=dev-agents/dev-env-coordinator-lead", "--grant-policy-delegates=dev-agents/dev-env-coordinator-lead"}},
		{name: "not subset", args: []string{"--grant-policy-delegates=dev-agents/dev-env-coordinator-lead"}, bad: true},
		{name: "legacy exact", args: []string{"--grant-legacy-delegates=" + legacyFlagFixture, "--grant-delegates=dev-agents/dev-env-agent", "--grant-policy-delegates=dev-agents/dev-env-agent"}},
		{name: "shared no exact actor", args: []string{"--grant-delegates=dev-agents/dev-env-agent", "--grant-policy-delegates=dev-agents/dev-env-agent"}, bad: true},
		{name: "legacy no subset", args: []string{"--grant-legacy-delegates=" + legacyFlagFixture, "--grant-delegates=dev-agents/dev-env-agent"}, bad: true},
		{name: "legacy no list", args: []string{"--grant-legacy-delegates=" + legacyFlagFixture}, bad: true},
		{name: "malformed", args: []string{"--grant-legacy-delegates=dev-agents/dev-env-agent:guess"}, bad: true},
		{name: "repeated", args: []string{"--grant-legacy-delegates=" + legacyFlagFixture + "," + legacyFlagFixture}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op, err := parseFlags(tc.args)
			if (err != nil) != tc.bad {
				t.Fatalf("operator: %+v %v", op, err)
			}
			br, err := parseBrokerFlags(tc.args)
			if (err != nil) != tc.bad {
				t.Fatalf("broker: %+v %v", br, err)
			}
			if tc.name == "default" && (len(op.grantPolicyDelegates) != 0 || len(op.legacyGrantDelegates) != 0 || len(br.grantPolicyDelegates) != 0 || len(br.legacyGrantDelegates) != 0) {
				t.Fatal("new paths enabled by default")
			}
			if tc.name == "legacy exact" && (len(op.legacyGrantDelegates) != 1 || len(br.legacyGrantDelegates) != 1) {
				t.Fatal("exact actor not retained")
			}
		})
	}
}
