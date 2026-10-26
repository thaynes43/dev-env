package apiserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const bootstrapBody = `{"requestID":"request-178"}`

var bootstrapKey = types.NamespacedName{Namespace: portalBootstrapTargetNamespace, Name: portalBootstrapTargetName}
var bootstrapPermitKey = types.NamespacedName{Namespace: portalBootstrapPermitNamespace, Name: portalBootstrapPermitName}

// Real UID/RV receipts are emulated explicitly, after fake API Create checks.
func bootstrapFixture(t *testing.T, hooks interceptor.Funcs) (*fixture, string, *atomic.Int32) {
	t.Helper()
	count := new(atomic.Int32)
	create := hooks.Create
	hooks.Create = func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == portalBootstrapTargetName && cm.UID == "" {
			count.Add(1)
			if create != nil {
				return create(ctx, c, obj, opts...)
			}
			cm.UID = "created-uid"
			if err := c.Create(ctx, obj, opts...); err != nil {
				return err
			}
			return c.Get(ctx, bootstrapKey, cm)
		}
		return c.Create(ctx, obj, opts...)
	}
	f := newFixtureWith(t, hooks)
	token := f.sessionPod("bootstrap-caller", "full", 0)
	var sess v1alpha1.AgentSession
	key := types.NamespacedName{Namespace: sessionNS, Name: "bootstrap-caller"}
	mustBootstrap(t, f.c.Get(context.Background(), key, &sess))
	sess.Spec.Mode = v1alpha1.ModeRemote
	sess.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	mustBootstrap(t, f.c.Update(context.Background(), &sess))
	sess.Status.Phase = v1alpha1.PhaseRunning
	mustBootstrap(t, f.c.Status().Update(context.Background(), &sess))
	var pod corev1.Pod
	mustBootstrap(t, f.c.Get(context.Background(), key, &pod))
	pod.Status.Phase = corev1.PodRunning
	mustBootstrap(t, f.c.Status().Update(context.Background(), &pod))
	permit := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: bootstrapPermitKey.Namespace, Name: bootstrapPermitKey.Name, UID: "permit-uid"}, Data: map[string]string{
		"state": "active", "requestID": "request-178", "sessionUID": string(sess.UID), "podUID": string(pod.UID), "permitUID": "permit-uid", "sessionGeneration": "0", "payloadDigest": portalBootstrapDigest(), "expiresAt": f.now.Add(time.Hour).Format(time.RFC3339),
	}}
	mustBootstrap(t, f.c.Create(context.Background(), permit))
	f.srv.PortalAdmissionBootstrap = &PortalAdmissionBootstrap{Enabled: true, CreateClosed: func(ctx context.Context, cm *corev1.ConfigMap) error { return f.srv.Client.Create(ctx, cm) }, ExpectedPermitUID: string(permit.UID), ExpectedPermitResourceVersion: permit.ResourceVersion}
	return f, token, count
}
func mustBootstrap(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func bootstrapPost(f *fixture, token string) *httptest.ResponseRecorder {
	return f.do(http.MethodPost, apiv1.PortalAdmissionBootstrapPath, token, bootstrapBody)
}
func bootstrapTarget(t *testing.T, f *fixture, uid string) *corev1.ConfigMap {
	t.Helper()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: bootstrapKey.Namespace, Name: bootstrapKey.Name, UID: types.UID(uid)}, Data: map[string]string{"schema": "1", "state": "closed"}}
	mustBootstrap(t, f.c.Create(context.Background(), cm))
	return cm
}

// Keep the durable store and handler dependencies, but give each simulated
// process its own Server and mutexes rather than copying a live lock.
func bootstrapServerInstance(s *Server) *Server {
	return &Server{
		Client:                   s.Client,
		Live:                     s.Live,
		Auth:                     s.Auth,
		Policy:                   s.Policy,
		Templates:                s.Templates,
		Now:                      s.Now,
		Log:                      s.Log,
		PortalAdmissionBootstrap: s.PortalAdmissionBootstrap,
	}
}

