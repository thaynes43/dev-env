package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/contextpublication"
)

// Scope parsing shares syntax with claim metadata, never its permissions.
func ParseContextReadProjects(raw string) ([]string, error) { return ParseTaskClaimReadProjects(raw) }
func contextDenied() error {
	return forbidden("published context requires its explicit payload scope and live owned identity")
}
func (s *Server) contextsAvailable() error {
	if s.PublishedContexts == nil || s.Live == nil || s.Projects == nil {
		return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "published context is not enabled")
	}
	return nil
}
func (s *Server) contextCaller(ctx context.Context, c *caller) (*v1alpha1.AgentSession, error) {
	sess, err := s.claimCaller(ctx, c)
	if err != nil {
		return nil, contextDenied()
	}
	if c.kind == kindCoordinator {
		return nil, nil
	}
	var pod corev1.Pod
	if s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: c.identity.PodName}, &pod) != nil ||
		string(pod.UID) != c.identity.PodUID || !pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning ||
		sess.Spec.OperatingMode == v1alpha1.OperatingModeSuspended || sess.Status.Phase == v1alpha1.PhaseArchived || sess.Status.Phase == v1alpha1.PhaseSuspended || sess.Status.Phase == v1alpha1.PhaseFailed {
		return nil, contextDenied()
	}
	return sess, nil
}
func (s *Server) contextReadScope(ctx context.Context, c *caller, project string) error {
	if !slices.Contains(s.ContextReadProjects, project) {
		return contextDenied()
	}
	sess, err := s.contextCaller(ctx, c)
	if err != nil {
		return err
	}
	if c.kind == kindSession {
		snapshot, err := claimSnapshot(sess)
		if err != nil || snapshot.Project() != project {
			return contextDenied()
		}
	}
	return nil
}

// managedExecutor reports whether the Session's retained private history names
// this exact Pod as its last executor, the binding a managed output relies on.
func managedExecutor(sess *v1alpha1.AgentSession, podUID string) bool {
	h := sess.Status.PrivateHistory
	return h != nil && h.SessionUID == string(sess.UID) && h.LastExecutor != nil && h.LastExecutor.PodUID == podUID
}

