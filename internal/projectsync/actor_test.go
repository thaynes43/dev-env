package projectsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

const testCatalog = "{\"version\":1,\"repositories\":{\"demo\":{\"github\":\"thaynes43/demo\"}},\"projects\":{\"sample\":{\"repositories\":[{\"name\":\"demo\"}],\"rules\":\"Exact rules.\"}}}"

type fakeReader struct {
	pod                    *corev1.Pod
	job                    *batchv1.Job
	catalog                *corev1.ConfigMap
	podReads, catalogReads int
	onPod                  func(int)
}

func (r *fakeReader) Pod(_ context.Context, namespace, name string) (*corev1.Pod, error) {
	r.podReads++
	if r.onPod != nil {
		r.onPod(r.podReads)
	}
	if namespace != r.pod.Namespace || name != r.pod.Name {
		return nil, errors.New("unexpected Pod read")
	}
	return r.pod.DeepCopy(), nil
}
func (r *fakeReader) Job(_ context.Context, namespace, name string) (*batchv1.Job, error) {
	if namespace != r.job.Namespace || name != r.job.Name {
		return nil, errors.New("unexpected Job read")
	}
	return r.job.DeepCopy(), nil
}
func (r *fakeReader) Catalog(_ context.Context, namespace, name string) (*corev1.ConfigMap, error) {
	r.catalogReads++
	if namespace != r.catalog.Namespace || name != r.catalog.Name {
		return nil, errors.New("unexpected catalog read")
	}
	return r.catalog.DeepCopy(), nil
}

type forbiddenRunner struct{}

func (forbiddenRunner) Run(context.Context, agentd.Cmd) (agentd.Result, error) {
	return agentd.Result{}, errors.New("unexpected process")
}
func (forbiddenRunner) LookPath(string) (string, error) { return "", errors.New("unexpected process") }

type fixtureRunner struct {
	run func(context.Context, agentd.Cmd) (agentd.Result, error)
}

func (r fixtureRunner) Run(ctx context.Context, command agentd.Cmd) (agentd.Result, error) {
	return r.run(ctx, command)
}
func (fixtureRunner) LookPath(string) (string, error) { return "", errors.New("unexpected lookup") }

func actorFixture(t *testing.T) (actor, *fakeReader, *int) {
	t.Helper()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	config := Config{Enabled: true, Namespace: "dev-agents", CatalogNamespace: "dev-env-system", CatalogName: "dev-env-project-catalog", WorkspaceID: "workspace-1", PodName: "project-sync-1", PodUID: "pod-1", CloneOwner: "thaynes43"}
	home := t.TempDir()
	settings := agentd.Settings{Home: home, StateDir: filepath.Join(home, ".agentd"), WorkspaceID: config.WorkspaceID, PodUID: string(config.PodUID)}
	args := []string{"project-sync", "--enabled", "--namespace", config.Namespace, "--accepted-catalog", config.CatalogNamespace + "/" + config.CatalogName, "--workspace-id", config.WorkspaceID, "--project-clone-owner", config.CloneOwner, "--github-token-file", GitHubTokenFile, "--kube-token-file", APITokenFile, "--kube-ca-file", APICAFile}
	spec := corev1.PodSpec{ServiceAccountName: ServiceAccount, RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: ptr.To(false),
		SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To[int64](1000), RunAsGroup: ptr.To[int64](1000), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Containers:      []corev1.Container{{Name: "project-sync", Image: "ghcr.io/thaynes43/dev-env:fixture-reviewed-pin", Command: []string{"/usr/local/bin/tini", "--", "/usr/local/bin/agentd"}, Args: args, Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")}}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}}},
		Volumes: []corev1.Volume{
			{Name: "home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: PrivateHomeClaim}}},
			{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "workspace-retained"}}},
			{Name: "github", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "dev-env-gh-token", Items: []corev1.KeyToPath{{Key: "gh_token", Path: "gh_token"}}}}},
			{Name: "api", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{
				{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token", ExpirationSeconds: ptr.To[int64](3600)}},
				{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"}, Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}},
			}}}},
		},
	}
	spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "home", MountPath: home}, {Name: "workspace", MountPath: filepath.Join(home, ".workspace"), SubPath: "metadata"}, {Name: "workspace", MountPath: settings.ReposDir(), SubPath: "repos"}, {Name: "workspace", MountPath: filepath.Join(home, "codex"), SubPath: "codex"}, {Name: "workspace", MountPath: settings.WorkDir(), SubPath: "work"}, {Name: "github", MountPath: "/creds", ReadOnly: true}, {Name: "api", MountPath: filepath.Dir(APITokenFile), ReadOnly: true}}
	for name, field := range map[string]string{"DEV_ENV_POD_NAME": "metadata.name", "DEV_ENV_POD_UID": "metadata.uid", "DEV_ENV_POD_NAMESPACE": "metadata.namespace"} {
		spec.Containers[0].Env = append(spec.Containers[0].Env, corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: field}}})
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "project-sync-run-1", Namespace: config.Namespace, UID: "job-1"}, Spec: batchv1.JobSpec{BackoffLimit: ptr.To[int32](0), Parallelism: ptr.To[int32](1), Completions: ptr.To[int32](1), ActiveDeadlineSeconds: ptr.To[int64](600), Template: corev1.PodTemplateSpec{Spec: spec}}, Status: batchv1.JobStatus{StartTime: ptr.To(metav1.NewTime(now))}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: config.PodName, Namespace: config.Namespace, UID: config.PodUID, OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true)}}}, Spec: *spec.DeepCopy()}
	reader := &fakeReader{pod: pod, job: job, catalog: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: config.CatalogName, Namespace: config.CatalogNamespace, UID: "catalog-1", ResourceVersion: "7"}, Data: map[string]string{"catalog.json": testCatalog}}}
	syncs := new(int)
	a := actor{config: config, settings: settings, reader: reader, runner: forbiddenRunner{}, now: func() time.Time { return now }, checkMounts: func(agentd.Settings) error { return nil }, prepareStorage: func(agentd.Settings, bool) error { return nil }, checkToken: func() error { return nil }, prepareGit: func(r agentd.Runner, _ agentd.Settings, _, _ string) (agentd.Runner, error) { return r, nil }}
	a.sync = func(ctx context.Context, r agentd.Runner, s agentd.Settings, c *projectcatalog.Catalog, options agentd.ProjectSyncOptions) (agentd.ProjectSyncReport, error) {
		*syncs++
		if options.BeforeMutation == nil || options.BeforeMutation(ctx) != nil || options.AcceptedCatalog == nil {
			t.Fatal("fixed actor omitted its mandatory live fence")
		}
		if _, err := options.AcceptedCatalog.Read(ctx); err != nil {
			return agentd.ProjectSyncReport{}, err
		}
		return agentd.ProjectSyncReport{CatalogRevision: c.Revision(), Findings: []agentd.ProjectFinding{{Path: filepath.Join(s.Home, "codex", "sample"), State: "materialized", Detail: "confirmed"}}}, nil
	}
	return a, reader, syncs
}

