package session

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

type PostgresConfig struct {
	Host, Username, Password string
	CA                       []byte
	// Key is standard base64 of exactly 32 bytes, held outside DB backups.
	Key, Generation string
	Now             func() time.Time
	Timeout         time.Duration
}

type PostgresStore struct {
	pool       *pgxpool.Pool
	seal       cipher.AEAD
	generation string
	now        func() time.Time
	timeout    time.Duration
	// beforeCommit is a test seam: nil in production. Tests pause a real
	// transaction at its pre-commit boundary with it.
	beforeCommit func(context.Context) error
	// admissionNow is a test-only clock transform after the real database clock
	// read. Production leaves it nil; concurrent fixtures can freeze/advance it.
	admissionNow func(time.Time) time.Time
}

// NewPostgresStore accepts only the proposed dedicated DB/role and CNPG primary
// service. Configuration cannot select an admin user, replica, DSN, insecure
// sslmode, Unix socket, ambient password/service file or plaintext fallback.
// The constructor opens no connection. There is no recovery witness: a database
// restart or failover needs no human action (Tom, 2026-10-26, DESIGN-029).
func NewPostgresStore(c PostgresConfig) (*PostgresStore, error) {
	if c.Host != "postgres16-rw.database.svc.cluster.local" || c.Username != "dev_env_v2_portal_runtime" || c.Password == "" || len(c.Password) > 4096 {
		return nil, portal.ErrUnavailable
	}
	roots, err := postgresRoots(c.CA)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	config, err := postgresPoolConfig(c.Host, c.Username, c.Password, roots)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	s, err := newPostgresStore(c)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	s.pool = pool
	return s, nil
}

// postgresRoots trusts the pinned CNPG CA by its key, not by its dates. CNPG
// renews its CA every 90 days with the same key and subject, and Go refuses an
// expired root, so each pinned CA joins the pool with its validity window
// opened. The chain, hostname, key usage and the server certificate's own
// dates are still verified, so no re-pin is needed after a renewal.
func postgresRoots(pemBytes []byte) (*x509.CertPool, error) {
	roots := x509.NewCertPool()
	n := 0
	for rest := pemBytes; ; {
		var block *pem.Block
		if block, rest = pem.Decode(rest); block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		ca, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, portal.ErrUnavailable
		}
		open := *ca
		open.NotBefore, open.NotAfter = time.Time{}, time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC)
		roots.AddCert(&open)
		n++
	}
	if n == 0 {
		return nil, portal.ErrUnavailable
	}
	return roots, nil
}

func postgresPoolConfig(host, username, password string, roots *x509.CertPool) (*pgxpool.Config, error) {
	// pgx otherwise reads ambient service/TLS/password files while parsing.
	// Refuse them before parsing, rather than accepting hidden authority.
	for _, name := range []string{"PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGSSLCRL", "PGSSLCRLDIR"} {
		if os.Getenv(name) != "" {
			return nil, portal.ErrUnavailable
		}
	}
	u := &url.URL{Scheme: "postgres", Host: host + ":5432", Path: "/dev_env_v2_portal", User: url.UserPassword(username, password)}
	q := url.Values{"sslmode": {"verify-full"}, "target_session_attrs": {"read-write"}, "connect_timeout": {"5"}, "sslrootcert": {""}, "sslcert": {""}, "sslkey": {""}, "passfile": {""}}
	u.RawQuery = q.Encode()
	c, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	c.ConnConfig.TLSConfig = &tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12}
	c.ConnConfig.Fallbacks = nil
	c.ConnConfig.RuntimeParams = map[string]string{"application_name": "dev-env-portal", "search_path": "pg_catalog,portal", "statement_timeout": "5000"}
	c.ConnConfig.Tracer = nil
	// Two connections per replica. Advisory-lock independence does not keep a
	// same-replica revoke available while both connections are already held.
	c.MaxConns = 2
	c.MinConns = 0
	c.BeforeConnect = nil
	c.AfterConnect = nil
	return c, nil
}

