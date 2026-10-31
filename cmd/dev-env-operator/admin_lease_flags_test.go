package main

import "testing"

func TestAdminLeaseFlagsDefaultOffAndKeepSessionNamespace(t *testing.T) {
	o, err := parseBrokerFlags(nil)
	if err != nil || o.enableAdminLeases || o.adminIssuerNamespace != "" {
		t.Fatal("admin issuance did not default off/unconfigured", err)
	}
	for _, args := range [][]string{{"--enable-admin-leases"}, {"--enable-admin-leases", "--admin-issuer-namespace=dev-agents"}, {"--enable-admin-leases", "--admin-issuer-namespace=arbitrary"}, {"--enable-admin-leases", "--admin-issuer-namespace=dev-env-admin-issuance", "--session-namespace=dev-env-admin-issuance"}} {
		// Invalid issuer settings close issuance in the broker, not its cleanup loop.
		if parsed, err := parseBrokerFlags(args); err != nil || !parsed.enableAdminLeases {
			t.Fatal("issuer reconfiguration prevented cleanup startup", args, err)
		}
	}
	o, err = parseBrokerFlags([]string{"--enable-admin-leases", "--admin-issuer-namespace=dev-env-admin-issuance"})
	if err != nil || !o.enableAdminLeases || o.sessionNamespace != "dev-agents" {
		t.Fatal("issuer changed session placement", err)
	}
}
