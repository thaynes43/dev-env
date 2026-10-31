package broker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func rd2ProposalObjects(now time.Time) (rd2ProposalBinding, *v1alpha1.AccessGrant, []client.Object) {
	b := rd2ProposalBinding{GrantName: "grant-rd2-unit", GrantUID: "grant-uid", ServiceAccountUID: "sa-uid", Requester: "requester", RequesterUID: "session-uid", PodUID: "pod-uid"}
	g := &v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: b.GrantName, UID: b.GrantUID}, Spec: v1alpha1.AccessGrantSpec{
		Requester: v1alpha1.GrantRequester{Session: b.Requester, SessionUID: b.RequesterUID}, Type: v1alpha1.GrantBreakglass,
		Kube: &v1alpha1.KubeGrant{Role: v1alpha1.RoleBreakglass}, TTL: metav1.Duration{Duration: 30 * time.Minute},
	}, Status: v1alpha1.AccessGrantStatus{Phase: v1alpha1.GrantActive, ServiceAccount: b.GrantName, InstalledPodUID: string(b.PodUID), ApprovedBy: "delegate/synthetic/actor", ApprovedAt: rd2Time(now), ApprovedTTL: &metav1.Duration{Duration: 30 * time.Minute}, ExpiresAt: rd2Time(now.Add(30 * time.Minute))}}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: b.GrantName, UID: b.ServiceAccountUID, Labels: map[string]string{v1alpha1.LabelGrant: b.GrantName}}}
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: b.Requester, UID: b.RequesterUID}}
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: b.Requester, UID: b.PodUID, Labels: map[string]string{v1alpha1.LabelSession: b.Requester}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	return b, g, []client.Object{g, sa, s, p}
}

func rd2Fake(objects ...client.Object) client.Client {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build()
}

func TestRD2ProposalCompiler(t *testing.T) {
	now := time.Date(2026, 10, 31, 21, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		mutate func(rd2ProposalBinding, *v1alpha1.AccessGrant, []client.Object) rd2ProposalBinding
	}{
		{"grant UID", func(b rd2ProposalBinding, _ *v1alpha1.AccessGrant, _ []client.Object) rd2ProposalBinding {
			b.GrantUID = "replacement"
			return b
		}},
		{"same SA name different UID", func(b rd2ProposalBinding, _ *v1alpha1.AccessGrant, o []client.Object) rd2ProposalBinding {
			o[1].SetUID("replacement")
			return b
		}},
		{"requester UID", func(b rd2ProposalBinding, g *v1alpha1.AccessGrant, _ []client.Object) rd2ProposalBinding {
			g.Spec.Requester.SessionUID = "replacement"
			return b
		}},
		{"pod UID", func(b rd2ProposalBinding, _ *v1alpha1.AccessGrant, o []client.Object) rd2ProposalBinding {
			o[3].SetUID("replacement")
			return b
		}},
		{"released", func(b rd2ProposalBinding, g *v1alpha1.AccessGrant, _ []client.Object) rd2ProposalBinding {
			g.Spec.Release = true
			return b
		}},
		{"revoked", func(b rd2ProposalBinding, g *v1alpha1.AccessGrant, _ []client.Object) rd2ProposalBinding {
			g.Status.Phase = v1alpha1.GrantReleased
			return b
		}},
		{"expired", func(b rd2ProposalBinding, g *v1alpha1.AccessGrant, _ []client.Object) rd2ProposalBinding {
			g.Status.ExpiresAt = rd2Time(now)
			return b
		}},
		{"pending", func(b rd2ProposalBinding, g *v1alpha1.AccessGrant, _ []client.Object) rd2ProposalBinding {
			g.Status.Phase = v1alpha1.GrantPending
			return b
		}},
		{"over thirty minutes", func(b rd2ProposalBinding, g *v1alpha1.AccessGrant, _ []client.Object) rd2ProposalBinding {
			g.Status.ExpiresAt = rd2Time(now.Add(time.Hour))
			return b
		}},
		{"beyond approved TTL", func(b rd2ProposalBinding, g *v1alpha1.AccessGrant, _ []client.Object) rd2ProposalBinding {
			g.Status.ApprovedTTL = &metav1.Duration{Duration: 10 * time.Minute}
			return b
		}},
		{"missing approved TTL", func(b rd2ProposalBinding, g *v1alpha1.AccessGrant, _ []client.Object) rd2ProposalBinding {
			g.Status.ApprovedTTL = nil
			return b
		}},
		{"missing binding", func(b rd2ProposalBinding, _ *v1alpha1.AccessGrant, _ []client.Object) rd2ProposalBinding {
			b.ServiceAccountUID = ""
			return b
		}},
		{"unapproved", func(b rd2ProposalBinding, g *v1alpha1.AccessGrant, _ []client.Object) rd2ProposalBinding {
			g.Status.ApprovedBy = ""
			return b
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, g, o := rd2ProposalObjects(now)
			b = tc.mutate(b, g, o)
			got, err := rd2ProposalExemption(context.Background(), true, rd2Fake(o...), b, now)
			if err == nil || got != "false" {
				t.Fatalf("got %q, %v; require no exemption", got, err)
			}
		})
	}
	b, _, o := rd2ProposalObjects(now)
	got, err := rd2ProposalExemption(context.Background(), true, rd2Fake(o...), b, now)
	if err != nil || !strings.Contains(got, `request.userInfo.uid == "sa-uid"`) {
		t.Fatalf("binding: %q %v", got, err)
	}
	got, err = rd2ProposalExemption(context.Background(), false, nil, rd2ProposalBinding{}, now)
	if err != nil || got != "false" {
		t.Fatalf("default off: %q %v", got, err)
	}
	got, err = rd2ProposalExemption(context.Background(), true, rd2FailedReader{rd2Fake(o...)}, b, now)
	if err == nil || got != "false" {
		t.Fatalf("lookup failure: %q %v", got, err)
	}
	// No Grant found is also a refusal, never prefix-based approval.
	got, err = rd2ProposalExemption(context.Background(), true, rd2Fake(o[1:]...), b, now)
	if err == nil || got != "false" {
		t.Fatalf("absent grant: %q %v", got, err)
	}
}

