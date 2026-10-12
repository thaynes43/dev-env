package controller

// DESIGN-025 unit H1: the durable terminal record of a private executor, and
// the once-only missed marker. The unit tests drive Reconcile against the fake
// client; the envtest test drives it against a real API server with the CRD's
// rules (T6, T15 and T16's rebuild case).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

type terminalFixture struct {
	s     *v1alpha1.AgentSession
	home  *corev1.PersistentVolumeClaim
	pod   *corev1.Pod
	node  *corev1.Node
	lease *coordinationv1.Lease
	now   time.Time
}

// newTerminalFixture is a running private Session with a bound HOME whose
// recorded executor ended with DeadlineExceeded on a Ready node with a fresh
// lease: everything section 3.2 asks for.
func newTerminalFixture() terminalFixture {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	s, home := privateHistoryFixture()
	s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	s.Status.SuspendedAt, s.Status.Rescue = nil, nil
	h := s.Status.PrivateHistory
	h.ExecutorAttempted, h.ExecutorIntent = true, "original-intent"
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: "original-executor", OwnerReferences: []metav1.OwnerReference{ownerRef(s)},
		Labels:      map[string]string{v1alpha1.LabelRevision: "original-template"},
		Annotations: map[string]string{privateHistoryExecutorAnnotation: h.ExecutorIntent, privateHistoryHomeAnnotation: h.HomeUID}},
		Spec: corev1.PodSpec{NodeName: "worker-a", RestartPolicy: corev1.RestartPolicyAlways,
			Containers: []corev1.Container{{Name: ContainerName, Image: "synthetic.invalid/agent:original",
				Env:          []corev1.EnvVar{{Name: "HOME", Value: "/home/dev"}, {Name: "CLAUDE_CONFIG_DIR", Value: "/home/dev/.claude"}, {Name: "CODEX_HOME", Value: "/home/dev/.codex"}},
				VolumeMounts: []corev1.VolumeMount{{Name: "home", MountPath: "/home/dev"}, {Name: "shared", MountPath: "/home/dev/.shared"}}}},
			Volumes: []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: home.Name}}},
				{Name: "shared", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "DeadlineExceeded", ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 143, Reason: "Error", FinishedAt: metav1.NewTime(now.Add(-2 * time.Second))}}}}}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-a", UID: "node-1"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: node.Name, Namespace: "kube-node-lease", OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: node.Name, UID: node.UID}}},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(node.Name), RenewTime: &metav1.MicroTime{Time: now.Add(-time.Second)}}}
	return terminalFixture{s: s, home: home, pod: pod, node: node, lease: lease, now: now}
}

func (f terminalFixture) client(skip ...client.Object) client.WithWatch {
	var objects []client.Object
	for _, o := range []client.Object{f.s, f.home, f.pod, f.node, f.lease} {
		keep := true
		for _, s := range skip {
			if s == o {
				keep = false
			}
		}
		if keep {
			objects = append(objects, o)
		}
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentSession{}, &corev1.Pod{}, &corev1.Node{}).WithObjects(objects...).Build()
}

func (f terminalFixture) reconciler(c client.Client, reader client.Reader, counter *prometheus.CounterVec) *Reconciler {
	return &Reconciler{Client: c, APIReader: reader, PrivateTerminalRecord: true, PrivateExecutorTerminal: counter, Now: func() time.Time { return f.now }}
}

func reconcileSession(t *testing.T, r *Reconciler, s *v1alpha1.AgentSession, times int) ctrl.Result {
	t.Helper()
	var res ctrl.Result
	for range times {
		var err error
		if res, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
			t.Fatal(err)
		}
	}
	return res
}

func storedHistory(t *testing.T, c client.Reader, s *v1alpha1.AgentSession) *v1alpha1.AgentSession {
	t.Helper()
	var got v1alpha1.AgentSession
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), &got); err != nil {
		t.Fatal(err)
	}
	return &got
}

