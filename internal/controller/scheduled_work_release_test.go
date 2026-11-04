package controller

// The envtest cases of DESIGN-054 S3a: the session-end rule and the releaser in
// the AgentSession reconciler. No executor runs in any of them (S3b does not
// exist yet, and the releaser has no flag): the tests play the executor's
// part, creating the ScheduledWork with its occurrence and the session with
// the finalizer, label and annotations of the executor's atomic Create.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
)

// swBase is the first occurrence instant; each fixture takes its own minute
// after it, so no two derived session names collide.
var swBase = time.Date(2026, 12, 3, 14, 0, 0, 0, time.UTC)

var scheduledWorkGK = schema.GroupKind{Group: v1alpha1.GroupVersion.Group, Kind: "ScheduledWork"}

// derivedName is DESIGN-054's session name:
// sw-<first 12 hex of the ScheduledWork UID>-<occurrence UTC YYYYMMDDHHMM>.
func derivedName(uid types.UID, at time.Time) string {
	return "sw-" + strings.ReplaceAll(string(uid), "-", "")[:12] + "-" + at.UTC().Format("200601021504")
}

// swFixture is one ScheduledWork with one occurrence, and its session's name.
type swFixture struct {
	key  types.NamespacedName
	uid  types.UID
	at   time.Time
	name string
	mode string
}

// newScheduledWork creates a ScheduledWork in the operator's namespace whose
// status records one occurrence in state, claimed for its derived session, as
// the executor leaves it once it has dispatched.
func newScheduledWork(t *testing.T, mode, state string) swFixture {
	t.Helper()
	seq++
	at := swBase.Add(time.Duration(seq) * time.Minute)
	w := &v1alpha1.ScheduledWork{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("work-%d", seq), Namespace: systemNS},
		Spec: v1alpha1.ScheduledWorkSpec{
			Title: "Loki retention trend audit", Owner: "human/dev-env-system/tom", Editor: "human/dev-env-system/tom",
			Due: "2026-12-03T09:00", TimeZone: "America/New_York", Mode: mode,
			NotAfter: metav1.NewTime(time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second)),
			Work:     v1alpha1.ScheduledWorkOrder{Template: "ops-audit", Repo: "haynes-ops", Prompt: "Audit the Loki retention trend and report."},
			Notify:   []string{"pushover:default"},
		},
	}
	if err := k8s.Create(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	f := swFixture{key: client.ObjectKeyFromObject(w), uid: w.UID, at: at, name: derivedName(w.UID, at), mode: mode}
	claimed := metav1.NewTime(at)
	f.setStatus(t, func(st *v1alpha1.ScheduledWorkStatus) {
		st.Occurrences = []v1alpha1.ScheduledWorkOccurrence{{At: metav1.NewTime(at), State: state, Session: f.name, ClaimedAt: &claimed}}
	})
	return f
}

// setStatus edits the item's status, retrying on a conflict.
func (f swFixture) setStatus(t *testing.T, edit func(*v1alpha1.ScheduledWorkStatus)) {
	t.Helper()
	eventually(t, "the ScheduledWork status", func() error {
		var w v1alpha1.ScheduledWork
		if err := k8s.Get(context.Background(), f.key, &w); err != nil {
			return err
		}
		edit(&w.Status)
		return k8s.Status().Update(context.Background(), &w)
	})
}

func (f swFixture) item(t *testing.T) *v1alpha1.ScheduledWork {
	t.Helper()
	var w v1alpha1.ScheduledWork
	if err := k8s.Get(context.Background(), f.key, &w); err != nil {
		t.Fatal(err)
	}
	return &w
}

// occurrence is the fixture's occurrence as stored now.
func (f swFixture) occurrence(t *testing.T) v1alpha1.ScheduledWorkOccurrence {
	t.Helper()
	o := scheduledwork.Find(f.item(t), f.at)
	if o == nil {
		t.Fatalf("no occurrence at %s", f.at)
	}
	return *o
}

// waitOccurrence waits for the occurrence to reach state.
func (f swFixture) waitOccurrence(t *testing.T, state string) v1alpha1.ScheduledWorkOccurrence {
	t.Helper()
	var o v1alpha1.ScheduledWorkOccurrence
	eventually(t, "occurrence "+state, func() error {
		var w v1alpha1.ScheduledWork
		if err := k8s.Get(context.Background(), f.key, &w); err != nil {
			return err
		}
		p := scheduledwork.Find(&w, f.at)
		if p == nil || p.State != state {
			return fmt.Errorf("occurrence %+v, want %s", p, state)
		}
		o = *p
		return nil
	})
	return o
}

