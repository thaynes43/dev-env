package allowancecollector

import (
	"bytes"
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"encoding/json"
	"errors"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func evictedFixture(t *testing.T, host Host) []byte {
	t.Helper()
	yes := true
	pod := corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: host.Name, UID: types.UID(host.PodUID), ResourceVersion: "42", OwnerReferences: []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentSession", Name: host.Name, UID: types.UID(host.SessionUID), Controller: &yes}}}, Spec: corev1.PodSpec{ServiceAccountName: host.ServiceAccount}, Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"}}
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func handoffFixture(t *testing.T) HandoffInput {
	t.Helper()
	now := time.Date(2026, 11, 3, 15, 0, 0, 0, time.UTC)
	old := collectorConfig(t, now)
	oldRaw, _ := json.Marshal(old)
	next := *old
	next.Host.PodUID = "44444444-4444-4444-4444-444444444444"
	next.Host.Generation++
	next.ValidUntil = time.Date(2026, 11, 4, 15, 33, 23, 0, time.UTC).Format(time.RFC3339Nano)
	nextRaw, _ := json.Marshal(next)
	report := []byte{}
	j := journal{Version: 1, Fingerprint: old.fingerprint, HelperSHA: strings.Repeat("c", 64), Cycle: 56, Nonce: "55555555-5555-5555-5555-555555555555", Phase: "idle", StartedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), NotBefore: now.Add(time.Minute).Format(time.RFC3339Nano), ReportSHA: digest(report), Captures: []Capture{}}
	raw, _ := json.Marshal(j)
	return HandoffInput{OldConfig: oldRaw, NewConfig: nextRaw, Trust: old.trust, PublisherUID: hostUID, PublisherSA: "dev-env-workbench", SlotUID: slotUID, ResourceVersion: "7", HomeUID: "66666666-6666-6666-6666-666666666666", KubeconfigPath: "/fixture/kubeconfig", KubeContext: "fixture", Journal: raw, Report: report, EvictedPod: evictedFixture(t, old.Host), ExpectedHelperSHA: j.HelperSHA, ExpectedJournalSHA: digest(raw), ExpectedReportSHA: digest(report), ExpectedCycle: 56, ExpectedNonce: j.Nonce, Now: now}
}
func TestPrepareHandoffPreservesJournalBytes(t *testing.T) {
	in := handoffFixture(t)
	p, err := PrepareHandoff(in)
	if err != nil {
		t.Fatal(err)
	}
	restored := bytes.Replace(p.NewJournal, []byte(p.NewFingerprint), []byte(p.OldFingerprint), 1)
	if !bytes.Equal(restored, in.Journal) || p.ReportSHA != in.ExpectedReportSHA || p.Hash == "" {
		t.Fatal("changed original history")
	}
	again, err := PrepareHandoff(in)
	if err != nil || p.Hash != again.Hash || !bytes.Equal(p.NewJournal, again.NewJournal) {
		t.Fatal("non-deterministic preparation")
	}
}
func TestPrepareHandoffRejectsFences(t *testing.T) {
	for name, edit := range map[string]func(*HandoffInput){
		"unknown helper": func(i *HandoffInput) { i.ExpectedHelperSHA = strings.Repeat("d", 64) }, "missing eviction": func(i *HandoffInput) { i.EvictedPod = nil },
		"slot": func(i *HandoffInput) { i.SlotUID = hostUID }, "rv": func(i *HandoffInput) { i.ResourceVersion = "" },
		"journal": func(i *HandoffInput) { i.Journal = append(i.Journal, ' ') }, "report": func(i *HandoffInput) { i.Report = []byte("x") },
		"cycle": func(i *HandoffInput) { i.ExpectedCycle = 55 }, "nonce": func(i *HandoffInput) { i.ExpectedNonce = hostUID },
		"old host": func(i *HandoffInput) { i.NewConfig = i.OldConfig }, "publisher": func(i *HandoffInput) { i.PublisherUID = podUID },
		"expired": func(i *HandoffInput) { i.Now = i.Now.Add(25 * time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			in := handoffFixture(t)
			edit(&in)
			if _, err := PrepareHandoff(in); err == nil {
				t.Fatal("accepted mismatched fence")
			}
		})
	}
}

