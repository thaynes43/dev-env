package apiserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/scheduledwork/authority"
	"github.com/thaynes43/dev-env/internal/scheduledwork/executor"
)

// The scheduled-work routes on the fake client (DESIGN-054 S5b). The fake
// client gets the ScheduledWork status subresource, a UID on create, and the
// ScheduledWork CRD for the approve route's schema preflight, read from the
// generated config/crd/ file so the preflight checks the schema this commit
// installs.

const (
	swNS = "dev-env-system"
	// swTemplatesDoc is the dispatch templates ConfigMap's document.
	swTemplatesDoc = `version: 1
templates:
  ops-audit:
    agent: claude
    model: claude-opus-5-5
    effort: xhigh
    size: M
    profile: dev
    repos: [haynes-ops, dev-env]
    modes: [headless]
    maxDuration: 2h
    admits: [human, session, approved]
  ops-desk:
    agent: claude
    model: claude-sonnet-5-5
    effort: high
    size: S
    profile: ops
    repos: [haynes-ops]
    modes: [interactive]
    admits: [human]
`
)

type swFixture struct {
	*fixture
	// crd is what the preflight reads; nil is NotFound. crdErr, when set,
	// is the read's error instead.
	crd    *unstructured.Unstructured
	crdErr error
	// statusWrites counts the ScheduledWork status writes that reached the
	// fake client; specWrites its spec writes.
	statusWrites, specWrites atomic.Int32
	// onStatus, when set, may change or refuse a status write first.
	onStatus func(*v1alpha1.ScheduledWork) error
	// onUpdate, when set, may refuse a spec write first.
	onUpdate func(*v1alpha1.ScheduledWork) error
	// pruneStoredDigest stores each status write without the approval's
	// template digest, as an older CRD would, and leaves the written copy
	// as the client would: still carrying it.
	pruneStoredDigest bool
}

func scheduledWorkFixture(t *testing.T) *swFixture {
	t.Helper()
	f := newFixture(t)
	sf := &swFixture{fixture: f, crd: generatedScheduledWorkCRD(t)}
	f.c = fake.NewClientBuilder().
		WithScheme(testScheme).
		WithStatusSubresource(&v1alpha1.AgentSession{}, &v1alpha1.AccessGrant{}, &v1alpha1.ScheduledWork{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				obj.SetCreationTimestamp(metav1.NewTime(f.now))
				if _, ok := obj.(*v1alpha1.ScheduledWork); ok && obj.GetUID() == "" {
					obj.SetUID(uuid.NewUUID())
				}
				return c.Create(ctx, obj, opts...)
			},
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if u, ok := obj.(*unstructured.Unstructured); ok && u.GroupVersionKind().Kind == "CustomResourceDefinition" {
					switch {
					case sf.crdErr != nil:
						return sf.crdErr
					case sf.crd == nil || key.Name != scheduledWorkCRDName:
						return apierrors.NewNotFound(schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}, key.Name)
					}
					sf.crd.DeepCopyInto(u)
					return nil
				}
				return c.Get(ctx, key, obj, opts...)
			},
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if sw, ok := obj.(*v1alpha1.ScheduledWork); ok {
					if sf.onUpdate != nil {
						if err := sf.onUpdate(sw); err != nil {
							return err
						}
					}
					sf.specWrites.Add(1)
				}
				return c.Update(ctx, obj, opts...)
			},
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if sw, ok := obj.(*v1alpha1.ScheduledWork); ok && sub == "status" {
					if sf.onStatus != nil {
						if err := sf.onStatus(sw); err != nil {
							return err
						}
					}
					sf.statusWrites.Add(1)
					if sf.pruneStoredDigest && sw.Status.Approval != nil {
						cp := sw.DeepCopy()
						cp.Status.Approval.TemplateDigest = ""
						if err := c.SubResource(sub).Update(ctx, cp, opts...); err != nil {
							return err
						}
						sw.ResourceVersion = cp.ResourceVersion
						return nil
					}
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).
		Build()
	f.srv.Client, f.srv.Live = f.c, f.c
	f.srv.ScheduledWork = &ScheduledWorkConfig{Namespace: swNS}
	f.h = f.srv.Handler()
	sf.setTemplates(swTemplatesDoc)
	return sf
}

// generatedScheduledWorkCRD is the ScheduledWork CRD as make generate writes
// it.
func generatedScheduledWorkCRD(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	b, err := os.ReadFile("../../config/crd/dev-env.haynesops.com_scheduledworks.yaml")
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(b, &u.Object); err != nil {
		t.Fatal(err)
	}
	return u
}

// withoutApprovalDigest is crd as an older CRD has it: no
// status.approval.templateDigest.
func withoutApprovalDigest(t *testing.T, crd *unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()
	out := crd.DeepCopy()
	versions, _, _ := unstructured.NestedSlice(out.Object, "spec", "versions")
	for i, v := range versions {
		version := v.(map[string]any)
		path := []string{"schema", "openAPIV3Schema", "properties", "status", "properties", "approval", "properties"}
		props, found, err := unstructured.NestedMap(version, path...)
		if err != nil || !found {
			t.Fatalf("the generated CRD has no approval properties: %v", err)
		}
		if _, ok := props["templateDigest"]; !ok {
			t.Fatal("the generated CRD has no status.approval.templateDigest")
		}
		delete(props, "templateDigest")
		if err := unstructured.SetNestedMap(version, props, path...); err != nil {
			t.Fatal(err)
		}
		versions[i] = version
	}
	if err := unstructured.SetNestedSlice(out.Object, versions, "spec", "versions"); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *swFixture) setTemplates(doc string) {
	f.t.Helper()
	ctx := context.Background()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: swNS, Name: authority.TemplatesConfigMap}}
	err := f.c.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	switch {
	case doc == "" && err == nil:
		if err := f.c.Delete(ctx, cm); err != nil {
			f.t.Fatal(err)
		}
	case doc == "":
	case err == nil:
		cm.Data = map[string]string{authority.TemplatesKey: doc}
		if err := f.c.Update(ctx, cm); err != nil {
			f.t.Fatal(err)
		}
	default:
		cm.Data = map[string]string{authority.TemplatesKey: doc}
		if err := f.c.Create(ctx, cm); err != nil {
			f.t.Fatal(err)
		}
	}
}

// templateDigest is the digest of the named template in the current
// templates ConfigMap, as the executor computes it.
func (f *swFixture) templateDigest(name string) string {
	f.t.Helper()
	var cm corev1.ConfigMap
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: swNS, Name: authority.TemplatesConfigMap}, &cm); err != nil {
		f.t.Fatal(err)
	}
	ts, err := authority.ParseTemplates(cm.Data)
	if err != nil {
		f.t.Fatal(err)
	}
	return authority.TemplateDigest(ts[name])
}

func (f *swFixture) stored(name string) *v1alpha1.ScheduledWork {
	f.t.Helper()
	var w v1alpha1.ScheduledWork
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: swNS, Name: name}, &w); err != nil {
		f.t.Fatal(err)
	}
	return &w
}

