package activity

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The reaper deletes what has expired in its namespace, and nothing else.
func TestReap(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 7, 23, 0, 0, 0, time.UTC)
	act := func(name, ns string, expires time.Time) *v1alpha1.Activity {
		return &v1alpha1.Activity{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: v1alpha1.ActivitySpec{Description: "x", Scope: []string{"a"}, DeclaredBy: "human/x", ExpiresAt: metav1.NewTime(expires)}}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		act("expired", "dev-env-system", now.Add(-time.Minute)),
		act("live", "dev-env-system", now.Add(time.Minute)),
		act("elsewhere", "other", now.Add(-time.Hour)),
	).Build()
	r := &Reaper{Client: c, Reader: c, Namespace: "dev-env-system", Log: logr.Discard(), Now: func() time.Time { return now }}
	if n := r.Reap(context.Background()); n != 1 {
		t.Errorf("reaped %d, want 1", n)
	}
	var list v1alpha1.ActivityList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range list.Items {
		names = append(names, a.Name)
	}
	slices.Sort(names)
	if len(names) != 2 || names[0] != "elsewhere" || names[1] != "live" {
		t.Errorf("left %v", names)
	}
}
