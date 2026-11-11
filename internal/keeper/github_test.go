package keeper

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestMintSignsAJWTAndDownScopes(t *testing.T) {
	k := keys(t)
	gh := newFakeGitHub(t, &k[0].PublicKey, time.Now)
	app := gh.app(writeAppDir(t, k[0]))

	tok, err := app.Mint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, _, tokens, _, _ := gh.snapshot()
	if len(tokens) != 1 || tok.Token.Reveal() != tokens[0] {
		t.Fatalf("got a token other than the one minted")
	}
	if life := time.Until(tok.ExpiresAt); life < 59*time.Minute || life > time.Hour {
		t.Errorf("expires in %s, want about an hour", life)
	}
	if tok.RepositorySelection != "all" {
		t.Errorf("repository selection %q", tok.RepositorySelection)
	}
	if !reflect.DeepEqual(gh.lastBody["permissions"], DefaultDevBotPermissions) {
		t.Errorf("asked for %v, want v1's set %v", gh.lastBody["permissions"], DefaultDevBotPermissions)
	}
	wantBody, err := json.Marshal(map[string]any{"permissions": DefaultDevBotPermissions})
	if err != nil {
		t.Fatal(err)
	}
	if string(gh.lastRaw) != string(wantBody) || len(gh.lastRepositories) != 0 {
		t.Errorf("dev mint body %s, want %s", gh.lastRaw, wantBody)
	}
	if missing := app.MissingPermissions(tok.Permissions); len(missing) != 0 {
		t.Errorf("missing %v from a full grant", missing)
	}
}

