package apiserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestPortalConversationTargetAndUnsupportedAgent(t *testing.T) {
	owner := true
	sess := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: sessionNS, UID: types.UID("uid-a")}, Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Mode: v1alpha1.ModeRemote}, Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseRunning}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: sessionNS, UID: types.UID("pod-a"), OwnerReferences: []metav1.OwnerReference{{UID: sess.UID, Controller: &owner}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	f := newFixture(t, sess, pod)
	ex := &fakeExec{code: 2}
	f.srv.Exec = ex
	f.srv.PortalConversationTargets = map[string]string{"uid-a": "agent-a"}
	invoke := func(uid, query string, c *caller) (int, any, error) {
		r := httptest.NewRequest(http.MethodGet, apiv1.PortalConversationPrefix+uid+"/conversation"+query, nil)
		r.SetPathValue("uid", uid)
		return f.srv.portalConversation(context.Background(), httptest.NewRecorder(), r, c)
	}
	if _, _, err := invoke("other", "", &caller{kind: kindPortalReader}); err == nil || len(ex.cmds) != 0 {
		t.Fatal("unapproved UID reached exec")
	}
	if _, _, err := invoke("uid-a", "", &caller{kind: kindHuman}); err == nil || len(ex.cmds) != 0 {
		t.Fatal("nonportal caller reached exec")
	}
	if _, _, err := invoke("uid-a", "?limit=61", &caller{kind: kindPortalReader}); err == nil || len(ex.cmds) != 0 {
		t.Fatal("unbounded request reached exec")
	}
	status, body, err := invoke("uid-a", "", &caller{kind: kindPortalReader})
	out, ok := body.(apiv1.PortalConversationResponse)
	if err != nil || status != http.StatusOK || !ok || out.Projection.State != "unavailable" || len(out.Projection.Turns) != 0 || len(ex.cmds) != 1 || !strings.Contains(ex.cmds[0], "--expected-pod-uid pod-a") || !strings.Contains(ex.cmds[0], "--expected-session-uid uid-a") {
		t.Fatal("old agent compatibility or UID fence failed")
	}
}

func TestPortalConversationDefaultOff(t *testing.T) {
	f := newFixture(t)
	w := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, apiv1.PortalConversationPrefix+"uid-a/conversation", nil))
	if w.Code != http.StatusNotFound {
		t.Fatal("default-off history route was present")
	}
}

func TestPortalConversationDiscardsReplacementDuringExec(t *testing.T) {
	for _, mode := range []string{"session", "pod"} {
		t.Run(mode, func(t *testing.T) {
			owner := true
			sess := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: sessionNS, UID: types.UID("uid-a")}, Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Mode: v1alpha1.ModeRemote}, Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseRunning}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: sessionNS, UID: types.UID("pod-a"), OwnerReferences: []metav1.OwnerReference{{UID: sess.UID, Controller: &owner}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
			f := newFixture(t, sess, pod)
			f.srv.PortalConversationTargets = map[string]string{"uid-a": "agent-a"}
			f.srv.Exec = &fakeExec{handle: func([]string, string) (string, int) {
				if mode == "session" {
					current := &v1alpha1.AgentSession{}
					if f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "agent-a"}, current) != nil {
						t.Fatal("session fixture")
					}
					current.UID = "replacement-session"
					if f.c.Update(context.Background(), current) != nil {
						t.Fatal("session replacement")
					}
				} else {
					current := &corev1.Pod{}
					if f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "agent-a"}, current) != nil {
						t.Fatal("pod fixture")
					}
					current.UID = "replacement-pod"
					if f.c.Update(context.Background(), current) != nil {
						t.Fatal("pod replacement")
					}
				}
				return `{"state":"partial","reasonCode":"coverage_unverified","conversationID":"12345678-1234-1234-1234-123456789abc","oldestSeq":1,"newestSeq":1,"observedAt":"2026-10-27T12:00:00Z","turns":[{"seq":1,"at":"2026-10-27T11:00:00Z","role":"user","text":"SYNTHETIC-PRIVATE"}]}`, 0
			}}
			r := httptest.NewRequest(http.MethodGet, apiv1.PortalConversationPrefix+"uid-a/conversation", nil)
			r.SetPathValue("uid", "uid-a")
			_, body, err := f.srv.portalConversation(context.Background(), httptest.NewRecorder(), r, &caller{kind: kindPortalReader})
			if err != nil {
				t.Fatal("read failed")
			}
			result := body.(apiv1.PortalConversationResponse)
			if result.Projection.State != "unavailable" || len(result.Projection.Turns) != 0 {
				t.Fatal("replacement leaked private content")
			}
		})
	}
}

func TestPortalConversationAllowsBoundSyntheticProjection(t *testing.T) {
	owner := true
	sess := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: sessionNS, UID: types.UID("uid-a")}, Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Mode: v1alpha1.ModeRemote}, Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseRunning}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: sessionNS, UID: types.UID("pod-a"), OwnerReferences: []metav1.OwnerReference{{UID: sess.UID, Controller: &owner}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	for _, duplicate := range []bool{false, true} {
		f := newFixture(t, sess.DeepCopy(), pod.DeepCopy())
		f.srv.PortalConversationTargets = map[string]string{"uid-a": "agent-a"}
		raw := `{"state":"partial","reasonCode":"coverage_unverified","conversationID":"12345678-1234-1234-1234-123456789abc","oldestSeq":1,"newestSeq":1,"observedAt":"2026-10-27T12:00:00Z","turns":[{"seq":1,"at":"2026-10-27T11:00:00Z","role":"user","text":"synthetic visible"}]}`
		if duplicate {
			raw = strings.Replace(raw, `"state":"partial"`, `"state":"partial","state":"partial"`, 1)
		}
		f.srv.Exec = &fakeExec{out: raw}
		r := httptest.NewRequest(http.MethodGet, apiv1.PortalConversationPrefix+"uid-a/conversation", nil)
		r.SetPathValue("uid", "uid-a")
		_, body, err := f.srv.portalConversation(context.Background(), httptest.NewRecorder(), r, &caller{kind: kindPortalReader})
		if err != nil {
			t.Fatal("request failed")
		}
		result := body.(apiv1.PortalConversationResponse)
		if duplicate {
			if result.Projection.State != "unavailable" || len(result.Projection.Turns) != 0 {
				t.Fatal("duplicate fields leaked content")
			}
		} else if result.Projection.State != "partial" || len(result.Projection.Turns) != 1 || result.Projection.Turns[0].Text != "synthetic visible" {
			t.Fatal("bound projection not returned")
		}
	}
}
