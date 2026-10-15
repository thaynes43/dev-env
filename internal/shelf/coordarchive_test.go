package shelf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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

	var projected map[string]any
	if err := json.Unmarshal(object, &projected); err != nil {
		t.Fatal(err)
	}
	projectedSpec := projected["spec"].(map[string]any)
	delete(projectedSpec, "text")
	projection, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	e.out = `{"items":[{"uid":"` + m.UID + `","manifest":` + string(mustJSON(t, stored)) + `,"object":` + string(projection) + `}]}`
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
	e.out, e.errOut, e.err = "", "agentd: usage: ctl status | rescue", utilexec.CodeExitError{Err: errors.New("exit"), Code: 2}
	_, err = c.List(context.Background(), protocol.CoordArchiveListQuery{Resource: m.Resource, Project: m.Project, Limit: 1})
	var refusal *protocol.HistoryError
	if !errors.As(err, &refusal) || refusal.Code != protocol.HistoryErrNoArchive {
		t.Fatalf("old shelf: %v", err)
	}
}

func TestCoordinationArchiveRefusesIncompleteAndNoncanonicalReplies(t *testing.T) {
	m, object := coordFixture(t)
	stored := protocol.CoordArchiveStoredManifest{CoordArchiveManifest: m, ArchivedAt: time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC)}
	e := &fakeExec{}
	c := coordClient(t, e)
	id := protocol.CoordArchiveIdentity{Resource: m.Resource, Project: m.Project, Name: m.Name, UID: m.UID}
	mark := []protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: id, Event: protocol.CoordArchiveEvent{Format: protocol.CoordArchiveEventFormat, State: "reset", At: stored.ArchivedAt, Rule: "owner-absent", By: "fixture"}}}

	for _, answer := range []string{"null", `{}`, `[{"resource":"taskclaims","project":"demo","name":"` + m.Name + `","uid":"` + m.UID + `"}]`} {
		e.out = answer
		if got, err := c.Lookup(context.Background(), []protocol.CoordArchiveIdentity{id}); err == nil || got != nil {
			t.Errorf("lookup %q = %#v, %v", answer, got, err)
		}
		if got, err := c.Mark(context.Background(), mark); err == nil || got != nil {
			t.Errorf("mark %q = %#v, %v", answer, got, err)
		}
	}
	for _, answer := range []string{"null", `{}`, `{"items":null}`} {
		e.out = answer
		if got, err := c.List(context.Background(), protocol.CoordArchiveListQuery{Resource: m.Resource, Project: m.Project, Limit: 1}); err == nil || got.Items != nil {
			t.Errorf("list %q = %#v, %v", answer, got, err)
		}
	}
	manifest, _ := json.Marshal(stored)
	e.out = `{"manifest":` + string(manifest) + `,"object": { "kind":"TaskClaim", "apiVersion":"coordination.dev-env.haynesops.com/v1alpha1", "metadata":{"uid":"` + m.UID + `","name":"` + m.Name + `","namespace":"dev-agents","resourceVersion":"42"},"spec":{"identity":{"project":"demo"}} }}`
	got, err := c.Get(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name, UID: m.UID, ResourceVersion: m.ResourceVersion, SHA256: m.Object.SHA256})
	if err == nil || got.Object != nil {
		t.Fatalf("noncanonical get = %#v, %v", got, err)
	}

	e.out = `{"items":[{"uid":"` + m.UID + `","error":"corrupt","manifest":` + string(manifest) + `}]}`
	if got, err := c.List(context.Background(), protocol.CoordArchiveListQuery{Resource: m.Resource, Project: m.Project, Limit: 1}); err == nil || got.Items != nil {
		t.Fatalf("corrupt item leaks = %#v, %v", got, err)
	}

	e.out, e.errOut = strings.Repeat("x", maxOutput+1), ""
	if _, err := c.Get(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name}); err == nil {
		t.Fatal("stdout overflow accepted")
	}
	e.out, e.errOut = `{"items":[]}`, strings.Repeat("x", 4097)
	if _, err := c.List(context.Background(), protocol.CoordArchiveListQuery{Resource: m.Resource, Project: m.Project, Limit: 1}); err == nil {
		t.Fatal("stderr overflow accepted")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := len(e.cmds)
	if _, err := c.List(ctx, protocol.CoordArchiveListQuery{Resource: m.Resource, Project: m.Project, Limit: 1}); !errors.Is(err, context.Canceled) || len(e.cmds) != before {
		t.Fatalf("cancelled call launched or succeeded: %v", err)
	}
	_ = object
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

// coordExecFunc inspects cancellation/deadlines without timers or subprocesses.
type coordExecFunc func(context.Context, string, string, string, []string, io.Reader, io.Writer, io.Writer) error

func (f coordExecFunc) Run(ctx context.Context, namespace, pod, container string, cmd []string, in io.Reader, out, stderr io.Writer) error {
	return f(ctx, namespace, pod, container, cmd, in, out, stderr)
}

func TestCoordinationArchiveRetryAndBatchContracts(t *testing.T) {
	m, object := coordFixture(t)
	at := time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC)
	stored := protocol.CoordArchiveStoredManifest{CoordArchiveManifest: m, ArchivedAt: at}
	stored.ArchivedBy = "first publisher"
	id := protocol.CoordArchiveIdentity{Resource: m.Resource, Project: m.Project, Name: m.Name, UID: m.UID}
	absent := protocol.CoordArchiveEvent{Format: protocol.CoordArchiveEventFormat, State: "absent", At: at, Rule: "owner-absent", By: "fixture", Sessions: []protocol.CoordArchiveObservation{{Role: "owner", Namespace: ns, Name: "owner", UID: m.UID, Observed: "not-found"}}}
	e := &fakeExec{out: string(mustJSON(t, protocol.CoordArchivePutResult{Manifest: stored, GraceStart: &at}))}
	c := coordClient(t, e)
	req := protocol.CoordArchivePutRequest{Manifest: m, Object: object, Event: &absent}
	got, err := c.Put(context.Background(), req, true)
	if err != nil || got.Manifest.ArchivedBy != "first publisher" || e.cmds[0] != "agentd ctl coord-archive put --mark" || e.stdin[0] != string(mustCanonical(t, req)) {
		t.Fatalf("marked retry: %#v %v", got, err)
	}
	req.Event = nil
	if got, err = c.Put(context.Background(), req, false); err != nil || got.GraceStart == nil {
		t.Fatalf("plain retry must preserve grace: %#v %v", got, err)
	}
	for _, grace := range []*time.Time{nil, &at} {
		answer := []protocol.CoordArchiveLookupResult{{CoordArchiveIdentity: id, Holds: true, Revisions: []protocol.CoordArchiveRevision{{ResourceVersion: m.ResourceVersion, SHA256: m.Object.SHA256}}, GraceStart: grace}}
		e.out = string(mustJSON(t, answer))
		ids := []protocol.CoordArchiveIdentity{id}
		if out, err := c.Lookup(context.Background(), ids); err != nil || !out[0].Holds {
			t.Fatalf("lookup valid grace: %#v %v", out, err)
		}
		if e.stdin[len(e.stdin)-1] != string(mustCanonical(t, ids)) {
			t.Fatal("lookup stdin differs")
		}
	}
	e.out = string(mustJSON(t, []protocol.CoordArchiveLookupResult{{CoordArchiveIdentity: id, Revisions: []protocol.CoordArchiveRevision{}}}))
	if out, err := c.Lookup(context.Background(), []protocol.CoordArchiveIdentity{id}); err != nil || out[0].Holds {
		t.Fatalf("lookup miss: %#v %v", out, err)
	}
	reset := absent
	reset.State = "reset"
	reset.Sessions = nil
	marks := []protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: id, Event: reset}}
	e.out = string(mustJSON(t, []protocol.CoordArchiveMarkResult{{CoordArchiveIdentity: id, Appended: false}}))
	if _, err := c.Mark(context.Background(), marks); err != nil || e.stdin[len(e.stdin)-1] != string(mustCanonical(t, marks)) {
		t.Fatalf("mark canonical stdin: %v", err)
	}
	for _, answer := range []string{"[]", `[{"resource":"taskclaims","project":"wrong","name":"` + m.Name + `","uid":"` + m.UID + `","appended":false,"graceStart":null}]`, `[{"resource":"taskclaims","project":"demo","name":"` + m.Name + `","uid":"` + m.UID + `","appended":null,"graceStart":null}]`} {
		e.out = answer
		if out, err := c.Mark(context.Background(), marks); err == nil || out != nil {
			t.Fatalf("bad mark accepted: %#v %v", out, err)
		}
	}
	for _, answer := range []string{"[]", `[{"resource":"taskclaims","project":"demo","name":"` + m.Name + `","uid":"` + m.UID + `","holds":false,"revisions":[{"resourceVersion":"42","sha256":"` + m.Object.SHA256 + `"}],"graceStart":null}]`, `[{"resource":"taskclaims","project":"demo","name":"` + m.Name + `","uid":"` + m.UID + `","holds":true,"revisions":[{"resourceVersion":"../x","sha256":"` + m.Object.SHA256 + `"}],"graceStart":null}]`} {
		e.out = answer
		if out, err := c.Lookup(context.Background(), []protocol.CoordArchiveIdentity{id}); err == nil || out != nil {
			t.Fatalf("bad lookup accepted: %#v %v", out, err)
		}
	}
	for _, answer := range []string{"null", `{"manifest":` + string(mustJSON(t, stored)) + `}`, `{"manifest":null,"graceStart":null}`} {
		e.out = answer
		if out, err := c.Put(context.Background(), req, false); err == nil || out.Manifest.UID != "" {
			t.Fatalf("incomplete put: %#v %v", out, err)
		}
	}
}