func (s *Server) publishContext(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.contextsAvailable(); err != nil {
		return 0, nil, err
	}
	if c.kind != kindSession {
		return 0, nil, contextDenied()
	}
	owner, err := s.contextCaller(ctx, c)
	if err != nil {
		return 0, nil, err
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, apiv1.MaxContextRequestBytes))
	if err != nil || !utf8.Valid(data) {
		return 0, nil, badRequest("expected bounded UTF-8 context publication")
	}
	var req apiv1.PublishContextRequest
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || !errors.Is(d.Decode(new(any)), io.EOF) || !apiv1.ValidContextRequest(req) {
		return 0, nil, badRequest("expected kind, logical key and UTF-8 text of at most 64KiB")
	}
	snapshot, err := claimSnapshot(owner)
	// Publication, like the inbox, is enabled per explicit project: the payload
	// project list that readers need is also the list a publisher may write to.
	if err != nil || !slices.Contains(s.ContextReadProjects, snapshot.Project()) {
		return 0, nil, contextDenied()
	}
	_, selected, err := s.Projects.snapshot(ctx, s.Live, snapshot.Project(), snapshot.Selected().Name)
	if err != nil || !strings.EqualFold(selected.GitHub, snapshot.Selected().GitHub) {
		return 0, nil, contextDenied()
	}
	spec := coord.PublishedContextSpec{
		Identity: coord.ContextIdentity{SessionUID: string(owner.UID), Kind: req.Kind, Key: req.Key},
		Source:   coord.SessionIdentity{Namespace: owner.Namespace, Name: owner.Name, UID: string(owner.UID)}, SourceGeneration: owner.Generation, SourceParent: owner.Spec.Parent,
		Project: snapshot.Project(), Repository: snapshot.Selected().Name,
		Actor:       coord.ClaimActor{Kind: "session", ServiceAccount: c.identity.serviceAccountRef(), PodName: c.identity.PodName, PodUID: c.identity.PodUID},
		Provenance:  coord.ClaimProvenance{CatalogRevision: snapshot.CatalogRevision(), RulesRevision: snapshot.RulesRevision(), GitHub: strings.ToLower(snapshot.Selected().GitHub), DefaultBranch: snapshot.Selected().DefaultBranch, InitialPinState: "Unknown"},
		Attribution: coord.ContextAttribution{Provider: string(owner.Spec.Agent), RequestedModel: owner.Spec.Model, ObservedModelState: "Unknown", UsageState: "Unknown", CostState: "Unknown"},
		Origin:      "ExplicitPublished", Completeness: "ExplicitExcerpt", StopState: "Unknown", Text: req.Text, ContentSHA256: apiv1.ContextHash(req.Text), ByteCount: int32(len(req.Text)),
	}
	if m := req.ManagedOutput; m != nil {
		b := owner.Spec.ProjectCanary
		if !managedExecutor(owner, c.identity.PodUID) {
			return 0, nil, contextDenied()
		}
		if b == nil || !b.CaptureOutput || !s.PrivateHistoryRetention || owner.Spec.Mode != v1alpha1.ModeTask || owner.Spec.Workspace != nil || s.Projects == nil {
			return 0, nil, contextDenied()
		}
		record, ok := s.PrivateProjectCanaries.Lookup(b.ID)
		if !ok || !record.CaptureOutput || record.MatchSession(owner, s.Projects.CloneOwner) != nil {
			return 0, nil, contextDenied()
		}
		invocation := apiv1.ManagedInvocation(string(owner.UID), c.identity.PodUID)
		if req.Key != apiv1.ManagedOutputKey(invocation, m.Ordinal) {
			return 0, nil, contextDenied()
		}
		spec.ManagedOutput = &coord.ManagedOutputAssertion{Invocation: invocation, Ordinal: m.Ordinal, ConversationID: m.ConversationID}
		spec.Origin, spec.Completeness = "ManagedOutputAssertion", m.Completeness
	}
	if !contextpublication.ValidSpec(spec) {
		return 0, nil, contextDenied()
	}
	// Recheck the original live source after request/catalog I/O, immediately
	// before the one atomic mutation. Reads never check the stored source.
	// Status is not compared, so a heartbeat between the reads is no refusal; the
	// fresh live caller check and the managed executor recheck cover status.
	fresh, err := s.contextCaller(ctx, c)
	if err != nil || !sameSessionBinding(fresh, owner) || (spec.ManagedOutput != nil && !managedExecutor(fresh, c.identity.PodUID)) {
		return 0, nil, contextDenied()
	}
	publication, created, err := s.PublishedContexts.Create(ctx, spec)
	if errors.Is(err, contextpublication.ErrLimit) {
		return 0, nil, newError(http.StatusTooManyRequests, apiv1.CodeLimitExceeded, "published context limit reached for this source Session or project; nothing was published")
	}
	if errors.Is(err, contextpublication.ErrConflict) {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "publication key already has different immutable content or provenance")
	}
	if err != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "publication is unconfirmed; retry only the same original identity and content")
	}
	status, outcome := http.StatusOK, "Confirmed"
	if created {
		status, outcome = http.StatusCreated, "Created"
	}
	m := contextMetadata(publication)
	if providerAttribution(r) {
		m.Attribution = contextAttribution(m.Attribution, publication, fresh)
	}
	return status, apiv1.PublishContextResult{Outcome: outcome, Metadata: m}, nil
}

