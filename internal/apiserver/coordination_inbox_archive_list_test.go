package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/shelf"
)

type inboxArchiveListFake struct {
	list   protocol.CoordArchiveListResult
	calls  int
	query  protocol.CoordArchiveListQuery
	err    error
	before func()
}

func (*inboxArchiveListFake) Get(context.Context, protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	return protocol.CoordArchiveGetResult{}, errors.New("unexpected inbox archive get")
}
func (f *inboxArchiveListFake) List(_ context.Context, q protocol.CoordArchiveListQuery) (protocol.CoordArchiveListResult, error) {
	f.calls++
	f.query = q
	if f.before != nil {
		f.before()
	}
	return f.list, f.err
}

func archivedInboxListItem(t *testing.T, m *coord.InboxMessage) protocol.CoordArchiveListItem {
	t.Helper()
	m.TypeMeta = metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "InboxMessage"}
	full, err := protocol.CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(full)
	var projected map[string]any
	if json.Unmarshal(full, &projected) != nil {
		t.Fatal("fixture")
	}
	delete(projected["spec"].(map[string]any), "text")
	object, err := protocol.CanonicalJSON(projected)
	if err != nil {
		t.Fatal(err)
	}
	manifest := protocol.CoordArchiveManifest{Format: protocol.CoordArchiveFormat, Resource: "inboxmessages", Kind: "InboxMessage", APIVersion: protocol.CoordArchiveAPIVersion, Namespace: m.Namespace, Name: m.Name, UID: string(m.UID), ResourceVersion: m.ResourceVersion, Project: m.Spec.Project, RecipientUID: m.Spec.Recipient.UID, Object: protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(full))}, ArchivedBy: "fixture"}
	return protocol.CoordArchiveListItem{UID: string(m.UID), Manifest: &protocol.CoordArchiveStoredManifest{CoordArchiveManifest: manifest, ArchivedAt: time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC)}, Object: object}
}

func TestInboxArchiveListDefaultOffMetadataAndRecipientBinding(t *testing.T) {
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	sender, recipient := claimSession(t, f, "archive-sender", humanSA), claimSession(t, f, "archive-recipient", clientSA)
	w := f.do(http.MethodPost, apiv1.InboxPath, sender, inboxRequest(f, "archive-recipient", "archive-list"))
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	created := decode[apiv1.InboxResult](t, w).Message
	m, err := f.srv.Inbox.Get(context.Background(), created.Name)
	if err != nil {
		t.Fatal(err)
	}
	archive := &inboxArchiveListFake{list: protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{archivedInboxListItem(t, m)}}}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(2)
	path := apiv1.InboxPath + "?project=sample&limit=1"
	if w = f.do(http.MethodGet, path, recipient, nil); w.Code != http.StatusOK || archive.calls != 0 {
		t.Fatalf("live/default = %d calls=%d", w.Code, archive.calls)
	}
	w = f.do(http.MethodGet, path+"&archived=only", recipient, nil)
	page := decode[apiv1.InboxList](t, w)
	if w.Code != http.StatusOK || archive.calls != 1 || archive.query.Resource != "inboxmessages" || archive.query.RecipientUID != m.Spec.Recipient.UID || len(page.Items) != 1 || !page.Items[0].Archived || page.Items[0].ArchivedAt == nil || page.Items[0].Sender != created.Sender || page.Items[0].Recipient != created.Recipient || strings.Contains(w.Body.String(), "PRIVATE_INBOX_EXACT_TEXT") {
		t.Fatalf("archived page = %d calls=%d query=%#v body=%s", w.Code, archive.calls, archive.query, w.Body.String())
	}
	if w = f.do(http.MethodGet, apiv1.InboxPath+"?project=sample&archived=only&limit=101", recipient, nil); w.Code != http.StatusBadRequest || archive.calls != 1 {
		t.Fatalf("limit = %d calls=%d", w.Code, archive.calls)
	}
	item := archivedInboxListItem(t, m)
	item.Manifest.RecipientUID = "replacement-recipient"
	if _, _, err := f.srv.archivedInboxMetadata(*item.Manifest, item.Object); err == nil {
		t.Fatal("recipient manifest mismatch accepted")
	}
}

