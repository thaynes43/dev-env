package taskbudget

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/validation"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

var errOwnedPodStarting = fmt.Errorf("%w: original owned Pod status is still starting", ErrUnavailable)

var ErrNativeStarting = errors.New("native root is in its bounded startup phase")

const OwnedRootPolicyKey = "owned-root.json"
const OwnedRootContainer = "owned-root"

type OwnedRootPolicy struct {
	AllowContinuation bool                 `json:"allowContinuation,omitempty"`
	HomeInputsName    string               `json:"homeInputsName,omitempty"`
	HomeInputsUID     string               `json:"homeInputsUID,omitempty"`
	HomeInputsSHA     string               `json:"homeInputsSHA,omitempty"`
	Version           int                  `json:"version"`
	Namespace         string               `json:"namespace"`
	Name              string               `json:"name"`
	ServiceAccount    string               `json:"serviceAccount"`
	TaskUID           string               `json:"taskUID"`
	HostID            string               `json:"hostID"`
	Image             string               `json:"image"`
	HomeClaim         string               `json:"homeClaim"`
	HomeUID           string               `json:"homeUID"`
	Baseline          string               `json:"baseline"`
	NetworkPolicyName string               `json:"networkPolicyName"`
	NetworkPolicyUID  string               `json:"networkPolicyUID"`
	NetworkPolicySHA  string               `json:"networkPolicySHA"`
	Spec              apiv1.TaskBudgetSpec `json:"spec"`
}

func (p OwnedRootPolicy) Valid() bool {
	if p.AllowContinuation && p.HomeInputsName == "" {
		return false
	}
	if p.HomeInputsName != "" || p.HomeInputsUID != "" || p.HomeInputsSHA != "" {
		if len(validation.IsDNS1123Subdomain(p.HomeInputsName)) != 0 || !protocol.ValidTaskBudgetIdentifier(p.HomeInputsUID) || len(p.HomeInputsSHA) != 64 {
			return false
		}
		if _, err := hex.DecodeString(p.HomeInputsSHA); err != nil {
			return false
		}
	}
	if p.Version != 1 || p.Namespace != "dev-agents" || p.HostID == NativeFixtureHostID || !ValidNativeFixtureImage(p.Image) || p.Spec.SuccessCondition == "" || len(p.Spec.SuccessCondition) > 1024 || p.Spec.OverallSeconds < 1 || p.Spec.OverallSeconds > 2700 || p.Spec.EffortSeconds < 1 || p.Spec.EffortSeconds > 3600 || p.Spec.CheckpointSeconds < 1 || p.Spec.CheckpointSeconds > 600 || p.Spec.CheckpointSeconds > p.Spec.OverallSeconds {
		return false
	}
	for _, n := range []string{p.Name, p.ServiceAccount, p.HomeClaim, p.NetworkPolicyName} {
		if len(validation.IsDNS1123Subdomain(n)) != 0 {
			return false
		}
	}
	for _, s := range []string{p.Name, p.ServiceAccount, p.TaskUID, p.HostID, p.HomeClaim, p.HomeUID, p.NetworkPolicyName, p.NetworkPolicyUID} {
		if !protocol.ValidTaskBudgetIdentifier(s) {
			return false
		}
	}
	for _, s := range []string{p.Baseline, p.NetworkPolicySHA} {
		if len(s) != 64 {
			return false
		}
		if _, err := hex.DecodeString(s); err != nil {
			return false
		}
	}
	return true
}
func OwnedPolicyDigest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// OwnedRootPod is source preparation only; it never creates a workload. The
// original PID1 and immutable image/rootfs are part of the evidence boundary.
func OwnedRootPod(p OwnedRootPolicy) *corev1.Pod {
	pod := NativeFixturePod(p.Image)
	pod.Name, pod.Namespace = p.Name, p.Namespace
	pod.Labels = map[string]string{"app.kubernetes.io/name": p.Name}
	pod.Annotations = map[string]string{"k8tz.io/inject": "false", "dev-env.haynesops.com/coordinator-host": p.HostID}
	pod.Spec.ServiceAccountName = p.ServiceAccount
	life := int64(2760)
	pod.Spec.ActiveDeadlineSeconds = &life
	c := &pod.Spec.Containers[0]
	c.Name = OwnedRootContainer
	c.Command = []string{"/usr/local/bin/agentd", "owned-codex-host"}
	c.Args = nil
	c.Env[0].Value = hostexecutor.OwnedRootHome
	c.Env[1].Value = hostexecutor.OwnedRootHome + "/.codex"
	budget, _ := json.Marshal(p.Spec)
	c.Env = append(c.Env, corev1.EnvVar{Name: "AGENTD_OWNED_ROOT_BOOTSTRAP", Value: "true"}, corev1.EnvVar{Name: "AGENTD_OWNED_TASK_UID", Value: p.TaskUID}, corev1.EnvVar{Name: "AGENTD_OWNED_HOST_ID", Value: p.HostID}, corev1.EnvVar{Name: "AGENTD_OWNED_HOME_UID", Value: p.HomeUID}, corev1.EnvVar{Name: "AGENTD_OWNED_HOME_BASELINE", Value: p.Baseline}, corev1.EnvVar{Name: "AGENTD_OWNED_BUDGET_SPEC", Value: string(budget)})
	c.VolumeMounts[0].MountPath = "/workspace"
	pod.Spec.Volumes[0].VolumeSource = corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: p.HomeClaim}}
	if p.AllowContinuation {
		c.Env = append(c.Env, corev1.EnvVar{Name: "AGENTD_OWNED_ALLOW_CONTINUATION", Value: "true"})
	}
	if p.HomeInputsName != "" {
		mode := int32(0444)
		c.Env = append(c.Env, corev1.EnvVar{Name: "AGENTD_OWNED_HOME_INPUTS_SHA", Value: p.HomeInputsSHA})
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "owned-home-inputs", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: p.HomeInputsName}, DefaultMode: &mode, Items: []corev1.KeyToPath{{Key: "input.json", Path: "input.json"}, {Key: "requirements.toml", Path: "requirements.toml"}}}}})
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "owned-home-inputs", MountPath: "/opt/dev-env/owned-home", ReadOnly: true}, corev1.VolumeMount{Name: "owned-home-inputs", MountPath: "/etc/codex/requirements.toml", SubPath: "requirements.toml", ReadOnly: true})
	}
	return pod
}

