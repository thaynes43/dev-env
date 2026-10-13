package controller

// The HOME expiry's two D-45 exceptions (DESIGN-025 11), its gauges
// (section 10) and its relay's answers (6.4), as unit tables.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilexec "k8s.io/client-go/util/exec"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/shelf"
)

// guardSession is a private Session whose expiry record is at phase, resting
// on the terminal record of executor, with hold pod holdUID named.
func guardSession(phase string) (*v1alpha1.AgentSession, *corev1.Pod, *corev1.Pod) {
	f := newTerminalFixture()
	s := f.s
	h := s.Status.PrivateHistory
	at := metav1.NewTime(f.now)
	h.ExecutorTerminal = &v1alpha1.PrivateExecutorTerminalStatus{Version: 1, PodUID: h.LastExecutor.PodUID, Phase: "Failed", ObservedAt: at}
	h.Expiry = &v1alpha1.PrivateHomeExpiryStatus{Version: 1, ExecutorUID: h.LastExecutor.PodUID, HomeUID: h.HomeUID, GraceStart: at, DueAt: at,
		Phase: phase, HoldIntent: "intent-1", HoldPodUID: "hold-1"}
	executor := f.pod
	executor.ResourceVersion = "7"
	hold := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: "hold-1", ResourceVersion: "9", OwnerReferences: []metav1.OwnerReference{ownerRef(s)},
		Labels:      map[string]string{v1alpha1.LabelHold: "true"},
		Annotations: map[string]string{privateHistoryHomeAnnotation: h.HomeUID, homeExpiryExecutorAnnotation: h.LastExecutor.PodUID, homeExpiryHoldIntentAnnotation: "intent-1"}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	return s, executor, hold
}

func TestHomeExpiryPodGuardTable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*v1alpha1.AgentSession, *corev1.Pod)
		allow bool
	}{
		{"the ended executor in Rescuing", func(*v1alpha1.AgentSession, *corev1.Pod) {}, true},
		{"Waiting", func(s *v1alpha1.AgentSession, _ *corev1.Pod) {
			s.Status.PrivateHistory.Expiry.Phase = v1alpha1.HomeExpiryWaiting
		}, false},
		{"Exporting", func(s *v1alpha1.AgentSession, _ *corev1.Pod) {
			s.Status.PrivateHistory.Expiry.Phase = v1alpha1.HomeExpiryExporting
		}, false},
		{"no expiry record", func(s *v1alpha1.AgentSession, _ *corev1.Pod) { s.Status.PrivateHistory.Expiry = nil }, false},
		{"no terminal record", func(s *v1alpha1.AgentSession, _ *corev1.Pod) { s.Status.PrivateHistory.ExecutorTerminal = nil }, false},
		{"another Pod", func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.UID = "replacement" }, false},
		{"not terminal", func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.Status.Phase = corev1.PodRunning }, false},
		{"a hold pod", func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.Labels[v1alpha1.LabelHold] = "true" }, false},
		{"a foreign owner", func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.OwnerReferences[0].UID = "other" }, false},
		{"no resourceVersion", func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.ResourceVersion = "" }, false},
		{"a canary", func(s *v1alpha1.AgentSession, _ *corev1.Pod) {
			s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{Version: 1, ID: "canary-a"}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, executor, _ := guardSession(v1alpha1.HomeExpiryRescuing)
			tc.edit(s, executor)
			if err := expiryPodRemovalAllowed(s, executor); (err == nil) != tc.allow {
				t.Fatalf("allowed %v (%v), want %v", err == nil, err, tc.allow)
			}
			// The guard is the delete's own first check.
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(executor.DeepCopy()).Build()
			err := deleteExpiryExecutor(context.Background(), c, s, executor)
			if tc.allow != (err == nil) {
				t.Fatalf("deleteExpiryExecutor: %v, want allowed %v", err, tc.allow)
			}
		})
	}
	// A Pod changed since the guard judged it is never deleted.
	s, executor, _ := guardSession(v1alpha1.HomeExpiryRescuing)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(executor.DeepCopy()).Build()
	stale := executor.DeepCopy()
	stale.ResourceVersion = "1"
	if err := deleteExpiryExecutor(context.Background(), c, s, stale); err == nil {
		t.Fatal("deleted a Pod by a stale resourceVersion")
	}
}