func restartBootstrap(f *fixture) {
	f.srv = bootstrapServerInstance(f.srv)
	f.h = f.srv.Handler()
}
func assertBootstrapConsumed(t *testing.T, f *fixture, uid, outcome string) {
	t.Helper()
	var p corev1.ConfigMap
	mustBootstrap(t, f.c.Get(context.Background(), bootstrapPermitKey, &p))
	if p.Data["state"] != "consumed" || p.Data["targetUID"] != uid || p.Data["outcome"] != outcome || p.Data["consumedFromResourceVersion"] != f.srv.PortalAdmissionBootstrap.ExpectedPermitResourceVersion {
		t.Fatalf("receipt %#v", p.Data)
	}
}

func TestPortalBootstrapCreateReplayRestartAndDrift(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(itoaBool(existing), func(t *testing.T) {
			f, token, count := bootstrapFixture(t, interceptor.Funcs{})
			uid, outcome, status := "created-uid", "Created", 201
			if existing {
				bootstrapTarget(t, f, "original-uid")
				count.Store(0)
				uid, outcome, status = "original-uid", "Present", 200
			}
			w := bootstrapPost(f, token)
			if w.Code != status {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			receipt := decode[apiv1.PortalAdmissionBootstrapResult](t, w)
			if receipt.TargetUID != uid || receipt.TargetRV == "" {
				t.Fatalf("%#v", receipt)
			}
			assertBootstrapConsumed(t, f, uid, outcome)
			restartBootstrap(f)
			if w = bootstrapPost(f, token); w.Code != 200 {
				t.Fatalf("reconcile %d %s", w.Code, w.Body)
			}
			var cm corev1.ConfigMap
			mustBootstrap(t, f.c.Get(context.Background(), bootstrapKey, &cm))
			if string(cm.UID) != uid {
				t.Fatal("UID changed")
			}
			mustBootstrap(t, f.c.Delete(context.Background(), &cm))
			if w = bootstrapPost(f, token); w.Code != 409 {
				t.Fatalf("deleted %d", w.Code)
			}
			bootstrapTarget(t, f, "replacement-uid")
			if w = bootstrapPost(f, token); w.Code != 409 {
				t.Fatalf("replaced %d", w.Code)
			}
			want := int32(1)
			if existing {
				want = 0
			}
			if count.Load() != want {
				t.Fatalf("creates %d", count.Load())
			}
			assertBootstrapConsumed(t, f, uid, outcome)
		})
	}
}
func itoaBool(b bool) string {
	if b {
		return "existing"
	}
	return "created"
}

func TestPortalBootstrapStrictBodyBeforeMutation(t *testing.T) {
	prefix := bootstrapBody + strings.Repeat(" ", 1024-len(bootstrapBody))
	for _, body := range []string{prefix + "x", bootstrapBody + " x", `{"requestID":"request-178","requestID":"other"}`, `{"RequestID":"request-178"}`, `{"requestID":"request-178","x":1}`, `[]`, `null`, `{}`, `{"requestID":null}`, `{"requestID":1}`, `{"requestID":"bad/selector"}`, `{"requestID":"request-178` + string([]byte{0xff}) + `"}`} {
		t.Run(body[:min(len(body), 45)], func(t *testing.T) {
			f, token, count := bootstrapFixture(t, interceptor.Funcs{})
			w := f.do(http.MethodPost, apiv1.PortalAdmissionBootstrapPath, token, body)
			if w.Code != 400 || count.Load() != 0 {
				t.Fatalf("status=%d creates=%d", w.Code, count.Load())
			}
			var p corev1.ConfigMap
			mustBootstrap(t, f.c.Get(context.Background(), bootstrapPermitKey, &p))
			if p.Data["state"] != "active" {
				t.Fatal("mutated permit")
			}
		})
	}
}

