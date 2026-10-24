// Package portal is a source-only, summary-only read boundary. It supplies no
// listener, production authentication, authorization policy or live connector.
package portal

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var (
	ErrDenied      = errors.New("portal read denied")
	ErrUnavailable = errors.New("portal read unavailable")
	identifier     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// Principal comes only from a trusted authenticated session dependency, never
// from request headers. Issuer and Subject together are the immutable identity.
type Principal struct{ Issuer, Subject string }

// Resource names a project/environment collection, or one task when Task is set.
// Collection access never implies access to every member.
type Resource struct{ Project, Environment, Task string }

type Authenticator interface {
	Authenticate(*http.Request) (Principal, error)
}

// Authorizer must consult current explicit authority, including revocation. No
// role, policy cache, wildcard membership or operator identity is inferred here.
type Authorizer interface {
	Authorize(context.Context, Principal, Resource) error
}

// ReleaseAuthorizer checks all returned objects and fresh current authority in
// one final operation. An early allow must never stand in for this call.
type ReleaseAuthorizer interface {
	AuthorizeRelease(context.Context, Principal, Resource, []Item) error
}

type MetadataReader interface {
	Read(context.Context, Resource) (Snapshot, error)
}

// UnavailableReader is the adapter until the canonical #177 read contract is
// accepted and separately wired. It does not synthesize workflow transitions.
type UnavailableReader struct{}

func (UnavailableReader) Read(context.Context, Resource) (Snapshot, error) {
	return Snapshot{}, ErrUnavailable
}

type ReadState string

const (
	Verified    ReadState = "verified"
	Unknown     ReadState = "unknown"
	Unavailable ReadState = "unavailable"
)

type Group string

const (
	NeedsYou       Group = "needs-you"
	NeedsWorker    Group = "needs-worker"
	ReviewReturned Group = "review-returned"
	HandoffMissing Group = "handoff-missing"
)

// Item deliberately has no unstructured payload, imported link, prompt, log,
// artifact, provider token, pairing information or credential field.
type Item struct {
	Resource                                          Resource
	WorkPackage                                       int
	Group                                             Group
	Title, Owner, NextActor, NextAction               string
	TaskOutcome, WorkerState, ReviewState, ReviewHead string
	AuthorityState, ReleaseState                      string
	UpdatedAt, VerifiedAt                             time.Time
}

type Snapshot struct {
	Resource              Resource
	State                 ReadState
	UpdatedAt, VerifiedAt time.Time
	Items                 []Item
}

type Dependencies struct {
	Authenticator Authenticator
	Authorizer    Authorizer
	Reader        MetadataReader
	// FreshFor must be explicit. It is not a production SLO or policy default.
	FreshFor time.Duration
	Now      func() time.Time
}

type handler struct{ deps Dependencies }

func NewHandler(deps Dependencies) http.Handler { return &handler{deps: deps} }

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		refuse(w, http.StatusMethodNotAllowed)
		return
	}
	resource, ok := requestResource(r)
	if !ok {
		refuse(w, http.StatusNotFound)
		return
	}
	d := h.deps
	if d.Authenticator == nil || d.Authorizer == nil || d.Reader == nil || d.FreshFor <= 0 {
		refuse(w, http.StatusServiceUnavailable)
		return
	}
	principal, err := d.Authenticator.Authenticate(r)
	if err != nil || !validPrincipal(principal) {
		if errors.Is(err, ErrDenied) {
			refuse(w, http.StatusNotFound)
		} else {
			refuse(w, http.StatusServiceUnavailable)
		}
		return
	}
	if err = d.Authorizer.Authorize(r.Context(), principal, resource); err != nil {
		refuseDependency(w, err)
		return
	}
	snapshot, err := d.Reader.Read(r.Context(), resource)
	if err != nil {
		refuseDependency(w, err)
		return
	}
	if !validSnapshot(snapshot, resource) {
		refuse(w, http.StatusServiceUnavailable)
		return
	}
	if snapshot.State == Unavailable {
		refuse(w, http.StatusServiceUnavailable)
		return
	}
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}
	var body bytes.Buffer
	if err = pageTemplate.Execute(&body, makePage(snapshot, now, d.FreshFor)); err != nil {
		refuse(w, http.StatusServiceUnavailable)
		return
	}
	// Buffer first: no metadata or success status escapes before these current
	// post-read checks. A revoked/replaced session also fails this identity check.
	current, err := d.Authenticator.Authenticate(r)
	if err != nil || current != principal {
		if errors.Is(err, ErrDenied) || err == nil {
			refuse(w, http.StatusNotFound)
		} else {
			refuse(w, http.StatusServiceUnavailable)
		}
		return
	}
	if release, ok := d.Authorizer.(ReleaseAuthorizer); ok {
		err = release.AuthorizeRelease(r.Context(), current, resource, snapshot.Items)
	} else {
		err = d.Authorizer.Authorize(r.Context(), current, resource)
		if err == nil {
			for _, item := range snapshot.Items {
				if err = d.Authorizer.Authorize(r.Context(), current, item.Resource); err != nil {
					break
				}
			}
		}
	}
	if err != nil {
		refuseDependency(w, err)
		return
	}
	if r.Context().Err() != nil {
		refuse(w, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// A failed client write cannot be repaired or replayed; no operation occurred.
	_, _ = w.Write(body.Bytes())
}

func requestResource(r *http.Request) (Resource, bool) {
	// Fixed paths only. No query, encoded aliases, arbitrary resource URLs or
	// paths delegated to a proxy/connector. Reject before any dependency read.
	if r.URL.RawQuery != "" || r.URL.RawPath != "" || r.URL.ForceQuery {
		return Resource{}, false
	}
	p := strings.Split(r.URL.Path, "/")
	if (len(p) != 7 && len(p) != 8) || p[0] != "" || p[1] != "portal" || p[2] != "projects" || p[4] != "environments" {
		return Resource{}, false
	}
	result := Resource{Project: p[3], Environment: p[5]}
	if len(p) == 7 && p[6] != "attention" {
		return Resource{}, false
	}
	if len(p) == 8 {
		if p[6] != "tasks" || !identifier.MatchString(p[7]) {
			return Resource{}, false
		}
		result.Task = p[7]
	}
	return result, identifier.MatchString(result.Project) && identifier.MatchString(result.Environment)
}

func validPrincipal(p Principal) bool {
	return p.Issuer != "" && p.Subject != "" && validText(p.Issuer) && validText(p.Subject)
}

func validSnapshot(s Snapshot, requested Resource) bool {
	if s.Resource != requested || len(s.Items) > 24 {
		return false
	}
	if s.State != Verified && s.State != Unknown && s.State != Unavailable {
		return false
	}
	if s.State != Verified && len(s.Items) != 0 {
		if s.State != Unknown {
			return false
		}
		for _, item := range s.Items {
			if item.Group != "" || item.NextActor != "" || item.NextAction != "" || item.TaskOutcome != "" || item.ReviewState != "" || item.ReviewHead != "" || item.AuthorityState != "" || item.ReleaseState != "" {
				return false
			}
		}
	}
	if requested.Task != "" && s.State == Verified && len(s.Items) != 1 {
		return false
	}
	seen := make(map[string]bool)
	for _, item := range s.Items {
		r := item.Resource
		if r.Project != requested.Project || r.Environment != requested.Environment || !identifier.MatchString(r.Task) || (requested.Task != "" && r.Task != requested.Task) || seen[r.Task] {
			return false
		}
		seen[r.Task] = true
		if item.WorkPackage < 1 || item.WorkPackage > 999999999 || (s.State == Verified && !knownGroup(item.Group)) || (s.State == Unknown && item.Group != "") {
			return false
		}
		if item.ReviewHead != "" && !exactHead.MatchString(item.ReviewHead) {
			return false
		}
		for _, field := range []string{item.Title, item.Owner, item.NextActor, item.NextAction, item.TaskOutcome, item.WorkerState, item.ReviewState, item.AuthorityState, item.ReleaseState} {
			if !validText(field) {
				return false
			}
		}
	}
	return true
}

func knownGroup(g Group) bool {
	return g == NeedsYou || g == NeedsWorker || g == ReviewReturned || g == HandoffMissing
}

func refuseDependency(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrDenied) {
		refuse(w, http.StatusNotFound)
	} else {
		refuse(w, http.StatusServiceUnavailable)
	}
}

func refuse(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte("Attention unavailable. Current task state is Unknown.\n"))
}

func securityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", contentPolicy)
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}
