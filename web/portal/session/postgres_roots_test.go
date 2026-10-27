package session

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"
)

// CNPG renews its CA with the same key and subject. A server certificate
// signed after the renewal must verify against the pinned, now expired, CA;
// a CA with another key, a hostname mismatch and an expired server
// certificate must not.
func TestPostgresRootsSurviveCNPGRenewal(t *testing.T) {
	const host = "postgres16-rw.database.svc.cluster.local"
	now := time.Now()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := func(k *ecdsa.PrivateKey, from, to time.Time) *x509.Certificate {
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(from.UnixNano()), Subject: pkix.Name{OrganizationalUnit: []string{"database"}, CommonName: "postgres16"},
			NotBefore: from, NotAfter: to, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := x509.ParseCertificate(der)
		return c
	}
	leaf := func(parent *x509.Certificate, signer *ecdsa.PrivateKey, from, to time.Time) *x509.Certificate {
		lk, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "postgres16-rw"}, DNSNames: []string{host},
			NotBefore: from, NotAfter: to, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &lk.PublicKey, signer)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := x509.ParseCertificate(der)
		return c
	}
	pinned := ca(key, now.Add(-100*24*time.Hour), now.Add(-10*24*time.Hour)) // expired ten days ago
	renewed := ca(key, now.Add(-time.Hour), now.Add(80*24*time.Hour))
	server := leaf(renewed, key, now.Add(-time.Hour), now.Add(80*24*time.Hour))
	pinnedPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pinned.Raw})

	strict := x509.NewCertPool()
	strict.AddCert(pinned)
	var expired x509.CertificateInvalidError
	if _, err := server.Verify(x509.VerifyOptions{Roots: strict, DNSName: host}); !errors.As(err, &expired) || expired.Reason != x509.Expired {
		t.Fatalf("an expired root must fail Go's own check, or this test proves nothing: %v", err)
	}
	roots, err := postgresRoots(pinnedPEM)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Verify(x509.VerifyOptions{Roots: roots, DNSName: host}); err != nil {
		t.Fatalf("renewed server certificate was refused: %v", err)
	}
	if _, err := server.Verify(x509.VerifyOptions{Roots: roots, DNSName: "wrong.example"}); err == nil {
		t.Fatal("hostname mismatch accepted")
	}
	stale := leaf(renewed, key, now.Add(-90*24*time.Hour), now.Add(-time.Hour))
	if _, err := stale.Verify(x509.VerifyOptions{Roots: roots, DNSName: host}); err == nil {
		t.Fatal("expired server certificate accepted")
	}
	otherKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	other := ca(otherKey, now.Add(-time.Hour), now.Add(80*24*time.Hour))
	forged := leaf(other, otherKey, now.Add(-time.Hour), now.Add(80*24*time.Hour))
	var unknown x509.UnknownAuthorityError
	if _, err := forged.Verify(x509.VerifyOptions{Roots: roots, DNSName: host}); !errors.As(err, &unknown) {
		t.Fatalf("certificate from another key accepted: %v", err)
	}
	broken := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")})
	for name, input := range map[string][]byte{"empty": nil, "garbage": []byte("not pem"), "bad DER": broken} {
		if _, err := postgresRoots(input); err == nil {
			t.Errorf("%s accepted as a CA", name)
		}
	}
}
