package session

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/thaynes43/dev-env/web/portal"
)

var loginAdmissionHandle = sha256.Sum256([]byte("dev-env-portal/shared-login-admission/v1"))

type loginAdmissionPayload struct {
	FullAt time.Time
}

// nextLoginFullAt represents the token bucket as its full-refill deadline.
// Each admission adds one second of debt, with at most four seconds outstanding.
// Expired/missing debt means a full bucket, so ordinary row cleanup is safe.
func nextLoginFullAt(now, fullAt time.Time) (time.Time, bool) {
	if fullAt.Before(now) {
		fullAt = now
	}
	next := fullAt.Add(time.Second)
	return next, !next.After(now.Add(4 * time.Second))
}

// AdmitLogin consumes the one aggregate burst-4, one-per-second bucket. The
// existing sessions lock, AEAD and DELETE/INSERT privileges suffice. This row
// occupies one of the existing 256 slots and expires only when fully refilled.
// There is no retry or in-memory fallback on lock, SQL or ambiguous commit error.
func (s *PostgresStore) AdmitLogin(ctx context.Context) error {
	return s.run(ctx, s.admitLogin)
}

func (s *PostgresStore) admitLogin(ctx context.Context, tx pgx.Tx) error {
	if err := lockPortalInsert(ctx, tx, "sessions"); err != nil {
		return err
	}
	// Read the database clock after acquiring the cross-replica lock. App clocks
	// and transaction-start timestamps cannot refill the shared bucket.
	var now time.Time
	if tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now) != nil {
		return portal.ErrUnavailable
	}
	if s.admissionNow != nil {
		now = s.admissionNow(now)
	}
	var expiry time.Time
	var generation string
	var version int
	var data []byte
	err := tx.QueryRow(ctx, "SELECT generation,version,expires_at,payload FROM portal.sessions WHERE handle=$1", loginAdmissionHandle[:]).Scan(&generation, &version, &expiry, &data)
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return portal.ErrUnavailable
	}
	fullAt := now
	if found && expiry.After(now) {
		// Never filter by generation: rotation must not look like a new bucket.
		// Once the old debt expires (at most four seconds), a fresh row is safe.
		var payload loginAdmissionPayload
		if generation != s.generation || version != 1 || s.decrypt(s.row("portal_login_admission", loginAdmissionHandle, expiry, [32]byte{}), data, &payload) != nil || !payload.FullAt.Equal(expiry) {
			return portal.ErrUnavailable
		}
		fullAt = expiry
	}
	next, allowed := nextLoginFullAt(now, fullAt)
	if !allowed {
		return portal.ErrUnavailable
	}
	if !found {
		if err := admitPortalInsert(ctx, tx, "sessions"); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM portal.sessions WHERE handle=$1", loginAdmissionHandle[:]); err != nil {
		return portal.ErrUnavailable
	}
	next = next.UTC().Truncate(time.Microsecond)
	data, err = s.encrypt(s.row("portal_login_admission", loginAdmissionHandle, next, [32]byte{}), loginAdmissionPayload{FullAt: next})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO portal.sessions (handle,generation,version,expires_at,payload) VALUES ($1,$2,1,$3,$4)", loginAdmissionHandle[:], s.generation, next, data)
	return insertError(err)
}
