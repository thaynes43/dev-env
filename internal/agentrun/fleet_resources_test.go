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
	contains(t, "unparseable", h.stdout.String(), "Unknown (unavailable)")
	if strings.Contains(h.stdout.String(), "not-a-quantity") || strings.Contains(h.stdout.String(), "cpu 0") {
		t.Fatalf("raw quantity printed: %s", h.stdout.String())
	}
	h.mustRun(ExitOK, "fleet", "--resources", "-o", "json")
	if strings.Contains(h.stdout.String(), "not-a-quantity") || strings.Contains(h.stdout.String(), `"value"`) {
		t.Fatalf("json kept the bad quantity: %s", h.stdout.String())
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

func TestCanonicalFleetQuantity(t *testing.T) {
	for _, value := range []string{"0", "100m", "1", "1Gi", "64Mi", "256Mi", "1500m", "1536Mi", "1e3", "1e-3", "2Ki", "1n", "1u", "1k", "1M", "-100m"} {
		if !canonicalFleetQuantity(value) {
			t.Errorf("%s should be canonical", value)
		}
	}
	for _, value := range []string{"", "not-a-quantity", "1.5", "1.0", "01", "0m", "-0", "1e+3", "1E3", "1e03", "100 m", "1Gi\n", strings.Repeat("1", 33)} {
		if canonicalFleetQuantity(value) {
			t.Errorf("%s should be unknown", value)
		}
	}
}

func TestFleetResourcesMalformedMetadata(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		absent := apiv1.FleetQuantity{State: apiv1.FleetQuantityAbsent}
		writeTestJSON(w, http.StatusOK, apiv1.Fleet{
			Revision: "rev",
			Phases:   map[string]int{"Running": 5},
			Sessions: []apiv1.FleetSession{
				{Name: "badqty", Phase: "Running", CreatedAt: testNow, Resources: &apiv1.FleetResources{
					State: apiv1.FleetResourcesObserved, PodUID: "pod-1",
					Node: &apiv1.FleetResourceNode{State: apiv1.FleetNodeSet, Name: "n"},
					Containers: []apiv1.FleetContainerResources{{Kind: apiv1.FleetContainerOrdinary, Name: "agent",
						CPURequest: apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "not-a-quantity"}, CPULimit: absent, MemoryRequest: absent, MemoryLimit: absent}},
				}},
				{Name: "nouid", Phase: "Running", CreatedAt: testNow, Resources: &apiv1.FleetResources{
					State: apiv1.FleetResourcesObserved, PodUID: "",
					Node: &apiv1.FleetResourceNode{State: apiv1.FleetNodeSet, Name: "n"},
					Containers: []apiv1.FleetContainerResources{{Kind: apiv1.FleetContainerOrdinary, Name: "agent",
						CPURequest: apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "0"}, CPULimit: absent, MemoryRequest: absent, MemoryLimit: absent}},
				}},
				{Name: "badreason", Phase: "Running", CreatedAt: testNow, Resources: &apiv1.FleetResources{State: apiv1.FleetResourcesUnknown, Reason: "made-up\nreason"}},
				{Name: "badkind", Phase: "Running", CreatedAt: testNow, Resources: &apiv1.FleetResources{
					State: apiv1.FleetResourcesObserved, PodUID: "pod-2",
					Node: &apiv1.FleetResourceNode{State: apiv1.FleetNodeSet, Name: "node\tname"},
					Containers: []apiv1.FleetContainerResources{{Kind: "sidecar", Name: "agent",
						CPURequest: absent, CPULimit: absent, MemoryRequest: absent, MemoryLimit: absent}},
				}},
				{Name: "ok", Phase: "Running", Node: "status-node", CreatedAt: testNow, Resources: &apiv1.FleetResources{
					State: apiv1.FleetResourcesObserved, ObservedAt: &testNow, PodUID: "pod-ok",
					Node: &apiv1.FleetResourceNode{State: apiv1.FleetNodeSet, Name: "spec-node"},
					Containers: []apiv1.FleetContainerResources{{Kind: apiv1.FleetContainerOrdinary, Name: "agent",
						CPURequest: apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "100m"}, CPULimit: apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "0"},
						MemoryRequest: apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "1Gi"}, MemoryLimit: apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "1e3"}}},
					InitContainers: []apiv1.FleetContainerResources{{Kind: apiv1.FleetContainerInit, Name: "bootstrap",
						CPURequest: absent, CPULimit: absent, MemoryRequest: apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "64Mi"}, MemoryLimit: absent}},
				}},
			},
		})
	}
	h.mustRun(ExitOK, "fleet", "--resources")
	out := h.stdout.String()
	for _, leaked := range []string{"not-a-quantity", "made-up", "sidecar", "node\tname", "\nreason"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("text leaked %q\n%s", leaked, out)
		}
	}
	contains(t, "malformed text", out, "Unknown (unavailable)", "agent cpu 100m/0 memory 1Gi/1e3", "init/bootstrap cpu absent/absent memory 64Mi/absent")
	h.mustRun(ExitOK, "fleet", "--resources", "-o", "json")
	raw := h.stdout.String()
	for _, leaked := range []string{"not-a-quantity", "made-up", "sidecar", "node\tname"} {
		if strings.Contains(raw, leaked) {
			t.Fatalf("json leaked %q\n%s", leaked, raw)
		}
	}
	var decoded apiv1.Fleet
	if err := json.Unmarshal(h.stdout.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	byName := map[string]apiv1.FleetResources{}
	for _, session := range decoded.Sessions {
		if session.Resources == nil {
			t.Fatalf("%s resources missing", session.Name)
		}
		byName[session.Name] = *session.Resources
	}
	for _, name := range []string{"badqty", "nouid", "badreason", "badkind"} {
		if byName[name].State != apiv1.FleetResourcesUnknown || byName[name].Reason != apiv1.FleetResourcesUnavailable || byName[name].PodUID != "" || len(byName[name].Containers) != 0 {
			t.Fatalf("%s stayed partial: %+v", name, byName[name])
		}
	}
	ok := byName["ok"]
	if ok.State != apiv1.FleetResourcesObserved || ok.PodUID != "pod-ok" || ok.Containers[0].CPULimit.Value != "0" || ok.Containers[0].MemoryLimit.Value != "1e3" || ok.InitContainers[0].MemoryRequest.Value != "64Mi" {
		t.Fatalf("valid observation changed: %+v", ok)
	}
}

