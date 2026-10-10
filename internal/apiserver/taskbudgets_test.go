package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

type apiBudgetValidator struct{}

func (apiBudgetValidator) Validate(_ context.Context, _ *taskbudget.Ledger, e apiv1.TaskBudgetEvent) error {
	// Supervised fixture receipts have one canonical unresolved checkpoint.
	known := map[string]string{"fixture:attempt-a": "attempt-a", "fixture:attempt-b": "attempt-b", "fixture:attempt-c": "attempt-c"}
	attempt, ok := known[e.Evidence.Reference]
	if e.Kind != "failure" || !ok || e.AttemptID != attempt || e.BlockerID != "same-blocker" {
		return taskbudget.ErrDenied
	}
	return nil
}

func (apiBudgetValidator) ValidateOwnerDecision(context.Context, *taskbudget.Ledger, apiv1.TaskBudgetExtension, string) error {
	return errors.New("native owner decision is not integrated")
}

func budgetFixture(t *testing.T) (*fixture, apiv1.TaskBudgetBinding) {
	t.Helper()
	f := coordinatorFixture(t)
	f.srv.AssignedTaskBudgets = map[string]string{"host-a": "campaign-a"}
	f.srv.TaskBudgets = &taskbudget.Service{Store: taskbudget.KubeStore{Client: f.c, Live: f.c, Namespace: "budget-system"}, Now: func() time.Time { return f.now }}
	f.srv.TaskBudgets.Validator = apiBudgetValidator{}
	w := f.do(http.MethodPost, apiv1.TaskBudgetsPath, tokCoordinator, apiv1.CreateTaskBudgetRequest{TaskUID: "campaign-a", Spec: apiv1.TaskBudgetSpec{SuccessCondition: "verified result", OverallSeconds: 2700, EffortSeconds: 3600, CheckpointSeconds: 600}})
	if w.Code != http.StatusCreated {
		t.Fatalf("create budget: %d %s", w.Code, w.Body.String())
	}
	var st apiv1.TaskBudgetStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	return f, st.Binding
}

func TestBudgetAPIExactLiveHostBindingAndDisabledDefault(t *testing.T) {
	for _, scenario := range []string{"ready", "unknown-campaign", "unavailable", "old-pod", "later-deadline", "client", "extension-by-agent"} {
		t.Run(scenario, func(t *testing.T) {
			f, b := budgetFixture(t)
			token := tokCoordinator
			path := apiv1.TaskBudgetPath(b.TaskUID) + "/admit"
			switch scenario {
			case "unknown-campaign":
				f.srv.AssignedTaskBudgets["host-a"] = "other"
			case "unavailable":
				f.srv.TaskBudgets = nil
			case "old-pod":
				b.PodUID = "old-pod"
			case "later-deadline":
				b.Deadline = b.Deadline.Add(time.Hour)
			case "client":
				token = tokClient
			case "extension-by-agent":
				wantError(t, f.do(http.MethodPost, apiv1.TaskBudgetPath(b.TaskUID)+"/extend", token, apiv1.TaskBudgetExtension{Binding: b}), http.StatusForbidden, apiv1.CodeForbidden)
				return
			}
			w := f.do(http.MethodPost, path, token, apiv1.TaskBudgetRequest{Binding: b})
			if scenario == "ready" {
				if w.Code != http.StatusOK {
					t.Fatal(w.Body.String())
				}
				var st apiv1.TaskBudgetStatus
				if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
					t.Fatal(err)
				}
				if !st.Observed || !st.Admitted || st.Binding != b {
					t.Fatal("positive exact admission missing")
				}
			} else if w.Code == http.StatusOK {
				t.Fatalf("%s admitted", scenario)
			}
		})
	}
	f := coordinatorFixture(t)
	w := f.do(http.MethodPost, apiv1.TaskBudgetsPath, tokCoordinator, apiv1.CreateTaskBudgetRequest{TaskUID: "campaign-a"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("default server enabled budget route")
	}
	f = coordinatorFixture(t)
	f.srv.AssignedTaskBudgets = map[string]string{"host-a": "campaign-a"}
	f.srv.TaskBudgets = &taskbudget.Service{Store: taskbudget.KubeStore{Client: f.c, Live: f.c, Namespace: "budget-system"}, Now: func() time.Time { return f.now }}
	w = f.do(http.MethodPost, apiv1.TaskBudgetsPath, tokCoordinator, apiv1.CreateTaskBudgetRequest{TaskUID: "campaign-a", Spec: apiv1.TaskBudgetSpec{SuccessCondition: "result", OverallSeconds: 2701, EffortSeconds: 3600, CheckpointSeconds: 600}})
	if w.Code != http.StatusBadRequest {
		t.Fatal("executor widened assigned initial budget")
	}
}

