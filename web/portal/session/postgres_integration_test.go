package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thaynes43/dev-env/web/portal"
)

// Hosted CI only: this suite refuses non-loopback DBs and uses disposable
// identities. Absence is an explicit skip, never replica/DB evidence.
func integrationPools(t *testing.T) (*pgxpool.Pool, *PostgresStore, *PostgresStore, *fixtureFence) {
	t.Helper()
	dsn := os.Getenv("PORTAL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable PostgreSQL not configured; no DB proof")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "postgres" || u.Hostname() != "localhost" || u.Path != "/dev_env_v2_portal" || u.Query().Get("sslmode") != "verify-full" {
		t.Fatal("integration DB must be disposable loopback with verified TLS")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("fixture admin connection configuration failed")
	}
	t.Cleanup(admin.Close)
	if admin.Ping(ctx) != nil {
		t.Fatal("disposable PostgreSQL unavailable")
	}
	bootstrap := `CREATE ROLE dev_env_v2_portal_migrator NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE dev_env_v2_portal_runtime LOGIN PASSWORD 'synthetic-ci-only-password' NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
REVOKE ALL ON DATABASE dev_env_v2_portal FROM PUBLIC;
GRANT CONNECT ON DATABASE dev_env_v2_portal TO dev_env_v2_portal_runtime;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA portal AUTHORIZATION dev_env_v2_portal_migrator;
CREATE TABLE public.fixture_private (value text);
SET ROLE dev_env_v2_portal_migrator;`
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal("fixture admin acquire failed")
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, bootstrap); err != nil {
		t.Fatal("fixture bootstrap failed")
	}
	migration, err := os.ReadFile("migrations/001_sessions.sql")
	if err != nil {
		t.Fatal("migration fixture absent")
	}
	if _, err = conn.Exec(ctx, string(migration)); err != nil {
		t.Fatal("fixture migration failed")
	}
	if _, err = conn.Exec(ctx, `RESET ROLE; REVOKE ALL ON SCHEMA portal FROM PUBLIC;
GRANT USAGE ON SCHEMA portal TO dev_env_v2_portal_runtime;
GRANT SELECT,INSERT,DELETE ON portal.pending_logins,portal.sessions TO dev_env_v2_portal_runtime;`); err != nil {
		t.Fatal("fixture grant failed")
	}
	fence := &fixtureFence{generation: strings.Repeat("g", 32)}
	newReplica := func() *PostgresStore {
		s := fixtureStore(t)
		s.fence = fence
		c := integrationRuntimeConfig(t)
		pool, err := pgxpool.NewWithConfig(ctx, c)
		if err != nil {
			t.Fatal("fixture runtime pool failed")
		}
		s.pool = pool
		t.Cleanup(s.Close)
		return s
	}
	return admin, newReplica(), newReplica(), fence
}

func integrationRuntimeConfig(t *testing.T) *pgxpool.Config {
	t.Helper()
	u, err := url.Parse(os.Getenv("PORTAL_TEST_DATABASE_URL"))
	if err != nil || u.Hostname() != "localhost" {
		t.Fatal("fixture config must be loopback")
	}
	ca, err := os.ReadFile(u.Query().Get("sslrootcert"))
	if err != nil {
		t.Fatal("fixture CA missing")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("fixture CA invalid")
	}
	c, err := postgresPoolConfig("localhost", "dev_env_v2_portal_runtime", "synthetic-ci-only-password", roots)
	if err != nil {
		t.Fatal("production pool config failed")
	}
	return c
}

func requireSQLState(t *testing.T, err error, want string) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != want {
		t.Fatalf("expected SQLSTATE %s (error type %T)", want, err)
	}
}

