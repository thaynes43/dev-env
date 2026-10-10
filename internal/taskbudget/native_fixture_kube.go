package taskbudget

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	NativeFixtureNamespace      = "dev-agents"
	NativeFixtureName           = "dev-env-owned-native-lifecycle"
	NativeFixtureHostID         = "owned-native-fixture"
	NativeFixtureServiceAccount = "dev-env-owned-native-fixture"
	NativeFixtureSuccess        = "local native initialize and owned tree stop; zero task turns"
	NativeFixtureCommand        = "umask 077; mkdir -p /fixture/home/.codex/app-server-daemon /fixture/home/.agentd /fixture/home/work; test ! -e /fixture/home/.codex/app-server-daemon/settings.json; printf '%s\\n' '{\"updater\":{\"autoUpdateEnabled\":false}}' > /fixture/home/.codex/app-server-daemon/settings.json; printf \"owned native stop canary\\n\" > /fixture/home/work/canary; exec sleep 100"
)

var nativeFixtureImage = regexp.MustCompile(`^ghcr\.io/thaynes43/dev-env(?:\:[0-9]+\.[0-9]+\.[0-9]+)?@sha256:[a-f0-9]{64}$`)

// ValidNativeFixtureImage checks identity syntax only; external signature and
// source verification are deployment prerequisites, never inferred from a tag.
func ValidNativeFixtureImage(image string) bool { return nativeFixtureImage.MatchString(image) }

// KubeNativeFixtureInspector admits only the reviewed local, zero-task profile.
// Reader must be uncached and Image must be an independently signature-verified
// published digest supplied by operator configuration, never by a host request.
// It checks Kubernetes policy intent. Effective Cilium enforcement and sole
// trusted exec-client ownership must separately pass deployment acceptance.
// NativeReady is proven after admission by agentd; requiring it here is circular.
type KubeNativeFixtureInspector struct {
	Reader client.Reader
	Image  string
	Now    func() time.Time
}

