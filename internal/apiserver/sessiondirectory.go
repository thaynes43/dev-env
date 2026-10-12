package apiserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kjson "sigs.k8s.io/json"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/sessiondirectory"
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

// Preserve the retained Kubernetes timestamp representation: UTC seconds and
// null for an observation or task timestamp that has not been recorded.
func directoryRecordedTime(value metav1.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	stamp := value.UTC().Truncate(time.Second)
	return &stamp
}

func directoryReportedTime(value *metav1.Time) *apiv1.DirectoryReportedTime {
	if value == nil {
		return nil
	}
	return &apiv1.DirectoryReportedTime{Time: value.Time}
}

func directoryCommonSource(source *coord.DirectoryCommonSource) *apiv1.DirectoryCommonSource {
	if source == nil {
		return nil
	}
	return &apiv1.DirectoryCommonSource{Namespace: string(source.Namespace), Name: string(source.Name), Key: string(source.Key), UID: string(source.UID), ResourceVersion: string(source.ResourceVersion), SHA256: string(source.SHA256)}
}

func directoryOriginal(spec coord.SessionDirectorySpec) apiv1.DirectoryOriginal {
	r := spec.Revisions
	c := r.Common
	view := apiv1.DirectoryOriginal{Version: spec.Version, Source: apiv1.TaskClaimOwner{Namespace: spec.Source.Namespace, Name: spec.Source.Name, UID: spec.Source.UID}, SourceCreatedAt: spec.SourceCreatedAt.UTC().Truncate(time.Second), Project: spec.Project,
		Repository: string(spec.Repository), Base: string(spec.Base), Parent: spec.Parent, TaskKey: string(spec.TaskKey),
		Request: apiv1.DirectoryRequest{Provider: spec.Request.Provider, CLI: string(spec.Request.CLI), Model: string(spec.Request.Model), Effort: string(spec.Request.Effort)},
		Revisions: apiv1.DirectoryRevisions{CanaryPolicy: string(r.CanaryPolicy), CaptureOutput: r.CaptureOutput, CatalogUID: string(r.CatalogUID), CatalogResourceVersion: string(r.CatalogResourceVersion), Catalog: string(r.Catalog), Rules: string(r.Rules), Template: string(r.Template), Image: string(r.Image), ProjectSnapshot: string(r.ProjectSnapshot),
			Common: apiv1.DirectoryCommon{State: c.State, Revision: string(c.Revision), SnapshotDigest: string(c.SnapshotDigest), Common: directoryCommonSource(c.Common), Header: directoryCommonSource(c.Header)}},
	}
	if local := spec.Request.Local; local != nil {
		view.Request.Local = &apiv1.DirectoryLocalProvider{Backend: local.Backend, ActualModelState: local.ActualModelState, BackendReadinessState: local.BackendReadinessState, GPUReadinessState: local.GPUReadinessState, HouseholdReservationState: local.HouseholdReservationState, Pool: string(local.Pool), RequestedModel: string(local.RequestedModel), RuntimeAdapter: local.RuntimeAdapter}
	}
	return view
}

func directoryWitness(witness *coord.DirectoryWitness) *apiv1.DirectoryWitness {
	if witness == nil {
		return nil
	}
	view := &apiv1.DirectoryWitness{SourceGeneration: witness.SourceGeneration, SourceResourceVersion: string(witness.SourceResourceVersion), ObservedAt: witness.ObservedAt.UTC().Truncate(time.Second), PodName: string(witness.PodName), PodUID: witness.PodUID,
		Node: string(witness.Node), Template: string(witness.Template), Image: string(witness.Image), PodPhase: witness.PodPhase, Ready: witness.Ready, ReportedState: string(witness.ReportedState), DeclaredConversationID: string(witness.DeclaredConversationID),
		ReportedHeartbeatAt: directoryReportedTime(witness.ReportedHeartbeatAt), ReportedTaskFinishedAt: directoryReportedTime(witness.ReportedTaskFinishedAt),
	}
	if task := witness.ReportedTask; task != nil {
		view.ReportedTask = &apiv1.DirectoryTaskReport{ExitCode: task.ExitCode, FinishedAt: directoryRecordedTime(task.FinishedAt), TimedOut: task.TimedOut, Subtype: string(task.Subtype), IsError: task.IsError}
	}
	return view
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
	return apiv1.SessionDirectory{Name: d.Name, UID: string(d.UID), ResourceVersion: d.ResourceVersion, Original: directoryOriginal(d.Spec),
		Retained: apiv1.DirectoryRetained{LastWitness: directoryWitness(retained.LastWitness), SourceState: retained.SourceState, ObservedAt: directoryRecordedTime(retained.ObservedAt), StopState: retained.StopState, ActualModelState: retained.ActualModelState, UsageState: retained.UsageState, CostState: retained.CostState},
		Current:  apiv1.DirectoryCurrent{SourceState: c.SourceState, PlacementState: c.PlacementState, ObservedAt: c.ObservedAt.Time, SourceResourceVersion: c.SourceResourceVersion, Witness: directoryWitness(c.Witness), StopState: c.StopState}}
}

// directoryRecord reads one named record after the caller's scope passed and
// rechecks that scope after the read. Absence, an invalid name and any record
// of another project stay the hidden 404. An unconfirmed read in the caller's
// own project is Unknown (503), never reported as absence.
func (s *Server) directoryRecord(ctx context.Context, c *caller, name, project string) (*coord.SessionDirectory, error) {
	if !sessiondirectory.ValidName(name) {
		return nil, directoryHidden()
	}
	d, err := s.SessionDirectory.GetForProject(ctx, name, project)
	if s.directoryScope(ctx, c, project) != nil || errors.Is(err, sessiondirectory.ErrNotFound) {
		return nil, directoryHidden()
	}
	if err != nil {
		return nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "session directory is unconfirmed; retry")
	}
	if d.Spec.Project != project {
		return nil, directoryHidden()
	}
	return d, nil
}

func (s *Server) getDirectory(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	project, _, err := directoryQuery(r, false)
	if s.directoryScope(ctx, c, project) != nil {
		return 0, nil, directoryHidden()
	}
	if err != nil {
		return 0, nil, err
	}
	d, err := s.directoryRecord(ctx, c, r.PathValue("directory"), project)
	if err != nil {
		return 0, nil, err
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
	d, err := s.directoryRecord(ctx, c, r.PathValue("directory"), project)
	if err != nil {
		return 0, nil, err
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
		ref := apiv1.DirectoryContextReference{Name: p.Name, UID: string(p.UID), ResourceVersion: p.ResourceVersion, Kind: p.Spec.Identity.Kind, Key: p.Spec.Identity.Key, ContentSHA256: p.Spec.ContentSHA256, ByteCount: p.Spec.ByteCount, Origin: p.Spec.Origin, Completeness: p.Spec.Completeness, PublishedAt: p.CreationTimestamp.Time}
		if assertion := p.Spec.ManagedOutput; assertion != nil {
			ref.DeclaredThread = &apiv1.DirectoryThreadAssertion{ConversationID: assertion.ConversationID, Invocation: assertion.Invocation, SourceNamespace: p.Spec.Source.Namespace, SourceUID: p.Spec.Source.UID, PodName: p.Spec.Actor.PodName, PodUID: p.Spec.Actor.PodUID, PublishedAt: p.CreationTimestamp.Time}
		}
		refs = append(refs, ref)
	}
	return http.StatusOK, apiv1.DirectoryContextReferences{Items: refs, DirectoryResourceVersion: d.ResourceVersion, Continue: encoded}, nil
}
