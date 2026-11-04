package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
// client that asks for it (OpenProject #385, C3). The table covers every task
// budget route that can return a status: create (201), status, observe,
// admit, events, extend, continue and native host/stop (202). An older strict
// reader decodes every 2xx body, and no body sent without the header carries
// the field. Extend and continue cannot succeed on an unlatched stalled
// ledger in this source (no owner-decision validator; continue needs a
// confirmed NativeRootStopped latch, which reports no stall), so for them the
// test only proves that what they send has no stall field.
func TestTaskBudgetStallIsSentOnlyToClientsThatAsk(t *testing.T) {
	ctx := context.Background()
	f := coordinatorFixture(t)
	// Native control bounds its request by the wall clock, so the campaign's
	// deadline must lie in the real future (as in the native control tests).
	f.now = time.Now().UTC()
	f.srv.AssignedTaskBudgets = map[string]string{"host-a": "campaign-a"}
	store := taskbudget.KubeStore{Client: f.c, Live: f.c, Namespace: "budget-system"}
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return f.now }
	f.srv.TaskBudgets = &taskbudget.Service{Store: store, Now: clock, Validator: apiBudgetValidator{}, NativeInspector: ownerReadyFixture{}}
	f.srv.NativeOwnerControl = &nativeOwnerFixture{}
	legacy := legacyTaskBudgetStatus()
	stallHeader := http.Header{apiv1.TaskBudgetStallHeader: {apiv1.TaskBudgetStallV1}}
	older := func(t *testing.T, w *httptest.ResponseRecorder) {
		t.Helper()
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &keys); err != nil {
			t.Fatalf("%d %s: %v", w.Code, w.Body.String(), err)
		}
		if _, sent := keys["stall"]; sent {
			t.Fatal("a client that did not ask was sent the stall field")
		}
		if w.Code < 200 || w.Code > 299 {
			return
		}
		d := json.NewDecoder(bytes.NewReader(w.Body.Bytes()))
		d.DisallowUnknownFields()
		if err := d.Decode(reflect.New(legacy).Interface()); err != nil {
			t.Fatalf("an older strict reader refused the status: %v", err)
		}
	}
	// The API's create route is bounded by the pilot ceilings (45 minutes
	// overall), so a campaign it creates latches before the 60 minute stall.
	// Its 201 receipt is checked on its own fixture; the stalled campaign below
	// is created through the service, as the decision 94 tests do.
	t.Run("create older reader", func(t *testing.T) {
		g := coordinatorFixture(t)
		g.srv.AssignedTaskBudgets = map[string]string{"host-a": "campaign-a"}
		g.srv.TaskBudgets = &taskbudget.Service{Store: taskbudget.KubeStore{Client: g.c, Live: g.c, Namespace: "budget-system"}, Now: func() time.Time { return g.now }}
		w := g.do(http.MethodPost, apiv1.TaskBudgetsPath, tokCoordinator, apiv1.CreateTaskBudgetRequest{TaskUID: "campaign-a", Spec: apiv1.TaskBudgetSpec{SuccessCondition: "result", OverallSeconds: 600, EffortSeconds: 600, CheckpointSeconds: 120}})
		if w.Code != http.StatusCreated {
			t.Fatalf("create %d %s", w.Code, w.Body.String())
		}
		older(t, w)
	})
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
	activity := func(id string) apiv1.TaskBudgetEvent {
		return apiv1.TaskBudgetEvent{Binding: b, ID: id, Kind: "activity"}
	}
	for _, r := range []struct {
		name, method, path, token string
		body, asked               any
		stalled                   bool
	}{
		{"status", http.MethodGet, path, tokCoordinator, nil, nil, true},
		{"observe", http.MethodPost, path + "/observe", tokCoordinator, apiv1.TaskBudgetRequest{Binding: b}, apiv1.TaskBudgetRequest{Binding: b}, true},
		{"admit", http.MethodPost, path + "/admit", tokCoordinator, apiv1.TaskBudgetRequest{Binding: b}, apiv1.TaskBudgetRequest{Binding: b}, true},
		{"events", http.MethodPost, path + "/events", tokCoordinator, activity("activity-older"), activity("activity-asked"), true},
		{"extend", http.MethodPost, path + "/extend", tokHuman, apiv1.TaskBudgetExtension{Binding: b}, nil, false},
		{"continue", http.MethodPost, path + "/continue", tokHuman, apiv1.ContinueTaskBudgetRequest{Binding: b}, nil, false},
		// Last: host/stop is consumed once, so only the older reader asks it.
		{"native host/stop", http.MethodPost, path + "/native", tokHuman, apiv1.NativeControlRequest{Binding: b, ID: "stop-once", Operation: "host/stop"}, nil, true},
	} {
		t.Run(r.name+" older reader", func(t *testing.T) {
			w := f.do(r.method, r.path, r.token, r.body)
			if r.stalled && (w.Code < 200 || w.Code > 299) {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			older(t, w)
		})
		if r.asked == nil && r.method != http.MethodGet {
			continue
		}
		t.Run(r.name+" stall reader", func(t *testing.T) {
			body := r.asked
			if r.method == http.MethodGet {
				body = nil
			}
			w := f.doHeader(r.method, r.path, r.token, body, stallHeader)
			if w.Code != http.StatusOK {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			st := decode[apiv1.TaskBudgetStatus](t, w)
			if st.Latched || st.Reason != "" || st.Stall == nil || st.Stall.Signals != 1 || !st.Stall.Since.Equal(created.CreatedAt) || st.Binding != b {
				t.Fatalf("stall reader status %+v", st)
			}
		})
	}
	// host/stop answered while the campaign was stalled and unlatched: a
	// reader that asks still sees the stall after it.
	w := f.doHeader(http.MethodGet, path, tokCoordinator, nil, stallHeader)
	if st := decode[apiv1.TaskBudgetStatus](t, w); w.Code != http.StatusOK || st.Latched || st.Stall == nil || !st.NativeStopRequested {
		t.Fatalf("after host/stop: %d %+v", w.Code, st)
	}
	l, _, err := store.Read(ctx, b.TaskUID)
	if err != nil || l.Latched {
		t.Fatalf("ledger changed: latched %v err %v", l != nil && l.Latched, err)
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