// setStatus writes w's status as the executor would.
func (f *swFixture) setStatus(name string, edit func(*v1alpha1.ScheduledWorkStatus)) {
	f.t.Helper()
	w := f.stored(name)
	edit(&w.Status)
	if err := f.c.Status().Update(context.Background(), w); err != nil {
		f.t.Fatal(err)
	}
	f.statusWrites.Store(0)
}

func (f *swFixture) create(token string, req apiv1.CreateScheduledWorkRequest) apiv1.ScheduledWork {
	f.t.Helper()
	w := f.do(http.MethodPost, apiv1.ScheduledWorkPath, token, req)
	if w.Code != http.StatusCreated {
		f.t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	return decode[apiv1.ScheduledWork](f.t, w)
}

// newWork is a headless one-shot create, valid at the fixture's clock
// (2026-10-06T17:22:26Z).
func newWork() apiv1.CreateScheduledWorkRequest {
	return apiv1.CreateScheduledWorkRequest{
		Key: "loki-audit", Title: "Loki retention trend audit", Due: "2026-10-20T09:00", TimeZone: "America/New_York",
		NotAfter: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Mode: apiv1.ScheduledWorkHeadless,
		Work:   apiv1.ScheduledWorkOrder{Template: "ops-audit", Repo: "haynes-ops", Prompt: "Audit Loki retention."},
		Notify: []string{"pushover:default"},
	}
}

// coordinator adds a configured coordinator host to the fixture and returns
// its token.
func (f *swFixture) coordinator() string {
	f.t.Helper()
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "codex-host-a-0", Namespace: "dev-env-system", UID: "host-pod-uid",
		Annotations: map[string]string{CoordinatorHostAnnotation: "host-a"}},
		Spec: corev1.PodSpec{ServiceAccountName: "codex-host-a", Containers: []corev1.Container{{Name: "host", Image: "synthetic"}}}}
	if err := f.c.Create(context.Background(), p); err != nil {
		f.t.Fatal(err)
	}
	id := saIdentity(coordinatorSA)
	id.PodName, id.PodUID = p.Name, string(p.UID)
	f.auth[tokCoordinator] = id
	f.srv.Policy.CoordinatorEnabled = true
	f.srv.Policy.Coordinators = []CoordinatorHost{{ServiceAccount: coordinatorSA, PodName: p.Name, HostID: "host-a"}}
	return tokCoordinator
}

func sessionPrincipal(f *swFixture, name string) string {
	return "session/" + name + "/" + string(f.session(name).UID)
}

const humanPrincipal = "human/" + humanSA

func wantField(t *testing.T, e apiv1.Error, field string) {
	t.Helper()
	for _, fe := range e.Fields {
		if fe.Field == field {
			return
		}
	}
	t.Fatalf("refusal %q names no field %q: %+v", e.Message, field, e.Fields)
}

// The routes are off unless the Server is given a ScheduledWorkConfig: every
// one answers 404, as an operator without --enable-scheduled-work-api does.
func TestScheduledWorkRoutesAreOffByDefault(t *testing.T) {
	f := newFixture(t)
	if ScheduledWorkAPI(false, swNS) != nil {
		t.Fatal("the flag off made a config")
	}
	if c := ScheduledWorkAPI(true, swNS); c == nil || c.Namespace != swNS {
		t.Fatalf("the flag on made %+v", c)
	}
	if ScheduledWorkAPI(true, "") != nil {
		t.Fatal("the flag on without a namespace made a config")
	}
	unscoped := scheduledWorkFixture(t)
	unscoped.srv.ScheduledWork = &ScheduledWorkConfig{}
	unscoped.h = unscoped.srv.Handler()
	wantError(t, unscoped.do(http.MethodGet, apiv1.ScheduledWorkPath, tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, apiv1.ScheduledWorkPath}, {http.MethodGet, apiv1.ScheduledWorkPath},
		{http.MethodGet, apiv1.ScheduledWorkItemPath("sched-a")}, {http.MethodPatch, apiv1.ScheduledWorkItemPath("sched-a")},
		{http.MethodPost, apiv1.ScheduledWorkCancelPath("sched-a")}, {http.MethodPost, apiv1.ScheduledWorkApprovePath("sched-a")},
		{http.MethodPost, apiv1.ScheduledWorkRetryPath("sched-a")},
	} {
		wantError(t, f.do(c.method, c.path, tokHuman, newWork()), http.StatusNotFound, apiv1.CodeNotFound)
	}
}

func TestScheduledWorkCreateTakesTheOwnerFromTheToken(t *testing.T) {
	f := scheduledWorkFixture(t)
	tok := f.sessionPod("dev-env-1104-010332", "full", 0)
	got := f.create(tok, newWork())
	me := sessionPrincipal(f, "dev-env-1104-010332")
	stored := f.stored(got.Name)
	switch {
	case !strings.HasPrefix(got.Name, "sched-") || len(got.Name) != len("sched-")+20:
		t.Errorf("name %q", got.Name)
	case stored.Spec.Owner != me || stored.Spec.Editor != me || got.Spec.Owner != me || got.Spec.Editor != me:
		t.Errorf("owner %q editor %q, want %q", stored.Spec.Owner, stored.Spec.Editor, me)
	case got.SpecHash != authority.SpecHash(stored.Spec):
		t.Errorf("view hash %s, stored spec's %s", got.SpecHash, authority.SpecHash(stored.Spec))
	case got.State != apiv1.ScheduledWorkActive || got.Spec.Cancelled || got.Approval != nil:
		t.Errorf("view %+v", got)
	case !strings.Contains(got.Message, "the owner is a session"):
		t.Errorf("message %q", got.Message)
	case !got.CreatedAt.Equal(f.now):
		t.Errorf("createdAt %s", got.CreatedAt)
	}
	// The same key and spec answer the first item; another spec is 409.
	w := f.do(http.MethodPost, apiv1.ScheduledWorkPath, tok, newWork())
	if w.Code != http.StatusOK || decode[apiv1.ScheduledWork](t, w).Name != got.Name {
		t.Fatalf("a replay: %d %s", w.Code, w.Body.String())
	}
	other := newWork()
	other.Title = "something else"
	wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkPath, tok, other), http.StatusConflict, apiv1.CodeConflict)
	// A replay after the due time has passed still answers the item: the
	// lookup comes before the clock's checks. Another spec under the key is
	// still a 409, not the 422 its past due time would get.
	saved := f.now
	f.now = time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	if w := f.do(http.MethodPost, apiv1.ScheduledWorkPath, tok, newWork()); w.Code != http.StatusOK || decode[apiv1.ScheduledWork](t, w).Name != got.Name {
		t.Fatalf("a late replay: %d %s", w.Code, w.Body.String())
	}
	wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkPath, tok, other), http.StatusConflict, apiv1.CodeConflict)
	f.now = saved
	var list v1alpha1.ScheduledWorkList
	if err := f.c.List(context.Background(), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("%d items after a replay and a conflict (%v)", len(list.Items), err)
	}
	// Another caller's same key is another item, owned by that caller.
	h := f.create(tokHuman, newWork())
	if h.Name == got.Name || h.Spec.Owner != humanPrincipal || h.Message != "" {
		t.Errorf("a human's create: %+v", h)
	}
	c := f.create(tokClient, newWork())
	if c.Spec.Owner != "client/"+clientSA || !strings.Contains(c.Message, "no standing authority") {
		t.Errorf("a client's create: owner %q message %q", c.Spec.Owner, c.Message)
	}
}

