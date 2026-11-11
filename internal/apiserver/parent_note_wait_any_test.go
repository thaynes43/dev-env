package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/templates"
)

// waitAnyStamp is one second shared by notes whose order must come from
// storage, not from the clock or from their names.
var waitAnyStamp = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

func waitAnyName(i int) string {
	return fmt.Sprintf("message.%032x.%032x", i, 0)
}

func waitAnyParty(t *testing.T, f *fixture, name string) coord.SessionIdentity {
	t.Helper()
	return sessionIdentity(f.session(name))
}

func waitAnyRecipient(party coord.SessionIdentity) string {
	data, err := json.Marshal(party)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:63]
}

func waitAnyNote(to coord.SessionIdentity, name, kind, rv, text string, created time.Time) *coord.InboxMessage {
	return &coord.InboxMessage{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "dev-env-system", ResourceVersion: rv,
			CreationTimestamp: metav1.NewTime(created),
			Labels:            map[string]string{inbox.ParentNoteLabel: "true"},
		},
		Spec: coord.InboxMessageSpec{
			Sender:    coord.SessionIdentity{Namespace: "dev-agents", Name: "child", UID: "child-uid"},
			Recipient: to,
			Kind:      kind, Key: "PRIVATE_KEY", Text: text, Project: "dev-env",
		},
	}
}

func rvAfter(have, from string) bool {
	got, err1 := strconv.ParseUint(have, 10, 64)
	start, err2 := strconv.ParseUint(from, 10, 64)
	return err1 == nil && err2 == nil && got > start
}

// gateWatch delivers one event only when the handler is receiving it.
type gateWatch struct {
	ch      chan watch.Event
	stop    chan struct{}
	once    sync.Once
	stopped atomic.Bool
}

func newGateWatch() *gateWatch {
	return &gateWatch{ch: make(chan watch.Event), stop: make(chan struct{})}
}

func (g *gateWatch) Stop() {
	g.once.Do(func() {
		g.stopped.Store(true)
		close(g.stop)
	})
}

func (g *gateWatch) IsStopped() bool { return g.stopped.Load() }

func (g *gateWatch) ResultChan() <-chan watch.Event { return g.ch }

func (g *gateWatch) send(t *testing.T, ev watch.Event) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case g.ch <- ev:
	case <-g.stop:
		t.Fatal("watch stopped before the event")
	case <-timer.C:
		t.Fatal("handler did not receive the watch event")
	}
}

type waitAnyProbe struct {
	notes    []*coord.InboxMessage
	gate     bool
	started  chan watch.Interface
	watchErr error
	listRV   string
	inject   *coord.InboxMessage
	onList   func()

	lists, watches atomic.Int32
	mu             sync.Mutex
	listSels       []string
	listLimits     []int64
	watchSels      []string
	watchRVs       []string
	bookmarks      []bool
	watchLimits    []int64
	namespaces     []string
	sawInject      bool
	listCheck      func(context.Context)
	watchCheck     func(context.Context)
}

func (p *waitAnyProbe) install(f *fixture) {
	f.srv.Inbox.Live = interceptor.NewClient(f.c, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, obj client.ObjectList, opts ...client.ListOption) error {
		var o client.ListOptions
		o.ApplyOptions(opts)
		err := c.List(ctx, obj, opts...)
		list, ok := obj.(*coord.InboxMessageList)
		if !ok {
			return err
		}
		p.lists.Add(1)
		sel := ""
		if o.LabelSelector != nil {
			sel = o.LabelSelector.String()
		}
		p.mu.Lock()
		p.listSels = append(p.listSels, sel)
		p.listLimits = append(p.listLimits, o.Limit)
		p.mu.Unlock()
		list.Continue = ""
		if p.listRV != "" {
			list.ResourceVersion = p.listRV
		}
		if p.inject != nil {
			list.Items = append(list.Items, *p.inject)
			p.sawInject = true
		}
		if p.onList != nil {
			p.onList()
		}
		if p.listCheck != nil {
			p.listCheck(ctx)
		}
		return err
	}})
	f.srv.Inbox.Watcher = interceptor.NewClient(f.c, interceptor.Funcs{Watch: func(ctx context.Context, _ client.WithWatch, obj client.ObjectList, opts ...client.ListOption) (watch.Interface, error) {
		if p.watchCheck != nil {
			p.watchCheck(ctx)
		}
		if _, ok := obj.(*coord.InboxMessageList); !ok {
			return nil, errors.New("unexpected watch type")
		}
		var o client.ListOptions
		o.ApplyOptions(opts)
		p.watches.Add(1)
		p.recordWatch(o)
		if p.watchErr != nil {
			return nil, p.watchErr
		}
		if p.gate {
			g := newGateWatch()
			p.started <- g
			return g, nil
		}
		stream := watch.NewRaceFreeFake()
		p.mu.Lock()
		notes := append([]*coord.InboxMessage(nil), p.notes...)
		p.mu.Unlock()
		from := ""
		if o.Raw != nil {
			from = o.Raw.ResourceVersion
		}
		for _, note := range notes {
			if rvAfter(note.ResourceVersion, from) {
				stream.Add(note)
			}
		}
		return stream, nil
	}})
}

func (p *waitAnyProbe) recordWatch(o client.ListOptions) {
	sel := ""
	if o.LabelSelector != nil {
		sel = o.LabelSelector.String()
	}
	rv := ""
	mark := false
	if o.Raw != nil {
		rv = o.Raw.ResourceVersion
		mark = o.Raw.AllowWatchBookmarks
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.watchSels = append(p.watchSels, sel)
	p.watchRVs = append(p.watchRVs, rv)
	p.bookmarks = append(p.bookmarks, mark)
	p.watchLimits = append(p.watchLimits, o.Limit)
	p.namespaces = append(p.namespaces, o.Namespace)
}

func (p *waitAnyProbe) requireWatch(t *testing.T, i int, party coord.SessionIdentity, rv string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.watchSels) {
		t.Fatalf("watch %d missing (%d)", i, len(p.watchSels))
	}
	requireWaitAnySelector(t, p.watchSels[i], party)
	if p.namespaces[i] != "dev-env-system" || p.watchLimits[i] != 0 || !p.bookmarks[i] || p.watchRVs[i] != rv {
		t.Fatalf("watch %d ns %q limit %d bookmark %v rv %q, want %q", i, p.namespaces[i], p.watchLimits[i], p.bookmarks[i], p.watchRVs[i], rv)
	}
}

func (p *waitAnyProbe) requireList(t *testing.T, i int, party coord.SessionIdentity) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.listSels) {
		t.Fatalf("list %d missing (%d)", i, len(p.listSels))
	}
	requireWaitAnySelector(t, p.listSels[i], party)
	if p.listLimits[i] != 1 {
		t.Fatalf("list limit %d, want 1", p.listLimits[i])
	}
}

