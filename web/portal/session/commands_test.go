package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

type commandMemory struct {
	mu           sync.Mutex
	reservations map[string]commandReservation
	terminals    map[string]apiv1.PortalMessageReceipt
	reads        int
	reserves     int
}

func (m *commandMemory) key(h Handle, id string) string { return fmt.Sprintf("%x/%s", h, id) }
func (m *commandMemory) reserveCommand(_ context.Context, h Handle, w commandReservation) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reserves++
	k := m.key(h, w.RequestID)
	if old, ok := m.reservations[k]; ok {
		if old.SessionName != w.SessionName || old.SessionUID != w.SessionUID || old.Digest != w.Digest {
			return false, portal.ErrDenied
		}
		return false, nil
	}
	w.ExpiresAt = time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	m.reservations[k] = w
	return true, nil
}
func (m *commandMemory) readCommand(_ context.Context, h Handle, id string) (commandReservation, *apiv1.PortalMessageReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(h, id)
	m.reads++
	r, ok := m.reservations[k]
	if !ok {
		return r, nil, portal.ErrDenied
	}
	if x, ok := m.terminals[k]; ok {
		return r, &x, nil
	}
	return r, nil, nil
}
func (m *commandMemory) finishCommand(_ context.Context, h Handle, r commandReservation, x apiv1.PortalMessageReceipt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(h, r.RequestID)
	if _, ok := m.reservations[k]; !ok {
		return portal.ErrDenied
	}
	m.terminals[k] = x
	return nil
}

type commandTransportFake struct {
	mu       sync.Mutex
	requests []apiv1.PortalMessageRequest
	fail     bool
}

func (f *commandTransportFake) send(_ context.Context, r apiv1.PortalMessageRequest) (apiv1.PortalMessageReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	if f.fail {
		return apiv1.PortalMessageReceipt{}, errors.New("synthetic failure")
	}
	return apiv1.PortalMessageReceipt{RequestID: r.RequestID, SessionName: r.SessionName, SessionUID: r.SessionUID, ResolvedPodUID: "pod-a", ObservedAt: time.Date(2026, 10, 23, 16, 0, 0, 0, time.UTC), TransportStatus: apiv1.PortalTransportDelivered, Reason: "delivered"}, nil
}
func (f *commandTransportFake) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.requests) }

