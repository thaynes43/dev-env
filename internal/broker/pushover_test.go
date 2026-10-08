package broker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// All credentials in this file are synthetic. Tests send only to httptest
// servers or an injected transport; none can reach the production endpoint.
const (
	pushoverTestToken = "SyntheticAppToken0000000000000"
	pushoverTestUser  = "SyntheticUserKey00000000000000"
)

func TestPushoverRejectsUnsafeConfiguration(t *testing.T) {
	for _, origin := range []string{
		"", "http://console.example", "https:///", "https://user:password@console.example",
		"https://console.example/approvals", "https://console.example?", "https://console.example#",
		"https://console.example/?secret=value", "https://console.example/#fragment",
		"https://console.example/%2f", "https://console.example:invalid",
		"https://" + strings.Repeat("a", pushoverMaxURL) + ".example",
	} {
		t.Run(origin, func(t *testing.T) {
			if _, err := NewPushoverNotifier(t.TempDir(), origin); err == nil {
				t.Fatal("accepted an unsafe or oversized console origin")
			}
		})
	}
	if _, err := NewPushoverNotifier("", "https://console.example"); err == nil {
		t.Fatal("accepted an empty credential directory")
	}
	n, err := NewPushoverNotifier(t.TempDir(), "https://console.example/")
	if err != nil {
		t.Fatal(err)
	}
	if n.consoleOrigin != "https://console.example" || n.endpoint != pushoverAPIURL || n.http.Timeout != pushoverTimeout {
		t.Fatal("production configuration is not fixed and bounded")
	}
}

func TestPushoverNotificationIncludesApprovalLinkAndPriority(t *testing.T) {
	for _, kind := range []v1alpha1.GrantType{v1alpha1.GrantKube, v1alpha1.GrantBreakglass} {
		t.Run(string(kind), func(t *testing.T) {
			var got url.Values
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/1/messages.json" || r.URL.RawQuery != "" {
					t.Error("credentials were not sent as a POST to the messages endpoint")
				}
				if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.Header.Get("Accept") != "application/json" {
					t.Error("wrong request content type")
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				got = r.PostForm
				_, _ = io.WriteString(w, `{"status":1,"request":"synthetic-request"}`)
			}))
			defer server.Close()
			n := newPushoverTestNotifier(t, server)
			g := pushoverTestGrant()
			g.Spec.Type = kind
			if err := n.NotifyPending(context.Background(), g); err != nil {
				t.Fatal(err)
			}
			wantPriority := "0"
			if kind == v1alpha1.GrantBreakglass {
				wantPriority = "1"
			}
			if got.Get("token") != pushoverTestToken || got.Get("user") != pushoverTestUser || got.Get("priority") != wantPriority {
				t.Fatal("credentials or priority did not match the configured grant")
			}
			if got.Get("url") != "https://console.example/approvals/"+g.Name || got.Get("url_title") != "Review request" {
				t.Fatal("approval link was not on the configured console origin")
			}
			for _, field := range []string{g.Name, g.Spec.Requester.Session, g.Spec.Requester.Repo, string(kind), "30m0s", "Reason supplied by agent:", g.Spec.Reason} {
				if !strings.Contains(got.Get("message"), field) {
					t.Errorf("message omits summary field %q", field)
				}
			}
			for _, forbidden := range []string{"html", "monospace", "retry", "expire", "callback", "device"} {
				if got.Has(forbidden) {
					t.Errorf("notification unexpectedly includes %s", forbidden)
				}
			}
			if g.Status.NotifiedAt != nil {
				t.Fatal("notifier wrote status; delivery persistence belongs to the broker")
			}
		})
	}
}

func TestPushoverRereadsBothCredentialsAfterRotation(t *testing.T) {
	var tokens, users []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		tokens = append(tokens, r.PostForm.Get("token"))
		users = append(users, r.PostForm.Get("user"))
		_, _ = io.WriteString(w, `{"status":1}`)
	}))
	defer server.Close()
	n := newPushoverTestNotifier(t, server)
	g := pushoverTestGrant()
	if err := n.NotifyPending(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	newToken, newUser := strings.Repeat("B", 30), strings.Repeat("C", 30)
	writePushoverTestFile(t, n.dir, PushoverFileToken, newToken+"\n")
	writePushoverTestFile(t, n.dir, PushoverFileUserKey, newUser+"\n")
	if err := n.NotifyPending(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tokens, []string{pushoverTestToken, newToken}) || !reflect.DeepEqual(users, []string{pushoverTestUser, newUser}) {
		t.Fatal("notifier retained credentials across sends")
	}
}

