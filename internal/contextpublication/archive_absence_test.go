package contextpublication

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
)

func TestContextArchiveProjectMaskedIsStillNotFound(t *testing.T) {
	for _, mode := range []string{"absent", "malformed foreign", "malformed same project"} {
		t.Run(mode, func(t *testing.T) {
			store := fixtureStore(t)
			name := Name(sampleSpec().Identity)
			if mode != "absent" {
				p := &coord.PublishedContext{ObjectMeta: metav1.ObjectMeta{Namespace: store.Namespace, Name: name}}
				p.Spec.Project = "sample"
				if mode == "malformed foreign" {
					p.Spec.Project = "other"
				}
				if err := store.Client.Create(context.Background(), p); err != nil {
					t.Fatal(err)
				}
			}
			got, err := store.GetForProject(context.Background(), name, "sample")
			if got != nil || err == nil {
				t.Fatal("invalid/missing object became readable")
			}
			if mode == "malformed same project" {
				if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNotFound) {
					t.Fatalf("same-project malformed: %v", err)
				}
			} else if !errors.Is(err, ErrNotFound) || errors.Is(err, ErrProjectMasked) != (mode == "malformed foreign") {
				t.Fatalf("absence/mask distinction: %v", err)
			}
		})
	}
}
