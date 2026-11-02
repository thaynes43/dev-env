package apiserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// PortalTaskClaimsConfig serves GET /v1/portal/task-claims (DESIGN-045), one
// page of a project's existing TaskClaims as an allowlist for the portal
// reader. Default off. Projects must be a subset of TaskClaimReadProjects; an
// empty list leaves the route unregistered. It does not admit the portal
// reader to /v1/task-claims, and it changes no session or coordinator scope.
// A claim is not a grant, an approval or permission to act.
type PortalTaskClaimsConfig struct {
	Enabled  bool
	Projects []string
}

// portalTaskClaimsEnabled is the route gate. The claim store is not part of
// it: an enabled route on an operator without claims answers Unconfigured.
func (s *Server) portalTaskClaimsEnabled() bool {
	return s != nil && s.PortalTaskClaims.Enabled && len(s.PortalTaskClaims.Projects) > 0 && s.Policy.PortalReaderEnabled
}

func (s *Server) registerPortalTaskClaims(mux *http.ServeMux) {
	if !s.portalTaskClaimsEnabled() {
		return
	}
	mux.Handle(apiv1.PortalTaskClaimsPath, s.serve(route{methods: map[string]handler{http.MethodGet: s.portalTaskClaims}}))
}

// portalTaskClaimsRoute admits the portal reader to the exact GET path only.
func (s *Server) portalTaskClaimsRoute(r *http.Request) bool {
	return s.portalTaskClaimsEnabled() && r != nil && r.URL != nil && r.Method == http.MethodGet && r.URL.Path == apiv1.PortalTaskClaimsPath &&
		(r.URL.RawPath == "" || r.URL.RawPath == apiv1.PortalTaskClaimsPath)
}

// portalTaskClaims reads one store page. It never calls claimCaller or
// claimReadScope: the portal reader is not a session or a coordinator. Every
// refusal after a valid query is a named Unknown state, never an empty list.
func (s *Server) portalTaskClaims(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if !s.portalTaskClaimsEnabled() {
		return 0, nil, notFound("portal task claims were not found")
	}
	if c == nil || c.kind != kindPortalReader {
		return 0, nil, forbidden("only the portal reader may read portal task claims")
	}
	q, err := parsePortalTaskClaimsQuery(r)
	if err != nil {
		return 0, nil, err
	}
	now := s.now().UTC()
	page := apiv1.PortalTaskClaims{Version: apiv1.PortalTaskClaimsVersion, Project: q.project, Items: []apiv1.PortalTaskClaimItem{}, Limit: q.limit, ObservedAt: now}
	unknown := func(reason string) (int, any, error) {
		page.State, page.Reason, page.Items, page.NextCursor, page.Complete = apiv1.PortalTaskClaimsStateUnknown, reason, []apiv1.PortalTaskClaimItem{}, "", false
		return http.StatusOK, page, nil
	}
	if s.claimsAvailable() != nil {
		return unknown(apiv1.PortalTaskClaimsReasonUnconfigured)
	}
	if !slices.Contains(s.PortalTaskClaims.Projects, q.project) || !slices.Contains(s.TaskClaimReadProjects, q.project) {
		return unknown(apiv1.PortalTaskClaimsReasonScopeUnconfirmed)
	}
	// One store page per request, never a refill loop. Any store error,
	// an expired continue token included, is the whole page Unavailable.
	claims, next, err := s.TaskClaims.List(ctx, q.project, int64(q.limit), q.cont)
	if err != nil || len(claims) > q.limit {
		return unknown(apiv1.PortalTaskClaimsReasonUnavailable)
	}
	owners := map[coord.SessionIdentity]apiv1.PortalTaskClaimOwner{}
	for i := range claims {
		claim := &claims[i]
		owner, seen := owners[claim.Spec.Owner]
		if !seen {
			owner = s.portalClaimOwner(ctx, claim.Spec.Owner, now)
			owners[claim.Spec.Owner] = owner
		}
		page.Items = append(page.Items, apiv1.PortalTaskClaimItem{
			Name: claim.Name, UID: string(claim.UID),
			Repository: claim.Spec.Identity.Repository, TaskKey: claim.Spec.Identity.TaskKey,
			GitHub: claim.Spec.Provenance.GitHub, CreatedAt: claim.CreationTimestamp.UTC(), Owner: owner,
		})
	}
	switch {
	case next != "":
		cursor, cErr := encodePortalClaimsCursor(portalClaimsCursor{Continue: next, Project: q.project, Limit: q.limit})
		if cErr != nil {
			return unknown(apiv1.PortalTaskClaimsReasonUnavailable)
		}
		page.State, page.Reason, page.NextCursor = apiv1.PortalTaskClaimsStateAvailable, apiv1.PortalTaskClaimsReasonTruncated, cursor
	case len(page.Items) == 0 && !q.cursor:
		page.State, page.Reason, page.Complete = apiv1.PortalTaskClaimsStateEmpty, apiv1.PortalTaskClaimsReasonComplete, true
	default:
		page.State, page.Reason, page.Complete = apiv1.PortalTaskClaimsStateAvailable, apiv1.PortalTaskClaimsReasonComplete, true
	}
	return http.StatusOK, boundPortalTaskClaims(page, apiv1.PortalTaskClaimsMaxBytes), nil
}

