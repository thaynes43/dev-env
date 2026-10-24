package session

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thaynes43/dev-env/web/portal"
)

func poolIncarnation(t *testing.T, pool *pgxpool.Pool) Incarnation {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal("fixture identity transaction failed")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	i, err := measureIncarnation(context.Background(), tx)
	if err != nil {
		t.Fatal("runtime role could not measure actual backend")
	}
	return i
}

func testProtectedReadCost(t *testing.T, a *PostgresStore) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	fence, f := newAdmissionFixture(t, poolIncarnation(t, a.pool))
	old := a.fence
	a.fence = fence
	defer func() { a.fence = old }()
	auth := setupAuth(t)
	auth.now = now
	auth.e.config.Store = a
	startedLogin := time.Now()
	auth.login(t)
	f.mu.Lock()
	loginGets := f.calls
	f.mu.Unlock()
	if loginGets != 9 {
		t.Fatal("legitimate login witness request count changed")
	}
	t.Logf("LEGITIMATE_LOGIN_EVIDENCE begin+callback=%s witness_GETs=%d login_starts=1", time.Since(startedLogin), loginGets)
	authorizer := currentFixture(auth, func(*http.Request) (*http.Response, error) { return userInfoResponse(currentAllow), nil })
	authorizer.store = a
	handler := NewAssembly(AssemblyConfig{Session: auth.e, Authorizer: authorizer, Reader: readFunc(func(_ context.Context, r portal.Resource) (portal.Snapshot, error) {
		return portal.Snapshot{Resource: r, State: portal.Verified, UpdatedAt: now, VerifiedAt: now}, nil
	}), FreshFor: time.Minute, Now: func() time.Time { return now }})
	f.mu.Lock()
	f.calls = 0
	f.delays = nil
	f.mu.Unlock()
	started := time.Now()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, auth.request("GET", LandingPath, ""))
	elapsed := time.Since(started)
	f.mu.Lock()
	calls := f.calls
	f.mu.Unlock()
	if w.Code != 200 || calls != 21 {
		t.Fatalf("protected read: status %d, GETs %d, expected 21", w.Code, calls)
	}
	t.Logf("PROTECTED_READ_EVIDENCE witness_GETs=%d elapsed=%s store_reads=7 current_UserInfo=2; synthetic loopback only", calls, elapsed)
	// Three fresh round trips per protected SQL operation, measured serially; this
	// is transport+decode latency, not merely time in the fixture HTTP handler.
	var total time.Duration
	for range 3 {
		started = time.Now()
		if _, err := fence.read(context.Background()); err != nil {
			t.Fatal("measured GET failed")
		}
		total += time.Since(started)
	}
	t.Logf("WITNESS_GET_EVIDENCE samples=3 mean=%s concurrency_cap=2 no polling/cache/retry", total/3)
}

func recoveryControl(t *testing.T, action string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	script := filepath.Join("..", "..", "..", "scripts", "trials", "portal_recovery", "control.sh")
	// Fixed script/action only. Never execute output, DSNs or caller-selected code.
	if err := exec.CommandContext(ctx, "bash", script, action).Run(); err != nil {
		t.Fatalf("disposable recovery %s failed (no subprocess output retained)", action)
	}
}