func TestJobActorRefusesForeignAndUnsafeResourcesBeforeMutation(t *testing.T) {
	for _, mode := range []string{"disabled", "PodUID", "JobUID", "deleting", "SA", "legacy home", "provider secret", "operator audience", "unproven audience", "unbounded CPU", "retry", "template", "command", "empty command", "other command", "empty image", "changed image", "different Pod identity", "different container security", "root identity", "missing seccomp", "privileged", "escalation", "writable root", "added capability", "missing drop", "lifecycle launcher", "probe launcher", "host namespaces", "shared processes", "duplicate identity", "deadline", "catalog owner"} {
		t.Run(mode, func(t *testing.T) {
			a, r, syncs := actorFixture(t)
			switch mode {
			case "disabled":
				a.config.Enabled = false
			case "PodUID":
				r.pod.UID = "replacement-pod"
			case "JobUID":
				r.job.UID = "replacement-job"
			case "deleting":
				r.pod.DeletionTimestamp = ptr.To(metav1.NewTime(a.now()))
			case "SA":
				r.pod.Spec.ServiceAccountName = "dev-env-agent"
			case "legacy home":
				r.pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = "old-session-home"
			case "provider secret":
				r.pod.Spec.Volumes[2].Secret.SecretName = "dev-env-claude-live"
			case "operator audience":
				r.pod.Spec.Volumes[3].Projected.Sources[0].ServiceAccountToken.Audience = "dev-env-operator"
			case "unproven audience":
				r.pod.Spec.Volumes[3].Projected.Sources[0].ServiceAccountToken.Audience = "https://kubernetes.default.svc"
			case "unbounded CPU":
				delete(r.pod.Spec.Containers[0].Resources.Limits, corev1.ResourceCPU)
			case "retry":
				r.job.Spec.BackoffLimit = ptr.To[int32](1)
			case "template":
				r.job.Spec.Template.Spec.Containers[0].VolumeMounts[1].ReadOnly = true
			case "command":
				r.pod.Spec.Containers[0].Args = []string{"run"}
			case "empty command":
				r.pod.Spec.Containers[0].Command = nil
			case "other command":
				r.pod.Spec.Containers[0].Command = []string{"/bin/sh", "-c"}
			case "empty image":
				r.pod.Spec.Containers[0].Image = ""
			case "changed image":
				r.pod.Spec.Containers[0].Image = "another-image:fixture"
			case "different Pod identity":
				r.pod.Spec.SecurityContext.RunAsUser = ptr.To[int64](2000)
			case "different container security":
				r.pod.Spec.Containers[0].SecurityContext.RunAsGroup = ptr.To[int64](2000)
			case "root identity":
				r.pod.Spec.SecurityContext.RunAsUser = ptr.To[int64](0)
			case "missing seccomp":
				r.pod.Spec.SecurityContext.SeccompProfile = nil
			case "privileged":
				r.pod.Spec.Containers[0].SecurityContext.Privileged = ptr.To(true)
			case "escalation":
				r.pod.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation = ptr.To(true)
			case "writable root":
				r.pod.Spec.Containers[0].SecurityContext.ReadOnlyRootFilesystem = ptr.To(false)
			case "added capability":
				r.pod.Spec.Containers[0].SecurityContext.Capabilities.Add = []corev1.Capability{"SYS_ADMIN"}
			case "missing drop":
				r.pod.Spec.Containers[0].SecurityContext.Capabilities.Drop = nil
			case "lifecycle launcher":
				r.pod.Spec.Containers[0].Lifecycle = &corev1.Lifecycle{PostStart: &corev1.LifecycleHandler{Exec: &corev1.ExecAction{Command: []string{"claude"}}}}
			case "probe launcher":
				r.pod.Spec.Containers[0].LivenessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"codex"}}}}
			case "host namespaces":
				r.pod.Spec.HostPID = true
			case "shared processes":
				r.pod.Spec.ShareProcessNamespace = ptr.To(true)
			case "duplicate identity":
				r.pod.Spec.Containers[0].Env = append(r.pod.Spec.Containers[0].Env, r.pod.Spec.Containers[0].Env[0])
			case "deadline":
				r.job.Status.StartTime = ptr.To(metav1.NewTime(a.now().Add(-Budget)))
			case "catalog owner":
				r.catalog.Data["catalog.json"] = "{\"version\":1,\"repositories\":{\"demo\":{\"github\":\"foreign/demo\"}},\"projects\":{}}"
			}
			a.prepareStorage = func(agentd.Settings, bool) error { t.Fatal("refusal mutated shared storage"); return nil }
			if _, err := a.run(context.Background()); err == nil || *syncs != 0 {
				t.Fatalf("unsafe actor admitted %s: %v", mode, err)
			}
			if _, err := os.Stat(filepath.Join(a.settings.StateDir, "project-sync", "job-1", "receipt.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("pre-admission refusal recorded work as started")
			}
		})
	}
}