// scheduledSession creates the session the executor would dispatch for f: the
// derived name, the label, the three annotations and the finalizer, all in the
// Create. Its start deadline is an hour away unless edit changes it.
func scheduledSession(t *testing.T, f swFixture, edit func(*v1alpha1.AgentSession)) *v1alpha1.AgentSession {
	t.Helper()
	return newSession(t, func(s *v1alpha1.AgentSession) {
		s.Name = f.name
		s.Labels = map[string]string{v1alpha1.LabelScheduledWork: string(f.uid)}
		s.Annotations = map[string]string{
			v1alpha1.AnnotationScheduledWorkName:          f.key.Name,
			v1alpha1.AnnotationScheduledWorkOccurrence:    strconv.FormatInt(f.at.Unix(), 10),
			v1alpha1.AnnotationScheduledWorkStartDeadline: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		}
		s.Finalizers = []string{v1alpha1.ScheduledWorkFinalizer}
		if f.mode == v1alpha1.ScheduledWorkInteractive {
			s.Spec.Mode, s.Spec.Prompt = v1alpha1.ModeRemote, ""
		}
		if edit != nil {
			edit(s)
		}
	})
}

func suspendedAtCreate(s *v1alpha1.AgentSession) {
	s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
}

// waitReleased waits until the session has no scheduled-work finalizer, or is
// gone.
func waitReleased(t *testing.T, name string) {
	t.Helper()
	eventually(t, "the releaser lifts the scheduled-work finalizer of "+name, func() error {
		s, err := get(t, sessionNS, name, &v1alpha1.AgentSession{})
		switch {
		case apierrors.IsNotFound(err):
			return nil
		case err != nil:
			return err
		case controllerutil.ContainsFinalizer(s, v1alpha1.ScheduledWorkFinalizer):
			return errors.New("still held")
		}
		return nil
	})
}

// holdsFinalizer checks, for d, that the session still carries the finalizer.
func holdsFinalizer(t *testing.T, name string, d time.Duration) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		s := session(t, name)
		if !controllerutil.ContainsFinalizer(s, v1alpha1.ScheduledWorkFinalizer) {
			t.Fatalf("the releaser lifted the scheduled-work finalizer of %s; finalizers %v", name, s.Finalizers)
		}
	}
}

func withBlockedCounter(c *prometheus.CounterVec) func(*Reconciler) {
	return func(r *Reconciler) { r.ScheduledWorkReleaseHolds = c }
}

// setTask writes what agentd's last heartbeat of a task would: the agent
// exited with this task, after boot.
func setTask(t *testing.T, name string, task v1alpha1.TaskStatus, outcome *v1alpha1.TaskOutcomeStatus) {
	t.Helper()
	eventually(t, "the heartbeat's task", func() error {
		live := session(t, name)
		hb := metav1.Now()
		live.Status.Agent = &v1alpha1.AgentStatus{Boot: protocol.BootReady, Status: protocol.AgentExited, LastHeartbeat: &hb, Task: &task}
		if outcome != nil {
			o := *outcome
			o.SessionUID = string(live.UID)
			live.Status.TaskOutcome = &o
		}
		return k8s.Status().Update(context.Background(), live)
	})
}

// endPod plays the kubelet for a task that ran and ended before the operator
// ever saw the pod Running: it binds the pod and reports it Succeeded.
func endPod(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	ctx := context.Background()
	binding := &corev1.Binding{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace}, Target: corev1.ObjectReference{Kind: "Node", Name: "talosw01"}}
	if err := k8s.SubResource("binding").Create(ctx, pod, binding); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the pod ends", func() error {
		p, err := get(t, pod.Namespace, pod.Name, &corev1.Pod{})
		if err != nil {
			return err
		}
		p.Status.Phase = corev1.PodSucceeded
		return k8s.Status().Update(ctx, p)
	})
}

