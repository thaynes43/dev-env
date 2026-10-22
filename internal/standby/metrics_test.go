package standby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

var metricNow = time.Date(2026, 10, 22, 21, 0, 0, 0, time.UTC)

func metricScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func metricClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(metricScheme(t)).WithStatusSubresource(&v1alpha1.AgentSession{}).WithObjects(objs...).Build()
}

func newCollector(r client.Reader, enabled bool, now time.Time) *Collector {
	return &Collector{
		Reader:           r,
		SessionNamespace: sessNS,
		StateNamespace:   stateNS,
		Enabled:          enabled,
		Now:              func() time.Time { return now },
	}
}

func claudeRemote(name string) *v1alpha1.AgentSession {
	s := &v1alpha1.AgentSession{}
	s.Namespace = sessNS
	s.Name = name
	s.Labels = map[string]string{v1alpha1.LabelStandby: "true"}
	s.Spec.Agent = v1alpha1.AgentClaude
	s.Spec.Mode = v1alpha1.ModeRemote
	s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	s.Status.Phase = v1alpha1.PhaseRunning
	return s
}

func setReady(s *v1alpha1.AgentSession, agentStatus, remote string, hb time.Time) {
	agent := &v1alpha1.AgentStatus{Status: agentStatus, Boot: protocol.BootReady}
	if !hb.IsZero() {
		stamp := metav1.NewTime(hb)
		agent.LastHeartbeat = &stamp
	}
	s.Status.Agent = agent
	s.Status.RemoteControl = &v1alpha1.RemoteControlStatus{
		State: remote, SessionID: "bridge-secret", URL: "https://claude.ai/code/secret",
	}
}

func registeredSession(name string, hb time.Time) *v1alpha1.AgentSession {
	s := claudeRemote(name)
	setReady(s, protocol.AgentBusy, protocol.RemoteRegistered, hb)
	return s
}

func breakerMap(raw string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: stateNS, Name: DefaultStateName},
		Data:       map[string]string{stateKey: raw},
	}
}

func creationJSON(t *testing.T, times ...time.Time) string {
	t.Helper()
	raw, err := json.Marshal(times)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func series(t *testing.T, c prometheus.Collector) []string {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatal(err)
	}
	return seriesReg(t, reg)
}

func seriesReg(t *testing.T, reg prometheus.Gatherer) []string {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			var gotLabels []string
			for _, l := range m.GetLabel() {
				if strings.HasPrefix(mf.GetName(), "dev_env_standby_") && l.GetName() != metricLabelState {
					t.Errorf("%s has label %s", mf.GetName(), l.GetName())
				}
				gotLabels = append(gotLabels, l.GetName()+"="+l.GetValue())
			}
			line := mf.GetName() + "{" + strings.Join(gotLabels, ",") + "} " + strconv.FormatFloat(m.GetGauge().GetValue(), 'f', -1, 64)
			if strings.Contains(line, "http") || strings.Contains(line, "bridge-secret") || strings.Contains(line, "claude.ai") {
				t.Errorf("series carries session data: %s", line)
			}
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out
}

func standbyOnly(lines []string) []string {
	var out []string
	for _, line := range lines {
		if strings.HasPrefix(line, "dev_env_standby_") {
			out = append(out, line)
		}
	}
	return out
}

