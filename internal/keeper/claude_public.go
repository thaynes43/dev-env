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

	"github.com/thaynes43/dev-env/internal/claudeauth"
)

// AnnotationRefreshExpiresAt is the refresh credential's expiry, for status and
// paging. It is provider metadata, not material.
const AnnotationRefreshExpiresAt = "dev-env.haynesops.com/refresh-token-expires-at"

var errClaudePublication = errors.New("claude access publication is unavailable")

// A document is read back at the Unix epoch so an expired stored generation
// still decodes: after an outage the previous generation is normally expired,
// and it must stay comparable.
var claudeEpoch = time.Unix(0, 0)

// claudePublicSecret publishes the access-only projection to the Secret GitOps
// created. Like Codex's, it needs exact-name GET as well as PATCH: a delayed
// previous leader must not roll back a newer publication. The document type
// has no refresh member, so no refresh token can reach these bytes.
type claudePublicSecret struct {
	Client client.Client
	Secret types.NamespacedName
}

// publish writes generation a with one bounded CAS and no conflict retry. A later
// tick may retry after reading the generation; it never repeats a refresh.
// refreshExpiresAt, when set, is annotated.
func (p *claudePublicSecret) publish(ctx context.Context, a claudeauth.Access, refreshExpiresAt, now time.Time) error {
	raw, err := claudeauth.Encode(a, now)
	if err != nil {
		return errClaudePublication
	}
	c, cancel := context.WithTimeout(ctx, claudeSaveTimeout)
	defer cancel()
	var s corev1.Secret
	if p.Client.Get(c, p.Secret, &s) != nil || s.ResourceVersion == "" {
		return errClaudePublication
	}
	if oldRaw := s.Data[claudeauth.LiveKey]; len(oldRaw) != 0 {
		old, err := claudeauth.Decode(oldRaw, claudeEpoch)
		if err != nil || old.Generation > a.Generation || old.AccountUUID != a.AccountUUID {
			return errClaudePublication
		}
		if old.Generation == a.Generation {
			canonical, err := claudeauth.Encode(old, claudeEpoch)
			if err != nil || !bytes.Equal(canonical, raw) {
				return errClaudePublication
			}
			return nil
		}
	}
	annotations := map[string]string{
		AnnotationExpiresAt: a.ExpiresAt.UTC().Format(time.RFC3339), AnnotationWrittenAt: now.UTC().Format(time.RFC3339),
	}
	if !refreshExpiresAt.IsZero() {
		annotations[AnnotationRefreshExpiresAt] = refreshExpiresAt.UTC().Format(time.RFC3339)
	}
	patch, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{"resourceVersion": s.ResourceVersion, "uid": s.UID, "annotations": annotations},
		"data":     map[string][]byte{claudeauth.LiveKey: raw},
	})
	target := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: p.Secret.Namespace, Name: p.Secret.Name}}
	if p.Client.Patch(c, target, client.RawPatch(types.MergePatchType, patch), client.FieldOwner(FieldOwner)) != nil {
		return errClaudePublication
	}
	return nil
}

// generation reads the published generation: 0 for an empty Secret. It is the
// floor for nextClaudeGeneration.
func (p *claudePublicSecret) generation(ctx context.Context) (uint64, error) {
	c, cancel := context.WithTimeout(ctx, claudeSaveTimeout)
	defer cancel()
	var s corev1.Secret
	if p.Client.Get(c, p.Secret, &s) != nil {
		return 0, errClaudePublication
	}
	raw := s.Data[claudeauth.LiveKey]
	if len(raw) == 0 {
		return 0, nil
	}
	old, err := claudeauth.Decode(raw, claudeEpoch)
	if err != nil {
		return 0, errClaudePublication
	}
	return old.Generation, nil
}
