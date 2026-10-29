package apiserver

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

type latencyOwnedReader struct {
	client.Reader
	armed *atomic.Bool
}

func (r latencyOwnedReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if r.armed.Load() {
		if err := ownedLatencyWait(ctx, 60*time.Millisecond); err != nil {
			return err
		}
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

type latencyOwnedExec struct {
	ownedWorkflowExec
	armed *atomic.Bool
	calls *atomic.Int32
}

func (e latencyOwnedExec) Run(ctx context.Context, ns, pod, container string, args []string, in io.Reader, out, stderr io.Writer) error {
	if e.armed.Load() {
		e.calls.Add(1)
		if err := ownedLatencyWait(ctx, 650*time.Millisecond); err != nil {
			return err
		}
	}
	return e.ownedWorkflowExec.Run(ctx, ns, pod, container, args, in, out, stderr)
}
func ownedLatencyWait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Real TLS API, real Kubernetes storage/CAS, original guard/helper/native fixture,
// kernel peer/challenge and CRI-status adapter. Inject transport/metadata/exec
// scheduling latency not a readiness verdict. Ordinary observations intentionally do not write
// unchanged ledgers or contend with owner mutations. No
// provider, credential, model turn, pairing or live-cluster mutation is involved.
func TestEnvtestOwnedAuthorityLatencyComposition(t *testing.T) {
	ctx := context.Background()
	live, err := client.NewWithWatch(envConfig(t), client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dev-agents", "dev-env-system", "owned-latency-budgets"} {
		if err := live.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}
	}
	now := func() time.Time { return time.Now().UTC() }
	if err := live.Create(ctx, &schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: controller.PriorityClassName}, Value: -10, PreemptionPolicy: ptr.To(corev1.PreemptNever)}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	owned := prepareOwnedWorkflowNamed(t, live, now, "latency")
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
	owned.inspector.Reader = latencyOwnedReader{Reader: owned.inspector.Reader, armed: &armed}
	owned.inspector.Probe = controller.OwnedRootProber{Exec: latencyOwnedExec{ownedWorkflowExec: ownedWorkflowExec{path: filepath.Join(owned.home, ".agentd", hostexecutor.EvidenceSocketName), anchor: os.Getpid()}, armed: &armed, calls: &probes}}

	service := &taskbudget.Service{Store: taskbudget.KubeStore{Client: live, Live: live, Namespace: "owned-latency-budgets"}, Now: now, NativeInspector: owned.inspector, Validator: controller.OwnedRootBudgetValidator{Root: owned.inspector}}
	f := coordinatorFixture(t)
	f.srv.Client, f.srv.Live, f.srv.Now = live, live, now
	id := saIdentity("dev-agents/codex-host-a")
	id.PodName, id.PodUID = root.Name, string(root.UID)
	f.auth[tokCoordinator] = id
	f.srv.Policy.Coordinators[0].PodName = root.Name
	f.srv.Policy.Coordinators[0].ServiceAccount = "dev-agents/codex-host-a"
	f.srv.TaskBudgets = service
	f.srv.AssignedTaskBudgets = map[string]string{"host-a": "campaign-a"}
	completed := make(chan time.Duration, 1)
	var canceled atomic.Bool
	handler := f.srv.Handler()
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slow := armed.Load() && r.URL.Path == apiv1.TaskBudgetPath("campaign-a")+"/observe"
		started := time.Now()
		if slow {
			w.Header().Set("Connection", "close")
			if ownedLatencyWait(r.Context(), 120*time.Millisecond) != nil {
				canceled.Store(true)
				return
			}
		}
		handler.ServeHTTP(w, r)
		if slow {
			if r.Context().Err() != nil {
				canceled.Store(true)
			}
			select {
			case completed <- time.Since(started):
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
	defer server.Close()
	hc := server.Client()
	hc.Timeout = 2 * time.Second
	remote := &apiv1.HTTPTaskBudgetGate{BaseURL: server.URL, Client: hc, Token: func(context.Context) (string, error) { return tokCoordinator, nil }}
	b, err := remote.BootstrapOwned(ctx, apiv1.CreateTaskBudgetRequest{TaskUID: "campaign-a", Spec: owned.policy.Spec}, "host-a", string(root.UID))
	if err != nil {
		t.Fatal(err)
	}
	cancel, done := owned.run(t, remote, b)
	defer cancel()
	readyBy := time.Now().Add(4 * time.Second)
	for {
		l, _, readErr := service.Store.Read(ctx, b.TaskUID)
		if readErr == nil && l.NativeAdmitted() && owned.inspector.InspectNativeContinuation(ctx, l, b) == nil {
			break
		}
		select {
		case e := <-done:
			t.Fatal("guard stopped before latency probe", e)
		default:
		}
		if time.Now().After(readyBy) {
			t.Fatal("native never ready", readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	armed.Store(true)
	// Complete accepted replies may drain their HTTP connection. The next
	// observation must reconnect with the same authority, never replay a mutation.
	for pass := 0; pass < 2; pass++ {
		select {
		case elapsed := <-completed:
			t.Logf("complete authority roundtrip=%v probes=%d callerCanceled=%v", elapsed, probes.Load(), canceled.Load())
			if elapsed < time.Second || elapsed >= 2*time.Second || probes.Load() < 1 || canceled.Load() {
				t.Fatal("latency composition not successfully exercised")
			}
		case e := <-done:
			t.Fatal("valid authority killed original native", e)
		case <-time.After(4 * time.Second):
			t.Fatal("authority roundtrip did not complete")
		}
	}
	if connections.Load() < 2 {
		t.Fatal("fresh TLS connection after accepted reply was not exercised")
	}

	// Revoke the real fixture policy: no cache, grace, retry, renewal or stale
	// success may keep the original native process running after refusal.
	var policy corev1.ConfigMap
	if err := live.Get(ctx, owned.inspector.Policy, &policy); err != nil {
		t.Fatal(err)
	}
	policy.Data[taskbudget.OwnedRootPolicyKey] = "{}"
	revoked := time.Now()
	if err := live.Update(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	var stopped error
	select {
	case stopped = <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("revocation did not stop original process")
	}
	raw, err := os.ReadFile(filepath.Join(owned.home, ".agentd", "owned-host.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt hostexecutor.Receipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.State != "Stopped" || !receipt.RootWaitObserved || !receipt.TreeReaped || receipt.Reason != "budget authority unavailable" {
		t.Fatalf("no independent revocation stop: %s (run: %v)", raw, stopped)
	}
	t.Logf("revocation through original wait/reap=%v", time.Since(revoked))
}
