package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/controller"
)

// MaintenancePod is broker-owned, never an agent-provided PodSpec. It has no API
// token and no network; only the reviewed operator exec route can reach it.
func MaintenancePod(g *v1alpha1.AccessGrant, now time.Time) *corev1.Pod {
	m := g.Spec.Maintenance
	caps := []corev1.Capability{}
	for _, c := range m.Capabilities {
		caps = append(caps, corev1.Capability(c))
	}
	labels := map[string]string{MaintenanceLabel: string(g.UID), v1alpha1.LabelGrant: g.Name}
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: g.Namespace, Name: g.Name, Labels: labels, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(g, v1alpha1.GroupVersion.WithKind("AccessGrant"))}}, Spec: corev1.PodSpec{
		NodeName: m.Node, ServiceAccountName: "dev-env-maintenance", AutomountServiceAccountToken: ptr.To(false), RestartPolicy: corev1.RestartPolicyNever,
		ActiveDeadlineSeconds: ptr.To(max(int64(1), int64(g.Status.ExpiresAt.Sub(now)/time.Second))), TerminationGracePeriodSeconds: ptr.To(int64(10)), EnableServiceLinks: ptr.To(false),
		SecurityContext: &corev1.PodSecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Containers: []corev1.Container{{Name: "maintenance", Image: m.Image, Command: []string{"/bin/sleep", "infinity"},
			SecurityContext: &corev1.SecurityContext{RunAsUser: ptr.To(int64(0)), Privileged: ptr.To(false), AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: caps}},
			Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: *resource.NewMilliQuantity(m.CPUMillis, resource.DecimalSI), corev1.ResourceMemory: resource.MustParse(strconv.FormatInt(m.MemoryMiB, 10) + "Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: *resource.NewMilliQuantity(m.CPUMillis, resource.DecimalSI), corev1.ResourceMemory: resource.MustParse(strconv.FormatInt(m.MemoryMiB, 10) + "Mi")}},
		}},
	}}
	if m.RuntimeProfile == "nvidia-utility" {
		p.Spec.RuntimeClassName = ptr.To("nvidia")
		p.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "NVIDIA_VISIBLE_DEVICES", Value: m.Resource}, {Name: "NVIDIA_DRIVER_CAPABILITIES", Value: "utility"}}
	}
	for _, group := range []struct {
		paths    []string
		readOnly bool
		kind     corev1.HostPathType
	}{{m.Devices, false, corev1.HostPathCharDev}, {m.ReadOnlyMounts, true, corev1.HostPathFile}} {
		for _, host := range group.paths {
			name := fmt.Sprintf("bound-%d", len(p.Spec.Volumes))
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: host, Type: ptr.To(group.kind)}}})
			p.Spec.Containers[0].VolumeMounts = append(p.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: name, MountPath: host, ReadOnly: group.readOnly})
		}
	}
	return p
}

func (b *Broker) maintenancePod(ctx context.Context, g *v1alpha1.AccessGrant) (*corev1.Pod, error) {
	var p corev1.Pod
	if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(g), &p); err != nil {
		return nil, err
	}
	owner := metav1.GetControllerOf(&p)
	if p.Labels[MaintenanceLabel] != string(g.UID) || owner == nil || owner.UID != g.UID || owner.Kind != "AccessGrant" || owner.APIVersion != v1alpha1.GroupVersion.String() || owner.Name != g.Name || (g.Status.Maintenance != nil && g.Status.Maintenance.PodUID != "" && g.Status.Maintenance.PodUID != string(p.UID)) {
		return nil, errors.New("executor ownership or UID changed")
	}
	return &p, nil
}

