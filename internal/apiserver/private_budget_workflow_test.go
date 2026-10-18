package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/commonrules"
	commonfixture "github.com/thaynes43/dev-env/internal/commonrules/testutil"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/taskbudget"
	"github.com/thaynes43/dev-env/internal/templates"
)

func privateWorkflowChild(t *testing.T, l *taskbudget.Ledger) taskbudget.Worker {
	t.Helper()
	for _, w := range l.Workers {
		if w.Managed != nil {
			return w
		}
	}
	t.Fatal("missing private worker")
	return taskbudget.Worker{}
}

// Real TLS API, real Kubernetes admission/storage and production controller/gates.
// Token verification is the existing synthetic identity fixture. Envtest supplies
// no kubelet/provider: the test writes independent Pod/Node/Lease observations.
func TestEnvtestPrivateBudgetDispatchReserveStopPreserve(t *testing.T) {
	ctx := context.Background()
	live, err := client.NewWithWatch(envConfig(t), client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{sessionNS, "dev-env-system", "private-budget-fixture", "kube-node-lease", "dev-agents"} {
		if err := live.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}
	}
	if err := live.Create(ctx, &schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: controller.PriorityClassName}, Value: -10, PreemptionPolicy: ptr.To(corev1.PreemptNever)}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	var clock atomic.Int64
	clock.Store(time.Now().UTC().UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	f := coordinatorFixture(t)
	f.srv.Client, f.srv.Live, f.srv.Now = live, live, now

	owned := prepareOwnedWorkflow(t, live, now)
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
	id := saIdentity("dev-agents/codex-host-a")
	id.PodName, id.PodUID = root.Name, string(root.UID)
	f.auth[tokCoordinator] = id
	f.srv.Policy.Coordinators[0].PodName = root.Name
	f.srv.Policy.Coordinators[0].ServiceAccount = "dev-agents/codex-host-a"
	catalog := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "dev-env-project-catalog", Namespace: "dev-env-system"}, Data: map[string]string{"catalog.json": apiProjectCatalog}}
	if err := live.Create(ctx, catalog); err != nil {
		t.Fatal(err)
	}
	f.srv.Projects = &CatalogBinding{Key: client.ObjectKeyFromObject(catalog), CloneOwner: "thaynes43"}
	f.srv.PrivateProjectTasks, f.srv.PrivateHistoryRetention, f.srv.RetainedCommonRules, f.srv.ManagedCodexTasks = true, true, true, true
	common, header := commonfixture.Sources("shared trusted rules", "codex header")
	for _, cm := range []*corev1.ConfigMap{common, header} {
		cm.UID = ""
		cm.ResourceVersion = ""
		if err := live.Create(ctx, cm); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := os.ReadFile("../templates/testdata/templates.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// All new provider selections are cheap IDs; no provider process is started.
	doc = bytes.ReplaceAll(doc, []byte("claude-opus-5-5"), []byte("claude-haiku-4-5"))
	f.tmpl, err = templates.Parse(map[string]string{templates.Key: string(doc)})
	if err != nil {
		t.Fatal(err)
	}
	template := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "private-budget-templates", Namespace: "dev-env-system"}, Data: map[string]string{templates.Key: string(doc)}}
	if err := live.Create(ctx, template); err != nil {
		t.Fatal(err)
	}
	service := &taskbudget.Service{Store: taskbudget.KubeStore{Client: live, Live: live, Namespace: "private-budget-fixture"}, Now: now, Validator: controller.ManagedBudgetEvidenceValidator{Reader: live, Now: now}, ManagedInspector: controller.ManagedPodAdmissionInspector{Reader: live, Templates: client.ObjectKeyFromObject(template), Image: f.tmpl.Image}}
	service.NativeInspector = owned.inspector
	service.Validator = controller.OwnedRootBudgetValidator{Root: owned.inspector, Managed: service.Validator}
	f.srv.TaskBudgets = service
	f.srv.AssignedTaskBudgets = map[string]string{"host-a": "campaign-a"}
	auth := &ownedWorkflowAuth{values: f.auth}
	f.srv.Auth = auth
	server := httptest.NewTLSServer(f.srv.Handler())
	defer server.Close()
	request := func(method, path, token string, body any, want int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		data, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s err=%v", method, path, response.StatusCode, want, data, err)
		}
		return data
	}

	remote := &apiv1.HTTPTaskBudgetGate{BaseURL: server.URL, Client: server.Client(), Token: func(context.Context) (string, error) { return tokCoordinator, nil }}
	clock.Store(time.Now().UTC().UnixNano())
	binding, err := remote.BootstrapOwned(ctx, apiv1.CreateTaskBudgetRequest{TaskUID: "campaign-a", Spec: owned.policy.Spec}, "host-a", string(root.UID))
	if err != nil {
		t.Fatal("bootstrap", err)
	}
	budget := apiv1.TaskBudgetStatus{Binding: binding}
	// A client-selected/local receipt is not production authority: before the
	// actual guard endpoint exists, admission must refuse without charging.
	request(http.MethodPost, apiv1.TaskBudgetPath(binding.TaskUID)+"/admit", tokCoordinator, apiv1.TaskBudgetRequest{Binding: binding}, http.StatusServiceUnavailable)
	stopRoot, rootDone := owned.run(t, remote, binding)
	readyBy := time.Now().Add(4 * time.Second)
	for {
		l, _, err := service.Store.Read(ctx, binding.TaskUID)
		if err == nil && l.NativeAdmitted() && owned.inspector.InspectNativeContinuation(ctx, l, binding) == nil {
			break
		}
		select {
		case err := <-rootDone:
			t.Fatal("guard exited before ready", err)
		default:
		}
		if time.Now().After(readyBy) {
			t.Fatal("original guard never became ready", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var raw []byte
	req := coordinatorTask()
	req.Model, req.Effort, req.IdempotencyKey = "claude-haiku-4-5", "", "private-budget-once"
	req.Limits = &apiv1.Limits{Timeout: "1m"}
	raw = request(http.MethodPost, apiv1.SessionsPath, tokCoordinator, req, http.StatusCreated)
	var created apiv1.Session
	if json.Unmarshal(raw, &created) != nil || created.UID == "" {
		t.Fatalf("child receipt %s", raw)
	}
	var s v1alpha1.AgentSession
	key := types.NamespacedName{Namespace: sessionNS, Name: created.Name}
	if err := live.Get(ctx, key, &s); err != nil {
		t.Fatal(err)
	}
	if s.Spec.Workspace != nil || s.Spec.CommonRules == nil {
		t.Fatal("private/common binding missing")
	}
	retained, err := commonrules.BoundSession(&s)
	if err != nil || retained.Confirm(ctx, live) != nil {
		t.Fatal("retained common source invalid", err)
	}
	// The actual API server has admitted the ordinary finite common-input schema.
	lost := errors.New("synthetic lost Pod-create acknowledgement")
	podCreates := 0
	writer := interceptor.NewClient(live, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			return c.Create(ctx, obj, opts...)
		}
		podCreates++
		ledger, _, err := service.Store.Read(ctx, budget.Binding.TaskUID)
		if err != nil || len(ledger.Workers) != 2 || !privateWorkflowChild(t, ledger).Managed.LaunchRequested || privateWorkflowChild(t, ledger).Managed.SessionUID != created.UID {
			t.Error("Pod create preceded ledger reservation", err)
		}
		pod.Spec.NodeName = "private-budget-worker"
		if err := c.Create(ctx, obj, opts...); err != nil {
			return err
		}
		return lost
	}})
	r := &controller.Reconciler{Client: writer, APIReader: live, Templates: client.ObjectKeyFromObject(template), APIURL: server.URL, PrivateHistoryRetention: true, PrivateTerminalRecord: true, RetainedCommonRules: true, ManagedCodexTasks: true, TaskBudgets: service, TaskBudgetWorkerImage: f.tmpl.Image, Now: now}
	step := func() {
		t.Helper()
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		if err != nil && !errors.Is(err, lost) {
			t.Fatal("reconcile", err)
		}
	}
	var pod corev1.Pod
	for attempt := 0; attempt < 6; attempt++ {
		step()
		if live.Get(ctx, key, &pod) == nil {
			break
		}
	}
	if pod.UID == "" || podCreates != 1 {
		_ = live.Get(ctx, key, &s)
		ledger, _, readErr := service.Store.Read(ctx, budget.Binding.TaskUID)
		t.Logf("ledger=%+v read=%v inspector=%v", ledger, readErr, owned.inspector.InspectNativeContinuation(ctx, ledger, budget.Binding))
		t.Fatalf("no single private launch: creates=%d session=%+v private=%+v", podCreates, s.Status, s.Status.PrivateHistory)
	}
	step()
	step() // Bind the acknowledged live Pod and retain original identity.
	if err := live.Get(ctx, key, &s); err != nil {
		t.Fatal(err)
	}
	if s.Status.PrivateHistory == nil || s.Status.PrivateHistory.LastExecutor == nil || s.Status.PrivateHistory.LastExecutor.PodUID != string(pod.UID) {
		t.Fatalf("original executor not retained: %+v", s.Status.PrivateHistory)
	}
	homeKey := client.ObjectKey{Namespace: s.Namespace, Name: controller.HomeClaimName(s.Name)}
	var home corev1.PersistentVolumeClaim
	if err := live.Get(ctx, homeKey, &home); err != nil {
		t.Fatal(err)
	}
	homeUID, homeRV := home.UID, home.ResourceVersion
	if pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 30 || *pod.Spec.TerminationGracePeriodSeconds != 5 {
		t.Fatal("finite Pod deadlines missing")
	}
	var timer protocol.TaskBudgetDeadline
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == protocol.TaskBudgetEnv {
			if json.Unmarshal([]byte(e.Value), &timer) != nil {
				t.Fatal("timer JSON")
			}
		}
	}
	if !timer.CampaignDeadline.Equal(budget.Binding.Deadline) || !timer.Deadline.Equal(now().Add(30*time.Second)) {
		t.Fatalf("root/local deadlines changed: %+v", timer)
	}
	childIdentity := saIdentity(sessionNS + "/" + controller.ServiceAccountName)
	childIdentity.PodName, childIdentity.PodUID = pod.Name, string(pod.UID)
	auth.set("private-child-token", childIdentity)
	gate := &apiv1.HTTPTaskBudgetGate{BaseURL: server.URL, Client: server.Client(), Token: func(context.Context) (string, error) { return "private-child-token", nil }}
	if latch, err := gate.Observe(ctx, budget.Binding); err != nil || latch {
		t.Fatal("live private worker could not observe", latch, err)
	}
	stopRoot()
	select {
	case err := <-rootDone:
		if err != nil {
			t.Fatal("original root stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("root stop timeout")
	}
	// Return completion to cleanup without hiding a second execution.

	ledgerAfterRoot, _, err := service.Store.Read(ctx, budget.Binding.TaskUID)
	if err != nil || !ledgerAfterRoot.Latched || ledgerAfterRoot.Reason != "NativeRootStopped" || !privateWorkflowChild(t, ledgerAfterRoot).Active {
		t.Fatal("root stop did not latch while child stayed charged", err)
	}
	clock.Store(now().Add(31 * time.Second).UnixNano())
	if latch, err := gate.Observe(ctx, budget.Binding); !latch {
		t.Fatal("checkpoint did not stop private observer", err)
	}
	// A stale node lease must not uncharge even a terminal Pod.
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: pod.Spec.NodeName}}
	if err := live.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	if err := live.Status().Update(ctx, node); err != nil {
		t.Fatal(err)
	}
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: node.Name, Namespace: "kube-node-lease", OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: node.Name, UID: node.UID}}}, Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(node.Name), RenewTime: &metav1.MicroTime{Time: now().Add(-90 * time.Second)}}}
	if err := live.Create(ctx, lease); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodFailed
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: controller.ContainerName, Image: pod.Spec.Containers[0].Image, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 124, FinishedAt: metav1.NewTime(now())}}}}
	if err := live.Status().Update(ctx, &pod); err != nil {
		t.Fatal(err)
	}
	step()
	step()
	ledger, _, err := service.Store.Read(ctx, budget.Binding.TaskUID)
	if err != nil || !privateWorkflowChild(t, ledger).Active {
		t.Fatal("stale stop evidence released worker", err)
	}
	lease.Spec.RenewTime = &metav1.MicroTime{Time: now().Add(-time.Second)}
	if err := live.Update(ctx, lease); err != nil {
		t.Fatal(err)
	}
	step()
	step()
	ledger, _, err = service.Store.Read(ctx, budget.Binding.TaskUID)
	if err != nil || privateWorkflowChild(t, ledger).Active || !ledger.Latched {
		if ledger != nil && len(ledger.Workers) > 0 && privateWorkflowChild(t, ledger).Managed != nil {
			w := privateWorkflowChild(t, ledger)
			m := w.Managed
			event := apiv1.TaskBudgetEvent{Binding: ledger.Binding, ID: "diagnostic-stop", Kind: "worker-stop", WorkerID: w.ID, Evidence: apiv1.TaskBudgetEvidence{ID: "diagnostic", Kind: "private-retained-stop", Reference: m.Namespace + "/" + m.Name + "/" + m.SessionUID + "/" + m.PodUID}}
			t.Logf("direct proof: %v managed=%+v", service.Validator.Validate(ctx, ledger, event), m)
		}
		t.Fatalf("verified private stop not retained: %+v %v", ledger, err)
	}
	if err := live.Get(ctx, homeKey, &home); err != nil || home.UID != homeUID || home.ResourceVersion != homeRV {
		t.Fatal("original HOME changed", err)
	}
	if err := live.Get(ctx, key, &pod); err != nil || podCreates != 1 {
		t.Fatal("original executor replaced/deleted", err)
	}
	request(http.MethodPost, apiv1.SessionResumePath(s.Name), tokCoordinator, nil, http.StatusConflict)
	// A later missing executor remains final; no second Create is authorized.
	if err := live.Delete(ctx, &pod, client.GracePeriodSeconds(0)); err != nil {
		t.Fatal(err)
	}
	step()
	step()
	if podCreates != 1 {
		t.Fatal("missing executor rearmed launch")
	}
	if err := live.Get(ctx, homeKey, &home); err != nil || home.UID != homeUID {
		t.Fatal("missing executor lost HOME", err)
	}
	t.Log("model-free actual guard/peer/RPC/operator-inspector/TLS/envtest: private dispatch, reservation-before-create, lost ACK with one Pod, exact root/local timers, latch, stale proof refusal, verified stop, retained HOME/Pod, denied resume and no replay; zero provider actors")
}