func (s *Server) getPublishedContext(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.contextsAvailable(); err != nil {
		return 0, nil, err
	}
	project := r.URL.Query().Get("project")
	if err := s.contextReadScope(ctx, c, project); err != nil {
		return 0, nil, err
	}
	if !contextpublication.ValidName(r.PathValue("context")) {
		return 0, nil, badRequest("invalid publication name")
	}
	p, err := s.PublishedContexts.GetForProject(ctx, r.PathValue("context"), project)
	if errors.Is(err, contextpublication.ErrNotFound) {
		if errors.Is(err, contextpublication.ErrProjectMasked) || !s.CoordinationArchiveReads {
			return 0, nil, notFound("published context not found")
		}
		archived, archivedAt, archiveErr := s.archivedPublishedContext(ctx, r.PathValue("context"), project)
		if archiveErr != nil {
			return 0, nil, archiveErr
		}
		return http.StatusOK, apiv1.PublishedContext{Metadata: archivedContextMetadata(archived, archivedAt), Text: archived.Spec.Text}, nil
	}
	if err != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "published context unavailable")
	}
	if p.Spec.Project != project {
		return 0, nil, notFound("published context not found")
	}
	m := contextMetadata(p)
	if providerAttribution(r) {
		m.Attribution = s.joinedAttribution(ctx, m.Attribution, p, map[string]*v1alpha1.AgentSession{})
	}
	return http.StatusOK, apiv1.PublishedContext{Metadata: m, Text: p.Spec.Text}, nil
}
func (s *Server) listPublishedContexts(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.contextsAvailable(); err != nil {
		return 0, nil, err
	}
	project := r.URL.Query().Get("project")
	if err := s.contextReadScope(ctx, c, project); err != nil {
		return 0, nil, err
	}
	limit := int64(20)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || limit < 1 || limit > 20 {
			return 0, nil, badRequest("limit must be between 1 and 20")
		}
	}
	archivedValues := r.URL.Query()["archived"]
	if len(archivedValues) > 1 {
		return 0, nil, badRequest("archived must be only")
	}
	if archived := r.URL.Query().Get("archived"); archived != "" {
		if archived != "only" {
			return 0, nil, badRequest("archived must be only")
		}
		for key, values := range r.URL.Query() {
			if len(values) != 1 || (key != "project" && key != "limit" && key != "continue" && key != "archived") {
				return 0, nil, badRequest("invalid archived context query")
			}
		}
		if !s.CoordinationArchiveReads {
			return 0, nil, notFound("published context not found")
		}
		return s.listArchivedPublishedContexts(ctx, project, limit, r.URL.Query().Get("continue"))
	}
	items, next, err := s.PublishedContexts.List(ctx, project, limit, r.URL.Query().Get("continue"))
	if err != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "published context unavailable")
	}
	views := make([]apiv1.ContextMetadata, 0, len(items))
	sources := map[string]*v1alpha1.AgentSession{}
	for i := range items {
		m := contextMetadata(&items[i])
		if providerAttribution(r) {
			m.Attribution = s.joinedAttribution(ctx, m.Attribution, &items[i], sources)
		}
		views = append(views, m)
	}
	return http.StatusOK, apiv1.PublishedContextList{Items: views, Continue: next}, nil
}
func contextMetadata(c *coord.PublishedContext) apiv1.ContextMetadata {
	p := c.Spec
	s := p.Source
	a := p.Actor
	v := p.Provenance
	m := p.Attribution
	result := apiv1.ContextMetadata{Name: c.Name, UID: string(c.UID), Kind: p.Identity.Kind, Key: p.Identity.Key, Source: apiv1.TaskClaimOwner{Namespace: s.Namespace, Name: s.Name, UID: s.UID}, SourceGeneration: p.SourceGeneration, SourceParent: p.SourceParent, Project: p.Project, Repository: p.Repository,
		Actor: apiv1.TaskClaimActor{Kind: a.Kind, ServiceAccount: a.ServiceAccount, PodName: a.PodName, PodUID: a.PodUID}, Provenance: apiv1.TaskClaimProvenance{CatalogRevision: v.CatalogRevision, RulesRevision: v.RulesRevision, GitHub: v.GitHub, DefaultBranch: v.DefaultBranch, InitialPinState: v.InitialPinState},
		Attribution: apiv1.ContextAttribution{Provider: m.Provider, RequestedModel: m.RequestedModel, ObservedModelState: m.ObservedModelState, UsageState: m.UsageState, CostState: m.CostState}, ContentSHA256: p.ContentSHA256, ByteCount: p.ByteCount, PublishedAt: c.CreationTimestamp.Time, Origin: p.Origin, Completeness: p.Completeness, StopState: p.StopState}
	if m := p.ManagedOutput; m != nil {
		result.ManagedOutput = &apiv1.ManagedOutputAssertion{Invocation: m.Invocation, Ordinal: m.Ordinal, ConversationID: m.ConversationID}
	}
	return result
}