// The releaser is part of the reconciler, which always runs: with no executor
// and no flag, a scheduled task the user suspends mid-run is recorded Failed
// with no task result, and only the scheduled-work finalizer goes. The session
// is otherwise untouched: still suspended, its rescue finalizer and its pod
// kept (no rescue answers, so the guard keeps the pod), no pod or volume write,
// and no ScheduledWork read through the cache.
func TestScheduledWorkReleaserNeedsNoExecutor(t *testing.T) {
	blocked := NewScheduledWorkReleaseHoldsCounter()
	op := startOperator(t, withBlockedCounter(blocked))
	f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
	s := scheduledSession(t, f, nil)
	waitClaim(t, s.Name)
	pod := markRunning(t, waitPod(t, s.Name), "talosw02")
	waitStatus(t, s.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	holdsFinalizer(t, s.Name, 2*time.Second)
	if o := f.occurrence(t); o.State != v1alpha1.OccurrenceRunning {
		t.Fatalf("a running session's occurrence changed: %+v", o)
	}

	setMode(t, s.Name, v1alpha1.OperatingModeSuspended)
	o := f.waitOccurrence(t, v1alpha1.OccurrenceFailed)
	if o.Reason != scheduledwork.ReasonNoTaskResult || o.Execution != nil || o.Session != s.Name {
		t.Errorf("occurrence %+v", o)
	}
	waitReleased(t, s.Name)
	if c := f.item(t).Status.Counts; c.Failed != 1 || c.Succeeded+c.Ended != 0 {
		t.Errorf("counts %+v", c)
	}
	live := session(t, s.Name)
	if !controllerutil.ContainsFinalizer(live, Finalizer) || !live.DeletionTimestamp.IsZero() || live.Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		t.Errorf("the session changed beyond its finalizer: finalizers %v, deleted %v, mode %s", live.Finalizers, live.DeletionTimestamp, live.Spec.OperatingMode)
	}
	if p, err := get(t, sessionNS, s.Name, &corev1.Pod{}); err != nil || p.UID != pod.UID {
		t.Errorf("the pod went: %v", err)
	}
	for _, reason := range releaseBlockedReasons {
		if n := terminalCount(blocked, reason); n != 0 {
			t.Errorf("blocked %s %v", reason, n)
		}
	}
	assertNoPodOrVolumeWrites(t, op)
}

// A fast headless success: the task succeeded and its pod ended before the
// operator ever saw it Running, so the phase is Failed and status.taskResult is
// Succeeded. The occurrence follows the task result, with the execution and the
// agent's reported outcome copied.
func TestScheduledWorkReleaseRecordsAFastHeadlessSuccess(t *testing.T) {
	first := startOperator(t)
	f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceDispatched)
	s := scheduledSession(t, f, nil)
	pod := waitPod(t, s.Name)
	first.stop()
	// While no operator runs, the task succeeds and its pod ends, and the
	// status reads as the last observation would have written it.
	setTask(t, s.Name, v1alpha1.TaskStatus{Subtype: "success", NumTurns: 2, FinishedAt: metav1.Now()},
		&v1alpha1.TaskOutcomeStatus{PodUID: string(pod.UID), Record: v1alpha1.TaskOutcomeRecordStatus{
			Summary: "opened the audit PR", PRURLs: []string{"https://github.com/thaynes43/haynes-ops/pull/4242"}}})
	endPod(t, pod)
	eventually(t, "the observed status", func() error {
		live := session(t, s.Name)
		live.Status.Phase = v1alpha1.PhaseFailed
		live.Status.TaskResult = &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultSucceeded, Reason: "TaskSucceeded", Message: "the task exited 0 (success) after 2 turns"}
		return k8s.Status().Update(context.Background(), live)
	})
	startOperator(t)
	o := f.waitOccurrence(t, v1alpha1.OccurrenceSucceeded)
	if o.Execution == nil || o.Execution.State != v1alpha1.TaskResultSucceeded || o.Execution.Reason != "TaskSucceeded" {
		t.Errorf("execution %+v", o.Execution)
	}
	if o.Reported == nil || o.Reported.Summary != "opened the audit PR" || len(o.Reported.PRURLs) != 1 {
		t.Errorf("reported %+v", o.Reported)
	}
	if o.Reason != "the task exited 0 (success) after 2 turns" {
		t.Errorf("reason %q", o.Reason)
	}
	waitReleased(t, s.Name)
	waitStatus(t, s.Name, "phase Failed with taskResult Succeeded", func(st *v1alpha1.AgentSessionStatus) error {
		if st.Phase != v1alpha1.PhaseFailed || st.TaskResult == nil || st.TaskResult.State != v1alpha1.TaskResultSucceeded {
			return fmt.Errorf("phase %s, task result %+v", st.Phase, st.TaskResult)
		}
		return nil
	})
	if c := f.item(t).Status.Counts; c.Succeeded != 1 || c.Failed+c.Ended != 0 {
		t.Errorf("counts %+v", c)
	}
}