func commandFixture(t *testing.T) (*authFixture, *commandHandler, *commandMemory, *commandTransportFake, url.Values) {
	t.Helper()
	f := setupAuth(t)
	f.login(t)
	store := &commandMemory{reservations: map[string]commandReservation{}, terminals: map[string]apiv1.PortalMessageReceipt{}}
	transport := &commandTransportFake{}
	authorizer := currentFixture(f, func(*http.Request) (*http.Response, error) { return userInfoResponse(currentAllow), nil })
	handler := &commandHandler{engine: f.e, authorizer: authorizer, receipts: store, transport: transport, now: func() time.Time { return f.now }, render: func(r apiv1.PortalMessageReceipt) ([]byte, error) { return []byte(string(r.TransportStatus)), nil }}
	key, _ := requestHandle(f.request("GET", portalMessagesPath, ""), sessionCookie)
	handler.scope = &actionScope{Targets: map[string]string{"uid-a": "agent-a"}}
	values := url.Values{"csrf": {f.store.sessions[key].CSRF}, "requestID": {"request-0000000001"}, "session": {"agent-a"}, "sessionUID": {"uid-a"}, "text": {"SYNTHETIC MESSAGE"}}
	return f, handler, store, transport, values
}
func commandPost(f *authFixture, h *commandHandler, v url.Values, origin string) *httptest.ResponseRecorder {
	r := f.request("POST", portalMessagesPath, v.Encode())
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Each synthetic user gets a separately verified login and access token. The
// UserInfo fixture selects claims by that token, just as the issuer does.
func actionLogin(t *testing.T, f *authFixture, subject string) Record {
	t.Helper()
	f.subject = subject
	f.login(t)
	_, record, err := f.e.current(f.request(http.MethodGet, SessionsPath, ""))
	if err != nil || record.Principal.Subject != subject {
		t.Fatal("synthetic action login failed")
	}
	return record
}

func actionGroupAuthorizer(f *authFixture, records map[string]Record, checks map[string]int) *CurrentAuthorizer {
	return currentFixture(f, func(req *http.Request) (*http.Response, error) {
		for subject, record := range records {
			if req.Header.Get("Authorization") == "Bearer "+record.accessToken {
				checks[subject]++
				return userInfoResponse(fmt.Sprintf(`{"sub":%q,"portal_is_active":true,"portal_is_admin":true}`, subject)), nil
			}
		}
		return userInfoResponse(`{}`), nil
	})
}

func TestCommandsAllowTwoGroupMembersWithPrivateReceipts(t *testing.T) {
	f, h, _, transport, values := commandFixture(t)
	first := actionLogin(t, f, "synthetic-first")
	firstCookie := f.session
	second := actionLogin(t, f, "synthetic-second")
	secondCookie := f.session
	checks := map[string]int{}
	h.authorizer = actionGroupAuthorizer(f, map[string]Record{"synthetic-first": first, "synthetic-second": second}, checks)
	for _, user := range []struct {
		cookie *http.Cookie
		record Record
	}{{firstCookie, first}, {secondCookie, second}} {
		f.session = user.cookie
		values.Set("csrf", user.record.CSRF)
		if w := commandPost(f, h, values, "https://portal.example"); w.Code != http.StatusOK || w.Body.String() != "TransportDelivered" {
			t.Fatalf("admitted member send answered %d", w.Code)
		}
		if checks[user.record.Principal.Subject] != 2 {
			t.Fatal("send did not reauthorize the request's own token before dispatch")
		}
	}
	if transport.count() != 2 || transport.requests[0].ActorRef == transport.requests[1].ActorRef {
		t.Fatal("group members did not get separate attributed sends for the same request ID")
	}
	// A receipt created only in the second browser is private to that session.
	values.Set("requestID", "request-0000000002")
	if w := commandPost(f, h, values, "https://portal.example"); w.Code != http.StatusOK {
		t.Fatal("second receipt fixture failed")
	}
	f.session = firstCookie
	w := httptest.NewRecorder()
	h.ServeHTTP(w, f.request(http.MethodGet, portalMessagesPath+"/request-0000000002", ""))
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "TransportDelivered") {
		t.Fatal("group membership exposed another browser's receipt")
	}
}

func TestActionFormsStayOffWithoutConfiguration(t *testing.T) {
	page := portal.ProductPage{Connected: true, Groups: []portal.ProductGroup{{Rows: []portal.ProductRow{{Messages: &portal.ProductMessages{Draftable: true}}}}}}
	got := withSendForms(page, strings.Repeat("c", 43), nil)
	if got.Groups[0].Rows[0].Messages.Send != nil {
		t.Fatal("unconfigured send displayed a form")
	}
	got = withLaunchForm(page, portal.Principal{Issuer: "https://issuer.example/portal/", Subject: "synthetic-first"}, strings.Repeat("c", 43), nil)
	if got.Launch != nil {
		t.Fatal("unconfigured launch displayed a form")
	}
}

