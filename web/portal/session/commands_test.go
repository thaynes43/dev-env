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
}

func (m *commandMemory) key(h Handle, id string) string { return fmt.Sprintf("%x/%s", h, id) }
func (m *commandMemory) reserveCommand(_ context.Context, h Handle, w commandReservation) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	for _, mode := range []string{"missing-origin", "wrong-origin", "duplicate-origin", "wrong-csrf", "duplicate-csrf", "extra-field", "control", "url", "revoked", "removed-group", "revoke-before-send"} {
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
			case "removed-group":
				h.authorizer = currentFixture(f, func(*http.Request) (*http.Response, error) {
					return userInfoResponse(strings.Replace(currentAllow, `"portal_is_admin":true`, `"portal_is_admin":false`, 1)), nil
				})
			case "revoke-before-send":
				calls := 0
				h.authorizer = currentFixture(f, func(*http.Request) (*http.Response, error) {
					calls++
					if calls == 2 {
						key, _ := requestHandle(f.request("GET", portalMessagesPath, ""), sessionCookie)
						_ = f.store.RevokeSession(context.Background(), key)
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
