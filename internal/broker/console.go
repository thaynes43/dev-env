package broker

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"errors"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

//go:embed approvals.html
var approvalsHTML string

const csrfCookieName = "__Host-dev-env-csrf"

// ConsolePage is the complete data passed to the escaped approval template.
type ConsolePage struct {
	Owner, CSRFToken, ReauthURL string
	Detail                      bool
	Grant                       *ApprovalGrant
	Grants                      []ApprovalGrant
}

type ApprovalGrant struct {
	Name, URL, DecisionURL, Session, Repo, Profile, Agent, Parent, ParentRepo string
	Type, Role, TTL, Reason, Phase, Message, ApprovedBy, DeniedBy             string
	CreatedAt, ExpiresAt, Scope, PolicySnippet                                string
	Namespaces                                                                []string
	Pending, Breakglass, SecretsRead, Workloads, FreshLogin                   bool
}

// Console uses the broker's live reader and status-only decision seam. There
// is no operator API route that approves requests (D-67).
type Console struct {
	Broker    *Broker
	Auth      *ForwardAuthAuthenticator
	Origin    string
	ReauthURL string
	Template  *template.Template
}

func NewConsole(b *Broker, auth *ForwardAuthAuthenticator, origin, reauthURL string) (*Console, error) {
	if b == nil || b.APIReader == nil || auth == nil || auth.Owner == "" {
		return nil, errors.New("the console needs a broker, live reader and owner authenticator")
	}
	origin, err := ConsoleOrigin(origin)
	if err != nil {
		return nil, err
	}
	if err := ValidateReauthURL(reauthURL); err != nil {
		return nil, err
	}
	t, err := template.New("approvals").Parse(approvalsHTML)
	if err != nil {
		return nil, errors.New("parse the approval template")
	}
	return &Console{Broker: b, Auth: auth, Origin: origin, ReauthURL: reauthURL, Template: t}, nil
}

// ConsoleOrigin is an exact HTTPS origin, never derived from request headers.
func ConsoleOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("the console origin must be an HTTPS origin without a path, credentials, query or fragment")
	}
	return "https://" + u.Host, nil
}

func ValidateReauthURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return errors.New("a fixed HTTPS reauthentication URL is required")
	}
	return nil
}

func (s *Console) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /approvals", s.list)
	mux.HandleFunc("GET /approvals/{grant}", s.detail)
	mux.HandleFunc("POST /approvals/{grant}/decision", s.decision)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func (s *Console) authenticate(w http.ResponseWriter, r *http.Request) (ConsoleIdentity, bool) {
	i, err := s.Auth.Authenticate(r)
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, ErrConsoleUnauthorized) {
			status = http.StatusForbidden
		}
		http.Error(w, http.StatusText(status), status)
		return ConsoleIdentity{}, false
	}
	return i, true
}

func (s *Console) page(w http.ResponseWriter, r *http.Request, identity ConsoleIdentity) (ConsolePage, bool) {
	token, err := browserCSRF(r)
	if err != nil {
		http.Error(w, "Invalid browser token", http.StatusBadRequest)
		return ConsolePage{}, false
	}
	if token == "" {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return ConsolePage{}, false
		}
		token = base64.RawURLEncoding.EncodeToString(b[:])
		http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	}
	return ConsolePage{Owner: identity.Username, CSRFToken: token, ReauthURL: s.ReauthURL}, true
}

