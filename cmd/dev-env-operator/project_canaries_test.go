package main

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
)

func TestPrivateProjectCanaryFlagsDefaultOffAndConcretePrerequisites(t *testing.T) {
	f := testutil.New(t, "codex", "dev/dev-env")
	raw, _ := json.Marshal([]projectcanary.Record{f.Record})
	base := []string{"--enable-private-project-canaries", "--private-project-canaries=" + string(raw), "--client-service-accounts=dev/dev-env", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=fixture", "--enable-private-history-retention", "--enable-managed-codex-tasks", "--api-url=https://operator.example"}
	o, err := parseFlags(nil)
	if err != nil || o.privateProjectCanariesEnabled || o.privateProjectCanaries != nil || o.privateProjectTasks {
		t.Fatal("canaries enabled by default", err)
	}
	if o, err = parseFlags(base); err != nil || !o.privateProjectCanariesEnabled || o.privateProjectCanaries == nil || o.privateProjectTasks {
		t.Fatal("exact canary prerequisites refused", err)
	}
	for _, remove := range []string{"--private-project-canaries=" + string(raw), "--enable-private-history-retention", "--enable-managed-codex-tasks", "--project-clone-owner=fixture", "--api-url=https://operator.example"} {
		args := slices.DeleteFunc(slices.Clone(base), func(s string) bool { return s == remove })
		if _, err := parseFlags(args); err == nil {
			t.Fatal("missing canary prerequisite accepted", remove)
		}
	}
	for _, extra := range []string{"--enable-private-project-tasks", "--api-bind-address=0", "--client-service-accounts=other/client", "--api-url=http://operator.example", "--private-project-canaries=[]", "--private-project-canaries=[{}]"} {
		if _, err := parseFlags(append(slices.Clone(base), extra)); err == nil {
			t.Fatal("invalid canary configuration accepted", extra)
		}
	}
	if o, err := parseFlags([]string{"--private-project-canaries=" + string(raw)}); err != nil || o.privateProjectCanaries != nil {
		t.Fatal("unenabled records admitted canaries", err)
	}
	reserved := f.Record
	reserved.Client = "dev-agents/dev-env-agent"
	raw, _ = json.Marshal([]projectcanary.Record{reserved})
	if _, err := parseFlags(append(slices.Clone(base), "--client-service-accounts="+reserved.Client, "--private-project-canaries="+string(raw))); err == nil {
		t.Fatal("projected worker identity accepted as an external canary client")
	}
}
