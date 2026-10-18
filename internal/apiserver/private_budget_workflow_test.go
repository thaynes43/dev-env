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
	"sync"
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

// Independent model-free root-readiness fixture. No native/provider readiness
// is claimed; production has no general native inspector supplied by this test.
type privateWorkflowRootInspector struct {
	reader client.Reader
	key    client.ObjectKey
	uid    types.UID
}

func (i privateWorkflowRootInspector) InspectNativeAdmission(ctx context.Context, l *taskbudget.Ledger, b taskbudget.Binding) error {
	var pod corev1.Pod
	if l.Binding != b || l.Parent != coordinatorSA || i.reader.Get(ctx, i.key, &pod) != nil || pod.UID != i.uid || string(pod.UID) != b.PodUID || !pod.DeletionTimestamp.IsZero() || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != "synthetic-model-free-host" {
		return taskbudget.ErrDenied
	}
	return nil
}
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
	for _, name := range []string{sessionNS, "dev-env-system", "private-budget-fixture", "kube-node-lease"} {
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
	root := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "private-budget-host", Namespace: "dev-env-system", Annotations: map[string]string{CoordinatorHostAnnotation: "host-a"}}, Spec: corev1.PodSpec{ServiceAccountName: "codex-host-a", Containers: []corev1.Container{{Name: "host", Image: "synthetic-model-free-host"}}}}
	if err := live.Create(ctx, root); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(coordinatorSA)
	id.PodName, id.PodUID = root.Name, string(root.UID)
	f.auth[tokCoordinator] = id
	f.srv.Policy.Coordinators[0].PodName = root.Name
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
	service.NativeInspector = privateWorkflowRootInspector{reader: live, key: client.ObjectKeyFromObject(root), uid: root.UID}
	f.srv.TaskBudgets = service
	f.srv.AssignedTaskBudgets = map[string]string{"host-a": "campaign-a"}
	var serverMu sync.Mutex
	handler := f.srv.Handler()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverMu.Lock()
		defer serverMu.Unlock()
		handler.ServeHTTP(w, r)
	}))
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
	raw := request(http.MethodPost, apiv1.TaskBudgetsPath, tokCoordinator, apiv1.CreateTaskBudgetRequest{TaskUID: "campaign-a", Spec: apiv1.TaskBudgetSpec{SuccessCondition: "private HOME retained after bounded stop", OverallSeconds: 120, EffortSeconds: 180, CheckpointSeconds: 30}}, http.StatusCreated)
	var budget apiv1.TaskBudgetStatus
	if json.Unmarshal(raw, &budget) != nil {
		t.Fatal("budget receipt")
	}
	request(http.MethodPost, apiv1.TaskBudgetPath(budget.Binding.TaskUID)+"/admit", tokCoordinator, apiv1.TaskBudgetRequest{Binding: budget.Binding}, http.StatusOK)
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
		t.Fatalf("no single private launch: creates=%d session=%+v", podCreates, s.Status)
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
	serverMu.Lock()
	f.auth["private-child-token"] = childIdentity
	serverMu.Unlock()
	gate := &apiv1.HTTPTaskBudgetGate{BaseURL: server.URL, Client: server.Client(), Token: func(context.Context) (string, error) { return "private-child-token", nil }}
	if latch, err := gate.Observe(ctx, budget.Binding); err != nil || latch {
		t.Fatal("live private worker could not observe", latch, err)
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
	t.Log("model-free TLS/envtest: private dispatch, reservation-before-create, lost ACK with one Pod, exact root/local timers, latch, stale proof refusal, verified stop, retained HOME/Pod, denied resume and no replay; zero provider actors")
}
