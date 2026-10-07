package keeper

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"k8s.io/utils/clock"
)

// The files of a GitHub App's directory: the keys of the Secret that the
// keeper's ExternalSecret writes from 1Password (haynes-ops, KICKOFF 8.8), mounted
// as a directory. The JWT's issuer is client-id when that file exists, else
// app-id; GitHub recommends the client ID.
const (
	AppFileClientID       = "client-id"
	AppFileAppID          = "app-id"
	AppFileInstallationID = "installation-id"
	AppFilePrivateKey     = "private-key"
)

// DefaultGitHubAPIURL is GitHub's REST API.
const DefaultGitHubAPIURL = "https://api.github.com"

// DefaultDevBotPermissions is the down-scoped permission set of v1's gh-refresher
// (haynes-ops apps/dev/dev-env, GITHUB_BOT_TOKEN_PERMISSIONS), which DESIGN-001
// 6.4 keeps. A token may only name permissions the App itself was granted: one
// it lacks makes GitHub refuse the mint with 422, and every session's gh goes
// dark when the last token expires. Grant a permission on the App and approve it
// on the installation first, then add it here.
var DefaultDevBotPermissions = map[string]string{
	"contents":      "write",
	"pull_requests": "write",
	"workflows":     "write",
	"issues":        "write",
	"checks":        "read",
	"actions":       "read",
}

const (
	// jwtBackdate and jwtLifetime follow GitHub's rules for an App JWT: issued
	// in the past to allow for clock drift, and expiring at most 10 minutes
	// after it was issued. v1's script used the same values.
	jwtBackdate = 60 * time.Second
	jwtLifetime = 10 * time.Minute
	// maxKeyFile bounds what is read from the key file; a PEM key is a few KB.
	maxKeyFile = 64 << 10
	// maxResponse bounds what is read from GitHub's answer.
	maxResponse = 1 << 20
	// maxMessage bounds how much of GitHub's error message reaches a log line.
	maxMessage = 300
)

// GitHubApp mints installation tokens for one installation of one GitHub App:
// a JWT signed with the App's key, exchanged for a token that lasts an hour
// (DESIGN-001 6.4, D-13). It reads the App's files from Dir at every mint, so a
// key rotated in 1Password reaches the keeper with the kubelet's next sync of the
// mounted Secret, without a restart.
type GitHubApp struct {
	// Dir holds the App's files (AppFile*).
	Dir string
	// APIURL is GitHub's REST API; DefaultGitHubAPIURL when empty.
	APIURL string
	// Permissions down-scope the token. They must be a subset of the App's own.
	Permissions map[string]string
	// HTTP sends the request; a client with a 30 s timeout when nil.
	HTTP *http.Client
	// Clock dates the JWT; the real clock when nil.
	Clock clock.PassiveClock
	// UserAgent names the keeper to GitHub, which requires one.
	UserAgent string
}

// InstallationToken is what a mint returns.
type InstallationToken struct {
	Token     secretValue
	ExpiresAt time.Time
	// Permissions are those GitHub granted, which can be fewer than asked for.
	Permissions map[string]string
	// RepositorySelection is "all" or "selected".
	RepositorySelection string
}

// GitHubError is GitHub's refusal of a mint: the HTTP status and GitHub's own
// message, never the response body as a whole.
type GitHubError struct {
	Status  int
	Message string
}

func (e *GitHubError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("GitHub refused the mint: HTTP %d", e.Status)
	}
	return fmt.Sprintf("GitHub refused the mint: HTTP %d: %s", e.Status, e.Message)
}

