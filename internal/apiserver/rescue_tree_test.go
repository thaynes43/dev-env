package apiserver

import (
	"context"
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
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
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
	sh.tree = protocol.RescueTree{OK: true, Files: []protocol.RescueTreeFile{{Path: "README.md", Bytes: []byte("PLANTED-SECRET")}}}

	name := "byte-1006-170000"
	uid := f.podlessRescue(name, treeRepo, humanSA, "")
	id := name + "/" + treeStamp
	path := treePath(name, uid, treePod, "")
	e := wantError(t, f.do(http.MethodGet, path, tokHuman, nil), http.StatusForbidden, apiv1.CodeForbidden)
	if strings.Contains(e.Message, "PLANTED-SECRET") || len(sh.trees) != 0 || len(sh.held) != 0 {
		t.Fatalf("human reached bytes: %s trees %v", e.Message, sh.trees)
	}
	if w := f.do(http.MethodGet, path, tokHuman, nil); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "edited") {
		t.Fatalf("repeat: %d %s", w.Code, w.Body.String())
	}

	sh.list.Rescues = []protocol.RescueEntry{{ID: id, Session: name, Name: treeStamp, Finished: true}}
	if w := f.do(http.MethodGet, apiv1.RescuesPath, tokClient, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), id) {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	e = wantError(t, f.do(http.MethodGet, path, tokClient, nil), http.StatusForbidden, apiv1.CodeForbidden)
	if strings.Contains(e.Message, humanSA) || len(sh.trees) != 0 {
		t.Fatalf("client leaked or reached the shelf: %s trees %v", e.Message, sh.trees)
	}
	clientName := "byte-1006-170001"
	clientUID := f.podlessRescue(clientName, treeRepo, clientSA, "")
	wantError(t, f.do(http.MethodGet, treePath(clientName, clientUID, treePod, ""), tokClient, nil), http.StatusForbidden, apiv1.CodeForbidden)

	own := "byte-1006-170002"
	ownTok := f.sessionPod(own, "dev", 0)
	ownUID := f.markRescue(own, treeRepo, "")
	withheld := wantError(t, f.do(http.MethodGet, treePath(own, ownUID, treePod, ""), ownTok, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if !strings.Contains(withheld.Message, "not served") || strings.Contains(withheld.Message, "PLANTED-SECRET") || len(sh.trees) != 0 {
		t.Fatalf("own session served bytes: %s trees %v", withheld.Message, sh.trees)
	}
	otherTok := f.sessionPod("byte-1006-170003", "dev", 0)
	e = wantError(t, f.do(http.MethodGet, treePath(own, ownUID, treePod, ""), otherTok, nil), http.StatusForbidden, apiv1.CodeForbidden)
	if strings.Contains(e.Message, humanSA) {
		t.Fatalf("sibling leaked %s", e.Message)
	}

	parentName := "byte-1006-170010"
	parentTok := f.sessionPod(parentName, "dev", 0)
	parentUID := string(f.session(parentName).UID)
	named := "byte-1006-170011"
	f.podlessRescue(named, treeRepo, parentName, "")
	wantError(t, f.do(http.MethodGet, treePath(named, string(f.session(named).UID), treePod, ""), parentTok, nil), http.StatusForbidden, apiv1.CodeForbidden)
	proved := "byte-1006-170012"
	f.childRescue(proved, parentName, parentUID)
	child := wantError(t, f.do(http.MethodGet, treePath(proved, string(f.session(proved).UID), treePod, ""), parentTok, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if !strings.Contains(child.Message, "not served") || len(sh.trees) != 0 {
		t.Fatalf("proved child: %s trees %v", child.Message, sh.trees)
	}

	omitted := f.session(own)
	omitted.Status.Rescue.OmittedRefs = 1
	if err := f.c.Status().Update(context.Background(), omitted); err != nil {
		t.Fatal(err)
	}
	e = wantError(t, f.do(http.MethodGet, treePath(own, ownUID, treePod, ""), ownTok, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if !strings.Contains(e.Message, "incomplete") || len(sh.trees) != 0 {
		t.Fatalf("omitted refs: %s", e.Message)
	}
	omitted = f.session(own)
	omitted.Status.Rescue.OmittedRefs = 0
	omitted.Status.Rescue.UnpushedRefs[0].Commit = strings.Repeat("b", 40)
	if err := f.c.Status().Update(context.Background(), omitted); err != nil {
		t.Fatal(err)
	}
	e = wantError(t, f.do(http.MethodGet, treePath(own, ownUID, treePod, ""), ownTok, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if !strings.Contains(e.Message, "canonical") || len(sh.trees) != 0 {
		t.Fatalf("absent commit: %s", e.Message)
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
	pilot := "byte-1006-170013"
	pilotTok := f.sessionPod(pilot, "dev", 0)
	ps := f.session(pilot)
	ps.Annotations = map[string]string{projectcanary.PilotAnnotation: "pilot"}
	if err := f.c.Update(context.Background(), ps); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodGet, treePath(pilot, f.markRescue(pilot, treeRepo, ""), treePod, ""), pilotTok, nil), http.StatusForbidden, apiv1.CodeForbidden)
	wantError(t, f.do(http.MethodGet, apiv1.RescueTreePath(name, treeStamp), tokStranger, nil), http.StatusForbidden, apiv1.CodeForbidden)

	wantError(t, f.do(http.MethodGet, apiv1.RescueTreePath("missing-1006-170000", treeStamp)+query(ownUID, treePod, ""), ownTok, nil), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodGet, apiv1.RescueTreePath("missing-1006-170000", treeStamp)+query(uid, treePod, ""), tokHuman, nil), http.StatusForbidden, apiv1.CodeForbidden)
	wantError(t, f.do(http.MethodGet, apiv1.RescueTreePath("Not_A_Name", treeStamp)+query(ownUID, treePod, ""), ownTok, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantError(t, f.do(http.MethodGet, treePath(own, "22222222-2222-4222-8222-222222222222", treePod, ""), ownTok, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantError(t, f.do(http.MethodGet, treePath(own, ownUID, "33333333-3333-4333-8333-333333333333", ""), ownTok, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantError(t, f.do(http.MethodGet, strings.Replace(treePath(own, ownUID, treePod, ""), "repo="+treeRepo, "repo=dev-env", 1), ownTok, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantError(t, f.do(http.MethodGet, treePath(own, ownUID, treePod, "")+"&extra=1", ownTok, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	f.supersede(own)
	wantError(t, f.do(http.MethodGet, treePath(own, ownUID, treePod, ""), ownTok, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if len(sh.trees) != 0 || len(sh.held) != 0 {
		t.Fatalf("a refused binding called the shelf: trees %v held %v", sh.trees, sh.held)
	}

	projectName := "byte-1006-170006"
	projectTok := f.sessionPod(projectName, "dev", 0)
	snap := treeSnapshot(t, "dev-env", treeRepo)
	projectUID := f.markRescue(projectName, treeRepo, snap)
	if gotSnap := f.session(projectName).Annotations[v1alpha1.AnnotationProjectSnapshot]; gotSnap == "" {
		t.Fatal("project snapshot was not stored")
	}
	e = wantError(t, f.do(http.MethodGet, treePath(projectName, projectUID, treePod, "dev-env"), projectTok, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if !strings.Contains(e.Message, "not served") || strings.Contains(e.Message, "PLANTED-SECRET") {
		t.Fatalf("project: %s", e.Message)
	}
	wantError(t, f.do(http.MethodGet, treePath(projectName, projectUID, treePod, ""), projectTok, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantError(t, f.do(http.MethodGet, treePath(projectName, projectUID, treePod, "other"), projectTok, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantError(t, f.do(http.MethodGet, treePath(name, uid, treePod, "dev-env"), tokHuman, nil), http.StatusForbidden, apiv1.CodeForbidden)
	if len(sh.trees) != 0 {
		t.Fatal("the shelf was asked for file bytes")
	}
	wantNames := []string{name, clientName, own, "byte-1006-170003", parentName, named, proved, deep, canary, pilot, projectName}
	after := sessionNames(t, f)
	if len(after) != len(before)+len(wantNames) {
		t.Fatalf("a read created a session: before %d after %d %v", len(before), len(after), after)
	}
	for _, n := range wantNames {
		if !after[n] {
			t.Fatalf("missing %s in %v", n, after)
		}
	}
}

func (f *fixture) childRescue(name, parent, parentUID string) string {
	f.t.Helper()
	s := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: uuid.NewUUID()},
		Spec: v1alpha1.AgentSessionSpec{
			Repo: treeRepo, Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p",
			Parent: parent, UsageCaptureParentUID: parentUID,
		},
	}
	if err := f.c.Create(context.Background(), s); err != nil {
		f.t.Fatal(err)
	}
	return f.markRescue(name, treeRepo, "")
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
		UnpushedRefs: []v1alpha1.RescuedRef{{
			Repo: "/home/dev/repos/" + repo, Name: "refs/heads/rescue/" + name + "-" + treeStamp,
			Commit: treeCommit, Bundle: "rescue/" + name + "/" + treeStamp + "/" + repo + ".bundle",
		}},
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
