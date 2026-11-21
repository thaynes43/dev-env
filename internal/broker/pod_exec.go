package broker

import (
	"context"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
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

// ReadPodExecTarget reads the pod once. A stored UID makes this a recheck
// of the same pod instance. A missing or changed target cannot be retried
// into authority over a replacement.
func ReadPodExecTarget(ctx context.Context, reader client.Reader, target *v1alpha1.PodExecGrant) (string, error) {
	var pod corev1.Pod
	key := client.ObjectKey{Namespace: target.Namespace, Name: target.Pod}
	if err := reader.Get(ctx, key, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return "", permanent{fmt.Errorf("pod %s was not found", key)}
		}
		return "", fmt.Errorf("read target pod: %w", err)
	}
	uid := string(pod.UID)
	switch {
	case uid == "" || (target.PodUID != "" && uid != target.PodUID):
		return "", permanent{fmt.Errorf("pod %s no longer has the requested UID", key)}
	case !pod.DeletionTimestamp.IsZero():
		return "", permanent{fmt.Errorf("pod %s is being deleted", key)}
	case !podHasContainer(&pod, target.Container):
		return "", permanent{fmt.Errorf("pod %s has no container named %s", key, target.Container)}
	}
	return uid, nil
}

func podHasContainer(pod *corev1.Pod, name string) bool {
	named := func(c corev1.Container) bool { return c.Name == name }
	return slices.ContainsFunc(pod.Spec.Containers, named) ||
		slices.ContainsFunc(pod.Spec.InitContainers, named) ||
		slices.ContainsFunc(pod.Spec.EphemeralContainers, func(c corev1.EphemeralContainer) bool { return c.Name == name })
}
