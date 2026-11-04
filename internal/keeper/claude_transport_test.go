package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	claudeOldRefreshCanary = "old-refresh-synthetic-canary"
	claudeNewRefreshCanary = "new-refresh-synthetic-canary"
	claudeAccessCanary     = "access-synthetic-canary"
)

type claudeCapture struct {
	Method, Path, ContentType string
	Body                      map[string]string
	Calls                     atomic.Int32
}

// claudeTokenServer serves status and body for every request and records the last.
func claudeTokenServer(t *testing.T, status int, body string) (*claudeHTTPRefresh, *claudeCapture) {
	t.Helper()
	cap := &claudeCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.Calls.Add(1)
		cap.Method, cap.Path, cap.ContentType = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		cap.Body = nil
		_ = json.Unmarshal(raw, &cap.Body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	tr := newClaudeHTTPRefresh()
	tr.endpoint = srv.URL + "/v1/oauth/token"
	tr.profilePath = srv.URL + "/api/oauth/profile"
	return tr, cap
}

func okClaudeBody(extra string) string {
	return `{"access_token":"` + claudeAccessCanary + `","expires_in":28800` + extra + `}`
}

func TestClaudeRefreshRequestShape(t *testing.T) {
	tr, cap := claudeTokenServer(t, 200, okClaudeBody(""))
	if _, err := tr.refresh(context.Background(), newSecretValue(claudeOldRefreshCanary), []string{"user:profile", "user:inference"}, claudeNoCommit); err != nil {
		t.Fatal(err)
	}
	if cap.Method != http.MethodPost || cap.Path != "/v1/oauth/token" || cap.ContentType != "application/json" || cap.Calls.Load() != 1 {
		t.Fatalf("request shape: %s %s %s calls=%d", cap.Method, cap.Path, cap.ContentType, cap.Calls.Load())
	}
	want := map[string]string{"grant_type": "refresh_token", "client_id": claudeClientID, "refresh_token": claudeOldRefreshCanary, "scope": "user:profile user:inference"}
	if len(cap.Body) != len(want) {
		t.Fatalf("body members: %d", len(cap.Body))
	}
	for k, v := range want {
		if cap.Body[k] != v {
			t.Fatalf("body member %s differs", k)
		}
	}
	if claudeClientID != "9d1c250a-e61b-44d9-88ed-5944d1962f5e" || claudeTokenURL != "https://platform.claude.com/v1/oauth/token" {
		t.Fatal("pinned endpoint or client id changed")
	}
	// No scopes known: the member is omitted.
	if _, err := tr.refresh(context.Background(), newSecretValue(claudeOldRefreshCanary), nil, claudeNoCommit); err != nil {
		t.Fatal(err)
	}
	if _, ok := cap.Body["scope"]; ok {
		t.Fatal("empty scope was sent")
	}
}

func TestClaudeRefreshRotatedAndAbsentRefreshToken(t *testing.T) {
	full := okClaudeBody(`,"refresh_token":"` + claudeNewRefreshCanary + `","refresh_token_expires_in":2592000,"token_type":"Bearer","scope":"user:profile user:inference","account":{"uuid":"acct-1","email_address":"synthetic@example.invalid"},"organization":{"uuid":"org-1"}`)
	tr, _ := claudeTokenServer(t, 200, full)
	got, err := tr.refresh(context.Background(), newSecretValue(claudeOldRefreshCanary), nil, claudeNoCommit)
	if err != nil {
		t.Fatal(err)
	}
	if got.Access.Reveal() != claudeAccessCanary || got.Refresh.Reveal() != claudeNewRefreshCanary || got.ExpiresIn != 8*time.Hour || got.RefreshExpiresIn != 30*24*time.Hour ||
		strings.Join(got.Scopes, ",") != "user:profile,user:inference" || got.AccountUUID != "acct-1" || got.OrganizationUUID != "org-1" {
		t.Fatal("rotated response decoded wrongly")
	}
	// Absent refresh_token: success, with no replacement (the caller keeps the old one).
	tr, _ = claudeTokenServer(t, 200, okClaudeBody(""))
	got, err = tr.refresh(context.Background(), newSecretValue(claudeOldRefreshCanary), nil, claudeNoCommit)
	if err != nil || !got.Refresh.Empty() || got.RefreshExpiresIn != 0 || got.Scopes != nil || got.AccountUUID != "" {
		t.Fatal("absent refresh_token must succeed with no replacement")
	}
	// An unchanged refresh_token is also fine, unlike the Codex transport.
	tr, _ = claudeTokenServer(t, 200, okClaudeBody(`,"refresh_token":"`+claudeOldRefreshCanary+`"`))
	if got, err = tr.refresh(context.Background(), newSecretValue(claudeOldRefreshCanary), nil, claudeNoCommit); err != nil || got.Refresh.Reveal() != claudeOldRefreshCanary {
		t.Fatal("unchanged refresh_token must be accepted")
	}
}

// An unknown member must not discard a 200: the provider already spent the old
// refresh token. Required fields and duplicate-member refusal still hold.
func TestClaudeRefreshToleratesUnknownMembers(t *testing.T) {
	body := `{"surprise":{"deep":[1,2]},"access_token":"` + claudeAccessCanary + `","token_type":"Bearer","expires_in":28800,"refresh_token":"` + claudeNewRefreshCanary +
		`","account":{"uuid":"acct-1","email_address":"synthetic@example.invalid","phone":"1"},"organization":{"uuid":"org-1","name":"Synthetic Org","role":"x"},"future":true}`
	tr, cap := claudeTokenServer(t, 200, body)
	got, err := tr.refresh(context.Background(), newSecretValue(claudeOldRefreshCanary), nil, claudeNoCommit)
	if err != nil {
		t.Fatalf("unknown members discarded a 200: %v", err)
	}
	if got.Access.Reveal() != claudeAccessCanary || got.Refresh.Reveal() != claudeNewRefreshCanary || got.AccountUUID != "acct-1" || got.OrganizationUUID != "org-1" || got.ExpiresIn != 8*time.Hour {
		t.Fatal("known members decoded wrongly beside unknown ones")
	}
	if cap.Calls.Load() != 1 {
		t.Fatal("more than one request")
	}
	// Unknown members never rescue a missing required one.
	for _, b := range []string{`{"extra":1,"expires_in":28800}`, `{"extra":1,"access_token":"` + claudeAccessCanary + `"}`} {
		tr, _ := claudeTokenServer(t, 200, b)
		if _, err := tr.refresh(context.Background(), newSecretValue(claudeOldRefreshCanary), nil, claudeNoCommit); !errors.Is(err, errClaudeRefresh) {
			t.Fatal("a missing required member was accepted")
		}
	}
}

func TestClaudeRefreshErrorClasses(t *testing.T) {
	huge := okClaudeBody(`,"scope":"` + strings.Repeat("a", 70<<10) + `"`)
	cases := map[string]struct {
		status int
		body   string
	}{
		"bad request":        {400, `{"error":"invalid_grant","detail":"` + claudeOldRefreshCanary + `"}`},
		"server error":       {500, claudeOldRefreshCanary},
		"created":            {201, okClaudeBody("")},
		"not json":           {200, `access ` + claudeAccessCanary},
		"empty":              {200, ``},
		"oversize":           {200, huge},
		"duplicate field":    {200, okClaudeBody(`,"expires_in":10`)},
		"trailing document":  {200, okClaudeBody("") + `{}`},
		"missing access":     {200, `{"expires_in":28800}`},
		"empty access":       {200, `{"access_token":"","expires_in":28800}`},
		"access whitespace":  {200, `{"access_token":"a b","expires_in":28800}`},
		"missing expires":    {200, `{"access_token":"` + claudeAccessCanary + `"}`},
		"zero expires":       {200, `{"access_token":"` + claudeAccessCanary + `","expires_in":0}`},
		"negative expires":   {200, `{"access_token":"` + claudeAccessCanary + `","expires_in":-5}`},
		"huge expires":       {200, `{"access_token":"` + claudeAccessCanary + `","expires_in":99999999999}`},
		"fractional expires": {200, `{"access_token":"` + claudeAccessCanary + `","expires_in":1.5}`},
		"string expires":     {200, `{"access_token":"` + claudeAccessCanary + `","expires_in":"28800"}`},
		"bad refresh":        {200, okClaudeBody(`,"refresh_token":"a b"`)},
		"huge refresh life":  {200, okClaudeBody(`,"refresh_token_expires_in":99999999999`)},
		"empty account uuid": {200, okClaudeBody(`,"account":{"uuid":""}`)},
		"duplicate nested":   {200, okClaudeBody(`,"account":{"uuid":"a","uuid":"b"}`)},
		"empty org uuid":     {200, okClaudeBody(`,"organization":{}`)},
		"blank scope token":  {200, okClaudeBody(`,"scope":"   "`)},
		"too many scopes":    {200, okClaudeBody(`,"scope":"` + strings.TrimSpace(strings.Repeat("s ", 40)) + `"`)},
		"array":              {200, `[]`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tr, cap := claudeTokenServer(t, c.status, c.body)
			got, err := tr.refresh(context.Background(), newSecretValue(claudeOldRefreshCanary), nil, claudeNoCommit)
			if !errors.Is(err, errClaudeRefresh) {
				t.Fatalf("want the fixed error, got %v", err)
			}
			if got.Access.Reveal() != "" || got.Refresh.Reveal() != "" {
				t.Fatal("an error returned material")
			}
			if cap.Calls.Load() != 1 {
				t.Fatalf("requests: %d, a spent token must never be replayed", cap.Calls.Load())
			}
			assertNoClaudeCanary(t, err.Error(), fmt.Sprintf("%v %+v", err, got))
		})
	}
}

