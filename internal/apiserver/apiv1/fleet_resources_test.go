package apiv1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFleetResourcesLegacyJSONOmitsTheField(t *testing.T) {
	legacy := []byte(`{"phases":{"Running":1},"sessions":[{"name":"a","repo":"r","mode":"task","phase":"Running","createdAt":"2026-10-06T17:22:26Z"}],"nodes":[],"outdated":[]}`)
	var fleet Fleet
	if err := json.Unmarshal(legacy, &fleet); err != nil {
		t.Fatal(err)
	}
	if len(fleet.Sessions) != 1 || fleet.Sessions[0].Resources != nil {
		t.Fatalf("legacy session decoded resources: %+v", fleet.Sessions)
	}
	raw, err := json.Marshal(fleet)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "resources") {
		t.Fatalf("legacy marshal added resources: %s", raw)
	}
}

func TestFleetQuantityZeroIsDistinctFromAbsent(t *testing.T) {
	zero := FleetQuantity{State: FleetQuantitySet, Value: "0"}
	absent := FleetQuantity{State: FleetQuantityAbsent}
	rawZero, err := json.Marshal(zero)
	if err != nil {
		t.Fatal(err)
	}
	rawAbsent, err := json.Marshal(absent)
	if err != nil {
		t.Fatal(err)
	}
	if string(rawZero) == string(rawAbsent) || !strings.Contains(string(rawZero), `"value":"0"`) || strings.Contains(string(rawAbsent), "value") {
		t.Fatalf("zero %s absent %s", rawZero, rawAbsent)
	}
	at := time.Date(2026, 10, 22, 23, 0, 0, 0, time.UTC)
	obs := FleetResources{
		State: FleetResourcesObserved, ObservedAt: &at, PodUID: "pod-1",
		Node:       &FleetResourceNode{State: FleetNodeUnscheduled},
		Containers: []FleetContainerResources{{Kind: FleetContainerOrdinary, Name: "agent", CPURequest: zero, CPULimit: absent, MemoryRequest: absent, MemoryLimit: absent}},
	}
	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"secret", "http://", "capacity", "millicpu"} {
		if strings.Contains(string(raw), hidden) {
			t.Fatalf("observation carries %s: %s", hidden, raw)
		}
	}
}