// providerAttribution reports whether the reader asked for provider-reported
// attribution (DESIGN-018). Without the header the original all-Unknown shape
// is returned, which older strict readers accept.
func providerAttribution(r *http.Request) bool {
	return r.Header.Get(apiv1.AttributionHeader) == apiv1.AttributionProvider
}

// joinedAttribution reads the publication's source Session (once per list) and
// joins its retained observation. A missing or replaced source stays Unknown.
func (s *Server) joinedAttribution(ctx context.Context, a apiv1.ContextAttribution, p *coord.PublishedContext, sources map[string]*v1alpha1.AgentSession) apiv1.ContextAttribution {
	if p.Spec.ManagedOutput == nil {
		return a
	}
	src := p.Spec.Source
	source, cached := sources[src.UID]
	if !cached {
		var sess v1alpha1.AgentSession
		if s.get(ctx, types.NamespacedName{Namespace: src.Namespace, Name: src.Name}, &sess) == nil && string(sess.UID) == src.UID {
			source = &sess
		}
		sources[src.UID] = source
	}
	return contextAttribution(a, p, source)
}

// contextAttribution joins a managed output assertion with the source
// Session's retained version 2 observation of the same invocation: the same
// Session UID and Pod UID, provider and requested model. Each state becomes
// ProviderReported only when that part was reported; anything else keeps the
// stored attribution, Unknown. The stored record is never changed.
func contextAttribution(a apiv1.ContextAttribution, p *coord.PublishedContext, source *v1alpha1.AgentSession) apiv1.ContextAttribution {
	m := p.Spec.ManagedOutput
	if m == nil || source == nil || p.Spec.Origin != "ManagedOutputAssertion" || string(source.UID) != p.Spec.Source.UID {
		return a
	}
	u := sessionUsage(source)
	if u == nil || u.Version != protocol.UsageVersion || u.Source.SessionUID != p.Spec.Source.UID || u.Source.PodUID != p.Spec.Actor.PodUID ||
		apiv1.ManagedInvocation(u.Source.SessionUID, u.Source.PodUID) != m.Invocation || u.Source.Provider != a.Provider || u.Source.RequestedModel != a.RequestedModel {
		return a
	}
	out := a
	out.Usage = &apiv1.ContextUsage{ObservationID: u.ID, BillingBasis: u.BillingBasis, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		CacheReadInputTokens: u.CacheReadInputTokens, CacheCreationInputTokens: u.CacheCreationInputTokens, Cost: u.Cost}
	for _, model := range u.ServedModels {
		out.ObservedModels = append(out.ObservedModels, model.Model)
	}
	reported := func(ok bool) string {
		if ok {
			return protocol.UsageProviderReported
		}
		return protocol.UsageUnknown
	}
	out.ObservedModelState = reported(len(out.ObservedModels) > 0)
	out.UsageState = reported(u.InputTokens.State == protocol.UsageReported || u.OutputTokens.State == protocol.UsageReported || u.CacheReadInputTokens.State == protocol.UsageReported || u.CacheCreationInputTokens.State == protocol.UsageReported)
	out.CostState = reported(u.Cost.State == protocol.UsageReported)
	if !apiv1.ValidContextAttribution(out, true) {
		return a
	}
	return out
}
