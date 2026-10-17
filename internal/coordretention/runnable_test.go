package coordretention

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	devv1 "github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/shelf"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

func TestProbeIdentityAndGraceBoundary(t *testing.T) {
	if _, err := coordinationProbe.RecordDir(); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if graceOver(start.Add(DefaultGrace-time.Minute), start, DefaultGrace) {
		t.Fatal("grace was over a minute early")
	}
	if !graceOver(start.Add(DefaultGrace), start, DefaultGrace) {
		t.Fatal("grace was not over at 720h")
	}
}

func TestCanonicalRecordRefusesOversize(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"text": strings.Repeat("a", protocol.MaxCoordArchiveObjectBytes)}}}
	if _, _, err := canonicalBytes(u); err == nil || !strings.Contains(err.Error(), "archive_invalid") {
		t.Fatalf("oversize = %v", err)
	}
}

func TestDisabledRunnableAndShortGraceDoNoPass(t *testing.T) {
	arch := newMemArchive()
	runnable := &Runnable{Enabled: false, Archive: arch, Client: &rigClient{}, Now: func() time.Time { return time.Now().UTC() }}
	if err := runnable.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := runnable.Pass(context.Background()); got != "" || arch.lookups != 0 {
		t.Fatalf("disabled pass %q lookups %d", got, arch.lookups)
	}
	runnable.Enabled = true
	runnable.Grace = time.Hour
	if err := runnable.Start(context.Background()); err == nil {
		t.Fatal("grace under 720h started")
	}
	if got := runnable.Pass(context.Background()); got != resultFailed || arch.lookups != 0 {
		t.Fatalf("short grace pass %q lookups %d", got, arch.lookups)
	}
}

