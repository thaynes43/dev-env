package apiserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/shelf"
)

// TestTaskClaimArchiveGetReadsTheRealArchive is DESIGN-024 E1's archived read.
// The live claim is gone. The GET and the archived-only list answer from the
// real coordination archive. A process exec remains outside this test.
func TestTaskClaimArchiveGetReadsTheRealArchive(t *testing.T) {
	restore := agentd.SetCoordArchiveMountCheck(func(string, string) error { return nil })
	t.Cleanup(restore)

	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableClaims(f)
	owner, reader := claimSession(t, f, "archive-owner", humanSA), claimSession(t, f, "archive-reader", clientSA)
	w := f.do(http.MethodPost, apiv1.TaskClaimsPath, owner, claimRequest("issue:330"))
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	claim, err := f.srv.TaskClaims.Get(context.Background(), decode[apiv1.TaskClaimResult](t, w).Claim.Name)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	root := filepath.Join(home, protocol.CoordArchiveDirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := agentd.CoordArchive{Root: root, Home: home, Now: func() time.Time {
		return time.Date(2026, 10, 17, 22, 0, 0, 0, time.UTC)
	}}
	if err := putRealTaskClaim(&archive, claim); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	f.srv.CoordinationArchiveReads = true
	f.srv.CoordinationArchive = realCoordArchive{a: archive}
	f.srv.ShelfSlots = shelf.NewExecSlots(2)

	w = f.do(http.MethodGet, apiv1.TaskClaimsPath+"/"+claim.Name+"?project=sample", reader, nil)
	got := decode[apiv1.TaskClaim](t, w)
	if w.Code != http.StatusOK || !got.Archived || got.ArchivedAt == nil || !got.ArchivedAt.Equal(time.Date(2026, 10, 17, 22, 0, 0, 0, time.UTC)) || got.Name != claim.Name || got.Identity.Project != "sample" {
		t.Fatalf("archived get = %d %#v %s", w.Code, got, w.Body.String())
	}
	w = f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample&archived=only", reader, nil)
	list := decode[apiv1.TaskClaimList](t, w)
	if w.Code != http.StatusOK || len(list.Items) != 1 || !list.Items[0].Archived || list.Items[0].Name != claim.Name {
		t.Fatalf("archived list = %d %#v %s", w.Code, list, w.Body.String())
	}
}

func putRealTaskClaim(archive *agentd.CoordArchive, claim *coord.TaskClaim) error {
	stored := claim.DeepCopy()
	stored.TypeMeta = metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "TaskClaim"}
	raw, err := protocol.CanonicalJSON(stored)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	manifest := protocol.CoordArchiveManifest{
		Format: protocol.CoordArchiveFormat, Resource: "taskclaims", Kind: "TaskClaim",
		APIVersion: protocol.CoordArchiveAPIVersion, Namespace: stored.Namespace, Name: stored.Name,
		UID: string(stored.UID), ResourceVersion: stored.ResourceVersion, Project: stored.Spec.Identity.Project,
		Object:     protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(raw))},
		ArchivedBy: "dev-env-operator",
	}
	_, err = archive.Put(manifest, bytes.NewReader(raw))
	return err
}

type realCoordArchive struct{ a agentd.CoordArchive }

func (r realCoordArchive) Get(_ context.Context, q protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	return r.a.GetSelected(q)
}

func (r realCoordArchive) GetItem(_ context.Context, q protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	return r.a.GetItemSelected(q)
}

func (r realCoordArchive) List(_ context.Context, q protocol.CoordArchiveListQuery) (protocol.CoordArchiveListResult, error) {
	return r.a.List(q)
}
