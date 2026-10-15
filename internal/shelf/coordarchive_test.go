package shelf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func coordClient(t *testing.T, e *fakeExec) *CoordinationArchive {
	t.Helper()
	c := newClient(t, shelfPod("shelf-1", time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), "Running", true))
	return &CoordinationArchive{Shelf: &Shelf{Reader: c, Exec: e, Namespace: ns}}
}

func coordFixture(t *testing.T) (protocol.CoordArchiveManifest, []byte) {
	t.Helper()
	uid := "6f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f"
	name := "task-claim.0123456789abcdef0123456789abcdef.abcdef0123456789abcdef0123456789"
	raw := []byte(`{"apiVersion":"coordination.dev-env.haynesops.com/v1alpha1","kind":"TaskClaim","metadata":{"namespace":"dev-agents","name":"` + name + `","uid":"` + uid + `","resourceVersion":"42"},"spec":{"identity":{"project":"demo"}}}`)
	canonical, err := protocol.CanonicalJSON(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	return protocol.CoordArchiveManifest{Format: protocol.CoordArchiveFormat, Resource: "taskclaims", Kind: "TaskClaim", APIVersion: protocol.CoordArchiveAPIVersion, Namespace: ns, Name: name, UID: uid, ResourceVersion: "42", Project: "demo", Object: protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(canonical))}, ArchivedBy: "operator fixture"}, canonical
}

func TestCoordinationArchiveRunsTypedCommandsAndVerifiesGet(t *testing.T) {
	m, object := coordFixture(t)
	stored := protocol.CoordArchiveStoredManifest{CoordArchiveManifest: m, ArchivedAt: time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC)}
	putAnswer, _ := json.Marshal(protocol.CoordArchivePutResult{Manifest: stored})
	e := &fakeExec{out: string(putAnswer)}
	c := coordClient(t, e)
	put, err := c.Put(context.Background(), protocol.CoordArchivePutRequest{Manifest: m, Object: object}, false)
	if err != nil || put.Manifest != stored || e.cmds[0] != "agentd ctl coord-archive put" {
		t.Fatalf("put = %#v, %v, %q", put, err, e.cmds)
	}
	if !bytes.Equal([]byte(e.stdin[0]), mustCanonical(t, protocol.CoordArchivePutRequest{Manifest: m, Object: object})) {
		t.Fatal("put stdin is not canonical request")
	}

	e.out = `[{"resource":"taskclaims","project":"demo","name":"` + m.Name + `","uid":"` + m.UID + `","holds":true,"revisions":[{"resourceVersion":"42","sha256":"` + m.Object.SHA256 + `"}],"graceStart":null}]`
	lookup, err := c.Lookup(context.Background(), []protocol.CoordArchiveIdentity{{Resource: m.Resource, Project: m.Project, Name: m.Name, UID: m.UID}})
	if err != nil || len(lookup) != 1 || e.cmds[1] != "agentd ctl coord-archive lookup" {
		t.Fatalf("lookup = %#v, %v, %q", lookup, err, e.cmds)
	}

	e.out = `[{"resource":"taskclaims","project":"demo","name":"` + m.Name + `","uid":"` + m.UID + `","appended":true,"graceStart":null}]`
	event := protocol.CoordArchiveEvent{Format: protocol.CoordArchiveEventFormat, State: "reset", At: time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC), Rule: "owner-absent", By: "fixture"}
	if _, err := c.Mark(context.Background(), []protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: protocol.CoordArchiveIdentity{Resource: m.Resource, Project: m.Project, Name: m.Name, UID: m.UID}, Event: event}}); err != nil || e.cmds[2] != "agentd ctl coord-archive mark" {
		t.Fatalf("mark: %v, %q", err, e.cmds)
	}

	getAnswer, _ := json.Marshal(protocol.CoordArchiveGetResult{Manifest: stored, Object: object})
	e.out = string(getAnswer)
	got, err := c.Get(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name, UID: m.UID, ResourceVersion: m.ResourceVersion, SHA256: m.Object.SHA256})
	if err != nil || !bytes.Equal(got.Object, object) || e.cmds[3] != "agentd ctl coord-archive get --resource taskclaims --name "+m.Name+" --uid "+m.UID+" --resource-version 42 --sha256 "+m.Object.SHA256 {
		t.Fatalf("get: %#v, %v, %q", got, err, e.cmds)
	}

	e.out = `{"items":[{"uid":"` + m.UID + `","manifest":` + string(mustJSON(t, stored)) + `,"object":{"redacted":true}}]}`
	list, err := c.List(context.Background(), protocol.CoordArchiveListQuery{Resource: m.Resource, Project: m.Project, Limit: 1})
	if err != nil || len(list.Items) != 1 || e.cmds[4] != "agentd ctl coord-archive list --resource taskclaims --project demo --limit 1" {
		t.Fatalf("list: %#v, %v, %q", list, err, e.cmds)
	}
}

func TestCoordinationArchiveRefusesInvalidAndUnsafeAnswers(t *testing.T) {
	m, object := coordFixture(t)
	e := &fakeExec{}
	c := coordClient(t, e)
	if _, err := c.Get(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource}); err == nil || len(e.cmds) != 0 {
		t.Fatalf("invalid get launched: %v, %#v", err, e.cmds)
	}
	if _, err := c.Lookup(context.Background(), nil); err == nil || len(e.cmds) != 0 {
		t.Fatalf("invalid lookup launched: %v, %#v", err, e.cmds)
	}
	stored := protocol.CoordArchiveStoredManifest{CoordArchiveManifest: m, ArchivedAt: time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC)}
	bad := m
	bad.Object.SHA256 = strings.Repeat("0", 64)
	answer, _ := json.Marshal(protocol.CoordArchiveGetResult{Manifest: protocol.CoordArchiveStoredManifest{CoordArchiveManifest: bad, ArchivedAt: stored.ArchivedAt}, Object: object})
	e.out = string(answer)
	got, err := c.Get(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name})
	if err == nil || got.Object != nil {
		t.Fatalf("mismatched get returned %#v, %v", got, err)
	}
	for _, out := range []string{"null", `{}` + `{}`, `{"manifest":null,"object":null}`} {
		e.out = out
		got, err = c.Get(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name})
		if err == nil || got.Object != nil {
			t.Errorf("%q returned %#v, %v", out, got, err)
		}
	}
	e.out, e.errOut, e.err = "", "unknown command coord-archive", utilexec.CodeExitError{Err: errors.New("exit"), Code: 2}
	_, err = c.List(context.Background(), protocol.CoordArchiveListQuery{Resource: m.Resource, Project: m.Project, Limit: 1})
	var refusal *protocol.HistoryError
	if !errors.As(err, &refusal) || refusal.Code != protocol.HistoryErrNoArchive {
		t.Fatalf("old shelf: %v", err)
	}
}

func mustCanonical(t *testing.T, v any) []byte {
	t.Helper()
	b, err := protocol.CanonicalJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