func TestRetentionScheduleWaitsThenPasses(t *testing.T) {
	now := time.Date(2026, 10, 17, 18, 0, 0, 0, time.UTC)
	reg := prometheus.NewRegistry()
	waits := make(chan time.Duration, 1)
	next := make(chan chan time.Time, 1)
	pods := &staticPods{}
	runnable := &Runnable{
		Enabled: true, Grace: DefaultGrace, Registerer: reg,
		Now: func() time.Time { return now },
		After: func(d time.Duration) <-chan time.Time {
			ch := make(chan time.Time, 1)
			waits <- d
			next <- ch
			return ch
		},
		Reader: &fixtureReader{}, Namespace: retentionNS,
		Pods: pods, Archive: newMemArchive(), Slots: shelf.NewExecSlots(2),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runnable.Start(ctx) }()
	if got := <-waits; got != 10*time.Minute {
		t.Fatalf("first wait %s", got)
	}
	for _, result := range passResults {
		if got := sampleValue(runnable.metrics.Passes.WithLabelValues(result)); got != 0 {
			t.Fatalf("result %s = %v before the first pass", result, got)
		}
	}
	(<-next) <- now
	if got := <-waits; got != 6*time.Hour {
		t.Fatalf("interval %s", got)
	}
	<-next
	if pods.deadline < 20*time.Minute || pods.deadline > 30*time.Minute {
		t.Fatalf("pass deadline %s", pods.deadline)
	}
	if got := sampleValue(runnable.metrics.Passes.WithLabelValues(resultComplete)); got != 1 {
		t.Fatalf("complete passes = %v", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if families, err := reg.Gather(); err != nil || len(families) != 0 {
		t.Fatalf("leader metrics remained after start returned: %d %v", len(families), err)
	}
}

func TestShelfUnavailableDoesNotDelete(t *testing.T) {
	for _, tc := range []struct {
		name string
		pods *staticPods
		arch *memArchive
	}{
		{name: "no pod", pods: &staticPods{err: shelf.ErrNoShelf}, arch: newMemArchive()},
		{name: "no archive", pods: &staticPods{name: "shelf"}, arch: &memArchive{err: protocol.HistoryErrorf(protocol.HistoryErrNoArchive, "", "coordination archive is unavailable")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := &passRig{pods: tc.pods, archive: tc.arch}
			runnable := rig.build(t, retentionClaim(t, "issue:301", "55555555-5555-4555-8555-555555555555"))
			if got := runnable.Pass(context.Background()); got != resultShelfUnavailable || rig.realDeletes != 0 {
				t.Fatalf("result %s deletes %d", got, rig.realDeletes)
			}
		})
	}
}

func TestRetentionPassKeepsUnsafeRecordsAndDeletesVerified(t *testing.T) {
	uid := "55555555-5555-4555-8555-555555555555"
	t.Run("unknown", func(t *testing.T) {
		rig := releasedRig()
		rig.clearReconciled = true
		runnable := rig.build(t, retentionClaim(t, "issue:301", uid))
		if got := runnable.Pass(context.Background()); got != resultComplete || rig.realDeletes != 0 || deletedCount(runnable) != 0 {
			t.Fatalf("result %s real %d deleted %v", got, rig.realDeletes, deletedCount(runnable))
		}
	})
	t.Run("owner present", func(t *testing.T) {
		rig := releasedRig()
		rig.session = &devv1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sender.Namespace, Name: sender.Name, UID: types.UID(sender.UID)}}
		rig.sessionMisses = 1
		runnable := rig.build(t, retentionClaim(t, "issue:301", uid))
		if got := runnable.Pass(context.Background()); got != resultComplete || rig.realDeletes != 0 || deletedCount(runnable) != 0 {
			t.Fatalf("result %s real %d deleted %v marks %#v", got, rig.realDeletes, deletedCount(runnable), rig.archive.marks)
		}
		if sampleValue(runnable.metrics.Refused.WithLabelValues(string(TaskClaim), reasonOwnerPresent)) != 1 {
			t.Fatal("owner present was not refused")
		}
		if len(rig.archive.marks) != 2 || rig.archive.marks[0].Event.State != "hold" || rig.archive.marks[1].Event.State != "present" {
			t.Fatalf("hold then present = %#v", rig.archive.marks)
		}
	})
	t.Run("grace not over", func(t *testing.T) {
		rig := releasedRig()
		claim := retentionClaim(t, "issue:301", uid)
		runnable := rig.build(t, claim)
		start := rig.now
		rig.archive.grace[claimIdentity(claim)] = &start
		if got := runnable.Pass(context.Background()); got != resultComplete || rig.realDeletes != 0 || deletedCount(runnable) != 0 {
			t.Fatalf("result %s real %d deleted %v", got, rig.realDeletes, deletedCount(runnable))
		}
		if sampleValue(runnable.metrics.Pending.WithLabelValues(string(TaskClaim))) != 1 {
			t.Fatal("pending gauge was not set")
		}
	})
	t.Run("precondition conflict", func(t *testing.T) {
		rig := releasedRig()
		claim := retentionClaim(t, "issue:301", uid)
		runnable := rig.build(t, claim)
		rig.archive.onGet = func() { rig.conflictRV = "11" }
		if got := runnable.Pass(context.Background()); got != resultComplete || rig.realDeletes != 0 || deletedCount(runnable) != 0 {
			t.Fatalf("result %s real %d deleted %v", got, rig.realDeletes, deletedCount(runnable))
		}
		if sampleValue(runnable.metrics.Refused.WithLabelValues(string(TaskClaim), reasonPreconditionFailed)) != 1 {
			t.Fatal("conflict was not refused")
		}
		if err := rig.base.Get(context.Background(), client.ObjectKey{Namespace: claim.Namespace, Name: claim.Name}, &coord.TaskClaim{}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("forbidden", func(t *testing.T) {
		rig := releasedRig()
		claim := retentionClaim(t, "issue:301", uid)
		runnable := rig.build(t, claim)
		rig.forbid = true
		if got := runnable.Pass(context.Background()); got != resultComplete || rig.realDeletes != 0 || deletedCount(runnable) != 0 {
			t.Fatalf("result %s real %d deleted %v", got, rig.realDeletes, deletedCount(runnable))
		}
		if sampleValue(runnable.metrics.Refused.WithLabelValues(string(TaskClaim), reasonForbidden)) != 1 {
			t.Fatal("forbidden was not refused")
		}
		if err := rig.base.Get(context.Background(), client.ObjectKey{Namespace: claim.Namespace, Name: claim.Name}, &coord.TaskClaim{}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("not found counts done", func(t *testing.T) {
		rig := releasedRig()
		claim := retentionClaim(t, "issue:301", uid)
		runnable := rig.build(t, claim)
		rig.archive.onGet = func() { rig.hide[claim.Name] = true }
		if got := runnable.Pass(context.Background()); got != resultComplete || rig.realDeletes != 0 || deletedCount(runnable) != 1 {
			t.Fatalf("result %s real %d deleted %v", got, rig.realDeletes, deletedCount(runnable))
		}
	})
	t.Run("deletes after verify", func(t *testing.T) {
		rig := releasedRig()
		rig.slots = shelf.NewExecSlots(2)
		claim := retentionClaim(t, "issue:301", uid)
		runnable := rig.build(t, claim)
		rig.archive.onCall = func() {
			first, ok1 := rig.slots.TryTake()
			_, ok2 := rig.slots.TryTake()
			if ok1 {
				first()
			}
			if !ok1 || ok2 {
				t.Fatalf("slot hold ok1=%v ok2=%v", ok1, ok2)
			}
		}
		if got := runnable.Pass(context.Background()); got != resultComplete || rig.realDeletes != 1 || deletedCount(runnable) != 1 {
			t.Fatalf("result %s real %d deleted %v order %v", got, rig.realDeletes, deletedCount(runnable), rig.order)
		}
		if !archiveVerifiedBeforeDelete(rig.order) {
			t.Fatalf("order %v", rig.order)
		}
		if err := rig.base.Get(context.Background(), client.ObjectKey{Namespace: claim.Namespace, Name: claim.Name}, &coord.TaskClaim{}); !apierrors.IsNotFound(err) {
			t.Fatalf("record after delete: %v", err)
		}
	})
}

func timePtr(at time.Time) *time.Time {
	at = at.UTC()
	return &at
}

func TestInboxReleaseDeletesTheMessageBeforeTheAck(t *testing.T) {
	message := messageSpec()
	ackSpec := coord.InboxReadAckSpec{
		Project: message.Project, MessageName: inbox.MessageName(message.Sender, message.Key),
		MessageUID: "44444444-4444-4444-8444-444444444444", Recipient: message.Recipient, Actor: actor(),
	}
	if !inbox.ValidMessage(message) || !inbox.ValidAck(ackSpec) {
		t.Fatal("inbox fixture")
	}
	msg := &coord.InboxMessage{
		TypeMeta:   metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "InboxMessage"},
		ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: ackSpec.MessageName, UID: types.UID(ackSpec.MessageUID), ResourceVersion: "30"},
		Spec:       message,
	}
	ack := &coord.InboxReadAck{
		TypeMeta:   metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "InboxReadAck"},
		ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: inbox.AckName(ackSpec.MessageUID, ackSpec.Recipient.UID), UID: types.UID("77777777-7777-4777-8777-777777777777"), ResourceVersion: "40"},
		Spec:       ackSpec,
	}
	toUnstructured := func(v runtime.Object) unstructured.Unstructured {
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(v)
		if err != nil {
			t.Fatal(err)
		}
		return unstructured.Unstructured{Object: raw}
	}
	rig := releasedRig()
	rig.now = time.Date(2026, 10, 17, 17, 0, 0, 0, time.UTC)
	past := rig.now.Add(-DefaultGrace - time.Minute).UTC()
	for _, id := range []protocol.CoordArchiveIdentity{
		{Resource: "inboxmessages", Project: message.Project, Name: msg.Name, UID: string(msg.UID)},
		{Resource: "inboxreadacks", Project: ackSpec.Project, Name: ack.Name, UID: string(ack.UID)},
	} {
		key := lookupKey(id)
		rig.archive.grace[key] = timePtr(past)
		rig.archive.latest[key] = protocol.CoordArchiveEvent{State: "absent", At: past}
		rig.archive.revisions[key] = []protocol.CoordArchiveRevision{{ResourceVersion: "seed", SHA256: strings.Repeat("ab", 32)}}
	}
	rig.archive.note = func(step string) { rig.order = append(rig.order, step) }
	rig.base = fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(msg.DeepCopy(), ack.DeepCopy()).Build()
	reader := &rigReader{Client: rig.base, lists: &fixtureReader{objects: map[string][]unstructured.Unstructured{
		"InboxMessageList": {toUnstructured(msg)},
		"InboxReadAckList": {toUnstructured(ack)},
	}}, rig: rig}
	runnable := &Runnable{
		Enabled: true, Grace: DefaultGrace, Client: &rigClient{Client: rig.base, rig: rig}, Reader: reader,
		Namespace: retentionNS, Reconciled: map[string]bool{"dev-agents": true},
		Kinds: map[Kind]bool{InboxMessage: true, InboxReadAck: true},
		Pods:  &staticPods{name: "dev-env-shelf"}, Archive: rig.archive, Slots: shelf.NewExecSlots(2),
		metrics: newRetentionMetrics(), Now: func() time.Time { return rig.now },
	}
	if got := runnable.Pass(context.Background()); got != resultComplete || rig.realDeletes != 2 {
		t.Fatalf("result %s deletes %d order %v", got, rig.realDeletes, rig.order)
	}
	messageAt, ackAt := -1, -1
	for i, step := range rig.order {
		if step == "delete:InboxMessage" {
			messageAt = i
		}
		if step == "delete:InboxReadAck" {
			ackAt = i
		}
	}
	if messageAt < 0 || ackAt < 0 || messageAt > ackAt {
		t.Fatalf("message must be deleted before its ack, order %v", rig.order)
	}
	if err := rig.base.Get(context.Background(), client.ObjectKey{Namespace: msg.Namespace, Name: msg.Name}, &coord.InboxMessage{}); !apierrors.IsNotFound(err) {
		t.Fatalf("message after delete: %v", err)
	}
	if err := rig.base.Get(context.Background(), client.ObjectKey{Namespace: ack.Namespace, Name: ack.Name}, &coord.InboxReadAck{}); !apierrors.IsNotFound(err) {
		t.Fatalf("ack after delete: %v", err)
	}
}

func TestFailedPresentResetDoesNotDeleteOnNewLeader(t *testing.T) {
	opened := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := retentionClaim(t, "issue:301", "55555555-5555-4555-8555-555555555555")
	arch := newMemArchive()
	rig := &passRig{archive: arch, now: opened}
	first := rig.build(t, claim)
	if got := first.Pass(context.Background()); got != resultComplete || len(arch.puts) != 1 || arch.puts[0].Event == nil || arch.puts[0].Event.State != "absent" || rig.realDeletes != 0 {
		t.Fatalf("absent mark: result %s puts %d deletes %d", got, len(arch.puts), rig.realDeletes)
	}
	key := claimIdentity(claim)
	if arch.grace[key] == nil || arch.latest[key].State != "absent" || !arch.latest[key].At.Equal(opened) {
		t.Fatalf("grace event = %#v grace %v", arch.latest[key], arch.grace[key])
	}

	rig.now = opened.Add(25 * 24 * time.Hour)
	arch.failPresent = true
	arch.failReset = true
	owner := &devv1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sender.Namespace, Name: sender.Name, UID: types.UID(sender.UID)}}
	if err := rig.base.Create(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	if got := first.Pass(context.Background()); got != resultComplete || rig.realDeletes != 0 || arch.grace[key] == nil || arch.latest[key].State != "hold" {
		t.Fatalf("failed present: result %s deletes %d grace %v latest %s", got, rig.realDeletes, arch.grace[key], arch.latest[key].State)
	}

	// The hold is durable. The day-0 absence is still the grace. A new leader
	// must clear that grace and must not delete.
	next := &passRig{archive: arch, now: opened.Add(31 * 24 * time.Hour)}
	leader := next.build(t, claim)
	if len(leader.blocked) != 0 {
		t.Fatal("new leader kept the old block")
	}
	if got := leader.Pass(context.Background()); got != resultFailed || next.realDeletes != 0 || deletedCount(leader) != 0 || arch.grace[key] == nil {
		t.Fatalf("new leader while reset fails: result %s deletes %d grace %v counted %v", got, next.realDeletes, arch.grace[key], deletedCount(leader))
	}
	arch.failReset = false
	if got := leader.Pass(context.Background()); got != resultComplete || next.realDeletes != 0 || deletedCount(leader) != 0 || arch.grace[key] != nil || arch.latest[key].State != "reset" {
		t.Fatalf("new leader clear: result %s deletes %d grace %v latest %s", got, next.realDeletes, arch.grace[key], arch.latest[key].State)
	}
}

func TestCappedPassResumesAfterLastRecord(t *testing.T) {
	rig := &passRig{archive: newMemArchive()}
	rig.archive.freezeGrace = true
	first := retentionClaim(t, "issue:301", "55555555-5555-4555-8555-555555555555")
	second := retentionClaim(t, "issue:302", "55555555-5555-4555-8555-555555555556")
	runnable := rig.build(t, first, second)
	runnable.maxMarks = 1
	if got := runnable.Pass(context.Background()); got != resultCapped || len(rig.archive.puts) != 1 {
		t.Fatalf("first pass %s puts %d", got, len(rig.archive.puts))
	}
	marked := rig.archive.puts[0].Manifest.Name
	if got := runnable.Pass(context.Background()); got != resultCapped || len(rig.archive.puts) != 2 {
		t.Fatalf("second pass %s puts %d", got, len(rig.archive.puts))
	}
	if rig.archive.puts[1].Manifest.Name == marked {
		t.Fatal("second pass repeated the capped record")
	}
}

func releasedRig() *passRig {
	arch := newMemArchive()
	return &passRig{archive: arch, released: true}
}

func retentionClaim(t *testing.T, task, uid string) *coord.TaskClaim {
	t.Helper()
	spec := claimSpec()
	spec.Identity.TaskKey = task
	if !taskclaim.ValidSpec(spec) {
		t.Fatal("claim spec")
	}
	return &coord.TaskClaim{
		TypeMeta:   metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "TaskClaim"},
		ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: taskclaim.Name(spec.Identity), UID: types.UID(uid), ResourceVersion: "10"},
		Spec:       spec,
	}
}