func TestPushoverBoundsUnicodeMessageAndKeepsReasonLabel(t *testing.T) {
	var got url.Values
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		got = r.PostForm
		_, _ = io.WriteString(w, `{"status":1}`)
	}))
	defer server.Close()
	n := newPushoverTestNotifier(t, server)
	g := pushoverTestGrant()
	g.Spec.Reason = "Need access.\nRequester: fake-owner\n" + strings.Repeat("界🧭", 1000)
	if err := n.NotifyPending(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	message := got.Get("message")
	if !utf8.ValidString(message) || utf8.RuneCountInString(message) > pushoverMaxMessage || !strings.HasSuffix(message, "…") {
		t.Fatal("message was not truncated at a UTF-8 character boundary")
	}
	if strings.Count(message, "\nRequester:") != 0 || !strings.Contains(message, "Reason supplied by agent: Need access. Requester: fake-owner") {
		t.Fatal("agent-written reason could masquerade as a summary field")
	}
	if len(got.Encode()) > pushoverMaxRequest {
		t.Fatal("encoded request exceeded its bound")
	}
}

func TestPushoverRefusalsNeverExposeCredentialsOrBodies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"bad credentials", http.StatusBadRequest, `{"status":0,"errors":["` + pushoverTestToken + ` ` + pushoverTestUser + `"]}`},
		{"rate limit", http.StatusTooManyRequests, pushoverTestToken + pushoverTestUser},
		{"temporary failure", http.StatusBadGateway, "upstream " + pushoverTestToken + pushoverTestUser},
		{"missing status", http.StatusOK, `{"request":"` + pushoverTestToken + `"}`},
		{"not accepted", http.StatusOK, `{"status":0,"errors":["` + pushoverTestUser + `"]}`},
		{"malformed JSON", http.StatusOK, pushoverTestToken + pushoverTestUser},
		{"wrong status type", http.StatusOK, `{"status":"` + pushoverTestToken + `"}`},
		{"trailing JSON", http.StatusOK, `{"status":1}{"user":"` + pushoverTestUser + `"}`},
		{"oversized response", http.StatusOK, `{"status":1,"token":"` + pushoverTestToken + strings.Repeat("x", pushoverMaxResponse) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			n := newPushoverTestNotifier(t, server)
			assertPushoverSafeError(t, n.NotifyPending(context.Background(), pushoverTestGrant()))
		})
	}
}

func TestPushoverDoesNotFollowRedirects(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forwarded.Add(1)
		_, _ = io.WriteString(w, `{"status":1}`)
	}))
	defer target.Close()
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/1/messages.json", status)
			}))
			defer server.Close()
			n := newPushoverTestNotifier(t, server)
			assertPushoverSafeError(t, n.NotifyPending(context.Background(), pushoverTestGrant()))
		})
	}
	if forwarded.Load() != 0 {
		t.Fatal("redirect forwarded a notification away from the configured endpoint")
	}
}

func TestPushoverCredentialFailuresStayLocalAndSafe(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testing.T, string)
	}{
		{"missing token", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, PushoverFileToken)); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing user key", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, PushoverFileUserKey)); err != nil {
				t.Fatal(err)
			}
		}},
		{"empty", func(t *testing.T, dir string) { writePushoverTestFile(t, dir, PushoverFileToken, "\n") }},
		{"bad characters", func(t *testing.T, dir string) {
			writePushoverTestFile(t, dir, PushoverFileToken, strings.Repeat("!", 30))
		}},
		{"oversized", func(t *testing.T, dir string) {
			writePushoverTestFile(t, dir, PushoverFileToken, strings.Repeat("A", pushoverMaxCredential+1))
		}},
		{"not a regular file", func(t *testing.T, dir string) {
			path := filepath.Join(dir, PushoverFileToken)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = io.WriteString(w, `{"status":1}`)
			}))
			defer server.Close()
			n := newPushoverTestNotifier(t, server)
			tc.edit(t, n.dir)
			assertPushoverSafeError(t, n.NotifyPending(context.Background(), pushoverTestGrant()))
			if requests.Load() != 0 {
				t.Fatal("invalid local credentials reached the API")
			}
		})
	}
}