func assertSeries(t *testing.T, got, want []string) {
	t.Helper()
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func validLines(counts map[string]int, beats map[string]int64, enabled, budget int) []string {
	lines := []string{
		fmt.Sprintf("%s{} %d", MetricEnabled, enabled),
		fmt.Sprintf("%s{} %d", MetricSnapshotValid, 1),
		fmt.Sprintf("%s{} %d", MetricBudgetExhausted, budget),
	}
	for _, state := range observationStates {
		n := 0
		if counts != nil {
			n = counts[state]
		}
		lines = append(lines, fmt.Sprintf("%s{state=%s} %d", MetricSessionCount, state, n))
		if beats == nil {
			continue
		}
		if ts, ok := beats[state]; ok {
			lines = append(lines, fmt.Sprintf("%s{state=%s} %d", MetricLatestHeartbeat, state, ts))
		}
	}
	return lines
}

func invalidLines(enabled int) []string {
	return []string{
		fmt.Sprintf("%s{} %d", MetricEnabled, enabled),
		fmt.Sprintf("%s{} %d", MetricSnapshotValid, 0),
	}
}

func TestObservationState(t *testing.T) {
	hb := metricNow.Add(-time.Minute)
	deleting := metav1.NewTime(metricNow)
	cases := []struct {
		name string
		edit func(*v1alpha1.AgentSession)
		want string
	}{
		{"foreign agent stays unknown when archived", func(s *v1alpha1.AgentSession) {
			s.Spec.Agent = v1alpha1.AgentCodex
			s.Status.Phase = v1alpha1.PhaseArchived
		}, stateUnknown},
		{"foreign mode stays unknown", func(s *v1alpha1.AgentSession) {
			s.Spec.Mode = v1alpha1.ModeLocal
			setReady(s, protocol.AgentBusy, protocol.RemoteRegistered, hb)
		}, stateUnknown},
		{"archived", func(s *v1alpha1.AgentSession) { s.Status.Phase = v1alpha1.PhaseArchived }, stateTerminal},
		{"failed", func(s *v1alpha1.AgentSession) { s.Status.Phase = v1alpha1.PhaseFailed }, stateTerminal},
		{"deleting with a registered remote state", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentBusy, protocol.RemoteRegistered, hb)
			s.DeletionTimestamp = &deleting
		}, stateTerminal},
		{"suspended mode keeps an old registered remote state out of registered", func(s *v1alpha1.AgentSession) {
			s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
			setReady(s, protocol.AgentBusy, protocol.RemoteRegistered, hb)
		}, stateSuspended},
		{"suspended phase", func(s *v1alpha1.AgentSession) {
			s.Status.Phase = v1alpha1.PhaseSuspended
			setReady(s, protocol.AgentBusy, protocol.RemoteRegistered, hb)
		}, stateSuspended},
		{"suspended mode wins over draining", func(s *v1alpha1.AgentSession) {
			s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
			s.Status.Phase = v1alpha1.PhaseDraining
			setReady(s, protocol.AgentExited, protocol.RemoteRegistered, hb)
		}, stateSuspended},
		{"pending is moving", func(s *v1alpha1.AgentSession) {
			s.Status.Phase = v1alpha1.PhasePending
			setReady(s, protocol.AgentBusy, protocol.RemoteRegistered, hb)
		}, stateMoving},
		{"draining stays moving when the agent has exited", func(s *v1alpha1.AgentSession) {
			s.Status.Phase = v1alpha1.PhaseDraining
			setReady(s, protocol.AgentExited, protocol.RemoteRegistered, hb)
		}, stateMoving},
		{"missing agent", func(s *v1alpha1.AgentSession) {}, stateUnknown},
		{"empty phase", func(s *v1alpha1.AgentSession) {
			s.Status.Phase = ""
			setReady(s, protocol.AgentBusy, protocol.RemoteRegistered, hb)
		}, stateUnknown},
		{"failed boot", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentBusy, protocol.RemoteRegistered, hb)
			s.Status.Agent.Boot = protocol.BootFailed
		}, stateUnknown},
		{"empty boot", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentBusy, protocol.RemoteRegistered, hb)
			s.Status.Agent.Boot = ""
		}, stateUnknown},
		{"booting keeps an old registered remote state out of registered", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentBusy, protocol.RemoteRegistered, hb)
			s.Status.Agent.Boot = protocol.BootBooting
		}, stateBooting},
		{"idle and booting", func(s *v1alpha1.AgentSession) {
			s.Status.Phase = v1alpha1.PhaseIdle
			setReady(s, protocol.AgentBusy, protocol.RemoteRegistered, hb)
			s.Status.Agent.Boot = protocol.BootBooting
		}, stateBooting},
		{"agent login-required", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentLoginRequired, protocol.RemoteRegistered, hb)
		}, stateLoginRequired},
		{"remote login-required", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentBusy, protocol.RemoteLoginRequired, hb)
		}, stateLoginRequired},
		{"login-required wins over remote offline", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentLoginRequired, protocol.RemoteOffline, hb)
		}, stateLoginRequired},
		{"agent exited", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentExited, protocol.RemoteRegistered, hb)
		}, stateOffline},
		{"agent failed", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentFailed, protocol.RemoteRegistered, hb)
		}, stateOffline},
		{"agent interrupted", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentInterrupted, protocol.RemoteRegistered, hb)
		}, stateOffline},
		{"remote offline", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentBusy, protocol.RemoteOffline, hb)
		}, stateOffline},
		{"busy registered", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentBusy, protocol.RemoteRegistered, hb)
		}, stateRegistered},
		{"idle registered", func(s *v1alpha1.AgentSession) {
			s.Status.Phase = v1alpha1.PhaseIdle
			setReady(s, protocol.AgentIdle, protocol.RemoteRegistered, hb)
		}, stateRegistered},
		{"waiting registered", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentWaiting, protocol.RemoteRegistered, hb)
		}, stateRegistered},
		{"pending agent with registered remote state", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentPending, protocol.RemoteRegistered, hb)
		}, stateUnknown},
		{"remote starting", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentBusy, protocol.RemoteStarting, hb)
		}, stateStarting},
		{"url and session id are not registration", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentBusy, "", hb)
		}, stateUnknown},
		{"nil remote control", func(s *v1alpha1.AgentSession) {
			setReady(s, protocol.AgentBusy, protocol.RemoteRegistered, hb)
			s.Status.RemoteControl = nil
		}, stateUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := claudeRemote("standby")
			tc.edit(s)
			if got := observationState(s); got != tc.want {
				t.Fatalf("observationState() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestCollectorFamiliesAndZeroBuckets(t *testing.T) {
	c := newCollector(metricClient(t), true, metricNow)
	reg := prometheus.NewPedanticRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatal(err)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	help := map[string]string{}
	for _, mf := range mfs {
		help[mf.GetName()] = mf.GetHelp()
		if mf.GetType() != dto.MetricType_GAUGE {
			t.Errorf("%s type %v", mf.GetName(), mf.GetType())
		}
	}
	if _, ok := help[MetricLatestHeartbeat]; ok {
		t.Fatal("empty valid scrape emitted a heartbeat family")
	}
	wantHelp := map[string]string{
		MetricEnabled:         "enable-standby-session",
		MetricSnapshotValid:   "snapshot_valid",
		MetricSessionCount:    "including zero",
		MetricBudgetExhausted: "three creations in 30 minutes",
	}
	if len(help) != len(wantHelp) {
		t.Fatalf("families %v", help)
	}
	for name, phrase := range wantHelp {
		if !strings.Contains(help[name], phrase) {
			t.Errorf("%s help %q lacks %q", name, help[name], phrase)
		}
		if strings.Contains(help[name], "http://") || strings.Contains(help[name], "token") {
			t.Errorf("%s help carries session data: %s", name, help[name])
		}
	}
	descs := make(chan *prometheus.Desc, 8)
	c.Describe(descs)
	close(descs)
	var described []string
	for desc := range descs {
		described = append(described, desc.String())
	}
	if len(described) != 5 {
		t.Fatalf("describe count %d: %v", len(described), described)
	}
	joined := strings.Join(described, "\n")
	if !strings.Contains(joined, MetricLatestHeartbeat) || !strings.Contains(joined, "Unix seconds") {
		t.Fatalf("heartbeat descriptor: %s", joined)
	}
	assertSeries(t, seriesReg(t, reg), validLines(nil, nil, 1, 0))
}

func TestCollectorFiltersNamespaceAndLabel(t *testing.T) {
	in := registeredSession("in", metricNow.Add(-time.Minute))
	otherNS := registeredSession("other-ns", metricNow.Add(-time.Minute))
	otherNS.Namespace = "other"
	unlabeled := registeredSession("plain", metricNow.Add(-time.Minute))
	unlabeled.Labels = nil
	foreign := registeredSession("codex", metricNow.Add(-time.Minute))
	foreign.Spec.Agent = v1alpha1.AgentCodex
	c := newCollector(metricClient(t, in, otherNS, unlabeled, foreign), true, metricNow)
	assertSeries(t, series(t, c), validLines(map[string]int{stateRegistered: 1, stateUnknown: 1}, map[string]int64{
		stateRegistered: metricNow.Add(-time.Minute).Unix(),
		stateUnknown:    metricNow.Add(-time.Minute).Unix(),
	}, 1, 0))
}

func TestCollectorRecordsEachCategory(t *testing.T) {
	hb := metricNow.Add(-time.Minute)
	suspended := registeredSession("suspended", hb)
	suspended.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	draining := registeredSession("draining", hb)
	draining.Status.Phase = v1alpha1.PhaseDraining
	draining.Status.Agent.Status = protocol.AgentExited
	booting := registeredSession("booting", hb)
	booting.Status.Agent.Boot = protocol.BootBooting
	login := registeredSession("login", hb)
	login.Status.Agent.Status = protocol.AgentLoginRequired
	offline := registeredSession("offline", hb)
	offline.Status.RemoteControl.State = protocol.RemoteOffline
	starting := registeredSession("starting", hb)
	starting.Status.RemoteControl.State = protocol.RemoteStarting
	archived := registeredSession("archived", hb)
	archived.Status.Phase = v1alpha1.PhaseArchived
	unknown := registeredSession("unknown", hb)
	unknown.Status.Agent.Boot = protocol.BootFailed
	registered := registeredSession("ready", hb)
	c := newCollector(metricClient(t, suspended, draining, booting, login, offline, starting, archived, unknown, registered), true, metricNow)
	assertSeries(t, series(t, c), validLines(map[string]int{
		stateSuspended: 1, stateMoving: 1, stateBooting: 1, stateLoginRequired: 1,
		stateOffline: 1, stateStarting: 1, stateTerminal: 1, stateUnknown: 1, stateRegistered: 1,
	}, map[string]int64{
		stateSuspended: hb.Unix(), stateMoving: hb.Unix(), stateBooting: hb.Unix(),
		stateLoginRequired: hb.Unix(), stateOffline: hb.Unix(), stateStarting: hb.Unix(),
		stateTerminal: hb.Unix(), stateUnknown: hb.Unix(), stateRegistered: hb.Unix(),
	}, 1, 0))
}

func TestCollectorDuplicateRecordsAndRemoval(t *testing.T) {
	older := metricNow.Add(-2 * time.Hour)
	newer := metricNow.Add(-time.Hour)
	a := registeredSession("a", older)
	b := registeredSession("b", newer)
	cl := metricClient(t, a, b)
	c := newCollector(cl, true, metricNow)
	assertSeries(t, series(t, c), validLines(map[string]int{stateRegistered: 2}, map[string]int64{stateRegistered: newer.Unix()}, 1, 0))
	if err := cl.Delete(context.Background(), &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessNS, Name: "b"}}); err != nil {
		t.Fatal(err)
	}
	assertSeries(t, series(t, c), validLines(map[string]int{stateRegistered: 1}, map[string]int64{stateRegistered: older.Unix()}, 1, 0))
}