func requireWaitAnySelector(t *testing.T, sel string, party coord.SessionIdentity) {
	t.Helper()
	if !strings.Contains(sel, inbox.ParentNoteLabel+"=true") || !strings.Contains(sel, inbox.RecipientLabel+"="+waitAnyRecipient(party)) || strings.Contains(sel, inbox.KindLabel) {
		t.Fatalf("selector %q", sel)
	}
}

func armWaitAnyDeadline(f *fixture) *context.CancelFunc {
	var expire context.CancelFunc
	f.srv.parentWaitTimeout = func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		child, cancel := context.WithCancel(ctx)
		expire = cancel
		return parentDeadlineContext{child}, cancel
	}
	return &expire
}

func startWaitAny(f *fixture, token, query string) (<-chan *httptest.ResponseRecorder, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan *httptest.ResponseRecorder, 1)
	r := httptest.NewRequest(http.MethodGet, apiv1.ParentNotesWaitAnyPath+"?"+query, nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+token)
	go func() {
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		result <- w
	}()
	return result, cancel
}

func doWaitAny(t *testing.T, f *fixture, token, query string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, apiv1.ParentNotesWaitAnyPath+"?"+query, nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func decodeWaitAny(t *testing.T, w *httptest.ResponseRecorder) (apiv1.ParentNoteWaitAnyResult, map[string]json.RawMessage) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got := decode[apiv1.ParentNoteWaitAnyResult](t, w)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["text"]; ok {
		t.Fatal("response included text")
	}
	if _, ok := raw["key"]; ok {
		t.Fatal("response included key")
	}
	return got, raw
}

func assertNoPrivate(t *testing.T, body string) {
	t.Helper()
	for _, secret := range []string{"PRIVATE_NOTE_TEXT", "PRIVATE_KEY", "PRIVATE_EXPIRED", "PRIVATE_WATCH_ERROR", "PRIVATE_FOREIGN", "PRIVATE_REPLY", "PRIVATE_PLAIN", "PRIVATE_FUTURE"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaked %s: %s", secret, body)
		}
	}
}

func TestParentNoteWaitAnyReturnsStoredNote(t *testing.T) {
	f, parent, _, _ := parentNoteFixture(t, false)
	party := waitAnyParty(t, f, "parent")
	note := waitAnyNote(party, waitAnyName(4), inbox.KindQuestion, "5", "PRIVATE_NOTE_TEXT", waitAnyStamp)
	var logs strings.Builder
	f.srv.Log = logr.FromSlogHandler(slog.NewJSONHandler(&logs, nil))
	p := &waitAnyProbe{notes: []*coord.InboxMessage{note}, listRV: "1"}
	p.install(f)
	start := time.Now()
	w := doWaitAny(t, f, parent, "seconds=30&after=4")
	if time.Since(start) > time.Second {
		t.Fatal("a stored note did not return at once")
	}
	got, _ := decodeWaitAny(t, w)
	assertNoPrivate(t, w.Body.String())
	if strings.Contains(logs.String(), "PRIVATE_NOTE_TEXT") || strings.Contains(logs.String(), "PRIVATE_KEY") {
		t.Fatal("log included note text or key")
	}
	if got.State != apiv1.ParentNoteArrived || got.Name != note.Name || got.Child != "child" || got.Kind != inbox.KindQuestion || !got.CreatedAt.Equal(waitAnyStamp) || got.Cursor != "5" {
		t.Fatalf("note %+v", got)
	}
	if len(f.srv.parentWaits.held) != 0 || p.lists.Load() != 0 || p.watches.Load() != 1 {
		t.Fatalf("lists %d watches %d held %d", p.lists.Load(), p.watches.Load(), len(f.srv.parentWaits.held))
	}
	p.requireWatch(t, 0, party, "4")
}

func TestParentNoteWaitAnyHoldsUntilStored(t *testing.T) {
	f, parent, _, _ := parentNoteFixture(t, false)
	party := waitAnyParty(t, f, "parent")
	note := waitAnyNote(party, waitAnyName(7), inbox.KindResult, "8", "PRIVATE_NOTE_TEXT", waitAnyStamp)
	p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1)}
	p.install(f)
	result, cancel := startWaitAny(f, parent, "seconds=30&after=7")
	defer cancel()
	g := receiveParentWait(t, p.started).(*gateWatch)
	select {
	case early := <-result:
		t.Fatalf("returned before the note was stored: %s", early.Body.String())
	case <-time.After(200 * time.Millisecond):
	}
	g.send(t, watch.Event{Type: watch.Added, Object: note})
	w := receiveParentWait(t, result)
	got, _ := decodeWaitAny(t, w)
	assertNoPrivate(t, w.Body.String())
	if got.State != apiv1.ParentNoteArrived || got.Name != note.Name || got.Child != "child" || got.Kind != inbox.KindResult || got.Cursor != "8" || !g.IsStopped() || len(f.srv.parentWaits.held) != 0 {
		t.Fatalf("held wait %+v stopped %v", got, g.IsStopped())
	}
	p.requireWatch(t, 0, party, "7")
}

func TestParentNoteWaitAnyWaitingAtDeadline(t *testing.T) {
	for _, withAfter := range []bool{true, false} {
		t.Run(fmt.Sprintf("after=%v", withAfter), func(t *testing.T) {
			f, parent, _, _ := parentNoteFixture(t, false)
			party := waitAnyParty(t, f, "parent")
			p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1), listRV: "5"}
			p.install(f)
			expire := armWaitAnyDeadline(f)
			query := "seconds=30"
			if withAfter {
				query += "&after=5"
			}
			result, cancel := startWaitAny(f, parent, query)
			defer cancel()
			g := receiveParentWait(t, p.started).(*gateWatch)
			(*expire)()
			w := receiveParentWait(t, result)
			got, _ := decodeWaitAny(t, w)
			if got.State != apiv1.ParentNoteWaiting || got.Cursor != "5" || got.Name != "" || strings.Contains(w.Body.String(), "createdAt") || !g.IsStopped() || len(f.srv.parentWaits.held) != 0 {
				t.Fatalf("deadline %+v %s", got, w.Body.String())
			}
			wantLists := int32(1)
			if withAfter {
				wantLists = 0
			}
			if p.lists.Load() != wantLists || p.watches.Load() != 1 {
				t.Fatalf("lists %d watches %d", p.lists.Load(), p.watches.Load())
			}
			p.requireWatch(t, 0, party, "5")
			if !withAfter {
				p.requireList(t, 0, party)
			}
		})
	}
}

