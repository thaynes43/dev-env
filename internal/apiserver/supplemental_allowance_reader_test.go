package apiserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/internal/templates"
)

const (
	supplementalName       = "dev-env-1104-010332"
	supplementalSessionUID = types.UID("9323443a-4a8f-404f-8b41-b583e3bcc1a9")
	supplementalSA         = "dev-env-workbench"
	supplementalSAUID      = "55cc1f98-5b17-46e7-8770-c8bf9de85437"
	supplementalPodUID     = types.UID("pod-live-1")
)

func TestSupplementalReaderAdmitsExactLiveIdentity(t *testing.T) {
	live := newKeyedLive()
	pod, sess := approvedLive()
	live.pods[liveKey(pod)] = pod
	live.sessions[liveKey(sess)] = sess
	store := &readCounter{}
	h := supplementalHandler(live, store)
	// Cached objects are absent. Admission has to use the uncached reader.
	c := supplementalCaller(pod)
	c.session, c.pod = nil, nil
	status, _, err := h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), c)
	if err != nil || status != http.StatusOK || store.reads != 1 || store.puts != 0 || live.serviceAccounts != 0 {
		t.Fatalf("status %d reads %d puts %d sa %d err %v gets %v", status, store.reads, store.puts, live.serviceAccounts, err, live.gets)
	}
	if len(live.gets) != 2 || !strings.HasPrefix(live.gets[0], "pod/") || !strings.HasPrefix(live.gets[1], "session/") {
		t.Fatalf("live gets %v", live.gets)
	}

	stalePod, staleSess := pod.DeepCopy(), sess.DeepCopy()
	stamp := metav1.NewTime(time.Unix(1, 0))
	staleSess.UID = "stale-session"
	staleSess.DeletionTimestamp = &stamp
	staleSess.Status.Phase = v1alpha1.PhaseIdle
	stalePod.UID = "stale-pod"
	stalePod.DeletionTimestamp = &stamp
	stalePod.Status.Phase = corev1.PodPending
	c.session, c.pod = staleSess, stalePod
	status, _, err = h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), c)
	if err != nil || status != http.StatusOK || store.reads != 2 || live.serviceAccounts != 0 {
		t.Fatalf("stale cache was authoritative status %d reads %d err %v", status, store.reads, err)
	}
	sess.Status.Phase = v1alpha1.PhaseIdle
	status, _, err = h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), c)
	if err != nil || status != http.StatusOK || store.reads != 3 || live.serviceAccounts != 0 {
		t.Fatalf("idle phase revoked status %d reads %d err %v", status, store.reads, err)
	}
}