func terminalCount(c *prometheus.CounterVec, result string) float64 {
	var m dto.Metric
	if err := c.WithLabelValues(result).Write(&m); err != nil {
		panic(err)
	}
	return m.GetCounter().GetValue()
}

// Terminal recorded once; the Pod lost after the record keeps it and reads
// ExecutorEnded; an operator restart is idempotent.
func TestPrivateTerminalRecordedOnceAndSurvivesPodLossAndRestart(t *testing.T) {
	f := newTerminalFixture()
	c := f.client()
	counter := NewPrivateExecutorTerminalCounter()
	r := f.reconciler(c, c, counter)
	reconcileSession(t, r, f.s, 3)
	got := storedHistory(t, c, f.s)
	rec := got.Status.PrivateHistory.ExecutorTerminal
	if rec == nil {
		t.Fatalf("no terminal record: %+v", got.Status.PrivateHistory)
	}
	want := &v1alpha1.PrivateExecutorTerminalStatus{Version: 1, PodUID: "original-executor", Phase: "Failed", Reason: "DeadlineExceeded", PodResourceVersion: rec.PodResourceVersion,
		NodeName: "worker-a", NodeUID: "node-1", LeaseRenewedAt: metav1.NewTime(f.now.Add(-time.Second)), ObservedAt: metav1.NewTime(f.now),
		Containers:      []v1alpha1.PrivateExecutorContainerTermination{{Name: ContainerName, ExitCode: 143, Reason: "Error", FinishedAt: metav1.NewTime(f.now.Add(-2 * time.Second))}},
		ClaudeConfigDir: ".claude", CodexHome: ".codex"}
	if rec.PodResourceVersion == "" || !apiequality.Semantic.DeepEqual(rec, want) {
		t.Fatalf("terminal record\n got %+v\nwant %+v", rec, want)
	}
	if got.Status.PrivateHistory.ExecutorMissed != nil || got.Status.PrivateHistory.StopState != "Unknown" {
		t.Fatal("a terminal record came with a missed marker or a stop claim")
	}
	if n := terminalCount(counter, terminalRecorded); n != 1 {
		t.Fatalf("recorded counted %v times", n)
	}

	// The Pod goes, as a node reboot would take it.
	if err := c.Delete(context.Background(), f.pod); err != nil {
		t.Fatal(err)
	}
	reconcileSession(t, r, f.s, 2)
	got = storedHistory(t, c, f.s)
	if !apiequality.Semantic.DeepEqual(got.Status.PrivateHistory.ExecutorTerminal, rec) || got.Status.PrivateHistory.ExecutorMissed != nil {
		t.Fatalf("pod loss changed the record: %+v", got.Status.PrivateHistory)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, ConditionPrivateHistory)
	ready := meta.FindStatusCondition(got.Status.Conditions, ConditionPodReady)
	if cond == nil || cond.Reason != "ExecutorEnded" || cond.Status != metav1.ConditionTrue || ready == nil || ready.Reason != "ExecutorEnded" {
		t.Fatalf("a lost Pod after its record is not ExecutorEnded: %+v %+v", cond, ready)
	}

	// Two restarts: new reconcilers, the same counter. Nothing more is written
	// or counted.
	for range 2 {
		reconcileSession(t, f.reconciler(c, c, counter), f.s, 2)
	}
	got = storedHistory(t, c, f.s)
	if !apiequality.Semantic.DeepEqual(got.Status.PrivateHistory.ExecutorTerminal, rec) || got.Status.PrivateHistory.ExecutorMissed != nil {
		t.Fatal("a restart changed the record")
	}
	if terminalCount(counter, terminalRecorded) != 1 || terminalCount(counter, terminalMissed) != 0 {
		t.Fatal("a restart counted again")
	}
}