type rd2FailedReader struct{ client.Reader }

func (rd2FailedReader) Get(context.Context, types.NamespacedName, client.Object, ...client.GetOption) error {
	return errors.New("synthetic API lookup failure")
}

func rd2Time(t time.Time) *metav1.Time { v := metav1.NewTime(t); return &v }

// An isolated fixture cannot silently loosen unrelated grant validation. Check
// the actual proposed YAML, including Kyverno (which envtest cannot execute).
func TestRD2ProposedGuardDelta(t *testing.T) {
	load := func(path string) map[string]any {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(b), 4096)
		for {
			var u map[string]any
			if err := dec.Decode(&u); err != nil {
				t.Fatal(err)
			}
			if len(u) != 0 {
				return u
			}
		}
	}
	base := load(filepath.Join("testdata", "identity-guard", "identity-guard.yaml"))
	proposed := load(filepath.Join("testdata", "proposed-haynes-ops", "rd2", "identity-guard.yaml"))
	baseSpec := base["spec"].(map[string]any)
	proposedSpec := proposed["spec"].(map[string]any)
	baseConditions := baseSpec["matchConditions"].([]any)
	proposedConditions := proposedSpec["matchConditions"].([]any)
	baseExpr := baseConditions[0].(map[string]any)["expression"].(string)
	expected := baseExpr + " && !(request.userInfo.username == 'system:serviceaccount:dev-agents:grant-rd2-proposal' && request.userInfo.uid == 'rd2-proposal-sa-uid')"
	proposedExpr := proposedConditions[0].(map[string]any)["expression"].(string)
	normalize := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if normalize(proposedExpr) != normalize(expected) {
		t.Fatalf("unexpected exemption: %s", proposedExpr)
	}
	proposedSpec["matchConditions"] = baseConditions
	if !reflect.DeepEqual(base, proposed) {
		t.Fatal("proposal changes more than the exact identity match; all other grant denials must remain identical")
	}
	kyverno := load(filepath.Join("testdata", "proposed-haynes-ops", "rd2", "exec-guard.yaml"))
	kySpec := kyverno["spec"].(map[string]any)
	kyExpr := kySpec["matchConditions"].([]any)[0].(map[string]any)["expression"].(string)
	if normalize(kyExpr) != normalize(expected) || kySpec["failurePolicy"] != "Fail" || !reflect.DeepEqual(kySpec["validationActions"], []any{"Deny"}) {
		t.Fatal("Kyverno must share the exact UID exemption and Fail/Deny")
	}
	role := load(filepath.Join("testdata", "proposed-haynes-ops", "rd2", "admin-role.yaml"))
	rules := role["rules"].([]any)
	if len(rules) != 2 || !reflect.DeepEqual(rules[1], map[string]any{"nonResourceURLs": []any{"*"}, "verbs": []any{"*"}}) {
		t.Fatal("admin role must include every non-resource URL/method")
	}
}