func TestSupplementalReaderRefusesIdentityAndLiveMismatches(t *testing.T) {
	pod, sess := approvedLive()
	base := supplementalCaller(pod)
	cases := []struct {
		name string
		edit func(c *caller, pod *corev1.Pod, sess *v1alpha1.AgentSession)
	}{
		{"session uid", func(_ *caller, _ *corev1.Pod, sess *v1alpha1.AgentSession) { sess.UID = "other-session" }},
		{"session name", func(_ *caller, _ *corev1.Pod, sess *v1alpha1.AgentSession) { sess.Name = "other-name" }},
		{"namespace", func(c *caller, _ *corev1.Pod, _ *v1alpha1.AgentSession) { c.identity.Namespace = "other" }},
		{"service account name", func(c *caller, _ *corev1.Pod, _ *v1alpha1.AgentSession) {
			c.identity.ServiceAccount = "dev-env-other"
		}},
		{"recreated service account uid", func(c *caller, _ *corev1.Pod, _ *v1alpha1.AgentSession) {
			c.identity.ServiceAccountUID = "recreated-sa-uid"
		}},
		{"deleted pod", func(_ *caller, pod *corev1.Pod, _ *v1alpha1.AgentSession) {
			stamp := metav1.NewTime(time.Unix(2, 0))
			pod.DeletionTimestamp = &stamp
		}},
		{"deleted session", func(_ *caller, _ *corev1.Pod, sess *v1alpha1.AgentSession) {
			stamp := metav1.NewTime(time.Unix(2, 0))
			sess.DeletionTimestamp = &stamp
		}},
		{"nonowned pod", func(_ *caller, pod *corev1.Pod, _ *v1alpha1.AgentSession) { pod.OwnerReferences = nil }},
		{"owner not controller", func(_ *caller, pod *corev1.Pod, _ *v1alpha1.AgentSession) {
			pod.OwnerReferences[0].Controller = nil
		}},
		{"owner kind", func(_ *caller, pod *corev1.Pod, _ *v1alpha1.AgentSession) { pod.OwnerReferences[0].Kind = "Pod" }},
		{"owner api", func(_ *caller, pod *corev1.Pod, _ *v1alpha1.AgentSession) {
			pod.OwnerReferences[0].APIVersion = "apps/v1"
		}},
		{"owner name", func(_ *caller, pod *corev1.Pod, _ *v1alpha1.AgentSession) { pod.OwnerReferences[0].Name = "other-name" }},
		{"owner uid", func(_ *caller, pod *corev1.Pod, _ *v1alpha1.AgentSession) {
			pod.OwnerReferences[0].UID = "other-session"
		}},
		{"pod uid", func(c *caller, _ *corev1.Pod, _ *v1alpha1.AgentSession) { c.identity.PodUID = "other-pod" }},
		{"pod not running", func(_ *caller, pod *corev1.Pod, _ *v1alpha1.AgentSession) { pod.Status.Phase = corev1.PodPending }},
		{"session not running", func(_ *caller, _ *corev1.Pod, sess *v1alpha1.AgentSession) { sess.Status.Phase = v1alpha1.PhaseFailed }},
		{"operating mode", func(_ *caller, _ *corev1.Pod, sess *v1alpha1.AgentSession) {
			sess.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		}},
		{"pod name status", func(_ *caller, _ *corev1.Pod, sess *v1alpha1.AgentSession) { sess.Status.PodName = "other-pod" }},
		{"pod service account", func(_ *caller, pod *corev1.Pod, _ *v1alpha1.AgentSession) {
			pod.Spec.ServiceAccountName = controller.ServiceAccountName
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			live := newKeyedLive()
			podCopy, sessCopy := pod.DeepCopy(), sess.DeepCopy()
			callerCopy := *base
			callerCopy.identity = base.identity
			callerCopy.session, callerCopy.pod = nil, nil
			tc.edit(&callerCopy, podCopy, sessCopy)
			live.pods[liveKey(pod)] = podCopy
			live.sessions[liveKey(sess)] = sessCopy
			store := &readCounter{}
			h := supplementalHandler(live, store)
			_, _, err := h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), &callerCopy)
			wantAllowanceDenied(t, store, err)
			if live.serviceAccounts != 0 {
				t.Fatal("ServiceAccount was read")
			}
		})
	}
}

func TestSupplementalReaderRetainedUIDIsNotAServiceAccountRead(t *testing.T) {
	// The old authenticated UID is admitted. A new authenticated UID is refused.
	// Neither request reads a ServiceAccount. Upstream may briefly continue to
	// present the previous UID after deletion; that retention is not revoked here.
	live := newKeyedLive()
	pod, sess := approvedLive()
	live.pods[liveKey(pod)] = pod
	live.sessions[liveKey(sess)] = sess
	store := &readCounter{}
	h := supplementalHandler(live, store)
	oldID := supplementalCaller(pod)
	status, _, err := h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), oldID)
	if err != nil || status != http.StatusOK || store.reads != 1 || live.serviceAccounts != 0 {
		t.Fatalf("retained uid status %d reads %d sa %d err %v", status, store.reads, live.serviceAccounts, err)
	}
	recreated := supplementalCaller(pod)
	recreated.identity.ServiceAccountUID = "recreated-sa-uid"
	_, _, err = h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), recreated)
	wantAllowanceDenied(t, &readCounter{reads: store.reads - 1}, err)
	if store.reads != 1 || live.serviceAccounts != 0 {
		t.Fatalf("recreated uid read the slot or a ServiceAccount reads %d sa %d", store.reads, live.serviceAccounts)
	}
}

