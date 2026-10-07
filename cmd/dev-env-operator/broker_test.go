package main

import "testing"

func TestParseBrokerFlags(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "dev-env-system")
	o, err := parseBrokerFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := brokerOptions{sessionNamespace: "dev-agents", policyNamespace: "dev-env-system", leaderElect: true, metricsAddr: ":8080", probeAddr: ":8081"}
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