func inboxArchiveListRig(t *testing.T) (*fixture, string, *coord.InboxMessage, *inboxArchiveListFake) {
	t.Helper()
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	sender := claimSession(t, f, "archive-sender", humanSA)
	recipient := claimSession(t, f, "archive-recipient", clientSA)
	w := f.do(http.MethodPost, apiv1.InboxPath, sender, inboxRequest(f, "archive-recipient", "archive-list"))
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	m, err := f.srv.Inbox.Get(context.Background(), decode[apiv1.InboxResult](t, w).Message.Name)
	if err != nil {
		t.Fatal(err)
	}
	a := &inboxArchiveListFake{list: protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{archivedInboxListItem(t, m)}}}
	f.srv.CoordinationArchiveReads = true
	f.srv.CoordinationArchive = a
	f.srv.ShelfSlots = shelf.NewExecSlots(2)
	return f, recipient, m, a
}

func TestInboxArchiveListLimitsACLAndCallerFence(t *testing.T) {
	f, recipient, _, a := inboxArchiveListRig(t)
	path := apiv1.InboxPath + "?project=sample&archived=only"
	f.srv.CoordinationArchiveReads = false
	off := f.do(http.MethodGet, path, recipient, nil)
	if off.Code != 404 || a.calls != 0 {
		t.Fatalf("default off = %d calls=%d", off.Code, a.calls)
	}
	f.srv.CoordinationArchiveReads = true
	f.srv.InboxCoordinatorReadProjects = []string{"sample"}
	for _, tc := range []struct {
		token, suffix string
		limit         int
		recipient     bool
	}{{recipient, "", 100, true}, {recipient, "&limit=1", 1, true}, {recipient, "&limit=100", 100, true}, {tokCoordinator, "", 100, false}} {
		w := f.do(http.MethodGet, path+tc.suffix, tc.token, nil)
		if w.Code != 200 || a.query.Limit != tc.limit || (a.query.RecipientUID != "") != tc.recipient {
			t.Fatalf("scope/page = %d %#v %s", w.Code, a.query, w.Body.String())
		}
	}
	for _, suffix := range []string{"&limit=%zz", "&limit=1;bad", "&limit=0", "&limit=101", "&limit=bad", "&limit=1&limit=1", "&archived=only", "&unexpected=x", "&continue=live-cursor"} {
		before := a.calls
		w := f.do(http.MethodGet, path+suffix, recipient, nil)
		if w.Code != 400 || a.calls != before {
			t.Fatalf("query %s = %d calls=%d", suffix, w.Code, a.calls)
		}
	}
	for _, tc := range []struct {
		name   string
		change func()
		token  string
	}{
		{"coordinator-without-scope", func() { f.srv.InboxCoordinatorReadProjects = nil }, tokCoordinator},
		{"foreign-project", func() { f.srv.InboxProjects = []string{"other"} }, recipient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := a.calls
			tc.change()
			w := f.do(http.MethodGet, path, tc.token, nil)
			if w.Code != 404 || a.calls != before {
				t.Fatalf("scope = %d calls=%d", w.Code, a.calls)
			}
		})
	}
	for _, mode := range []string{"deleting-original", "replacement-before", "replacement-after"} {
		t.Run(mode, func(t *testing.T) {
			f, reader, _, a := inboxArchiveListRig(t)
			change := func() {
				s := f.session("archive-recipient")
				s.UID = "replacement-session"
				if err := f.c.Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			}
			want, calls := 404, 0
			switch mode {
			case "deleting-original":
				s := f.session("archive-recipient")
				s.Finalizers = []string{"fixture-retain"}
				if err := f.c.Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
				if err := f.c.Delete(context.Background(), s); err != nil {
					t.Fatal(err)
				}
				want, calls = 200, 1
			case "replacement-before":
				change()
			case "replacement-after":
				a.before = change
				calls = 1
			}
			w := f.do(http.MethodGet, path, reader, nil)
			if w.Code != want || a.calls != calls {
				t.Fatalf("%s = %d calls=%d %s", mode, w.Code, a.calls, w.Body.String())
			}
		})
	}
}

