package apiserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	kjson "sigs.k8s.io/json"
)

func directoryHidden() error { return notFound("session directory not found") }

// Discovery has its own explicit scope and fresh identity proof. It never
// inherits legacy session, TaskClaim, context payload or direct-child rights.
func (s *Server) directoryScope(ctx context.Context, c *caller, project string) error {
	if s.SessionDirectory == nil || s.Live == nil || !projectcatalog.ValidName(project) || !slices.Contains(s.MetadataDiscoveryProjects, project) {
		return directoryHidden()
	}
	switch c.kind {
	case kindHuman, kindClient:
		return nil // Exact trusted identities were classified; scope is still explicit.
	case kindCoordinator:
		if _, err := s.claimCaller(ctx, c); err != nil {
			return directoryHidden()
		}
		return nil
	case kindSession:
		owner, err := s.contextCaller(ctx, c)
		if err != nil {
			return directoryHidden()
		}
		snapshot, err := claimSnapshot(owner)
		if err != nil || snapshot.Project() != project {
			return directoryHidden()
		}
		return nil
	}
	return directoryHidden()
}

type directoryCursor struct {
	Version   int    `json:"version"`
	Route     string `json:"route"`
	Project   string `json:"project"`
	Namespace string `json:"namespace"`
	UID       string `json:"uid"`
	Limit     int64  `json:"limit"`
	Continue  string `json:"continue"`
}

