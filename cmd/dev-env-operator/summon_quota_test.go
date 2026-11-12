package main

// The operator-side proofs of story 783 (slice a of item 743; DESIGN-064 6.9):
// the four flags have no code defaults and refuse to start on a bad value;
// with every line unset no gate is built and nothing is read; the gate reads
// the session allowance store at most once per 30 seconds, through a bounded
// read that admits on a timeout; and in a real admission pass on a fake API
// server only sessions with spec.caller reach the gate.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/summonledger"
	"github.com/thaynes43/dev-env/internal/summonquota"
)

var quotaT0 = time.Date(2026, 11, 12, 22, 0, 0, 0, time.UTC)

// installQuotaArgs are the values haynes-ops is to set (C8): the weekly line
// unset.
var installQuotaArgs = []string{"--summon-normal-max-five-hour=95", "--summon-bulk-max-five-hour=80", "--summon-usage-max-age=15m"}

// Proof 7, and ruling Q12: no code defaults, so an unset line is off.
func TestSummonQuotaFlagsHaveNoDefaults(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.summonQuota != (summonquota.Lines{}) {
		t.Fatalf("a quota line or max age is set by default: %+v", o.summonQuota)
	}
	o, err = parseFlags(installQuotaArgs)
	if err != nil {
		t.Fatal(err)
	}
	if want := (summonquota.Lines{NormalFiveHour: 95, BulkFiveHour: 80, MaxAge: 15 * time.Minute}); o.summonQuota != want {
		t.Fatalf("install values: %+v, want %+v", o.summonQuota, want)
	}
	o, err = parseFlags(append([]string{"--summon-bulk-max-seven-day=90"}, installQuotaArgs...))
	if err != nil || o.summonQuota.BulkSevenDay != 90 {
		t.Fatalf("weekly line: %+v %v", o.summonQuota, err)
	}
	// An empty value is the unset default, as for --summon-create-kinds.
	o, err = parseFlags([]string{"--summon-normal-max-five-hour=", "--summon-bulk-max-five-hour=", "--summon-bulk-max-seven-day="})
	if err != nil || o.summonQuota.Set() {
		t.Fatalf("empty values: %+v %v", o.summonQuota, err)
	}
	// Each line takes the bounds 1 and 100.
	for _, v := range []string{"1", "100"} {
		if _, err := parseFlags([]string{"--summon-bulk-max-five-hour=" + v, "--summon-usage-max-age=1s"}); err != nil {
			t.Errorf("--summon-bulk-max-five-hour=%s refused: %v", v, err)
		}
	}
}

// Proof 7: the start refusals, each naming its flag.
func TestSummonQuotaStartRefusals(t *testing.T) {
	for _, flagName := range []string{"summon-normal-max-five-hour", "summon-bulk-max-five-hour", "summon-bulk-max-seven-day"} {
		for _, v := range []string{"0", "101", "-1", "80.5", "+80", "080", " 80", "eighty", "1e2"} {
			_, err := parseFlags([]string{"--" + flagName + "=" + v, "--summon-usage-max-age=15m"})
			if err == nil {
				t.Errorf("--%s=%q started", flagName, v)
				continue
			}
			if !strings.Contains(err.Error(), "--"+flagName) || !strings.Contains(err.Error(), "1 to 100") {
				t.Errorf("--%s=%q: %v", flagName, v, err)
			}
		}
		// A line needs a positive max age.
		for _, age := range []string{"", "--summon-usage-max-age=0s", "--summon-usage-max-age=-5m"} {
			args := []string{"--" + flagName + "=80"}
			if age != "" {
				args = append(args, age)
			}
			_, err := parseFlags(args)
			if err == nil || !strings.Contains(err.Error(), "--summon-usage-max-age") {
				t.Errorf("--%s=80 with %q: %v", flagName, age, err)
			}
		}
	}
	// A max age with no line is inert and starts (the order refuses it only
	// while a line is set).
	if _, err := parseFlags([]string{"--summon-usage-max-age=15m"}); err != nil {
		t.Errorf("a max age alone: %v", err)
	}
	if _, err := parseFlags([]string{"--summon-usage-max-age=fifteen"}); err == nil {
		t.Error("an unparsable max age started")
	}
}

