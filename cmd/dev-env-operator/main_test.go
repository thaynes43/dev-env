package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
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

// ADR-020 retires archive startup; DESIGN-061 F1 will remove its code. The
// unused legacy grace retains its existing validation until then.
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
	if err == nil || !strings.Contains(err.Error(), "ADR-020") || !strings.Contains(err.Error(), "DESIGN-061 F1") {
		t.Fatal("retired archive flag must refuse startup:", err)
	}
}

// ADR-020 retires reads and deletion through the legacy archive pipeline.
// Even their former dependency combinations must refuse startup.
func TestHistoryReadsAndHomeDeleteFlagsAreExplicitDefaultOff(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.historyArchiveReads || o.homeExpiryDelete {
		t.Fatalf("defaults: reads %v, delete %v, err %v; want both off", o.historyArchiveReads, o.homeExpiryDelete, err)
	}
	o, err = parseFlags([]string{"--enable-history-archive-reads"})
	if err == nil || !strings.Contains(err.Error(), "ADR-020") || !strings.Contains(err.Error(), "DESIGN-061 F1") {
		t.Fatal("retired reads flag must refuse startup:", err)
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
	if err == nil || !strings.Contains(err.Error(), "ADR-020") || !strings.Contains(err.Error(), "DESIGN-061 F1") {
		t.Fatalf("retired delete flag must refuse startup even with its former dependencies: %v", err)
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

func TestCodexHostSelectFlag(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.codexHostSelect {
		t.Fatalf("--enable-codex-host-select must be off by default: %v %v", o.codexHostSelect, err)
	}
	if o.codexHostMaxPerCell != 1 || o.codexHostMaxRunning != 2 {
		t.Fatalf("caps %d %d", o.codexHostMaxPerCell, o.codexHostMaxRunning)
	}
	if o.codexHostHandsOff != "" {
		t.Fatalf("hands-off default %q", o.codexHostHandsOff)
	}
	if _, err = parseFlags([]string{"--enable-codex-host-select"}); err == nil {
		t.Fatal("--enable-codex-host-select without --codex-host-hands-off was accepted")
	}
	o, err = parseFlags([]string{"--enable-codex-host-select", "--codex-host-hands-off="})
	if err != nil || !o.codexHostSelect || o.codexHostHandsOff != "" {
		t.Fatalf("explicit empty hands-off: %v %q %v", o.codexHostSelect, o.codexHostHandsOff, err)
	}
	o, err = parseFlags([]string{"--enable-codex-host-select", "--codex-host-hands-off=kept-off"})
	if err != nil || !o.codexHostSelect || o.codexHostHandsOff != "kept-off" {
		t.Fatalf("named hands-off: %v %q %v", o.codexHostSelect, o.codexHostHandsOff, err)
	}
	if _, err = parseFlags([]string{"--enable-codex-host-select", "--codex-host-hands-off=kept-off", "--api-bind-address=0"}); err == nil {
		t.Fatal("--enable-codex-host-select without the API was accepted")
	}
	if _, err = parseFlags([]string{"--enable-codex-host-select", "--codex-host-hands-off=kept-off", "--codex-host-max-running=0"}); err == nil {
		t.Fatal("a zero cap was accepted")
	}
	if _, err = parseFlags([]string{"--enable-codex-host-select", "--codex-host-hands-off=kept-off", "--codex-host-max-running-per-cell=0"}); err == nil {
		t.Fatal("a zero cell cap was accepted")
	}
	if _, err = parseFlags([]string{"--enable-codex-host-select", "--codex-host-hands-off=Not_A_Name"}); err == nil {
		t.Fatal("a hands-off name that is not a session name was accepted")
	}
}

func TestAsksFlagDefaultOff(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.asksEnabled {
		t.Fatalf("--enable-asks must be off by default: %v %v", o.asksEnabled, err)
	}
	o, err = parseFlags([]string{"--enable-asks"})
	if err != nil || !o.asksEnabled {
		t.Fatalf("--enable-asks: %v %v", o.asksEnabled, err)
	}
	if _, err = parseFlags([]string{"--enable-asks", "--api-bind-address=0"}); err == nil {
		t.Fatal("--enable-asks without the API was accepted")
	}
	if _, err = parseFlags([]string{"--enable-asks", "--session-namespace=other"}); err == nil {
		t.Fatal("--enable-asks outside dev-agents was accepted")
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
	if err != nil || len(o.portalLaunchRepos) != 0 || o.portalLaunchProfile != "" {
		t.Fatalf("portal launch must be off by default: %v %v", o.portalLaunchRepos, err)
	}
	o, err = parseFlags([]string{"--enable-portal-reader", "--portal-launch-profile=dev", "--portal-launch-repo=dev-env", "--portal-launch-repo=haynes-ops"})
	if err != nil || len(o.portalLaunchRepos) != 2 || o.portalLaunchProfile != "dev" || o.portalLaunchMaxUnfinished != 2 {
		t.Fatalf("valid repos refused: %+v %v", o.portalLaunchRepos, err)
	}
	var many []string
	for i := 0; i < 17; i++ {
		many = append(many, "--portal-launch-repo=repo"+strings.Repeat("x", i))
	}
	base := []string{"--enable-portal-reader", "--portal-launch-profile=dev"}
	with := func(extra ...string) []string { return append(append([]string{}, base...), extra...) }
	for name, args := range map[string][]string{
		"without the reader": {"--portal-launch-profile=dev", "--portal-launch-repo=dev-env"},
		"duplicate":          with("--portal-launch-repo=dev-env", "--portal-launch-repo=dev-env"),
		"empty":              with("--portal-launch-repo="),
		"a path":             with("--portal-launch-repo=a/b"),
		"dot dot":            with("--portal-launch-repo=.."),
		"dot":                with("--portal-launch-repo=."),
		"a space":            with("--portal-launch-repo=a b"),
		"too long":           with("--portal-launch-repo=" + strings.Repeat("r", 101)),
		"seventeen":          with(many...),
	} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := parseFlags(with(many[:16]...)); err != nil {
		t.Errorf("sixteen repos refused: %v", err)
	}
}

func TestPortalLaunchProfileAndCapFlags(t *testing.T) {
	repo := []string{"--enable-portal-reader", "--portal-launch-repo=dev-env"}
	with := func(extra ...string) []string { return append(append([]string{}, repo...), extra...) }
	for name, args := range map[string][]string{
		"no profile":         with(),
		"empty profile":      with("--portal-launch-profile="),
		"uppercase profile":  with("--portal-launch-profile=Dev"),
		"workbench profile":  {"--enable-portal-reader", "--portal-launch-profile=workbench", "--portal-launch-repo=dev-env"},
		"underscore profile": with("--portal-launch-profile=a_b"),
		"slash profile":      with("--portal-launch-profile=a/b"),
		"cap zero":           with("--portal-launch-profile=dev", "--portal-launch-max-unfinished=0"),
		"cap nine":           with("--portal-launch-profile=dev", "--portal-launch-max-unfinished=9"),
		"cap negative":       with("--portal-launch-profile=dev", "--portal-launch-max-unfinished=-1"),
		"cap not a number":   with("--portal-launch-profile=dev", "--portal-launch-max-unfinished=x"),
		"profile alone":      {"--enable-portal-reader", "--portal-launch-profile=dev"},
		"cap alone":          {"--enable-portal-reader", "--portal-launch-max-unfinished=3"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for n, want := range map[string]int{"1": 1, "2": 2, "8": 8} {
		o, err := parseFlags(with("--portal-launch-profile=dev", "--portal-launch-max-unfinished="+n))
		if err != nil || o.portalLaunchMaxUnfinished != want {
			t.Errorf("cap %s: %d %v", n, o.portalLaunchMaxUnfinished, err)
		}
	}
}

func TestPortalConversationUsageFlag(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.portalConversationUsage {
		t.Fatalf("conversation usage must be off by default: %v %v", o.portalConversationUsage, err)
	}
	o, err = parseFlags([]string{"--enable-portal-reader", "--portal-conversation-usage"})
	if err != nil || !o.portalConversationUsage {
		t.Fatalf("the flag with the reader was refused: %v %v", o.portalConversationUsage, err)
	}
	if _, err = parseFlags([]string{"--portal-conversation-usage"}); err == nil {
		t.Fatal("the flag without the portal reader was accepted")
	}
}

func TestTaskPilotAgentTupleIsIndependent(t *testing.T) {
	image := "ghcr.io/thaynes43/dev-env:2.62.1@sha256:" + strings.Repeat("a", 64)
	o, err := parseFlags([]string{"--task-pilot-agent-image=" + image, "--task-pilot-agent-session=dev-env-1027-111645/task-pilot", "--pilot-agent-image=" + image, "--pilot-agent-session=dev-env-1027-111645/legacy"})
	if err != nil {
		t.Fatal("valid independent Codex task pilot tuple rejected", err)
	}
	if o.taskPilotAgent == nil || o.taskPilotAgent.Key != "task-pilot" || o.pilotAgent == nil || o.pilotAgent.Key != "legacy" {
		t.Fatal("pilot tuples not independent")
	}
	for _, args := range [][]string{{}, {"--task-pilot-agent-image=" + image}, {"--task-pilot-agent-session=dev-env-1027-111645/task-pilot"}, {"--task-pilot-agent-image=bad", "--task-pilot-agent-session=dev-env-1027-111645/task-pilot"}, {"--task-pilot-agent-image=" + image, "--task-pilot-agent-session=bad"}} {
		got, err := parseFlags(args)
		if len(args) == 0 {
			if err != nil || got.taskPilotAgent != nil {
				t.Fatal("default not off")
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "--task-pilot-agent-") {
			t.Fatal("invalid task pilot accepted or misreported", err)
		}
	}
}

func TestTerminalTaskReclaimFlagsDefaultOffAndRequireStopProof(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.terminalTaskPodReclaim || o.terminalTaskReclaimGrace != 10*time.Minute {
		t.Fatal("unsafe reclaim defaults", err)
	}
	for _, args := range [][]string{
		{"--enable-terminal-task-pod-reclaim"},
		{"--enable-terminal-task-pod-reclaim", "--enable-private-terminal-record"},
		{"--terminal-task-pod-reclaim-grace=0"},
		{"--terminal-task-pod-reclaim-grace=-1m"},
		{"--terminal-task-pod-reclaim-grace=1ms"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Fatal("unsafe reclaim flags accepted", args)
		}
	}
	o, err = parseFlags([]string{"--enable-terminal-task-pod-reclaim", "--enable-private-terminal-record", "--enable-private-executor-stop", "--terminal-task-pod-reclaim-grace=20m"})
	if err != nil {
		t.Fatal(err)
	}
	r := reconcilerForOptions(o)
	if !r.TerminalTaskPodReclaim || r.TerminalTaskReclaimGrace != 20*time.Minute || !r.PrivateExecutorStop || !r.PrivateTerminalRecord || r.HomeHistoryArchive || r.HomeExpiryDelete {
		t.Fatal("reclaim wiring changed retention flags")
	}
}

func TestLegacyTerminalTaskReclaimFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
		bad  bool
	}{
		{name: "default-off"},
		{name: "missing-reclaim", args: []string{"--enable-legacy-terminal-task-reclaim"}, bad: true},
		{name: "stop-without-reclaim", args: []string{"--enable-legacy-terminal-task-reclaim", "--enable-private-terminal-record", "--enable-private-executor-stop"}, bad: true},
		{name: "missing-prerequisites", args: []string{"--enable-legacy-terminal-task-reclaim", "--enable-terminal-task-pod-reclaim"}, bad: true},
		{name: "enabled", args: []string{"--enable-legacy-terminal-task-reclaim", "--enable-terminal-task-pod-reclaim", "--enable-private-terminal-record", "--enable-private-executor-stop"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := parseFlags(tc.args)
			if (err != nil) != tc.bad {
				t.Fatal("dependency validation", err)
			}
			if tc.bad {
				if !strings.Contains(err.Error(), "--enable-terminal-task-pod-reclaim") {
					t.Fatal("missing reclaim dependency diagnostic", err)
				}
				return
			}
			if o.legacyTerminalTaskReclaim != tc.want || reconcilerForOptions(o).LegacyTerminalTaskReclaim != tc.want {
				t.Fatal("legacy flag default or Reconciler wiring differs")
			}
		})
	}
}

func TestPortalQuestionAuditFlagsDefaultOff(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.portalQuestionAudit || len(o.portalQuestionProjects) != 0 {
		t.Fatal("question audit enabled by default", err)
	}
	base := []string{"--enable-durable-inbox", "--inbox-projects=sample", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43", "--enable-inbox-questions", "--enable-portal-reader"}
	o, err = parseFlags(base)
	if err != nil || o.portalQuestionAudit || len(o.portalQuestionProjects) != 0 || !o.inboxQuestions || !o.portalReaderEnabled {
		t.Fatal("prerequisites enabled question audit", err)
	}
	o, err = parseFlags(append(slices.Clone(base), "--enable-portal-question-audit", "--portal-question-projects=sample"))
	if err != nil || !o.portalQuestionAudit || !slices.Equal(o.portalQuestionProjects, []string{"sample"}) {
		t.Fatal("explicit question audit refused", err)
	}
	both := []string{"--enable-durable-inbox", "--inbox-projects=sample,other", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43", "--enable-inbox-questions", "--enable-portal-reader", "--enable-portal-question-audit", "--portal-question-projects=other"}
	o, err = parseFlags(both)
	if err != nil || !slices.Equal(o.portalQuestionProjects, []string{"other"}) {
		t.Fatal("subset project refused", err)
	}
	refused := [][]string{
		{"--enable-portal-question-audit"},
		{"--portal-question-projects=sample"},
		append(slices.Clone(base), "--enable-portal-question-audit"),
		{"--enable-durable-inbox", "--inbox-projects=sample", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43", "--enable-inbox-questions", "--enable-portal-question-audit", "--portal-question-projects=sample"},
		{"--enable-durable-inbox", "--inbox-projects=sample", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43", "--enable-portal-reader", "--enable-portal-question-audit", "--portal-question-projects=sample"},
		append(slices.Clone(base), "--enable-portal-question-audit", "--portal-question-projects=other"),
		append(slices.Clone(base), "--enable-portal-question-audit", "--portal-question-projects=sample,sample"),
		append(slices.Clone(base), "--enable-portal-question-audit", "--portal-question-projects=*"),
	}
	for _, args := range refused {
		if _, err := parseFlags(args); err == nil {
			t.Fatal("inconsistent question audit accepted", args)
		}
	}
}

// TestTaskCleanupFlagsAreExplicitDefaultOffAndBounded (DESIGN-061 2, item 8):
// the clean-up is off by default with its defaults; it needs the terminal
// record and the executor stop; the grace is 1h to 168h, max-active 0 to 3,
// the free floor a quantity, and the allowlist Session names.
func TestTaskCleanupFlagsAreExplicitDefaultOffAndBounded(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.taskSessionCleanup || o.taskCleanupGrace != 168*time.Hour || o.taskCleanupMaxActive != 2 ||
		o.taskCleanupMinSharedFree.Value() != 2<<30 || len(o.taskCleanupSessions) != 0 {
		t.Fatalf("defaults: %v %v %v %v %v, err %v", o.taskSessionCleanup, o.taskCleanupGrace, o.taskCleanupMaxActive, o.taskCleanupMinSharedFree.String(), o.taskCleanupSessions, err)
	}
	if r := reconcilerForOptions(o); r.TaskSessionCleanup || r.TaskCleanupMaxActive != 2 || r.TaskCleanupMinSharedFree != 2<<30 {
		t.Fatalf("the reconciler is not given the defaults: %+v", r)
	}
	on := []string{"--enable-task-session-cleanup", "--enable-private-terminal-record", "--enable-private-executor-stop"}
	for _, args := range [][]string{
		{"--enable-task-session-cleanup"},
		{"--enable-task-session-cleanup", "--enable-private-terminal-record"},
		{"--enable-task-session-cleanup", "--enable-private-executor-stop"},
	} {
		// The last is refused by the stop flag's own rule first.
		if _, err := parseFlags(args); err == nil || !strings.Contains(err.Error(), "--enable-") {
			t.Errorf("%v was accepted: %v", args, err)
		}
	}
	for _, extra := range []string{"--task-cleanup-grace=59m", "--task-cleanup-grace=169h", "--task-cleanup-grace=0s",
		"--task-cleanup-max-active=4", "--task-cleanup-max-active=-1", "--task-cleanup-min-shared-free=-1Gi",
		"--task-cleanup-min-shared-free=lots", "--task-cleanup-sessions=Not_A_Name"} {
		if _, err := parseFlags(append(slices.Clone(on), extra)); err == nil {
			t.Errorf("%s was accepted", extra)
		}
	}
	o, err = parseFlags(append(slices.Clone(on), "--task-cleanup-grace=1h", "--task-cleanup-max-active=0", "--task-cleanup-min-shared-free=512Mi",
		"--task-cleanup-sessions=dev-env-1109-234800, haynes-studio-1110-000448"))
	if err != nil || !o.taskSessionCleanup || o.taskCleanupGrace != time.Hour || o.taskCleanupMaxActive != 0 || o.taskCleanupMinSharedFree.Value() != 512<<20 ||
		!slices.Equal(o.taskCleanupSessions, []string{"dev-env-1109-234800", "haynes-studio-1110-000448"}) {
		t.Fatalf("explicit flags: %+v, err %v", o, err)
	}
	r := reconcilerForOptions(o)
	if !r.TaskSessionCleanup || r.TaskCleanupGrace != time.Hour || r.TaskCleanupMaxActive != 0 || r.TaskCleanupMinSharedFree != 512<<20 || len(r.TaskCleanupSessions) != 2 {
		t.Fatalf("the reconciler is not given the flags: %+v", r)
	}
}

func TestTaskCleanupRequiresLeaderElection(t *testing.T) {
	args := []string{"--enable-task-session-cleanup", "--enable-private-terminal-record", "--enable-private-executor-stop", "--leader-elect=false"}
	if _, err := parseFlags(args); err == nil || !strings.Contains(err.Error(), "--leader-elect") {
		t.Fatalf("cleanup without leader election accepted: %v", err)
	}
	if _, err := parseFlags([]string{"--leader-elect=false"}); err != nil {
		t.Fatalf("leader election remains optional with cleanup off: %v", err)
	}
}

func TestRetiredArchiveFlagsRefuseStartup(t *testing.T) {
	for _, flag := range []string{"--enable-home-history-archive", "--enable-history-archive-reads", "--enable-home-expiry-delete"} {
		t.Run(flag, func(t *testing.T) {
			for _, args := range [][]string{{flag}, {flag, "--enable-private-terminal-record", "--enable-private-executor-stop", "--enable-task-session-cleanup"}} {
				if _, err := parseFlags(args); err == nil || !strings.Contains(err.Error(), flag) || !strings.Contains(err.Error(), "ADR-020") || !strings.Contains(err.Error(), "DESIGN-061 F1") {
					t.Fatalf("retired flag lacks its startup refusal: %v", err)
				}
			}
		})
	}
}

// Proof 8 of story 770: --summon-create-kinds is empty by default, takes task
// and remote once each, and refuses to start on anything else (DESIGN-064
// 5.9, the correction of 2026-11-12).
func TestSummonCreateKindsFlag(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || len(o.summonCreateKinds) != 0 {
		t.Fatalf("--summon-create-kinds must be empty by default: %v %v", o.summonCreateKinds, err)
	}
	// Since OP777 (ruling D8) a kind starts only with the lanes flag and, for
	// task, both task latch flags; remote is refused until slice d
	// (TestSummonedLanesStartRefusals). The parse itself is unchanged.
	withLanes := []string{"--enable-summoned-lanes", "--enable-private-history-retention", "--enable-private-terminal-record", "--enable-private-executor-stop", "--enable-terminal-task-pod-reclaim"}
	for _, tc := range []struct {
		arg  string
		want []v1alpha1.SummonKind
	}{
		{"--summon-create-kinds=", nil},
		{"--summon-create-kinds=task", []v1alpha1.SummonKind{v1alpha1.SummonKindTask}},
	} {
		o, err := parseFlags(append([]string{tc.arg}, withLanes...))
		if err != nil || !slices.Equal(o.summonCreateKinds, tc.want) {
			t.Errorf("%s: %v %v, want %v", tc.arg, o.summonCreateKinds, err, tc.want)
		}
		// The start line carries the value as the flag names it.
		if got := summonCreateKindsValue(o.summonCreateKinds); got != strings.TrimPrefix(tc.arg, "--summon-create-kinds=") {
			t.Errorf("%s: start line value %q", tc.arg, got)
		}
	}
	for _, v := range []string{"Task", "task,task", "task,", ",task", ",", "local", "task, remote", " task", "task,remote,task", "remote", "task,remote", "remote,task"} {
		_, err := parseFlags(append([]string{"--summon-create-kinds=" + v}, withLanes...))
		if err == nil {
			t.Errorf("--summon-create-kinds=%q was accepted", v)
			continue
		}
		if !strings.Contains(err.Error(), "--summon-create-kinds") {
			t.Errorf("--summon-create-kinds=%q: the error does not name the flag: %v", v, err)
		}
	}
}

// OP777 (ruling D8 on item 741): --enable-summoned-lanes is off by default,
// and the operator refuses to start with summoning creates switched on where
// it cannot hold them: any kind without the lanes flag, task without both task
// latch flags, and remote at all until slice d.
func TestSummonedLanesStartRefusals(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.summonedLanes {
		t.Fatalf("--enable-summoned-lanes must be off by default: %v %v", o.summonedLanes, err)
	}
	if o, err := parseFlags([]string{"--enable-summoned-lanes"}); err != nil || !o.summonedLanes {
		t.Fatalf("the lanes flag alone, with no kind switched on, must start: %v", err)
	}
	latch := []string{"--enable-private-history-retention", "--enable-private-terminal-record", "--enable-private-executor-stop", "--enable-terminal-task-pod-reclaim"}
	noRetention := []string{"--enable-private-terminal-record", "--enable-private-executor-stop", "--enable-terminal-task-pod-reclaim"}
	for _, tc := range []struct {
		name string
		args []string
		want string // "" starts
	}{
		{"task without lanes", append([]string{"--summon-create-kinds=task"}, latch...), "--enable-summoned-lanes"},
		{"task with lanes", append([]string{"--summon-create-kinds=task", "--enable-summoned-lanes"}, latch...), ""},
		{"task without retention", append([]string{"--summon-create-kinds=task", "--enable-summoned-lanes"}, noRetention...), "--enable-private-history-retention and --enable-terminal-task-pod-reclaim"},
		{"task without reclaim", []string{"--summon-create-kinds=task", "--enable-summoned-lanes", "--enable-private-history-retention", "--enable-private-terminal-record", "--enable-private-executor-stop"}, "--enable-private-history-retention and --enable-terminal-task-pod-reclaim"},
		{"remote with everything", append([]string{"--summon-create-kinds=remote", "--enable-summoned-lanes"}, latch...), "may not name remote"},
		{"remote without lanes", []string{"--summon-create-kinds=remote"}, "--enable-summoned-lanes"},
		{"empty without lanes", []string{"--summon-create-kinds="}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseFlags(tc.args)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.want != "" && err == nil:
				t.Fatal("started")
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Fatalf("error %q does not say %q", err, tc.want)
			}
		})
	}
}
