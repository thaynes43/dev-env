package broker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/clock"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The Secret's keys, mounted as files and reread for every notification. Its
// ExternalSecret maps upgrade-gate's PUSHOVER_TOKEN and PUSHOVER_USER_KEY here.
const (
	PushoverFileToken   = "token"
	PushoverFileUserKey = "user-key"
)

const (
	pushoverAPIURL        = "https://api.pushover.net/1/messages.json"
	pushoverTimeout       = 10 * time.Second
	pushoverRetryInterval = 5 * time.Second
	pushoverMaxRetry      = 5 * time.Minute
	pushoverMaxCredential = 1024
	pushoverMaxRequest    = 32 << 10
	pushoverMaxResponse   = 16 << 10
	pushoverMaxMessage    = 1024 // UTF-8 characters, as Pushover's API specifies.
	pushoverMaxURL        = 512
)

// PushoverNotifier tells Tom a pending grant needs his decision (D-67). Sending
// does not write the grant: the reconciler records notifiedAt after success. A
// lost status write or an ambiguous HTTP outcome can therefore send it again.
// The notifier enforces cooldown and rejected-credential state without sleeping;
// its safe errors tell the reconciler when another attempt may be useful.
type PushoverNotifier struct {
	dir           string
	consoleOrigin string
	endpoint      string // Only tests replace the fixed production endpoint.
	http          *http.Client
	sending       chan struct{}
	clock         clock.PassiveClock
	// Protected by sending; watcher events cannot bypass cooldown. Only the
	// digest is retained, never the credential values used to compute it.
	credentialsSeen bool
	credentialsHash [sha256.Size]byte
	rejected        bool
	retryAt         time.Time
	retryDelay      time.Duration
}

var _ Notifier = (*PushoverNotifier)(nil)

// pushoverRetryError exposes safe scheduling information without retaining an
// underlying transport error, response body or credential.
type pushoverRetryError struct {
	message string
	after   time.Duration
}

func (e *pushoverRetryError) Error() string             { return e.message }
func (e *pushoverRetryError) RetryAfter() time.Duration { return e.after }

// NewPushoverNotifier configures a notifier without reading credentials or
// sending a request. consoleOrigin is the approval console's HTTPS origin.
func NewPushoverNotifier(dir, consoleOrigin string) (*PushoverNotifier, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("pushover credential directory is required")
	}
	u, err := url.Parse(consoleOrigin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" ||
		strings.ContainsAny(consoleOrigin, "?#") {
		return nil, errors.New("pushover approval console must be an HTTPS origin")
	}
	origin := strings.TrimSuffix(u.String(), "/")
	// Every schema-valid grant name must fit Pushover's supplementary URL.
	if utf8.RuneCountInString(origin)+len("/approvals/")+validation.DNS1123LabelMaxLength > pushoverMaxURL {
		return nil, errors.New("pushover approval console origin is too long")
	}
	transport := &http.Transport{
		Proxy:                  http.ProxyFromEnvironment,
		DialContext:            (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:      true,
		MaxConnsPerHost:        1,
		MaxIdleConns:           1,
		MaxIdleConnsPerHost:    1,
		IdleConnTimeout:        90 * time.Second,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		MaxResponseHeaderBytes: pushoverMaxResponse,
	}
	return &PushoverNotifier{
		dir: dir, consoleOrigin: origin, endpoint: pushoverAPIURL,
		http:    &http.Client{Transport: transport, Timeout: pushoverTimeout},
		sending: make(chan struct{}, 1),
		clock:   clock.RealClock{},
	}, nil
}

// NotifyPending sends one bounded, plain-text notification. Every returned
// error is safe for the broker's logs and Kubernetes Events: it never copies a
// credential, filename, HTTP error, JSON error or response body.
func (n *PushoverNotifier) NotifyPending(ctx context.Context, g *v1alpha1.AccessGrant) error {
	if n == nil || n.sending == nil || n.http == nil {
		return errors.New("pushover notifier is not configured")
	}
	if g == nil || !strings.HasPrefix(g.Name, "grant-") || len(g.Name) > validation.DNS1123LabelMaxLength ||
		len(validation.IsDNS1123Label(g.Name)) > 0 {
		return errors.New("pushover notification needs a valid grant name")
	}
	ctx, cancel := context.WithTimeout(ctx, pushoverTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return errors.New("pushover notification canceled or timed out")
	}
	// One request per notifier, including its response read. Waiting for that
	// request observes the same deadline and the caller's cancellation.
	select {
	case n.sending <- struct{}{}:
		defer func() { <-n.sending }()
	case <-ctx.Done():
		return errors.New("pushover notification canceled or timed out")
	}
	// Read even during cooldown or rejection, so Secret rotation can recover
	// immediately. No unchanged rejected credentials reach Pushover again.
	token, err := n.credential(PushoverFileToken)
	if err != nil {
		return errors.New("pushover application token is missing or invalid")
	}
	user, err := n.credential(PushoverFileUserKey)
	if err != nil {
		return errors.New("pushover user key is missing or invalid")
	}
	fingerprint := sha256.Sum256([]byte(token + "\x00" + user))
	if !n.credentialsSeen || fingerprint != n.credentialsHash {
		n.credentialsSeen, n.credentialsHash, n.rejected = true, fingerprint, false
		n.retryAt, n.retryDelay = time.Time{}, 0
	}
	if n.rejected {
		return &pushoverRetryError{message: "pushover rejected the configured credentials; update the mounted credentials before retrying", after: pushoverMaxRetry}
	}
	if now := n.clock.Now(); now.Before(n.retryAt) {
		return &pushoverRetryError{message: "pushover notification retry is delayed", after: n.retryAt.Sub(now)}
	}
	priority, title := "0", "Access approval requested"
	if g.Spec.Type == v1alpha1.GrantBreakglass {
		priority, title = "1", "Break-glass approval requested"
	}
	body := url.Values{
		"token": {token}, "user": {user}, "priority": {priority},
		"title": {title}, "message": {pushoverMessage(g)},
		"url": {n.consoleOrigin + "/approvals/" + g.Name}, "url_title": {"Review request"},
	}.Encode()
	if len(body) > pushoverMaxRequest {
		return errors.New("pushover notification is too large")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.endpoint, strings.NewReader(body))
	if err != nil {
		return errors.New("pushover request could not be prepared")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	// Refuse redirects even when a test supplies its own client. A redirect
	// must never forward the form's credentials to another endpoint.
	client := *n.http
	client.Timeout = pushoverTimeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return n.transientFailure("pushover notification could not be delivered")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		message := fmt.Sprintf("pushover refused notification: HTTP %d", resp.StatusCode)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
			return n.permanentFailure(message)
		}
		return n.transientFailure(message)
	}
	answer, err := io.ReadAll(io.LimitReader(resp.Body, pushoverMaxResponse+1))
	if err != nil {
		return n.transientFailure("pushover notification response could not be read")
	}
	if len(answer) > pushoverMaxResponse {
		return n.transientFailure("pushover notification response is too large")
	}
	var result struct {
		Status *int `json:"status"`
	}
	if err := json.Unmarshal(answer, &result); err != nil {
		return n.transientFailure("pushover notification response is invalid")
	}
	if result.Status != nil && *result.Status == 0 {
		return n.permanentFailure("pushover did not accept notification")
	}
	if result.Status == nil || *result.Status != 1 {
		return n.transientFailure("pushover notification response is invalid")
	}
	n.retryAt, n.retryDelay = time.Time{}, 0
	return nil
}

