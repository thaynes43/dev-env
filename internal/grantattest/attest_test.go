package grantattest

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func grant() *v1alpha1.AccessGrant {
	return &v1alpha1.AccessGrant{Spec: v1alpha1.AccessGrantSpec{
		Requester: v1alpha1.GrantRequester{Session: "s1", SessionUID: "11111111-1111-1111-1111-111111111111"},
		Type:      v1alpha1.GrantKube, Kube: &v1alpha1.KubeGrant{Role: v1alpha1.RoleWorkloads, Namespaces: []string{"b", "a"}},
		TTL: metav1.Duration{Duration: time.Hour},
	}}
}

func TestScopeDigestIsStableAndSensitive(t *testing.T) {
	g := grant()
	d := ScopeDigest(g)
	if len(d) != 43 {
		t.Fatalf("digest %q", d)
	}
	swapped := grant()
	swapped.Spec.Kube.Namespaces = []string{"a", "b"}
	if ScopeDigest(swapped) != d {
		t.Fatal("namespace order changed the digest")
	}
	for name, edit := range map[string]func(*v1alpha1.AccessGrant){
		"ttl":       func(g *v1alpha1.AccessGrant) { g.Spec.TTL.Duration = 30 * time.Minute },
		"namespace": func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = append(g.Spec.Kube.Namespaces, "c") },
		"role":      func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = v1alpha1.RoleStorage },
		"session":   func(g *v1alpha1.AccessGrant) { g.Spec.Requester.SessionUID = "22222222-2222-2222-2222-222222222222" },
	} {
		c := grant()
		edit(c)
		if ScopeDigest(c) == d {
			t.Errorf("%s did not change the digest", name)
		}
	}
}

// The digest of a grant without a maintenance envelope is byte for byte what it
// was before the envelope joined the digest (ADR-013): this value is the one
// origin/main computes for grant().
func TestScopeDigestUnchangedWithoutMaintenance(t *testing.T) {
	if got, want := ScopeDigest(grant()), "tsyAaGCwtSXeBzGftjA-UyPyQ7Y-pGKLpTyB2xZPTS0"; got != want {
		t.Fatalf("kube grant digest %q, want %q", got, want)
	}
}

func maintenanceGrant() *v1alpha1.AccessGrant {
	return &v1alpha1.AccessGrant{Spec: v1alpha1.AccessGrantSpec{
		Requester: v1alpha1.GrantRequester{Session: "s1", SessionUID: "11111111-1111-1111-1111-111111111111"},
		Type:      v1alpha1.GrantMaintenance, TTL: metav1.Duration{Duration: 10 * time.Minute},
		Maintenance: &v1alpha1.MaintenanceTask{
			RuntimeProfile: "nvidia-utility",
			Coordinator:    v1alpha1.MaintenanceIdentity{Name: "s1", UID: "11111111-1111-1111-1111-111111111111", PodUID: "p1"},
			Workers:        []v1alpha1.MaintenanceIdentity{{Name: "w2", UID: "u2", PodUID: "p2"}, {Name: "w1", UID: "u1", PodUID: "p1w"}},
			Node:           "talosw01", NodeUID: "node-uid", Resource: "GPU-d8a856f1-f955-f683-bc24-654561496774", Operation: "power limit",
			Capabilities: []string{"SYS_RAWIO", "SYS_ADMIN"}, Image: "registry.example/tool@sha256:aaaa",
			ReadOnlyMounts: []string{"/var/lib/maintenance-inputs/b", "/var/lib/maintenance-inputs/a"},
			CPUMillis:      100, MemoryMiB: 128, Restore: "read back",
		},
	}}
}

