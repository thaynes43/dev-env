package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/commonrules"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/templates"
)

// errCanaryUnobserved is a canary refusal caused by a failed read, not by a
// changed authority. After the executor latch it is a known non-attempt
// (DESIGN-019): ensure() returned before any Pod create for the intent.
var errCanaryUnobserved = fmt.Errorf("%w: an uncached canary authority read failed", projectcanary.ErrRefused)

// readTracker notes whether any read failed for a reason other than NotFound.
// A missing object is an authority change; any other error says nothing about
// the authority, only that it could not be observed.
type readTracker struct {
	client.Reader
	failed bool
}

func (t *readTracker) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return t.note(t.Reader.Get(ctx, key, obj, opts...))
}

func (t *readTracker) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return t.note(t.Reader.List(ctx, list, opts...))
}

func (t *readTracker) note(err error) error {
	if err != nil && !apierrors.IsNotFound(err) {
		t.failed = true
	}
	return err
}

func (r *Reconciler) validateProjectCanary(ctx context.Context, s *v1alpha1.AgentSession, t *templates.Templates) error {
	if projectcanary.IsPilot(s) {
		return r.validatePrivatePilot(ctx, s, t)
	}
	if s.Spec.ProjectCanary == nil {
		return nil
	}
	record, ok := r.PrivateProjectCanaries.Lookup(s.Spec.ProjectCanary.ID)
	if !ok || !r.PrivateHistoryRetention || r.APIReader == nil || r.APIURL == "" || record.Client == s.Namespace+"/"+ServiceAccountName || budgetBound(s) || record.Provider == "codex" && !r.ManagedCodexTasks || record.MatchSession(s, r.PrivateProjectCloneOwner) != nil || record.ValidateTemplate(t, r.PrivateProjectCloneOwner) != nil || !privateHistoryProtected(s) {
		return projectcanary.ErrRefused
	}
	if r.RetainedCommonRules != (record.CommonRevision != "") {
		return projectcanary.ErrRefused
	}
	reader := &readTracker{Reader: r.APIReader}
	refuse := func() error {
		if reader.failed {
			return errCanaryUnobserved
		}
		return projectcanary.ErrRefused
	}
	var fresh v1alpha1.AgentSession
	if reader.Get(ctx, client.ObjectKeyFromObject(s), &fresh) != nil || fresh.UID == "" || fresh.UID != s.UID || fresh.Generation != s.Generation || !fresh.DeletionTimestamp.IsZero() || wantsPodGone(&fresh) || record.MatchSession(&fresh, r.PrivateProjectCloneOwner) != nil || !privateHistoryProtected(&fresh) || budgetBound(&fresh) {
		return refuse()
	}
	evidence, err := projectcanary.ResolveCatalog(ctx, reader, r.PrivateProjectCloneOwner, record)
	b := fresh.Spec.ProjectCanary
	if err != nil || evidence.UID != b.CatalogUID || evidence.ResourceVersion != b.CatalogResourceVersion || string(evidence.Snapshot) != fresh.Annotations[v1alpha1.AnnotationProjectSnapshot] {
		return refuse()
	}
	if r.RetainedCommonRules {
		common, err := commonrules.BoundSession(&fresh)
		if err != nil || record.CheckCommon(common) != nil || common.Confirm(ctx, reader) != nil {
			return refuse()
		}
	}
	current, err := projectcanary.ReadTemplates(ctx, reader, r.Templates)
	if err != nil || record.ValidateTemplate(current, r.PrivateProjectCloneOwner) != nil {
		return refuse()
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

// Final rendered executor proof is independent of template admission. The only
// persistent storage a managed canary may receive is its original own HOME.
func validateProjectCanaryExecutor(s *v1alpha1.AgentSession, pod *corev1.Pod) error {
	if s.Spec.ProjectCanary == nil {
		return nil
	}
	if pod == nil || len(pod.Spec.Containers) != 1 || len(pod.Spec.InitContainers) != 0 || len(pod.Spec.EphemeralContainers) != 0 || pod.Spec.RestartPolicy != corev1.RestartPolicyNever || pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != projectcanary.ActiveDeadlineSeconds || pod.Spec.TerminationGracePeriodSeconds == nil || *pod.Spec.TerminationGracePeriodSeconds != projectcanary.TerminationGraceSeconds || pod.Annotations["k8tz.io/inject"] != "false" {
		return projectcanary.ErrRefused
	}
	homeVolumes := 0
	for _, v := range pod.Spec.Volumes {
		if v.Name == "shared" || v.Name == "workspace" {
			return projectcanary.ErrRefused
		}
		if pvc := v.PersistentVolumeClaim; pvc != nil {
			if v.Name != "home" || pvc.ClaimName != HomeClaimName(s.Name) || pvc.ReadOnly {
				return projectcanary.ErrRefused
			}
			homeVolumes++
		}
	}
	homeMounts := 0
	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		if m.Name == "home" {
			if m.MountPath != templates.HomePath || m.SubPath != "" || m.SubPathExpr != "" || m.ReadOnly {
				return projectcanary.ErrRefused
			}
			homeMounts++
			continue
		}
		if m.Name == "shared" || m.Name == "workspace" || canaryMountOverlaps(m.MountPath, templates.SharedPath) || canaryMountOverlaps(m.MountPath, templates.WorkspaceMetadataPath) {
			return projectcanary.ErrRefused
		}
	}
	if homeVolumes != 1 || homeMounts != 1 {
		return projectcanary.ErrRefused
	}
	return nil
}

func canaryMountOverlaps(a, b string) bool {
	return a == b || a == "/" || b == "/" || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// A pending pilot is never a legacy fallback when its opt-in policy disappears.
// ensure calls this before resource allocation and again before Pod Create;
// already-present pods are preserved by ensure's first guard.
func (r *Reconciler) validatePrivatePilot(ctx context.Context, s *v1alpha1.AgentSession, t *templates.Templates) error {
	p := r.PrivatePilot
	if p == nil || !r.ManagedCodexTasks || !r.PrivateHistoryRetention || !r.RetainedCommonRules || r.APIReader == nil || r.APIURL == "" || r.PrivateProjectCloneOwner != "thaynes43" || budgetBound(s) || s.Spec.ProjectCanary != nil {
		return projectcanary.ErrRefused
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	slot := 0
	if s.Name == projectcanary.PilotSessionName(1) {
		slot = 1
	}
	if p.MatchSession(s, slot) != nil || p.ValidateTemplate(t) != nil {
		return projectcanary.ErrRefused
	}
	var fresh v1alpha1.AgentSession
	if r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &fresh) != nil || fresh.UID != s.UID || fresh.Generation != s.Generation || wantsPodGone(&fresh) || p.MatchSession(&fresh, slot) != nil {
		return projectcanary.ErrRefused
	}
	evidence, err := p.Resolve(ctx, r.APIReader, r.Templates, now)
	if err != nil || string(evidence.Snapshot) != fresh.Annotations[v1alpha1.AnnotationProjectSnapshot] {
		return projectcanary.ErrRefused
	}
	record, err := projectcanary.BoundPilot(&fresh)
	if err != nil || evidence.CatalogUID != record.CatalogUID || evidence.CatalogResourceVersion != record.CatalogResourceVersion {
		return projectcanary.ErrRefused
	}
	common, err := commonrules.BoundSession(&fresh)
	if err != nil || common.Confirm(ctx, r.APIReader) != nil {
		return projectcanary.ErrRefused
	}
	now = time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	if !p.Open(now) {
		return projectcanary.ErrRefused
	}
	return nil
}