func TestPortalBootstrapPermitAndCallerRefusals(t *testing.T) {
	for _, name := range []string{"disabled", "missing-anchor", "bad-anchor", "forged-uid", "recreated", "generation", "expired", "revoked", "blank-revocation", "sessionUID", "podUID", "requestID", "payloadDigest", "state", "human", "client", "stranger", "owner-name", "owner-version", "owner-kind", "owner-uid", "sa", "pod-uid", "pod-deleting", "pod-stopped", "session-uid", "session-deleting", "mode", "operating-mode", "phase"} {
		t.Run(name, func(t *testing.T) {
			f, token, count := bootstrapFixture(t, interceptor.Funcs{})
			var p corev1.ConfigMap
			mustBootstrap(t, f.c.Get(context.Background(), bootstrapPermitKey, &p))
			key := types.NamespacedName{Namespace: sessionNS, Name: "bootstrap-caller"}
			var pod corev1.Pod
			mustBootstrap(t, f.c.Get(context.Background(), key, &pod))
			var sess v1alpha1.AgentSession
			mustBootstrap(t, f.c.Get(context.Background(), key, &sess))
			switch name {
			case "disabled":
				f.srv.PortalAdmissionBootstrap.Enabled = false
			case "missing-anchor":
				f.srv.PortalAdmissionBootstrap.ExpectedPermitUID = ""
			case "bad-anchor":
				f.srv.PortalAdmissionBootstrap.ExpectedPermitResourceVersion = "garbage"
			case "human":
				token = tokHuman
			case "client":
				token = tokClient
			case "stranger":
				token = tokStranger
			case "forged-uid", "recreated":
				mustBootstrap(t, f.c.Delete(context.Background(), &p))
				p.ResourceVersion = ""
				p.UID = "replacement-permit"
				p.Data["permitUID"] = string(p.UID)
				mustBootstrap(t, f.c.Create(context.Background(), &p))
			case "generation":
				p.Data["sessionGeneration"] = "99"
			case "expired":
				p.Data["expiresAt"] = f.now.Format(time.RFC3339)
			case "revoked":
				p.Data["revokedAt"] = "now"
			case "blank-revocation":
				p.Data["revokedAt"] = " "
			case "sessionUID", "podUID", "requestID", "payloadDigest", "state":
				p.Data[name] = "wrong"
			case "owner-name":
				pod.OwnerReferences[0].Name = "wrong"
			case "owner-version":
				pod.OwnerReferences[0].APIVersion = "wrong/v1"
			case "owner-kind":
				pod.OwnerReferences[0].Kind = "Pod"
			case "owner-uid":
				pod.OwnerReferences[0].UID = "wrong"
			case "sa":
				pod.Spec.ServiceAccountName = "wrong"
			case "pod-uid":
				pod.UID = "replacement-pod"
			case "pod-deleting":
				pod.Finalizers = []string{"hold"}
				mustBootstrap(t, f.c.Update(context.Background(), &pod))
				mustBootstrap(t, f.c.Delete(context.Background(), &pod))
			case "pod-stopped":
				pod.Status.Phase = corev1.PodSucceeded
				mustBootstrap(t, f.c.Status().Update(context.Background(), &pod))
			case "session-uid":
				sess.UID = "replacement-session"
			case "session-deleting":
				mustBootstrap(t, f.c.Delete(context.Background(), &sess))
			case "mode":
				sess.Spec.Mode = v1alpha1.ModeTask
			case "operating-mode":
				sess.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
			case "phase":
				sess.Status.Phase = v1alpha1.PhaseFailed
				mustBootstrap(t, f.c.Status().Update(context.Background(), &sess))
			}
			if strings.HasPrefix(name, "owner-") || name == "sa" || name == "pod-uid" {
				mustBootstrap(t, f.c.Update(context.Background(), &pod))
			}
			if name == "mode" || name == "operating-mode" || name == "session-uid" {
				mustBootstrap(t, f.c.Update(context.Background(), &sess))
			}
			if name == "generation" || name == "expired" || name == "revoked" || name == "blank-revocation" || name == "sessionUID" || name == "podUID" || name == "requestID" || name == "payloadDigest" || name == "state" {
				mustBootstrap(t, f.c.Update(context.Background(), &p))
				f.srv.PortalAdmissionBootstrap.ExpectedPermitResourceVersion = p.ResourceVersion
			}
			w := bootstrapPost(f, token)
			if w.Code < 400 || count.Load() != 0 {
				t.Fatalf("%d creates=%d %s", w.Code, count.Load(), w.Body)
			}
		})
	}
}