func TestPostgresIntegration(t *testing.T) {
	admin, a, b, fence := integrationPools(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	login := Login{State: strings.Repeat("s", 43), Nonce: strings.Repeat("n", 43), Verifier: strings.Repeat("v", 43), ExpiresAt: now.Add(time.Minute)}
	record := Record{Principal: portal.Principal{Issuer: "https://issuer.example/portal/", Subject: "synthetic-subject"}, CSRF: strings.Repeat("c", 43), CreatedAt: now.Add(-time.Second), IdleExpiresAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Minute), accessToken: "SYNTHETIC-TOKEN-CANARY", tokenExpiresAt: now.Add(time.Hour)}
	handle := func(label string) Handle { return sha256.Sum256([]byte(label)) }

	t.Run("wrong-state-does-not-consume-and-two-pools-take-once", func(t *testing.T) {
		h := handle("atomic-login")
		if a.PutLogin(ctx, h, login) != nil {
			t.Fatal("insert login failed")
		}
		if _, err := b.TakeLogin(ctx, h, strings.Repeat("x", 43), now); !errors.Is(err, portal.ErrDenied) {
			t.Fatal("wrong state accepted")
		}
		if !errors.Is(b.PutLogin(ctx, h, login), portal.ErrDenied) {
			t.Fatal("collision replaced login")
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, store := range []*PostgresStore{a, b} {
			go func(s *PostgresStore) { <-start; _, err := s.TakeLogin(ctx, h, login.State, now); results <- err }(store)
		}
		close(start)
		ok, denied := 0, 0
		for i := 0; i < 2; i++ {
			err := <-results
			if err == nil {
				ok++
			} else if errors.Is(err, portal.ErrDenied) {
				denied++
			} else {
				t.Fatal("database atomic consumption failed")
			}
		}
		if ok != 1 || denied != 1 {
			t.Fatal("cross-pool transaction consumed more than once")
		}
	})

	t.Run("insert-only-session-and-cross-pool-revoke", func(t *testing.T) {
		h := handle("session-revoke")
		if a.PutSession(ctx, h, record) != nil {
			t.Fatal("session insert failed")
		}
		changed := record
		changed.accessToken = "replacement-canary"
		if !errors.Is(b.PutSession(ctx, h, changed), portal.ErrDenied) {
			t.Fatal("collision replaced session")
		}
		got, err := b.GetSession(ctx, h)
		if err != nil || got.accessToken != record.accessToken {
			t.Fatal("fresh replica read lost exact session credential")
		}
		if b.RevokeSession(ctx, h) != nil {
			t.Fatal("durable revoke failed")
		}
		if _, err := a.GetSession(ctx, h); !errors.Is(err, portal.ErrDenied) {
			t.Fatal("other replica authorized revoked session")
		}
	})

	t.Run("database-expiry-and-encryption", func(t *testing.T) {
		h := handle("encryption")
		if a.PutSession(ctx, h, record) != nil {
			t.Fatal("insert failed")
		}
		var payload, storedHandle []byte
		if admin.QueryRow(ctx, "SELECT handle,payload FROM portal.sessions WHERE handle=$1", h[:]).Scan(&storedHandle, &payload) != nil {
			t.Fatal("fixture row read failed")
		}
		for _, secret := range []string{record.accessToken, record.CSRF, record.Principal.Subject} {
			if bytes.Contains(payload, []byte(secret)) {
				t.Fatal("plaintext secret in database")
			}
		}
		if !bytes.Equal(storedHandle, h[:]) {
			t.Fatal("handle digest not stored")
		}
		payload[len(payload)-1] ^= 1
		if _, err := admin.Exec(ctx, "UPDATE portal.sessions SET payload=$2 WHERE handle=$1", h[:], payload); err != nil {
			t.Fatal("fixture tamper failed")
		}
		if _, err := b.GetSession(ctx, h); !errors.Is(err, portal.ErrUnavailable) {
			t.Fatal("tampered ciphertext authorized")
		}
		l := login
		// Independent of how long earlier SQL subtests took on the hosted runner.
		l.ExpiresAt = time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
		lh := handle("expired-login")
		if a.PutLogin(ctx, lh, l) != nil {
			t.Fatal("expiry fixture insert failed")
		}
		if _, err := b.TakeLogin(ctx, lh, l.State, l.ExpiresAt); !errors.Is(err, portal.ErrDenied) {
			t.Fatal("expired transaction consumed")
		}
	})

	t.Run("database-clock-overrides-lagging-application-clock", func(t *testing.T) {
		h := handle("db-clock")
		if a.PutSession(ctx, h, record) != nil {
			t.Fatal("clock fixture insert failed")
		}
		if _, err := admin.Exec(ctx, "UPDATE portal.sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE handle=$1", h[:]); err != nil {
			t.Fatal("clock fixture failed")
		}
		a.now = func() time.Time { return now.Add(-time.Hour) }
		defer func() { a.now = time.Now }()
		if _, err := a.GetSession(ctx, h); !errors.Is(err, portal.ErrDenied) {
			t.Fatal("lagging app clock bypassed database expiry")
		}
	})

	t.Run("post-commit-fence-loss-is-unknown-never-replay", func(t *testing.T) {
		// Three checks: before txn, before commit, after commit. Inject witness
		// loss after a real acknowledged commit; caller must receive unavailable.
		fence.calls = 0
		fence.failAt = 3
		h := handle("uncertain-commit")
		if !errors.Is(a.PutSession(ctx, h, record), portal.ErrUnavailable) {
			t.Fatal("uncertain postcommit authority returned success")
		}
		fence.failAt = 0
		var count int
		if admin.QueryRow(ctx, "SELECT count(*) FROM portal.sessions WHERE handle=$1", h[:]).Scan(&count) != nil || count != 1 {
			t.Fatal("fixture did not reach durable commit")
		}
		if !errors.Is(b.PutSession(ctx, h, record), portal.ErrDenied) {
			t.Fatal("uncertain commit overwrote existing session")
		}
	})

	t.Run("recovery-restore-fences-both-tables", func(t *testing.T) {
		h, lh := handle("restored-session"), handle("restored-login")
		if a.PutSession(ctx, h, record) != nil || a.PutLogin(ctx, lh, login) != nil {
			t.Fatal("restore fixtures failed")
		}
		var sessionCipher, loginCipher []byte
		if admin.QueryRow(ctx, "SELECT payload FROM portal.sessions WHERE handle=$1", h[:]).Scan(&sessionCipher) != nil || admin.QueryRow(ctx, "SELECT payload FROM portal.pending_logins WHERE handle=$1", lh[:]).Scan(&loginCipher) != nil {
			t.Fatal("snapshot fixture read failed")
		}
		if b.RevokeSession(ctx, h) != nil {
			t.Fatal("fixture revoke failed")
		}
		if _, err := b.TakeLogin(ctx, lh, login.State, now); err != nil {
			t.Fatal("fixture consume failed")
		}
		// Simulate restored pre-revocation/consumption data with real SQL. This
		// is not a live backup/promotion proof; independent witness is synthetic.
		if _, err := admin.Exec(ctx, "INSERT INTO portal.sessions (handle,generation,version,expires_at,payload) VALUES ($1,$2,1,$3,$4)", h[:], a.generation, record.ExpiresAt, sessionCipher); err != nil {
			t.Fatal("restore fixture failed")
		}
		state := sha256.Sum256([]byte(login.State))
		if _, err := admin.Exec(ctx, "INSERT INTO portal.pending_logins (handle,generation,version,state_digest,expires_at,payload) VALUES ($1,$2,1,$3,$4,$5)", lh[:], a.generation, state[:], login.ExpiresAt, loginCipher); err != nil {
			t.Fatal("restore fixture failed")
		}
		fence.unavailable = true
		if _, err := a.GetSession(ctx, h); !errors.Is(err, portal.ErrUnavailable) {
			t.Fatal("uncertain recovery allowed session")
		}
		if _, err := b.TakeLogin(ctx, lh, login.State, now); !errors.Is(err, portal.ErrUnavailable) {
			t.Fatal("uncertain recovery allowed login")
		}
		fence.unavailable = false
		fence.generation = strings.Repeat("h", 32)
		if _, err := a.GetSession(ctx, h); !errors.Is(err, portal.ErrUnavailable) {
			t.Fatal("old replica generation escaped external fence")
		}
		oldGeneration := b.generation
		b.generation = fence.generation
		if _, err := b.GetSession(ctx, h); !errors.Is(err, portal.ErrDenied) {
			t.Fatal("restored old generation session resurrected")
		}
		if _, err := b.TakeLogin(ctx, lh, login.State, now); !errors.Is(err, portal.ErrDenied) {
			t.Fatal("restored old pending login resurrected")
		}
		b.generation = oldGeneration
		fence.generation = oldGeneration
	})

	t.Run("actual-parallel-callback-and-cross-replica-logout", func(t *testing.T) {
		f := setupAuth(t)
		f.now = now
		f.e.config.Store = a
		f.begin(t)
		other := *f.e
		other.config = f.e.config
		other.config.Store = b
		responses := make(chan *httptest.ResponseRecorder, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, engine := range []*Engine{f.e, &other} {
			wg.Add(1)
			go func(e *Engine) {
				defer wg.Done()
				<-start
				w := httptest.NewRecorder()
				e.ServeHTTP(w, f.callback.Clone(ctx))
				responses <- w
			}(engine)
		}
		close(start)
		wg.Wait()
		close(responses)
		success := 0
		for w := range responses {
			if w.Code == 303 {
				success++
				f.session = findCookie(t, w, sessionCookie)
			} else if w.Code != 403 {
				t.Fatal("parallel callback failed unexpectedly")
			}
		}
		if success != 1 || f.exchanges != 1 {
			t.Fatal("parallel callbacks exchanged twice")
		}
		r := f.request("GET", LandingPath, "")
		h, _ := requestHandle(r, sessionCookie)
		stored, err := b.GetSession(ctx, h)
		if err != nil {
			t.Fatal("second replica did not read session")
		}
		logout := f.request("POST", "/auth/logout", "csrf="+stored.CSRF)
		logout.Header.Set("Origin", "https://portal.example")
		logout.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		other.ServeHTTP(w, logout)
		if w.Code != 200 {
			t.Fatal("second replica logout failed")
		}
		if _, err := f.e.Authenticate(r); !errors.Is(err, portal.ErrDenied) {
			t.Fatal("first replica accepted cross-replica logout")
		}
	})

	t.Run("least-privilege-and-TLS-HBA-negatives", func(t *testing.T) {
		for _, sql := range []string{"UPDATE portal.sessions SET expires_at=expires_at", "UPDATE portal.pending_logins SET expires_at=expires_at", "CREATE TABLE portal.forbidden (v int)", "CREATE SCHEMA forbidden", "CREATE TEMP TABLE forbidden (v int)", "CREATE ROLE forbidden", "SELECT * FROM public.fixture_private", "SET ROLE dev_env_v2_portal_migrator", "TRUNCATE portal.sessions", "TRUNCATE portal.pending_logins"} {
			_, err := a.pool.Exec(ctx, sql)
			requireSQLState(t, err, "42501")
		}
		for _, mode := range []string{"other-database", "plaintext", "wrong-hostname", "untrusted-CA"} {
			c := integrationRuntimeConfig(t)
			switch mode {
			case "other-database":
				c.ConnConfig.Database = "postgres"
			case "plaintext":
				c.ConnConfig.TLSConfig = nil
				c.ConnConfig.Fallbacks = nil
			case "wrong-hostname":
				c.ConnConfig.TLSConfig.ServerName = "wrong.example"
			case "untrusted-CA":
				c.ConnConfig.TLSConfig.RootCAs = nil
			}
			pool, err := pgxpool.NewWithConfig(ctx, c)
			if err != nil {
				t.Fatal("negative pool config invalid before network proof")
			}
			err = pool.Ping(ctx)
			pool.Close()
			if err == nil {
				t.Fatalf("%s connection accepted", mode)
			}
			switch mode {
			case "other-database", "plaintext":
				requireSQLState(t, err, "28000")
			case "wrong-hostname":
				var hostname x509.HostnameError
				if !errors.As(err, &hostname) {
					t.Fatalf("expected x509 hostname failure, got %T", err)
				}
			case "untrusted-CA":
				var authority x509.UnknownAuthorityError
				if !errors.As(err, &authority) {
					t.Fatalf("expected x509 CA failure, got %T", err)
				}
			}
		}
	})

	t.Run("shared-insert-ceiling-and-bounded-expiry-cleanup", func(t *testing.T) {
		// This sequential subtest owns a fresh pending-login fixture. Remove
		// earlier rows so cleanup's arbitrary LIMIT cannot depend on their age.
		if _, err := admin.Exec(ctx, "DELETE FROM portal.pending_logins"); err != nil {
			t.Fatal("pending fixture reset failed")
		}
		fresh := time.Now().UTC().Truncate(time.Microsecond)
		l := login
		l.ExpiresAt = fresh.Add(time.Minute)
		// A finite 256-row capacity fixture, not a load/stress test. No concurrent loop.
		_, err := admin.Exec(ctx, `INSERT INTO portal.pending_logins(handle,generation,version,state_digest,expires_at,payload)
   SELECT decode(lpad(to_hex(i),64,'0'),'hex'),$1,1,$2,$3,$4 FROM generate_series(1000,1255) AS i`, a.generation, make([]byte, 32), l.ExpiresAt, make([]byte, 28))
		if err != nil {
			t.Fatal("bounded capacity fixture failed")
		}
		defer func() { _, _ = admin.Exec(ctx, "DELETE FROM portal.pending_logins WHERE payload=$1", make([]byte, 28)) }()
		if !errors.Is(a.PutLogin(ctx, Handle{199}, l), portal.ErrUnavailable) {
			t.Fatal("shared pending-login cardinality bound bypassed")
		}
		if _, err := admin.Exec(ctx, "UPDATE portal.pending_logins SET expires_at=clock_timestamp()-interval '1 second' WHERE payload=$1", make([]byte, 28)); err != nil {
			t.Fatal("cleanup fixture failed")
		}
		if b.PutLogin(ctx, Handle{200}, l) != nil {
			t.Fatal("bounded cleanup failed to admit legitimate login")
		}
		var remaining int
		if admin.QueryRow(ctx, "SELECT count(*) FROM portal.pending_logins WHERE payload=$1", make([]byte, 28)).Scan(&remaining) != nil || remaining != 224 {
			t.Fatal("cleanup did not remove exactly 32 expired rows")
		}
	})

	t.Run("paused-insert-admission-does-not-block-revoke-or-callback", func(t *testing.T) { testInsertDoesNotBlockRevocation(t, a, b, record) })

	t.Run("actual-lost-commit-response-discards-write-result-and-read-payload", func(t *testing.T) { testActualLostCommitResponse(t, admin, a, record) })

	t.Run("protected-read-witness-count-and-latency", func(t *testing.T) { testProtectedReadCost(t, a) })
	t.Run("physical-asynchronous-failover-loses-acknowledged-deletes", func(t *testing.T) { testDisposableAsyncFailover(t, admin, a, b) })

	t.Run("outage-has-no-fallback", func(t *testing.T) {
		a.Close()
		if _, err := a.GetSession(ctx, handle("outage")); !errors.Is(err, portal.ErrUnavailable) {
			t.Fatal("closed primary store fell back")
		}
	})
}