func newPostgresStore(c PostgresConfig) (*PostgresStore, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(c.Key)
	if err != nil || !canonicalKey(c.Key) || len(key) != 32 || c.Now == nil || c.Timeout <= 0 || c.Timeout > 10*time.Second || !validGeneration(c.Generation) {
		return nil, portal.ErrUnavailable
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	seal, err := cipher.NewGCM(block)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	return &PostgresStore{seal: seal, generation: c.Generation, now: c.Now, timeout: c.Timeout}, nil
}

func validGeneration(s string) bool {
	if len(s) < 32 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func (s *PostgresStore) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

type rowContext struct {
	Table      string
	Handle     Handle
	Version    int
	Generation string
	ExpiresAt  time.Time
	State      [32]byte
}

func (s *PostgresStore) row(kind string, h Handle, expiry time.Time, state [32]byte) rowContext {
	return rowContext{kind, h, 1, s.generation, expiry.UTC().Truncate(time.Microsecond), state}
}

func (s *PostgresStore) encrypt(row rowContext, payload any) ([]byte, error) {
	plain, err := json.Marshal(payload)
	if err != nil || len(plain) > 128*1024-64 {
		return nil, portal.ErrUnavailable
	}
	aad, err := json.Marshal(row)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	nonce := make([]byte, s.seal.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, portal.ErrUnavailable
	}
	return s.seal.Seal(nonce, nonce, plain, aad), nil
}

func (s *PostgresStore) decrypt(row rowContext, data []byte, out any) error {
	if len(data) < s.seal.NonceSize()+s.seal.Overhead() || len(data) > 128*1024 {
		return portal.ErrUnavailable
	}
	aad, err := json.Marshal(row)
	if err != nil {
		return portal.ErrUnavailable
	}
	n := s.seal.NonceSize()
	plain, err := s.seal.Open(nil, data[:n], data[n:], aad)
	if err != nil || json.Unmarshal(plain, out) != nil {
		return portal.ErrUnavailable
	}
	return nil
}

// run never retries a statement/commit. A lost commit response is unavailable,
// even if the write actually committed. The browser must start a fresh login.
func (s *PostgresStore) run(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	if s == nil || s.pool == nil {
		return portal.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if ctx.Err() != nil {
		return portal.ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return portal.ErrUnavailable
	}
	return s.runAcquired(ctx, tx, fn)
}

// runAcquired receives only the transaction already acquired by run, so session
// SQL cannot select another connection from the pool.
func (s *PostgresStore) runAcquired(ctx context.Context, tx pgx.Tx, fn func(context.Context, pgx.Tx) error) error {
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
		return portal.ErrUnavailable
	}
	if err := fn(ctx, tx); err != nil {
		return normalize(err)
	}
	if s.beforeCommit != nil && s.beforeCommit(ctx) != nil {
		return portal.ErrUnavailable
	}
	if tx.Commit(ctx) != nil || ctx.Err() != nil {
		return portal.ErrUnavailable
	}
	return nil
}

func insertError(err error) error {
	if err == nil {
		return nil
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return portal.ErrDenied
	}
	return portal.ErrUnavailable
}

// Serialize insertion admission across replicas without table-wide locks. A
// transaction advisory lock is taken only by inserters; revocation
// and callback DELETEs never acquire it. No role/admin grant is added.
func lockPortalInsert(ctx context.Context, tx pgx.Tx, table string) error {
	var key int32
	switch table {
	case "pending_logins":
		key = 1
	case "sessions":
		key = 2
	default:
		return portal.ErrUnavailable
	}
	// Waiting is bounded by the existing operation context; it does not retry
	// an INSERT or COMMIT and never locks out DELETEs.
	if _, err := tx.Exec(ctx, "SELECT pg_catalog.pg_advisory_xact_lock($1,$2)", int32(178), key); err != nil {
		return portal.ErrUnavailable
	}
	return nil
}

func admitPortalInsert(ctx context.Context, tx pgx.Tx, table string) error {
	if err := lockPortalInsert(ctx, tx, table); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM portal."+table+" WHERE handle IN (SELECT handle FROM portal."+table+" WHERE expires_at<=clock_timestamp() LIMIT 32)"); err != nil {
		return portal.ErrUnavailable
	}
	var count int
	if tx.QueryRow(ctx, "SELECT count(*) FROM (SELECT 1 FROM portal."+table+" LIMIT 256) AS bounded").Scan(&count) != nil || count >= 256 {
		return portal.ErrUnavailable
	}
	return nil
}

func (s *PostgresStore) PutLogin(ctx context.Context, h Handle, l Login) error {
	if s == nil || s.now == nil || len(l.State) != 43 || len(l.Nonce) != 43 || len(l.Verifier) != 43 || !l.ExpiresAt.After(s.now()) {
		return portal.ErrDenied
	}
	l.ExpiresAt = l.ExpiresAt.UTC().Truncate(time.Microsecond)
	state := sha256.Sum256([]byte(l.State))
	row := s.row("pending_login", h, l.ExpiresAt, state)
	data, err := s.encrypt(row, l)
	if err != nil {
		return err
	}
	return s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := admitPortalInsert(ctx, tx, "pending_logins"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO portal.pending_logins (handle,generation,version,state_digest,expires_at,payload) VALUES ($1,$2,1,$3,$4,$5)", h[:], s.generation, state[:], l.ExpiresAt, data)
		return insertError(err)
	})
}