func TestPortalBootstrapTimeoutsAndReceiptFailure(t *testing.T) {
	for _, fault := range []string{"before-create", "after-create", "receipt", "consume-before", "consume-after"} {
		t.Run(fault, func(t *testing.T) {
			hooks := interceptor.Funcs{}
			hooks.Create = func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				cm := obj.(*corev1.ConfigMap)
				if fault == "before-create" {
					return errors.New("private transport fault")
				}
				cm.UID = "created-uid"
				if err := c.Create(ctx, obj, opts...); err != nil {
					return err
				}
				if fault == "after-create" {
					return errors.New("private lost response")
				}
				return c.Get(ctx, bootstrapKey, obj)
			}
			hooks.Update = func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if p, ok := obj.(*corev1.ConfigMap); ok && p.Name == bootstrapPermitKey.Name {
					if (fault == "receipt" && p.Data["targetUID"] != "") || fault == "consume-before" {
						return errors.New("private update fault")
					}
					if fault == "consume-after" {
						if err := c.Update(ctx, obj, opts...); err != nil {
							return err
						}
						return errors.New("private update response lost")
					}
				}
				return c.Update(ctx, obj, opts...)
			}
			f, token, count := bootstrapFixture(t, hooks)
			if w := bootstrapPost(f, token); w.Code != 409 || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			restartBootstrap(f)
			if w := bootstrapPost(f, token); w.Code != 409 {
				t.Fatalf("repeat %d", w.Code)
			}
			want := int32(1)
			if strings.HasPrefix(fault, "consume") {
				want = 0
			}
			if count.Load() != want {
				t.Fatalf("creates %d", count.Load())
			}
			if fault != "consume-before" {
				assertBootstrapConsumed(t, f, "", "CreateUncertain")
			}
			var cm corev1.ConfigMap
			err := f.c.Get(context.Background(), bootstrapKey, &cm)
			if fault == "after-create" || fault == "receipt" {
				mustBootstrap(t, err)
				if cm.UID != "created-uid" {
					t.Fatal("lost original UID")
				}
			} else if !apierrors.IsNotFound(err) {
				t.Fatalf("target exists: %v", err)
			}
		})
	}
}

func TestPortalBootstrapConcurrentReplicasCAS(t *testing.T) {
	arrived := new(atomic.Int32)
	ready := make(chan struct{})
	f, token, count := bootstrapFixture(t, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		if p, ok := obj.(*corev1.ConfigMap); ok && p.Name == bootstrapPermitKey.Name && p.Data["outcome"] == "CreateUncertain" {
			if arrived.Add(1) == 2 {
				close(ready)
			}
			<-ready
		}
		return c.Update(ctx, obj, opts...)
	}})
	replica := bootstrapServerInstance(f.srv)
	handlers := []http.Handler{f.h, replica.Handler()}
	done := make(chan int, 2)
	for _, h := range handlers {
		go func(h http.Handler) {
			r := httptest.NewRequest(http.MethodPost, apiv1.PortalAdmissionBootstrapPath, strings.NewReader(bootstrapBody))
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			done <- w.Code
		}(h)
	}
	a, b := <-done, <-done
	if (a != 201 && b != 201) || (a != 201 && a != 200 && a != 409) || (b != 201 && b != 200 && b != 409) || count.Load() != 1 {
		t.Fatalf("statuses=%d,%d creates=%d", a, b, count.Load())
	}
	assertBootstrapConsumed(t, f, "created-uid", "Created")
}

