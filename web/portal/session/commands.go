package session

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

const portalMessagesPath = "/portal/projects/dev-env/environments/internal/session-messages"

var messageURL = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://|\bwww\.)`)

type commandTransport interface {
	send(context.Context, apiv1.PortalMessageRequest) (apiv1.PortalMessageReceipt, error)
}

// commandHandler requires explicit composition; production leaves it disabled.
// Every dependency, including a renderer, is required before it serves a route.
type commandHandler struct {
	engine     *Engine
	authorizer portal.Authorizer
	receipts   commandReceipts
	transport  commandTransport
	render     func(apiv1.PortalMessageReceipt) ([]byte, error)
	now        func() time.Time
}

func (*commandHandler) NewRequestID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", portal.ErrUnavailable
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// actorRef ties attribution to the private authenticated session without
// disclosing the cookie, access token, issuer or subject to the operator.
func actorRef(h Handle, p portal.Principal) string {
	mac := hmac.New(sha256.New, h[:])
	_, _ = mac.Write([]byte("dev-env/portal-actor/v1\x00"))
	_, _ = mac.Write([]byte(p.Issuer))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(p.Subject))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (h *commandHandler) authorized(r *http.Request) (*http.Request, Handle, Record, error) {
	if h == nil || h.engine == nil || h.authorizer == nil {
		return r, Handle{}, Record{}, portal.ErrUnavailable
	}
	r = h.engine.bind(r)
	key, record, err := h.engine.current(r)
	if err != nil {
		return r, Handle{}, Record{}, err
	}
	principal, err := h.engine.Authenticate(r)
	if err != nil {
		return r, Handle{}, Record{}, err
	}
	if err = h.authorizer.Authorize(r.Context(), principal, portal.Resource{Project: "dev-env", Environment: "internal"}); err != nil {
		return r, Handle{}, Record{}, err
	}
	return r, key, record, nil
}

func (h *commandHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	authHeaders(w)
	if h == nil || h.engine == nil || h.receipts == nil || h.transport == nil || h.render == nil || h.now == nil || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	r, key, record, err := h.authorized(r)
	if err != nil {
		authFailure(w, err)
		return
	}
	var receipt apiv1.PortalMessageReceipt
	switch {
	case r.Method == http.MethodPost && r.URL.Path == portalMessagesPath:
		receipt, err = h.post(w, r, key, record)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, portalMessagesPath+"/"):
		id := strings.TrimPrefix(r.URL.Path, portalMessagesPath+"/")
		receipt, err = h.receipt(r.Context(), key, id)
	default:
		err = portal.ErrDenied
	}
	if err != nil {
		authFailure(w, err)
		return
	}
	body, err := h.render(receipt)
	if err != nil {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", portal.ProductPolicy)
	_, _ = w.Write(body)
}

func (h *commandHandler) receipt(ctx context.Context, key Handle, id string) (apiv1.PortalMessageReceipt, error) {
	reservation, terminal, err := h.receipts.readCommand(ctx, key, id)
	if err != nil {
		return apiv1.PortalMessageReceipt{}, err
	}
	if terminal != nil {
		return *terminal, nil
	}
	return apiv1.PortalMessageReceipt{RequestID: id, SessionName: reservation.SessionName, SessionUID: reservation.SessionUID, ObservedAt: h.now().UTC(), TransportStatus: apiv1.PortalTransportUnknown, Reason: "unknown"}, nil
}

func (h *commandHandler) post(w http.ResponseWriter, r *http.Request, key Handle, record Record) (apiv1.PortalMessageReceipt, error) {
	var empty apiv1.PortalMessageReceipt
	if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != h.engine.config.Origin {
		return empty, portal.ErrDenied
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" {
		return empty, portal.ErrDenied
	}
	// Each input byte can expand to three bytes in a URL-encoded form.
	// 2048 additional bytes cover the five fixed keys and maximum valid metadata.
	r.Body = http.MaxBytesReader(w, r.Body, 3*apiv1.MaxMessageBytes+2048)
	if r.ParseForm() != nil || len(r.PostForm) != 5 {
		return empty, portal.ErrDenied
	}
	for _, name := range []string{"csrf", "requestID", "session", "sessionUID", "text"} {
		if len(r.PostForm[name]) != 1 {
			return empty, portal.ErrDenied
		}
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(record.CSRF)) != 1 {
		return empty, portal.ErrDenied
	}
	id, name, uid, text := r.PostForm.Get("requestID"), r.PostForm.Get("session"), r.PostForm.Get("sessionUID"), strings.TrimSpace(r.PostForm.Get("text"))
	if !validCommandID(id) || name == "" || len(name) > 253 || uid == "" || len(uid) > 128 || text == "" || len(text) > apiv1.MaxMessageBytes || messageURL.MatchString(text) || invalidMessageControl(text) {
		return empty, portal.ErrDenied
	}
	wanted := commandReservation{RequestID: id, SessionName: name, SessionUID: uid, Digest: sha256.Sum256([]byte(text))}
	created, err := h.receipts.reserveCommand(r.Context(), key, wanted)
	if err != nil {
		return empty, err
	}
	if !created {
		return h.receipt(r.Context(), key, id)
	}
	// A reservation that cannot be read back or whose fresh authorization fails
	// is never executed. Revocation and external Exec cannot be globally atomic.
	reservation, _, err := h.receipts.readCommand(r.Context(), key, id)
	if err != nil {
		return empty, err
	}
	fresh, freshKey, freshRecord, err := h.authorized(r)
	if err != nil || freshKey != key || freshRecord.Principal != record.Principal || sha256.Sum256([]byte(freshRecord.accessToken)) != sha256.Sum256([]byte(record.accessToken)) || subtle.ConstantTimeCompare([]byte(freshRecord.CSRF), []byte(record.CSRF)) != 1 {
		return empty, portal.ErrDenied
	}
	result, err := h.transport.send(fresh.Context(), apiv1.PortalMessageRequest{RequestID: id, Project: "dev-env", SessionName: name, SessionUID: uid, Text: text, ActorRef: actorRef(key, record.Principal)})
	if err != nil {
		return h.receipt(r.Context(), key, id)
	}
	if h.receipts.finishCommand(r.Context(), key, reservation, result) != nil {
		return h.receipt(r.Context(), key, id)
	}
	return result, nil
}

func invalidMessageControl(s string) bool {
	for _, c := range s {
		if c != '\n' && c != '\t' && (c < 0x20 || c == 0x7f || c >= 0x80 && c <= 0x9f) {
			return true
		}
	}
	return false
}