// Never from absence: the Pod lost before its record gives the missed marker,
// once, and stays Unknown; nothing ever writes a terminal record after it.
func TestPrivateTerminalNeverFromAbsence(t *testing.T) {
	f := newTerminalFixture()
	f.pod.Status.Phase, f.pod.Status.Reason = corev1.PodRunning, ""
	f.pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	c := f.client(f.pod)
	counter := NewPrivateExecutorTerminalCounter()
	reconcileSession(t, f.reconciler(c, c, counter), f.s, 3)
	got := storedHistory(t, c, f.s)
	h := got.Status.PrivateHistory
	if h.ExecutorTerminal != nil || h.ExecutorMissed == nil || h.ExecutorMissed.PodUID != "original-executor" || !h.ExecutorMissed.ObservedAt.Equal(&metav1.Time{Time: f.now}) {
		t.Fatalf("absence did not give exactly the missed marker: %+v", h)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, ConditionPrivateHistory)
	if cond == nil || cond.Reason != "PreservationUnknown" {
		t.Fatalf("a missed executor is not PreservationUnknown: %+v", cond)
	}
	for range 2 {
		reconcileSession(t, f.reconciler(c, c, counter), f.s, 2)
	}
	if n := terminalCount(counter, terminalMissed); n != 1 {
		t.Fatalf("missed counted %v times across restarts", n)
	}
	if terminalCount(counter, terminalRecorded) != 0 {
		t.Fatal("absence counted as recorded")
	}
	// Even a terminal Pod of the same name with the recorded UID showing up
	// later cannot be recorded beside the marker.
	if err := c.Create(context.Background(), newTerminalFixture().pod); err != nil {
		t.Fatal(err)
	}
	reconcileSession(t, f.reconciler(c, c, counter), f.s, 2)
	if got := storedHistory(t, c, f.s); got.Status.PrivateHistory.ExecutorTerminal != nil {
		t.Fatal("a terminal record was written beside the missed marker")
	}
}