func TestPortalBootstrapConcurrentProducer(t *testing.T) {
	for _, where := range []string{"after-consume", "already-exists"} {
		t.Run(where, func(t *testing.T) {
			hooks := interceptor.Funcs{}
			if where == "after-consume" {
				hooks.Update = func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					err := c.Update(ctx, obj, opts...)
					if p, ok := obj.(*corev1.ConfigMap); err == nil && ok && p.Data["outcome"] == "CreateUncertain" {
						cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: bootstrapKey.Namespace, Name: bootstrapKey.Name, UID: "producer-uid"}, Data: map[string]string{"schema": "1", "state": "closed"}}
						return c.Create(ctx, cm)
					}
					return err
				}
			} else {
				hooks.Create = func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					obj.SetUID("producer-uid")
					if err := c.Create(ctx, obj, opts...); err != nil {
						return err
					}
					return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "configmaps"}, obj.GetName())
				}
			}
			f, token, count := bootstrapFixture(t, hooks)
			w := bootstrapPost(f, token)
			if w.Code != 200 {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			assertBootstrapConsumed(t, f, "producer-uid", "Present")
			restartBootstrap(f)
			if bootstrapPost(f, token).Code != 200 {
				t.Fatal("reconcile")
			}
			want := int32(0)
			if where == "already-exists" {
				want = 1
			}
			if count.Load() != want {
				t.Fatalf("creates %d", count.Load())
			}
		})
	}
}

func TestPortalBootstrapFreshUncachedBoundary(t *testing.T) {
	for _, change := range []string{"session-mode", "session-generation", "pod-owner", "pod-uid", "pod-deleted", "expired-at-consume"} {
		t.Run(change, func(t *testing.T) {
			f, token, count := bootstrapFixture(t, interceptor.Funcs{})
			// Classification reads Client. Only Live's second Session/Pod reads change,
			// modeling an independently stale manager cache at the consuming boundary.
			sessionReads, podReads := 0, 0
			f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				switch v := obj.(type) {
				case *v1alpha1.AgentSession:
					sessionReads++
					if sessionReads >= 2 {
						switch change {
						case "session-mode":
							v.Spec.Mode = v1alpha1.ModeTask
						case "session-generation":
							v.Generation++
						}
					}
				case *corev1.Pod:
					podReads++
					if podReads >= 2 {
						switch change {
						case "pod-owner":
							v.OwnerReferences[0].Name = "other"
						case "pod-uid":
							v.UID = "other"
						case "pod-deleted":
							return apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, key.Name)
						case "expired-at-consume":
							f.now = f.now.Add(2 * time.Hour)
						}
					}
				}
				return nil
			}})
			w := bootstrapPost(f, token)
			if w.Code != 403 || count.Load() != 0 {
				t.Fatalf("status=%d creates=%d %s", w.Code, count.Load(), w.Body)
			}
			if sessionReads < 2 {
				t.Fatal("no fresh consuming read")
			}
		})
	}
}

func TestPortalBootstrapPermitCannotBeRearmed(t *testing.T) {
	f, token, count := bootstrapFixture(t, interceptor.Funcs{})
	if w := bootstrapPost(f, token); w.Code != 201 {
		t.Fatalf("initial %d %s", w.Code, w.Body)
	}
	var p corev1.ConfigMap
	mustBootstrap(t, f.c.Get(context.Background(), bootstrapPermitKey, &p))
	var target corev1.ConfigMap
	mustBootstrap(t, f.c.Get(context.Background(), bootstrapKey, &target))
	mustBootstrap(t, f.c.Delete(context.Background(), &target))
	// Same UID, forged active data: the trusted initial RV still fences it.
	p.Data["state"] = "active"
	delete(p.Data, "targetUID")
	mustBootstrap(t, f.c.Update(context.Background(), &p))
	restartBootstrap(f)
	if w := bootstrapPost(f, token); w.Code != 403 {
		t.Fatalf("reset %d", w.Code)
	}
	// New incarnation, explicitly update self-asserted permitUID to the new UID.
	mustBootstrap(t, f.c.Delete(context.Background(), &p))
	p.UID = "new-permit-uid"
	p.ResourceVersion = ""
	p.Data["permitUID"] = string(p.UID)
	mustBootstrap(t, f.c.Create(context.Background(), &p))
	restartBootstrap(f)
	if w := bootstrapPost(f, token); w.Code != 403 || count.Load() != 1 {
		t.Fatalf("recreated status=%d creates=%d", w.Code, count.Load())
	}
}