func TestParentNoteWaitAnySameSecondOrder(t *testing.T) {
	f, parent, _, _ := parentNoteFixture(t, false)
	party := waitAnyParty(t, f, "parent")
	// The first stored name sorts after the second. Storage order is the answer.
	first := waitAnyNote(party, waitAnyName(90), inbox.KindQuestion, "2", "PRIVATE_NOTE_TEXT", waitAnyStamp)
	second := waitAnyNote(party, waitAnyName(1), inbox.KindBlocker, "3", "PRIVATE_NOTE_TEXT", waitAnyStamp)
	if first.Name < second.Name || !first.CreationTimestamp.Equal(&second.CreationTimestamp) {
		t.Fatal("fixture does not reverse names inside one second")
	}
	p := &waitAnyProbe{notes: []*coord.InboxMessage{first, second}}
	p.install(f)
	w1 := doWaitAny(t, f, parent, "seconds=30&after=1")
	got1, _ := decodeWaitAny(t, w1)
	w2 := doWaitAny(t, f, parent, "seconds=30&after="+got1.Cursor)
	got2, _ := decodeWaitAny(t, w2)
	assertNoPrivate(t, w1.Body.String()+w2.Body.String())
	if got1.Name != first.Name || got1.Kind != inbox.KindQuestion || got1.Cursor != "2" || got2.Name != second.Name || got2.Kind != inbox.KindBlocker || got2.Cursor != "3" || !got1.CreatedAt.Equal(got2.CreatedAt) {
		t.Fatalf("order %s then %s", got1.Name, got2.Name)
	}
}

func TestParentNoteWaitAnyFortyNotes(t *testing.T) {
	f, parent, _, _ := parentNoteFixture(t, false)
	party := waitAnyParty(t, f, "parent")
	kinds := []string{inbox.KindQuestion, inbox.KindResult, inbox.KindBlocker}
	notes := make([]*coord.InboxMessage, 40)
	for i := range notes {
		notes[i] = waitAnyNote(party, waitAnyName(i^1), kinds[i%3], strconv.Itoa(i+2), "PRIVATE_NOTE_TEXT", waitAnyStamp)
	}
	if notes[0].Name < notes[1].Name || notes[1].Name > notes[2].Name {
		t.Fatal("fixture names are monotonic")
	}
	p := &waitAnyProbe{notes: notes}
	p.install(f)
	seen := map[string]bool{}
	cursor := "1"
	for i, note := range notes {
		w := doWaitAny(t, f, parent, "seconds=30&after="+cursor)
		got, _ := decodeWaitAny(t, w)
		assertNoPrivate(t, w.Body.String())
		if got.State != apiv1.ParentNoteArrived || got.Name != note.Name || got.Kind != note.Spec.Kind || got.Child != "child" || got.Cursor != note.ResourceVersion || seen[got.Name] || !got.CreatedAt.Equal(waitAnyStamp) {
			t.Fatalf("note %d: %+v", i, got)
		}
		seen[got.Name] = true
		cursor = got.Cursor
		p.requireWatch(t, i, party, cursorBefore(note.ResourceVersion))
	}
	if len(seen) != 40 || cursor != "41" {
		t.Fatalf("saw %d cursor %s", len(seen), cursor)
	}
	f.srv.parentWaitTimeout = expiredParentWait
	w := doWaitAny(t, f, parent, "seconds=30&after="+cursor)
	got, _ := decodeWaitAny(t, w)
	if got.State != apiv1.ParentNoteWaiting || got.Cursor != "41" || got.Name != "" || p.watches.Load() != 40 || p.lists.Load() != 0 {
		t.Fatalf("after forty %+v watches %d", got, p.watches.Load())
	}
}

func cursorBefore(rv string) string {
	n, err := strconv.ParseUint(rv, 10, 64)
	if err != nil || n == 0 {
		return ""
	}
	return strconv.FormatUint(n-1, 10)
}

func TestParentNoteWaitAnyBetweenListAndWatch(t *testing.T) {
	f, parent, _, _ := parentNoteFixture(t, false)
	party := waitAnyParty(t, f, "parent")
	note := waitAnyNote(party, waitAnyName(3), inbox.KindBlocker, "11", "PRIVATE_NOTE_TEXT", waitAnyStamp)
	listed := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	p := &waitAnyProbe{listRV: "10", onList: func() {
		once.Do(func() { close(listed) })
		<-release
	}}
	p.install(f)
	result, cancel := startWaitAny(f, parent, "seconds=30")
	defer cancel()
	receiveParentWait(t, listed)
	p.mu.Lock()
	p.notes = []*coord.InboxMessage{note}
	p.mu.Unlock()
	close(release)
	w := receiveParentWait(t, result)
	got, _ := decodeWaitAny(t, w)
	assertNoPrivate(t, w.Body.String())
	if got.State != apiv1.ParentNoteArrived || got.Name != note.Name || got.Cursor != "11" || p.lists.Load() != 1 || p.watches.Load() != 1 {
		t.Fatalf("between %+v lists %d watches %d", got, p.lists.Load(), p.watches.Load())
	}
	p.requireList(t, 0, party)
	p.requireWatch(t, 0, party, "10")
}

func TestParentNoteWaitAnyNoAfterSkipsExisting(t *testing.T) {
	f, parent, _, _ := parentNoteFixture(t, false)
	party := waitAnyParty(t, f, "parent")
	existing := waitAnyNote(party, waitAnyName(9), inbox.KindQuestion, "1", "PRIVATE_FUTURE", time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))
	p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1), listRV: "50", inject: existing}
	p.install(f)
	clocks := 0
	f.srv.Now = func() time.Time {
		clocks++
		return f.now
	}
	expire := armWaitAnyDeadline(f)
	result, cancel := startWaitAny(f, parent, "seconds=30")
	defer cancel()
	g := receiveParentWait(t, p.started).(*gateWatch)
	(*expire)()
	w := receiveParentWait(t, result)
	got, _ := decodeWaitAny(t, w)
	assertNoPrivate(t, w.Body.String())
	if !p.sawInject || got.State != apiv1.ParentNoteWaiting || got.Cursor != "50" || strings.Contains(w.Body.String(), existing.Name) || clocks != 2 || !g.IsStopped() {
		t.Fatalf("existing %+v clocks %d body %s", got, clocks, w.Body.String())
	}
	if p.lists.Load() != 1 || p.watches.Load() != 1 {
		t.Fatalf("lists %d watches %d", p.lists.Load(), p.watches.Load())
	}
	p.requireList(t, 0, party)
	p.requireWatch(t, 0, party, "50")
}

