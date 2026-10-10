// Package projectsync runs bounded, model-free GitOps project management. It
// has no AgentSession, provider, enrollment or general Kubernetes capability.
package projectsync

import (
	"context"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/internal/agentd"
)

const (
	ServiceAccount   = "dev-env-project-sync"
	PrivateHomeClaim = "dev-env-project-sync-home"
	GitHubTokenFile  = "/creds/gh_token"
	APITokenFile     = "/var/run/secrets/project-sync/token"
	APICAFile        = "/var/run/secrets/project-sync/ca.crt"
	Budget           = 10 * time.Minute
)

type Reader interface {
	Pod(context.Context, string, string) (*corev1.Pod, error)
	Job(context.Context, string, string) (*batchv1.Job, error)
	Catalog(context.Context, string, string) (*corev1.ConfigMap, error)
}

type Config struct {
	Enabled                                               bool
	Namespace, CatalogNamespace, CatalogName, WorkspaceID string
	CloneOwner                                            string
	PodName                                               string
	PodUID                                                types.UID
	Initialize                                            bool
}

type binding struct {
	reader   Reader
	config   Config
	settings agentd.Settings
	jobName  string
	jobUID   types.UID
	deadline time.Time
}

func bind(ctx context.Context, reader Reader, config Config, settings agentd.Settings, now time.Time) (*binding, error) {
	b := &binding{reader: reader, config: config, settings: settings}
	job, err := b.read(ctx)
	if err != nil {
		return nil, err
	}
	if job.Status.StartTime == nil || job.Status.StartTime.After(now) {
		return nil, errors.New("sync Job start time is unconfirmed")
	}
	b.deadline = job.Status.StartTime.Add(time.Duration(*job.Spec.ActiveDeadlineSeconds) * time.Second)
	return b, nil
}

func (b *binding) confirm(ctx context.Context) error {
	_, err := b.read(ctx)
	return err
}

func (b *binding) read(ctx context.Context) (*batchv1.Job, error) {
	pod, err := b.reader.Pod(ctx, b.config.Namespace, b.config.PodName)
	if err != nil || pod == nil || pod.Namespace != b.config.Namespace || pod.Name != b.config.PodName || pod.UID != b.config.PodUID || pod.DeletionTimestamp != nil {
		return nil, errors.New("live sync Pod identity is unconfirmed")
	}
	if len(pod.OwnerReferences) != 1 {
		return nil, errors.New("sync Pod requires its exact Job controller")
	}
	owner := pod.OwnerReferences[0]
	if owner.APIVersion != "batch/v1" || owner.Kind != "Job" || owner.Name == "" || owner.UID == "" || owner.Controller == nil || !*owner.Controller || (b.jobUID != "" && (owner.UID != b.jobUID || owner.Name != b.jobName)) {
		return nil, errors.New("sync Pod Job owner is unconfirmed or replaced")
	}
	job, err := b.reader.Job(ctx, b.config.Namespace, owner.Name)
	if err != nil || job == nil || job.Namespace != b.config.Namespace || job.Name != owner.Name || job.UID != owner.UID || job.DeletionTimestamp != nil {
		return nil, errors.New("live sync Job identity is unconfirmed")
	}
	if job.Spec.Suspend != nil && *job.Spec.Suspend || job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 || job.Spec.Parallelism == nil || *job.Spec.Parallelism != 1 || job.Spec.Completions == nil || *job.Spec.Completions != 1 || job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds <= 0 || *job.Spec.ActiveDeadlineSeconds > int64(Budget/time.Second) {
		return nil, errors.New("sync Job must have one attempt and a finite original deadline")
	}
	if !b.deadline.IsZero() && (job.Status.StartTime == nil || !job.Status.StartTime.Add(time.Duration(*job.Spec.ActiveDeadlineSeconds)*time.Second).Equal(b.deadline)) {
		return nil, errors.New("sync Job original deadline changed")
	}
	if err := verifySpec(pod.Spec, b.settings, b.config); err != nil {
		return nil, err
	}
	if err := verifySpec(job.Spec.Template.Spec, b.settings, b.config); err != nil {
		return nil, err
	}
	podContainer, jobContainer := pod.Spec.Containers[0], job.Spec.Template.Spec.Containers[0]
	if podContainer.Image == "" || podContainer.Image != jobContainer.Image || !reflect.DeepEqual(pod.Spec.SecurityContext, job.Spec.Template.Spec.SecurityContext) || !reflect.DeepEqual(podContainer.SecurityContext, jobContainer.SecurityContext) {
		return nil, errors.New("sync Pod image or security differs from its accepted Job template")
	}
	if !reflect.DeepEqual(pod.Spec.Containers[0].VolumeMounts, job.Spec.Template.Spec.Containers[0].VolumeMounts) || !reflect.DeepEqual(pod.Spec.Volumes, job.Spec.Template.Spec.Volumes) {
		return nil, errors.New("sync Pod mounts differ from its accepted Job template")
	}
	b.jobName, b.jobUID = owner.Name, owner.UID
	return job, nil
}

