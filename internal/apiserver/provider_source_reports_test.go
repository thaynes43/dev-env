package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	sourcePublisherNS   = "synthetic-sessions"
	sourcePublisherName = "synthetic-publisher"
	sourcePublisherSA   = "synthetic-workbench"
	sourcePortalNS      = "synthetic-portal"
	sourcePortalSA      = "synthetic-portal-reader"
	sourcePortalPod     = "synthetic-portal-pod"
)

var (
	sourceSessionUID   = types.UID("synthetic-session-uid")
	sourcePodUID       = types.UID("synthetic-pod-uid")
	sourcePortalPodUID = types.UID("synthetic-portal-pod-uid")
)

func TestProviderSourceCallerFence(t *testing.T) {
	raw := syntheticSourceRaw(t, "40", "60", sourceRetrieved, sourceResets, 1)

	t.Run("running and idle publish without a snapshot", func(t *testing.T) {
		for _, phase := range []v1alpha1.SessionPhase{v1alpha1.PhaseRunning, v1alpha1.PhaseIdle} {
			h, live, caller, mem := sourceHandler(t)
			live.sessions[sourcePublisherKey()].Status.Phase = phase
			status, body, err := callProviderSource(t, h, http.MethodPut, ProviderSourceReportPath, "", raw, caller)
			if err != nil || status != http.StatusNoContent || body != nil || mem.puts != 1 || mem.reads != 0 || !mem.lastNow.Equal(sourceNow) {
				t.Fatalf("%s status %d puts %d err %v", phase, status, mem.puts, err)
			}
			sess := live.sessions[sourcePublisherKey()]
			if sess.Spec.Workspace == nil || sess.Annotations[v1alpha1.AnnotationProjectSnapshot] != "" || sess.Status.Agent != nil {
				t.Fatal("fixture called a snapshot or an inference gate")
			}
		}
	})

	t.Run("replacement pod uid is allowed", func(t *testing.T) {
		h, live, caller, mem := sourceHandler(t)
		const replacement = types.UID("synthetic-replacement-pod")
		live.pods[sourcePublisherKey()].UID = replacement
		caller.identity.PodUID = string(replacement)
		status, _, err := callProviderSource(t, h, http.MethodPut, ProviderSourceReportPath, "", raw, caller)
		if err != nil || status != http.StatusNoContent || mem.puts != 1 {
			t.Fatal(err)
		}
	})

	t.Run("publisher get does not write", func(t *testing.T) {
		h, _, caller, mem := sourceHandler(t)
		mem.view = ProviderSourceView{Found: true, Fresh: true, Raw: append([]byte(nil), raw...)}
		status, body, err := callProviderSource(t, h, http.MethodGet, ProviderSourceReportPath, "", nil, caller)
		if err != nil || status != http.StatusOK || !bytes.Equal(body.(json.RawMessage), raw) || mem.puts != 0 || mem.reads != 1 {
			t.Fatalf("status %d body %#v puts %d err %v", status, body, mem.puts, err)
		}
	})

	denies := []struct {
		name   string
		method string
		mutate func(live sourceLive, c *caller)
	}{
		{name: "wrong session uid", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			live.sessions[sourcePublisherKey()].UID = "synthetic-other-session"
			live.pods[sourcePublisherKey()].OwnerReferences[0].UID = "synthetic-other-session"
		}},
		{name: "other workbench session", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			other := sourceSession(v1alpha1.PhaseRunning, v1alpha1.OperatingModeRunning)
			other.Name = "synthetic-other-session"
			other.UID = "synthetic-other-session"
			other.Status.PodName = other.Name
			pod := sourcePod(types.UID("synthetic-other-pod"), corev1.PodRunning)
			pod.Name = other.Name
			pod.OwnerReferences[0].Name = other.Name
			pod.OwnerReferences[0].UID = other.UID
			key := client.ObjectKey{Namespace: sourcePublisherNS, Name: other.Name}
			live.pods[key] = pod
			live.sessions[key] = other
			c.identity.PodName = other.Name
			c.identity.PodUID = string(pod.UID)
		}},
		{name: "session service account", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			c.identity.ServiceAccount = "synthetic-agent"
			c.identity.Username = serviceAccountPrefix + sourcePublisherNS + ":synthetic-agent"
			live.pods[sourcePublisherKey()].Spec.ServiceAccountName = "synthetic-agent"
		}},
		{name: "human service account", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			c.kind = kindHuman
			c.identity.ServiceAccount = "synthetic-human"
		}},
		{name: "unbound workbench client", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			c.kind = kindClient
			c.identity.PodName = ""
			c.identity.PodUID = ""
		}},
		{name: "portal account on put", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			*c = *sourcePortalCaller(string(sourcePortalPodUID))
		}},
		{name: "old pod uid", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			c.identity.PodUID = "synthetic-old-pod"
		}},
		{name: "old pod name", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			old := sourcePod(types.UID("synthetic-old-pod"), corev1.PodRunning)
			old.Name = "synthetic-old-pod"
			live.pods[client.ObjectKey{Namespace: sourcePublisherNS, Name: old.Name}] = old
			c.identity.PodName = old.Name
			c.identity.PodUID = string(old.UID)
		}},
		{name: "deleting session", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			ts := metav1.NewTime(sourceNow)
			live.sessions[sourcePublisherKey()].DeletionTimestamp = &ts
		}},
		{name: "deleting pod", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			ts := metav1.NewTime(sourceNow)
			live.pods[sourcePublisherKey()].DeletionTimestamp = &ts
		}},
		{name: "operating mode suspended", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			live.sessions[sourcePublisherKey()].Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		}},
		{name: "missing operating mode", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			live.sessions[sourcePublisherKey()].Spec.OperatingMode = ""
		}},
		{name: "missing phase", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			live.sessions[sourcePublisherKey()].Status.Phase = ""
		}},
		{name: "missing pod name", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			live.sessions[sourcePublisherKey()].Status.PodName = ""
		}},
		{name: "missing pod", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			delete(live.pods, sourcePublisherKey())
		}},
		{name: "missing session", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			delete(live.sessions, sourcePublisherKey())
		}},
		{name: "pod service account", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			live.pods[sourcePublisherKey()].Spec.ServiceAccountName = "synthetic-agent"
		}},
		{name: "wrong owner api", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			live.pods[sourcePublisherKey()].OwnerReferences[0].APIVersion = "v1"
		}},
		{name: "no controller", method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			live.pods[sourcePublisherKey()].OwnerReferences = nil
		}},
	}
	for _, phase := range []v1alpha1.SessionPhase{v1alpha1.PhasePending, v1alpha1.PhaseDraining, v1alpha1.PhaseSuspended, v1alpha1.PhaseArchived, v1alpha1.PhaseFailed} {
		phase := phase
		denies = append(denies, struct {
			name   string
			method string
			mutate func(live sourceLive, c *caller)
		}{name: "phase " + string(phase), method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			live.sessions[sourcePublisherKey()].Status.Phase = phase
		}})
	}
	for _, phase := range []corev1.PodPhase{corev1.PodPending, corev1.PodSucceeded, corev1.PodFailed, corev1.PodUnknown} {
		phase := phase
		denies = append(denies, struct {
			name   string
			method string
			mutate func(live sourceLive, c *caller)
		}{name: "pod " + string(phase), method: http.MethodPut, mutate: func(live sourceLive, c *caller) {
			live.pods[sourcePublisherKey()].Status.Phase = phase
		}})
	}
	for _, tc := range denies {
		t.Run(tc.name, func(t *testing.T) {
			h, live, caller, mem := sourceHandler(t)
			mem.view = ProviderSourceView{Found: true, Fresh: true, Raw: append([]byte(nil), raw...)}
			tc.mutate(live, caller)
			status, body, err := callProviderSource(t, h, tc.method, ProviderSourceReportPath, "", raw, caller)
			requireRefusal(t, status, body, err, http.StatusForbidden)
			if mem.puts != 0 || mem.reads != 0 {
				t.Fatalf("puts %d reads %d", mem.puts, mem.reads)
			}
		})
	}
}

