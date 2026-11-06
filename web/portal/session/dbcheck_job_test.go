package session

import (
	"os"
	"path"
	"slices"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/yaml"
)

// TestDBCheckDocJobMountsOnlyWhatItReads parses the Job example in
// docs/portal-db-check.md. It mounts exactly the files db-check reads (the two
// DB keys of the existing runtime Secret and the existing Postgres CA), no
// token, OIDC secret, session key or TLS key; and it runs once: a generated
// name, no TTL, no retry.
func TestDBCheckDocJobMountsOnlyWhatItReads(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/portal-db-check.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)
	start := strings.Index(text, "```yaml\n")
	if start < 0 || strings.Count(text, "```yaml\n") != 1 {
		t.Fatal("the how-to must hold exactly one yaml block, the Job")
	}
	body := text[start+len("```yaml\n"):]
	body = body[:strings.Index(body, "```")]
	var job batchv1.Job
	if err := yaml.UnmarshalStrict([]byte(body), &job); err != nil {
		t.Fatalf("Job example does not parse: %v", err)
	}
	spec, pod := job.Spec, job.Spec.Template.Spec
	if job.Kind != "Job" || job.Namespace != "dev-agents" || job.Name != "" || job.GenerateName != "dev-env-v2-portal-db-check-" {
		t.Fatalf("Job identity %s %s/%s %q", job.Kind, job.Namespace, job.Name, job.GenerateName)
	}
	if spec.TTLSecondsAfterFinished != nil || spec.BackoffLimit == nil || *spec.BackoffLimit != 0 || spec.ActiveDeadlineSeconds == nil || *spec.ActiveDeadlineSeconds != 60 || pod.RestartPolicy != "Never" {
		t.Fatal("the Job must run once: no TTL, backoffLimit 0, activeDeadlineSeconds 60, restartPolicy Never")
	}
	if pod.ServiceAccountName != "dev-env-v2-portal" || pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || job.Spec.Template.Labels["app.kubernetes.io/name"] != "dev-env-v2-portal" {
		t.Fatal("the Job must keep the portal ServiceAccount, no token automount, and the network-policy label")
	}
	if len(pod.Containers) != 1 || len(pod.InitContainers) != 0 || !slices.Equal(pod.Containers[0].Args, []string{"db-check"}) || len(pod.Containers[0].Command) != 0 || len(pod.Containers[0].Ports) != 0 || len(pod.Containers[0].Env) != 0 || len(pod.Containers[0].EnvFrom) != 0 {
		t.Fatal("one container: args [db-check], no command, port or environment")
	}
	c := pod.Containers[0]
	if c.SecurityContext == nil || c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem || c.Resources.Limits.Cpu().IsZero() || c.Resources.Limits.Memory().IsZero() {
		t.Fatal("the container must keep a read-only root and CPU and memory limits")
	}

	// Every file the pod can see under the portal root, by volume.
	mounted := map[string]bool{}
	volumes := map[string]bool{}
	for _, v := range pod.Volumes {
		volumes[v.Name] = true
		switch {
		case v.Secret != nil && v.Secret.SecretName == "dev-env-v2-portal-runtime" && v.ConfigMap == nil && v.Projected == nil:
			for _, m := range c.VolumeMounts {
				if m.Name == v.Name {
					for _, item := range v.Secret.Items {
						mounted[path.Join(m.MountPath, item.Path)] = item.Key == item.Path && (item.Key == "DB_USERNAME" || item.Key == "DB_PASSWORD")
					}
				}
			}
			if len(v.Secret.Items) == 0 {
				t.Fatal("the runtime Secret must project named items only")
			}
		case v.ConfigMap != nil && v.ConfigMap.Name == "dev-env-v2-portal-postgres-ca" && v.Secret == nil && v.Projected == nil:
			for _, m := range c.VolumeMounts {
				if m.Name == v.Name {
					for _, item := range v.ConfigMap.Items {
						mounted[path.Join(m.MountPath, item.Path)] = item.Key == "ca.crt"
					}
				}
			}
			if len(v.ConfigMap.Items) == 0 {
				t.Fatal("the CA ConfigMap must project named items only")
			}
		default:
			t.Fatalf("volume %q is not one of the two existing sources db-check needs", v.Name)
		}
	}
	for _, m := range c.VolumeMounts {
		if !volumes[m.Name] || !m.ReadOnly || m.SubPath != "" {
			t.Fatalf("mount %q must be a read-only whole volume", m.Name)
		}
	}
	want := []string{path.Join(ProductionRoot, dbCheckMountedFiles.user), path.Join(ProductionRoot, dbCheckMountedFiles.password), path.Join(ProductionRoot, dbCheckMountedFiles.ca)}
	if len(mounted) != len(want) {
		t.Fatalf("mounted files %v, want exactly %v", mounted, want)
	}
	for _, file := range want {
		if !mounted[file] {
			t.Fatalf("mounted files %v, want exactly %v", mounted, want)
		}
	}
}
