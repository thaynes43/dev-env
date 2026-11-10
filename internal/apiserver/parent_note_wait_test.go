package apiserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

const missingParentNoteName = "message.00000000000000000000000000000000.00000000000000000000000000000000"

func answerParentTest(t *testing.T, f *fixture, token, name, key, text string, status int) apiv1.InboxMetadata {
	t.Helper()
	w := f.do(http.MethodPost, apiv1.ParentNotesPath+"/"+name+"/answer", token, apiv1.AnswerParentRequest{Key: key, Text: text})
	if w.Code != status {
		t.Fatalf("answer status %d, want %d: %s", w.Code, status, w.Body.String())
	}
	if status/100 != 2 {
		return apiv1.InboxMetadata{}
	}
	return decode[apiv1.InboxResult](t, w).Message
}

type parentDeadlineWriter struct {
	*httptest.ResponseRecorder
	read, write time.Time
	fail        bool
}

func (w *parentDeadlineWriter) SetReadDeadline(d time.Time) error {
	w.read = d
	if w.fail {
		return errors.New("PRIVATE_DEADLINE_ERROR")
	}
	return nil
}

func (w *parentDeadlineWriter) SetWriteDeadline(d time.Time) error {
	w.write = d
	return nil
}

func TestParentNoteWaitMovesBothDeadlines(t *testing.T) {
	f, _, child, _ := parentNoteFixture(t, false)
	note := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "note", Kind: "question", Text: "x"}, 201)
	f.srv.parentWaitTimeout = expiredParentWait
	for _, fail := range []bool{false, true} {
		w := &parentDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), fail: fail}
		r := httptest.NewRequest("GET", apiv1.ParentNotesPath+"/"+note.Name+"/wait?seconds=900", nil)
		r.Header.Set("Authorization", "Bearer "+child)
		start := time.Now()
		f.h.ServeHTTP(w, r)
		if fail {
			wantError(t, w.ResponseRecorder, 503, apiv1.CodeUnavailable)
			if strings.Contains(w.Body.String(), "PRIVATE_DEADLINE_ERROR") {
				t.Fatal("deadline error escaped")
			}
		} else if w.Code != 200 || !w.read.Equal(w.write) || w.read.Before(start.Add(930*time.Second)) || w.read.After(time.Now().Add(930*time.Second)) {
			t.Fatal("wait did not move both deadlines by N plus 30 seconds")
		}
		if len(f.srv.parentWaits.held) != 0 {
			t.Fatal("deadline path leaked a wait slot")
		}
	}
}

type parentDeadlineContext struct{ context.Context }

func (c parentDeadlineContext) Err() error {
	if c.Context.Err() != nil {
		return context.DeadlineExceeded
	}
	return nil
}

func TestParentNoteWaitDeadlineStopsWatch(t *testing.T) {
	f, _, child, _ := parentNoteFixture(t, false)
	note := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "note", Kind: "question", Text: "x"}, 201)
	p := observeParentWait(t, f)
	var expire context.CancelFunc
	f.srv.parentWaitTimeout = func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		child, cancel := context.WithCancel(ctx)
		expire = cancel
		return parentDeadlineContext{child}, cancel
	}
	result, cancel := startParentWait(f, child, note.Name)
	defer cancel()
	stream := receiveParentWait(t, p.started)
	expire()
	w := receiveParentWait(t, result)
	if w.Code != 200 || w.Body.String() != "{\"state\":\"waiting\"}\n" || !stream.IsStopped() || len(f.srv.parentWaits.held) != 0 || p.watches.Load() != 1 || p.lists.Load() != 1 {
		t.Fatal("deadline did not return waiting and release the one watch")
	}
}