func (n *PushoverNotifier) transientFailure(message string) error {
	if n.retryDelay == 0 {
		n.retryDelay = pushoverRetryInterval
	} else {
		n.retryDelay = min(2*n.retryDelay, pushoverMaxRetry)
	}
	n.retryAt = n.clock.Now().Add(n.retryDelay)
	return &pushoverRetryError{message: message, after: n.retryDelay}
}

func (n *PushoverNotifier) permanentFailure(message string) error {
	// A valid status:0 also fails closed: an operator must correct the
	// configuration and rotate the mounted credentials to clear this latch.
	n.rejected = true
	return &pushoverRetryError{message: message, after: pushoverMaxRetry}
}

func (n *PushoverNotifier) credential(name string) (string, error) {
	path := filepath.Join(n.dir, name)
	// A mounted Secret contains regular files behind symlinks, never pipes or
	// devices whose read could block beyond the request's timeout.
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("invalid credential file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("credential file unavailable")
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, pushoverMaxCredential+1))
	if err != nil || len(b) > pushoverMaxCredential {
		return "", errors.New("invalid credential file")
	}
	value := strings.TrimSpace(string(b))
	// Both identifiers are exactly 30 case-sensitive ASCII alphanumerics.
	if len(value) != 30 {
		return "", errors.New("invalid credential value")
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return "", errors.New("invalid credential value")
		}
	}
	return value, nil
}

func pushoverMessage(g *v1alpha1.AccessGrant) string {
	message := fmt.Sprintf("Requester: %s\nRepo: %s\nGrant: %s (%s)\nTTL: %s\nReason supplied by agent: %s",
		pushoverText(g.Spec.Requester.Session, 63), pushoverText(g.Spec.Requester.Repo, 100),
		g.Name, pushoverText(string(g.Spec.Type), 20), g.Spec.TTL.Duration,
		pushoverText(g.Spec.Reason, pushoverMaxMessage))
	return pushoverTruncate(message, pushoverMaxMessage)
}

func pushoverText(value string, limit int) string {
	// Agent-authored newlines cannot manufacture another summary field.
	return pushoverTruncate(strings.Join(strings.Fields(strings.ToValidUTF8(value, "�")), " "), limit)
}

func pushoverTruncate(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}
