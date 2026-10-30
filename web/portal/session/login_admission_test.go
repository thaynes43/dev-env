package session

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

func TestSharedLoginBucketFakeClock(t *testing.T) {
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	fullAt := time.Time{}
	for i := 0; i < 4; i++ {
		var ok bool
		fullAt, ok = nextLoginFullAt(now, fullAt)
		if !ok {
			t.Fatal("initial burst rejected")
		}
	}
	for _, advance := range []time.Duration{0, time.Nanosecond, time.Second - time.Nanosecond} {
		if _, ok := nextLoginFullAt(now.Add(advance), fullAt); ok {
			t.Fatal("fifth login admitted before refill")
		}
	}
	for i := 1; i <= 4; i++ {
		at := now.Add(time.Duration(i) * time.Second)
		var ok bool
		fullAt, ok = nextLoginFullAt(at, fullAt)
		if !ok {
			t.Fatal("one-second refill rejected")
		}
		if _, ok := nextLoginFullAt(at, fullAt); ok {
			t.Fatal("sustained rate exceeded one per second")
		}
	}
	if _, ok := nextLoginFullAt(now.Add(-time.Second), fullAt); ok {
		t.Fatal("backwards clock created allowance")
	}
	later := now.Add(time.Hour)
	fullAt = now
	for i := 0; i < 4; i++ {
		var ok bool
		fullAt, ok = nextLoginFullAt(later, fullAt)
		if !ok {
			t.Fatal("fully recovered bucket rejected burst")
		}
	}
	if _, ok := nextLoginFullAt(later, fullAt); ok {
		t.Fatal("idle time accumulated more than burst four")
	}
}

func TestSharedLoginAdmissionHasNoMemoryFallback(t *testing.T) {
	f := setupAuth(t)
	f.e.config.Store = fixtureStore(t) // concrete production type, without a DB
	w := httptest.NewRecorder()
	f.e.ServeHTTP(w, f.request("GET", "/auth/login", ""))
	if w.Code != 503 || w.Header().Get("Location") != "" || len(w.Result().Cookies()) != 0 {
		t.Fatal("unavailable shared admission emitted a login")
	}
	var missing *PostgresStore
	if !errors.Is(missing.AdmitLogin(context.Background()), portal.ErrUnavailable) {
		t.Fatal("nil database store admitted a login")
	}
}

func TestLoginAdmissionCiphertextCannotAuthenticate(t *testing.T) {
	s := fixtureStore(t)
	expiry := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
	data, err := s.encrypt(s.row("portal_login_admission", loginAdmissionHandle, expiry, [32]byte{}), loginAdmissionPayload{FullAt: expiry})
	if err != nil {
		t.Fatal("admission encryption failed")
	}
	var auth sessionPayload
	if s.decrypt(s.row("session", loginAdmissionHandle, expiry, [32]byte{}), data, &auth) == nil {
		t.Fatal("admission ciphertext accepted as an authentication session")
	}
}