// A real API-server watch must replay an answer created after the list but
// before Watch. A fake stream alone cannot prove resourceVersion semantics.
func TestEnvtestParentNoteWaitReplaysReplyAfterList(t *testing.T) {
	f, parent, child, _ := parentNoteFixture(t, false)
	note := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "note", Kind: "question", Text: "x"}, 201)
	answer := answerParentTest(t, f, parent, note.Name, "answer", "replayed answer", 201)
	q, err := f.srv.Inbox.Get(context.Background(), note.Name)
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.srv.Inbox.Get(context.Background(), answer.Name)
	if err != nil {
		t.Fatal(err)
	}
	live, err := client.NewWithWatch(envConfig(t), client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "parent-wait-"}}
	if err := live.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	store := &inbox.KubeStore{Client: live, Live: live, Watcher: live, Namespace: ns.Name}
	q, _, err = store.CreateParentNote(ctx, q.Spec)
	if err != nil {
		t.Fatal(err)
	}
	replies, version, err := store.ParentReplies(ctx, q)
	if err != nil || len(replies) != 0 || version == "" {
		t.Fatal("initial live list has no usable version", err)
	}
	ref := inbox.Ref(q)
	r.Spec.InReplyTo = &ref
	r, _, err = store.CreateParentNote(ctx, r.Spec)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Inbox: store}
	got, err := s.awaitParentReply(ctx, q, version)
	if err != nil || got == nil || got.UID != r.UID || got.Spec.Text != r.Spec.Text {
		t.Fatal("watch missed the answer stored between list and watch", err)
	}
}

func TestParentNoteAnswerAndStoredWait(t *testing.T) {
	f, parent, child, _ := parentNoteFixture(t, true)
	note := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "question", Kind: "question", Text: "private question"}, 201)
	path := apiv1.ParentNotesPath + "/" + note.Name
	w := f.do(http.MethodPost, path+"/answer", parent, map[string]string{"key": "answer", "text": "private answer"})
	if w.Code != http.StatusCreated {
		t.Fatalf("answer status %d, want 201", w.Code)
	}
	w = f.do(http.MethodGet, path+"/wait?seconds=900", child, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("stored wait status %d, want 200", w.Code)
	}
	got := decode[apiv1.ParentNoteWaitResult](t, w)
	if got.State != "answered" || got.Reply == nil || got.Reply.Text != "private answer" || got.Reply.Kind != "reply" || got.Reply.InReplyTo == nil || got.Reply.InReplyTo.Name != note.Name || got.Reply.InReplyTo.UID != note.UID || got.Reply.Sender != note.Recipient || got.Reply.Recipient != note.Sender {
		t.Fatal("answer lost its server-derived binding or text")
	}
	// No watcher is wired: an already-stored reply must need none, even when
	// this session already holds every available slot.
	for range parentHeldWaitLimit {
		if !f.srv.parentWaits.acquire(sessionIdentity(f.session("child"))) {
			t.Fatal("fixture could not reserve slots")
		}
	}
	w = f.do(http.MethodGet, path+"/wait?seconds=900", child, nil)
	if w.Code != 200 || !reflect.DeepEqual(decode[apiv1.ParentNoteWaitResult](t, w), got) {
		t.Fatal("repeat wait did not return stored answer at once")
	}
}

func TestParentNoteAnswerRetryConflictAndKinds(t *testing.T) {
	for _, kind := range []string{"question", "result", "blocker"} {
		t.Run(kind, func(t *testing.T) {
			f, parent, child, _ := parentNoteFixture(t, true)
			note := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "note", Kind: kind, Text: "question"}, 201)
			answer := answerParentTest(t, f, parent, note.Name, "answer", "  original answer\né\x00", 201)
			resumeInboxPod(t, f, parent)
			if got := answerParentTest(t, f, parent, note.Name, "answer", "  original answer\né\x00", 200); !reflect.DeepEqual(got, answer) {
				t.Fatal("retry changed the stored answer receipt")
			}
			answerParentTest(t, f, parent, note.Name, "answer", "different text", 409)
			answerParentTest(t, f, parent, note.Name, "another-key", "different text", 409)
			stored, err := f.srv.Inbox.Get(context.Background(), answer.Name)
			if err != nil || stored.Spec.Text != "  original answer\né\x00" || countMessages(t, f) != 2 {
				t.Fatal("conflict replaced or duplicated the answer")
			}
			original, err := f.srv.Inbox.Get(context.Background(), note.Name)
			if err != nil || !reflect.DeepEqual(stored.Spec.Provenance, original.Spec.Provenance) || stored.Spec.Repository != f.session("parent").Spec.Repo || stored.Spec.Provider != string(f.session("parent").Spec.Agent) {
				t.Fatal("answer lost writer attribution or child provenance")
			}
		})
	}
}