func TestPortalBootstrapUnexpectedTargetAndReadFailure(t *testing.T) {
	for _, fault := range []string{"open", "extra", "binary", "metadata", "read-before", "read-after", "present-receipt"} {
		t.Run(fault, func(t *testing.T) {
			reads := 0
			hooks := interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if key == bootstrapKey {
					reads++
					if fault == "read-before" || fault == "read-after" && reads == 2 {
						return errors.New("private raw read failure")
					}
				}
				return c.Get(ctx, key, obj, opts...)
			}}
			if fault == "present-receipt" {
				hooks.Update = func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if p, ok := obj.(*corev1.ConfigMap); ok && p.Data["outcome"] == "CreateUncertain" {
						if err := c.Update(ctx, obj, opts...); err != nil {
							return err
						}
						return c.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: bootstrapKey.Namespace, Name: bootstrapKey.Name, UID: "producer-uid"}, Data: map[string]string{"schema": "1", "state": "closed"}})
					}
					if p, ok := obj.(*corev1.ConfigMap); ok && p.Data["outcome"] == "Present" {
						return errors.New("private receipt failure")
					}
					return c.Update(ctx, obj, opts...)
				}
			}
			f, token, count := bootstrapFixture(t, hooks)
			if fault == "open" || fault == "extra" || fault == "binary" || fault == "metadata" {
				cm := bootstrapTarget(t, f, "original-uid")
				switch fault {
				case "open":
					cm.Data["state"] = "open"
				case "extra":
					cm.Data["extra"] = "bad"
				case "binary":
					cm.BinaryData = map[string][]byte{"x": {1}}
				case "metadata":
					cm.Labels = map[string]string{"x": "bad"}
				}
				mustBootstrap(t, f.c.Update(context.Background(), cm))
			}
			w := bootstrapPost(f, token)
			if w.Code < 400 || count.Load() != 0 || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("%d creates=%d %s", w.Code, count.Load(), w.Body)
			}
			if fault == "present-receipt" || fault == "read-after" {
				assertBootstrapConsumed(t, f, "", "CreateUncertain")
				restartBootstrap(f)
				if w = bootstrapPost(f, token); w.Code != 409 || count.Load() != 0 {
					t.Fatalf("repeat %d creates=%d", w.Code, count.Load())
				}
			}
		})
	}
}

func TestPortalBootstrapTokenReviewAudience(t *testing.T) {
	for _, audience := range []string{apiv1.TokenAudience, "https://kubernetes.default.svc"} {
		t.Run(audience, func(t *testing.T) {
			f, token, count := bootstrapFixture(t, interceptor.Funcs{})
			id := f.auth[token]
			var asked authenticationv1.TokenReviewSpec
			f.srv.Auth = reviewer(t, authenticationv1.TokenReviewStatus{Authenticated: true, Audiences: []string{audience}, User: authenticationv1.UserInfo{Username: id.Username, Extra: map[string]authenticationv1.ExtraValue{extraPodName: {id.PodName}, extraPodUID: {id.PodUID}}}}, nil, &asked)
			w := bootstrapPost(f, token)
			wantStatus, wantCount := 201, int32(1)
			if audience != apiv1.TokenAudience {
				wantStatus, wantCount = 401, 0
			}
			if w.Code != wantStatus || count.Load() != wantCount || len(asked.Audiences) != 1 || asked.Audiences[0] != apiv1.TokenAudience {
				t.Fatalf("status=%d creates=%d review=%#v", w.Code, count.Load(), asked)
			}
		})
	}
}

