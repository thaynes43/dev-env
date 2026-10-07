package main

import (
	"slices"
	"testing"
)

func TestParseFlagsAPI(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "dev-env-system")
	o, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.apiAddr != ":8443" || o.humanSA != "dev-env-system/dev-env-human" || !slices.Equal(o.clientSAs, []string{"dev-agents/dev-env-workbench"}) {
		t.Errorf("defaults %+v", o)
	}

	o, err = parseFlags([]string{"--client-service-accounts=dev-agents/dev-env-workbench,dev/dev-env"})
	if err != nil || !slices.Equal(o.clientSAs, []string{"dev-agents/dev-env-workbench", "dev/dev-env"}) {
		t.Errorf("the v1 pod as a client: %+v %v", o.clientSAs, err)
	}

	for _, bad := range [][]string{
		{"--client-service-accounts=dev-env"},
		{"--human-service-account=dev-env-human"},
		{"--human-service-account="},
		{"--api-tls-dir="},
	} {
		if _, err := parseFlags(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if _, err := parseFlags([]string{"--api-bind-address=0", "--api-tls-dir=", "--human-service-account="}); err != nil {
		t.Errorf("with the API off its flags are not checked: %v", err)
	}
}