func TestHomeExpiryHoldGuardTable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		phase string
		why   expiryHoldRelease
		edit  func(*v1alpha1.AgentSession, *corev1.Pod)
		allow bool
	}{
		{"a cancel", v1alpha1.HomeExpiryExporting, holdReleaseCancel, nil, true},
		{"archive off", v1alpha1.HomeExpiryRescuing, holdReleaseArchiveOff, nil, true},
		{"Archived", v1alpha1.HomeExpiryArchived, holdReleaseArchived, nil, true},
		{"Archived claimed at Exporting", v1alpha1.HomeExpiryExporting, holdReleaseArchived, nil, false},
		{"an owner hold", v1alpha1.HomeExpiryRescuing, holdReleaseOwnerHold, func(s *v1alpha1.AgentSession, _ *corev1.Pod) {
			s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
		}, true},
		{"an owner hold claimed with none", v1alpha1.HomeExpiryRescuing, holdReleaseOwnerHold, nil, false},
		{"an owner hold claimed with release-now", v1alpha1.HomeExpiryRescuing, holdReleaseOwnerHold, func(s *v1alpha1.AgentSession, _ *corev1.Pod) {
			s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryReleaseNow}
		}, false},
		{"ended", v1alpha1.HomeExpiryRescuing, holdReleaseEnded, func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.Status.Phase = corev1.PodFailed }, true},
		{"ended claimed while it runs", v1alpha1.HomeExpiryRescuing, holdReleaseEnded, nil, false},
		{"another hold pod", v1alpha1.HomeExpiryArchived, holdReleaseArchived, func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.UID = "hold-2" }, false},
		{"another intent", v1alpha1.HomeExpiryArchived, holdReleaseArchived, func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Annotations[homeExpiryHoldIntentAnnotation] = "intent-2"
		}, false},
		{"another HOME", v1alpha1.HomeExpiryArchived, holdReleaseArchived, func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Annotations[privateHistoryHomeAnnotation] = "home-2"
		}, false},
		{"no hold label", v1alpha1.HomeExpiryArchived, holdReleaseArchived, func(_ *v1alpha1.AgentSession, p *corev1.Pod) { delete(p.Labels, v1alpha1.LabelHold) }, false},
		{"a foreign owner", v1alpha1.HomeExpiryArchived, holdReleaseArchived, func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.OwnerReferences[0].UID = "other" }, false},
		{"no record", v1alpha1.HomeExpiryArchived, holdReleaseCancel, func(s *v1alpha1.AgentSession, _ *corev1.Pod) { s.Status.PrivateHistory.Expiry = nil }, false},
		{"the executor itself", v1alpha1.HomeExpiryArchived, holdReleaseCancel, func(s *v1alpha1.AgentSession, p *corev1.Pod) {
			p.UID = types.UID(s.Status.PrivateHistory.LastExecutor.PodUID)
			delete(p.Labels, v1alpha1.LabelHold)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, hold := guardSession(tc.phase)
			if tc.phase == v1alpha1.HomeExpiryArchived {
				s.Status.PrivateHistory.Expiry.Archive = &v1alpha1.PrivateHomeArchiveStatus{Entry: "v1/_none/x/0123456789abcdef", ContentSHA256: strings.Repeat("ab", 32)}
			}
			if tc.edit != nil {
				tc.edit(s, hold)
			}
			if err := expiryHoldRemovalAllowed(s, hold, tc.why); (err == nil) != tc.allow {
				t.Fatalf("allowed %v (%v), want %v", err == nil, err, tc.allow)
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(hold.DeepCopy()).Build()
			if err := deleteExpiryHold(context.Background(), c, s, hold, tc.why); (err == nil) != tc.allow {
				t.Fatalf("deleteExpiryHold: %v, want allowed %v", err, tc.allow)
			}
		})
	}
	// podRemovalAllowed still keeps every Pod of a private Session: the
	// expiry's exceptions are their own functions.
	s, executor, hold := guardSession(v1alpha1.HomeExpiryArchived)
	for _, p := range []*corev1.Pod{executor, hold} {
		if err := podRemovalAllowed(s, p); !errors.Is(err, errPrivateHistoryRetained) {
			t.Fatalf("podRemovalAllowed let a private Session's Pod go: %v", err)
		}
	}
}

