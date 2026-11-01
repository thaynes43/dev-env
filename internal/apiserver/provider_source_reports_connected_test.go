package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// TestProviderSourceServerConnection is the dormant Server.Handler join.
// Tokens go through fakeAuth and resolveCaller. The slot is the real store
// over one retained synthetic ConfigMap. MaxAge does not register the route.
func TestProviderSourceServerConnection(t *testing.T) {
	c := newSourceConnection(t)

	t.Run("nil registers nothing and does not read", func(t *testing.T) {
		c.mount(nil)
		c.deny(t, http.MethodGet, ProviderSourceReportPath, c.publisher, "")
		c.deny(t, http.MethodPut, ProviderSourceReportPath, c.publisher, string(c.raw))
		c.holds(t, 0, 0)
	})

	t.Run("max age does not enable", func(t *testing.T) {
		h := c.reports(false)
		h.Store.(*ProviderSourceReportStore).Selection.MaxAge = 1800 * time.Second
		if h.RouteEnabled() {
			t.Fatal("max age registered a disabled route")
		}
		c.mount(h)
		c.deny(t, http.MethodGet, ProviderSourceReportPath, c.publisher, "")
		c.deny(t, http.MethodPut, ProviderSourceReportPath, c.publisher, string(c.raw))
		c.holds(t, 0, 0)
	})

	t.Run("fresh get returns the retained source bytes", func(t *testing.T) {
		c.mount(c.reports(true))
		if !c.f.srv.ProviderSourceReports.RouteEnabled() {
			t.Fatal("configured route is off")
		}
		w := c.f.do(http.MethodGet, ProviderSourceReportPath, c.publisher, nil)
		doc := sourceDocument(t, w)
		if w.Code != http.StatusOK || !bytes.Equal(doc, c.raw) {
			t.Fatalf("status %d body %s", w.Code, w.Body.String())
		}
		if !sourceReportOnly(t, doc) {
			t.Fatal(string(doc))
		}
		c.holds(t, 1, 0)
	})

	t.Run("session shape stays strict", func(t *testing.T) {
		before := c.gets
		w := c.f.do(http.MethodGet, apiv1.SessionPath(sourcePublisherName), c.publisher, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d %s", w.Code, w.Body.String())
		}
		dec := json.NewDecoder(w.Body)
		dec.DisallowUnknownFields()
		var sess apiv1.Session
		if err := dec.Decode(&sess); err != nil || sess.Name != sourcePublisherName || sess.UID != string(c.sessionUID) {
			t.Fatalf("strict session: %v %s", err, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "usedPercent") || strings.Contains(w.Body.String(), "current_source") || strings.Contains(w.Body.String(), `"availability"`) {
			t.Fatal(w.Body.String())
		}
		var report providerallowance.CurrentSourceReport
		if json.Unmarshal(w.Body.Bytes(), &report) == nil && report.Schema != "" {
			t.Fatal("session decoded as a source report")
		}
		c.holds(t, before, c.updates)
	})

	t.Run("replay put does not rewrite the slot", func(t *testing.T) {
		before := c.gets
		w := c.f.do(http.MethodPut, ProviderSourceReportPath, c.publisher, string(c.raw))
		if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
			t.Fatalf("status %d %s", w.Code, w.Body.String())
		}
		c.holds(t, before+1, 0)
	})

	t.Run("portal get matches and put and foreign paths do not", func(t *testing.T) {
		before := c.gets
		w := c.f.do(http.MethodGet, ProviderSourceReportPath, c.portal, nil)
		doc := sourceDocument(t, w)
		if w.Code != http.StatusOK || !bytes.Equal(doc, c.raw) || !sourceReportOnly(t, doc) {
			t.Fatalf("status %d %s", w.Code, w.Body.String())
		}
		c.holds(t, before+1, 0)
		c.deny(t, http.MethodPut, ProviderSourceReportPath, c.portal, string(c.raw))
		for _, path := range []string{apiv1.InboxPath, apiv1.PublishedContextsPath, "/v1/provider-source-reports", "/v1/provider-source-reports/other-slot"} {
			c.deny(t, http.MethodGet, path, c.portal, "")
		}
		listed := c.gets
		page := c.f.do(http.MethodGet, apiv1.SessionsPath, c.portal, nil)
		if page.Code != http.StatusOK || strings.Contains(page.Body.String(), "usedPercent") || strings.Contains(page.Body.String(), sourceIncarnation) {
			t.Fatalf("portal list %d %s", page.Code, page.Body.String())
		}
		c.holds(t, listed, c.updates)
	})

	t.Run("caller fence", func(t *testing.T) {
		c.deny(t, http.MethodGet, ProviderSourceReportPath, "", "")
		c.deny(t, http.MethodGet, ProviderSourceReportPath, tokStranger, "")
		c.deny(t, http.MethodPut, ProviderSourceReportPath, c.client, string(c.raw))
		c.deny(t, http.MethodPut, ProviderSourceReportPath, c.other, string(c.raw))
		c.deny(t, http.MethodGet, ProviderSourceReportPath, c.oldPod, "")
	})

	t.Run("stale value is unknown and retained", func(t *testing.T) {
		c.when = sourceRetrievedAt().Add(1800*time.Second + time.Second)
		t.Cleanup(func() { c.when = sourceNow })
		before := c.gets
		w := c.f.do(http.MethodGet, ProviderSourceReportPath, c.publisher, nil)
		doc := sourceDocument(t, w)
		if w.Code != http.StatusOK || string(doc) != `{"availability":"Unknown"}` || strings.Contains(string(doc), "usedPercent") {
			t.Fatalf("status %d %s", w.Code, w.Body.String())
		}
		c.when = sourceNow
		c.holds(t, before+1, 0)
	})

	t.Run("zero max age leaves the route mounted and still suppresses", func(t *testing.T) {
		store := c.store()
		store.Selection.MaxAge = 0
		t.Cleanup(func() { store.Selection.MaxAge = 1800 * time.Second })
		if !c.f.srv.ProviderSourceReports.RouteEnabled() {
			t.Fatal("clearing max age unregistered an enabled route")
		}
		before := c.gets
		w := c.f.do(http.MethodGet, ProviderSourceReportPath, c.publisher, nil)
		doc := sourceDocument(t, w)
		if w.Code != http.StatusOK || string(doc) != `{"availability":"Unknown"}` || strings.Contains(string(doc), "usedPercent") {
			t.Fatalf("status %d %s", w.Code, w.Body.String())
		}
		c.holds(t, before+1, 0)
	})

	t.Run("replacement pod uid is current and the previous uid is not", func(t *testing.T) {
		const replacement = types.UID("synthetic-replacement-pod")
		c.replacePod(t, replacement)
		id := c.f.auth[c.publisher]
		id.PodUID = string(replacement)
		c.f.auth[c.publisher] = id
		former := id
		former.PodUID = string(sourcePodUID)
		c.f.auth[c.former] = former
		before := c.gets
		w := c.f.do(http.MethodGet, ProviderSourceReportPath, c.publisher, nil)
		doc := sourceDocument(t, w)
		if w.Code != http.StatusOK || !bytes.Equal(doc, c.raw) {
			t.Fatalf("replacement status %d %s", w.Code, w.Body.String())
		}
		c.holds(t, before+1, 0)
		c.deny(t, http.MethodGet, ProviderSourceReportPath, c.former, "")
		c.deny(t, http.MethodPut, ProviderSourceReportPath, c.other, string(c.raw))
	})
}