func TestCollectorHeartbeats(t *testing.T) {
	older := metricNow.Add(-2 * time.Hour)
	newer := metricNow.Add(-time.Minute)
	future := metricNow.Add(time.Minute)
	sessions := []client.Object{
		registeredSession("older", older),
		registeredSession("newer", newer),
		registeredSession("future", future),
		registeredSession("missing", time.Time{}),
	}
	zeroHB := registeredSession("zero", metricNow)
	zeroHB.Status.Agent.LastHeartbeat = &metav1.Time{}
	epoch := registeredSession("epoch", time.Unix(0, 0).UTC())
	sessions = append(sessions, zeroHB, epoch)
	c := newCollector(metricClient(t, sessions...), true, metricNow)
	assertSeries(t, series(t, c), validLines(map[string]int{stateRegistered: 6}, map[string]int64{stateRegistered: newer.Unix()}, 1, 0))

	now := metricNow
	advancing := newCollector(metricClient(t, registeredSession("old", older)), true, now)
	advancing.Now = func() time.Time { return now }
	assertSeries(t, series(t, advancing), validLines(map[string]int{stateRegistered: 1}, map[string]int64{stateRegistered: older.Unix()}, 1, 0))
	now = now.Add(3 * time.Hour)
	got := series(t, advancing)
	assertSeries(t, got, validLines(map[string]int{stateRegistered: 1}, map[string]int64{stateRegistered: older.Unix()}, 1, 0))
	for _, line := range got {
		if strings.HasPrefix(line, MetricLatestHeartbeat) && strings.Contains(line, strconv.FormatInt(now.Unix(), 10)) {
			t.Fatalf("heartbeat followed the collector clock: %s", line)
		}
	}
}

