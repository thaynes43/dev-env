package allowancecollector

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver"
)

// slotHandoff is the state in the large continuation test, reusable by name:
// an idle cycle-56 journal held in the slot, and a replacement host ready.
type slotHandoff struct {
	r       *Runner
	remote  *fixtureRemote
	now     *time.Time
	in      HandoffInput
	plan    *PreparedHandoff
	next    *Config
	hostKey types.NamespacedName
	slot    types.NamespacedName
	held    journal
}

func newSlotHandoff(t *testing.T) *slotHandoff {
	t.Helper()
	r, remote, now := collectorFixture(t)
	ctx := context.Background()
	h := &slotHandoff{r: r, remote: remote, now: now, slot: types.NamespacedName{Namespace: apiserver.AggregateAllowanceNamespace, Name: apiserver.AggregateAllowanceName}}
	if err := r.Step(ctx); err != nil {
		t.Fatal(err)
	}
	var cm corev1.ConfigMap
	if r.Client.Get(ctx, h.slot, &cm) != nil || json.Unmarshal([]byte(cm.Data[StateKey]), &h.held) != nil {
		t.Fatal("slot")
	}
	h.held.Cycle = 56
	oldJournal, _ := json.Marshal(h.held)
	cm.Data[StateKey] = string(oldJournal)
	if r.Client.Update(ctx, &cm) != nil || r.Client.Get(ctx, h.slot, &cm) != nil {
		t.Fatal("slot update")
	}
	report := []byte(cm.Data[apiserver.AggregateAllowanceDataKey])
	oldConfig, _ := json.Marshal(r.Config)
	n := *r.Config
	n.Host.PodUID = "44444444-4444-4444-4444-444444444444"
	n.Host.Generation = 2
	n.ValidUntil = time.Date(2026, 11, 4, 15, 33, 23, 0, time.UTC).Format(time.RFC3339Nano)
	nextRaw, _ := json.Marshal(n)
	var err error
	if h.next, err = ParseConfig(nextRaw, r.Config.trust, hostUID, "dev-env-workbench", *now); err != nil {
		t.Fatal(err)
	}
	h.hostKey = types.NamespacedName{Namespace: Namespace, Name: n.Host.Name}
	var session v1alpha1.AgentSession
	var pod corev1.Pod
	if r.Client.Get(ctx, h.hostKey, &session) != nil || r.Client.Get(ctx, h.hostKey, &pod) != nil {
		t.Fatal("host")
	}
	session.Generation = 2
	if r.Client.Update(ctx, &session) != nil {
		t.Fatal("session")
	}
	pod.UID = types.UID(n.Host.PodUID)
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "home-" + n.Host.Name}}})
	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "home", MountPath: "/home/dev"})
	if r.Client.Update(ctx, &pod) != nil {
		t.Fatal("pod")
	}
	yes := true
	homeUID := "66666666-6666-6666-6666-666666666666"
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: "home-" + n.Host.Name, UID: types.UID(homeUID), OwnerReferences: []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentSession", Name: session.Name, UID: session.UID, Controller: &yes}}}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}
	if r.Client.Create(ctx, pvc) != nil {
		t.Fatal("pvc")
	}
	h.in = HandoffInput{OldConfig: oldConfig, NewConfig: nextRaw, Trust: r.Config.trust, PublisherUID: hostUID, PublisherSA: "dev-env-workbench", SlotUID: slotUID, ResourceVersion: cm.ResourceVersion, HomeUID: homeUID, KubeconfigPath: OperatorKubeconfigPath, KubeContext: OperatorKubeContext, Journal: oldJournal, Report: report, EvictedPod: evictedFixture(t, r.Config.Host), ExpectedHelperSHA: h.held.HelperSHA, ExpectedJournalSHA: digest(oldJournal), ExpectedReportSHA: digest(report), ExpectedCycle: 56, ExpectedNonce: h.held.Nonce, Now: *now}
	if h.plan, err = PrepareHandoff(h.in); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *slotHandoff) slotNow(t *testing.T) corev1.ConfigMap {
	t.Helper()
	var cm corev1.ConfigMap
	if h.r.Client.Get(context.Background(), h.slot, &cm) != nil {
		t.Fatal("read slot")
	}
	return cm
}

func (h *slotHandoff) handoffRunner() *HandoffRunner {
	var snap HandoffSnapshot
	snap.OldConfig, snap.NewConfig, snap.SlotUID, snap.ResourceVersion, snap.HomeUID = h.in.OldConfig, h.in.NewConfig, h.in.SlotUID, h.in.ResourceVersion, h.in.HomeUID
	snap.Journal, snap.Report, snap.EvictedPod = h.in.Journal, h.in.Report, h.in.EvictedPod
	snap.HelperSHA, snap.JournalSHA, snap.ReportSHA, snap.Cycle, snap.Nonce = h.in.ExpectedHelperSHA, h.in.ExpectedJournalSHA, h.in.ExpectedReportSHA, h.in.ExpectedCycle, h.in.ExpectedNonce
	return &HandoffRunner{Approved: h.plan.Hash, Snapshot: snap, Running: h.next, Trust: h.r.Config.trust, PublisherUID: hostUID, PublisherSA: "dev-env-workbench", Client: h.r.Client, Now: func() time.Time { return *h.now }}
}