func TestPushoverTransportErrorsAreSafeAndRequestsHaveDeadline(t *testing.T) {
	n := newPushoverTestNotifier(t, nil)
	n.http.Transport = pushoverTestTransport(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > pushoverTimeout || time.Until(deadline) <= 0 {
			t.Error("request lacks a bounded deadline")
		}
		return nil, errors.New("transport echoed " + pushoverTestToken + " " + pushoverTestUser)
	})
	assertPushoverSafeError(t, n.NotifyPending(context.Background(), pushoverTestGrant()))
}

func TestPushoverResponseReadErrorsAreSafeAndBounded(t *testing.T) {
	n := newPushoverTestNotifier(t, nil)
	var readBound int
	n.http.Transport = pushoverTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: pushoverTestReader(func(b []byte) (int, error) {
				readBound = len(b)
				return 0, errors.New(pushoverTestToken + pushoverTestUser)
			}),
			Header: make(http.Header),
		}, nil
	})
	assertPushoverSafeError(t, n.NotifyPending(context.Background(), pushoverTestGrant()))
	if readBound == 0 || readBound > pushoverMaxResponse+1 {
		t.Fatal("response reader was not bounded")
	}
}

func TestPushoverSerializesSendsAndCancellationDoesNotSend(t *testing.T) {
	n := newPushoverTestNotifier(t, nil)
	started := make(chan struct{})
	finish := make(chan struct{})
	var requests atomic.Int32
	n.http.Transport = pushoverTestTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		close(started)
		select {
		case <-finish:
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"status":1}`)), Header: make(http.Header)}, nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})
	first := make(chan error, 1)
	go func() { first <- n.NotifyPending(context.Background(), pushoverTestGrant()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(finish)
		t.Fatal("first request did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	assertPushoverSafeError(t, n.NotifyPending(ctx, pushoverTestGrant()))
	if requests.Load() != 1 {
		t.Error("canceled second notification reached the transport")
	}
	close(finish)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestPushoverFailureCooldownCannotBeBypassedByAnotherGrant(t *testing.T) {
	n := newPushoverTestNotifier(t, nil)
	clk := clocktesting.NewFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	n.clock = clk
	requests := 0
	var usedTokens []string
	n.http.Transport = pushoverTestTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		usedTokens = append(usedTokens, r.PostForm.Get("token"))
		if requests == 1 {
			return nil, errors.New("synthetic temporary transport failure")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"status":1}`)), Header: make(http.Header)}, nil
	})
	g := pushoverTestGrant()
	assertPushoverSafeError(t, n.NotifyPending(context.Background(), g))
	g.Name = "grant-another"
	assertPushoverSafeError(t, n.NotifyPending(context.Background(), g))
	clk.Step(pushoverRetryInterval - time.Nanosecond)
	assertPushoverSafeError(t, n.NotifyPending(context.Background(), g))
	if requests != 1 {
		t.Fatal("a watcher event bypassed the temporary failure cooldown")
	}
	rotated := strings.Repeat("R", 30)
	writePushoverTestFile(t, n.dir, PushoverFileToken, rotated)
	clk.Step(time.Nanosecond)
	if err := n.NotifyPending(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || usedTokens[1] != rotated {
		t.Fatal("next attempt did not use the rotated credential after five seconds")
	}
	g.Name = "grant-after-success"
	if err := n.NotifyPending(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	if requests != 3 {
		t.Fatal("ordinary successful deliveries were unexpectedly delayed")
	}
}

func TestPushoverPermanentRejectionWaitsForCredentialRotation(t *testing.T) {
	for _, file := range []string{PushoverFileToken, PushoverFileUserKey} {
		t.Run(file, func(t *testing.T) {
			n := newPushoverTestNotifier(t, nil)
			clk := clocktesting.NewFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
			n.clock = clk
			requests := 0
			var sent url.Values
			n.http.Transport = pushoverTestTransport(func(r *http.Request) (*http.Response, error) {
				requests++
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				sent = r.PostForm
				status, body := http.StatusBadRequest, `{"status":0}`
				if requests > 1 {
					status, body = http.StatusOK, `{"status":1}`
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			g := pushoverTestGrant()
			if wait := pushoverRetryAfter(t, n.NotifyPending(context.Background(), g)); wait != pushoverMaxRetry {
				t.Fatalf("permanent rejection wait %s, want %s", wait, pushoverMaxRetry)
			}
			clk.Step(10 * pushoverMaxRetry)
			g.Name = "grant-another"
			if wait := pushoverRetryAfter(t, n.NotifyPending(context.Background(), g)); wait != pushoverMaxRetry || requests != 1 {
				t.Fatal("unchanged rejected credentials reached the API for another grant")
			}
			// Rejection does not cache the raw files: even invalid replacement
			// credentials must be read before any rejection/cooldown check.
			writePushoverTestFile(t, n.dir, file, "invalid")
			assertPushoverSafeError(t, n.NotifyPending(context.Background(), g))
			if requests != 1 {
				t.Fatal("invalid replacement credential reached the API")
			}
			rotated := strings.Repeat("R", 30)
			writePushoverTestFile(t, n.dir, file, rotated)
			if err := n.NotifyPending(context.Background(), g); err != nil {
				t.Fatal(err)
			}
			field := "token"
			if file == PushoverFileUserKey {
				field = "user"
			}
			if requests != 2 || sent.Get(field) != rotated || n.rejected {
				t.Fatal("credential rotation did not clear the rejection")
			}
		})
	}
}

func TestPushoverClassifiesPermanentAndTransientResponses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		permanent bool
	}{
		{"bad request", http.StatusBadRequest, `{"status":0}`, true},
		{"unauthorized", http.StatusUnauthorized, "", true},
		{"forbidden", http.StatusForbidden, "", true},
		{"unprocessable", http.StatusUnprocessableEntity, "", true},
		{"valid status zero", http.StatusOK, `{"status":0}`, true},
		{"request timeout", http.StatusRequestTimeout, "", false},
		{"rate limited", http.StatusTooManyRequests, "", false},
		{"server failure", http.StatusInternalServerError, "", false},
		{"bad gateway", http.StatusBadGateway, "", false},
		{"redirect", http.StatusTemporaryRedirect, "", false},
		{"malformed JSON", http.StatusOK, "invalid", false},
		{"missing status", http.StatusOK, `{}`, false},
		{"null status", http.StatusOK, `{"status":null}`, false},
		{"string status zero", http.StatusOK, `{"status":"0"}`, false},
		{"unexpected status", http.StatusOK, `{"status":2}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newPushoverTestNotifier(t, nil)
			clk := clocktesting.NewFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
			n.clock = clk
			requests := 0
			n.http.Transport = pushoverTestTransport(func(*http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})
			want := pushoverRetryInterval
			if tc.permanent {
				want = pushoverMaxRetry
			}
			if wait := pushoverRetryAfter(t, n.NotifyPending(context.Background(), pushoverTestGrant())); wait != want {
				t.Fatalf("response wait %s, want %s", wait, want)
			}
			clk.Step(10 * pushoverMaxRetry)
			g := pushoverTestGrant()
			g.Name = "grant-another"
			wait := pushoverRetryAfter(t, n.NotifyPending(context.Background(), g))
			if tc.permanent {
				if requests != 1 || wait != pushoverMaxRetry {
					t.Fatal("permanent response was not suppressed globally")
				}
			} else if requests != 2 || wait != 2*pushoverRetryInterval {
				t.Fatal("transient response did not retry with exponential delay")
			}
		})
	}
}

func TestPushoverTransientBackoffDoublesCapsAndResetsOnSuccess(t *testing.T) {
	n := newPushoverTestNotifier(t, nil)
	clk := clocktesting.NewFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	n.clock = clk
	requests, succeeds := 0, false
	n.http.Transport = pushoverTestTransport(func(*http.Request) (*http.Response, error) {
		requests++
		if !succeeds {
			return nil, errors.New("synthetic temporary transport failure")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"status":1}`)), Header: make(http.Header)}, nil
	})
	g := pushoverTestGrant()
	for i, seconds := range []int{5, 10, 20, 40, 80, 160, 300, 300} {
		want := time.Duration(seconds) * time.Second
		if wait := pushoverRetryAfter(t, n.NotifyPending(context.Background(), g)); wait != want || requests != i+1 {
			t.Fatalf("attempt %d: delay %s, want %s; requests %d", i+1, wait, want, requests)
		}
		clk.Step(want - time.Nanosecond)
		g.Name = "grant-watcher"
		if wait := pushoverRetryAfter(t, n.NotifyPending(context.Background(), g)); wait != time.Nanosecond || requests != i+1 {
			t.Fatal("watcher event bypassed exponential cooldown")
		}
		clk.Step(time.Nanosecond)
	}
	succeeds = true
	if err := n.NotifyPending(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	if n.retryDelay != 0 || !n.retryAt.IsZero() {
		t.Fatal("successful delivery retained exponential cooldown")
	}
	succeeds = false
	if wait := pushoverRetryAfter(t, n.NotifyPending(context.Background(), g)); wait != pushoverRetryInterval {
		t.Fatal("next transient failure did not restart at five seconds")
	}
}