func TestSupplementalReaderRefusesUncachedLiveAndUnboundToken(t *testing.T) {
	pod, sess := approvedLive()
	withCache := func() *caller {
		c := supplementalCaller(pod)
		c.session = sess.DeepCopy()
		c.pod = pod.DeepCopy()
		return c
	}
	handle := func(t *testing.T, live *keyedLive, c *caller) (*readCounter, error) {
		t.Helper()
		store := &readCounter{}
		_, _, err := supplementalHandler(live, store).Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), c)
		return store, err
	}
	t.Run("not found", func(t *testing.T) {
		live := newKeyedLive()
		store, err := handle(t, live, withCache())
		wantAllowanceDenied(t, store, err)
		if live.serviceAccounts != 0 {
			t.Fatal("ServiceAccount was read")
		}
	})
	t.Run("live error", func(t *testing.T) {
		live := newKeyedLive()
		live.err = errors.New("live read")
		live.pods[liveKey(pod)] = pod.DeepCopy()
		live.sessions[liveKey(sess)] = sess.DeepCopy()
		store, err := handle(t, live, withCache())
		wantAllowanceDenied(t, store, err)
		if live.serviceAccounts != 0 {
			t.Fatal("ServiceAccount was read")
		}
	})
	t.Run("replacement", func(t *testing.T) {
		live := newKeyedLive()
		replacedPod, replacedSess := pod.DeepCopy(), sess.DeepCopy()
		replacedSess.UID = "replacement-session"
		replacedPod.UID = "replacement-pod"
		replacedPod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(replacedSess, v1alpha1.GroupVersion.WithKind("AgentSession"))}
		live.pods[liveKey(pod)] = replacedPod
		live.sessions[liveKey(sess)] = replacedSess
		store, err := handle(t, live, withCache())
		wantAllowanceDenied(t, store, err)
		if live.serviceAccounts != 0 {
			t.Fatal("ServiceAccount was read")
		}
	})
	t.Run("missing pod binding", func(t *testing.T) {
		live := newKeyedLive()
		live.pods[liveKey(pod)] = pod.DeepCopy()
		live.sessions[liveKey(sess)] = sess.DeepCopy()
		c := withCache()
		c.identity.PodName, c.identity.PodUID = "", ""
		store, err := handle(t, live, c)
		wantAllowanceDenied(t, store, err)
		if len(live.gets) != 0 || live.serviceAccounts != 0 {
			t.Fatalf("unbound token got %v sa %d", live.gets, live.serviceAccounts)
		}
	})
	t.Run("empty service account uid", func(t *testing.T) {
		live := newKeyedLive()
		live.pods[liveKey(pod)] = pod.DeepCopy()
		live.sessions[liveKey(sess)] = sess.DeepCopy()
		c := withCache()
		c.identity.ServiceAccountUID = ""
		store, err := handle(t, live, c)
		wantAllowanceDenied(t, store, err)
		if len(live.gets) != 0 || live.serviceAccounts != 0 {
			t.Fatalf("empty uid got %v sa %d", live.gets, live.serviceAccounts)
		}
	})
}