func TestProviderSourcePortalAndRoute(t *testing.T) {
	raw := syntheticSourceRaw(t, "40", "60", sourceRetrieved, sourceResets, 1)

	t.Run("portal get of the exact path", func(t *testing.T) {
		h, _, _, mem := sourceHandler(t)
		mem.view = ProviderSourceView{Found: true, Fresh: true, Raw: append([]byte(nil), raw...)}
		status, body, err := callProviderSource(t, h, http.MethodGet, ProviderSourceReportPath, "", nil, sourcePortalCaller(string(sourcePortalPodUID)))
		if err != nil || status != http.StatusOK || !bytes.Equal(body.(json.RawMessage), raw) || mem.puts != 0 || mem.reads != 1 {
			t.Fatalf("status %d body %#v err %v", status, body, err)
		}
	})

	t.Run("portal put and other paths", func(t *testing.T) {
		h, _, _, mem := sourceHandler(t)
		mem.view = ProviderSourceView{Found: true, Fresh: true, Raw: append([]byte(nil), raw...)}
		portal := sourcePortalCaller(string(sourcePortalPodUID))
		status, body, err := callProviderSource(t, h, http.MethodPut, ProviderSourceReportPath, "", raw, portal)
		requireRefusal(t, status, body, err, http.StatusForbidden)
		for _, path := range []string{apiv1.SessionsPath, apiv1.InboxPath, apiv1.PublishedContextsPath, "/v1/provider-source-reports", "/v1/provider-source-reports/other-slot"} {
			status, body, err = callProviderSource(t, h, http.MethodGet, path, "", nil, portal)
			requireRefusal(t, status, body, err, http.StatusNotFound)
		}
		if mem.puts != 0 || mem.reads != 0 {
			t.Fatalf("puts %d reads %d", mem.puts, mem.reads)
		}
	})

	t.Run("portal pod is not current", func(t *testing.T) {
		h, live, _, mem := sourceHandler(t)
		key := client.ObjectKey{Namespace: sourcePortalNS, Name: sourcePortalPod}
		live.pods[key].Status.Phase = corev1.PodPending
		_, _, err := callProviderSource(t, h, http.MethodGet, ProviderSourceReportPath, "", nil, sourcePortalCaller(string(sourcePortalPodUID)))
		requireRefusal(t, 0, nil, err, http.StatusForbidden)
		live.pods[key].Status.Phase = corev1.PodRunning
		ts := metav1.NewTime(sourceNow)
		live.pods[key].DeletionTimestamp = &ts
		_, _, err = callProviderSource(t, h, http.MethodGet, ProviderSourceReportPath, "", nil, sourcePortalCaller(string(sourcePortalPodUID)))
		requireRefusal(t, 0, nil, err, http.StatusForbidden)
		live.pods[key].DeletionTimestamp = nil
		_, _, err = callProviderSource(t, h, http.MethodGet, ProviderSourceReportPath, "", nil, sourcePortalCaller("synthetic-old-portal-pod"))
		requireRefusal(t, 0, nil, err, http.StatusForbidden)
		if mem.reads != 0 || mem.puts != 0 {
			t.Fatalf("reads %d puts %d", mem.reads, mem.puts)
		}
	})

	t.Run("suppressed value omits report bytes", func(t *testing.T) {
		h, _, caller, mem := sourceHandler(t)
		mem.view = ProviderSourceView{Found: true, Suppressed: true, Raw: []byte(`{"usedPercent":"41"}`)}
		status, body, err := callProviderSource(t, h, http.MethodGet, ProviderSourceReportPath, "", nil, caller)
		if err != nil || status != http.StatusOK || jsonString(t, body) != `{"availability":"Unknown"}` {
			t.Fatalf("status %d body %s err %v", status, jsonString(t, body), err)
		}
		if strings.Contains(jsonString(t, body), "41") || strings.Contains(jsonString(t, body), "Measured") || strings.Contains(jsonString(t, body), "entitlement") {
			t.Fatal(jsonString(t, body))
		}
	})

	t.Run("disabled suppresses a fresh report", func(t *testing.T) {
		h, _, caller, mem := sourceHandler(t)
		mem.view = ProviderSourceView{Found: true, Fresh: true, Raw: []byte(`{"usedPercent":"41","marker":"synthetic-fresh"}`)}
		h.Enabled = false
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			status, body, err := callProviderSource(t, h, method, ProviderSourceReportPath, "", []byte(`{"usedPercent":"41"}`), caller)
			requireRefusal(t, status, body, err, http.StatusNotFound)
			if strings.Contains(err.Error(), "synthetic-fresh") || strings.Contains(err.Error(), "usedPercent") {
				t.Fatal(err)
			}
		}
		if mem.reads != 0 || mem.puts != 0 {
			t.Fatalf("reads %d puts %d", mem.reads, mem.puts)
		}
	})

	t.Run("oversize query and method do not write", func(t *testing.T) {
		h, _, caller, mem := sourceHandler(t)
		status, body, err := callProviderSource(t, h, http.MethodPut, ProviderSourceReportPath, "", bytes.Repeat([]byte("x"), providerSourceMaxBody+1), caller)
		requireRefusal(t, status, body, err, http.StatusRequestEntityTooLarge)
		status, body, err = callProviderSource(t, h, http.MethodGet, ProviderSourceReportPath, "n=1", nil, caller)
		requireRefusal(t, status, body, err, http.StatusBadRequest)
		status, body, err = callProviderSource(t, h, http.MethodPost, ProviderSourceReportPath, "", nil, caller)
		requireRefusal(t, status, body, err, http.StatusMethodNotAllowed)
		if mem.puts != 0 || mem.reads != 0 {
			t.Fatalf("puts %d reads %d", mem.puts, mem.reads)
		}
	})
}

