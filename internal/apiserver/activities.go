package apiserver

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// declare-activity's routes (DESIGN-001 6.9, D-17, D-66). The API enforces what
// v1's script enforced client-side: a scope is required, the TTL is 45 minutes
// by default and 8 hours at most, and 2 hours at most for the wildcard scope
// cluster. The declarer comes from the caller's token. The operator deletes a
// declaration when it expires (internal/activity).

// The TTL rules (D-17).
const (
	defaultActivityTTL = 45 * time.Minute
	maxActivityTTL     = 8 * time.Hour
	maxClusterTTL      = 2 * time.Hour
	minActivityTTL     = time.Minute
	maxActivityBody    = 64 << 10
)

func (s *Server) activityNamespace() (string, error) {
	if s.Policy.ActivityNamespace == "" {
		return "", newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "this API keeps no activities")
	}
	return s.Policy.ActivityNamespace, nil
}

// declareActivity serves POST /v1/activities.
func (s *Server) declareActivity(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	ns, err := s.activityNamespace()
	if err != nil {
		return 0, nil, err
	}
	var req apiv1.DeclareActivityRequest
	if err := decodeJSON(w, r, maxActivityBody, &req, true); err != nil {
		return 0, nil, err
	}
	var fields []apiv1.FieldError
	add := func(field, format string, args ...any) { fields = append(fields, fieldError(field, format, args...)) }
	desc := strings.TrimSpace(req.Description)
	switch {
	case desc == "":
		add("description", "say what you are doing")
	case len(desc) > 512:
		add("description", "%d bytes, more than 512", len(desc))
	}
	var scope []string
	for i, tok := range req.Scope {
		tok = strings.TrimSpace(tok)
		switch {
		case tok == "":
			add(fmt.Sprintf("scope[%d]", i), "empty")
		case tok == "*":
			add(fmt.Sprintf("scope[%d]", i), "the wildcard is %q, not *", v1alpha1.ScopeCluster)
		case len(tok) > 63 || strings.IndexFunc(tok, unicode.IsSpace) >= 0:
			add(fmt.Sprintf("scope[%d]", i), "%q is not one token of at most 63 bytes: a namespace, an app, a node or %q", tok, v1alpha1.ScopeCluster)
		case !slices.Contains(scope, tok):
			scope = append(scope, tok)
		}
	}
	switch {
	case len(req.Scope) == 0:
		add("scope", "required: the namespaces, apps or nodes your work can disturb, or %q", v1alpha1.ScopeCluster)
	case len(req.Scope) > 32:
		add("scope", "%d tokens, more than 32", len(req.Scope))
	}
	ttl := defaultActivityTTL
	if req.TTL != "" {
		d, err := time.ParseDuration(req.TTL)
		if err != nil || d < minActivityTTL {
			add("ttl", "%q is not a duration of a minute or more, such as 45m", req.TTL)
		} else {
			ttl = d
		}
	}
	limit := maxActivityTTL
	if slices.Contains(scope, v1alpha1.ScopeCluster) {
		limit = maxClusterTTL
	}
	if ttl > limit {
		add("ttl", "%s is more than %s, the cap for this scope (D-17)", ttl, limit)
	}
	if len(fields) > 0 {
		return 0, nil, invalid(fields...)
	}

	now := s.now()
	a := &v1alpha1.Activity{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns},
		Spec: v1alpha1.ActivitySpec{
			Description: desc,
			Scope:       scope,
			DeclaredBy:  c.String(),
			ExpiresAt:   metav1.NewTime(now.Add(ttl).Truncate(time.Second)),
		},
	}
	if c.kind == kindSession {
		a.Spec.Session = c.parent
	}
	for range 5 {
		a.Name, err = activityName(now)
		if err != nil {
			return 0, nil, internal("name the activity: %v", err)
		}
		err = s.Client.Create(ctx, a)
		if !apierrors.IsAlreadyExists(err) {
			break
		}
	}
	if err != nil {
		return 0, nil, fromKubeError(err, "activity")
	}
	s.Log.Info("activity declared", "activity", a.Name, "caller", c.String(), "ttl", ttl.String(), "scope", strings.Join(scope, ","))
	return http.StatusCreated, activityView(a), nil
}

// listActivities serves GET /v1/activities: the live declarations, newest
// first. An expired one the operator has not deleted yet is left out.
func (s *Server) listActivities(ctx context.Context, _ http.ResponseWriter, _ *http.Request, _ *caller) (int, any, error) {
	ns, err := s.activityNamespace()
	if err != nil {
		return 0, nil, err
	}
	var list v1alpha1.ActivityList
	if err := s.Live.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return 0, nil, fromKubeError(err, "activities")
	}
	now := s.now()
	out := apiv1.ActivityList{Activities: []apiv1.Activity{}}
	for i := range list.Items {
		if list.Items[i].Spec.ExpiresAt.After(now) {
			out.Activities = append(out.Activities, activityView(&list.Items[i]))
		}
	}
	slices.SortFunc(out.Activities, func(a, b apiv1.Activity) int { return b.DeclaredAt.Compare(a.DeclaredAt) })
	return http.StatusOK, out, nil
}

// endActivity serves DELETE /v1/activities/{name}: any caller may end any
// declaration, as v1's file could be removed by anyone in the pod; ending one
// early is always the safe direction.
func (s *Server) endActivity(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	ns, err := s.activityNamespace()
	if err != nil {
		return 0, nil, err
	}
	name := r.PathValue("name")
	var a v1alpha1.Activity
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &a); err != nil {
		return 0, nil, fromKubeError(err, "activity "+name)
	}
	if err := s.Client.Delete(ctx, &a, client.Preconditions{UID: &a.UID}); client.IgnoreNotFound(err) != nil {
		return 0, nil, fromKubeError(err, "activity "+name)
	}
	s.Log.Info("activity ended", "activity", name, "caller", c.String())
	return http.StatusOK, activityView(&a), nil
}

// activityName is v1's id shape, act-<HHMMSS>-<6 digits>, in UTC.
func activityName(now time.Time) (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("act-%s-%06d", now.UTC().Format("150405"), n.Int64()), nil
}

func activityView(a *v1alpha1.Activity) apiv1.Activity {
	return apiv1.Activity{
		Name:        a.Name,
		Description: a.Spec.Description,
		Scope:       append([]string{}, a.Spec.Scope...),
		DeclaredBy:  a.Spec.DeclaredBy,
		Session:     a.Spec.Session,
		DeclaredAt:  a.CreationTimestamp.UTC(),
		ExpiresAt:   a.Spec.ExpiresAt.UTC(),
	}
}