func testDisposableAsyncFailover(t *testing.T, admin *pgxpool.Pool, a, b *PostgresStore) {
	t.Helper()
	requireHostedProof(t, hostedFailoverInputMissing(), "hosted disposable physical failover not configured; no failover proof")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	login := Login{State: strings.Repeat("s", 43), Nonce: strings.Repeat("n", 43), Verifier: strings.Repeat("v", 43), ExpiresAt: now.Add(5 * time.Minute)}
	record := Record{Principal: portal.Principal{Issuer: "https://issuer.example/portal/", Subject: "synthetic-subject"}, CSRF: strings.Repeat("c", 43), CreatedAt: now, IdleExpiresAt: now.Add(5 * time.Minute), ExpiresAt: now.Add(5 * time.Minute), accessToken: "SYNTHETIC-FAILOVER-TOKEN", tokenExpiresAt: now.Add(time.Hour)}
	sh, lh := Handle{231}, Handle{232}
	admitted := poolIncarnation(t, a.pool)
	fence, witness := newAdmissionFixture(t, admitted)
	oldA, oldB := a.fence, b.fence
	a.fence = fence
	b.fence = fence
	defer func() { a.fence = oldA; b.fence = oldB }()
	if a.PutSession(ctx, sh, record) != nil || a.PutLogin(ctx, lh, login) != nil {
		t.Fatal("failover seed failed")
	}
	// Pin a still-open old-primary backend to prove later checks do not assume
	// the pool has followed the service to the newly admitted primary.
	pinned, err := a.pool.Acquire(ctx)
	if err != nil {
		t.Fatal("old primary pool fixture failed")
	}
	defer pinned.Release()
	recoveryControl(t, "snapshot")
	t.Cleanup(func() { recoveryControl(t, "cleanup") })
	u, _ := url.Parse(os.Getenv("PORTAL_TEST_DATABASE_URL"))
	u.Host = "localhost:5433"
	standby, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal("standby fixture config failed")
	}
	defer standby.Close()
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		var count int
		err = standby.QueryRow(waitCtx, "SELECT (SELECT count(*) FROM portal.sessions WHERE handle=$1)+(SELECT count(*) FROM portal.pending_logins WHERE handle=$2)", sh[:], lh[:]).Scan(&count)
		if err == nil && count == 2 {
			break
		}
		select {
		case <-tick.C:
		case <-waitCtx.Done():
			t.Fatal("standby never received seed rows")
		}
	}
	recoveryControl(t, "partition")
	// Both calls acknowledge real committed DELETE on the original primary.
	if b.RevokeSession(ctx, sh) != nil {
		t.Fatal("acknowledged revoke failed")
	}
	if _, err := b.TakeLogin(ctx, lh, login.State, now); err != nil {
		t.Fatal("acknowledged callback consumption failed")
	}
	var primaryCount int
	if admin.QueryRow(ctx, "SELECT (SELECT count(*) FROM portal.sessions WHERE handle=$1)+(SELECT count(*) FROM portal.pending_logins WHERE handle=$2)", sh[:], lh[:]).Scan(&primaryCount) != nil || primaryCount != 0 {
		t.Fatal("primary deletes not durable")
	}
	recoveryControl(t, "promote")
	standby.Reset()
	var recovered int
	if standby.QueryRow(ctx, "SELECT (SELECT count(*) FROM portal.sessions WHERE handle=$1)+(SELECT count(*) FROM portal.pending_logins WHERE handle=$2)", sh[:], lh[:]).Scan(&recovered) != nil || recovered != 2 {
		t.Fatal("fixture did not lose acknowledged deletes")
	}
	promoted := poolIncarnation(t, standby)
	if promoted.SystemIdentifier != admitted.SystemIdentifier || promoted.Timeline == admitted.Timeline || promoted.PostmasterStartedAt.Equal(admitted.PostmasterStartedAt) {
		t.Fatal("fixture did not physically promote/restart")
	}
	newReplica := func() *PostgresStore {
		c := integrationRuntimeConfig(t)
		c.ConnConfig.Port = 5433
		pool, err := pgxpool.NewWithConfig(ctx, c)
		if err != nil {
			t.Fatal("promoted runtime pool config failed")
		}
		s := fixtureStore(t)
		s.pool = pool
		s.fence = fence
		t.Cleanup(s.Close)
		return s
	}
	c, d := newReplica(), newReplica()
	// Witness is byte-for-byte unchanged. No observer or recovery update happens
	// before either replica rejects both resurrected rows on its own connection.
	for _, store := range []*PostgresStore{c, d} {
		if _, err := store.GetSession(ctx, sh); !errors.Is(err, portal.ErrUnavailable) {
			t.Fatal("resurrected old session authorized before observer")
		}
		if _, err := store.TakeLogin(ctx, lh, login.State, now); !errors.Is(err, portal.ErrUnavailable) {
			t.Fatal("resurrected old callback authorized before observer")
		}
	}
	witness.mu.Lock()
	if witness.doc.ResourceVersion != "17" {
		t.Fatal("witness changed before immediate failover rejection")
	}
	// Model approved owner CAS-open after explicit closed recovery, with a new
	// independent generation. This is synthetic HTTP data, no live Kube mutation.
	witness.doc = admissionDocument(promoted)
	witness.doc.ResourceVersion = "19"
	witness.doc.Data["generation"] = strings.Repeat("h", 32)
	witness.mu.Unlock()
	tx, err := pinned.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal("old pooled transaction failed")
	}
	if _, err := fence.Check(ctx, tx, strings.Repeat("h", 32)); !errors.Is(err, portal.ErrUnavailable) {
		t.Fatal("old-primary pooled connection matched new admission")
	}
	_ = tx.Rollback(ctx)
	c.generation = strings.Repeat("h", 32)
	if _, err := c.GetSession(ctx, sh); !errors.Is(err, portal.ErrDenied) {
		t.Fatal("new generation authorized resurrected session")
	}
	if _, err := c.TakeLogin(ctx, lh, login.State, now); !errors.Is(err, portal.ErrDenied) {
		t.Fatal("new generation consumed resurrected callback")
	}
	t.Log("ASYNC_FAILOVER_EVIDENCE acknowledged DELETEs=2 resurrected rows=2 unchanged witness; promoted pools=2 denied session+callback before observer; old-primary pooled connection denied")
}

// This pause is a deterministic transaction boundary, not a sleep/load test.
// The underlying witness and identity checks still use production code.
type pausedInsertFence struct {
	RecoveryFence
	reached, resume chan struct{}
	once            sync.Once
}

