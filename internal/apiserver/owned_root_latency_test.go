package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kexec "k8s.io/client-go/util/exec"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

// The composition case does not sleep and does not dial the evidence socket.
// The prober's one-second context and the evidence server's socket deadline
// are both wall clocks. Spending that context down to the socket reserve and
// then running the live probe can refuse while the proof context is still
// open: the error is a probeRefusal, the category stays transport, and the
// roundtrip never reaches a one-second floor. The success path checks that
// the prober context is live and that its deadline is the one-second budget,
// then returns the ready exit.
// Startup still uses the real probe, which waitReady retries. The deadline
// case still waits until that same prober context ends.
const latencyProofBudget = time.Second

type latencyExecMode uint8

const (
	latencyExecCompose latencyExecMode = iota
	latencyExecUntilProofDeadline
)

// latencySlowKey marks one request whose handler opted into the composition.
// The probe reads it from the request context, so a call already inside
// ServeHTTP cannot observe a later arm.
type latencySlowKey struct{}

func latencySlow(ctx context.Context) bool {
	slow, _ := ctx.Value(latencySlowKey{}).(bool)
	return slow
}

type latencyOwnedExec struct {
	ownedWorkflowExec
	calls     *atomic.Int32
	deadlines *atomic.Int32
	mode      latencyExecMode
}

func (e latencyOwnedExec) Run(ctx context.Context, ns, pod, container string, args []string, in io.Reader, out, stderr io.Writer) error {
	if !latencySlow(ctx) {
		return e.ownedWorkflowExec.Run(ctx, ns, pod, container, args, in, out, stderr)
	}
	e.calls.Add(1)
	if e.mode == latencyExecUntilProofDeadline {
		// The prober context is the one-second proof budget.
		<-ctx.Done()
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// The prober's own timeout is one second. A missing deadline, a deadline
	// already behind, or a longer caller deadline means this is not that budget.
	dl, ok := ctx.Deadline()
	remaining := time.Until(dl)
	if !ok || remaining <= 0 || remaining > latencyProofBudget {
		return errors.New("proof context deadline is not the one-second budget")
	}
	if e.deadlines != nil {
		e.deadlines.Add(1)
	}
	return kexec.CodeExitError{Err: errors.New("composed proof"), Code: hostexecutor.EvidenceReady}
}

// ownedRootOnly passes on the Info lines of the logger named owned-root, and
// nothing else: no Error line, and no line of another logger.
type ownedRootOnly struct {
	logr.LogSink
	name string
}

func (s ownedRootOnly) Enabled(level int) bool {
	return s.name == "owned-root" && s.LogSink.Enabled(level)
}

func (s ownedRootOnly) Info(level int, msg string, kv ...any) {
	if s.name == "owned-root" {
		s.LogSink.Info(level, msg, kv...)
	}
}

func (s ownedRootOnly) Error(error, string, ...any) {}

func (s ownedRootOnly) WithName(name string) logr.LogSink {
	full := name
	if s.name != "" {
		full = s.name + "/" + name
	}
	return ownedRootOnly{LogSink: s.LogSink.WithName(name), name: full}
}

func (s ownedRootOnly) WithValues(kv ...any) logr.LogSink {
	return ownedRootOnly{LogSink: s.LogSink.WithValues(kv...), name: s.name}
}

// latencyLogs records the fixed owned-root Info messages for assertions.
type latencyLogs struct {
	mu    sync.Mutex
	lines []string
}

func (l *latencyLogs) add(msg string) {
	l.mu.Lock()
	l.lines = append(l.lines, msg)
	l.mu.Unlock()
}

func (l *latencyLogs) reset() {
	l.mu.Lock()
	l.lines = nil
	l.mu.Unlock()
}

func (l *latencyLogs) has(msg string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if line == msg {
			return true
		}
	}
	return false
}

type latencyLog struct {
	ownedRootOnly
	logs *latencyLogs
}

func (s latencyLog) Info(level int, msg string, kv ...any) {
	if s.name == "owned-root" && s.logs != nil {
		s.logs.add(msg)
	}
	s.ownedRootOnly.Info(level, msg, kv...)
}