func TestParentNoteWaitAnyExpiredCursorResync(t *testing.T) {
	t.Run("watch call", func(t *testing.T) {
		f, parent, _, _ := parentNoteFixture(t, false)
		party := waitAnyParty(t, f, "parent")
		p := &waitAnyProbe{watchErr: apierrors.NewResourceExpired("PRIVATE_EXPIRED"), listRV: "900"}
		p.install(f)
		w := doWaitAny(t, f, parent, "seconds=5&after=3")
		got, _ := decodeWaitAny(t, w)
		assertNoPrivate(t, w.Body.String())
		if got.State != apiv1.ParentNoteResync || got.Cursor != "900" || got.Name != "" || w.Code != http.StatusOK || p.lists.Load() != 1 || p.watches.Load() != 1 || len(f.srv.parentWaits.held) != 0 {
			t.Fatalf("resync %+v %s lists %d", got, w.Body.String(), p.lists.Load())
		}
		p.requireList(t, 0, party)
	})
	t.Run("error event", func(t *testing.T) {
		f, parent, _, _ := parentNoteFixture(t, false)
		party := waitAnyParty(t, f, "parent")
		p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1), listRV: "900"}
		p.install(f)
		result, cancel := startWaitAny(f, parent, "seconds=30&after=3")
		defer cancel()
		g := receiveParentWait(t, p.started).(*gateWatch)
		g.send(t, watch.Event{Type: watch.Error, Object: &metav1.Status{Status: metav1.StatusFailure, Code: http.StatusGone, Reason: metav1.StatusReasonExpired, Message: "PRIVATE_EXPIRED"}})
		w := receiveParentWait(t, result)
		got, _ := decodeWaitAny(t, w)
		assertNoPrivate(t, w.Body.String())
		if got.State != apiv1.ParentNoteResync || got.Cursor != "900" || got.Name != "" || p.lists.Load() != 1 || p.watches.Load() != 1 || !g.IsStopped() {
			t.Fatalf("error event %+v lists %d watches %d", got, p.lists.Load(), p.watches.Load())
		}
		p.requireWatch(t, 0, party, "3")
		p.requireList(t, 0, party)
	})
}

func TestParentNoteWaitAnyModifiedDoesNotWake(t *testing.T) {
	f, parent, _, _ := parentNoteFixture(t, false)
	party := waitAnyParty(t, f, "parent")
	note := waitAnyNote(party, waitAnyName(6), inbox.KindQuestion, "11", "PRIVATE_NOTE_TEXT", waitAnyStamp)
	p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1)}
	p.install(f)
	expire := armWaitAnyDeadline(f)
	result, cancel := startWaitAny(f, parent, "seconds=30&after=10")
	defer cancel()
	g := receiveParentWait(t, p.started).(*gateWatch)
	g.send(t, watch.Event{Type: watch.Modified, Object: note})
	(*expire)()
	w := receiveParentWait(t, result)
	got, _ := decodeWaitAny(t, w)
	assertNoPrivate(t, w.Body.String())
	if got.State != apiv1.ParentNoteWaiting || got.Name != "" || got.Cursor != "11" || strings.Contains(w.Body.String(), note.Name) {
		t.Fatalf("modified %+v %s", got, w.Body.String())
	}
}

func TestParentNoteWaitAnyBookmarkCursor(t *testing.T) {
	f, parent, _, _ := parentNoteFixture(t, false)
	party := waitAnyParty(t, f, "parent")
	p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 2)}
	p.install(f)
	expire := armWaitAnyDeadline(f)
	result, cancel := startWaitAny(f, parent, "seconds=30&after=10")
	defer cancel()
	g := receiveParentWait(t, p.started).(*gateWatch)
	g.send(t, watch.Event{Type: watch.Bookmark, Object: &coord.InboxMessage{ObjectMeta: metav1.ObjectMeta{ResourceVersion: "80"}}})
	(*expire)()
	w := receiveParentWait(t, result)
	got, _ := decodeWaitAny(t, w)
	if got.State != apiv1.ParentNoteWaiting || got.Cursor != "80" || got.Name != "" {
		t.Fatalf("bookmark %+v", got)
	}
	result2, cancel2 := startWaitAny(f, parent, "seconds=30&after=80")
	defer cancel2()
	receiveParentWait(t, p.started)
	(*expire)()
	w2 := receiveParentWait(t, result2)
	got2, _ := decodeWaitAny(t, w2)
	if w2.Code != http.StatusOK || got2.State != apiv1.ParentNoteWaiting || got2.Cursor != "80" || p.watches.Load() != 2 || p.lists.Load() != 0 {
		t.Fatalf("next %+v watches %d", got2, p.watches.Load())
	}
	p.requireWatch(t, 1, party, "80")
}

func TestParentNoteWaitAnyOtherSession(t *testing.T) {
	f, parent, _, _ := parentNoteFixture(t, false)
	party := waitAnyParty(t, f, "parent")
	stranger := waitAnyParty(t, f, "stranger")
	if stranger.Namespace != party.Namespace || stranger.Name == party.Name || stranger.UID == party.UID {
		t.Fatal("stranger is not another session in the same namespace")
	}
	foreign := waitAnyNote(stranger, waitAnyName(8), inbox.KindQuestion, "12", "PRIVATE_FOREIGN", waitAnyStamp)
	p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1)}
	p.install(f)
	expire := armWaitAnyDeadline(f)
	result, cancel := startWaitAny(f, parent, "seconds=30&after=10")
	defer cancel()
	g := receiveParentWait(t, p.started).(*gateWatch)
	g.send(t, watch.Event{Type: watch.Added, Object: foreign})
	(*expire)()
	w := receiveParentWait(t, result)
	got, _ := decodeWaitAny(t, w)
	assertNoPrivate(t, w.Body.String())
	if got.State != apiv1.ParentNoteWaiting || got.Cursor != "10" || got.Name != "" || strings.Contains(w.Body.String(), foreign.Name) {
		t.Fatalf("foreign %+v %s", got, w.Body.String())
	}
	p.requireWatch(t, 0, party, "10")
}

func TestParentNoteWaitAnyReplyAndPlain(t *testing.T) {
	f, parent, _, _ := parentNoteFixture(t, false)
	party := waitAnyParty(t, f, "parent")
	reply := waitAnyNote(party, waitAnyName(2), inbox.KindReply, "11", "PRIVATE_REPLY", waitAnyStamp)
	plain := waitAnyNote(party, waitAnyName(3), "", "12", "PRIVATE_PLAIN", waitAnyStamp)
	p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1)}
	p.install(f)
	expire := armWaitAnyDeadline(f)
	result, cancel := startWaitAny(f, parent, "seconds=30&after=10")
	defer cancel()
	g := receiveParentWait(t, p.started).(*gateWatch)
	g.send(t, watch.Event{Type: watch.Added, Object: reply})
	g.send(t, watch.Event{Type: watch.Added, Object: plain})
	(*expire)()
	w := receiveParentWait(t, result)
	got, _ := decodeWaitAny(t, w)
	assertNoPrivate(t, w.Body.String())
	if got.State != apiv1.ParentNoteWaiting || got.Name != "" || got.Cursor != "12" || strings.Contains(w.Body.String(), reply.Name) || strings.Contains(w.Body.String(), plain.Name) {
		t.Fatalf("reply or plain woke %+v %s", got, w.Body.String())
	}
}

