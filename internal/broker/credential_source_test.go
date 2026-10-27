package broker

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestReadCredentialSourceCopiesOnlySelectedFields(t *testing.T) {
	t.Parallel()
	r := fake.NewClientBuilder().WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "existing"}, Data: map[string][]byte{
		"token": []byte("synthetic-token"), "other": []byte("must-not-copy"),
	}}).Build()
	got, err := readCredentialSource(context.Background(), r, &v1alpha1.CredentialSource{Namespace: "media", Name: "existing", Fields: []string{"token"}})
	if err != nil || string(got["token"]) != "synthetic-token" || len(got) != 1 {
		t.Fatalf("selected source = %#v, %v", got, err)
	}
}

func TestReadCredentialSourceRefusesMissingSelectedField(t *testing.T) {
	t.Parallel()
	r := fake.NewClientBuilder().WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "existing"}, Data: map[string][]byte{"token": []byte("synthetic-token")}}).Build()
	if _, err := readCredentialSource(context.Background(), r, &v1alpha1.CredentialSource{Namespace: "media", Name: "existing", Fields: []string{"missing"}}); err == nil {
		t.Fatal("missing selected field was accepted")
	}
}
