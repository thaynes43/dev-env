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

// The db-check classes (DESIGN-056). The command prints exactly one line:
// "PASS" (exit 0), "FAIL <class>" (exit 3) or "INTERRUPTED" (exit 4, no usable
// verdict); a usage error exits 2 with nothing on stdout.
const (
	DBCheckConfig  = "config"
	DBCheckDNS     = "dns"
	DBCheckRefused = "refused"
	DBCheckTLS     = "tls"
	DBCheckAuth    = "auth"
	DBCheckTimeout = "timeout"
	DBCheckQuery   = "query"
	// DBCheckInterrupted is no result: the caller cancelled the check (for
	// example SIGTERM during a node drain) before it finished. It does not
	// claim that no query ran.
	DBCheckInterrupted = "interrupted"
)

// errDBCheckPlaintext is returned if pgx ever hands the hook a connection
// that is not TLS; the store's sslmode=verify-full never does.
var errDBCheckPlaintext = errors.New("portal db-check connection is not TLS")

// errDBCheckSecondConnection refuses every dial after the check's one
// connection. When a deadline interrupts a statement, pgx abandons the
// connection in a background goroutine (pgconn asyncClose) that first dials
// the server again to send a cancel request. The check opens exactly one
// connection, so that dial is refused before it reaches the network; pgx
// then sends Terminate on the original connection and closes it.
var errDBCheckSecondConnection = errors.New("portal db-check opens one connection only")

// Bounds (DESIGN-056): the whole operation, resolve through rollback, has a
// 10 s deadline. Inside it the connect has the store's 5 s connect_timeout
// and the read-only transaction 3 s. Closing the connection afterwards,
// including pgx's background cleanup of an interrupted connection, has its
// own deadline of at most 1 s, so the check returns within 11 s.
const (
	dbCheckQueryTimeout = 3 * time.Second
	dbCheckTotalTimeout = 10 * time.Second
	dbCheckCloseTimeout = time.Second
)

// dbCheckMountedFiles are the only files db-check reads, relative to the mounted
// root. The how-to's Job mounts exactly these; a test pins both.
var dbCheckMountedFiles = struct{ user, password, ca string }{"secrets/DB_USERNAME", "secrets/DB_PASSWORD", "ca/ca.crt"}

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
// and exit code: PASS 0, FAIL <class> 3, INTERRUPTED 4. An unknown value can
// only be a programming error; it is reported as a query failure rather than
// printed.
func DBCheckLine(class string) (string, int) {
	switch class {
	case "":
		return "PASS", 0
	case DBCheckInterrupted:
		return "INTERRUPTED", 4
	case DBCheckConfig, DBCheckDNS, DBCheckRefused, DBCheckTLS, DBCheckAuth, DBCheckTimeout, DBCheckQuery:
		return "FAIL " + class, 3
	default:
		return "FAIL " + DBCheckQuery, 3
	}
}

// dbCheckSeams are test seams. Production leaves every field zero: pgx's own
// resolver and dialer, the store's 5 s connect_timeout, and the fixed query,
// total and close deadlines.
type dbCheckSeams struct {
	lookup                                            func(context.Context, string) ([]string, error)
	dial                                              func(context.Context, string, string) (net.Conn, error)
	connectTimeout, queryTimeout, total, closeTimeout time.Duration
	// closing runs as the close starts; closed observes its end with pgx's own
	// cleanup channel and the number of dials refused after the one
	// connection. Tests only.
	closing func()
	closed  func(cleanup <-chan struct{}, refusedDials int)
}

// dbCheckProgress records how far a connection attempt got, so a failure is
// classified by phase and error type, never by error text.
type dbCheckProgress struct {
	mu                         sync.Mutex
	resolved, dialed, upgraded bool
	transport                  net.Conn // the dialed connection under TLS
	refused                    int      // dials refused after the one connection
}

func (p *dbCheckProgress) refuseDial() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refused++
}

func (p *dbCheckProgress) refusedDials() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refused
}

func (p *dbCheckProgress) dialedConn(conn net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dialed, p.transport = true, conn
}

func (p *dbCheckProgress) hasDialed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dialed
}

