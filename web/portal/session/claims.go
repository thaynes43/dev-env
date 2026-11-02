package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// ClaimsPath is the read-only Claims page (DESIGN-045). It lists one
// project's existing TaskClaims; a claim is not a grant, an approval or
// permission to act. The page has no form and no POST route.
const ClaimsPath = "/portal/projects/dev-env/environments/internal/claims"

// claimsProject is the one project this portal's routes serve.
const claimsProject = "dev-env"

// claimsReadLimit is the one page size the portal asks for. The portal
// follows no cursor itself; the page's Next link carries it.
const claimsReadLimit = apiv1.PortalTaskClaimsMaxLimit

// ClaimsSource reads GET /v1/portal/task-claims with the same pinned operator
// transport and rotating credential as Sessions. The token is read on every
// request. A nil source serves no Claims route.
type ClaimsSource struct {
	path    string
	client  *http.Client
	now     func() time.Time
	project string
}

// config/claims.json is opt-in: exactly {"enabled":true,"project":"dev-env"}.
// Missing or {"enabled":false} leaves credentials untouched and no source.
//
// TODO(DESIGN-045 section 8, after dev-env #850 merges): LoadProduction calls
// `claims, err := optionalClaimsSource(root, operatorCA)` after the
// optionalAsksSource block, returns errProductionConfig on err, and sets
// `Claims: claims,` in the AssemblyConfig literal; serveProduct sets
// `page.ClaimsNav = c.Claims != nil`. production.go and product.go are frozen
// by #850, so nothing calls this in production yet.
func optionalClaimsSource(root string, ca []byte) (*ClaimsSource, error) {
	present, err := mountedPresent(filepath.Join(root, "config/claims.json"))
	if err != nil || !present {
		return nil, err
	}
	raw, err := readMountedBytes(root, "config/claims.json", 8192)
	if err != nil || !utf8.Valid(raw) || !protocol.StrictPortalJSON(raw) {
		return nil, errProductionConfig
	}
	fields, err := jsonObject(raw)
	if err != nil || !exactKeys(fields, []string{"enabled"}, "project") {
		return nil, errProductionConfig
	}
	enabled, ok := jsonBool(fields["enabled"])
	if !ok {
		return nil, errProductionConfig
	}
	project, hasProject := "", false
	if value, found := fields["project"]; found {
		if project, ok = jsonString(value); !ok || project != claimsProject {
			return nil, errProductionConfig
		}
		hasProject = true
	}
	if !enabled {
		return nil, nil
	}
	if !hasProject {
		return nil, errProductionConfig
	}
	path := filepath.Join(root, "secrets/OPERATOR_TOKEN")
	if _, err := operatorCredential(path, time.Now()); err != nil {
		return nil, errProductionConfig
	}
	return NewClaimsSource(path, ca, project, time.Now)
}

// NewClaimsSource pins the operator CA and host. project must be dev-env.
func NewClaimsSource(tokenPath string, ca []byte, project string, now func() time.Time) (*ClaimsSource, error) {
	if project != claimsProject {
		return nil, portal.ErrUnavailable
	}
	source, err := newSessionSource(tokenPath, ca, now, nil)
	if err != nil {
		return nil, err
	}
	return &ClaimsSource{path: source.path, client: source.client, now: source.now, project: project}, nil
}

