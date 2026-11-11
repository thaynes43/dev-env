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
// app-id; GitHub recommends the client ID. A value that is all digits is an
// app id, whichever of those files holds it.
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

// DefaultOpsBotPermissions is v1's haynes-ops-bot set, the string
// {"contents":"write","pull_requests":"write","checks":"read","issues":"write"}
// from GITHUB_BOT_TOKEN_PERMISSIONS in dev-env-ops's HelmRelease (DESIGN-064
// 6.2). It has no workflows and no actions.
var DefaultOpsBotPermissions = map[string]string{
	"contents":      "write",
	"pull_requests": "write",
	"checks":        "read",
	"issues":        "write",
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
	// maxMessage bounds how much of GitHub's text reaches an error or a log line.
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
	// Repositories limits the token to these repository names (the name, not
	// owner/name). Empty omits the field. The haynes-dev-bot mint leaves it
	// empty, so GitHub scopes that token to every repository the installation
	// can access, which is the request it sends today. The ops bot sets it
	// (DESIGN-064 6.2). An empty ops list is refused before the loop starts.
	Repositories []string
	// HTTP sends the request; a client with a 30 s timeout when nil.
	HTTP *http.Client
	// Clock dates the JWT; the real clock when nil.
	Clock clock.PassiveClock
	// UserAgent names the keeper to GitHub, which requires one.
	UserAgent string
	// DistinctFrom, when set, is an App this mint must not be. The ops loop
	// sets it to the dev bot's App. Mint loads both directories and refuses,
	// before any request, when the issuers are the same, when one issuer is a
	// client id and the other is an app id (those two forms cannot be
	// compared), when the directories hold the same signing key, or when the
	// installation ids are the same. Equality and the different-forms check
	// use the classified value: all digits is an app id, whichever file holds
	// it. Path cleaning cannot see a symlink.
	DistinctFrom *GitHubApp
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
	// GitHub reads a missing or null permission set as "every permission of the
	// App", which for haynes-dev-bot includes secrets: write.
	if len(a.Permissions) == 0 {
		return InstallationToken{}, errors.New("no permissions to down-scope the token to: an unscoped token would carry every permission of the App")
	}
	creds, err := a.load()
	if err != nil {
		return InstallationToken{}, err
	}
	if err := a.refuseSharedIssuer(creds); err != nil {
		return InstallationToken{}, err
	}
	now := a.now()
	jwt, err := signJWT(creds.key, creds.issuer, now)
	if err != nil {
		return InstallationToken{}, err
	}
	// One field when Repositories is empty, so the dev bot's body stays the
	// permissions object alone.
	payload := map[string]any{"permissions": a.Permissions}
	if len(a.Repositories) > 0 {
		payload["repositories"] = a.Repositories
	}
	body, err := json.Marshal(payload)
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
		// The URL we requested holds nothing secret. A transport error can
		// quote a response header, so it takes the same scrubber and bound
		// as GitHub's message. Both App loops mint through here.
		return InstallationToken{}, fmt.Errorf("mint request: %s", redactGitHubText(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		// A redirect is an error that names the status and nothing from the
		// response. noRedirectTransport already does this; this is the same
		// rule if a response arrives anyway.
		return InstallationToken{}, fmt.Errorf("mint request redirected: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return InstallationToken{}, fmt.Errorf("read the mint response: %s", redactGitHubText(err.Error()))
	}
	if resp.StatusCode != http.StatusCreated {
		return InstallationToken{}, githubError(resp.StatusCode, raw)
	}
	// repositories, the selected-repo objects, is left out of this struct on
	// purpose. The success log records repository_selection, not those names,
	// and a value that is never stored cannot reach a log line.
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
		return InstallationToken{}, fmt.Errorf("the mint response's expires_at %q is not RFC 3339", redactGitHubText(out.ExpiresAt))
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
	c := &http.Client{Timeout: 30 * time.Second}
	if a.HTTP != nil {
		copied := *a.HTTP
		c = &copied
	}
	// Client.Do parses a Location header before CheckRedirect, and quotes a
	// bad one. The transport turns a redirect into a status-only error first,
	// so the mint follows no redirect. CheckRedirect is the same rule if a
	// redirect is ever returned as a response.
	c.Transport = noRedirectTransport{base: c.Transport}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

// noRedirectTransport refuses a redirect. The error names the status and
// nothing from the response: no Location, no body, no reason phrase. Both
// App loops use it, because both mint through GitHubApp.Mint.
type noRedirectTransport struct {
	base http.RoundTripper
}

func (t noRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return resp, nil
	}
	if resp.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponse))
		_ = resp.Body.Close()
	}
	return nil, fmt.Errorf("mint request redirected: HTTP %d", resp.StatusCode)
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
		c.issuer, err = classifyIssuer(filepath.Join(a.Dir, AppFileClientID), clientID)
		if err != nil {
			return c, err
		}
	case appID != "":
		n, err := parseAppID(filepath.Join(a.Dir, AppFileAppID), appID)
		if err != nil {
			return c, err
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

// refuseSharedIssuer reports when DistinctFrom is not a different App. Each
// check is a refusal before any request. A failure to read the other App is
// itself a refusal: the mint does not run when the comparison cannot be made.
// When more than one check matches, the error is the first of: the same
// issuer, issuers of different forms, the same signing key, the same
// installation.
func (a *GitHubApp) refuseSharedIssuer(creds appCreds) error {
	if a.DistinctFrom == nil {
		return nil
	}
	other, err := a.DistinctFrom.load()
	if err != nil {
		return fmt.Errorf("refusing to mint: the dev bot's App could not be read: %w", err)
	}
	if creds.issuer == other.issuer {
		return errors.New("refusing to mint: this App has the same issuer as the dev bot's App")
	}
	if issuerFormsDiffer(creds.issuer, other.issuer) {
		return errors.New("refusing to mint: the issuers are of different forms, one a client id and the other an app id, so they cannot be compared")
	}
	if sameSigningKey(creds.key, other.key) {
		return errors.New("refusing to mint: this App has the same signing key as the dev bot's App")
	}
	if samePositiveID(creds.installationID, other.installationID) {
		return errors.New("refusing to mint: this App has the same installation id as the dev bot's App")
	}
	return nil
}

// issuerFormsDiffer reports a client id (a string) paired with an app id (an
// int64). Those two cannot be compared, so the mint is refused. load
// classifies each issuer first: all digits is an app id.
func issuerFormsDiffer(a, b any) bool {
	_, aClient := a.(string)
	_, bClient := b.(string)
	_, aApp := a.(int64)
	_, bApp := b.(int64)
	return (aClient && bApp) || (aApp && bClient)
}

// classifyIssuer classifies an issuer by its value. All digits is an app id.
// Any other value is a client id. The file name does not decide the form.
func classifyIssuer(file, raw string) (any, error) {
	if !allDigits(raw) {
		return raw, nil
	}
	n, err := parseAppID(file, raw)
	if err != nil {
		return nil, err
	}
	return n, nil
}

// parseAppID parses a positive App id. The error names the file.
func parseAppID(file, raw string) (int64, error) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: not a positive number", file)
	}
	return n, nil
}