func TestProviderSourceMaxAgeDoesNotRegisterRoute(t *testing.T) {
	store := &ProviderSourceReportStore{
		Live: sourceLive{}, Writer: nopUpdater{}, Namespace: sourceSlotNS, Name: sourceSlotName,
		DataKey: ProviderSourceDataKey, Selection: sourceSelection(1),
	}
	store.Selection.MaxAge = 0
	h := &ProviderSourceReports{
		Enabled: true, Store: store, Live: sourceLive{},
		SessionNamespace: sourcePublisherNS, PublisherSessionUID: sourceSessionUID, PublisherServiceAccount: sourcePublisherSA,
		PortalNamespace: sourcePortalNS, PortalServiceAccount: sourcePortalSA,
	}
	if !h.RouteEnabled() {
		t.Fatal("a zero max age unregistered the route")
	}
	h.Enabled = false
	store.Selection.MaxAge = 1800 * time.Second
	if h.RouteEnabled() {
		t.Fatal("a fresh max age registered a disabled route")
	}
	h.Enabled = true
	if !h.RouteEnabled() {
		t.Fatal("enabled flag did not register")
	}
	store.Selection.ProducerIncarnation = ""
	if h.RouteEnabled() {
		t.Fatal("missing selection registered the route")
	}
	if PortalProviderSourceGET(false, http.MethodGet, ProviderSourceReportPath) {
		t.Fatal("helper treated max age as enablement")
	}
}

