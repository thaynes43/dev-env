package main

import "testing"

func TestParseBrokerFlags(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "dev-env-system")
	o, err := parseBrokerFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := brokerOptions{sessionNamespace: "dev-agents", policyNamespace: "dev-env-system", leaderElect: true, metricsAddr: ":8080", probeAddr: ":8081", consoleAddr: "0"}
	if o != want {
		t.Errorf("defaults %+v, want %+v", o, want)
	}
	t.Setenv("POD_NAMESPACE", "")
	if o, err := parseBrokerFlags([]string{"--leader-elect=false", "--session-namespace=agents"}); err != nil || o.leaderElect || o.sessionNamespace != "agents" || o.policyNamespace != "dev-env-system" {
		t.Errorf("%+v %v", o, err)
	}
	for _, bad := range [][]string{{"--policy-namespace="}, {"--session-namespace="}, {"extra"}, {"--api-url=x"}} {
		if _, err := parseBrokerFlags(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestParseBrokerConsoleFlags(t *testing.T) {
	base := []string{"--console-bind-address=:8082", "--console-origin=https://approvals.example.com/", "--console-owner=owner", "--console-reauth-url=https://auth.example.com/if/flow/fresh/", "--pushover-secret-dir=/run/pushover"}
	o, err := parseBrokerFlags(base)
	if err != nil || o.consoleOrigin != "https://approvals.example.com" || o.consoleAddr != ":8082" {
		t.Fatalf("console flags: %+v, %v", o, err)
	}
	for i := range base {
		args := append([]string{}, base[:i]...)
		args = append(args, base[i+1:]...)
		if _, err := parseBrokerFlags(args); err == nil {
			t.Errorf("missing required flag %s was accepted", base[i])
		}
	}
	for _, extra := range []string{
		"--console-origin=http://approvals.example.com", "--console-origin=https://approvals.example.com/path", "--console-origin=https://approvals.example.com?",
		"--console-origin=https://user:pass@approvals.example.com", "--console-owner=", "--console-owner=policy/owner", "--console-reauth-url=//auth.example.com/fresh",
		"--console-auth-issuer=http://auth.example.com", "--console-auth-audience= audience ", "--console-bind-address=", "--console-bind-address=0",
	} {
		args := append(append([]string{}, base...), extra)
		if _, err := parseBrokerFlags(args); err == nil {
			t.Errorf("unsafe flag accepted: %s", extra)
		}
	}
}