func TestClaudeRefreshNoRedirectNoRetryNetworkError(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	var posts atomic.Int32
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	tr := newClaudeHTTPRefresh()
	tr.endpoint = redirector.URL
	if _, err := tr.refresh(context.Background(), newSecretValue(claudeOldRefreshCanary), nil, claudeNoCommit); !errors.Is(err, errClaudeRefresh) {
		t.Fatalf("redirect: %v", err)
	}
	if posts.Load() != 1 || hits.Load() != 0 {
		t.Fatalf("redirect followed or retried: posts=%d target=%d", posts.Load(), hits.Load())
	}
	// A connection that never answers: one request, bounded, fixed error.
	var slow atomic.Int32
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		slow.Add(1)
		_, _ = io.Copy(io.Discard, r.Body) // reading the body lets the server notice the client hanging up
		<-r.Context().Done()
	}))
	defer hang.Close()
	tr = newClaudeHTTPRefresh()
	tr.endpoint = hang.URL
	tr.Client.Timeout = 50 * time.Millisecond
	if _, err := tr.refresh(context.Background(), newSecretValue(claudeOldRefreshCanary), nil, claudeNoCommit); !errors.Is(err, errClaudeRefresh) {
		t.Fatalf("timeout: %v", err)
	}
	if slow.Load() != 1 {
		t.Fatalf("timeout was retried: %d", slow.Load())
	}
	// A closed port is a network error, with the same fixed text.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	tr = newClaudeHTTPRefresh()
	tr.endpoint = deadURL
	if _, err := tr.refresh(context.Background(), newSecretValue(claudeOldRefreshCanary), nil, claudeNoCommit); !errors.Is(err, errClaudeRefresh) {
		t.Fatalf("network error: %v", err)
	}
	// A cancelled context never reaches the server.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tr, cap := claudeTokenServer(t, 200, okClaudeBody(""))
	if _, err := tr.refresh(ctx, newSecretValue(claudeOldRefreshCanary), nil, claudeNoCommit); !errors.Is(err, errClaudeRefresh) || cap.Calls.Load() != 0 {
		t.Fatal("cancelled context reached the provider")
	}
}

