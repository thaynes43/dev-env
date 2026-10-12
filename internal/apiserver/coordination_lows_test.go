package apiserver

import (
	"context"
	"encoding/json"
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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/sessiondirectory"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

// rebindCatalog replaces the named catalog document, as a GitOps revision would.
func rebindCatalog(t *testing.T, f *fixture, document string) {
	t.Helper()
	var cm corev1.ConfigMap
	if err := f.c.Get(context.Background(), f.srv.Projects.Key, &cm); err != nil {
		t.Fatal(err)
	}
	cm.Data["catalog.json"] = document
	if err := f.c.Update(context.Background(), &cm); err != nil {
		t.Fatal(err)
	}
}

// bindSessionSnapshot saves a project snapshot of document on a Session, as
// admission would for a Session created under that catalog revision.
func bindSessionSnapshot(t *testing.T, f *fixture, name, document, repo string) {
	t.Helper()
	catalog, err := projectcatalog.Parse([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("sample", repo)
	if err != nil {
		t.Fatal(err)
	}
	data, err := snapshot.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	s := f.session(name)
	s.Spec.Repo = repo
	s.Annotations[v1alpha1.AnnotationProjectSnapshot] = string(data)
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

// beat writes only Session status, as agentd's heartbeat does. That bumps each
// named Session's resourceVersion without changing identity, spec or binding.
func beat(ctx context.Context, t *testing.T, f *fixture, names ...string) {
	t.Helper()
	for _, name := range names {
		s := f.session(name)
		now := metav1.NewTime(f.now.Add(-time.Second))
		if s.Status.Agent == nil {
			s.Status.Agent = &v1alpha1.AgentStatus{}
		}
		s.Status.Agent.LastHeartbeat = &now
		if err := f.c.Status().Update(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
}

// changeOnGet returns a client over f.c that runs change before every Get of
// an object of type T: a write that lands between the API's two reads.
func changeOnGet[T client.Object](f *fixture, change func(context.Context)) client.WithWatch {
	return interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(T); ok {
			change(ctx)
		}
		return c.Get(ctx, key, obj, opts...)
	}})
}

// heartbeatDuringCatalogRead beats the named Sessions while the API rereads
// the named catalog.
func heartbeatDuringCatalogRead(t *testing.T, f *fixture, names ...string) {
	t.Helper()
	f.srv.Live = changeOnGet[*corev1.ConfigMap](f, func(ctx context.Context) { beat(ctx, t, f, names...) })
}

func TestCoordinationFenceIgnoresStatusOnlyHeartbeat(t *testing.T) {
	t.Run("inbox", func(t *testing.T) {
		f := newFixture(t)
		bindCatalogFixture(t, f)
		enableInbox(f)
		sender := claimSession(t, f, "sender", humanSA)
		claimSession(t, f, "recipient", clientSA)
		heartbeatDuringCatalogRead(t, f, "sender", "recipient")
		w := f.do(http.MethodPost, apiv1.InboxPath, sender, inboxRequest(f, "recipient", "beat"))
		if w.Code != http.StatusCreated {
			t.Fatal("status-only heartbeat refused a valid inbox publish", w.Code, w.Body.String())
		}
		// The accepted retry path reads no catalog. Its sender fence follows
		// the inbox store's read of the existing message, so beat there.
		f.srv.Inbox.Live = changeOnGet[*coord.InboxMessage](f, func(ctx context.Context) { beat(ctx, t, f, "sender") })
		w = f.do(http.MethodPost, apiv1.InboxPath, sender, inboxRequest(f, "recipient", "beat"))
		if w.Code != http.StatusOK {
			t.Fatal("status-only heartbeat refused a confirmed inbox retry", w.Code, w.Body.String())
		}
	})
	t.Run("task-claim", func(t *testing.T) {
		f := newFixture(t)
		bindCatalogFixture(t, f)
		enableClaims(f)
		owner := claimSession(t, f, "owner", humanSA)
		heartbeatDuringCatalogRead(t, f, "owner")
		w := f.do(http.MethodPost, apiv1.TaskClaimsPath, owner, claimRequest("issue:175"))
		if w.Code != http.StatusCreated {
			t.Fatal("status-only heartbeat refused a valid claim", w.Code, w.Body.String())
		}
	})
	t.Run("published-context", func(t *testing.T) {
		f := newFixture(t)
		bindCatalogFixture(t, f)
		enableContexts(f)
		source := contextSession(t, f, "source", humanSA)
		heartbeatDuringCatalogRead(t, f, "source")
		w := f.do(http.MethodPost, apiv1.PublishedContextsPath, source, contextReq())
		if w.Code != http.StatusCreated {
			t.Fatal("status-only heartbeat refused a valid publication", w.Code, w.Body.String())
		}
	})
	t.Run("published-context-spec-change-still-refused", func(t *testing.T) {
		f := newFixture(t)
		bindCatalogFixture(t, f)
		enableContexts(f)
		source := contextSession(t, f, "source", humanSA)
		f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.ConfigMap); ok {
				s := f.session("source")
				s.Spec.Parent = "changed-parent"
				if err := f.c.Update(ctx, s); err != nil {
					t.Fatal(err)
				}
			}
			return c.Get(ctx, key, obj, opts...)
		}})
		w := f.do(http.MethodPost, apiv1.PublishedContextsPath, source, contextReq())
		wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
		items, _, err := f.srv.PublishedContexts.List(context.Background(), "sample", 20, "")
		if err != nil || len(items) != 0 {
			t.Fatal("changed source binding created publication", err)
		}
	})
}