func deletedCount(r *Runnable) float64 {
	return sampleValue(r.metrics.Deleted.WithLabelValues(string(TaskClaim)))
}

func sampleValue(c prometheus.Collector) float64 {
	ch := make(chan prometheus.Metric, 1)
	c.Collect(ch)
	var metric dto.Metric
	if err := (<-ch).Write(&metric); err != nil {
		panic(err)
	}
	if metric.GetCounter() != nil {
		return metric.GetCounter().GetValue()
	}
	if metric.GetGauge() != nil {
		return metric.GetGauge().GetValue()
	}
	return 0
}

func claimIdentity(claim *coord.TaskClaim) string {
	return lookupKey(protocol.CoordArchiveIdentity{Resource: "taskclaims", Project: claim.Spec.Identity.Project, Name: claim.Name, UID: string(claim.UID)})
}

func deleteLabel(obj client.Object) string {
	if u, ok := obj.(*unstructured.Unstructured); ok && u.GetKind() != "" {
		return "delete:" + u.GetKind()
	}
	return "delete"
}

func archiveVerifiedBeforeDelete(order []string) bool {
	sawGet := false
	for _, step := range order {
		if step == "get" {
			sawGet = true
		}
		if step == "delete" || strings.HasPrefix(step, "delete:") {
			return sawGet
		}
	}
	return false
}