// No request names the owner, the editor or the cancel: the body is decoded
// strictly, so a forged owner is refused whole.
func TestScheduledWorkCreateRefusesForgedIdentity(t *testing.T) {
	f := scheduledWorkFixture(t)
	tok := f.sessionPod("s-one", "full", 0)
	for _, field := range []string{"owner", "editor", "cancelled", "approval", "status"} {
		body := `{"key":"k","title":"x","due":"2026-10-20T09:00","timeZone":"UTC","notAfter":"2026-12-31T00:00:00Z","mode":"headless",` +
			`"work":{"template":"ops-audit","repo":"haynes-ops","prompt":"p"},"` + field + `":"human/dev-env-system/dev-env-human"}`
		wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkPath, tok, body), http.StatusBadRequest, apiv1.CodeBadRequest)
	}
	if w := f.do(http.MethodPost, apiv1.ScheduledWorkPath, "", newWork()); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
	wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkPath, tokStranger, newWork()), http.StatusForbidden, apiv1.CodeForbidden)
	var list v1alpha1.ScheduledWorkList
	if err := f.c.List(context.Background(), &list); err != nil || len(list.Items) != 0 {
		t.Fatalf("%d items after refusals (%v)", len(list.Items), err)
	}
}

func TestScheduledWorkCreateRefusesBadInput(t *testing.T) {
	f := scheduledWorkFixture(t)
	tok := f.sessionPod("s-one", "full", 0)
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for name, c := range map[string]struct {
		edit  func(*apiv1.CreateScheduledWorkRequest)
		field string
	}{
		"no title":            {func(r *apiv1.CreateScheduledWorkRequest) { r.Title = "  " }, "title"},
		"a 121-char title":    {func(r *apiv1.CreateScheduledWorkRequest) { r.Title = strings.Repeat("é", 121) }, "title"},
		"a bad due":           {func(r *apiv1.CreateScheduledWorkRequest) { r.Due = "2026-10-20 09:00" }, "due"},
		"a due in the past":   {func(r *apiv1.CreateScheduledWorkRequest) { r.Due = "2026-10-01T09:00" }, "due"},
		"a bad zone":          {func(r *apiv1.CreateScheduledWorkRequest) { r.TimeZone = "Mars/Olympus" }, "timeZone"},
		"the Local zone":      {func(r *apiv1.CreateScheduledWorkRequest) { r.TimeZone = "Local" }, "timeZone"},
		"no notAfter":         {func(r *apiv1.CreateScheduledWorkRequest) { r.NotAfter = time.Time{} }, "notAfter"},
		"a past notAfter":     {func(r *apiv1.CreateScheduledWorkRequest) { r.NotAfter = at("2026-10-01T00:00:00Z") }, "notAfter"},
		"notAfter 181 days":   {func(r *apiv1.CreateScheduledWorkRequest) { r.NotAfter = at("2027-04-06T00:00:00Z") }, "notAfter"},
		"notAfter before due": {func(r *apiv1.CreateScheduledWorkRequest) { r.NotAfter = at("2026-10-15T00:00:00Z") }, "notAfter"},
		"a bad mode":          {func(r *apiv1.CreateScheduledWorkRequest) { r.Mode = "batch" }, "mode"},
		"no template":         {func(r *apiv1.CreateScheduledWorkRequest) { r.Work.Template = "" }, "work.template"},
		"a bad template":      {func(r *apiv1.CreateScheduledWorkRequest) { r.Work.Template = "Ops_Audit" }, "work.template"},
		"a dot repo":          {func(r *apiv1.CreateScheduledWorkRequest) { r.Work.Repo = ".." }, "work.repo"},
		"a path repo":         {func(r *apiv1.CreateScheduledWorkRequest) { r.Work.Repo = "a/b" }, "work.repo"},
		"no prompt":           {func(r *apiv1.CreateScheduledWorkRequest) { r.Work.Prompt = "" }, "work.prompt"},
		"an 8193-byte prompt": {func(r *apiv1.CreateScheduledWorkRequest) { r.Work.Prompt = strings.Repeat("a", 8193) }, "work.prompt"},
		"another pushover":    {func(r *apiv1.CreateScheduledWorkRequest) { r.Notify = []string{"pushover:ops"} }, "notify"},
		"a twice-listed page": {func(r *apiv1.CreateScheduledWorkRequest) { r.Notify = []string{"pushover:default", "pushover:default"} }, "notify"},
		"nine pages": {func(r *apiv1.CreateScheduledWorkRequest) {
			r.Notify = strings.Split(strings.Repeat("pushover:default,", 9), ",")[:9]
		}, "notify"},
		"a bad calendar":         {func(r *apiv1.CreateScheduledWorkRequest) { r.Calendar = "gcal:me@example.com" }, "calendar"},
		"interactive, no notify": {func(r *apiv1.CreateScheduledWorkRequest) { r.Mode, r.Notify = apiv1.ScheduledWorkInteractive, nil }, "notify"},
		"a 129-byte key":         {func(r *apiv1.CreateScheduledWorkRequest) { r.Key = strings.Repeat("k", 129) }, "key"},
		"an unknown freq":        {func(r *apiv1.CreateScheduledWorkRequest) { r.Recurrence = &apiv1.ReminderRecurrence{Freq: "hourly"} }, "recurrence"},
		"a wrapping byMonthDay": {func(r *apiv1.CreateScheduledWorkRequest) {
			r.Recurrence = &apiv1.ReminderRecurrence{Freq: "monthly", ByMonthDay: 1<<32 + 15}
		}, "recurrence"},
		"a count of 1001": {func(r *apiv1.CreateScheduledWorkRequest) {
			r.Recurrence = &apiv1.ReminderRecurrence{Freq: "daily", Count: 1001}
		}, "recurrence"},
		"a series already ended": {func(r *apiv1.CreateScheduledWorkRequest) {
			u := at("2026-10-02T00:00:00Z")
			r.Due = "2026-09-01T09:00"
			r.Recurrence = &apiv1.ReminderRecurrence{Freq: "daily", Until: &u}
		}, "recurrence"},
		"eight byDay entries": {func(r *apiv1.CreateScheduledWorkRequest) {
			r.Recurrence = &apiv1.ReminderRecurrence{Freq: "weekly", ByDay: strings.Split("MO,TU,WE,TH,FR,SA,SU,MO", ",")}
		}, "recurrence"},
		"until and count at once": {func(r *apiv1.CreateScheduledWorkRequest) {
			u := at("2026-12-01T00:00:00Z")
			r.Recurrence = &apiv1.ReminderRecurrence{Freq: "daily", Count: 3, Until: &u}
		}, "recurrence"},
	} {
		t.Run(name, func(t *testing.T) {
			req := newWork()
			c.edit(&req)
			e := wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkPath, tok, req), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
			wantField(t, e, c.field)
		})
	}
	// A body over the bound is refused before it is read whole.
	big := `{"title":"` + strings.Repeat("a", maxScheduledWorkBody) + `"}`
	wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkPath, tok, big), http.StatusRequestEntityTooLarge, apiv1.CodeTooLarge)
	var list v1alpha1.ScheduledWorkList
	if err := f.c.List(context.Background(), &list); err != nil || len(list.Items) != 0 {
		t.Fatalf("%d items after refusals (%v)", len(list.Items), err)
	}
	// The bounds themselves are accepted: 120 characters, 8192 bytes, notAfter
	// exactly 180 days on, a weekly recurrence.
	req := newWork()
	req.Title = strings.Repeat("é", 120)
	req.Work.Prompt = strings.Repeat("a", 8192)
	req.NotAfter = f.now.Add(apiv1.MaxScheduledWorkNotAfter)
	req.Recurrence = &apiv1.ReminderRecurrence{Freq: "weekly", ByDay: []string{"MO", "TH"}}
	req.Calendar = "calendar:haynesnetwork"
	f.create(tok, req)
}