func TestPublishedContextLowercasesMixedCaseCatalogRepository(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	enableClaims(f)
	source := contextSession(t, f, "source", humanSA)
	// The catalog accepts a mixed-case repository part; the clone owner stays exact.
	mixed := strings.ReplaceAll(apiProjectCatalog, "thaynes43/actual-source", "thaynes43/Actual-Source")
	rebindCatalog(t, f, mixed)
	bindSessionSnapshot(t, f, "source", mixed, "alias")
	w := f.do(http.MethodPost, apiv1.PublishedContextsPath, source, contextReq())
	if w.Code != http.StatusCreated {
		t.Fatal("mixed-case catalog identity refused publication", w.Code, w.Body.String())
	}
	if got := decode[apiv1.PublishContextResult](t, w).Metadata.Provenance.GitHub; got != "thaynes43/actual-source" {
		t.Fatal("publication GitHub identity not normalized", got)
	}
	// Claims and inbox already normalize the same identity.
	w = f.do(http.MethodPost, apiv1.TaskClaimsPath, source, claimRequest("issue:7"))
	if w.Code != http.StatusCreated || decode[apiv1.TaskClaimResult](t, w).Claim.Provenance.GitHub != "thaynes43/actual-source" {
		t.Fatal("claim normalization changed", w.Code, w.Body.String())
	}
}

func TestDirectoryUnconfirmedReadIsUnknownNotAbsent(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	reader := contextSession(t, f, "reader", clientSA)
	d := directoryFixture(t, f, "")
	routes := func(name string) []string {
		return []string{apiv1.SessionDirectoryPath + "/" + name + "?project=sample", apiv1.SessionDirectoryPath + "/" + name + "/contexts?project=sample"}
	}
	f.srv.SessionDirectory.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*coord.SessionDirectory); ok {
			return apierrors.NewServerTimeout(schema.GroupResource{Group: coord.GroupVersion.Group, Resource: "sessiondirectories"}, "get", 1)
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	for _, path := range routes(d.Name) {
		wantError(t, f.do(http.MethodGet, path, reader, nil), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	}
	f.srv.SessionDirectory.Live = f.c
	malformed := func(uid, project string) string {
		r := d.DeepCopy()
		r.ObjectMeta = metav1.ObjectMeta{Namespace: d.Namespace, UID: types.UID("directory-" + uid), CreationTimestamp: d.CreationTimestamp,
			Labels: map[string]string{taskclaim.ProjectLabel: project, "unexpected": "label"}}
		r.Spec.Source.UID, r.Spec.Project = uid, project
		r.Name = sessiondirectory.Name(r.Spec.Source)
		if err := f.c.Create(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		return r.Name
	}
	// A malformed foreign record stays indistinguishable from absence.
	foreign := malformed("foreign-original", "other")
	absent := sessiondirectory.Name(coord.SessionIdentity{Namespace: sessionNS, Name: "absent", UID: "absent-original"})
	for i, path := range routes(foreign) {
		w := f.do(http.MethodGet, path, reader, nil)
		wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
		if w.Body.String() != f.do(http.MethodGet, routes(absent)[i], reader, nil).Body.String() {
			t.Fatal("foreign record distinguishable from absence", w.Body.String())
		}
	}
	// A malformed record in the caller's own scope is unconfirmed, not absent.
	for _, path := range routes(malformed("own-malformed", "sample")) {
		wantError(t, f.do(http.MethodGet, path, reader, nil), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	}
}

func TestTaskClaimAliasRenameCannotMintSecondOwner(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "labelled", true: "pre-label-claim"}[legacy], func(t *testing.T) { aliasRenameCase(t, legacy) })
	}
}

