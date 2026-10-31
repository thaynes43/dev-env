package apiserver

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func outcomeHeartbeat(f *fixture, name, token string) protocol.Status {
	u := fixtureUsage(f, name, token)
	st := status(name)
	st.UsageObservation = &u
	st.Agent.Attached = 0
	st.Agent.Task.ExitSource = &u.Source
	st.TaskOutcome = &protocol.TaskOutcome{SessionUID: u.Source.SessionUID, PodUID: u.Source.PodUID, Record: protocol.TaskOutcomeRecord{Summary: "Ready for lead review", PRURLs: []string{"https://github.com/thaynes43/dev-env/pull/123"}, HeadSHAs: []string{strings.Repeat("a", 40)}}}
	return st
}

func TestTaskOutcomeHeartbeat(t *testing.T) {
	f := newFixture(t)
	name := "task-outcome"
	tok := f.sessionPod(name, "full", 0)
	st := outcomeHeartbeat(f, name, tok)
	post := func(st protocol.Status) {
		t.Helper()
		if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), tok, st); w.Code != http.StatusNoContent {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	post(st)
	first := f.session(name).Status.TaskOutcome.DeepCopy()
	if first == nil || first.Record.Summary != st.TaskOutcome.Record.Summary {
		t.Fatal("valid outcome not retained", first)
	}
	for _, kind := range []string{"replay", "second", "omitted", "wrong pod"} {
		t.Run(kind, func(t *testing.T) {
			changed := outcomeHeartbeat(f, name, tok)
			switch kind {
			case "second":
				changed.TaskOutcome.Record.Summary = "replacement"
			case "omitted":
				changed.TaskOutcome = nil
			case "wrong pod":
				changed.TaskOutcome.PodUID = "other-pod"
			}
			post(changed)
			if !reflect.DeepEqual(first, f.session(name).Status.TaskOutcome) {
				t.Fatal("original was replaced or erased")
			}
		})
	}
	s := f.session(name)
	s.Status.Phase = v1alpha1.PhaseSuspended
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if got := view(f.session(name), false).TaskOutcome; got == nil || got.Record.Summary != first.Record.Summary {
		t.Fatal("suspended read lost outcome")
	}
}

func TestTaskOutcomeHeartbeatRefusals(t *testing.T) {
	for _, kind := range []string{"off org", "invalid URL", "bad SHA", "oversized", "wrong pod", "wrong session", "busy", "unbound exit", "wrong attempt", "original executor", "non task"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			name := "refused-outcome"
			tok := f.sessionPod(name, "full", 0)
			st := outcomeHeartbeat(f, name, tok)
			switch kind {
			case "off org":
				st.TaskOutcome.Record.PRURLs[0] = "https://github.com/other/dev-env/pull/1"
			case "invalid URL":
				st.TaskOutcome.Record.PRURLs[0] += "/files"
			case "bad SHA":
				st.TaskOutcome.Record.HeadSHAs[0] = "bad"
			case "oversized":
				st.TaskOutcome.Record.Summary = strings.Repeat("x", 201)
			case "wrong pod":
				st.TaskOutcome.PodUID = "other-pod"
			case "wrong session":
				st.TaskOutcome.SessionUID = "other-session"
			case "busy":
				st.Agent.State = protocol.AgentBusy
			case "unbound exit":
				st.Agent.Task.ExitSource = nil
			case "wrong attempt":
				source := *st.Agent.Task.ExitSource
				source.AttemptID = strings.Repeat("b", 64)
				st.Agent.Task.ExitSource = &source
			case "original executor":
				s := f.session(name)
				s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{SessionUID: string(s.UID), LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: "original-pod"}}
				if err := f.c.Status().Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			case "non task":
				s := f.session(name)
				s.Spec.Mode = v1alpha1.ModeLocal
				if err := f.c.Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			}
			if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), tok, st); w.Code != http.StatusNoContent {
				t.Fatal("invalid outcome blocked liveness", w.Code, w.Body.String())
			}
			if s := f.session(name); s.Status.TaskOutcome != nil || s.Status.Agent == nil {
				t.Fatal("invalid outcome retained or liveness lost", s.Status.TaskOutcome)
			}
		})
	}
}

func TestTaskOutcomeConcurrentFirstWrite(t *testing.T) {
	f := newFixture(t)
	name := "outcome-race"
	tok := f.sessionPod(name, "full", 0)
	st := outcomeHeartbeat(f, name, tok)
	raced := false
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		if x, ok := obj.(*v1alpha1.AgentSession); ok && x.Status.TaskOutcome != nil && !raced {
			raced = true
			fresh := f.session(name)
			fresh.Status.TaskOutcome = x.Status.TaskOutcome.DeepCopy()
			fresh.Status.TaskOutcome.Record.Summary = "concurrent first snapshot"
			if err := f.c.Status().Update(ctx, fresh); err != nil {
				return err
			}
		}
		return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
	}})
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), tok, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	got := f.session(name)
	if !raced || got.Status.TaskOutcome == nil || got.Status.TaskOutcome.Record.Summary != "concurrent first snapshot" || got.Status.Agent == nil {
		t.Fatal("concurrent first snapshot or liveness lost", got.Status)
	}
}

func TestTaskOutcomeNegotiatedRead(t *testing.T) {
	f := newFixture(t)
	name := "outcome-read"
	tok := f.sessionPod(name, "full", 0)
	st := outcomeHeartbeat(f, name, tok)
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), tok, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{apiv1.SessionPath(name), apiv1.SessionsPath} {
		for _, optIn := range []bool{false, true} {
			headers := http.Header{}
			if optIn {
				headers.Set(apiv1.TaskOutcomeHeader, apiv1.TaskOutcomeV1)
			}
			w := f.doHeader(http.MethodGet, path, tok, nil, headers)
			if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"taskOutcome"`) != optIn {
				t.Fatal("negotiated read", path, optIn, w.Code, w.Body.String())
			}
		}
	}
}
