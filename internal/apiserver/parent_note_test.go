package apiserver

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/shelf"
	"github.com/thaynes43/dev-env/internal/templates"
)

// Reproduce section 3's shape with synthetic identities: a dev-env worker,
// its older parent with no snapshot, and the parent's recorded UID. The
// workbench variant also uses the real workbench caller classification path.
func parentNoteFixture(t *testing.T, workbench bool) (f *fixture, parent, child, stranger string) {
	t.Helper()
	f = coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	f.srv.ParentNotes, f.srv.InboxQuestions = true, true
	f.srv.InboxProjects = []string{"dev-env"}
	raw := strings.NewReplacer("sample", "dev-env", "alias", "dev-env", "actual-source", "dev-env").Replace(apiProjectCatalog)
	var cm corev1.ConfigMap
	if err := f.c.Get(context.Background(), f.srv.Projects.Key, &cm); err != nil {
		t.Fatal(err)
	}
	cm.Data["catalog.json"] = raw
	if err := f.c.Update(context.Background(), &cm); err != nil {
		t.Fatal(err)
	}
	parent = f.sessionPod("parent", "dev", 0)
	p := f.session("parent")
	p.Spec.Mode, p.Spec.Parent = v1alpha1.ModeRemote, "flux-system/kustomize-controller"
	if err := f.c.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if workbench {
		f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
		f.srv.Policy.Clients = append(f.srv.Policy.Clients, sessionNS+"/"+templates.WorkbenchServiceAccount)
		pod := sessionPodObject(t, f, "parent")
		pod.Spec.ServiceAccountName = templates.WorkbenchServiceAccount
		pod.Labels[v1alpha1.LabelProfile] = "workbench"
		if err := f.c.Update(context.Background(), pod); err != nil {
			t.Fatal(err)
		}
		id := f.auth[parent]
		id.ServiceAccount = templates.WorkbenchServiceAccount
		id.Username = "system:serviceaccount:" + sessionNS + ":" + templates.WorkbenchServiceAccount
		f.auth[parent] = id
	}
	f.now = f.now.Add(time.Minute)
	child = f.sessionPod("child", "dev", 1)
	s := f.session("child")
	s.Spec.Parent, s.Spec.UsageCaptureParentUID, s.Spec.Repo = p.Name, string(p.UID), "dev-env"
	catalog, err := projectcatalog.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("dev-env", "dev-env")
	if err != nil {
		t.Fatal(err)
	}
	data, err := snapshot.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	s.Annotations = map[string]string{v1alpha1.AnnotationProjectSnapshot: string(data)}
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	stranger = f.sessionPod("stranger", "dev", 0)
	f.h = f.srv.Handler()
	return f, parent, child, stranger
}

func tellParentNote(t *testing.T, f *fixture, token string, req apiv1.TellParentRequest, status int) apiv1.InboxMetadata {
	t.Helper()
	w := f.do(http.MethodPost, apiv1.ParentNotesPath, token, req)
	if w.Code != status {
		t.Fatalf("parent tell status %d, want %d: %s", w.Code, status, w.Body.String())
	}
	if status/100 != 2 {
		return apiv1.InboxMetadata{}
	}
	return decode[apiv1.InboxResult](t, w).Message
}

