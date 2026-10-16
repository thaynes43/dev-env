package apiserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

func TestPrivatePilotDeletingSlotRefusesBothAdmissions(t *testing.T) {
	f, b := pilotAPI(t, interceptor.Funcs{})
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, b.Policy.Request(0)); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	var original v1alpha1.AgentSession
	key := client.ObjectKey{Namespace: "dev-agents", Name: projectcanary.PilotSessionName(0)}
	if err := f.c.Get(context.Background(), key, &original); err != nil {
		t.Fatal(err)
	}
	uid := original.UID
	if err := f.c.Delete(context.Background(), &original); err != nil {
		t.Fatal(err)
	}
	for slot := range 2 {
		if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, b.Policy.Request(slot)); w.Code != http.StatusConflict {
			t.Fatal("deleting original must stop both admissions", slot, w.Code, w.Body.String())
		}
	}
	var kept v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), key, &kept); err != nil || kept.UID != uid || kept.DeletionTimestamp.IsZero() {
		t.Fatal("original consumed identity lost", err)
	}
	var sessions v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &sessions); err != nil || len(sessions.Items) != 1 {
		t.Fatal("peer admitted after deletion", err)
	}
}

func TestPrivatePilotPreflightCannotPersistOrFallback(t *testing.T) {
	for _, kind := range []string{"valid", "before", "expired", "schema-error", "occupied", "disabled", "ordinary", "canary", "wrong-caller"} {
		t.Run(kind, func(t *testing.T) {
			dryCalls := 0
			realCalls := 0
			seeding := true
			f, b := pilotAPI(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				if seeding {
					return c.Create(ctx, o, opts...)
				}
				co := (&client.CreateOptions{}).ApplyOptions(opts)
				if len(co.DryRun) != 1 || co.DryRun[0] != metav1.DryRunAll {
					realCalls++
					return errors.New("unexpected persistent create")
				}
				dryCalls++
				if kind == "schema-error" {
					return apierrors.NewInvalid(schema.GroupKind{Group: v1alpha1.GroupVersion.Group, Kind: "AgentSession"}, o.GetName(), nil)
				}
				return nil
			}})
			req := b.Policy.Request(0)
			token := tokClient
			switch kind {
			case "before":
				f.now = b.Policy.NotBefore.Add(-time.Hour)
			case "expired":
				f.now = b.Policy.ExpiresAt.Add(time.Hour)
			case "disabled":
				f.srv.PrivatePilot = nil
			case "ordinary":
				req.Project = ""
			case "canary":
				req = apiv1.CreateSessionRequest{CanaryID: "ignored"}
			case "wrong-caller":
				token = tokHuman
			case "occupied":
				if err := f.c.Create(context.Background(), b.Session(t, 0)); err != nil {
					t.Fatal(err)
				}
			}
			seeding = false
			w := f.do(http.MethodPost, apiv1.PilotPreflightPath, token, req)
			if kind == "valid" || kind == "before" || kind == "expired" {
				if w.Code != 200 || dryCalls != 1 {
					t.Fatal(w.Code, w.Body.String(), dryCalls)
				}
				var result apiv1.PilotPreflight
				if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.DryRun != "All" || result.Persisted {
					t.Fatal("not explicit dry-run")
				}
			} else if w.Code < 400 {
				t.Fatal("bad preflight accepted", w.Body.String())
			}
			if realCalls != 0 {
				t.Fatal("persistent create attempted")
			}
			if kind != "occupied" {
				var absent v1alpha1.AgentSession
				if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: "dev-agents", Name: projectcanary.PilotSessionName(0)}, &absent); !apierrors.IsNotFound(err) {
					t.Fatal("dry-run persisted", err)
				}
			}
		})
	}
}