type OwnedRootProbe interface {
	Observe(context.Context, client.ObjectKey, string, Binding, string, string) (int, error)
}

type OwnedRootInspector struct {
	Reader         client.Reader
	Policy         client.ObjectKey
	Probe          OwnedRootProbe
	Now            func() time.Time
	AssignedHost   string
	AssignedTask   string
	AssignedParent string
	AssignedPod    string
}

func (i *OwnedRootInspector) load(ctx context.Context) (*corev1.ConfigMap, OwnedRootPolicy, error) {
	var cm corev1.ConfigMap
	var p OwnedRootPolicy
	if i == nil || i.Reader == nil || i.Probe == nil || i.Policy.Namespace != "dev-env-system" || i.Policy.Name == "" || i.Reader.Get(ctx, i.Policy, &cm) != nil || cm.UID == "" || !cm.DeletionTimestamp.IsZero() || len(cm.Data[OwnedRootPolicyKey]) > 16384 {
		return nil, p, ErrUnavailable
	}
	d := json.NewDecoder(bytes.NewBufferString(cm.Data[OwnedRootPolicyKey]))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || !p.Valid() || p.Name != i.AssignedPod || p.HostID != i.AssignedHost || p.TaskUID != i.AssignedTask || p.Namespace+"/"+p.ServiceAccount != i.AssignedParent {
		return nil, p, ErrDenied
	}
	return &cm, p, nil
}