func parentNoteList(t *testing.T, f *fixture, token, query string) apiv1.InboxList {
	t.Helper()
	w := f.do(http.MethodGet, apiv1.ParentNotesPath+query, token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("parent list status %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"text"`) {
		t.Fatal("parent list exposed text")
	}
	return decode[apiv1.InboxList](t, w)
}

func TestParentNotesLiveShapes(t *testing.T) {
	for _, workbench := range []bool{false, true} {
		name := "session"
		if workbench {
			name = "workbench"
		}
		t.Run(name, func(t *testing.T) {
			f, parent, child, stranger := parentNoteFixture(t, workbench)
			var logs bytes.Buffer
			f.srv.Log = logr.FromSlogHandler(slog.NewJSONHandler(&logs, nil))
			executor := &fakeExec{}
			f.srv.Exec = executor
			req := apiv1.TellParentRequest{Key: "private-parent-key", Kind: inbox.KindQuestion, Text: "  PRIVATE_PARENT_NOTE\né\x00"}
			m := tellParentNote(t, f, child, req, http.StatusCreated)
			if m.Sender != owner(f, "child") || m.Recipient != owner(f, "parent") || m.Project != "dev-env" || m.Kind != req.Kind || m.Actor.Kind != "session" || m.Repository != "dev-env" || m.Provenance.CatalogRevision == "" {
				t.Fatal("note lost its server-derived parties or attribution")
			}
			for _, token := range []string{parent, child} {
				w := f.do(http.MethodGet, apiv1.ParentNotesPath+"/"+m.Name, token, nil)
				if w.Code != http.StatusOK || decode[apiv1.InboxMessage](t, w).Text != req.Text {
					t.Fatal("parent note did not round-trip exact text", w.Code)
				}
			}
			got := parentNoteList(t, f, parent, "?box=inbox")
			if len(got.Items) != 1 || got.Items[0].Name != m.Name || got.Box != "inbox" {
				t.Fatal("parent did not list its note")
			}
			got = parentNoteList(t, f, child, "?box=sent")
			if len(got.Items) != 1 || got.Items[0].Name != m.Name || got.Box != "sent" {
				t.Fatal("child did not list its sent note")
			}
			for _, box := range []string{"inbox", "sent"} {
				if got := parentNoteList(t, f, stranger, "?box="+box); len(got.Items) != 0 {
					t.Fatal("unrelated session learned about a note")
				}
			}
			w := f.do(http.MethodGet, apiv1.ParentNotesPath+"/"+m.Name, stranger, nil)
			wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
			if strings.Contains(w.Body.String(), m.Name) || strings.Contains(w.Body.String(), "PRIVATE_PARENT_NOTE") {
				t.Fatal("hidden read disclosed a note")
			}
			// The new route removes only the parent snapshot refusal. The old
			// peer ask still requires it, with the same 404 it returned before.
			peer := apiv1.PublishInboxRequest{Project: "dev-env", Key: "peer-key", Kind: inbox.KindQuestion, Text: req.Text, ToParent: true}
			wantError(t, f.do(http.MethodPost, apiv1.InboxPath, child, peer), http.StatusNotFound, apiv1.CodeNotFound)
			if len(executor.cmds) != 0 || strings.Contains(logs.String(), "PRIVATE_PARENT_NOTE") || strings.Contains(logs.String(), req.Key) {
				t.Fatal("note text/key was logged or a provider was invoked")
			}
		})
	}
}

func TestParentNotesFailureCases(t *testing.T) {
	cases := []struct {
		name   string
		status int
		change func(*fixture, *apiv1.TellParentRequest)
	}{
		{"no-parent", 422, func(f *fixture, _ *apiv1.TellParentRequest) {
			changeParentChild(t, f, func(s *v1alpha1.AgentSession) { s.Spec.Parent = "" })
		}},
		{"account-parent", 422, func(f *fixture, _ *apiv1.TellParentRequest) {
			changeParentChild(t, f, func(s *v1alpha1.AgentSession) { s.Spec.Parent = "other/account" })
		}},
		{"missing-parent", 404, func(f *fixture, _ *apiv1.TellParentRequest) { removeInboxSession(t, f, "parent") }},
		{"parent-uid", 404, func(f *fixture, _ *apiv1.TellParentRequest) {
			changeParentChild(t, f, func(s *v1alpha1.AgentSession) { s.Spec.UsageCaptureParentUID = "earlier-parent" })
		}},
		{"later-parent", 404, func(f *fixture, _ *apiv1.TellParentRequest) {
			p := f.session("parent")
			p.CreationTimestamp = metav1.NewTime(f.now.Add(time.Hour))
			if err := f.c.Update(context.Background(), p); err != nil {
				t.Fatal(err)
			}
		}},
		{"no-snapshot", 422, func(f *fixture, _ *apiv1.TellParentRequest) {
			changeParentChild(t, f, func(s *v1alpha1.AgentSession) { s.Annotations = nil })
		}},
		{"workspace", 422, func(f *fixture, _ *apiv1.TellParentRequest) {
			changeParentChild(t, f, func(s *v1alpha1.AgentSession) { s.Spec.Workspace = &v1alpha1.WorkspaceSpec{} })
		}},
		{"empty-text", 400, func(_ *fixture, r *apiv1.TellParentRequest) { r.Text = "" }},
		{"oversized-text", 400, func(_ *fixture, r *apiv1.TellParentRequest) { r.Text = strings.Repeat("é", 8193) }},
		{"unknown-kind", 400, func(_ *fixture, r *apiv1.TellParentRequest) { r.Kind = "approval" }},
		{"empty-kind", 400, func(_ *fixture, r *apiv1.TellParentRequest) { r.Kind = "" }},
		{"reply-kind", 400, func(_ *fixture, r *apiv1.TellParentRequest) { r.Kind = "reply" }},
		{"bad-key", 400, func(_ *fixture, r *apiv1.TellParentRequest) { r.Key = "bad/key" }},
		{"project-disabled", 404, func(f *fixture, _ *apiv1.TellParentRequest) { f.srv.InboxProjects = []string{"other"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _, child, _ := parentNoteFixture(t, false)
			req := apiv1.TellParentRequest{Key: "failure", Kind: "question", Text: "private text"}
			tc.change(f, &req)
			tellParentNote(t, f, child, req, tc.status)
			if countMessages(t, f) != 0 {
				t.Fatal("refusal stored a note")
			}
		})
	}
}

func changeParentChild(t *testing.T, f *fixture, change func(*v1alpha1.AgentSession)) {
	t.Helper()
	s := f.session("child")
	change(s)
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func TestParentNotesIntentOnlyAndBounds(t *testing.T) {
	f, _, child, _ := parentNoteFixture(t, false)
	for _, body := range []string{
		`{"key":"k","kind":"question","text":"x","recipient":{"name":"stranger"}}`,
		`{"key":"k","kind":"question","text":"x","project":"other"}`,
		`{"key":"k","kind":"question","text":"x","sender":{}}`,
		`{"key":"k","kind":"question","text":"x","child":"stranger"}`,
		"{\"key\":\"k\",\"kind\":\"question\",\"text\":\"" + string([]byte{255}) + "\"}",
		`{} {}`, `null`, strings.Repeat(" ", inbox.MaxRequestBytes+1),
	} {
		wantError(t, f.do(http.MethodPost, apiv1.ParentNotesPath, child, body), 400, apiv1.CodeBadRequest)
	}
	if countMessages(t, f) != 0 {
		t.Fatal("invalid intent stored a record")
	}
	for _, kind := range []string{"question", "result", "blocker"} {
		req := apiv1.TellParentRequest{Key: kind, Kind: kind, Text: strings.Repeat("\x00", 16384)}
		tellParentNote(t, f, child, req, http.StatusCreated)
	}
	for _, query := range []string{"?box=other", "?box=sent&box=inbox", "?limit=0", "?limit=101", "?limit=one", "?limit=1&limit=2", "?continue=bogus", "?continue=0:%00", "?project=dev-env", "?recipient=stranger", "?box=%zz"} {
		wantError(t, f.do(http.MethodGet, apiv1.ParentNotesPath+query, child, nil), 400, apiv1.CodeBadRequest)
	}
}

func TestParentNotesDefaultOffAndIndependentQuestionFlag(t *testing.T) {
	f, parent, child, _ := parentNoteFixture(t, false)
	f.srv.ParentNotes = false
	f.h = f.srv.Handler()
	// Flag off (DESIGN-062 section 7.3): each route this slice serves answers
	// 503 to an authenticated caller, as the durable inbox does when it is off.
	for _, rt := range []struct{ method, path string }{
		{http.MethodPost, apiv1.ParentNotesPath}, {http.MethodGet, apiv1.ParentNotesPath}, {http.MethodGet, apiv1.ParentNotesPath + "/name"},
	} {
		for _, token := range []string{child, parent} {
			w := f.do(rt.method, rt.path, token, struct{}{})
			wantError(t, w, http.StatusServiceUnavailable, apiv1.CodeUnavailable)
			if !strings.Contains(w.Body.String(), "parent notes are not enabled") {
				t.Fatalf("%s %s: the refusal does not say parent notes are off: %s", rt.method, rt.path, w.Body.String())
			}
		}
		// A caller with no token is refused before the flag is read.
		if w := f.do(rt.method, rt.path, "", struct{}{}); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s with no token: status %d, want 401", rt.method, rt.path, w.Code)
		}
	}
	// The answer and wait routes belong to a later slice and do not exist.
	for _, path := range []string{apiv1.ParentNotesPath + "/name/answer", apiv1.ParentNotesPath + "/name/wait"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			for _, token := range []string{"", child, parent} {
				w := f.do(method, path, token, struct{}{})
				wantError(t, w, 404, apiv1.CodeNotFound)
				if !strings.Contains(w.Body.String(), "no route "+method+" "+path) {
					t.Fatal("a later slice's route was registered")
				}
			}
		}
	}
	if countMessages(t, f) != 0 {
		t.Fatal("disabled route wrote a note")
	}
	f.srv.ParentNotes, f.srv.InboxQuestions = true, false
	f.h = f.srv.Handler()
	m := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "independent", Kind: "question", Text: "x"}, 201)
	if got := parentNoteList(t, f, parent, ""); len(got.Items) != 1 {
		t.Fatal("questions flag gated parent list")
	}
	for _, path := range []string{apiv1.ParentNotesPath + "/" + m.Name + "/answer", apiv1.ParentNotesPath + "/" + m.Name + "/wait"} {
		wantError(t, f.do(http.MethodPost, path, parent, struct{}{}), 404, apiv1.CodeNotFound)
	}
}

