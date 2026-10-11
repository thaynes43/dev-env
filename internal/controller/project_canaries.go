package controller

import (
	"context"
	"errors"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/templates"
)

func (r *Reconciler) validateProjectCanary(ctx context.Context, s *v1alpha1.AgentSession, t *templates.Templates) error {
	if s.Spec.ProjectCanary == nil {
		return nil
	}
	record, ok := r.PrivateProjectCanaries.Lookup(s.Spec.ProjectCanary.ID)
	if !ok || !r.PrivateHistoryRetention || r.APIReader == nil || r.APIURL == "" || record.Client == s.Namespace+"/"+ServiceAccountName || budgetBound(s) || record.Provider == "codex" && !r.ManagedCodexTasks || record.MatchSession(s, r.PrivateProjectCloneOwner) != nil || record.ValidateTemplate(t, r.PrivateProjectCloneOwner) != nil || !privateHistoryProtected(s) {
		return projectcanary.ErrRefused
	}
	var fresh v1alpha1.AgentSession
	if r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &fresh) != nil || fresh.UID == "" || fresh.UID != s.UID || fresh.Generation != s.Generation || !fresh.DeletionTimestamp.IsZero() || wantsPodGone(&fresh) || record.MatchSession(&fresh, r.PrivateProjectCloneOwner) != nil || !privateHistoryProtected(&fresh) || budgetBound(&fresh) {
		return projectcanary.ErrRefused
	}
	evidence, err := projectcanary.ResolveCatalog(ctx, r.APIReader, r.PrivateProjectCloneOwner, record)
	b := fresh.Spec.ProjectCanary
	if err != nil || evidence.UID != b.CatalogUID || evidence.ResourceVersion != b.CatalogResourceVersion || string(evidence.Snapshot) != fresh.Annotations[v1alpha1.AnnotationProjectSnapshot] {
		return projectcanary.ErrRefused
	}
	current, err := projectcanary.ReadTemplates(ctx, r.APIReader, r.Templates)
	if err != nil || record.ValidateTemplate(current, r.PrivateProjectCloneOwner) != nil {
		return projectcanary.ErrRefused
	}
	return nil
}

func validateProjectCanaryPod(s *v1alpha1.AgentSession, t *templates.Templates, podShape bool) error {
	if s.Spec.ProjectCanary == nil {
		return nil
	}
	record, err := projectcanary.BoundRecord(s)
	if err != nil || record.ValidateTemplate(t, s.Spec.ProjectCanary.CloneOwner) != nil || !podShape {
		return errors.New("private project canary Pod shape or retained binding is unconfirmed")
	}
	return nil
}
