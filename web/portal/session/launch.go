package session

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// portalLaunchesPath is the portal's same-origin launch route (DESIGN-029).
const portalLaunchesPath = portal.ProductSessionLaunchesHref

// LaunchForms connects the Sessions page to the portal launch route. A nil
// LaunchForms keeps launching off. NewRequestID returns a fresh request ID in
// the route's format, one per rendered form, so a resubmitted form maps to one
// launch. LaunchRepos is the repositories the principal's private scope allows,
// or nil for any other principal.
type LaunchForms interface {
	NewRequestID() (string, error)
	LaunchRepos(portal.Principal) []string
}

// launchScope binds the launch action to one issuer/subject and the repositories
// it may start sessions in, in the order the private file lists them.
type launchScope struct {
	Principal portal.Principal
	Repos     []string
}

var launchRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// launchHandler serves POST portalLaunchesPath. Production leaves it disabled;
// every dependency is required before it serves a route.
type launchHandler struct {
	scope      *launchScope
	engine     *Engine
	authorizer portal.Authorizer
	transport  launchTransport
	now        func() time.Time
	// dispatch serialises the fresh re-check and the operator call. The portal
	// runs one replica, so two concurrent sends of one form reach the operator
	// one after the other, and the operator's per-parent idempotency returns the
	// same session to the second instead of racing the first. It is held for the
	// operator call (up to the client's timeout); one launching person at a time.
	dispatch sync.Mutex
}

// NewLaunchForms constructs an explicitly configured launch route. Production
// calls it only when the private action file has a launch scope.
func NewLaunchForms(engine *Engine, authorizer *CurrentAuthorizer, tokenPath string, ca []byte, now func() time.Time, scope *launchScope) (LaunchForms, error) {
	if engine == nil || authorizer == nil || authorizer.client == nil || authorizer.now == nil || now == nil || scope == nil || len(scope.Repos) == 0 ||
		engine.config.Verifier == nil || authorizer.issuer != engine.config.Verifier.issuer {
		return nil, portal.ErrUnavailable
	}
	transport, err := newLaunchClient(tokenPath, ca, now, nil)
	if err != nil {
		return nil, err
	}
	return &launchHandler{scope: scope, engine: engine, authorizer: authorizer, transport: transport, now: now}, nil
}

func (*launchHandler) NewRequestID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", portal.ErrUnavailable
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// LaunchRepos returns a copy of the allowed repositories for the scope's own
// principal, and nil for anyone else.
func (h *launchHandler) LaunchRepos(p portal.Principal) []string {
	if h == nil || h.scope == nil || h.scope.Principal != p {
		return nil
	}
	return slices.Clone(h.scope.Repos)
}