// Proof 5 in main.go: with every line unset no gate is built, so the
// reconciler's seam stays nil and admits as today, and the store is never
// read.
func TestSummonQuotaHookOffWithoutLines(t *testing.T) {
	spy := &countingReader{}
	if hook := summonQuotaHook(summonquota.Lines{MaxAge: 15 * time.Minute}, spy, logr.Discard(), nil); hook != nil {
		t.Fatal("a quota hook was built with every line unset")
	}
	o, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if hook := summonQuotaHook(o.summonQuota, spy, logr.Discard(), nil); hook != nil || reconcilerForOptions(o).SummonQuota != nil || spy.gets != 0 {
		t.Fatalf("default flags: hook %v, %d reads", hook != nil, spy.gets)
	}
}

// countingReader counts the gate's reads of the allowance ConfigMap. With
// block set a read waits for its context.
type countingReader struct {
	client.Reader
	gets  int
	block bool
}

func (c *countingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if key.Namespace == apiserver.SessionAllowanceNamespace && key.Name == apiserver.SessionAllowanceName {
		c.gets++
	}
	if c.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return c.Reader.Get(ctx, key, obj, opts...)
}

// allowanceConfigMap is the store's ConfigMap with one Claude reading taken
// two minutes before quotaT0.
func allowanceConfigMap(t *testing.T, fiveHour, sevenDay string) *corev1.ConfigMap {
	t.Helper()
	readAt := quotaT0.Add(-2 * time.Minute)
	var windows []protocol.AllowanceWindow
	if fiveHour != "" {
		windows = append(windows, protocol.AllowanceWindow{Minutes: summonquota.FiveHourMinutes, UsedPercent: fiveHour, ResetsAt: readAt.Add(3 * time.Hour)})
	}
	if sevenDay != "" {
		windows = append(windows, protocol.AllowanceWindow{Minutes: summonquota.SevenDayMinutes, UsedPercent: sevenDay, ResetsAt: readAt.Add(72 * time.Hour)})
	}
	entry := map[string]any{"schema": apiserver.SessionAllowanceSchema, "provider": protocol.AgentClaude, "readings": []apiserver.StoredSessionReading{{
		Session: "dev-env-1112-000000", Source: protocol.AllowanceSourceClaudeStatusline, ReadAt: readAt, ReportedAt: readAt, Windows: windows}}}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: apiserver.SessionAllowanceNamespace, Name: apiserver.SessionAllowanceName},
		Data: map[string]string{protocol.AgentClaude + ".json": string(raw)}}
}

func quotaScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func quotaSession(name string, p v1alpha1.SummonPriority) *v1alpha1.AgentSession {
	s := &v1alpha1.AgentSession{}
	s.Name, s.Namespace = name, "dev-agents"
	s.Spec.Caller, s.Spec.Agent, s.Spec.Lane = "cigar-ns.cigar-curator", v1alpha1.AgentClaude, v1alpha1.LaneCuration
	s.Spec.Summon = &v1alpha1.SessionSummon{DispatchID: "d-" + name, Priority: p, FallbackModel: "claude-opus-5", OnUnreported: v1alpha1.SummonOnUnreportedFailed}
	return s
}

type quotaLogs struct{ lines []string }

func (l *quotaLogs) logger() logr.Logger {
	return funcr.New(func(prefix, args string) { l.lines = append(l.lines, args) }, funcr.Options{})
}

