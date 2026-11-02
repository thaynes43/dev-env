package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
)

func TestInstructionPublisherFreshParentCreatesSeparatelyBoundReceiver(t *testing.T) {
	// This fake API server assigns synthetic object UIDs at Create, as the real
	// Kubernetes API does. It is source-contract evidence, never native runtime ACK.
	f := newFixtureWith(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*v1alpha1.AgentSession); ok && obj.GetUID() == "" {
			obj.SetUID(uuid.NewUUID())
		}
		return c.Create(ctx, obj, opts...)
	}})
	bindInstructionCatalogFixture(t, f)
	f.srv.CoordinatorInstructionAgentImage = "ghcr.io/thaynes43/dev-env:2.87.0@sha256:" + strings.Repeat("a", 64)
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, publisherRequest())
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	parent := f.session(decode[apiv1.Session](t, w).Name)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: parent.Name, Namespace: sessionNS, UID: uuid.NewUUID(), Labels: map[string]string{v1alpha1.LabelProfile: "dev"}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(parent, v1alpha1.GroupVersion.WithKind("AgentSession"))}}, Spec: corev1.PodSpec{ServiceAccountName: controller.ServiceAccountName, Containers: []corev1.Container{{Name: "agent", Image: "qualified-fixture"}}}}
	if err := f.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(sessionNS + "/" + controller.ServiceAccountName)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	f.auth["fresh-publisher"] = id
	f.now = f.now.Add(time.Second) // Server-generated child name is distinct.
	req := optInRequest()
	req.IdempotencyKey = "fresh-publisher-receiver"
	w = f.do(http.MethodPost, apiv1.SessionsPath, "fresh-publisher", req)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	child := f.session(decode[apiv1.Session](t, w).Name)
	if !child.Spec.CoordinatorInstructions || child.Spec.CoordinatorInstructionPublisher || child.Spec.CoordinatorInstructionParentUID != string(parent.UID) || child.Spec.Parent != parent.Name {
		t.Fatal("actual parent binding or separate receiver enrollment lost", child.Spec)
	}
	if f.session(parent.Name).Spec.CoordinatorInstructions {
		t.Fatal("creating a receiver enrolled its publisher")
	}
}

func publisherRequest() apiv1.CreateSessionRequest {
	r := coordinatorTask()
	r.CoordinatorInstructionPublisher = true
	r.IdempotencyKey = "ordinary-publisher"
	return r
}

func TestInstructionPublisherFreshClientRootAndAcceptedRetry(t *testing.T) {
	f := newFixture(t)
	bindInstructionCatalogFixture(t, f)
	f.srv.CoordinatorInstructionAgentImage = "ghcr.io/thaynes43/dev-env:2.87.0@sha256:" + strings.Repeat("a", 64)
	r := publisherRequest()
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("publisher create: %d %s", w.Code, w.Body.String())
	}
	created := decode[apiv1.Session](t, w)
	s := f.session(created.Name)
	if !s.Spec.CoordinatorInstructionPublisher || s.Spec.CoordinatorInstructions || s.Spec.CoordinatorInstructionParentUID != "" || s.Spec.Parent != clientSA || s.Spec.Profile != "dev" || len(s.Annotations) == 0 {
		t.Fatalf("publisher was enrolled or detached from real client/project: %+v", s.Spec)
	}
	before := sessionNames(t, f)
	f.srv.CoordinatorInstructionAgentImage = ""
	w = f.do(http.MethodPost, apiv1.SessionsPath, tokClient, r)
	if w.Code != http.StatusOK || decode[apiv1.Session](t, w).Name != created.Name {
		t.Fatalf("lost accepted publisher on rollback: %d %s", w.Code, w.Body.String())
	}
	r.Prompt += " changed"
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokClient, r), http.StatusConflict, apiv1.CodeConflict)
	r.IdempotencyKey = "another-publisher"
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokClient, r), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if len(sessionNames(t, f)) != len(before) {
		t.Fatal("rollback created another publisher")
	}
}

func TestInstructionPublisherRejectsWrongCallerScopeAndOldClientImage(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		edit        func(*apiv1.CreateSessionRequest)
		oldImage    bool
	}{
		{name: "human", token: tokHuman},
		{name: "session", token: "session"},
		{name: "coordinator host", token: tokCoordinator},
		{name: "receiver mixed", token: tokClient, edit: func(r *apiv1.CreateSessionRequest) { r.CoordinatorInstructions = true }},
		{name: "no project", token: tokClient, edit: func(r *apiv1.CreateSessionRequest) { r.Project = "" }},
		{name: "workbench profile", token: tokClient, edit: func(r *apiv1.CreateSessionRequest) { r.Profile = "workbench" }},
		{name: "old client image", token: tokClient, oldImage: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			bindInstructionCatalogFixture(t, f)
			f.srv.CoordinatorInstructionAgentImage = "ghcr.io/thaynes43/dev-env:2.87.0@sha256:" + strings.Repeat("a", 64)
			if tc.oldImage {
				f.srv.CoordinatorInstructionAgentImage = "ghcr.io/thaynes43/dev-env:2.86.0@sha256:" + strings.Repeat("a", 64)
			}
			token := tc.token
			if token == "session" {
				token = f.sessionPod("ordinary-owner", "dev", 0)
			}
			r := publisherRequest()
			if tc.edit != nil {
				tc.edit(&r)
			}
			w := f.do(http.MethodPost, apiv1.SessionsPath, token, r)
			if w.Code/100 == 2 {
				t.Fatalf("unsafe publisher accepted: %s", w.Body.String())
			}
		})
	}
}
