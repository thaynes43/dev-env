package commonrules

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func fixture(t *testing.T) (Snapshot, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if corev1.AddToScheme(scheme) != nil {
		t.Fatal("scheme")
	}
	cm := func(name, key, text, uid string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: name, UID: types.UID(uid), ResourceVersion: "1"}, Data: map[string]string{key: text, "unrelated": "must not be captured"}}
	}
	live := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm(CommonName, CommonKey, "COMMON_RULE_MARKER\n", "common-uid"), cm(HeaderName, HeaderKey, "CODEX_HEADER\n", "header-uid")).Build()
	s, err := Resolve(context.Background(), live, Digest([]byte("accepted-project-snapshot")))
	if err != nil {
		t.Fatal(err)
	}
	return s, live
}

func TestSnapshotRetainsExactInstructionProvenance(t *testing.T) {
	s, live := fixture(t)
	if s.CommonRevision != "d0a41d583f4fa9fa6c994886452a23de2530ca7e3409fec1f24930af0438c51d" {
		t.Fatal("declarable canonical content revision changed")
	}
	other, err := New(s.Common, s.Header, Digest([]byte("another-project")))
	if err != nil || other.CommonRevision != s.CommonRevision {
		t.Fatal("project observation changed common content revision", err)
	}
	raw, err := s.Bytes()
	if err != nil || bytes.Contains(raw, []byte("unrelated")) {
		t.Fatal("captured unrelated configuration")
	}
	parsed, err := Parse(raw)
	if err != nil || parsed.CodexGlobal() != "CODEX_HEADER\n\nCOMMON_RULE_MARKER\n" || parsed.Confirm(context.Background(), live) != nil {
		t.Fatal("exact snapshot lost")
	}
	changed := s
	changed.Common.UID = "replacement-uid"
	changed.Common.ResourceVersion = "2"
	if changed.Validate() != nil || changed.CommonRevision != s.CommonRevision {
		t.Fatal("observation identity entered declarable content revision")
	}
	var cm corev1.ConfigMap
	if live.Get(context.Background(), types.NamespacedName{Namespace: Namespace, Name: CommonName}, &cm) != nil {
		t.Fatal("read")
	}
	cm.Data[CommonKey] += "changed"
	if live.Update(context.Background(), &cm) != nil || s.Confirm(context.Background(), live) == nil {
		t.Fatal("changed source authority accepted")
	}
}

func TestSnapshotBoundsAndStrictIdentity(t *testing.T) {
	s, _ := fixture(t)
	s.Common.Text, s.Header.Text = strings.Repeat("\x01", MaxCommonBytes), strings.Repeat("\x02", MaxHeaderBytes)
	max, err := New(s.Common, s.Header, s.ProjectDigest)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := max.Bytes()
	if err != nil || len(raw) > MaxSnapshotBytes {
		t.Fatal("accepted maximum escaping exceeds transport")
	}
	if _, err := Parse(raw); err != nil {
		t.Fatal("maximum snapshot cannot be resumed")
	}
	for _, change := range []func(*Snapshot){func(s *Snapshot) { s.Common.Text += "x" }, func(s *Snapshot) { s.Header.Text += "x" }, func(s *Snapshot) { s.Common.Namespace = "foreign" }, func(s *Snapshot) { s.Common.Key = "auth.json" }, func(s *Snapshot) { s.ProjectDigest = "unknown" }, func(s *Snapshot) { s.Common.Text = string([]byte{0xff}) }} {
		bad := max
		change(&bad)
		if _, err := New(bad.Common, bad.Header, bad.ProjectDigest); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
	for _, raw := range [][]byte{[]byte(strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1)), []byte(strings.Replace(string(raw), `"version":1`, `"Version":1`, 1)), []byte(strings.Replace(string(raw), `"version":1`, `"version":1,"unexpected":true`, 1))} {
		if _, err := Parse(raw); err == nil {
			t.Fatal("ambiguous snapshot accepted")
		}
	}
	changed := max
	changed.Common.Text = "changed"
	bad, _ := json.Marshal(changed)
	if _, err := Parse(bad); err == nil {
		t.Fatal("incorrect content digest accepted")
	}
}