func (s *PostgresStore) TakeLogin(ctx context.Context, h Handle, state string, now time.Time) (Login, error) {
	var l Login
	if len(state) != 43 {
		return l, portal.ErrDenied
	}
	digest := sha256.Sum256([]byte(state))
	err := s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var data []byte
		var expiry time.Time
		err := tx.QueryRow(ctx, "DELETE FROM portal.pending_logins WHERE handle=$1 AND generation=$2 AND version=1 AND state_digest=$3 AND expires_at>GREATEST($4,clock_timestamp()) RETURNING expires_at,payload", h[:], s.generation, digest[:], now).Scan(&expiry, &data)
		if errors.Is(err, pgx.ErrNoRows) {
			return portal.ErrDenied
		}
		if err != nil {
			return portal.ErrUnavailable
		}
		if err = s.decrypt(s.row("pending_login", h, expiry, digest), data, &l); err != nil {
			return err
		}
		if l.State != state || !l.ExpiresAt.Equal(expiry) || !now.Before(l.ExpiresAt) || len(l.Nonce) != 43 || len(l.Verifier) != 43 {
			return portal.ErrDenied
		}
		return nil
	})
	if err != nil {
		return Login{}, err
	}
	return l, nil
}

type sessionPayload struct {
	Record         Record
	AccessToken    string
	TokenExpiresAt time.Time
}

func (s *PostgresStore) PutSession(ctx context.Context, h Handle, r Record) error {
	if s == nil || s.now == nil || !validRecord(r, s.now()) {
		return portal.ErrDenied
	}
	r.ExpiresAt = r.ExpiresAt.UTC().Truncate(time.Microsecond)
	r.IdleExpiresAt = r.IdleExpiresAt.UTC().Truncate(time.Microsecond)
	r.CreatedAt = r.CreatedAt.UTC().Truncate(time.Microsecond)
	r.tokenExpiresAt = r.tokenExpiresAt.UTC().Truncate(time.Microsecond)
	data, err := s.encrypt(s.row("session", h, r.ExpiresAt, [32]byte{}), sessionPayload{r, r.accessToken, r.tokenExpiresAt})
	if err != nil {
		return err
	}
	return s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := admitPortalInsert(ctx, tx, "sessions"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO portal.sessions (handle,generation,version,expires_at,payload) VALUES ($1,$2,1,$3,$4)", h[:], s.generation, r.ExpiresAt, data)
		return insertError(err)
	})
}

func (s *PostgresStore) GetSession(ctx context.Context, h Handle) (Record, error) {
	var p sessionPayload
	err := s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var expiry time.Time
		var data []byte
		err := tx.QueryRow(ctx, "SELECT expires_at,payload FROM portal.sessions WHERE handle=$1 AND generation=$2 AND version=1 AND expires_at>GREATEST($3,clock_timestamp())", h[:], s.generation, s.now()).Scan(&expiry, &data)
		if errors.Is(err, pgx.ErrNoRows) {
			return portal.ErrDenied
		}
		if err != nil {
			return portal.ErrUnavailable
		}
		if err = s.decrypt(s.row("session", h, expiry, [32]byte{}), data, &p); err != nil {
			return err
		}
		p.Record.accessToken = p.AccessToken
		p.Record.tokenExpiresAt = p.TokenExpiresAt
		if !validRecord(p.Record, s.now()) || !p.Record.ExpiresAt.Equal(expiry) {
			return portal.ErrDenied
		}
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return p.Record, nil
}

func (s *PostgresStore) RevokeSession(ctx context.Context, h Handle) error {
	return s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "DELETE FROM portal.sessions WHERE handle=$1 AND generation=$2", h[:], s.generation)
		if err != nil {
			return portal.ErrUnavailable
		}
		return nil
	})
}