// The heartbeat stores the task's exit first and the agent's retained outcome
// after it, in a separate write. The occurrence is recorded at the exit; the
// finalizer stays for scheduledwork.ReportWait so the outcome, when it lands,
// is copied into the terminal record (once, with no second count). With no
// outcome, the session is released when the wait ends.
func TestScheduledWorkReleaseWaitsForTheReportedOutcome(t *testing.T) {
	var ahead atomic.Int64
	startOperator(t, func(r *Reconciler) { r.Now = func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) } })
	t.Run("the outcome lands after the exit", func(t *testing.T) {
		f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
		s := scheduledSession(t, f, nil)
		pod := markRunning(t, waitPod(t, s.Name), "talosw02")
		setTask(t, s.Name, v1alpha1.TaskStatus{Subtype: "success", FinishedAt: metav1.Now()}, nil)
		if o := f.waitOccurrence(t, v1alpha1.OccurrenceSucceeded); o.Reported != nil {
			t.Fatalf("reported before the outcome landed: %+v", o.Reported)
		}
		holdsFinalizer(t, s.Name, 2*time.Second)
		eventually(t, "the retained outcome", func() error {
			live := session(t, s.Name)
			live.Status.TaskOutcome = &v1alpha1.TaskOutcomeStatus{SessionUID: string(live.UID), PodUID: string(pod.UID),
				Record: v1alpha1.TaskOutcomeRecordStatus{Summary: "opened the audit PR"}}
			return k8s.Status().Update(context.Background(), live)
		})
		eventually(t, "the reported outcome copied", func() error {
			if o := f.occurrence(t); o.Reported == nil || o.Reported.Summary != "opened the audit PR" || o.State != v1alpha1.OccurrenceSucceeded {
				return fmt.Errorf("occurrence %+v", o)
			}
			return nil
		})
		waitReleased(t, s.Name)
		if c := f.item(t).Status.Counts; c != (v1alpha1.ScheduledWorkCounts{Succeeded: 1}) {
			t.Errorf("counts %+v", c)
		}
	})
	t.Run("no outcome", func(t *testing.T) {
		f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
		s := scheduledSession(t, f, nil)
		markRunning(t, waitPod(t, s.Name), "talosw02")
		setTask(t, s.Name, v1alpha1.TaskStatus{Subtype: "success", FinishedAt: metav1.Now()}, nil)
		f.waitOccurrence(t, v1alpha1.OccurrenceSucceeded)
		holdsFinalizer(t, s.Name, 2*time.Second)
		ahead.Store(int64(scheduledwork.ReportWait + time.Minute))
		t.Cleanup(func() { ahead.Store(0) })
		// The next heartbeat finds the wait over.
		setTask(t, s.Name, v1alpha1.TaskStatus{Subtype: "success", FinishedAt: metav1.Now()}, nil)
		waitReleased(t, s.Name)
		if o := f.occurrence(t); o.Reported != nil || o.State != v1alpha1.OccurrenceSucceeded {
			t.Errorf("occurrence %+v", o)
		}
	})
}

// A task that exited 0 with no success event has taskResult Unknown: the
// occurrence is Ended, not Succeeded.
func TestScheduledWorkReleaseRecordsUnknownAsEnded(t *testing.T) {
	startOperator(t)
	f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
	s := scheduledSession(t, f, nil)
	markRunning(t, waitPod(t, s.Name), "talosw02")
	// It finished longer ago than the wait for a reported outcome.
	setTask(t, s.Name, v1alpha1.TaskStatus{FinishedAt: metav1.NewTime(time.Now().Add(-2 * scheduledwork.ReportWait))}, nil)
	o := f.waitOccurrence(t, v1alpha1.OccurrenceEnded)
	if o.Execution == nil || o.Execution.State != v1alpha1.TaskResultUnknown || !strings.Contains(o.Reason, "no success result event") {
		t.Errorf("occurrence %+v", o)
	}
	waitReleased(t, s.Name)
	if c := f.item(t).Status.Counts; c.Ended != 1 {
		t.Errorf("counts %+v", c)
	}
}

// An occurrence already terminal (the executor recorded it) is never
// overwritten: the finalizer goes, the record and the counts stay.
func TestScheduledWorkReleaseKeepsATerminalOccurrence(t *testing.T) {
	op := startOperator(t)
	f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
	f.setStatus(t, func(st *v1alpha1.ScheduledWorkStatus) {
		st.Occurrences[0].State, st.Occurrences[0].Reason = v1alpha1.OccurrenceFailed, "not Running by due + grace"
		st.Counts.Failed = 1
	})
	s := scheduledSession(t, f, nil)
	markRunning(t, waitPod(t, s.Name), "talosw02")
	setTask(t, s.Name, v1alpha1.TaskStatus{Subtype: "success", FinishedAt: metav1.NewTime(time.Now().Add(-2 * scheduledwork.ReportWait))}, nil)
	waitReleased(t, s.Name)
	w := f.item(t)
	o := scheduledwork.Find(w, f.at)
	if o.State != v1alpha1.OccurrenceFailed || o.Reason != "not Running by due + grace" || o.Execution != nil {
		t.Errorf("the terminal occurrence was overwritten: %+v", o)
	}
	if w.Status.Counts != (v1alpha1.ScheduledWorkCounts{Failed: 1}) {
		t.Errorf("counts %+v", w.Status.Counts)
	}
	if slices.Contains(op.writes.orderLog(), "update status *v1alpha1.ScheduledWork "+f.key.Name) {
		t.Errorf("the releaser wrote the item: %v", op.writes.orderLog())
	}
}

