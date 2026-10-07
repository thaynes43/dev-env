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
	if missing := app.MissingPermissions(tok.Permissions); len(missing) != 0 {
		t.Errorf("missing %v from a full grant", missing)
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