// claudeNoCommit is a dispatch commit that always succeeds.
func claudeNoCommit() error { return nil }

func TestClaudeRefreshBudgetCoversRequestAndSave(t *testing.T) {
	if claudeRequestBudget < claudeRequestTime+claudeSaveTimeout || claudeRequestTime > 5*time.Second {
		t.Fatal("budget does not cover the request, the replacement save and a margin")
	}
}

const claudeProfileBody = `{"account":{"uuid":"acct-1","full_name":"Synthetic","email":"synthetic@example.invalid"},"organization":{"uuid":"org-1","name":"Synthetic Org","organization_type":"claude_max","rate_limit_tier":"default_claude_max_20x","extra":{"a":1}}}`

func TestClaudeProfile(t *testing.T) {
	var auth, method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, method = r.Header.Get("Authorization"), r.Method
		_, _ = io.WriteString(w, claudeProfileBody)
	}))
	defer srv.Close()
	tr := newClaudeHTTPRefresh()
	tr.profilePath = srv.URL
	p, err := tr.profile(context.Background(), newSecretValue(claudeAccessCanary))
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet || auth != "Bearer "+claudeAccessCanary {
		t.Fatal("profile request shape")
	}
	if p != (claudeProfile{AccountUUID: "acct-1", OrganizationUUID: "org-1", SubscriptionType: "max", RateLimitTier: "default_claude_max_20x"}) {
		t.Fatalf("profile = %+v", p)
	}
	for in, want := range map[string]string{"claude_pro": "pro", "claude_team": "team", "claude_enterprise": "enterprise", "claude_max": "max", "something_new": "", "": ""} {
		if claudeSubscription(in) != want {
			t.Fatalf("subscription %q", in)
		}
	}
}

