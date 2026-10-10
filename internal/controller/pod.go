package controller

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/templates"
)

// The pod shape that is design, not template data (D-44). Placement is
// DESIGN-001 7.1 and the security settings 3.6; neither is a knob in the
// templates, so no template change can put a session on a control-plane node or
// run it as root.
const (
	// ContainerName is the session pod's one container.
	ContainerName = "agent"
	// ServiceAccountName is the agents' identity (DESIGN-001 6.11).
	ServiceAccountName = "dev-env-agent"
	// PriorityClassName is -10 with preemptionPolicy Never (D-20), created by
	// haynes-ops.
	PriorityClassName = "dev-env-agent"
	// APITokenAudience is the audience of the token a session uses to call the
	// operator's /v1 API (D-05).
	APITokenAudience = "dev-env-operator"
	// APITokenFile is that token's path in the pod, which the operator passes
	// as AGENTD_API_TOKEN_FILE (D-41); it is also agentd's default.
	APITokenFile = templates.APITokenDir + "/token"

	// TerminationGracePeriod gives agentd time to forward SIGTERM to the CLI and
	// wait for it: the CLI flushes its transcript and archives its Remote Control
	// entry on the way out (S-6). Rescue runs before a pod is deleted, by exec, so
	// the grace covers the CLI's shutdown only.
	TerminationGracePeriod = 60

	// ZoneLabel and WorkerZone keep sessions on the workers (D-20).
	ZoneLabel  = "topology.kubernetes.io/zone"
	WorkerZone = "w"
	// GPULabel marks GPU nodes, which sessions avoid by preference (D-20). NFD
	// sets it.
	GPULabel = "feature.node.kubernetes.io/nvidia-gpu"

	// tolerationSeconds keeps a session on a node that stops answering: its RWO
	// volume cannot attach elsewhere until the old node lets go, so an early
	// eviction never helps (DESIGN-001 3.6).
	tolerationSeconds = 3600
	uid               = 1000
	tmpSizeLimit      = "8Gi"
	grantsSizeLimit   = "16Mi"
	apiTokenSeconds   = 3600
	runtimeDir        = "/dev/shm/run-1000"
	// agentd's Git credential helper reads /creds/gh_token by default.
	rescueGitHubMountPath = "/creds"
)

// HoldArgs are the rescue pod's container arguments (D-55): the image's
// entrypoint is `tini -- agentd`, and `hold` replaces its default `run`.
var HoldArgs = []string{"hold"}

// HomeClaimName is the session volume's name (DESIGN-001 3.2).
func HomeClaimName(session string) string { return "home-" + session }

// buildPod returns the session's pod. apiURL is the operator API's base URL for
// agentd's heartbeat (D-41); empty turns the heartbeat off. The pod is never
// updated after create: a change in the templates reaches a session only through
// a drain (5.2).
func buildPod(s *v1alpha1.AgentSession, t *templates.Templates, apiURL string, managedCodex ...bool) (*corev1.Pod, error) {
	return buildManagedPod(s, t, apiURL, len(managedCodex) > 0 && managedCodex[0], false, nil)
}

func buildManagedPod(s *v1alpha1.AgentSession, t *templates.Templates, apiURL string, managedCodex, childDecisions bool, coordinatorParents []string) (*corev1.Pod, error) {
	// The API assigns Parent from authenticated caller identity. Only exact
	// configured coordinator SAs can use the coordinator-only decision route.
	childDecisions = childDecisions && s.Spec.Parent != "" && slices.Contains(coordinatorParents, s.Spec.Parent)
	return buildSessionPod(s, t, apiURL, false, managedCodex, childDecisions)
}

// buildHoldPod returns the session's rescue pod (D-55): the session's pod with
// `agentd hold` in place of `agentd run`, the S class's requests and limits,
// and no agent credentials: no static token, and no profile env or envFrom. It
// keeps the volumes a rescue needs: the session volume, the shared volume,
// the templates' mounts, and the profile mount at /creds for GitHub fetch.
// Other profile mounts are only for agents. Heartbeats are off, because no
// agent runs in it.
func buildHoldPod(s *v1alpha1.AgentSession, t *templates.Templates) (*corev1.Pod, error) {
	return buildSessionPod(s, t, "", true, false, false)
}

// isHoldPod reports whether the pod is a session's rescue pod (D-55).
func isHoldPod(p *corev1.Pod) bool { return p.Labels[v1alpha1.LabelHold] == "true" }

