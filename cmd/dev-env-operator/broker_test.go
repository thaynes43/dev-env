package main

import (
	"reflect"
	"testing"
)

func TestParseBrokerFlags(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "dev-env-system")
	o, err := parseBrokerFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := brokerOptions{sessionNamespace: "dev-agents", policyNamespace: "dev-env-system", maintenanceHuman: "dev-env-system/dev-env-human", leaderElect: true, metricsAddr: ":8080", probeAddr: ":8081", attestedCreators: []string{"dev-env-system/dev-env-human"}}
	if !reflect.DeepEqual(o, want) {
		t.Errorf("defaults %+v, want %+v", o, want)
	}
	if o, err := parseBrokerFlags([]string{"--enable-proxmox-grants"}); err != nil || !o.enableProxmoxGrants {
		t.Fatalf("explicit Proxmox enable: %+v %v", o, err)
	}
	if o, err := parseBrokerFlags([]string{"--enable-hw-ssh-grants"}); err != nil || !o.enableHWSSHGrants || o.enableProxmoxGrants {
		t.Fatalf("explicit hw-ssh enable must not enable proxmox: %+v %v", o, err)
	}
	// ADR-013: its own flag, default off, which turns on neither prerequisite.
	if o, err := parseBrokerFlags([]string{"--enable-attested-maintenance"}); err != nil || !o.enableAttestedMaint || o.enableAttested || o.enableMaintenance {
		t.Fatalf("explicit attested maintenance enable: %+v %v", o, err)
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
