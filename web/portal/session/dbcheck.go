package session

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The db-check classes (DESIGN-056). The command prints "PASS" or
// "FAIL <class>" and nothing else.
const (
	DBCheckConfig  = "config"
	DBCheckDNS     = "dns"
	DBCheckRefused = "refused"
	DBCheckTLS     = "tls"
	DBCheckAuth    = "auth"
	DBCheckTimeout = "timeout"
	DBCheckQuery   = "query"
)

// errDBCheckPlaintext is returned if pgx ever hands the hook a connection
// that is not TLS; the store's sslmode=verify-full never does.
var errDBCheckPlaintext = errors.New("portal db-check connection is not TLS")

const (
	dbCheckQueryTimeout = 3 * time.Second
	dbCheckTotalTimeout = 10 * time.Second
)

// CheckDatabase loads the mounted production database settings under
// ProductionRoot, opens one connection with the store's own TLS and DSN rules,
// runs SELECT 1 in a read-only transaction and closes. It returns "" on
// success or one db-check class. It starts no server, reads no OIDC setting or
// session key, runs no migration and writes nothing. It never returns error
// text, addresses or values.
func CheckDatabase(ctx context.Context) string {
	return checkDatabase(ctx, ProductionRoot, dbCheckSeams{})
}

// DBCheckLine maps a CheckDatabase result to the command's only output line
// and exit code. An unknown value can only be a programming error; it is
// reported as a query failure rather than printed.
func DBCheckLine(class string) (string, int) {
	switch class {
	case "":
		return "PASS", 0
	case DBCheckConfig, DBCheckDNS, DBCheckRefused, DBCheckTLS, DBCheckAuth, DBCheckTimeout, DBCheckQuery:
		return "FAIL " + class, 3
	default:
		return "FAIL " + DBCheckQuery, 3
	}
}

// dbCheckSeams are test seams. Production leaves every field zero: pgx's own
// resolver and dialer, the store's 5 s connect_timeout, and the fixed query
// and total deadlines.
type dbCheckSeams struct {
	lookup                              func(context.Context, string) ([]string, error)
	dial                                func(context.Context, string, string) (net.Conn, error)
	connectTimeout, queryTimeout, total time.Duration
}

// dbCheckProgress records how far a connection attempt got, so a failure is
// classified by phase and error type, never by error text.
type dbCheckProgress struct {
	mu                         sync.Mutex
	resolved, dialed, upgraded bool
}

func (p *dbCheckProgress) mark(step *bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	*step = true
}

func (p *dbCheckProgress) snapshot() (resolved, dialed, upgraded bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resolved, p.dialed, p.upgraded
}

func checkDatabase(ctx context.Context, root string, s dbCheckSeams) string {
	if s.total <= 0 {
		s.total = dbCheckTotalTimeout
	}
	if s.queryTimeout <= 0 {
		s.queryTimeout = dbCheckQueryTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, s.total)
	defer cancel()

	// The same files, readers and rules as LoadProduction, and nothing else.
	user, userErr := readMountedText(root, "secrets/DB_USERNAME", 4096)
	password, passwordErr := readMountedText(root, "secrets/DB_PASSWORD", 4096)
	ca, caErr := readMountedBytes(root, "ca/ca.crt", 64<<10)
	if userErr != nil || passwordErr != nil || caErr != nil || user != productionDBUser || !bearerText(password) || !pemPool(ca) {
		return DBCheckConfig
	}
	roots, err := postgresRoots(ca)
	if err != nil {
		return DBCheckConfig
	}
	pool, err := postgresPoolConfig(productionDBHost, user, password, roots)
	if err != nil {
		return DBCheckConfig
	}
	config := pool.ConnConfig.Copy()
	// Simple protocol only: the server sees plain statements, no prepared ones.
	config.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	if s.connectTimeout > 0 {
		config.ConnectTimeout = s.connectTimeout
	}
	progress := &dbCheckProgress{}
	lookup := config.LookupFunc
	if s.lookup != nil {
		lookup = s.lookup
	}
	config.LookupFunc = func(ctx context.Context, host string) ([]string, error) {
		addrs, err := lookup(ctx, host)
		if err == nil {
			progress.mark(&progress.resolved)
		}
		return addrs, err
	}
	dial := config.DialFunc
	if s.dial != nil {
		dial = s.dial
	}
	config.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dial(ctx, network, addr)
		if err == nil {
			progress.mark(&progress.dialed)
		}
		return conn, err
	}
	// pgx calls AfterNetConnect after it has wrapped the connection in TLS, but
	// crypto/tls shakes hands lazily, on the first write. Completing the
	// handshake here, with the same config, makes the TLS phase observable: a
	// verification failure ends the attempt before the login is sent.
	config.AfterNetConnect = func(ctx context.Context, _ *pgconn.Config, conn net.Conn) (net.Conn, error) {
		// pgx closes the returned connection on error, so it is always returned.
		tlsConn, ok := conn.(*tls.Conn)
		if !ok {
			return conn, errDBCheckPlaintext
		}
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return conn, err
		}
		progress.mark(&progress.upgraded)
		return conn, nil
	}

	db, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return dbCheckConnectClass(ctx, err, progress)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		_ = db.Close(closeCtx)
	}()
	queryCtx, queryCancel := context.WithTimeout(ctx, s.queryTimeout)
	defer queryCancel()
	tx, err := db.BeginTx(queryCtx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return dbCheckQueryClass(queryCtx, err)
	}
	var one int
	scanErr := tx.QueryRow(queryCtx, "SELECT 1").Scan(&one)
	rollbackErr := tx.Rollback(queryCtx)
	if scanErr != nil {
		return dbCheckQueryClass(queryCtx, scanErr)
	}
	if rollbackErr != nil {
		return dbCheckQueryClass(queryCtx, rollbackErr)
	}
	if one != 1 {
		return DBCheckQuery
	}
	return ""
}

func dbCheckTimedOut(ctx context.Context, err error) bool {
	var netErr net.Error
	return ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout())
}

// dbCheckConnectClass names the phase that failed: the name lookup, the dial,
// the TLS handshake (before AfterNetConnect), or the login and session check.
func dbCheckConnectClass(ctx context.Context, err error, progress *dbCheckProgress) string {
	if dbCheckTimedOut(ctx, err) {
		return DBCheckTimeout
	}
	resolved, dialed, upgraded := progress.snapshot()
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr) || !resolved:
		return DBCheckDNS
	case !dialed:
		return DBCheckRefused
	case !upgraded:
		return DBCheckTLS
	case errors.Is(err, pgconn.ErrReadOnlyConnection):
		return DBCheckQuery
	default:
		return DBCheckAuth
	}
}

func dbCheckQueryClass(ctx context.Context, err error) string {
	if dbCheckTimedOut(ctx, err) {
		return DBCheckTimeout
	}
	return DBCheckQuery
}