type staticPods struct {
	name     string
	err      error
	deadline time.Duration
}

func (s *staticPods) Pod(ctx context.Context) (string, error) {
	if deadline, ok := ctx.Deadline(); ok {
		s.deadline = time.Until(deadline)
	}
	if s.err != nil {
		return "", s.err
	}
	if s.name == "" {
		return "dev-env-shelf", nil
	}
	return s.name, nil
}

type passRig struct {
	archive         *memArchive
	pods            *staticPods
	session         *devv1.AgentSession
	sessionMisses   int
	sessionGets     int
	clearReconciled bool
	released        bool
	hide            map[string]bool
	conflictRV      string
	forbid          bool
	realDeletes     int
	order           []string
	base            client.Client
	slots           *shelf.ExecSlots
	now             time.Time
}

func (rig *passRig) build(t *testing.T, claims ...*coord.TaskClaim) *Runnable {
	t.Helper()
	if rig.archive == nil {
		rig.archive = newMemArchive()
	}
	if rig.hide == nil {
		rig.hide = map[string]bool{}
	}
	if rig.now.IsZero() {
		rig.now = time.Date(2026, 10, 17, 17, 0, 0, 0, time.UTC)
	}
	if rig.slots == nil {
		rig.slots = shelf.NewExecSlots(2)
	}
	var objects []client.Object
	var listed []unstructured.Unstructured
	for _, claim := range claims {
		objects = append(objects, claim.DeepCopy())
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(claim)
		if err != nil {
			t.Fatal(err)
		}
		listed = append(listed, unstructured.Unstructured{Object: raw})
		if rig.released {
			past := rig.now.Add(-DefaultGrace - time.Minute).UTC()
			key := claimIdentity(claim)
			rig.archive.grace[key] = timePtr(past)
			rig.archive.latest[key] = protocol.CoordArchiveEvent{State: "absent", At: past}
			rig.archive.revisions[key] = []protocol.CoordArchiveRevision{{ResourceVersion: claim.ResourceVersion, SHA256: strings.Repeat("ab", 32)}}
		}
	}
	if rig.session != nil {
		objects = append(objects, rig.session.DeepCopy())
	}
	rig.archive.note = func(step string) { rig.order = append(rig.order, step) }
	rig.base = fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(objects...).Build()
	reader := &rigReader{Client: rig.base, lists: &fixtureReader{objects: map[string][]unstructured.Unstructured{"TaskClaimList": listed}}, rig: rig}
	reconciled := map[string]bool{"dev-agents": true}
	if rig.clearReconciled {
		reconciled = map[string]bool{}
	}
	if rig.pods == nil {
		rig.pods = &staticPods{name: "dev-env-shelf"}
	}
	return &Runnable{
		Enabled: true, Grace: DefaultGrace, Client: &rigClient{Client: rig.base, rig: rig}, Reader: reader,
		Namespace: retentionNS, Reconciled: reconciled, Kinds: map[Kind]bool{TaskClaim: true},
		Pods: rig.pods, Archive: rig.archive, Slots: rig.slots, metrics: newRetentionMetrics(),
		Now: func() time.Time { return rig.now },
	}
}