func (p *dbCheckProgress) transportConn() net.Conn {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.transport
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
	// The class is decided when the operation ends, before the close, and is
	// never changed afterwards. Only an operation that ended because the
	// caller cancelled (SIGTERM) is INTERRUPTED; see dbCheckEnd.
	parent := ctx
	if s.total <= 0 {
		s.total = dbCheckTotalTimeout
	}
	if s.queryTimeout <= 0 {
		s.queryTimeout = dbCheckQueryTimeout
	}
	if s.closeTimeout <= 0 {
		s.closeTimeout = dbCheckCloseTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, s.total)
	defer cancel()

	// The same files, readers and rules as LoadProduction, and nothing else.
	user, userErr := readMountedText(root, dbCheckMountedFiles.user, 4096)
	password, passwordErr := readMountedText(root, dbCheckMountedFiles.password, 4096)
	ca, caErr := readMountedBytes(root, dbCheckMountedFiles.ca, 64<<10)
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
		if progress.hasDialed() {
			progress.refuseDial()
			return nil, errDBCheckSecondConnection
		}
		conn, err := dial(ctx, network, addr)
		if err == nil {
			progress.dialedConn(conn)
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
		return dbCheckEnd(parent, ctx, err, dbCheckConnectPhase(err, progress))
	}
	// Close after the operation, success or not, within its own bound. A slow
	// or failed close, or a refused cancel dial, changes neither the result
	// nor the line.
	defer func() {
		if s.closing != nil {
			s.closing()
		}
		dbCheckClose(db, progress.transportConn(), s.closeTimeout)
		if s.closed != nil {
			s.closed(db.PgConn().CleanupDone(), progress.refusedDials())
		}
	}()
	queryCtx, queryCancel := context.WithTimeout(ctx, s.queryTimeout)
	defer queryCancel()
	tx, err := db.BeginTx(queryCtx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return dbCheckEnd(parent, queryCtx, err, DBCheckQuery)
	}
	// SELECT 1 decides the result as soon as it ends; the rollback that
	// follows cannot change a decided failure.
	var one int
	decided := ""
	if scanErr := tx.QueryRow(queryCtx, "SELECT 1").Scan(&one); scanErr != nil {
		decided = dbCheckEnd(parent, queryCtx, scanErr, DBCheckQuery)
	} else if one != 1 {
		decided = DBCheckQuery
	}
	rollbackErr := tx.Rollback(queryCtx)
	if decided != "" {
		return decided
	}
	if rollbackErr != nil {
		return dbCheckEnd(parent, queryCtx, rollbackErr, DBCheckQuery)
	}
	return ""
}

// dbCheckClose closes the connection and returns within bound, waiting for
// pgx's cleanup to finish, measured by PgConn.CleanupDone, not by Close
// returning: after a deadline pgx has already abandoned the connection
// (pgconn asyncClose), Close returns at once, and a background goroutine
// still sends Terminate and drains under its own 15 s deadline. A normal Close
// honours the context for Terminate, but crypto/tls then writes close_notify
// under its own 5 s write deadline. Both would outlast the bound on a stalled
// peer, so at four fifths of the bound the dialed transport underneath is
// closed, which ends any pending write or drain at once.
func dbCheckClose(db *pgx.Conn, transport net.Conn, bound time.Duration) {
	deadline := time.Now().Add(bound)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	go func() { _ = db.Close(ctx) }()
	cleanup := db.PgConn().CleanupDone()
	force := time.NewTimer(bound * 4 / 5)
	defer force.Stop()
	select {
	case <-cleanup:
		return
	case <-force.C:
	}
	if transport != nil {
		_ = transport.Close()
	}
	select {
	case <-cleanup:
	case <-ctx.Done():
	}
}

// dbCheckEnd classifies the error that ended the operation, at the moment it
// ended. A server's answer (a PgError) keeps its own class. An error that is
// itself the end of a context is INTERRUPTED when it is the caller's
// cancellation, and timeout when it is a deadline. Anything else keeps its own
// class. The check's own contexts are cancelled only after it returns, so a
// cancellation seen here is the caller's.
func dbCheckEnd(parent, ctx context.Context, err error, otherwise string) string {
	var pgErr *pgconn.PgError
	var netErr net.Error
	switch {
	case errors.As(err, &pgErr):
		return otherwise
	case errors.Is(err, context.DeadlineExceeded):
		return DBCheckTimeout
	case errors.Is(err, context.Canceled):
		return DBCheckInterrupted
	case (errors.As(err, &netErr) && netErr.Timeout()) || ctx.Err() != nil:
		if errors.Is(parent.Err(), context.Canceled) {
			return DBCheckInterrupted
		}
		return DBCheckTimeout
	default:
		return otherwise
	}
}

// dbCheckConnectPhase names the connect phase that failed: the name lookup,
// the dial, the TLS handshake (before AfterNetConnect), or the login and
// session check.
func dbCheckConnectPhase(err error, progress *dbCheckProgress) string {
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
