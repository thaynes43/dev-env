package session

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

// db-check tests (DESIGN-056). Every Postgres server here is an in-process
// fake on net.Pipe: no socket is opened and no real address is reached. The
// process resolver is replaced for each test so any stray lookup fails and is
// recorded.

const dbCheckPassword = "fixture-db-password-not-a-secret"

// dbCheckFixtureIP is a documentation address (RFC 5737). The test dialer only
// routes it, in memory, to the fake server.
const dbCheckFixtureIP = "192.0.2.10"

type dbCheckCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newDBCheckCA(t *testing.T, notBefore, notAfter time.Time) dbCheckCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture postgres16-ca"}, NotBefore: notBefore, NotAfter: notAfter, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return dbCheckCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca dbCheckCA) leaf(t *testing.T, dns string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: dns}, DNSNames: []string{dns}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func writeDBCheckRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func dbCheckFiles(caPEM []byte) map[string]string {
	return map[string]string{"secrets/DB_USERNAME": productionDBUser + "\n", "secrets/DB_PASSWORD": dbCheckPassword + "\n", "ca/ca.crt": string(caPEM)}
}

// fakePostgres answers one connection the way a Postgres 16 primary does, as
// far as db-check needs: SSL negotiation, TLS, cleartext password login, the
// read-write check and simple-protocol statements. It records everything.
type fakePostgres struct {
	cert       tls.Certificate
	mode       string
	readOnly   bool
	loginError string // SQLSTATE of a FATAL login refusal; "" accepts
	release    chan struct{}

	mu         sync.Mutex
	serverName string
	startup    map[string]string
	statements []string
	unexpected []string
	done       chan struct{}
}

func (f *fakePostgres) record(list *[]string, s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*list = append(*list, s)
}

func (f *fakePostgres) serve(conn net.Conn) {
	defer close(f.done)
	defer func() { _ = conn.Close() }()
	backend := pgproto3.NewBackend(conn, conn)
	msg, err := backend.ReceiveStartupMessage()
	if err != nil {
		return
	}
	if _, ok := msg.(*pgproto3.SSLRequest); !ok {
		f.record(&f.unexpected, fmt.Sprintf("%T before TLS", msg))
		return
	}
	if f.mode == "no-tls" {
		_, _ = conn.Write([]byte("N"))
		return
	}
	if _, err = conn.Write([]byte("S")); err != nil {
		return
	}
	tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{f.cert}, MinVersion: tls.VersionTLS12, GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		f.mu.Lock()
		f.serverName = hello.ServerName
		f.mu.Unlock()
		return nil, nil
	}})
	if tlsConn.Handshake() != nil {
		return
	}
	backend = pgproto3.NewBackend(tlsConn, tlsConn)
	msg, err = backend.ReceiveStartupMessage()
	startup, ok := msg.(*pgproto3.StartupMessage)
	if err != nil || !ok {
		return
	}
	f.mu.Lock()
	f.startup = startup.Parameters
	f.mu.Unlock()
	if f.mode == "hang-login" {
		_, _ = tlsConn.Read(make([]byte, 1))
		return
	}
	if f.mode == "drop-after-tls" {
		// The session ends after a good handshake, before any login answer.
		return
	}
	backend.Send(&pgproto3.AuthenticationCleartextPassword{})
	if backend.Flush() != nil || backend.SetAuthType(pgproto3.AuthTypeCleartextPassword) != nil {
		return
	}
	msg, err = backend.Receive()
	password, ok := msg.(*pgproto3.PasswordMessage)
	if err != nil || !ok {
		return
	}
	if f.mode == "bad-auth" || password.Password != dbCheckPassword {
		backend.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: "28P01", Message: "password authentication failed for user " + productionDBUser})
		_ = backend.Flush()
		return
	}
	if f.loginError != "" {
		backend.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: f.loginError, Message: "fixture login refusal"})
		_ = backend.Flush()
		return
	}
	backend.Send(&pgproto3.AuthenticationOk{})
	for _, p := range [][2]string{{"server_version", "16.4"}, {"client_encoding", "UTF8"}, {"standard_conforming_strings", "on"}, {"DateStyle", "ISO, MDY"}} {
		backend.Send(&pgproto3.ParameterStatus{Name: p[0], Value: p[1]})
	}
	backend.Send(&pgproto3.BackendKeyData{ProcessID: 7, SecretKey: []byte{0, 0, 0, 9}})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if backend.Flush() != nil {
		return
	}
	for {
		msg, err := backend.Receive()
		if err != nil {
			return
		}
		switch m := msg.(type) {
		case *pgproto3.Query:
			f.record(&f.statements, m.String)
			if !f.answer(backend, tlsConn, m.String) {
				return
			}
		case *pgproto3.Terminate:
			return
		default:
			f.record(&f.unexpected, fmt.Sprintf("%T", m))
			return
		}
	}
}

