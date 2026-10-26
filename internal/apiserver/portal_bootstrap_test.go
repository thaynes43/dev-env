package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestPortalAdmissionBootstrapDisabledAndExistingClosed(t *testing.T) {
	f := newFixture(t)
	token := f.sessionPod("bootstrap-caller", "full", 0)
	permit := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: portalBootstrapPermitNamespace, Name: portalBootstrapPermitName, UID: "permit-uid"}, Data: map[string]string{
		"state": "active", "requestID": "request-178", "sessionUID": "", "podUID": "", "permitUID": "permit-uid", "sessionGeneration": "0", "payloadDigest": portalBootstrapDigest(), "expiresAt": f.now.Add(time.Hour).Format(time.RFC3339),
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
	if after.Data["state"] != "consumed" || after.Data["outcome"] != "Present" || after.Data["targetUID"] != "target-uid" {
		t.Fatalf("existing CLOSED must persist a consumed observation: %#v", after.Data)
	}
	if err := f.c.Delete(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if again := f.do(http.MethodPost, apiv1.PortalAdmissionBootstrapPath, token, apiv1.PortalAdmissionBootstrapRequest{RequestID: "request-178"}); again.Code < 400 {
		t.Fatalf("consumed witness replay accepted: %d %s", again.Code, again.Body.String())
	}
}

func TestPortalAdmissionBootstrapCreatesOnceAndRejectsAmbiguousBodies(t *testing.T) {
	creates := 0
	f := newFixtureWith(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == portalBootstrapTargetName {
			creates++
			if err := c.Create(ctx, obj, opts...); err != nil {
				return err
			}
			// The fake client does not emulate the API server's create receipt.
			// Deliberately add one only after its Create validation has run.
			cm.UID, cm.ResourceVersion = "created-uid", "created-rv"
			return nil
		}
		return c.Create(ctx, obj, opts...)
	}})
	token := f.sessionPod("bootstrap-create", "full", 0)
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "bootstrap-create"}, &pod); err != nil {
		t.Fatal(err)
	}
	permit := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: portalBootstrapPermitNamespace, Name: portalBootstrapPermitName, UID: "permit-uid"}, Data: map[string]string{
		"state": "active", "requestID": "request-178", "sessionUID": string(pod.OwnerReferences[0].UID), "podUID": string(pod.UID), "permitUID": "permit-uid", "sessionGeneration": "0", "payloadDigest": portalBootstrapDigest(), "expiresAt": f.now.Add(time.Hour).Format(time.RFC3339),
	}}
	if err := f.c.Create(context.Background(), permit); err != nil {
		t.Fatal(err)
	}
	f.srv.PortalAdmissionBootstrap = &PortalAdmissionBootstrap{Enabled: true}
	w := f.do(http.MethodPost, apiv1.PortalAdmissionBootstrapPath, token, apiv1.PortalAdmissionBootstrapRequest{RequestID: "request-178"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", w.Code, w.Body.String())
	}
	got := decode[apiv1.PortalAdmissionBootstrapResult](t, w)
	if got.Disposition != "Created" || got.TargetUID != "created-uid" || creates != 1 {
		t.Fatalf("create receipt=%#v creates=%d", got, creates)
	}
	if w := f.do(http.MethodPost, apiv1.PortalAdmissionBootstrapPath, token, apiv1.PortalAdmissionBootstrapRequest{RequestID: "request-178"}); w.Code < 400 || creates != 1 {
		t.Fatalf("replay status=%d creates=%d", w.Code, creates)
	}
	for _, body := range []string{"{\"requestID\":\"request-178\",\"requestID\":\"other\"}", "{\"requestID\":\"request-178\"}" + strings.Repeat(" ", 1025), "{\"requestID\":\"request-178\"} trailing"} {
		if w := f.do(http.MethodPost, apiv1.PortalAdmissionBootstrapPath, token, body); w.Code != http.StatusBadRequest {
			t.Fatalf("ambiguous body status=%d body=%q", w.Code, body)
		}
	}
}
