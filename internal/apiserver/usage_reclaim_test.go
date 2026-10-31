package apiserver

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/usageconsolidation"
)

// These are read fixtures, not a claim that reclaim or history expiry ran.
// Reap means DELETE with the private-history finalizer still protecting the
// Session; removal of the Session itself is a different retention boundary.
func TestUsageReadsSurviveTaskComputeReclaimAndRetainedReap(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.sessionPod("usage-root", "full", 0)
	f.sessionPod("usage-child", "full", 1)
	f.sessionPod("usage-grandchild", "full", 2)
	names := []string{"usage-root", "usage-child", "usage-grandchild"}
	for i, name := range names {
		s := f.session(name)
		s.Finalizers = []string{controller.Finalizer}
		if i > 0 {
			s.Spec.Parent = names[i-1]
		}
		if err := f.c.Update(ctx, s); err != nil {
			t.Fatal(err)
		}
		s = f.session(name)
		var originalPod corev1.Pod
		if err := f.c.Get(ctx, client.ObjectKeyFromObject(s), &originalPod); err != nil {
			t.Fatal(err)
		}
		u := providerObservation(string(s.UID), string(originalPod.UID), "claude", s.Spec.Model, int64(i+1)*10, 0, "0.25", servedModel("claude-haiku-4-5", int64(i+1)*10, "0.25"))
		s.Status.UsageObservation = usageObservationStatus(u)
		s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{SessionUID: string(s.UID), HomeUID: "retained-home-" + name, LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: u.Source.PodUID}}
		s.Status.Phase = v1alpha1.PhaseRunning
		if err := f.c.Status().Update(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	headers := http.Header{apiv1.AttributionHeader: {apiv1.AttributionProvider}}
	read := func() []apiv1.Session {
		t.Helper()
		out := make([]apiv1.Session, 0, len(names))
		for _, name := range names {
			w := f.doHeader(http.MethodGet, apiv1.SessionPath(name), tokHuman, nil, headers)
			if w.Code != http.StatusOK {
				t.Fatal(w.Code, w.Body.String())
			}
			out = append(out, decode[apiv1.Session](t, w))
		}
		return out
	}
	project := func(sessions []apiv1.Session) usageconsolidation.Report {
		t.Helper()
		in := usageconsolidation.Input{}
		for _, s := range sessions {
			in.InitialInvocations = append(in.InitialInvocations, *s.UsageObservation, *s.UsageObservation)
		}
		r, err := usageconsolidation.Project(in, usageconsolidation.DefaultLimits)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Attribution.Rows) != 3 || len(r.Attribution.WithheldConflicts) != 0 {
			t.Fatal("invocations lost or counted twice", r.Attribution)
		}
		return r
	}
	before := read()
	report := project(before)
	if r := before[0].UsageRollup; r == nil || r.Counted != 2 || r.Sessions != 2 || len(r.Invocations) != 2 || *r.InputTokens.Amount != 50 || *r.OutputTokens.Amount != 0 || r.CacheReadInputTokens.Amount != nil || r.Cost.Amount != "0.5" {
		t.Fatalf("wrong descendant total: %+v", r)
	}
	for _, name := range names {
		var pod corev1.Pod
		if err := f.c.Get(ctx, client.ObjectKey{Namespace: sessionNS, Name: name}, &pod); err != nil {
			t.Fatal(err)
		}
		if err := f.c.Delete(ctx, &pod); err != nil {
			t.Fatal(err)
		}
		s := f.session(name)
		s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		if err := f.c.Update(ctx, s); err != nil {
			t.Fatal(err)
		}
		s = f.session(name)
		s.Status.Phase, s.Status.PodName = v1alpha1.PhaseSuspended, ""
		s.Status.Conditions = []metav1.Condition{{Type: controller.ConditionPrivateHistory, Status: metav1.ConditionTrue, Reason: "TaskComputeReclaimed", LastTransitionTime: metav1.NewTime(f.now)}}
		if err := f.c.Status().Update(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// Fail if a usage read even attempts a task-pod lookup after reclaim.
	guarded := interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, pod := obj.(*corev1.Pod); pod {
			t.Fatalf("usage attempted live pod read: %s", key)
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	f.srv.Client, f.srv.Live = guarded, guarded
	check := func(label string) {
		t.Helper()
		after := read()
		for i := range before {
			if !reflect.DeepEqual(before[i].UsageObservation, after[i].UsageObservation) || !reflect.DeepEqual(before[i].UsageRollup, after[i].UsageRollup) {
				t.Fatal(label, "changed retained usage", names[i])
			}
		}
		if !reflect.DeepEqual(report, project(after)) {
			t.Fatal(label, "changed consolidated report")
		}
	}
	check("reclaim")
	if err := f.c.Delete(ctx, f.session("usage-grandchild")); err != nil {
		t.Fatal(err)
	}
	if f.session("usage-grandchild").DeletionTimestamp.IsZero() {
		t.Fatal("reap was not requested")
	}
	check("retained reap")
	// Missing evidence stays Unknown despite a retained original executor ID.
	s := f.session("usage-child")
	s.Status.UsageObservation = nil
	if err := f.c.Status().Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	missing := read()
	if missing[1].UsageObservation != nil || missing[0].UsageRollup.Coverage != "Partial" || missing[0].UsageRollup.InputTokens.State != protocol.UsageUnknown {
		t.Fatal("invented usage from pod absence or requested model")
	}
}

func TestPortalUsageReadsRetainedStatusAfterReclaimAndReap(t *testing.T) {
	f := newFixture(t)
	now := f.now
	task := v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "task", Namespace: sessionNS, UID: "task-uid"}, Spec: v1alpha1.AgentSessionSpec{Agent: "claude", Model: "claude-opus-5-5", Mode: v1alpha1.ModeTask}}
	u := providerObservation(string(task.UID), "gone-pod", "claude", task.Spec.Model, 100, 0, "0.25", servedModel("claude-haiku-4-5", 100, "0.25"))
	task.Status.UsageObservation = usageObservationStatus(u)
	task.Status.Phase = v1alpha1.PhaseRunning
	interactive := task.DeepCopy()
	interactive.Name, interactive.UID, interactive.Spec.Mode = "conversation", "conversation-uid", v1alpha1.ModeRemote
	interactive.Status.UsageObservation = nil
	interactive.Status.ConversationUsage = convStatus("claude", now)
	initial := portalActivity(&task, now).Usage
	conversation := portalConversationUsage(interactive, now)
	if initial.Scope != protocol.UsageInitial || *initial.InputTokens.Amount != 100 || initial.ServedModels[0] != "claude-haiku-4-5" || conversation == nil || *conversation.Models[0].InputTokens.Amount != 1234 {
		t.Fatal("bad usage fixture", initial, conversation)
	}
	portalPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal-pod", Namespace: sessionNS, UID: "portal-pod-uid"}, Spec: corev1.PodSpec{ServiceAccountName: "dev-env-v2-portal"}}
	live := &portalLive{pod: portalPod, rv: "7"}
	f.srv.Live = live
	f.srv.Policy.PortalReaderEnabled, f.srv.PortalConversationUsage = true, true
	id := saIdentity(portalReaderRef)
	id.PodName, id.PodUID = portalPod.Name, string(portalPod.UID)
	f.auth["tok-portal"] = id
	readPortal := func() {
		t.Helper()
		live.sessions = []v1alpha1.AgentSession{task, *interactive}
		w := f.do(http.MethodGet, apiv1.SessionsPath+"?limit=50&view=activity", "tok-portal", nil)
		if w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		page := decode[apiv1.PortalSessionPage](t, w)
		if !page.Complete || len(page.Sessions) != 2 {
			t.Fatal("portal dropped retained Sessions", page)
		}
		for _, row := range page.Sessions {
			if row.Name == task.Name {
				if !reflect.DeepEqual(initial, row.Activity.Usage) || row.Activity.ConversationUsage != nil {
					t.Fatal("portal wire changed initial usage", row)
				}
			} else if !reflect.DeepEqual(conversation, row.Activity.ConversationUsage) || row.Activity.Usage.Scope != protocol.UsageUnknown {
				t.Fatal("portal wire changed conversation usage", row)
			}
		}
	}
	readPortal()
	for _, reaped := range []bool{false, true} {
		task.Status.Phase, interactive.Status.Phase = v1alpha1.PhaseSuspended, v1alpha1.PhaseSuspended
		task.Status.PodName, interactive.Status.PodName = "", ""
		task.Status.Conditions = []metav1.Condition{{Type: controller.ConditionPrivateHistory, Status: metav1.ConditionTrue, Reason: "TaskComputeReclaimed"}}
		if reaped {
			at := metav1.NewTime(now)
			task.DeletionTimestamp, interactive.DeletionTimestamp = &at, &at
		}
		if !reflect.DeepEqual(initial, portalActivity(&task, now).Usage) || !reflect.DeepEqual(conversation, portalConversationUsage(interactive, now)) {
			t.Fatal("lifecycle changed retained portal usage", reaped)
		}
		readPortal()
		if portalConversationUsage(&task, now) != nil || portalActivity(interactive, now).Usage.Scope != protocol.UsageUnknown {
			t.Fatal("task and cumulative conversation usage overlap")
		}
	}
	interactive.Status.ConversationUsage = nil
	if portalConversationUsage(interactive, now) != nil {
		t.Fatal("invented conversation usage after stop")
	}
}
