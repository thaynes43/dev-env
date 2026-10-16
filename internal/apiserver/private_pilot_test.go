package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
	"github.com/thaynes43/dev-env/internal/sessiondirectory"
)

func pilotAPI(t *testing.T, funcs interceptor.Funcs) (*fixture, testutil.PilotFixture) {
	t.Helper()
	b := testutil.NewPilot(t)
	create := funcs.Create
	funcs.Create = func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		if s, ok := o.(*v1alpha1.AgentSession); ok {
			s.UID = uuid.NewUUID()
			s.Generation = 1
		}
		var err error
		if create != nil {
			err = create(ctx, c, o, opts...)
		} else {
			err = c.Create(ctx, o, opts...)
		}
		if apierrors.IsAlreadyExists(err) {
			o.SetUID("")
		}
		return err
	}
	f := newFixtureWith(t, funcs, b.Objects()...)
	f.now = b.Now
	f.tmpl = b.Templates
	f.srv.PrivatePilot = b.Policy
	f.srv.ManagedCodexTasks = true
	f.srv.PrivateHistoryRetention = true
	f.srv.RetainedCommonRules = true
	f.srv.Projects = &CatalogBinding{Key: client.ObjectKeyFromObject(b.Catalog), CloneOwner: "thaynes43"}
	f.srv.PrivateCanaryTemplates = client.ObjectKeyFromObject(b.Template)
	f.srv.SessionDirectory = &sessiondirectory.KubeStore{Client: f.c, Live: f.c, Namespace: "dev-env-system"}
	f.srv.SessionDirectoryProjects = []string{"dev-env"}
	return f, b
}
func TestPrivatePilotTwoSlotsConcurrentAndCompletedReplay(t *testing.T) {
	f, b := pilotAPI(t, interceptor.Funcs{})
	second := &Server{Policy: f.srv.Policy, Client: f.c, Live: f.c, Auth: f.auth, Now: f.srv.Now, Templates: f.srv.Templates, Projects: f.srv.Projects, PrivatePilot: b.Policy, ManagedCodexTasks: true, PrivateHistoryRetention: true, RetainedCommonRules: true, PrivateCanaryTemplates: f.srv.PrivateCanaryTemplates, SessionDirectory: f.srv.SessionDirectory, SessionDirectoryProjects: f.srv.SessionDirectoryProjects}
	handlers := []http.Handler{f.h, second.Handler()}
	var wg sync.WaitGroup
	results := make(chan int, 4)
	for i := range 4 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data, _ := json.Marshal(b.Policy.Request(i / 2))
			req := httptest.NewRequest(http.MethodPost, apiv1.SessionsPath, bytes.NewReader(data))
			req.Header.Set("Authorization", "Bearer "+tokClient)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handlers[i%2].ServeHTTP(w, req)
			results <- w.Code
		}(i)
	}
	wg.Wait()
	close(results)
	for code := range results {
		if code != 200 && code != 201 {
			t.Fatal("fixed slot create", code)
		}
	}
	var list v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &list); err != nil || len(list.Items) != 2 {
		t.Fatal("wrong count", len(list.Items), err)
	}
	for i := range list.Items {
		s := &list.Items[i]
		uid := s.UID
		s.Status.Agent = &v1alpha1.AgentStatus{Status: "exited"}
		if err := f.c.Status().Update(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		slot := 0
		if s.Name == projectcanary.PilotSessionName(1) {
			slot = 1
		}
		w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, b.Policy.Request(slot))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var kept v1alpha1.AgentSession
		if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(s), &kept); err != nil || kept.UID != uid || kept.Spec.CommonRules == nil || kept.Spec.SessionDirectory == nil {
			t.Fatal("replay replaced or lost binding", err)
		}
	}
	req := b.Policy.Request(0)
	req.IdempotencyKey = "third"
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, req); w.Code < 400 {
		t.Fatal("third slot admitted")
	}
}
func TestPrivatePilotExpiryScopeAndLostCreate(t *testing.T) {
	for _, kind := range []string{"expired", "before", "wrong-caller", "wrong-project", "global-enabled", "disabled", "seal-missing", "lost-ack", "unknown-create", "expiry-during-read", "config-drift"} {
		t.Run(kind, func(t *testing.T) {
			var f *fixture
			var b testutil.PilotFixture
			creates := 0
			reads := 0
			funcs := interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				if _, ok := o.(*v1alpha1.AgentSession); ok {
					creates++
					if kind == "unknown-create" {
						return errors.New("synthetic unconfirmed create")
					}
					if kind == "lost-ack" {
						if err := c.Create(ctx, o, opts...); err != nil {
							return err
						}
						return errors.New("synthetic lost response")
					}
				}
				return c.Create(ctx, o, opts...)
			}, Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
				if _, ok := o.(*corev1.ConfigMap); ok {
					reads++
					if kind == "expiry-during-read" && f != nil && reads == 3 {
						f.now = b.Policy.ExpiresAt
					}
				}
				return c.Get(ctx, key, o, opts...)
			}}
			f, b = pilotAPI(t, funcs)
			req := b.Policy.Request(0)
			token := tokClient
			switch kind {
			case "expired":
				f.now = b.Policy.ExpiresAt
			case "before":
				f.now = b.Policy.NotBefore.Add(-time.Second)
			case "wrong-caller":
				token = tokHuman
			case "wrong-project":
				req.Project = "other"
			case "global-enabled":
				f.srv.PrivateProjectTasks = true
			case "disabled":
				f.srv.PrivatePilot = nil
			case "seal-missing":
				if err := f.c.Delete(context.Background(), b.Seal); err != nil {
					t.Fatal(err)
				}
			case "config-drift":
				b.Policy.ExpiresAt = b.Policy.ExpiresAt.Add(time.Minute)
			}
			w := f.do(http.MethodPost, apiv1.SessionsPath, token, req)
			if kind == "lost-ack" {
				if w.Code != 200 || creates != 1 {
					t.Fatal("lost response not bound", w.Code, w.Body.String(), creates)
				}
			} else if w.Code < 400 {
				t.Fatal("refusal accepted", kind, w.Code, w.Body.String())
			}
			if kind != "lost-ack" && kind != "unknown-create" && creates != 0 {
				t.Fatal("refusal created", creates)
			}
		})
	}
}
func TestPrivatePilotRetainedSlotsAndChildRefusal(t *testing.T) {
	f, b := pilotAPI(t, interceptor.Funcs{})
	s := b.Session(t, 0)
	s.ResourceVersion = ""
	if err := f.c.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	// A policy change cannot reinterpret an old consumed slot as a new allowance.
	b.Policy.Prompts[0] += " changed"
	if err := f.c.Delete(context.Background(), b.Seal); err != nil {
		t.Fatal(err)
	}
	b.Policy.SealUID = "66666666-6666-4666-8666-666666666666"
	b.Seal.UID = types.UID(b.Policy.SealUID)
	b.Seal.ResourceVersion = ""
	b.Seal.Data["policySHA256"] = b.Policy.Digest()
	if err := f.c.Create(context.Background(), b.Seal); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, b.Policy.Request(1)); w.Code != 409 {
		t.Fatal("old slot drift accepted", w.Code, w.Body.String())
	}
	// Fixed name remains a pilot marker even when annotations disappear.
	s.Annotations = nil
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: s.Name, UID: "55555555-5555-4555-8555-555555555555", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))}}, Spec: corev1.PodSpec{ServiceAccountName: controller.ServiceAccountName}}
	if err := f.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(s.Namespace + "/" + controller.ServiceAccountName)
	id.PodName = s.Name
	id.PodUID = string(pod.UID)
	token := "pilot-token"
	f.auth[token] = id
	for _, req := range []apiv1.CreateSessionRequest{{Repo: "repo", Agent: "claude", Mode: "task", Model: "claude-opus-5-5"}, {CanaryID: "anything"}, b.Policy.Request(1)} {
		if w := f.do(http.MethodPost, apiv1.SessionsPath, token, req); w.Code < 400 {
			t.Fatal("pilot child escape")
		}
	}
	var kept v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: s.Namespace, Name: s.Name}, &kept); err != nil {
		t.Fatal(err)
	}
	kept.DeletionTimestamp = &metav1.Time{Time: f.now}
	if !projectcanary.IsPilot(&kept) {
		t.Fatal("pilot mark lost")
	}
}