func verifySpec(spec corev1.PodSpec, s agentd.Settings, config Config) error {
	if spec.ServiceAccountName != ServiceAccount || spec.RestartPolicy != corev1.RestartPolicyNever || spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken || len(spec.Containers) != 1 || len(spec.InitContainers) != 0 || len(spec.EphemeralContainers) != 0 || spec.HostNetwork || spec.HostPID || spec.HostIPC || spec.ShareProcessNamespace != nil && *spec.ShareProcessNamespace {
		return errors.New("sync actor requires its fixed ServiceAccount and one model-free container")
	}
	c := spec.Containers[0]
	if err := verifySecurity(spec.SecurityContext, c.SecurityContext); err != nil {
		return err
	}
	if !reflect.DeepEqual(c.Command, []string{"/usr/local/bin/tini", "--", "/usr/local/bin/agentd"}) || len(c.Args) == 0 || c.Args[0] != "project-sync" {
		return errors.New("sync Job must run only the fixed project-sync command")
	}
	if err := verifyArguments(c.Args[1:], config); err != nil {
		return err
	}
	if c.Name != "project-sync" || c.Lifecycle != nil || c.LivenessProbe != nil || c.ReadinessProbe != nil || c.StartupProbe != nil || len(c.EnvFrom) != 0 || len(c.Resources.Limits) == 0 || c.Resources.Limits.Cpu().Sign() <= 0 || c.Resources.Limits.Cpu().MilliValue() > 2000 || c.Resources.Limits.Memory().Sign() <= 0 {
		return errors.New("sync container must have bounded CPU and memory and no inherited credentials")
	}
	environment := map[string]bool{}
	for _, env := range c.Env {
		if environment[env.Name] {
			return errors.New("sync environment identity is ambiguous")
		}
		environment[env.Name] = true
		if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
			return errors.New("sync actor must not import provider credentials")
		}
	}
	volumes := map[string]corev1.Volume{}
	for _, v := range spec.Volumes {
		if _, exists := volumes[v.Name]; exists {
			return errors.New("sync volume identity is ambiguous")
		}
		volumes[v.Name] = v
		if v.PersistentVolumeClaim == nil && v.EmptyDir == nil && v.Secret == nil && v.Projected == nil {
			return errors.New("sync actor has an unsupported mount source")
		}
		if v.Secret != nil && (v.Secret.SecretName != "dev-env-gh-token" || len(v.Secret.Items) != 1 || v.Secret.Items[0].Key != "gh_token" || v.Secret.Items[0].Path != "gh_token") {
			return errors.New("sync actor may mount only the keeper GitHub token")
		}
		if v.Projected != nil {
			if len(v.Projected.Sources) != 2 {
				return errors.New("sync requires only an explicit API identity and public CA projection")
			}
			var token, ca bool
			for _, source := range v.Projected.Sources {
				if source.ServiceAccountToken != nil && source.ServiceAccountToken.Path == "token" && source.ServiceAccountToken.Audience == "" && source.ServiceAccountToken.ExpirationSeconds != nil && *source.ServiceAccountToken.ExpirationSeconds <= 3600 && *source.ServiceAccountToken.ExpirationSeconds >= 600 {
					token = true
				} else if source.ConfigMap != nil && source.ConfigMap.Name == "kube-root-ca.crt" && len(source.ConfigMap.Items) == 1 && source.ConfigMap.Items[0].Key == "ca.crt" && source.ConfigMap.Items[0].Path == "ca.crt" {
					ca = true
				} else {
					return errors.New("sync API projection is not its explicit identity and public CA")
				}
			}
			if !token || !ca {
				return errors.New("sync API projection is incomplete")
			}
		}
	}
	mounts := map[string]corev1.VolumeMount{}
	for _, m := range c.VolumeMounts {
		if _, exists := mounts[m.MountPath]; exists || m.SubPathExpr != "" {
			return errors.New("sync mount binding is ambiguous")
		}
		mounts[m.MountPath] = m
		v, ok := volumes[m.Name]
		if !ok {
			return errors.New("sync mount source is missing")
		}
		allowed := m.MountPath == s.Home || m.MountPath == filepath.Join(s.Home, ".workspace") || m.MountPath == s.ReposDir() || m.MountPath == filepath.Join(s.Home, "codex") || m.MountPath == s.WorkDir() || m.MountPath == "/creds" || m.MountPath == filepath.Dir(APITokenFile) || (m.MountPath == "/tmp" && v.EmptyDir != nil)
		if !allowed {
			return errors.New("sync actor must not mount provider state or configuration")
		}
	}
	private, ok := mounts[s.Home]
	privateVolume := volumes[private.Name]
	if !ok || private.ReadOnly || private.SubPath != "" || privateVolume.PersistentVolumeClaim == nil || privateVolume.PersistentVolumeClaim.ReadOnly || privateVolume.PersistentVolumeClaim.ClaimName != PrivateHomeClaim {
		return errors.New("sync requires its dedicated writable private home")
	}
	var workspaceName, workspaceClaim string
	for path, sub := range map[string]string{filepath.Join(s.Home, ".workspace"): "metadata", s.ReposDir(): "repos", filepath.Join(s.Home, "codex"): "codex", s.WorkDir(): "work"} {
		m, ok := mounts[path]
		v := volumes[m.Name]
		if !ok || m.ReadOnly || m.SubPath != sub || v.PersistentVolumeClaim == nil || v.PersistentVolumeClaim.ReadOnly || v.PersistentVolumeClaim.ClaimName == "" || v.PersistentVolumeClaim.ClaimName == privateVolume.PersistentVolumeClaim.ClaimName {
			return errors.New("sync requires separate literal writable shared subpaths")
		}
		if workspaceName != "" && (workspaceName != m.Name || workspaceClaim != v.PersistentVolumeClaim.ClaimName) {
			return errors.New("sync shared subpaths must use the same retained claim")
		}
		workspaceName, workspaceClaim = m.Name, v.PersistentVolumeClaim.ClaimName
	}
	for path, kind := range map[string]string{"/creds": "github", filepath.Dir(APITokenFile): "api"} {
		m, ok := mounts[path]
		v := volumes[m.Name]
		if !ok || !m.ReadOnly || m.SubPath != "" || kind == "github" && v.Secret == nil || kind == "api" && v.Projected == nil {
			return errors.New("sync requires exact read-only credential projections")
		}
	}
	// The downward API values are identity carriers; the live read above supplies
	// authority. Reject a Job that supplies these as arbitrary literal strings.
	for name, field := range map[string]string{"DEV_ENV_POD_NAME": "metadata.name", "DEV_ENV_POD_UID": "metadata.uid", "DEV_ENV_POD_NAMESPACE": "metadata.namespace"} {
		found := false
		for _, env := range c.Env {
			if env.Name == name {
				found = env.Value == "" && env.ValueFrom != nil && env.ValueFrom.FieldRef != nil && env.ValueFrom.FieldRef.FieldPath == field
			}
		}
		if !found {
			return errors.New("sync requires explicit downward Pod identity")
		}
	}
	if !strings.HasPrefix(s.Home, "/") {
		return errors.New("sync private home is invalid")
	}
	return nil
}