// allDigits reports whether s is one or more ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func sameSigningKey(a, b *rsa.PrivateKey) bool {
	if a == nil || b == nil {
		return false
	}
	return a.PublicKey.Equal(&b.PublicKey)
}

func samePositiveID(a, b string) bool {
	ai, err1 := strconv.ParseInt(a, 10, 64)
	bi, err2 := strconv.ParseInt(b, 10, 64)
	return err1 == nil && err2 == nil && ai > 0 && ai == bi
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
	return &GitHubError{Status: status, Message: redactGitHubText(body.Message)}
}

// A JWT is three base64url parts. A GitHub token starts with one of these
// prefixes. base64URLRun is 20 or more base64url characters: a JWT part left
// when a dot is followed by a space or a line break, or a signature broken
// across lines. A fragment shorter than 20 characters can remain. All of
// these are removed before the length cut, so truncation cannot leave a
// prefix of a credential in an error or a log line.
var (
	jwtInGitHubText   = regexp.MustCompile(`[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)
	gitHubTokenInText = regexp.MustCompile(`github_pat_[A-Za-z0-9_]{8,}|gh[pousr]_[A-Za-z0-9]{8,}`)
	base64URLRun      = regexp.MustCompile(`[A-Za-z0-9_-]{20,}`)
)

// redactGitHubText is applied to text GitHub sent, and to transport and
// body-read errors, before that text reaches an error or a log line. Both
// App loops use it.
func redactGitHubText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = jwtInGitHubText.ReplaceAllString(s, redacted)
	s = gitHubTokenInText.ReplaceAllString(s, redacted)
	s = base64URLRun.ReplaceAllString(s, redacted)
	if len(s) > maxMessage {
		s = s[:maxMessage] + "..."
	}
	return s
}

// permissionName and permissionLevel bound what ParsePermissions accepts.
var (
	permissionName  = regexp.MustCompile(`^[a-z][a-z_]*$`)
	permissionLevel = map[string]bool{"read": true, "write": true, "admin": true}
	// repositoryName is GitHub's repository name, not owner/name. 100 characters
	// is GitHub's limit. A leading dot is allowed (.github). "." and ".." are
	// refused separately.
	repositoryName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
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

// ParseRepositories reads a comma-separated list of repository names. An empty
// string is an empty list, which the ops loop refuses when it is enabled: an
// omitted repositories field would mint a token for every repository the
// installation can access. Names are the repository name, not owner/name.
func ParseRepositories(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if len(s) > 64<<10 {
		return nil, errors.New("repository list is too long")
	}
	parts := strings.Split(s, ",")
	if len(parts) > 500 {
		return nil, errors.New("more than 500 repositories; GitHub refuses a longer list")
	}
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "." || name == ".." || !repositoryName.MatchString(name) {
			return nil, fmt.Errorf("%q is not a repository name (the name, not owner/name)", name)
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("duplicate repository %q", name)
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out, nil
}
