package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/shelf"
)

type contextArchiveFake struct {
	result protocol.CoordArchiveGetResult
	err    error
	calls  int
}

func (f *contextArchiveFake) Get(_ context.Context, q protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	f.calls++
	if q.Resource != "publishedcontexts" || q.Name == "" {
		return protocol.CoordArchiveGetResult{}, errors.New("unexpected query")
	}
	return f.result, f.err
}

func archivedContextResult(t *testing.T, p *coord.PublishedContext) protocol.CoordArchiveGetResult {
	t.Helper()
	p.TypeMeta = metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "PublishedContext"}
	raw, err := protocol.CanonicalJSON(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	m := protocol.CoordArchiveManifest{Format: protocol.CoordArchiveFormat, Resource: "publishedcontexts", Kind: "PublishedContext", APIVersion: protocol.CoordArchiveAPIVersion, Namespace: p.Namespace, Name: p.Name, UID: string(p.UID), ResourceVersion: p.ResourceVersion, Project: p.Spec.Project, Object: protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(raw))}, ArchivedBy: "fixture"}
	return protocol.CoordArchiveGetResult{Manifest: protocol.CoordArchiveStoredManifest{CoordArchiveManifest: m, ArchivedAt: time.Date(2026, 10, 15, 15, 0, 0, 0, time.UTC)}, Object: raw}
}

func TestContextArchivePublishedContextFallbackAndLiveFence(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	source, reader := contextSession(t, f, "source", humanSA), contextSession(t, f, "reader", clientSA)
	w := f.do(http.MethodPost, apiv1.PublishedContextsPath, source, contextReq())
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	published := decode[apiv1.PublishContextResult](t, w)
	p, err := f.srv.PublishedContexts.Get(context.Background(), published.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	archive := &contextArchiveFake{result: archivedContextResult(t, p)}
	f.srv.CoordinationArchiveReads = true
	f.srv.CoordinationArchive = archive
	f.srv.ShelfSlots = shelf.NewExecSlots(1)
	path := apiv1.PublishedContextsPath + "/" + p.Name + "?project=sample"
	// A live object remains byte-for-byte the live projection and never asks shelf.
	w = f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusOK || archive.calls != 0 || decode[apiv1.PublishedContext](t, w).Metadata.Archived {
		t.Fatalf("live read = %d calls=%d %s", w.Code, archive.calls, w.Body.String())
	}
	if err := f.c.Delete(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodGet, path, reader, nil)
	got := decode[apiv1.PublishedContext](t, w)
	if w.Code != http.StatusOK || archive.calls != 1 || !got.Metadata.Archived || got.Metadata.ArchivedAt == nil || got.Text != contextReq().Text || got.Metadata.ContentSHA256 != apiv1.ContextHash(got.Text) || got.Metadata.Attribution.Provider != "" {
		t.Fatalf("archived read = %d calls=%d %#v", w.Code, archive.calls, got)
	}
}

func TestContextArchiveMissingAndForeignDoNotLeak(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	reader := contextSession(t, f, "reader", clientSA)
	archive := &contextArchiveFake{err: protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "absent")}
	f.srv.CoordinationArchive = archive
	f.srv.ShelfSlots = shelf.NewExecSlots(1)
	name := apiv1.ContextName("absent-uid", "result", "missing")
	path := apiv1.PublishedContextsPath + "/" + name + "?project=sample"
	w := f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusNotFound || archive.calls != 0 {
		t.Fatalf("default off = %d calls=%d", w.Code, archive.calls)
	}
	f.srv.CoordinationArchiveReads = true
	w = f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusNotFound || archive.calls != 1 {
		t.Fatalf("archive miss = %d calls=%d", w.Code, archive.calls)
	}
	// A malformed foreign live object is masked before archive fallback.
	foreign := &coord.PublishedContext{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: name}}
	foreign.Spec.Project = "other"
	if err := f.c.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusNotFound || archive.calls != 1 {
		t.Fatalf("foreign mask = %d calls=%d", w.Code, archive.calls)
	}
}

func TestContextArchiveRefusesCorruptResult(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	reader := contextSession(t, f, "reader", clientSA)
	archive := &contextArchiveFake{result: protocol.CoordArchiveGetResult{Object: []byte(`{}`)}}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(1)
	name := apiv1.ContextName("absent-uid", "result", "corrupt")
	w := f.do(http.MethodGet, apiv1.PublishedContextsPath+"/"+name+"?project=sample", reader, nil)
	if w.Code != http.StatusServiceUnavailable || archive.calls != 1 {
		t.Fatalf("corrupt archive = %d calls=%d", w.Code, archive.calls)
	}
}
