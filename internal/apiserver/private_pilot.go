package apiserver

import (
	"context"
	"net/http"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
)

func (s *Server) createPrivatePilot(ctx context.Context, w http.ResponseWriter, c *caller, req apiv1.CreateSessionRequest) (int, any, error) {
	p := s.PrivatePilot
	slot, ok := p.Slot(req)
	if !ok || s.PrivateProjectTasks || !s.ManagedCodexTasks || !s.PrivateHistoryRetention || !s.RetainedCommonRules || s.Live == nil || s.Projects == nil || s.Projects.CloneOwner != "thaynes43" || s.Policy.SessionNamespace != "dev-agents" || s.SessionDirectory == nil || !slices.Contains(s.SessionDirectoryProjects, projectcanary.PilotProject) || !p.Open(s.now()) {
		return 0, nil, forbidden("sealed private pilot admission is unavailable or expired")
	}
	if c.kind != kindClient || c.parent != projectcanary.PilotCaller || s.callerHasTaskBudget(c) {
		return 0, nil, forbidden("sealed private pilot is unavailable to this caller")
	}
	s.createMu.Lock()
	defer s.createMu.Unlock()
	evidence, err := p.Resolve(ctx, s.Live, s.PrivateCanaryTemplates, s.now())
	if err != nil {
		return 0, nil, forbidden("sealed pilot authority is unavailable or changed")
	}
	// Both original slots fence config drift. Never replace a consumed identity,
	// even if it completed, is deleting, or another replica's response was lost.
	var found *v1alpha1.AgentSession
	for i := range 2 {
		var old v1alpha1.AgentSession
		err = s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: projectcanary.PilotSessionName(i)}, &old)
		if err == nil {
			if p.MatchSession(&old, i) != nil {
				return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "original pilot slot or policy is unconfirmed")
			}
			if i == slot {
				found = &old
			}
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return 0, nil, fromKubeError(err, "read retained pilot slot")
		}
	}
	if found != nil {
		w.Header().Set("Location", apiv1.SessionPath(found.Name))
		return http.StatusOK, view(found, false), nil
	}
	sess, err := s.newSession(ctx, req, c)
	if err != nil {
		return 0, nil, err
	}
	if sess.Spec.Workspace != nil || sess.Annotations[v1alpha1.AnnotationProjectSnapshot] != string(evidence.Snapshot) {
		return 0, nil, forbidden("pilot catalog changed before create")
	}
	sess.Name = projectcanary.PilotSessionName(slot)
	sess.Finalizers = append(sess.Finalizers, projectcanary.PilotFinalizer)
	sess.Annotations[projectcanary.PilotAnnotation] = p.Binding(slot)
	sess.Annotations[projectcanary.PilotPolicyAnnotation] = p.Record(evidence)
	sess.Spec.CommonRules, err = evidence.Common.Binding()
	if err != nil {
		return 0, nil, forbidden("pilot common rules are unconfirmed")
	}
	sess.Spec.SessionDirectory = &v1alpha1.SessionDirectoryEnrollment{Version: 1, Project: projectcanary.PilotProject}
	if err = checkAgentd(sess); err != nil {
		return 0, nil, err
	}
	// Re-read every authority and clock immediately before the only create.
	latest, err := p.Resolve(ctx, s.Live, s.PrivateCanaryTemplates, s.now())
	if err != nil || string(latest.Snapshot) != string(evidence.Snapshot) || latest.CatalogUID != evidence.CatalogUID || latest.CatalogResourceVersion != evidence.CatalogResourceVersion || evidence.Common.Confirm(ctx, s.Live) != nil || !p.Open(s.now()) {
		return 0, nil, forbidden("pilot admission expired or authority changed before create")
	}
	createErr := s.Client.Create(ctx, sess)
	var original v1alpha1.AgentSession
	if err = s.Live.Get(ctx, client.ObjectKeyFromObject(sess), &original); err != nil {
		if createErr != nil {
			return 0, nil, fromKubeError(createErr, "create retained pilot slot")
		}
		return 0, nil, fromKubeError(err, "confirm retained pilot slot")
	}
	if p.MatchSession(&original, slot) != nil || (sess.UID != "" && original.UID != sess.UID) {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "original pilot identity is unconfirmed")
	}
	status := http.StatusOK
	if createErr == nil {
		status = http.StatusCreated
	}
	w.Header().Set("Location", apiv1.SessionPath(original.Name))
	return status, view(&original, false), nil
}