func TestParentNotesRetryAndDurableParties(t *testing.T) {
	f, parent, child, stranger := parentNoteFixture(t, false)
	req := apiv1.TellParentRequest{Key: "retry", Kind: "result", Text: "same text"}
	m := tellParentNote(t, f, child, req, 201)
	resumeInboxPod(t, f, child)
	if got := tellParentNote(t, f, child, req, 200); !reflect.DeepEqual(got, m) {
		t.Fatal("retry changed receipt")
	}
	changed := req
	changed.Text += "changed"
	tellParentNote(t, f, child, changed, 409)
	changed = req
	changed.Kind = "blocker"
	tellParentNote(t, f, child, changed, 409)
	removeInboxSession(t, f, "parent")
	var cm corev1.ConfigMap
	if err := f.c.Get(context.Background(), f.srv.Projects.Key, &cm); err != nil {
		t.Fatal(err)
	}
	cm.Data["catalog.json"] = "unavailable"
	if err := f.c.Update(context.Background(), &cm); err != nil {
		t.Fatal(err)
	}
	if got := tellParentNote(t, f, child, req, 200); !reflect.DeepEqual(got, m) {
		t.Fatal("retry required live parent or catalog")
	}
	for _, token := range []string{parent, stranger} {
		wantError(t, f.do(http.MethodGet, apiv1.ParentNotesPath+"/"+m.Name, token, nil), 404, apiv1.CodeNotFound)
	}
	if w := f.do(http.MethodGet, apiv1.ParentNotesPath+"/"+m.Name, child, nil); w.Code != 200 {
		t.Fatal("sender lost durable read", w.Code)
	}
	if countMessages(t, f) != 1 {
		t.Fatal("retry duplicated or replaced note")
	}

	// Conversely, a parent needs neither the live child nor a current catalog
	// or enabled project to read its saved notes.
	f, parent, child, _ = parentNoteFixture(t, true)
	m = tellParentNote(t, f, child, req, 201)
	removeInboxSession(t, f, "child")
	f.srv.Projects, f.srv.InboxProjects = nil, nil
	if w := f.do(http.MethodGet, apiv1.ParentNotesPath+"/"+m.Name, parent, nil); w.Code != 200 {
		t.Fatal("parent required live child or catalog", w.Code)
	}
	if got := parentNoteList(t, f, parent, ""); len(got.Items) != 1 {
		t.Fatal("parent lost retained list")
	}
}