func TestCollectorDisabledSkipsReads(t *testing.T) {
	stub := &stubReader{listErr: errors.New("disabled read"), getErr: errors.New("disabled read")}
	c := newCollector(stub, false, metricNow)
	assertSeries(t, series(t, c), validLines(nil, nil, 0, 0))
	if stub.listCalls != 0 || stub.getCalls != 0 {
		t.Fatalf("disabled collector made %d lists and %d gets", stub.listCalls, stub.getCalls)
	}
	nilReader := &Collector{Enabled: false}
	assertSeries(t, series(t, nilReader), validLines(nil, nil, 0, 0))
}

func TestCollectorInvalidSnapshotOmitsObservations(t *testing.T) {
	session := registeredSession("in", metricNow.Add(-time.Minute))
	forbidden := apierrors.NewForbidden(corev1.Resource("configmaps"), DefaultStateName, errors.New("denied"))
	cases := []struct {
		name   string
		reader client.Reader
	}{
		{"list failure", &stubReader{listErr: errors.New("list failed"), getErr: apierrors.NewNotFound(corev1.Resource("configmaps"), DefaultStateName)}},
		{"permission failure", &stubReader{getErr: forbidden}},
		{"malformed breaker", metricClient(t, session, breakerMap("{"))},
		{"bad creation timestamp", metricClient(t, session, breakerMap(`["not-a-time"]`))},
		{"nil reader", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newCollector(tc.reader, true, metricNow)
			lines := standbyOnly(series(t, c))
			assertSeries(t, lines, invalidLines(1))
			for _, line := range lines {
				if strings.Contains(line, MetricSessionCount) || strings.Contains(line, MetricLatestHeartbeat) || strings.Contains(line, MetricBudgetExhausted) {
					t.Errorf("invalid snapshot emitted %s", line)
				}
			}
		})
	}
}

