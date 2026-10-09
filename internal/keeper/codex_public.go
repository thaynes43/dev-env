package keeper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/codexauth"
)

var errCodexPublication = errors.New("codex access publication is unavailable")

// Codex alone needs exact-name GET as well as PATCH. A delayed previous leader
// must not roll back a newer publication; the generic SecretWriter remains
// patch-only for its existing credentials.
type codexPublicSecret struct {
	Client client.Client
	Secret types.NamespacedName
}

func (p *codexPublicSecret) publish(ctx context.Context, a codexauth.Access, now time.Time) error {
	raw, err := codexauth.Encode(a, now)
	if err != nil {
		return errCodexPublication
	}
	c, cancel := context.WithTimeout(ctx, codexSaveTimeout)
	defer cancel()
	var s corev1.Secret
	if p.Client.Get(c, p.Secret, &s) != nil || s.ResourceVersion == "" {
		return errCodexPublication
	}
	if oldRaw := s.Data[codexauth.LiveKey]; len(oldRaw) != 0 {
		old, err := codexauth.DecodeStored(oldRaw)
		if err != nil || old.Generation > a.Generation || old.AccountID != a.AccountID {
			return errCodexPublication
		}
		if old.Generation == a.Generation {
			canonical, err := codexauth.Encode(old, old.LastRefresh)
			if err != nil || !bytes.Equal(canonical, raw) {
				return errCodexPublication
			}
			return nil
		}
	}
	patch, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{"resourceVersion": s.ResourceVersion, "uid": s.UID, "annotations": map[string]string{
			AnnotationExpiresAt: a.ExpiresAt.UTC().Format(time.RFC3339), AnnotationWrittenAt: now.UTC().Format(time.RFC3339),
		}},
		"data": map[string][]byte{codexauth.LiveKey: raw},
	})
	target := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: p.Secret.Namespace, Name: p.Secret.Name}}
	// One bounded CAS, without conflict retry. A later tick may retry access
	// publication after reading its generation; it never repeats a refresh POST.
	if p.Client.Patch(c, target, client.RawPatch(types.MergePatchType, patch), client.FieldOwner(FieldOwner)) != nil {
		return errCodexPublication
	}
	return nil
}