func TestHandoffRunnerContinuationAndLostAcknowledgement(t *testing.T) {
	r, remote, now := collectorFixture(t)
	ctx := context.Background()
	if err := r.Step(ctx); err != nil {
		t.Fatal(err)
	}
	key := types.NamespacedName{Namespace: apiserver.AggregateAllowanceNamespace, Name: apiserver.AggregateAllowanceName}
	var cm corev1.ConfigMap
	if err := r.Client.Get(ctx, key, &cm); err != nil {
		t.Fatal(err)
	}
	var held journal
	if err := json.Unmarshal([]byte(cm.Data[StateKey]), &held); err != nil {
		t.Fatal(err)
	}
	held.Cycle = 56
	oldJournal, _ := json.Marshal(held)
	cm.Data[StateKey] = string(oldJournal)
	if err := r.Client.Update(ctx, &cm); err != nil {
		t.Fatal(err)
	}
	if err := r.Client.Get(ctx, key, &cm); err != nil {
		t.Fatal(err)
	}
	report := []byte(cm.Data[apiserver.AggregateAllowanceDataKey])
	oldConfig, _ := json.Marshal(r.Config)
	next := *r.Config
	next.Host.PodUID = "44444444-4444-4444-4444-444444444444"
	next.Host.Generation = 2
	next.ValidUntil = time.Date(2026, 11, 4, 15, 33, 23, 0, time.UTC).Format(time.RFC3339Nano)
	nextRaw, _ := json.Marshal(next)
	nextCfg, err := ParseConfig(nextRaw, r.Config.trust, hostUID, "dev-env-workbench", *now)
	if err != nil {
		t.Fatal(err)
	}
	hostKey := types.NamespacedName{Namespace: Namespace, Name: next.Host.Name}
	var session v1alpha1.AgentSession
	var pod corev1.Pod
	if err = r.Client.Get(ctx, hostKey, &session); err != nil {
		t.Fatal(err)
	}
	session.Generation = 2
	if err = r.Client.Update(ctx, &session); err != nil {
		t.Fatal(err)
	}
	if err = r.Client.Get(ctx, hostKey, &pod); err != nil {
		t.Fatal(err)
	}
	pod.UID = types.UID(next.Host.PodUID)
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "home-" + next.Host.Name}}})
	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "home", MountPath: "/home/dev"})
	if err = r.Client.Update(ctx, &pod); err != nil {
		t.Fatal(err)
	}
	yes := true
	homeUID := "66666666-6666-6666-6666-666666666666"
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: "home-" + next.Host.Name, UID: types.UID(homeUID), OwnerReferences: []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentSession", Name: session.Name, UID: session.UID, Controller: &yes}}}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}
	if err = r.Client.Create(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	in := HandoffInput{OldConfig: oldConfig, NewConfig: nextRaw, Trust: r.Config.trust, PublisherUID: hostUID, PublisherSA: "dev-env-workbench", SlotUID: slotUID, ResourceVersion: cm.ResourceVersion, HomeUID: homeUID, KubeconfigPath: "/fixture/kubeconfig", KubeContext: "fixture", Journal: oldJournal, Report: report, EvictedPod: evictedFixture(t, r.Config.Host), ExpectedHelperSHA: held.HelperSHA, ExpectedJournalSHA: digest(oldJournal), ExpectedReportSHA: digest(report), ExpectedCycle: 56, ExpectedNonce: held.Nonce, Now: *now}
	plan, err := PrepareHandoff(in)
	if err != nil {
		t.Fatal(err)
	}
	calls := len(remote.calls)
	if r.Step(ctx) == nil || len(remote.calls) != calls {
		t.Fatal("old host dispatched")
	}
	newRunner := *r
	newRunner.Config = nextCfg
	if newRunner.Step(ctx) == nil || len(remote.calls) != calls {
		t.Fatal("new host dispatched before handoff")
	}
	backup := t.TempDir()
	if err = os.Chmod(backup, 0700); err != nil {
		t.Fatal(err)
	}
	if err = CheckHost(ctx, r.Client, nextCfg.Host, *now); err != nil {
		t.Fatal("host", err)
	}
	if err = verifyHandoffHost(ctx, r.Client, nextCfg, homeUID, *now); err != nil {
		t.Fatal("home", err)
	}
	if err = checkHandoffBackup(backup, plan.Hash, in, true); err != nil {
		t.Fatal("backup", err)
	}

	// Refusals must happen before any ConfigMap update, even when a caller
	// recomputes a syntactically valid plan for a mismatched live fence.
	for _, mode := range []string{"hash", "home", "resource-version", "expiry", "backup"} {
		t.Run(mode, func(t *testing.T) {
			candidate := in
			hash := plan.Hash
			testClock := func() time.Time { return *now }
			testBackup := backup
			switch mode {
			case "hash":
				hash = strings.Repeat("a", 64)
			case "home":
				candidate.HomeUID = "77777777-7777-7777-7777-777777777777"
			case "resource-version":
				candidate.ResourceVersion = "999999"
			case "expiry":
				testClock = func() time.Time { return time.Date(2026, 11, 4, 15, 33, 23, 0, time.UTC) }
			case "backup":
				testBackup = filepath.Join(backup, "absent")
			}
			if mode == "home" || mode == "resource-version" {
				other, err := PrepareHandoff(candidate)
				if err != nil {
					t.Fatal(err)
				}
				hash = other.Hash
			}
			spy := &countHandoffUpdates{Client: r.Client}
			if applyHandoff(ctx, spy, candidate, hash, testBackup, testClock) == nil || spy.calls != 0 {
				t.Fatal("unsafe attempt or slot mutation")
			}
		})
	}
	lost := &lostHandoffAck{Client: r.Client}
	if err = applyHandoff(ctx, lost, in, plan.Hash, backup, func() time.Time { return *now }); err != nil {
		t.Fatal(err)
	}
	if !lost.sent {
		t.Fatal("lost-ack path was not exercised")
	}
	if err = applyHandoff(ctx, r.Client, in, plan.Hash, backup, func() time.Time { return *now }); err != nil {
		t.Fatal("idempotent readback", err)
	}
	if err = r.Client.Get(ctx, key, &cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data[StateKey] != string(plan.NewJournal) || cm.Data[apiserver.AggregateAllowanceDataKey] != string(report) {
		t.Fatal("handoff altered history")
	}
	// A restarted runner honors the old not-before time and cannot replay.
	restarted := newRunner
	if err = restarted.Step(ctx); err != nil || len(remote.calls) != calls {
		t.Fatal("restart replayed or ignored cadence", err)
	}
	*now = now.Add(10 * time.Minute)
	if err = r.Client.Get(ctx, hostKey, &session); err != nil {
		t.Fatal(err)
	}
	session.Status.Agent.LastHeartbeat = &metav1.Time{Time: *now}
	if err = r.Client.Update(ctx, &session); err != nil {
		t.Fatal(err)
	}
	if err = newRunner.Step(ctx); err != nil {
		t.Fatal("natural cycle", err)
	}
	if len(remote.calls) != calls+1 || remote.calls[len(remote.calls)-1] != "capture" {
		t.Fatal("unexpected acquisition count")
	}
	if err = r.Client.Get(ctx, key, &cm); err != nil {
		t.Fatal(err)
	}
	var after journal
	if err = json.Unmarshal([]byte(cm.Data[StateKey]), &after); err != nil {
		t.Fatal(err)
	}
	if after.Cycle != 57 || after.Nonce == held.Nonce || len(after.Captures) != len(held.Captures)*2 {
		t.Fatal("cycle, nonce or retained captures")
	}
	spy := &countHandoffUpdates{Client: r.Client}
	if applyHandoff(ctx, spy, in, plan.Hash, backup, func() time.Time { return *now }) == nil || spy.calls != 0 {
		t.Fatal("old plan replayed after a new cycle")
	}
	for _, old := range held.Captures {
		found := false
		for _, got := range after.Captures {
			a, _ := json.Marshal(old)
			b, _ := json.Marshal(got)
			if bytes.Equal(a, b) {
				found = true
			}
		}
		if !found {
			t.Fatal("old capture changed")
		}
	}
}

// lostHandoffAck simulates a successful API mutation whose response is lost.
type lostHandoffAck struct {
	client.Client
	sent bool
}

func (c *lostHandoffAck) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if err := c.Client.Update(ctx, obj, opts...); err != nil {
		return err
	}
	c.sent = true
	return errors.New("synthetic lost acknowledgement")
}

