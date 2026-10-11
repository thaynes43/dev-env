package apiserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

func enableClaims(f *fixture) {
	f.srv.TaskClaims = &taskclaim.KubeStore{Client: f.c, Live: f.c, Namespace: "dev-env-system"}
	f.srv.TaskClaimReadProjects = []string{"sample"}
}

func claimSession(t *testing.T, f *fixture, name, parent string) string {
	t.Helper()
	token := f.sessionPod(name, "dev", 0)
	s := f.session(name)
	s.Spec.Parent, s.Spec.Repo = parent, "alias"
	catalog, err := projectcatalog.Parse([]byte(apiProjectCatalog))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("sample", "alias")
	if err != nil {
		t.Fatal(err)
	}
	data, err := snapshot.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	s.Annotations = map[string]string{v1alpha1.AnnotationProjectSnapshot: string(data)}
	s.Spec.Prompt = "PRIVATE_PROMPT_MUST_NOT_LEAK"
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	s.Status.Agent = &v1alpha1.AgentStatus{Head: strings.Repeat("a", 40), LastHeartbeat: ptrClaimTime(f.now)}
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return token
}

func ptrClaimTime(t time.Time) *metav1.Time { m := metav1.NewTime(t); return &m }

func claimRequest(key string) apiv1.CreateTaskClaimRequest {
	return apiv1.CreateTaskClaimRequest{Identity: apiv1.TaskClaimIdentity{Project: "sample", Repository: "alias", TaskKey: key}}
}

func TestTaskClaimsTwoAPIsAtomicOwnerAndReconstruction(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableClaims(f)
	tokens := []string{claimSession(t, f, "parent-a", humanSA), claimSession(t, f, "parent-b", clientSA)}
	second := &Server{Client: f.c, Live: f.c, Auth: f.auth, Policy: f.srv.Policy, Projects: f.srv.Projects,
		TaskClaims: &taskclaim.KubeStore{Client: f.c, Live: f.c, Namespace: "dev-env-system"}, TaskClaimReadProjects: []string{"sample"}, Now: f.srv.Now, Log: f.srv.Log}
	handlers := []http.Handler{f.h, second.Handler()}
	results := make([]*httptest.ResponseRecorder, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range handlers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req := httptest.NewRequest(http.MethodPost, apiv1.TaskClaimsPath, strings.NewReader(`{"identity":{"project":"sample","repository":"alias","taskKey":"issue:175"}}`))
			req.Header.Set("Authorization", "Bearer "+tokens[i])
			results[i] = httptest.NewRecorder()
			handlers[i].ServeHTTP(results[i], req)
		}(i)
	}
	close(start)
	wg.Wait()
	winner := 0
	if results[1].Code == http.StatusCreated {
		winner = 1
	}
	if results[winner].Code != http.StatusCreated || results[1-winner].Code != http.StatusConflict {
		t.Fatalf("atomic results: %d %s; %d %s", results[0].Code, results[0].Body.String(), results[1].Code, results[1].Body.String())
	}
	won := decode[apiv1.TaskClaimResult](t, results[winner])
	conflict := decode[apiv1.TaskClaimResult](t, results[1-winner])
	if won.Claim.UID == "" || won.Claim.UID != conflict.Claim.UID || won.Claim.Owner != conflict.Claim.Owner {
		t.Fatal("conflict changed original ownership")
	}
	if won.Claim.Provenance.InitialPinState != "Unknown" || won.Claim.Provenance.ReportedHead != strings.Repeat("a", 40) || won.Claim.StopState != "Unknown" {
		t.Fatal("source/stop proof inflated", won)
	}
	for _, w := range results {
		if strings.Contains(w.Body.String(), "PRIVATE_PROMPT") || strings.Contains(w.Body.String(), "EXACT_ACCEPTED_RULES") || strings.Contains(w.Body.String(), tokens[0]) {
			t.Fatal("claim leaked private content")
		}
	}
	// Reconstruct the server/store against the same durable backend.
	f.h = second.Handler()
	changed := f.session(won.Claim.Owner.Name)
	changed.Status.Agent.Head = strings.Repeat("b", 40)
	if err := f.c.Status().Update(context.Background(), changed); err != nil {
		t.Fatal(err)
	}
	var catalog corev1.ConfigMap
	if err := f.c.Get(context.Background(), f.srv.Projects.Key, &catalog); err != nil {
		t.Fatal(err)
	}
	catalog.Data["catalog.json"] = strings.ReplaceAll(apiProjectCatalog, "EXACT_ACCEPTED_RULES", "NEW_RULES_REVISION")
	if err := f.c.Update(context.Background(), &catalog); err != nil {
		t.Fatal(err)
	}
	w := f.do(http.MethodPost, apiv1.TaskClaimsPath, tokens[winner], claimRequest("issue:175"))
	if w.Code != http.StatusOK || decode[apiv1.TaskClaimResult](t, w).Claim.UID != won.Claim.UID || !reflect.DeepEqual(decode[apiv1.TaskClaimResult](t, w).Claim.Provenance, won.Claim.Provenance) {
		t.Fatal("same-owner retry/reconstruction changed claim", w.Code, w.Body.String())
	}
	reader := tokens[1-winner]
	owner := f.session(won.Claim.Owner.Name)
	owner.Status.Phase = v1alpha1.PhaseSuspended
	if err := f.c.Status().Update(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodGet, apiv1.TaskClaimsPath+"/"+won.Claim.Name+"?project=sample", reader, nil)
	if w.Code != http.StatusOK || decode[apiv1.TaskClaim](t, w).OwnerState != "Suspended" || decode[apiv1.TaskClaim](t, w).StopState != "Unknown" {
		t.Fatal("observed stopped state became transfer proof", w.Code, w.Body.String())
	}
	owner = f.session(won.Claim.Owner.Name)
	if err := f.c.Delete(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodGet, apiv1.TaskClaimsPath+"/"+won.Claim.Name+"?project=sample", reader, nil)
	if w.Code != http.StatusOK || decode[apiv1.TaskClaim](t, w).OwnerState != "Deleting" {
		t.Fatal("deleting owner metadata not retained", w.Code, w.Body.String())
	}
	owner = f.session(won.Claim.Owner.Name)
	owner.Finalizers = nil
	if err := f.c.Update(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample", reader, nil)
	list := decode[apiv1.TaskClaimList](t, w)
	if w.Code != http.StatusOK || len(list.Items) != 1 || list.Items[0].OwnerState != "Unknown" || list.Items[0].UID != won.Claim.UID {
		t.Fatal("absent owner released or hid claim", w.Code, w.Body.String())
	}
	w = f.do(http.MethodPost, apiv1.TaskClaimsPath, reader, claimRequest("issue:175"))
	if w.Code != http.StatusConflict {
		t.Fatal("absence transferred ownership", w.Code, w.Body.String())
	}
	stored, err := f.srv.TaskClaims.Get(context.Background(), won.Claim.Name)
	if err != nil {
		t.Fatal(err)
	}
	foreignSpec := stored.Spec
	foreignSpec.Identity.Project = "other"
	foreign, _, err := f.srv.TaskClaims.Create(context.Background(), foreignSpec)
	if err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodGet, apiv1.TaskClaimsPath+"/"+foreign.Name+"?project=sample", reader, nil)
	wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
}