type sourceConnection struct {
	f          *fixture
	inner      client.Reader
	when       time.Time
	gets       int
	updates    int
	raw        []byte
	sessionUID types.UID
	publisher  string
	other      string
	portal     string
	client     string
	oldPod     string
	former     string
}

func newSourceConnection(t *testing.T) *sourceConnection {
	t.Helper()
	f := newFixture(t)
	f.now = sourceNow
	c := &sourceConnection{f: f, when: sourceNow, sessionUID: sourceSessionUID, former: "tok-source-former"}
	inner := f.c
	c.inner = inner
	wrapped := interceptor.NewClient(inner, interceptor.Funcs{
		Get: func(ctx context.Context, next client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Namespace == sourceSlotNS && key.Name == sourceSlotName {
				c.gets++
			}
			return next.Get(ctx, key, obj, opts...)
		},
		Update: func(ctx context.Context, next client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Namespace == sourceSlotNS && cm.Name == sourceSlotName {
				c.updates++
			}
			return next.Update(ctx, obj, opts...)
		},
	})
	f.c = wrapped
	f.srv.Client = wrapped
	f.srv.Live = wrapped
	f.srv.Policy.WorkbenchServiceAccount = sourcePublisherSA
	f.srv.Policy.Clients = append(f.srv.Policy.Clients, sessionNS+"/"+sourcePublisherSA)
	f.srv.Policy.PortalReaderEnabled = true
	c.raw = syntheticSourceRaw(t, "40", "60", sourceRetrieved, sourceResets, 1)
	if err := f.c.Create(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace: sourceSlotNS, Name: sourceSlotName,
		Labels:      map[string]string{"synthetic.example/slot": "fixed"},
		Annotations: map[string]string{"synthetic.example/manager": "gitops"},
	}, Data: map[string]string{"keep": "yes", ProviderSourceDataKey: string(c.raw)}}); err != nil {
		t.Fatal(err)
	}
	c.publisher = c.bindSession(t, sourcePublisherName, sourceSessionUID, sourcePodUID)
	c.other = c.bindSession(t, "synthetic-other-session", types.UID("synthetic-other-session"), types.UID("synthetic-other-pod"))
	c.portal = c.bindPortal(t)
	c.client = "tok-source-client"
	f.auth[c.client] = saIdentity(sessionNS + "/" + sourcePublisherSA)
	c.oldPod = "tok-source-old-pod"
	old := f.auth[c.publisher]
	old.PodUID = "synthetic-old-pod"
	f.auth[c.oldPod] = old
	c.mount(nil)
	return c
}