func TestClaudeProfileErrors(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"unauthorized":   {401, claudeProfileBody},
		"not json":       {200, claudeAccessCanary},
		"duplicate uuid": {200, `{"account":{"uuid":"a","uuid":"b"},"organization":{"uuid":"o"}}`},
		"no account":     {200, `{"organization":{"uuid":"org-1"}}`},
		"no org":         {200, `{"account":{"uuid":"acct-1"}}`},
		"blank tier":     {200, `{"account":{"uuid":"a"},"organization":{"uuid":"o","rate_limit_tier":"a b"}}`},
		"oversize":       {200, `{"account":{"uuid":"a"},"organization":{"uuid":"o"},"pad":"` + strings.Repeat("x", 70<<10) + `"}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tr, _ := claudeTokenServer(t, c.status, c.body)
			p, err := tr.profile(context.Background(), newSecretValue(claudeAccessCanary))
			if !errors.Is(err, errClaudeProfile) || p != (claudeProfile{}) {
				t.Fatalf("want the fixed error, got %v", err)
			}
			assertNoClaudeCanary(t, err.Error())
		})
	}
	// A redirect must not carry the bearer token to another host.
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	defer target.Close()
	redirector := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusFound))
	defer redirector.Close()
	tr := newClaudeHTTPRefresh()
	tr.profilePath = redirector.URL
	if _, err := tr.profile(context.Background(), newSecretValue(claudeAccessCanary)); !errors.Is(err, errClaudeProfile) || leaked.Load() != 0 {
		t.Fatal("profile followed a redirect")
	}
}

func assertNoClaudeCanary(t *testing.T, texts ...string) {
	t.Helper()
	for _, text := range texts {
		for _, canary := range []string{claudeOldRefreshCanary, claudeNewRefreshCanary, claudeAccessCanary} {
			if strings.Contains(text, canary) {
				t.Fatalf("a credential canary reached text: %.40q", text)
			}
		}
	}
}

type claudeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f claudeRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type claudeFailReader struct{}

func (claudeFailReader) Read([]byte) (int, error) { return 0, errors.New("PRIVATE_SENTINEL") }
func (claudeFailReader) Close() error             { return nil }

func TestClaudeRefreshDispatchBoundaryAndPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name         string
		cancelBefore bool
		roundTrip    claudeRoundTripFunc
		phase, class string
	}{
		{"pre-cancel", true, func(*http.Request) (*http.Response, error) {
			t.Error("dispatched cancelled request")
			return nil, errors.New("PRIVATE_SENTINEL")
		}, "prepare", "not-sent"},
		{"post-cancel", false, func(r *http.Request) (*http.Response, error) { return nil, context.Canceled }, "dispatch", "possibly-spent"},
		{"dial", false, func(*http.Request) (*http.Response, error) { return nil, errors.New("PRIVATE_SENTINEL") }, "dispatch", "possibly-spent"},
		{"status", false, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("PRIVATE_SENTINEL")), Header: make(http.Header)}, nil
		}, "response", "status"},
		{"read", false, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: claudeFailReader{}, Header: make(http.Header)}, nil
		}, "response", "read"},
		{"parse", false, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("PRIVATE_SENTINEL")), Header: make(http.Header)}, nil
		}, "response", "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelBefore {
				cancel()
			}
			tr := newClaudeHTTPRefresh()
			tr.endpoint = "https://example.invalid/PRIVATE_SENTINEL"
			tr.Client = &http.Client{Transport: tc.roundTrip}
			_, err := tr.refresh(ctx, newSecretValue("PRIVATE_SENTINEL"), nil, claudeNoCommit)
			if err == nil {
				t.Fatal("expected failure")
			}
			phase, class := claudeFailureLabels(err)
			if phase != tc.phase || class != tc.class || errors.Is(err, errClaudeNotSent) != tc.cancelBefore {
				t.Fatalf("classification %s/%s", phase, class)
			}
			if strings.Contains(err.Error()+phase+class, "PRIVATE_SENTINEL") {
				t.Fatal("sensitive value escaped")
			}
		})
	}
}

func TestClaudeCancellationAfterDispatchIsPossiblySpent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	tr := newClaudeHTTPRefresh()
	tr.endpoint = "https://example.invalid/token"
	tr.Client = &http.Client{Transport: claudeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return nil, context.Canceled
	})}
	_, err := tr.refresh(ctx, newSecretValue(claudeOldRefreshCanary), nil, claudeNoCommit)
	phase, class := claudeFailureLabels(err)
	if calls != 1 || errors.Is(err, errClaudeNotSent) || phase != "dispatch" || class != "possibly-spent" {
		t.Fatal("post-dispatch cancellation was treated as unsent")
	}
}

// The transport runs the dispatch commit after every check that can prove the
// request unsent and immediately before sending. A failed or missing commit,
// or a context cancelled before it, sends nothing and is errClaudeNotSent.
func TestClaudeRefreshCommitsImmediatelyBeforeDispatch(t *testing.T) {
	t.Run("commit precedes the request", func(t *testing.T) {
		tr, cap := claudeTokenServer(t, 200, okClaudeBody(""))
		commits, seenBefore := 0, int32(-1)
		_, err := tr.refresh(context.Background(), newSecretValue(claudeOldRefreshCanary), nil, func() error {
			commits++
			seenBefore = cap.Calls.Load()
			return nil
		})
		if err != nil || commits != 1 || seenBefore != 0 || cap.Calls.Load() != 1 {
			t.Fatalf("err=%v commits=%d requests before commit=%d after=%d", err, commits, seenBefore, cap.Calls.Load())
		}
	})
	for _, tc := range []struct {
		name   string
		cancel bool
		commit func() error
	}{
		{name: "commit fails", commit: func() error { return errors.New("synthetic commit failure") }},
		{name: "no commit"},
		{name: "cancelled before the commit", cancel: true, commit: func() error {
			t.Error("committed a cancelled request")
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, cap := claudeTokenServer(t, 200, okClaudeBody(""))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			if _, err := tr.refresh(ctx, newSecretValue(claudeOldRefreshCanary), nil, tc.commit); !errors.Is(err, errClaudeNotSent) || cap.Calls.Load() != 0 {
				t.Fatalf("err=%v requests=%d, want not sent and none", err, cap.Calls.Load())
			}
		})
	}
}

// After the commit nothing is provably unsent, so a shutdown that begins then
// does not stop the request; it keeps its own time bound.
func TestClaudeRefreshAfterCommitIgnoresShutdown(t *testing.T) {
	tr, cap := claudeTokenServer(t, 200, okClaudeBody(""))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got, err := tr.refresh(ctx, newSecretValue(claudeOldRefreshCanary), nil, func() error {
		cancel()
		return nil
	})
	if err != nil || cap.Calls.Load() != 1 || got.Access.Reveal() != claudeAccessCanary {
		t.Fatalf("err=%v requests=%d: a committed request was stopped by shutdown", err, cap.Calls.Load())
	}
}