func TestSupplementalReaderKeepsPrivatePortalPublisherAndSource(t *testing.T) {
	live := newKeyedLive()
	live.err = errors.New("live read")
	store := &readCounter{}
	h := supplementalHandler(live, store)
	priv := &caller{
		kind: kindSession,
		identity: Identity{
			Namespace: sessionNS, ServiceAccount: controller.ServiceAccountName,
			PodName: "reader", PodUID: "reader-pod",
		},
		session: &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: sessionNS, UID: "reader-uid"}},
		pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: sessionNS, UID: "reader-pod"},
			Spec: corev1.PodSpec{ServiceAccountName: controller.ServiceAccountName}},
	}
	status, _, err := h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), priv)
	if err != nil || status != http.StatusOK || store.reads != 1 || len(live.gets) != 0 {
		t.Fatalf("private reader status %d reads %d gets %v err %v", status, store.reads, live.gets, err)
	}
	portalPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal-pod", Namespace: portalReaderNamespace, UID: "portal-pod"},
		Spec: corev1.PodSpec{ServiceAccountName: portalReaderAccount}}
	portal := &caller{kind: kindPortalReader, identity: Identity{Namespace: portalReaderNamespace, ServiceAccount: portalReaderAccount, PodName: portalPod.Name, PodUID: string(portalPod.UID)}, pod: portalPod}
	status, _, err = h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), portal)
	if err != nil || status != http.StatusOK || store.reads != 2 || len(live.gets) != 0 {
		t.Fatalf("portal status %d reads %d err %v", status, store.reads, err)
	}
	pubPod, pubSess, pub := publisherCaller()
	live.err = nil
	live.pods[liveKey(pubPod)] = pubPod
	live.sessions[liveKey(pubSess)] = pubSess
	status, _, err = h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, ProviderAllowanceReportsPath, nil), pub)
	if err != nil || status != http.StatusNoContent || store.puts != 1 || store.reads != 2 {
		t.Fatalf("publisher put status %d puts %d reads %d err %v", status, store.puts, store.reads, err)
	}
	approvedPod, _ := approvedLive()
	status, _, err = h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, ProviderAllowanceReportsPath, nil), supplementalCaller(approvedPod))
	if err == nil || status == http.StatusNoContent || store.puts != 1 {
		t.Fatalf("supplemental put status %d puts %d err %v", status, store.puts, err)
	}
	unbound := supplementalCaller(approvedPod)
	unbound.kind = kindClient
	_, _, err = h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), unbound)
	if store.reads != 2 {
		t.Fatal("unbound workbench read the slot", store.reads)
	}
	wantAllowanceDenied(t, &readCounter{}, err)

	calls := 0
	source := supplementalHandler(nil, nil)
	source.Store, source.Live = nil, nil
	source.PublisherSessionUID, source.PublisherServiceAccount = "", ""
	source.Source = func(context.Context, time.Time) (providerallowance.ReadResponse, error) {
		calls++
		return allowanceSnapshot("a"), nil
	}
	if !source.RouteEnabled() {
		t.Fatal("source mode route changed")
	}
	_, _, err = source.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), supplementalCaller(approvedPod))
	if err == nil || calls != 0 {
		t.Fatal("source mode admitted the supplemental reader", calls, err)
	}
	status, _, err = source.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), priv)
	if err != nil || status != http.StatusOK || calls != 1 {
		t.Fatalf("private source read status %d calls %d err %v", status, calls, err)
	}
}

func TestSupplementalReaderOperatorProducerKeepsGETAndRefusesPUT(t *testing.T) {
	live := newKeyedLive()
	pod, sess := approvedLive()
	live.pods[liveKey(pod)] = pod
	live.sessions[liveKey(sess)] = sess
	store := &readCounter{}
	h := supplementalHandler(live, store)
	h.OperatorProducer = true
	if !h.RouteEnabled() || h.PublishEnabled() {
		t.Fatal("valid operator producer lost GET or enabled external PUT")
	}
	c := supplementalCaller(pod)
	status, _, err := h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), c)
	if err != nil || status != http.StatusOK || store.reads != 1 || store.puts != 0 || live.serviceAccounts != 0 {
		t.Fatalf("operator GET status %d reads %d puts %d sa %d err %v", status, store.reads, store.puts, live.serviceAccounts, err)
	}
	puts := store.puts
	_, _, err = h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, ProviderAllowanceReportsPath, nil), c)
	var api *apiError
	if !errors.As(err, &api) || api.status != http.StatusMethodNotAllowed || store.puts != puts || store.reads != 1 {
		t.Fatalf("operator PUT reads %d puts %d err %v", store.reads, store.puts, err)
	}
}

