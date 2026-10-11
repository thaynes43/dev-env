package main

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
)

func TestDirectoryFlagsDefaultOffAndIndependentScopes(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.directoryEnabled || len(o.directoryProjects) != 0 || len(o.metadataDiscoveryProjects) != 0 {
		t.Fatal("directory on by default", err)
	}
	f := testutil.New(t, "codex", "dev/dev-env")
	raw, _ := json.Marshal([]projectcanary.Record{f.Record})
	base := []string{"--enable-session-directory", "--session-directory-projects=sample", "--metadata-discovery-projects=sample", "--enable-private-project-canaries", "--private-project-canaries=" + string(raw), "--client-service-accounts=dev/dev-env", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=fixture", "--enable-private-history-retention", "--enable-managed-codex-tasks", "--api-url=https://operator.example"}
	o, err = parseFlags(base)
	if err != nil || !o.directoryEnabled || len(o.metadataDiscoveryProjects) != 1 || o.contextsEnabled || o.taskClaimsEnabled || o.inboxEnabled {
		t.Fatal("directory refused or granted other scopes", err)
	}
	for _, remove := range []string{"--enable-session-directory", "--session-directory-projects=sample", "--metadata-discovery-projects=sample", "--enable-private-project-canaries", "--enable-private-history-retention"} {
		args := slices.DeleteFunc(slices.Clone(base), func(s string) bool { return s == remove })
		if _, err := parseFlags(args); err == nil {
			t.Fatal("missing directory prerequisite accepted", remove)
		}
	}
	for _, extra := range []string{"--metadata-discovery-projects=other", "--metadata-discovery-projects=*", "--api-bind-address=0"} {
		if _, err := parseFlags(append(slices.Clone(base), extra)); err == nil {
			t.Fatal("invalid or inherited discovery scope", extra)
		}
	}
}
