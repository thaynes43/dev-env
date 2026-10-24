package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/shelf"
)

const (
	treeStamp  = "20261006-1730"
	treeCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	treePod    = "11111111-1111-4111-8111-111111111111"
	treeRepo   = "haynes-ops"
)

func TestReadRescueTreeAuthorizesTheParent(t *testing.T) {
	f := newFixture(t)
	sh := &fakeShelf{}
	f.srv.Shelf = sh
	before := sessionNames(t, f)
	name := "byte-1006-170000"
	uid := f.podlessRescue(name, treeRepo, humanSA, "")
	id := name + "/" + treeStamp
	sh.tree = goodTree(id)
	path := treePath(name, uid, treePod, "")

	w := f.do(http.MethodGet, path, tokHuman, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("human parent: %d %s", w.Code, w.Body.String())
	}
	got := decode[apiv1.RescueTree](t, w)
	sum := sha256.Sum256([]byte("edited\n"))
	if got.ID != id || got.SessionUID != uid || got.PodUID != treePod || got.Repo != treeRepo || got.Commit != treeCommit || len(got.Files) != 1 || got.Files[0].SHA256 != hex.EncodeToString(sum[:]) || string(got.Files[0].Bytes) != "edited\n" {
		t.Fatalf("tree %+v", got)
	}
	if w2 := f.do(http.MethodGet, path, tokHuman, nil); w2.Code != http.StatusOK {
		t.Fatalf("repeat: %d %s", w2.Code, w2.Body.String())
	}
	if len(sh.held) != 0 || len(sh.trees) != 2 {
		t.Fatalf("held %v trees %v", sh.held, sh.trees)
	}
	if names := sessionNames(t, f); len(names) != len(before)+1 || !names[name] {
		t.Fatalf("sessions changed: before %v after %v", before, names)
	}

	// Listing is not byte permission. The client can list and cannot read.
	sh.list.Rescues = []protocol.RescueEntry{{ID: id, Session: name, Name: treeStamp, Finished: true}}
	if w := f.do(http.MethodGet, apiv1.RescuesPath, tokClient, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), id) {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	e := wantError(t, f.do(http.MethodGet, path, tokClient, nil), http.StatusForbidden, apiv1.CodeForbidden)
	if strings.Contains(e.Message, humanSA) || len(sh.trees) != 2 {
		t.Fatalf("other owner leaked or reached the shelf: %s trees %v", e.Message, sh.trees)
	}

	clientName := "byte-1006-170001"
	clientUID := f.podlessRescue(clientName, treeRepo, clientSA, "")
	sh.tree = goodTree(clientName + "/" + treeStamp)
	if w := f.do(http.MethodGet, treePath(clientName, clientUID, treePod, ""), tokClient, nil); w.Code != http.StatusOK {
		t.Fatalf("owning client: %d %s", w.Code, w.Body.String())
	}

	own := "byte-1006-170002"
	ownTok := f.sessionPod(own, "dev", 0)
	ownUID := f.markRescue(own, treeRepo, "")
	sh.tree = goodTree(own + "/" + treeStamp)
	if w := f.do(http.MethodGet, treePath(own, ownUID, treePod, ""), ownTok, nil); w.Code != http.StatusOK {
		t.Fatalf("own session: %d %s", w.Code, w.Body.String())
	}
	otherTok := f.sessionPod("byte-1006-170003", "dev", 0)
	e = wantError(t, f.do(http.MethodGet, treePath(own, ownUID, treePod, ""), otherTok, nil), http.StatusForbidden, apiv1.CodeForbidden)
	if strings.Contains(e.Message, humanSA) {
		t.Fatalf("sibling leaked %s", e.Message)
	}

	deep := "byte-1006-170004"
	deepTok := f.sessionPod(deep, "dev", 2)
	wantError(t, f.do(http.MethodGet, treePath(deep, f.markRescue(deep, treeRepo, ""), treePod, ""), deepTok, nil), http.StatusForbidden, apiv1.CodeForbidden)
	canary := "byte-1006-170005"
	canaryTok := f.sessionPod(canary, "dev", 0)
	s := f.session(canary)
	s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{}
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodGet, treePath(canary, f.markRescue(canary, treeRepo, ""), treePod, ""), canaryTok, nil), http.StatusForbidden, apiv1.CodeForbidden)
	wantError(t, f.do(http.MethodGet, apiv1.RescueTreePath(name, treeStamp), tokStranger, nil), http.StatusForbidden, apiv1.CodeForbidden)

	trees := len(sh.trees)
	wantError(t, f.do(http.MethodGet, apiv1.RescueTreePath("missing-1006-170000", treeStamp)+query(uid, treePod, ""), tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodGet, apiv1.RescueTreePath("Not_A_Name", treeStamp)+query(uid, treePod, ""), tokHuman, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantError(t, f.do(http.MethodGet, treePath(name, "22222222-2222-4222-8222-222222222222", treePod, ""), tokHuman, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantError(t, f.do(http.MethodGet, treePath(name, uid, "33333333-3333-4333-8333-333333333333", ""), tokHuman, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantError(t, f.do(http.MethodGet, strings.Replace(treePath(name, uid, treePod, ""), "repo="+treeRepo, "repo=dev-env", 1), tokHuman, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantError(t, f.do(http.MethodGet, treePath(name, uid, treePod, "")+"&extra=1", tokHuman, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	f.supersede(name)
	wantError(t, f.do(http.MethodGet, path, tokHuman, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if len(sh.trees) != trees {
		t.Fatalf("a refused binding called the shelf: %v", sh.trees[trees:])
	}

	projectName := "byte-1006-170006"
	snap := treeSnapshot(t, "dev-env", treeRepo)
	projectUID := f.podlessRescue(projectName, treeRepo, humanSA, snap)
	if gotSnap := f.session(projectName).Annotations[v1alpha1.AnnotationProjectSnapshot]; gotSnap == "" {
		t.Fatal("project snapshot was not stored")
	}
	projectPath := treePath(projectName, projectUID, treePod, "dev-env")
	sh.tree = goodTree(projectName + "/" + treeStamp)
	w = f.do(http.MethodGet, projectPath, tokHuman, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("project: %d %s", w.Code, w.Body.String())
	}
	if got := decode[apiv1.RescueTree](t, w); got.Project != "dev-env" {
		t.Fatalf("project field %q body %s", got.Project, w.Body.String())
	}
	wantError(t, f.do(http.MethodGet, treePath(projectName, projectUID, treePod, ""), tokHuman, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantError(t, f.do(http.MethodGet, treePath(projectName, projectUID, treePod, "other"), tokHuman, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantError(t, f.do(http.MethodGet, treePath(name, uid, treePod, "dev-env"), tokHuman, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)

	secret := []byte("PLANTED-SECRET")
	sh.tree = protocol.RescueTree{Code: protocol.RescueTreeIncomplete, Files: []protocol.RescueTreeFile{{Path: "README.md", Bytes: secret}}}
	refused := f.do(http.MethodGet, projectPath, tokHuman, nil)
	e = wantError(t, refused, http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if strings.Contains(refused.Body.String(), "PLANTED-SECRET") {
		t.Fatal("incomplete response contained file bytes")
	}
	sum = sha256.Sum256(secret)
	sh.tree = protocol.RescueTree{OK: true, ID: projectName + "/" + treeStamp, Repo: treeRepo, Commit: treeCommit, Ref: "refs/heads/rescue/x", Files: []protocol.RescueTreeFile{{Path: "README.md", Mode: "100644", SHA256: hex.EncodeToString(sum[:]), Bytes: append([]byte(nil), secret...)}}}
	sh.tree.Files[0].SHA256 = strings.Repeat("ab", 32)
	w = f.do(http.MethodGet, projectPath, tokHuman, nil)
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "PLANTED-SECRET") {
		t.Fatalf("hash mismatch: %d %s", w.Code, w.Body.String())
	}
	sh.err = shelf.ErrNoShelf
	wantError(t, f.do(http.MethodGet, projectPath, tokHuman, nil), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	sh.err = nil
	sh.tree = protocol.RescueTree{Code: protocol.RescueTreeNotFound}
	wantError(t, f.do(http.MethodGet, projectPath, tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)
	if after := sessionNames(t, f); len(after) != len(before)+7 {
		t.Fatalf("a read created a session: %v", after)
	}
}

func (f *fixture) podlessRescue(name, repo, parent, snapshot string) string {
	f.t.Helper()
	s := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: uuid.NewUUID()},
		Spec:       v1alpha1.AgentSessionSpec{Repo: repo, Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p", Parent: parent},
	}
	if snapshot != "" {
		s.Annotations = map[string]string{v1alpha1.AnnotationProjectSnapshot: snapshot}
	}
	if err := f.c.Create(context.Background(), s); err != nil {
		f.t.Fatal(err)
	}
	return f.markRescue(name, repo, snapshot)
}

func (f *fixture) markRescue(name, repo, snapshot string) string {
	f.t.Helper()
	s := f.session(name)
	if snapshot != "" && s.Annotations[v1alpha1.AnnotationProjectSnapshot] == "" {
		s.Annotations = map[string]string{v1alpha1.AnnotationProjectSnapshot: snapshot}
		if err := f.c.Update(context.Background(), s); err != nil {
			f.t.Fatal(err)
		}
		s = f.session(name)
	}
	s.Status.Phase = v1alpha1.PhaseSuspended
	s.Status.Rescue = &v1alpha1.RescueStatus{
		Stamp: treeStamp, Result: v1alpha1.RescueVerified, PodUID: treePod,
		LastBundle: "rescue/" + name + "/" + treeStamp + "/manifest.json",
	}
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		f.t.Fatal(err)
	}
	return string(f.session(name).UID)
}

func (f *fixture) supersede(name string) {
	f.t.Helper()
	s := f.session(name)
	s.Status.Rescue.Superseded = true
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		f.t.Fatal(err)
	}
}

func treePath(name, sessionUID, podUID, project string) string {
	return apiv1.RescueTreePath(name, treeStamp) + query(sessionUID, podUID, project)
}

func query(sessionUID, podUID, project string) string {
	q := url.Values{}
	q.Set("repo", treeRepo)
	q.Set("commit", treeCommit)
	q.Set("sessionUID", sessionUID)
	q.Set("podUID", podUID)
	if project != "" {
		q.Set("project", project)
	}
	return "?" + q.Encode()
}

func goodTree(id string) protocol.RescueTree {
	body := []byte("edited\n")
	sum := sha256.Sum256(body)
	return protocol.RescueTree{
		OK: true, ID: id, Repo: treeRepo, Commit: treeCommit, Ref: "refs/heads/rescue/" + id,
		Files: []protocol.RescueTreeFile{{Path: "README.md", Mode: "100644", SHA256: hex.EncodeToString(sum[:]), Bytes: body}},
	}
}

func treeSnapshot(t *testing.T, project, repo string) string {
	t.Helper()
	rules := "public\n"
	doc := map[string]any{
		"version": 1, "project": project, "catalogRevision": strings.Repeat("ab", 32),
		"rulesRevision": projectcatalog.Digest([]byte(rules)), "rules": rules,
		"repositories": []map[string]string{{"name": repo, "github": "thaynes43/" + repo, "defaultBranch": "main"}},
		"selected":     repo,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projectcatalog.ParseSnapshot(b); err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sessionNames(t *testing.T, f *fixture) map[string]bool {
	t.Helper()
	var list v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, s := range list.Items {
		out[s.Name] = true
	}
	return out
}