func TestParentNotesPrivacyAcrossPeerRoutes(t *testing.T) {
	f, parent, child, _ := askReplyFixture(t)
	f.srv.ParentNotes = true
	f.h = f.srv.Handler()
	req := apiv1.TellParentRequest{Key: "parent-key", Kind: "question", Text: "PRIVATE_PARENT_NOTE"}
	m := tellParentNote(t, f, child, req, 201)
	for _, token := range []string{parent, child, tokCoordinator, tokHuman, tokClient} {
		wantError(t, f.do(http.MethodGet, inboxPath(m), token, nil), 404, apiv1.CodeNotFound)
		wantError(t, f.do(http.MethodGet, ackPath(m), token, nil), 404, apiv1.CodeNotFound)
	}
	for _, token := range []string{parent, tokCoordinator} {
		w := f.do(http.MethodGet, apiv1.InboxPath+"?project=sample&view=thread", token, nil)
		if w.Code != 200 || len(decode[apiv1.InboxList](t, w).Items) != 0 {
			t.Fatal("peer list exposed parent note", w.Code)
		}
	}
	if w := f.do(http.MethodGet, apiv1.InboxPath+"?project=sample&box=sent", child, nil); w.Code != 200 || len(decode[apiv1.InboxList](t, w).Items) != 0 {
		t.Fatal("peer sent list exposed parent note")
	}
	wantError(t, f.do(http.MethodPost, ackPath(m), parent, struct{}{}), 404, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodPost, apiv1.InboxPath, parent, replyTo(m, "answer", "x")), 404, apiv1.CodeNotFound)
	for _, token := range []string{tokCoordinator, tokHuman, tokClient} {
		wantError(t, f.do(http.MethodGet, apiv1.ParentNotesPath+"/"+m.Name, token, nil), 404, apiv1.CodeNotFound)
		wantError(t, f.do(http.MethodGet, apiv1.ParentNotesPath, token, nil), 404, apiv1.CodeNotFound)
		wantError(t, f.do(http.MethodPost, apiv1.ParentNotesPath, token, req), 404, apiv1.CodeNotFound)
	}
	// The shared sender/key name must never confirm the other route's record.
	peer := askParent("peer-key", req.Text)
	p := publish(t, f, child, peer, 201)
	wantError(t, f.do(http.MethodGet, apiv1.ParentNotesPath+"/"+p.Name, parent, nil), 404, apiv1.CodeNotFound)
	req.Key = peer.Key
	tellParentNote(t, f, child, req, 409)
	peer.Key = m.Key
	publish(t, f, child, peer, 409)
	if countMessages(t, f) != 2 {
		t.Fatal("cross-route retry wrote another record")
	}
}