func (s *Console) render(w http.ResponseWriter, page ConsolePage) {
	var buf bytes.Buffer
	if err := s.Template.Execute(&buf, page); err != nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (s *Console) list(w http.ResponseWriter, r *http.Request) {
	i, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var grants v1alpha1.AccessGrantList
	if err := s.Broker.APIReader.List(r.Context(), &grants, client.InNamespace(s.Broker.SessionNamespace)); err != nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	page, ok := s.page(w, r, i)
	if !ok {
		return
	}
	sort.Slice(grants.Items, func(a, b int) bool {
		return grants.Items[a].CreationTimestamp.After(grants.Items[b].CreationTimestamp.Time)
	})
	for j := range grants.Items {
		g := &grants.Items[j]
		if consolePending(g, s.Broker.now().Time) {
			view, err := s.grantView(r.Context(), g, i)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			page.Grants = append(page.Grants, view)
		}
	}
	s.render(w, page)
}

func (s *Console) getGrant(w http.ResponseWriter, r *http.Request) (*v1alpha1.AccessGrant, bool) {
	name := r.PathValue("grant")
	if !strings.HasPrefix(name, "grant-") || len(name) > 63 || len(validation.IsDNS1123Label(name)) != 0 {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return nil, false
	}
	var g v1alpha1.AccessGrant
	err := s.Broker.APIReader.Get(r.Context(), types.NamespacedName{Namespace: s.Broker.SessionNamespace, Name: name}, &g)
	if err != nil {
		status := http.StatusServiceUnavailable
		if apierrors.IsNotFound(err) {
			status = http.StatusNotFound
		}
		http.Error(w, http.StatusText(status), status)
		return nil, false
	}
	return &g, true
}

func (s *Console) detail(w http.ResponseWriter, r *http.Request) {
	i, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	g, ok := s.getGrant(w, r)
	if !ok {
		return
	}
	view, err := s.grantView(r.Context(), g, i)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	page, ok := s.page(w, r, i)
	if !ok {
		return
	}
	page.Detail, page.Grant = true, &view
	s.render(w, page)
}

func (s *Console) decision(w http.ResponseWriter, r *http.Request) {
	i, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	origins := r.Header.Values("Origin")
	if len(origins) != 1 || origins[0] != s.Origin {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		http.Error(w, http.StatusText(http.StatusUnsupportedMediaType), http.StatusUnsupportedMediaType)
		return
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		http.Error(w, http.StatusText(http.StatusUnsupportedMediaType), http.StatusUnsupportedMediaType)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		status := http.StatusBadRequest
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	for key, values := range r.PostForm {
		if len(values) != 1 || (key != "csrf" && key != "action" && key != "ttl" && key != "reason") {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
	}
	cookie, err := browserCSRF(r)
	if err != nil || cookie == "" || subtle.ConstantTimeCompare([]byte(cookie), []byte(r.PostForm.Get("csrf"))) != 1 {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	d := Decision{By: "authentik/" + i.Username}
	switch r.PostForm.Get("action") {
	case "approve":
		d.Approve = true
		if r.PostForm.Get("reason") != "" {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		if ttl := r.PostForm.Get("ttl"); ttl != "" {
			d.TTL, err = time.ParseDuration(ttl)
			if err != nil || d.TTL <= 0 {
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				return
			}
		}
	case "deny":
		d.Reason = r.PostForm.Get("reason")
		if len(d.Reason) > maxMessage || r.PostForm.Get("ttl") != "" {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	g, ok := s.getGrant(w, r)
	if !ok {
		return
	}
	if d.Approve && g.Spec.Type == v1alpha1.GrantBreakglass && !i.fresh(s.Broker.now().Time) {
		http.Error(w, "Fresh login required", http.StatusForbidden)
		return
	}
	if err := s.Broker.Decide(r.Context(), g.Name, d); err != nil {
		status := http.StatusServiceUnavailable
		switch {
		case errors.Is(err, ErrNotPending):
			status = http.StatusConflict
		case errors.Is(err, ErrRefused):
			status = http.StatusUnprocessableEntity
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	http.Redirect(w, r, "/approvals/"+g.Name, http.StatusSeeOther)
}

func browserCSRF(r *http.Request) (string, error) {
	var token string
	for _, cookie := range r.Cookies() {
		if cookie.Name != csrfCookieName {
			continue
		}
		if token != "" || len(cookie.Value) != 43 {
			return "", errors.New("invalid browser token")
		}
		b, err := tokenPart(cookie.Value)
		if err != nil || len(b) != 32 {
			return "", errors.New("invalid browser token")
		}
		token = cookie.Value
	}
	return token, nil
}

func consolePending(g *v1alpha1.AccessGrant, now time.Time) bool {
	return g.DeletionTimestamp.IsZero() && !g.Spec.Release && (g.Status.Phase == "" || g.Status.Phase == v1alpha1.GrantPending) && now.Before(g.CreationTimestamp.Add(PendingTimeout))
}