func TestParentNoteWaitAnyNinthHeldWithParentWait(t *testing.T) {
	f, parent, child, _ := parentNoteFixture(t, false)
	note := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "note", Kind: "question", Text: "x"}, 201)
	p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 16), listRV: "42"}
	p.install(f)
	var results []<-chan *httptest.ResponseRecorder
	var cancels []context.CancelFunc
	var streams []*gateWatch
	hold := func(result <-chan *httptest.ResponseRecorder, cancel context.CancelFunc) {
		t.Helper()
		t.Cleanup(cancel)
		results = append(results, result)
		cancels = append(cancels, cancel)
		streams = append(streams, receiveParentWait(t, p.started).(*gateWatch))
	}
	for range 4 {
		hold(startParentWait(f, child, note.Name))
	}
	for range 4 {
		hold(startWaitAny(f, child, "seconds=30&after=42"))
	}
	// Another session does not share the child's eight.
	parentResult, parentCancel := startWaitAny(f, parent, "seconds=30&after=42")
	t.Cleanup(parentCancel)
	receiveParentWait(t, p.started)
	ninth := f.do(http.MethodGet, apiv1.ParentNotesWaitAnyPath+"?seconds=1&after=42", child, nil)
	e := wantError(t, ninth, http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	parentNinth := f.do(http.MethodGet, apiv1.ParentNotesPath+"/"+note.Name+"/wait?seconds=1", child, nil)
	pe := wantError(t, parentNinth, http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	if e.Message != parentHeldWaitLimitMessage || pe.Message != parentHeldWaitLimitMessage || p.watches.Load() != 9 {
		t.Fatalf("ninth %q parent %q watches %d", e.Message, pe.Message, p.watches.Load())
	}
	parentCancel()
	receiveParentWait(t, parentResult)
	for i, cancel := range cancels {
		cancel()
		receiveParentWait(t, results[i])
		if !streams[i].IsStopped() {
			t.Fatal("canceled request leaked its watch")
		}
	}
	if len(f.srv.parentWaits.held) != 0 {
		t.Fatal("canceled requests leaked slots")
	}
}

func TestParentNoteWaitAnyRefusedWatch(t *testing.T) {
	f, parent, _, _ := parentNoteFixture(t, false)
	p := &waitAnyProbe{watchErr: errors.New("PRIVATE_WATCH_ERROR"), listRV: "5"}
	p.install(f)
	w := doWaitAny(t, f, parent, "seconds=5&after=3")
	e := wantError(t, w, http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	assertNoPrivate(t, w.Body.String())
	if e.Message != parentWaitAnyWatchRefused || p.watches.Load() != 1 || p.lists.Load() != 0 || len(f.srv.parentWaits.held) != 0 {
		t.Fatalf("refused %q watches %d lists %d held %d", e.Message, p.watches.Load(), p.lists.Load(), len(f.srv.parentWaits.held))
	}
}

func TestParentNoteWaitAnyFlagOff(t *testing.T) {
	f, parent, _, _ := parentNoteFixture(t, false)
	p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1), listRV: "5"}
	p.install(f)
	f.srv.ParentNotes = false
	f.h = f.srv.Handler()
	w := f.do(http.MethodGet, apiv1.ParentNotesWaitAnyPath+"?seconds=1", parent, nil)
	e := wantError(t, w, http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	if e.Message != "parent notes are not enabled" || p.watches.Load() != 0 || p.lists.Load() != 0 || len(f.srv.parentWaits.held) != 0 {
		t.Fatalf("flag off %q watches %d lists %d", e.Message, p.watches.Load(), p.lists.Load())
	}
	post := f.do(http.MethodPost, apiv1.ParentNotesWaitAnyPath, parent, nil)
	if post.Code != http.StatusMethodNotAllowed || !strings.Contains(post.Body.String(), "/v1/parent-notes/wait takes ") {
		t.Fatalf("method %d %s", post.Code, post.Body.String())
	}
}

func TestParentNoteWaitAnyCallerPolicyRefused(t *testing.T) {
	f, _, _, _ := parentNoteFixture(t, false)
	if err := f.c.Create(context.Background(), meatballPolicy()); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(meatballAccount)
	id.PodName, id.PodUID = "front-door", "front-door-pod"
	f.auth[tokMeatball] = id
	p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1), listRV: "5"}
	p.install(f)
	w := f.do(http.MethodGet, apiv1.ParentNotesWaitAnyPath+"?seconds=1", tokMeatball, nil)
	wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
	if p.watches.Load() != 0 || p.lists.Load() != 0 || len(f.srv.parentWaits.held) != 0 {
		t.Fatalf("policy caller watches %d lists %d", p.watches.Load(), p.lists.Load())
	}
}

