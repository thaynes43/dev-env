package broker

import (
	"context"
	"errors"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// credentialSourceReader is deliberately narrower than client.Reader. It is
// the only broker seam allowed to read an existing credential source, and it
// returns only the exact keys named in the immutable grant request.
type credentialSourceReader interface {
	Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error
}

// readCredentialSource obtains exactly the fields selected in a grant. Callers
// must keep the returned bytes private: they are never suitable for status,
// events, errors, or logs. A missing selected key fails closed; an unselected
// key is never copied into the result.
func readCredentialSource(ctx context.Context, r credentialSourceReader, source *v1alpha1.CredentialSource) (map[string][]byte, error) {
	if source == nil || source.Namespace == "" || source.Name == "" || len(source.Fields) == 0 {
		return nil, errors.New("credential source is incomplete")
	}
	if slices.Contains(DevEnvNamespaces, source.Namespace) {
		return nil, errors.New("credential source namespace is reserved")
	}
	var secret corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: source.Namespace, Name: source.Name}, &secret); err != nil {
		return nil, fmt.Errorf("read selected credential source: %w", err)
	}
	selected := make(map[string][]byte, len(source.Fields))
	for _, field := range source.Fields {
		value, ok := secret.Data[field]
		if !ok || len(value) == 0 {
			return nil, errors.New("selected credential field is unavailable")
		}
		selected[field] = append([]byte(nil), value...)
	}
	return selected, nil
}