func aliasRenameCase(t *testing.T, legacy bool) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableClaims(f)
	first := claimSession(t, f, "first", humanSA)
	w := f.do(http.MethodPost, apiv1.TaskClaimsPath, first, claimRequest("issue:175"))
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	original := decode[apiv1.TaskClaimResult](t, w).Claim
	if legacy {
		toLegacyLabels(t, f, &coord.TaskClaim{}, client.ObjectKey{Namespace: "dev-env-system", Name: original.Name})
	}
	// GitOps renames the repository key; the GitHub repository is unchanged.
	renamed := strings.ReplaceAll(apiProjectCatalog, `"alias"`, `"actual"`)
	rebindCatalog(t, f, renamed)
	second := claimSession(t, f, "second", clientSA)
	bindSessionSnapshot(t, f, "second", renamed, "actual")
	req := claimRequest("issue:175")
	req.Identity.Repository = "actual"
	w = f.do(http.MethodPost, apiv1.TaskClaimsPath, second, req)
	if w.Code != http.StatusConflict {
		t.Fatal("alias rename minted a second owner of one GitHub issue", w.Code, w.Body.String())
	}
	if got := decode[apiv1.TaskClaimResult](t, w).Claim; got.UID != original.UID || got.Owner != original.Owner {
		t.Fatal("conflict did not return the original owner", got)
	}
	var claims coord.TaskClaimList
	if err := f.c.List(context.Background(), &claims); err != nil || len(claims.Items) != 1 {
		t.Fatal("rename created a second claim", err, len(claims.Items))
	}
	// Other work under the renamed key is still claimable.
	req.Identity.TaskKey = "issue:176"
	if w = f.do(http.MethodPost, apiv1.TaskClaimsPath, second, req); w.Code != http.StatusCreated {
		t.Fatal("renamed repository refused distinct work", w.Code, w.Body.String())
	}
}

