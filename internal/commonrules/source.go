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
// It fences the steps that capture or first apply inputs: Create and the first
// executor.
func (s Snapshot) Confirm(ctx context.Context, live client.Reader) error {
	fresh, err := Resolve(ctx, live, s.ProjectDigest)
	if err != nil || !reflect.DeepEqual(fresh, s) {
		return ErrRefused
	}
	return nil
}

// ConfirmContent compares only the bound content identity: the project digest,
// CommonRevision and each fixed source's namespace, name, key, hash and bytes.
// Source UID and resourceVersion stay retained provenance in the snapshot, not
// inputs here, so another key of the same ConfigMap or an identical re-create
// does not change the answer (DESIGN-022 amendment). It serves reads of output
// that already exists; capture and execution keep Confirm.
func (s Snapshot) ConfirmContent(ctx context.Context, live client.Reader) error {
	if s.Validate() != nil {
		return ErrRefused
	}
	fresh, err := Resolve(ctx, live, s.ProjectDigest)
	if err != nil || fresh.ProjectDigest != s.ProjectDigest || fresh.CommonRevision != s.CommonRevision || !sameContent(fresh.Common, s.Common) || !sameContent(fresh.Header, s.Header) {
		return ErrRefused
	}
	return nil
}

func sameContent(a, b Source) bool {
	return a.Namespace == b.Namespace && a.Name == b.Name && a.Key == b.Key && a.SHA256 == b.SHA256 && a.Text == b.Text
}
