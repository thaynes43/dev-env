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
	for _, extra := range []string{"--enable-private-project-tasks", "--enable-private-history-retention=false", "--enable-managed-codex-tasks=false", "--client-service-accounts=other/client", "--session-namespace=other", "--session-directory-projects=other", "--enable-task-budgets=false", "--task-budget-namespace=other", "--private-project-pilot={}"} {
		if _, err := parseFlags(append(slices.Clone(base), extra)); err == nil {
			t.Fatal("pilot widened", extra)
		}
	}
}

// The standby session is default off and needs the API (Story110 T25).
func TestStandbyFlag(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.standbyEnabled {
		t.Fatalf("the standby must be off by default: %+v, %v", o.standbyEnabled, err)
	}
	if o.standbyRepo != "haynes-ops" || o.standbyModel != "claude-opus-5-5" || o.standbyProfile != "full" {
		t.Errorf("standby defaults: %s %s %s", o.standbyRepo, o.standbyModel, o.standbyProfile)
	}
	if o, err = parseFlags([]string{"--enable-standby-session"}); err != nil || !o.standbyEnabled {
		t.Errorf("--enable-standby-session: %v %v", o.standbyEnabled, err)
	}
	if _, err = parseFlags([]string{"--enable-standby-session", "--api-bind-address=0"}); err == nil {
		t.Error("the standby without the API was accepted")
	}
}

func TestPortalReaderFlag(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.portalReaderEnabled {
		t.Fatal("portal reader must be off by default")
	}
	o, err = parseFlags([]string{"--enable-portal-reader"})
	if err != nil || !o.portalReaderEnabled {
		t.Fatal("portal reader flag was not accepted")
	}
	if _, err = parseFlags([]string{"--enable-portal-reader", "--session-namespace=other"}); err == nil {
		t.Fatal("portal reader accepted a namespace other than dev-agents")
	}
	if _, err = parseFlags([]string{"--client-service-accounts=dev-agents/dev-env-v2-portal"}); err == nil {
		t.Fatal("portal account was also accepted as a client")
	}
	if _, err = parseFlags([]string{"--human-service-account=dev-agents/dev-env-v2-portal"}); err == nil {
		t.Fatal("portal account was also accepted as the human caller")
	}
}

func TestPortalActionTargetsConfig(t *testing.T) {
	for _, tc := range []struct {
		args          []string
		ok            bool
		send, history int
	}{
		{nil, true, 0, 0}, {[]string{"--enable-portal-reader"}, true, 0, 0},
		{[]string{"--enable-portal-reader", "--portal-send-target=agent-a=uid-a"}, true, 1, 0},
		{[]string{"--enable-portal-reader", "--portal-history-target=agent-a=uid-a"}, true, 0, 1},
		{[]string{"--portal-send-target=agent-a=uid-a"}, false, 0, 0},
		{[]string{"--enable-portal-reader", "--portal-send-target=bad"}, false, 0, 0},
		{[]string{"--enable-portal-reader", "--portal-send-target=agent-a=uid-a", "--portal-send-target=agent-a=uid-a"}, false, 0, 0},
		{[]string{"--enable-portal-reader", "--portal-history-target=agent-a=uid-a", "--portal-history-target=agent-b=uid-a"}, false, 0, 0},
	} {
		o, err := parseFlags(tc.args)
		if (err == nil) != tc.ok {
			t.Fatalf("scope parse mismatch for %v", tc.args)
		}
		if tc.ok && (len(o.portalMessageTargets) != tc.send || len(o.portalConversationTargets) != tc.history) {
			t.Fatal("scope leaked across actions")
		}
	}
}

