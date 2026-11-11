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
	"io"
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
	const opsClient = "Iv23opsclientid"
	opsGH.issuer = opsClient
	devDir := writeAppDir(t, pair[0])
	opsDir := writeAppDir(t, pair[1])
	writeFile(t, filepath.Join(opsDir, AppFileClientID), opsClient+"\n")
	devApp := devGH.app(devDir)
	opsApp := opsGH.app(opsDir)
	opsApp.Permissions = DefaultOpsBotPermissions
	opsApp.Repositories = []string{"haynes-ops"}
	opsApp.DistinctFrom = devApp

	_, err := opsApp.Mint(context.Background())
	if err == nil || !strings.Contains(err.Error(), "installation") {
		t.Fatalf("different client ids, same installation: %v", err)
	}
	if strings.Contains(err.Error(), testInstallationID) || strings.Contains(err.Error(), testClientID) || strings.Contains(err.Error(), opsClient) {
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

func TestOpsMintRefusesMixedIssuerFormsWithDifferentInstallations(t *testing.T) {
	pair := keys(t)
	devGH := newFakeGitHub(t, &pair[0].PublicKey, time.Now)
	opsGH := newFakeGitHub(t, &pair[0].PublicKey, time.Now)
	const opsInstallation = "4343"
	devDir := writeAppDir(t, pair[0])
	opsDir := writeAppDir(t, pair[0])
	if err := os.Remove(filepath.Join(opsDir, AppFileClientID)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(opsDir, AppFileAppID), "4291021\n")
	writeFile(t, filepath.Join(opsDir, AppFileInstallationID), opsInstallation+"\n")
	opsGH.issuer = int64(4291021)
	opsGH.installationID = opsInstallation
	devApp := devGH.app(devDir)
	opsApp := opsGH.app(opsDir)
	opsApp.Permissions = DefaultOpsBotPermissions
	opsApp.Repositories = []string{"haynes-ops"}
	opsApp.DistinctFrom = devApp

	_, err := opsApp.Mint(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cannot be compared") {
		t.Fatalf("client id against app id, different installations: %v", err)
	}
	for _, id := range []string{testClientID, testInstallationID, "4291021", opsInstallation} {
		if strings.Contains(err.Error(), id) {
			t.Fatalf("the refusal quotes an id: %v", err)
		}
	}
	if _, reqs, _, _, _ := opsGH.snapshot(); reqs != 0 {
		t.Fatalf("mixed issuer forms sent a mint: %d requests", reqs)
	}
	if _, err := devApp.Mint(context.Background()); err != nil {
		t.Fatalf("the dev bot's own mint: %v", err)
	}
	if _, reqs, _, _, _ := devGH.snapshot(); reqs != 1 {
		t.Fatalf("dev requests %d, want 1", reqs)
	}
}

func TestOpsMintRefusesTheSameSigningKey(t *testing.T) {
	k := keys(t)
	devGH := newFakeGitHub(t, &k[0].PublicKey, time.Now)
	opsGH := newFakeGitHub(t, &k[0].PublicKey, time.Now)
	const opsClient = "Iv23opsclientid"
	const opsInstallation = "4343"
	devDir := writeAppDir(t, k[0])
	opsDir := writeAppDir(t, k[0])
	writeFile(t, filepath.Join(opsDir, AppFileClientID), opsClient+"\n")
	writeFile(t, filepath.Join(opsDir, AppFileInstallationID), opsInstallation+"\n")
	opsGH.issuer = opsClient
	opsGH.installationID = opsInstallation
	devApp := devGH.app(devDir)
	opsApp := opsGH.app(opsDir)
	opsApp.Permissions = DefaultOpsBotPermissions
	opsApp.Repositories = []string{"haynes-ops"}
	opsApp.DistinctFrom = devApp

	_, err := opsApp.Mint(context.Background())
	if err == nil || !strings.Contains(err.Error(), "signing key") {
		t.Fatalf("same signing key, different issuer and installation: %v", err)
	}
	if strings.Contains(err.Error(), "PRIVATE KEY") || strings.Contains(err.Error(), opsClient) || strings.Contains(err.Error(), opsInstallation) {
		t.Fatalf("the refusal quotes the key or an id: %v", err)
	}
	if _, reqs, _, _, _ := opsGH.snapshot(); reqs != 0 {
		t.Fatalf("same signing key sent a mint: %d requests", reqs)
	}
}

// newMintApp is the dev bot, or the ops bot compared with a distinct dev bot.
func newMintApp(t *testing.T, ops bool) (*fakeGitHub, *GitHubApp) {
	t.Helper()
	pair := keys(t)
	if !ops {
		gh := newFakeGitHub(t, &pair[0].PublicKey, time.Now)
		return gh, gh.app(writeAppDir(t, pair[0]))
	}
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
	app := opsGH.app(opsDir)
	app.Permissions = DefaultOpsBotPermissions
	app.Repositories = []string{"haynes-ops"}
	app.DistinctFrom = devApp
	return opsGH, app
}

func scriptedMint(t *testing.T, ops bool, write func(w http.ResponseWriter, jwt string)) (string, string) {
	t.Helper()
	gh, app := newMintApp(t, ops)
	gh.respond = func(w http.ResponseWriter, _ *http.Request, jwt string) {
		write(w, jwt)
	}
	_, err := app.Mint(context.Background())
	if err == nil {
		t.Fatal("mint succeeded")
	}
	jwts := gh.requestJWTs()
	if len(jwts) != 1 {
		t.Fatalf("recorded %d request JWTs", len(jwts))
	}
	if _, reqs, _, _, _ := gh.snapshot(); reqs != 1 {
		t.Fatalf("requests %d, want 1", reqs)
	}
	return err.Error(), jwts[0]
}

func scriptedLog(t *testing.T, write func(w http.ResponseWriter, jwt string)) (string, string) {
	t.Helper()
	h := newHarness(t, []client.Object{emptySecret()}, nil)
	h.gh.respond = func(w http.ResponseWriter, _ *http.Request, jwt string) {
		write(w, jwt)
	}
	h.start()
	if w := h.nextWait(); w != 10*time.Second {
		t.Fatalf("wait %s, want the first backoff", w)
	}
	jwts := h.gh.requestJWTs()
	if len(jwts) != 1 {
		t.Fatalf("recorded %d request JWTs", len(jwts))
	}
	logs := h.logs.String()
	if logs == "" {
		t.Fatal("no logs captured")
	}
	return logs, jwts[0]
}

func writeRedirect(w http.ResponseWriter, jwt string) {
	loc := "https://example.invalid/cb?token=" + leakSentinel + "&jwt=" + jwt + "%ZZ"
	w.Header().Set("Location", loc)
	w.WriteHeader(http.StatusFound)
}

func writeMalformedHeader(w http.ResponseWriter, jwt string) {
	raw := "HTTP/1.1 502 Bad\r\nX-Echo: " + leakSentinel + " " + jwt + "\x00 " + strings.Repeat("z ", 200) + "\r\n\r\n"
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = buf.WriteString(raw)
	_ = buf.Flush()
}

func assertNoCredentialFragments(t *testing.T, what, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret == "" {
			t.Fatalf("%s: empty secret", what)
		}
		if strings.Contains(text, secret) {
			t.Errorf("%s contains a credential", what)
		}
		for i := 0; i+20 <= len(secret); i++ {
			if strings.Contains(text, secret[i:i+20]) {
				t.Errorf("%s contains a 20-character credential fragment at offset %d", what, i)
				break
			}
		}
	}
}