// T15: no record while a check fails; the reconcile asks again in 30s. Each
// failure leaves the HOME and the Pod alone.
func TestPrivateTerminalChecksBlockRecording(t *testing.T) {
	forbidden := func(kind string) func(*terminalFixture) interceptor.Funcs {
		return func(*terminalFixture) interceptor.Funcs {
			return interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
				switch o.(type) {
				case *corev1.Node:
					if kind == "nodes" {
						return apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, key.Name, errors.New("no RBAC until H2"))
					}
				case *coordinationv1.Lease:
					if kind == "leases" {
						return apierrors.NewForbidden(schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"}, key.Name, errors.New("no RBAC until H2"))
					}
				}
				return c.Get(ctx, key, o, opts...)
			}}
		}
	}
	for _, tc := range []struct {
		name     string
		edit     func(*terminalFixture)
		reader   func(*terminalFixture) interceptor.Funcs
		terminal bool // the Pod's phase is terminal, so refused is counted
	}{
		{name: "node not ready", terminal: true, edit: func(f *terminalFixture) { f.node.Status.Conditions[0].Status = corev1.ConditionFalse }},
		{name: "node ready unknown", terminal: true, edit: func(f *terminalFixture) { f.node.Status.Conditions[0].Status = corev1.ConditionUnknown }},
		{name: "two ready conditions", terminal: true, edit: func(f *terminalFixture) {
			f.node.Status.Conditions = append(f.node.Status.Conditions, f.node.Status.Conditions[0])
		}},
		{name: "node deleting", terminal: true, edit: func(f *terminalFixture) {
			f.node.DeletionTimestamp, f.node.Finalizers = &metav1.Time{Time: f.now}, []string{"keep"}
		}},
		{name: "lease stale", terminal: true, edit: func(f *terminalFixture) { f.lease.Spec.RenewTime.Time = f.now.Add(-41 * time.Second) }},
		{name: "lease future", terminal: true, edit: func(f *terminalFixture) { f.lease.Spec.RenewTime.Time = f.now.Add(2 * time.Second) }},
		{name: "lease held by another node", terminal: true, edit: func(f *terminalFixture) { f.lease.Spec.HolderIdentity = ptr.To("worker-b") }},
		{name: "lease owned by another node UID", terminal: true, edit: func(f *terminalFixture) { f.lease.OwnerReferences[0].UID = "node-2" }},
		{name: "container still running", terminal: true, edit: func(f *terminalFixture) {
			f.pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
		}},
		{name: "container waiting", terminal: true, edit: func(f *terminalFixture) {
			f.pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
		}},
		{name: "init container unreported", terminal: true, edit: func(f *terminalFixture) {
			f.pod.Spec.InitContainers = []corev1.Container{{Name: "k8tz"}}
		}},
		{name: "pod GC marked it failed", terminal: true, edit: func(f *terminalFixture) {
			f.pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue, Reason: "DeletionByPodGC"}}
		}},
		{name: "unscheduled", terminal: true, edit: func(f *terminalFixture) { f.pod.Spec.NodeName = "" }},
		{name: "node read forbidden", terminal: true, reader: forbidden("nodes")},
		{name: "lease read forbidden", terminal: true, reader: forbidden("leases")},
		{name: "running", edit: func(f *terminalFixture) {
			f.pod.Status.Phase, f.pod.Status.Reason = corev1.PodRunning, ""
			f.pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
		}},
		{name: "terminated container but running phase", edit: func(f *terminalFixture) { f.pod.Status.Phase = corev1.PodRunning }},
		{name: "flag off", edit: func(f *terminalFixture) {}},
		{name: "executor not yet recorded", edit: func(f *terminalFixture) {
			// A first observation records lastExecutor only; the terminal record
			// waits for the next reconcile, which this test does not run.
			f.s.Status.PrivateHistory.LastExecutor = nil
		}},
		{name: "unbound HOME", edit: func(f *terminalFixture) {
			f.s.Status.PrivateHistory.HomeUID = ""
			f.s.Status.PrivateHistory.Admission = "Unknown"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTerminalFixture()
			if tc.edit != nil {
				tc.edit(&f)
			}
			base := f.client()
			var reader client.Reader = base
			if tc.reader != nil {
				reader = interceptor.NewClient(base, tc.reader(&f))
			}
			counter := NewPrivateExecutorTerminalCounter()
			r := f.reconciler(base, reader, counter)
			if tc.name == "flag off" {
				r.PrivateTerminalRecord = false
			}
			times := 3
			if tc.name == "executor not yet recorded" {
				times = 1
			}
			res := reconcileSession(t, r, f.s, times)
			got := storedHistory(t, base, f.s)
			if got.Status.PrivateHistory.ExecutorTerminal != nil || got.Status.PrivateHistory.ExecutorMissed != nil {
				t.Fatalf("recorded despite a failed check: %+v", got.Status.PrivateHistory)
			}
			if tc.terminal {
				if terminalCount(counter, terminalRefused) == 0 {
					t.Fatal("a refused terminal observation was not counted")
				}
				if res.RequeueAfter <= 0 || res.RequeueAfter > privateTerminalRetry {
					t.Fatalf("a refused observation does not ask again within 30s: %v", res.RequeueAfter)
				}
			} else if terminalCount(counter, terminalRefused) != 0 {
				t.Fatal("refused counted for an executor that is not terminal")
			}
			var pod corev1.Pod
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); err != nil || pod.UID != f.pod.UID {
				t.Fatal("the executor Pod was touched", err)
			}
		})
	}
}