func TestPilotAgentFlags(t *testing.T) {
	const digest = "@sha256:0000000000000000000000000000000000000000000000000000000000000009"
	const image = "ghcr.io/thaynes43/dev-env:2.47.0" + digest
	o, err := parseFlags(nil)
	if err != nil || o.pilotAgent != nil {
		t.Fatalf("pilot agent must be off by default: %v %v", o.pilotAgent, err)
	}
	for _, img := range []string{image, "ghcr.io/thaynes43/dev-env:2.0.0" + digest} {
		o, err = parseFlags([]string{"--pilot-agent-image=" + img, "--pilot-agent-session=dev-env-1027-111645/history-pilot-1"})
		if err != nil || o.pilotAgent == nil || o.pilotAgent.Image != img || o.pilotAgent.Parent != "dev-env-1027-111645" || o.pilotAgent.Key != "history-pilot-1" {
			t.Fatalf("valid pair refused or misread for %s: %+v %v", img, o.pilotAgent, err)
		}
	}
	for name, args := range map[string][]string{
		"image only":          {"--pilot-agent-image=" + image},
		"session only":        {"--pilot-agent-session=dev-env-1027-111645/k"},
		"no digest":           {"--pilot-agent-image=ghcr.io/thaynes43/dev-env:2.47.0", "--pilot-agent-session=dev-env-1027-111645/k"},
		"digest only":         {"--pilot-agent-image=ghcr.io/thaynes43/dev-env" + digest, "--pilot-agent-session=dev-env-1027-111645/k"},
		"no tag no digest":    {"--pilot-agent-image=ghcr.io/thaynes43/dev-env", "--pilot-agent-session=dev-env-1027-111645/k"},
		"1.x tag":             {"--pilot-agent-image=ghcr.io/thaynes43/dev-env:1.9.0" + digest, "--pilot-agent-session=dev-env-1027-111645/k"},
		"0.x tag":             {"--pilot-agent-image=ghcr.io/thaynes43/dev-env:0.1.0" + digest, "--pilot-agent-session=dev-env-1027-111645/k"},
		"3.x tag":             {"--pilot-agent-image=ghcr.io/thaynes43/dev-env:3.0.0" + digest, "--pilot-agent-session=dev-env-1027-111645/k"},
		"suffixed tag":        {"--pilot-agent-image=ghcr.io/thaynes43/dev-env:2.47.0-rc1" + digest, "--pilot-agent-session=dev-env-1027-111645/k"},
		"trailing text":       {"--pilot-agent-image=" + image + "x", "--pilot-agent-session=dev-env-1027-111645/k"},
		"other repository":    {"--pilot-agent-image=ghcr.io/thaynes43/other:2.47.0" + digest, "--pilot-agent-session=dev-env-1027-111645/k"},
		"other registry":      {"--pilot-agent-image=docker.io/thaynes43/dev-env" + digest, "--pilot-agent-session=dev-env-1027-111645/k"},
		"uppercase digest":    {"--pilot-agent-image=ghcr.io/thaynes43/dev-env:2.47.0@sha256:" + strings.Repeat("A", 64), "--pilot-agent-session=dev-env-1027-111645/k"},
		"short digest":        {"--pilot-agent-image=ghcr.io/thaynes43/dev-env:2.47.0@sha256:abc", "--pilot-agent-session=dev-env-1027-111645/k"},
		"session no slash":    {"--pilot-agent-image=" + image, "--pilot-agent-session=dev-env-1027-111645"},
		"session two slashes": {"--pilot-agent-image=" + image, "--pilot-agent-session=dev-env-1027-111645/a/b"},
		"bad parent":          {"--pilot-agent-image=" + image, "--pilot-agent-session=Dev_Env/k"},
		"dotted parent":       {"--pilot-agent-image=" + image, "--pilot-agent-session=dev.env/k"},
		"empty parent":        {"--pilot-agent-image=" + image, "--pilot-agent-session=/k"},
		"empty key":           {"--pilot-agent-image=" + image, "--pilot-agent-session=dev-env-1027-111645/"},
		"bad key":             {"--pilot-agent-image=" + image, "--pilot-agent-session=dev-env-1027-111645/-bad"},
		"key with space":      {"--pilot-agent-image=" + image, "--pilot-agent-session=dev-env-1027-111645/a b"},
		"key too long":        {"--pilot-agent-image=" + image, "--pilot-agent-session=dev-env-1027-111645/" + strings.Repeat("k", 64)},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPortalLaunchRepoFlag(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || len(o.portalLaunchRepos) != 0 {
		t.Fatalf("portal launch must be off by default: %v %v", o.portalLaunchRepos, err)
	}
	o, err = parseFlags([]string{"--enable-portal-reader", "--portal-launch-repo=dev-env", "--portal-launch-repo=haynes-ops"})
	if err != nil || len(o.portalLaunchRepos) != 2 {
		t.Fatalf("valid repos refused: %v %v", o.portalLaunchRepos, err)
	}
	var many []string
	for i := 0; i < 17; i++ {
		many = append(many, "--portal-launch-repo=repo"+strings.Repeat("x", i))
	}
	for name, args := range map[string][]string{
		"without the reader": {"--portal-launch-repo=dev-env"},
		"duplicate":          {"--enable-portal-reader", "--portal-launch-repo=dev-env", "--portal-launch-repo=dev-env"},
		"empty":              {"--enable-portal-reader", "--portal-launch-repo="},
		"a path":             {"--enable-portal-reader", "--portal-launch-repo=a/b"},
		"dot dot":            {"--enable-portal-reader", "--portal-launch-repo=.."},
		"dot":                {"--enable-portal-reader", "--portal-launch-repo=."},
		"a space":            {"--enable-portal-reader", "--portal-launch-repo=a b"},
		"too long":           {"--enable-portal-reader", "--portal-launch-repo=" + strings.Repeat("r", 101)},
		"seventeen":          append([]string{"--enable-portal-reader"}, many...),
	} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := parseFlags(append([]string{"--enable-portal-reader"}, many[:16]...)); err != nil {
		t.Errorf("sixteen repos refused: %v", err)
	}
}
