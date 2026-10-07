package apiserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// The extra keys the API server puts in a TokenReview's user info for a token
// bound to a pod (k8s.io/apiserver/pkg/authentication/serviceaccount).
const (
	extraPodName = "authentication.kubernetes.io/pod-name"
	extraPodUID  = "authentication.kubernetes.io/pod-uid"
)

// serviceAccountPrefix starts a ServiceAccount's user name:
// system:serviceaccount:<namespace>:<name>.
const serviceAccountPrefix = "system:serviceaccount:"

// Identity is who the API server says a token belongs to.
type Identity struct {
	// Username is the user name, for a ServiceAccount
	// system:serviceaccount:<namespace>:<name>.
	Username string
	// Namespace and ServiceAccount are set for a ServiceAccount token.
	Namespace      string
	ServiceAccount string
	// PodName and PodUID are set for a token bound to a pod: a projected
	// ServiceAccount token. The API server has checked that the pod exists with
	// that UID.
	PodName string
	PodUID  string
}

// serviceAccountRef is "<namespace>/<name>", the form the flags and spec.parent
// use. Empty for a token that is not a ServiceAccount's.
func (id Identity) serviceAccountRef() string {
	if id.ServiceAccount == "" {
		return ""
	}
	return id.Namespace + "/" + id.ServiceAccount
}

// Authenticator turns a bearer token into an identity (D-05).
type Authenticator interface {
	// Authenticate returns the token's identity. A token the API server does
	// not accept for the dev-env-operator audience is an *apiError with status
	// 401; a failure to ask is a 503.
	Authenticate(ctx context.Context, token string) (Identity, error)
}

// TokenReviewer authenticates with a TokenReview against the Kubernetes API
// server, for the dev-env-operator audience only (D-05). It needs create on
// tokenreviews (DESIGN-001 6.11). It keeps no cache: a review costs one API call,
// and heartbeats come once a minute per session.
type TokenReviewer struct {
	Client client.Client
}

// Authenticate implements Authenticator.
func (t TokenReviewer) Authenticate(ctx context.Context, token string) (Identity, error) {
	tr := &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token, Audiences: []string{apiv1.TokenAudience}},
	}
	if err := t.Client.Create(ctx, tr); err != nil {
		// The error is the API server's, about the review; it never holds the
		// token.
		return Identity{}, fromKubeError(err, "token review")
	}
	if !tr.Status.Authenticated {
		return Identity{}, unauthenticated("the token was not accepted for audience %s", apiv1.TokenAudience)
	}
	if !slices.Contains(tr.Status.Audiences, apiv1.TokenAudience) {
		return Identity{}, unauthenticated("the token is not for audience %s", apiv1.TokenAudience)
	}
	return identityFrom(tr.Status.User), nil
}

func identityFrom(u authenticationv1.UserInfo) Identity {
	id := Identity{Username: u.Username}
	if rest, ok := strings.CutPrefix(u.Username, serviceAccountPrefix); ok {
		if ns, name, ok := strings.Cut(rest, ":"); ok && ns != "" && name != "" && !strings.Contains(name, ":") {
			id.Namespace, id.ServiceAccount = ns, name
		}
	}
	if v := u.Extra[extraPodName]; len(v) == 1 {
		id.PodName = v[0]
	}
	if v := u.Extra[extraPodUID]; len(v) == 1 {
		id.PodUID = v[0]
	}
	return id
}

func unauthenticated(format string, args ...any) *apiError {
	return newError(http.StatusUnauthorized, apiv1.CodeUnauthenticated, format, args...)
}

// bearerToken reads the request's bearer token.
func bearerToken(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", unauthenticated("no bearer token: send a ServiceAccount token for audience %s", apiv1.TokenAudience)
	}
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", unauthenticated("the Authorization header is not a bearer token")
	}
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", unauthenticated("the bearer token is empty")
	}
	return tok, nil
}

// ParseServiceAccountRefs parses "<namespace>/<name>" references, as the
// operator's flags give them.
func ParseServiceAccountRefs(refs []string) ([]string, error) {
	var out []string
	var errs []error
	for _, r := range refs {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		ns, name, ok := strings.Cut(r, "/")
		if !ok || ns == "" || name == "" || strings.Contains(name, "/") {
			errs = append(errs, fmt.Errorf("%q is not <namespace>/<serviceaccount>", r))
			continue
		}
		out = append(out, r)
	}
	return out, errors.Join(errs...)
}
