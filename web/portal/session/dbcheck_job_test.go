package session

import (
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/yaml"
)

// TestDBCheckDocJobMountsOnlyWhatItReads parses the manifests in
// docs/portal-db-check.md (DESIGN-056 section 4). The Job mounts exactly the
// files db-check reads (the two DB keys of the existing runtime Secret and the
// existing Postgres CA): no token, OIDC secret, session key or TLS key. It runs
// once: one fixed name, no TTL, no retry. Its own Flux Kustomization neither
// waits on it nor gates the portal.
func TestDBCheckDocJobMountsOnlyWhatItReads(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/portal-db-check.md")
	if err != nil {
		t.Fatal(err)
	}
	var blocks []string
	for rest := string(doc); ; {
		start := strings.Index(rest, "```yaml\n")
		if start < 0 {
			break
		}
		rest = rest[start+len("```yaml\n"):]
		end := strings.Index(rest, "```")
		blocks, rest = append(blocks, rest[:end]), rest[end+3:]
	}
	var job batchv1.Job
	var ks map[string]any
	jobs, kustomizations := 0, 0
	for _, block := range blocks {
		var head struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal([]byte(block), &head); err != nil {
			t.Fatalf("yaml block does not parse: %v", err)
		}
		switch head.Kind {
		case "Job":
			jobs++
			if err := yaml.UnmarshalStrict([]byte(block), &job); err != nil {
				t.Fatalf("Job example does not parse: %v", err)
			}
		case "Kustomization":
			kustomizations++
			if err := yaml.Unmarshal([]byte(block), &ks); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unexpected yaml block kind %q", head.Kind)
		}
	}
	if jobs != 1 || kustomizations != 1 {
		t.Fatalf("the how-to must hold one Job and one Flux Kustomization, found %d and %d", jobs, kustomizations)
	}
	spec, pod := job.Spec, job.Spec.Template.Spec
	if job.Namespace != "dev-agents" || job.GenerateName != "" || !regexp.MustCompile(`^dev-env-v2-portal-db-check-[0-9]{8}$`).MatchString(job.Name) {
		t.Fatalf("Job identity %s/%s %q: one fixed dated name, no generateName", job.Namespace, job.Name, job.GenerateName)
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
	// The Kustomization: its own, applying only the db-check app path, no wait
	// or health checks, depending on nothing but the portal's egress policy.
	ksSpec, _ := ks["spec"].(map[string]any)
	meta, _ := ks["metadata"].(map[string]any)
	if ks["apiVersion"] != "kustomize.toolkit.fluxcd.io/v1" || meta["name"] != "dev-env-v2-portal-db-check" || ksSpec == nil || ksSpec["wait"] != false || ksSpec["prune"] != true || ksSpec["healthChecks"] != nil || ksSpec["healthCheckExprs"] != nil {
		t.Fatalf("Kustomization must be its own, prune true, wait false, no health checks: %v", ks)
	}
	if p, _ := ksSpec["path"].(string); !strings.HasSuffix(p, "/portal-db-check/app") {
		t.Fatalf("Kustomization path %q must be the db-check app only", p)
	}
	deps, _ := ksSpec["dependsOn"].([]any)
	for _, d := range deps {
		if dep, _ := d.(map[string]any); dep["name"] != "dev-env-v2-portal-policy" {
			t.Fatalf("Kustomization depends on %v; only the portal's egress policy is allowed", dep)
		}
	}

	// The record step waits, bounded, for Flux to create the Job before it
	// waits, bounded, for Complete or Failed; a wait on a missing Job would
	// record a premature NOT RUN.
	text := string(doc)
	create, complete, failed := strings.Index(text, "wait --for=create --timeout="), strings.Index(text, "wait --for=condition=complete --timeout="), strings.Index(text, "wait --for=condition=failed --timeout=")
	if create < 0 || complete < 0 || failed < 0 || create > complete || complete > failed {
		t.Fatal("the record step must wait for the Job to exist, then for Complete or Failed, each with a timeout")
	}
	// The child Kustomization does not exist until cluster-apps (30 min
	// interval) applies the merged commit, so the record step reconciles
	// nothing and waits for the Job longer than that interval.
	if strings.Contains(text, "reconcile kustomization dev-env-v2-portal-db-check") || !strings.Contains(text, "wait --for=create --timeout=35m") {
		t.Fatal("the record step must not reconcile the Job's own Kustomization and must wait 35m for the Job to exist")
	}

	// The record step classifies INTERRUPTED as no result, like no output,
	// never as PASS or a db-check FAIL; the PDB is rechecked in a preflight
	// before the run.
	if !strings.Contains(text, `"INTERRUPTED") result="NOT RUN (`) || strings.Contains(text, `"INTERRUPTED"|`) || strings.Contains(text, `|"INTERRUPTED"`) {
		t.Fatal("the record step must classify INTERRUPTED as NOT RUN, on its own")
	}
	preflight, pdb := strings.Index(text, "### Preflight, immediately before the Job run"), strings.Index(text, "get pdb dev-env-v2-portal")
	if preflight < 0 || pdb < preflight || pdb > create {
		t.Fatal("a preflight must recheck the portal PDB before the record step's waits")
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