func (b *Broker) activeMaintenance(ctx context.Context, g *v1alpha1.AccessGrant, now metav1.Time) (ctrl.Result, error) {
	if !b.EnableMaintenanceGrants {
		return b.end(ctx, g, v1alpha1.GrantReleased, "", "maintenance disabled", now)
	}
	if err := CheckMaintenanceIdentities(ctx, b.APIReader, b.SessionNamespace, g); err != nil {
		return b.end(ctx, g, v1alpha1.GrantReleased, "", "maintenance identity or target no longer valid", now)
	}
	// A policy approval (ADR-013) is execution authority only while it holds.
	if err := b.attestedMaintenanceHolds(ctx, g); err != nil {
		if p := (permanent{}); errors.As(err, &p) {
			return b.end(ctx, g, v1alpha1.GrantReleased, "", "maintenance approval no longer holds: "+err.Error(), now)
		}
		return ctrl.Result{}, err
	}
	// So is a delegate approval (DESIGN-042 6.2).
	if err := b.delegatedHolds(ctx, g); err != nil {
		if p := (permanent{}); errors.As(err, &p) {
			return b.end(ctx, g, v1alpha1.GrantReleased, "", "maintenance approval no longer holds: "+err.Error(), now)
		}
		return ctrl.Result{}, err
	}
	// Deny network before any executor is made. RBAC/admission review is held.
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: g.Namespace, Name: g.Name, Labels: grantLabels(g), OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(g, v1alpha1.GroupVersion.WithKind("AccessGrant"))}}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{MaintenanceLabel: string(g.UID)}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}}}
	var have networkingv1.NetworkPolicy
	if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(g), &have); apierrors.IsNotFound(err) {
		if err := b.Client.Create(ctx, policy); err != nil {
			return ctrl.Result{}, err
		}
	} else if err != nil {
		return ctrl.Result{}, err
	} else {
		a, _ := json.Marshal(have.Spec)
		z, _ := json.Marshal(policy.Spec)
		owner := metav1.GetControllerOf(&have)
		if string(a) != string(z) || owner == nil || owner.UID != g.UID {
			return ctrl.Result{}, errors.New("maintenance network policy changed")
		}
	}
	p, err := b.maintenancePod(ctx, g)
	if apierrors.IsNotFound(err) {
		if g.Status.Maintenance != nil && g.Status.Maintenance.PodUID != "" {
			return b.end(ctx, g, v1alpha1.GrantFailed, "", "executor disappeared; termination unproven", now)
		}
		// Persist creation intent before the API call. A missing Pod after an
		// ambiguous create is never permission to recreate or claim termination.
		if g.Status.Maintenance != nil {
			return b.end(ctx, g, v1alpha1.GrantFailed, "", "executor creation or termination unproven", now)
		}
		g.Status.Maintenance = &v1alpha1.MaintenanceStatus{Executor: "Unknown"}
		if written, err := b.writeStatus(ctx, g); err != nil {
			return ctrl.Result{}, err
		} else if !written {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		p = MaintenancePod(g, now.Time)
		if err = b.Client.Create(ctx, p); err != nil {
			// A definitive admission/auth/body refusal cannot have created this
			// executor. Timeouts/5xx/AlreadyExists remain ambiguous and fenced.
			if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) {
				if writeErr := b.recordMaintenanceRefusal(ctx, g); writeErr != nil {
					return ctrl.Result{}, fmt.Errorf("executor creation definitively refused (%w), but refusal receipt failed: %w", err, writeErr)
				}
				return b.end(ctx, g, v1alpha1.GrantFailed, "", "executor creation refused: "+err.Error(), now)
			}
			return ctrl.Result{}, err
		}
	} else if err != nil {
		return ctrl.Result{}, err
	}
	if g.Status.Maintenance == nil {
		g.Status.Maintenance = &v1alpha1.MaintenanceStatus{}
	}
	g.Status.Maintenance.PodUID = string(p.UID)
	g.Status.Maintenance.Executor = "Running"
	// Restore evidence is an attributed participant report, separate from stop proof.
	g.Status.Maintenance.RestoreEvidence = g.Annotations[MaintenanceRestoreAnnotation]
	if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
		return b.end(ctx, g, v1alpha1.GrantReleased, "", "executor completed", now)
	}
	_, err = b.writeStatus(ctx, g)
	return ctrl.Result{RequeueAfter: min(5*time.Second, g.Status.ExpiresAt.Sub(now.Time))}, err
}

// StopMaintenancePatch changes exactly one field with UID/resourceVersion tests.
func StopMaintenancePatch(p *corev1.Pod) []byte {
	raw, _ := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": string(p.UID)},
		{"op": "test", "path": "/metadata/resourceVersion", "value": p.ResourceVersion},
		{"op": "add", "path": "/spec/activeDeadlineSeconds", "value": int64(1)},
	})
	return raw
}