func (f *fakePostgres) answer(backend *pgproto3.Backend, conn net.Conn, statement string) bool {
	switch {
	case statement == "show transaction_read_only":
		value := "off"
		if f.readOnly {
			value = "on"
		}
		backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("transaction_read_only"), DataTypeOID: 25, DataTypeSize: -1, TypeModifier: -1}}})
		backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte(value)}})
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SHOW")})
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	case strings.HasPrefix(statement, "begin"):
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("BEGIN")})
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'T'})
	case statement == "SELECT 1":
		switch f.mode {
		case "hang-query":
			_, _ = conn.Read(make([]byte, 1))
			return false
		case "query-error":
			backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "42501", Message: "permission denied for fixture"})
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'E'})
		default:
			backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("?column?"), DataTypeOID: 23, DataTypeSize: 4, TypeModifier: -1}}})
			backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("1")}})
			backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'T'})
		}
	case statement == "rollback":
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("ROLLBACK")})
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if f.mode == "hang-close" {
			// Stop reading: the client's Terminate write then blocks, as on a
			// stalled TCP peer, until its close deadline.
			if backend.Flush() != nil {
				return false
			}
			<-f.release
			return false
		}
	default:
		backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "0A000", Message: "statement not served by the fixture"})
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	}
	return backend.Flush() == nil
}

// dbCheckNet is the only network the tests give the check: the lookup answers
// the production host with the documentation address, and the dialer turns
// that address into an in-memory pipe to the fake server.
type dbCheckNet struct {
	server    *fakePostgres
	lookupErr error
	dialErr   error

	mu      sync.Mutex
	lookups []string
	dials   []string
}

func (n *dbCheckNet) seams() dbCheckSeams {
	return dbCheckSeams{
		lookup: func(_ context.Context, host string) ([]string, error) {
			n.mu.Lock()
			n.lookups = append(n.lookups, host)
			n.mu.Unlock()
			if n.lookupErr != nil {
				return nil, n.lookupErr
			}
			if host != productionDBHost {
				return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
			}
			return []string{dbCheckFixtureIP}, nil
		},
		dial: func(_ context.Context, network, addr string) (net.Conn, error) {
			n.mu.Lock()
			n.dials = append(n.dials, network+" "+addr)
			n.mu.Unlock()
			if n.dialErr != nil {
				return nil, n.dialErr
			}
			if network != "tcp" || addr != net.JoinHostPort(dbCheckFixtureIP, "5432") || n.server == nil {
				return nil, &net.OpError{Op: "dial", Net: network, Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
			}
			client, server := net.Pipe()
			go n.server.serve(newBufferedServerConn(server))
			return client, nil
		},
		connectTimeout: 2 * time.Second,
		queryTimeout:   time.Second,
		total:          4 * time.Second,
	}
}

func interceptDBCheckResolver(t *testing.T) {
	t.Helper()
	var mu sync.Mutex
	var attempts []string
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, StrictErrors: true, Dial: func(_ context.Context, network, address string) (net.Conn, error) {
		mu.Lock()
		attempts = append(attempts, network+" "+address)
		mu.Unlock()
		return nil, errors.New("outbound attempt intercepted by the test")
	}}
	t.Cleanup(func() { net.DefaultResolver = previous })
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if len(attempts) != 0 {
			t.Errorf("outbound attempts intercepted: %v", attempts)
		}
	})
}