// The maintenance envelope is in the digest: every field changes it, and the
// order of its sets does not.
func TestScopeDigestCoversTheMaintenanceEnvelope(t *testing.T) {
	d := ScopeDigest(maintenanceGrant())
	bare := maintenanceGrant()
	bare.Spec.Maintenance = nil
	if d == ScopeDigest(bare) {
		t.Fatal("the envelope is not in the digest")
	}
	reordered := maintenanceGrant()
	m := reordered.Spec.Maintenance
	m.Capabilities = []string{"SYS_ADMIN", "SYS_RAWIO"}
	m.ReadOnlyMounts = []string{"/var/lib/maintenance-inputs/a", "/var/lib/maintenance-inputs/b"}
	m.Workers = []v1alpha1.MaintenanceIdentity{m.Workers[1], m.Workers[0]}
	if ScopeDigest(reordered) != d {
		t.Fatal("set order changed the digest")
	}
	if got := maintenanceGrant().Spec.Maintenance.Capabilities[0]; got != "SYS_RAWIO" {
		t.Fatalf("the digest sorted the grant's own list: %s", got)
	}
	for name, edit := range map[string]func(*v1alpha1.MaintenanceTask){
		"node":        func(m *v1alpha1.MaintenanceTask) { m.Node = "talosw02" },
		"node UID":    func(m *v1alpha1.MaintenanceTask) { m.NodeUID = "other" },
		"resource":    func(m *v1alpha1.MaintenanceTask) { m.Resource = "GPU-00000000-0000-0000-0000-000000000000" },
		"runtime":     func(m *v1alpha1.MaintenanceTask) { m.RuntimeProfile = "" },
		"image":       func(m *v1alpha1.MaintenanceTask) { m.Image = "registry.example/tool@sha256:bbbb" },
		"capability":  func(m *v1alpha1.MaintenanceTask) { m.Capabilities = []string{"SYS_ADMIN"} },
		"cpu":         func(m *v1alpha1.MaintenanceTask) { m.CPUMillis = 200 },
		"memory":      func(m *v1alpha1.MaintenanceTask) { m.MemoryMiB = 256 },
		"device":      func(m *v1alpha1.MaintenanceTask) { m.Devices = []string{"/dev/nvidia0"} },
		"mount":       func(m *v1alpha1.MaintenanceTask) { m.ReadOnlyMounts = m.ReadOnlyMounts[:1] },
		"worker":      func(m *v1alpha1.MaintenanceTask) { m.Workers = m.Workers[:1] },
		"worker pod":  func(m *v1alpha1.MaintenanceTask) { m.Workers[0].PodUID = "other" },
		"coordinator": func(m *v1alpha1.MaintenanceTask) { m.Coordinator.PodUID = "other" },
		"operation":   func(m *v1alpha1.MaintenanceTask) { m.Operation = "other" },
		"restore":     func(m *v1alpha1.MaintenanceTask) { m.Restore = "" },
		"extension":   func(m *v1alpha1.MaintenanceTask) { m.ExtensionOf = "grant-earlier" },
	} {
		g := maintenanceGrant()
		edit(g.Spec.Maintenance)
		if ScopeDigest(g) == d {
			t.Errorf("%s did not change the digest", name)
		}
	}
}

func TestStatementRoundTripAndBounds(t *testing.T) {
	a, err := ParseApprover("coordinator:meatball")
	if err != nil {
		t.Fatal(err)
	}
	st := Statement{V: Version, Attestor: Attestor{Session: "s1", SessionUID: "u", PodUID: "p"}, Approver: a, Ref: "msg-1", GrantUID: "g", ScopeDigest: ScopeDigest(grant()), TTL: "1h0m0s", AttestedAt: time.Now().UTC().Truncate(time.Second)}
	raw, err := st.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(raw)
	if err != nil || !SameClaim(got, st) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	for _, bad := range []string{"", "{}", raw + " x", `{"v":2}`, raw[:len(raw)-1], `{"v":1,"unknown":true}`} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, bad := range []string{"", "tom", "coordinator:", "coordinator:a b", "user:x"} {
		if _, err := ParseApprover(bad); err == nil {
			t.Errorf("accepted approver %q", bad)
		}
	}
	if ValidRef("has space") || ValidRef("") || !ValidRef("msg_1:2/3#4") {
		t.Error("ref shape")
	}
}