func (s latencyLog) WithName(name string) logr.LogSink {
	full := name
	if s.name != "" {
		full = s.name + "/" + name
	}
	return latencyLog{ownedRootOnly: ownedRootOnly{LogSink: s.LogSink.WithName(name), name: full}, logs: s.logs}
}

func (s latencyLog) WithValues(kv ...any) logr.LogSink {
	return latencyLog{ownedRootOnly: ownedRootOnly{LogSink: s.LogSink.WithValues(kv...), name: s.name}, logs: s.logs}
}

// latencyRoundtrip is one slow observation: wall time, status, and body.
type latencyRoundtrip struct {
	elapsed time.Duration
	status  int
	body    []byte
}

// latencyStatusWriter records the status and body a handler writes.
type latencyStatusWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *latencyStatusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *latencyStatusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	_, _ = w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *latencyStatusWriter) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

type ownedLatencyRun struct {
	t           *testing.T
	ctx         context.Context
	live        client.Client
	service     *taskbudget.Service
	owned       *ownedWorkflow
	binding     apiv1.TaskBudgetBinding
	completed   <-chan latencyRoundtrip
	done        <-chan error
	armed       *atomic.Bool
	probes      *atomic.Int32
	deadlines   *atomic.Int32
	canceled    *atomic.Bool
	connections *atomic.Int32
	logs        *latencyLogs
}

// Real TLS API, real Kubernetes storage/CAS, original guard/helper/native fixture,
// kernel peer/challenge and CRI-status adapter. Startup uses that live probe.
// An armed composition request returns the ready exit once the prober context
// is live, instead of a wall-clock sleep. Ordinary observations intentionally
// do not write unchanged ledgers or contend with owner mutations. No provider,
// credential, model turn, pairing or live-cluster mutation is involved.
func startOwnedLatency(t *testing.T, suffix, budgetNS string, mode latencyExecMode) *ownedLatencyRun {
	t.Helper()
	ctx := context.Background()
	live, err := client.NewWithWatch(envConfig(t), client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dev-agents", "dev-env-system", budgetNS} {
		if err := live.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}
	}
	now := func() time.Time { return time.Now().UTC() }
	if err := live.Create(ctx, &schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: controller.PriorityClassName}, Value: -10, PreemptionPolicy: ptr.To(corev1.PreemptNever)}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	owned := prepareOwnedWorkflowNamed(t, live, now, suffix)
	root := taskbudget.OwnedRootPod(owned.policy)
	root.Spec.NodeName = "synthetic-root-worker"
	if err := live.Create(ctx, root); err != nil {
		t.Fatal(err)
	}
	root.Status.Phase = corev1.PodRunning
	root.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: taskbudget.OwnedRootContainer, Image: owned.policy.Image, ImageID: owned.policy.Image, Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now())}}}}
	if err := live.Status().Update(ctx, root); err != nil {
		t.Fatal(err)
	}
	var armed atomic.Bool
	var probes atomic.Int32
	var deadlines atomic.Int32
	owned.inspector.Probe = controller.OwnedRootProber{Exec: latencyOwnedExec{ownedWorkflowExec: ownedWorkflowExec{path: filepath.Join(owned.home, ".agentd", hostexecutor.EvidenceSocketName), anchor: os.Getpid()}, calls: &probes, deadlines: &deadlines, mode: mode}}

	service := &taskbudget.Service{Store: taskbudget.KubeStore{Client: live, Live: live, Namespace: budgetNS}, Now: now, NativeInspector: owned.inspector, Validator: controller.OwnedRootBudgetValidator{Root: owned.inspector}}
	f := coordinatorFixture(t)
	f.srv.Client, f.srv.Live, f.srv.Now = live, live, now
	id := saIdentity("dev-agents/codex-host-a")
	id.PodName, id.PodUID = root.Name, string(root.UID)
	f.auth[tokCoordinator] = id
	f.srv.Policy.Coordinators[0].PodName = root.Name
	f.srv.Policy.Coordinators[0].ServiceAccount = "dev-agents/codex-host-a"
	f.srv.TaskBudgets = service
	f.srv.AssignedTaskBudgets = map[string]string{"host-a": "campaign-a"}
	completed := make(chan latencyRoundtrip, 1)
	var canceled atomic.Bool
	handler := f.srv.Handler()
	var connections atomic.Int32
	// Diagnostics only: the owned-root refusal logs (fixed stage and category
	// names) reach the test output through the request's logger. Only Info
	// lines of the owned-root logger pass, so no other log, and no error
	// value, reaches CI output.
	logs := &latencyLogs{}
	logger := logr.New(latencyLog{ownedRootOnly: ownedRootOnly{LogSink: testr.New(t).GetSink()}, logs: logs})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(log.IntoContext(r.Context(), logger))
		slow := armed.Load() && r.URL.Path == apiv1.TaskBudgetPath("campaign-a")+"/observe"
		started := time.Now()
		if slow {
			r = r.WithContext(context.WithValue(r.Context(), latencySlowKey{}, true))
			w.Header().Set("Connection", "close")
		}
		recorded := &latencyStatusWriter{ResponseWriter: w}
		handler.ServeHTTP(recorded, r)
		if slow {
			if r.Context().Err() != nil {
				canceled.Store(true)
			}
			select {
			case completed <- latencyRoundtrip{elapsed: time.Since(started), status: recorded.code(), body: bytes.Clone(recorded.body.Bytes())}:
			default:
			}
		}
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.StartTLS()
	// Close after the guard cleanup waits, so a stop report can still reach the API.
	t.Cleanup(server.Close)
	hc := server.Client()
	hc.Timeout = 2 * time.Second
	remote := &apiv1.HTTPTaskBudgetGate{BaseURL: server.URL, Client: hc, Token: func(context.Context) (string, error) { return tokCoordinator, nil }}
	b, err := remote.BootstrapOwned(ctx, apiv1.CreateTaskBudgetRequest{TaskUID: "campaign-a", Spec: owned.policy.Spec}, "host-a", string(root.UID))
	if err != nil {
		t.Fatal(err)
	}
	cancel, done := owned.run(t, remote, b)
	t.Cleanup(cancel)
	return &ownedLatencyRun{t: t, ctx: ctx, live: live, service: service, owned: owned, binding: b, completed: completed, done: done, armed: &armed, probes: &probes, deadlines: &deadlines, canceled: &canceled, connections: &connections, logs: logs}
}