func TestScheduledWorkCreateCapsAnOwnersItems(t *testing.T) {
	f := scheduledWorkFixture(t)
	tok := f.sessionPod("s-one", "full", 0)
	for i := range maxLiveScheduledWorkPerOwner {
		req := newWork()
		req.Key = "k" + itoa(i)
		f.create(tok, req)
	}
	req := newWork()
	req.Key = "one-more"
	wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkPath, tok, req), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	// Another owner is not counted against it.
	f.create(tokHuman, req)
}

func TestScheduledWorkListAndShow(t *testing.T) {
	f := scheduledWorkFixture(t)
	one := f.sessionPod("s-one", "full", 0)
	two := f.sessionPod("s-two", "full", 0)
	coord := f.coordinator()
	mine := f.create(one, newWork())
	theirs := f.create(two, newWork())
	f.setStatus(mine.Name, func(st *v1alpha1.ScheduledWorkStatus) {
		st.Occurrences = []v1alpha1.ScheduledWorkOccurrence{{At: metav1.NewTime(time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC)), State: v1alpha1.OccurrenceParked, Reason: "no standing authority"}}
	})

	l := decode[apiv1.ScheduledWorkList](t, f.do(http.MethodGet, apiv1.ScheduledWorkPath, one, nil))
	if len(l.Items) != 1 || l.Items[0].Name != mine.Name || l.Items[0].Occurrences != nil {
		t.Fatalf("own list %+v", l.Items)
	}
	wantError(t, f.do(http.MethodGet, apiv1.ScheduledWorkPath+"?all=true", one, nil), http.StatusForbidden, apiv1.CodeForbidden)
	for _, tok := range []string{tokHuman, coord} {
		if l := decode[apiv1.ScheduledWorkList](t, f.do(http.MethodGet, apiv1.ScheduledWorkPath+"?all=true", tok, nil)); len(l.Items) != 2 {
			t.Errorf("all=true listed %d", len(l.Items))
		}
	}
	if l := decode[apiv1.ScheduledWorkList](t, f.do(http.MethodGet, apiv1.ScheduledWorkPath, tokClient, nil)); len(l.Items) != 0 {
		t.Errorf("a client with no items listed %d", len(l.Items))
	}

	for _, tok := range []string{one, tokHuman, coord} {
		v := decode[apiv1.ScheduledWork](t, f.do(http.MethodGet, apiv1.ScheduledWorkItemPath(mine.Name), tok, nil))
		if v.Name != mine.Name || len(v.Occurrences) != 1 || v.Occurrences[0].State != apiv1.ScheduledOccurrenceParked || v.SpecHash != mine.SpecHash {
			t.Errorf("show: %+v", v)
		}
	}
	// Another session, or a client, cannot tell that the item exists.
	wantError(t, f.do(http.MethodGet, apiv1.ScheduledWorkItemPath(mine.Name), two, nil), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodGet, apiv1.ScheduledWorkItemPath(theirs.Name), tokClient, nil), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodGet, apiv1.ScheduledWorkItemPath("sched-none"), tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)
}