func TestJobActorPreservesAcceptedRepositoryAlias(t *testing.T) {
	a, r, syncs := actorFixture(t)
	r.catalog.Data["catalog.json"] = "{\"version\":1,\"repositories\":{\"demo\":{\"github\":\"thaynes43/actual-source\"}},\"projects\":{\"sample\":{\"repositories\":[{\"name\":\"demo\"}],\"rules\":\"Exact rules.\"}}}"
	result, err := a.run(context.Background())
	if err != nil || *syncs != 1 || result.State != "Terminal" {
		t.Fatalf("accepted alias refused: %v %+v", err, result)
	}
}

func TestJobActorStartedLostACKAndUnknownReceiptNeverReplay(t *testing.T) {
	a, _, syncs := actorFixture(t)
	writer := writeReceipt
	t.Cleanup(func() { writeReceipt = writer })
	writeReceipt = func(path string, r Result, exclusive bool) error {
		if err := writer(path, r, exclusive); err != nil {
			return err
		}
		return errors.New("fixture lost write ACK")
	}
	if _, err := a.run(context.Background()); err == nil || *syncs != 0 {
		t.Fatal("lost Started ACK admitted work")
	}
	writeReceipt = writer
	if _, err := a.run(context.Background()); err == nil || *syncs != 0 {
		t.Fatal("Started operation was replayed")
	}
	path := filepath.Join(a.settings.StateDir, "project-sync", "job-1", "receipt.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.run(context.Background()); err == nil || *syncs != 0 {
		t.Fatal("unknown operation evidence was replayed")
	}
}