func verifySecurity(pod *corev1.PodSecurityContext, container *corev1.SecurityContext) error {
	if pod == nil || pod.RunAsNonRoot == nil || !*pod.RunAsNonRoot || pod.RunAsUser == nil || *pod.RunAsUser <= 0 || pod.RunAsGroup == nil || *pod.RunAsGroup <= 0 || pod.SeccompProfile == nil || pod.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault || pod.SeccompProfile.LocalhostProfile != nil {
		return errors.New("sync Pod requires explicit nonroot identity and runtime-default seccomp")
	}
	if container == nil || container.AllowPrivilegeEscalation == nil || *container.AllowPrivilegeEscalation || container.ReadOnlyRootFilesystem == nil || !*container.ReadOnlyRootFilesystem || container.Privileged != nil && *container.Privileged || container.Capabilities == nil || len(container.Capabilities.Add) != 0 || !reflect.DeepEqual(container.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		return errors.New("sync container requires read-only root, no escalation and all capabilities dropped")
	}
	if container.RunAsNonRoot != nil && !*container.RunAsNonRoot || container.RunAsUser != nil && *container.RunAsUser <= 0 || container.RunAsGroup != nil && *container.RunAsGroup <= 0 || container.SeccompProfile != nil && (container.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault || container.SeccompProfile.LocalhostProfile != nil) || container.ProcMount != nil && *container.ProcMount != corev1.DefaultProcMount {
		return errors.New("sync container must preserve its nonroot and seccomp policy")
	}
	return nil
}

func verifyArguments(args []string, expected Config) error {
	fs := flag.NewFlagSet("project-sync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var enabled, initialize bool
	var namespace, catalog, workspace, owner, githubToken, apiToken, apiCA string
	fs.BoolVar(&enabled, "enabled", false, "")
	fs.BoolVar(&initialize, "initialize-new-workspace", false, "")
	fs.StringVar(&namespace, "namespace", "", "")
	fs.StringVar(&catalog, "accepted-catalog", "", "")
	fs.StringVar(&workspace, "workspace-id", "", "")
	fs.StringVar(&owner, "project-clone-owner", "", "")
	fs.StringVar(&githubToken, "github-token-file", "", "")
	fs.StringVar(&apiToken, "kube-token-file", "", "")
	fs.StringVar(&apiCA, "kube-ca-file", "", "")
	if fs.Parse(args) != nil || fs.NArg() != 0 || !enabled || initialize != expected.Initialize || namespace != expected.Namespace || catalog != expected.CatalogNamespace+"/"+expected.CatalogName || workspace != expected.WorkspaceID || owner != expected.CloneOwner || githubToken != GitHubTokenFile || apiToken != APITokenFile || apiCA != APICAFile {
		return errors.New("sync invocation differs from its fixed live Job command")
	}
	return nil
}

var cloneOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