func TestParentNoteAnswerWaitHideOtherPartiesAndPeerRecords(t *testing.T) {
	f, parent, child, stranger := parentNoteFixture(t, false)
	note := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "note", Kind: "question", Text: "private question"}, 201)
	for _, tc := range []struct{ method, suffix, token string }{
		{"POST", "/answer", child}, {"POST", "/answer", stranger},
		{"GET", "/wait?seconds=1", parent}, {"GET", "/wait?seconds=1", stranger},
	} {
		w := f.do(tc.method, apiv1.ParentNotesPath+"/"+note.Name+tc.suffix, tc.token, apiv1.AnswerParentRequest{Key: "a", Text: "x"})
		missing := f.do(tc.method, apiv1.ParentNotesPath+"/"+missingParentNoteName+tc.suffix, tc.token, apiv1.AnswerParentRequest{Key: "a", Text: "x"})
		wantError(t, w, 404, apiv1.CodeNotFound)
		if w.Body.String() != missing.Body.String() {
			t.Fatal("hidden party learned that a note exists")
		}
	}
	// Remove only the route marker: a valid peer question with these parties
	// still cannot be answered or waited on through the parent route.
	stored, err := f.srv.Inbox.Get(context.Background(), note.Name)
	if err != nil {
		t.Fatal(err)
	}
	delete(stored.Labels, inbox.ParentNoteLabel)
	if err := f.c.Update(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, suffix, token string }{{"POST", "/answer", parent}, {"GET", "/wait?seconds=1", child}} {
		w := f.do(tc.method, apiv1.ParentNotesPath+"/"+note.Name+tc.suffix, tc.token, apiv1.AnswerParentRequest{Key: "a", Text: "x"})
		missing := f.do(tc.method, apiv1.ParentNotesPath+"/"+missingParentNoteName+tc.suffix, tc.token, nil)
		wantError(t, w, 404, apiv1.CodeNotFound)
		if w.Body.String() != missing.Body.String() {
			t.Fatal("parent route exposed a peer record")
		}
	}
	if countMessages(t, f) != 1 {
		t.Fatal("hidden operation stored a reply")
	}
}

func TestParentNoteAnswerIntentAndWaitBounds(t *testing.T) {
	f, parent, child, _ := parentNoteFixture(t, false)
	note := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "note", Kind: "question", Text: "x"}, 201)
	path := apiv1.ParentNotesPath + "/" + note.Name
	for _, body := range []string{
		`{"key":"a","text":"x","inReplyTo":{"name":"other"}}`,
		`{"key":"a","text":"x","recipient":{}}`, `{"key":"a","text":"x","sender":{}}`,
		`{"key":"a","text":"x","kind":"reply"}`, `{"key":"a","text":"x","project":"other"}`,
		`{"key":"bad/key","text":"x"}`, `{"key":"a","text":""}`, `null`, `{} {}`,
		"{\"key\":\"a\",\"text\":\"" + string([]byte{255}) + "\"}",
		`{"key":"a","text":"` + strings.Repeat("é", 8193) + `"}`, strings.Repeat(" ", inbox.MaxRequestBytes+1),
	} {
		wantError(t, f.do("POST", path+"/answer", parent, body), 400, apiv1.CodeBadRequest)
	}
	for _, query := range []string{"", "?seconds=0", "?seconds=901", "?seconds=-1", "?seconds=1.5", "?seconds=one", "?seconds=1&seconds=2", "?seconds=1&project=x", "?seconds=%zz"} {
		wantError(t, f.do("GET", path+"/wait"+query, child, nil), 400, apiv1.CodeBadRequest)
	}
	if countMessages(t, f) != 1 {
		t.Fatal("invalid intent wrote an answer")
	}
	answerParentTest(t, f, parent, note.Name, "maximum", strings.Repeat("\x00", inbox.MaxTextBytes), 201)
}

func expiredParentWait(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
	return context.WithDeadline(ctx, time.Now().Add(-time.Second))
}

func TestParentNoteWaitExpiredDeadline(t *testing.T) {
	f, _, child, _ := parentNoteFixture(t, false)
	note := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "note", Kind: "question", Text: "PRIVATE_QUESTION"}, 201)
	f.srv.parentWaitTimeout = expiredParentWait
	for _, seconds := range []string{"1", "900"} {
		w := f.do("GET", apiv1.ParentNotesPath+"/"+note.Name+"/wait?seconds="+seconds, child, nil)
		if w.Code != 200 || w.Body.String() != "{\"state\":\"waiting\"}\n" {
			t.Fatalf("expired wait returned %d %s", w.Code, w.Body.String())
		}
	}
	if len(f.srv.parentWaits.held) != 0 {
		t.Fatal("deadline leaked a wait slot")
	}
}