func TestTaskClaimsLostAcknowledgementAndUnknownConfirmation(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "recover", true: "unknown"}[unavailable], func(t *testing.T) {
			f := newFixtureWith(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if err := c.Create(ctx, obj, opts...); err != nil {
					return err
				}
				if _, ok := obj.(*coord.TaskClaim); ok {
					return errors.New("synthetic lost ACK")
				}
				return nil
			}})
			bindCatalogFixture(t, f)
			enableClaims(f)
			token := claimSession(t, f, "owner", humanSA)
			if unavailable {
				f.srv.TaskClaims.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					return errors.New("synthetic confirmation unavailable")
				}})
			}
			w := f.do(http.MethodPost, apiv1.TaskClaimsPath, token, claimRequest("task:bounded"))
			if unavailable {
				wantError(t, w, http.StatusServiceUnavailable, apiv1.CodeUnavailable)
				f.srv.TaskClaims.Live = f.c
				w = f.do(http.MethodPost, apiv1.TaskClaimsPath, token, claimRequest("task:bounded"))
			}
			if w.Code != http.StatusOK || decode[apiv1.TaskClaimResult](t, w).Outcome != "Confirmed" {
				t.Fatal("lost ACK did not confirm same owner", w.Code, w.Body.String())
			}
		})
	}
}