func TestBudgetAPIStatusRecoversExactBinding(t *testing.T) {
	f, b := budgetFixture(t)
	w := f.do(http.MethodGet, apiv1.TaskBudgetPath(b.TaskUID), tokCoordinator, nil)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	st := decode[apiv1.TaskBudgetStatus](t, w)
	if !st.Observed || st.Admitted || st.Binding != b {
		t.Fatal("status invented admission or lost exact binding")
	}
	f.now = f.now.Add(10 * time.Minute)
	w = f.do(http.MethodGet, apiv1.TaskBudgetPath(b.TaskUID), tokCoordinator, nil)
	st = decode[apiv1.TaskBudgetStatus](t, w)
	if w.Code != http.StatusOK || !st.Latched || st.Reason != "Checkpoint" {
		t.Fatal("status did not persist due checkpoint")
	}
}

func TestBudgetAPILatchBeforeDispatchResumeAndContinuation(t *testing.T) {
	f, b := budgetFixture(t)
	bindCatalogFixture(t, f)
	if w := f.do(http.MethodPost, apiv1.TaskBudgetPath(b.TaskUID)+"/admit", tokCoordinator, apiv1.TaskBudgetRequest{Binding: b}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	for i, id := range []string{"attempt-a", "attempt-b", "attempt-c"} {
		w := f.do(http.MethodPost, apiv1.TaskBudgetPath(b.TaskUID)+"/events", tokCoordinator, apiv1.TaskBudgetEvent{Binding: b, ID: id, Kind: "failure", AttemptID: id, BlockerID: "same-blocker", Evidence: apiv1.TaskBudgetEvidence{ID: "receipt:" + id, Kind: "supervised-failure", Reference: "fixture:" + id}})
		if w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
		if i == 2 {
			var st apiv1.TaskBudgetStatus
			if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
				t.Fatal(err)
			}
			if !st.Latched || st.Escalation == nil {
				t.Fatal("third failure did not persist latch")
			}
		}
	}
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokCoordinator, coordinatorTask())
	if w.Code != http.StatusConflict {
		t.Fatalf("latched child dispatch admitted: %d %s", w.Code, w.Body.String())
	}
	var noChildren v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &noChildren); err != nil || len(noChildren.Items) != 0 {
		t.Fatal("stopped root dispatched child", err)
	}
	// A live bound child cannot resume or receive a message after its root
	// campaign stopped. No exec is attempted, even when the Pod is present.
	f.sessionPod("bound-child", "", 0)
	var sess v1alpha1.AgentSession
	key := client.ObjectKey{Namespace: sessionNS, Name: "bound-child"}
	if err := f.c.Get(context.Background(), key, &sess); err != nil {
		t.Fatal(err)
	}
	sess.Spec.Parent = coordinatorSA
	sess.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	annotateTaskBudget(&sess, b)
	if err := f.c.Update(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{apiv1.SessionResumePath(sess.Name), apiv1.SessionMessagesPath(sess.Name)} {
		var body any
		if path == apiv1.SessionMessagesPath(sess.Name) {
			body = apiv1.MessageRequest{Text: "continue"}
		}
		w := f.do(http.MethodPost, path, tokCoordinator, body)
		if w.Code != http.StatusConflict {
			t.Fatalf("latched control admitted: %s %d %s", path, w.Code, w.Body.String())
		}
	}
	// Removal of its old host Pod is not termination proof and does not erase
	// ownership or permit a new task binding.
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: "dev-env-system", Name: "codex-host-a-0"}, &pod); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	l, _, err := f.srv.TaskBudgets.Store.Read(context.Background(), b.TaskUID)
	if err != nil || !l.Latched || l.Status().StopConfirmed {
		t.Fatal("missing Pod treated as stop proof")
	}
}