func TestPushoverCredentialRotationClearsTransientCooldownImmediately(t *testing.T) {
	n := newPushoverTestNotifier(t, nil)
	clk := clocktesting.NewFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	n.clock = clk
	requests := 0
	n.http.Transport = pushoverTestTransport(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("synthetic temporary transport failure")
	})
	g := pushoverTestGrant()
	if wait := pushoverRetryAfter(t, n.NotifyPending(context.Background(), g)); wait != pushoverRetryInterval {
		t.Fatal("initial transient delay is incorrect")
	}
	clk.Step(pushoverRetryInterval)
	if wait := pushoverRetryAfter(t, n.NotifyPending(context.Background(), g)); wait != 2*pushoverRetryInterval {
		t.Fatal("second transient delay did not double")
	}
	writePushoverTestFile(t, n.dir, PushoverFileUserKey, strings.Repeat("U", 30))
	if wait := pushoverRetryAfter(t, n.NotifyPending(context.Background(), g)); wait != pushoverRetryInterval || requests != 3 {
		t.Fatal("credential rotation did not immediately clear transient cooldown")
	}
}

func TestPushoverRefusesInvalidGrantWithoutSending(t *testing.T) {
	n := newPushoverTestNotifier(t, nil)
	n.http.Transport = pushoverTestTransport(func(*http.Request) (*http.Response, error) {
		t.Error("invalid grant reached the transport")
		return nil, errors.New("unexpected request")
	})
	for _, name := range []string{"", "grant-../other", "grant-a?b", "grant-" + strings.Repeat("a", 60)} {
		g := pushoverTestGrant()
		g.Name = name
		assertPushoverSafeError(t, n.NotifyPending(context.Background(), g))
	}
	assertPushoverSafeError(t, n.NotifyPending(context.Background(), nil))
}