func TestParentNoteWaitAnyBadCursor(t *testing.T) {
	f, parent, _, _ := parentNoteFixture(t, false)
	p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1), listRV: "5"}
	p.install(f)
	// Zero is a canonical integer, and a watch from resourceVersion 0 replays
	// notes that already exist, so it is not a cursor.
	bad := []string{"0", "nope", "01", "-1", "1.2", strings.Repeat("9", 256), strings.Repeat("9", 257)}
	for _, after := range bad {
		w := f.do(http.MethodGet, apiv1.ParentNotesWaitAnyPath+"?seconds=1&after="+after, parent, nil)
		e := wantError(t, w, http.StatusUnprocessableEntity, apiv1.CodeInvalid)
		if e.Message != "after must be a resource version" || strings.Contains(w.Body.String(), after) {
			t.Fatalf("cursor %q echoed or changed the sentence: %s", after, w.Body.String())
		}
	}
	empty := f.do(http.MethodGet, apiv1.ParentNotesWaitAnyPath+"?seconds=1&after=", parent, nil)
	wantError(t, empty, http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	for _, query := range []string{"", "?seconds=901", "?seconds=1&seconds=2", "?seconds=1&after=1&after=2"} {
		wantError(t, f.do(http.MethodGet, apiv1.ParentNotesWaitAnyPath+query, parent, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	}
	if p.watches.Load() != 0 || p.lists.Load() != 0 || len(f.srv.parentWaits.held) != 0 {
		t.Fatalf("bad cursor watched lists %d watches %d", p.lists.Load(), p.watches.Load())
	}
}

func TestParentNoteWaitAnySecondsZero(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		f, parent, _, _ := parentNoteFixture(t, false)
		party := waitAnyParty(t, f, "parent")
		p := &waitAnyProbe{listRV: "15"}
		heldDuring := -1
		p.listCheck = func(context.Context) { heldDuring = len(f.srv.parentWaits.held) }
		p.install(f)
		f.srv.parentWaitTimeout = func(context.Context, time.Duration) (context.Context, context.CancelFunc) {
			t.Fatal("seconds=0 opened a wait")
			return context.Background(), func() {}
		}
		w := doWaitAny(t, f, parent, "seconds=0")
		got, _ := decodeWaitAny(t, w)
		if got.State != apiv1.ParentNoteWaiting || got.Cursor != "15" || got.Name != "" || p.lists.Load() != 1 || p.watches.Load() != 0 || heldDuring != 0 || len(f.srv.parentWaits.held) != 0 {
			t.Fatalf("zero %+v lists %d watches %d held during %d", got, p.lists.Load(), p.watches.Load(), heldDuring)
		}
		p.requireList(t, 0, party)
	})
	t.Run("given cursor", func(t *testing.T) {
		f, parent, _, _ := parentNoteFixture(t, false)
		p := &waitAnyProbe{listRV: "15", gate: true, started: make(chan watch.Interface, 1)}
		p.install(f)
		w := doWaitAny(t, f, parent, "seconds=0&after=42")
		got, _ := decodeWaitAny(t, w)
		if got.State != apiv1.ParentNoteWaiting || got.Cursor != "42" || p.lists.Load() != 0 || p.watches.Load() != 0 || len(f.srv.parentWaits.held) != 0 {
			t.Fatalf("given %+v lists %d watches %d", got, p.lists.Load(), p.watches.Load())
		}
	})
	t.Run("bad cursor", func(t *testing.T) {
		f, parent, _, _ := parentNoteFixture(t, false)
		p := &waitAnyProbe{listRV: "15"}
		p.install(f)
		wantError(t, f.do(http.MethodGet, apiv1.ParentNotesWaitAnyPath+"?seconds=0&after=0", parent, nil), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
		if p.lists.Load() != 0 || p.watches.Load() != 0 || len(f.srv.parentWaits.held) != 0 {
			t.Fatal("bad zero cursor listed or watched")
		}
	})
	t.Run("revoked during the baseline read", func(t *testing.T) {
		f, _, _, _ := parentNoteFixture(t, false)
		tok := waitAnyNamedParent(t, f)
		p := &waitAnyProbe{listRV: "15"}
		p.listCheck = func(context.Context) { revokeNamedAdmission(t, f) }
		p.install(f)
		f.srv.parentWaitTimeout = func(context.Context, time.Duration) (context.Context, context.CancelFunc) {
			t.Error("seconds=0 opened a wait")
			return context.Background(), func() {}
		}
		start := time.Now()
		w := doWaitAny(t, f, tok, "seconds=0")
		if time.Since(start) > time.Second {
			t.Fatal("seconds=0 refusal was not immediate")
		}
		wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
		if p.lists.Load() != 1 || p.watches.Load() != 0 || len(f.srv.parentWaits.held) != 0 {
			t.Fatalf("zero revoked lists %d watches %d held %d", p.lists.Load(), p.watches.Load(), len(f.srv.parentWaits.held))
		}
	})
}

// waitBudgetKey marks the context the wait's deadline owns.
type waitBudgetKey struct{}

func TestParentNoteWaitAnyReadBudget(t *testing.T) {
	t.Run("list and watch", func(t *testing.T) {
		f, parent, _, _ := parentNoteFixture(t, false)
		p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1), listRV: "5"}
		var listKey, watchKey any
		var listHeld, watchHeld int
		p.listCheck = func(ctx context.Context) {
			listKey = ctx.Value(waitBudgetKey{})
			listHeld = len(f.srv.parentWaits.held)
		}
		p.watchCheck = func(ctx context.Context) {
			watchKey = ctx.Value(waitBudgetKey{})
			watchHeld = len(f.srv.parentWaits.held)
		}
		p.install(f)
		var expire context.CancelFunc
		f.srv.parentWaitTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
			child, cancel := context.WithCancel(context.WithValue(ctx, waitBudgetKey{}, d))
			expire = cancel
			return parentDeadlineContext{child}, cancel
		}
		result, cancel := startWaitAny(f, parent, "seconds=30")
		defer cancel()
		receiveParentWait(t, p.started)
		if listKey != 30*time.Second || watchKey != 30*time.Second || listHeld != 1 || watchHeld != 1 {
			t.Fatalf("list key %v held %d; watch key %v held %d", listKey, listHeld, watchKey, watchHeld)
		}
		expire()
		w := receiveParentWait(t, result)
		got, _ := decodeWaitAny(t, w)
		if got.State != apiv1.ParentNoteWaiting || got.Cursor != "5" {
			t.Fatalf("budget %+v", got)
		}
	})
	t.Run("resync", func(t *testing.T) {
		f, parent, _, _ := parentNoteFixture(t, false)
		p := &waitAnyProbe{watchErr: apierrors.NewResourceExpired("PRIVATE_EXPIRED"), listRV: "900"}
		var listKey any
		var listHeld int
		p.listCheck = func(ctx context.Context) {
			listKey = ctx.Value(waitBudgetKey{})
			listHeld = len(f.srv.parentWaits.held)
		}
		p.watchCheck = func(ctx context.Context) {
			if ctx.Value(waitBudgetKey{}) != 5*time.Second {
				t.Errorf("watch outside the wait")
			}
		}
		p.install(f)
		f.srv.parentWaitTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
			return parentDeadlineContext{context.WithValue(ctx, waitBudgetKey{}, d)}, func() {}
		}
		w := doWaitAny(t, f, parent, "seconds=5&after=3")
		got, _ := decodeWaitAny(t, w)
		assertNoPrivate(t, w.Body.String())
		if listKey != 5*time.Second || listHeld != 1 || got.State != apiv1.ParentNoteResync || got.Cursor != "900" {
			t.Fatalf("resync %+v key %v held %d", got, listKey, listHeld)
		}
	})
	t.Run("fence", func(t *testing.T) {
		f, parent, _, _ := parentNoteFixture(t, false)
		p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1)}
		p.install(f)
		var expire context.CancelFunc
		f.srv.parentWaitTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
			child, cancel := context.WithCancel(context.WithValue(ctx, waitBudgetKey{}, d))
			expire = cancel
			return parentDeadlineContext{child}, cancel
		}
		var armed atomic.Bool
		var fenceErr error
		var fenceLeft time.Duration
		var fenceWait, saw bool
		f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			err := c.Get(ctx, key, obj, opts...)
			if armed.Load() {
				if _, ok := obj.(*v1alpha1.AgentSession); ok {
					saw = true
					fenceErr = ctx.Err()
					fenceWait = ctx.Value(waitBudgetKey{}) != nil
					if dl, ok := ctx.Deadline(); ok {
						fenceLeft = time.Until(dl)
					}
				}
			}
			return err
		}})
		result, cancel := startWaitAny(f, parent, "seconds=30&after=10")
		defer cancel()
		receiveParentWait(t, p.started)
		armed.Store(true)
		expire()
		w := receiveParentWait(t, result)
		got, _ := decodeWaitAny(t, w)
		if got.State != apiv1.ParentNoteWaiting || got.Cursor != "10" || !saw || fenceErr != nil || fenceWait || fenceLeft < 8*time.Second || fenceLeft > parentNoteFenceAllowance {
			t.Fatalf("fence %+v err %v waitKey %v left %s saw %v", got, fenceErr, fenceWait, fenceLeft, saw)
		}
	})
}