// boundPortalTaskClaims returns page when it follows the contract and its
// encoding, with the encoder's newline, fits max. Otherwise the page is
// Unknown/Unavailable with no items: never truncated, never partial.
func boundPortalTaskClaims(page apiv1.PortalTaskClaims, max int) apiv1.PortalTaskClaims {
	if apiv1.ValidatePortalTaskClaims(page) == nil {
		if encoded, err := json.Marshal(page); err == nil && len(encoded)+1 <= max {
			return page
		}
	}
	page.State, page.Reason, page.Items, page.NextCursor, page.Complete = apiv1.PortalTaskClaimsStateUnknown, apiv1.PortalTaskClaimsReasonUnavailable, []apiv1.PortalTaskClaimItem{}, "", false
	return page
}

// portalClaimOwner is one uncached Get of the owner by name in the session
// namespace. A same-UID Session reports Deleting or its phase and a heartbeat
// freshness; a different UID is Replaced; not found is NotFound; any other
// error, another namespace or an empty or unrecognized phase is Unknown. No
// same-UID read means no heartbeat. A future heartbeat is never sent.
func (s *Server) portalClaimOwner(ctx context.Context, id coord.SessionIdentity, now time.Time) apiv1.PortalTaskClaimOwner {
	owner := apiv1.PortalTaskClaimOwner{Name: id.Name, UID: id.UID, State: apiv1.PortalClaimOwnerUnknown, Freshness: apiv1.PortalClaimFreshnessUnknown}
	if s.Live == nil || id.Namespace != s.Policy.SessionNamespace {
		return owner
	}
	var sess v1alpha1.AgentSession
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: id.Namespace, Name: id.Name}, &sess); err != nil {
		if apierrors.IsNotFound(err) {
			owner.State = apiv1.PortalClaimOwnerNotFound
		}
		return owner
	}
	if sess.Namespace != id.Namespace || sess.Name != id.Name {
		return owner
	}
	if string(sess.UID) != id.UID {
		owner.State = apiv1.PortalClaimOwnerReplaced
		return owner
	}
	if !sess.DeletionTimestamp.IsZero() {
		owner.State = apiv1.PortalClaimOwnerDeleting
	} else {
		switch sess.Status.Phase {
		case v1alpha1.PhasePending, v1alpha1.PhaseRunning, v1alpha1.PhaseIdle, v1alpha1.PhaseDraining, v1alpha1.PhaseSuspended, v1alpha1.PhaseArchived, v1alpha1.PhaseFailed:
			owner.State = string(sess.Status.Phase)
		default:
			return owner
		}
	}
	if sess.Status.Agent != nil && sess.Status.Agent.LastHeartbeat != nil && !sess.Status.Agent.LastHeartbeat.IsZero() {
		beat := sess.Status.Agent.LastHeartbeat.UTC()
		if !beat.After(now) {
			owner.HeartbeatAt = &beat
			if now.Sub(beat) <= portalFreshFor {
				owner.Freshness = apiv1.PortalClaimFreshnessObserved
			} else {
				owner.Freshness = apiv1.PortalClaimFreshnessStale
			}
		}
	}
	return owner
}

