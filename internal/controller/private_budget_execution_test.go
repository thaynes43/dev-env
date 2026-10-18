package controller

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/commonrules"
	commonfixture "github.com/thaynes43/dev-env/internal/commonrules/testutil"
	"github.com/thaynes43/dev-env/internal/taskbudget"
	"github.com/thaynes43/dev-env/internal/templates"
)

type privateRootAdmissionFixture struct{}

func (privateRootAdmissionFixture) InspectNativeAdmission(_ context.Context, l *taskbudget.Ledger, b taskbudget.Binding) error {
	if l.Binding != b || l.Parent != "system/parent" {
		return taskbudget.ErrDenied
	}
	return nil
}
func privateLaunchWorker(t *testing.T, l *taskbudget.Ledger) taskbudget.Worker {
	t.Helper()
	for _, w := range l.Workers {
		if w.Managed != nil {
			return w
		}
	}
	t.Fatal("missing private worker")
	return taskbudget.Worker{}
}

func privateLaunchFixture(t *testing.T, scenario string) (*taskbudget.Service, taskbudget.Binding, *v1alpha1.AgentSession, client.WithWatch) {
	t.Helper()
	f := newPrivateBudgetFixture(t, "claude")
	s := f.s
	s.Spec.ProjectCanary, s.Spec.SessionDirectory = nil, nil
	s.Status.SessionDirectory = nil
	s.Spec.Profile, s.Spec.Model, s.Spec.Effort = "dev", "claude-haiku-4-5", ""
	s.Spec.Limits = &v1alpha1.SessionLimits{Timeout: &metav1.Duration{Duration: time.Minute}}
	s.Status.PrivateHistory.LastExecutor = nil
	common, header := commonfixture.Sources("shared fixture rules", "codex fixture header")
	snapshot := commonfixture.Snapshot(t, []byte(s.Annotations[v1alpha1.AnnotationProjectSnapshot]), "shared fixture rules", "codex fixture header")
	var err error
	s.Spec.CommonRules, err = snapshot.Binding()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile("../templates/testdata/templates.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := templates.Parse(map[string]string{templates.Key: string(doc)})
	if err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "budget-templates", Namespace: "dev-env-system"}, Data: map[string]string{templates.Key: string(doc)}}
	objects := []client.Object{s, cm, common, header}
	switch scenario {
	case "replaced-home":
		f.home.UID = "replacement"
	case "changed-common":
		common.Data[commonrules.CommonKey] = "replacement rules"
	case "replaced-common":
		common.UID = "replacement-common"
	case "stopped":
		s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	case "missing-common-binding":
		s.Spec.CommonRules = nil
	case "existing-executor":
		s.Status.PrivateHistory.LastExecutor = &v1alpha1.PrivateExecutorStatus{PodUID: "original", PodName: s.Name, Generation: s.Generation}
	case "unconfirmed-intent":
		s.Status.PrivateHistory.ExecutorNotCreated = true
	}
	if scenario != "missing-home" {
		objects = append(objects, f.home)
	}
	if scenario == "pod-present" {
		objects = append(objects, f.pod)
	}
	c := fake.NewClientBuilder().WithScheme(f.client.Scheme()).WithStatusSubresource(&v1alpha1.AgentSession{}).WithObjects(objects...).Build()
	svc, b := budgetService(t, c, f.now)
	svc.NativeInspector = privateRootAdmissionFixture{}
	if scenario != "root-unadmitted" {
		if _, err := svc.Admit(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range budgetAnnotations(b, "child:finite") {
		s.Annotations[k] = v
	}
	if err := c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Record(context.Background(), apiv1.TaskBudgetEvent{Binding: b, ID: "dispatch:finite", Kind: "worker-start", WorkerID: "child:finite"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindManagedWorker(context.Background(), b, "child:finite", taskbudget.ManagedWorkerRef{Namespace: s.Namespace, Name: s.Name, SessionUID: string(s.UID)}); err != nil {
		t.Fatal(err)
	}
	svc.ManagedInspector = ManagedPodAdmissionInspector{Reader: c, Templates: client.ObjectKeyFromObject(cm), Image: tmpl.Image}
	return svc, b, s, c
}

func TestPrivateManagedLaunchRequiresOriginalPreCreateAuthority(t *testing.T) {
	for _, scenario := range []string{"ready", "root-unadmitted", "missing-home", "replaced-home", "changed-common", "replaced-common", "stopped", "missing-common-binding", "existing-executor", "unconfirmed-intent", "pod-present"} {
		t.Run(scenario, func(t *testing.T) {
			svc, b, _, _ := privateLaunchFixture(t, scenario)
			_, err := svc.ReserveManagedLaunch(context.Background(), b, "child:finite")
			if (err == nil) != (scenario == "ready") {
				t.Fatalf("reservation %s: %v", scenario, err)
			}
			l, _, err := svc.Store.Read(context.Background(), b.TaskUID)
			if err != nil {
				t.Fatal(err)
			}
			if !privateLaunchWorker(t, l).Active || privateLaunchWorker(t, l).Managed.LaunchRequested != (scenario == "ready") {
				t.Fatalf("refusal changed charge/launch authority: %+v", privateLaunchWorker(t, l))
			}
			if scenario == "ready" {
				if _, err := svc.ReserveManagedLaunch(context.Background(), b, "child:finite"); !errors.Is(err, taskbudget.ErrDenied) {
					t.Fatal("reservation replayed", err)
				}
			}
		})
	}
}

func TestPrivateLaunchUnknownACKKeepsReservationAndCannotProveNoLaunch(t *testing.T) {
	svc, b, s, c := privateLaunchFixture(t, "ready")
	original := svc.Store
	unknown := &privateBudgetUnknownACKStore{Store: original}
	svc.Store = unknown
	if _, err := svc.ReserveManagedLaunch(context.Background(), b, "child:finite"); !errors.Is(err, taskbudget.ErrUnavailable) {
		t.Fatal("lost ACK confirmed", err)
	}
	if unknown.updates != 1 {
		t.Fatal("unknown write replayed", unknown.updates)
	}
	svc.Store = original
	if _, err := svc.ReserveManagedLaunch(context.Background(), b, "child:finite"); !errors.Is(err, taskbudget.ErrDenied) {
		t.Fatal("unknown launch rearmed", err)
	}
	s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if err := c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: c, APIReader: c, TaskBudgets: svc}
	if err := r.recordTaskBudgetNeverLaunched(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	l, _, err := original.Read(context.Background(), b.TaskUID)
	if err != nil || !privateLaunchWorker(t, l).Active || !privateLaunchWorker(t, l).Managed.LaunchRequested {
		t.Fatal("absence erased unknown launch", err)
	}
}

// A current healthy Node cannot convert synthetic PodGC status or a stale
// retained Pod snapshot into independent proof that the original executor ended.
func TestPrivateBudgetStopRequiresKubeletProofAndStableOriginalPod(t *testing.T) {
	for _, scenario := range []string{"ready", "podgc", "replaced-during-observation", "deleted-during-observation"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTerminalFixture()
			f.s.Spec.Mode, f.s.Spec.Profile = v1alpha1.ModeTask, "dev"
			f.s.Spec.Limits = &v1alpha1.SessionLimits{Timeout: &metav1.Duration{Duration: time.Minute}}
			snapshot := commonfixture.Snapshot(t, []byte("{}"), "rules", "header")
			var err error
			f.s.Spec.CommonRules, err = snapshot.Binding()
			if err != nil {
				t.Fatal(err)
			}
			f.s.Annotations = map[string]string{}
			for k, v := range budgetAnnotations(taskbudget.Binding{TaskUID: "campaign", Epoch: 1, Deadline: f.now.Add(time.Minute), HostID: "root", PodUID: "root-pod"}, "child:finite") {
				f.s.Annotations[k] = v
			}
			f.pod.Spec.RestartPolicy = corev1.RestartPolicyNever
			if scenario == "podgc" {
				f.pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue, Reason: podGCDeletionReason}}
			}
			c := f.client()
			podReads := 0
			reader := interceptor.NewClient(c, interceptor.Funcs{Get: func(ctx context.Context, live client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Pod); ok {
					podReads++
					if podReads > 1 && scenario == "deleted-during-observation" {
						return apierrors.NewNotFound(corev1.Resource("pods"), key.Name)
					}
				}
				err := live.Get(ctx, key, obj, opts...)
				if pod, ok := obj.(*corev1.Pod); ok && err == nil && podReads > 1 && scenario == "replaced-during-observation" {
					pod.UID = "replacement"
				}
				return err
			}})
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.s), f.s); err != nil {
				t.Fatal(err)
			}
			_, err = privateBudgetStop(context.Background(), reader, f.s, f.pod.UID, func() time.Time { return f.now })
			if (err == nil) != (scenario == "ready") {
				t.Fatalf("%s: %v", scenario, err)
			}
		})
	}
}
