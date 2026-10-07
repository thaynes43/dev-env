package v1alpha1_test

// The envtest cases of the Activity CRD (D-17, D-66): a declaration needs a
// description, a scope, a declarer and an expiry, and is immutable.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func activity() *v1alpha1.Activity {
	return &v1alpha1.Activity{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("act-t-%d", seq.Add(1)), Namespace: ns},
		Spec: v1alpha1.ActivitySpec{
			Description: "restarting zigbee2mqtt for a broker migration test",
			Scope:       []string{"home-automation", "zigbee2mqtt", "emqx"},
			DeclaredBy:  "session/haynes-ops-1007-171500",
			Session:     "haynes-ops-1007-171500",
			ExpiresAt:   metav1.NewTime(time.Now().Add(45 * time.Minute)),
		},
	}
}

func TestActivityAccepts(t *testing.T) {
	a := activity()
	if err := k8s.Create(ctx(t), a); err != nil {
		t.Fatalf("create: %v", err)
	}
	tom := activity()
	tom.Spec.Session, tom.Spec.DeclaredBy, tom.Spec.Scope = "", "human/dev-env-system/dev-env-human", []string{v1alpha1.ScopeCluster}
	if err := k8s.Create(ctx(t), tom); err != nil {
		t.Fatalf("a human's cluster-wide declaration: %v", err)
	}
}

func TestActivityRefuses(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(*v1alpha1.Activity)
		want string
	}{
		"no description":      {func(a *v1alpha1.Activity) { a.Spec.Description = "" }, "spec.description"},
		"a long description":  {func(a *v1alpha1.Activity) { a.Spec.Description = strings.Repeat("x", 513) }, "spec.description"},
		"no scope":            {func(a *v1alpha1.Activity) { a.Spec.Scope = nil }, "spec.scope"},
		"an empty scope item": {func(a *v1alpha1.Activity) { a.Spec.Scope = []string{""} }, "spec.scope[0]"},
		"no declarer":         {func(a *v1alpha1.Activity) { a.Spec.DeclaredBy = "" }, "spec.declaredBy"},
	} {
		t.Run(name, func(t *testing.T) {
			a := activity()
			c.edit(a)
			wantInvalid(t, k8s.Create(ctx(t), a), c.want)
		})
	}
}

func TestActivityIsImmutable(t *testing.T) {
	a := activity()
	if err := k8s.Create(ctx(t), a); err != nil {
		t.Fatal(err)
	}
	a.Spec.ExpiresAt = metav1.NewTime(a.Spec.ExpiresAt.Add(time.Hour))
	wantInvalid(t, k8s.Update(ctx(t), a), "an activity is immutable")
}
