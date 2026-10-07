package keeper

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The annotations the keeper puts on every Secret it writes. They hold no
// material: when the credential in the Secret expires and when the keeper wrote
// it, so `kubectl get secret -o yaml` shows how fresh it is.
const (
	AnnotationExpiresAt = "dev-env.haynesops.com/expires-at"
	AnnotationWrittenAt = "dev-env.haynesops.com/written-at"
)

// FieldOwner is the field manager of the keeper's writes. Flux takes over the
// fields of a few well-known managers (kubectl's); this one is not among them.
const FieldOwner = "dev-env-keeper"

// SecretWriter writes credentials into Secrets that GitOps created empty
// (DESIGN-001 6.11: the keeper's Role has get, update and patch on them by name,
// and no create). It never reads a Secret.
type SecretWriter struct {
	// Client must not be cache-backed for Secrets: the keeper's Role grants no
	// list or watch, so an informer could never sync. The keeper only patches.
	Client client.Client
}

// Write sets one key of the Secret and the two annotations in one merge patch.
// The API server applies a patch as one update, and the kubelet swaps a mounted
// Secret's files in one step, so a reader sees the old value or the new one,
// never a mix. Other keys, labels and annotations are left as they are.
func (w *SecretWriter) Write(ctx context.Context, secret types.NamespacedName, key string, value []byte, expiresAt, writtenAt time.Time) error {
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				AnnotationExpiresAt: expiresAt.UTC().Format(time.RFC3339),
				AnnotationWrittenAt: writtenAt.UTC().Format(time.RFC3339),
			},
		},
		// encoding/json writes []byte as base64, which is what data holds.
		"data": map[string][]byte{key: value},
	})
	if err != nil {
		return err
	}
	obj := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: secret.Namespace, Name: secret.Name}}
	err = w.Client.Patch(ctx, obj, client.RawPatch(types.MergePatchType, patch), client.FieldOwner(FieldOwner))
	switch {
	case err == nil:
		return nil
	case apierrors.IsNotFound(err):
		return fmt.Errorf("the Secret %s does not exist: GitOps creates it empty and the keeper only patches it (DESIGN-001 6.11)", secret)
	case apierrors.IsForbidden(err):
		return fmt.Errorf("patch Secret %s is forbidden: the keeper's Role needs patch on it by name (DESIGN-001 6.11): %w", secret, err)
	default:
		return fmt.Errorf("patch Secret %s: %w", secret, err)
	}
}
