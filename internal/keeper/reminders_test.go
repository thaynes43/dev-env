package keeper

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/reminders"
)

// reminderPushover is a fake Pushover: it records the form it receives and answers
// with a scripted status. No real credential and no real endpoint are involved.
func reminderPushover(t *testing.T, status int) (*PushoverSender, *atomic.Int32, *url.Values) {
	t.Helper()
	var n atomic.Int32
	var last url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		b, _ := io.ReadAll(r.Body)
		last, _ = url.ParseQuery(string(b))
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	token, user := filepath.Join(dir, "token"), filepath.Join(dir, "user")
	for _, f := range []string{token, user} {
		if err := os.WriteFile(f, []byte("FAKE-"+filepath.Base(f)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &PushoverSender{URL: srv.URL, TokenFile: token, UserFile: user, Client: srv.Client()}, &n, &last
}

func TestReminderSenderClassifiesHowFarARequestGot(t *testing.T) {
	for name, c := range map[string]struct {
		status      int
		notSent     bool
		rejected    bool
		wantNilErr  bool
		wantRequest int32
	}{
		"accepted":       {status: 200, wantNilErr: true, wantRequest: 1},
		"a bad request":  {status: 400, rejected: true, wantRequest: 1},
		"unauthorized":   {status: 401, rejected: true, wantRequest: 1},
		"rate limited":   {status: 429, notSent: true, wantRequest: 1},
		"a server fault": {status: 503, notSent: true, wantRequest: 1},
	} {
		t.Run(name, func(t *testing.T) {
			p, n, form := reminderPushover(t, c.status)
			err := ReminderSender{Pushover: p}.Send(context.Background(), reminders.Message{Title: "T", Body: "B"})
			switch {
			case c.wantNilErr && err != nil:
				t.Fatalf("err %v", err)
			case c.notSent && !errors.Is(err, reminders.ErrNotSent):
				t.Errorf("err %v, want ErrNotSent", err)
			case c.rejected && !errors.Is(err, reminders.ErrRejected):
				t.Errorf("err %v, want ErrRejected", err)
			}
			if err != nil && strings.Contains(err.Error(), "FAKE-") {
				t.Errorf("the error holds a credential: %v", err)
			}
			if n.Load() != c.wantRequest {
				t.Errorf("%d requests", n.Load())
			}
			if (*form).Get("title") != "T" || (*form).Get("message") != "B" || (*form).Get("priority") != "0" {
				t.Errorf("form %v", *form)
			}
		})
	}
}

func TestReminderSenderNoCredentialsIsNotSentAndATransportErrorIsAmbiguous(t *testing.T) {
	missing := &PushoverSender{URL: "http://127.0.0.1:1", TokenFile: filepath.Join(t.TempDir(), "absent"), UserFile: filepath.Join(t.TempDir(), "absent")}
	if err := (ReminderSender{Pushover: missing}).Send(context.Background(), reminders.Message{Title: "T", Body: "B"}); !errors.Is(err, reminders.ErrNotSent) {
		t.Errorf("no credentials: %v", err)
	}
	// A server that closes the connection mid-request: the request may have been accepted.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	defer srv.Close()
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "t"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "u"), []byte("y"), 0o600)
	p := &PushoverSender{URL: srv.URL, TokenFile: filepath.Join(dir, "t"), UserFile: filepath.Join(dir, "u"), Client: srv.Client()}
	err := ReminderSender{Pushover: p}.Send(context.Background(), reminders.Message{Title: "T", Body: "B"})
	if err == nil || errors.Is(err, reminders.ErrNotSent) || errors.Is(err, reminders.ErrRejected) {
		t.Errorf("a transport error must stay ambiguous: %v", err)
	}
}

func TestReminderSenderTruncatesToPushoverLimits(t *testing.T) {
	p, _, form := reminderPushover(t, 200)
	long := make([]byte, 3000)
	for i := range long {
		long[i] = 'x'
	}
	if err := (ReminderSender{Pushover: p}).Send(context.Background(), reminders.Message{Title: string(long), Body: string(long)}); err != nil {
		t.Fatal(err)
	}
	if len((*form).Get("title")) != pushoverTitleMax || len((*form).Get("message")) != pushoverMessageMax {
		t.Errorf("title %d, message %d", len((*form).Get("title")), len((*form).Get("message")))
	}
}

// The real sender path end to end, against a fake HTTP server and a fake
// Kubernetes client: one reminder, one request, however often the scheduler passes.
func TestSchedulerDeliversThroughThePushoverSenderOnce(t *testing.T) {
	p, n, form := reminderPushover(t, 200)
	sch := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(sch).WithStatusSubresource(&v1alpha1.Reminder{}).Build()
	rem := &v1alpha1.Reminder{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: "rem-a"},
		Spec: v1alpha1.ReminderSpec{Title: "Renew the VPN", Body: "pay it", Owner: "session/s", Due: "2026-10-28T13:00", TimeZone: "UTC", Targets: []string{"pushover:default"}}}
	if err := c.Create(context.Background(), rem); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s := &reminders.Scheduler{Client: c, Reader: c, Namespace: "dev-env-system", Sender: ReminderSender{Pushover: p}, Log: logr.Discard(), Now: func() time.Time { return now }}
	s.Tick(context.Background())
	now = now.Add(time.Hour)
	for range 3 {
		s.Tick(context.Background())
	}
	if n.Load() != 1 || (*form).Get("title") != "Renew the VPN" {
		t.Fatalf("%d requests, form %v", n.Load(), *form)
	}
	var got v1alpha1.Reminder
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "dev-env-system", Name: "rem-a"}, &got); err != nil || got.Status.State != v1alpha1.ReminderSent {
		t.Errorf("state %+v %v", got.Status, err)
	}
}