type rigReader struct {
	client.Client
	lists *fixtureReader
	rig   *passRig
}

func (r *rigReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return r.lists.List(ctx, list, opts...)
}

func (r *rigReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	if r.rig.hide[key.Name] {
		return apierrors.NewNotFound(schema.GroupResource{Group: "coordination.dev-env.haynesops.com", Resource: "taskclaims"}, key.Name)
	}
	if _, ok := obj.(*devv1.AgentSession); ok && r.rig.session != nil && r.rig.sessionGets < r.rig.sessionMisses {
		r.rig.sessionGets++
		return apierrors.NewNotFound(schema.GroupResource{Group: "dev-env.haynesops.com", Resource: "agentsessions"}, key.Name)
	}
	return r.Client.Get(ctx, key, obj)
}

type rigClient struct {
	client.Client
	rig *passRig
}

func (c *rigClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.rig.order = append(c.rig.order, deleteLabel(obj))
	var options client.DeleteOptions
	options.ApplyOptions(opts)
	if options.Preconditions == nil || options.Preconditions.UID == nil || options.Preconditions.ResourceVersion == nil {
		return apierrors.NewBadRequest("delete requires UID and resourceVersion preconditions")
	}
	if c.rig.forbid {
		return apierrors.NewForbidden(schema.GroupResource{Group: "coordination.dev-env.haynesops.com", Resource: "taskclaims"}, obj.GetName(), apierrors.NewBadRequest("delete is not granted"))
	}
	if c.rig.hide[obj.GetName()] {
		return apierrors.NewNotFound(schema.GroupResource{Group: "coordination.dev-env.haynesops.com", Resource: "taskclaims"}, obj.GetName())
	}
	if c.rig.conflictRV != "" && *options.Preconditions.ResourceVersion != c.rig.conflictRV {
		return apierrors.NewConflict(schema.GroupResource{Group: "coordination.dev-env.haynesops.com", Resource: "taskclaims"}, obj.GetName(), apierrors.NewBadRequest("resourceVersion"))
	}
	c.rig.realDeletes++
	return c.Client.Delete(ctx, obj, opts...)
}

