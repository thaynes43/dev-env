package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/commonrules"
	commonfixture "github.com/thaynes43/dev-env/internal/commonrules/testutil"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

func TestPrivateBudgetDispatchRequiresNativeRootAndCompleteInputs(t *testing.T) {
	for _, scenario := range []string{"unadmitted-root", "native-authority-lost", "fixed-model-free-root", "missing-common", "timeout-too-long", "missing-home-retention", "oversize-common-document"} {
		t.Run(scenario, func(t *testing.T) {
			f := coordinatorFixture(t)
			bindCatalogFixture(t, f)
			f.srv.PrivateHistoryRetention, f.srv.RetainedCommonRules = true, true
			a, b := commonfixture.Sources("fixture common rules", "fixture header")
			if scenario == "oversize-common-document" {
				a.Data[commonrules.CommonKey] = strings.Repeat("\x01", commonrules.MaxCommonBytes)
			}
			for _, cm := range []*corev1.ConfigMap{a, b} {
				cm.ResourceVersion = ""
				if err := f.c.Create(context.Background(), cm); err != nil {
					t.Fatal(err)
				}
			}
			host := "host-a"
			if scenario == "fixed-model-free-root" {
				host = taskbudget.NativeFixtureHostID
				f.srv.Policy.Coordinators[0].HostID = host
				var pod corev1.Pod
				if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: "dev-env-system", Name: "codex-host-a-0"}, &pod); err != nil {
					t.Fatal(err)
				}
				pod.Annotations[CoordinatorHostAnnotation] = host
				if err := f.c.Update(context.Background(), &pod); err != nil {
					t.Fatal(err)
				}
			}
			f.srv.AssignedTaskBudgets = map[string]string{host: "campaign-a"}
			svc := &taskbudget.Service{Store: taskbudget.KubeStore{Client: f.c, Live: f.c, Namespace: "budget-system"}, Now: f.srv.Now, Validator: apiBudgetValidator{}, NativeInspector: apiBudgetValidator{}}
			f.srv.TaskBudgets = svc
			w := f.do(http.MethodPost, apiv1.TaskBudgetsPath, tokCoordinator, apiv1.CreateTaskBudgetRequest{TaskUID: "campaign-a", Spec: apiv1.TaskBudgetSpec{SuccessCondition: "fixture", OverallSeconds: 120, EffortSeconds: 180, CheckpointSeconds: 30}})
			if w.Code != http.StatusCreated {
				t.Fatal(w.Code, w.Body.String())
			}
			binding := decode[apiv1.TaskBudgetStatus](t, w).Binding
			if scenario != "unadmitted-root" {
				if _, err := svc.Admit(context.Background(), binding); err != nil {
					t.Fatal(err)
				}
			}
			req := coordinatorTask()
			req.Model, req.Effort = "claude-haiku-4-5", ""
			req.Limits = &apiv1.Limits{Timeout: "1m"}
			req.IdempotencyKey = "refused-private-document"
			switch scenario {
			case "native-authority-lost":
				svc.NativeInspector = nil
			case "missing-common":
				if err := f.c.Delete(context.Background(), a); err != nil {
					t.Fatal(err)
				}
			case "timeout-too-long":
				req.Limits.Timeout = "46m"
			case "missing-home-retention":
				f.srv.PrivateHistoryRetention = false
			}
			w = f.do(http.MethodPost, apiv1.SessionsPath, tokCoordinator, req)
			if w.Code >= 200 && w.Code < 300 {
				t.Fatal("incomplete private admission accepted", w.Body.String())
			}
			if scenario == "oversize-common-document" {
				if w.Code != http.StatusUnprocessableEntity {
					t.Fatal("document refusal did not reach deterministic validation", w.Code, w.Body.String())
				}
				retry := f.do(http.MethodPost, apiv1.SessionsPath, tokCoordinator, req)
				if retry.Code != w.Code || !strings.Contains(retry.Body.String(), "bounded transport") {
					t.Fatal("deterministic refusal became an unknown charged dispatch", retry.Code, retry.Body.String())
				}
			}
			var sessions v1alpha1.AgentSessionList
			if err := f.c.List(context.Background(), &sessions); err != nil || len(sessions.Items) != 0 {
				t.Fatal("refusal created a child", err)
			}
			l, _, err := svc.Store.Read(context.Background(), binding.TaskUID)
			if err != nil || len(l.Events) != 0 {
				t.Fatal("refusal reserved/erased child history", err)
			}
			for _, worker := range l.Workers {
				if worker.Managed != nil {
					t.Fatal("refusal reserved a private worker")
				}
			}
		})
	}
}
