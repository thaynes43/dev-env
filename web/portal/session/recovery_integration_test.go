package session

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thaynes43/dev-env/web/portal"
)

// backendStart is test evidence only: the store itself never reads it.
func backendStart(t *testing.T, pool *pgxpool.Pool) (string, time.Time) {
	t.Helper()
	var timeline string
	var started time.Time
	if pool.QueryRow(context.Background(), "SELECT pg_catalog.left(pg_catalog.pg_walfile_name(pg_catalog.pg_current_wal_lsn()),8), pg_catalog.pg_postmaster_start_time()").Scan(&timeline, &started) != nil {
		t.Fatal("fixture backend identity read failed")
	}
	return timeline, started
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

// A physical promotion changes the timeline and postmaster start time. With
// the recovery witness gone (Tom, 2026-10-26), the promoted primary serves the
// replicated session with no human action; only a generation rotation denies it.
func testDisposableFailover(t *testing.T, a *PostgresStore) {
	t.Helper()
	requireHostedProof(t, hostedFailoverInputMissing(), "hosted disposable physical failover not configured; no failover proof")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	record := Record{Principal: portal.Principal{Issuer: "https://issuer.example/portal/", Subject: "synthetic-subject"}, CSRF: strings.Repeat("c", 43), CreatedAt: now, IdleExpiresAt: now.Add(5 * time.Minute), ExpiresAt: now.Add(5 * time.Minute), accessToken: "SYNTHETIC-FAILOVER-TOKEN", tokenExpiresAt: now.Add(time.Hour)}
	sh := Handle{231}
	beforeTimeline, beforeStart := backendStart(t, a.pool)
	if a.PutSession(ctx, sh, record) != nil {
		t.Fatal("failover seed failed")
	}
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
		err = standby.QueryRow(waitCtx, "SELECT count(*) FROM portal.sessions WHERE handle=$1", sh[:]).Scan(&count)
		if err == nil && count == 1 {
			break
		}
		select {
		case <-tick.C:
		case <-waitCtx.Done():
			t.Fatal("standby never received seed row")
		}
	}
	// The control script restarts the stopped standby before it promotes it.
	recoveryControl(t, "partition")
	recoveryControl(t, "promote")
	standby.Reset()
	afterTimeline, afterStart := backendStart(t, standby)
	if afterTimeline == beforeTimeline || afterStart.Equal(beforeStart) {
		t.Fatal("fixture did not physically promote/restart")
	}
	c := integrationRuntimeConfig(t)
	c.ConnConfig.Port = 5433
	pool, err := pgxpool.NewWithConfig(ctx, c)
	if err != nil {
		t.Fatal("promoted runtime pool config failed")
	}
	promoted := fixtureStore(t)
	promoted.pool = pool
	t.Cleanup(promoted.Close)
	got, err := promoted.GetSession(ctx, sh)
	if err != nil || got.accessToken != record.accessToken {
		t.Fatal("promoted primary denied a replicated session")
	}
	promoted.generation = strings.Repeat("h", 32)
	if _, err := promoted.GetSession(ctx, sh); !errors.Is(err, portal.ErrDenied) {
		t.Fatal("rotated generation authorized a session after failover")
	}
	t.Log("FAILOVER_EVIDENCE changed timeline and postmaster start; promoted primary served the session with no witness or human action; rotated generation denied it")
}

// This pause is a deterministic transaction boundary, not a sleep/load test.
type precommitPause struct {
	reached, resume chan struct{}
	once            sync.Once
}

func newPrecommitPause() *precommitPause {
	return &precommitPause{reached: make(chan struct{}), resume: make(chan struct{})}
}

func (p *precommitPause) hold(ctx context.Context) error {
	cancelled := false
	p.once.Do(func() {
		close(p.reached)
		select {
		case <-p.resume:
		case <-ctx.Done():
			cancelled = true
		}
	})
	if cancelled {
		return portal.ErrUnavailable
	}
	return nil
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
	session, callback, insert := Handle{201}, Handle{202}, Handle{203}
	if b.PutSession(ctx, session, record) != nil || b.PutLogin(ctx, callback, login) != nil {
		t.Fatal("concurrency seed failed")
	}
	pause := newPrecommitPause()
	writer := *a
	writer.beforeCommit = pause.hold
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
	pause := newPrecommitPause()
	first, second := *a, *b
	first.beforeCommit = pause.hold
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
