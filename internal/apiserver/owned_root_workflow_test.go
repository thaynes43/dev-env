package apiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kexec "k8s.io/client-go/util/exec"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

// Only these inert subprocess modes bypass the ordinary test runner. They have
// no provider, enrollment, pairing, credentials or model task implementation.
func ownedRootFixtureProcess() {
	if len(os.Args) < 2 {
		return
	}
	switch os.Args[1] {
	case "owned-codex-host-helper":
		if hostexecutor.Helper() != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "app-server":
		home := os.Getenv("HOME")
		if os.WriteFile(filepath.Join(home, "wip"), []byte("preserved native WIP"), 0600) != nil {
			os.Exit(2)
		}
		listener, err := net.Listen("unix", filepath.Join(home, "owned-control.sock"))
		if err != nil {
			os.Exit(2)
		}
		srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = ws.Close() }()
			_ = ws.SetReadDeadline(time.Now().Add(time.Second))
			_ = ws.SetWriteDeadline(time.Now().Add(time.Second))
			var req struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			if ws.ReadJSON(&req) != nil || req.ID != 1 || req.Method != "initialize" {
				return
			}
			if ws.WriteJSON(map[string]any{"id": 1, "result": map[string]string{"userAgent": "inert/" + hostexecutor.PinnedNativeVersion}}) != nil {
				return
			}
			var note any
			_ = ws.ReadJSON(&note)
		})}
		_ = srv.Serve(listener)
		os.Exit(2)
	}
}

// The envtest has no kubelet/exec endpoint. This transport adapter performs the
// actual kernel peer + fresh challenge + original native RPC observation, with
// the test process as original guard. Production always anchors namespace PID1.
// It does not inject a readiness verdict or read a receipt file.
type ownedWorkflowExec struct {
	path   string
	anchor int
}

func (e ownedWorkflowExec) Run(ctx context.Context, ns, pod, container string, args []string, _ io.Reader, out, stderr io.Writer) error {
	if len(args) != 6 || args[0] != "/usr/local/bin/agentd" || args[1] != "owned-codex-host-probe" || container != taskbudget.OwnedRootContainer {
		return fmt.Errorf("unexpected probe")
	}
	var b hostexecutor.Binding
	if json.Unmarshal([]byte(args[3]), &b) != nil {
		return fmt.Errorf("binding")
	}
	proof, err := hostexecutor.ProbeEvidence(ctx, e.path, e.anchor, b, args[2])
	if err != nil {
		return err
	}
	return kexec.CodeExitError{Err: fmt.Errorf("probe phase"), Code: hostexecutor.EvidenceExit(proof, b, args[4], args[5])}
}

// Cilium datapath is not present in envtest. Only its independent stored policy
// object is supplied here; every Pod/PVC/ConfigMap/ledger uses the real API.
type ownedWorkflowReader struct {
	client.Reader
	policy client.Reader
}

