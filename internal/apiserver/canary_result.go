package apiserver

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/commonrules"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func canaryResultDenied() error {
	return forbidden("the original fixed canary publication is unavailable to this caller")
}

// Selection is checked before authentication; no invalid verb or query can
// become a create, source lookup or credential operation.
func (s *Server) canaryResultRoute() http.Handler {
	protected := s.serve(route{identityGuard: func(id Identity, r *http.Request) error {
		return s.canaryResultIdentity(id, r.PathValue("id"))
	}, methods: map[string]handler{http.MethodGet: s.getCanaryResult}})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if _, _, err := canaryResultQuery(r); err != nil {
				writeError(w, err)
				return
			}
		}
		protected.ServeHTTP(w, r)
	})
}

// Reject every non-initiating or ambiguously configured identity before the
// generic resolver can read a Session or coordinator Pod. Authentication itself
// remains the existing ordinary TokenReview path.
func (s *Server) canaryResultIdentity(id Identity, canaryID string) error {
	record, ok := s.PrivateProjectCanaries.Lookup(canaryID)
	ref := id.serviceAccountRef()
	if !ok || ref == "" || ref != record.Client || !slices.Contains(s.Policy.Clients, ref) || ref == s.Policy.Human || ref == s.Policy.SessionNamespace+"/"+s.Policy.SessionServiceAccount {
		return canaryResultDenied()
	}
	for _, host := range s.Policy.Coordinators {
		if host.ServiceAccount == ref {
			return canaryResultDenied()
		}
	}
	return nil
}

func canaryResultQuery(r *http.Request) (string, string, *apiError) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(r.URL.RawQuery) > 1024 || !projectcatalog.ValidName(r.PathValue("id")) || len(q) != 2 || len(q["sessionUID"]) != 1 || len(q["context"]) != 1 || !apiv1.ValidContextUID(q.Get("sessionUID")) || !apiv1.ValidContextName(q.Get("context")) {
		return "", "", badRequest("result requires one fixed ID, original sessionUID and named context")
	}
	return q.Get("sessionUID"), q.Get("context"), nil
}

// This read authority is deliberately separate from generic context ACLs. It
// never resolves a source Pod, creates a Session or captures replacement inputs.
func (s *Server) canaryResultAuthority(ctx context.Context, c *caller, id, uid string) (*v1alpha1.AgentSession, projectcanary.Record, error) {
	record, ok := s.PrivateProjectCanaries.Lookup(id)
	if c == nil || !ok || !record.CaptureOutput || s.PrivateProjectTasks || !s.PrivateHistoryRetention || s.Projects == nil || s.Projects.Validate() != nil || s.Live == nil || s.PublishedContexts == nil || s.PublishedContexts.Namespace != s.Projects.Key.Namespace || !slices.Contains(s.ContextReadProjects, record.Project) || s.PrivateCanaryTemplates.Name == "" || s.PrivateCanaryTemplates.Namespace == "" || record.Provider == "codex" && !s.ManagedCodexTasks || s.RetainedCommonRules != (record.CommonRevision != "") {
		return nil, record, canaryResultDenied()
	}
	if s.canaryResultIdentity(c.identity, id) != nil {
		return nil, record, canaryResultDenied()
	}
	fresh, err := s.resolveCaller(ctx, c.identity)
	if err != nil || fresh.kind != kindClient || c.kind != kindClient || fresh.parent != record.Client || c.parent != fresh.parent || c.identity.serviceAccountRef() != record.Client || record.Client == s.Policy.SessionNamespace+"/"+s.Policy.SessionServiceAccount || s.callerHasTaskBudget(fresh) {
		return nil, record, canaryResultDenied()
	}
	var original v1alpha1.AgentSession
	if s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: record.SessionName()}, &original) != nil || string(original.UID) != uid || original.Namespace != s.Policy.SessionNamespace || original.ResourceVersion == "" || original.Generation < 1 || original.CreationTimestamp.IsZero() || record.MatchSession(&original, s.Projects.CloneOwner) != nil || original.Annotations[v1alpha1.AnnotationRequestHash] != requestHash(apiv1.CreateSessionRequest{CanaryID: id}) {
		return nil, record, canaryResultDenied()
	}
	// MatchSession binds the fixed ID/idempotency label from which the Directory
	// task key derives, plus retained policy, common, template and image revisions.
	evidence, err := projectcanary.ResolveCatalog(ctx, s.Live, s.Projects.CloneOwner, record)
	b := original.Spec.ProjectCanary
	if err != nil || evidence.UID != b.CatalogUID || evidence.ResourceVersion != b.CatalogResourceVersion || string(evidence.Snapshot) != original.Annotations[v1alpha1.AnnotationProjectSnapshot] {
		return nil, record, canaryResultDenied()
	}
	t, err := projectcanary.ReadTemplates(ctx, s.Live, s.PrivateCanaryTemplates)
	if err != nil || record.ValidateTemplate(t, s.Projects.CloneOwner) != nil {
		return nil, record, canaryResultDenied()
	}
	if record.CommonRevision != "" {
		// MatchSession binds the record's CommonRevision to the retained
		// snapshot. Live sources must still carry the bound bytes, but their
		// UID/resourceVersion are provenance, not read authority: another key
		// of the same ConfigMap or an identical re-create must not strand the
		// retained result (DESIGN-022 amendment; Create and the first executor
		// keep the strict Confirm).
		common, err := commonrules.BoundSession(&original)
		if err != nil || record.CheckCommon(common) != nil || common.ConfirmContent(ctx, s.Live) != nil {
			return nil, record, canaryResultDenied()
		}
	}
	return &original, record, nil
}

