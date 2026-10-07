package apiserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// writeCert writes a self-signed tls.crt and tls.key for 127.0.0.1 into dir, as
// cert-manager's Secret mount would hold them, and returns a pool that trusts it.
func writeCert(t *testing.T, dir string) *x509.CertPool {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "dev-env-operator"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	crt := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), crt, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(crt)
	return pool
}

// startRunner serves h over HTTPS on a free loopback port until the test ends.
func startRunner(t *testing.T, h http.Handler) (*Runner, *x509.CertPool, string) {
	t.Helper()
	dir := t.TempDir()
	pool := writeCert(t, dir)
	r := &Runner{Addr: "127.0.0.1:0", CertDir: dir, Handler: h, ErrorLog: log.New(io.Discard, "", 0)}
	if r.NeedLeaderElection() {
		t.Fatal("the API must serve on every replica")
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- r.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errc:
			if err != nil {
				t.Errorf("Start returned %v after a clean stop", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("Start did not return after its context ended")
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for r.ReadyCheck(nil) != nil {
		select {
		case err := <-errc:
			t.Fatalf("Start: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the API never listened")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return r, pool, "https://" + r.boundAddr()
}

func TestRunnerServesTLS13(t *testing.T) {
	f := newFixture(t)
	_, pool, base := startRunner(t, f.h)

	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	resp, err := c.Get(base + apiv1.FleetPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.TLS == nil || resp.TLS.Version != tls.VersionTLS13 {
		t.Fatalf("status %d, TLS %+v", resp.StatusCode, resp.TLS)
	}

	old := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MaxVersion: tls.VersionTLS12}}}
	if resp, err := old.Get(base + apiv1.FleetPath); err == nil {
		_ = resp.Body.Close()
		t.Fatal("a TLS 1.2 client was served")
	}
}

func TestRunnerNeedsItsCertificate(t *testing.T) {
	r := &Runner{Addr: "127.0.0.1:0", CertDir: t.TempDir(), Handler: http.NotFoundHandler()}
	if err := r.Start(context.Background()); err == nil {
		t.Fatal("started with no certificate")
	}
	if r.ReadyCheck(nil) == nil {
		t.Fatal("ready with no certificate")
	}
}