func TestScheduledWorkEdit(t *testing.T) {
	f := scheduledWorkFixture(t)
	one := f.sessionPod("s-one", "full", 0)
	two := f.sessionPod("s-two", "full", 0)
	coord := f.coordinator()
	item := f.create(one, newWork())
	title := "Loki retention, monthly"
	edit := apiv1.EditScheduledWorkRequest{Title: &title}

	// The owner's edit keeps the owner as the editor and changes the hash.
	v := decode[apiv1.ScheduledWork](t, f.do(http.MethodPatch, apiv1.ScheduledWorkItemPath(item.Name), one, edit))
	if v.Spec.Title != title || v.Spec.Editor != sessionPrincipal(f, "s-one") || v.SpecHash == item.SpecHash {
		t.Fatalf("owner edit: %+v", v)
	}
	// A coordinator may see it but not edit it; another session cannot see it.
	wantError(t, f.do(http.MethodPatch, apiv1.ScheduledWorkItemPath(item.Name), coord, edit), http.StatusForbidden, apiv1.CodeForbidden)
	wantError(t, f.do(http.MethodPatch, apiv1.ScheduledWorkItemPath(item.Name), two, edit), http.StatusNotFound, apiv1.CodeNotFound)
	// A human's edit makes the human the editor, in the same write: the item
	// then needs approval.
	prompt := "Audit Loki retention; open a PR."
	writes := f.specWrites.Load()
	v = decode[apiv1.ScheduledWork](t, f.do(http.MethodPatch, apiv1.ScheduledWorkItemPath(item.Name), tokHuman, apiv1.EditScheduledWorkRequest{Work: &apiv1.EditScheduledWorkOrder{Prompt: &prompt}}))
	stored := f.stored(item.Name)
	if f.specWrites.Load() != writes+1 || stored.Spec.Editor != humanPrincipal || stored.Spec.Work.Prompt != prompt || !strings.Contains(v.Message, "not the owner's") {
		t.Fatalf("human edit: writes %d, stored %+v, message %q", f.specWrites.Load()-writes, stored.Spec, v.Message)
	}
	// An edit that changes nothing writes nothing, and leaves the editor.
	writes = f.specWrites.Load()
	decode[apiv1.ScheduledWork](t, f.do(http.MethodPatch, apiv1.ScheduledWorkItemPath(item.Name), one, apiv1.EditScheduledWorkRequest{Work: &apiv1.EditScheduledWorkOrder{Prompt: &prompt}}))
	if f.specWrites.Load() != writes || f.stored(item.Name).Spec.Editor != humanPrincipal {
		t.Error("a no-op edit wrote")
	}
	// Notify [] clears, calendar "" clears, clearRecurrence makes it one-shot.
	cal, none := "calendar:haynesnetwork", []string{}
	rec := &apiv1.ReminderRecurrence{Freq: "weekly", ByDay: []string{"TU"}}
	decode[apiv1.ScheduledWork](t, f.do(http.MethodPatch, apiv1.ScheduledWorkItemPath(item.Name), one, apiv1.EditScheduledWorkRequest{Calendar: &cal, Recurrence: rec}))
	if s := f.stored(item.Name).Spec; s.Calendar != cal || s.Recurrence == nil || s.Editor != sessionPrincipal(f, "s-one") {
		t.Fatalf("calendar and recurrence: %+v", s)
	}
	empty := ""
	decode[apiv1.ScheduledWork](t, f.do(http.MethodPatch, apiv1.ScheduledWorkItemPath(item.Name), one, apiv1.EditScheduledWorkRequest{Calendar: &empty, Notify: &none, ClearRecurrence: true}))
	if s := f.stored(item.Name).Spec; s.Calendar != "" || s.Recurrence != nil || len(s.Notify) != 0 {
		t.Fatalf("clears: %+v", s)
	}
	// Refusals change nothing.
	before := f.stored(item.Name).ResourceVersion
	wantError(t, f.do(http.MethodPatch, apiv1.ScheduledWorkItemPath(item.Name), one, apiv1.EditScheduledWorkRequest{Recurrence: rec, ClearRecurrence: true}), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	mode := apiv1.ScheduledWorkInteractive
	wantField(t, wantError(t, f.do(http.MethodPatch, apiv1.ScheduledWorkItemPath(item.Name), one, apiv1.EditScheduledWorkRequest{Mode: &mode}), http.StatusUnprocessableEntity, apiv1.CodeInvalid), "notify")
	far := f.now.Add(apiv1.MaxScheduledWorkNotAfter + time.Hour)
	wantField(t, wantError(t, f.do(http.MethodPatch, apiv1.ScheduledWorkItemPath(item.Name), one, apiv1.EditScheduledWorkRequest{NotAfter: &far}), http.StatusUnprocessableEntity, apiv1.CodeInvalid), "notAfter")
	wantError(t, f.do(http.MethodPatch, apiv1.ScheduledWorkItemPath(item.Name), one, `{"editor":"human/x/y"}`), http.StatusBadRequest, apiv1.CodeBadRequest)
	if f.stored(item.Name).ResourceVersion != before {
		t.Error("a refused edit wrote")
	}
}

// An edit carries the resourceVersion it read: a write that loses a race is
// decided again on the new state, and one that keeps losing is a 409, never a
// blind overwrite.
func TestScheduledWorkEditIsCompareAndSet(t *testing.T) {
	f := scheduledWorkFixture(t)
	one := f.sessionPod("s-one", "full", 0)
	item := f.create(one, newWork())
	var calls atomic.Int32
	f.onUpdate = func(sw *v1alpha1.ScheduledWork) error {
		if calls.Add(1) == 1 {
			// Another writer gets there between the read and the write: its
			// title lands, and the API server (the fake client) refuses the
			// write that carries the older resourceVersion.
			cur := f.stored(sw.Name)
			cur.Spec.Title = "raced"
			if err := f.c.Update(context.Background(), cur); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	prompt := "new prompt"
	v := decode[apiv1.ScheduledWork](t, f.do(http.MethodPatch, apiv1.ScheduledWorkItemPath(item.Name), one, apiv1.EditScheduledWorkRequest{Work: &apiv1.EditScheduledWorkOrder{Prompt: &prompt}}))
	if v.Spec.Title != "raced" || v.Spec.Work.Prompt != prompt {
		t.Fatalf("the retried edit lost the other write: %+v", v.Spec)
	}
	f.onUpdate = func(sw *v1alpha1.ScheduledWork) error {
		return apierrors.NewConflict(schema.GroupResource{Resource: "scheduledworks"}, sw.Name, errors.New("injected"))
	}
	other := "other"
	wantError(t, f.do(http.MethodPatch, apiv1.ScheduledWorkItemPath(item.Name), one, apiv1.EditScheduledWorkRequest{Title: &other}), http.StatusConflict, apiv1.CodeConflict)
}

func TestScheduledWorkCancel(t *testing.T) {
	f := scheduledWorkFixture(t)
	one := f.sessionPod("s-one", "full", 0)
	two := f.sessionPod("s-two", "full", 0)
	coord := f.coordinator()
	item := f.create(one, newWork())
	wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkCancelPath(item.Name), two, nil), http.StatusNotFound, apiv1.CodeNotFound)
	f.setStatus(item.Name, func(st *v1alpha1.ScheduledWorkStatus) {
		st.Occurrences = []v1alpha1.ScheduledWorkOccurrence{{At: metav1.NewTime(time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC)), State: v1alpha1.OccurrenceRunning, Session: "sw-0123456789ab-202610201300"}}
	})
	v := decode[apiv1.ScheduledWork](t, f.do(http.MethodPost, apiv1.ScheduledWorkCancelPath(item.Name), coord, nil))
	stored := f.stored(item.Name)
	if !stored.Spec.Cancelled || stored.Spec.Editor != "coordinator/"+coordinatorSA || v.State != apiv1.ScheduledWorkCancelled || v.Warning == "" || v.NextDue != nil {
		t.Fatalf("cancel: stored %+v view %+v", stored.Spec, v)
	}
	// Already cancelled answers 200 and writes nothing.
	writes := f.specWrites.Load()
	if w := f.do(http.MethodPost, apiv1.ScheduledWorkCancelPath(item.Name), one, nil); w.Code != http.StatusOK || f.specWrites.Load() != writes {
		t.Fatalf("a second cancel: %d, %d writes", w.Code, f.specWrites.Load()-writes)
	}
	title := "x"
	wantError(t, f.do(http.MethodPatch, apiv1.ScheduledWorkItemPath(item.Name), one, apiv1.EditScheduledWorkRequest{Title: &title}), http.StatusConflict, apiv1.CodeConflict)
	wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, apiv1.ApproveScheduledWorkRequest{SpecHash: v.SpecHash}), http.StatusConflict, apiv1.CodeConflict)
}