func (s *Server) getCanaryResult(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	uid, name, selectionErr := canaryResultQuery(r)
	if selectionErr != nil {
		return 0, nil, selectionErr
	}
	id := r.PathValue("id")
	original, record, err := s.canaryResultAuthority(ctx, c, id, uid)
	if err != nil {
		return 0, nil, err
	}
	p, err := s.PublishedContexts.GetForProject(ctx, name, record.Project)
	if err != nil || !s.canaryResultPublication(original, record, p) {
		return 0, nil, canaryResultDenied()
	}
	// Recheck caller, original UID and binding, the publication against the fresh
	// status and named revision authorities after the payload read. A status-only
	// write is no refusal; a changed or ambiguous binding never returns the text.
	fresh, _, err := s.canaryResultAuthority(ctx, c, id, uid)
	if err != nil || !sameSessionBinding(fresh, original) || !s.canaryResultPublication(fresh, record, p) {
		return 0, nil, canaryResultDenied()
	}
	return http.StatusOK, apiv1.CanaryResult{CanaryID: id, SessionUID: uid, Context: apiv1.PublishedContext{Metadata: contextMetadata(p), Text: p.Spec.Text}}, nil
}

func (s *Server) canaryResultPublication(original *v1alpha1.AgentSession, record projectcanary.Record, p *coord.PublishedContext) bool {
	if p == nil || original.Status.PrivateHistory == nil || original.Status.PrivateHistory.SessionUID != string(original.UID) || original.Status.PrivateHistory.LastExecutor == nil {
		return false
	}
	h := original.Status.PrivateHistory.LastExecutor
	v := p.Spec
	m := v.ManagedOutput
	snapshot, err := claimSnapshot(original)
	return err == nil && m != nil && v.Identity.SessionUID == string(original.UID) && v.Source.Namespace == original.Namespace && v.Source.Name == original.Name && v.Source.UID == string(original.UID) && v.SourceGeneration == publishedGeneration(original) && v.SourceParent == record.Client && v.Project == record.Project && v.Repository == record.Repository &&
		v.Actor.Kind == "session" && v.Actor.ServiceAccount == s.Policy.SessionNamespace+"/"+s.Policy.SessionServiceAccount && v.Actor.PodName == original.Name && h.PodName == original.Name && v.Actor.PodUID == h.PodUID && apiv1.ValidContextUID(h.PodUID) &&
		v.Attribution.Provider == record.Provider && v.Attribution.RequestedModel == record.Model && v.Provenance.CatalogRevision == snapshot.CatalogRevision() && v.Provenance.RulesRevision == snapshot.RulesRevision() && v.Provenance.GitHub == strings.ToLower(snapshot.Selected().GitHub) && v.Provenance.DefaultBranch == snapshot.Selected().DefaultBranch &&
		v.Origin == "ManagedOutputAssertion" && apiv1.ValidManagedOutput(&apiv1.ManagedOutputAssertion{Invocation: m.Invocation, Ordinal: m.Ordinal, ConversationID: m.ConversationID}) && m.Invocation == apiv1.ManagedInvocation(string(original.UID), h.PodUID) && v.Identity.Key == apiv1.ManagedOutputKey(m.Invocation, m.Ordinal)
}

// publishedGeneration is the generation a publication from this Session must
// carry when no spec mutation has happened since it was made. A finalizer-held
// DELETE (a reap of a retained original) sets deletionTimestamp and bumps
// metadata.generation exactly once, and publishers refuse a deleting source
// (claimCaller), so every publication predates that bump. Any spec mutation
// before or after the reap, such as a suspend, still moves the generation past
// this value and holds the read.
func publishedGeneration(s *v1alpha1.AgentSession) int64 {
	if s.DeletionTimestamp.IsZero() {
		return s.Generation
	}
	return s.Generation - 1
}
