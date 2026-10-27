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