// approve is a human's alone, and it records the authenticated human, the
// spec hash the request named and the template digest the API read: a body
// cannot name the approver.
func TestScheduledWorkApproveBindsTheHumanTheSpecAndTheTemplate(t *testing.T) {
	f := scheduledWorkFixture(t)
	one := f.sessionPod("s-one", "full", 0)
	coord := f.coordinator()
	item := f.create(tokClient, newWork())
	req := apiv1.ApproveScheduledWorkRequest{SpecHash: item.SpecHash}
	for _, tok := range []string{one, tokClient, coord} {
		wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tok, req), http.StatusForbidden, apiv1.CodeForbidden)
	}
	wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), "", req), http.StatusUnauthorized, apiv1.CodeUnauthenticated)
	// A forged approver in the body is not a field.
	wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, `{"specHash":"`+item.SpecHash+`","by":"human/dev-env-system/someone"}`), http.StatusBadRequest, apiv1.CodeBadRequest)
	// A stale or malformed hash records nothing.
	wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, apiv1.ApproveScheduledWorkRequest{SpecHash: strings.Repeat("ab", 32)}), http.StatusConflict, apiv1.CodeConflict)
	wantField(t, wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, apiv1.ApproveScheduledWorkRequest{SpecHash: strings.ToUpper(item.SpecHash)}), http.StatusUnprocessableEntity, apiv1.CodeInvalid), "specHash")
	if n := f.statusWrites.Load(); n != 0 || f.stored(item.Name).Status.Approval != nil {
		t.Fatalf("refusals wrote %d statuses", n)
	}

	v := decode[apiv1.ScheduledWork](t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, req))
	stored := f.stored(item.Name)
	a := stored.Status.Approval
	digest := f.templateDigest("ops-audit")
	switch {
	case a == nil || a.By != humanPrincipal || a.SpecHash != item.SpecHash || a.TemplateDigest != digest || !a.At.Equal(&metav1.Time{Time: f.now}):
		t.Fatalf("stored approval %+v, want by %s, hash %s, digest %s", a, humanPrincipal, item.SpecHash, digest)
	case v.Approval == nil || v.Approval.TemplateDigest != digest || v.Approval.By != humanPrincipal:
		t.Fatalf("view approval %+v", v.Approval)
	}
	// The executor's rule holds the approval under that template.
	if d := authority.Decide(stored, authority.OwnerState{}, authority.TemplateBinding{Current: digest, Recorded: a.TemplateDigest}, f.now, time.Minute); !d.Approved {
		t.Fatalf("the recorded approval does not hold: %s", d.ApprovalReason)
	}
	// The same approval again writes nothing.
	writes := f.statusWrites.Load()
	decode[apiv1.ScheduledWork](t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, req))
	if f.statusWrites.Load() != writes {
		t.Error("a repeated approval wrote again")
	}

	// A template edit voids it: show says so, the executor's rule refuses
	// it, and approving again records the new digest.
	f.setTemplates(strings.Replace(swTemplatesDoc, "maxDuration: 2h", "maxDuration: 20h", 1))
	newDigest := f.templateDigest("ops-audit")
	if newDigest == digest {
		t.Fatal("the template edit did not change the digest")
	}
	shown := decode[apiv1.ScheduledWork](t, f.do(http.MethodGet, apiv1.ScheduledWorkItemPath(item.Name), tokHuman, nil))
	if !strings.Contains(shown.Message, "another version of template ops-audit") {
		t.Errorf("show after a template edit: %q", shown.Message)
	}
	if d := authority.Decide(stored, authority.OwnerState{}, authority.TemplateBinding{Current: newDigest, Recorded: a.TemplateDigest}, f.now, time.Minute); d.Approved || d.ApprovalReason != authority.ReasonApprovalTemplate {
		t.Fatalf("an approval under the old template holds: %+v", d)
	}
	decode[apiv1.ScheduledWork](t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, req))
	if got := f.stored(item.Name).Status.Approval.TemplateDigest; got != newDigest {
		t.Fatalf("re-approval recorded %s, want %s", got, newDigest)
	}
	// A spec edit voids it too: the view shows the old hash, show says so.
	title := "edited"
	decode[apiv1.ScheduledWork](t, f.do(http.MethodPatch, apiv1.ScheduledWorkItemPath(item.Name), tokHuman, apiv1.EditScheduledWorkRequest{Title: &title}))
	shown = decode[apiv1.ScheduledWork](t, f.do(http.MethodGet, apiv1.ScheduledWorkItemPath(item.Name), tokHuman, nil))
	if shown.Approval == nil || shown.Approval.SpecHash == shown.SpecHash || !strings.Contains(shown.Message, "earlier spec") {
		t.Errorf("show after a spec edit: %+v", shown)
	}
	wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, req), http.StatusConflict, apiv1.CodeConflict)
}

// An approval needs a template it can bind: each refusal records nothing.
func TestScheduledWorkApproveRefusesWithoutABindableTemplate(t *testing.T) {
	for name, c := range map[string]struct {
		doc  string
		edit func(*apiv1.CreateScheduledWorkRequest)
		want string
	}{
		"no templates ConfigMap": {doc: "", want: "no dispatch templates"},
		"an invalid document":    {doc: "version: 2\n", want: "invalid"},
		"an unknown template":    {doc: swTemplatesDoc, edit: func(r *apiv1.CreateScheduledWorkRequest) { r.Work.Template = "ops-gone" }, want: "no such dispatch template"},
		"a repo it does not allow": {doc: swTemplatesDoc, edit: func(r *apiv1.CreateScheduledWorkRequest) { r.Work.Repo = "cigar-journal" },
			want: "does not allow the repository"},
		"a mode it does not serve": {doc: swTemplatesDoc, edit: func(r *apiv1.CreateScheduledWorkRequest) { r.Work.Template = "ops-desk" }, want: "does not serve headless"},
		"no approved basis": {doc: swTemplatesDoc, edit: func(r *apiv1.CreateScheduledWorkRequest) {
			r.Work.Template, r.Mode = "ops-desk", apiv1.ScheduledWorkInteractive
		}, want: "does not admit approved work"},
	} {
		t.Run(name, func(t *testing.T) {
			f := scheduledWorkFixture(t)
			req := newWork()
			if c.edit != nil {
				c.edit(&req)
			}
			item := f.create(tokClient, req)
			f.setTemplates(c.doc)
			e := wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, apiv1.ApproveScheduledWorkRequest{SpecHash: item.SpecHash}), http.StatusConflict, apiv1.CodeConflict)
			if !strings.Contains(e.Message, c.want) {
				t.Errorf("refusal %q, want %q", e.Message, c.want)
			}
			if f.statusWrites.Load() != 0 || f.stored(item.Name).Status.Approval != nil {
				t.Error("a refused approval wrote")
			}
		})
	}
}

// The approve route's schema preflight: an installed CRD without
// status.approval.templateDigest, or one that cannot be read, is a 503 that
// writes nothing; and an approval whose digest the API server did not keep is
// a 503 too, not a reported success.
func TestScheduledWorkApproveFailsClosedOnAnOlderSchema(t *testing.T) {
	f := scheduledWorkFixture(t)
	item := f.create(tokClient, newWork())
	req := apiv1.ApproveScheduledWorkRequest{SpecHash: item.SpecHash}
	full := f.crd
	f.crd = withoutApprovalDigest(t, full)
	e := wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, req), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	if !strings.Contains(e.Message, "templateDigest") {
		t.Errorf("refusal %q", e.Message)
	}
	f.crd = nil
	wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, req), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	f.crd, f.crdErr = full, apierrors.NewForbidden(schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}, scheduledWorkCRDName, errors.New("injected: no get grant"))
	wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, req), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	if f.statusWrites.Load() != 0 || f.stored(item.Name).Status.Approval != nil {
		t.Fatal("a refused approval wrote")
	}
	// The preflight passes, but the API server prunes the digest: only a
	// fresh read shows it, since the written copy still carries it.
	f.crdErr = nil
	f.pruneStoredDigest = true
	e = wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, req), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	if !strings.Contains(e.Message, "authorises nothing") || strings.Contains(e.Message, "retry the occurrence") {
		t.Errorf("refusal %q", e.Message)
	}
	// With at, the answer says the retry in the same write stands.
	g := scheduledWorkFixture(t)
	parked := parkedItem(t, g, nil)
	g.pruneStoredDigest = true
	at := parkedAt
	e = wantError(t, g.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(parked.Name), tokHuman, apiv1.ApproveScheduledWorkRequest{SpecHash: parked.SpecHash, At: &at}), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	if !strings.Contains(e.Message, "did retry the occurrence at 2026-10-06T13:00:00Z (now Pending, attempt 1)") {
		t.Errorf("refusal %q", e.Message)
	}
	if o := g.stored(parked.Name).Status.Occurrences[0]; o.State != v1alpha1.OccurrencePending || o.Attempt != 1 {
		t.Errorf("the stored occurrence %+v", o)
	}
}

