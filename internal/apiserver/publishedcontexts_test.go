package apiserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/contextpublication"
)

func enableContexts(f *fixture) {
	f.srv.PublishedContexts = &contextpublication.KubeStore{Client: f.c, Live: f.c, Namespace: "dev-env-system"}
	f.srv.ContextReadProjects = []string{"sample"}
}
func contextSession(t *testing.T, f *fixture, name, parent string) string {
	t.Helper()
	token := claimSession(t, f, name, parent)
	s := f.session(name)
	s.Generation = 1
	if f.c.Update(context.Background(), s) != nil {
		t.Fatal("session fixture")
	}
	var pod corev1.Pod
	if f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &pod) != nil {
		t.Fatal("pod fixture")
	}
	pod.Status.Phase = corev1.PodRunning
	if f.c.Status().Update(context.Background(), &pod) != nil {
		t.Fatal("pod status")
	}
	return token
}
func contextReq() apiv1.PublishContextRequest {
	return apiv1.PublishContextRequest{Kind: "handoff", Key: "task-174", Text: "PRIVATE_EXPLICIT_SYNTHETIC_CONTEXT"}
}
func TestPublishedContextImmutableRetryAndStoppedAbsentSourceRead(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	sender := contextSession(t, f, "source", humanSA)
	reader := contextSession(t, f, "reader", clientSA)
	w := f.do(http.MethodPost, apiv1.PublishedContextsPath, sender, contextReq())
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	original := decode[apiv1.PublishContextResult](t, w)
	if strings.Contains(w.Body.String(), contextReq().Text) || original.Metadata.StopState != "Unknown" || original.Metadata.Attribution.ObservedModelState != "Unknown" {
		t.Fatal("ack leaked text or invented proof")
	}
	w = f.do(http.MethodPost, apiv1.PublishedContextsPath, sender, contextReq())
	if w.Code != http.StatusOK || decode[apiv1.PublishContextResult](t, w).Metadata.UID != original.Metadata.UID {
		t.Fatal("retry", w.Code, w.Body.String())
	}
	changed := contextReq()
	changed.Text = "changed"
	w = f.do(http.MethodPost, apiv1.PublishedContextsPath, sender, changed)
	wantError(t, w, http.StatusConflict, apiv1.CodeConflict)
	if strings.Contains(w.Body.String(), contextReq().Text) {
		t.Fatal("conflict leaked content")
	}
	// Reconstruct both server and store. No source lookup, executor, resume or
	// Ready condition belongs to an authorized immutable payload read.
	f.srv = &Server{Client: f.c, Live: f.c, Auth: f.auth, Policy: f.srv.Policy, Projects: f.srv.Projects, PublishedContexts: &contextpublication.KubeStore{Client: f.c, Live: f.c, Namespace: "dev-env-system"}, ContextReadProjects: []string{"sample"}, Log: f.srv.Log}
	f.h = f.srv.Handler()
	f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if key.Name == "source" {
			t.Fatal("read consulted stopped source")
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	var pod corev1.Pod
	key := types.NamespacedName{Namespace: sessionNS, Name: "source"}
	if f.c.Get(context.Background(), key, &pod) != nil || f.c.Delete(context.Background(), &pod) != nil {
		t.Fatal("source pod deletion fixture")
	}
	source := f.session("source")
	source.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if f.c.Update(context.Background(), source) != nil {
		t.Fatal("stopped fixture")
	}
	path := apiv1.PublishedContextsPath + "/" + original.Metadata.Name + "?project=sample"
	for stage := 0; stage < 3; stage++ {
		w = f.do(http.MethodGet, path, reader, nil)
		if w.Code != http.StatusOK || decode[apiv1.PublishedContext](t, w).Text != contextReq().Text || decode[apiv1.PublishedContext](t, w).Metadata.Source.UID != original.Metadata.Source.UID {
			t.Fatal("stopped/deleting/absent read", stage, w.Code, w.Body.String())
		}
		switch stage {
		case 0:
			if f.c.Delete(context.Background(), source) != nil {
				t.Fatal("delete fixture")
			}
		case 1:
			source = f.session("source")
			source.Finalizers = nil
			if f.c.Update(context.Background(), source) != nil {
				t.Fatal("absent fixture")
			}
		}
	}
	w = f.do(http.MethodGet, apiv1.PublishedContextsPath+"?project=sample", reader, nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), contextReq().Text) || len(decode[apiv1.PublishedContextList](t, w).Items) != 1 {
		t.Fatal("metadata list leaked text")
	}
}