func TestCollectorInvalidSnapshotKeepsUnrelatedMetrics(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	extra := prometheus.NewGauge(prometheus.GaugeOpts{Name: "dev_env_test_unrelated", Help: "Unrelated exporter series."})
	reg.MustRegister(extra)
	extra.Set(7)
	c := newCollector(&stubReader{listErr: errors.New("list failed"), getErr: errors.New("get failed")}, true, metricNow)
	if err := reg.Register(c); err != nil {
		t.Fatal(err)
	}
	lines := seriesReg(t, reg)
	assertSeries(t, standbyOnly(lines), invalidLines(1))
	if !containsLine(lines, "dev_env_test_unrelated{} 7") {
		t.Fatalf("unrelated series missing: %v", lines)
	}
}

func TestCollectorBudgetWindow(t *testing.T) {
	inside := metricNow.Add(-time.Minute)
	before := metricNow.Add(-DefaultWindow + time.Second)
	exact := metricNow.Add(-DefaultWindow)
	after := metricNow.Add(-DefaultWindow - time.Second)
	future := metricNow.Add(time.Hour)
	cases := []struct {
		name   string
		object client.Object
		want   int
	}{
		{"not found", nil, 0},
		{"empty history", breakerMap(""), 0},
		{"empty array", breakerMap("[]"), 0},
		{"two inside the window", breakerMap(creationJSON(t, inside, inside)), 0},
		{"third slot exhausts the budget", breakerMap(creationJSON(t, inside, inside, inside)), 1},
		{"before 30 minutes counts", breakerMap(creationJSON(t, inside, inside, before)), 1},
		{"exactly 30 minutes is outside", breakerMap(creationJSON(t, inside, inside, exact)), 0},
		{"after 30 minutes is outside", breakerMap(creationJSON(t, inside, inside, after)), 0},
		{"future timestamps stay inside", breakerMap(creationJSON(t, inside, inside, future)), 1},
		{"three future timestamps exhaust the budget", breakerMap(creationJSON(t, future, future, future)), 1},
		{"other configmap is not the breaker", &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: stateNS, Name: "dev-env-templates"},
			Data:       map[string]string{stateKey: creationJSON(t, inside, inside, inside)},
		}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var objs []client.Object
			if tc.object != nil {
				objs = append(objs, tc.object)
			}
			c := newCollector(metricClient(t, objs...), true, metricNow)
			assertSeries(t, series(t, c), validLines(nil, nil, 1, tc.want))
		})
	}
}

