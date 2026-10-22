package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestFleetResourcesCLI(t *testing.T) {
	h := newHarness(t)
	at := testNow
	var headers []string
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		headers = append(headers, r.Header.Get(apiv1.FleetHeader))
		writeTestJSON(w, http.StatusOK, apiv1.Fleet{
			Revision: "rev",
			Phases:   map[string]int{"Running": 1, "Pending": 1},
			Sessions: []apiv1.FleetSession{
				{Name: "pend", Phase: "Pending", Pending: "0/7 nodes are available: 3 Insufficient memory", CreatedAt: testNow.Add(-time.Minute),
					Resources: &apiv1.FleetResources{State: apiv1.FleetResourcesUnknown, Reason: apiv1.FleetResourcesMissing}},
				{Name: "run", Phase: "Running", Node: "status-node", CreatedAt: testNow.Add(-time.Hour),
					Resources: &apiv1.FleetResources{
						State: apiv1.FleetResourcesObserved, ObservedAt: &at, PodUID: "pod-run",
						Node: &apiv1.FleetResourceNode{State: apiv1.FleetNodeSet, Name: "spec-node"},
						Containers: []apiv1.FleetContainerResources{{
							Kind: apiv1.FleetContainerOrdinary, Name: "agent",
							CPURequest:    apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "100m"},
							CPULimit:      apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "0"},
							MemoryRequest: apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "1Gi"},
							MemoryLimit:   apiv1.FleetQuantity{State: apiv1.FleetQuantityAbsent},
						}},
						InitContainers: []apiv1.FleetContainerResources{{
							Kind: apiv1.FleetContainerInit, Name: "bootstrap",
							CPURequest: apiv1.FleetQuantity{State: apiv1.FleetQuantityAbsent}, CPULimit: apiv1.FleetQuantity{State: apiv1.FleetQuantityAbsent},
							MemoryRequest: apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "64Mi"}, MemoryLimit: apiv1.FleetQuantity{State: apiv1.FleetQuantityAbsent},
						}},
					}},
			},
		})
	}
	h.mustRun(ExitOK, "fleet", "--resources")
	contains(t, "resources", h.stdout.String(),
		"POD", "SPEC NODE",
		"pend", "Unknown (missing)",
		"run", "status-node", "pod-run", "spec-node",
		"agent cpu 100m/0 memory 1Gi/absent",
		"init/bootstrap cpu absent/absent memory 64Mi/absent")
	if len(h.api.requests()) != 1 || headers[0] != apiv1.FleetResourcesV1 {
		t.Fatalf("requests=%d headers=%v", len(h.api.requests()), headers)
	}

	h.mustRun(ExitOK, "fleet", "--resources", "-o", "json")
	var decoded apiv1.Fleet
	if err := json.Unmarshal(h.stdout.Bytes(), &decoded); err != nil || decoded.Sessions[1].Resources.PodUID != "pod-run" {
		t.Fatalf("json: %v %s", err, h.stdout.String())
	}
}

func TestFleetResourcesOldServerIsUnknown(t *testing.T) {
	h := newHarness(t)
	var headers []string
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		headers = append(headers, r.Header.Get(apiv1.FleetHeader))
		writeTestJSON(w, http.StatusOK, apiv1.Fleet{
			Revision: "rev", Phases: map[string]int{"Running": 1},
			Sessions: []apiv1.FleetSession{{Name: "run", Phase: "Running", Node: "status-node", CreatedAt: testNow}},
		})
	}
	h.mustRun(ExitOK, "fleet", "--resources")
	contains(t, "old server", h.stdout.String(), "run", "Unknown (unavailable)")
	if strings.Contains(h.stdout.String(), "cpu 0") || len(h.api.requests()) != 1 || headers[0] != apiv1.FleetResourcesV1 {
		t.Fatalf("replay or zero capacity: requests=%d headers=%v\n%s", len(h.api.requests()), headers, h.stdout.String())
	}
	h.mustRun(ExitOK, "fleet", "-o", "json", "--resources")
	if !strings.Contains(h.stdout.String(), `"state": "Unknown"`) || strings.Contains(h.stdout.String(), `"value": "0"`) {
		t.Fatalf("json unknown: %s", h.stdout.String())
	}
}

func TestFleetResourcesUnparseableQuantity(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeTestJSON(w, http.StatusOK, apiv1.Fleet{Phases: map[string]int{"Running": 1}, Sessions: []apiv1.FleetSession{{
			Name: "run", Phase: "Running", CreatedAt: testNow,
			Resources: &apiv1.FleetResources{State: apiv1.FleetResourcesObserved, PodUID: "pod", Node: &apiv1.FleetResourceNode{State: apiv1.FleetNodeSet, Name: "n"},
				Containers: []apiv1.FleetContainerResources{{Kind: apiv1.FleetContainerOrdinary, Name: "agent",
					CPURequest: apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "not-a-quantity"}, CPULimit: apiv1.FleetQuantity{State: apiv1.FleetQuantityAbsent},
					MemoryRequest: apiv1.FleetQuantity{State: apiv1.FleetQuantityAbsent}, MemoryLimit: apiv1.FleetQuantity{State: apiv1.FleetQuantityAbsent}}}},
		}}})
	}
	h.mustRun(ExitOK, "fleet", "--resources")
	contains(t, "unparseable", h.stdout.String(), "agent cpu Unknown/absent memory absent/absent")
	if strings.Contains(h.stdout.String(), "not-a-quantity") {
		t.Fatalf("raw quantity printed: %s", h.stdout.String())
	}
}

func TestFleetResourcesTransportErrorIsNotZero(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Header.Get(apiv1.FleetHeader) != apiv1.FleetResourcesV1 {
			t.Errorf("transport retry dropped the resources header")
		}
		apiError(w, http.StatusInternalServerError, apiv1.CodeInternal, "list pods failed")
	}
	h.mustRun(ExitFailed, "fleet", "--resources")
	if strings.Contains(h.stdout.String(), "cpu 0") || strings.Contains(h.stdout.String(), "capacity") {
		t.Fatalf("transport error printed capacity: %s", h.stdout.String())
	}
	if n := len(h.api.requests()); n != 1 {
		t.Fatalf("sent %d requests", n)
	}
}

func TestFleetWithoutResourcesKeepsItsHeaderOff(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Header.Get(apiv1.FleetHeader) != "" {
			t.Errorf("header %q", r.Header.Get(apiv1.FleetHeader))
		}
		writeTestJSON(w, http.StatusOK, apiv1.Fleet{Revision: "rev", Phases: map[string]int{}})
	}
	h.mustRun(ExitOK, "fleet")
	if strings.Contains(h.stdout.String(), "RESOURCES") || strings.Contains(h.stdout.String(), "resources") {
		t.Fatalf("plain fleet printed resources: %s", h.stdout.String())
	}
}
