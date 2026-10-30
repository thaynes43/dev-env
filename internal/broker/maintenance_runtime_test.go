package broker

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const utilityGPU = "GPU-d8a856f1-f955-f683-bc24-654561496774"

func TestMaintenanceRuntimeProfile(t *testing.T) {
	b, g := maintenanceFixture(t)
	defaultDigest := MaintenanceDigest(g)
	approve(g, "human/dev-env-system/dev-env-human", g.Spec.TTL.Duration, b.now())
	generic := MaintenancePod(g, b.now().Time)
	if generic.Spec.RuntimeClassName != nil || len(generic.Spec.Containers[0].Env) != 0 {
		t.Fatal("default runtime changed")
	}
	g.Spec.Maintenance.RuntimeProfile = "nvidia-utility"
	g.Spec.Maintenance.Resource = utilityGPU
	g.Spec.Maintenance.Devices = nil
	g.Spec.Maintenance.ReadOnlyMounts = []string{"/var/lib/maintenance-inputs/approved"}
	if err := ValidateMaintenance(g); err != nil {
		t.Fatal(err)
	}
	if MaintenanceDigest(g) == defaultDigest {
		t.Fatal("profile not digest bound")
	}
	changed := g.DeepCopy()
	changed.Spec.Maintenance.RuntimeProfile = ""
	if MaintenanceDigest(g) == MaintenanceDigest(changed) {
		t.Fatal("profile alone not digest bound")
	}
	pod := MaintenancePod(g, b.now().Time)
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "nvidia" {
		t.Fatal("runtime class missing")
	}
	env := pod.Spec.Containers[0].Env
	if len(env) != 2 || env[0].Name != "NVIDIA_VISIBLE_DEVICES" || env[0].Value != utilityGPU || env[1].Name != "NVIDIA_DRIVER_CAPABILITIES" || env[1].Value != "utility" {
		t.Fatal(env)
	}
	if len(pod.Spec.Containers[0].SecurityContext.Capabilities.Add) != 1 || pod.Spec.Containers[0].SecurityContext.Capabilities.Add[0] != "SYS_ADMIN" {
		t.Fatal("capabilities broadened")
	}
	for name, mutate := range map[string]func(*v1alpha1.MaintenanceTask){
		"unknown":   func(m *v1alpha1.MaintenanceTask) { m.RuntimeProfile = "nvidia-compute" },
		"all":       func(m *v1alpha1.MaintenanceTask) { m.Resource = "all" },
		"list":      func(m *v1alpha1.MaintenanceTask) { m.Resource = utilityGPU + ",GPU-other" },
		"malformed": func(m *v1alpha1.MaintenanceTask) { m.Resource = "GPU-bad" },
		"device":    func(m *v1alpha1.MaintenanceTask) { m.Devices = []string{"/dev/nvidia0"} },
	} {
		t.Run(name, func(t *testing.T) {
			bad := g.DeepCopy()
			mutate(bad.Spec.Maintenance)
			if ValidateMaintenance(bad) == nil {
				t.Fatal("invalid profile accepted")
			}
		})
	}
}

func TestMaintenanceCreateAdmissionProfiles(t *testing.T) {
	for _, profile := range []string{"", "nvidia-utility"} {
		t.Run("profile="+profile, func(t *testing.T) {
			b, g := maintenanceFixture(t)
			g.Spec.Maintenance.RuntimeProfile = profile
			if profile != "" {
				g.Spec.Maintenance.Resource = utilityGPU
				g.Spec.Maintenance.Devices = nil
			}
			approve(g, "human/dev-env-system/dev-env-human", g.Spec.TTL.Duration, b.now())
			pod := MaintenancePod(g, b.now().Time)
			caller := "system:serviceaccount:dev-env-system:dev-env-broker"
			if !maintenanceAdmissionAllowed(t, "CREATE", caller, nil, pod, g) {
				t.Fatal("valid create refused")
			}
			for name, mutate := range map[string]func(*corev1.Pod){
				"runtime": func(p *corev1.Pod) { p.Spec.RuntimeClassName = ptr.To("other") },
				"env": func(p *corev1.Pod) {
					p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "EXTRA", Value: "bad"})
				},
				"env source": func(p *corev1.Pod) { p.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{Prefix: "bad"}} },
			} {
				t.Run(name, func(t *testing.T) {
					bad := pod.DeepCopy()
					mutate(bad)
					if maintenanceAdmissionAllowed(t, "CREATE", caller, nil, bad, g) {
						t.Fatal("forged create accepted")
					}
				})
			}
			if profile != "" {
				for name, mutate := range map[string]func(*corev1.Pod){
					"absent environment": func(p *corev1.Pod) { p.Spec.Containers[0].Env = nil },
					"UUID":               func(p *corev1.Pod) { p.Spec.Containers[0].Env[0].Value = "all" },
					"compute":            func(p *corev1.Pod) { p.Spec.Containers[0].Env[1].Value = "compute,utility" },
					"valueFrom": func(p *corev1.Pod) {
						p.Spec.Containers[0].Env[0].ValueFrom = &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}
					},
					"overhead": func(p *corev1.Pod) {
						p.Spec.Overhead = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")}
					},
					"selector":   func(p *corev1.Pod) { p.Spec.NodeSelector = map[string]string{"extra": "true"} },
					"toleration": func(p *corev1.Pod) { p.Spec.Tolerations = []corev1.Toleration{{Operator: corev1.TolerationOpExists}} },
				} {
					t.Run(name, func(t *testing.T) {
						bad := pod.DeepCopy()
						mutate(bad)
						if maintenanceAdmissionAllowed(t, "CREATE", caller, nil, bad, g) {
							t.Fatal("profile expansion accepted")
						}
					})
				}
				for _, invalid := range []string{"", "unknown"} {
					bad := g.DeepCopy()
					bad.Spec.Maintenance.RuntimeProfile = invalid
					if maintenanceAdmissionAllowed(t, "CREATE", caller, nil, pod, bad) {
						t.Fatal("profile mismatch accepted")
					}
				}
			}
		})
	}
}

func TestMaintenanceUtilityAPIServerNormalized(t *testing.T) {
	suite(t)
	ctx := context.Background()
	rc := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "nvidia"}, Handler: "nvidia"}
	if err := admin.Create(ctx, rc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Delete(ctx, rc) })
	b, g := maintenanceFixture(t)
	g.Name = name("grant-utility")
	g.Spec.Maintenance.RuntimeProfile = "nvidia-utility"
	g.Spec.Maintenance.Resource = utilityGPU
	g.Spec.Maintenance.Devices = nil
	approve(g, "human/dev-env-system/dev-env-human", g.Spec.TTL.Duration, b.now())
	p := MaintenancePod(g, b.now().Time)
	if err := admin.Create(ctx, p, client.FieldOwner("maintenance-broker")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Delete(ctx, p, client.PropagationPolicy(metav1.DeletePropagationOrphan)) })
	if p.Spec.DNSPolicy == "" || p.UID == "" || p.Spec.RuntimeClassName == nil {
		t.Fatal("normalization absent")
	}
	if !maintenanceAdmissionAllowed(t, "CREATE", "system:serviceaccount:dev-env-system:dev-env-broker", nil, p, g) {
		t.Fatal("normalized utility create refused")
	}
}
