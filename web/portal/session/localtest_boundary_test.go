package session

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"go/build"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

// DESIGN-055: the local test instance and its fixed principal compile only
// with the portallocaltest tag. These tests run in the default build, the one
// make build-portal and the image use.

func TestLocalTestExcludedFromDefaultBuild(t *testing.T) {
	plain := build.Default
	plain.BuildTags = nil
	pkg, err := plain.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(pkg.GoFiles, "localtest.go") || !slices.Contains(pkg.IgnoredGoFiles, "localtest.go") {
		t.Fatalf("localtest.go is in the default build: %v", pkg.GoFiles)
	}
	for _, name := range pkg.GoFiles {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "portallocaltest") || strings.Contains(string(raw), "issuer.localtest.invalid") {
			t.Fatalf("%s mentions the local test boundary", name)
		}
	}
	tagged := plain
	tagged.BuildTags = []string{"portallocaltest"}
	if pkg, err = tagged.ImportDir(".", 0); err != nil || !slices.Contains(pkg.GoFiles, "localtest.go") {
		t.Fatalf("tagged build lost localtest.go: %v", err)
	}
	var none *build.NoGoError
	if _, err = plain.ImportDir("../localtest", 0); !errors.As(err, &none) {
		t.Fatalf("web/portal/localtest builds without the tag: %v", err)
	}
	for _, path := range []string{"../../../images/portal/Dockerfile", "../../../cmd/dev-env-portal/main.go"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "portallocaltest") || strings.Contains(string(raw), "-tags") {
			t.Fatalf("%s selects build tags", path)
		}
	}
	makefile, err := os.ReadFile("../../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(makefile), "\n") {
		if strings.Contains(line, "./cmd/dev-env-portal") && strings.Contains(line, "-tags") {
			t.Fatalf("Makefile builds the portal with tags: %s", line)
		}
	}
}

// TestProductionRefusesLocalTestShapedRequests sends the production gate what
// the local test gate would accept or supply: plain HTTP, a loopback or test
// host, forwarded identity, and a session minted for the local test issuer.
func TestProductionRefusesLocalTestShapedRequests(t *testing.T) {
	now := time.Now()
	store := &memoryStore{logins: map[Handle]Login{}, sessions: map[Handle]Record{}}
	valid := base64.RawURLEncoding.EncodeToString(bytes32(5))
	store.sessions[sha256.Sum256([]byte(valid))] = Record{Principal: portal.Principal{Issuer: productionIssuer, Subject: "synthetic-subject"}, CSRF: strings.Repeat("c", 43), CreatedAt: now.Add(-time.Minute), IdleExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(time.Hour), accessToken: "synthetic-access-token", tokenExpiresAt: now.Add(time.Hour)}
	foreign := base64.RawURLEncoding.EncodeToString(bytes32(6))
	store.sessions[sha256.Sum256([]byte(foreign))] = Record{Principal: portal.Principal{Issuer: "https://issuer.localtest.invalid/fixture-only/", Subject: "local-test-reader"}, CSRF: strings.Repeat("d", 43), CreatedAt: now.Add(-time.Minute), IdleExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(time.Hour), accessToken: "local-test-fixture-access-token", tokenExpiresAt: now.Add(time.Hour)}
	engine := testEngine(t, store, now)
	allow := authorizeFunc(func(_ context.Context, p portal.Principal, _ portal.Resource) error {
		if p.Issuer != productionIssuer {
			return portal.ErrDenied
		}
		return nil
	})
	handler := productionGate(NewAssembly(AssemblyConfig{Session: engine, Authorizer: allow, Reader: portal.UnavailableReader{}, FreshFor: time.Minute, Now: time.Now}), productionHost)
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := serve(portalRequest(http.MethodGet, SessionsPath, valid)); w.Code != http.StatusOK {
		t.Fatalf("control request refused: %d", w.Code)
	}
	if _, err := engine.Authenticate(portalRequest(http.MethodGet, SessionsPath, foreign)); !errors.Is(err, portal.ErrDenied) {
		t.Fatalf("local test issuer authenticated in production: %v", err)
	}
	if w := serve(portalRequest(http.MethodGet, SessionsPath, foreign)); w.Code == http.StatusOK {
		t.Fatal("local test session read a production page")
	}
	plain := portalRequest(http.MethodGet, SessionsPath, valid)
	plain.TLS = nil
	if w := serve(plain); w.Code == http.StatusOK {
		t.Fatal("plain HTTP accepted")
	}
	for _, host := range []string{"127.0.0.1:8099", "localhost:8099", "[::1]:8099", "portal.localtest.invalid"} {
		r := portalRequest(http.MethodGet, SessionsPath, valid)
		r.Host = host
		if w := serve(r); w.Code == http.StatusOK {
			t.Fatalf("host %s accepted", host)
		}
	}
	for _, header := range forwardedIdentity {
		r := portalRequest(http.MethodGet, SessionsPath, valid)
		r.Header.Set(header, "local-test-reader")
		if w := serve(r); w.Code != http.StatusForbidden {
			t.Fatalf("%s: status %d", header, w.Code)
		}
	}
}