func TestParentNoteWaitAnyRecipientNameAndUID(t *testing.T) {
	f, parent, _, _ := parentNoteFixture(t, false)
	party := waitAnyParty(t, f, "parent")
	sameName := party
	sameName.UID = party.UID + "-other"
	sameUID := party
	sameUID.Name = "other-name"
	for _, tc := range []struct {
		name string
		to   coord.SessionIdentity
	}{
		{"same name other uid", sameName},
		{"same uid other name", sameUID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			note := waitAnyNote(tc.to, waitAnyName(8), inbox.KindQuestion, "12", "PRIVATE_FOREIGN", waitAnyStamp)
			p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1)}
			p.install(f)
			expire := armWaitAnyDeadline(f)
			result, cancel := startWaitAny(f, parent, "seconds=30&after=10")
			defer cancel()
			g := receiveParentWait(t, p.started).(*gateWatch)
			g.send(t, watch.Event{Type: watch.Added, Object: note})
			(*expire)()
			w := receiveParentWait(t, result)
			got, _ := decodeWaitAny(t, w)
			assertNoPrivate(t, w.Body.String())
			if got.State != apiv1.ParentNoteWaiting || got.Cursor != "10" || got.Name != "" || strings.Contains(w.Body.String(), note.Name) {
				t.Fatalf("%s woke %+v %s", tc.name, got, w.Body.String())
			}
		})
	}
}

// waitAnyNamedParent admits the parent fixture's session as a named coordinator
// and returns a token for that pod.
func waitAnyNamedParent(t *testing.T, f *fixture) string {
	t.Helper()
	const account = "dev-env-coordinator-lead"
	f.srv.Policy.NamedCoordinatorIdentities = true
	sess := f.session("parent")
	sess.Labels[templates.CoordinatorIdentityLabel] = account
	if err := f.c.Update(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "parent"}, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Spec.ServiceAccountName = account
	if err := f.c.Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	tok := "tok-named-parent"
	id := saIdentity(sessionNS + "/" + account)
	id.PodName, id.PodUID = "parent", string(pod.UID)
	f.auth[tok] = id
	return tok
}

func revokeNamedAdmission(t *testing.T, f *fixture) {
	t.Helper()
	sess := f.session("parent")
	sess.Labels[templates.CoordinatorIdentityLabel] = "dev-env-coordinator-other"
	if err := f.c.Update(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
}

func TestParentNoteWaitAnyRevokedAdmission(t *testing.T) {
	t.Run("before the wait", func(t *testing.T) {
		f, _, _, _ := parentNoteFixture(t, false)
		tok := waitAnyNamedParent(t, f)
		revokeNamedAdmission(t, f)
		p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1), listRV: "5"}
		p.install(f)
		result, cancel := startWaitAny(f, tok, "seconds=30&after=10")
		defer cancel()
		w := receiveParentWait(t, result)
		wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
		if p.watches.Load() != 0 || p.lists.Load() != 0 || len(f.srv.parentWaits.held) != 0 {
			t.Fatalf("early refusal watched %d listed %d held %d", p.watches.Load(), p.lists.Load(), len(f.srv.parentWaits.held))
		}
	})
	t.Run("note after the watch", func(t *testing.T) {
		f, _, _, _ := parentNoteFixture(t, false)
		tok := waitAnyNamedParent(t, f)
		party := waitAnyParty(t, f, "parent")
		note := waitAnyNote(party, waitAnyName(4), inbox.KindQuestion, "12", "PRIVATE_NOTE_TEXT", waitAnyStamp)
		p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1)}
		p.install(f)
		result, cancel := startWaitAny(f, tok, "seconds=30&after=10")
		defer cancel()
		g := receiveParentWait(t, p.started).(*gateWatch)
		revokeNamedAdmission(t, f)
		g.send(t, watch.Event{Type: watch.Added, Object: note})
		select {
		case early := <-result:
			t.Fatalf("revoked fence answered before the refusal time: %s", early.Body.String())
		case <-time.After(200 * time.Millisecond):
		}
		cancel()
		w := receiveParentWait(t, result)
		wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
		assertNoPrivate(t, w.Body.String())
		if strings.Contains(w.Body.String(), note.Name) || len(f.srv.parentWaits.held) != 0 || p.watches.Load() != 1 {
			t.Fatalf("revoked body %s watches %d", w.Body.String(), p.watches.Load())
		}
	})
	t.Run("deadline after the watch", func(t *testing.T) {
		f, _, _, _ := parentNoteFixture(t, false)
		tok := waitAnyNamedParent(t, f)
		p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1)}
		p.install(f)
		expire := armWaitAnyDeadline(f)
		result, cancel := startWaitAny(f, tok, "seconds=30&after=10")
		defer cancel()
		receiveParentWait(t, p.started)
		revokeNamedAdmission(t, f)
		(*expire)()
		select {
		case early := <-result:
			t.Fatalf("answered when the wait ended, before the refusal time: %s", early.Body.String())
		case <-time.After(200 * time.Millisecond):
		}
		cancel()
		w := receiveParentWait(t, result)
		wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
		if strings.Contains(w.Body.String(), "waiting") || len(f.srv.parentWaits.held) != 0 {
			t.Fatalf("deadline refusal %s", w.Body.String())
		}
	})
}

