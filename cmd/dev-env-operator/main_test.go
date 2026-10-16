package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
)

func TestPublishedContextFlagsDefaultOffAndIndependentScope(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.contextsEnabled || len(o.contextReadProjects) != 0 {
		t.Fatal("context enabled by default", err)
	}
	bound := []string{"--enable-published-contexts", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43", "--context-payload-read-projects=sample"}
	o, err = parseFlags(bound)
	if err != nil || !o.contextsEnabled || o.taskClaimsEnabled || len(o.taskClaimReadProjects) != 0 || o.privateProjectTasks || o.managedCodexTasks || o.privateHistoryRetention {
		t.Fatal("context widened other gates", err)
	}
	for _, args := range [][]string{{"--enable-published-contexts"}, {"--context-payload-read-projects=sample"}, append(slices.Clone(bound), "--api-bind-address=0"), append(slices.Clone(bound), "--context-payload-read-projects=*"), append(slices.Clone(bound), "--context-payload-read-projects=sample,sample")} {
		if _, err := parseFlags(args); err == nil {
			t.Fatal("missing/broad context authority accepted", args)
		}
	}
}

func TestTaskClaimFlagsDefaultOffAndExplicitScope(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.taskClaimsEnabled || len(o.taskClaimReadProjects) != 0 {
		t.Fatal("claims enabled by default", err)
	}
	bound := []string{"--enable-task-claims", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43", "--task-claims-read-projects=sample"}
	o, err = parseFlags(bound)
	if err != nil || !o.taskClaimsEnabled || len(o.taskClaimReadProjects) != 1 || o.coordinatorEnabled || o.privateProjectTasks || o.taskBudgetsEnabled {
		t.Fatal("claim gate widened unrelated features", err)
	}
	for _, args := range [][]string{
		{"--enable-task-claims"}, {"--task-claims-read-projects=sample"}, append(slices.Clone(bound), "--api-bind-address=0"),
		append(slices.Clone(bound), "--task-claims-read-projects="), append(slices.Clone(bound), "--task-claims-read-projects=sample,sample"),
		append(slices.Clone(bound), "--task-claims-read-projects=*"),
	} {
		if _, err := parseFlags(args); err == nil {
			t.Fatal("claims accepted missing/nonnarrow scope", args)
		}
	}
}

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

func TestPrivateHistoryRetentionFlagIsExplicitAndDefaultOff(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.privateHistoryRetention {
		t.Fatal("private retention enabled by default")
	}
	o, err = parseFlags([]string{"--enable-private-history-retention"})
	if err != nil || !o.privateHistoryRetention {
		t.Fatal("explicit retention setting missing")
	}
}

// DESIGN-025 13: the terminal record is default off and turned on explicitly.
func TestPrivateTerminalRecordFlagIsExplicitAndDefaultOff(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.privateTerminalRecord {
		t.Fatal("the private terminal record is on by default")
	}
	o, err = parseFlags([]string{"--enable-private-terminal-record"})
	if err != nil || !o.privateTerminalRecord {
		t.Fatal("explicit terminal record setting missing")
	}
}

// DESIGN-025 13: the stop request is default off, turned on explicitly, and
// refused at start without the terminal record that proves its stop.
func TestPrivateExecutorStopFlagIsExplicitDefaultOffAndNeedsTheRecord(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.privateExecutorStop {
		t.Fatal("the private executor stop is on by default")
	}
	if _, err := parseFlags([]string{"--enable-private-executor-stop"}); err == nil {
		t.Fatal("the stop was accepted without the terminal record flag")
	}
	o, err = parseFlags([]string{"--enable-private-terminal-record", "--enable-private-executor-stop"})
	if err != nil || !o.privateExecutorStop || !o.privateTerminalRecord {
		t.Fatal("explicit stop setting missing:", err)
	}
}

// T17's flag part (DESIGN-025 13): the HOME expiry up to Archived is off by
// default, refused without the terminal record, and its grace may only be
// lengthened.
func TestHomeHistoryArchiveFlagIsExplicitDefaultOffAndNeedsTheRecord(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.homeHistoryArchive || o.homeExpiryGrace != controller.DefaultHomeExpiryGrace {
		t.Fatalf("defaults: archive %v, grace %s, err %v; want off and 720h", o.homeHistoryArchive, o.homeExpiryGrace, err)
	}
	if _, err := parseFlags([]string{"--enable-home-history-archive"}); err == nil {
		t.Fatal("the archive was accepted without the terminal record flag")
	}
	for _, grace := range []string{"719h59m", "24h", "0s"} {
		if _, err := parseFlags([]string{"--home-expiry-grace", grace}); err == nil {
			t.Fatalf("a grace of %s under 720h was accepted", grace)
		}
	}
	o, err = parseFlags([]string{"--enable-private-terminal-record", "--enable-home-history-archive", "--home-expiry-grace", "1000h"})
	if err != nil || !o.homeHistoryArchive || o.homeExpiryGrace != 1000*time.Hour {
		t.Fatal("explicit archive setting missing:", err)
	}
}

// T17's flag part (DESIGN-025 13): the archived-history reads and the HOME
// delete are each off unless set, and the delete is refused at start unless
// the archive and the reads are both on.
func TestHistoryReadsAndHomeDeleteFlagsAreExplicitDefaultOff(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.historyArchiveReads || o.homeExpiryDelete {
		t.Fatalf("defaults: reads %v, delete %v, err %v; want both off", o.historyArchiveReads, o.homeExpiryDelete, err)
	}
	o, err = parseFlags([]string{"--enable-history-archive-reads"})
	if err != nil || !o.historyArchiveReads || o.homeExpiryDelete {
		t.Fatalf("reads alone: %+v %v", o.historyArchiveReads, err)
	}
	for _, args := range [][]string{
		{"--enable-home-expiry-delete"},
		{"--enable-home-expiry-delete", "--enable-history-archive-reads"},
		{"--enable-home-expiry-delete", "--enable-private-terminal-record", "--enable-home-history-archive"},
		{"--enable-home-expiry-delete", "--enable-private-terminal-record", "--enable-history-archive-reads"},
	} {
		if _, err := parseFlags(args); err == nil || !strings.Contains(err.Error(), "--enable-home-expiry-delete") {
			t.Errorf("%v was accepted: %v", args, err)
		}
	}
	o, err = parseFlags([]string{"--enable-home-expiry-delete", "--enable-private-terminal-record", "--enable-home-history-archive", "--enable-history-archive-reads"})
	if err != nil || !o.homeExpiryDelete {
		t.Fatalf("delete with archive and reads: %v", err)
	}
}

func TestPrivatePilotFlagDefaultOffAndExactExistingAuthority(t *testing.T) {
	b := testutil.NewPilot(t)
	raw, _ := json.Marshal(b.Policy)
	old := testutil.New(t, "codex", "dev/dev-env")
	old.Record.CommonRevision = b.Policy.CommonRevision
	canaries, _ := json.Marshal([]projectcanary.Record{old.Record})
	base := []string{"--private-project-pilot=" + string(raw), "--enable-private-project-canaries", "--private-project-canaries=" + string(canaries), "--enable-retained-common-rules", "--client-service-accounts=dev/dev-env", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43", "--enable-private-history-retention", "--enable-managed-codex-tasks", "--api-url=https://operator.example", "--enable-session-directory", "--session-directory-projects=dev-env", "--metadata-discovery-projects=dev-env", "--enable-task-budgets", "--enable-coordinator-callers", `--coordinator-hosts=[{"serviceAccount":"dev-env-system/host-a","podName":"host-a","hostID":"codex-a"}]`, `--assigned-task-budgets={"codex-a":"campaign"}`, "--task-budget-namespace=dev-env-budgets", "--task-budget-worker-image=example/worker@sha256:" + strings.Repeat("a", 64)}
	o, err := parseFlags(nil)
	if err != nil || o.privatePilot != nil {
		t.Fatal("pilot default enabled", err)
	}
	o, err = parseFlags(base)
	if err != nil || o.privatePilot == nil || o.privateProjectTasks {
		t.Fatal("exact existing authority refused", err)
	}
	for _, extra := range []string{"--enable-private-project-tasks", "--enable-private-history-retention=false", "--enable-managed-codex-tasks=false", "--client-service-accounts=other/client", "--session-namespace=other", "--session-directory-projects=other", "--private-project-pilot={}"} {
		if _, err := parseFlags(append(slices.Clone(base), extra)); err == nil {
			t.Fatal("pilot widened", extra)
		}
	}
}