func TestInSlotHandoffWritesFullBackupWithJournalInOneUpdate(t *testing.T) {
	h := newSlotHandoff(t)
	ctx := context.Background()
	calls := len(h.remote.calls)
	spy := &countHandoffUpdates{Client: h.r.Client}
	run := h.handoffRunner()
	run.Client = spy
	if err := run.Attempt(ctx); err != nil || spy.calls != 1 {
		t.Fatal("apply", err, spy.calls)
	}
	cm := h.slotNow(t)
	want, _ := handoffBackupBytes(h.in)
	if cm.Data[StateKey] != string(h.plan.NewJournal) || cm.Data[apiserver.AggregateAllowanceDataKey] != string(h.in.Report) || cm.Data[HandoffBackupKey(h.plan.Hash)] != string(want) || cm.Data["unrelated"] != "preserved" {
		t.Fatal("slot content")
	}
	var backup struct {
		Schema                     string
		OldConfig, Journal, Report []byte
	}
	if json.Unmarshal([]byte(cm.Data[HandoffBackupKey(h.plan.Hash)]), &backup) != nil || string(backup.Journal) != string(h.in.Journal) || string(backup.Report) != string(h.in.Report) || string(backup.OldConfig) != string(h.in.OldConfig) {
		t.Fatal("backup is not the exact pre-image")
	}
	// A second attempt (operator restart) is acknowledged without writing.
	if err := run.Attempt(ctx); err != nil || spy.calls != 1 {
		t.Fatal("restart rewrote", err, spy.calls)
	}
	if len(h.remote.calls) != calls {
		t.Fatal("provider IO during handoff")
	}
}

func TestInSlotHandoffAcknowledgesAfterCollectorAdvances(t *testing.T) {
	h := newSlotHandoff(t)
	ctx := context.Background()
	run := h.handoffRunner()
	if err := run.Attempt(ctx); err != nil {
		t.Fatal(err)
	}
	*h.now = h.now.Add(10 * time.Minute)
	var session v1alpha1.AgentSession
	if h.r.Client.Get(ctx, h.hostKey, &session) != nil {
		t.Fatal("session")
	}
	session.Status.Agent.LastHeartbeat = &metav1.Time{Time: *h.now}
	if h.r.Client.Update(ctx, &session) != nil {
		t.Fatal("heartbeat")
	}
	collector := *h.r
	collector.Config = h.next
	if err := collector.Step(ctx); err != nil {
		t.Fatal("natural cycle", err)
	}
	before := h.slotNow(t)
	spy := &countHandoffUpdates{Client: h.r.Client}
	run.Client = spy
	if err := run.Attempt(ctx); err != nil || spy.calls != 0 {
		t.Fatal("advanced slot not acknowledged read-only", err, spy.calls)
	}
	if after := h.slotNow(t); after.ResourceVersion != before.ResourceVersion {
		t.Fatal("slot changed")
	}
}

func TestInSlotHandoffLostAcknowledgement(t *testing.T) {
	h := newSlotHandoff(t)
	lost := &lostHandoffAck{Client: h.r.Client}
	run := h.handoffRunner()
	run.Client = lost
	if err := run.Attempt(context.Background()); err != nil || !lost.sent {
		t.Fatal("lost ack", err)
	}
	if h.slotNow(t).Data[StateKey] != string(h.plan.NewJournal) {
		t.Fatal("not applied")
	}
}