func TestParentNotesPeerArchivePrivacy(t *testing.T) {
	f, _, child, _ := askReplyFixture(t)
	f.srv.ParentNotes = true
	f.h = f.srv.Handler()
	m := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "archive", Kind: "blocker", Text: "PRIVATE_PARENT_NOTE"}, 201)
	stored, err := f.srv.Inbox.Get(context.Background(), m.Name)
	if err != nil {
		t.Fatal(err)
	}
	archived := archivedInboxGetResult(t, stored)
	item := archivedInboxListItem(t, stored)
	if err := f.c.Delete(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	f.srv.CoordinationArchiveReads, f.srv.ShelfSlots = true, shelf.NewExecSlots(2)
	f.srv.CoordinationArchive = &inboxArchiveGetFake{result: archived}
	wantError(t, f.do(http.MethodGet, inboxPath(m), tokCoordinator, nil), 404, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodGet, ackPath(m), tokCoordinator, nil), 404, apiv1.CodeNotFound)
	query := protocol.CoordArchiveListQuery{Resource: "inboxmessages", Project: "sample", Limit: 100}
	next, err := query.Cursor("20261015T180000.000000000Z-" + string(stored.UID) + ".json")
	if err != nil {
		t.Fatal(err)
	}
	f.srv.CoordinationArchive = &inboxArchiveListFake{list: protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{item}, Next: next}}
	w := f.do(http.MethodGet, apiv1.InboxPath+"?project=sample&archived=only", tokCoordinator, nil)
	page := decode[apiv1.InboxList](t, w)
	if w.Code != 200 || len(page.Items) != 0 || page.Continue == "" {
		t.Fatal("archive list exposed parent note", w.Code)
	}
	inner, err := decodeContextArchiveListCursor(page.Continue, contextArchiveListCursor{Route: apiv1.InboxPath, Project: "sample", Limit: 100})
	if err != nil || inner != next {
		t.Fatal("filtered archive page lost its continuation", err)
	}
}