type parentWaitProbe struct {
	started chan *watch.RaceFreeFakeWatcher
	lists   atomic.Int32
	watches atomic.Int32
}

func observeParentWait(t *testing.T, f *fixture) *parentWaitProbe {
	t.Helper()
	p := &parentWaitProbe{started: make(chan *watch.RaceFreeFakeWatcher, 16)}
	f.srv.Inbox.Live = interceptor.NewClient(f.c, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, obj client.ObjectList, opts ...client.ListOption) error {
		err := c.List(ctx, obj, opts...)
		if list, ok := obj.(*coord.InboxMessageList); ok {
			p.lists.Add(1)
			list.ResourceVersion = "42"
		}
		return err
	}})
	f.srv.Inbox.Watcher = interceptor.NewClient(f.c, interceptor.Funcs{Watch: func(_ context.Context, _ client.WithWatch, obj client.ObjectList, opts ...client.ListOption) (watch.Interface, error) {
		var o client.ListOptions
		o.ApplyOptions(opts)
		if _, ok := obj.(*coord.InboxMessageList); !ok || o.Namespace != f.srv.Inbox.Namespace || o.Raw == nil || o.Raw.ResourceVersion != "42" || !strings.Contains(o.LabelSelector.String(), inbox.ParentNoteLabel+"=true") || !strings.Contains(o.LabelSelector.String(), inbox.InReplyToLabel+"=") {
			t.Error("watch lost the namespace, parent marker, note binding or list version")
		}
		p.watches.Add(1)
		stream := watch.NewRaceFreeFake()
		p.started <- stream
		return stream, nil
	}})
	return p
}

func startParentWait(f *fixture, token, name string) (<-chan *httptest.ResponseRecorder, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan *httptest.ResponseRecorder, 1)
	r := httptest.NewRequest("GET", apiv1.ParentNotesPath+"/"+name+"/wait?seconds=900", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+token)
	go func() {
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		result <- w
	}()
	return result, cancel
}

func receiveParentWait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case got := <-ch:
		return got
	case <-timer.C:
		t.Fatal("wait did not reach the next event")
		var zero T
		return zero
	}
}

func TestParentNoteWaitNinthHeldAndCancellation(t *testing.T) {
	f, _, child, _ := parentNoteFixture(t, false)
	note := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "note", Kind: "question", Text: "x"}, 201)
	p := observeParentWait(t, f)
	var results []<-chan *httptest.ResponseRecorder
	var cancels []context.CancelFunc
	var streams []*watch.RaceFreeFakeWatcher
	for range parentHeldWaitLimit {
		result, cancel := startParentWait(f, child, note.Name)
		t.Cleanup(cancel)
		results, cancels = append(results, result), append(cancels, cancel)
		streams = append(streams, receiveParentWait(t, p.started))
	}
	w := f.do("GET", apiv1.ParentNotesPath+"/"+note.Name+"/wait?seconds=1", child, nil)
	wantError(t, w, 429, apiv1.CodeLimitExceeded)
	if p.watches.Load() != 8 || p.lists.Load() != 9 {
		t.Fatal("held requests polled or ninth request opened a watch")
	}
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
	// The next wait can take a released slot.
	result, cancel := startParentWait(f, child, note.Name)
	defer cancel()
	receiveParentWait(t, p.started).Stop()
	wantError(t, receiveParentWait(t, result), 503, apiv1.CodeUnavailable)
}

func TestParentNoteWaitWatchClosesOrErrorsWithoutRetry(t *testing.T) {
	for _, closeStream := range []bool{true, false} {
		f, _, child, _ := parentNoteFixture(t, false)
		note := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "note", Kind: "question", Text: "x"}, 201)
		p := observeParentWait(t, f)
		result, cancel := startParentWait(f, child, note.Name)
		defer cancel()
		stream := receiveParentWait(t, p.started)
		if closeStream {
			stream.Stop()
		} else {
			stream.Error(&metav1.Status{Message: "PRIVATE_WATCH_ERROR"})
		}
		w := receiveParentWait(t, result)
		wantError(t, w, 503, apiv1.CodeUnavailable)
		if !stream.IsStopped() || p.watches.Load() != 1 || p.lists.Load() != 1 || strings.Contains(w.Body.String(), "PRIVATE_WATCH_ERROR") || len(f.srv.parentWaits.held) != 0 {
			t.Fatal("failed watch leaked, reconnected, polled or exposed its error")
		}
	}
}

