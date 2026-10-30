package session

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thaynes43/dev-env/web/portal"
)

// Called inside the existing disposable suite; no live endpoint or new fixture
// identity. The finite eight-request batch proves independent-pool contention.
func testSharedLoginAdmission(t *testing.T, admin *pgxpool.Pool, a, b *PostgresStore) {
	ctx := context.Background()
	var fixtureNow time.Time
	if admin.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&fixtureNow) != nil {
		t.Fatal("fixture clock read failed")
	}
	fixedClock := func(time.Time) time.Time { return fixtureNow }
	a.admissionNow, b.admissionNow = fixedClock, fixedClock
	defer func() { a.admissionNow, b.admissionNow = nil, nil }()
	reset := func() {
		t.Helper()
		if _, err := admin.Exec(ctx, "DELETE FROM portal.sessions WHERE handle=$1", loginAdmissionHandle[:]); err != nil {
			t.Fatal("admission fixture reset failed")
		}
	}
	reset()
	defer reset()
	seed := func(s *PostgresStore, offset time.Duration) time.Time {
		t.Helper()
		reset()
		expiry := fixtureNow.Add(offset).UTC().Truncate(time.Microsecond)
		data, err := s.encrypt(s.row("portal_login_admission", loginAdmissionHandle, expiry, [32]byte{}), loginAdmissionPayload{FullAt: expiry})
		if err != nil {
			t.Fatal("fixture encryption failed")
		}
		if _, err := admin.Exec(ctx, "INSERT INTO portal.sessions(handle,generation,version,expires_at,payload) VALUES($1,$2,1,$3,$4)", loginAdmissionHandle[:], s.generation, expiry, data); err != nil {
			t.Fatal("admission fixture seed failed")
		}
		return expiry
	}
	readExpiry := func() time.Time {
		t.Helper()
		var expiry time.Time
		if admin.QueryRow(ctx, "SELECT expires_at FROM portal.sessions WHERE handle=$1", loginAdmissionHandle[:]).Scan(&expiry) != nil {
			t.Fatal("admission fixture read failed")
		}
		return expiry
	}
	t.Run("two-pools-aggregate-burst-and-process-recreation", func(t *testing.T) {
		start := make(chan struct{})
		results := make(chan error, 8)
		for i := 0; i < 8; i++ {
			s := []*PostgresStore{a, b}[i%2]
			go func() { <-start; results <- s.AdmitLogin(ctx) }()
		}
		close(start)
		allowed := 0
		for i := 0; i < 8; i++ {
			err := <-results
			if err == nil {
				allowed++
			} else if !errors.Is(err, portal.ErrUnavailable) {
				t.Fatal("admission returned an unbounded error")
			}
		}
		if allowed != 4 {
			t.Fatalf("independent pools admitted %d starts; want burst four", allowed)
		}
		// A new process/store and independent connection pool must retain debt.
		recreated := fixtureStore(t)
		pool, err := pgxpool.NewWithConfig(ctx, integrationRuntimeConfig(t))
		if err != nil {
			t.Fatal("recreated replica pool failed")
		}
		defer pool.Close()
		recreated.pool = pool
		recreated.admissionNow = fixedClock
		recreated.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
		if !errors.Is(recreated.AdmitLogin(ctx), portal.ErrUnavailable) {
			t.Fatal("replica recreation or application clock reset allowance")
		}
		f := setupAuth(t)
		c := f.e.config
		c.Store = recreated
		engine, err := New(c)
		if err != nil {
			t.Fatal("recreated engine fixture failed")
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, f.request("GET", "/auth/login", ""))
		if w.Code != 503 || w.Header().Get("Location") != "" || w.Header().Get("Set-Cookie") != "" {
			t.Fatal("fresh engine bypassed shared debt")
		}
	})
	t.Run("deterministic-refill", func(t *testing.T) {
		before := seed(a, 3*time.Second)
		if b.AdmitLogin(ctx) != nil || !readExpiry().Equal(before.Add(time.Second)) {
			t.Fatal("partially refilled bucket did not consume exactly one credit")
		}
		if !errors.Is(a.AdmitLogin(ctx), portal.ErrUnavailable) {
			t.Fatal("refill admitted two credits")
		}
		fixtureNow = fixtureNow.Add(time.Second)
		if a.AdmitLogin(ctx) != nil || !readExpiry().Equal(before.Add(2*time.Second)) {
			t.Fatal("database clock did not refill one credit per second")
		}
	})
	t.Run("production-database-clock-overrides-application-clock", func(t *testing.T) {
		reset()
		a.admissionNow = nil
		oldNow := a.now
		a.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
		defer func() { a.admissionNow, a.now = fixedClock, oldNow }()
		var before, after time.Time
		if admin.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&before) != nil || a.AdmitLogin(ctx) != nil || admin.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&after) != nil {
			t.Fatal("database-clock admission fixture failed")
		}
		expiry := readExpiry()
		if expiry.Before(before.Add(time.Second)) || expiry.After(after.Add(time.Second)) {
			t.Fatal("admission did not use database clock after locking")
		}
	})
	t.Run("rotation-fails-closed-until-debt-expires", func(t *testing.T) {
		before := seed(a, 3*time.Second)
		rotated := *b
		rotated.generation = strings.Repeat("h", 32)
		if !errors.Is(rotated.AdmitLogin(ctx), portal.ErrUnavailable) || !readExpiry().Equal(before) {
			t.Fatal("generation rotation reset outstanding debt")
		}
		key := make([]byte, 32)
		key[0] = 1
		newKey, err := newPostgresStore(PostgresConfig{Key: base64.StdEncoding.EncodeToString(key), Generation: a.generation, Now: time.Now, Timeout: time.Second})
		if err != nil {
			t.Fatal("rotated-key fixture failed")
		}
		newKey.pool = b.pool
		newKey.admissionNow = fixedClock
		if !errors.Is(newKey.AdmitLogin(ctx), portal.ErrUnavailable) || !readExpiry().Equal(before) {
			t.Fatal("key rotation reset outstanding debt")
		}
		seed(a, -time.Second)
		rotated.seal = newKey.seal
		if rotated.AdmitLogin(ctx) != nil {
			t.Fatal("fully refilled old-generation row prevented safe rotation")
		}
	})
	t.Run("timeout-and-precommit-failure-do-not-admit", func(t *testing.T) {
		before := seed(a, time.Second)
		tx, err := admin.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatal("fixture lock transaction failed")
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if lockPortalInsert(ctx, tx, "sessions") != nil {
			t.Fatal("fixture admission lock failed")
		}
		limited := *a
		limited.timeout = 25 * time.Millisecond
		if !errors.Is(limited.AdmitLogin(ctx), portal.ErrUnavailable) {
			t.Fatal("lock timeout admitted login")
		}
		if tx.Rollback(ctx) != nil || !readExpiry().Equal(before) {
			t.Fatal("timeout changed admission debt")
		}
		limited.timeout = time.Second
		limited.beforeCommit = func(context.Context) error { return errors.New("synthetic precommit failure") }
		if !errors.Is(limited.AdmitLogin(ctx), portal.ErrUnavailable) || !readExpiry().Equal(before) {
			t.Fatal("precommit failure admitted or persisted new debt")
		}
	})
	t.Run("lost-commit-response-keeps-spent-credit", func(t *testing.T) {
		before := seed(a, time.Second)
		store := fixtureStore(t)
		pool, result := commitLossPool(t)
		store.pool = pool
		store.admissionNow = fixedClock
		if !errors.Is(store.AdmitLogin(ctx), portal.ErrUnavailable) || <-result != 1 {
			t.Fatal("ambiguous admission commit returned success or retried")
		}
		if !readExpiry().Equal(before.Add(time.Second)) {
			t.Fatal("ambiguous commit refunded a consumed credit")
		}
	})
	t.Run("bounded-row-cleanup-and-existing-capacity", func(t *testing.T) {
		seed(a, -time.Hour)
		if a.run(ctx, func(ctx context.Context, tx pgx.Tx) error { return admitPortalInsert(ctx, tx, "sessions") }) != nil {
			t.Fatal("existing bounded cleanup failed")
		}
		var count int
		if admin.QueryRow(ctx, "SELECT count(*) FROM portal.sessions WHERE handle=$1", loginAdmissionHandle[:]).Scan(&count) != nil || count != 0 {
			t.Fatal("fully refilled admission debt was not safe to clean up")
		}
		if a.AdmitLogin(ctx) != nil {
			t.Fatal("cleaned-up fully refilled bucket did not admit")
		}
		if got, err := a.GetSession(ctx, loginAdmissionHandle); err == nil || got.Principal.Subject != "" || got.accessToken != "" {
			t.Fatal("admission row authenticated as a session")
		}
		reset()
		if admin.QueryRow(ctx, "SELECT count(*) FROM portal.sessions").Scan(&count) != nil {
			t.Fatal("capacity fixture read failed")
		}
		_, err := admin.Exec(ctx, `INSERT INTO portal.sessions(handle,generation,version,expires_at,payload)
SELECT decode(lpad(to_hex(n),64,'0'),'hex'),$1,1,clock_timestamp()+interval '1 hour',$2
FROM generate_series(3000,3000+$3-1) n`, a.generation, make([]byte, 28), 256-count)
		if err != nil {
			t.Fatal("capacity fixture seed failed")
		}
		defer func() { _, _ = admin.Exec(ctx, "DELETE FROM portal.sessions WHERE payload=$1", make([]byte, 28)) }()
		if !errors.Is(a.AdmitLogin(ctx), portal.ErrUnavailable) {
			t.Fatal("admission bypassed existing sessions ceiling")
		}
		if admin.QueryRow(ctx, "SELECT count(*) FROM portal.sessions").Scan(&count) != nil || count != 256 {
			t.Fatal("admission changed saturated table cardinality")
		}
	})
}