func (h *launchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	authHeaders(w)
	if h == nil || h.scope == nil || h.engine == nil || h.authorizer == nil || h.transport == nil || h.now == nil || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != portalLaunchesPath {
		authFailure(w, portal.ErrDenied)
		return
	}
	r, key, record, err := authorizedAction(h.engine, h.authorizer, h.scope.Principal, r)
	if err != nil {
		authFailure(w, err)
		return
	}
	req, problems, err := h.parse(w, r, record)
	if err != nil {
		authFailure(w, err)
		return
	}
	if len(problems) > 0 {
		h.result(w, launchResult{Outcome: launchInvalid, Fields: problems}, req.Prompt)
		return
	}
	h.dispatch.Lock()
	defer h.dispatch.Unlock()
	// Fresh authorization right before dispatch: a revoked or replaced session
	// sends nothing.
	fresh, freshKey, freshRecord, err := authorizedAction(h.engine, h.authorizer, h.scope.Principal, r)
	if err != nil || freshKey != key || freshRecord.Principal != record.Principal || sha256.Sum256([]byte(freshRecord.accessToken)) != sha256.Sum256([]byte(record.accessToken)) || subtle.ConstantTimeCompare([]byte(freshRecord.CSRF), []byte(record.CSRF)) != 1 {
		authFailure(w, portal.ErrDenied)
		return
	}
	req.ActorRef = actorRef(key, record.Principal)
	result := h.transport.launch(fresh.Context(), req)
	if result.Outcome == launchCreated && launchSessionUID.MatchString(result.Receipt.SessionUID) {
		// The selected agent's page is the receipt: name, UID, phase and Remote
		// Control state.
		w.Header().Set("Location", SessionsPath+"?session="+url.QueryEscape(result.Receipt.SessionUID))
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	if result.Outcome == launchCreated {
		result.Outcome = launchUnknown
	}
	h.result(w, result, req.Prompt)
}

// parse checks the form. A tampered form (origin, content type, keys, CSRF,
// request ID, repo outside the scope, a value outside its fixed set) is denied
// and nothing reaches the operator. A well-formed form that breaks a prompt
// rule returns field problems for the result page instead.
func (h *launchHandler) parse(w http.ResponseWriter, r *http.Request, record Record) (apiv1.PortalLaunchRequest, []apiv1.FieldError, error) {
	var req apiv1.PortalLaunchRequest
	if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != h.engine.config.Origin {
		return req, nil, portal.ErrDenied
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" {
		return req, nil, portal.ErrDenied
	}
	// The textarea allows 65536 UTF-16 units, up to three UTF-8 bytes each, and
	// each byte can be three bytes in a URL-encoded form (%XX). That is the most a
	// browser that respects maxlength can send, so a prompt over the byte limit
	// reaches the check below and gets a field error, not a denial. 4096 further
	// bytes cover the other fields.
	r.Body = http.MaxBytesReader(w, r.Body, 9*protocol.MaxPromptBytes+4096)
	if r.ParseForm() != nil {
		return req, nil, portal.ErrDenied
	}
	for name, values := range r.PostForm {
		if len(values) != 1 || !slices.Contains([]string{"csrf", "requestID", "repo", "mode", "model", "effort", "size", "prompt"}, name) {
			return req, nil, portal.ErrDenied
		}
	}
	for _, name := range []string{"csrf", "requestID", "repo", "mode", "model"} {
		if len(r.PostForm[name]) != 1 {
			return req, nil, portal.ErrDenied
		}
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(record.CSRF)) != 1 {
		return req, nil, portal.ErrDenied
	}
	req = apiv1.PortalLaunchRequest{
		RequestID: r.PostForm.Get("requestID"), Repo: r.PostForm.Get("repo"), Mode: r.PostForm.Get("mode"), Model: r.PostForm.Get("model"),
		Effort: r.PostForm.Get("effort"), Size: r.PostForm.Get("size"),
		// A browser sends line breaks in a textarea as CRLF.
		Prompt: strings.ReplaceAll(r.PostForm.Get("prompt"), "\r\n", "\n"),
	}
	if !launchRequestID.MatchString(req.RequestID) || !slices.Contains(h.scope.Repos, req.Repo) ||
		!slices.Contains([]string{"task", "local", "remote"}, req.Mode) || !slices.Contains(apiv1.PortalLaunchModels(), req.Model) ||
		(req.Effort != "" && !slices.Contains(apiv1.PortalLaunchEfforts(), req.Effort)) || (req.Size != "" && req.Size != "S" && req.Size != "M") ||
		(len(req.Prompt) <= protocol.MaxPromptBytes && invalidMessageControl(req.Prompt)) {
		return req, nil, portal.ErrDenied
	}
	var problems []apiv1.FieldError
	switch {
	case len(req.Prompt) > protocol.MaxPromptBytes:
		// UTF-8 bytes, as the operator counts them. The prompt is never echoed.
		problems = append(problems, apiv1.FieldError{Field: "prompt", Message: "is longer than 64 KiB"})
	case req.Mode == "task" && strings.TrimSpace(req.Prompt) == "":
		problems = append(problems, apiv1.FieldError{Field: "prompt", Message: "A headless task needs a prompt."})
	case req.Mode != "task" && req.Prompt != "":
		problems = append(problems, apiv1.FieldError{Field: "prompt", Message: "Only a headless task takes a prompt."})
	}
	return req, problems, nil
}

// result renders the page for a launch that did not become a redirect. The
// operator's field errors appear by field name and message, short ones only. The
// operator never echoes a prompt; a message that contains a prompt of 16 bytes or
// more is dropped anyway, so the page cannot show it.
func (h *launchHandler) result(w http.ResponseWriter, r launchResult, prompt string) {
	page := portal.ProductLaunchResult{Outcome: string(r.Outcome), Back: SessionsPath + "#launch"}
	for _, f := range r.Fields {
		if len(f.Field) <= 64 && len(f.Message) <= 200 && (len(prompt) < 16 || !strings.Contains(f.Field+f.Message, prompt)) {
			page.Fields = append(page.Fields, portal.ProductField{Label: f.Field, Value: f.Message})
		}
	}
	body, err := portal.RenderLaunchResult(page)
	if err != nil {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", portal.ProductPolicy)
	_, _ = w.Write(body)
}

// withLaunchForm sets the Sessions page's launch form for the caller whose
// principal the launch scope names. It draws one request ID per rendered form.
// Any failure returns page unchanged.
func withLaunchForm(page portal.ProductPage, principal portal.Principal, csrf string, forms LaunchForms) portal.ProductPage {
	if forms == nil || csrf == "" {
		return page
	}
	repos := forms.LaunchRepos(principal)
	if len(repos) == 0 {
		return page
	}
	id, err := forms.NewRequestID()
	if err != nil {
		return page
	}
	page.Launch = &portal.ProductLaunch{Action: portalLaunchesPath, CSRF: csrf, RequestID: id, Repos: repos, Models: apiv1.PortalLaunchModels(), Efforts: apiv1.PortalLaunchEfforts()}
	return page
}