// The hook reads the real SessionAllowanceStore once for several sessions
// at one time, and decides by it: bulk at 81 % of the 5-hour window waits,
// normal at 81 % starts.
func TestSummonQuotaHookReadsTheStoreOnce(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(quotaScheme(t)).WithObjects(allowanceConfigMap(t, "81", "90")).Build()
	spy := &countingReader{Reader: c}
	now := quotaT0
	logs := &quotaLogs{}
	hook := summonQuotaHook(summonquota.Lines{NormalFiveHour: 95, BulkFiveHour: 80, MaxAge: 15 * time.Minute}, spy, logs.logger(), func() time.Time { return now })
	if hook == nil {
		t.Fatal("no hook with the install lines")
	}
	if hook(quotaSession("bulk-a", v1alpha1.SummonPriorityBulk)) || hook(quotaSession("bulk-b", v1alpha1.SummonPriorityBulk)) {
		t.Error("bulk at 81 % of the 5-hour window, line 80, admitted")
	}
	if !hook(quotaSession("normal-a", v1alpha1.SummonPriorityNormal)) || !hook(quotaSession("urgent-a", v1alpha1.SummonPriorityUrgent)) {
		t.Error("normal at 81 % or urgent held")
	}
	if spy.gets != 1 {
		t.Fatalf("%d store reads for four sessions at one time, want 1", spy.gets)
	}
	if len(logs.lines) != 3 || !strings.Contains(logs.lines[0], `"fiveHourUsed"="81"`) || !strings.Contains(logs.lines[0], `"sevenDayUsed"="90"`) || !strings.Contains(logs.lines[0], `"readingAge"="2m0s"`) {
		t.Fatalf("logs %q", logs.lines)
	}
	now = now.Add(summonquota.DefaultReuse)
	hook(quotaSession("bulk-a", v1alpha1.SummonPriorityBulk))
	if spy.gets != 2 {
		t.Fatalf("30 s later: %d store reads, want 2", spy.gets)
	}
	// A store with no ConfigMap is no reading: admitted, and it says so.
	empty := &countingReader{Reader: fake.NewClientBuilder().WithScheme(quotaScheme(t)).Build()}
	logs.lines = nil
	hook = summonQuotaHook(summonquota.Lines{BulkFiveHour: 80, MaxAge: 15 * time.Minute}, empty, logs.logger(), func() time.Time { return now })
	if !hook(quotaSession("bulk-a", v1alpha1.SummonPriorityBulk)) || empty.gets != 1 || !strings.Contains(logs.lines[0], `"reason"="no-reading"`) {
		t.Fatalf("no ConfigMap: %d reads, logs %q", empty.gets, logs.lines)
	}
}

// Proof 4 through the real store: a read that does not answer is cut off by
// the gate's bounded timeout and admits, logged as a timeout.
func TestSummonQuotaHookTimeoutAdmits(t *testing.T) {
	spy := &countingReader{Reader: fake.NewClientBuilder().WithScheme(quotaScheme(t)).Build(), block: true}
	logs := &quotaLogs{}
	gate := summonQuotaGate(summonquota.Lines{BulkFiveHour: 80, MaxAge: 15 * time.Minute}, spy, logs.logger(), func() time.Time { return quotaT0 })
	gate.ReadTimeout = 20 * time.Millisecond
	if !gate.Admits(quotaSession("bulk-a", v1alpha1.SummonPriorityBulk)) || spy.gets != 1 || !strings.Contains(logs.lines[0], `"reason"="read-timeout"`) {
		t.Fatalf("%d reads, logs %q", spy.gets, logs.lines)
	}
	// main.go leaves ReadTimeout zero: the gate's 5-second default.
	if summonQuotaGate(summonquota.Lines{BulkFiveHour: 80, MaxAge: time.Minute}, spy, logr.Discard(), nil).ReadTimeout != 0 || summonquota.DefaultReadTimeout != 5*time.Second {
		t.Fatal("the read timeout is not the 5 s default")
	}
}

// The gate's store never writes: its writer refuses.
func TestSummonQuotaStoreIsReadOnly(t *testing.T) {
	if err := (quotaReadOnly{}).Update(context.Background(), &corev1.ConfigMap{}); err == nil {
		t.Fatal("the quota gate's store writer accepted a write")
	}
}