// approve and retry write status with the resourceVersion they read: an
// executor write between the read and the write is never overwritten. The
// request is decided again on the new state, so an occurrence the executor
// marked ending meanwhile is refused, and a write that changed something
// else keeps it.
func TestScheduledWorkDecisionsAreCompareAndSet(t *testing.T) {
	for _, verb := range []string{"approve", "retry"} {
		t.Run(verb+" against a concurrent ending mark", func(t *testing.T) {
			f := scheduledWorkFixture(t)
			item := parkedItem(t, f, nil)
			var calls atomic.Int32
			f.onStatus = func(sw *v1alpha1.ScheduledWork) error {
				if calls.Add(1) == 1 {
					cur := f.stored(sw.Name)
					cur.Status.Occurrences[0].Ending = true
					cur.Status.Occurrences[0].Reason = "cancelled while parked"
					if err := f.c.Status().Update(context.Background(), cur); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}
			at := parkedAt
			var w *httptest.ResponseRecorder
			if verb == "approve" {
				w = f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, apiv1.ApproveScheduledWorkRequest{SpecHash: item.SpecHash, At: &at})
			} else {
				w = f.do(http.MethodPost, apiv1.ScheduledWorkRetryPath(item.Name), tokHuman, apiv1.RetryScheduledWorkRequest{At: at, SpecHash: item.SpecHash})
			}
			if e := wantError(t, w, http.StatusConflict, apiv1.CodeConflict); !strings.Contains(e.Message, "ending") {
				t.Errorf("refusal %q", e.Message)
			}
			o := f.stored(item.Name).Status.Occurrences[0]
			if !o.Ending || o.State != v1alpha1.OccurrenceParked || o.Attempt != 0 || o.RetriedAt != nil || f.stored(item.Name).Status.Approval != nil {
				t.Fatalf("the concurrent mark was overwritten: %+v", o)
			}
		})
	}
	t.Run("approve keeps a concurrent count", func(t *testing.T) {
		f := scheduledWorkFixture(t)
		item := f.create(tokClient, newWork())
		var calls atomic.Int32
		f.onStatus = func(sw *v1alpha1.ScheduledWork) error {
			if calls.Add(1) == 1 {
				cur := f.stored(sw.Name)
				cur.Status.Counts.Skipped = 5
				if err := f.c.Status().Update(context.Background(), cur); err != nil {
					t.Fatal(err)
				}
			}
			return nil
		}
		decode[apiv1.ScheduledWork](t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, apiv1.ApproveScheduledWorkRequest{SpecHash: item.SpecHash}))
		if st := f.stored(item.Name).Status; st.Counts.Skipped != 5 || st.Approval == nil {
			t.Fatalf("status %+v", st)
		}
	})
}

// The API's decision window is the executor's.
func TestScheduledWorkDecisionWindowIsTheExecutors(t *testing.T) {
	if defaultScheduledWorkDecisionWindow != executor.DefaultDecisionWindow {
		t.Fatalf("the API's default decision window %s is not the executor's %s", defaultScheduledWorkDecisionWindow, executor.DefaultDecisionWindow)
	}
}

// parkedItem is a client-owned item with one Parked occurrence at parkedAt,
// as the executor parks one.
var parkedAt = time.Date(2026, 10, 6, 13, 0, 0, 0, time.UTC)

func parkedItem(t *testing.T, f *swFixture, edit func(*v1alpha1.ScheduledWorkOccurrence)) apiv1.ScheduledWork {
	t.Helper()
	req := newWork()
	req.Due, req.Recurrence = "2026-10-06T09:00", &apiv1.ReminderRecurrence{Freq: "daily"}
	item := f.create(tokClient, req)
	o := v1alpha1.ScheduledWorkOccurrence{At: metav1.NewTime(parkedAt), State: v1alpha1.OccurrenceParked, Reason: "no standing authority",
		SpecHash: item.SpecHash, ClaimedAt: &metav1.Time{Time: parkedAt.Add(time.Second)}}
	if edit != nil {
		edit(&o)
	}
	f.setStatus(item.Name, func(st *v1alpha1.ScheduledWorkStatus) { st.Occurrences = []v1alpha1.ScheduledWorkOccurrence{o} })
	return item
}

func TestScheduledWorkApproveAtRetriesTheParkedOccurrence(t *testing.T) {
	f := scheduledWorkFixture(t)
	item := parkedItem(t, f, nil)
	at := parkedAt
	v := decode[apiv1.ScheduledWork](t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, apiv1.ApproveScheduledWorkRequest{SpecHash: item.SpecHash, At: &at}))
	stored := f.stored(item.Name)
	o := stored.Status.Occurrences[0]
	switch {
	case f.statusWrites.Load() != 1:
		t.Fatalf("%d status writes, want one for the approval and the retry", f.statusWrites.Load())
	case stored.Status.Approval == nil || stored.Status.Approval.By != humanPrincipal:
		t.Fatalf("approval %+v", stored.Status.Approval)
	case o.State != v1alpha1.OccurrencePending || o.Attempt != 1 || o.RetriedAt == nil || !o.RetriedAt.Equal(&metav1.Time{Time: f.now}) || o.SpecHash != item.SpecHash:
		t.Fatalf("occurrence %+v", o)
	case v.Occurrences[0].State != apiv1.ScheduledOccurrencePending:
		t.Fatalf("view occurrence %+v", v.Occurrences[0])
	}
	// It is no longer Parked, so the same request again is a 409.
	wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, apiv1.ApproveScheduledWorkRequest{SpecHash: item.SpecHash, At: &at}), http.StatusConflict, apiv1.CodeConflict)
	// A plain approve leaves a Parked occurrence parked.
	g := scheduledWorkFixture(t)
	item = parkedItem(t, g, nil)
	decode[apiv1.ScheduledWork](t, g.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, apiv1.ApproveScheduledWorkRequest{SpecHash: item.SpecHash}))
	if o := g.stored(item.Name).Status.Occurrences[0]; o.State != v1alpha1.OccurrenceParked || o.Attempt != 0 || o.RetriedAt != nil {
		t.Fatalf("a plain approve changed the parked occurrence: %+v", o)
	}
}

