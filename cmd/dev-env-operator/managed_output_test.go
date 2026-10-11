package main

import (
	"encoding/json"
	"testing"

	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
)

func TestManagedOutputFlagsRequireTrustedRecordAndContextAPI(t *testing.T) {
	f := testutil.New(t, "claude", "dev/dev-env")
	f.Record.CaptureOutput = true
	raw, _ := json.Marshal([]projectcanary.Record{f.Record})
	args := []string{"--enable-private-project-canaries", "--private-project-canaries=" + string(raw), "--client-service-accounts=dev/dev-env", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=fixture", "--enable-private-history-retention", "--api-url=https://operator.example"}
	if _, err := parseFlags(args); err == nil {
		t.Fatal("capture admitted without context API")
	}
	if o, err := parseFlags(append(args, "--enable-published-contexts", "--context-payload-read-projects=sample")); err != nil || !o.privateProjectCanaries.OutputCaptureEnabled() {
		t.Fatal("trusted enabled capture refused", err)
	}
}