// No error or formatting helper returns DSNs, SQL/provider errors or payloads.
func (PostgresConfig) String() string { return "[redacted portal database config]" }
func (PostgresStore) String() string  { return "[portal database store]" }

var _ Store = (*PostgresStore)(nil)

// strings is used for strict explicit key encoding; whitespace is not accepted.
func canonicalKey(key string) bool { return !strings.ContainsAny(key, "\r\n\t ") }

func (c PostgresConfig) GoString() string { return c.String() }
func (s PostgresStore) GoString() string  { return s.String() }
func (sessionPayload) String() string     { return "[redacted portal payload]" }
func (s sessionPayload) GoString() string { return s.String() }

// commandAuth verifies the existing immutable authentication row. It does not
// lock out logout: a command accepted before revocation may already be in flight.
func (s *PostgresStore) commandAuth(ctx context.Context, tx pgx.Tx, h Handle) (time.Time, error) {
	var expiry time.Time
	var data []byte
	err := tx.QueryRow(ctx, "SELECT expires_at,payload FROM portal.sessions WHERE handle=$1 AND generation=$2 AND version=1 AND expires_at>GREATEST($3,clock_timestamp())", h[:], s.generation, s.now()).Scan(&expiry, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, portal.ErrDenied
	}
	if err != nil {
		return time.Time{}, portal.ErrUnavailable
	}
	var p sessionPayload
	if s.decrypt(s.row("session", h, expiry, [32]byte{}), data, &p) != nil {
		return time.Time{}, portal.ErrDenied
	}
	p.Record.accessToken, p.Record.tokenExpiresAt = p.AccessToken, p.TokenExpiresAt
	if !validRecord(p.Record, s.now()) || !p.Record.ExpiresAt.Equal(expiry) {
		return time.Time{}, portal.ErrDenied
	}
	return expiry, nil
}

// commandRow reads only the purpose-bound ciphertext and never treats it as an
// authentication payload. All command rows retain the original session expiry.
func (s *PostgresStore) commandRow(ctx context.Context, tx pgx.Tx, h Handle, id, purpose string, out any) (bool, error) {
	key := commandKey(h, id, purpose)
	var expiry time.Time
	var data []byte
	err := tx.QueryRow(ctx, "SELECT expires_at,payload FROM portal.sessions WHERE handle=$1 AND generation=$2 AND version=1 AND expires_at>GREATEST($3,clock_timestamp())", key[:], s.generation, s.now()).Scan(&expiry, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil || s.decrypt(s.row("portal_command_"+purpose, key, expiry, [32]byte{}), data, out) != nil {
		return false, portal.ErrUnavailable
	}
	return true, nil
}

func (s *PostgresStore) reserveCommand(ctx context.Context, h Handle, wanted commandReservation) (bool, error) {
	if s == nil || s.now == nil || !validCommandID(wanted.RequestID) || wanted.SessionName == "" || len(wanted.SessionName) > 253 || wanted.SessionUID == "" || len(wanted.SessionUID) > 128 || wanted.Digest == ([32]byte{}) {
		return false, portal.ErrDenied
	}
	created := false
	err := s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		expiry, err := s.commandAuth(ctx, tx, h)
		if err != nil {
			return err
		}
		// Admission serializes the duplicate check and insertion across replicas.
		// Check existing before admission so saturated storage still answers replays.
		var existing commandReservation
		found, err := s.commandRow(ctx, tx, h, wanted.RequestID, "reservation", &existing)
		if err != nil {
			return err
		}
		if found {
			if existing.RequestID != wanted.RequestID || existing.SessionName != wanted.SessionName || existing.SessionUID != wanted.SessionUID || existing.Digest != wanted.Digest || !existing.ExpiresAt.Equal(expiry) {
				return portal.ErrDenied
			}
			return nil
		}
		if err = admitPortalInsert(ctx, tx, "sessions"); err != nil {
			return err
		}
		found, err = s.commandRow(ctx, tx, h, wanted.RequestID, "reservation", &existing)
		if err != nil {
			return err
		}
		if found {
			if existing.RequestID != wanted.RequestID || existing.SessionName != wanted.SessionName || existing.SessionUID != wanted.SessionUID || existing.Digest != wanted.Digest || !existing.ExpiresAt.Equal(expiry) {
				return portal.ErrDenied
			}
			return nil
		}
		wanted.ExpiresAt = expiry
		key := commandKey(h, wanted.RequestID, "reservation")
		data, err := s.encrypt(s.row("portal_command_reservation", key, expiry, [32]byte{}), wanted)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO portal.sessions (handle,generation,version,expires_at,payload) SELECT $1,$2,1,$3,$4 WHERE $3>clock_timestamp()", key[:], s.generation, expiry, data)
		if err != nil {
			return insertError(err)
		}
		// Check clock again: INSERT SELECT can affect zero rows on expiry.
		found, err = s.commandRow(ctx, tx, h, wanted.RequestID, "reservation", &existing)
		if err != nil || !found {
			return portal.ErrDenied
		}
		created = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return created, nil
}

func (s *PostgresStore) readCommand(ctx context.Context, h Handle, id string) (commandReservation, *apiv1.PortalMessageReceipt, error) {
	var reservation commandReservation
	var receipt apiv1.PortalMessageReceipt
	if s == nil || s.now == nil || !validCommandID(id) {
		return reservation, nil, portal.ErrDenied
	}
	terminal := false
	err := s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		expiry, err := s.commandAuth(ctx, tx, h)
		if err != nil {
			return err
		}
		found, err := s.commandRow(ctx, tx, h, id, "reservation", &reservation)
		if err != nil {
			return err
		}
		if !found || reservation.RequestID != id || !reservation.ExpiresAt.Equal(expiry) {
			return portal.ErrDenied
		}
		terminal, err = s.commandRow(ctx, tx, h, id, "terminal", &receipt)
		return err
	})
	if err != nil {
		return commandReservation{}, nil, err
	}
	if !terminal {
		return reservation, nil, nil
	}
	return reservation, &receipt, nil
}

