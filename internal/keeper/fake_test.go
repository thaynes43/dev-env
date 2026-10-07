package keeper

// Fixtures: a test App (an RSA key made once per run, written to a directory as
// the ExternalSecret's mount would hold it), a fake GitHub that checks the App's
// JWT and mints numbered tokens, and a log buffer the tests search for
// material. No value here is a real credential.

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

const (
	testClientID       = "Iv23testclientid"
	testInstallationID = "4242"
	// leakSentinel is put where a careless implementation would copy it into a
	// log line: GitHub error bodies and API server messages.
	leakSentinel = "ghs_LEAKSENTINEL0123456789"
)

var (
	keyOnce  sync.Once
	testKeys [2]*rsa.PrivateKey
	keyErr   error
)

// keys returns two RSA keys, made once per test run: a current one and one to
// rotate to.
func keys(t *testing.T) [2]*rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		for i := range testKeys {
			testKeys[i], keyErr = rsa.GenerateKey(rand.Reader, 2048)
			if keyErr != nil {
				return
			}
		}
	})
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	return testKeys
}

func pkcs1PEM(key *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// writeAppDir writes an App directory: client-id, installation-id and the key
// in PKCS #1, as GitHub hands it out.
func writeAppDir(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, AppFileClientID), testClientID+"\n")
	writeFile(t, filepath.Join(dir, AppFileInstallationID), testInstallationID+"\n")
	writeFile(t, filepath.Join(dir, AppFilePrivateKey), string(pkcs1PEM(key)))
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// failure is one canned refusal.
type failure struct {
	status int
	body   string
}

// fakeGitHub is GitHub's access_tokens endpoint for one installation. It checks
// the JWT against the public key in pub, then answers with the next queued
// failure or mints ghs_test_<n>, valid for life.
type fakeGitHub struct {
	t   *testing.T
	srv *httptest.Server
	now func() time.Time

	mu       sync.Mutex
	pub      *rsa.PublicKey
	issuer   any // what the JWT's iss must be
	life     time.Duration
	failures []failure
	mints    int
	requests int
	lastBody map[string]map[string]string
	lastJWT  string
	tokens   []string
	errs     []string
}

func newFakeGitHub(t *testing.T, pub *rsa.PublicKey, now func() time.Time) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{t: t, pub: pub, now: now, issuer: testClientID, life: time.Hour}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	fail := func(status int, msg string) {
		f.errs = append(f.errs, msg)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
	}
	if r.Method != http.MethodPost || r.URL.Path != "/app/installations/"+testInstallationID+"/access_tokens" {
		fail(http.StatusNotFound, "unexpected "+r.Method+" "+r.URL.Path)
		return
	}
	for h, want := range map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"Content-Type":         "application/json",
	} {
		if got := r.Header.Get(h); got != want {
			fail(http.StatusBadRequest, fmt.Sprintf("header %s = %q", h, got))
			return
		}
	}
	if r.Header.Get("User-Agent") == "" {
		fail(http.StatusForbidden, "no User-Agent")
		return
	}
	jwt, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		fail(http.StatusUnauthorized, "no bearer JWT")
		return
	}
	if msg := f.checkJWT(jwt); msg != "" {
		fail(http.StatusUnauthorized, msg)
		return
	}
	f.lastJWT = jwt
	var body map[string]map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(http.StatusBadRequest, "body: "+err.Error())
		return
	}
	f.lastBody = body
	if len(f.failures) > 0 {
		next := f.failures[0]
		f.failures = f.failures[1:]
		w.WriteHeader(next.status)
		_, _ = w.Write([]byte(next.body))
		return
	}
	f.mints++
	tok := fmt.Sprintf("ghs_test_%d_%s", f.mints, strings.Repeat("x", 20))
	f.tokens = append(f.tokens, tok)
	perms := map[string]string{"metadata": "read"}
	for k, v := range body["permissions"] {
		perms[k] = v
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":                tok,
		"expires_at":           f.now().Add(f.life).UTC().Format(time.RFC3339),
		"permissions":          perms,
		"repository_selection": "all",
	})
}