func (i *KubeNativeFixtureInspector) InspectNativeAdmission(ctx context.Context, l *Ledger, b Binding) error {
	if i == nil || i.Reader == nil || !nativeFixtureImage.MatchString(i.Image) || l == nil {
		return ErrUnavailable
	}
	now := time.Now().UTC()
	if i.Now != nil {
		now = i.Now().UTC()
	}
	if l.checkBinding(b) != nil || l.Latched || b.HostID != NativeFixtureHostID || b.Epoch != 1 ||
		l.Parent != NativeFixtureNamespace+"/"+NativeFixtureServiceAccount || l.Spec.SuccessCondition != NativeFixtureSuccess ||
		l.Spec.OverallSeconds != 20 || l.Spec.EffortSeconds != 20 || l.Spec.CheckpointSeconds != 20 ||
		l.CreatedAt.IsZero() || !b.Deadline.Equal(l.CreatedAt.Add(20*time.Second)) || now.Before(l.CreatedAt) || !now.Before(b.Deadline) ||
		len(l.Events) != 0 || len(l.Extensions) != 0 || len(l.FailureAllowance) != 0 || len(l.Workers) > 1 {
		return ErrDenied
	}
	for _, w := range l.Workers {
		if w.ID != fmt.Sprintf("host:%s:%d", b.HostID, b.Epoch) || !w.Active || w.Managed != nil {
			return ErrDenied
		}
	}
	var pod corev1.Pod
	if i.Reader.Get(ctx, client.ObjectKey{Namespace: NativeFixtureNamespace, Name: NativeFixtureName}, &pod) != nil {
		return ErrUnavailable
	}
	if string(pod.UID) != b.PodUID || pod.UID == "" || !pod.DeletionTimestamp.IsZero() || len(pod.OwnerReferences) != 0 ||
		pod.CreationTimestamp.IsZero() || l.CreatedAt.Before(pod.CreationTimestamp.Time) ||
		now.Sub(pod.CreationTimestamp.Time) >= 120*time.Second || pod.Status.Phase != corev1.PodRunning || pod.Spec.NodeName == "" ||
		pod.Labels["app.kubernetes.io/name"] != NativeFixtureName ||
		pod.Annotations["k8tz.io/inject"] != "false" || pod.Annotations["dev-env.haynesops.com/coordinator-host"] != NativeFixtureHostID {
		return ErrDenied
	}
	if len(pod.Status.ContainerStatuses) != 1 {
		return ErrDenied
	}
	status := pod.Status.ContainerStatuses[0]
	_, digest, _ := strings.Cut(i.Image, "@")
	if status.Name != "fixture" || status.RestartCount != 0 || !status.Ready || status.State.Running == nil ||
		status.LastTerminationState.Terminated != nil || !strings.HasSuffix(status.ImageID, "@"+digest) {
		return ErrDenied
	}
	// Leave ten seconds for the fixed three-second owned stop and scheduling
	// before either PID 1's sleep100 or the Pod's lifetime can remove the tree.
	started := status.State.Running.StartedAt.Time
	if started.IsZero() || now.Before(started) || b.Deadline.After(started.Add(90*time.Second)) ||
		b.Deadline.After(pod.CreationTimestamp.Add(110*time.Second)) {
		return ErrDenied
	}
	expected := NativeFixturePod(i.Image)
	got := pod.Spec.DeepCopy()
	// These two fields are populated by the scheduler and the API's legacy
	// service-account alias. Every capability-bearing field remains compared.
	got.NodeName = ""
	if got.DeprecatedServiceAccount != "" && got.DeprecatedServiceAccount != NativeFixtureServiceAccount {
		return ErrDenied
	}
	got.DeprecatedServiceAccount = ""
	if !apiequality.Semantic.DeepEqual(*got, expected.Spec) {
		return ErrDenied
	}
	policy := NativeFixturePolicy()
	actual := &unstructured.Unstructured{}
	actual.SetGroupVersionKind(policy.GroupVersionKind())
	if i.Reader.Get(ctx, client.ObjectKeyFromObject(policy), actual) != nil {
		return ErrUnavailable
	}
	if actual.GetUID() == "" || !actual.GetDeletionTimestamp().IsZero() || len(actual.GetOwnerReferences()) != 0 ||
		!apiequality.Semantic.DeepEqual(actual.Object["spec"], policy.Object["spec"]) || actual.Object["specs"] != nil {
		return ErrDenied
	}
	return nil
}