func (s *PostgresStore) finishCommand(ctx context.Context, h Handle, reservation commandReservation, receipt apiv1.PortalMessageReceipt) error {
	if s == nil || s.now == nil || !validCommandID(reservation.RequestID) || receipt.RequestID != reservation.RequestID || receipt.SessionName != reservation.SessionName || receipt.SessionUID != reservation.SessionUID || receipt.ObservedAt.IsZero() || (receipt.TransportStatus != apiv1.PortalTransportDelivered && receipt.TransportStatus != apiv1.PortalTransportRefused && receipt.TransportStatus != apiv1.PortalTransportUnknown) || !validCommandReason(receipt.TransportStatus, receipt.Reason) || len(receipt.ResolvedPodUID) > 128 {
		return portal.ErrDenied
	}
	return s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		expiry, err := s.commandAuth(ctx, tx, h)
		if err != nil {
			return err
		}
		var existing commandReservation
		found, err := s.commandRow(ctx, tx, h, reservation.RequestID, "reservation", &existing)
		if err != nil {
			return err
		}
		if !found || !sameCommandReservation(existing, reservation) || !existing.ExpiresAt.Equal(expiry) {
			return portal.ErrDenied
		}
		var previous apiv1.PortalMessageReceipt
		found, err = s.commandRow(ctx, tx, h, reservation.RequestID, "terminal", &previous)
		if err != nil {
			return err
		}
		if found {
			if !sameCommandReceipt(previous, receipt) {
				return portal.ErrDenied
			}
			return nil
		}
		if err = admitPortalInsert(ctx, tx, "sessions"); err != nil {
			return err
		}
		found, err = s.commandRow(ctx, tx, h, reservation.RequestID, "terminal", &previous)
		if err != nil {
			return err
		}
		if found {
			if !sameCommandReceipt(previous, receipt) {
				return portal.ErrDenied
			}
			return nil
		}
		key := commandKey(h, reservation.RequestID, "terminal")
		data, err := s.encrypt(s.row("portal_command_terminal", key, expiry, [32]byte{}), receipt)
		if err != nil {
			return err
		}
		result, err := tx.Exec(ctx, "INSERT INTO portal.sessions (handle,generation,version,expires_at,payload) SELECT $1,$2,1,$3,$4 WHERE $3>clock_timestamp()", key[:], s.generation, expiry, data)
		if err != nil {
			return insertError(err)
		}
		if result.RowsAffected() != 1 {
			return portal.ErrDenied
		}
		return nil
	})
}