func (r ownedWorkflowReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*unstructured.Unstructured); ok {
		return r.policy.Get(ctx, key, obj, opts...)
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

type ownedWorkflowGate struct{ remote *apiv1.HTTPTaskBudgetGate }

func (g ownedWorkflowGate) Admit(ctx context.Context, b hostexecutor.Binding) error {
	return g.remote.Admit(ctx, wireOwnedBinding(b))
}
func (g ownedWorkflowGate) Observe(ctx context.Context, b hostexecutor.Binding) (bool, error) {
	return g.remote.Observe(ctx, wireOwnedBinding(b))
}
func wireOwnedBinding(b hostexecutor.Binding) apiv1.TaskBudgetBinding {
	return apiv1.TaskBudgetBinding{TaskUID: b.TaskUID, Epoch: b.Epoch, Deadline: b.Deadline, HostID: b.HostID, PodUID: b.PodUID}
}

type ownedWorkflow struct {
	policy    taskbudget.OwnedRootPolicy
	home      string
	inspector *taskbudget.OwnedRootInspector
}

func prepareOwnedWorkflow(t *testing.T, live client.Client, now func() time.Time) *ownedWorkflow {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	if os.Chmod(home, 0700) != nil || os.Mkdir(filepath.Join(home, ".codex"), 0700) != nil || os.Mkdir(filepath.Join(home, ".agentd"), 0700) != nil {
		t.Fatal("private directories")
	}
	if os.WriteFile(filepath.Join(home, ".codex", "recovery.json"), []byte("authorized recovery"), 0600) != nil {
		t.Fatal("baseline")
	}
	baseline, err := hostexecutor.NativeHomeBaseline(home)
	if err != nil {
		t.Fatal(err)
	}
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "owned-root-home", Namespace: "dev-agents"}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}, VolumeName: "synthetic-owned-volume", Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
	if err := live.Create(ctx, claim); err != nil {
		t.Fatal(err)
	}
	claim.Status.Phase = corev1.ClaimBound
	if err := live.Status().Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	network := taskbudget.NativeFixturePolicy()
	network.SetName("owned-root-network")
	network.SetUID("synthetic-independent-policy")
	network.SetResourceVersion("1")
	network.Object["spec"].(map[string]any)["endpointSelector"] = map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": "private-budget-host"}}
	policy := taskbudget.OwnedRootPolicy{Version: 1, Namespace: "dev-agents", Name: "private-budget-host", ServiceAccount: "codex-host-a", TaskUID: "campaign-a", HostID: "host-a", Image: "ghcr.io/thaynes43/dev-env@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", HomeClaim: claim.Name, HomeUID: string(claim.UID), Baseline: baseline, NetworkPolicyName: network.GetName(), NetworkPolicyUID: string(network.GetUID()), NetworkPolicySHA: taskbudget.OwnedPolicyDigest(network.Object["spec"]), Spec: apiv1.TaskBudgetSpec{SuccessCondition: "private HOME retained after bounded stop", OverallSeconds: 120, EffortSeconds: 180, CheckpointSeconds: 30}}
	raw, _ := json.Marshal(policy)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "owned-root-policy", Namespace: "dev-env-system"}, Data: map[string]string{taskbudget.OwnedRootPolicyKey: string(raw)}}
	if err := live.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}
	reader := ownedWorkflowReader{Reader: live, policy: fake.NewClientBuilder().WithScheme(testScheme).WithObjects(network).Build()}
	inspector := &taskbudget.OwnedRootInspector{Reader: reader, Policy: client.ObjectKeyFromObject(cm), Probe: controller.OwnedRootProber{Exec: ownedWorkflowExec{path: filepath.Join(home, ".agentd", hostexecutor.EvidenceSocketName), anchor: os.Getpid()}}, Now: now, AssignedHost: policy.HostID, AssignedTask: policy.TaskUID, AssignedParent: policy.Namespace + "/" + policy.ServiceAccount, AssignedPod: policy.Name}
	return &ownedWorkflow{policy: policy, home: home, inspector: inspector}
}
func (f *ownedWorkflow) run(t *testing.T, remote *apiv1.HTTPTaskBudgetGate, b apiv1.TaskBudgetBinding) (context.CancelFunc, <-chan error) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	c := hostexecutor.Config{Binding: hostexecutor.Binding{TaskUID: b.TaskUID, Epoch: b.Epoch, Deadline: b.Deadline, HostID: b.HostID, PodUID: b.PodUID}, Home: f.home, HelperBinary: binary, NativeBinary: binary, SocketPath: filepath.Join(f.home, "owned-control.sock"), ReceiptPath: filepath.Join(f.home, ".agentd", "owned-host.json"), ReadyVersion: hostexecutor.PinnedNativeVersion, StopTimeout: 3 * time.Second, Evidence: &hostexecutor.EvidenceConfig{HomeUID: f.policy.HomeUID, Baseline: f.policy.Baseline, ReportStopped: func(ctx context.Context, r hostexecutor.Receipt) error { return remote.StopOwned(ctx, b) }}}
	go func() {
		r, err := hostexecutor.Run(ctx, c, ownedWorkflowGate{remote})
		if err == nil && ctx.Err() == nil {
			err = fmt.Errorf("unexpected original root stop: %s", r.Reason)
		}
		if err == nil && (!r.NativeReady || !r.RootWaitObserved || !r.TreeReaped) {
			err = fmt.Errorf("missing real readiness/owned stop")
		}
		done <- err
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Error("guard cleanup timed out")
		}
	})
	return cancel, done
}

// Only the synthetic token map needs locking; handlers must remain concurrent,
// as production is, so a slow child request cannot block root observation.
type ownedWorkflowAuth struct {
	mu     sync.RWMutex
	values fakeAuth
}

func (a *ownedWorkflowAuth) Authenticate(ctx context.Context, token string) (Identity, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.values.Authenticate(ctx, token)
}
func (a *ownedWorkflowAuth) set(token string, id Identity) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.values[token] = id
}
