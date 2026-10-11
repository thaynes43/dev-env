package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/contextpublication"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
	"github.com/thaynes43/dev-env/internal/sessiondirectory"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

func directoryFixture(t *testing.T, f *fixture, uid string) *coord.SessionDirectory {
	t.Helper()
	s := testutil.New(t, "codex", clientSA).Session
	s.UID = "original-session"
	if uid != "" {
		s.UID = types.UID(uid)
	}
	s.CreationTimestamp = metav1.NewTime(f.now)
	s.Spec.SessionDirectory = &v1alpha1.SessionDirectoryEnrollment{Version: 1, Project: "sample"}
	spec, err := sessiondirectory.Spec(s)
	if err != nil {
		t.Fatal(err)
	}
	d := &coord.SessionDirectory{ObjectMeta: metav1.ObjectMeta{Name: sessiondirectory.Name(spec.Source), Namespace: "dev-env-system", UID: "directory-original", CreationTimestamp: metav1.NewTime(f.now), Labels: map[string]string{"dev-env.haynesops.com/project": "sample"}}, Spec: spec}
	// Use the store's existing coordination label, independently checked below.
	d.Labels = map[string]string{taskclaim.ProjectLabel: "sample"}
	if err := f.c.Create(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	f.srv.SessionDirectory = &sessiondirectory.KubeStore{Client: f.c, Live: f.c, Namespace: "dev-env-system"}
	f.srv.DirectoryContexts = &contextpublication.KubeStore{Client: f.c, Live: f.c, Namespace: "dev-env-system"}
	f.srv.MetadataDiscoveryProjects = []string{"sample"}
	return d
}
func TestDirectoryIndependentDiscoveryAndMissingSource(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	reader := contextSession(t, f, "reader", clientSA)
	d := directoryFixture(t, f, "")
	path := apiv1.SessionDirectoryPath + "/" + d.Name + "?project=sample"
	w := f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	view := decode[apiv1.SessionDirectory](t, w)
	if view.Current.SourceState != "SourceMissing" || view.Current.StopState != "Unknown" || view.Original.Request.Model != "gpt-6.1-sol" || strings.Contains(w.Body.String(), "prompt") {
		t.Fatal("metadata invented placement or exposed content", w.Body.String())
	}
	f.srv.MetadataDiscoveryProjects = nil
	for _, token := range []string{reader, tokClient, tokHuman, tokStranger} {
		w = f.do(http.MethodGet, path, token, nil)
		if w.Code != http.StatusNotFound || w.Body.String() != f.do(http.MethodGet, apiv1.SessionDirectoryPath+"/missing?project=sample", token, nil).Body.String() {
			t.Fatal("legacy rights or named probe disclosed directory", token, w.Code, w.Body.String())
		}
	}
	if w = f.do(http.MethodGet, path, "unrecognized", nil); w.Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated classification hidden", w.Code)
	}
}
func TestDirectoryContextReferenceWhitelistAndSparseBoundedPage(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	sender := contextSession(t, f, "source", humanSA)
	reader := contextSession(t, f, "reader", clientSA)
	w := f.do(http.MethodPost, apiv1.PublishedContextsPath, sender, contextReq())
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	publication := decode[apiv1.PublishContextResult](t, w)
	d := directoryFixture(t, f, publication.Metadata.Source.UID)
	f.srv.ContextReadProjects = nil // Metadata discovery grants no payload scope.
	path := apiv1.SessionDirectoryPath + "/" + d.Name + "/contexts?project=sample&limit=1"
	w = f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	refs := decode[apiv1.DirectoryContextReferences](t, w)
	if len(refs.Items) != 1 || refs.Items[0].ContentSHA256 != publication.Metadata.ContentSHA256 || strings.Contains(w.Body.String(), contextReq().Text) || strings.Contains(w.Body.String(), "\"text\"") {
		t.Fatal("reference leaked or lost metadata", w.Body.String())
	}
	if w = f.do(http.MethodGet, apiv1.PublishedContextsPath+"/"+publication.Metadata.Name+"?project=sample", reader, nil); w.Code != http.StatusForbidden {
		t.Fatal("discovery granted payload", w.Code)
	}
	p, err := f.srv.PublishedContexts.Get(context.Background(), publication.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	p.Spec.Source.Namespace = "another-namespace"
	lists := 0
	f.srv.DirectoryContexts.Live = interceptor.NewClient(f.c, interceptor.Funcs{List: func(_ context.Context, _ client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		lists++
		options := &client.ListOptions{}
		for _, opt := range opts {
			opt.ApplyToList(options)
		}
		if options.Limit != 1 || options.Namespace != "dev-env-system" {
			t.Fatal("unbounded page", options)
		}
		typed := list.(*coord.PublishedContextList)
		typed.Items = []coord.PublishedContext{*p}
		typed.Continue = "next-one-page"
		return nil
	}})
	w = f.do(http.MethodGet, path, reader, nil)
	refs = decode[apiv1.DirectoryContextReferences](t, w)
	if w.Code != http.StatusOK || len(refs.Items) != 0 || refs.Continue == "" || lists != 1 {
		t.Fatal("sparse page scanned or lost continuation", w.Code, refs, lists)
	}
	if w = f.do(http.MethodGet, apiv1.SessionDirectoryPath+"/"+d.Name+"/contexts?project=sample&limit=2&continue="+refs.Continue, reader, nil); w.Code != http.StatusBadRequest || lists != 1 {
		t.Fatal("rebound cursor read storage", w.Code, lists)
	}
}
func TestDirectoryStrictQueryAndCursor(t *testing.T) {
	f := newFixture(t)
	directoryFixture(t, f, "")
	for _, query := range []string{"project=sample&limit=0", "project=sample&limit=21", "project=sample&limit=1&unexpected=x", "project=sample&project=sample", "project=sample&continue=invalid", "project=sample&continue=" + strings.Repeat("x", 4097)} {
		if w := f.do(http.MethodGet, apiv1.SessionDirectoryPath+"?"+query, tokClient, nil); w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
			t.Fatal("non-strict query accepted", query, w.Code)
		}
	}
	scope := directoryCursor{Version: 1, Route: "directory", Project: "sample", Limit: 20}
	raw, err := encodeDirectoryCursor("synthetic", scope)
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := decodeDirectoryCursor(raw, scope); err != nil || decoded != "synthetic" {
		t.Fatal("cursor round trip", err)
	}
	scope.Project = "other"
	if _, err := decodeDirectoryCursor(raw, scope); err == nil {
		t.Fatal("cursor rebound project")
	}
}
func TestDirectoryOnlyProspectiveServerCanaryEnrollment(t *testing.T) {
	f, b := newCanaryFixture(t, "codex", interceptor.Funcs{})
	f.srv.SessionDirectory = &sessiondirectory.KubeStore{Client: f.c, Live: f.c, Namespace: "dev-env-system"}
	f.srv.SessionDirectoryProjects = []string{"sample"}
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, `{"canaryID":"codex-rules"}`)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	s := f.session(b.Record.SessionName())
	if s.Spec.SessionDirectory == nil || s.Spec.SessionDirectory.Project != "sample" {
		t.Fatal("server selection missing")
	}
	f.srv.SessionDirectory = nil // Flag disable preserves immutable original latch.
	w = f.do(http.MethodPost, apiv1.SessionsPath, tokClient, `{"canaryID":"codex-rules"}`)
	if w.Code != http.StatusOK || f.session(b.Record.SessionName()).Spec.SessionDirectory == nil {
		t.Fatal("flag disable reinterpreted history", w.Code)
	}
	if w = f.do(http.MethodPost, apiv1.SessionsPath, tokClient, `{"canaryID":"codex-rules","sessionDirectory":{"version":1,"project":"sample"}}`); w.Code != http.StatusBadRequest {
		t.Fatal("caller opted in", w.Code)
	}
}