// read performs one bounded GET and maps every outcome to a named page state.
// Only a decoded page that follows the contract carries items; a refusal,
// a missing route, a timeout or any bad body shows no claims.
func (s *ClaimsSource) read(ctx context.Context, cursor string) portal.ClaimsPage {
	page := portal.ClaimsPage{State: portal.ClaimsNotConnected, Project: claimsProject, Cursor: cursor != ""}
	if s == nil || s.client == nil || s.now == nil || s.project != claimsProject {
		return page
	}
	page.State = portal.ClaimsUnavailable
	token, err := operatorCredential(s.path, s.now())
	if err != nil {
		return page
	}
	ctx, cancel := context.WithTimeout(ctx, sessionReadTimeout)
	defer cancel()
	query := url.Values{apiv1.PortalTaskClaimsQueryProject: {s.project}, apiv1.PortalTaskClaimsQueryLimit: {strconv.Itoa(claimsReadLimit)}}
	if cursor != "" {
		query.Set(apiv1.PortalTaskClaimsQueryCursor, cursor)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, operatorPortalOrigin+apiv1.PortalTaskClaimsPath+"?"+query.Encode(), nil)
	if err != nil {
		return page
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", mediaJSON)
	response, err := s.client.Do(req)
	if err != nil || response == nil || response.Body == nil {
		return page
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		page.State = portal.ClaimsRefused
		return page
	case http.StatusNotFound:
		page.State = portal.ClaimsNotServed
		return page
	default:
		return page
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != mediaJSON {
		return page
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, apiv1.PortalTaskClaimsMaxBytes+1))
	if err != nil || len(raw) > apiv1.PortalTaskClaimsMaxBytes {
		return page
	}
	claims, err := decodePortalTaskClaims(raw)
	if err != nil || claims.Limit != claimsReadLimit {
		return page
	}
	page.State = portal.ClaimsStateOf(claims, s.project, page.Cursor)
	if page.State == portal.ClaimsEmpty || page.State == portal.ClaimsAvailable {
		page.Read = claims
	}
	return page
}

var errClaimsContract = errors.New("portal task claims response contract")

// decodePortalTaskClaims refuses unknown, missing, duplicate or null keys at
// every level before a typed decode that also refuses unknown fields. Words
// and grammars are checked by apiv1.ValidatePortalTaskClaims.
func decodePortalTaskClaims(raw []byte) (apiv1.PortalTaskClaims, error) {
	var page apiv1.PortalTaskClaims
	if !utf8.Valid(raw) || !protocol.StrictPortalJSON(raw) {
		return page, errClaimsContract
	}
	fields, err := jsonObject(raw)
	if err != nil || !exactKeys(fields, apiv1.PortalTaskClaimsKeys, apiv1.PortalTaskClaimsOptional...) {
		return page, errClaimsContract
	}
	if value, found := fields["nextCursor"]; found {
		if cursor, ok := jsonString(value); !ok || cursor == "" {
			return page, errClaimsContract
		}
	}
	rows, ok := jsonArray(fields["items"])
	if !ok || len(rows) > apiv1.PortalTaskClaimsMaxLimit {
		return page, errClaimsContract
	}
	for _, row := range rows {
		item, err := jsonObject(row)
		if err != nil || !exactKeys(item, apiv1.PortalTaskClaimItemKeys) {
			return page, errClaimsContract
		}
		owner, err := jsonObject(item["owner"])
		if err != nil || !exactKeys(owner, apiv1.PortalTaskClaimOwnerKeys, apiv1.PortalTaskClaimOwnerOptional...) {
			return page, errClaimsContract
		}
		if value, found := owner["heartbeatAt"]; found {
			if _, ok := jsonString(value); !ok {
				return page, errClaimsContract
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&page) != nil || decoder.Decode(new(any)) != io.EOF || apiv1.ValidatePortalTaskClaims(page) != nil {
		return apiv1.PortalTaskClaims{}, errClaimsContract
	}
	return page, nil
}

// claimsQuery accepts no query or exactly one cursor in the operator's
// continuation grammar. Anything else is refused before any read.
func claimsQuery(r *http.Request) (string, bool) {
	if r.URL.ForceQuery {
		return "", false
	}
	if r.URL.RawQuery == "" {
		return "", true
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 1 || len(q[apiv1.PortalTaskClaimsQueryCursor]) != 1 {
		return "", false
	}
	cursor := q.Get(apiv1.PortalTaskClaimsQueryCursor)
	if !apiv1.ValidPortalTaskClaimsCursor(cursor) {
		return "", false
	}
	return cursor, true
}

func serveClaims(w http.ResponseWriter, r *http.Request, c AssemblyConfig) {
	authHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 19*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	cursor, queryOK := claimsQuery(r)
	if r.Method != http.MethodGet || r.URL.Path != ClaimsPath || r.URL.RawPath != "" || !queryOK || c.Now == nil {
		authFailure(w, portal.ErrDenied)
		return
	}
	r, key, record, err := authorizedAction(c.Session, c.Authorizer, r)
	if err != nil {
		if errors.Is(err, portal.ErrDenied) {
			if _, authErr := c.Session.Authenticate(r); errors.Is(authErr, portal.ErrDenied) {
				w.Header().Set("Location", "/auth/login")
				w.WriteHeader(http.StatusSeeOther)
				return
			}
		}
		authFailure(w, err)
		return
	}
	page := c.Claims.read(r.Context(), cursor)
	// The same session must still be authorized after the read.
	_, freshKey, fresh, err := authorizedAction(c.Session, c.Authorizer, r)
	if err != nil || !sameAskSession(key, record, freshKey, fresh) {
		authFailure(w, portal.ErrDenied)
		return
	}
	body, policy, err := portal.RenderClaims(page, maximumResponse*7/8)
	if err != nil {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	w.Header().Set("Content-Security-Policy", policy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
