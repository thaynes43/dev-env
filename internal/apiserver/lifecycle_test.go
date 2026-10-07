package apiserver

import (
	"context"
	"net/http"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
)

// Suspend and resume set spec.operatingMode and say who suspended (D-60); a
// repeat is a 200 that changes nothing; a reaped session is a 409.
func TestSuspendAndResume(t *testing.T) {
	f := newFixture(t)
	created := decode[apiv1.Session](t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, task()))
	live := func() *v1alpha1.AgentSession {
		var s v1alpha1.AgentSession
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: created.Name}, &s); err != nil {
			t.Fatal(err)
		}
		return &s
	}

	w := f.do(http.MethodPost, apiv1.SessionSuspendPath(created.Name), tokClient, nil)
	if w.Code != http.StatusAccepted || decode[apiv1.Session](t, w).OperatingMode != "Suspended" {
		t.Fatalf("suspend: %d %s", w.Code, w.Body.String())
	}
	if s := live(); s.Spec.OperatingMode != v1alpha1.OperatingModeSuspended || s.Annotations[v1alpha1.AnnotationSuspendedBy] != "client/"+clientSA {
		t.Errorf("after suspend: %s %v", s.Spec.OperatingMode, s.Annotations)
	}
	if w := f.do(http.MethodPost, apiv1.SessionSuspendPath(created.Name), tokHuman, nil); w.Code != http.StatusOK || decode[apiv1.Session](t, w).SuspendedBy != "client/"+clientSA {
		t.Errorf("a second suspend: %d %s", w.Code, w.Body.String())
	}

	if w := f.do(http.MethodPost, apiv1.SessionResumePath(created.Name), tokHuman, nil); w.Code != http.StatusAccepted {
		t.Fatalf("resume: %d %s", w.Code, w.Body.String())
	}
	if s := live(); s.Spec.OperatingMode != v1alpha1.OperatingModeRunning || s.Annotations[v1alpha1.AnnotationSuspendedBy] != "" {
		t.Errorf("after resume: %s %v", s.Spec.OperatingMode, s.Annotations)
	}
	if w := f.do(http.MethodPost, apiv1.SessionResumePath(created.Name), tokHuman, nil); w.Code != http.StatusOK {
		t.Errorf("a resume of a running session: %d", w.Code)
	}

	wantError(t, f.do(http.MethodPost, apiv1.SessionResumePath("nope"), tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodGet, apiv1.SessionResumePath(created.Name), tokHuman, nil), http.StatusMethodNotAllowed, apiv1.CodeMethodNotAllowed)
	wantError(t, f.do(http.MethodPost, apiv1.SessionSuspendPath(created.Name), tokStranger, nil), http.StatusForbidden, apiv1.CodeForbidden)

	// A reaped session cannot be resumed or suspended: a reap is final.
	s := live()
	s.Finalizers = []string{controller.Finalizer}
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), live()); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodPost, apiv1.SessionResumePath(created.Name), tokHuman, nil), http.StatusConflict, apiv1.CodeConflict)
}