// Stale or forged Pod UIDs are refused: a replaced Pod, a Pod another owner
// controls, and a Pod whose UID changed after the observation.
func TestPrivateTerminalRefusesStaleOrForgedPod(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*terminalFixture)
	}{
		{"replaced UID", func(f *terminalFixture) { f.pod.UID = "replacement" }},
		{"foreign controller", func(f *terminalFixture) { f.pod.OwnerReferences[0].UID = "other-session" }},
		{"no controller", func(f *terminalFixture) { f.pod.OwnerReferences[0].Controller = nil }},
		{"other intent", func(f *terminalFixture) { f.pod.Annotations[privateHistoryExecutorAnnotation] = "forged-intent" }},
		{"other HOME", func(f *terminalFixture) { f.pod.Annotations[privateHistoryHomeAnnotation] = "forged-home" }},
		{"hold pod", func(f *terminalFixture) { f.pod.Labels[v1alpha1.LabelHold] = "true" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTerminalFixture()
			tc.edit(&f)
			c := f.client()
			counter := NewPrivateExecutorTerminalCounter()
			reconcileSession(t, f.reconciler(c, c, counter), f.s, 3)
			got := storedHistory(t, c, f.s)
			if got.Status.PrivateHistory.ExecutorTerminal != nil || got.Status.PrivateHistory.ExecutorMissed != nil || terminalCount(counter, terminalRecorded) != 0 {
				t.Fatalf("recorded a stale or forged Pod: %+v", got.Status.PrivateHistory)
			}
		})
	}
	t.Run("UID changed after the observation", func(t *testing.T) {
		f := newTerminalFixture()
		c := f.client()
		r := f.reconciler(c, c, nil)
		if _, err := r.verifyExecutorTerminated(context.Background(), f.s, "original-executor"); err != nil {
			t.Fatal("the fixture's proof is refused:", err)
		}
		if rec, err := r.verifyExecutorTerminated(context.Background(), f.s, "stale-executor"); err == nil || rec != nil {
			t.Fatal("a proof for another UID was accepted")
		}
	})
}

// A stale CRD that prunes the record fails the confirmed write; nothing is
// counted as recorded.
func TestPrivateTerminalPrunedBySchemaIsDetected(t *testing.T) {
	f := newTerminalFixture()
	base := f.client()
	c := interceptor.NewClient(base, interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
		if typed, ok := o.(*v1alpha1.AgentSession); ok && typed.Status.PrivateHistory != nil {
			typed.Status.PrivateHistory.ExecutorTerminal = nil // what a schema without the field prunes
		}
		return c.SubResource(sub).Update(ctx, o, opts...)
	}})
	counter := NewPrivateExecutorTerminalCounter()
	r := f.reconciler(c, base, counter)
	var failed bool
	for range 3 {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)}); err != nil {
			failed = true
		}
	}
	if !failed || terminalCount(counter, terminalRecorded) != 0 || storedHistory(t, base, f.s).Status.PrivateHistory.ExecutorTerminal != nil {
		t.Fatal("a pruned terminal record was confirmed or counted")
	}
}

// Canary originals are recorded too (section 3.3): the record is evidence, and
// canaries never expire by section 2's rule, not by this one.
func TestPrivateTerminalRecordsCanaryOriginals(t *testing.T) {
	f := newTerminalFixture()
	f.s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{Version: 1, ID: "fixture-canary"}
	c := f.client()
	r := f.reconciler(c, c, nil)
	obs := observation{}
	r.observePrivateHistory(context.Background(), f.s, &obs)
	if f.s.Status.PrivateHistory.ExecutorTerminal == nil || obs.privateTerminal != terminalRecorded {
		t.Fatalf("a canary original's terminal executor was not recorded: %v", obs.privateHistoryUnknown)
	}
}