func TestCoordinationArchivePrelaunchAndGetPins(t *testing.T) {
	m, object := coordFixture(t)
	e := &fakeExec{}
	c := coordClient(t, e)
	cases := []struct {
		name string
		call func() error
	}{
		{"put", func() error {
			_, err := c.Put(context.Background(), protocol.CoordArchivePutRequest{}, false)
			return err
		}},
		{"put mark mismatch", func() error {
			_, err := c.Put(context.Background(), protocol.CoordArchivePutRequest{Manifest: m, Object: object}, true)
			return err
		}},
		{"mark", func() error { _, err := c.Mark(context.Background(), nil); return err }},
		{"mark batch", func() error {
			_, err := c.Mark(context.Background(), make([]protocol.CoordArchiveMarkRequest, protocol.MaxCoordArchiveLookupItems+1))
			return err
		}},
		{"lookup batch", func() error {
			_, err := c.Lookup(context.Background(), make([]protocol.CoordArchiveIdentity, protocol.MaxCoordArchiveLookupItems+1))
			return err
		}},
		{"get unpaired pins", func() error {
			_, err := c.Get(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name, ResourceVersion: "42"})
			return err
		}},
		{"list", func() error {
			_, err := c.List(context.Background(), protocol.CoordArchiveListQuery{Resource: m.Resource, Project: m.Project, Limit: 101})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(e.cmds)
			if err := tc.call(); err == nil || len(e.cmds) != before {
				t.Fatalf("invalid launched: %v", err)
			}
		})
	}
	stored := protocol.CoordArchiveStoredManifest{CoordArchiveManifest: m, ArchivedAt: time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC)}
	e.out = string(mustJSON(t, protocol.CoordArchiveGetResult{Manifest: stored, Object: object}))
	for _, uid := range []string{"", m.UID} {
		q := protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name, UID: uid}
		out, err := c.Get(context.Background(), q)
		sum := sha256.Sum256(out.Object)
		want := "agentd ctl coord-archive get --resource " + m.Resource + " --name " + m.Name
		if uid != "" {
			want += " --uid " + uid
		}
		if err != nil || hex.EncodeToString(sum[:]) != m.Object.SHA256 || e.cmds[len(e.cmds)-1] != want {
			t.Fatalf("optional get: %v", err)
		}
	}
	for _, pin := range []string{"resource", "name", "uid", "revision", "digest"} {
		q := protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name, UID: m.UID, ResourceVersion: m.ResourceVersion, SHA256: m.Object.SHA256}
		switch pin {
		case "resource":
			q.Resource = "publishedcontexts"
			q.Name = strings.Replace(m.Name, "task-claim.", "published-context.", 1)
		case "name":
			q.Name = strings.Replace(m.Name, "0123456789abcdef0123456789abcdef", "11111111111111111111111111111111", 1)
		case "uid":
			q.UID = "11111111-1111-4111-8111-111111111111"
		case "revision":
			q.ResourceVersion = "43"
		case "digest":
			q.SHA256 = strings.Repeat("0", 64)
		}
		if out, err := c.Get(context.Background(), q); err == nil || out.Object != nil {
			t.Fatalf("%s pin accepted: %v", pin, err)
		}
	}
}

