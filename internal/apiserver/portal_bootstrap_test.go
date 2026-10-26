package apiserver

import (
	"context"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestPortalAdmissionBootstrapDisabledAndExistingClosed(t *testing.T) {
	f := newFixture(t)
	token := f.sessionPod("bootstrap-caller", "full", 0)
	permit := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: portalBootstrapPermitNamespace, Name: portalBootstrapPermitName, UID: "permit-uid"}, Data: map[string]string{
		"state": "active", "requestID": "request-178", "sessionUID": "", "podUID": "", "payloadDigest": portalBootstrapDigest(), "expiresAt": f.now.Add(time.Hour).Format(time.RFC3339),
	}}
	// Bind the fixed permit to the live AgentSession and Pod created above.
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "bootstrap-caller"}, &pod); err != nil {
		t.Fatal(err)
	}
	// The fixture's session and pod binding are resolved by the server; locate the
	// Session through the pod controller reference rather than request data.
	permit.Data["sessionUID"] = string(pod.OwnerReferences[0].UID)
	permit.Data["podUID"] = string(pod.UID)
	if err := f.c.Create(context.Background(), permit); err != nil {
		t.Fatal(err)
	}
	f.srv.PortalAdmissionBootstrap = &PortalAdmissionBootstrap{Enabled: false}
	if w := f.do(http.MethodPost, apiv1.PortalAdmissionBootstrapPath, token, apiv1.PortalAdmissionBootstrapRequest{RequestID: "request-178"}); w.Code != http.StatusNotFound {
		t.Fatalf("disabled status %d: %s", w.Code, w.Body.String())
	}
	target := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: portalBootstrapTargetNamespace, Name: portalBootstrapTargetName, UID: "target-uid"}, Data: map[string]string{"schema": "1", "state": "closed"}}
	if err := f.c.Create(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	f.srv.PortalAdmissionBootstrap.Enabled = true
	w := f.do(http.MethodPost, apiv1.PortalAdmissionBootstrapPath, token, apiv1.PortalAdmissionBootstrapRequest{RequestID: "request-178"})
	if w.Code != http.StatusOK {
		t.Fatalf("existing closed status %d: %s", w.Code, w.Body.String())
	}
	got := decode[apiv1.PortalAdmissionBootstrapResult](t, w)
	if got.Disposition != "Present" || got.TargetUID != "target-uid" {
		t.Fatalf("receipt %#v", got)
	}
	var after corev1.ConfigMap
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: permit.Namespace, Name: permit.Name}, &after); err != nil {
		t.Fatal(err)
	}
	if after.Data["state"] != "active" {
		t.Fatalf("existing CLOSED must not consume permit: %#v", after.Data)
	}
}