type memArchive struct {
	err          error
	freezeGrace  bool
	revisions    map[string][]protocol.CoordArchiveRevision
	grace        map[string]*time.Time
	latest       map[string]protocol.CoordArchiveEvent
	objects      map[string][]byte
	puts         []protocol.CoordArchivePutRequest
	marks        []protocol.CoordArchiveMarkRequest
	lookups      int
	failPresent  bool
	failReset    bool
	failResetUID string
	onCall       func()
	onGet        func()
	note         func(string)
}

func newMemArchive() *memArchive {
	return &memArchive{
		revisions: map[string][]protocol.CoordArchiveRevision{},
		grace:     map[string]*time.Time{},
		latest:    map[string]protocol.CoordArchiveEvent{},
		objects:   map[string][]byte{},
	}
}

func (m *memArchive) Lookup(_ context.Context, ids []protocol.CoordArchiveIdentity) ([]protocol.CoordArchiveLookupResult, error) {
	m.lookups++
	if m.onCall != nil {
		m.onCall()
	}
	if m.err != nil {
		return nil, m.err
	}
	out := make([]protocol.CoordArchiveLookupResult, len(ids))
	for i, id := range ids {
		key := lookupKey(id)
		revs := append([]protocol.CoordArchiveRevision(nil), m.revisions[key]...)
		if revs == nil {
			revs = []protocol.CoordArchiveRevision{}
		}
		item := protocol.CoordArchiveLookupResult{CoordArchiveIdentity: id, Holds: len(revs) > 0, Revisions: revs, GraceStart: m.grace[key]}
		if ev, ok := m.latest[key]; ok && ev.State != "" {
			at := ev.At.UTC()
			item.LatestState = ev.State
			item.LatestAt = &at
		}
		out[i] = item
	}
	return out, nil
}