// The retained-result read path compares bound content, not object identity.
// The strict Confirm keeps its UID/resourceVersion fence for pre-Create and
// pre-executor checks.
func TestConfirmContentIgnoresObservationIdentityOnly(t *testing.T) {
	ctx := context.Background()
	update := func(t *testing.T, live client.Client, name string, change func(*corev1.ConfigMap)) {
		t.Helper()
		var cm corev1.ConfigMap
		if live.Get(ctx, types.NamespacedName{Namespace: Namespace, Name: name}, &cm) != nil {
			t.Fatal("read")
		}
		change(&cm)
		if live.Update(ctx, &cm) != nil {
			t.Fatal("update")
		}
	}
	recreate := func(t *testing.T, live client.Client, name string, change func(*corev1.ConfigMap)) {
		t.Helper()
		var cm corev1.ConfigMap
		if live.Get(ctx, types.NamespacedName{Namespace: Namespace, Name: name}, &cm) != nil || live.Delete(ctx, &cm) != nil {
			t.Fatal("delete")
		}
		cm.ResourceVersion, cm.UID = "", types.UID("replacement-"+name)
		change(&cm)
		if live.Create(ctx, &cm) != nil {
			t.Fatal("re-create")
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, client.Client)
		accept bool
	}{
		{"unchanged", func(*testing.T, client.Client) {}, true},
		{"unrelated-common-key", func(t *testing.T, live client.Client) {
			update(t, live, CommonName, func(cm *corev1.ConfigMap) { cm.Data["mcp.json"] = "{}" })
		}, true},
		{"unrelated-header-key", func(t *testing.T, live client.Client) {
			update(t, live, HeaderName, func(cm *corev1.ConfigMap) { cm.Data["config.toml"] = "model = \"changed\"" })
		}, true},
		{"labels", func(t *testing.T, live client.Client) {
			update(t, live, CommonName, func(cm *corev1.ConfigMap) {
				cm.Labels = map[string]string{"kustomize.toolkit.fluxcd.io/name": "session-config"}
			})
		}, true},
		{"identical-recreate", func(t *testing.T, live client.Client) {
			recreate(t, live, CommonName, func(*corev1.ConfigMap) {})
			recreate(t, live, HeaderName, func(*corev1.ConfigMap) {})
		}, true},
		{"common-bytes", func(t *testing.T, live client.Client) {
			update(t, live, CommonName, func(cm *corev1.ConfigMap) { cm.Data[CommonKey] += "changed" })
		}, false},
		{"header-bytes", func(t *testing.T, live client.Client) {
			update(t, live, HeaderName, func(cm *corev1.ConfigMap) { cm.Data[HeaderKey] = "OTHER_HEADER\n" })
		}, false},
		{"recreate-changed", func(t *testing.T, live client.Client) {
			recreate(t, live, CommonName, func(cm *corev1.ConfigMap) { cm.Data[CommonKey] = "REPLACEMENT\n" })
		}, false},
		{"key-removed", func(t *testing.T, live client.Client) {
			update(t, live, HeaderName, func(cm *corev1.ConfigMap) { delete(cm.Data, HeaderKey) })
		}, false},
		{"key-binary", func(t *testing.T, live client.Client) {
			update(t, live, CommonName, func(cm *corev1.ConfigMap) {
				cm.BinaryData = map[string][]byte{CommonKey: []byte("COMMON_RULE_MARKER\n")}
			})
		}, false},
		{"absent", func(t *testing.T, live client.Client) {
			var cm corev1.ConfigMap
			if live.Get(ctx, types.NamespacedName{Namespace: Namespace, Name: CommonName}, &cm) != nil || live.Delete(ctx, &cm) != nil {
				t.Fatal("delete")
			}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, live := fixture(t)
			tc.mutate(t, live)
			if err := s.ConfirmContent(ctx, live); (err == nil) != tc.accept {
				t.Fatalf("content confirmation = %v, want accept=%v", err, tc.accept)
			}
			// The strict fence still refuses every observation identity change.
			if strict := s.Confirm(ctx, live); (strict == nil) != (tc.name == "unchanged") {
				t.Fatalf("strict confirmation = %v", strict)
			}
		})
	}
	// A retained snapshot that is itself invalid never confirms.
	s, live := fixture(t)
	s.Common.SHA256 = strings.Repeat("0", 64)
	if s.ConfirmContent(ctx, live) == nil {
		t.Fatal("invalid retained snapshot confirmed")
	}
}