func TestFleetResourcesObservedStampAndReason(t *testing.T) {
	h := newHarness(t)
	zero := time.Time{}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		absent := apiv1.FleetQuantity{State: apiv1.FleetQuantityAbsent}
		valid := apiv1.FleetContainerResources{Kind: apiv1.FleetContainerOrdinary, Name: "agent",
			CPURequest: apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "100m"}, CPULimit: apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "0"},
			MemoryRequest: absent, MemoryLimit: absent}
		writeTestJSON(w, http.StatusOK, apiv1.Fleet{
			Revision: "rev", Phases: map[string]int{"Running": 4},
			Sessions: []apiv1.FleetSession{
				{Name: "reason", Phase: "Running", CreatedAt: testNow, Resources: &apiv1.FleetResources{
					State: apiv1.FleetResourcesObserved, Reason: "leaked-reason-text", ObservedAt: &testNow, PodUID: "pod-r",
					Node: &apiv1.FleetResourceNode{State: apiv1.FleetNodeSet, Name: "n"}, Containers: []apiv1.FleetContainerResources{valid}}},
				{Name: "nostamp", Phase: "Running", CreatedAt: testNow, Resources: &apiv1.FleetResources{
					State: apiv1.FleetResourcesObserved, PodUID: "pod-n",
					Node: &apiv1.FleetResourceNode{State: apiv1.FleetNodeSet, Name: "n"}, Containers: []apiv1.FleetContainerResources{valid}}},
				{Name: "zerostamp", Phase: "Running", CreatedAt: testNow, Resources: &apiv1.FleetResources{
					State: apiv1.FleetResourcesObserved, ObservedAt: &zero, PodUID: "pod-z",
					Node: &apiv1.FleetResourceNode{State: apiv1.FleetNodeSet, Name: "n"}, Containers: []apiv1.FleetContainerResources{valid}}},
				{Name: "ok", Phase: "Running", CreatedAt: testNow, Resources: &apiv1.FleetResources{
					State: apiv1.FleetResourcesObserved, ObservedAt: &testNow, PodUID: "pod-ok",
					Node: &apiv1.FleetResourceNode{State: apiv1.FleetNodeSet, Name: "spec-node"}, Containers: []apiv1.FleetContainerResources{valid}}},
			},
		})
	}
	h.mustRun(ExitOK, "fleet", "--resources")
	out := h.stdout.String()
	if strings.Contains(out, "leaked-reason-text") || strings.Contains(out, "pod-r") || strings.Contains(out, "pod-n") || strings.Contains(out, "pod-z") {
		t.Fatalf("text kept a malformed observation\n%s", out)
	}
	contains(t, "stamp", out, "Unknown (unavailable)", "pod-ok", "agent cpu 100m/0 memory absent/absent")
	h.mustRun(ExitOK, "fleet", "--resources", "-o", "json")
	raw := h.stdout.String()
	if strings.Contains(raw, "leaked-reason-text") {
		t.Fatalf("json leaked the reason\n%s", raw)
	}
	var decoded apiv1.Fleet
	if err := json.Unmarshal(h.stdout.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, session := range decoded.Sessions {
		if session.Name == "ok" {
			if session.Resources == nil || session.Resources.State != apiv1.FleetResourcesObserved || session.Resources.ObservedAt == nil || session.Resources.ObservedAt.IsZero() || session.Resources.Reason != "" {
				t.Fatalf("valid observation: %+v", session.Resources)
			}
			continue
		}
		if session.Resources == nil || session.Resources.State != apiv1.FleetResourcesUnknown || session.Resources.Reason != apiv1.FleetResourcesUnavailable || session.Resources.PodUID != "" {
			t.Fatalf("%s: %+v", session.Name, session.Resources)
		}
	}
}
