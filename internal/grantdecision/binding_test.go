package grantdecision

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestBindingFencesIdentityAndEveryRequestChange(t *testing.T) {
	g := &v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant-test", UID: "grant-one"}, Spec: v1alpha1.AccessGrantSpec{
		Requester: v1alpha1.GrantRequester{Session: "session-one", SessionUID: "session-uid"},
		Type:      v1alpha1.GrantEgress, TTL: metav1.Duration{Duration: time.Hour}, Reason: "inspect service",
		Egress: &v1alpha1.EgressGrant{FQDNs: []string{"example.com"}, Ports: []v1alpha1.GrantPort{{Port: 443}}},
	}}
	b, err := Bind(g, "pod-one")
	if err != nil || !b.Matches(g, "pod-one") {
		t.Fatal("original request did not match")
	}
	for name, mutate := range map[string]func(*v1alpha1.AccessGrant){
		"grant replacement":   func(x *v1alpha1.AccessGrant) { x.UID = "grant-two" },
		"generation":          func(x *v1alpha1.AccessGrant) { x.Generation++ },
		"session replacement": func(x *v1alpha1.AccessGrant) { x.Spec.Requester.SessionUID = "other" },
		"scope":               func(x *v1alpha1.AccessGrant) { x.Spec.Egress.FQDNs[0] = "other.example.com" },
		"port":                func(x *v1alpha1.AccessGrant) { x.Spec.Egress.Ports[0].Port = 80 },
		"duration":            func(x *v1alpha1.AccessGrant) { x.Spec.TTL.Duration += time.Minute },
		"reason":              func(x *v1alpha1.AccessGrant) { x.Spec.Reason = "different" },
		"release":             func(x *v1alpha1.AccessGrant) { x.Spec.Release = true },
	} {
		t.Run(name, func(t *testing.T) {
			changed := g.DeepCopy()
			mutate(changed)
			if b.Matches(changed, "pod-one") {
				t.Fatal("changed request matched")
			}
		})
	}
	if b.Matches(g, types.UID("pod-two")) {
		t.Fatal("replacement pod matched")
	}
	if _, err := Bind(g, ""); err == nil {
		t.Fatal("missing pod identity accepted")
	}
	if _, err := Bind(nil, "pod-one"); err == nil {
		t.Fatal("missing grant accepted")
	}
}
