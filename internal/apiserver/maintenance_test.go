package apiserver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/broker"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
)

type maintenanceExec struct {
	args     []string
	pod      string
	err      error
	deadline time.Time
}

func (e *maintenanceExec) Run(ctx context.Context, _, pod, _ string, args []string, _ io.Reader, out, _ io.Writer) error {
	e.args = args
	e.pod = pod
	e.deadline, _ = ctx.Deadline()
	_, _ = io.WriteString(out, "synthetic output")
	return e.err
}

func maintenanceAPI(t *testing.T) (*fixture, string, string, apiv1.CreateGrantRequest) {
	t.Helper()
	f := newFixtureWith(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if obj.GetUID() == "" {
			obj.SetUID(uuid.NewUUID())
		}
		return c.Create(ctx, obj, opts...)
	}})
	f.srv.MaintenanceEnabled = true
	f.h = f.srv.Handler()
	parent := f.sessionPod("maintenance-parent", "full", 0)
	worker := f.sessionPod("maintenance-worker", "full", 1)
	ctx := context.Background()
	var ids []apiv1.MaintenanceIdentity
	for _, name := range []string{"maintenance-parent", "maintenance-worker"} {
		sess := f.session(name)
		if name == "maintenance-worker" {
			sess.Spec.Parent = "maintenance-parent"
			if err := f.c.Update(ctx, sess); err != nil {
				t.Fatal(err)
			}
		}
		var p corev1.Pod
		if err := f.c.Get(ctx, client.ObjectKeyFromObject(sess), &p); err != nil {
			t.Fatal(err)
		}
		p.Status.Phase = corev1.PodRunning
		if err := f.c.Status().Update(ctx, &p); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, apiv1.MaintenanceIdentity{Name: name, UID: string(sess.UID), PodUID: string(p.UID)})
	}
	if err := f.c.Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "fixture-node", UID: "fixture-node-uid"}}); err != nil {
		t.Fatal(err)
	}
	m := &apiv1.MaintenanceTask{Coordinator: ids[0], Workers: ids[1:], Node: "fixture-node", NodeUID: "fixture-node-uid", Resource: "fixture-device", Operation: "bounded device maintenance", Capabilities: []string{"SYS_ADMIN"}, Devices: []string{"/dev/nvidia0"}, Image: "registry.example/tool@sha256:" + strings.Repeat("a", 64), CPUMillis: 100, MemoryMiB: 128, Restore: "restore authorized original state and read back"}
	return f, parent, worker, apiv1.CreateGrantRequest{Type: "maintenance", Maintenance: m, Reason: "synthetic maintenance"}
}