func TestBudgetAPIChildReservationIsIdempotentAndShared(t *testing.T) {
	f, b := budgetFixture(t)
	bindCatalogFixture(t, f)
	r := coordinatorTask()
	r.IdempotencyKey = "child-a"
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokCoordinator, r)
	if w.Code != http.StatusCreated {
		t.Fatal("create bound child", w.Code, w.Body.String())
	}
	created := decode[apiv1.Session](t, w)
	for range 2 {
		w = f.do(http.MethodPost, apiv1.SessionsPath, tokCoordinator, r)
		if w.Code != http.StatusOK || decode[apiv1.Session](t, w).Name != created.Name {
			t.Fatal("idempotent child differs", w.Code, w.Body.String())
		}
	}
	l, _, err := f.srv.TaskBudgets.Store.Read(context.Background(), b.TaskUID)
	if err != nil || len(l.Workers) != 1 || len(l.Events) != 1 || !l.Workers[0].Active {
		t.Fatal("child effort reservation duplicated", err)
	}
	child := f.session(created.Name)
	actual, err := f.srv.bindingForSession(child)
	if err != nil || actual != b {
		t.Fatal("child lost root budget binding", err)
	}
	f.now = f.now.Add(time.Minute)
	l, err = f.srv.TaskBudgets.Observe(context.Background(), b)
	if err != nil || l.EffortMilliseconds != 60000 {
		t.Fatal("child effort omitted", err)
	}
}

func TestBudgetAPIChildNameCollisionHasOneReservation(t *testing.T) {
	f, b := budgetFixture(t)
	bindCatalogFixture(t, f)
	base := generatedName("alias", f.now)
	if err := f.c.Create(context.Background(), &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: base}}); err != nil {
		t.Fatal(err)
	}
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokCoordinator, coordinatorTask())
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	created := decode[apiv1.Session](t, w)
	if created.Name != base+"-2" {
		t.Fatal("fixture did not force name collision", created.Name)
	}
	l, _, err := f.srv.TaskBudgets.Store.Read(context.Background(), b.TaskUID)
	if err != nil || len(l.Workers) != 1 || len(l.Events) != 1 || f.session(created.Name).Annotations[TaskBudgetWorkerAnnotation] != l.Workers[0].ID {
		t.Fatal("collision charged a phantom worker or lost stable dispatch binding", err)
	}
	f.now = f.now.Add(time.Minute)
	l, err = f.srv.TaskBudgets.Observe(context.Background(), b)
	if err != nil || l.EffortMilliseconds != 60000 {
		t.Fatal("name collision inflated effort", err, l.EffortMilliseconds)
	}
}

func TestBudgetAPIUnknownDispatchCannotReplayOrReuseFinishedKey(t *testing.T) {
	f, b := budgetFixture(t)
	bindCatalogFixture(t, f)
	r := coordinatorTask()
	r.IdempotencyKey = "retained-dispatch"
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokCoordinator, r)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	created := decode[apiv1.Session](t, w)
	sess := f.session(created.Name)
	sess.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if err := f.c.Update(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodPost, apiv1.SessionsPath, tokCoordinator, r)
	if w.Code != http.StatusOK || decode[apiv1.Session](t, w).Name != created.Name {
		t.Fatal("finished request silently relaunched", w.Code, w.Body.String())
	}
	if err := f.c.Delete(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodPost, apiv1.SessionsPath, tokCoordinator, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("unknown prior dispatch replayed", w.Code, w.Body.String())
	}
	l, _, err := f.srv.TaskBudgets.Store.Read(context.Background(), b.TaskUID)
	if err != nil || len(l.Workers) != 1 || len(l.Events) != 1 {
		t.Fatal("unknown replay discarded reservation", err)
	}
}

func TestBudgetAPIUnrelatedSessionsPreserved(t *testing.T) {
	f, _ := budgetFixture(t)
	created := decode[apiv1.Session](t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, task()))
	if w := f.do(http.MethodPost, apiv1.SessionSuspendPath(created.Name), tokHuman, nil); w.Code != http.StatusAccepted {
		t.Fatal(w.Body.String())
	}
	if w := f.do(http.MethodPost, apiv1.SessionResumePath(created.Name), tokHuman, nil); w.Code != http.StatusAccepted {
		t.Fatal("unrelated session blocked", w.Body.String())
	}
}