// The provider directories are recorded only from a literal env value inside
// the HOME volume; the last entry of a name wins.
func TestPrivateTerminalProviderDirs(t *testing.T) {
	lit := func(name, value string) corev1.EnvVar { return corev1.EnvVar{Name: name, Value: value} }
	from := func(name string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Key: "dir"}}}
	}
	for _, tc := range []struct {
		name         string
		env          []corev1.EnvVar
		claude, code string
	}{
		{"literal", []corev1.EnvVar{lit("CLAUDE_CONFIG_DIR", "/home/dev/.claude"), lit("CODEX_HOME", "/home/dev/.codex")}, ".claude", ".codex"},
		{"nested and cleaned", []corev1.EnvVar{lit("CLAUDE_CONFIG_DIR", "/home/dev/a/../cfg/claude/"), lit("CODEX_HOME", "/home/dev/x/codex")}, "cfg/claude", "x/codex"},
		{"unset", nil, "", ""},
		{"valueFrom", []corev1.EnvVar{from("CLAUDE_CONFIG_DIR"), from("CODEX_HOME")}, "", ""},
		{"last wins: literal then valueFrom", []corev1.EnvVar{lit("CLAUDE_CONFIG_DIR", "/home/dev/.claude"), from("CLAUDE_CONFIG_DIR")}, "", ""},
		{"last wins: valueFrom then literal", []corev1.EnvVar{from("CODEX_HOME"), lit("CODEX_HOME", "/home/dev/.codex")}, "", ".codex"},
		{"expansion", []corev1.EnvVar{lit("CLAUDE_CONFIG_DIR", "$(HOME)/.claude")}, "", ""},
		{"relative", []corev1.EnvVar{lit("CLAUDE_CONFIG_DIR", ".claude")}, "", ""},
		{"outside HOME", []corev1.EnvVar{lit("CLAUDE_CONFIG_DIR", "/tmp/.claude"), lit("CODEX_HOME", "/home/devx/.codex")}, "", ""},
		{"HOME itself", []corev1.EnvVar{lit("CLAUDE_CONFIG_DIR", "/home/dev"), lit("CODEX_HOME", "/home/dev/")}, "", ""},
		{"escapes HOME", []corev1.EnvVar{lit("CLAUDE_CONFIG_DIR", "/home/dev/../etc")}, "", ""},
		{"another volume", []corev1.EnvVar{lit("CLAUDE_CONFIG_DIR", "/home/dev/.shared/claude")}, "", ""},
		{"too long", []corev1.EnvVar{lit("CODEX_HOME", "/home/dev/"+strings.Repeat("d", 257))}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := newTerminalFixture().pod
			pod.Spec.Containers[0].Env = tc.env
			claude, codex := executorProviderDirs(pod)
			if claude != tc.claude || codex != tc.code {
				t.Fatalf("got %q %q, want %q %q", claude, codex, tc.claude, tc.code)
			}
		})
	}
	t.Run("no agent container", func(t *testing.T) {
		pod := newTerminalFixture().pod
		pod.Spec.Containers[0].Name = "other"
		if claude, codex := executorProviderDirs(pod); claude != "" || codex != "" {
			t.Fatal("recorded directories of a container that is not the agent")
		}
	})
	t.Run("HOME on a subPath", func(t *testing.T) {
		pod := newTerminalFixture().pod
		pod.Spec.Containers[0].VolumeMounts[0].SubPath = "dev"
		if claude, codex := executorProviderDirs(pod); claude != "" || codex != "" {
			t.Fatal("recorded directories relative to a HOME subPath mount")
		}
	})
}