type portalTaskClaimsQuery struct {
	project string
	limit   int
	cont    string
	cursor  bool
}

// parsePortalTaskClaimsQuery is strict: project is required, limit is 1 to
// 100 in canonical decimal (default 50), cursor is optional and must carry
// the same limit. Any other key, a repeated key, ';' or a non-canonical path
// is 400.
func parsePortalTaskClaimsQuery(r *http.Request) (portalTaskClaimsQuery, error) {
	var q portalTaskClaimsQuery
	bad := badRequest("invalid portal task claims query")
	if r == nil || r.URL == nil || r.URL.Path != apiv1.PortalTaskClaimsPath || (r.URL.RawPath != "" && r.URL.RawPath != apiv1.PortalTaskClaimsPath) || strings.Contains(r.URL.RawQuery, ";") {
		return q, bad
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return q, bad
	}
	for key, got := range values {
		switch key {
		case apiv1.PortalTaskClaimsQueryProject, apiv1.PortalTaskClaimsQueryLimit, apiv1.PortalTaskClaimsQueryCursor:
		default:
			return q, bad
		}
		if len(got) != 1 {
			return q, bad
		}
	}
	project, ok := values[apiv1.PortalTaskClaimsQueryProject]
	if !ok || !projectcatalog.ValidName(project[0]) {
		return q, bad
	}
	q.project = project[0]
	q.limit = portalPageDefault
	if raw, ok := values[apiv1.PortalTaskClaimsQueryLimit]; ok {
		n, nErr := strconv.Atoi(raw[0])
		if nErr != nil || n < 1 || n > portalPageMax || strconv.Itoa(n) != raw[0] {
			return q, bad
		}
		q.limit = n
	}
	if raw, ok := values[apiv1.PortalTaskClaimsQueryCursor]; ok {
		cursor, cErr := decodePortalClaimsCursor(raw[0])
		if cErr != nil || cursor.Limit != q.limit || cursor.Project != q.project {
			return q, bad
		}
		q.cont, q.cursor = cursor.Continue, true
	}
	return q, nil
}

// portalClaimsCursor is the opaque continuation: the store's continue token
// and the project and page limit it was issued for. A cursor for another
// project or limit is refused, so one project's listing never resumes from
// another's position.
type portalClaimsCursor struct {
	Continue string `json:"continue"`
	Project  string `json:"project"`
	Limit    int    `json:"limit"`
}

var errPortalClaimsCursor = badRequest("the portal task claims cursor is malformed")

func decodePortalClaimsCursor(raw string) (portalClaimsCursor, error) {
	var cursor portalClaimsCursor
	if len(raw) > portalCursorMax || !apiv1.ValidPortalTaskClaimsCursor(raw) {
		return cursor, errPortalClaimsCursor
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) == 0 {
		return cursor, errPortalClaimsCursor
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if dec.Decode(&cursor) != nil || !portalCursorText(cursor.Continue, 1500) || !projectcatalog.ValidName(cursor.Project) || cursor.Limit < 1 || cursor.Limit > portalPageMax {
		return portalClaimsCursor{}, errPortalClaimsCursor
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return portalClaimsCursor{}, errPortalClaimsCursor
	}
	return cursor, nil
}

func encodePortalClaimsCursor(cursor portalClaimsCursor) (string, error) {
	if !portalCursorText(cursor.Continue, 1500) || !projectcatalog.ValidName(cursor.Project) || cursor.Limit < 1 || cursor.Limit > portalPageMax {
		return "", errPortalClaimsCursor
	}
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if !apiv1.ValidPortalTaskClaimsCursor(encoded) {
		return "", errPortalClaimsCursor
	}
	return encoded, nil
}