func gaugeValues(t *testing.T, c prometheus.Collector) map[string]float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 64)
	c.Collect(ch)
	close(ch)
	out := map[string]float64{}
	for m := range ch {
		var d dto.Metric
		if err := m.Write(&d); err != nil {
			t.Fatal(err)
		}
		name := m.Desc().String()
		key := name[strings.Index(name, `fqName: "`)+9:]
		key = key[:strings.Index(key, `"`)]
		for _, l := range d.GetLabel() {
			key += "/" + l.GetValue()
		}
		out[key] = d.GetGauge().GetValue()
	}
	return out
}

// Section 10's gauges from the sessions: unproven, held and each phase, the
// refusing count by reason, and the largest overdue of unheld records before
// Archived. Canaries and Expired records are not counted.
func TestHomeExpiryGauges(t *testing.T) {
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	mk := func(name, phase, refusal string, due time.Time, annotations map[string]string, terminal bool) *v1alpha1.AgentSession {
		s, _, _ := guardSession(phase)
		s.Name, s.UID, s.Annotations = name, types.UID(name), annotations
		h := s.Status.PrivateHistory
		h.SessionUID = name
		if phase == "" {
			h.Expiry = nil
		} else {
			h.Expiry.DueAt, h.Expiry.Refusal = metav1.NewTime(due), refusal
		}
		if !terminal {
			h.ExecutorTerminal = nil
		}
		return s
	}
	canary := mk("canary", "", "", now, nil, false)
	canary.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{Version: 1, ID: "canary-a"}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		mk("unproven", "", "", now, nil, false),
		mk("proven-idle", "", "", now, nil, true),
		mk("waiting", v1alpha1.HomeExpiryWaiting, "", now.Add(time.Hour), nil, true),
		mk("overdue", v1alpha1.HomeExpiryRescuing, expiryShelfUnavailable, now.Add(-3*time.Hour), nil, true),
		mk("held", v1alpha1.HomeExpiryWaiting, "", now.Add(-10*time.Hour), map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}, true),
		mk("exporting", v1alpha1.HomeExpiryExporting, protocol.HistoryErrCredentialInTree, now.Add(-time.Hour), nil, true),
		mk("archived", v1alpha1.HomeExpiryArchived, "", now.Add(-100*time.Hour), nil, true),
		canary,
	).Build()
	got := gaugeValues(t, &HomeExpiryCollector{Reader: c, Namespace: "dev-agents", Now: func() time.Time { return now }})
	for key, want := range map[string]float64{
		MetricHomeExpirySessions + "/unproven": 1, MetricHomeExpirySessions + "/waiting": 1, MetricHomeExpirySessions + "/held": 1,
		MetricHomeExpirySessions + "/rescuing": 1, MetricHomeExpirySessions + "/exporting": 1, MetricHomeExpirySessions + "/archived": 1,
		MetricHomeExpirySessions + "/deleting": 0, MetricHomeExpiryRefusing + "/shelf_unavailable": 1,
		MetricHomeExpiryRefusing + "/credential_in_tree": 1, MetricHomeExpiryRefusing + "/unknown": 0, MetricHomeExpiryOverdue: 3 * 3600,
	} {
		if v, ok := got[key]; !ok || v != want {
			t.Errorf("%s = %v (present %v), want %v", key, v, ok, want)
		}
	}
	if len(got) != len(homeExpiryGaugePhases)+len(homeExpiryReasons)+1 {
		t.Errorf("%d series; every phase and reason is reported, at 0 when none: %v", len(got), got)
	}
}