func TestInboxArchiveListImmutableAttributionWithoutSenderJoin(t *testing.T) {
	f, reader, m, a := inboxArchiveListRig(t)
	at := metav1.NewTime(time.Date(2026, 10, 15, 17, 0, 0, 0, time.UTC))
	m.Spec.Provenance.ReportedAt = &at
	m.Spec.Provenance.ReportedHead = strings.Repeat("a", 40)
	a.list.Items = []protocol.CoordArchiveListItem{archivedInboxListItem(t, m)}
	removeInboxSession(t, f, "archive-sender")
	w := f.do(http.MethodGet, apiv1.InboxPath+"?project=sample&archived=only", reader, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	got := decode[apiv1.InboxList](t, w).Items[0]
	if !got.Archived || got.ArchivedAt == nil || !got.ArchivedAt.Equal(a.list.Items[0].Manifest.ArchivedAt) {
		t.Fatal("missing archive receipt")
	}
	want := inboxMetadata(m)
	wire, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(wire, &want); err != nil {
		t.Fatal(err)
	}
	got.Archived = false
	got.ArchivedAt = nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("immutable attribution changed: got=%#v want=%#v", got, want)
	}
	if strings.Contains(w.Body.String(), "PRIVATE_INBOX_EXACT_TEXT") || strings.Contains(w.Body.String(), `"text"`) {
		t.Fatal("text leaked")
	}
}

