package main

import (
	"slices"
	"testing"
)

func TestInboxFlagsDefaultOffAndSeparateContentScope(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.inboxEnabled || len(o.inboxProjects) != 0 || len(o.inboxCoordinatorReadProjects) != 0 {
		t.Fatal("inbox enabled by default", err)
	}
	bound := []string{"--enable-durable-inbox", "--inbox-projects=sample", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43"}
	o, err = parseFlags(bound)
	if err != nil || !o.inboxEnabled || len(o.inboxProjects) != 1 || o.coordinatorEnabled || o.privateProjectTasks || o.taskClaimsEnabled || len(o.inboxCoordinatorReadProjects) != 0 {
		t.Fatal("inbox widened independent gates", err)
	}
	for _, args := range [][]string{{"--enable-durable-inbox"}, {"--inbox-projects=sample"}, {"--inbox-coordinator-read-projects=sample"}, append(slices.Clone(bound), "--api-bind-address=0"), append(slices.Clone(bound), "--inbox-projects=sample,sample"), append(slices.Clone(bound), "--inbox-projects=*"), append(slices.Clone(bound), "--inbox-coordinator-read-projects=sample")} {
		if _, err := parseFlags(args); err == nil {
			t.Fatal("invalid independent inbox scope accepted", args)
		}
	}
	host := `--coordinator-hosts=[{"serviceAccount":"dev-env-system/host-a","podName":"host-a","hostID":"codex-a"}]`
	args := append(slices.Clone(bound), "--enable-coordinator-callers", "--enable-managed-codex-tasks", host, "--inbox-coordinator-read-projects=sample")
	o, err = parseFlags(args)
	if err != nil || !slices.Equal(o.inboxCoordinatorReadProjects, []string{"sample"}) || len(o.taskClaimReadProjects) != 0 {
		t.Fatal("explicit coordinator content scope refused or granted metadata scope", err)
	}
	if _, err := parseFlags(append(args, "--inbox-coordinator-read-projects=other")); err == nil {
		t.Fatal("coordinator scope exceeded enabled projects")
	}
}