func TestJobActorTerminalLostACKOnlyReturnsExactSavedResult(t *testing.T) {
	a, r, syncs := actorFixture(t)
	writer := writeReceipt
	t.Cleanup(func() { writeReceipt = writer })
	writeReceipt = func(path string, result Result, exclusive bool) error {
		if err := writer(path, result, exclusive); err != nil {
			return err
		}
		if !exclusive {
			return errors.New("fixture lost terminal ACK")
		}
		return nil
	}
	if _, err := a.run(context.Background()); err == nil || *syncs != 1 {
		t.Fatal("unconfirmed terminal receipt claimed success")
	}
	writeReceipt = writer
	original := a.now()
	a.now = func() time.Time { return original.Add(2 * Budget) }
	result, err := a.run(context.Background())
	if err != nil || result.State != "Terminal" || *syncs != 1 || !result.Deadline.Equal(original.Add(Budget)) {
		t.Fatalf("saved result replayed or renewed its deadline: %v %+v", err, result)
	}
	r.pod.UID = types.UID("new-pod")
	if _, err := a.run(context.Background()); err == nil || *syncs != 1 {
		t.Fatal("replacement Pod recovered another actor's receipt")
	}
}

func TestJobActorRechecksUIDBeforeSharedPreparation(t *testing.T) {
	for _, read := range []int{2, 3} {
		t.Run(string(rune('0'+read)), func(t *testing.T) {
			a, r, syncs := actorFixture(t)
			r.onPod = func(n int) {
				if n == read {
					r.pod.UID = "new-pod"
				}
			}
			a.prepareStorage = func(agentd.Settings, bool) error { t.Fatal("replaced Pod mutated shared storage"); return nil }
			if _, err := a.run(context.Background()); err == nil || *syncs != 0 {
				t.Fatal("replacement identity admitted a writer")
			}
		})
	}
}

func TestJobActorRechecksAfterStorageBeforeCredentials(t *testing.T) {
	a, r, syncs := actorFixture(t)
	prepared := false
	a.prepareStorage = func(agentd.Settings, bool) error { prepared = true; return nil }
	r.onPod = func(n int) {
		if n == 4 {
			r.pod.UID = "replacement-after-storage"
		}
	}
	a.checkToken = func() error { t.Fatal("replaced actor inspected credentials"); return nil }
	a.prepareGit = func(agentd.Runner, agentd.Settings, string, string) (agentd.Runner, error) {
		t.Fatal("replaced actor prepared a credential helper")
		return nil, nil
	}
	if _, err := a.run(context.Background()); err == nil || !prepared || *syncs != 0 {
		t.Fatal("storage-stage identity replacement admitted credential use")
	}
}

func TestJobActorRechecksBeforeEachGitProcess(t *testing.T) {
	a, reader, _ := actorFixture(t)
	processes := 0
	a.runner = fixtureRunner{run: func(_ context.Context, command agentd.Cmd) (agentd.Result, error) {
		if command.Name != "git" {
			t.Fatal("sync attempted a model process")
		}
		processes++
		reader.pod.UID = "replacement-after-first-process"
		return agentd.Result{}, nil
	}}
	a.sync = func(ctx context.Context, runner agentd.Runner, _ agentd.Settings, _ *projectcatalog.Catalog, _ agentd.ProjectSyncOptions) (agentd.ProjectSyncReport, error) {
		if _, err := runner.Run(ctx, agentd.Cmd{Name: "git", Args: []string{"status"}}); err != nil {
			t.Fatal(err)
		}
		_, err := runner.Run(ctx, agentd.Cmd{Name: "git", Args: []string{"fetch"}})
		if err == nil {
			t.Fatal("replaced actor started another Git process")
		}
		return agentd.ProjectSyncReport{}, err
	}
	if _, err := a.run(context.Background()); err == nil || processes != 1 {
		t.Fatal("Git process identity replacement admitted more work")
	}
	receipt, err := readReceipt(filepath.Join(a.settings.StateDir, "project-sync", "job-1", "receipt.json"))
	if err != nil || receipt.State != "Started" {
		t.Fatal("unconfirmed actor rewrote its durable operation result")
	}
}

func TestJobActorEarlyFailurePrecedesPreservedFindingsAndRecoversExactly(t *testing.T) {
	a, _, syncs := actorFixture(t)
	a.sync = func(context.Context, agentd.Runner, agentd.Settings, *projectcatalog.Catalog, agentd.ProjectSyncOptions) (agentd.ProjectSyncReport, error) {
		*syncs++
		return agentd.ProjectSyncReport{Findings: []agentd.ProjectFinding{{Path: "prepared-anchor", State: "preserved", Detail: "kept previous work"}}}, errors.New("fixture stopped before later projects")
	}
	result, err := a.run(context.Background())
	if err == nil || result.State != "Terminal" || result.Failure != "sync stopped with preserved partial work" || len(result.Report.Findings) != 1 || *syncs != 1 {
		t.Fatalf("early failure was reported as completion: %+v, %v", result, err)
	}
	recovered, err := a.run(context.Background())
	if err == nil || recovered.Failure != result.Failure || recovered.Report.Findings[0] != result.Report.Findings[0] || *syncs != 1 {
		t.Fatal("terminal early-failure recovery changed or replayed the saved result")
	}
}