func TestInboxArchiveListMalformedResultsAndBounds(t *testing.T) {
	for _, mode := range []string{"nil-reader", "over-count", "bad-marker", "unknown-error", "missing-manifest", "namespace", "recipient-manifest", "uid", "revision", "project", "name", "spec", "zero-created", "timestamp", "noncanonical", "text", "failure", "cancelled", "busy", "envelope"} {
		t.Run(mode, func(t *testing.T) {
			f, reader, m, a := inboxArchiveListRig(t)
			item := archivedInboxListItem(t, m)
			want, calls := 503, 1
			switch mode {
			case "nil-reader":
				f.srv.CoordinationArchive = nil
				calls = 0
			case "over-count":
				a.list.Items = append(a.list.Items, item)
			case "bad-marker":
				item.Error = protocol.HistoryErrCorrupt
				a.list.Items = []protocol.CoordArchiveListItem{item}
			case "unknown-error":
				item.Error = "unknown"
				a.list.Items = []protocol.CoordArchiveListItem{item}
			case "missing-manifest":
				item.Manifest = nil
				a.list.Items = []protocol.CoordArchiveListItem{item}
			case "namespace":
				item.Manifest.Namespace = "other"
				a.list.Items = []protocol.CoordArchiveListItem{item}
			case "recipient-manifest":
				item.Manifest.RecipientUID = "replacement-recipient"
				a.list.Items = []protocol.CoordArchiveListItem{item}
			case "uid":
				item.UID = "different-uid"
				a.list.Items = []protocol.CoordArchiveListItem{item}
			case "revision":
				item.Manifest.ResourceVersion = "different-revision"
				a.list.Items = []protocol.CoordArchiveListItem{item}
			case "project":
				m.Spec.Project = "other"
				a.list.Items = []protocol.CoordArchiveListItem{archivedInboxListItem(t, m)}
			case "name":
				m.Name = inbox.MessageName(m.Spec.Sender, "different-key")
				a.list.Items = []protocol.CoordArchiveListItem{archivedInboxListItem(t, m)}
			case "spec":
				m.Spec.RequestedModel = ""
				a.list.Items = []protocol.CoordArchiveListItem{archivedInboxListItem(t, m)}
			case "zero-created":
				m.CreationTimestamp = metav1.Time{}
				a.list.Items = []protocol.CoordArchiveListItem{archivedInboxListItem(t, m)}
			case "timestamp":
				item.Manifest.ArchivedAt = time.Time{}
				a.list.Items = []protocol.CoordArchiveListItem{item}
			case "noncanonical":
				item.Object = append([]byte(" "), item.Object...)
				a.list.Items = []protocol.CoordArchiveListItem{item}
			case "text":
				var obj map[string]any
				if json.Unmarshal(item.Object, &obj) != nil {
					t.Fatal("fixture")
				}
				obj["spec"].(map[string]any)["text"] = "must-not-leak"
				item.Object, _ = protocol.CanonicalJSON(obj)
				a.list.Items = []protocol.CoordArchiveListItem{item}
			case "failure":
				a.err = errors.New("unavailable")
			case "cancelled":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				req := httptest.NewRequest(http.MethodGet, apiv1.InboxPath+"?project=sample&archived=only&limit=1", nil).WithContext(ctx)
				req.Header.Set("Authorization", "Bearer "+reader)
				w := httptest.NewRecorder()
				f.h.ServeHTTP(w, req)
				if w.Code != 503 || a.calls != 0 {
					t.Fatalf("cancel = %d calls=%d", w.Code, a.calls)
				}
				assertContextArchiveSlots(t, f.srv.ShelfSlots)
				return
			case "busy":
				for range 2 {
					release, ok := f.srv.ShelfSlots.TryTake()
					if !ok {
						t.Fatal("fixture could not take slots")
					}
					defer release()
				}
				calls = 0
			case "envelope":
				a.list.Items = nil
				for i := range 20 {
					copy := m.DeepCopy()
					copy.UID = types.UID(fmt.Sprintf("%08d-1111-2222-3333-444444444444", i))
					row := archivedInboxListItem(t, copy)
					var obj map[string]any
					if json.Unmarshal(row.Object, &obj) != nil {
						t.Fatal("fixture")
					}
					obj["padding"] = strings.Repeat("x", 220<<10)
					row.Object, _ = protocol.CanonicalJSON(obj)
					if _, _, err := f.srv.archivedInboxMetadata(*row.Manifest, row.Object); err != nil {
						t.Fatalf("row invalid: %v", err)
					}
					a.list.Items = append(a.list.Items, row)
				}
			}
			a.list.Items = append([]protocol.CoordArchiveListItem(nil), a.list.Items...)
			limit := 1
			if mode == "envelope" {
				limit = 100
			}
			var w *httptest.ResponseRecorder
			if mode == "busy" {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
				defer cancel()
				req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("%s?project=sample&archived=only&limit=%d", apiv1.InboxPath, limit), nil).WithContext(ctx)
				req.Header.Set("Authorization", "Bearer "+reader)
				w = httptest.NewRecorder()
				f.h.ServeHTTP(w, req)
			} else {
				w = f.do(http.MethodGet, fmt.Sprintf("%s?project=sample&archived=only&limit=%d", apiv1.InboxPath, limit), reader, nil)
			}
			if w.Code != want || a.calls != calls || strings.Contains(w.Body.String(), "must-not-leak") {
				t.Fatalf("%s = %d calls=%d %s", mode, w.Code, a.calls, w.Body.String())
			}
			if mode != "busy" {
				assertContextArchiveSlots(t, f.srv.ShelfSlots)
			}
		})
	}
}