func (i *OwnedRootInspector) observe(ctx context.Context, l *Ledger, b Binding) (result int, policy OwnedRootPolicy, resultErr error) {
	const empty = 0
	stage := "policy"
	defer func() {
		if resultErr != nil && !errors.Is(resultErr, errOwnedPodStarting) {
			// Fixed stages only; no remote errors, output or policy contents.
			log.FromContext(ctx).WithName("owned-root").Info("owned root observation refused", "stage", stage)
		}
	}()
	cm, p, err := i.load(ctx)
	if err != nil {
		return empty, p, err
	}
	stage = "binding"
	now := time.Now().UTC()
	if i.Now != nil {
		now = i.Now().UTC()
	}
	if l == nil || l.Binding != b || p.TaskUID != b.TaskUID || p.HostID != b.HostID || l.Parent != p.Namespace+"/"+p.ServiceAccount || l.Spec.SuccessCondition != p.Spec.SuccessCondition || l.Spec.OverallSeconds != p.Spec.OverallSeconds || l.Spec.EffortSeconds != p.Spec.EffortSeconds || l.Spec.CheckpointSeconds != p.Spec.CheckpointSeconds || (b.Epoch != 1 && (b.Epoch != 2 || len(l.Continuations) != 1 || l.Continuations[0].Prior.Epoch != 1 || l.Continuations[0].Prior.TaskUID != b.TaskUID || l.Continuations[0].Prior.HostID != b.HostID || l.Continuations[0].Prior.PodUID != b.PodUID || !l.Continuations[0].Prior.Deadline.Equal(b.Deadline))) || !b.Deadline.Equal(l.CreatedAt.Add(time.Duration(p.Spec.OverallSeconds)*time.Second)) || now.Before(l.CreatedAt) {
		return empty, p, ErrDenied
	}
	var pod corev1.Pod
	stage = "pod-identity"
	key := client.ObjectKey{Namespace: p.Namespace, Name: p.Name}
	if i.Reader.Get(ctx, key, &pod) != nil || string(pod.UID) != b.PodUID || pod.UID == "" || pod.CreationTimestamp.IsZero() || l.CreatedAt.Before(pod.CreationTimestamp.Time) || b.Deadline.After(pod.CreationTimestamp.Add(2750*time.Second)) || !pod.DeletionTimestamp.IsZero() || len(pod.OwnerReferences) != 0 {
		return empty, p, ErrUnavailable
	}
	expected := OwnedRootPod(p)
	stage = "pod-profile"
	for k, v := range expected.Labels {
		if pod.Labels[k] != v {
			return empty, p, ErrDenied
		}
	}
	for k, v := range expected.Annotations {
		if pod.Annotations[k] != v {
			return empty, p, ErrDenied
		}
	}
	actual := pod.Spec.DeepCopy()
	actual.NodeName = ""
	if actual.DeprecatedServiceAccount != "" && actual.DeprecatedServiceAccount != p.ServiceAccount {
		return empty, p, ErrDenied
	}
	actual.DeprecatedServiceAccount = ""
	if !apiequality.Semantic.DeepEqual(*actual, expected.Spec) {
		return empty, p, ErrDenied
	}
	// The guard can make its single admission request before kubelet publishes
	// Running/Ready. Wait only for this original exact-profile Pod's startup;
	// no native process or admission mutation is allowed from this observation.
	if pod.Status.Phase == corev1.PodPending && len(pod.Status.ContainerStatuses) <= 1 {
		if len(pod.Status.ContainerStatuses) == 0 {
			return empty, p, errOwnedPodStarting
		}
		c := pod.Status.ContainerStatuses[0]
		if c.Name == OwnedRootContainer && c.RestartCount == 0 && c.LastTerminationState.Terminated == nil && c.State.Waiting != nil && (c.State.Waiting.Reason == "ContainerCreating" || c.State.Waiting.Reason == "PodInitializing") {
			return empty, p, errOwnedPodStarting
		}
	}
	stage = "pod-status"
	if pod.Status.Phase != corev1.PodRunning || len(pod.Status.ContainerStatuses) != 1 {
		return empty, p, ErrUnavailable
	}
	st := pod.Status.ContainerStatuses[0]
	_, digest, _ := strings.Cut(p.Image, "@")
	if st.Name != OwnedRootContainer || !st.Ready || st.RestartCount != 0 || st.State.Running == nil || st.LastTerminationState.Terminated != nil || !strings.HasSuffix(st.ImageID, "@"+digest) || st.State.Running.StartedAt.IsZero() || now.Before(st.State.Running.StartedAt.Time) || b.Deadline.After(st.State.Running.StartedAt.Add(2750*time.Second)) {
		return empty, p, ErrDenied
	}
	var home corev1.PersistentVolumeClaim
	stage = "home"
	if i.Reader.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: p.HomeClaim}, &home) != nil || string(home.UID) != p.HomeUID || !home.DeletionTimestamp.IsZero() || home.Status.Phase != corev1.ClaimBound || home.Spec.VolumeName == "" || len(home.Spec.AccessModes) != 1 || home.Spec.AccessModes[0] != corev1.ReadWriteOncePod {
		return empty, p, ErrUnavailable
	}
	netPolicy := &unstructured.Unstructured{}
	stage = "network"
	netPolicy.SetAPIVersion("cilium.io/v2")
	netPolicy.SetKind("CiliumNetworkPolicy")
	if i.Reader.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: p.NetworkPolicyName}, netPolicy) != nil || string(netPolicy.GetUID()) != p.NetworkPolicyUID || !netPolicy.GetDeletionTimestamp().IsZero() || OwnedPolicyDigest(netPolicy.Object["spec"]) != p.NetworkPolicySHA || netPolicy.Object["specs"] != nil {
		return empty, p, ErrUnavailable
	}
	var inputs corev1.ConfigMap
	stage = "inputs"
	if p.HomeInputsName != "" {
		if i.Reader.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: p.HomeInputsName}, &inputs) != nil || string(inputs.UID) != p.HomeInputsUID || !inputs.DeletionTimestamp.IsZero() || len(inputs.Data) != 2 || len(inputs.Data["input.json"]) > 512<<10 || len(inputs.Data["requirements.toml"]) > 64<<10 || OwnedPolicyDigest(inputs.Data) != p.HomeInputsSHA {
			return empty, p, ErrUnavailable
		}
	}
	stage = "process-proof"
	proof, err := i.Probe.Observe(ctx, key, OwnedRootContainer, b, p.HomeUID, p.Baseline)
	if err != nil || !hostexecutor.ValidEvidenceExit(proof) {
		return empty, p, ErrUnavailable
	}
	var again corev1.Pod
	var cmAgain corev1.ConfigMap
	var homeAgain corev1.PersistentVolumeClaim
	netAgain := netPolicy.DeepCopy()
	stage = "pod-readback"
	if i.Reader.Get(ctx, key, &again) != nil || !reflect.DeepEqual(&pod, &again) {
		return empty, p, ErrUnavailable
	}
	stage = "policy-readback"
	if i.Reader.Get(ctx, i.Policy, &cmAgain) != nil || !reflect.DeepEqual(cm, &cmAgain) {
		return empty, p, ErrUnavailable
	}
	stage = "home-readback"
	if i.Reader.Get(ctx, client.ObjectKeyFromObject(&home), &homeAgain) != nil || !reflect.DeepEqual(&home, &homeAgain) {
		return empty, p, ErrUnavailable
	}
	stage = "network-readback"
	if i.Reader.Get(ctx, client.ObjectKeyFromObject(netPolicy), netAgain) != nil || !reflect.DeepEqual(netPolicy, netAgain) {
		return empty, p, ErrUnavailable
	}
	stage = "inputs-readback"
	if p.HomeInputsName != "" {
		var inputsAgain corev1.ConfigMap
		if i.Reader.Get(ctx, client.ObjectKeyFromObject(&inputs), &inputsAgain) != nil || !reflect.DeepEqual(inputs, inputsAgain) {
			return empty, p, ErrUnavailable
		}
	}
	return proof, p, nil
}
func (i *OwnedRootInspector) InspectNativeAdmission(ctx context.Context, l *Ledger, b Binding) error {
	if l.NativeAdmitted() {
		return i.InspectNativeContinuation(ctx, l, b)
	}
	// One original admission request, not a mutation retry. Only the precise
	// kubelet startup state may wait, bounded inside the existing request budget.
	waitUntil := time.Now().Add(time.Second)
	var proof int
	var err error
	for {
		proof, _, err = i.observe(ctx, l, b)
		if !errors.Is(err, errOwnedPodStarting) || !time.Now().Before(waitUntil) {
			break
		}
		select {
		case <-ctx.Done():
			log.FromContext(ctx).WithName("owned-root").Info("owned root observation refused", "stage", "pod-startup-context")
			return ErrUnavailable
		case <-time.After(50 * time.Millisecond):
		}
	}
	if err != nil {
		if errors.Is(err, errOwnedPodStarting) {
			log.FromContext(ctx).WithName("owned-root").Info("owned root observation refused", "stage", "pod-startup-cap")
		}
		return err
	}
	if proof != hostexecutor.EvidencePrepared {
		return ErrDenied
	}
	return nil
}
func (i *OwnedRootInspector) InspectNativeContinuation(ctx context.Context, l *Ledger, b Binding) error {
	proof, _, err := i.observe(ctx, l, b)
	if err != nil {
		return err
	}
	if proof == hostexecutor.EvidenceStarting || proof == hostexecutor.EvidencePrepared {
		return ErrNativeStarting
	}
	if !l.NativeAdmitted() || proof != hostexecutor.EvidenceReady {
		return ErrUnavailable
	}
	return nil
}
func (i *OwnedRootInspector) ValidateStop(ctx context.Context, l *Ledger, e apiv1.TaskBudgetEvent) error {
	id := "host:" + l.Binding.HostID + ":" + strconv.FormatUint(l.Binding.Epoch, 10)
	if e.Kind != "worker-stop" || e.WorkerID != id || e.ID != apiv1.OwnedStopEventID(l.Binding) || e.Evidence.Kind != "owned-root-stop" || e.Evidence.ID != e.ID || e.Evidence.Reference != l.Binding.PodUID || !l.NativeAdmitted() {
		return ErrDenied
	}
	proof, _, err := i.observe(ctx, l, l.Binding)
	if err != nil {
		return err
	}
	if proof != hostexecutor.EvidenceStopped && proof != hostexecutor.EvidenceContinuable {
		return ErrUnavailable
	}
	return nil
}

func (i *OwnedRootInspector) AuthorizeNativeContinue(ctx context.Context, l *Ledger, b Binding) error {
	phase, p, err := i.observe(ctx, l, b)
	if err != nil {
		return err
	}
	if !p.AllowContinuation || phase != hostexecutor.EvidenceContinuable {
		return ErrDenied
	}
	return nil
}