func TestDirectoryFreshPeerAndCoordinatorAuthority(t *testing.T) {
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	reader := contextSession(t, f, "reader", clientSA)
	d := directoryFixture(t, f, "")
	path := apiv1.SessionDirectoryPath + "/" + d.Name + "?project=sample"
	if w := f.do(http.MethodGet, path, tokCoordinator, nil); w.Code != http.StatusOK {
		t.Fatal("configured scoped coordinator denied retained sibling", w.Code)
	}
	f.srv.MetadataDiscoveryProjects = nil
	if w := f.do(http.MethodGet, path, tokCoordinator, nil); w.Code != http.StatusNotFound {
		t.Fatal("direct-child/control scope inherited", w.Code)
	}
	f.srv.MetadataDiscoveryProjects = []string{"sample"}
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: "reader"}, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodSucceeded
	if err := f.c.Status().Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodGet, path, reader, nil); w.Code != http.StatusNotFound {
		t.Fatal("stopped reader retained live discovery", w.Code)
	}
	id := f.auth[tokCoordinator]
	id.PodUID = "earlier-host"
	f.auth[tokCoordinator] = id
	if w := f.do(http.MethodGet, path, tokCoordinator, nil); w.Code != http.StatusNotFound {
		t.Fatal("unconfirmed host disclosed retained directory", w.Code)
	}
}