// scriptedExec answers each command from a table, as pods/exec would.
type scriptedExec struct {
	answers map[string]func(stdin io.Reader, stdout, stderr io.Writer) error
}

func (s *scriptedExec) Run(_ context.Context, _, _, _ string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	a := s.answers[cmd[3]]
	if a == nil {
		return errors.New("unexpected " + cmd[3])
	}
	return a(stdin, stdout, stderr)
}

func exitOne(w io.Writer, code string) error {
	data, _ := protocol.CanonicalJSON(protocol.HistoryErrorf(code, "", "refused"))
	_, _ = w.Write(append(data, '\n'))
	return utilexec.CodeExitError{Err: errors.New("command terminated with exit code 1"), Code: 1}
}

// The relay reads agentd's answers: a refusal keeps its code, whichever side
// refused; a failed exec into the hold pod is Unknown, into the shelf
// shelf_unavailable; no shelf is shelf_unavailable; a second export at once,
// or no free slot, is busy; a put that answers exists needs none of the stream.
func TestExecHistoryRelayAnswers(t *testing.T) {
	plan := protocol.HistoryExportPlan{Format: protocol.HistoryExportPlanFormat, Session: "s"}
	planOK := func(_ io.Reader, out, _ io.Writer) error {
		data, _ := protocol.CanonicalJSON(plan)
		_, err := out.Write(append(data, '\n'))
		return err
	}
	streamOK := func(_ io.Reader, out, _ io.Writer) error {
		_, err := out.Write(bytes.Repeat([]byte{0}, 1024))
		return err
	}
	putOK := func(status string) func(io.Reader, io.Writer, io.Writer) error {
		return func(in io.Reader, out, _ io.Writer) error {
			if status == protocol.HistoryPutStored {
				if _, err := io.Copy(io.Discard, in); err != nil {
					return err
				}
			}
			data, _ := protocol.CanonicalJSON(protocol.HistoryPutResult{Status: status, ContentSHA256: strings.Repeat("ab", 32), Entry: "v1/_none/x/abababababababab"})
			_, err := out.Write(append(data, '\n'))
			return err
		}
	}
	content := func(protocol.HistoryExportPlan, string) ([]byte, string, error) {
		return []byte(`{}`), strings.Repeat("ab", 32), nil
	}
	hold := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "dev-agents"}}
	withShelf := fake.NewClientBuilder().WithScheme(scheme).WithObjects(shelfPodObject("dev-agents")).Build()
	noShelf := fake.NewClientBuilder().WithScheme(scheme).Build()
	for _, tc := range []struct {
		name    string
		answers map[string]func(io.Reader, io.Writer, io.Writer) error
		shelf   bool
		want    string
	}{
		{"stored", map[string]func(io.Reader, io.Writer, io.Writer) error{"plan": planOK, "stream": streamOK, "put": putOK(protocol.HistoryPutStored)}, true, ""},
		{"exists, the stream on a closed pipe", map[string]func(io.Reader, io.Writer, io.Writer) error{"plan": planOK,
			"stream": func(_ io.Reader, out, errOut io.Writer) error {
				time.Sleep(10 * time.Millisecond)
				if _, err := out.Write([]byte("tar")); err != nil {
					return exitOne(errOut, protocol.HistoryErrIO)
				}
				return nil
			}, "put": putOK(protocol.HistoryPutExists)}, true, ""},
		{"the plan refuses", map[string]func(io.Reader, io.Writer, io.Writer) error{"plan": func(_ io.Reader, out, _ io.Writer) error {
			return exitOne(out, protocol.HistoryErrCredentialInTree)
		}}, true, protocol.HistoryErrCredentialInTree},
		{"the stream refuses", map[string]func(io.Reader, io.Writer, io.Writer) error{"plan": planOK,
			"stream": func(_ io.Reader, _, errOut io.Writer) error { return exitOne(errOut, protocol.HistoryErrChanged) },
			"put": func(in io.Reader, out, _ io.Writer) error {
				_, _ = io.Copy(io.Discard, in)
				return exitOne(out, protocol.HistoryErrInvalid)
			}}, true, protocol.HistoryErrChanged},
		{"put refuses", map[string]func(io.Reader, io.Writer, io.Writer) error{"plan": planOK, "stream": streamOK,
			"put": func(in io.Reader, out, _ io.Writer) error { return exitOne(out, protocol.HistoryErrConflict) }}, true, protocol.HistoryErrConflict},
		{"the hold pod's stream breaks, and put refuses the short tar", map[string]func(io.Reader, io.Writer, io.Writer) error{"plan": planOK,
			"stream": func(io.Reader, io.Writer, io.Writer) error { return errors.New("connection reset by peer") },
			"put": func(in io.Reader, out, _ io.Writer) error {
				_, _ = io.Copy(io.Discard, in)
				return exitOne(out, protocol.HistoryErrInvalid)
			}}, true, "exec"},
		{"the hold pod's exec fails", map[string]func(io.Reader, io.Writer, io.Writer) error{"plan": func(io.Reader, io.Writer, io.Writer) error {
			return errors.New("unable to upgrade connection")
		}}, true, "exec"},
		{"the shelf's exec fails", map[string]func(io.Reader, io.Writer, io.Writer) error{"plan": planOK, "stream": streamOK,
			"put": func(io.Reader, io.Writer, io.Writer) error { return errors.New("unable to upgrade connection") }}, true, "shelf"},
		{"no shelf", map[string]func(io.Reader, io.Writer, io.Writer) error{"plan": planOK}, false, "shelf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := noShelf
			if tc.shelf {
				reader = withShelf
			}
			relay := &ExecHistoryRelay{Exec: &scriptedExec{answers: tc.answers}, Shelf: &shelf.Shelf{Reader: reader, Namespace: "dev-agents"}, Slots: shelf.NewExecSlots(1)}
			_, err := relay.Archive(context.Background(), hold, HistoryExportArgs{Session: "s"}, content)
			var he *protocol.HistoryError
			switch tc.want {
			case "":
				if err != nil {
					t.Fatalf("want success, got %v", err)
				}
			case "exec":
				if !errors.Is(err, errHistoryExec) || historyRefusal(err).reason != expiryUnknown {
					t.Fatalf("want an Unknown exec failure, got %v", err)
				}
			case "shelf":
				if !errors.Is(err, errShelfUnavailable) || historyRefusal(err).reason != expiryShelfUnavailable {
					t.Fatalf("want shelf_unavailable, got %v", err)
				}
			default:
				if !errors.As(err, &he) || he.Code != tc.want {
					t.Fatalf("want %s, got %v", tc.want, err)
				}
			}
		})
	}

	// Busy: a second export while one runs, and no free shelf slot.
	relay := &ExecHistoryRelay{Exec: &scriptedExec{}, Shelf: &shelf.Shelf{Reader: withShelf, Namespace: "dev-agents"}, Slots: shelf.NewExecSlots(1)}
	relay.export.Lock()
	if _, err := relay.Archive(context.Background(), hold, HistoryExportArgs{}, content); !errors.Is(err, errHistoryRelayBusy) {
		t.Fatalf("a second export at once: %v", err)
	}
	relay.export.Unlock()
	release, _ := relay.Slots.TryTake()
	if _, err := relay.Verify(context.Background(), "uid", strings.Repeat("ab", 32)); !errors.Is(err, errHistoryRelayBusy) {
		t.Fatalf("no free slot: %v", err)
	}
	release()
}