func (r *ownedLatencyRun) waitReady() {
	r.t.Helper()
	// Startup fence only. An armed composition request does not sleep, and the
	// deadline case waits on the prober context.
	readyBy := time.Now().Add(4 * time.Second)
	var readErr error
	for {
		var l *taskbudget.Ledger
		l, _, readErr = r.service.Store.Read(r.ctx, r.binding.TaskUID)
		if readErr == nil && l.NativeAdmitted() && r.owned.inspector.InspectNativeContinuation(r.ctx, l, r.binding) == nil {
			return
		}
		select {
		case e := <-r.done:
			r.t.Fatal("guard stopped before latency probe", e)
		default:
		}
		if time.Now().After(readyBy) {
			r.t.Fatal("native never ready", readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func requireAuthorityStop(t *testing.T, home string, stopped error) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".agentd", "owned-host.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt hostexecutor.Receipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.State != "Stopped" || !receipt.RootWaitObserved || !receipt.TreeReaped || receipt.Reason != "budget authority unavailable" {
		t.Fatalf("no independent authority stop: %s (run: %v)", raw, stopped)
	}
}

func TestEnvtestOwnedAuthorityLatencyComposition(t *testing.T) {
	run := startOwnedLatency(t, "latency", "owned-latency-budgets", latencyExecCompose)
	run.waitReady()
	run.logs.reset()
	run.armed.Store(true)
	// Complete accepted replies may drain their HTTP connection. The next
	// observation must reconnect with the same authority, never replay a mutation.
	for pass := 0; pass < 2; pass++ {
		select {
		case rt := <-run.completed:
			t.Logf("complete authority roundtrip=%v status=%d probes=%d deadlines=%d callerCanceled=%v", rt.elapsed, rt.status, run.probes.Load(), run.deadlines.Load(), run.canceled.Load())
			if rt.status != http.StatusOK {
				t.Fatalf("observation refused, status=%d (pass %d)", rt.status, pass)
			}
			if run.probes.Load() < int32(pass+1) || run.canceled.Load() {
				t.Fatal("latency composition not exercised")
			}
		case e := <-run.done:
			t.Fatal("valid authority killed original native", e)
		case <-time.After(4 * time.Second):
			t.Fatal("authority roundtrip did not complete")
		}
	}
	if run.deadlines.Load() != run.probes.Load() || run.deadlines.Load() < 2 {
		t.Fatalf("proof deadline was not the composition path: deadlines=%d probes=%d", run.deadlines.Load(), run.probes.Load())
	}
	if run.connections.Load() < 2 {
		t.Fatal("fresh TLS connection after accepted reply was not exercised")
	}
	if run.logs.has("owned root process proof refused") {
		t.Fatal("in-budget composition refused process proof")
	}

	// Revoke the real fixture policy: no cache, grace, retry, renewal or stale
	// success may keep the original native process running after refusal.
	var policy corev1.ConfigMap
	if err := run.live.Get(run.ctx, run.owned.inspector.Policy, &policy); err != nil {
		t.Fatal(err)
	}
	policy.Data[taskbudget.OwnedRootPolicyKey] = "{}"
	revoked := time.Now()
	if err := run.live.Update(run.ctx, &policy); err != nil {
		t.Fatal(err)
	}
	var stopped error
	select {
	case stopped = <-run.done:
	case <-time.After(4 * time.Second):
		t.Fatal("revocation did not stop original process")
	}
	requireAuthorityStop(t, run.owned.home, stopped)
	t.Logf("revocation through original wait/reap=%v", time.Since(revoked))
}

// TestEnvtestOwnedAuthorityProofDeadlineRefusal forces the exec hook to occupy
// the prober context until that context ends. The caller stays live. The proof
// refusal must still surface as HTTP 503, latch the budget, and stop the
// original native.
func TestEnvtestOwnedAuthorityProofDeadlineRefusal(t *testing.T) {
	run := startOwnedLatency(t, "latency-proof", "owned-latency-proof-budgets", latencyExecUntilProofDeadline)
	run.waitReady()
	run.logs.reset()
	run.armed.Store(true)
	// Observe the buffered refusal before reading done. Both can be ready, and
	// a select that includes done can report a correct stop as a missing
	// observation.
	var rt latencyRoundtrip
	select {
	case rt = <-run.completed:
	case <-time.After(4 * time.Second):
		t.Fatal("proof deadline roundtrip did not complete")
	}
	t.Logf("proof deadline roundtrip=%v status=%d probes=%d callerCanceled=%v", rt.elapsed, rt.status, run.probes.Load(), run.canceled.Load())
	if rt.elapsed < latencyProofBudget {
		t.Fatalf("proof deadline was not reached: %s", rt.elapsed)
	}
	if rt.status != http.StatusServiceUnavailable || run.canceled.Load() || run.probes.Load() < 1 {
		t.Fatalf("process proof was not refused inside a live caller: status=%d canceled=%v probes=%d body=%s", rt.status, run.canceled.Load(), run.probes.Load(), rt.body)
	}
	var decoded apiv1.ErrorResponse
	if json.Unmarshal(rt.body, &decoded) != nil || decoded.Error.Code != apiv1.CodeUnavailable || decoded.Error.Message != "task budget authority is unavailable" {
		t.Fatalf("task budget not refused: %s", rt.body)
	}
	if !run.logs.has("owned root process proof refused") || !run.logs.has("owned root observation refused") {
		t.Fatal("process proof refusal was not recorded")
	}
	ledger, _, err := run.service.Store.Read(run.ctx, run.binding.TaskUID)
	if err != nil || ledger == nil || !ledger.Latched || ledger.Reason != "NativeAuthorityUnavailable" {
		t.Fatalf("task budget latch missing: %+v %v", ledger, err)
	}
	var stopped error
	select {
	case stopped = <-run.done:
	case <-time.After(4 * time.Second):
		t.Fatal("proof refusal did not stop original process")
	}
	requireAuthorityStop(t, run.owned.home, stopped)
}
