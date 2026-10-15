package main

import (
	"encoding/json"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestCoordArchiveCLIListDispatchesBoundedPage(t *testing.T) {
	env, _ := coordCLIRig(t)
	m, _, packet := coordCLIData(t)
	if code, out, _ := runCoordCLI([]string{"ctl", "coord-archive", "put"}, packet, env); code != exitOK {
		t.Fatalf("put: %d %s", code, out)
	}
	code, out, stderr := runCoordCLI([]string{"ctl", "coord-archive", "list", "--resource", m.Resource, "--project", m.Project, "--limit", "1"}, nil, env)
	var page protocol.CoordArchiveListResult
	if code != exitOK || stderr != "" || json.Unmarshal(out, &page) != nil || len(page.Items) != 1 || page.Items[0].UID != m.UID {
		t.Fatalf("list: exit=%d stderr=%q output=%s", code, stderr, out)
	}
}