func TestParentNotesLimitsAndOldSchema(t *testing.T) {
	for _, limit := range []string{"sender", "project"} {
		t.Run(limit, func(t *testing.T) {
			f, _, child, _ := parentNoteFixture(t, false)
			if limit == "sender" {
				f.srv.Inbox.Limits.PerSession = 1
			} else {
				f.srv.Inbox.Limits.PerProject = 1
			}
			req := apiv1.TellParentRequest{Key: "first", Kind: "question", Text: "x"}
			tellParentNote(t, f, child, req, 201)
			tellParentNote(t, f, child, req, 200)
			req.Key = "over-limit"
			wantError(t, f.do(http.MethodPost, apiv1.ParentNotesPath, child, req), 429, apiv1.CodeLimitExceeded)
			if countMessages(t, f) != 1 {
				t.Fatal("limit stored a note")
			}
		})
	}
	f, _, child, _ := parentNoteFixture(t, false)
	real := 0
	f.srv.Inbox.Client = interceptor.NewClient(f.srv.Inbox.Client.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		var o client.CreateOptions
		o.ApplyOptions(opts)
		if len(o.DryRun) == 0 {
			real++
			return c.Create(ctx, obj, opts...)
		}
		if err := c.Create(ctx, obj, opts...); err != nil {
			return err
		}
		if u, ok := obj.(*unstructured.Unstructured); ok {
			unstructured.RemoveNestedField(u.Object, "spec", "kind")
		}
		return nil
	}})
	for _, kind := range []string{"question", "result", "blocker"} {
		wantError(t, f.do(http.MethodPost, apiv1.ParentNotesPath, child, apiv1.TellParentRequest{Key: kind, Kind: kind, Text: "x"}), 503, apiv1.CodeUnavailable)
	}
	if real != 0 || countMessages(t, f) != 0 {
		t.Fatal("old schema stored a note")
	}
}

func TestParentNotesRecheckCallerAfterRead(t *testing.T) {
	for _, operation := range []string{"read", "list"} {
		t.Run(operation, func(t *testing.T) {
			f, parent, child, _ := parentNoteFixture(t, false)
			m := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "fence", Kind: "question", Text: "PRIVATE_PARENT_NOTE"}, 201)
			changed := false
			change := func() {
				if !changed {
					resumeInboxPod(t, f, parent)
					changed = true
				}
			}
			f.srv.Inbox.Live = interceptor.NewClient(f.c, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					err := c.Get(ctx, key, obj, opts...)
					change()
					return err
				},
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					err := c.List(ctx, list, opts...)
					change()
					return err
				},
			})
			path := apiv1.ParentNotesPath
			if operation == "read" {
				path += "/" + m.Name
			}
			w := f.do(http.MethodGet, path, parent, nil)
			wantError(t, w, 404, apiv1.CodeNotFound)
			if strings.Contains(w.Body.String(), m.Name) || strings.Contains(w.Body.String(), "PRIVATE_PARENT_NOTE") {
				t.Fatal("fence disclosed a note")
			}
		})
	}
}