func TestSupplementalReaderHTTPChecksLiveNotCache(t *testing.T) {
	f := newFixture(t)
	f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
	f.srv.Policy.Clients = append([]string{sessionNS + "/" + templates.WorkbenchServiceAccount}, f.srv.Policy.Clients...)
	cache := fake.NewClientBuilder().WithScheme(testScheme).WithStatusSubresource(&v1alpha1.AgentSession{}).Build()
	f.c = cache
	liveBase := fake.NewClientBuilder().WithScheme(testScheme).WithStatusSubresource(&v1alpha1.AgentSession{}).Build()
	if err := putRunningSession(cache, v1alpha1.PhaseIdle, v1alpha1.OperatingModeSuspended, corev1.PodRunning, controller.ServiceAccountName); err != nil {
		t.Fatal(err)
	}
	if err := putRunningSession(liveBase, v1alpha1.PhaseRunning, v1alpha1.OperatingModeRunning, corev1.PodRunning, supplementalSA); err != nil {
		t.Fatal(err)
	}
	sas := 0
	live := interceptor.NewClient(liveBase, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.ServiceAccount); ok {
				sas++
				return errors.New("serviceaccount read")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	store := &readCounter{}
	f.srv.Client = cache
	f.srv.Live = cache
	f.srv.AggregateAllowanceReports = supplementalHandler(live, store)
	auth := &authCounter{fakeAuth: f.auth}
	f.srv.Auth = auth
	id := saIdentity(sessionNS + "/" + supplementalSA)
	id.ServiceAccountUID = supplementalSAUID
	id.PodName = supplementalName
	id.PodUID = string(supplementalPodUID)
	f.auth["tok-supplemental"] = id
	f.h = f.srv.Handler()
	got := f.do(http.MethodGet, ProviderAllowanceReportsPath, "tok-supplemental", nil)
	if got.Code != http.StatusOK || store.reads != 1 || auth.n != 1 || sas != 0 {
		t.Fatalf("live admission %d reads %d auth %d sa %d %s", got.Code, store.reads, auth.n, sas, got.Body.String())
	}
	var current v1alpha1.AgentSession
	if err := liveBase.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: supplementalName}, &current); err != nil {
		t.Fatal(err)
	}
	current.Status.Phase = v1alpha1.PhaseIdle
	if err := liveBase.Status().Update(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
	idle := f.do(http.MethodGet, ProviderAllowanceReportsPath, "tok-supplemental", nil)
	if idle.Code != http.StatusOK || store.reads != 2 || auth.n != 2 || sas != 0 {
		t.Fatalf("idle live session %d reads %d auth %d sa %d %s", idle.Code, store.reads, auth.n, sas, idle.Body.String())
	}
	current.Status.Phase = v1alpha1.PhaseFailed
	if err := liveBase.Status().Update(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
	denied := f.do(http.MethodGet, ProviderAllowanceReportsPath, "tok-supplemental", nil)
	if denied.Code != http.StatusForbidden || store.reads != 2 || auth.n != 3 || sas != 0 || strings.Contains(denied.Body.String(), supplementalSAUID) {
		t.Fatalf("terminal live session %d reads %d auth %d %s", denied.Code, store.reads, auth.n, denied.Body.String())
	}
	id.ServiceAccountUID = "recreated-sa-uid"
	f.auth["tok-recreated"] = id
	current.Status.Phase = v1alpha1.PhaseRunning
	if err := liveBase.Status().Update(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
	recreated := f.do(http.MethodGet, ProviderAllowanceReportsPath, "tok-recreated", nil)
	if recreated.Code != http.StatusForbidden || store.reads != 2 || auth.n != 4 || sas != 0 {
		t.Fatalf("recreated sa %d reads %d auth %d sa %d %s", recreated.Code, store.reads, auth.n, sas, recreated.Body.String())
	}
	privTok := f.sessionPod("reader", "task", 0)
	var priv v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "reader"}, &priv); err != nil {
		t.Fatal(err)
	}
	f.srv.AggregateAllowanceReports.SessionUIDs = []types.UID{priv.UID}
	f.h = f.srv.Handler()
	if got = f.do(http.MethodGet, ProviderAllowanceReportsPath, privTok, nil); got.Code != http.StatusOK || store.reads != 3 {
		t.Fatalf("private reader through the server %d reads %d %s", got.Code, store.reads, got.Body.String())
	}
}