func TestPrepareRejectsChangedPolicyAndUncertainJournal(t *testing.T) {
	for name, edit := range map[string]func(*Config){
		"host":            func(c *Config) { c.Host.Name = "other-host" },
		"session":         func(c *Config) { c.Host.SessionUID = slotUID },
		"generation":      func(c *Config) { c.Host.Generation = 1 },
		"pod":             func(c *Config) { c.Host.PodUID = podUID },
		"service account": func(c *Config) { c.Host.ServiceAccount = "other" },
		"image":           func(c *Config) { c.Host.Image = "unqualified" },
		"slot":            func(c *Config) { c.SlotUID = hostUID },
		"cadence":         func(c *Config) { c.CadenceSeconds = 600 },
		"backoff":         func(c *Config) { c.BackoffSeconds = 900 },
		"deadline":        func(c *Config) { c.ValidUntil = "2026-11-04T15:33:24Z" },
	} {
		t.Run(name, func(t *testing.T) {
			in := handoffFixture(t)
			var c Config
			if json.Unmarshal(in.NewConfig, &c) != nil {
				t.Fatal("fixture")
			}
			edit(&c)
			in.NewConfig, _ = json.Marshal(c)
			if _, err := PrepareHandoff(in); err == nil {
				t.Fatal("accepted changed policy")
			}
		})
	}
	for _, phase := range []string{"inflight", "uncertain"} {
		t.Run(phase, func(t *testing.T) {
			in := handoffFixture(t)
			var j journal
			if json.Unmarshal(in.Journal, &j) != nil {
				t.Fatal("fixture")
			}
			j.Phase = phase
			in.Journal, _ = json.Marshal(j)
			in.ExpectedJournalSHA = digest(in.Journal)
			if _, err := PrepareHandoff(in); err == nil {
				t.Fatal("accepted non-idle journal")
			}
		})
	}
}

type countHandoffUpdates struct {
	client.Client
	calls int
}

func (c *countHandoffUpdates) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.calls++
	return c.Client.Update(ctx, obj, opts...)
}

func TestPublicApplyRejectsUnqualifiedEvidence(t *testing.T) {
	spy := &countHandoffUpdates{}
	in := handoffFixture(t)
	in.EvictedPod = nil
	if ApplyHandoff(context.Background(), spy, in, strings.Repeat("a", 64), t.TempDir()) == nil || spy.calls != 0 {
		t.Fatal("unqualified public apply accepted")
	}
}