// checkDatabaseWithin runs one check and fails the test cleanly if it does not
// return within limit, so a regression cannot hang the suite.
func checkDatabaseWithin(t *testing.T, limit time.Duration, root string, seams dbCheckSeams) (string, time.Duration) {
	t.Helper()
	type outcome struct {
		class   string
		elapsed time.Duration
	}
	done := make(chan outcome, 1)
	go func() {
		started := time.Now()
		class := checkDatabase(context.Background(), root, seams)
		done <- outcome{class, time.Since(started)}
	}()
	select {
	case o := <-done:
		return o.class, o.elapsed
	case <-time.After(limit):
		t.Fatalf("db-check did not return within %v", limit)
		return "", 0
	}
}

func newFakePostgres(cert tls.Certificate, mode string) *fakePostgres {
	return &fakePostgres{cert: cert, mode: mode, done: make(chan struct{}), release: make(chan struct{})}
}

func TestDBCheckPassesAgainstAPrimaryWithReadOnlySelect(t *testing.T) {
	interceptDBCheckResolver(t)
	ca := newDBCheckCA(t, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	server := newFakePostgres(ca.leaf(t, productionDBHost), "")
	network := &dbCheckNet{server: server}
	if got := checkDatabase(context.Background(), writeDBCheckRoot(t, dbCheckFiles(ca.pem)), network.seams()); got != "" {
		t.Fatalf("db-check failed: %q", got)
	}
	<-server.done
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.serverName != productionDBHost {
		t.Fatalf("TLS server name %q", server.serverName)
	}
	if server.startup["user"] != productionDBUser || server.startup["database"] != "dev_env_v2_portal" || server.startup["application_name"] != "dev-env-portal" {
		t.Fatalf("startup %v", server.startup)
	}
	// No-write: exactly the read-write check and one read-only SELECT 1, over
	// the simple protocol; no migration, no prepared statement, no write.
	want := []string{"show transaction_read_only", "begin read only", "SELECT 1", "rollback"}
	if strings.Join(server.statements, "|") != strings.Join(want, "|") || len(server.unexpected) != 0 {
		t.Fatalf("statements %q unexpected %q", server.statements, server.unexpected)
	}
	if strings.Join(network.lookups, ",") != productionDBHost || strings.Join(network.dials, ",") != "tcp "+dbCheckFixtureIP+":5432" {
		t.Fatalf("lookups %v dials %v", network.lookups, network.dials)
	}
}

func TestDBCheckTrustsAnExpiredPinnedCAByKey(t *testing.T) {
	interceptDBCheckResolver(t)
	// CNPG renews its CA with the same key and subject; the store opens the
	// pinned CA's validity window, so an expired pinned CA still verifies.
	expired := newDBCheckCA(t, time.Now().Add(-100*24*time.Hour), time.Now().Add(-24*time.Hour))
	server := newFakePostgres(expired.leaf(t, productionDBHost), "")
	network := &dbCheckNet{server: server}
	if got := checkDatabase(context.Background(), writeDBCheckRoot(t, dbCheckFiles(expired.pem)), network.seams()); got != "" {
		t.Fatalf("expired pinned CA: %q", got)
	}
}

func TestDBCheckClassifiesEveryFailure(t *testing.T) {
	interceptDBCheckResolver(t)
	ca := newDBCheckCA(t, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	other := newDBCheckCA(t, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	good := ca.leaf(t, productionDBHost)
	for _, tc := range []struct {
		name     string
		server   *fakePostgres
		readOnly bool
		network  func(*dbCheckNet)
		want     string
		reached  bool // whether the fake server should see a login
	}{
		{name: "server certificate from another CA", server: newFakePostgres(other.leaf(t, productionDBHost), ""), want: DBCheckTLS},
		{name: "server certificate for another host", server: newFakePostgres(ca.leaf(t, "postgres16-ro.database.svc.cluster.local"), ""), want: DBCheckTLS},
		{name: "server refuses TLS", server: newFakePostgres(good, "no-tls"), want: DBCheckTLS},
		{name: "password refused", server: newFakePostgres(good, "bad-auth"), want: DBCheckAuth, reached: true},
		{name: "login never answers", server: newFakePostgres(good, "hang-login"), want: DBCheckTimeout, reached: true},
		{name: "SELECT 1 never answers", server: newFakePostgres(good, "hang-query"), want: DBCheckTimeout, reached: true},
		{name: "SELECT 1 fails", server: newFakePostgres(good, "query-error"), want: DBCheckQuery, reached: true},
		{name: "read-only session", server: newFakePostgres(good, ""), readOnly: true, want: DBCheckQuery, reached: true},
		{name: "connection refused", network: func(n *dbCheckNet) {
			n.dialErr = &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
		}, want: DBCheckRefused},
		{name: "connection reset", network: func(n *dbCheckNet) {
			n.dialErr = &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNRESET)}
		}, want: DBCheckRefused},
		{name: "host not found", network: func(n *dbCheckNet) {
			n.lookupErr = &net.DNSError{Err: "no such host", Name: productionDBHost, IsNotFound: true}
		}, want: DBCheckDNS},
		{name: "dial timeout", network: func(n *dbCheckNet) { n.dialErr = &net.OpError{Op: "dial", Net: "tcp", Err: timeoutError{}} }, want: DBCheckTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			network := &dbCheckNet{server: tc.server}
			if tc.server != nil {
				tc.server.readOnly = tc.readOnly
			}
			if tc.network != nil {
				tc.network(network)
			}
			seams := network.seams()
			if strings.HasPrefix(tc.name, "login never") || strings.HasSuffix(tc.name, "never answers") {
				seams.connectTimeout, seams.queryTimeout = 300*time.Millisecond, 300*time.Millisecond
			}
			got, _ := checkDatabaseWithin(t, 10*time.Second, writeDBCheckRoot(t, dbCheckFiles(ca.pem)), seams)
			if got != tc.want {
				t.Fatalf("class %q, want %q", got, tc.want)
			}
			if tc.server != nil && tc.server.mode != "hang-login" && tc.server.mode != "hang-query" {
				<-tc.server.done
			}
			if tc.server != nil {
				tc.server.mu.Lock()
				defer tc.server.mu.Unlock()
				if (tc.server.startup != nil) != tc.reached {
					t.Fatalf("login reached the server: %v", tc.server.startup != nil)
				}
				for _, s := range tc.server.statements {
					if s != "show transaction_read_only" && s != "begin read only" && s != "SELECT 1" && s != "rollback" {
						t.Fatalf("unexpected statement %q", s)
					}
				}
				if len(tc.server.unexpected) != 0 {
					t.Fatalf("unexpected messages %q", tc.server.unexpected)
				}
			}
		})
	}
}