func TestPortalProviderSourceGETDoesNotWiden(t *testing.T) {
	if PortalProviderSourceGET(false, http.MethodGet, ProviderSourceReportPath) {
		t.Fatal("disabled")
	}
	if !PortalProviderSourceGET(true, http.MethodGet, ProviderSourceReportPath) {
		t.Fatal("exact get")
	}
	denied := []struct{ method, path string }{
		{http.MethodPut, ProviderSourceReportPath},
		{http.MethodGet, apiv1.SessionsPath},
		{http.MethodGet, apiv1.InboxPath},
		{http.MethodPost, apiv1.InboxPath},
		{http.MethodGet, apiv1.PublishedContextsPath},
		{http.MethodPost, apiv1.PublishedContextsPath},
		{http.MethodGet, "/v1/provider-source-reports"},
		{http.MethodGet, "/v1/provider-source-reports/other-slot"},
		{http.MethodGet, ProviderSourceReportPath + "/extra"},
	}
	for _, tc := range denied {
		if PortalProviderSourceGET(true, tc.method, tc.path) {
			t.Fatalf("allowed %s %s", tc.method, tc.path)
		}
	}
}

func sourceHandler(t *testing.T) (*ProviderSourceReports, sourceLive, *caller, *memorySource) {
	t.Helper()
	live, caller := sourceBase()
	mem := &memorySource{}
	h := sourceReports(mem, live)
	if !h.RouteEnabled() {
		t.Fatal("fixture route is off")
	}
	return h, live, caller, mem
}

func sourceReports(store providerSourceStore, live objectGetter) *ProviderSourceReports {
	return &ProviderSourceReports{
		Enabled: true, Store: store, Live: live, Now: func() time.Time { return sourceNow },
		SessionNamespace: sourcePublisherNS, PublisherSessionUID: sourceSessionUID, PublisherServiceAccount: sourcePublisherSA,
		PortalNamespace: sourcePortalNS, PortalServiceAccount: sourcePortalSA,
	}
}

func sourceBase() (sourceLive, *caller) {
	live := sourceLive{
		pods: map[client.ObjectKey]*corev1.Pod{
			sourcePublisherKey(): sourcePod(sourcePodUID, corev1.PodRunning),
			{Namespace: sourcePortalNS, Name: sourcePortalPod}: {
				ObjectMeta: metav1.ObjectMeta{Namespace: sourcePortalNS, Name: sourcePortalPod, UID: sourcePortalPodUID},
				Spec:       corev1.PodSpec{ServiceAccountName: sourcePortalSA},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning},
			},
		},
		sessions: map[client.ObjectKey]*v1alpha1.AgentSession{
			sourcePublisherKey(): sourceSession(v1alpha1.PhaseRunning, v1alpha1.OperatingModeRunning),
		},
	}
	return live, sourceCaller(sourcePublisherName, string(sourcePodUID))
}

