package session

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thaynes43/dev-env/web/portal"
)

// A disposable wire-level proxy suppresses the real backend's COMMIT response
// after PostgreSQL has committed. It terminates verified TLS at both legs using
// ONLY the runner's synthetic localhost certificate. No auth/header is decoded,
// logged or retained; no live destination, credential or proxy is supported.
func commitLossPool(t *testing.T) (*pgxpool.Pool, <-chan int) {
	t.Helper()
	u, err := url.Parse(os.Getenv("PORTAL_TEST_DATABASE_URL"))
	if err != nil || u.Host != "localhost:5432" {
		t.Fatal("commit-loss fixture requires disposable localhost:5432")
	}
	certPath := u.Query().Get("sslrootcert")
	cert, err := tls.LoadX509KeyPair(certPath, filepath.Join(filepath.Dir(certPath), "server.key"))
	if err != nil {
		t.Fatal("synthetic fixture certificate failed")
	}
	config := integrationRuntimeConfig(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("fixture listener failed")
	}
	t.Cleanup(func() { _ = listener.Close() })
	result := make(chan int, 1)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		committed := 0
		defer func() { result <- committed; close(result) }()
		front, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = front.Close() }()
		_ = front.SetDeadline(time.Now().Add(5 * time.Second))
		// Initial SSLRequest: length8, PostgreSQL's fixed SSL negotiation code.
		request := make([]byte, 8)
		if _, err := io.ReadFull(front, request); err != nil || binary.BigEndian.Uint32(request[:4]) != 8 || binary.BigEndian.Uint32(request[4:]) != 80877103 {
			return
		}
		if _, err := front.Write([]byte{'S'}); err != nil {
			return
		}
		client := tls.Server(front, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		if client.Handshake() != nil {
			return
		}
		back, err := net.DialTimeout("tcp4", "127.0.0.1:5432", time.Second)
		if err != nil {
			return
		}
		defer func() { _ = back.Close() }()
		_ = back.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := back.Write(request); err != nil {
			return
		}
		accepted := make([]byte, 1)
		if _, err := io.ReadFull(back, accepted); err != nil || accepted[0] != 'S' {
			return
		}
		server := tls.Client(back, config.ConnConfig.TLSConfig.Clone())
		if server.Handshake() != nil {
			return
		}
		// Startup is untyped. Copy its bounded frame, then relay opaque frontend
		// frames. Backend framing alone is inspected for a COMMIT CommandComplete.
		size := make([]byte, 4)
		if _, err := io.ReadFull(client, size); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(size)
		if n < 8 || n > 16*1024 {
			return
		}
		startup := make([]byte, n-4)
		if _, err := io.ReadFull(client, startup); err != nil {
			return
		}
		if _, err := server.Write(append(size, startup...)); err != nil {
			return
		}
		copyDone := make(chan struct{})
		go func() { _, _ = io.Copy(server, client); close(copyDone) }()
		defer func() { _ = front.Close(); _ = back.Close(); <-copyDone }()
		for {
			header := make([]byte, 5)
			if _, err := io.ReadFull(server, header); err != nil {
				return
			}
			length := binary.BigEndian.Uint32(header[1:])
			if length < 4 || length > 16*1024 {
				return
			}
			body := make([]byte, length-4)
			if _, err := io.ReadFull(server, body); err != nil {
				return
			}
			if header[0] == 'C' && bytes.Equal(body, []byte("COMMIT\x00")) {
				committed++
				return
			}
			if _, err := client.Write(append(header, body...)); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); workers.Wait() })
	config.ConnConfig.Port = uint16(listener.Addr().(*net.TCPAddr).Port)
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal("commit-loss runtime pool failed")
	}
	t.Cleanup(pool.Close)
	return pool, result
}

func testActualLostCommitResponse(t *testing.T, admin *pgxpool.Pool, record Record) {
	t.Helper()
	h := Handle{211}
	s := fixtureStore(t)
	pool, writeResult := commitLossPool(t)
	s.pool = pool
	if err := s.PutSession(context.Background(), h, record); !errors.Is(err, portal.ErrUnavailable) {
		t.Fatal("lost actual COMMIT response returned success")
	}
	if <-writeResult != 1 {
		t.Fatal("fixture did not suppress actual COMMIT response")
	}
	var count int
	if admin.QueryRow(context.Background(), "SELECT count(*) FROM portal.sessions WHERE handle=$1", h[:]).Scan(&count) != nil || count != 1 {
		t.Fatal("commit-loss fixture did not commit exactly one row")
	}
	// A successful SQL read is also discarded when its COMMIT result is uncertain.
	readStore := fixtureStore(t)
	readPool, readResult := commitLossPool(t)
	readStore.pool = readPool
	got, err := readStore.GetSession(context.Background(), h)
	if !errors.Is(err, portal.ErrUnavailable) || got.Principal.Subject != "" || got.accessToken != "" {
		t.Fatal("read payload escaped after lost actual COMMIT response")
	}
	if <-readResult != 1 {
		t.Fatal("fixture did not suppress read transaction COMMIT response")
	}
	t.Log("UNCERTAIN_COMMIT_EVIDENCE actual PostgreSQL COMMIT CommandComplete suppressed over disposable TLS; one durable insert, zero read payload returned, no write retry")
}