func buildSessionPod(s *v1alpha1.AgentSession, t *templates.Templates, apiURL string, hold bool, managedCodex bool, childDecisions bool) (*corev1.Pod, error) {
	if w := s.Spec.Workspace; w != nil && (t.Workspace == nil || !t.Workspace.Enabled || t.Workspace.Claim == "" || t.Workspace.ID != w.ID) {
		return nil, fmt.Errorf("session workspace %q does not match an enabled template", w.ID)
	}
	profileName, profile, err := t.Profile(s.Spec.Profile)
	if err != nil {
		return nil, err
	}
	sizeClass := s.Spec.Size
	if hold {
		sizeClass = v1alpha1.SizeS
	}
	size, err := t.Size(sizeClass)
	if err != nil {
		return nil, err
	}
	doc, err := sessionDocument(s)
	if err != nil {
		return nil, err
	}

	labels := sessionLabels(s, profileName)
	maps.Copy(labels, profile.Labels)
	labels[v1alpha1.LabelRevision] = t.Revision()
	if hold {
		labels[v1alpha1.LabelSize] = string(v1alpha1.SizeS)
		labels[v1alpha1.LabelHold] = "true"
	}

	env := []corev1.EnvVar{
		{Name: "HOME", Value: templates.HomePath},
		{Name: templates.WorkspaceIDEnv, Value: ""},
		// Claude refuses a group-writable socket directory; agentd creates
		// this one 0700 (DESIGN-001 3.6).
		{Name: "XDG_RUNTIME_DIR", Value: runtimeDir},
		// Agents size their test workers to this (DESIGN-001 7.2). With a
		// divisor of 1 the kubelet rounds the limit up to whole CPUs.
		{Name: "DEV_ENV_CPU_LIMIT", ValueFrom: &corev1.EnvVarSource{ResourceFieldRef: &corev1.ResourceFieldSelector{
			ContainerName: ContainerName, Resource: "limits.cpu", Divisor: resource.MustParse("1"),
		}}},
		{Name: protocol.SessionEnv, Value: doc},
		// Set, empty, so no envFrom source can point agentd at another
		// session document (env ranks above envFrom).
		{Name: protocol.SessionFileEnv, Value: ""},
		// Empty until the API is served: agentd's heartbeat is off then.
		{Name: "AGENTD_API_URL", Value: apiURL},
		{Name: "AGENTD_API_TOKEN_FILE", Value: APITokenFile},
		{Name: protocol.GrantsDirEnv, Value: protocol.GrantsDir},
		{Name: protocol.KubeconfigEnv, Value: protocol.GrantsDir + "/" + protocol.KubeconfigName},
		{Name: protocol.PodUIDEnv, ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}}},
		{Name: protocol.PodNamespaceEnv, ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
	}
	if staticTokenFor(s) && !hold {
		env = append(env, corev1.EnvVar{Name: "CLAUDE_CODE_OAUTH_TOKEN", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: t.Claude.StaticToken.Name},
				Key:                  t.Claude.StaticToken.Key,
			},
		}})
	}
	env = append(env, t.Env...)
	envFrom := []corev1.EnvFromSource{}
	if !hold {
		env = append(env, profile.Env...)
		envFrom = append(envFrom, profile.EnvFrom...)
	}
	// Controller-owned feature gate cannot be widened by template/profile env.
	env = append(env, corev1.EnvVar{Name: "AGENTD_ENABLE_CODEX_TASKS", Value: fmt.Sprint(managedCodex && !hold)})
	env = append(env, corev1.EnvVar{Name: "AGENTD_ENABLE_CHILD_DECISIONS", Value: fmt.Sprint(childDecisions && managedCodex && !hold && s.Spec.Agent == v1alpha1.AgentCodex && s.Spec.Mode == v1alpha1.ModeTask && s.Spec.Workspace != nil)})
	var args []string
	if hold {
		args = append(args, HoldArgs...)
	}

	volumes := []corev1.Volume{
		{Name: "home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: HomeClaimName(s.Name)}}},
		{Name: "shared", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: t.SharedClaim}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr.To(resource.MustParse(tmpSizeLimit))}}},
		{Name: "grants", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: ptr.To(resource.MustParse(grantsSizeLimit))}}},
		{Name: "api-token", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
			Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
				Audience: APITokenAudience, ExpirationSeconds: ptr.To[int64](apiTokenSeconds), Path: "token",
			}}},
		}}},
	}
	mounts := []corev1.VolumeMount{
		{Name: "home", MountPath: templates.HomePath},
		{Name: "shared", MountPath: templates.SharedPath},
		{Name: "tmp", MountPath: templates.TmpPath},
		{Name: "grants", MountPath: protocol.GrantsDir},
		{Name: "api-token", MountPath: templates.APITokenDir, ReadOnly: true},
	}
	if s.Spec.Workspace != nil {
		for i := range env {
			if env[i].Name == templates.WorkspaceIDEnv {
				env[i].Value = t.Workspace.ID
			}
		}
		volumes = append(volumes, corev1.Volume{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: t.Workspace.Claim}}})
		for _, sub := range []string{"repos", "codex", "work"} {
			mounts = append(mounts, corev1.VolumeMount{Name: "workspace", MountPath: templates.HomePath + "/" + sub, SubPath: sub})
		}
		mounts = append(mounts, corev1.VolumeMount{Name: "workspace", MountPath: templates.WorkspaceMetadataPath, SubPath: "metadata"})
	}
	templateMounts := append([]templates.Mount{}, t.Mounts...)
	for _, m := range profile.Mounts {
		if !hold || m.Path == rescueGitHubMountPath {
			templateMounts = append(templateMounts, m)
		}
	}
	for _, m := range templateMounts {
		volumes = append(volumes, templateVolume(m))
		mounts = append(mounts, corev1.VolumeMount{Name: m.Name, MountPath: m.Path, ReadOnly: true})
	}

	resources := corev1.ResourceRequirements{Requests: size.Requests.DeepCopy(), Limits: size.Limits.DeepCopy()}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            s.Name,
			Namespace:       s.Namespace,
			Labels:          labels,
			Annotations:     sessionAnnotations(s),
			OwnerReferences: []metav1.OwnerReference{ownerRef(s)},
		},
		Spec: corev1.PodSpec{
			// A crashed agentd restarts in place: same pod, same UID, same
			// node and volume. Nothing ever replaces a live pod (5.1).
			RestartPolicy:                 corev1.RestartPolicyAlways,
			TerminationGracePeriodSeconds: ptr.To[int64](TerminationGracePeriod),
			ServiceAccountName:            ServiceAccountName,
			AutomountServiceAccountToken:  ptr.To(true),
			PriorityClassName:             PriorityClassName,
			EnableServiceLinks:            ptr.To(false),
			// The DNS allowlist refuses search-expanded names (3.6).
			DNSConfig: &corev1.PodDNSConfig{Options: []corev1.PodDNSConfigOption{{Name: "ndots", Value: ptr.To("1")}}},
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:        ptr.To(true),
				RunAsUser:           ptr.To[int64](uid),
				RunAsGroup:          ptr.To[int64](uid),
				FSGroup:             ptr.To[int64](uid),
				FSGroupChangePolicy: ptr.To(corev1.FSGroupChangeOnRootMismatch),
				SeccompProfile:      &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Affinity:                  placement(),
			TopologySpreadConstraints: spread(),
			Tolerations: []corev1.Toleration{
				{Key: corev1.TaintNodeNotReady, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: ptr.To[int64](tolerationSeconds)},
				{Key: corev1.TaintNodeUnreachable, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: ptr.To[int64](tolerationSeconds)},
			},
			Volumes: volumes,
			Containers: []corev1.Container{{
				Name:            ContainerName,
				Image:           t.Image,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Args:            args,
				Env:             env,
				EnvFrom:         envFrom,
				Resources:       resources,
				VolumeMounts:    mounts,
				// No probes. A liveness probe would restart the agent CLI
				// under load, the failure 5.1 forbids; the pod has no
				// Service, so readiness gates nothing. agentd's heartbeat
				// reports the agent's state (4.2).
				TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
				SecurityContext: &corev1.SecurityContext{
					RunAsNonRoot:             ptr.To(true),
					ReadOnlyRootFilesystem:   ptr.To(true),
					AllowPrivilegeEscalation: ptr.To(false),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
	if s.Spec.Workspace != nil && !hold {
		pod.Spec.RestartPolicy = corev1.RestartPolicyNever
	}
	return pod, nil
}

// staticTokenFor reports whether the pod gets the static Claude token: Claude in
// task or local mode (DESIGN-001 6.1). Remote pods run on the keeper's access
// token and must not have it (6.2, "Environment rules").
func staticTokenFor(s *v1alpha1.AgentSession) bool {
	return s.Spec.Agent == v1alpha1.AgentClaude &&
		(s.Spec.Mode == v1alpha1.ModeTask || s.Spec.Mode == v1alpha1.ModeLocal)
}

func templateVolume(m templates.Mount) corev1.Volume {
	v := corev1.Volume{Name: m.Name}
	if m.Secret != "" {
		v.Secret = &corev1.SecretVolumeSource{SecretName: m.Secret, DefaultMode: m.DefaultMode}
	} else {
		v.ConfigMap = &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: m.ConfigMap},
			DefaultMode:          m.DefaultMode,
		}
	}
	return v
}

// placement is D-20: workers only (required), GPU nodes last (preferred).
func placement() *corev1.Affinity {
	return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{
				{Key: ZoneLabel, Operator: corev1.NodeSelectorOpIn, Values: []string{WorkerZone}},
			}}},
		},
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{
			Weight: 30,
			Preference: corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
				{Key: GPULabel, Operator: corev1.NodeSelectorOpNotIn, Values: []string{"true"}},
			}},
		}},
	}}
}

