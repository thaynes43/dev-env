package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

// CurrentAuthorizationConfig has no subject pin: access is by live group
// membership (Tom, 2026-10-26: "manage users by group not by ID").
type CurrentAuthorizationConfig struct {
	Issuer, UserInfoURL string
	Store               Store
	Now                 func() time.Time
	// Dedicated TLS transport; arbitrary clients/redirect policies are rejected.
	Transport *http.Transport
	Timeout   time.Duration
}

type CurrentAuthorizer struct {
	issuer, endpoint string
	store            Store
	now              func() time.Time
	client           *http.Client
}

func NewCurrentAuthorizer(c CurrentAuthorizationConfig) (*CurrentAuthorizer, error) {
	return newCurrentAuthorizer(c, nil)
}

func newCurrentAuthorizer(c CurrentAuthorizationConfig, dial func(context.Context, string, string) (net.Conn, error)) (*CurrentAuthorizer, error) {
	issuer, err := configuredURL(c.Issuer)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	endpoint, err := configuredURL(c.UserInfoURL)
	if err != nil || endpoint.Host != issuer.Host || endpoint.Path != "/application/o/userinfo/" || c.Store == nil || c.Now == nil || c.Timeout <= 0 || c.Timeout > 10*time.Second || c.Transport == nil || c.Transport.TLSClientConfig == nil {
		return nil, portal.ErrUnavailable
	}
	t := c.Transport.Clone()
	// No dynamic proxy, custom dial/TLS hooks, skip-verify or alternate TLS peer.
	legacyDial := t.Dial != nil || t.DialTLS != nil //nolint:staticcheck // Inspect deprecated hooks to reject them; never invoke them.
	if t.Proxy != nil || legacyDial || t.DialContext != nil || t.DialTLSContext != nil || len(t.TLSNextProto) > 0 || t.TLSClientConfig.KeyLogWriter != nil || t.TLSClientConfig.InsecureSkipVerify || t.TLSClientConfig.VerifyConnection != nil || t.TLSClientConfig.VerifyPeerCertificate != nil || (t.TLSClientConfig.ServerName != "" && t.TLSClientConfig.ServerName != endpoint.Hostname()) {
		return nil, portal.ErrUnavailable
	}
	t.TLSClientConfig = t.TLSClientConfig.Clone()
	t.TLSClientConfig.ServerName = endpoint.Hostname()
	if t.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	t.DisableCompression = true
	t.MaxResponseHeaderBytes = 16 * 1024
	t.TLSHandshakeTimeout = c.Timeout
	t.ResponseHeaderTimeout = c.Timeout
	// The exported constructor passes a nil dial and still rejects a caller DialContext.
	// Tests supply a dial only after those checks. TLS name and roots stay pinned.
	if dial != nil {
		t.DialContext = dial
	}
	return &CurrentAuthorizer{issuer: c.Issuer, endpoint: c.UserInfoURL, store: c.Store, now: c.Now, client: &http.Client{Transport: t, Timeout: c.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Authorize consults the exact request-private session, never a subject index.
// Old claims, login policy and any earlier successful call grant nothing here.
// Any subject may hold a session; access needs live UserInfo for that same
// subject with portal_is_active and portal_is_admin, the Authentik group check.
func (a *CurrentAuthorizer) Authorize(ctx context.Context, p portal.Principal, r portal.Resource) error {
	if a == nil || a.store == nil || a.client == nil || a.now == nil {
		return portal.ErrUnavailable
	}
	if p.Issuer != a.issuer || !identityText(p.Subject) || !allowedResource(r) {
		return portal.ErrDenied
	}
	ref, ok := ctx.Value(sessionContextKey{}).(sessionReference)
	if !ok || ref.principal != p {
		return portal.ErrDenied
	}
	record, err := a.store.GetSession(ctx, ref.handle)
	if err != nil {
		return normalize(err)
	}
	if !matchesReference(record, ref, a.now()) {
		return portal.ErrDenied
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint, nil)
	if err != nil {
		return portal.ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+record.accessToken)
	req.Header.Set("Accept", "application/json")
	response, err := a.client.Do(req)
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil || response == nil || response.Body == nil {
		return portal.ErrUnavailable
	}
	if response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests {
		return portal.ErrUnavailable
	}
	if response.StatusCode != http.StatusOK {
		return portal.ErrDenied
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return portal.ErrDenied
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if err != nil || len(body) > 64*1024 {
		return portal.ErrUnavailable
	}
	if !validUserInfo(body, p.Subject) {
		return portal.ErrDenied
	}
	// Revocation/replacement/expiry during UserInfo must also discard this allow.
	current, err := a.store.GetSession(ctx, ref.handle)
	if err != nil {
		return normalize(err)
	}
	if ctx.Err() != nil {
		return portal.ErrUnavailable
	}
	if !matchesReference(current, ref, a.now()) {
		return portal.ErrDenied
	}
	return nil
}

func matchesReference(r Record, ref sessionReference, now time.Time) bool {
	return r.Principal == ref.principal && validRecord(r, now) && sha256.Sum256([]byte(r.accessToken)) == ref.tokenDigest
}

func allowedResource(r portal.Resource) bool {
	if r.Project != "dev-env" || r.Environment != "internal" {
		return false
	}
	if r.Task == "" {
		return true
	}
	return canonicalWorkPackageID(r.Task)
}

func canonicalWorkPackageID(id string) bool {
	if id == "" || len(id) > 18 || id[0] == '0' {
		return false
	}
	n, err := strconv.Atoi(id)
	return err == nil && n > 0 && strconv.Itoa(n) == id
}

// Token-walk the top-level object to reject duplicate security claims. Other
// normal profile fields are permitted but never retained, logged or returned.
func validUserInfo(body []byte, subject string) bool {
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	var sub string
	active, admin := false, false
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return false
		}
		seen[name] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return false
		}
		switch name {
		case "sub":
			if json.Unmarshal(raw, &sub) != nil {
				return false
			}
		case "portal_is_active", "portal_is_admin":
			// JSON null unmarshals into bool without error: require true literal.
			if !bytes.Equal(bytes.TrimSpace(raw), []byte("true")) {
				return false
			}
			if name == "portal_is_active" {
				active = true
			} else {
				admin = true
			}
		}
	}
	if _, err := d.Token(); err != nil {
		return false
	}
	var extra any
	return d.Decode(&extra) == io.EOF && seen["sub"] && active && admin && sub == subject
}

// Batched final check permits one fresh UserInfo request after all local IDs
// are checked. This never reuses the early metadata-fetch authorization.
func (a *CurrentAuthorizer) AuthorizeRelease(ctx context.Context, p portal.Principal, r portal.Resource, items []portal.Item) error {
	for _, item := range items {
		if !allowedResource(item.Resource) || item.Resource.Task != workPackageTask(item.WorkPackage) {
			return portal.ErrDenied
		}
	}
	return a.Authorize(ctx, p, r)
}

func workPackageTask(id int) string {
	if id < 1 {
		return ""
	}
	return strconv.Itoa(id)
}

var _ portal.Authorizer = (*CurrentAuthorizer)(nil)

func (CurrentAuthorizationConfig) String() string     { return "[redacted portal authorization config]" }
func (c CurrentAuthorizationConfig) GoString() string { return c.String() }
func (CurrentAuthorizer) String() string              { return "[redacted portal authorizer]" }
func (a CurrentAuthorizer) GoString() string          { return a.String() }