func TestMintRedirectAndTransportErrorsCarryNoSecret(t *testing.T) {
	for _, ops := range []bool{false, true} {
		loop := "dev"
		if ops {
			loop = "ops"
		}
		t.Run(loop+"/redirect", func(t *testing.T) {
			text, jwt := scriptedMint(t, ops, writeRedirect)
			if !strings.Contains(text, "302") {
				t.Fatalf("error does not name the status: %s", text)
			}
			if strings.Contains(text, "example.invalid") {
				t.Fatalf("redirect error carries the response: %s", text)
			}
			assertNoCredentialFragments(t, loop+" redirect", text, leakSentinel, jwt)
			if len(text) > maxMessage+80 {
				t.Fatalf("length %d", len(text))
			}
		})
		t.Run(loop+"/malformed header", func(t *testing.T) {
			text, jwt := scriptedMint(t, ops, writeMalformedHeader)
			if !strings.Contains(text, "malformed") {
				t.Fatalf("error lost the classification: %s", text)
			}
			assertNoCredentialFragments(t, loop+" malformed header", text, leakSentinel, jwt)
			if len(text) > maxMessage+80 {
				t.Fatalf("length %d", len(text))
			}
		})
	}
	t.Run("redirect log", func(t *testing.T) {
		logs, jwt := scriptedLog(t, writeRedirect)
		if strings.Contains(logs, "example.invalid") {
			t.Fatalf("log carries the redirect response:\n%s", logs)
		}
		assertNoCredentialFragments(t, "redirect log", logs, leakSentinel, jwt)
	})
	t.Run("malformed header log", func(t *testing.T) {
		logs, jwt := scriptedLog(t, writeMalformedHeader)
		if !strings.Contains(logs, "malformed") {
			t.Fatalf("log lost the classification:\n%s", logs)
		}
		assertNoCredentialFragments(t, "malformed header log", logs, leakSentinel, jwt)
	})
	t.Run("body read", func(t *testing.T) {
		k := keys(t)
		gh := newFakeGitHub(t, &k[0].PublicKey, time.Now)
		app := gh.app(writeAppDir(t, k[0]))
		app.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusCreated,
				Status:     "201 Created",
				Header:     make(http.Header),
				Body: io.NopCloser(readerFunc(func([]byte) (int, error) {
					return 0, errors.New("body " + leakSentinel + " " + strings.Repeat("z ", 200))
				})),
				ContentLength: -1,
			}, nil
		})}
		_, err := app.Mint(context.Background())
		if err == nil || !strings.Contains(err.Error(), "read the mint response") {
			t.Fatalf("got %v, want a body-read error", err)
		}
		assertNoCredentialFragments(t, "body read", err.Error(), leakSentinel)
		if len(err.Error()) > maxMessage+80 {
			t.Fatalf("length %d", len(err.Error()))
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestRedactRemovesAWrappedRequestJWT(t *testing.T) {
	wraps := []struct {
		name string
		wrap func(part []string) string
	}{
		{"newline after each dot", func(p []string) string {
			return p[0] + ".\n" + p[1] + ".\n" + p[2]
		}},
		{"space after each dot", func(p []string) string {
			return p[0] + ". " + p[1] + ". " + p[2]
		}},
		{"inside the header", func(p []string) string {
			return p[0][:20] + "\n" + p[0][20:] + "." + p[1] + "." + p[2]
		}},
		{"inside the claims", func(p []string) string {
			n := len(p[1]) / 2
			return p[0] + "." + p[1][:n] + "\n" + p[1][n:] + "." + p[2]
		}},
		{"inside the signature", func(p []string) string {
			n := len(p[2]) / 2
			return p[0] + "." + p[1] + "." + p[2][:n] + "\n" + p[2][n:]
		}},
	}
	for _, c := range wraps {
		t.Run(c.name, func(t *testing.T) {
			text, jwt := scriptedMint(t, false, func(w http.ResponseWriter, jwt string) {
				parts := strings.Split(jwt, ".")
				w.WriteHeader(http.StatusUnprocessableEntity)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": c.wrap(parts)})
			})
			parts := strings.Split(jwt, ".")
			if len(parts) != 3 {
				t.Fatalf("JWT has %d parts", len(parts))
			}
			for i, p := range parts {
				if len(p) < 20 {
					t.Fatalf("part %d is %d characters; a 20-character run would not cover it", i, len(p))
				}
			}
			assertNoCredentialFragments(t, c.name, text, parts...)
			if len(text) > maxMessage+80 {
				t.Fatalf("length %d", len(text))
			}
		})
	}
	t.Run("signature split in the log", func(t *testing.T) {
		logs, jwt := scriptedLog(t, func(w http.ResponseWriter, jwt string) {
			parts := strings.Split(jwt, ".")
			n := len(parts[2]) / 2
			msg := parts[0] + "." + parts[1] + "." + parts[2][:n] + "\n" + parts[2][n:]
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
		})
		parts := strings.Split(jwt, ".")
		assertNoCredentialFragments(t, "log", logs, parts...)
	})
}

func TestLongPermissionNamesStayInTheGrantLog(t *testing.T) {
	const name = "vulnerability_alerts"
	if len(name) < 20 {
		t.Fatalf("%s is %d characters", name, len(name))
	}
	h := newHarness(t, []client.Object{emptySecret()}, nil)
	h.app.Permissions = map[string]string{
		"contents":             "write",
		"vulnerability_alerts": "write",
	}
	h.gh.respond = func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_test_perm_xxxxxxxxxxxxxxxxxxxx",
			"expires_at": h.clock.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"permissions": map[string]string{
				"contents":                      "read",
				"vulnerability_alerts":          "not-a-level",
				"ghs_SYNTHETICMARKER0123456789": "write",
			},
			"repository_selection": "all",
		})
	}
	h.start()
	if w := h.nextWait(); w != 40*time.Minute {
		t.Fatalf("wait %s, want a successful mint", w)
	}
	logs := h.logs.String()
	for _, want := range []string{"vulnerability_alerts:write", "contents:read", "contents:write"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, "[redacted]:write") || strings.Contains(logs, "not-a-level") || strings.Contains(logs, "ghs_SYNTHETICMARKER0123456789") || strings.Contains(logs, "ghs_test_perm_") {
		t.Errorf("the log hides the permission name or keeps a rejected entry:\n%s", logs)
	}
}

func TestRedactRemovesBase64URLRunsBeforeTheLengthCut(t *testing.T) {
	run := strings.Repeat("A", 40)
	got := redactGitHubText(strings.Repeat("ab ", 93) + run)
	if strings.Contains(got, strings.Repeat("A", 20)) {
		t.Fatalf("a run that crosses the length cut survived: %q", got)
	}
	if len(got) > maxMessage+3 {
		t.Fatalf("length %d", len(got))
	}
}