func TestInSlotHandoffRefusalsWriteNothing(t *testing.T) {
	for name, edit := range map[string]func(*testing.T, *slotHandoff, *HandoffRunner){
		"hash": func(_ *testing.T, _ *slotHandoff, r *HandoffRunner) { r.Approved = strings.Repeat("a", 64) },
		"home": func(_ *testing.T, _ *slotHandoff, r *HandoffRunner) {
			r.Snapshot.HomeUID = "77777777-7777-7777-7777-777777777777"
		},
		"stale resource version": func(t *testing.T, h *slotHandoff, _ *HandoffRunner) {
			cm := h.slotNow(t)
			cm.Data["other"] = "x"
			if h.r.Client.Update(context.Background(), &cm) != nil {
				t.Fatal("bump")
			}
		},
		"running config differs": func(_ *testing.T, h *slotHandoff, r *HandoffRunner) {
			c := *h.next
			c.until = c.until.Add(-time.Hour)
			r.Running = &c
		},
		"running config fingerprint differs": func(_ *testing.T, h *slotHandoff, r *HandoffRunner) {
			c := *h.next
			c.fingerprint = strings.Repeat("e", 64)
			r.Running = &c
		},
		"expired": func(_ *testing.T, h *slotHandoff, r *HandoffRunner) {
			r.Now = func() time.Time { return time.Date(2026, 11, 4, 15, 33, 23, 0, time.UTC) }
		},
		"earlier backup entry": func(t *testing.T, h *slotHandoff, _ *HandoffRunner) {
			cm := h.slotNow(t)
			cm.Data[HandoffBackupKey(strings.Repeat("b", 64))] = "{}"
			if h.r.Client.Update(context.Background(), &cm) != nil {
				t.Fatal("seed")
			}
			// The seeded write moves the resource version; re-prepare against it.
			h.in.ResourceVersion = h.slotNow(t).ResourceVersion
			var err error
			if h.plan, err = PrepareHandoff(h.in); err != nil {
				t.Fatal(err)
			}
		},
		"oversize slot": func(t *testing.T, h *slotHandoff, _ *HandoffRunner) {
			cm := h.slotNow(t)
			cm.Data["filler"] = strings.Repeat("x", handoffSlotBackupMax)
			if h.r.Client.Update(context.Background(), &cm) != nil {
				t.Fatal("seed")
			}
			h.in.ResourceVersion = h.slotNow(t).ResourceVersion
			var err error
			if h.plan, err = PrepareHandoff(h.in); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newSlotHandoff(t)
			run := h.handoffRunner()
			edit(t, h, run)
			// Rebuild the runner's snapshot and hash after any re-preparation.
			if name == "earlier backup entry" || name == "oversize slot" {
				run = h.handoffRunner()
			}
			before := h.slotNow(t)
			spy := &countHandoffUpdates{Client: h.r.Client}
			run.Client = spy
			if run.Attempt(context.Background()) == nil || spy.calls != 0 {
				t.Fatal("unsafe attempt")
			}
			after := h.slotNow(t)
			if after.ResourceVersion != before.ResourceVersion || after.Data[StateKey] != before.Data[StateKey] {
				t.Fatal("slot mutated by refusal")
			}
		})
	}
}

// A new-config collector must not dispatch to the provider until the handoff
// has landed, and the old-config collector must not either.
func TestMismatchedConfigsMakeNoProviderCalls(t *testing.T) {
	h := newSlotHandoff(t)
	ctx := context.Background()
	calls := len(h.remote.calls)
	newer := *h.r
	newer.Config = h.next
	for i := 0; i < 3; i++ {
		if newer.Step(ctx) == nil || h.r.Step(ctx) == nil {
			t.Fatal("collector stepped against the wrong journal or host")
		}
	}
	run := h.handoffRunner()
	run.Approved = strings.Repeat("a", 64)
	_ = run.Attempt(ctx)
	if len(h.remote.calls) != calls {
		t.Fatal("provider call under mismatched configuration")
	}
}

func TestHandoffRunnerStartStopsAtDeadlineAndContext(t *testing.T) {
	h := newSlotHandoff(t)
	run := h.handoffRunner()
	run.Approved = strings.Repeat("a", 64)
	run.Retry = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	refusals := 0
	run.Logf = func(string) { refusals++ }
	if run.Start(ctx) != nil || refusals == 0 {
		t.Fatal("did not retry and stop")
	}
	done := h.handoffRunner()
	done.Now = func() time.Time { return time.Date(2026, 11, 4, 16, 0, 0, 0, time.UTC) }
	if done.Start(context.Background()) != nil {
		t.Fatal("ran past the window")
	}
}

func TestDecodeHandoffRequestExactKeys(t *testing.T) {
	h := newSlotHandoff(t)
	run := h.handoffRunner()
	snap, _ := json.Marshal(run.Snapshot)
	good, _ := json.Marshal(map[string]any{"approvedHash": h.plan.Hash, "snapshot": json.RawMessage(snap)})
	if _, got, err := DecodeHandoffRequest(good); err != nil || got.SlotUID != slotUID {
		t.Fatal("good request", err)
	}
	for name, raw := range map[string]string{
		"empty":         "",
		"extra key":     `{"approvedHash":"` + h.plan.Hash + `","snapshot":` + string(snap) + `,"x":1}`,
		"short hash":    `{"approvedHash":"abc","snapshot":` + string(snap) + `}`,
		"bad snapshot":  `{"approvedHash":"` + h.plan.Hash + `","snapshot":{}}`,
		"trailing data": string(good) + ` {}`,
	} {
		if _, _, err := DecodeHandoffRequest([]byte(raw)); err == nil {
			t.Fatal("accepted", name)
		}
	}
}