func TestParentNoteWaitRechecksPodBeforeAnswer(t *testing.T) {
	f, parent, child, _ := parentNoteFixture(t, false)
	note := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "note", Kind: "question", Text: "x"}, 201)
	p := observeParentWait(t, f)
	result, cancel := startParentWait(f, child, note.Name)
	defer cancel()
	stream := receiveParentWait(t, p.started)
	answer := answerParentTest(t, f, parent, note.Name, "answer", "PRIVATE_ANSWER", 201)
	stored, err := f.srv.Inbox.Get(context.Background(), answer.Name)
	if err != nil {
		t.Fatal(err)
	}
	resumeInboxPod(t, f, child)
	stream.Add(stored)
	w := receiveParentWait(t, result)
	wantError(t, w, 404, apiv1.CodeNotFound)
	missing := f.do("GET", apiv1.ParentNotesPath+"/"+missingParentNoteName+"/wait?seconds=1", child, nil)
	if w.Body.String() != missing.Body.String() {
		t.Fatal("changed pod received private answer or a different hidden body")
	}
}

func TestParentNoteWaitOutlivesServerTimeouts(t *testing.T) {
	f, parent, child, _ := parentNoteFixture(t, false)
	note := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "note", Kind: "question", Text: "x"}, 201)
	p := observeParentWait(t, f)
	server := httptest.NewUnstartedServer(f.h)
	server.Config.ReadTimeout, server.Config.WriteTimeout = 200*time.Millisecond, 200*time.Millisecond
	server.Start()
	defer server.Close()
	request, err := http.NewRequest("GET", server.URL+apiv1.ParentNotesPath+"/"+note.Name+"/wait?seconds=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+child)
	result := make(chan error, 1)
	go func() {
		c := &http.Client{Timeout: 2 * time.Second}
		response, err := c.Do(request)
		if err != nil {
			result <- err
			return
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(response.Body)
		if err == nil && (response.StatusCode != 200 || !strings.Contains(string(body), "PRIVATE_ANSWER")) {
			err = fmt.Errorf("held response status %d or missing answer", response.StatusCode)
		}
		result <- err
	}()
	stream := receiveParentWait(t, p.started)
	// Only this transport test uses real time: 250 ms total, past both 200 ms
	// server deadlines. All other deadline tests inject their wait contexts.
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	answer := answerParentTest(t, f, parent, note.Name, "answer", "PRIVATE_ANSWER", 201)
	stored, err := f.srv.Inbox.Get(context.Background(), answer.Name)
	if err != nil {
		t.Fatal(err)
	}
	stream.Add(stored)
	if err := receiveParentWait(t, result); err != nil {
		t.Fatal(err)
	}
	if !stream.IsStopped() || p.watches.Load() != 1 {
		t.Fatal("completed wait leaked or repeated its watch")
	}
}

func TestParentNoteAnswerLimitsAndSchema(t *testing.T) {
	for _, failure := range []string{"sender", "project", "schema"} {
		t.Run(failure, func(t *testing.T) {
			f, parent, child, _ := parentNoteFixture(t, false)
			note := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "note", Kind: "question", Text: "x"}, 201)
			status := 429
			switch failure {
			case "sender":
				answerParentTest(t, f, parent, note.Name, "first", "x", 201)
				note = tellParentNote(t, f, child, apiv1.TellParentRequest{Key: "next", Kind: "question", Text: "x"}, 201)
				f.srv.Inbox.Limits.PerSession = 1
			case "project":
				f.srv.Inbox.Limits.PerProject = 1
			case "schema":
				status = 503
				f.srv.Inbox.Client = interceptor.NewClient(f.srv.Inbox.Client.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if err := c.Create(ctx, obj, opts...); err != nil {
						return err
					}
					if u, ok := obj.(*unstructured.Unstructured); ok {
						unstructured.RemoveNestedField(u.Object, "spec", "inReplyTo")
					}
					return nil
				}})
			}
			before := countMessages(t, f)
			answerParentTest(t, f, parent, note.Name, "refused", "PRIVATE_ANSWER", status)
			if countMessages(t, f) != before {
				t.Fatal("refusal stored a reply")
			}
		})
	}
}
