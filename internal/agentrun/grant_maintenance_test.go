package agentrun

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestMaintenanceCLI(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantsPath: func(*http.Request) (int, string) {
			return http.StatusCreated, `{"name":"grant-fixture","type":"maintenance","phase":"Pending"}`
		},
		apiv1.GrantPath("grant-fixture") + "/maintenance-decision": func(*http.Request) (int, string) {
			return http.StatusAccepted, `{"name":"grant-fixture","phase":"Pending"}`
		},
		apiv1.GrantPath("grant-fixture") + "/maintenance-exec": func(*http.Request) (int, string) { return http.StatusOK, `{"stdout":"synthetic","stderr":""}` },
	})
	h.mustRun(ExitOK, "grant", "maintenance-request", "--task-json", `{"node":"synthetic-node","nodeUID":"node-uid","resource":"device","operation":"maintain","image":"digest-reviewed","cpuMillis":100,"memoryMiB":128,"coordinator":{"name":"ignored","uid":"ignored","podUID":"ignored"}}`, "--reason", "synthetic maintenance")
	var req apiv1.CreateGrantRequest
	if err := json.Unmarshal(h.api.requests()[0].body, &req); err != nil {
		t.Fatal(err)
	}
	if req.Type != "maintenance" || req.Maintenance == nil || req.TTL != "" {
		t.Fatal(req)
	}
	h.mustRun(ExitOK, "grant", "maintenance-decide", "--grant-uid", "fixture-uid", "--digest", "scope-digest", "--approve", "grant-fixture")
	var decision apiv1.MaintenanceDecisionRequest
	if err := json.Unmarshal(h.api.requests()[1].body, &decision); err != nil {
		t.Fatal(err)
	}
	if !decision.Approve || decision.GrantUID != "fixture-uid" || decision.Digest != "scope-digest" {
		t.Fatal(decision)
	}
	h.mustRun(ExitOK, "grant", "maintenance-exec", "grant-fixture", "--", "/bin/sh", "-c", "printf chosen")
	var command apiv1.MaintenanceCommand
	if err := json.Unmarshal(h.api.requests()[2].body, &command); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(command.Command, []string{"/bin/sh", "-c", "printf chosen"}) {
		t.Fatal(command)
	}
	h.mustRun(ExitUsage, "grant", "maintenance-decide", "grant-fixture")
	h.mustRun(ExitUsage, "grant", "maintenance-request", "--task-json", "{}", "--reason", "x", "--ttl", "2h")
}
