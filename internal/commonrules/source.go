package commonrules

import (
	"context"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Resolve only performs uncached named GETs supplied by the operator's API
// reader. Caller text, files and provider configuration cannot select authority.
func Resolve(ctx context.Context, live client.Reader, projectDigest string) (Snapshot, error) {
	if live == nil || !ValidDigest(projectDigest) {
		return Snapshot{}, ErrRefused
	}
	read := func(name, key string) (Source, error) {
		var cm corev1.ConfigMap
		if live.Get(ctx, types.NamespacedName{Namespace: Namespace, Name: name}, &cm) != nil || cm.Namespace != Namespace || cm.Name != name || cm.UID == "" || cm.ResourceVersion == "" || !cm.DeletionTimestamp.IsZero() {
			return Source{}, ErrRefused
		}
		text, ok := cm.Data[key]
		if _, binary := cm.BinaryData[key]; !ok || binary {
			return Source{}, ErrRefused
		}
		return Source{Namespace: Namespace, Name: name, Key: key, UID: string(cm.UID), ResourceVersion: cm.ResourceVersion, Text: text}, nil
	}
	common, err := read(CommonName, CommonKey)
	if err != nil {
		return Snapshot{}, err
	}
	header, err := read(HeaderName, HeaderKey)
	if err != nil {
		return Snapshot{}, err
	}
	return New(common, header, projectDigest)
}

// Confirm includes original object identity and resourceVersion. An identical
// replacement source has the same content revision but is not original authority.
func (s Snapshot) Confirm(ctx context.Context, live client.Reader) error {
	fresh, err := Resolve(ctx, live, s.ProjectDigest)
	if err != nil || !reflect.DeepEqual(fresh, s) {
		return ErrRefused
	}
	return nil
}