// checkJWT verifies the App JWT as GitHub would; "" means it passes.
func (f *fakeGitHub) checkJWT(jwt string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return "JWT has no three parts"
	}
	enc := base64.RawURLEncoding
	var header map[string]string
	if b, err := enc.DecodeString(parts[0]); err != nil || json.Unmarshal(b, &header) != nil {
		return "JWT header"
	}
	if header["alg"] != "RS256" {
		return "JWT alg " + header["alg"]
	}
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		return "JWT signature encoding"
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(f.pub, crypto.SHA256, digest[:], sig); err != nil {
		return "A JSON web token could not be decoded"
	}
	var claims map[string]any
	b, err := enc.DecodeString(parts[1])
	if err != nil {
		return "JWT claims encoding"
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&claims); err != nil {
		return "JWT claims"
	}
	iat, err1 := claims["iat"].(json.Number).Int64()
	exp, err2 := claims["exp"].(json.Number).Int64()
	// The JWT is dated when it is signed and checked here a moment later.
	if age := f.now().Unix() - iat; err1 != nil || err2 != nil || exp-iat != 600 || age < 60 || age > 65 {
		return fmt.Sprintf("JWT times iat=%v exp=%v", claims["iat"], claims["exp"])
	}
	switch want := f.issuer.(type) {
	case string:
		if claims["iss"] != want {
			return fmt.Sprintf("JWT iss %v", claims["iss"])
		}
	case int64:
		if n, ok := claims["iss"].(json.Number); !ok || n.String() != fmt.Sprint(want) {
			return fmt.Sprintf("JWT iss %v is not the number %d", claims["iss"], want)
		}
	}
	return ""
}

func (f *fakeGitHub) failNext(fs ...failure) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, fs...)
}

func (f *fakeGitHub) setPub(pub *rsa.PublicKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pub = pub
}

func (f *fakeGitHub) snapshot() (mints, requests int, tokens, errs []string, jwt string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mints, f.requests, append([]string(nil), f.tokens...), append([]string(nil), f.errs...), f.lastJWT
}

// app is a GitHubApp against f.
func (f *fakeGitHub) app(dir string) *GitHubApp {
	return &GitHubApp{
		Dir:         dir,
		APIURL:      f.srv.URL,
		Permissions: DefaultDevBotPermissions,
		UserAgent:   "dev-env-keeper/test",
	}
}

// logBuffer collects JSON log lines for searching.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *logBuffer) logger() logr.Logger {
	return logr.FromSlogHandler(slog.NewJSONHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// assertNoMaterial fails if the logs hold any token, the JWT, the sentinel or
// any line of the key.
func assertNoMaterial(t *testing.T, logs string, keyPEM []byte, secrets ...string) {
	t.Helper()
	if logs == "" {
		t.Fatal("no logs captured; the check would prove nothing")
	}
	for _, s := range append(secrets, leakSentinel) {
		if s != "" && strings.Contains(logs, strings.TrimSpace(s)) {
			t.Errorf("the logs hold material %.12s...:\n%s", s, logs)
		}
	}
	for _, line := range strings.Split(string(keyPEM), "\n") {
		if len(line) > 20 && !strings.HasPrefix(line, "-----") && strings.Contains(logs, line) {
			t.Errorf("the logs hold a line of the private key")
		}
	}
	if strings.Contains(logs, "ghs_test_") {
		t.Errorf("the logs hold a minted token:\n%s", logs)
	}
}

// eventually polls cond every 5 ms for up to 10 s: for conditions on memory.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	poll(t, what, 5*time.Millisecond, 10*time.Second, cond)
}

// poll checks cond every interval until it holds, or fails the test after
// timeout.
func poll(t *testing.T, what string, interval, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(interval)
	}
}