// A ScheduledWork a person deleted leaves nothing to record and nothing that
// can redispatch it: the session is released. One recreated under the same
// name has another UID, which counts as deleted: released, and the new item is
// not written.
func TestScheduledWorkReleaseWithTheItemGone(t *testing.T) {
	op := startOperator(t)
	t.Run("deleted", func(t *testing.T) {
		f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
		if err := k8s.Delete(context.Background(), f.item(t)); err != nil {
			t.Fatal(err)
		}
		s := scheduledSession(t, f, suspendedAtCreate)
		waitReleased(t, s.Name)
	})
	t.Run("recreated under the same name", func(t *testing.T) {
		f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
		old := f.item(t)
		if err := k8s.Delete(context.Background(), old); err != nil {
			t.Fatal(err)
		}
		again := &v1alpha1.ScheduledWork{ObjectMeta: metav1.ObjectMeta{Name: old.Name, Namespace: old.Namespace}, Spec: old.Spec}
		if err := k8s.Create(context.Background(), again); err != nil {
			t.Fatal(err)
		}
		// The new item records the same instant for its own session.
		next := f
		next.uid, next.name = again.UID, derivedName(again.UID, f.at)
		next.setStatus(t, func(st *v1alpha1.ScheduledWorkStatus) {
			st.Occurrences = []v1alpha1.ScheduledWorkOccurrence{{At: metav1.NewTime(f.at), State: v1alpha1.OccurrenceRunning, Session: next.name}}
		})
		s := scheduledSession(t, f, suspendedAtCreate)
		waitReleased(t, s.Name)
		if o := next.occurrence(t); o.State != v1alpha1.OccurrenceRunning || o.Session != next.name {
			t.Errorf("the recreated item's occurrence changed: %+v", o)
		}
		if c := next.item(t).Status.Counts; c != (v1alpha1.ScheduledWorkCounts{}) {
			t.Errorf("counts %+v", c)
		}
	})
	assertNoPodOrVolumeWrites(t, op)
}

// unservedReader plays an API server that does not serve ScheduledWork, as the
// operator's uncached reader would see it before S6 installs the CRD or after a
// rollback removes it: every read of an item fails with itemErr, and a read of
// the CRD is NotFound while crdGone. Everything else is the real API server.
// envtest cannot remove the CRD under the running suite, so this stands in.
type unservedReader struct {
	client.Reader
	itemErr error
	crdGone bool
}

func (u *unservedReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	switch o := obj.(type) {
	case *v1alpha1.ScheduledWork:
		return u.itemErr
	case *metav1.PartialObjectMetadata:
		if o.GroupVersionKind().Kind == "CustomResourceDefinition" && u.crdGone {
			return apierrors.NewNotFound(schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}, key.Name)
		}
	}
	return u.Reader.Get(ctx, key, obj, opts...)
}

// The kind not served: the releaser cannot prove the items gone, so it waits
// for the session's start deadline (no executor creates for it after that)
// and for its CRD to read NotFound. Before the deadline the finalizer stays
// and the counter rises; after it, the session is released without a record.
// With the CRD still there, as when discovery lags, it stays even after the
// deadline.
func TestScheduledWorkReleaseWithTheKindNotServed(t *testing.T) {
	noKind := &meta.NoKindMatchError{GroupKind: scheduledWorkGK, SearchedVersions: []string{"v1alpha1"}}
	discoveryDown := &apiutil.ErrResourceDiscoveryFailed{v1alpha1.GroupVersion: errors.New("the server is currently unable to handle the request")}
	t.Run("before and after the start deadline", func(t *testing.T) {
		blocked := NewScheduledWorkReleaseHoldsCounter()
		startOperator(t, withBlockedCounter(blocked), func(r *Reconciler) {
			r.APIReader = &unservedReader{Reader: r.APIReader, itemErr: noKind, crdGone: true}
		})
		f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
		deadline := time.Now().Add(5 * time.Second)
		s := scheduledSession(t, f, func(s *v1alpha1.AgentSession) {
			suspendedAtCreate(s)
			s.Annotations[v1alpha1.AnnotationScheduledWorkStartDeadline] = deadline.UTC().Format(time.RFC3339)
		})
		holdsFinalizer(t, s.Name, 2*time.Second)
		if n := terminalCount(blocked, releaseBlockedKindUnserved); n == 0 {
			t.Error("the held release was not counted")
		}
		waitReleased(t, s.Name)
		if time.Now().Before(deadline) {
			t.Error("released before the start deadline")
		}
		if o := f.occurrence(t); o.State != v1alpha1.OccurrenceRunning {
			t.Errorf("an unserved kind was written: %+v", o)
		}
	})
	t.Run("a discovery error with the CRD present", func(t *testing.T) {
		blocked := NewScheduledWorkReleaseHoldsCounter()
		startOperator(t, withBlockedCounter(blocked), func(r *Reconciler) {
			r.APIReader = &unservedReader{Reader: r.APIReader, itemErr: discoveryDown}
		})
		f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
		s := scheduledSession(t, f, func(s *v1alpha1.AgentSession) {
			suspendedAtCreate(s)
			s.Annotations[v1alpha1.AnnotationScheduledWorkStartDeadline] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		})
		holdsFinalizer(t, s.Name, 3*time.Second)
		if n := terminalCount(blocked, releaseBlockedKindUnserved); n == 0 {
			t.Error("the held release was not counted")
		}
	})
}

