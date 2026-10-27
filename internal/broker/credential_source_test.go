package broker

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	got["token"][0] = 'X'
	var stored corev1.Secret
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: "media", Name: "existing"}, &stored); err != nil {
		t.Fatal(err)
	}
	if string(stored.Data["token"]) != "synthetic-token" {
		t.Fatal("selected bytes alias stored data")
	}
}

func TestReadCredentialSourceRefusesMissingSelectedField(t *testing.T) {
	t.Parallel()
	r := fake.NewClientBuilder().WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "existing"}, Data: map[string][]byte{"token": []byte("synthetic-token")}}).Build()
	if _, err := readCredentialSource(context.Background(), r, &v1alpha1.CredentialSource{Namespace: "media", Name: "existing", Fields: []string{"missing"}}); err == nil {
		t.Fatal("missing selected field was accepted")
	}
}

type countingCredentialSourceReader struct {
	calls int
	data  map[string][]byte
}

func (r *countingCredentialSourceReader) Get(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	r.calls++
	obj.(*corev1.Secret).Data = r.data
	return nil
}

func TestReadCredentialSourceCopiesReaderBytes(t *testing.T) {
	r := &countingCredentialSourceReader{data: map[string][]byte{"token": []byte("synthetic-token"), "other": []byte("not-selected")}}
	got, err := readCredentialSource(context.Background(), r, &v1alpha1.CredentialSource{Namespace: "media", Name: "existing", Fields: []string{"token"}})
	if err != nil || len(got) != 1 || string(got["token"]) != "synthetic-token" {
		t.Fatalf("selected source = %#v, %v", got, err)
	}
	got["token"][0] = 'X'
	if string(r.data["token"]) != "synthetic-token" {
		t.Fatal("selected bytes alias reader data")
	}
}

func TestReadCredentialSourceRejectsDevEnvNamespacesBeforeRead(t *testing.T) {
	for _, namespace := range DevEnvNamespaces {
		t.Run(namespace, func(t *testing.T) {
			r := &countingCredentialSourceReader{}
			_, err := readCredentialSource(context.Background(), r, &v1alpha1.CredentialSource{Namespace: namespace, Name: "existing", Fields: []string{"token"}})
			if err == nil || r.calls != 0 {
				t.Fatalf("error = %v, reader calls = %d; want refusal before read", err, r.calls)
			}
		})
	}
}