func newPushoverTestNotifier(t *testing.T, server *httptest.Server) *PushoverNotifier {
	t.Helper()
	n, err := NewPushoverNotifier(t.TempDir(), "https://console.example/")
	if err != nil {
		t.Fatal(err)
	}
	writePushoverTestFile(t, n.dir, PushoverFileToken, pushoverTestToken+"\n")
	writePushoverTestFile(t, n.dir, PushoverFileUserKey, pushoverTestUser+"\n")
	if server != nil {
		n.endpoint, n.http = server.URL+"/1/messages.json", server.Client()
	} else {
		// Tests using a transport still receive a URL distinct from production.
		n.endpoint = "https://test.example/1/messages.json"
	}
	return n
}

func pushoverTestGrant() *v1alpha1.AccessGrant {
	return &v1alpha1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant-synthetic"},
		Spec: v1alpha1.AccessGrantSpec{
			Requester: v1alpha1.GrantRequester{Session: "synthetic-session", Repo: "synthetic-repo"},
			Type:      v1alpha1.GrantKube, TTL: metav1.Duration{Duration: 30 * time.Minute},
			Reason: "Inspect the requested workload.",
		},
	}
}

func writePushoverTestFile(t *testing.T, dir, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertPushoverSafeError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a safe notification error")
	}
	if strings.Contains(err.Error(), pushoverTestToken) || strings.Contains(err.Error(), pushoverTestUser) {
		t.Fatal("notification error exposes synthetic credentials")
	}
	if errors.Unwrap(err) != nil {
		t.Fatal("notification error wraps an unsafe underlying error")
	}
}

func pushoverRetryAfter(t *testing.T, err error) time.Duration {
	t.Helper()
	assertPushoverSafeError(t, err)
	var retry interface{ RetryAfter() time.Duration }
	if !errors.As(err, &retry) || retry.RetryAfter() <= 0 {
		t.Fatal("notification error lacks a positive safe retry delay")
	}
	return retry.RetryAfter()
}

type pushoverTestTransport func(*http.Request) (*http.Response, error)

func (t pushoverTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return t(r) }

type pushoverTestReader func([]byte) (int, error)

func (r pushoverTestReader) Read(b []byte) (int, error) { return r(b) }
func (pushoverTestReader) Close() error                 { return nil }