func approvedSupplemental() *SupplementalAllowanceReader {
	return &SupplementalAllowanceReader{
		Namespace: sessionNS, Name: supplementalName, SessionUID: supplementalSessionUID,
		ServiceAccount: supplementalSA, ServiceAccountUID: supplementalSAUID,
	}
}

func supplementalHandler(live objectGetter, store aggregateAllowanceStore) *AggregateAllowanceReports {
	return &AggregateAllowanceReports{
		Enabled: true, Store: store, Live: live,
		SessionNamespace: sessionNS, SessionServiceAccount: controller.ServiceAccountName, SessionUIDs: []types.UID{"reader-uid"},
		PublisherSessionUID: "publisher-uid", PublisherServiceAccount: controller.ServiceAccountName,
		PortalNamespace: portalReaderNamespace, PortalServiceAccount: portalReaderAccount,
		Supplemental: approvedSupplemental(),
	}
}

func approvedLive() (*corev1.Pod, *v1alpha1.AgentSession) {
	sess := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: supplementalName, Namespace: sessionNS, UID: supplementalSessionUID},
		Spec:       v1alpha1.AgentSessionSpec{OperatingMode: v1alpha1.OperatingModeRunning},
	}
	sess.Status.Phase = v1alpha1.PhaseRunning
	sess.Status.PodName = supplementalName
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: supplementalName, Namespace: sessionNS, UID: supplementalPodUID,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sess, v1alpha1.GroupVersion.WithKind("AgentSession"))},
		},
		Spec:   corev1.PodSpec{ServiceAccountName: supplementalSA, Containers: []corev1.Container{{Name: "agent", Image: "x"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	return pod, sess
}

func supplementalCaller(pod *corev1.Pod) *caller {
	return &caller{
		kind: kindSession,
		identity: Identity{
			Username: serviceAccountPrefix + sessionNS + ":" + supplementalSA, Namespace: sessionNS,
			ServiceAccount: supplementalSA, ServiceAccountUID: supplementalSAUID,
			PodName: pod.Name, PodUID: string(supplementalPodUID),
		},
	}
}

func publisherCaller() (*corev1.Pod, *v1alpha1.AgentSession, *caller) {
	const name = "publisher-session"
	sess := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: "publisher-uid"},
		Spec:       v1alpha1.AgentSessionSpec{OperatingMode: v1alpha1.OperatingModeRunning},
	}
	sess.Status.Phase = v1alpha1.PhaseRunning
	sess.Status.PodName = name
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: sessionNS, UID: "publisher-pod",
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sess, v1alpha1.GroupVersion.WithKind("AgentSession"))},
		},
		Spec:   corev1.PodSpec{ServiceAccountName: controller.ServiceAccountName, Containers: []corev1.Container{{Name: "agent", Image: "x"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	c := &caller{
		kind: kindSession,
		identity: Identity{
			Namespace: sessionNS, ServiceAccount: controller.ServiceAccountName, ServiceAccountUID: "publisher-sa",
			PodName: name, PodUID: string(pod.UID),
		},
		session: sess, pod: pod,
	}
	return pod, sess, c
}

func wantAllowanceDenied(t *testing.T, store *readCounter, err error) {
	t.Helper()
	var api *apiError
	if !errors.As(err, &api) || api.status != http.StatusForbidden || store.reads != 0 || strings.Contains(err.Error(), supplementalSAUID) || strings.Contains(err.Error(), string(supplementalSessionUID)) {
		t.Fatalf("reads %d err %v", store.reads, err)
	}
}

func putRunningSession(c client.Client, phase v1alpha1.SessionPhase, mode v1alpha1.OperatingMode, podPhase corev1.PodPhase, account string) error {
	sess := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: supplementalName, Namespace: sessionNS, UID: supplementalSessionUID},
		Spec: v1alpha1.AgentSessionSpec{
			OperatingMode: mode, Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p", Parent: humanSA,
		},
	}
	ctx := context.Background()
	if err := c.Create(ctx, sess); err != nil {
		return err
	}
	sess.Status.Phase = phase
	sess.Status.PodName = supplementalName
	if err := c.Status().Update(ctx, sess); err != nil {
		return err
	}
	var current v1alpha1.AgentSession
	if err := c.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: supplementalName}, &current); err != nil {
		return err
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: supplementalName, Namespace: sessionNS, UID: supplementalPodUID,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&current, v1alpha1.GroupVersion.WithKind("AgentSession"))},
		},
		Spec:   corev1.PodSpec{ServiceAccountName: account, Containers: []corev1.Container{{Name: "agent", Image: "x"}}},
		Status: corev1.PodStatus{Phase: podPhase},
	}
	return c.Create(ctx, pod)
}

