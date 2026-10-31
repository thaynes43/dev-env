package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const (
	MaintenanceDecisionAnnotation = v1alpha1.LabelPrefix + "maintenance-decision"
	MaintenanceRestoreAnnotation  = v1alpha1.LabelPrefix + "maintenance-restore"
	MaintenanceLabel              = v1alpha1.LabelPrefix + "maintenance"
	MaintenanceDefaultTTL         = 30 * time.Minute
	MaintenanceMaxTTL             = time.Hour
)

var maintenanceGPUUUID = regexp.MustCompile(`^GPU-[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

var digestImage = regexp.MustCompile(`^[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}$`)

// MaintenanceDigest binds the complete immutable spec, including requester UID.
// Release is the only mutable field and is deliberately excluded.
func MaintenanceDigest(g *v1alpha1.AccessGrant) string {
	spec := g.Spec
	spec.Release = false
	raw, _ := json.Marshal(spec)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ValidateMaintenance repeats the envelope bounds independently of installed CRDs.
func ValidateMaintenance(g *v1alpha1.AccessGrant) error {
	m := g.Spec.Maintenance
	if g.Spec.Type != v1alpha1.GrantMaintenance || m == nil || g.Spec.Kube != nil || g.Spec.Egress != nil || g.Spec.Credential != nil {
		return errors.New("maintenance requires only its task envelope")
	}
	if g.Spec.Requester.SessionUID == "" || m.Coordinator.Name != g.Spec.Requester.Session || m.Coordinator.UID != string(g.Spec.Requester.SessionUID) || !validMaintenanceIdentity(m.Coordinator) {
		return errors.New("maintenance coordinator must be the authenticated requester and original pod")
	}
	if g.Spec.TTL.Duration < MinTTL || g.Spec.TTL.Duration > MaintenanceMaxTTL {
		return errors.New("maintenance TTL must be 10m to 1h; default is 30m")
	}
	if len(validation.IsDNS1123Subdomain(m.Node)) != 0 || m.NodeUID == "" || len(m.NodeUID) > 64 || strings.TrimSpace(m.Resource) == "" || len(m.Resource) > 253 || strings.TrimSpace(m.Operation) == "" || len(m.Operation) > 128 || !digestImage.MatchString(m.Image) || len(m.Image) > 512 {
		return errors.New("maintenance requires an exact node UID, resource, operation and digest image")
	}
	if m.CPUMillis < 10 || m.CPUMillis > 2000 || m.MemoryMiB < 16 || m.MemoryMiB > 4096 || len(m.Restore) > 1000 || len(m.Workers) > 8 || len(m.Devices) > 8 || len(m.ReadOnlyMounts) > 8 || len(m.Capabilities) > 2 {
		return errors.New("maintenance resource or list bounds exceeded")
	}
	if m.RuntimeProfile != "" && (m.RuntimeProfile != "nvidia-utility" || !maintenanceGPUUUID.MatchString(m.Resource) || len(m.Devices) != 0) {
		return errors.New("nvidia-utility requires an exact GPU UUID and no explicit devices; unknown runtime profile refused")
	}
	seen := map[string]bool{m.Coordinator.Name: true}
	for _, w := range m.Workers {
		if !validMaintenanceIdentity(w) || seen[w.Name] {
			return errors.New("invalid or duplicate delegated worker")
		}
		seen[w.Name] = true
	}
	for _, cap := range m.Capabilities {
		if cap != "SYS_ADMIN" && cap != "SYS_RAWIO" {
			return fmt.Errorf("capability %s is outside maintenance support", cap)
		}
	}
	for _, list := range [][]string{m.Capabilities, m.Devices, m.ReadOnlyMounts} {
		for i, value := range list {
			if slices.Contains(list[:i], value) {
				return errors.New("duplicate maintenance bound")
			}
		}
	}
	for _, d := range m.Devices {
		// Exact device files only, never /dev, disk, memory, kmsg or directory mounts.
		if path.Clean(d) != d || len(d) > 253 || !regexp.MustCompile(`^/dev/(nvidia[0-9]+|nvidiactl|nvidia-uvm|nvidia-uvm-tools|dri/renderD[0-9]+)$`).MatchString(d) {
			return fmt.Errorf("unsupported device file %q", d)
		}
	}
	for _, p := range m.ReadOnlyMounts {
		if path.Clean(p) != p || len(p) > 253 || !strings.HasPrefix(p, "/var/lib/maintenance-inputs/") {
			return errors.New("host inputs must be exact read-only maintenance-inputs files")
		}
	}
	if m.ExtensionOf != "" && (len(validation.IsDNS1123Label(m.ExtensionOf)) != 0 || !strings.HasPrefix(m.ExtensionOf, "grant-")) {
		return errors.New("extensionOf must name the earlier grant; a new approval is required")
	}
	return nil
}

func validMaintenanceIdentity(id v1alpha1.MaintenanceIdentity) bool {
	return len(validation.IsDNS1123Label(id.Name)) == 0 && id.UID != "" && len(id.UID) <= 64 && id.PodUID != "" && len(id.PodUID) <= 64
}

// CheckMaintenanceIdentities uses authoritative sessions and pod ownership; names
// or a coordinator:string attestation do not establish delegation authority.
// Only invalid envelopes, identity/eligibility mismatches and definitive NotFound
// reads wrap ErrRefused. All other read errors remain retryable.
func CheckMaintenanceIdentities(ctx context.Context, r client.Reader, namespace string, g *v1alpha1.AccessGrant) error {
	if err := ValidateMaintenance(g); err != nil {
		return fmt.Errorf("%w: %w", ErrRefused, err)
	}
	m := g.Spec.Maintenance
	for _, id := range append([]v1alpha1.MaintenanceIdentity{m.Coordinator}, m.Workers...) {
		var sess v1alpha1.AgentSession
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: id.Name}, &sess); err != nil {
			return maintenanceIdentityReadError(err)
		}
		if string(sess.UID) != id.UID || !sess.DeletionTimestamp.IsZero() {
			return fmt.Errorf("%w: maintenance session missing or replaced", ErrRefused)
		}
		if id.Name != m.Coordinator.Name && sess.Spec.Parent != m.Coordinator.Name {
			return fmt.Errorf("%w: maintenance worker is not an authoritative direct child", ErrRefused)
		}
		var pod corev1.Pod
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: id.Name}, &pod); err != nil {
			return maintenanceIdentityReadError(err)
		}
		owner := metav1.GetControllerOf(&pod)
		if string(pod.UID) != id.PodUID || !pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning || owner == nil || owner.UID != sess.UID || owner.Name != sess.Name || owner.Kind != "AgentSession" || owner.APIVersion != v1alpha1.GroupVersion.String() {
			return fmt.Errorf("%w: maintenance original session pod unavailable or replaced", ErrRefused)
		}
	}
	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: m.Node}, &node); err != nil {
		return maintenanceIdentityReadError(err)
	}
	if string(node.UID) != m.NodeUID || !node.DeletionTimestamp.IsZero() {
		return fmt.Errorf("%w: maintenance target node replaced", ErrRefused)
	}
	for key := range node.Labels {
		if key == "node-role.kubernetes.io/control-plane" || key == "node-role.kubernetes.io/master" {
			return fmt.Errorf("%w: maintenance may not target control-plane nodes", ErrRefused)
		}
	}
	for _, taint := range node.Spec.Taints {
		if taint.Key == "node-role.kubernetes.io/control-plane" || taint.Key == "node-role.kubernetes.io/master" {
			return fmt.Errorf("%w: maintenance may not target control-plane nodes", ErrRefused)
		}
	}
	return nil
}

func maintenanceIdentityReadError(err error) error {
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: %w", ErrRefused, err)
	}
	return err
}

// MaintenanceActor permits exactly the coordinator and explicitly approved workers
// on the same remaining lease. No new grant/approval is needed for listed workers.
func MaintenanceActor(g *v1alpha1.AccessGrant, name, uid, podUID string, now time.Time) error {
	if err := ValidateMaintenance(g); err != nil {
		return err
	}
	if !g.DeletionTimestamp.IsZero() || g.Spec.Release || g.Status.Phase != v1alpha1.GrantActive || g.Status.ExpiresAt == nil || !now.Before(g.Status.ExpiresAt.Time) {
		return errors.New("maintenance lease is not active")
	}
	for _, id := range append([]v1alpha1.MaintenanceIdentity{g.Spec.Maintenance.Coordinator}, g.Spec.Maintenance.Workers...) {
		if id.Name == name && id.UID == uid && id.PodUID == podUID {
			return nil
		}
	}
	return errors.New("identity is not an approved maintenance participant")
}