func TestClampUTF8CutsOnARuneBoundaryWithAMarker(t *testing.T) {
	for name, c := range map[string]struct {
		in      string
		max     int
		changed bool
	}{
		"fits exactly":             {strings.Repeat("x", 1024), 1024, false},
		"ascii over":               {strings.Repeat("x", 1025), 1024, true},
		"a 2-byte rune at the cut": {strings.Repeat("x", 1020) + "éééé", 1024, true},
		"a 3-byte rune at the cut": {strings.Repeat("x", 1021) + "日日", 1024, true},
		"a 4-byte rune at the cut": {strings.Repeat("x", 1019) + "😀😀", 1024, true},
		"all multi-byte":           {strings.Repeat("日", 400), 1024, true},
		"title boundary":           {strings.Repeat("x", 247) + "日日", 250, true},
		"title that fits":          {strings.Repeat("x", 247) + "日", 250, false},
	} {
		t.Run(name, func(t *testing.T) {
			got := clampUTF8(c.in, c.max)
			if !utf8.ValidString(got) || len(got) > c.max {
				t.Fatalf("invalid or too long: valid=%v len=%d", utf8.ValidString(got), len(got))
			}
			if c.changed != (got != c.in) || (c.changed && !strings.HasSuffix(got, clampMarker)) {
				t.Errorf("changed=%v marker=%v", got != c.in, strings.HasSuffix(got, clampMarker))
			}
		})
	}
}

// The text that reaches Pushover is valid UTF-8 and within its limits, so a
// split character cannot turn into a 4xx and a failed delivery.
func TestReminderSenderSendsValidUTF8AtTheBoundary(t *testing.T) {
	p, _, form := reminderPushover(t, 200)
	body := strings.Repeat("x", 1022) + "日日"
	title := strings.Repeat("y", 248) + "日日"
	if err := (ReminderSender{Pushover: p}).Send(context.Background(), reminders.Message{Title: title, Body: body}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		name string
		max  int
	}{{"title", pushoverTitleMax}, {"message", pushoverMessageMax}} {
		v := (*form).Get(f.name)
		if !utf8.ValidString(v) || len(v) > f.max || !strings.HasSuffix(v, clampMarker) {
			t.Errorf("%s: valid=%v len=%d suffix=%v", f.name, utf8.ValidString(v), len(v), strings.HasSuffix(v, clampMarker))
		}
	}
}
