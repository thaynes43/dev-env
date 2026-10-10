package main

import (
	"slices"
	"testing"
)

func TestParseFlagsAPI(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "dev-env-system")
	o, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.apiAddr != ":8443" || o.humanSA != "dev-env-system/dev-env-human" || !slices.Equal(o.clientSAs, []string{"dev-agents/dev-env-workbench"}) {
		t.Errorf("defaults %+v", o)
	}

	o, err = parseFlags([]string{"--client-service-accounts=dev-agents/dev-env-workbench,dev/dev-env"})
	if err != nil || !slices.Equal(o.clientSAs, []string{"dev-agents/dev-env-workbench", "dev/dev-env"}) {
		t.Errorf("the v1 pod as a client: %+v %v", o.clientSAs, err)
	}

	for _, bad := range [][]string{
		{"--client-service-accounts=dev-env"},
		{"--human-service-account=dev-env-human"},
		{"--human-service-account="},
		{"--api-tls-dir="},
		{"--grant-approval-url=http://dev-env.example.com/grants/"},
		{"--grant-approval-url=dev-env.example.com/grants/"},
		{"--grant-approval-url=https:///grants/"},
	} {
		if _, err := parseFlags(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	o, err = parseFlags([]string{"--grant-approval-url=https://dev-env.example.com/grants/"})
	if err != nil || o.grantApprovalURL != "https://dev-env.example.com/grants/" {
		t.Errorf("the approval page: %q %v", o.grantApprovalURL, err)
	}
	if _, err := parseFlags([]string{"--api-bind-address=0", "--api-tls-dir=", "--human-service-account="}); err != nil {
		t.Errorf("with the API off its flags are not checked: %v", err)
	}
}

func TestTaskFeatureFlagsRequireConcreteAuthority(t *testing.T) {
	binding := []string{"--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43"}
	hosts := `--coordinator-hosts=[{"serviceAccount":"dev-env-system/host-a","podName":"host-a","hostID":"codex-a"}]`
	o, err := parseFlags(nil)
	if err != nil || o.coordinatorEnabled || o.privateProjectTasks || o.managedCodexTasks || o.managedChildDecisions || o.catalogBinding != nil || len(o.coordinatorHosts) != 0 {
		t.Fatal("task features or catalog authority enabled by default")
	}
	for _, args := range [][]string{
		{"--enable-managed-codex-tasks"},
		{"--enable-coordinator-callers"},
		append(slices.Clone(binding), "--enable-managed-codex-tasks", "--api-bind-address=0"),
		append(slices.Clone(binding), "--enable-coordinator-callers", hosts),
		append(slices.Clone(binding), "--enable-coordinator-callers", "--enable-managed-codex-tasks"),
		{"--project-catalog=unknown/catalog", "--project-clone-owner=thaynes43", "--enable-managed-codex-tasks"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Fatalf("task features accepted missing concrete authority: %v", args)
		}
	}
	o, err = parseFlags(append(binding, "--enable-coordinator-callers", "--enable-managed-codex-tasks", hosts))
	if err != nil || !o.coordinatorEnabled || !o.managedCodexTasks || o.catalogBinding == nil || len(o.coordinatorHosts) != 1 {
		t.Fatal("fully bound task feature configuration refused")
	}
}

func TestPrivateProjectTaskFeatureRequiresAcceptedCatalog(t *testing.T) {
	for _, args := range [][]string{
		{"--enable-private-project-tasks"},
		{"--enable-private-project-tasks", "--project-catalog=dev-env-system/dev-env-project-catalog"},
		{"--enable-private-project-tasks", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43", "--api-bind-address=0"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Fatal("private project admission accepted unavailable catalog/API authority", args)
		}
	}
	o, err := parseFlags([]string{"--enable-private-project-tasks", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43"})
	if err != nil || !o.privateProjectTasks || o.coordinatorEnabled || o.managedCodexTasks || o.taskBudgetsEnabled || o.catalogBinding == nil {
		t.Fatal("private project gate widened independent provider/coordinator/budget gates or refused catalog authority", err)
	}
}

func TestDecisionFeatureRequiresConfiguredManagedParent(t *testing.T) {
	for _, args := range [][]string{
		{"--enable-managed-child-decisions"},
		{"--enable-managed-child-decisions", "--enable-managed-codex-tasks", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43"},
		{"--enable-managed-child-decisions", "--enable-coordinator-callers"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Fatal("decision feature accepted missing configured parent/provider authority", args)
		}
	}
	args := []string{"--enable-managed-child-decisions", "--enable-coordinator-callers", "--enable-managed-codex-tasks", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43", `--coordinator-hosts=[{"serviceAccount":"dev-env-system/host-a","podName":"host-a","hostID":"codex-a"}]`}
	if o, err := parseFlags(args); err != nil || !o.managedChildDecisions {
		t.Fatal("configured decision feature was refused", err)
	}
}

func TestBudgetFlagsDefaultOffAndRequireProtectedAuthority(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.taskBudgetsEnabled || o.assignedTaskBudgets != nil {
		t.Fatal("budget enabled by default", err)
	}
	base := []string{"--enable-task-budgets", "--enable-coordinator-callers", "--enable-managed-codex-tasks", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43", `--coordinator-hosts=[{"serviceAccount":"dev-env-system/host-a","podName":"host-a","hostID":"codex-a"}]`, `--assigned-task-budgets={"codex-a":"campaign"}`, "--task-budget-namespace=protected-budgets", "--task-budget-worker-image=example/worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if o, err := parseFlags(base); err != nil || !o.taskBudgetsEnabled || o.assignedTaskBudgets["codex-a"] != "campaign" {
		t.Fatal("concrete authority refused", err)
	}
	for _, args := range [][]string{{"--enable-task-budgets"}, {"--task-budget-namespace=protected-budgets"}, append(slices.Clone(base), "--task-budget-namespace=dev-agents"), append(slices.Clone(base), "--task-budget-worker-image=worker:latest"), append(slices.Clone(base), `--assigned-task-budgets={"unknown":"campaign"}`)} {
		if _, err := parseFlags(args); err == nil {
			t.Fatal("unsupported budget authority accepted", args)
		}
	}
}
