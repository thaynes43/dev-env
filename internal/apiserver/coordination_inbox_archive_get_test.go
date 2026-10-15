package apiserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/shelf"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type inboxArchiveGetFake struct {
	result protocol.CoordArchiveGetResult
	err    error
	calls  int
	after  func()
}

func (f *inboxArchiveGetFake) Get(_ context.Context, q protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	f.calls++
	if f.after != nil {
		f.after()
	}
	if q.Resource != "inboxmessages" || q.Name == "" || q.UID != "" || q.ResourceVersion != "" || q.SHA256 != "" {
		return protocol.CoordArchiveGetResult{}, errors.New("unexpected inbox archive query")
	}
	return f.result, f.err
}

func archivedInboxGetResult(t *testing.T, m *coord.InboxMessage) protocol.CoordArchiveGetResult {
	t.Helper()
	m.TypeMeta = metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "InboxMessage"}
	raw, err := protocol.CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	manifest := protocol.CoordArchiveManifest{Format: protocol.CoordArchiveFormat, Resource: "inboxmessages", Kind: "InboxMessage", APIVersion: protocol.CoordArchiveAPIVersion, Namespace: m.Namespace, Name: m.Name, UID: string(m.UID), ResourceVersion: m.ResourceVersion, Project: m.Spec.Project, RecipientUID: m.Spec.Recipient.UID, Object: protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(raw))}, ArchivedBy: "fixture"}
	if _, err := protocol.VerifyCoordArchiveObject(manifest, bytes.NewReader(raw)); err != nil {
		t.Fatalf("fixture full object invalid: %v", err)
	}
	return protocol.CoordArchiveGetResult{Manifest: protocol.CoordArchiveStoredManifest{CoordArchiveManifest: manifest, ArchivedAt: time.Date(2026, 10, 15, 19, 0, 0, 0, time.UTC)}, Object: raw}
}

func inboxArchiveGetRig(t *testing.T) (*fixture, string, string, *coord.InboxMessage, *inboxArchiveGetFake, string) {
	t.Helper()
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	sender, recipient := claimSession(t, f, "archive-sender", humanSA), claimSession(t, f, "archive-recipient", clientSA)
	w := f.do(http.MethodPost, apiv1.InboxPath, sender, inboxRequest(f, "archive-recipient", "archive-get"))
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	m, err := f.srv.Inbox.Get(context.Background(), decode[apiv1.InboxResult](t, w).Message.Name)
	if err != nil {
		t.Fatal(err)
	}
	a := &inboxArchiveGetFake{result: archivedInboxGetResult(t, m)}
	return f, sender, recipient, m, a, apiv1.InboxPath + "/" + m.Name + "?project=sample"
}

func TestInboxArchiveGetRedAcceptedBaseAndLiveWins(t *testing.T) {
	f, _, recipient, m, archive, path := inboxArchiveGetRig(t)
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(2)
	// This is the accepted-base counterexample: before the GET fallback is wired,
	// deleting the live object leaves a 404 despite this verified archive object.
	w := f.do(http.MethodGet, path, recipient, nil)
	if w.Code != http.StatusOK || archive.calls != 0 || decode[apiv1.InboxMessage](t, w).Text != m.Spec.Text {
		t.Fatalf("live message must win: status=%d calls=%d body=%s", w.Code, archive.calls, w.Body.String())
	}
	if err := f.c.Delete(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodGet, path, recipient, nil)
	got := decode[apiv1.InboxMessage](t, w)
	if w.Code != http.StatusOK || archive.calls != 1 || !got.Archived || got.ArchivedAt == nil || !got.ArchivedAt.Equal(archive.result.Manifest.ArchivedAt) || got.Text != m.Spec.Text {
		t.Fatalf("archive fallback = %d calls=%d body=%s", w.Code, archive.calls, w.Body.String())
	}
}