func TestCoordinationArchiveTransportRefusalsAndDeadlines(t *testing.T) {
	m, _ := coordFixture(t)
	q := protocol.CoordArchiveListQuery{Resource: m.Resource, Project: m.Project, Limit: 1}
	for _, code := range []string{protocol.HistoryErrInvalid, protocol.HistoryErrNotFound, protocol.HistoryErrAmbiguous, protocol.HistoryErrConflict, protocol.HistoryErrCorrupt, protocol.HistoryErrNoArchive} {
		e := &fakeExec{out: `{"error":"` + code + `","message":"fixture"}`, err: coordExit(1)}
		out, err := coordClient(t, e).List(context.Background(), q)
		var refusal *protocol.HistoryError
		if !errors.As(err, &refusal) || refusal.Code != code || out.Items != nil {
			t.Fatalf("refusal %s: %v", code, err)
		}
	}
	for _, tc := range []struct {
		out, stderr string
		err         error
	}{
		{`{"error":"unknown","message":"x"}`, "", coordExit(1)},
		{`{"error":"corrupt"}`, "", coordExit(1)},
		{`{"error":"corrupt","message":""}`, "", coordExit(1)},
		{`{"error":"corrupt","message":"x"}`, "", coordExit(2)},
		{`{"items":[]}`, "unknown command", errors.New("network")},
		{`{"items":[]}`, "agentd: usage: ctl status | rescue", errors.New("network")},
		{`{"items":[]}`, "unknown command", context.DeadlineExceeded},
		{`{"items":[]}`, "unrelated usage", coordExit(2)},
	} {
		e := &fakeExec{out: tc.out, errOut: tc.stderr, err: tc.err}
		out, err := coordClient(t, e).List(context.Background(), q)
		var refusal *protocol.HistoryError
		if err == nil || errors.As(err, &refusal) || out.Items != nil {
			t.Fatalf("transport became refusal/success: %v", err)
		}
	}
	for _, timeout := range []time.Duration{0, 2 * time.Minute, 15 * time.Second} {
		c := coordClient(t, &fakeExec{})
		c.Shelf.Timeout = timeout
		c.Shelf.Exec = coordExecFunc(func(ctx context.Context, _, _, _ string, _ []string, _ io.Reader, out, stderr io.Writer) error {
			deadline, ok := ctx.Deadline()
			cap := time.Minute
			if timeout > 0 && timeout < cap {
				cap = timeout
			}
			if !ok || time.Until(deadline) > cap || time.Until(deadline) <= 0 {
				t.Fatal("wrong exec deadline")
			}
			_, _ = io.WriteString(out, `{"items":[]}`)
			return nil
		})
		if _, err := c.List(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := coordClient(t, &fakeExec{})
	c.Shelf.Exec = coordExecFunc(func(_ context.Context, _, _, _ string, _ []string, _ io.Reader, out, stderr io.Writer) error {
		cancel()
		_, _ = io.WriteString(out, `{"items":[]}`)
		return nil
	})
	if out, err := c.List(ctx, q); !errors.Is(err, context.Canceled) || out.Items != nil {
		t.Fatalf("late success after cancellation: %v", err)
	}
	parent, cancelParent := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelParent()
	want, _ := parent.Deadline()
	c.Shelf.Exec = coordExecFunc(func(ctx context.Context, _, _, _ string, _ []string, _ io.Reader, out, stderr io.Writer) error {
		got, _ := ctx.Deadline()
		if !got.Equal(want) {
			t.Fatal("parent deadline lost")
		}
		_, _ = io.WriteString(out, `{"items":[]}`)
		return nil
	})
	if _, err := c.List(parent, q); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinationArchiveListScopesAndProjection(t *testing.T) {
	m, object := coordFixture(t)
	stored := protocol.CoordArchiveStoredManifest{CoordArchiveManifest: m, ArchivedAt: time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC)}
	e := &fakeExec{out: `{"items":[]}`}
	c := coordClient(t, e)
	q := protocol.CoordArchiveListQuery{Resource: m.Resource, Project: m.Project, Limit: 1}
	if out, err := c.List(context.Background(), q); err != nil || out.Items == nil || len(out.Items) != 0 {
		t.Fatalf("empty page: %#v %v", out, err)
	}
	cursor, err := q.Cursor("20261015T010203.000000000Z-" + m.UID + ".json")
	if err != nil {
		t.Fatal(err)
	}
	q.After = cursor
	e.out = string(mustJSON(t, protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{{UID: m.UID, Error: "corrupt"}}, Next: cursor}))
	if _, err := c.List(context.Background(), q); err != nil || !strings.HasSuffix(e.cmds[len(e.cmds)-1], " --after "+cursor) {
		t.Fatalf("cursor/corrupt page: %v", err)
	}
	item := protocol.CoordArchiveListItem{UID: m.UID, Manifest: &stored, Object: object}
	for _, mutate := range []func(*protocol.CoordArchiveListItem){
		func(i *protocol.CoordArchiveListItem) { i.Object = nil }, func(i *protocol.CoordArchiveListItem) { i.Object = []byte("null") }, func(i *protocol.CoordArchiveListItem) { i.UID = "invalid" },
		func(i *protocol.CoordArchiveListItem) {
			copy := *i.Manifest
			copy.Project = "other"
			i.Manifest = &copy
		},
		func(i *protocol.CoordArchiveListItem) {
			i.Object = bytes.Replace(i.Object, []byte(`"spec":{`), []byte(`"spec":{"text":"secret",`), 1)
		},
		func(i *protocol.CoordArchiveListItem) { i.Error = "unknown" },
	} {
		bad := item
		mutate(&bad)
		e.out = string(mustJSON(t, protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{bad}}))
		if out, err := c.List(context.Background(), q); err == nil || out.Items != nil {
			t.Fatalf("bad projection: %#v %v", out, err)
		}
	}
	e.out = string(mustJSON(t, protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{}, Next: "bad"}))
	if out, err := c.List(context.Background(), q); err == nil || out.Items != nil {
		t.Fatalf("bad cursor: %#v %v", out, err)
	}
	// A real inbox projection binds both recipient and metadata; its digest remains
	// the full object's digest, and must not be recomputed from this projection.
	var obj map[string]any
	if err := json.Unmarshal(object, &obj); err != nil {
		t.Fatal(err)
	}
	inbox := m
	inbox.Resource = "inboxmessages"
	inbox.Kind = "InboxMessage"
	inbox.Name = strings.Replace(m.Name, "task-claim.", "message.", 1)
	inbox.RecipientUID = m.UID
	obj["kind"] = inbox.Kind
	obj["metadata"].(map[string]any)["name"] = inbox.Name
	obj["spec"] = map[string]any{"project": m.Project, "recipient": map[string]any{"uid": m.UID}}
	projection := mustCanonical(t, obj)
	stored = protocol.CoordArchiveStoredManifest{CoordArchiveManifest: inbox, ArchivedAt: stored.ArchivedAt}
	iq := protocol.CoordArchiveListQuery{Resource: inbox.Resource, Project: inbox.Project, RecipientUID: inbox.RecipientUID, Limit: 1}
	cursor, err = iq.Cursor("20261015T010203.000000000Z-" + m.UID + ".json")
	if err != nil {
		t.Fatal(err)
	}
	iq.After = cursor
	e.out = string(mustJSON(t, protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{{UID: inbox.UID, Manifest: &stored, Object: projection}}}))
	if _, err := c.List(context.Background(), iq); err != nil || !strings.Contains(e.cmds[len(e.cmds)-1], " --recipient-uid "+m.UID+" --after "+cursor) {
		t.Fatalf("inbox projection: %v", err)
	}
	iq.RecipientUID = "11111111-1111-4111-8111-111111111111"
	iq.After = ""
	if out, err := c.List(context.Background(), iq); err == nil || out.Items != nil {
		t.Fatalf("recipient mismatch: %#v %v", out, err)
	}
}

func coordExit(code int) error {
	return utilexec.CodeExitError{Err: errors.New("command terminated"), Code: code}
}