func TestCommandsDeliveryReplayConflictAndUnknown(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			f, h, _, transport, v := commandFixture(t)
			transport.fail = fail
			first := commandPost(f, h, v, "https://portal.example")
			second := commandPost(f, h, v, "https://portal.example")
			want := "TransportDelivered"
			if fail {
				want = "Unknown"
			}
			if first.Code != 200 || second.Code != 200 || first.Body.String() != want || second.Body.String() != want || transport.count() != 1 {
				t.Fatal("delivery or repeat outcome incorrect")
			}
			v.Set("text", "different text")
			if w := commandPost(f, h, v, "https://portal.example"); w.Code == 200 || transport.count() != 1 {
				t.Fatal("conflicting replay executed")
			}
			get := httptest.NewRecorder()
			h.ServeHTTP(get, f.request("GET", portalMessagesPath+"/request-0000000001", ""))
			if get.Code != 200 || get.Body.String() != want {
				t.Fatal("same-user receipt unavailable")
			}
			other := setupAuth(t)
			other.login(t)
			get = httptest.NewRecorder()
			h.ServeHTTP(get, other.request("GET", portalMessagesPath+"/request-0000000001", ""))
			if get.Code == 200 {
				t.Fatal("other browser session read receipt")
			}
		})
	}
}

func TestCommandsSecurityRefusalsBeforeTransport(t *testing.T) {
	for _, mode := range []string{"missing-origin", "wrong-origin", "duplicate-origin", "wrong-csrf", "duplicate-csrf", "extra-field", "control", "url", "revoked", "expired", "missing-group", "removed-group", "revoke-before-send", "group-removed-before-send", "token-replaced-before-send"} {
		t.Run(mode, func(t *testing.T) {
			f, h, _, transport, v := commandFixture(t)
			origin := "https://portal.example"
			switch mode {
			case "missing-origin":
				origin = ""
			case "wrong-origin":
				origin = "https://wrong.example"
			case "wrong-csrf":
				v.Set("csrf", "incorrect")
			case "duplicate-csrf":
				v.Add("csrf", "second")
			case "extra-field":
				v.Set("actorRef", "forged")
			case "control":
				v.Set("text", "\x1b[2J")
			case "url":
				v.Set("text", "https://example.com")
			case "revoked":
				key, _ := requestHandle(f.request("GET", portalMessagesPath, ""), sessionCookie)
				_ = f.store.RevokeSession(context.Background(), key)
			case "expired":
				f.now = f.now.Add(2 * time.Hour)
			case "missing-group":
				h.authorizer = currentFixture(f, func(*http.Request) (*http.Response, error) {
					return userInfoResponse(`{"sub":"synthetic-subject"}`), nil
				})
			case "removed-group":
				h.authorizer = currentFixture(f, func(*http.Request) (*http.Response, error) {
					return userInfoResponse(strings.Replace(currentAllow, `"portal_is_admin":true`, `"portal_is_admin":false`, 1)), nil
				})
			case "revoke-before-send", "group-removed-before-send", "token-replaced-before-send":
				calls := 0
				h.authorizer = currentFixture(f, func(*http.Request) (*http.Response, error) {
					calls++
					if calls == 2 {
						key, _ := requestHandle(f.request("GET", portalMessagesPath, ""), sessionCookie)
						switch mode {
						case "revoke-before-send":
							_ = f.store.RevokeSession(context.Background(), key)
						case "group-removed-before-send":
							return userInfoResponse(strings.Replace(currentAllow, `"portal_is_admin":true`, `"portal_is_admin":false`, 1)), nil
						case "token-replaced-before-send":
							record := f.store.sessions[key]
							record.accessToken = "synthetic-replaced-token"
							f.store.sessions[key] = record
						}
					}
					return userInfoResponse(currentAllow), nil
				})
			}
			var w *httptest.ResponseRecorder
			if mode == "duplicate-origin" {
				r := f.request("POST", portalMessagesPath, v.Encode())
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.Header.Add("Origin", origin)
				r.Header.Add("Origin", origin)
				w = httptest.NewRecorder()
				h.ServeHTTP(w, r)
			} else {
				w = commandPost(f, h, v, origin)
			}
			if w.Code == 200 || transport.count() != 0 || strings.Contains(w.Body.String(), "SYNTHETIC MESSAGE") {
				t.Fatal("security refusal failed")
			}
		})
	}
}

func TestCommandsConcurrentDuplicate(t *testing.T) {
	f, h, _, transport, v := commandFixture(t)
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; results <- commandPost(f, h, v, "https://portal.example") }()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if (<-results).Code != 200 {
			t.Fatal("duplicate request failed")
		}
	}
	if transport.count() != 1 {
		t.Fatal("duplicate dispatched more than once")
	}
}

