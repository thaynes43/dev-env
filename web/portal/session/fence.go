package session

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"

	"github.com/thaynes43/dev-env/internal/tokenfile"
	"github.com/thaynes43/dev-env/web/portal"
)

const admissionPath = "/api/v1/namespaces/dev-agents/configmaps/dev-env-v2-portal-admission"
const maximumAdmission = 16 * 1024

// Incarnation identifies the actual backend, not the pool's configured hostname.
// Timeline is the active WAL filename prefix, never checkpoint timeline metadata.
type Incarnation struct {
	SystemIdentifier    string
	Timeline            string
	PostmasterStartedAt time.Time
	ServerAddress       string
	ServerPort          int
}

// AdmissionSnapshot is immutable for one operation, including object incarnation
// and API version. Reopening identical data at another resourceVersion invalidates
// the operation as surely as closing admission does.
type AdmissionSnapshot struct {
	UID, ResourceVersion, Generation string
	Incarnation                      Incarnation
}

// RecoveryFence binds initial admission to the actual acquired transaction.
// Recheck must freshly fetch and require the exact same snapshot; it cannot
// admit a new generation, object, version or backend on an in-flight operation.
type RecoveryFence interface {
	Check(context.Context, pgx.Tx, string) (AdmissionSnapshot, error)
	Recheck(context.Context, AdmissionSnapshot) error
}

// AdmissionConfig is fixed trusted deployment configuration. It cannot come
// from a browser request, discovery, kubeconfig or ambient proxy environment.
// UID is pinned separately after approved closed recovery; recreation denies.
type AdmissionConfig struct {
	APIURL         string
	CA             []byte
	TokenFile, UID string
	Timeout        time.Duration
}

type KubernetesFence struct {
	endpoint, tokenFile, uid string
	client                   *http.Client
	slots                    chan struct{}
}

func NewKubernetesFence(c AdmissionConfig) (*KubernetesFence, error) {
	u, err := admissionAPIURL(c.APIURL)
	roots := x509.NewCertPool()
	if err != nil || (u.Path != "" && u.Path != "/") || !roots.AppendCertsFromPEM(c.CA) || c.TokenFile == "" || !boundedIdentifier(c.UID) || c.Timeout <= 0 || c.Timeout > 5*time.Second {
		return nil, portal.ErrUnavailable
	}
	// Deliberately build a dedicated transport; DefaultTransport/ProxyFromEnvironment
	// and arbitrary caller transports cannot add destinations or credential hooks.
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: u.Hostname(), MinVersion: tls.VersionTLS12},
		DialContext:     (&net.Dialer{Timeout: c.Timeout, KeepAlive: 30 * time.Second}).DialContext,
		MaxConnsPerHost: 2, MaxIdleConns: 2, MaxIdleConnsPerHost: 2,
		IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: c.Timeout,
		ResponseHeaderTimeout: c.Timeout, MaxResponseHeaderBytes: 8 * 1024, DisableCompression: true,
	}
	u.Path = admissionPath
	return &KubernetesFence{endpoint: u.String(), tokenFile: c.TokenFile, uid: c.UID, slots: make(chan struct{}, 2), client: &http.Client{Transport: transport, Timeout: c.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func admissionAPIURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || !identityText(raw) || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return nil, portal.ErrUnavailable
	}
	// Kubernetes trusted service endpoints may be an IP (including the configured
	// service IP); the OAuth issuer's public-DNS-only policy does not apply here.
	host := u.Hostname()
	_, ipErr := netip.ParseAddr(host)
	if host == "" || (ipErr != nil && !dnsName(host)) {
		return nil, portal.ErrUnavailable
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return nil, portal.ErrUnavailable
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, portal.ErrUnavailable
	}
	return u, nil
}

func boundedIdentifier(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' {
			return false
		}
	}
	return true
}

func (f *KubernetesFence) read(ctx context.Context) (AdmissionSnapshot, error) {
	if f == nil || f.client == nil || f.slots == nil {
		return AdmissionSnapshot{}, portal.ErrUnavailable
	}
	// Bound outstanding GETs across all operations sharing this fence, without a
	// polling goroutine or application retry. Waiting consumes the caller deadline.
	select {
	case f.slots <- struct{}{}:
	case <-ctx.Done():
		return AdmissionSnapshot{}, portal.ErrUnavailable
	}
	defer func() { <-f.slots }()
	raw, err := tokenfile.Read(f.tokenFile, tokenfile.MaxBytes)
	if err != nil {
		return AdmissionSnapshot{}, portal.ErrUnavailable
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, "\r\n\t ") {
		return AdmissionSnapshot{}, portal.ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.endpoint, nil)
	if err != nil {
		return AdmissionSnapshot{}, portal.ErrUnavailable
	}
	// No resourceVersion query at all: '0' permits stale cached reads.
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-store")
	response, err := f.client.Do(req)
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil || response == nil || response.Body == nil || response.StatusCode != http.StatusOK || response.Header.Get("Content-Encoding") != "" {
		return AdmissionSnapshot{}, portal.ErrUnavailable
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return AdmissionSnapshot{}, portal.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximumAdmission+1))
	if err != nil || len(data) > maximumAdmission || ctx.Err() != nil {
		return AdmissionSnapshot{}, portal.ErrUnavailable
	}
	return parseAdmission(data, f.uid)
}