func TestCollectorsAgree(t *testing.T) {
	session := registeredSession("in", metricNow.Add(-time.Minute))
	raw := creationJSON(t, metricNow.Add(-time.Minute), metricNow.Add(-2*time.Minute), metricNow.Add(-3*time.Minute))
	cl := metricClient(t, session, breakerMap(raw))
	a := newCollector(cl, true, metricNow)
	b := newCollector(cl, true, metricNow)
	if got, want := series(t, a), series(t, b); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("collectors disagree\n%s\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	assertSeries(t, series(t, a), validLines(map[string]int{stateRegistered: 1}, map[string]int64{stateRegistered: metricNow.Add(-time.Minute).Unix()}, 1, 1))
}

func TestCollectorDoesNotWriteOrEnsure(t *testing.T) {
	raw := creationJSON(t, metricNow.Add(-time.Minute), metricNow.Add(-2*time.Minute))
	session := registeredSession("in", metricNow.Add(-time.Hour))
	inner := metricClient(t, session, breakerMap(raw))
	guard := &writeCount{Client: inner}
	c := newCollector(guard, true, metricNow)
	assertSeries(t, series(t, c), validLines(map[string]int{stateRegistered: 1}, map[string]int64{stateRegistered: metricNow.Add(-time.Hour).Unix()}, 1, 0))
	if guard.writes != 0 {
		t.Fatalf("collector wrote %d times", guard.writes)
	}
	var cm corev1.ConfigMap
	if err := inner.Get(context.Background(), types.NamespacedName{Namespace: stateNS, Name: DefaultStateName}, &cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data[stateKey] != raw {
		t.Fatalf("breaker data changed to %s", cm.Data[stateKey])
	}
	var list v1alpha1.AgentSessionList
	if err := inner.List(context.Background(), &list, client.InNamespace(sessNS)); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Status.RemoteControl.URL != "https://claude.ai/code/secret" {
		t.Fatalf("sessions changed: %+v", list.Items)
	}

	empty := &writeCount{Client: metricClient(t)}
	idle := newCollector(empty, true, metricNow)
	assertSeries(t, series(t, idle), validLines(nil, nil, 1, 0))
	if empty.writes != 0 {
		t.Fatalf("empty scrape wrote %d times", empty.writes)
	}
	var created v1alpha1.AgentSessionList
	if err := empty.List(context.Background(), &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Items) != 0 {
		t.Fatalf("collector created sessions: %d", len(created.Items))
	}
}

func TestCollectorReadRequestsAndTimeout(t *testing.T) {
	stub := &stubReader{listErr: errors.New("stop"), getErr: apierrors.NewNotFound(corev1.Resource("configmaps"), DefaultStateName)}
	c := newCollector(stub, true, metricNow)
	assertSeries(t, standbyOnly(series(t, c)), invalidLines(1))
	if stub.listCalls != 1 || stub.getCalls != 1 {
		t.Fatalf("calls list=%d get=%d", stub.listCalls, stub.getCalls)
	}
	if stub.listNS != sessNS || stub.getKey != (types.NamespacedName{Namespace: stateNS, Name: DefaultStateName}) {
		t.Fatalf("list namespace %s get %#v", stub.listNS, stub.getKey)
	}
	if stub.selector == nil || !stub.selector.Matches(labels.Set{v1alpha1.LabelStandby: "true"}) || stub.selector.Matches(labels.Set{v1alpha1.LabelStandby: "false"}) {
		t.Fatalf("selector %v", stub.selector)
	}
	if stub.listTimeout < 9*time.Second || stub.listTimeout > 10*time.Second {
		t.Fatalf("list deadline %s", stub.listTimeout)
	}

	blocking := &stubReader{block: time.Minute}
	slow := newCollector(blocking, true, metricNow)
	slow.timeout = 30 * time.Millisecond
	start := time.Now()
	assertSeries(t, standbyOnly(series(t, slow)), invalidLines(1))
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("collect took %s", elapsed)
	}
	if blocking.listCalls != 1 || blocking.getCalls != 1 {
		t.Fatalf("timeout calls list=%d get=%d", blocking.listCalls, blocking.getCalls)
	}
}

func TestRegisterCollectorDuplicateIsSafe(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	extra := prometheus.NewGauge(prometheus.GaugeOpts{Name: "dev_env_test_unrelated", Help: "Unrelated exporter series."})
	reg.MustRegister(extra)
	extra.Set(7)
	first := &Collector{Enabled: false}
	if err := RegisterCollector(reg, first); err != nil {
		t.Fatal(err)
	}
	reader := &stubReader{listErr: errors.New("duplicate collector")}
	err := RegisterCollector(reg, &Collector{Enabled: true, Reader: reader, Now: func() time.Time { return metricNow }})
	var already prometheus.AlreadyRegisteredError
	if !errors.As(err, &already) {
		t.Fatalf("duplicate register: %v", err)
	}
	lines := seriesReg(t, reg)
	assertSeries(t, standbyOnly(lines), validLines(nil, nil, 0, 0))
	if !containsLine(lines, "dev_env_test_unrelated{} 7") {
		t.Fatalf("unrelated series missing: %v", lines)
	}
	if reader.listCalls != 0 || reader.getCalls != 0 {
		t.Fatal("unregistered collector was scraped")
	}
	if err := RegisterCollector(nil, first); err == nil {
		t.Fatal("nil registerer succeeded")
	}
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

type stubReader struct {
	listErr     error
	getErr      error
	block       time.Duration
	listCalls   int
	getCalls    int
	listNS      string
	selector    labels.Selector
	getKey      types.NamespacedName
	listTimeout time.Duration
}

func (s *stubReader) List(ctx context.Context, _ client.ObjectList, opts ...client.ListOption) error {
	s.listCalls++
	var lo client.ListOptions
	lo.ApplyOptions(opts)
	s.listNS = lo.Namespace
	s.selector = lo.LabelSelector
	if dl, ok := ctx.Deadline(); ok {
		s.listTimeout = time.Until(dl)
	}
	if s.block > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.block):
			return errors.New("list exceeded the test block")
		}
	}
	if s.listErr != nil {
		return s.listErr
	}
	return nil
}