// Proofs 6 and 8 in a real admission pass (internal/controller's
// SummonAdmissionPass, unchanged) on a fake API server, with the hook main.go
// builds: three Queued curation sessions of one caller and one ordinary
// session. At 81 % the three wait QuotaHold on one store read, and the
// ordinary session never reaches the gate; at 80 % the oldest is admitted.
func TestSummonQuotaInTheAdmissionPass(t *testing.T) {
	for _, tc := range []struct {
		used     string
		admitted string
	}{{"81", ""}, {"80", "curate-1"}} {
		t.Run(tc.used, func(t *testing.T) {
			scheme := quotaScheme(t)
			caller := "cigar-ns.cigar-curator"
			policy := &v1alpha1.CallerPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: caller, UID: "policy-uid"},
				Spec: v1alpha1.CallerPolicySpec{Summon: &v1alpha1.CallerPolicySummon{ServiceAccount: "cigar-ns/cigar-curator",
					Limits: v1alpha1.CallerPolicySummonLimits{Concurrent: 3, CreatesPerHour: 10}}}}
			entry := summonledger.Entry{Clock: quotaT0}
			objs := []client.Object{policy, allowanceConfigMap(t, tc.used, "")}
			var names []string
			for i := 1; i <= 3; i++ {
				s := quotaSession("curate-"+string(rune('0'+i)), v1alpha1.SummonPriorityBulk)
				s.UID = types.UID("uid-" + s.Name)
				s.CreationTimestamp = metav1.NewTime(quotaT0.Add(-time.Duration(10-i) * time.Minute))
				s.Spec.Mode, s.Spec.Parent, s.Spec.Profile, s.Spec.Repo = v1alpha1.ModeTask, "cigar-ns/cigar-curator", "ops", "dev-env"
				s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
				s.Spec.Summon.MaxHold = "12h"
				s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Admission: "NeverStarted"}
				entry.Charges = append(entry.Charges, summonledger.Charge{ID: s.Spec.Summon.DispatchID, At: quotaT0, Attempt: quotaT0, Hash: strings.Repeat("0", 64), Name: s.Name})
				objs = append(objs, s)
				names = append(names, s.Name)
			}
			ordinary := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: "dev-env-1112-000001", UID: "uid-ordinary"},
				Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeTask, OperatingMode: v1alpha1.OperatingModeRunning}}
			objs = append(objs, ordinary)
			raw, err := summonledger.Encode(entry)
			if err != nil {
				t.Fatal(err)
			}
			objs = append(objs, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: summonledger.Namespace, Name: summonledger.Name}, Data: map[string]string{caller: raw}})
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentSession{}).WithObjects(objs...).Build()

			spy := &countingReader{Reader: c}
			hook := summonQuotaHook(summonquota.Lines{NormalFiveHour: 95, BulkFiveHour: 80, MaxAge: 15 * time.Minute}, spy, logr.Discard(), func() time.Time { return quotaT0 })
			var seen []string
			r := &controller.Reconciler{Client: c, APIReader: c, SessionNamespace: "dev-agents", SummonedLanes: true,
				PrivateHistoryRetention: true, PrivateTerminalRecord: true, PrivateExecutorStop: true, TerminalTaskPodReclaim: true,
				Now: func() time.Time { return quotaT0 },
				SummonQuota: func(s *v1alpha1.AgentSession) bool {
					seen = append(seen, s.Name)
					return hook(s)
				}}
			if err := r.SummonAdmissionPass(context.Background()); err != nil {
				t.Fatal(err)
			}
			if spy.gets != 1 {
				t.Errorf("%d store reads in one pass, want 1", spy.gets)
			}
			for _, name := range seen {
				if name == ordinary.Name {
					t.Error("a session without spec.caller reached the quota gate")
				}
			}
			for _, name := range names {
				var s v1alpha1.AgentSession
				if err := c.Get(context.Background(), types.NamespacedName{Namespace: "dev-agents", Name: name}, &s); err != nil {
					t.Fatal(err)
				}
				admitted := s.Status.Summon != nil && s.Status.Summon.AdmittedAt != nil
				cond := meta.FindStatusCondition(s.Status.Conditions, controller.ConditionSummonAdmitted)
				switch {
				case name == tc.admitted && !admitted:
					t.Errorf("%s not admitted at %s %%: %+v", name, tc.used, cond)
				case name != tc.admitted && admitted:
					t.Errorf("%s admitted at %s %%", name, tc.used)
				case tc.admitted == "" && (cond == nil || cond.Reason != controller.SummonReasonQuotaHold):
					t.Errorf("%s at %s %%: condition %+v, want reason %s", name, tc.used, cond, controller.SummonReasonQuotaHold)
				}
			}
			// At 81 % every curation session reached the gate; at 80 % the
			// first was admitted and holds the single-flight lane, so the
			// others wait LaneBusy without asking it.
			want := names
			if tc.admitted != "" {
				want = names[:1]
			}
			if strings.Join(seen, ",") != strings.Join(want, ",") {
				t.Errorf("the gate saw %v, want %v", seen, want)
			}
		})
	}
}
