package broker

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// delegateIdentityHolds checks the operator-recorded actor against live objects
// again, both at Decide's status write and before materializing or retaining a
// lease. A recorded decision cannot outlive its pod or its sole session holder.
// Launch authorization is the operator's S4 rule; its protected admission record
// must still name this account here.
func (b *Broker) delegateIdentityHolds(ctx context.Context, d MaintenanceDecision) error {
	ref := strings.TrimPrefix(d.By, DelegateApprover)
	ns, account, ok := strings.Cut(ref, "/")
	if !ok || d.Delegate == nil || (d.Delegate.PodUID == "" || d.Delegate.PodName == "") {
		return fmt.Errorf("%w: missing delegate identity", ErrRefused)
	}
	var live corev1.Pod
	if err := b.APIReader.Get(ctx, types.NamespacedName{Namespace: ns, Name: d.Delegate.PodName}, &live); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			return fmt.Errorf("%w: delegate pod is absent or forbidden: %w", ErrRefused, err)
		}
		return fmt.Errorf("read delegate pod: %w", err)
	}
	pod := &live
	if string(pod.UID) != d.Delegate.PodUID || pod.Spec.ServiceAccountName != account || !pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning {
		return fmt.Errorf("%w: delegate pod is replaced, terminating or not Running", ErrRefused)
	}
	if d.Delegate.Form == 1 && (d.Delegate.HostID == "" || pod.Annotations[v1alpha1.LabelPrefix+"coordinator-host"] != d.Delegate.HostID) {
		return fmt.Errorf("%w: delegate host binding changed", ErrRefused)
	}
	owner := metav1.GetControllerOf(pod)
	id := d.Delegate
	if id.Form != 1 && id.Form != 2 {
		return fmt.Errorf("%w: unknown delegate form", ErrRefused)
	}
	if id.Session == "" {
		if id.Form != 1 || id.SessionUID != "" || (owner != nil && owner.Kind == "AgentSession") {
			return fmt.Errorf("%w: delegate pod controller changed", ErrRefused)
		}
		return nil
	}
	var sess v1alpha1.AgentSession
	if err := b.APIReader.Get(ctx, types.NamespacedName{Namespace: ns, Name: id.Session}, &sess); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			return fmt.Errorf("%w: delegate session is absent or forbidden: %w", ErrRefused, err)
		}
		return fmt.Errorf("read delegate session: %w", err)
	}
	if string(sess.UID) != id.SessionUID || !sess.DeletionTimestamp.IsZero() || sess.Status.Phase != v1alpha1.PhaseRunning ||
		sess.Status.ArchivedAt != nil || (sess.Spec.OperatingMode != "" && sess.Spec.OperatingMode != v1alpha1.OperatingModeRunning) ||
		owner == nil || owner.APIVersion != v1alpha1.GroupVersion.String() || owner.Kind != "AgentSession" || owner.Name != sess.Name || !metav1.IsControlledBy(pod, &sess) {
		return fmt.Errorf("%w: delegate session is replaced, inactive or no longer controls its pod", ErrRefused)
	}
	if id.Form == 1 {
		return nil
	}
	if ns != b.SessionNamespace || !templates.IsCoordinatorServiceAccount(account) || sess.Labels[templates.CoordinatorIdentityLabel] != account {
		return fmt.Errorf("%w: delegate session admission no longer names its account", ErrRefused)
	}
	var holders v1alpha1.AgentSessionList
	if err := b.APIReader.List(ctx, &holders, client.InNamespace(ns), client.MatchingLabels{templates.CoordinatorIdentityLabel: account}); err != nil {
		return fmt.Errorf("read delegate account holders: %w", err)
	}
	for i := range holders.Items {
		o := &holders.Items[i]
		if o.UID == sess.UID || o.Status.Phase == v1alpha1.PhaseArchived || o.Status.Phase == v1alpha1.PhaseFailed {
			continue
		}
		// S4 releases a suspended holder only after both the desired mode and
		// phase say Suspended and its controlled pod is absent or has ended.
		if o.Status.Phase == v1alpha1.PhaseSuspended && o.Spec.OperatingMode == v1alpha1.OperatingModeSuspended {
			var p corev1.Pod
			err := b.APIReader.Get(ctx, types.NamespacedName{Namespace: ns, Name: o.Name}, &p)
			if apierrors.IsNotFound(err) {
				continue
			}
			if apierrors.IsForbidden(err) {
				return fmt.Errorf("%w: read other delegate holder pod: %w", ErrRefused, err)
			}
			if err != nil {
				return fmt.Errorf("read other delegate holder pod: %w", err)
			}
			if !metav1.IsControlledBy(&p, o) || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
				continue
			}
		}
		return fmt.Errorf("%w: another session holds the delegate account", ErrRefused)
	}
	return nil
}

// delegateGrantHolds repeats separation and reach on fresh requester state too:
// a reparented or replaced requester cannot use a previously recorded decision.
func (b *Broker) delegateGrantHolds(ctx context.Context, g *v1alpha1.AccessGrant, d MaintenanceDecision) error {
	if err := b.delegateIdentityHolds(ctx, d); err != nil {
		return err
	}
	var requester v1alpha1.AgentSession
	if err := b.APIReader.Get(ctx, types.NamespacedName{Namespace: b.SessionNamespace, Name: g.Spec.Requester.Session}, &requester); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: requester session is absent", ErrRefused)
		}
		return fmt.Errorf("read requester session: %w", err)
	}
	if requester.UID != g.Spec.Requester.SessionUID {
		return fmt.Errorf("%w: requester session was replaced", ErrRefused)
	}
	if err := delegateSeparation(g, d.Delegate, d.By, b.sessionPod(ctx, &requester, false)); err != nil {
		return fmt.Errorf("%w: %w", ErrRefused, err)
	}
	if err := b.delegateReach(ctx, d, &requester); err != nil {
		return err
	}
	return nil
}