func TestMaintenanceAPIDefaultOffAndApproval(t *testing.T) {
	f, parent, _, req := maintenanceAPI(t)
	f.srv.MaintenanceEnabled = false
	f.h = f.srv.Handler()
	if w := f.do(http.MethodPost, apiv1.GrantsPath, parent, req); w.Code != http.StatusForbidden {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, suffix := range []string{"maintenance-decision", "maintenance-exec"} {
		if got := f.do(http.MethodPost, apiv1.GrantPath("grant-x")+"/"+suffix, parent, map[string]any{}); got.Code != http.StatusNotFound {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	f.srv.MaintenanceEnabled = true
	f.h = f.srv.Handler()
	got := f.request(parent, req, http.StatusCreated)
	if got.TTL != "30m0s" || got.Maintenance == nil || got.UID == "" || got.MaintenanceDigest == "" {
		t.Fatal(got)
	}
	decision := apiv1.MaintenanceDecisionRequest{GrantUID: got.UID, Digest: got.MaintenanceDigest, Approve: true}
	route := apiv1.GrantPath(got.Name) + "/maintenance-decision"
	for _, tok := range []string{parent, tokClient, tokStranger} {
		if w := f.do(http.MethodPost, route, tok, decision); w.Code != http.StatusForbidden {
			t.Fatal(tok, w.Code, w.Body.String())
		}
	}
	stale := decision
	stale.GrantUID = "old"
	if w := f.do(http.MethodPost, route, tokHuman, stale); w.Code != http.StatusConflict {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := f.do(http.MethodPost, route, tokHuman, decision); w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := f.do(http.MethodPost, route, tokHuman, decision); w.Code != http.StatusConflict {
		t.Fatal("replay", w.Code, w.Body.String())
	}
	g := f.grant(got.Name)
	if !strings.Contains(g.Annotations[broker.MaintenanceDecisionAnnotation], "human/dev-env-system/dev-env-human") {
		t.Fatal("approver was not authenticated human")
	}
	if g.Status.Phase == v1alpha1.GrantActive {
		t.Fatal("operator must not approve status directly")
	}
	// Actual operator annotation -> production broker reconcile -> Decide path.
	b := &broker.Broker{Client: f.c, APIReader: f.c, SessionNamespace: sessionNS, PolicyNamespace: "dev-env-system", EnableMaintenanceGrants: true, Clock: clocktesting.NewFakeClock(f.now)}
	for range 2 {
		if _, err := b.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g)}); err != nil {
			t.Fatal(err)
		}
	}
	if approved := f.grant(g.Name); approved.Status.Phase != v1alpha1.GrantActive || approved.Status.ApprovedBy != "human/dev-env-system/dev-env-human" {
		t.Fatal(approved.Status)
	}

}

func TestMaintenanceAPICommandFreedomAndIdentityFences(t *testing.T) {
	f, parent, worker, req := maintenanceAPI(t)
	got := f.request(parent, req, http.StatusCreated)
	ctx := context.Background()
	g := f.grant(got.Name)
	g.Status.Phase = v1alpha1.GrantActive
	g.Status.ExpiresAt = &metav1.Time{Time: f.now.Add(30 * time.Minute)}
	p := broker.MaintenancePod(g, f.now)
	p.UID = "fixture-executor-uid"
	if err := f.c.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Status.Phase = corev1.PodRunning
	if err := f.c.Status().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	g.Status.Maintenance = &v1alpha1.MaintenanceStatus{PodUID: string(p.UID), Executor: "Running"}
	if err := f.c.Status().Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	ex := &maintenanceExec{}
	f.srv.Exec = ex
	path := apiv1.GrantPath(g.Name) + "/maintenance-exec"
	for _, command := range [][]string{{"/bin/sh", "-c", "printf chosen-command"}, {"nvidia-smi", "--query-gpu=uuid", "--format=csv"}} {
		if w := f.do(http.MethodPost, path, worker, apiv1.MaintenanceCommand{Command: command}); w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		if !reflect.DeepEqual(ex.args, command) || ex.pod != g.Name || !ex.deadline.Equal(g.Status.ExpiresAt.Time) {
			t.Fatal("command changed or routed outside lease", ex)
		}
	}
	for _, tok := range []string{tokClient, tokHuman, f.sessionPod("unrelated", "full", 0)} {
		if w := f.do(http.MethodPost, path, tok, apiv1.MaintenanceCommand{Command: []string{"true"}}); w.Code != http.StatusForbidden {
			t.Fatal(tok, w.Code, w.Body.String())
		}
	}
	ex.err = errors.New("restore failed")
	if w := f.do(http.MethodPost, path, parent, apiv1.MaintenanceCommand{Command: []string{"restore-tool"}, RestoreEvidence: "readback mismatch"}); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if !strings.Contains(f.grant(g.Name).Annotations[broker.MaintenanceRestoreAnnotation], "success=false") {
		t.Fatal("restore failure not distinguished")
	}
	// Authoritative parent change refuses a previously approved worker.
	sess := f.session("maintenance-worker")
	sess.Spec.Parent = "other"
	if err := f.c.Update(ctx, sess); err != nil {
		t.Fatal(err)
	}
	ex.args = nil
	if w := f.do(http.MethodPost, path, worker, apiv1.MaintenanceCommand{Command: []string{"true"}}); w.Code != http.StatusForbidden || ex.args != nil {
		t.Fatal("wrong parent accepted", w.Code)
	}
	sess.Spec.Parent = "maintenance-parent"
	if err := f.c.Update(ctx, sess); err != nil {
		t.Fatal(err)
	}
	// Pod replacement refuses the otherwise same worker identity.
	var pod corev1.Pod
	k := types.NamespacedName{Namespace: sessionNS, Name: sess.Name}
	if err := f.c.Get(ctx, k, &pod); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(ctx, &pod); err != nil {
		t.Fatal(err)
	}
	pod.UID = "new-worker-pod"
	pod.ResourceVersion = ""
	if err := f.c.Create(ctx, &pod); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodPost, path, worker, apiv1.MaintenanceCommand{Command: []string{"true"}}); w.Code != http.StatusForbidden {
		t.Fatal("replacement accepted", w.Code)
	}
}
