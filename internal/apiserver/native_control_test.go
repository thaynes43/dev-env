package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

type nativeOwnerFixture struct {
	calls int
	fail  bool
}

func (f *nativeOwnerFixture) Control(_ context.Context, _ *taskbudget.Ledger, r apiv1.NativeControlRequest) (apiv1.NativeControlResponse, error) {
	f.calls++
	if f.fail {
		return apiv1.NativeControlResponse{}, errors.New("lost exec acknowledgement")
	}
	return apiv1.NativeControlResponse{Binding: r.Binding, ID: r.ID, Operation: r.Operation, Result: json.RawMessage(`{"thread":{"id":"thread-a","turns":[]}}`)}, nil
}

type ownerReadyFixture struct{ unknown bool }

func (f ownerReadyFixture) InspectNativeAdmission(context.Context, *taskbudget.Ledger, taskbudget.Binding) error {
	return nil
}
func (f ownerReadyFixture) InspectNativeContinuation(context.Context, *taskbudget.Ledger, taskbudget.Binding) error {
	if f.unknown {
		return taskbudget.ErrUnavailable
	}
	return nil
}

func TestNativeControlHumanOnlyDurableDispatchAndPassiveHistory(t *testing.T) {
	for _, scenario := range []string{"owner", "client", "coordinator", "default-off", "stale-pod", "unknown-native", "lost-result", "stop", "read"} {
		t.Run(scenario, func(t *testing.T) {
			f := coordinatorFixture(t)
			f.now = time.Now().UTC()
			f.srv.AssignedTaskBudgets = map[string]string{"host-a": "campaign-a"}
			f.srv.TaskBudgets = &taskbudget.Service{Store: taskbudget.KubeStore{Client: f.c, Live: f.c, Namespace: "budget-system"}, Now: func() time.Time { return f.now }, NativeInspector: ownerReadyFixture{}, Validator: apiBudgetValidator{}}
			w := f.do(http.MethodPost, apiv1.TaskBudgetsPath, tokCoordinator, apiv1.CreateTaskBudgetRequest{TaskUID: "campaign-a", Spec: apiv1.TaskBudgetSpec{SuccessCondition: "bounded fixture", OverallSeconds: 600, EffortSeconds: 600, CheckpointSeconds: 120}})
			if w.Code != http.StatusCreated {
				t.Fatal(w.Code, w.Body.String())
			}
			var st apiv1.TaskBudgetStatus
			if json.Unmarshal(w.Body.Bytes(), &st) != nil {
				t.Fatal("budget receipt")
			}
			if _, err := f.srv.TaskBudgets.Admit(context.Background(), st.Binding); err != nil {
				t.Fatal(err)
			}
			control := &nativeOwnerFixture{}
			f.srv.NativeOwnerControl = control
			r := apiv1.NativeControlRequest{Binding: st.Binding, ID: "owner-once", Operation: "turn/start", ThreadID: "thread-a", Text: "inert fixture"}
			token := tokHuman
			want := http.StatusOK
			switch scenario {
			case "client":
				token = tokClient
				want = http.StatusForbidden
			case "coordinator":
				token = tokCoordinator
				want = http.StatusForbidden
			case "default-off":
				f.srv.NativeOwnerControl = nil
				want = http.StatusServiceUnavailable
			case "stale-pod":
				r.Binding.PodUID = "another-pod"
				want = http.StatusConflict
			case "unknown-native":
				f.srv.TaskBudgets.NativeInspector = ownerReadyFixture{unknown: true}
				want = http.StatusServiceUnavailable
			case "lost-result":
				control.fail = true
				want = http.StatusServiceUnavailable
			case "stop":
				r.Operation = "host/stop"
				r.ThreadID = ""
				r.Text = ""
				want = http.StatusAccepted
			case "read":
				r.Operation = "thread/read"
				r.Text = ""
			}
			path := apiv1.TaskBudgetPath(r.Binding.TaskUID) + "/native"
			w = f.do(http.MethodPost, path, token, r)
			if w.Code != want {
				t.Fatalf("got %d want %d: %s", w.Code, want, w.Body.String())
			}
			if scenario == "owner" || scenario == "lost-result" {
				w = f.do(http.MethodPost, path, token, r)
				if w.Code != http.StatusConflict || control.calls != 1 {
					t.Fatal("mutation replayed", w.Code, control.calls)
				}
				consumePath := apiv1.TaskBudgetPath(r.Binding.TaskUID) + "/native-consume"
				w = f.do(http.MethodPost, consumePath, tokHuman, r)
				if w.Code != http.StatusForbidden {
					t.Fatal("human bypassed helper consumption class", w.Code)
				}
				w = f.do(http.MethodPost, consumePath, tokCoordinator, r)
				if w.Code != http.StatusOK {
					t.Fatal("bound root could not consume human intent", w.Code, w.Body.String())
				}
				w = f.do(http.MethodPost, consumePath, tokCoordinator, r)
				if w.Code != http.StatusConflict {
					t.Fatal("root replayed intent", w.Code)
				}
			}
			if scenario == "read" {
				w = f.do(http.MethodPost, path, token, r)
				if w.Code != http.StatusOK || control.calls != 2 {
					t.Fatal("passive reconnect read failed")
				}
			}
			if scenario == "stop" {
				if control.calls != 0 {
					t.Fatal("stop ran native RPC")
				}
				var stop apiv1.TaskBudgetStatus
				_ = json.Unmarshal(w.Body.Bytes(), &stop)
				if !stop.NativeStopRequested || stop.StopConfirmed || stop.Latched {
					t.Fatal("stop intent claimed stop proof")
				}
			}
			if scenario == "client" || scenario == "coordinator" || scenario == "default-off" || scenario == "stale-pod" || scenario == "unknown-native" {
				if control.calls != 0 {
					t.Fatal("unauthorized dispatch")
				}
			}
		})
	}
}