// unservedMapper is a RESTMapper that does not know ScheduledWork, as a
// cluster without the CRD: the manager's cache, client and API reader all use
// it.
type unservedMapper struct{ meta.RESTMapper }

func (m unservedMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	if gk == scheduledWorkGK {
		return nil, &meta.NoKindMatchError{GroupKind: gk, SearchedVersions: versions}
	}
	return m.RESTMapper.RESTMapping(gk, versions...)
}

func (m unservedMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	if gk == scheduledWorkGK {
		return nil, &meta.NoKindMatchError{GroupKind: gk, SearchedVersions: versions}
	}
	return m.RESTMapper.RESTMappings(gk, versions...)
}

// The operator starts and serves sessions while the API server does not serve
// ScheduledWork: the releaser starts no watch or informer on it. A scheduled
// session it cannot judge keeps its finalizer (the CRD itself still reads, so
// the items are not proven gone) and every other session is reconciled as
// before.
func TestScheduledWorkReleaseLetsTheOperatorStartWithoutTheKind(t *testing.T) {
	blocked := NewScheduledWorkReleaseHoldsCounter()
	op := startOperatorWith(t, func(o *ctrl.Options) {
		o.MapperProvider = func(c *rest.Config, h *http.Client) (meta.RESTMapper, error) {
			m, err := apiutil.NewDynamicRESTMapper(c, h)
			return unservedMapper{m}, err
		}
	}, withBlockedCounter(blocked))
	plain := newSession(t, nil)
	waitClaim(t, plain.Name)
	waitPod(t, plain.Name)

	f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
	s := scheduledSession(t, f, func(s *v1alpha1.AgentSession) {
		suspendedAtCreate(s)
		s.Annotations[v1alpha1.AnnotationScheduledWorkStartDeadline] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	})
	holdsFinalizer(t, s.Name, 3*time.Second)
	if n := terminalCount(blocked, releaseBlockedKindUnserved); n == 0 {
		t.Error("the held release was not counted")
	}
	assertNoPodOrVolumeWrites(t, op)
}