// TestParentNoteWaitAnyLiveAdmissionLabel gives the cache and the live reader
// different admission labels. The cache still admits the named coordinator.
// The live session does not. The fence must refuse. Removing the live label
// check lets resolveCaller pass on the cache and return the note.
func TestParentNoteWaitAnyLiveAdmissionLabel(t *testing.T) {
	f, _, _, _ := parentNoteFixture(t, false)
	tok := waitAnyNamedParent(t, f)
	party := waitAnyParty(t, f, "parent")
	note := waitAnyNote(party, waitAnyName(4), inbox.KindQuestion, "12", "PRIVATE_NOTE_TEXT", waitAnyStamp)
	cached := f.session("parent")
	if cached.Labels[templates.CoordinatorIdentityLabel] != "dev-env-coordinator-lead" {
		t.Fatalf("cache label %q", cached.Labels[templates.CoordinatorIdentityLabel])
	}
	liveSess := cached.DeepCopy()
	liveSess.Labels[templates.CoordinatorIdentityLabel] = "dev-env-coordinator-other"
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "parent"}, &pod); err != nil {
		t.Fatal(err)
	}
	live := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(liveSess, pod.DeepCopy()).Build()
	var gotLive v1alpha1.AgentSession
	if err := live.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "parent"}, &gotLive); err != nil {
		t.Fatal(err)
	}
	if gotLive.UID != cached.UID || gotLive.Labels[templates.CoordinatorIdentityLabel] == cached.Labels[templates.CoordinatorIdentityLabel] {
		t.Fatalf("live view uid %s label %q", gotLive.UID, gotLive.Labels[templates.CoordinatorIdentityLabel])
	}
	f.srv.Live = live
	f.srv.parentRefusalWait = func(context.Context, time.Time) {}
	p := &waitAnyProbe{notes: []*coord.InboxMessage{note}}
	p.install(f)
	w := doWaitAny(t, f, tok, "seconds=30&after=10")
	wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
	assertNoPrivate(t, w.Body.String())
	if strings.Contains(w.Body.String(), note.Name) || p.watches.Load() != 1 || len(f.srv.parentWaits.held) != 0 {
		t.Fatalf("live refusal body %s watches %d", w.Body.String(), p.watches.Load())
	}
	if again := f.session("parent"); again.Labels[templates.CoordinatorIdentityLabel] != "dev-env-coordinator-lead" {
		t.Fatalf("cache label changed to %q", again.Labels[templates.CoordinatorIdentityLabel])
	}
}

// TestParentNoteWaitAnyRefusalTime runs the note path and the idle path with
// a fence read that blocks until the test releases it. Both must name the
// refusal time fixed when the wait started: that wait's deadline plus the
// fence allowance. Neither answers before that hold ends. Cancelling the
// request ends it without waiting out the timer.
func TestParentNoteWaitAnyRefusalTime(t *testing.T) {
	for _, notePath := range []bool{true, false} {
		name := "idle"
		if notePath {
			name = "note"
		}
		t.Run(name, func(t *testing.T) {
			f, _, _, _ := parentNoteFixture(t, false)
			tok := waitAnyNamedParent(t, f)
			party := waitAnyParty(t, f, "parent")
			stored := waitAnyNote(party, waitAnyName(4), inbox.KindQuestion, "12", "PRIVATE_NOTE_TEXT", waitAnyStamp)
			p := &waitAnyProbe{}
			if notePath {
				p.notes = []*coord.InboxMessage{stored}
			} else {
				p.gate = true
				p.started = make(chan watch.Interface, 1)
			}
			p.install(f)
			var deadline time.Time
			var expire context.CancelFunc
			f.srv.parentWaitTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
				deadline = time.Now().Add(d)
				inner, cancel := context.WithDeadline(ctx, deadline)
				expire = cancel
				return parentDeadlineContext{inner}, cancel
			}
			fenceStarted := make(chan struct{})
			releaseFence := make(chan struct{})
			var sessionGets atomic.Int32
			var fenceOnce sync.Once
			f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				err := c.Get(ctx, key, obj, opts...)
				sess, ok := obj.(*v1alpha1.AgentSession)
				if err != nil || !ok || sess.Name != "parent" || sessionGets.Add(1) < 2 {
					return err
				}
				fenceOnce.Do(func() { close(fenceStarted) })
				select {
				case <-releaseFence:
				case <-ctx.Done():
					return ctx.Err()
				}
				sess.Labels[templates.CoordinatorIdentityLabel] = "dev-env-coordinator-other"
				return nil
			}})
			entered := make(chan struct{})
			releaseRefusal := make(chan struct{})
			var gotAt time.Time
			f.srv.parentRefusalWait = func(ctx context.Context, at time.Time) {
				gotAt = at
				close(entered)
				select {
				case <-ctx.Done():
				case <-releaseRefusal:
				}
			}
			result, cancel := startWaitAny(f, tok, "seconds=30&after=10")
			defer cancel()
			if !notePath {
				receiveParentWait(t, p.started)
				expire()
			}
			select {
			case <-fenceStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("fence read did not start")
			}
			close(releaseFence)
			select {
			case <-entered:
			case early := <-result:
				t.Fatalf("answered before the refusal time: %s", early.Body.String())
			case <-time.After(2 * time.Second):
				t.Fatal("refusal was not held")
			}
			want := deadline.Add(parentNoteFenceAllowance)
			if !gotAt.Equal(want) {
				t.Fatalf("refusal %s, want deadline plus allowance %s", gotAt, want)
			}
			select {
			case early := <-result:
				t.Fatalf("answered before the hold ended: %s", early.Body.String())
			default:
			}
			close(releaseRefusal)
			w := receiveParentWait(t, result)
			wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
			assertNoPrivate(t, w.Body.String())
			if len(f.srv.parentWaits.held) != 0 {
				t.Fatal("refusal leaked its slot")
			}
		})
	}
	t.Run("cancelled", func(t *testing.T) {
		f, _, _, _ := parentNoteFixture(t, false)
		tok := waitAnyNamedParent(t, f)
		party := waitAnyParty(t, f, "parent")
		note := waitAnyNote(party, waitAnyName(4), inbox.KindQuestion, "12", "PRIVATE_NOTE_TEXT", waitAnyStamp)
		p := &waitAnyProbe{gate: true, started: make(chan watch.Interface, 1)}
		p.install(f)
		result, cancel := startWaitAny(f, tok, "seconds=30&after=10")
		defer cancel()
		g := receiveParentWait(t, p.started).(*gateWatch)
		revokeNamedAdmission(t, f)
		g.send(t, watch.Event{Type: watch.Added, Object: note})
		select {
		case early := <-result:
			t.Fatalf("answered before cancellation: %s", early.Body.String())
		case <-time.After(200 * time.Millisecond):
		}
		cancel()
		select {
		case w := <-result:
			wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
			assertNoPrivate(t, w.Body.String())
		case <-time.After(2 * time.Second):
			t.Fatal("request cancellation did not end the refusal")
		}
	})
}