// NativeFixturePod is the sole supported, fixed-name GitOps profile. The helper
// never starts itself from this shell: a trusted finite acceptance client creates
// the off-Pod campaign, then execs the budget-bound agentd command exactly once.
func NativeFixturePod(image string) *corev1.Pod {
	f := false
	t := true
	u := int64(1000)
	deadline := int64(120)
	grace := int64(5)
	mode := int32(0440)
	publicMode := int32(0444)
	exp := int64(600)
	return &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Namespace: NativeFixtureNamespace, Name: NativeFixtureName,
		Labels: map[string]string{"app.kubernetes.io/name": NativeFixtureName}, Annotations: map[string]string{"k8tz.io/inject": "false", "dev-env.haynesops.com/coordinator-host": NativeFixtureHostID}},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: &deadline, TerminationGracePeriodSeconds: &grace,
			AutomountServiceAccountToken: &f, EnableServiceLinks: &f, ServiceAccountName: NativeFixtureServiceAccount, PriorityClassName: "dev-env-agent",
			Priority: int32ptr(-10), PreemptionPolicy: preemptionptr(corev1.PreemptNever), DNSPolicy: corev1.DNSClusterFirst, SchedulerName: "default-scheduler", NodeSelector: map[string]string{"topology.kubernetes.io/zone": "w"},
			SecurityContext: &corev1.PodSecurityContext{RunAsUser: &u, RunAsGroup: &u, FSGroup: &u, RunAsNonRoot: &t, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Tolerations:     []corev1.Toleration{{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: int64ptr(300)}, {Key: "node.kubernetes.io/unreachable", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: int64ptr(300)}},
			Containers: []corev1.Container{{Name: "fixture", Image: image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/bin/sh", "-ceu"}, Args: []string{NativeFixtureCommand},
				TerminationMessagePath: "/dev/termination-log", TerminationMessagePolicy: corev1.TerminationMessageReadFile,
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &f, ReadOnlyRootFilesystem: &t, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				Env:             []corev1.EnvVar{{Name: "HOME", Value: "/fixture/home"}, {Name: "CODEX_HOME", Value: "/fixture/home/.codex"}, {Name: "AGENTD_OWNED_CODEX_HOST_ENABLED", Value: "true"}, {Name: "AGENTD_API_URL", Value: "https://dev-env-operator.dev-env-system.svc.cluster.local:8443"}, {Name: "AGENTD_API_TOKEN_FILE", Value: "/var/run/secrets/dev-env/token"}, {Name: "AGENTD_API_CA_FILE", Value: "/opt/dev-env/api-ca/ca.crt"}, {Name: "AGENTD_CODEX_ACCESS_FILE", Value: "/opt/dev-env/codex-access/access.json"}, {Name: "DEV_ENV_POD_UID", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.uid"}}}},
				Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("256Mi")}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "home", MountPath: "/fixture"}, {Name: "tmp", MountPath: "/tmp"}, {Name: "api-token", MountPath: "/var/run/secrets/dev-env", ReadOnly: true}, {Name: "api-ca", MountPath: "/opt/dev-env/api-ca", ReadOnly: true}, {Name: "codex-access", MountPath: "/opt/dev-env/codex-access", ReadOnly: true}}}},
			Volumes: []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: quantityptr("64Mi")}}}, {Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: quantityptr("16Mi")}}},
				{Name: "api-token", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{DefaultMode: &mode, Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: "dev-env-operator", ExpirationSeconds: &exp, Path: "token"}}}}}},
				{Name: "api-ca", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "dev-env-api-ca"}, DefaultMode: &publicMode, Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}}},
				{Name: "codex-access", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "dev-env-codex-live", DefaultMode: &mode, Items: []corev1.KeyToPath{{Key: "access.json", Path: "access.json"}}}}}}}}
}

func int32ptr(n int32) *int32                                          { return &n }
func preemptionptr(v corev1.PreemptionPolicy) *corev1.PreemptionPolicy { return &v }
func int64ptr(n int64) *int64                                          { return &n }
func quantityptr(v string) *resource.Quantity                          { q := resource.MustParse(v); return &q }

// NativeFixturePolicy denies every inbound network client and provider/host
// egress, including baseline allow policies. Only cluster DNS and operator HTTPS
// are added. A datapath check must establish enforcement before the one exec.
func NativeFixturePolicy() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cilium.io/v2", "kind": "CiliumNetworkPolicy", "metadata": map[string]any{"namespace": NativeFixtureNamespace, "name": NativeFixtureName + "-offline"}, "spec": map[string]any{
		"endpointSelector": map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": NativeFixtureName}},
		"ingressDeny":      []any{map[string]any{"fromEntities": []any{"all"}}},
		"egressDeny":       []any{map[string]any{"toEntities": []any{"world", "host", "remote-node"}}},
		"egress": []any{
			map[string]any{"toEndpoints": []any{map[string]any{"matchLabels": map[string]any{"io.kubernetes.pod.namespace": "dev-env-system", "app.kubernetes.io/name": "dev-env-operator"}}}, "toPorts": []any{map[string]any{"ports": []any{map[string]any{"port": "8443", "protocol": "TCP"}}}}},
			map[string]any{"toEndpoints": []any{map[string]any{"matchLabels": map[string]any{"io.kubernetes.pod.namespace": "kube-system", "k8s-app": "kube-dns"}}}, "toPorts": []any{map[string]any{"ports": []any{map[string]any{"port": "53", "protocol": "UDP"}, map[string]any{"port": "53", "protocol": "TCP"}}}}},
		}}}}
}
