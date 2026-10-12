package apiserver

import (
	"context"
	"errors"
	"net/http"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/commonrules"
	"github.com/thaynes43/dev-env/internal/projectcanary"
)

func (s *Server) createProjectCanary(ctx context.Context, w http.ResponseWriter, c *caller, id string) (int, any, error) {
	record, ok := s.PrivateProjectCanaries.Lookup(id)
	if !ok || s.PrivateProjectTasks || !s.PrivateHistoryRetention || s.Projects == nil || s.Projects.Validate() != nil || s.Live == nil || s.PrivateCanaryTemplates.Name == "" || s.PrivateCanaryTemplates.Namespace == "" || record.Provider == "codex" && !s.ManagedCodexTasks {
		return 0, nil, invalid(fieldError("canaryID", "private managed canary admission or its prerequisites are not enabled"))
	}
	if s.RetainedCommonRules != (record.CommonRevision != "") {
		return 0, nil, invalid(fieldError("canaryID", "retained common rule authority is disabled or mismatched"))
	}
	if c.kind != kindClient || c.parent != record.Client || record.Client == s.Policy.SessionNamespace+"/"+s.Policy.SessionServiceAccount || s.callerHasTaskBudget(c) {
		return 0, nil, forbidden("the fixed canary is unavailable to this caller")
	}
	s.createMu.Lock()
	defer s.createMu.Unlock()
	key := types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: record.SessionName()}
	var existing v1alpha1.AgentSession
	err := s.Live.Get(ctx, key, &existing)
	if err == nil {
		return s.returnProjectCanary(w, &existing, record)
	}
	if !apierrors.IsNotFound(err) {
		return 0, nil, fromKubeError(err, "read original canary")
	}
	evidence, err := projectcanary.ResolveCatalog(ctx, s.Live, s.Projects.CloneOwner, record)
	if errors.Is(err, projectcanary.ErrPromptRepeatsRules) {
		return 0, nil, invalid(fieldError("canaryID", "the reviewed canary prompt repeats its bound project rules"))
	}
	if err != nil {
		return 0, nil, invalid(fieldError("canaryID", "accepted catalog authority is unavailable or changed"))
	}
	t, err := projectcanary.ReadTemplates(ctx, s.Live, s.PrivateCanaryTemplates)
	if err != nil || record.ValidateTemplate(t, s.Projects.CloneOwner) != nil {
		return 0, nil, invalid(fieldError("canaryID", "reviewed private template is unavailable or changed"))
	}
	req := apiv1.CreateSessionRequest{Project: record.Project, Repo: record.Repository, Agent: record.Provider, Mode: "task", Model: record.Model, Effort: record.Effort, Prompt: record.Prompt, Size: record.Size, Profile: record.Profile, IdempotencyKey: record.IdempotencyKey, Limits: &apiv1.Limits{Timeout: record.Timeout, MaxTurns: record.MaxTurns}}
	sess, err := s.newSession(ctx, req, c)
	if err != nil {
		return 0, nil, err
	}
	if sess.Spec.Workspace != nil || sess.Annotations[v1alpha1.AnnotationProjectSnapshot] != string(evidence.Snapshot) {
		return 0, nil, invalid(fieldError("canaryID", "accepted source changed before canary create"))
	}
	sess.Name = key.Name
	sess.Spec.ProjectCanary = record.Binding(evidence, s.Projects.CloneOwner)
	if s.SessionDirectory != nil && slices.Contains(s.SessionDirectoryProjects, record.Project) {
		sess.Spec.SessionDirectory = &v1alpha1.SessionDirectoryEnrollment{Version: 1, Project: record.Project}
	}
	var common commonrules.Snapshot
	if s.RetainedCommonRules {
		common, err = commonrules.Resolve(ctx, s.Live, commonrules.Digest(evidence.Snapshot))
		if err != nil || common.CommonRevision != record.CommonRevision {
			return 0, nil, invalid(fieldError("canaryID", "accepted common sources changed or are unavailable"))
		}
		// A marker the prompt supplies would make the evidence self-fulfilling.
		if record.CheckCommon(common) != nil {
			return 0, nil, invalid(fieldError("canaryID", "the reviewed canary prompt repeats the accepted common or header rules"))
		}
		sess.Spec.CommonRules, err = common.Binding()
		if err != nil {
			return 0, nil, invalid(fieldError("canaryID", "common rule snapshot is unconfirmed"))
		}
	}
	// This separate permanent guard closes delete-before-first-reconcile. The
	// existing private-history freshness proof still adds its own finalizer.
	sess.Finalizers = append(sess.Finalizers, v1alpha1.ProjectCanaryFinalizer)
	sess.Labels[v1alpha1.LabelProjectCanary] = record.ID
	sess.Annotations[v1alpha1.AnnotationRequestHash] = requestHash(apiv1.CreateSessionRequest{CanaryID: id})
	if record.MatchSession(sess, s.Projects.CloneOwner) != nil {
		return 0, nil, invalid(fieldError("canaryID", "original canary binding is unconfirmed"))
	}
	if err := checkAgentd(sess); err != nil {
		return 0, nil, err
	}
	latest, err := projectcanary.ResolveCatalog(ctx, s.Live, s.Projects.CloneOwner, record)
	if err != nil || latest.UID != evidence.UID || latest.ResourceVersion != evidence.ResourceVersion || string(latest.Snapshot) != string(evidence.Snapshot) {
		return 0, nil, invalid(fieldError("canaryID", "accepted catalog changed before create"))
	}
	t, err = projectcanary.ReadTemplates(ctx, s.Live, s.PrivateCanaryTemplates)
	if err != nil || record.ValidateTemplate(t, s.Projects.CloneOwner) != nil {
		return 0, nil, invalid(fieldError("canaryID", "reviewed template changed before create"))
	}
	if s.RetainedCommonRules && common.Confirm(ctx, s.Live) != nil {
		return 0, nil, invalid(fieldError("canaryID", "accepted common authority changed before create"))
	}
	createErr := s.Client.Create(ctx, sess)
	// A lost acknowledgement or another replica's atomic create is resolved
	// only through the original exact name; no generated suffix or new key.
	if err := s.Live.Get(ctx, key, &existing); err != nil {
		if createErr != nil {
			return 0, nil, fromKubeError(createErr, "create retained canary")
		}
		return 0, nil, fromKubeError(err, "confirm retained canary")
	}
	if sess.UID != "" && existing.UID != sess.UID {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "original canary identity is unconfirmed")
	}
	status, value, err := s.returnProjectCanary(w, &existing, record)
	if err == nil && createErr == nil {
		status = http.StatusCreated
	}
	return status, value, err
}

func (s *Server) returnProjectCanary(w http.ResponseWriter, existing *v1alpha1.AgentSession, record projectcanary.Record) (int, any, error) {
	if existing.UID == "" || existing.Namespace != s.Policy.SessionNamespace || record.MatchSession(existing, s.Projects.CloneOwner) != nil || existing.Annotations[v1alpha1.AnnotationRequestHash] != requestHash(apiv1.CreateSessionRequest{CanaryID: record.ID}) {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "the retained canary conflicts with its original policy or identity")
	}
	w.Header().Set("Location", apiv1.SessionPath(existing.Name))
	return http.StatusOK, view(existing, false), nil
}
