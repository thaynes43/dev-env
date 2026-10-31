package main

import (
	"reflect"
	"testing"
)

// TestGrantDelegatesFlag: --grant-delegates (DESIGN-042 6.2) is empty by
// default, parses like the broker's (sorted), and never names an identity the
// API already serves in another class.
func TestGrantDelegatesFlag(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "dev-env-system")
	for _, tc := range []struct {
		name string
		args []string
		want []string
		err  bool
	}{
		{name: "default"},
		{name: "empty", args: []string{"--grant-delegates="}},
		{name: "two, sorted", args: []string{"--grant-delegates=dev-env-system/codex-host-a,dev-agents/dev-env-coordinator-lead"},
			want: []string{"dev-agents/dev-env-coordinator-lead", "dev-env-system/codex-host-a"}},
		{name: "human", args: []string{"--grant-delegates=dev-env-system/dev-env-human"}, err: true},
		{name: "client", args: []string{"--grant-delegates=dev/dev-env", "--client-service-accounts=dev/dev-env"}, err: true},
		{name: "shared session", args: []string{"--grant-delegates=dev-agents/dev-env-agent"}, err: true},
		{name: "workbench", args: []string{"--grant-delegates=dev-agents/dev-env-workbench"}, err: true},
		{name: "portal", args: []string{"--grant-delegates=dev-agents/dev-env-v2-portal"}, err: true},
		{name: "repeated", args: []string{"--grant-delegates=a/b,a/b"}, err: true},
		{name: "malformed", args: []string{"--grant-delegates=nobody"}, err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := parseFlags(tc.args)
			if (err != nil) != tc.err {
				t.Fatalf("parseFlags(%q) error = %v, want error=%t", tc.args, err, tc.err)
			}
			if err == nil && !reflect.DeepEqual(o.grantDelegates, tc.want) {
				t.Fatalf("grantDelegates = %q, want %q", o.grantDelegates, tc.want)
			}
		})
	}
}

func TestDelegatePolicyNamespaceFlag(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "operator-system")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{want: "operator-system"},
		{args: []string{"--policy-namespace=grant-policies"}, want: "grant-policies"},
	} {
		o, err := parseFlags(tc.args)
		if err != nil || o.policyNamespace != tc.want {
			t.Fatalf("policy namespace=%q error=%v, want %q", o.policyNamespace, err, tc.want)
		}
	}
	if _, err := parseFlags([]string{"--policy-namespace="}); err == nil {
		t.Fatal("empty policy namespace accepted")
	}
}
