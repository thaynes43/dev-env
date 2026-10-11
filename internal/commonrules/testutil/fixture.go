// Package testutil provides synthetic nonsecret common source fixtures.
package testutil

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/internal/commonrules"
)

func Sources(common, header string) (*corev1.ConfigMap, *corev1.ConfigMap) {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: commonrules.Namespace, Name: commonrules.CommonName, UID: "common-source", ResourceVersion: "1"}, Data: map[string]string{commonrules.CommonKey: common}}, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: commonrules.Namespace, Name: commonrules.HeaderName, UID: "header-source", ResourceVersion: "1"}, Data: map[string]string{commonrules.HeaderKey: header}}
}

func Snapshot(t *testing.T, project []byte, common, header string) commonrules.Snapshot {
	t.Helper()
	a, b := Sources(common, header)
	s, err := commonrules.New(commonrules.Source{Namespace: a.Namespace, Name: a.Name, Key: commonrules.CommonKey, UID: string(a.UID), ResourceVersion: a.ResourceVersion, Text: common}, commonrules.Source{Namespace: b.Namespace, Name: b.Name, Key: commonrules.HeaderKey, UID: string(b.UID), ResourceVersion: b.ResourceVersion, Text: header}, commonrules.Digest(project))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
