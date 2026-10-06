package controller

// The envtest suite: a real kube-apiserver and etcd (internal/testenv) with the
// CRDs installed, and the reconciler running in a controller-runtime manager
// scoped as cmd/dev-env-operator scopes it. envtest has no scheduler, kubelet or
// garbage collector, so the tests play those parts: they bind pods to nodes and
// write pod status by hand.
//
// It runs through `make test`, one package at a time with the rest of the
// suites, under the CPU caps of CLAUDE.md. The tests run one after another.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/templates"
	"github.com/thaynes43/dev-env/internal/testenv"
)

const (
	sessionNS = "dev-agents"
	systemNS  = "dev-env-system"
	// wait bounds every eventually; the reconciler reacts in milliseconds.
	wait = 20 * time.Second
)

var (
	k8s          client.Client
	restCfg      *rest.Config
	scheme       = runtime.NewScheme()
	templatesKey = types.NamespacedName{Namespace: systemNS, Name: templates.DefaultName}
	exampleDoc   string
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	b, err := os.ReadFile("../templates/testdata/templates.yaml")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	exampleDoc = string(b)

	env, err := testenv.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "envtest:", err)
		return 1
	}
	defer func() {
		if err := env.Stop(); err != nil {
			fmt.Fprintln(os.Stderr, "envtest stop:", err)
		}
	}()
	restCfg = env.Config
	if err := errors.Join(clientgoscheme.AddToScheme(scheme), v1alpha1.AddToScheme(scheme)); err != nil {
		fmt.Fprintln(os.Stderr, "scheme:", err)
		return 1
	}
	k8s, err = client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintln(os.Stderr, "client:", err)
		return 1
	}
	if err := setup(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "setup:", err)
		return 1
	}
	return m.Run()
}

// setup creates what haynes-ops provides: the namespaces, the PriorityClass (the
// API server's Priority admission refuses a pod naming a missing one), the
// agents' ServiceAccount and the templates.
func setup(ctx context.Context) error {
	objs := []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: sessionNS}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: systemNS}},
		&schedulingv1.PriorityClass{
			ObjectMeta:       metav1.ObjectMeta{Name: PriorityClassName},
			Value:            -10,
			PreemptionPolicy: ptr.To(corev1.PreemptNever),
		},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: ServiceAccountName, Namespace: sessionNS}},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace},
			Data:       map[string]string{templates.Key: exampleDoc},
		},
	}
	for _, o := range objs {
		if err := k8s.Create(ctx, o); err != nil {
			return err
		}
	}
	return nil
}

// operator is one running operator: a manager with the reconciler, as
// cmd/dev-env-operator builds it, and a record of its writes to pods and volumes.
type operator struct {
	writes *recordingClient
	cancel context.CancelFunc
	done   chan error
}

func startOperator(t *testing.T) *operator {
	t.Helper()
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:                 scheme,
		Cache:                  CacheOptions(sessionNS, templatesKey),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		// Each test starts its own manager in this one process.
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingClient{Client: mgr.GetClient()}
	r := &Reconciler{Client: rec, Templates: templatesKey, APIReader: mgr.GetAPIReader()}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	op := &operator{writes: rec, cancel: cancel, done: make(chan error, 1)}
	go func() { op.done <- mgr.Start(ctx) }()
	t.Cleanup(op.stop)
	return op
}

// stop stops the manager and waits for it, as a pod deletion stops the
// operator. Calling it twice is fine.
func (o *operator) stop() {
	o.cancel()
	if o.done != nil {
		<-o.done
		o.done = nil
	}
}

// recordingClient passes every call through and records each write to a pod or
// a volume, and every delete of anything. The 5.1 tests read the record.
type recordingClient struct {
	client.Client
	mu     sync.Mutex
	record []string
}

func (c *recordingClient) note(verb string, o client.Object) {
	switch o.(type) {
	case *corev1.Pod, *corev1.PersistentVolumeClaim:
	default:
		if verb != "delete" && verb != "deleteAllOf" {
			return
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.record = append(c.record, fmt.Sprintf("%s %T %s", verb, o, o.GetName()))
}

func (c *recordingClient) writesTo() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.record...)
}

func (c *recordingClient) Delete(ctx context.Context, o client.Object, opts ...client.DeleteOption) error {
	c.note("delete", o)
	return c.Client.Delete(ctx, o, opts...)
}

func (c *recordingClient) DeleteAllOf(ctx context.Context, o client.Object, opts ...client.DeleteAllOfOption) error {
	c.note("deleteAllOf", o)
	return c.Client.DeleteAllOf(ctx, o, opts...)
}

func (c *recordingClient) Update(ctx context.Context, o client.Object, opts ...client.UpdateOption) error {
	c.note("update", o)
	return c.Client.Update(ctx, o, opts...)
}

func (c *recordingClient) Patch(ctx context.Context, o client.Object, p client.Patch, opts ...client.PatchOption) error {
	c.note("patch", o)
	return c.Client.Patch(ctx, o, p, opts...)
}

func (c *recordingClient) Status() client.SubResourceWriter {
	return recordingStatus{SubResourceWriter: c.Client.Status(), c: c}
}

type recordingStatus struct {
	client.SubResourceWriter
	c *recordingClient
}

func (s recordingStatus) Update(ctx context.Context, o client.Object, opts ...client.SubResourceUpdateOption) error {
	s.c.note("update status", o)
	return s.SubResourceWriter.Update(ctx, o, opts...)
}

