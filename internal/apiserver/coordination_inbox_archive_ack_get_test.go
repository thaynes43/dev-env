package apiserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/shelf"
)

type inboxArchiveAckGetFake struct {
	result protocol.CoordArchiveGetResult
	calls  int
}

func (f *inboxArchiveAckGetFake) Get(_ context.Context, q protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	f.calls++
	if q.Resource != "inboxreadacks" || q.Name == "" || q.UID != "" || q.ResourceVersion != "" || q.SHA256 != "" {
		return protocol.CoordArchiveGetResult{}, errors.New("unexpected inbox ack archive query")
	}
	return f.result, nil
}

func archivedInboxAckGetResult(t *testing.T, a *coord.InboxReadAck) protocol.CoordArchiveGetResult {
	t.Helper()
	a.TypeMeta = metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "InboxReadAck"}
	raw, err := protocol.CanonicalJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	manifest := protocol.CoordArchiveManifest{Format: protocol.CoordArchiveFormat, Resource: "inboxreadacks", Kind: "InboxReadAck", APIVersion: protocol.CoordArchiveAPIVersion, Namespace: a.Namespace, Name: a.Name, UID: string(a.UID), ResourceVersion: a.ResourceVersion, Project: a.Spec.Project, RecipientUID: a.Spec.Recipient.UID, MessageUID: a.Spec.MessageUID, Object: protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(raw))}, ArchivedBy: "fixture"}
	if _, err := protocol.VerifyCoordArchiveObject(manifest, bytes.NewReader(raw)); err != nil {
		t.Fatalf("fixture full ack invalid: %v", err)
	}
	return protocol.CoordArchiveGetResult{Manifest: protocol.CoordArchiveStoredManifest{CoordArchiveManifest: manifest, ArchivedAt: time.Date(2026, 10, 15, 19, 0, 0, 0, time.UTC)}, Object: raw}
}

// This compiles on the accepted base because it uses raw HTTP JSON rather than
// the additive archive fields. Before the production fallback is wired it is a
// real RED: the live read-ack NotFound returns 404 despite this valid archive.
func TestInboxArchiveAckGetRedAcceptedBase(t *testing.T) {
	f, _, recipient, m, _, messagePath := inboxArchiveGetRig(t)
	ackPath := messagePath[:len(messagePath)-len("?project=sample")] + "/ack?project=sample"
	if w := f.do(http.MethodPost, ackPath, recipient, bytes.NewBufferString(`{}`)); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	a, err := f.srv.Inbox.ReadAck(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	archive := &inboxArchiveAckGetFake{result: archivedInboxAckGetResult(t, a)}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(1)
	w := f.do(http.MethodGet, ackPath, recipient, nil)
	if w.Code != http.StatusOK || archive.calls != 1 {
		t.Fatalf("accepted-base archive ack fallback = %d calls=%d body=%s", w.Code, archive.calls, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["name"] != inbox.AckName(string(m.UID), m.Spec.Recipient.UID) || got["archived"] != true || got["archivedAt"] != archive.result.Manifest.ArchivedAt.Format(time.RFC3339) {
		t.Fatalf("archived wire receipt = %#v", got)
	}
}

func TestInboxArchiveAckGetDefaultOffLiveWinsAndPostStaysLiveOnly(t *testing.T) {
	f, _, recipient, m, _, messagePath := inboxArchiveGetRig(t)
	ackPath := messagePath[:len(messagePath)-len("?project=sample")] + "/ack?project=sample"
	if w := f.do(http.MethodPost, ackPath, recipient, bytes.NewBufferString(`{}`)); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	a, err := f.srv.Inbox.ReadAck(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	archive := &inboxArchiveAckGetFake{result: archivedInboxAckGetResult(t, a)}
	f.srv.CoordinationArchive, f.srv.ShelfSlots = archive, shelf.NewExecSlots(1)
	if err := f.c.Delete(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodGet, ackPath, recipient, nil); w.Code != http.StatusNotFound || archive.calls != 0 {
		t.Fatalf("default off = %d calls=%d body=%s", w.Code, archive.calls, w.Body.String())
	}
	if w := f.do(http.MethodPost, ackPath, recipient, bytes.NewBufferString(`{}`)); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	f.srv.CoordinationArchiveReads = true
	if w := f.do(http.MethodGet, ackPath, recipient, nil); w.Code != http.StatusOK || archive.calls != 0 {
		t.Fatalf("live ack must win = %d calls=%d body=%s", w.Code, archive.calls, w.Body.String())
	} else {
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if _, archived := got["archived"]; archived {
			t.Fatalf("live ack marked archived: %#v", got)
		}
	}
	if err := f.c.Delete(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodPost, ackPath, recipient, bytes.NewBufferString(`{}`)); w.Code != http.StatusNotFound || archive.calls != 0 {
		t.Fatalf("POST must remain live-only = %d calls=%d body=%s", w.Code, archive.calls, w.Body.String())
	}
}