func TestPublishedContextFinalLiveFenceAndProjectExistenceMask(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	sender := contextSession(t, f, "source", humanSA)
	// The source stops during catalog I/O. A stale first observation cannot
	// authorize publication after the final uncached live fence.
	f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		err := c.Get(ctx, key, obj, opts...)
		if _, ok := obj.(*corev1.ConfigMap); ok && err == nil {
			var pod corev1.Pod
			if c.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: "source"}, &pod) != nil {
				t.Fatal("fixture")
			}
			pod.Status.Phase = corev1.PodSucceeded
			if c.Status().Update(ctx, &pod) != nil {
				t.Fatal("fixture")
			}
		}
		return err
	}})
	w := f.do(http.MethodPost, apiv1.PublishedContextsPath, sender, contextReq())
	wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
	items, _, err := f.srv.PublishedContexts.List(context.Background(), "sample", 20, "")
	if err != nil || len(items) != 0 {
		t.Fatal("stopped source created publication")
	}
	f.srv.Live = f.c
	reader := contextSession(t, f, "reader", clientSA)
	snapshot, _ := claimSnapshot(f.session("reader"))
	src := f.session("reader")
	spec := coord.PublishedContextSpec{Identity: coord.ContextIdentity{SessionUID: string(src.UID), Kind: "result", Key: "foreign"}, Source: coord.SessionIdentity{Namespace: src.Namespace, Name: src.Name, UID: string(src.UID)}, SourceGeneration: 1, SourceParent: src.Spec.Parent, Project: "other", Repository: "alias", Actor: coord.ClaimActor{Kind: "session", ServiceAccount: sessionNS + "/dev-env-agent", PodName: "reader", PodUID: f.auth[reader].PodUID}, Provenance: coord.ClaimProvenance{CatalogRevision: snapshot.CatalogRevision(), RulesRevision: snapshot.RulesRevision(), GitHub: "thaynes43/dev-env", DefaultBranch: "main", InitialPinState: "Unknown"}, Attribution: coord.ContextAttribution{Provider: "claude", RequestedModel: "claude-opus-5-5", ObservedModelState: "Unknown", UsageState: "Unknown", CostState: "Unknown"}, Origin: "ExplicitPublished", Completeness: "ExplicitExcerpt", StopState: "Unknown", Text: "FOREIGN_CONTEXT", ContentSHA256: apiv1.ContextHash("FOREIGN_CONTEXT"), ByteCount: 15}
	// Seed a foreign resource directly through the test API, including malformed
	// foreign metadata. Its presence must be indistinguishable from absence.
	foreign, _, err := f.srv.PublishedContexts.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{foreign.Name, apiv1.ContextName("absent-uid", "result", "foreign")} {
		w = f.do(http.MethodGet, apiv1.PublishedContextsPath+"/"+name+"?project=sample", reader, nil)
		wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
		if strings.Contains(w.Body.String(), "FOREIGN_CONTEXT") {
			t.Fatal("foreign leak")
		}
	}
}
func TestPublishedContextSeparateACLAndRefusedCallerProvenance(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	sender := contextSession(t, f, "source", humanSA)
	for _, token := range []string{tokHuman, tokClient} {
		w := f.do(http.MethodPost, apiv1.PublishedContextsPath, token, contextReq())
		wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
		w = f.do(http.MethodGet, apiv1.PublishedContextsPath+"?project=sample", token, nil)
		wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
	}
	f.srv.ContextReadProjects = nil
	f.srv.TaskClaimReadProjects = []string{"sample"}
	w := f.do(http.MethodGet, apiv1.PublishedContextsPath+"?project=sample", sender, nil)
	wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
	for _, body := range []string{`{"kind":"handoff","key":"x","text":"x","provider":"codex"}`, `{"kind":"handoff","key":"x","text":"x","sourceUID":"forged"}`, string([]byte{'{', 255, '}'})} {
		w = f.do(http.MethodPost, apiv1.PublishedContextsPath, sender, body)
		wantError(t, w, http.StatusBadRequest, apiv1.CodeBadRequest)
	}
	s := f.session("source")
	if f.c.Delete(context.Background(), s) != nil {
		t.Fatal("deleting fixture")
	}
	w = f.do(http.MethodPost, apiv1.PublishedContextsPath, sender, contextReq())
	wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
}