func sourcePublisherKey() client.ObjectKey {
	return client.ObjectKey{Namespace: sourcePublisherNS, Name: sourcePublisherName}
}

func sourceSession(phase v1alpha1.SessionPhase, mode v1alpha1.OperatingMode) *v1alpha1.AgentSession {
	return &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Namespace: sourcePublisherNS, Name: sourcePublisherName, UID: sourceSessionUID},
		Spec:       v1alpha1.AgentSessionSpec{OperatingMode: mode, Workspace: &v1alpha1.WorkspaceSpec{ID: "synthetic-workspace"}},
		Status:     v1alpha1.AgentSessionStatus{Phase: phase, PodName: sourcePublisherName},
	}
}

func sourcePod(uid types.UID, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: sourcePublisherNS, Name: sourcePublisherName, UID: uid,
			OwnerReferences: []metav1.OwnerReference{sourceController(sourcePublisherName, sourceSessionUID)},
		},
		Spec:   corev1.PodSpec{ServiceAccountName: sourcePublisherSA},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func sourceController(name string, uid types.UID) metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{
		APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentSession", Name: name, UID: uid, Controller: &controller,
	}
}

func sourceCaller(podName, podUID string) *caller {
	return &caller{
		kind: kindSession,
		identity: Identity{
			Username:  serviceAccountPrefix + sourcePublisherNS + ":" + sourcePublisherSA,
			Namespace: sourcePublisherNS, ServiceAccount: sourcePublisherSA, PodName: podName, PodUID: podUID,
		},
		parent: sourcePublisherName,
	}
}

func sourcePortalCaller(podUID string) *caller {
	return &caller{
		kind: kindPortalReader,
		identity: Identity{
			Username:  serviceAccountPrefix + sourcePortalNS + ":" + sourcePortalSA,
			Namespace: sourcePortalNS, ServiceAccount: sourcePortalSA, PodName: sourcePortalPod, PodUID: podUID,
		},
		parent: sourcePortalNS + "/" + sourcePortalSA,
	}
}

type sourceLive struct {
	pods     map[client.ObjectKey]*corev1.Pod
	sessions map[client.ObjectKey]*v1alpha1.AgentSession
}

func (s sourceLive) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	switch dest := obj.(type) {
	case *corev1.Pod:
		pod := s.pods[key]
		if pod == nil {
			return apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, key.Name)
		}
		*dest = *pod.DeepCopy()
		return nil
	case *v1alpha1.AgentSession:
		sess := s.sessions[key]
		if sess == nil {
			return apierrors.NewNotFound(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "agentsessions"}, key.Name)
		}
		*dest = *sess.DeepCopy()
		return nil
	default:
		return apierrors.NewNotFound(schema.GroupResource{}, key.Name)
	}
}

type memorySource struct {
	view    ProviderSourceView
	puts    int
	reads   int
	lastNow time.Time
	last    []byte
}

func (m *memorySource) configured() bool { return m != nil }

func (m *memorySource) Read(context.Context, time.Time) (ProviderSourceView, error) {
	m.reads++
	view := m.view
	view.Raw = append([]byte(nil), m.view.Raw...)
	return view, nil
}

func (m *memorySource) Put(_ context.Context, raw []byte, now time.Time) error {
	m.puts++
	m.lastNow = now
	m.last = append([]byte(nil), raw...)
	return nil
}

type nopUpdater struct{}

func (nopUpdater) Update(context.Context, client.Object, ...client.UpdateOption) error {
	return errors.New("not used")
}

func callProviderSource(t *testing.T, h *ProviderSourceReports, method, path, rawQuery string, body []byte, c *caller) (int, any, error) {
	t.Helper()
	target := path
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	var r *http.Request
	if reader == nil {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, reader)
	}
	return h.Handle(context.Background(), httptest.NewRecorder(), r, c)
}

func requireRefusal(t *testing.T, status int, body any, err error, want int) {
	t.Helper()
	var ae *apiError
	if err == nil || !errors.As(err, &ae) || ae.status != want || body != nil {
		t.Fatalf("status %d body %#v err %v, want %d", status, body, err, want)
	}
	if strings.Contains(ae.Error(), "usedPercent") || strings.Contains(ae.Error(), "providerUpdatedAt") || strings.Contains(ae.Error(), sourceIncarnation) {
		t.Fatalf("refusal contains report: %v", err)
	}
}

func jsonString(t *testing.T, body any) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