func decodeDirectoryCursor(raw string, scope directoryCursor) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 4096 {
		return "", badRequest("directory cursor exceeds bound")
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil {
		return "", badRequest("invalid directory cursor")
	}
	var decoded directoryCursor
	strict, err := kjson.UnmarshalStrict(data, &decoded)
	canonical, _ := json.Marshal(decoded)
	if err != nil || len(strict) != 0 || string(canonical) != string(data) || decoded.Continue == "" {
		return "", badRequest("invalid directory cursor")
	}
	continuation := decoded.Continue
	decoded.Continue = ""
	if decoded != scope {
		return "", badRequest("directory cursor belongs to another query")
	}
	return continuation, nil
}
func encodeDirectoryCursor(next string, scope directoryCursor) (string, error) {
	if next == "" {
		return "", nil
	}
	scope.Continue = next
	raw, err := json.Marshal(scope)
	if err != nil {
		return "", badRequest("invalid directory continuation")
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > 4096 {
		return "", newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "directory continuation exceeds bound")
	}
	return encoded, nil
}
func directoryQuery(r *http.Request, paged bool) (string, int64, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	for key, values := range query {
		if len(values) != 1 || (key != "project" && (!paged || (key != "limit" && key != "continue"))) {
			err = badRequest("unexpected directory query")
		}
	}
	limit := int64(20)
	if value := query.Get("limit"); value != "" {
		parsed, parseErr := strconv.ParseInt(value, 10, 64)
		limit = parsed
		if parseErr != nil {
			err = parseErr
		}
	}
	if err != nil || limit < 1 || limit > 20 {
		return "", 0, badRequest("directory limit must be 1..20 with a strict query")
	}
	return query.Get("project"), limit, nil
}
func (s *Server) directoryView(ctx context.Context, d *coord.SessionDirectory) apiv1.SessionDirectory {
	c := s.SessionDirectory.Current(ctx, d, s.now())
	retained := d.Status
	if retained.StopState == "" {
		retained.SourceState = "Unknown"
		retained.StopState = "Unknown"
		retained.ActualModelState = "Unknown"
		retained.UsageState = "Unknown"
		retained.CostState = "Unknown"
	}
	return apiv1.SessionDirectory{Name: d.Name, UID: string(d.UID), ResourceVersion: d.ResourceVersion, Original: d.Spec, Retained: retained,
		Current: apiv1.DirectoryCurrent{SourceState: c.SourceState, PlacementState: c.PlacementState, ObservedAt: c.ObservedAt.Time, SourceResourceVersion: c.SourceResourceVersion, Witness: c.Witness, StopState: c.StopState}}
}
func (s *Server) getDirectory(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	project, _, err := directoryQuery(r, false)
	if s.directoryScope(ctx, c, project) != nil {
		return 0, nil, directoryHidden()
	}
	if err != nil {
		return 0, nil, err
	}
	d, err := s.SessionDirectory.Get(ctx, r.PathValue("directory"))
	if err != nil || d.Spec.Project != project {
		return 0, nil, directoryHidden()
	}
	if s.directoryScope(ctx, c, project) != nil {
		return 0, nil, directoryHidden()
	}
	return http.StatusOK, s.directoryView(ctx, d), nil
}
func (s *Server) listDirectory(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	project, limit, err := directoryQuery(r, true)
	if s.directoryScope(ctx, c, project) != nil {
		return 0, nil, directoryHidden()
	}
	if err != nil {
		return 0, nil, err
	}
	scope := directoryCursor{Version: 1, Route: "directory", Project: project, Limit: limit}
	continuation, err := decodeDirectoryCursor(r.URL.Query().Get("continue"), scope)
	if err != nil {
		return 0, nil, err
	}
	items, next, rv, err := s.SessionDirectory.List(ctx, project, limit, continuation)
	if err != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "directory page is unconfirmed")
	}
	encoded, err := encodeDirectoryCursor(next, scope)
	if err != nil {
		return 0, nil, err
	}
	if s.directoryScope(ctx, c, project) != nil {
		return 0, nil, directoryHidden()
	}
	views := make([]apiv1.SessionDirectory, 0, len(items))
	for i := range items {
		views = append(views, s.directoryView(ctx, &items[i]))
	}
	return http.StatusOK, apiv1.SessionDirectoryList{Items: views, ResourceVersion: rv, Continue: encoded}, nil
}
func (s *Server) listDirectoryContexts(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	project, limit, err := directoryQuery(r, true)
	if s.directoryScope(ctx, c, project) != nil {
		return 0, nil, directoryHidden()
	}
	if err != nil {
		return 0, nil, err
	}
	d, err := s.SessionDirectory.Get(ctx, r.PathValue("directory"))
	if err != nil || d.Spec.Project != project {
		return 0, nil, directoryHidden()
	}
	if s.DirectoryContexts == nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "context metadata store is unavailable")
	}
	scope := directoryCursor{Version: 1, Route: "contexts", Project: project, Namespace: d.Spec.Source.Namespace, UID: d.Spec.Source.UID, Limit: limit}
	continuation, err := decodeDirectoryCursor(r.URL.Query().Get("continue"), scope)
	if err != nil {
		return 0, nil, err
	}
	// Inspect exactly one existing bounded project page; sparse pages keep next.
	items, next, err := s.DirectoryContexts.List(ctx, project, limit, continuation)
	if err != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "context reference page is unconfirmed")
	}
	encoded, err := encodeDirectoryCursor(next, scope)
	if err != nil {
		return 0, nil, err
	}
	if s.directoryScope(ctx, c, project) != nil {
		return 0, nil, directoryHidden()
	}
	refs := make([]apiv1.DirectoryContextReference, 0, len(items))
	for i := range items {
		p := &items[i]
		if p.Spec.Source.Namespace != d.Spec.Source.Namespace || p.Spec.Source.UID != d.Spec.Source.UID || p.Spec.Project != project {
			continue
		}
		refs = append(refs, apiv1.DirectoryContextReference{Name: p.Name, UID: string(p.UID), ResourceVersion: p.ResourceVersion, Kind: p.Spec.Identity.Kind, Key: p.Spec.Identity.Key, ContentSHA256: p.Spec.ContentSHA256, ByteCount: p.Spec.ByteCount, Origin: p.Spec.Origin, Completeness: p.Spec.Completeness, PublishedAt: p.CreationTimestamp.Time})
	}
	return http.StatusOK, apiv1.DirectoryContextReferences{Items: refs, DirectoryResourceVersion: d.ResourceVersion, Continue: encoded}, nil
}
