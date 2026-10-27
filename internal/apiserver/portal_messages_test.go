package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestPortalMessageScopeAndTransport(t *testing.T) {
	controller := true
	session := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: sessionNS, UID: types.UID("uid-a")}, Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Mode: v1alpha1.ModeRemote}, Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseRunning}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: sessionNS, UID: types.UID("pod-a"), OwnerReferences: []metav1.OwnerReference{{UID: session.UID, Controller: &controller}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	f := newFixture(t, session, pod)
	ex := &fakeExec{}
	f.srv.Exec = ex
	f.srv.PortalMessageTargets = map[string]string{"agent-a": "uid-a"}
	valid := apiv1.PortalMessageRequest{RequestID: "request-0000000001", Project: "dev-env", SessionName: "agent-a", SessionUID: "uid-a", Text: "hello", ActorRef: "actor-000000000001"}
	call := func(req apiv1.PortalMessageRequest, kind callerKind) (int, any, error) {
		b, _ := json.Marshal(req)
		r := httptest.NewRequest(http.MethodPost, apiv1.PortalSessionMessagesPath, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		return f.srv.portalMessage(context.Background(), httptest.NewRecorder(), r, &caller{kind: kind})
	}
	for _, edit := range []func(*apiv1.PortalMessageRequest){func(r *apiv1.PortalMessageRequest) { r.Project = "other" }, func(r *apiv1.PortalMessageRequest) { r.SessionUID = "replacement" }, func(r *apiv1.PortalMessageRequest) { r.SessionName = "other" }, func(r *apiv1.PortalMessageRequest) { r.ActorRef = "bad" }, func(r *apiv1.PortalMessageRequest) { r.Text = "\x1b[2J" }, func(r *apiv1.PortalMessageRequest) { r.Text = "https://example.com" }} {
		req := valid
		edit(&req)
		_, _, _ = call(req, kindPortalReader)
		if len(ex.cmds) != 0 {
			t.Fatal("invalid request reached exec")
		}
	}
	if _, _, err := call(valid, kindHuman); err == nil || len(ex.cmds) != 0 {
		t.Fatal("other caller acquired portal command scope")
	}
	status, body, err := call(valid, kindPortalReader)
	receipt, ok := body.(apiv1.PortalMessageReceipt)
	if err != nil || status != http.StatusOK || !ok || receipt.TransportStatus != apiv1.PortalTransportDelivered || receipt.ResolvedPodUID != "pod-a" || len(ex.cmds) != 1 {
		t.Fatal("valid scoped delivery did not complete")
	}
	if !strings.Contains(ex.cmds[0], "--expected-session-uid uid-a") || !strings.Contains(ex.cmds[0], "--expected-pod-uid pod-a") {
		t.Fatal("delivery lacked immutable target fences")
	}
	ex.code = 3
	_, body, err = call(valid, kindPortalReader)
	receipt = body.(apiv1.PortalMessageReceipt)
	if err != nil || receipt.TransportStatus != apiv1.PortalTransportRefused || strings.Contains(receipt.Reason, "agentd") {
		t.Fatal("not-addressable did not produce static refusal")
	}
	ex.code = 4
	_, body, err = call(valid, kindPortalReader)
	receipt = body.(apiv1.PortalMessageReceipt)
	if err != nil || receipt.TransportStatus != apiv1.PortalTransportUnknown {
		t.Fatal("ambiguous exec did not produce Unknown")
	}
}

func TestPortalMessageRouteDefaultOff(t *testing.T) {
	f := newFixture(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, apiv1.PortalSessionMessagesPath, strings.NewReader(`{}`))
	f.srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("default-off route returned %d", w.Code)
	}
}

func TestPortalMessageRejectsDuplicateFields(t *testing.T) {
	f := newFixture(t)
	f.srv.PortalMessageTargets = map[string]string{"agent-a": "uid-a"}
	ex := &fakeExec{}
	f.srv.Exec = ex
	raw := `{"requestID":"request-0000000001","project":"dev-env","project":"dev-env","sessionName":"agent-a","sessionUID":"uid-a","text":"hello","actorRef":"actor-000000000001"}`
	r := httptest.NewRequest(http.MethodPost, apiv1.PortalSessionMessagesPath, strings.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	_, _, err := f.srv.portalMessage(context.Background(), httptest.NewRecorder(), r, &caller{kind: kindPortalReader})
	if err == nil || len(ex.cmds) != 0 {
		t.Fatal("duplicate key admitted")
	}
}