func parseAdmission(data []byte, uid string) (AdmissionSnapshot, error) {
	deny := func() (AdmissionSnapshot, error) { return AdmissionSnapshot{}, portal.ErrUnavailable }
	if !uniqueJSON(data) {
		return deny()
	}
	var cm corev1.ConfigMap
	if json.Unmarshal(data, &cm) != nil || cm.APIVersion != "v1" || cm.Kind != "ConfigMap" || cm.Name != "dev-env-v2-portal-admission" || cm.Namespace != "dev-agents" || string(cm.UID) != uid || !validResourceVersion(cm.ResourceVersion) || cm.DeletionTimestamp != nil || len(cm.BinaryData) != 0 || len(cm.Data) != 8 {
		return deny()
	}
	d := cm.Data
	if d["schema"] != "1" || d["state"] != "open" || !validGeneration(d["generation"]) {
		return deny()
	}
	started, err := time.Parse(time.RFC3339Nano, d["postmaster_started_at"])
	if err != nil || started.IsZero() || started.Format(time.RFC3339Nano) != d["postmaster_started_at"] || !strings.HasSuffix(d["postmaster_started_at"], "Z") {
		return deny()
	}
	port, err := strconv.Atoi(d["server_port"])
	if err != nil || strconv.Itoa(port) != d["server_port"] {
		return deny()
	}
	i := Incarnation{d["system_identifier"], d["timeline"], started, d["server_address"], port}
	if !validIncarnation(i) {
		return deny()
	}
	return AdmissionSnapshot{uid, cm.ResourceVersion, d["generation"], i}, nil
}

func validResourceVersion(s string) bool {
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == s
}

func validIncarnation(i Incarnation) bool {
	system, err := strconv.ParseUint(i.SystemIdentifier, 10, 64)
	if err != nil || system == 0 || strconv.FormatUint(system, 10) != i.SystemIdentifier || len(i.Timeline) != 8 || i.PostmasterStartedAt.IsZero() || i.ServerPort < 1 || i.ServerPort > 65535 {
		return false
	}
	timeline, err := strconv.ParseUint(i.Timeline, 16, 32)
	if err != nil || timeline == 0 || strings.ToUpper(i.Timeline) != i.Timeline {
		return false
	}
	addr, err := netip.ParseAddr(i.ServerAddress)
	return err == nil && addr.Zone() == "" && addr.String() == i.ServerAddress
}

// Reject duplicate keys recursively, trailing JSON, and excessive nesting before
// typed decode. This avoids parser disagreement about security-critical values.
func uniqueJSON(data []byte) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 16 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !value(depth + 1) {
					return false
				}
			}
			token, err = d.Token()
			return err == nil && token == json.Delim('}')
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
			token, err = d.Token()
			return err == nil && token == json.Delim(']')
		default:
			return false
		}
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

const incarnationSQL = `SELECT (pg_catalog.pg_control_system()).system_identifier::text,
 pg_catalog.left(pg_catalog.pg_walfile_name(pg_catalog.pg_current_wal_lsn()),8),
 pg_catalog.pg_postmaster_start_time(), pg_catalog.host(pg_catalog.inet_server_addr()),
 pg_catalog.inet_server_port(), pg_catalog.pg_is_in_recovery(),
 pg_catalog.current_setting('transaction_read_only')`

func measureIncarnation(ctx context.Context, tx pgx.Tx) (Incarnation, error) {
	var i Incarnation
	var recovery bool
	var readOnly string
	if tx == nil || tx.QueryRow(ctx, incarnationSQL).Scan(&i.SystemIdentifier, &i.Timeline, &i.PostmasterStartedAt, &i.ServerAddress, &i.ServerPort, &recovery, &readOnly) != nil || recovery || readOnly != "off" || !validIncarnation(i) {
		return Incarnation{}, portal.ErrUnavailable
	}
	i.PostmasterStartedAt = i.PostmasterStartedAt.UTC()
	return i, nil
}

func (f *KubernetesFence) Check(ctx context.Context, tx pgx.Tx, generation string) (AdmissionSnapshot, error) {
	i, err := measureIncarnation(ctx, tx)
	if err != nil {
		return AdmissionSnapshot{}, portal.ErrUnavailable
	}
	snapshot, err := f.read(ctx)
	if err != nil || snapshot.Generation != generation || snapshot.Incarnation != i {
		return AdmissionSnapshot{}, portal.ErrUnavailable
	}
	return snapshot, nil
}

func (f *KubernetesFence) Recheck(ctx context.Context, snapshot AdmissionSnapshot) error {
	current, err := f.read(ctx)
	if err != nil || current != snapshot {
		return portal.ErrUnavailable
	}
	return nil
}

func (AdmissionConfig) String() string     { return "[portal admission config]" }
func (c AdmissionConfig) GoString() string { return c.String() }
func (KubernetesFence) String() string     { return "[portal admission fence]" }
func (f KubernetesFence) GoString() string { return f.String() }