func TestInboxArchiveListCursorBindingProgressAndCorruptContinuation(t *testing.T) {
	f, reader, m, a := inboxArchiveListRig(t)
	q := protocol.CoordArchiveListQuery{Resource: "inboxmessages", Project: "sample", RecipientUID: m.Spec.Recipient.UID, Limit: 1}
	inner, err := q.Cursor("20261015T180000.000000000Z-" + string(m.UID) + ".json")
	if err != nil {
		t.Fatal(err)
	}
	scope := contextArchiveListCursor{Route: apiv1.InboxPath, Project: "sample", Recipient: m.Spec.Recipient.UID, Limit: 1}
	for _, mode := range []string{"route", "project", "recipient", "limit", "inner"} {
		t.Run(mode, func(t *testing.T) {
			bad := scope
			position := inner
			switch mode {
			case "route":
				bad.Route = apiv1.TaskClaimsPath
			case "project":
				bad.Project = "other"
			case "recipient":
				bad.Recipient = "other-recipient"
			case "limit":
				bad.Limit = 2
			case "inner":
				position = "invalid-inner"
			}
			outer, err := encodeContextArchiveListCursor(position, bad)
			if err != nil {
				t.Fatal(err)
			}
			before := a.calls
			w := f.do(http.MethodGet, apiv1.InboxPath+"?project=sample&archived=only&limit=1&continue="+outer, reader, nil)
			if w.Code != 400 || a.calls != before {
				t.Fatalf("foreign cursor = %d calls=%d", w.Code, a.calls)
			}
		})
	}
	outer, err := encodeContextArchiveListCursor(inner, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"same", "newer", "older"} {
		t.Run(mode, func(t *testing.T) {
			position := inner
			want := 503
			if mode != "same" {
				at := "20261015T190000.000000000Z-"
				if mode == "older" {
					at = "20261015T170000.000000000Z-"
					want = 200
				}
				position, err = q.Cursor(at + string(m.UID) + ".json")
				if err != nil {
					t.Fatal(err)
				}
			}
			a.list = protocol.CoordArchiveListResult{Next: position}
			w := f.do(http.MethodGet, apiv1.InboxPath+"?project=sample&archived=only&limit=1&continue="+outer, reader, nil)
			if w.Code != want {
				t.Fatalf("progress = %d %s", w.Code, w.Body.String())
			}
		})
	}
	a.list = protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{{UID: string(m.UID), Error: protocol.HistoryErrCorrupt}}, Next: inner}
	w := f.do(http.MethodGet, apiv1.InboxPath+"?project=sample&archived=only&limit=1", reader, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	page := decode[apiv1.InboxList](t, w)
	if len(page.Items) != 0 || page.Continue == "" {
		t.Fatal("corrupt row lost continuation")
	}
}

type inboxArchiveListExecutor struct {
	body      []byte
	calls     int
	recipient string
}

func (e *inboxArchiveListExecutor) Run(_ context.Context, ns, pod, container string, cmd []string, _ io.Reader, stdout, _ io.Writer) error {
	e.calls++
	if ns != sessionNS || pod != "archive-shelf" || container != shelf.Container || len(cmd) < 8 || strings.Join(cmd[:6], " ") != "agentd ctl coord-archive list --resource inboxmessages" || !strings.Contains(strings.Join(cmd, " "), "--recipient-uid "+e.recipient) {
		return errors.New("unexpected synthetic inbox list route")
	}
	_, err := stdout.Write(e.body)
	return err
}
func TestInboxArchiveListTypedShelfProjection(t *testing.T) {
	f, reader, m, fake := inboxArchiveListRig(t)
	body, err := json.Marshal(fake.list)
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "archive-shelf", Labels: map[string]string{"app.kubernetes.io/name": shelf.AppName}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	if err := f.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	exec := &inboxArchiveListExecutor{body: body, recipient: m.Spec.Recipient.UID}
	f.srv.CoordinationArchive = &shelf.CoordinationArchive{Shelf: &shelf.Shelf{Reader: f.c, Exec: exec, Namespace: sessionNS}}
	w := f.do(http.MethodGet, apiv1.InboxPath+"?project=sample&limit=1&archived=only", reader, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	got := decode[apiv1.InboxList](t, w)
	if exec.calls != 1 || fake.calls != 0 || len(got.Items) != 1 || got.Items[0].UID != string(m.UID) || !got.Items[0].Archived || got.Items[0].ArchivedAt == nil {
		t.Fatalf("typed projection: %#v calls=%d", got, exec.calls)
	}
	assertContextArchiveSlots(t, f.srv.ShelfSlots)
}