type readCounter struct {
	reads int
	puts  int
}

func (r *readCounter) Read(context.Context) (AggregateAllowanceView, error) {
	r.reads++
	return AggregateAllowanceView{}, nil
}

func (r *readCounter) Put(context.Context, []byte, time.Time) error {
	r.puts++
	return nil
}

func (r *readCounter) configured() bool { return true }

func (r *readCounter) trust() AggregateAllowanceTrust { return AggregateAllowanceTrust{} }

type keyedLive struct {
	pods            map[types.NamespacedName]*corev1.Pod
	sessions        map[types.NamespacedName]*v1alpha1.AgentSession
	gets            []string
	serviceAccounts int
	err             error
}

func newKeyedLive() *keyedLive {
	return &keyedLive{
		pods:     map[types.NamespacedName]*corev1.Pod{},
		sessions: map[types.NamespacedName]*v1alpha1.AgentSession{},
	}
}

func liveKey(obj metav1.Object) types.NamespacedName {
	return types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}
}

func (k *keyedLive) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	if k.pods == nil {
		k.pods = map[types.NamespacedName]*corev1.Pod{}
	}
	if k.sessions == nil {
		k.sessions = map[types.NamespacedName]*v1alpha1.AgentSession{}
	}
	switch dest := obj.(type) {
	case *corev1.ServiceAccount:
		k.serviceAccounts++
		return errors.New("serviceaccount read")
	case *corev1.Pod:
		k.gets = append(k.gets, "pod/"+key.String())
		if k.err != nil {
			return k.err
		}
		item := k.pods[key]
		if item == nil {
			return apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, key.Name)
		}
		*dest = *item.DeepCopy()
		return nil
	case *v1alpha1.AgentSession:
		k.gets = append(k.gets, "session/"+key.String())
		if k.err != nil {
			return k.err
		}
		item := k.sessions[key]
		if item == nil {
			return apierrors.NewNotFound(schema.GroupResource{Resource: "agentsessions"}, key.Name)
		}
		*dest = *item.DeepCopy()
		return nil
	default:
		k.gets = append(k.gets, "other")
		return errors.New("unexpected live type")
	}
}

type authCounter struct {
	fakeAuth
	n int
}

func (a *authCounter) Authenticate(ctx context.Context, token string) (Identity, error) {
	a.n++
	return a.fakeAuth.Authenticate(ctx, token)
}