// This passes the actual shared API builder output to a real kube-apiserver.
// No reconciler, kubelet or provider runs, and every Session write is DryRunAll.
func TestPrivatePilotPreflightRealSchemaAdmission(t *testing.T) {
	realClient, err := client.New(envConfig(t), client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, ns := range []string{"dev-agents"} {
		if err := realClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}
	}
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"})
	if err := realClient.Get(ctx, types.NamespacedName{Name: "agentsessions.dev-env.haynesops.com"}, crd); err != nil {
		t.Fatal(err)
	}
	corrected := crd.DeepCopy()
	old := crd.DeepCopy()
	captured, err := os.ReadFile("testdata/agentsession-installed-spec-20261016.json")
	if err != nil {
		t.Fatal(err)
	}
	var installedSpec map[string]any
	if err := json.Unmarshal(captured, &installedSpec); err != nil {
		t.Fatal(err)
	}
	oldJSON, _ := json.Marshal(installedSpec)
	if fmt.Sprintf("%x", sha256.Sum256(oldJSON)) != "6d5db36566f417cabb83d8b5151153b6f8c44c762f24e86a91f8e63c42e9ac63" {
		t.Fatal("captured installed spec schema fingerprint changed")
	}
	versions := old.Object["spec"].(map[string]any)["versions"].([]any)
	versions[0].(map[string]any)["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)["properties"].(map[string]any)["spec"] = installedSpec
	t.Logf("captured installed/released spec schema SHA256=%x", sha256.Sum256(oldJSON))
	if err := realClient.Update(ctx, old); err != nil {
		t.Fatal(err)
	}
	restore := func() {
		cur := corrected.DeepCopy()
		if err := realClient.Get(ctx, client.ObjectKeyFromObject(cur), cur); err != nil {
			t.Fatal(err)
		}
		cur.Object["spec"] = corrected.Object["spec"]
		if err := realClient.Update(ctx, cur); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(restore)
	var payload []byte
	f, b := pilotAPI(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		co := (&client.CreateOptions{}).ApplyOptions(opts)
		if len(co.DryRun) != 1 || co.DryRun[0] != metav1.DryRunAll {
			t.Fatal("real API write was not dry-run")
		}
		o.SetUID("")
		o.SetGeneration(0)
		o.SetCreationTimestamp(metav1.Time{})
		payload, _ = json.Marshal(o)
		return realClient.Create(ctx, o, opts...)
	}, Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if _, ok := o.(*v1alpha1.AgentSession); ok {
			return realClient.Get(ctx, k, o, opts...)
		}
		return c.Get(ctx, k, o, opts...)
	}})
	request := b.Policy.Request(0)
	call := func() *httptest.ResponseRecorder {
		return f.do(http.MethodPost, apiv1.PilotPreflightPath, tokClient, request)
	}
	var w *httptest.ResponseRecorder
	for i := 0; i < 10; i++ {
		w = call()
		if w.Code == 422 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if w.Code != 422 || !strings.Contains(w.Body.String(), "matching managed canary binding") || !strings.Contains(w.Body.String(), "directory enrollment requires") {
		t.Fatal("released schema counterexample not reproduced", w.Code, w.Body.String())
	}
	oldPayload := append([]byte(nil), payload...)
	t.Logf("released-schema actual generated payload rejected; SHA256=%x", sha256.Sum256(payload))
	restore()
	for i := 0; i < 10; i++ {
		w = call()
		if w.Code == 200 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if w.Code != 200 {
		t.Fatal("corrected schema refused exact shared-builder object", w.Code, w.Body.String())
	}
	if !bytes.Equal(payload, oldPayload) {
		t.Fatal("schema comparison changed the submitted payload")
	}
	var absent v1alpha1.AgentSession
	if err := realClient.Get(ctx, types.NamespacedName{Namespace: "dev-agents", Name: projectcanary.PilotSessionName(0)}, &absent); !apierrors.IsNotFound(err) {
		t.Fatal("real dry-run persisted actor", err)
	}
	if out := os.Getenv("C7_PILOT_SCHEMA_EVIDENCE_DIR"); out != "" {
		if err := os.WriteFile(filepath.Join(out, "generated-session.json"), append(payload, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "real-schema-dryrun.json"), []byte(fmt.Sprintf(`{"samePayloadSHA256":"%x","releasedSchema":"REJECTED422","correctedSchema":"ACCEPTED200","persisted":false}`, sha256.Sum256(payload))), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("corrected-schema SAME generated payload accepted; SHA256=%x; post-dry-run slot absent", sha256.Sum256(payload))
}