func TestPortalBootstrapPortalClassDoesNotInheritAction(t *testing.T) {
	f, _, count := bootstrapFixture(t, interceptor.Funcs{})
	f.srv.Policy.PortalReaderEnabled = true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "portal-pod", UID: "portal-uid"}, Spec: corev1.PodSpec{ServiceAccountName: "dev-env-v2-portal", Containers: []corev1.Container{{Name: "portal", Image: "x"}}}}
	mustBootstrap(t, f.c.Create(context.Background(), pod))
	id := saIdentity(portalReaderRef)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	f.auth["portal-token"] = id
	if w := bootstrapPost(f, "portal-token"); w.Code != 403 || count.Load() != 0 {
		t.Fatalf("status=%d creates=%d", w.Code, count.Load())
	}
}

func TestPortalBootstrapRevocationOrTerminationAfterConsume(t *testing.T) {
	for _, fault := range []string{"revoked", "pod-stopped", "expired"} {
		t.Run(fault, func(t *testing.T) {
			var f *fixture
			hooks := interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if err := c.Update(ctx, obj, opts...); err != nil {
					return err
				}
				if p, ok := obj.(*corev1.ConfigMap); ok && p.Name == bootstrapPermitKey.Name && p.Data["outcome"] == "CreateUncertain" {
					switch fault {
					case "revoked":
						latest := p.DeepCopy()
						latest.Data["revokedAt"] = "revoked"
						return c.Update(ctx, latest)
					case "pod-stopped":
						var pod corev1.Pod
						if err := c.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: "bootstrap-caller"}, &pod); err != nil {
							return err
						}
						pod.Status.Phase = corev1.PodSucceeded
						return c.Status().Update(ctx, &pod)
					case "expired":
						f.now = f.now.Add(2 * time.Hour)
					}
				}
				return nil
			}}
			var token string
			var count *atomic.Int32
			f, token, count = bootstrapFixture(t, hooks)
			if w := bootstrapPost(f, token); w.Code != 409 || count.Load() != 0 {
				t.Fatalf("status=%d creates=%d", w.Code, count.Load())
			}
			assertBootstrapConsumed(t, f, "", "CreateUncertain")
		})
	}
}

func TestPortalBootstrapLostDurableReceiptResponse(t *testing.T) {
	f, token, count := bootstrapFixture(t, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		if err := c.Update(ctx, obj, opts...); err != nil {
			return err
		}
		if p, ok := obj.(*corev1.ConfigMap); ok && p.Data["outcome"] == "Created" {
			return errors.New("private lost receipt response")
		}
		return nil
	}})
	if w := bootstrapPost(f, token); w.Code != 409 {
		t.Fatalf("first %d", w.Code)
	}
	assertBootstrapConsumed(t, f, "created-uid", "Created")
	restartBootstrap(f)
	w := bootstrapPost(f, token)
	if w.Code != 200 || count.Load() != 1 || decode[apiv1.PortalAdmissionBootstrapResult](t, w).TargetUID != "created-uid" {
		t.Fatalf("reconcile %d creates=%d", w.Code, count.Load())
	}
}

func TestPortalBootstrapFullBodyBoundary(t *testing.T) {
	f, token, count := bootstrapFixture(t, interceptor.Funcs{})
	body := bootstrapBody + strings.Repeat(" ", 1024-len(bootstrapBody))
	if w := f.do(http.MethodPost, apiv1.PortalAdmissionBootstrapPath, token, body); w.Code != 201 || count.Load() != 1 {
		t.Fatalf("boundary %d creates=%d", w.Code, count.Load())
	}
}

func TestPortalBootstrapRejectsQuerySelectorsAndNilConfiguration(t *testing.T) {
	f, token, count := bootstrapFixture(t, interceptor.Funcs{})
	if w := f.do(http.MethodPost, apiv1.PortalAdmissionBootstrapPath+"?caller=other", token, bootstrapBody); w.Code != 400 || count.Load() != 0 {
		t.Fatalf("query status=%d creates=%d", w.Code, count.Load())
	}
	f.srv.PortalAdmissionBootstrap = nil
	if w := bootstrapPost(f, token); w.Code != 404 || count.Load() != 0 {
		t.Fatalf("default status=%d creates=%d", w.Code, count.Load())
	}
}