func TestPublishedContextCoordinatorReadScopeAndReplacedPublisher(t *testing.T) {
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	sender := contextSession(t, f, "source", humanSA)
	w := f.do(http.MethodPost, apiv1.PublishedContextsPath, sender, contextReq())
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	published := decode[apiv1.PublishContextResult](t, w)
	path := apiv1.PublishedContextsPath + "/" + published.Metadata.Name + "?project=sample"
	w = f.do(http.MethodGet, path, tokCoordinator, nil)
	if w.Code != http.StatusOK {
		t.Fatal("explicit coordinator read", w.Code, w.Body.String())
	}
	w = f.do(http.MethodPost, apiv1.PublishedContextsPath, tokCoordinator, contextReq())
	wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
	f.srv.ContextReadProjects = nil
	f.srv.TaskClaimReadProjects = []string{"sample"}
	w = f.do(http.MethodGet, path, tokCoordinator, nil)
	wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
	f.srv.ContextReadProjects = []string{"sample"}
	var pod corev1.Pod
	if f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "source"}, &pod) != nil {
		t.Fatal("fixture")
	}
	pod.UID = "replacement-pod"
	if f.c.Update(context.Background(), &pod) != nil {
		t.Fatal("fixture")
	}
	w = f.do(http.MethodPost, apiv1.PublishedContextsPath, sender, contextReq())
	wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
	w = f.do(http.MethodGet, path, tokCoordinator, nil)
	if w.Code != http.StatusOK || decode[apiv1.PublishedContext](t, w).Metadata.Source.UID != published.Metadata.Source.UID {
		t.Fatal("replaced source altered durable read")
	}
}
func TestPublishedContextLostACKUnknownAndUTF8ByteBound(t *testing.T) {
	f := newFixtureWith(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if err := c.Create(ctx, obj, opts...); err != nil {
			return err
		}
		if _, ok := obj.(*coord.PublishedContext); ok {
			return errors.New("lost ACK")
		}
		return nil
	}})
	bindCatalogFixture(t, f)
	enableContexts(f)
	sender := contextSession(t, f, "source", humanSA)
	req := contextReq()
	req.Text = strings.Repeat("é", apiv1.MaxContextBytes/2)
	w := f.do(http.MethodPost, apiv1.PublishedContextsPath, sender, req)
	if w.Code != http.StatusOK {
		t.Fatal("lost ACK or UTF8 boundary", w.Code, w.Body.String())
	}
	req.Text += "é"
	w = f.do(http.MethodPost, apiv1.PublishedContextsPath, sender, req)
	wantError(t, w, http.StatusBadRequest, apiv1.CodeBadRequest)
	// JSON escaping can expand a valid decoded 64KiB chunk sixfold.
	// The envelope bound must not accidentally reduce the promised byte limit.
	req.Key = "escaped"
	req.Text = strings.Repeat("\x00", apiv1.MaxContextBytes)
	w = f.do(http.MethodPost, apiv1.PublishedContextsPath, sender, req)
	if w.Code != http.StatusOK {
		t.Fatal("valid escaped byte boundary refused", w.Code, w.Body.String())
	}
	f.srv.PublishedContexts.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		return errors.New("uncached unavailable")
	}})
	req = contextReq()
	req.Key = "second"
	w = f.do(http.MethodPost, apiv1.PublishedContextsPath, sender, req)
	wantError(t, w, http.StatusServiceUnavailable, apiv1.CodeUnavailable)
}