func (s recordingStatus) Patch(ctx context.Context, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
	s.c.note("patch status", o)
	return s.SubResourceWriter.Patch(ctx, o, p, opts...)
}

// assertNoPodOrVolumeWrites fails if the operator wrote to or deleted any pod or
// volume, or deleted anything at all (5.1).
func assertNoPodOrVolumeWrites(t *testing.T, ops ...*operator) {
	t.Helper()
	for _, o := range ops {
		if w := o.writes.writesTo(); len(w) > 0 {
			t.Errorf("the operator wrote to pods or volumes, or deleted something: %v", w)
		}
	}
}

var seq int

// newSession creates a task session with a unique name; edit changes it first.
func newSession(t *testing.T, edit func(*v1alpha1.AgentSession)) *v1alpha1.AgentSession {
	t.Helper()
	seq++
	s := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("s-%s-%d", strings.ToLower(sanitize(t.Name())), seq), Namespace: sessionNS},
		Spec: v1alpha1.AgentSessionSpec{
			Repo: "haynes-ops", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeTask,
			Model: "claude-opus-5-5", Effort: "xhigh", Prompt: "fix the docs",
		},
	}
	if edit != nil {
		edit(s)
	}
	if err := k8s.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return s
}

// sanitize keeps a test name's letters and digits, at most 30 of them, so the
// session name stays a DNS label.
func sanitize(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 30 {
		out = out[:30]
	}
	return out
}

// eventually retries f until it returns nil, and fails with its last error.
func eventually(t *testing.T, what string, f func() error) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		err := f()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %v", what, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func get[T client.Object](t *testing.T, ns, name string, obj T) (T, error) {
	t.Helper()
	err := k8s.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, obj)
	return obj, err
}

func session(t *testing.T, name string) *v1alpha1.AgentSession {
	t.Helper()
	s, err := get(t, sessionNS, name, &v1alpha1.AgentSession{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// waitPod waits for the session's pod and returns it.
func waitPod(t *testing.T, name string) *corev1.Pod {
	t.Helper()
	var pod *corev1.Pod
	eventually(t, "the pod "+name, func() error {
		var err error
		pod, err = get(t, sessionNS, name, &corev1.Pod{})
		return err
	})
	return pod
}

func waitClaim(t *testing.T, name string) *corev1.PersistentVolumeClaim {
	t.Helper()
	var c *corev1.PersistentVolumeClaim
	eventually(t, "the volume of "+name, func() error {
		var err error
		c, err = get(t, sessionNS, HomeClaimName(name), &corev1.PersistentVolumeClaim{})
		return err
	})
	return c
}

// waitStatus waits until the session's status passes check.
func waitStatus(t *testing.T, name, what string, check func(*v1alpha1.AgentSessionStatus) error) *v1alpha1.AgentSession {
	t.Helper()
	var s *v1alpha1.AgentSession
	eventually(t, what, func() error {
		var err error
		if s, err = get(t, sessionNS, name, &v1alpha1.AgentSession{}); err != nil {
			return err
		}
		return check(&s.Status)
	})
	return s
}

func phaseIs(p v1alpha1.SessionPhase) func(*v1alpha1.AgentSessionStatus) error {
	return func(st *v1alpha1.AgentSessionStatus) error {
		if st.Phase != p {
			return fmt.Errorf("phase %q (pending %q), want %q", st.Phase, st.PendingReason, p)
		}
		return nil
	}
}

func condition(st *v1alpha1.AgentSessionStatus, typ string) *metav1.Condition {
	for i := range st.Conditions {
		if st.Conditions[i].Type == typ {
			return &st.Conditions[i]
		}
	}
	return nil
}

// markRunning plays the scheduler and the kubelet: it binds the pod to a node and
// reports it Running and Ready.
func markRunning(t *testing.T, pod *corev1.Pod, node string) *corev1.Pod {
	t.Helper()
	ctx := context.Background()
	binding := &corev1.Binding{
		ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
		Target:     corev1.ObjectReference{Kind: "Node", Name: node},
	}
	if err := k8s.SubResource("binding").Create(ctx, pod, binding); err != nil {
		t.Fatal(err)
	}
	p, err := get(t, pod.Namespace, pod.Name, &corev1.Pod{})
	if err != nil {
		t.Fatal(err)
	}
	now := metav1.Now()
	p.Status.Phase = corev1.PodRunning
	p.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: now},
		{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: now},
	}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: ContainerName, Ready: true, Image: p.Spec.Containers[0].Image,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}},
	}}
	if err := k8s.Status().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	return p
}

// setTemplates replaces the templates document and restores the example when the
// test ends.
func setTemplates(t *testing.T, doc string) {
	t.Helper()
	update := func(doc string) error {
		cm := &corev1.ConfigMap{}
		if err := k8s.Get(context.Background(), templatesKey, cm); err != nil {
			return err
		}
		cm.Data = map[string]string{templates.Key: doc}
		return k8s.Update(context.Background(), cm)
	}
	if err := update(doc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := update(exampleDoc); err != nil {
			t.Errorf("restore the templates: %v", err)
		}
	})
}

// deleteTemplates removes the templates ConfigMap and puts it back when the test
// ends.
func deleteTemplates(t *testing.T) {
	t.Helper()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace}}
	if err := k8s.Delete(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace},
			Data:       map[string]string{templates.Key: exampleDoc},
		}
		if err := k8s.Create(context.Background(), cm); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Errorf("restore the templates: %v", err)
		}
	})
}