// Mint signs a JWT and exchanges it for an installation token.
func (a *GitHubApp) Mint(ctx context.Context) (InstallationToken, error) {
	creds, err := a.load()
	if err != nil {
		return InstallationToken{}, err
	}
	now := a.now()
	jwt, err := signJWT(creds.key, creds.issuer, now)
	if err != nil {
		return InstallationToken{}, err
	}
	body, err := json.Marshal(map[string]any{"permissions": a.Permissions})
	if err != nil {
		return InstallationToken{}, err
	}
	base := strings.TrimRight(a.APIURL, "/")
	if base == "" {
		base = DefaultGitHubAPIURL
	}
	url := base + "/app/installations/" + creds.installationID + "/access_tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return InstallationToken{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+jwt.Reveal())
	if a.UserAgent != "" {
		req.Header.Set("User-Agent", a.UserAgent)
	}

	resp, err := a.httpClient().Do(req)
	if err != nil {
		// A *url.Error names the method and the URL, which hold nothing secret.
		return InstallationToken{}, fmt.Errorf("mint request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return InstallationToken{}, fmt.Errorf("read the mint response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return InstallationToken{}, githubError(resp.StatusCode, raw)
	}
	var out struct {
		Token               string            `json:"token"`
		ExpiresAt           string            `json:"expires_at"`
		Permissions         map[string]string `json:"permissions"`
		RepositorySelection string            `json:"repository_selection"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return InstallationToken{}, fmt.Errorf("the mint response is not JSON (%d bytes, not shown)", len(raw))
	}
	tok := InstallationToken{
		Token:               newSecretValue(out.Token),
		Permissions:         out.Permissions,
		RepositorySelection: out.RepositorySelection,
	}
	if tok.Token.Empty() {
		return InstallationToken{}, errors.New("the mint response has no token")
	}
	tok.ExpiresAt, err = time.Parse(time.RFC3339, out.ExpiresAt)
	if err != nil {
		return InstallationToken{}, fmt.Errorf("the mint response's expires_at %q is not RFC 3339", out.ExpiresAt)
	}
	if !tok.ExpiresAt.After(now) {
		return InstallationToken{}, fmt.Errorf("the minted token expires at %s, which is not in the future", tok.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return tok, nil
}

// MissingPermissions lists the requested permissions that GitHub did not grant
// at the requested level, sorted, as "name:level".
func (a *GitHubApp) MissingPermissions(granted map[string]string) []string {
	var missing []string
	for _, name := range slices.Sorted(maps.Keys(a.Permissions)) {
		if granted[name] != a.Permissions[name] {
			missing = append(missing, name+":"+a.Permissions[name])
		}
	}
	return missing
}

func (a *GitHubApp) now() time.Time {
	if a.Clock == nil {
		return time.Now()
	}
	return a.Clock.Now()
}

func (a *GitHubApp) httpClient() *http.Client {
	if a.HTTP == nil {
		return &http.Client{Timeout: 30 * time.Second}
	}
	return a.HTTP
}

// appCreds is what one mint reads from Dir.
type appCreds struct {
	issuer         any // the client ID as a string, or the App ID as a number
	installationID string
	key            *rsa.PrivateKey
}

func (a *GitHubApp) load() (appCreds, error) {
	var c appCreds
	clientID, err := readOptional(a.Dir, AppFileClientID)
	if err != nil {
		return c, err
	}
	appID, err := readOptional(a.Dir, AppFileAppID)
	if err != nil {
		return c, err
	}
	switch {
	case clientID != "":
		c.issuer = clientID
	case appID != "":
		n, err := strconv.ParseInt(appID, 10, 64)
		if err != nil || n <= 0 {
			return c, fmt.Errorf("%s: not a positive number", filepath.Join(a.Dir, AppFileAppID))
		}
		c.issuer = n
	default:
		return c, fmt.Errorf("%s has neither %s nor %s", a.Dir, AppFileClientID, AppFileAppID)
	}
	c.installationID, err = readOptional(a.Dir, AppFileInstallationID)
	if err != nil {
		return c, err
	}
	if n, err := strconv.ParseInt(c.installationID, 10, 64); err != nil || n <= 0 {
		return c, fmt.Errorf("%s: missing, or not a positive number", filepath.Join(a.Dir, AppFileInstallationID))
	}
	c.key, err = readPrivateKey(filepath.Join(a.Dir, AppFilePrivateKey))
	return c, err
}

// readOptional reads a small file and trims it; a missing file is "".
func readOptional(dir, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// readPrivateKey parses an RSA key in PKCS #1 ("RSA PRIVATE KEY", the form GitHub
// hands out) or PKCS #8 ("PRIVATE KEY"). Its errors name the file, never its
// content.
func readPrivateKey(path string) (*rsa.PrivateKey, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxKeyFile+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(raw) > maxKeyFile {
		return nil, fmt.Errorf("%s is larger than %d bytes; it is not a PEM key", path, maxKeyFile)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("%s holds no PEM block", path)
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%s: not a PKCS #1 RSA key", path)
		}
		return key, nil
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%s: not a PKCS #8 key", path)
		}
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%s: a PKCS #8 key that is not RSA; GitHub App keys are RSA", path)
		}
		return key, nil
	default:
		return nil, fmt.Errorf("%s: PEM block of type %q, want RSA PRIVATE KEY or PRIVATE KEY", path, block.Type)
	}
}

// signJWT makes the App's JWT (RS256).
func signJWT(key *rsa.PrivateKey, issuer any, now time.Time) (secretValue, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	iat := now.Add(-jwtBackdate)
	claims, err := json.Marshal(map[string]any{
		"iat": iat.Unix(),
		"exp": iat.Add(jwtLifetime).Unix(),
		"iss": issuer,
	})
	if err != nil {
		return secretValue{}, err
	}
	signing := header + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return secretValue{}, fmt.Errorf("sign the App JWT: %w", err)
	}
	return newSecretValue(signing + "." + enc.EncodeToString(sig)), nil
}

// githubError keeps GitHub's message and drops the rest of the body.
func githubError(status int, raw []byte) error {
	var body struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &body)
	msg := strings.Join(strings.Fields(body.Message), " ")
	if len(msg) > maxMessage {
		msg = msg[:maxMessage] + "..."
	}
	return &GitHubError{Status: status, Message: msg}
}

// permissionName and permissionLevel bound what ParsePermissions accepts.
var (
	permissionName  = regexp.MustCompile(`^[a-z][a-z_]*$`)
	permissionLevel = map[string]bool{"read": true, "write": true, "admin": true}
)

// ParsePermissions reads a permission set as JSON, the form v1's
// GITHUB_BOT_TOKEN_PERMISSIONS used: {"contents":"write","checks":"read"}.
func ParsePermissions(s string) (map[string]string, error) {
	var p map[string]string
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil, fmt.Errorf("not a JSON object of strings: %w", err)
	}
	if len(p) == 0 {
		return nil, errors.New("empty: an unscoped token would carry every permission of the App")
	}
	for name, level := range p {
		if !permissionName.MatchString(name) {
			return nil, fmt.Errorf("%q is not a permission name", name)
		}
		if !permissionLevel[level] {
			return nil, fmt.Errorf("%s: %q is not read, write or admin", name, level)
		}
	}
	return p, nil
}