func (c *sourceConnection) store() *ProviderSourceReportStore {
	return c.f.srv.ProviderSourceReports.Store.(*ProviderSourceReportStore)
}

func (c *sourceConnection) reports(enabled bool) *ProviderSourceReports {
	return &ProviderSourceReports{
		Enabled: enabled, Store: &ProviderSourceReportStore{
			Writer: c.f.c, Live: c.f.srv.Live, Namespace: sourceSlotNS, Name: sourceSlotName,
			DataKey: ProviderSourceDataKey, Selection: sourceSelection(1),
		},
		Live: c.f.srv.Live, Now: func() time.Time { return c.when },
		SessionNamespace: sessionNS, PublisherSessionUID: c.sessionUID, PublisherServiceAccount: sourcePublisherSA,
		PortalNamespace: portalReaderNamespace, PortalServiceAccount: portalReaderAccount,
	}
}

func (c *sourceConnection) mount(h *ProviderSourceReports) {
	c.f.srv.ProviderSourceReports = h
	c.f.h = c.f.srv.Handler()
}

func (c *sourceConnection) bindSession(t *testing.T, name string, uid, podUID types.UID) string {
	t.Helper()
	ctx := context.Background()
	sess := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: uid},
		Spec: v1alpha1.AgentSessionSpec{
			Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "synthetic",
			Parent: humanSA, OperatingMode: v1alpha1.OperatingModeRunning,
			Workspace: &v1alpha1.WorkspaceSpec{ID: "synthetic-workspace"},
		},
	}
	if err := c.f.c.Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if sess.UID != uid {
		t.Fatalf("session uid %s", sess.UID)
	}
	sess.Status.Phase = v1alpha1.PhaseRunning
	sess.Status.PodName = name
	if err := c.f.c.Status().Update(ctx, sess); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: sessionNS, UID: podUID,
			OwnerReferences: []metav1.OwnerReference{sourceController(name, uid)},
		},
		Spec: corev1.PodSpec{ServiceAccountName: sourcePublisherSA, Containers: []corev1.Container{{Name: "agent", Image: "synthetic"}}},
	}
	if err := c.f.c.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodRunning
	if err := c.f.c.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	tok := "tok-source-" + name
	id := saIdentity(sessionNS + "/" + sourcePublisherSA)
	id.PodName, id.PodUID = name, string(podUID)
	c.f.auth[tok] = id
	return tok
}