// An interactive session that its template's idle suspend ends is recorded
// Ended, "participation not observed", and released.
func TestScheduledWorkReleaseAnIdleSuspendedInteractiveSession(t *testing.T) {
	startOperator(t)
	f := newScheduledWork(t, v1alpha1.ScheduledWorkInteractive, v1alpha1.OccurrenceOpened)
	s := scheduledSession(t, f, func(s *v1alpha1.AgentSession) {
		s.Spec.Lifecycle = &v1alpha1.Lifecycle{IdleSuspendAfter: &metav1.Duration{Duration: 2 * time.Second}}
	})
	waitClaim(t, s.Name)
	markRunning(t, waitPod(t, s.Name), "talosw02")
	waitStatus(t, s.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	holdsFinalizer(t, s.Name, time.Second)
	setAgent(t, s.Name, protocol.AgentIdle, time.Now().Add(-time.Hour))
	eventually(t, "the idle timer suspends it", func() error {
		if live := session(t, s.Name); live.Annotations[v1alpha1.AnnotationSuspendedBy] != IdleTimerSuspender {
			return fmt.Errorf("mode %s, annotations %v", live.Spec.OperatingMode, live.Annotations)
		}
		return nil
	})
	o := f.waitOccurrence(t, v1alpha1.OccurrenceEnded)
	if o.Reason != scheduledwork.ReasonParticipationNotObserved || o.Execution != nil || o.Reported != nil {
		t.Errorf("occurrence %+v", o)
	}
	waitReleased(t, s.Name)
	if c := f.item(t).Status.Counts; c.Ended != 1 {
		t.Errorf("counts %+v", c)
	}
}

// A drained or booting session has not ended: its agent's report of an end is
// the previous boot's, or not proven by a pod older than standby.PodGrace. A
// never-launched agent that agentd refused past its start deadline does end,
// once the pod-age proof holds, and is recorded Failed, "not started before
// grace ended".
func TestScheduledWorkReleaseWaitsOutADrainOrBoot(t *testing.T) {
	var ahead atomic.Int64
	clock := func(r *Reconciler) { r.Now = func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) } }
	startOperator(t, clock)
	f := newScheduledWork(t, v1alpha1.ScheduledWorkInteractive, v1alpha1.OccurrenceDispatched)
	s := scheduledSession(t, f, func(s *v1alpha1.AgentSession) {
		s.Annotations[v1alpha1.AnnotationScheduledWorkStartDeadline] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	})
	waitClaim(t, s.Name)
	markRunning(t, waitPod(t, s.Name), "talosw02")
	waitStatus(t, s.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	report := func(boot, state, msg string) {
		eventually(t, "the heartbeat", func() error {
			live := session(t, s.Name)
			hb := metav1.Now()
			live.Status.Agent = &v1alpha1.AgentStatus{Boot: boot, Status: state, Message: msg, LastHeartbeat: &hb}
			return k8s.Status().Update(context.Background(), live)
		})
	}
	// A drained pod boots and reports the previous boot's interrupted agent.
	report(protocol.BootBooting, protocol.AgentInterrupted, "")
	holdsFinalizer(t, s.Name, 2*time.Second)
	// Boot complete, agentd refused the late first launch, but the pod is young.
	report(protocol.BootReady, protocol.AgentFailed, protocol.StartDeadlinePassed)
	holdsFinalizer(t, s.Name, 2*time.Second)
	if o := f.occurrence(t); o.State != v1alpha1.OccurrenceDispatched {
		t.Fatalf("an unproven end was recorded: %+v", o)
	}
	// Ten minutes on, the pod-age proof holds; the next heartbeat brings it.
	ahead.Store(int64(10 * time.Minute))
	report(protocol.BootReady, protocol.AgentFailed, protocol.StartDeadlinePassed)
	o := f.waitOccurrence(t, v1alpha1.OccurrenceFailed)
	if o.Reason != scheduledwork.ReasonNotStarted {
		t.Errorf("occurrence %+v", o)
	}
	waitReleased(t, s.Name)
}

// A reaped session, its rescue finalizer present: the releaser records the
// reap and lifts its own finalizer without waiting on the rescue, and the
// rescue and archive then lift theirs. Neither waits on the other.
func TestScheduledWorkReleaseAReapedSessionWithItsRescue(t *testing.T) {
	op := startOperator(t)
	f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
	s := scheduledSession(t, f, nil)
	waitClaim(t, s.Name)
	pod := markRunning(t, waitPod(t, s.Name), "talosw02")
	waitStatus(t, s.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	release := make(chan struct{})
	rescuer.answer(t, s.Name, func(p *corev1.Pod) (protocol.RescueReport, error) {
		select {
		case <-release:
		case <-time.After(wait):
		}
		return goodReport(p.Name), nil
	})
	reap(t, s.Name)
	o := f.waitOccurrence(t, v1alpha1.OccurrenceFailed)
	if o.Reason != scheduledwork.ReasonReaped {
		t.Errorf("occurrence %+v", o)
	}
	waitReleased(t, s.Name)
	if live := session(t, s.Name); !controllerutil.ContainsFinalizer(live, Finalizer) {
		t.Fatalf("the rescue finalizer went before the rescue: %v", live.Finalizers)
	}
	close(release)
	finishPodDelete(t, pod)
	finishClaimDelete(t, s.Name)
	waitGone(t, s.Name)
	if !op.writes.before("update status *v1alpha1.ScheduledWork "+f.key.Name, "record rescue of "+string(pod.UID)) {
		t.Errorf("the releaser waited on the rescue: %v", op.writes.orderLog())
	}
}

// failingClient fails the writes a test names: the ScheduledWork status write,
// or the patch that lifts the scheduled-work finalizer. Every other call goes
// through.
type failingClient struct {
	client.Client
	failStatus  atomic.Bool
	failRelease atomic.Bool
}

func (c *failingClient) Patch(ctx context.Context, o client.Object, p client.Patch, opts ...client.PatchOption) error {
	if s, ok := o.(*v1alpha1.AgentSession); ok && c.failRelease.Load() && s.Labels[v1alpha1.LabelScheduledWork] != "" &&
		!controllerutil.ContainsFinalizer(s, v1alpha1.ScheduledWorkFinalizer) {
		if data, err := p.Data(o); err == nil && strings.Contains(string(data), "finalizers") {
			return apierrors.NewServiceUnavailable("the test fails the finalizer patch")
		}
	}
	return c.Client.Patch(ctx, o, p, opts...)
}

func (c *failingClient) Status() client.SubResourceWriter {
	return failingStatus{SubResourceWriter: c.Client.Status(), c: c}
}

type failingStatus struct {
	client.SubResourceWriter
	c *failingClient
}

func (s failingStatus) Update(ctx context.Context, o client.Object, opts ...client.SubResourceUpdateOption) error {
	if _, ok := o.(*v1alpha1.ScheduledWork); ok && s.c.failStatus.Load() {
		return apierrors.NewForbidden(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "scheduledworks"}, "x", errors.New("the test fails the status write"))
	}
	return s.SubResourceWriter.Update(ctx, o, opts...)
}