// envtest (T6, T15 and T16's rebuild case): the same rules against a real API
// server with the CRD's schema, Node and Lease objects and a fake clock.
func TestPrivateTerminalRecordOnTheAPIServer(t *testing.T) {
	ctx := context.Background()
	if err := k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-node-lease"}}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	envFixture := func(t *testing.T, name string) (*v1alpha1.AgentSession, *corev1.Pod, *corev1.Node, *coordinationv1.Lease) {
		t.Helper()
		f := newTerminalFixture()
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name + "-node"}}
		if err := k8s.Create(ctx, node); err != nil {
			t.Fatal(err)
		}
		node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse, LastHeartbeatTime: metav1.NewTime(now), LastTransitionTime: metav1.NewTime(now), Reason: "KubeletNotReady"}}
		if err := k8s.Status().Update(ctx, node); err != nil {
			t.Fatal(err)
		}
		lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: node.Name, Namespace: "kube-node-lease", OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: node.Name, UID: node.UID}}},
			Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(node.Name), RenewTime: &metav1.MicroTime{Time: now.Add(-time.Second)}}}
		if err := k8s.Create(ctx, lease); err != nil {
			t.Fatal(err)
		}
		s := taskSession()
		s.Name, s.Namespace, s.UID, s.ResourceVersion = name, sessionNS, "", ""
		s.Finalizers = []string{Finalizer}
		if err := k8s.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		home := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(s.Name), Namespace: s.Namespace, OwnerReferences: []metav1.OwnerReference{ownerRef(s)}, Finalizers: []string{Finalizer},
			Annotations: map[string]string{privateHistoryAdmissionAnnotation: "1"}},
			Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
		if err := k8s.Create(ctx, home); err != nil {
			t.Fatal(err)
		}
		pod := f.pod
		pod.Name, pod.Namespace, pod.UID = s.Name, s.Namespace, ""
		pod.OwnerReferences = []metav1.OwnerReference{ownerRef(s)}
		pod.Annotations[privateHistoryHomeAnnotation] = string(home.UID)
		pod.Spec.NodeName = node.Name
		pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = home.Name
		status := pod.Status
		pod.Status = corev1.PodStatus{}
		if err := k8s.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}
		pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName, Image: pod.Spec.Containers[0].Image,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-time.Hour))}}}}}
		if err := k8s.Status().Update(ctx, pod); err != nil {
			t.Fatal(err)
		}
		s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: home.Name, HomeUID: string(home.UID), BindingGeneration: s.Generation, Admission: sharedStarted,
			Provider: string(s.Spec.Agent), StopState: "Unknown", ExecutorAttempted: true, ExecutorIntent: "original-intent",
			LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: string(pod.UID), PodName: pod.Name, NodeName: node.Name, Revision: "original-template", Generation: s.Generation}}
		if err := k8s.Status().Update(ctx, s); err != nil {
			t.Fatal(err)
		}
		// The terminal status a kubelet would write: kept for the step that ends it.
		status.ContainerStatuses[0].Image = pod.Spec.Containers[0].Image
		status.ContainerStatuses[0].State.Terminated.FinishedAt = metav1.NewTime(now.Add(-2 * time.Second))
		pod.Status = status
		return s, pod, node, lease
	}
	operatorAt := func(counter *prometheus.CounterVec) *Reconciler {
		return &Reconciler{Client: k8s, APIReader: k8s, PrivateTerminalRecord: true, PrivateExecutorTerminal: counter, Now: func() time.Time { return now }}
	}
	setNodeReady := func(t *testing.T, node *corev1.Node, ready corev1.ConditionStatus) {
		t.Helper()
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(node), node); err != nil {
			t.Fatal(err)
		}
		node.Status.Conditions[0].Status = ready
		if err := k8s.Status().Update(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	endPod := func(t *testing.T, pod *corev1.Pod) {
		t.Helper()
		ended := pod.Status
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
			t.Fatal(err)
		}
		pod.Status = ended
		if err := k8s.Status().Update(ctx, pod); err != nil {
			t.Fatal(err)
		}
	}
	history := func(t *testing.T, s *v1alpha1.AgentSession) *v1alpha1.AgentSession {
		t.Helper()
		got := &v1alpha1.AgentSession{}
		if err := k8s.Get(ctx, client.ObjectKeyFromObject(s), got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	t.Run("recorded after the checks pass, kept through a rebuild and Pod loss", func(t *testing.T) {
		s, pod, node, lease := envFixture(t, "h1-terminal-recorded")
		counter := NewPrivateExecutorTerminalCounter()
		reconcileSession(t, operatorAt(counter), s, 2)
		if h := history(t, s).Status.PrivateHistory; h.ExecutorTerminal != nil || h.ExecutorMissed != nil || terminalCount(counter, terminalRefused) != 0 {
			t.Fatal("a running executor was recorded or refused")
		}
		endPod(t, pod)
		reconcileSession(t, operatorAt(counter), s, 2)
		if history(t, s).Status.PrivateHistory.ExecutorTerminal != nil || terminalCount(counter, terminalRefused) == 0 {
			t.Fatal("recorded while the node was NotReady")
		}
		setNodeReady(t, node, corev1.ConditionTrue)
		lease.Spec.RenewTime = &metav1.MicroTime{Time: now.Add(-41 * time.Second)}
		if err := k8s.Update(ctx, lease); err != nil {
			t.Fatal(err)
		}
		reconcileSession(t, operatorAt(counter), s, 2)
		if history(t, s).Status.PrivateHistory.ExecutorTerminal != nil {
			t.Fatal("recorded with a stale lease")
		}
		lease.Spec.RenewTime = &metav1.MicroTime{Time: now.Add(-time.Second)}
		if err := k8s.Update(ctx, lease); err != nil {
			t.Fatal(err)
		}
		reconcileSession(t, operatorAt(counter), s, 2)
		got := history(t, s)
		rec := got.Status.PrivateHistory.ExecutorTerminal
		if rec == nil || rec.PodUID != string(pod.UID) || rec.NodeUID != string(node.UID) || rec.Phase != "Failed" || rec.Reason != "DeadlineExceeded" ||
			!rec.ObservedAt.Equal(&metav1.Time{Time: now}) || len(rec.Containers) != 1 || rec.ClaudeConfigDir != ".claude" || terminalCount(counter, terminalRecorded) != 1 {
			t.Fatalf("no confirmed terminal record on the API server: %+v", rec)
		}
		// A rebuild of lastExecutor (a later generation) keeps the record.
		got.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		if err := k8s.Update(ctx, got); err != nil {
			t.Fatal(err)
		}
		reconcileSession(t, operatorAt(counter), s, 2)
		got = history(t, s)
		if got.Status.PrivateHistory.LastExecutor.Generation != got.Generation || !apiequality.Semantic.DeepEqual(got.Status.PrivateHistory.ExecutorTerminal, rec) {
			t.Fatalf("a lastExecutor rebuild dropped or changed the record: %+v", got.Status.PrivateHistory)
		}
		if err := k8s.Delete(ctx, pod, client.GracePeriodSeconds(0)); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			reconcileSession(t, operatorAt(counter), s, 2)
		}
		got = history(t, s)
		if !apiequality.Semantic.DeepEqual(got.Status.PrivateHistory.ExecutorTerminal, rec) || got.Status.PrivateHistory.ExecutorMissed != nil ||
			meta.FindStatusCondition(got.Status.Conditions, ConditionPrivateHistory).Reason != "ExecutorEnded" || terminalCount(counter, terminalRecorded) != 1 {
			t.Fatalf("pod loss after the record lost it or counted again: %+v", got.Status.PrivateHistory)
		}
	})

	t.Run("lost while the node was NotReady: missed once, never terminal", func(t *testing.T) {
		s, pod, _, _ := envFixture(t, "h1-terminal-missed")
		counter := NewPrivateExecutorTerminalCounter()
		endPod(t, pod)
		reconcileSession(t, operatorAt(counter), s, 2)
		if history(t, s).Status.PrivateHistory.ExecutorTerminal != nil {
			t.Fatal("recorded while the node was NotReady")
		}
		if err := k8s.Delete(ctx, pod, client.GracePeriodSeconds(0)); err != nil {
			t.Fatal(err)
		}
		reconcileSession(t, operatorAt(counter), s, 2)
		// Two restarts of the operator.
		for range 2 {
			reconcileSession(t, operatorAt(counter), s, 2)
		}
		got := history(t, s)
		h := got.Status.PrivateHistory
		if h.ExecutorTerminal != nil || h.ExecutorMissed == nil || h.ExecutorMissed.PodUID != string(pod.UID) ||
			meta.FindStatusCondition(got.Status.Conditions, ConditionPrivateHistory).Reason != "PreservationUnknown" {
			t.Fatalf("absence was not exactly the missed marker: %+v", h)
		}
		if terminalCount(counter, terminalMissed) != 1 || terminalCount(counter, terminalRecorded) != 0 {
			t.Fatalf("missed=%v recorded=%v", terminalCount(counter, terminalMissed), terminalCount(counter, terminalRecorded))
		}
	})
}