func (f *pausedInsertFence) Recheck(ctx context.Context, s AdmissionSnapshot) error {
	cancelled := false
	f.once.Do(func() {
		close(f.reached)
		select {
		case <-f.resume:
		case <-ctx.Done():
			cancelled = true
		}
	})
	if cancelled {
		return portal.ErrUnavailable
	}
	return f.RecoveryFence.Recheck(ctx, s)
}

func testInsertDoesNotBlockRevocation(t *testing.T, a, b *PostgresStore, record Record) {
	t.Helper()
	for _, kind := range []string{"sessions", "pending_logins"} {
		t.Run(kind, func(t *testing.T) { testInsertKindDoesNotBlockRevocation(t, a, b, record, kind) })
	}
}

func testInsertKindDoesNotBlockRevocation(t *testing.T, a, b *PostgresStore, record Record, kind string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	record.CreatedAt = now
	record.IdleExpiresAt = now.Add(5 * time.Minute)
	record.ExpiresAt = now.Add(5 * time.Minute)
	record.tokenExpiresAt = now.Add(time.Hour)
	login := Login{State: strings.Repeat("s", 43), Nonce: strings.Repeat("n", 43), Verifier: strings.Repeat("v", 43), ExpiresAt: now.Add(5 * time.Minute)}
	fence, _ := newAdmissionFixture(t, poolIncarnation(t, a.pool))
	original := b.fence
	b.fence = fence
	defer func() { b.fence = original }()
	session, callback, insert := Handle{201}, Handle{202}, Handle{203}
	if b.PutSession(ctx, session, record) != nil || b.PutLogin(ctx, callback, login) != nil {
		t.Fatal("concurrency seed failed")
	}
	pause := &pausedInsertFence{RecoveryFence: fence, reached: make(chan struct{}), resume: make(chan struct{})}
	writer := *a
	writer.fence = pause
	writer.timeout = 5 * time.Second
	result := make(chan error, 1)
	go func() {
		if kind == "pending_logins" {
			result <- writer.PutLogin(ctx, insert, login)
		} else {
			result <- writer.PutSession(ctx, insert, record)
		}
	}()
	defer func() {
		close(pause.resume)
		if err := <-result; err != nil && !t.Failed() {
			t.Error("paused insert failed after release")
		}
	}()
	deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	select {
	case <-pause.reached:
	case <-deadline.Done():
		t.Fatal("insert did not reach precommit boundary")
	}
	if b.RevokeSession(deadline, session) != nil {
		t.Fatal("held insert admission blocked revoke")
	}
	if _, err := b.TakeLogin(deadline, callback, login.State, now); err != nil {
		t.Fatal("held insert admission blocked callback consumption")
	}
	t.Log("INSERT_ADMISSION_EVIDENCE paused precommit insert does not block independent revoke or callback DELETE")
}

func testOverlappingLegitimateInserts(t *testing.T, admin *pgxpool.Pool, a, b *PostgresStore, record Record) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	record.CreatedAt = now
	record.IdleExpiresAt = now.Add(time.Minute)
	record.ExpiresAt = now.Add(time.Minute)
	record.tokenExpiresAt = now.Add(time.Hour)
	fence, _ := newAdmissionFixture(t, poolIncarnation(t, a.pool))
	pause := &pausedInsertFence{RecoveryFence: fence, reached: make(chan struct{}), resume: make(chan struct{})}
	first, second := *a, *b
	first.fence = pause
	second.fence = fence
	first.timeout = 5 * time.Second
	second.timeout = 5 * time.Second
	firstResult := make(chan error, 1)
	secondResult := make(chan error, 1)
	var release sync.Once
	defer func() { release.Do(func() { close(pause.resume) }) }()
	go func() { firstResult <- first.PutSession(ctx, Handle{205}, record) }()
	defer func() {
		release.Do(func() { close(pause.resume) })
		if err := <-firstResult; err != nil && !t.Failed() {
			t.Error("first overlapping insert failed")
		}
	}()
	select {
	case <-pause.reached:
	case <-ctx.Done():
		t.Fatal("first insert did not reach boundary")
	}
	go func() { secondResult <- second.PutSession(ctx, Handle{206}, record) }()
	defer func() {
		release.Do(func() { close(pause.resume) })
		if err := <-secondResult; err != nil && !t.Failed() {
			t.Error("second overlapping insert failed")
		}
	}()
	// Observe a real PostgreSQL wait, using the disposable administrator only.
	// This is a bounded two-insert fixture, not a load or retry loop.
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if admin.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_locks WHERE locktype='advisory' AND NOT granted AND database=(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database()))").Scan(&waiting) != nil {
			t.Fatal("fixture lock observation failed")
		}
		if waiting {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("second insert did not wait for admission")
		}
	}
	release.Do(func() { close(pause.resume) })
	t.Log("LOGIN_OVERLAP_EVIDENCE two legitimate app-pool inserts overlap at admission; second waits within deadline and both commit without retry")
}