func (s *stubReader) Get(ctx context.Context, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	s.getCalls++
	s.getKey = key
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if s.getErr != nil {
		return s.getErr
	}
	return nil
}

type writeCount struct {
	client.Client
	writes int
}

func (w *writeCount) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	w.writes++
	return w.Client.Create(ctx, obj, opts...)
}

func (w *writeCount) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	w.writes++
	return w.Client.Update(ctx, obj, opts...)
}

func (w *writeCount) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	w.writes++
	return w.Client.Patch(ctx, obj, patch, opts...)
}

func (w *writeCount) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	w.writes++
	return w.Client.Delete(ctx, obj, opts...)
}

func (w *writeCount) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	w.writes++
	return w.Client.DeleteAllOf(ctx, obj, opts...)
}

func (w *writeCount) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	w.writes++
	return w.Client.Apply(ctx, obj, opts...)
}

func (w *writeCount) Status() client.SubResourceWriter {
	return statusCount{w: w, inner: w.Client.Status()}
}

type statusCount struct {
	w     *writeCount
	inner client.SubResourceWriter
}

func (s statusCount) Create(ctx context.Context, obj client.Object, sub client.Object, opts ...client.SubResourceCreateOption) error {
	s.w.writes++
	return s.inner.Create(ctx, obj, sub, opts...)
}

func (s statusCount) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	s.w.writes++
	return s.inner.Update(ctx, obj, opts...)
}

func (s statusCount) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	s.w.writes++
	return s.inner.Patch(ctx, obj, patch, opts...)
}

func (s statusCount) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	s.w.writes++
	return s.inner.Apply(ctx, obj, opts...)
}