// checkBounds drives one coordination kind against PerSession 2, PerProject 3.
// Keys are distinct across both sessions, so each create is a new identity.
func checkBounds(t *testing.T, create func(token, key string) *httptest.ResponseRecorder, count func() int, first, second string) {
	t.Helper()
	for _, key := range []string{"k-0", "k-1"} {
		if w := create(first, key); w.Code != http.StatusCreated {
			t.Fatal("create under bound refused", key, w.Code, w.Body.String())
		}
	}
	wantError(t, create(first, "k-2"), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	if w := create(first, "k-1"); w.Code != http.StatusOK {
		t.Fatal("retry of an existing record refused at the bound", w.Code, w.Body.String())
	}
	if w := create(second, "k-3"); w.Code != http.StatusCreated {
		t.Fatal("another session refused under the project bound", w.Code, w.Body.String())
	}
	wantError(t, create(second, "k-4"), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	if n := count(); n != 3 {
		t.Fatal("bounded creates stored", n)
	}
}

// toLegacyLabels rewrites a stored record to the project-only label set that
// records created before the source label carry.
func toLegacyLabels(t *testing.T, f *fixture, obj client.Object, key client.ObjectKey, extra ...string) {
	t.Helper()
	if err := f.c.Get(context.Background(), key, obj); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{taskclaim.ProjectLabel: "sample"}
	for _, k := range extra {
		labels[k] = obj.GetLabels()[k]
	}
	obj.SetLabels(labels)
	if err := f.c.Update(context.Background(), obj); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinationObjectBounds(t *testing.T) {
	limits := taskclaim.Limits{PerSession: 2, PerProject: 3}
	t.Run("published-context", func(t *testing.T) {
		f := newFixture(t)
		bindCatalogFixture(t, f)
		enableContexts(f)
		f.srv.PublishedContexts.Limits = limits
		first, second := contextSession(t, f, "first", humanSA), contextSession(t, f, "second", clientSA)
		create := func(token, key string) *httptest.ResponseRecorder {
			r := contextReq()
			r.Key = key
			return f.do(http.MethodPost, apiv1.PublishedContextsPath, token, r)
		}
		count := func() int {
			var l coord.PublishedContextList
			if err := f.c.List(context.Background(), &l); err != nil {
				t.Fatal(err)
			}
			return len(l.Items)
		}
		checkBounds(t, create, count, first, second)
		name := apiv1.ContextName(string(f.session("first").UID), "handoff", "k-0")
		toLegacyLabels(t, f, &coord.PublishedContext{}, client.ObjectKey{Namespace: "dev-env-system", Name: name})
		if w := f.do(http.MethodGet, apiv1.PublishedContextsPath+"/"+name+"?project=sample", second, nil); w.Code != http.StatusOK {
			t.Fatal("publication with legacy labels became unreadable", w.Code, w.Body.String())
		}
	})
	t.Run("inbox", func(t *testing.T) {
		f := newFixture(t)
		bindCatalogFixture(t, f)
		enableInbox(f)
		f.srv.Inbox.Limits = limits
		first, second := claimSession(t, f, "first", humanSA), claimSession(t, f, "second", clientSA)
		recipient := claimSession(t, f, "recipient", clientSA)
		create := func(token, key string) *httptest.ResponseRecorder {
			return f.do(http.MethodPost, apiv1.InboxPath, token, inboxRequest(f, "recipient", key))
		}
		count := func() int {
			var l coord.InboxMessageList
			if err := f.c.List(context.Background(), &l); err != nil {
				t.Fatal(err)
			}
			return len(l.Items)
		}
		checkBounds(t, create, count, first, second)
		name := inbox.MessageName(sessionIdentity(f.session("first")), "k-0")
		toLegacyLabels(t, f, &coord.InboxMessage{}, client.ObjectKey{Namespace: "dev-env-system", Name: name}, inbox.RecipientLabel)
		if w := f.do(http.MethodGet, apiv1.InboxPath+"/"+name+"?project=sample", recipient, nil); w.Code != http.StatusOK {
			t.Fatal("message with legacy labels became unreadable", w.Code, w.Body.String())
		}
	})
	t.Run("task-claim", func(t *testing.T) {
		f := newFixture(t)
		bindCatalogFixture(t, f)
		enableClaims(f)
		f.srv.TaskClaims.Limits = limits
		first, second := claimSession(t, f, "first", humanSA), claimSession(t, f, "second", clientSA)
		create := func(token, key string) *httptest.ResponseRecorder {
			return f.do(http.MethodPost, apiv1.TaskClaimsPath, token, claimRequest("task:"+key))
		}
		count := func() int {
			var l coord.TaskClaimList
			if err := f.c.List(context.Background(), &l); err != nil {
				t.Fatal(err)
			}
			return len(l.Items)
		}
		checkBounds(t, create, count, first, second)
		name := taskclaim.Name(coord.TaskIdentity{Project: "sample", Repository: "alias", TaskKey: "task:k-0"})
		toLegacyLabels(t, f, &coord.TaskClaim{}, client.ObjectKey{Namespace: "dev-env-system", Name: name})
		if w := f.do(http.MethodGet, apiv1.TaskClaimsPath+"/"+name+"?project=sample", second, nil); w.Code != http.StatusOK {
			t.Fatal("claim with legacy labels became unreadable", w.Code, w.Body.String())
		}
	})
}

func TestPublishedContextRequiresExplicitProject(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	source := contextSession(t, f, "source", humanSA)
	f.srv.ContextReadProjects = []string{"other"}
	wantError(t, f.do(http.MethodPost, apiv1.PublishedContextsPath, source, contextReq()), http.StatusForbidden, apiv1.CodeForbidden)
	var l coord.PublishedContextList
	if err := f.c.List(context.Background(), &l); err != nil || len(l.Items) != 0 {
		t.Fatal("publication outside the explicit project list was stored", err)
	}
}

// managedPublishFixture binds a capture canary original whose retained history
// names the caller's Pod as last executor, so its managed output publishes.
func managedPublishFixture(t *testing.T) (*fixture, string, apiv1.PublishContextRequest) {
	t.Helper()
	f, binding := newCanaryFixture(t, "claude", interceptor.Funcs{})
	enableContexts(f)
	record := binding.Record
	record.CaptureOutput = true
	raw, _ := json.Marshal([]projectcanary.Record{record})
	policy, err := projectcanary.Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	f.srv.PrivateProjectCanaries = policy
	token := contextSession(t, f, record.SessionName(), clientSA)
	s := f.session(record.SessionName())
	s.Spec = binding.Session.Spec
	s.Spec.ProjectCanary.CaptureOutput = true
	s.Spec.ProjectCanary.PolicyDigest = record.Digest()
	s.Annotations, s.Labels, s.Finalizers = binding.Session.Annotations, binding.Session.Labels, binding.Session.Finalizers
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{SessionUID: string(s.UID), LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: f.auth[token].PodUID}}
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	invocation := apiv1.ManagedInvocation(string(s.UID), f.auth[token].PodUID)
	return f, token, apiv1.PublishContextRequest{Kind: "transcript-excerpt", Key: apiv1.ManagedOutputKey(invocation, 0), Text: "synthetic managed stdout",
		ManagedOutput: &apiv1.ManagedOutputRequest{Ordinal: 0, ConversationID: "11111111-1111-4111-8111-111111111111", Completeness: "PartialManagedOutput"}}
}

// replaceExecutor moves the retained last executor to another Pod: a status
// write that ends the caller Pod's authority to assert managed output.
func replaceExecutor(ctx context.Context, t *testing.T, f *fixture, name string) {
	t.Helper()
	s := f.session(name)
	s.Status.PrivateHistory.LastExecutor = &v1alpha1.PrivateExecutorStatus{PodUID: "replacement-executor-uid", PodName: s.Name, Generation: s.Generation}
	if err := f.c.Status().Update(ctx, s); err != nil {
		t.Fatal(err)
	}
}

// The publish fence ignores status, so the managed executor recheck is what
// refuses an executor that changed during the catalog read (#223 review).
func TestManagedOutputExecutorChangeDuringCatalogRead(t *testing.T) {
	for _, change := range []string{"heartbeat", "executor"} {
		t.Run(change, func(t *testing.T) {
			f, token, req := managedPublishFixture(t)
			name := f.auth[token].PodName
			f.srv.Live = changeOnGet[*corev1.ConfigMap](f, func(ctx context.Context) {
				if change == "executor" {
					replaceExecutor(ctx, t, f, name)
				} else {
					beat(ctx, t, f, name)
				}
			})
			w := f.do(http.MethodPost, apiv1.PublishedContextsPath, token, req)
			var stored coord.PublishedContextList
			if err := f.c.List(context.Background(), &stored); err != nil {
				t.Fatal(err)
			}
			if change == "heartbeat" {
				if w.Code != http.StatusCreated || len(stored.Items) != 1 {
					t.Fatal("status-only heartbeat refused managed output", w.Code, w.Body.String())
				}
				return
			}
			wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
			if len(stored.Items) != 0 {
				t.Fatal("replaced executor published managed output", len(stored.Items))
			}
		})
	}
}

// The result reader's final fence ignores status too: the recheck of the
// publication against the fresh retained history refuses a changed executor,
// and a status-only write is no refusal (#223 review).
func TestCanaryResultStatusChangeDuringPayloadRead(t *testing.T) {
	for _, change := range []string{"heartbeat", "executor"} {
		t.Run(change, func(t *testing.T) {
			f, s, p, path := canaryResultFixture(t, "claude")
			f.srv.PublishedContexts.Live = changeOnGet[*coord.PublishedContext](f, func(ctx context.Context) {
				if change == "executor" {
					replaceExecutor(ctx, t, f, s.Name)
				} else {
					beat(ctx, t, f, s.Name)
				}
			})
			w := f.do(http.MethodGet, path, tokClient, nil)
			if change == "heartbeat" {
				if w.Code != http.StatusOK || decode[apiv1.CanaryResult](t, w).Context.Text != p.Spec.Text {
					t.Fatal("status-only heartbeat refused the result read", w.Code, w.Body.String())
				}
				return
			}
			wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
			if strings.Contains(w.Body.String(), p.Spec.Text) {
				t.Fatal("replaced executor disclosed text")
			}
		})
	}
}