func TestMintRequestsTheOpsRepositories(t *testing.T) {
	k := keys(t)
	gh := newFakeGitHub(t, &k[0].PublicKey, time.Now)
	gh.repoEcho = "not-for-logs"
	app := gh.app(writeAppDir(t, k[0]))
	app.Permissions = DefaultOpsBotPermissions
	app.Repositories = []string{"haynes-ops", "cigar-journal"}

	tok, err := app.Mint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.RepositorySelection != "selected" {
		t.Errorf("repository selection %q", tok.RepositorySelection)
	}
	if !reflect.DeepEqual(gh.lastRepositories, app.Repositories) {
		t.Errorf("repositories %v", gh.lastRepositories)
	}
	if !reflect.DeepEqual(gh.lastBody["permissions"], DefaultOpsBotPermissions) {
		t.Errorf("permissions %v", gh.lastBody["permissions"])
	}
	wantBody, err := json.Marshal(map[string]any{
		"permissions":  DefaultOpsBotPermissions,
		"repositories": app.Repositories,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(gh.lastRaw) != string(wantBody) {
		t.Errorf("ops mint body %s, want %s", gh.lastRaw, wantBody)
	}
	if strings.Contains(fmt.Sprint(tok), "not-for-logs") || strings.Contains(fmt.Sprintf("%+v", tok), "not-for-logs") {
		t.Errorf("the token value carries repository names: %+v", tok)
	}
}

func TestParseRepositories(t *testing.T) {
	got, err := ParseRepositories(" haynes-ops, cigar-journal ")
	if err != nil || !reflect.DeepEqual(got, []string{"haynes-ops", "cigar-journal"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := ParseRepositories(""); err != nil || got != nil {
		t.Fatalf("empty: %v %v", got, err)
	}
	got, err = ParseRepositories(".github")
	if err != nil || !reflect.DeepEqual(got, []string{".github"}) {
		t.Fatalf(".github: %v %v", got, err)
	}
	for _, bad := range []string{"thaynes43/haynes-ops", "haynes-ops,haynes-ops", "haynes-ops,", "*", ".", ".."} {
		if _, err := ParseRepositories(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestDistinctAppsMintAndASharedIssuerDoesNot(t *testing.T) {
	pair := keys(t)
	devGH := newFakeGitHub(t, &pair[0].PublicKey, time.Now)
	opsGH := newFakeGitHub(t, &pair[1].PublicKey, time.Now)
	const opsClient = "Iv23opsclientid"
	const opsInstallation = "4343"
	opsGH.issuer = opsClient
	opsGH.installationID = opsInstallation
	devDir := writeAppDir(t, pair[0])
	opsDir := writeAppDir(t, pair[1])
	writeFile(t, filepath.Join(opsDir, AppFileClientID), opsClient+"\n")
	writeFile(t, filepath.Join(opsDir, AppFileInstallationID), opsInstallation+"\n")
	devApp := devGH.app(devDir)
	opsApp := opsGH.app(opsDir)
	opsApp.Permissions = DefaultOpsBotPermissions
	opsApp.Repositories = []string{"haynes-ops"}
	opsApp.DistinctFrom = devApp

	if _, err := opsApp.Mint(context.Background()); err != nil {
		t.Fatalf("two Apps, different issuers and keys: %v", err)
	}
	if _, reqs, _, _, _ := opsGH.snapshot(); reqs != 1 {
		t.Fatalf("distinct ops mint made %d requests, want 1", reqs)
	}
	if _, reqs, _, _, _ := devGH.snapshot(); reqs != 0 {
		t.Fatalf("the dev bot's GitHub was called: %d", reqs)
	}

	for _, dir := range []string{devDir, opsDir} {
		if err := os.Remove(filepath.Join(dir, AppFileClientID)); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, AppFileAppID), "4291021\n")
	}
	devGH.issuer = int64(4291021)
	opsGH.issuer = int64(4291021)
	_, before, _, _, _ := opsGH.snapshot()
	_, err := opsApp.Mint(context.Background())
	if err == nil || !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("same app id, no client id: %v", err)
	}
	if strings.Contains(err.Error(), "4291021") || strings.Contains(err.Error(), opsClient) {
		t.Fatalf("the refusal quotes an issuer: %v", err)
	}
	if _, reqs, _, _, _ := opsGH.snapshot(); reqs != before {
		t.Fatalf("same issuer sent a mint: requests %d, before %d", reqs, before)
	}
}

func TestOpsMintRefusesTheSameInstallationEvenWhenIssuerFilesDiffer(t *testing.T) {
	pair := keys(t)
	devGH := newFakeGitHub(t, &pair[0].PublicKey, time.Now)
	opsGH := newFakeGitHub(t, &pair[1].PublicKey, time.Now)
	opsGH.issuer = int64(4291021)
	devDir := writeAppDir(t, pair[0])
	opsDir := writeAppDir(t, pair[1])
	if err := os.Remove(filepath.Join(opsDir, AppFileClientID)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(opsDir, AppFileAppID), "4291021\n")
	devApp := devGH.app(devDir)
	opsApp := opsGH.app(opsDir)
	opsApp.Permissions = DefaultOpsBotPermissions
	opsApp.Repositories = []string{"haynes-ops"}
	opsApp.DistinctFrom = devApp

	_, err := opsApp.Mint(context.Background())
	if err == nil || !strings.Contains(err.Error(), "installation") {
		t.Fatalf("client-id against app-id, same installation: %v", err)
	}
	if strings.Contains(err.Error(), testInstallationID) || strings.Contains(err.Error(), "4291021") {
		t.Fatalf("the refusal quotes an id: %v", err)
	}
	if _, reqs, _, _, _ := opsGH.snapshot(); reqs != 0 {
		t.Fatalf("same installation sent a mint: %d requests", reqs)
	}
}

func TestMintRefusesAnUnscopedToken(t *testing.T) {
	k := keys(t)
	gh := newFakeGitHub(t, &k[0].PublicKey, time.Now)
	app := gh.app(writeAppDir(t, k[0]))
	app.Permissions = nil
	if _, err := app.Mint(context.Background()); err == nil || !strings.Contains(err.Error(), "unscoped") {
		t.Fatalf("got %v, want a refusal", err)
	}
	if _, requests, _, _, _ := gh.snapshot(); requests != 0 {
		t.Errorf("%d requests reached GitHub", requests)
	}
}

func TestMintIssuerIsTheAppIDWithoutAClientID(t *testing.T) {
	k := keys(t)
	gh := newFakeGitHub(t, &k[0].PublicKey, time.Now)
	gh.issuer = int64(4291021)
	dir := writeAppDir(t, k[0])
	if err := os.Remove(filepath.Join(dir, AppFileClientID)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, AppFileAppID), "4291021\n")
	if _, err := gh.app(dir).Mint(context.Background()); err != nil {
		t.Fatalf("with app-id only: %v", err)
	}
}

func TestMintRereadsTheKeyEachTime(t *testing.T) {
	k := keys(t)
	gh := newFakeGitHub(t, &k[0].PublicKey, time.Now)
	dir := writeAppDir(t, k[0])
	app := gh.app(dir)
	if _, err := app.Mint(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The key is rotated in 1Password and the kubelet syncs the new one.
	gh.setPub(&k[1].PublicKey)
	if _, err := app.Mint(context.Background()); err == nil {
		t.Fatal("GitHub accepted the old key after the rotation; the fake is not checking")
	}
	writeFile(t, filepath.Join(dir, AppFilePrivateKey), string(pkcs1PEM(k[1])))
	if _, err := app.Mint(context.Background()); err != nil {
		t.Fatalf("after the key file changed: %v", err)
	}
}

func TestMintRefusalKeepsOnlyGitHubsMessage(t *testing.T) {
	k := keys(t)
	gh := newFakeGitHub(t, &k[0].PublicKey, time.Now)
	app := gh.app(writeAppDir(t, k[0]))
	gh.failNext(
		failure{http.StatusUnprocessableEntity, `{"message":"The permissions requested are not granted to this installation.","token":"` + leakSentinel + `","documentation_url":"https://docs.github.com/rest"}`},
		failure{http.StatusBadGateway, `<html>upstream ` + leakSentinel + `</html>`},
	)

	_, err := app.Mint(context.Background())
	var ghErr *GitHubError
	if !errors.As(err, &ghErr) || ghErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("got %v, want a 422 GitHubError", err)
	}
	if !strings.Contains(err.Error(), "not granted to this installation") {
		t.Errorf("GitHub's message is lost: %v", err)
	}
	if strings.Contains(err.Error(), leakSentinel) {
		t.Errorf("the error copies the body: %v", err)
	}

	_, err = app.Mint(context.Background())
	if !errors.As(err, &ghErr) || ghErr.Status != http.StatusBadGateway || strings.Contains(err.Error(), leakSentinel) {
		t.Errorf("a non-JSON refusal: %v", err)
	}
}

func TestMintRefusesABadAnswer(t *testing.T) {
	k := keys(t)
	for name, body := range map[string]string{
		"no token":        `{"expires_at":"2099-01-01T00:00:00Z"}`,
		"bad expiry":      `{"token":"` + leakSentinel + `","expires_at":"tomorrow"}`,
		"expired":         `{"token":"` + leakSentinel + `","expires_at":"2001-01-01T00:00:00Z"}`,
		"not JSON at all": `token=` + leakSentinel,
	} {
		t.Run(name, func(t *testing.T) {
			gh := newFakeGitHub(t, &k[0].PublicKey, time.Now)
			gh.failNext(failure{http.StatusCreated, body})
			_, err := gh.app(writeAppDir(t, k[0])).Mint(context.Background())
			if err == nil {
				t.Fatal("accepted")
			}
			if strings.Contains(err.Error(), leakSentinel) {
				t.Errorf("the error holds the token: %v", err)
			}
		})
	}
}

func TestLoadNamesTheProblemNotTheKey(t *testing.T) {
	k := keys(t)
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	rsaPKCS8, err := x509.MarshalPKCS8PrivateKey(k[0])
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		edit    func(dir string)
		wantErr string
	}{
		{"PKCS 8 RSA", func(dir string) {
			writeFile(t, filepath.Join(dir, AppFilePrivateKey), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rsaPKCS8})))
		}, ""},
		{"no issuer", func(dir string) { _ = os.Remove(filepath.Join(dir, AppFileClientID)) }, "neither client-id nor app-id"},
		{"app-id not a number", func(dir string) {
			_ = os.Remove(filepath.Join(dir, AppFileClientID))
			writeFile(t, filepath.Join(dir, AppFileAppID), "dev-bot")
		}, "app-id: not a positive number"},
		{"no installation", func(dir string) { _ = os.Remove(filepath.Join(dir, AppFileInstallationID)) }, "installation-id: missing"},
		{"no key", func(dir string) { _ = os.Remove(filepath.Join(dir, AppFilePrivateKey)) }, "no such file"},
		{"not PEM", func(dir string) { writeFile(t, filepath.Join(dir, AppFilePrivateKey), leakSentinel) }, "no PEM block"},
		{"broken PKCS 1", func(dir string) {
			writeFile(t, filepath.Join(dir, AppFilePrivateKey), string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte(leakSentinel)})))
		}, "not a PKCS #1 RSA key"},
		{"EC key", func(dir string) {
			writeFile(t, filepath.Join(dir, AppFilePrivateKey), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER})))
		}, "not RSA"},
		{"wrong block", func(dir string) {
			writeFile(t, filepath.Join(dir, AppFilePrivateKey), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte(leakSentinel)})))
		}, `"CERTIFICATE"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := writeAppDir(t, k[0])
			c.edit(dir)
			_, err := (&GitHubApp{Dir: dir}).load()
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("got %v, want an error with %q", err, c.wantErr)
			case err != nil && strings.Contains(err.Error(), leakSentinel):
				t.Fatalf("the error quotes the file: %v", err)
			}
		})
	}
}

func TestParsePermissions(t *testing.T) {
	got, err := ParsePermissions(`{"contents":"write","checks":"read"}`)
	if err != nil || !reflect.DeepEqual(got, map[string]string{"contents": "write", "checks": "read"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	b, _ := json.Marshal(DefaultDevBotPermissions)
	if _, err := ParsePermissions(string(b)); err != nil {
		t.Fatalf("the default set: %v", err)
	}
	for _, bad := range []string{``, `{}`, `[]`, `{"contents":"all"}`, `{"Contents":"write"}`, `{"contents":1}`} {
		if _, err := ParsePermissions(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

// gitHubJWTMarker is three base64url parts, the shape of a JWT, and not a real one.
const gitHubJWTMarker = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJzeW50aGV0aWMifQ.c2lnbmF0dXJlMTIzNDU2"

func gitHubTokenMarkers() []string {
	return []string{
		"ghs_SYNTHETICMARKER0123456789",
		"ghp_SYNTHETICMARKER0123456789",
		"gho_SYNTHETICMARKER0123456789",
		"ghu_SYNTHETICMARKER0123456789",
		"ghr_SYNTHETICMARKER0123456789",
		"github_pat_SYNTHETICMARKER0123456789",
	}
}

func gitHubMarkerBlob() string {
	return strings.Join(append(gitHubTokenMarkers(), gitHubJWTMarker, "ECHO_JWT"), " ") + " " + strings.Repeat("b", 800)
}

func assertNoGitHubSecrets(t *testing.T, what, text string, jwts, markers []string) {
	t.Helper()
	for _, jwt := range jwts {
		if len(jwt) >= 24 && strings.Contains(text, jwt[:24]) {
			t.Errorf("%s contains a request JWT", what)
		}
	}
	for _, m := range markers {
		if strings.Contains(text, m) {
			t.Errorf("%s contains synthetic marker %.12s", what, m)
		}
	}
}

// Both App loops mint through Mint and GitHubTokenJob, so redacting here covers both.
func TestMintRedactsGitHubTextInTheError(t *testing.T) {
	k := keys(t)
	gh := newFakeGitHub(t, &k[0].PublicKey, time.Now)
	blob := gitHubMarkerBlob()
	gh.failNext(
		failure{http.StatusUnprocessableEntity, `{"message":"` + blob + `"}`},
		failure{http.StatusCreated, `{"token":"ghs_test_expiry_xxxxxxxxxxxxxxxxxxxx","expires_at":"` + blob + `"}`},
	)
	app := gh.app(writeAppDir(t, k[0]))
	markers := append(gitHubTokenMarkers(), gitHubJWTMarker)
	_, err := app.Mint(context.Background())
	if err == nil {
		t.Fatal("accepted a refusal")
	}
	if len(err.Error()) > maxMessage+80 {
		t.Errorf("message error length %d", len(err.Error()))
	}
	assertNoGitHubSecrets(t, "message error", err.Error(), gh.requestJWTs(), markers)

	_, err = app.Mint(context.Background())
	if err == nil {
		t.Fatal("accepted a bad expiry")
	}
	if len(err.Error()) > maxMessage+80 {
		t.Errorf("expiry error length %d", len(err.Error()))
	}
	assertNoGitHubSecrets(t, "expiry error", err.Error(), gh.requestJWTs(), markers)
}

func TestKeeperLogRedactsGitHubText(t *testing.T) {
	h := newHarness(t, []client.Object{emptySecret()}, nil)
	blob := gitHubMarkerBlob()
	h.gh.failNext(
		failure{http.StatusUnprocessableEntity, `{"message":"` + blob + `"}`},
		failure{http.StatusCreated, `{"token":"ghs_test_expiry_xxxxxxxxxxxxxxxxxxxx","expires_at":"` + blob + `"}`},
		failure{http.StatusCreated, `{"token":"ghs_test_metadata_xxxxxxxxxxxxxxxxxxxx","expires_at":"2099-01-01T00:00:00Z","permissions":{"contents":"ghs_SYNTHETICMARKER0123456789"},"repository_selection":"` + blob + `"}`},
	)
	h.start()
	if w := h.nextWait(); w != 10*time.Second {
		t.Fatalf("wait after the message %s, want 10s", w)
	}
	h.step(10 * time.Second)
	if w := h.nextWait(); w != 20*time.Second {
		t.Fatalf("wait after the expiry %s, want 20s", w)
	}
	h.step(20 * time.Second)
	if w := h.nextWait(); w != 40*time.Minute {
		t.Fatalf("wait after the metadata mint %s, want 40m", w)
	}
	logs := h.logs.String()
	if logs == "" {
		t.Fatal("no logs captured")
	}
	assertNoGitHubSecrets(t, "log", logs, h.gh.requestJWTs(), append(gitHubTokenMarkers(), gitHubJWTMarker))
	if strings.Contains(logs, "ghs_test_") {
		t.Error("the log holds a minted token")
	}
}

func TestSecretValueNeverPrints(t *testing.T) {
	v := newSecretValue(leakSentinel)
	var out []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d"} {
		out = append(out, fmt.Sprintf(verb, v))
	}
	b, err := json.Marshal(struct{ Token secretValue }{v})
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, string(b), fmt.Sprint(InstallationToken{Token: v}), fmt.Sprintf("%+v", &InstallationToken{Token: v}))
	var buf logBuffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "token", v, "group", InstallationToken{Token: v})
	buf.logger().Info("y", "token", v)
	out = append(out, buf.String())
	for _, s := range out {
		if strings.Contains(s, leakSentinel) {
			t.Errorf("printed: %s", s)
		}
	}
	if v.Reveal() != leakSentinel {
		t.Error("Reveal lost the value")
	}
	if got := scrub("token "+leakSentinel+" and "+leakSentinel, newSecretValue(leakSentinel+"\n")); strings.Contains(got, leakSentinel) {
		t.Errorf("scrub left it: %s", got)
	}
}