// spread is D-20's soft spread over the workers.
func spread() []corev1.TopologySpreadConstraint {
	return []corev1.TopologySpreadConstraint{{
		MaxSkew:           2,
		TopologyKey:       corev1.LabelHostname,
		WhenUnsatisfiable: corev1.ScheduleAnyway,
		LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{v1alpha1.LabelAppName: v1alpha1.AppNameSession}},
	}}
}

// sessionLabels are the labels a session's pod and volume share.
func sessionLabels(s *v1alpha1.AgentSession, profile string) map[string]string {
	size := s.Spec.Size
	if size == "" {
		size = v1alpha1.SizeM
	}
	l := map[string]string{
		v1alpha1.LabelAppName:   v1alpha1.AppNameSession,
		v1alpha1.LabelManagedBy: v1alpha1.ManagedByOperator,
		v1alpha1.LabelSession:   s.Name,
		v1alpha1.LabelAgent:     string(s.Spec.Agent),
		v1alpha1.LabelMode:      string(s.Spec.Mode),
		v1alpha1.LabelSize:      string(size),
		v1alpha1.LabelProfile:   profile,
	}
	if s.Spec.Lane != "" {
		l[v1alpha1.LabelLane] = string(s.Spec.Lane)
	}
	return l
}

func sessionAnnotations(s *v1alpha1.AgentSession) map[string]string {
	a := map[string]string{v1alpha1.AnnotationRepo: s.Spec.Repo}
	if s.Spec.Caller != "" {
		a[v1alpha1.AnnotationCaller] = s.Spec.Caller
	}
	return a
}