func TestCommandsEncodedFormTextBounds(t *testing.T) {
	for _, size := range []int{6000, apiv1.MaxMessageBytes, apiv1.MaxMessageBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			f, h, _, transport, values := commandFixture(t)
			text := strings.Repeat("&", size-2) + "<>"
			values.Set("text", text)
			if size == 6000 && len(values.Encode()) >= 2*apiv1.MaxMessageBytes+2048 {
				t.Fatal("fixture should fit the old form cap")
			}
			w := commandPost(f, h, values, "https://portal.example")
			if size <= apiv1.MaxMessageBytes {
				if w.Code != http.StatusOK || transport.count() != 1 || transport.requests[0].Text != text {
					t.Fatal("valid encoded text not dispatched unchanged")
				}
			} else if w.Code == http.StatusOK || transport.count() != 0 {
				t.Fatal("oversize decoded text reached transport")
			}
		})
	}
}

func TestCommandsExplicitProductionScope(t *testing.T) {
	for _, mode := range []string{"issuer", "name", "uid", "allowed"} {
		t.Run(mode, func(t *testing.T) {
			f, h, store, transport, v := commandFixture(t)
			key, _ := requestHandle(f.request("GET", portalMessagesPath, ""), sessionCookie)
			h.scope = &actionScope{Targets: map[string]string{"uid-a": "agent-a"}}
			switch mode {
			case "issuer":
				record := f.store.sessions[key]
				record.Principal.Issuer = "https://other.example/"
				f.store.sessions[key] = record
			case "name":
				v.Set("session", "agent-b")
			case "uid":
				v.Set("sessionUID", "uid-b")
			}
			w := commandPost(f, h, v, "https://portal.example")
			if mode == "allowed" {
				if w.Code != 200 || transport.count() != 1 || len(store.reservations) != 1 {
					t.Fatal("scoped send failed")
				}
				commandPost(f, h, v, "https://portal.example")
				if transport.count() != 1 {
					t.Fatal("duplicate executed")
				}
			} else if w.Code == 200 || transport.count() != 0 || len(store.reservations) != 0 {
				t.Fatal("unscoped send reserved or executed")
			}
		})
	}
}

func TestCommandsNilScopeDeniesBeforeAnyWork(t *testing.T) {
	f, h, store, transport, values := commandFixture(t)
	key, _ := requestHandle(f.request("GET", portalMessagesPath, ""), sessionCookie)
	id := values.Get("requestID")
	store.reservations[store.key(key, id)] = commandReservation{RequestID: id, SessionName: "agent-a", SessionUID: "uid-a"}
	store.terminals[store.key(key, id)] = apiv1.PortalMessageReceipt{RequestID: id, SessionName: "agent-a", SessionUID: "uid-a", TransportStatus: apiv1.PortalTransportDelivered, Reason: "delivered"}
	original := h.authorizer
	calls := 0
	h.authorizer = authorizeFunc(func(ctx context.Context, p portal.Principal, r portal.Resource) error {
		calls++
		return original.Authorize(ctx, p, r)
	})
	h.scope = nil
	post := commandPost(f, h, values, "https://portal.example")
	get := httptest.NewRecorder()
	h.ServeHTTP(get, f.request("GET", portalMessagesPath+"/"+id, ""))
	if post.Code == http.StatusOK || get.Code == http.StatusOK || calls != 0 || len(store.reservations) != 1 || store.reserves != 0 || transport.count() != 0 || store.reads != 0 || strings.Contains(get.Body.String(), "TransportDelivered") {
		t.Fatal("nil scope performed work or exposed a receipt")
	}
	if _, err := h.receipt(context.Background(), key, id); err == nil || store.reads != 0 {
		t.Fatal("direct nil-scope receipt accessed storage")
	}
}