func TestInboxArchiveGetDefaultOffAclAndImmutableProjection(t *testing.T) {
	f, _, recipient, m, archive, path := inboxArchiveGetRig(t)
	if err := f.c.Delete(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	w := f.do(http.MethodGet, path, recipient, nil)
	if w.Code != http.StatusNotFound || archive.calls != 0 {
		t.Fatalf("default off = %d calls=%d", w.Code, archive.calls)
	}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(2)
	f.srv.InboxCoordinatorReadProjects = []string{"sample"}
	for _, token := range []string{tokCoordinator, recipient} {
		w = f.do(http.MethodGet, path, token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("authorized archived read = %d %s", w.Code, w.Body.String())
		}
	}
	got := decode[apiv1.InboxMessage](t, w)
	want := inboxMetadata(m)
	wire, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wire, &want); err != nil {
		t.Fatal(err)
	}
	got.Archived, got.ArchivedAt = false, nil
	if !reflect.DeepEqual(got.InboxMetadata, want) || got.Text != m.Spec.Text {
		t.Fatalf("immutable projection differs: got=%#v want=%#v", got, want)
	}
	f.srv.InboxCoordinatorReadProjects = nil
	before := archive.calls
	if w = f.do(http.MethodGet, path, tokCoordinator, nil); w.Code != http.StatusNotFound || archive.calls != before {
		t.Fatalf("unscoped coordinator = %d calls=%d", w.Code, archive.calls)
	}
}

func TestInboxArchiveGetRefusesOnlyAfterGenuineLiveNotFound(t *testing.T) {
	for _, mode := range []string{"live-error", "archive-missing", "corrupt", "wrong-recipient", "busy"} {
		t.Run(mode, func(t *testing.T) {
			f, _, recipient, m, archive, path := inboxArchiveGetRig(t)
			if mode != "live-error" {
				if err := f.c.Delete(context.Background(), m); err != nil {
					t.Fatal(err)
				}
			}
			f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(1)
			want, calls := http.StatusServiceUnavailable, 1
			switch mode {
			case "live-error":
				f.srv.Inbox.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
					return errors.New("live unavailable")
				}})
				want, calls = http.StatusServiceUnavailable, 0
			case "archive-missing":
				archive.err = protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "missing")
				want = http.StatusNotFound
			case "corrupt":
				archive.result.Object = []byte(`{}`)
			case "wrong-recipient":
				m.Spec.Recipient.UID = "11111111-1111-4111-8111-111111111111"
				archive.result = archivedInboxGetResult(t, m)
				want = http.StatusNotFound
			case "busy":
				release, ok := f.srv.ShelfSlots.TryTake()
				if !ok {
					t.Fatal("slot")
				}
				defer release()
				want, calls = http.StatusServiceUnavailable, 0
			}
			w := f.do(http.MethodGet, path, recipient, nil)
			if w.Code != want || archive.calls != calls || strings.Contains(w.Body.String(), "PRIVATE_INBOX_EXACT_TEXT") {
				t.Fatalf("%s = %d calls=%d body=%s", mode, w.Code, archive.calls, w.Body.String())
			}
		})
	}
}

func TestInboxArchiveGetDoesNotEnableAcknowledgementFallback(t *testing.T) {
	f, sender, recipient, m, archive, path := inboxArchiveGetRig(t)
	if err := f.c.Delete(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(2)
	ackPath := strings.Split(path, "?")[0] + "/ack?project=sample"
	if w := f.do(http.MethodGet, ackPath, recipient, nil); w.Code != http.StatusNotFound || archive.calls != 0 {
		t.Fatalf("ack GET = %d calls=%d", w.Code, archive.calls)
	}
	if w := f.do(http.MethodPost, ackPath, recipient, strings.NewReader(`{}`)); w.Code != http.StatusNotFound || archive.calls != 0 {
		t.Fatalf("ack POST = %d calls=%d", w.Code, archive.calls)
	}
	if w := f.do(http.MethodPost, apiv1.InboxPath, sender, inboxRequest(f, "archive-recipient", "archive-get-next")); w.Code != http.StatusCreated || archive.calls != 0 {
		t.Fatalf("publish = %d calls=%d", w.Code, archive.calls)
	}
}