// ownerRef makes the session the controller of its pod and volume (D-03). It is
// the only owner reference the operator ever writes: never one to its own
// Deployment, ReplicaSet or pod (5.1).
func ownerRef(s *v1alpha1.AgentSession) metav1.OwnerReference {
	return *metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))
}

// sessionInvalidError marks a session agentd would refuse. Spec is fixed at
// create (D-39), so such a session can never start: it fails instead of waiting.
type sessionInvalidError struct{ err error }

func (e sessionInvalidError) Error() string { return e.err.Error() }
func (e sessionInvalidError) Unwrap() error { return e.err }

// CheckAgentdSession reports whether agentd would accept the session, by the same
// code that builds its pod. The /v1 API calls it before a create (D-46), so a
// session that could never start is refused to its caller instead of failing
// later.
func CheckAgentdSession(s *v1alpha1.AgentSession) error {
	_, err := sessionDocument(s)
	return err
}

// sessionDocument is AGENTD_SESSION's value (D-40): the session's name and the
// spec fields agentd needs, checked with agentd's own rules, so the operator never
// starts a pod whose agentd would refuse its session at boot.
func sessionDocument(s *v1alpha1.AgentSession) (string, error) {
	d := protocol.Session{
		SessionUID: string(s.UID),
		Name:       s.Name,
		Repo:       s.Spec.Repo,
		Base:       s.Spec.Base,
		Agent:      string(s.Spec.Agent),
		Mode:       string(s.Spec.Mode),
		Model:      s.Spec.Model,
		Effort:     s.Spec.Effort,
		Prompt:     s.Spec.Prompt,
		// The rescue the session restores from (D-67).
		Restore: s.Spec.Restore,
	}
	if raw := s.Annotations[v1alpha1.AnnotationProjectSnapshot]; raw != "" {
		snapshot, err := projectcatalog.ParseSnapshot([]byte(raw))
		if err != nil || snapshot.Selected().Name != s.Spec.Repo || snapshot.Selected().DefaultBranch != s.Spec.Base {
			return "", sessionInvalidError{fmt.Errorf("project snapshot does not bind the selected repository and base")}
		}
		d.ProjectSnapshot = json.RawMessage(raw)
	}
	if s.Spec.Workspace != nil {
		d.Workspace = &protocol.WorkspaceBinding{ID: s.Spec.Workspace.ID, SessionUID: string(s.UID)}
	}
	if l := s.Spec.Limits; l != nil {
		d.Limits = &protocol.Limits{MaxTurns: l.MaxTurns}
		if l.Timeout != nil {
			d.Limits.Timeout = l.Timeout.Duration.String()
		}
	}
	if err := d.Validate(); err != nil {
		return "", sessionInvalidError{fmt.Errorf("agentd would refuse this session: %w", err)}
	}
	b, err := json.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("session document: %w", err)
	}
	if len(b) > protocol.MaxSessionBytes {
		return "", sessionInvalidError{fmt.Errorf("session document exceeds the bounded transport")}
	}
	return string(b), nil
}