// bufferedServerConn gives the fake server's writes a buffer, as a TCP socket
// has. net.Pipe is synchronous: when the client aborts a TLS handshake it
// writes an alert while the server is still writing its flight, and two
// blocked writers would deadlock until the deadline.
type bufferedServerConn struct {
	net.Conn
	writes chan []byte
	once   sync.Once
	done   chan struct{}
}

func newBufferedServerConn(c net.Conn) *bufferedServerConn {
	b := &bufferedServerConn{Conn: c, writes: make(chan []byte, 256), done: make(chan struct{})}
	go func() {
		defer close(b.done)
		for p := range b.writes {
			if _, err := c.Write(p); err != nil {
				for range b.writes {
				}
				return
			}
		}
	}()
	return b
}

func (b *bufferedServerConn) Write(p []byte) (int, error) {
	b.writes <- append([]byte(nil), p...)
	return len(p), nil
}

func (b *bufferedServerConn) Close() error {
	b.once.Do(func() { close(b.writes) })
	<-b.done
	return b.Conn.Close()
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestDBCheckRefusesInvalidConfigBeforeAnyNetwork(t *testing.T) {
	interceptDBCheckResolver(t)
	ca := newDBCheckCA(t, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	for name, mutate := range map[string]func(map[string]string){
		"no password":           func(f map[string]string) { delete(f, "secrets/DB_PASSWORD") },
		"no username":           func(f map[string]string) { delete(f, "secrets/DB_USERNAME") },
		"no CA":                 func(f map[string]string) { delete(f, "ca/ca.crt") },
		"another role":          func(f map[string]string) { f["secrets/DB_USERNAME"] = "postgres\n" },
		"migrator role":         func(f map[string]string) { f["secrets/DB_USERNAME"] = "dev_env_v2_portal_migrator\n" },
		"password with a space": func(f map[string]string) { f["secrets/DB_PASSWORD"] = "two words\n" },
		"empty password":        func(f map[string]string) { f["secrets/DB_PASSWORD"] = "\n" },
		"CA not PEM":            func(f map[string]string) { f["ca/ca.crt"] = "not a certificate\n" },
	} {
		t.Run(name, func(t *testing.T) {
			files := dbCheckFiles(ca.pem)
			mutate(files)
			network := &dbCheckNet{}
			if got := checkDatabase(context.Background(), writeDBCheckRoot(t, files), network.seams()); got != DBCheckConfig {
				t.Fatalf("class %q, want config", got)
			}
			if len(network.lookups)+len(network.dials) != 0 {
				t.Fatalf("network used: %v %v", network.lookups, network.dials)
			}
		})
	}
	t.Run("ambient PGSSLROOTCERT", func(t *testing.T) {
		t.Setenv("PGSSLROOTCERT", "/tmp/elsewhere.crt")
		network := &dbCheckNet{}
		if got := checkDatabase(context.Background(), writeDBCheckRoot(t, dbCheckFiles(ca.pem)), network.seams()); got != DBCheckConfig || len(network.dials) != 0 {
			t.Fatalf("class %q dials %v", got, network.dials)
		}
	})
}

// TestDBCheckLineIsSanitized: the only output is one of eight fixed lines,
// whatever the failure, so no host, user, password, DSN, certificate detail
// or driver text can reach it.
func TestDBCheckLineIsSanitized(t *testing.T) {
	allowed := map[string]int{"PASS": 0, "FAIL config": 3, "FAIL dns": 3, "FAIL refused": 3, "FAIL tls": 3, "FAIL auth": 3, "FAIL timeout": 3, "FAIL query": 3}
	for _, class := range []string{"", DBCheckConfig, DBCheckDNS, DBCheckRefused, DBCheckTLS, DBCheckAuth, DBCheckTimeout, DBCheckQuery, "x509: certificate signed by unknown authority", productionDBHost, dbCheckPassword} {
		line, code := DBCheckLine(class)
		if want, ok := allowed[line]; !ok || want != code {
			t.Fatalf("DBCheckLine(%q) = %q, %d", class, line, code)
		}
		for _, leak := range []string{productionDBHost, productionDBUser, dbCheckPassword, "5432", "x509", "postgres", "sslmode", dbCheckFixtureIP} {
			if strings.Contains(line, leak) {
				t.Fatalf("line %q leaks %q", line, leak)
			}
		}
	}
}

// TestDBCheckProductionDefaultsAreFixed pins the production entry: the mounted
// root, the store's own connection rules, and the fixed deadlines.
func TestDBCheckProductionDefaultsAreFixed(t *testing.T) {
	if dbCheckQueryTimeout != 3*time.Second || dbCheckTotalTimeout != 10*time.Second || ProductionRoot != "/etc/dev-env-portal" {
		t.Fatal("db-check defaults changed")
	}
	roots, err := postgresRoots(newDBCheckCA(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour)).pem)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := postgresPoolConfig(productionDBHost, productionDBUser, dbCheckPassword, roots)
	if err != nil {
		t.Fatal(err)
	}
	c := pool.ConnConfig
	if c.Host != productionDBHost || c.Port != 5432 || c.Database != "dev_env_v2_portal" || c.User != productionDBUser || c.ConnectTimeout != 5*time.Second || c.TLSConfig == nil || c.TLSConfig.ServerName != productionDBHost || c.TLSConfig.InsecureSkipVerify || len(c.Fallbacks) != 0 || c.ValidateConnect == nil {
		t.Fatal("db-check would not use the store's connection rules")
	}
}

// TestDBCheckAuthCoversEveryRefusalAfterTLS: "auth" is any end of the session
// after a good TLS handshake and before the login completes, not only a bad
// password (DESIGN-056 section 3).
func TestDBCheckAuthCoversEveryRefusalAfterTLS(t *testing.T) {
	interceptDBCheckResolver(t)
	ca := newDBCheckCA(t, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	for _, code := range []string{"28P01", "28000", "3D000", "53300", "57P01", "57P03"} {
		server := newFakePostgres(ca.leaf(t, productionDBHost), "")
		server.loginError = code
		network := &dbCheckNet{server: server}
		if got, _ := checkDatabaseWithin(t, 10*time.Second, writeDBCheckRoot(t, dbCheckFiles(ca.pem)), network.seams()); got != DBCheckAuth {
			t.Fatalf("SQLSTATE %s: class %q, want auth", code, got)
		}
	}
	// A connection dropped after the handshake, before any login answer.
	server := newFakePostgres(ca.leaf(t, productionDBHost), "drop-after-tls")
	network := &dbCheckNet{server: server}
	if got, _ := checkDatabaseWithin(t, 10*time.Second, writeDBCheckRoot(t, dbCheckFiles(ca.pem)), network.seams()); got != DBCheckAuth {
		t.Fatalf("dropped after TLS: class %q, want auth", got)
	}
	<-server.done
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.serverName != productionDBHost || server.startup == nil {
		t.Fatal("the dropped case must complete TLS and send the startup message first")
	}
}

// TestDBCheckCloseIsBounded: the operation has its 10 s deadline (5 s connect,
// 3 s query inside it) and the close has its own deadline of at most 1 s. A
// stalled close, a query timeout and the total deadline each return within
// their bounds with one sanitized line, and no second connection is dialed.
func TestDBCheckCloseIsBounded(t *testing.T) {
	interceptDBCheckResolver(t)
	if dbCheckCloseTimeout != time.Second {
		t.Fatal("close bound changed")
	}
	ca := newDBCheckCA(t, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	// Bounds are measured against each case's own deadline. The slack allows a
	// CPU-starved runner, and stays far below crypto/tls's 5 s close_notify
	// deadline that an unbounded close would wait for.
	const closeBound = 200 * time.Millisecond
	const slack = 1500 * time.Millisecond
	for _, tc := range []struct {
		name, mode     string
		connect, query time.Duration
		total          time.Duration
		want           string
		least, most    time.Duration
	}{
		{name: "close stalls after a pass", mode: "hang-close", connect: 2 * time.Second, query: time.Second, total: 4 * time.Second, want: "", least: closeBound / 2, most: closeBound + slack},
		{name: "query deadline", mode: "hang-query", connect: 2 * time.Second, query: 300 * time.Millisecond, total: 4 * time.Second, want: DBCheckTimeout, least: 250 * time.Millisecond, most: 300*time.Millisecond + closeBound + slack},
		{name: "total deadline during login", mode: "hang-login", connect: 2 * time.Second, query: time.Second, total: 300 * time.Millisecond, want: DBCheckTimeout, least: 250 * time.Millisecond, most: 300*time.Millisecond + closeBound + slack},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newFakePostgres(ca.leaf(t, productionDBHost), tc.mode)
			t.Cleanup(func() { close(server.release) })
			network := &dbCheckNet{server: server}
			seams := network.seams()
			seams.connectTimeout, seams.queryTimeout, seams.total, seams.closeTimeout = tc.connect, tc.query, tc.total, closeBound
			got, elapsed := checkDatabaseWithin(t, 10*time.Second, writeDBCheckRoot(t, dbCheckFiles(ca.pem)), seams)
			if got != tc.want || elapsed < tc.least || elapsed > tc.most {
				t.Fatalf("class %q in %v, want %q within [%v, %v]", got, elapsed, tc.want, tc.least, tc.most)
			}
			if line, _ := DBCheckLine(got); line != "PASS" && line != "FAIL "+tc.want {
				t.Fatalf("line %q", line)
			}
			network.mu.Lock()
			defer network.mu.Unlock()
			if len(network.dials) != 1 {
				t.Fatalf("dials %v: a deadline must not dial a cancel request", network.dials)
			}
		})
	}
}
