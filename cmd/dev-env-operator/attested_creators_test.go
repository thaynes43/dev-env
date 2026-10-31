package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestAttestedMaintenanceCreatorsFlag: --attested-maintenance-creators
// (DESIGN-042 S6, ADR-014). Unset, it is the human ServiceAccount the broker
// already knows, and it follows --maintenance-human-service-account. Set empty,
// it names no creator. A list is sorted and bounded, and every entry must be
// <namespace>/<serviceaccount>.
func TestAttestedMaintenanceCreatorsFlag(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "dev-env-system")
	many := make([]string, 17)
	for i := range many {
		many[i] = fmt.Sprintf("dev-env-system/creator-%02d", i)
	}
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{name: "unset is the human ServiceAccount", want: []string{"dev-env-system/dev-env-human"}},
		{name: "unset follows the human flag", args: []string{"--maintenance-human-service-account=ops/tom"}, want: []string{"ops/tom"}},
		{name: "set empty names no creator", args: []string{"--attested-maintenance-creators="}},
		{name: "set blank names no creator", args: []string{"--attested-maintenance-creators= "}},
		{name: "set empty beats the human flag", args: []string{"--maintenance-human-service-account=ops/tom", "--attested-maintenance-creators="}},
		{name: "an explicit list replaces the default", args: []string{"--attested-maintenance-creators=ops/tom"}, want: []string{"ops/tom"}},
		{name: "sorted", args: []string{"--attested-maintenance-creators=dev-env-system/dev-env-human, dev-agents/dev-env-workbench"}, want: []string{"dev-agents/dev-env-workbench", "dev-env-system/dev-env-human"}},
		{name: "sixteen", args: []string{"--attested-maintenance-creators=" + strings.Join(many[:16], ",")}, want: many[:16]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := parseBrokerFlags(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(o.attestedCreators, tc.want) {
				t.Fatalf("creators %q, want %q", o.attestedCreators, tc.want)
			}
		})
	}
	for _, bad := range []string{
		"no-slash", "dev-agents/", "/dev-env-human", "a/b/c", "Dev/x", "dev/X", "a/b,", ",a/b", "a/b,,c/d",
		"a/b,a/b", "haynes-ops-1029-223503", strings.Join(many, ","),
	} {
		if o, err := parseBrokerFlags([]string{"--attested-maintenance-creators=" + bad}); err == nil {
			t.Errorf("%q accepted: %q", bad, o.attestedCreators)
		}
	}
	// Unset, a human ServiceAccount that is not one namespace/name gives no
	// creator, and the broker starts as it did before this flag existed.
	for _, human := range []string{"no-slash", "a/b,c/d", ""} {
		o, err := parseBrokerFlags([]string{"--maintenance-human-service-account=" + human})
		if err != nil || o.attestedCreators != nil {
			t.Errorf("human %q: creators %q, err %v; want none and no error", human, o.attestedCreators, err)
		}
	}
}