func (c *sourceConnection) bindPortal(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: sourcePortalPod, Namespace: portalReaderNamespace, UID: sourcePortalPodUID},
		Spec:       corev1.PodSpec{ServiceAccountName: portalReaderAccount, Containers: []corev1.Container{{Name: "portal", Image: "synthetic"}}},
	}
	if err := c.f.c.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodRunning
	if err := c.f.c.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	tok := "tok-source-portal"
	id := saIdentity(portalReaderNamespace + "/" + portalReaderAccount)
	id.PodName, id.PodUID = sourcePortalPod, string(sourcePortalPodUID)
	c.f.auth[tok] = id
	return tok
}

func (c *sourceConnection) replacePod(t *testing.T, uid types.UID) {
	t.Helper()
	ctx := context.Background()
	if err := c.f.c.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: sourcePublisherName}}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: sourcePublisherName, Namespace: sessionNS, UID: uid,
			OwnerReferences: []metav1.OwnerReference{sourceController(sourcePublisherName, c.sessionUID)},
		},
		Spec: corev1.PodSpec{ServiceAccountName: sourcePublisherSA, Containers: []corev1.Container{{Name: "agent", Image: "synthetic"}}},
	}
	if err := c.f.c.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodRunning
	if err := c.f.c.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
}

func (c *sourceConnection) deny(t *testing.T, method, path, token, body string) {
	t.Helper()
	gets, updates := c.gets, c.updates
	var payload any
	if method == http.MethodPut {
		payload = body
	}
	w := c.f.do(method, path, token, payload)
	if w.Code == http.StatusOK || w.Code == http.StatusNoContent {
		t.Fatalf("%s %s status %d %s", method, path, w.Code, w.Body.String())
	}
	if token == "" {
		wantError(t, w, http.StatusUnauthorized, apiv1.CodeUnauthenticated)
	}
	if strings.Contains(w.Body.String(), "usedPercent") || strings.Contains(w.Body.String(), sourceIncarnation) || strings.Contains(w.Body.String(), "providerUpdatedAt") {
		t.Fatalf("refusal contains the report: %s", w.Body.String())
	}
	c.holds(t, gets, updates)
}

func (c *sourceConnection) holds(t *testing.T, gets, updates int) {
	t.Helper()
	if c.gets != gets || c.updates != updates {
		t.Fatalf("slot gets %d want %d, updates %d want %d", c.gets, gets, c.updates, updates)
	}
	var cm corev1.ConfigMap
	if err := c.inner.Get(context.Background(), client.ObjectKey{Namespace: sourceSlotNS, Name: sourceSlotName}, &cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data[ProviderSourceDataKey] != string(c.raw) || cm.Data["keep"] != "yes" || cm.Labels["synthetic.example/slot"] != "fixed" || cm.Annotations["synthetic.example/manager"] != "gitops" {
		t.Fatalf("retained slot changed %#v labels %#v", cm.Data, cm.Labels)
	}
}

func sourceDocument(t *testing.T, w *httptest.ResponseRecorder) []byte {
	t.Helper()
	body := w.Body.Bytes()
	if !bytes.HasSuffix(body, []byte("\n")) {
		t.Fatalf("document %q", body)
	}
	return bytes.TrimSuffix(body, []byte("\n"))
}

func sourceRetrievedAt() time.Time {
	at, err := time.Parse(time.RFC3339, sourceRetrieved)
	if err != nil {
		panic(err)
	}
	return at
}

func sourceReportOnly(t *testing.T, doc []byte) bool {
	t.Helper()
	for _, word := range []string{"receipt", "quota", "Measured", "entitlement", "inputTokens", "capturedUsage", "usageObservation", "usageRollup"} {
		if strings.Contains(string(doc), word) {
			t.Fatalf("source document infers %s: %s", word, doc)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	var report providerallowance.CurrentSourceReport
	if err := dec.Decode(&report); err != nil || report.ProviderUpdatedAt != nil || report.UsedPercent != "40" {
		t.Fatalf("source document %v %s", err, doc)
	}
	var sess apiv1.Session
	sessionDec := json.NewDecoder(bytes.NewReader(doc))
	sessionDec.DisallowUnknownFields()
	if err := sessionDec.Decode(&sess); err == nil {
		t.Fatal("source document decoded as a session")
	}
	return true
}