func (m *memArchive) Put(_ context.Context, req protocol.CoordArchivePutRequest, mark bool) (protocol.CoordArchivePutResult, error) {
	if m.onCall != nil {
		m.onCall()
	}
	if m.err != nil {
		return protocol.CoordArchivePutResult{}, m.err
	}
	if req.Event != nil {
		if err := m.refuseBackward(lookupKey(protocol.CoordArchiveIdentity{Resource: req.Manifest.Resource, Project: req.Manifest.Project, Name: req.Manifest.Name, UID: req.Manifest.UID}), req.Event.At); err != nil {
			return protocol.CoordArchivePutResult{}, err
		}
	}
	if mark != (req.Event != nil) {
		return protocol.CoordArchivePutResult{}, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "mark flag")
	}
	m.puts = append(m.puts, req)
	key := lookupKey(protocol.CoordArchiveIdentity{Resource: req.Manifest.Resource, Project: req.Manifest.Project, Name: req.Manifest.Name, UID: req.Manifest.UID})
	m.objects[req.Manifest.UID+"/"+req.Manifest.ResourceVersion+"/"+req.Manifest.Object.SHA256] = append([]byte(nil), req.Object...)
	m.revisions[key] = append(m.revisions[key], protocol.CoordArchiveRevision{ResourceVersion: req.Manifest.ResourceVersion, SHA256: req.Manifest.Object.SHA256})
	var grace *time.Time
	if req.Event != nil && !m.freezeGrace {
		at := req.Event.At.UTC()
		grace = &at
		m.grace[key] = grace
		ev := *req.Event
		ev.At = at
		m.latest[key] = ev
	}
	return protocol.CoordArchivePutResult{Manifest: protocol.CoordArchiveStoredManifest{CoordArchiveManifest: req.Manifest, ArchivedAt: time.Unix(1, 0).UTC()}, GraceStart: grace}, nil
}

func (m *memArchive) Mark(_ context.Context, req []protocol.CoordArchiveMarkRequest) ([]protocol.CoordArchiveMarkResult, error) {
	if m.onCall != nil {
		m.onCall()
	}
	if m.err != nil {
		return nil, m.err
	}
	if m.failPresent {
		for _, item := range req {
			if item.Event.State == "present" {
				return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "present reset failed")
			}
		}
	}
	if m.failReset || m.failResetUID != "" {
		for _, item := range req {
			if item.Event.State == "reset" && (m.failReset || item.UID == m.failResetUID) {
				return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "reset failed")
			}
		}
	}
	for _, item := range req {
		if err := m.refuseBackward(lookupKey(item.CoordArchiveIdentity), item.Event.At); err != nil {
			return nil, err
		}
	}
	m.marks = append(m.marks, req...)
	out := make([]protocol.CoordArchiveMarkResult, len(req))
	for i, item := range req {
		out[i] = protocol.CoordArchiveMarkResult{CoordArchiveIdentity: item.CoordArchiveIdentity, Appended: true}
		key := lookupKey(item.CoordArchiveIdentity)
		ev := item.Event
		ev.At = item.Event.At.UTC()
		m.latest[key] = ev
		if item.Event.State == "present" || item.Event.State == "reset" {
			m.grace[key] = nil
		}
	}
	return out, nil
}

func (m *memArchive) Get(_ context.Context, q protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	if m.note != nil {
		m.note("get")
	}
	if m.onCall != nil {
		m.onCall()
	}
	if m.err != nil {
		return protocol.CoordArchiveGetResult{}, m.err
	}
	raw := m.objects[q.UID+"/"+q.ResourceVersion+"/"+q.SHA256]
	m.onGetHook()
	if raw == nil {
		return protocol.CoordArchiveGetResult{}, protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "not found")
	}
	return protocol.CoordArchiveGetResult{
		Manifest: protocol.CoordArchiveStoredManifest{CoordArchiveManifest: protocol.CoordArchiveManifest{
			Format: protocol.CoordArchiveFormat, Resource: q.Resource, Name: q.Name, UID: q.UID,
			ResourceVersion: q.ResourceVersion, Object: protocol.CoordArchiveObjectDigest{SHA256: q.SHA256, Bytes: int64(len(raw))},
			ArchivedBy: archivedBy,
		}, ArchivedAt: time.Unix(1, 0).UTC()},
		Object: append([]byte(nil), raw...),
	}, nil
}

