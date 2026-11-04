package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

// legacyTaskBudgetStatus is TaskBudgetStatus without the decision 94 stall
// field: what agentd and agent-run up to 2.89.x decode, with unknown fields
// refused.
func legacyTaskBudgetStatus() reflect.Type {
	t := reflect.TypeFor[apiv1.TaskBudgetStatus]()
	var fields []reflect.StructField
	for i := range t.NumField() {
		if f := t.Field(i); f.Name != "Stall" {
			fields = append(fields, f)
		}
	}
	return reflect.StructOf(fields)
}

// A stalled, unlatched campaign's status carries the stall field only to a
// client that asks for it (OpenProject #385, C3). An older strict reader
// decodes every budget status route; the ledger is the same whichever reader
// asked.
func TestTaskBudgetStallIsSentOnlyToClientsThatAsk(t *testing.T) {
	ctx := context.Background()
	f := coordinatorFixture(t)
	f.srv.AssignedTaskBudgets = map[string]string{"host-a": "campaign-a"}
	store := taskbudget.KubeStore{Client: f.c, Live: f.c, Namespace: "budget-system"}
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return f.now }
	f.srv.TaskBudgets = &taskbudget.Service{Store: store, Now: clock, Validator: apiBudgetValidator{}, NativeInspector: apiBudgetValidator{}}
	created, err := f.srv.TaskBudgets.Create(ctx, "campaign-a", "host-a", "host-pod-uid", coordinatorSA, apiv1.TaskBudgetSpec{SuccessCondition: "result", OverallSeconds: 3 * 3600, EffortSeconds: 3 * 3600, CheckpointSeconds: 2 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	b := created.Binding
	path := apiv1.TaskBudgetPath(b.TaskUID)
	if w := f.do(http.MethodPost, path+"/admit", tokCoordinator, apiv1.TaskBudgetRequest{Binding: b}); w.Code != http.StatusOK {
		t.Fatalf("owner admission refused: %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	f.now = f.now.Add(taskbudget.StallLimit + time.Minute)
	mu.Unlock()
	legacy := legacyTaskBudgetStatus()
	for _, r := range []struct {
		name, method, path string
		body               any
	}{
		{"status", http.MethodGet, path, nil},
		{"observe", http.MethodPost, path + "/observe", apiv1.TaskBudgetRequest{Binding: b}},
		{"admit", http.MethodPost, path + "/admit", apiv1.TaskBudgetRequest{Binding: b}},
	} {
		t.Run(r.name+" older reader", func(t *testing.T) {
			w := f.do(r.method, r.path, tokCoordinator, r.body)
			if w.Code != http.StatusOK {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			var keys map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &keys); err != nil {
				t.Fatal(err)
			}
			if _, sent := keys["stall"]; sent {
				t.Fatal("a client that did not ask was sent the stall field")
			}
			d := json.NewDecoder(bytes.NewReader(w.Body.Bytes()))
			d.DisallowUnknownFields()
			if err := d.Decode(reflect.New(legacy).Interface()); err != nil {
				t.Fatalf("an older strict reader refused the status: %v", err)
			}
		})
		t.Run(r.name+" stall reader", func(t *testing.T) {
			w := f.doHeader(r.method, r.path, tokCoordinator, r.body, http.Header{apiv1.TaskBudgetStallHeader: {apiv1.TaskBudgetStallV1}})
			if w.Code != http.StatusOK {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			st := decode[apiv1.TaskBudgetStatus](t, w)
			if st.Latched || st.Reason != "" || st.Stall == nil || st.Stall.Signals != 1 || !st.Stall.Since.Equal(created.CreatedAt) || st.Binding != b {
				t.Fatalf("stall reader status %+v", st)
			}
		})
	}
	l, _, err := store.Read(ctx, b.TaskUID)
	if err != nil || l.Latched || !l.StallAnchor.Equal(created.CreatedAt) {
		t.Fatalf("ledger changed: latched %v anchor %v err %v", l != nil && l.Latched, l != nil && l.StallAnchor.Equal(created.CreatedAt), err)
	}
}

// Only the stall field is negotiated: a status without one, and other bodies,
// pass through unchanged.
func TestWithoutTaskBudgetStall(t *testing.T) {
	stall := &apiv1.TaskBudgetStall{ID: "stall:1", Signals: 1}
	st := apiv1.TaskBudgetStatus{Observed: true, Latched: false, Stall: stall}
	if got := withoutTaskBudgetStall(st).(apiv1.TaskBudgetStatus); got.Stall != nil || !got.Observed {
		t.Fatalf("value %+v", got)
	}
	ptr := &apiv1.TaskBudgetStatus{Observed: true, Stall: stall}
	if got := withoutTaskBudgetStall(ptr).(*apiv1.TaskBudgetStatus); got.Stall != nil || !got.Observed || ptr.Stall == nil {
		t.Fatalf("pointer %+v, original changed %v", got, ptr.Stall == nil)
	}
	other := apiv1.Session{Name: "s"}
	if got := withoutTaskBudgetStall(other); !reflect.DeepEqual(got, other) {
		t.Fatalf("other body %+v", got)
	}
}
