package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestProjectBootstrapJSONRoundTrip(t *testing.T) {
	in := Status{Session: "s", Boot: BootReady, ProjectBootstrap: &ProjectBootstrap{
		State: BootstrapPartial, CatalogRevision: strings.Repeat("a", 64),
		Repositories: []ProjectBootstrapRepository{
			{Name: "demo", State: BootstrapReady},
			{Name: "extra", State: BootstrapPreserved, Step: StepAdvance, Reason: ReasonUnsupportedTrackedEntry},
		},
		Projects: []ProjectBootstrapProject{{Name: "sample", State: BootstrapFailed, Reason: ReasonAnchorUnproven, Repository: "extra", Cause: ReasonUnsupportedTrackedEntry}},
	}}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"projectBootstrap":{"state":"Partial"`, `"catalogRevision":`, `"step":"Advance"`, `"reason":"UnsupportedTrackedEntry"`, `"repository":"extra"`, `"cause":"UnsupportedTrackedEntry"`} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("%s missing from %s", key, data)
		}
	}
	// A Ready item has no step or reason on the wire.
	if strings.Contains(string(data), `{"name":"demo","state":"Ready","step"`) {
		t.Fatalf("empty fields are not omitted: %s", data)
	}
	var out Status
	if err := json.Unmarshal(data, &out); err != nil || !reflect.DeepEqual(in.ProjectBootstrap, out.ProjectBootstrap) {
		t.Fatalf("round trip: %v\n%+v\n%+v", err, in.ProjectBootstrap, out.ProjectBootstrap)
	}
	// An agentd without the field, or one that did not run a bootstrap,
	// sends none.
	data, _ = json.Marshal(Status{Session: "s"})
	if strings.Contains(string(data), "projectBootstrap") {
		t.Fatalf("absent bootstrap serialized: %s", data)
	}
}

func TestProjectBootstrapReasonSetsAreDistinctCodes(t *testing.T) {
	for name, set := range map[string][]string{"states": BootstrapStates, "repository states": BootstrapRepositoryStates, "project states": BootstrapProjectStates, "steps": BootstrapSteps, "repository": RepositoryReasons, "root": RootReasons, "catalog": CatalogReasons} {
		seen := map[string]bool{}
		for _, code := range set {
			if seen[code] || code == "" || strings.ContainsAny(code, " /=;@()") {
				t.Fatalf("%s set has a duplicate or unsafe code %q", name, code)
			}
			seen[code] = true
		}
	}
	if !InSet(RepositoryReasons, ReasonUncertain) || InSet(RepositoryReasons, "unknown") {
		t.Fatal("InSet")
	}
}