// A status write that keeps failing keeps the finalizer and counts: the
// session stays Terminating after its rescue and archive lifted their own
// finalizer, holding nothing but the record. Once the write works (here a
// fresh operator), the record lands and the session goes.
func TestScheduledWorkReleaseHoldsWhileTheStatusWriteFails(t *testing.T) {
	blocked := NewScheduledWorkReleaseHoldsCounter()
	var failing *failingClient
	first := startOperator(t, withBlockedCounter(blocked), func(r *Reconciler) {
		failing = &failingClient{Client: r.Client}
		failing.failStatus.Store(true)
		r.Client = failing
	})
	f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
	s := scheduledSession(t, f, nil)
	waitClaim(t, s.Name)
	pod := markRunning(t, waitPod(t, s.Name), "talosw02")
	waitStatus(t, s.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	rescuer.answer(t, s.Name, reportFor(goodReport))
	reap(t, s.Name)
	finishPodDelete(t, pod)
	finishClaimDelete(t, s.Name)
	eventually(t, "the rescue finalizer goes and the scheduled-work one stays", func() error {
		live := session(t, s.Name)
		if !slices.Equal(live.Finalizers, []string{v1alpha1.ScheduledWorkFinalizer}) {
			return fmt.Errorf("finalizers %v", live.Finalizers)
		}
		return nil
	})
	holdsFinalizer(t, s.Name, time.Second)
	if n := terminalCount(blocked, releaseBlockedStatusWrite); n == 0 {
		t.Error("the failed status write was not counted")
	}
	if o := f.occurrence(t); o.State != v1alpha1.OccurrenceRunning {
		t.Errorf("occurrence %+v", o)
	}
	first.stop()

	startOperator(t)
	o := f.waitOccurrence(t, v1alpha1.OccurrenceFailed)
	if o.Reason != scheduledwork.ReasonReaped {
		t.Errorf("occurrence %+v", o)
	}
	waitGone(t, s.Name)
}

// An operator that stops between the record and the finalizer patch: the next
// one finds the occurrence terminal, writes nothing and counts nothing again,
// and releases.
func TestScheduledWorkReleaseIsIdempotentAcrossARestart(t *testing.T) {
	blocked := NewScheduledWorkReleaseHoldsCounter()
	first := startOperator(t, withBlockedCounter(blocked), func(r *Reconciler) {
		failing := &failingClient{Client: r.Client}
		failing.failRelease.Store(true)
		r.Client = failing
	})
	f := newScheduledWork(t, v1alpha1.ScheduledWorkInteractive, v1alpha1.OccurrenceDelivered)
	s := scheduledSession(t, f, suspendedAtCreate)
	recorded := f.waitOccurrence(t, v1alpha1.OccurrenceEnded)
	eventually(t, "the failed patch is counted", func() error {
		if terminalCount(blocked, releaseBlockedPatch) == 0 {
			return errors.New("not yet")
		}
		return nil
	})
	holdsFinalizer(t, s.Name, time.Second)
	first.stop()

	second := startOperator(t)
	waitReleased(t, s.Name)
	w := f.item(t)
	o := scheduledwork.Find(w, f.at)
	if o.State != recorded.State || o.Reason != recorded.Reason {
		t.Errorf("occurrence %+v, recorded %+v", o, recorded)
	}
	if w.Status.Counts != (v1alpha1.ScheduledWorkCounts{Ended: 1}) {
		t.Errorf("counts %+v: the restart counted again", w.Status.Counts)
	}
	if slices.Contains(second.writes.orderLog(), "update status *v1alpha1.ScheduledWork "+f.key.Name) {
		t.Errorf("the second operator wrote the item again: %v", second.writes.orderLog())
	}
}
