package sessionraise

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Kube is a Store over Secrets and ConfigMaps. Names listed in Secrets are
// Secrets; every other name is a ConfigMap. Reads use Reader when it is set,
// so the ledger is uncached, and writes use Writer. A missing object is
// ErrUnreadable. The caller precreates the objects; Kube does not create them.
type Kube struct {
	Reader    client.Reader
	Writer    client.Client
	Namespace string
	Secrets   map[string]bool
}

// Get reads one object.
func (k Kube) Get(ctx context.Context, name string) (Object, error) {
	key := types.NamespacedName{Namespace: k.Namespace, Name: name}
	if k.Secrets[name] {
		var secret corev1.Secret
		if err := k.reader().Get(ctx, key, &secret); err != nil {
			return Object{}, mapErr(err)
		}
		return Object{Data: secret.Data, RV: secret.ResourceVersion}, nil
	}
	var cm corev1.ConfigMap
	if err := k.reader().Get(ctx, key, &cm); err != nil {
		return Object{}, mapErr(err)
	}
	data := make(map[string][]byte, len(cm.Data))
	for field, value := range cm.Data {
		data[field] = []byte(value)
	}
	return Object{Data: data, RV: cm.ResourceVersion}, nil
}

// Update writes data back when rv is still current.
func (k Kube) Update(ctx context.Context, name string, data map[string][]byte, rv string) error {
	key := types.NamespacedName{Namespace: k.Namespace, Name: name}
	if k.Secrets[name] {
		var secret corev1.Secret
		if err := k.reader().Get(ctx, key, &secret); err != nil {
			return mapErr(err)
		}
		if rv != "" && secret.ResourceVersion != rv {
			return ErrConflict
		}
		if k.Writer == nil {
			return ErrUnreadable
		}
		secret.Data = data
		return mapErr(k.Writer.Update(ctx, &secret))
	}
	var cm corev1.ConfigMap
	if err := k.reader().Get(ctx, key, &cm); err != nil {
		return mapErr(err)
	}
	if rv != "" && cm.ResourceVersion != rv {
		return ErrConflict
	}
	if k.Writer == nil {
		return ErrUnreadable
	}
	cm.Data = make(map[string]string, len(data))
	for field, value := range data {
		cm.Data[field] = string(value)
	}
	return mapErr(k.Writer.Update(ctx, &cm))
}

func (k Kube) reader() client.Reader {
	if k.Reader != nil {
		return k.Reader
	}
	return k.Writer
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return ErrUnreadable
	}
	if apierrors.IsConflict(err) {
		return ErrConflict
	}
	return err
}