func TestParentNotesConcurrentPeerCollision(t *testing.T) {
	for _, winner := range []string{"peer", "parent"} {
		t.Run(winner, func(t *testing.T) {
			f, _, child, _ := askReplyFixture(t)
			f.srv.ParentNotes = true
			f.h = f.srv.Handler()
			base := f.srv.Inbox.Client.(client.WithWatch)
			f.srv.Inbox.Client = interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if m, ok := obj.(*coord.InboxMessage); ok {
					other := m.DeepCopy()
					if winner == "peer" {
						delete(other.Labels, inbox.ParentNoteLabel)
					} else {
						other.Labels[inbox.ParentNoteLabel] = "true"
					}
					if err := c.Create(ctx, other); err != nil {
						return err
					}
				}
				return c.Create(ctx, obj, opts...)
			}})
			if winner == "peer" {
				tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "race", Kind: "question", Text: "x"}, 409)
			} else {
				publish(t, f, child, askParent("race", "x"), 409)
			}
			if countMessages(t, f) != 1 {
				t.Fatal("race duplicated a message")
			}
		})
	}
}

func TestParentNotesReplacementCannotAdoptSavedNotes(t *testing.T) {
	f, _, child, _ := parentNoteFixture(t, false)
	m := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "original", Kind: "blocker", Text: "x"}, 201)
	removeInboxSession(t, f, "parent")
	pod := sessionPodObject(t, f, "parent")
	if err := f.c.Delete(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	replacement := f.sessionPod("parent", "dev", 0)
	wantError(t, f.do(http.MethodGet, apiv1.ParentNotesPath+"/"+m.Name, replacement, nil), 404, apiv1.CodeNotFound)
	if got := parentNoteList(t, f, replacement, ""); len(got.Items) != 0 {
		t.Fatal("replacement adopted original parent's notes")
	}
}

func TestParentNotesFenceBeforeCreate(t *testing.T) {
	for _, change := range []string{"sender-pod", "sender-parent", "parent-uid"} {
		t.Run(change, func(t *testing.T) {
			f, _, child, _ := parentNoteFixture(t, false)
			f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.ConfigMap); ok {
					switch change {
					case "sender-pod":
						resumeInboxPod(t, f, child)
					case "sender-parent":
						changeParentChild(t, f, func(s *v1alpha1.AgentSession) { s.Spec.Parent = "stranger" })
					case "parent-uid":
						p := f.session("parent")
						p.UID = "replacement-parent"
						if err := f.c.Update(ctx, p); err != nil {
							t.Fatal(err)
						}
					}
				}
				return c.Get(ctx, key, obj, opts...)
			}})
			tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "fence", Kind: "result", Text: "x"}, 404)
			if countMessages(t, f) != 0 {
				t.Fatal("changed authority stored a note")
			}
		})
	}
}

func TestParentNotesUnknownCreateAndRetry(t *testing.T) {
	f, _, child, _ := parentNoteFixture(t, false)
	accepted := false
	f.srv.Inbox.Client = interceptor.NewClient(f.srv.Inbox.Client.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if err := c.Create(ctx, obj, opts...); err != nil {
			return err
		}
		if _, ok := obj.(*coord.InboxMessage); ok {
			accepted = true
			return errors.New("synthetic lost response")
		}
		return nil
	}})
	f.srv.Inbox.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if accepted {
			return errors.New("synthetic unavailable observation")
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	req := apiv1.TellParentRequest{Key: "unknown", Kind: "result", Text: "x"}
	wantError(t, f.do(http.MethodPost, apiv1.ParentNotesPath, child, req), 503, apiv1.CodeUnavailable)
	if !accepted || countMessages(t, f) != 1 {
		t.Fatal("fixture did not store one note")
	}
	f.srv.Inbox.Live = f.c
	tellParentNote(t, f, child, req, 200)
	if countMessages(t, f) != 1 {
		t.Fatal("retry duplicated a note")
	}
}