func TestEnsureAckMarkResetsOnlyAHeldAck(t *testing.T) {
	const (
		ackUID       = "66666666-6666-4666-8666-666666666666"
		recipientUID = "77777777-7777-4777-8777-777777777777"
		messageUID   = "88888888-8888-4888-8888-888888888888"
	)
	name := "read-ack." + strings.Repeat("ab", 16) + "." + strings.Repeat("cd", 16)
	object := map[string]any{
		"apiVersion": protocol.CoordArchiveAPIVersion,
		"kind":       "InboxReadAck",
		"metadata": map[string]any{
			"namespace": "dev-env-system", "name": name, "uid": ackUID, "resourceVersion": "1",
		},
		"spec": map[string]any{
			"project": "sample", "messageUID": messageUID,
			"recipient": map[string]any{"uid": recipientUID},
		},
	}
	raw, err := protocol.CanonicalJSON(object)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	ack := liveRecord{raw: raw, hash: hex.EncodeToString(sum[:]), summary: Summary{
		Kind: InboxReadAck, Resource: "inboxreadacks", Project: "sample", Namespace: "dev-env-system",
		Name: name, UID: ackUID, ResourceVersion: "1", Valid: true,
		Recipient: SessionRef{UID: recipientUID}, Message: MessageRef{UID: messageUID, Namespace: "dev-agents", Name: "message." + strings.Repeat("ab", 16) + "." + strings.Repeat("cd", 16)},
	}}
	message := Summary{Namespace: ack.summary.Message.Namespace, Name: ack.summary.Message.Name, UID: messageUID}
	arch := newMemArchive()
	key := lookupKey(identityOf(ack.summary))
	opened := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	arch.grace[key] = timePtr(opened)
	arch.latest[key] = protocol.CoordArchiveEvent{State: "hold", At: opened.Add(time.Hour)}
	arch.failResetUID = ackUID
	now := opened.Add(40 * 24 * time.Hour)
	runnable := &Runnable{Archive: arch, Slots: shelf.NewExecSlots(2), metrics: newRetentionMetrics(), Now: func() time.Time { return now }}
	if err := runnable.ensureAckMark(context.Background(), ack, message, now); err == nil || arch.grace[key] == nil || arch.latest[key].State != "hold" {
		t.Fatalf("held ack reset was skipped: %v grace %v latest %s", err, arch.grace[key], arch.latest[key].State)
	}
	arch.failResetUID = ""
	// The message's grace start is older than the hold. The new absence must
	// use the reset time, or the journal refuses it.
	if err := runnable.ensureAckMark(context.Background(), ack, message, opened); err != nil || arch.grace[key] == nil || !arch.latest[key].At.Equal(now) || arch.latest[key].State != "absent" {
		t.Fatalf("held ack not re-marked: %v grace %v latest %#v", err, arch.grace[key], arch.latest[key])
	}
	sawReset := false
	for _, item := range arch.marks {
		if item.UID == ackUID && item.Event.State == "reset" {
			sawReset = true
		}
	}
	if !sawReset {
		t.Fatal("ack reset was not attempted")
	}
	resetAt := now.Add(time.Minute)
	arch.grace[key] = nil
	arch.latest[key] = protocol.CoordArchiveEvent{State: "reset", At: resetAt}
	if err := runnable.ensureAckMark(context.Background(), ack, message, opened); err != nil || !arch.latest[key].At.Equal(resetAt) || arch.latest[key].State != "absent" {
		t.Fatalf("absence preceded the reset tail: %v latest %#v", err, arch.latest[key])
	}
}

func (m *memArchive) refuseBackward(key string, at time.Time) error {
	prev, ok := m.latest[key]
	if ok && !prev.At.IsZero() && at.Before(prev.At) {
		return protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "event timestamp cannot move backward")
	}
	return nil
}

func (m *memArchive) onGetHook() {
	if m.onGet != nil {
		m.onGet()
	}
}
