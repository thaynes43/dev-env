package broker

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const MaxPodExecTTL = time.Hour

// These namespaces are refused by dev-env-identity-guard or skipped by
// Kyverno, where the container and UID checks are enforced.
var podExecExcludedNamespaces = []string{
	"dev-env-system", "dev-agents", "dev-tools", "dev-env-admin-issuance",
	"kube-system", "kyverno", "flux-system", "external-secrets", "cert-manager",
	"ceph-csi", "openebs-system", "volsync-system", "k8tz", "kube-public", "kube-node-lease",
}

// Keep this list aligned with privilegedServiceAccounts in haynes-ops
// kubernetes/main/apps/kyverno/policies/app/dev-env-exec-guard.yaml.
var podExecPowerfulServiceAccounts = []string{
	"frontend/headlamp",
	"dev/dev-env", "upgrade-agent/dev-env-ops",
	"upgrade-agent/alert-responder", "upgrade-agent/upgrade-shepherd",
	"upgrade-agent/upgrade-health-gate", "upgrade-agent/cigar-curation",
	"database/cloudnative-pg", "database/dragonfly-operator-controller-manager",
	"network/multus", "network/traefik-external", "network/traefik-internal",
	"observability/grafana", "observability/kube-prometheus-stack-operator",
	"observability/kube-state-metrics", "observability/loki", "observability/promtail",
	"rook-ceph/rook-ceph-system", "rook-ceph/objectstorage-provisioner",
	"rook-ceph/rook-ceph-cephfs-csi-ceph-com-ctrlplugin-sa",
	"rook-ceph/rook-ceph-cephfs-csi-ceph-com-nodeplugin-sa",
	"rook-ceph/rook-ceph-rbd-csi-ceph-com-ctrlplugin-sa",
	"rook-ceph/rook-ceph-rbd-csi-ceph-com-nodeplugin-sa",
}

// ValidatePodExecScope checks the target before any pod lookup.
func ValidatePodExecScope(target *v1alpha1.PodExecGrant, reserved ...string) error {
	switch {
	case len(validation.IsDNS1123Label(target.Namespace)) > 0:
		return fmt.Errorf("pod-exec requires one namespace")
	case slices.Contains(podExecExcludedNamespaces, target.Namespace) || slices.Contains(reserved, target.Namespace):
		return fmt.Errorf("pod-exec grants are unavailable in namespace %s", target.Namespace)
	case len(validation.IsDNS1123Subdomain(target.Pod)) > 0:
		return fmt.Errorf("pod-exec requires a valid pod name")
	case len(validation.IsDNS1123Label(target.Container)) > 0:
		return fmt.Errorf("pod-exec requires a valid container name")
	}
	return nil
}

// ResolvePodExecTarget records the UID and security details from one pod read.
func ResolvePodExecTarget(ctx context.Context, reader client.Reader, target *v1alpha1.PodExecGrant) error {
	pod, err := readPodExecTarget(ctx, reader, target)
	if err != nil {
		return err
	}
	target.PodUID = string(pod.UID)
	target.Security = &v1alpha1.PodExecTargetSecurity{
		Privileged: podHasPrivilegedContainer(pod),
		HostPID:    pod.Spec.HostPID,
		PowerfulServiceAccount: slices.Contains(podExecPowerfulServiceAccounts,
			pod.Namespace+"/"+cmp.Or(pod.Spec.ServiceAccountName, "default")),
	}
	return nil
}

// readPodExecTarget rechecks a stored UID against the same pod instance.
// A missing or changed target cannot be retried into authority over a replacement.
func readPodExecTarget(ctx context.Context, reader client.Reader, target *v1alpha1.PodExecGrant) (*corev1.Pod, error) {
	var pod corev1.Pod
	key := client.ObjectKey{Namespace: target.Namespace, Name: target.Pod}
	if err := reader.Get(ctx, key, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, permanent{fmt.Errorf("pod %s was not found", key)}
		}
		readErr := fmt.Errorf("read target pod %s: %w", key, err)
		if !retryablePodRead(err) {
			return nil, permanent{readErr}
		}
		return nil, readErr
	}
	uid := string(pod.UID)
	switch {
	case uid == "" || (target.PodUID != "" && uid != target.PodUID):
		return nil, permanent{fmt.Errorf("pod %s no longer has the requested UID", key)}
	case !pod.DeletionTimestamp.IsZero():
		return nil, permanent{fmt.Errorf("pod %s is being deleted", key)}
	case !podHasContainer(&pod, target.Container):
		return nil, permanent{fmt.Errorf("pod %s has no container named %s", key, target.Container)}
	}
	return &pod, nil
}

func retryablePodRead(err error) bool {
	if apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) || apierrors.IsTooManyRequests(err) {
		return true
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		switch status.Status().Code {
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
		return false
	}
	var netErr net.Error
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		utilnet.IsConnectionRefused(err) || utilnet.IsConnectionReset(err) ||
		utilnet.IsProbableEOF(err) || utilnet.IsHTTP2ConnectionLost(err) ||
		(errors.As(err, &netErr) && netErr.Timeout())
}

func podHasPrivilegedContainer(pod *corev1.Pod) bool {
	isPrivileged := func(sc *corev1.SecurityContext) bool {
		return sc != nil && ptr.Deref(sc.Privileged, false)
	}
	privileged := func(c corev1.Container) bool { return isPrivileged(c.SecurityContext) }
	return slices.ContainsFunc(pod.Spec.Containers, privileged) ||
		slices.ContainsFunc(pod.Spec.InitContainers, privileged) ||
		slices.ContainsFunc(pod.Spec.EphemeralContainers, func(c corev1.EphemeralContainer) bool {
			return isPrivileged(c.SecurityContext)
		})
}

func podHasContainer(pod *corev1.Pod, name string) bool {
	named := func(c corev1.Container) bool { return c.Name == name }
	return slices.ContainsFunc(pod.Spec.Containers, named) ||
		slices.ContainsFunc(pod.Spec.InitContainers, named) ||
		slices.ContainsFunc(pod.Spec.EphemeralContainers, func(c corev1.EphemeralContainer) bool { return c.Name == name })
}