// Each refusal of a retry, by approve --at or by retry, is a 409 that records
// nothing, neither the occurrence nor (for approve) the approval.
func TestScheduledWorkRetryRefusals(t *testing.T) {
	sessionOf := func(f *swFixture, item apiv1.ScheduledWork) {
		uid := string(f.stored(item.Name).UID)
		name := "sw-" + strings.ReplaceAll(uid, "-", "")[:12] + "-" + parkedAt.Format("200601021504")
		s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS},
			Spec: v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p", Parent: "client/dev-env-system/scheduled-work"}}
		if err := f.c.Create(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	for name, c := range map[string]struct {
		edit  func(*v1alpha1.ScheduledWorkOccurrence)
		setup func(*swFixture, apiv1.ScheduledWork)
		at    time.Time
		want  string
	}{
		"no occurrence there": {at: parkedAt.Add(time.Hour), want: "has no occurrence at"},
		"not Parked":          {edit: func(o *v1alpha1.ScheduledWorkOccurrence) { o.State = v1alpha1.OccurrenceSkipped }, want: "not Parked"},
		"ending":              {edit: func(o *v1alpha1.ScheduledWorkOccurrence) { o.Ending = true }, want: "ending"},
		"three attempts used": {edit: func(o *v1alpha1.ScheduledWorkOccurrence) {
			o.Attempt, o.RetriedAt = 3, &metav1.Time{Time: parkedAt.Add(time.Hour)}
		}, want: "used its 3 retries"},
		"a session recorded":               {edit: func(o *v1alpha1.ScheduledWorkOccurrence) { o.Session = "sw-0123456789ab-202610061300" }, want: "already has session"},
		"claimed with an earlier spec":     {edit: func(o *v1alpha1.ScheduledWorkOccurrence) { o.SpecHash = strings.Repeat("cd", 32) }, want: "earlier spec"},
		"a session of its name still held": {setup: sessionOf, want: "still exists"},
		"another occurrence unfinished": {setup: func(f *swFixture, item apiv1.ScheduledWork) {
			f.setStatus(item.Name, func(st *v1alpha1.ScheduledWorkStatus) {
				st.Occurrences = append(st.Occurrences, v1alpha1.ScheduledWorkOccurrence{At: metav1.NewTime(parkedAt.Add(24 * time.Hour)), State: v1alpha1.OccurrenceClaimed})
			})
		}, want: "one occurrence runs at a time"},
		"after notAfter": {setup: func(f *swFixture, _ apiv1.ScheduledWork) { f.now = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }, want: "notAfter"},
		// The next daily occurrence, 24 hours on, ends the window.
		"its decision window ended": {setup: func(f *swFixture, _ apiv1.ScheduledWork) { f.now = parkedAt.Add(24 * time.Hour) }, want: "waited for a decision until 2026-10-07T13:00:00Z"},
		"a configured window ended": {setup: func(f *swFixture, _ apiv1.ScheduledWork) {
			f.srv.ScheduledWork.DecisionWindow = 4 * time.Hour
			f.now = parkedAt.Add(4 * time.Hour)
		}, want: "waited for a decision until 2026-10-06T17:00:00Z"},
		"a retried window ended": {edit: func(o *v1alpha1.ScheduledWorkOccurrence) {
			o.Attempt, o.RetriedAt = 1, &metav1.Time{Time: parkedAt.Add(-25 * time.Hour)}
		}, setup: func(f *swFixture, _ apiv1.ScheduledWork) { f.srv.ScheduledWork.DecisionWindow = 2 * time.Hour }, want: "waited for a decision until 2026-10-05T14:00:00Z"},
	} {
		for _, verb := range []string{"approve", "retry"} {
			t.Run(name+"/"+verb, func(t *testing.T) {
				f := scheduledWorkFixture(t)
				item := parkedItem(t, f, c.edit)
				if c.setup != nil {
					c.setup(f, item)
				}
				at := c.at
				if at.IsZero() {
					at = parkedAt
				}
				before := f.stored(item.Name)
				var w *httptest.ResponseRecorder
				if verb == "approve" {
					w = f.do(http.MethodPost, apiv1.ScheduledWorkApprovePath(item.Name), tokHuman, apiv1.ApproveScheduledWorkRequest{SpecHash: item.SpecHash, At: &at})
				} else {
					w = f.do(http.MethodPost, apiv1.ScheduledWorkRetryPath(item.Name), tokHuman, apiv1.RetryScheduledWorkRequest{At: at, SpecHash: item.SpecHash})
				}
				e := wantError(t, w, http.StatusConflict, apiv1.CodeConflict)
				if !strings.Contains(e.Message, c.want) {
					t.Errorf("refusal %q, want %q", e.Message, c.want)
				}
				if after := f.stored(item.Name); f.statusWrites.Load() != 0 || after.ResourceVersion != before.ResourceVersion || after.Status.Approval != nil {
					t.Errorf("a refused %s wrote", verb)
				}
			})
		}
	}
}

func TestScheduledWorkRetry(t *testing.T) {
	f := scheduledWorkFixture(t)
	one := f.sessionPod("s-one", "full", 0)
	// An occurrence parked before any claim has no claim hash and may be
	// retried.
	item := parkedItem(t, f, func(o *v1alpha1.ScheduledWorkOccurrence) { o.SpecHash, o.ClaimedAt = "", nil })
	req := apiv1.RetryScheduledWorkRequest{At: parkedAt, SpecHash: item.SpecHash}
	for _, tok := range []string{one, tokClient} {
		wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkRetryPath(item.Name), tok, req), http.StatusForbidden, apiv1.CodeForbidden)
	}
	e := wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkRetryPath(item.Name), tokHuman, `{}`), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantField(t, e, "at")
	wantField(t, e, "specHash")
	wantError(t, f.do(http.MethodPost, apiv1.ScheduledWorkRetryPath(item.Name), tokHuman, apiv1.RetryScheduledWorkRequest{At: parkedAt, SpecHash: strings.Repeat("ab", 32)}), http.StatusConflict, apiv1.CodeConflict)
	if f.statusWrites.Load() != 0 {
		t.Fatal("a refused retry wrote")
	}
	v := decode[apiv1.ScheduledWork](t, f.do(http.MethodPost, apiv1.ScheduledWorkRetryPath(item.Name), tokHuman, req))
	stored := f.stored(item.Name)
	o := stored.Status.Occurrences[0]
	if o.State != v1alpha1.OccurrencePending || o.Attempt != 1 || o.RetriedAt == nil || stored.Status.Approval != nil || v.Approval != nil {
		t.Fatalf("retry: occurrence %+v approval %+v", o, stored.Status.Approval)
	}
	if !strings.Contains(o.Reason, "retried by "+humanPrincipal) {
		t.Errorf("reason %q", o.Reason)
	}
	// A retry needs no template and no schema preflight: it grants nothing.
	g := scheduledWorkFixture(t)
	item = parkedItem(t, g, nil)
	g.setTemplates("")
	g.crd = nil
	decode[apiv1.ScheduledWork](t, g.do(http.MethodPost, apiv1.ScheduledWorkRetryPath(item.Name), tokHuman, apiv1.RetryScheduledWorkRequest{At: parkedAt, SpecHash: item.SpecHash}))
	// A cancelled item is not retried.
	c := scheduledWorkFixture(t)
	item = parkedItem(t, c, nil)
	decode[apiv1.ScheduledWork](t, c.do(http.MethodPost, apiv1.ScheduledWorkCancelPath(item.Name), tokHuman, nil))
	wantError(t, c.do(http.MethodPost, apiv1.ScheduledWorkRetryPath(item.Name), tokHuman, req), http.StatusConflict, apiv1.CodeConflict)
}