func (b *Broker) stopMaintenance(ctx context.Context, g *v1alpha1.AccessGrant) error {
	pod, err := b.maintenancePod(ctx, g)
	if g.Status.Maintenance == nil {
		g.Status.Maintenance = &v1alpha1.MaintenanceStatus{}
	}
	st := g.Status.Maintenance
	if apierrors.IsNotFound(err) && st.PodUID == "" && (st.Executor == "" || st.Executor == "NotCreated") {
		return b.deleteMaintenanceNetwork(ctx, g)
	} // no intent, or confirmed refusal: no executor was created
	if apierrors.IsNotFound(err) && st.Executor == "Terminated" {
		return b.deleteMaintenanceNetwork(ctx, g)
	}
	if err != nil {
		st.Executor = "Unknown"
		g.Status.Message = "executor termination unproven: " + truncate(err.Error())
		_, writeErr := b.writeStatus(ctx, g)
		return errors.Join(err, writeErr)
	}
	st.PodUID = string(pod.UID)
	st.RestoreEvidence = g.Annotations[MaintenanceRestoreAnnotation]
	if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		st.Executor = "StopPending"
		g.Status.Message = "API execution revoked; waiting for observed executor termination; restore is not implied"
		if _, err := b.writeStatus(ctx, g); err != nil {
			return err
		}
		if pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds > 1 {
			if err := b.Client.Patch(ctx, pod, client.RawPatch(types.JSONPatchType, StopMaintenancePatch(pod))); err != nil {
				return err
			}
		}
		return errCredentialCleanupPending // shared lifecycle retry, not a terminal claim
	}
	if err := controller.VerifyExecutorStopped(ctx, b.APIReader, pod, g.Spec.Maintenance.NodeUID, b.Clock.Now); err != nil {
		st.Executor = "Unknown"
		g.Status.Message = "executor termination unproven: " + truncate(err.Error())
		_, writeErr := b.writeStatus(ctx, g)
		return errors.Join(err, writeErr)
	}
	st.Executor = "Terminated"
	// Persist termination before deleting; restart/NotFound cannot erase proof.
	if written, err := b.writeStatus(ctx, g); err != nil {
		return err
	} else if !written {
		return errCredentialCleanupPending
	}
	if _, err := b.deleteOwn(ctx, g, pod); err != nil {
		return err
	}
	return b.deleteMaintenanceNetwork(ctx, g)
}

func (b *Broker) deleteMaintenanceNetwork(ctx context.Context, g *v1alpha1.AccessGrant) error {
	var n networkingv1.NetworkPolicy
	if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(g), &n); err != nil {
		return client.IgnoreNotFound(err)
	}
	owner := metav1.GetControllerOf(&n)
	if owner == nil || owner.UID != g.UID {
		return errors.New("maintenance network ownership changed")
	}
	return client.IgnoreNotFound(b.Client.Delete(ctx, &n, client.Preconditions{UID: &n.UID}))
}

// recordMaintenanceRefusal keeps the known no-create outcome across status CAS
// conflicts (including a concurrent release). It never repeats the Pod create.
func (b *Broker) recordMaintenanceRefusal(ctx context.Context, g *v1alpha1.AccessGrant) error {
	uid := g.UID
	for range 5 {
		if g.Status.Maintenance == nil || g.Status.Maintenance.PodUID != "" {
			return errors.New("refusal receipt encountered another executor binding")
		}
		g.Status.Maintenance.Executor = "NotCreated"
		err := b.Client.Status().Update(ctx, g)
		if err == nil {
			return nil
		}
		if apierrors.IsNotFound(err) {
			return b.deleteMaintenanceNetwork(ctx, g)
		}
		if !apierrors.IsConflict(err) {
			return err
		}
		var latest v1alpha1.AccessGrant
		if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(g), &latest); err != nil {
			if apierrors.IsNotFound(err) {
				return b.deleteMaintenanceNetwork(ctx, g)
			}
			return err
		}
		if latest.UID != uid {
			return errors.New("grant UID changed while recording definitive executor refusal")
		}
		*g = latest
	}
	return errors.New("definitive executor refusal receipt kept conflicting; cleanup remains unproven")
}