func TestTaskClaimsDedicatedACLAndUIDFence(t *testing.T) {
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableClaims(f)
	token := claimSession(t, f, "owned", coordinatorSA)
	other := claimSession(t, f, "foreign", clientSA)
	for _, caller := range []string{tokHuman, tokClient, tokStranger, ""} {
		w := f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample", caller, nil)
		if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
			t.Fatal("legacy caller gained claim rights", caller, w.Code)
		}
		w = f.do(http.MethodPost, apiv1.TaskClaimsPath, caller, claimRequest("issue:11"))
		if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
			t.Fatal("legacy caller gained creation", caller, w.Code)
		}
	}
	for _, query := range []string{"", "?project=other", "?project=sample&limit=101"} {
		w := f.do(http.MethodGet, apiv1.TaskClaimsPath+query, token, nil)
		if w.Code != http.StatusForbidden && w.Code != http.StatusBadRequest {
			t.Fatal("read scope/limit widened", w.Code)
		}
	}
	r := claimRequest("issue:1")
	s := f.session("owned")
	r.Owner = &apiv1.TaskClaimOwner{Namespace: s.Namespace, Name: s.Name, UID: string(s.UID)}
	w := f.do(http.MethodPost, apiv1.TaskClaimsPath, tokCoordinator, r)
	if w.Code != http.StatusCreated {
		t.Fatal("verified direct child refused", w.Code, w.Body.String())
	}
	won := decode[apiv1.TaskClaimResult](t, w)
	if won.Claim.Actor.Kind != "coordinator" || won.Claim.Owner.UID != string(s.UID) {
		t.Fatal("actor/owner not attributed")
	}
	r.Owner.UID = "replaced-owner-uid"
	if f.do(http.MethodPost, apiv1.TaskClaimsPath, tokCoordinator, r).Code != http.StatusForbidden {
		t.Fatal("wrong child UID accepted")
	}
	r.Owner.UID = string(s.UID)
	if f.do(http.MethodPost, apiv1.TaskClaimsPath, other, r).Code != http.StatusForbidden {
		t.Fatal("session claimed peer")
	}
	foreign := f.session("foreign")
	r.Owner = &apiv1.TaskClaimOwner{Namespace: foreign.Namespace, Name: foreign.Name, UID: string(foreign.UID)}
	if f.do(http.MethodPost, apiv1.TaskClaimsPath, tokCoordinator, r).Code != http.StatusForbidden {
		t.Fatal("coordinator claimed foreign child")
	}
	// Cached Session and token's Pod still name the old UID; Live replacement fails closed.
	s.UID = "new-session-uid"
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if f.do(http.MethodPost, apiv1.TaskClaimsPath, token, claimRequest("issue:2")).Code != http.StatusForbidden {
		t.Fatal("replaced Session adopted old token")
	}
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "owned"}, &pod); err != nil {
		t.Fatal(err)
	}
	pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))}
	if err := f.c.Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	r.Owner = nil
	w = f.do(http.MethodPost, apiv1.TaskClaimsPath, token, r)
	if w.Code != http.StatusConflict || decode[apiv1.TaskClaimResult](t, w).Claim.Owner.UID == string(s.UID) {
		t.Fatal("replacement UID took existing claim", w.Code, w.Body.String())
	}
	f.srv.TaskClaims = nil
	w = f.do(http.MethodPost, apiv1.TaskClaimsPath, token, claimRequest("issue:3"))
	wantError(t, w, http.StatusServiceUnavailable, apiv1.CodeUnavailable)
}

func TestTaskClaimsCatalogAndOwnerRecheckBeforeCreate(t *testing.T) {
	for _, failure := range []string{"saved-snapshot", "missing-catalog", "foreign-repository", "owner-changed-during-catalog"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			bindCatalogFixture(t, f)
			enableClaims(f)
			token := claimSession(t, f, "owner", humanSA)
			request := claimRequest("task:authority")
			switch failure {
			case "saved-snapshot":
				s := f.session("owner")
				s.Annotations[v1alpha1.AnnotationProjectSnapshot] = "{}"
				if err := f.c.Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			case "missing-catalog":
				var cm corev1.ConfigMap
				if err := f.c.Get(context.Background(), f.srv.Projects.Key, &cm); err != nil {
					t.Fatal(err)
				}
				if err := f.c.Delete(context.Background(), &cm); err != nil {
					t.Fatal(err)
				}
			case "foreign-repository":
				request.Identity.Repository = "second"
			case "owner-changed-during-catalog":
				f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*corev1.ConfigMap); ok {
						s := f.session("owner")
						s.Spec.Parent = "changed-parent"
						if err := c.Update(ctx, s); err != nil {
							return err
						}
					}
					return c.Get(ctx, key, obj, opts...)
				}})
			}
			w := f.do(http.MethodPost, apiv1.TaskClaimsPath, token, request)
			if w.Code != http.StatusForbidden {
				t.Fatal("ambiguous authority created claim", w.Code, w.Body.String())
			}
			var claims coord.TaskClaimList
			if err := f.c.List(context.Background(), &claims); err != nil || len(claims.Items) != 0 {
				t.Fatal("refusal mutated ownership", err)
			}
		})
	}
}
