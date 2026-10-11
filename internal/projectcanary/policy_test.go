package projectcanary_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
	"github.com/thaynes43/dev-env/internal/templates"
)

func TestPolicyStrictFiniteRecords(t *testing.T) {
	claude := testutil.New(t, "claude", "dev/dev-env").Record
	codex := testutil.New(t, "codex", "dev/dev-env").Record
	doc, _ := json.Marshal([]projectcanary.Record{claude, codex})
	p, err := projectcanary.Parse(string(doc))
	if err != nil || p.ValidateClients([]string{claude.Client}, true) != nil {
		t.Fatalf("valid two-provider policy: %v", err)
	}
	if p.ValidateClients([]string{claude.Client}, false) == nil || p.ValidateClients([]string{"other/client"}, true) == nil {
		t.Fatal("missing provider/client prerequisite accepted")
	}
	if p.ValidateClients([]string{claude.Client}, true, claude.Client) == nil {
		t.Fatal("reserved session/human identity accepted as canary client")
	}
	if p, err := projectcanary.Parse(""); err != nil || p != nil {
		t.Fatal("policy enabled by default")
	}
	for _, raw := range []string{"[]", "null", string(doc) + "{}", strings.Replace(string(doc), `"id":`, `"unknown":0,"id":`, 1), strings.Replace(string(doc), `"id":`, `"ID":`, 1), strings.Replace(string(doc), `"id":`, `"id":"duplicate","id":`, 1), strings.Repeat(" ", projectcanary.MaxPolicyBytes+1)} {
		if _, err := projectcanary.Parse(raw); err == nil {
			t.Fatal("ambiguous/unbounded policy accepted")
		}
	}
	for _, change := range []func(*projectcanary.Record){func(r *projectcanary.Record) { r.ID = "*" }, func(r *projectcanary.Record) { r.Client = "*/client" }, func(r *projectcanary.Record) { r.Timeout = "181s" }, func(r *projectcanary.Record) { r.Timeout = "0s" }, func(r *projectcanary.Record) { r.MaxTurns = 4 }, func(r *projectcanary.Record) { r.Profile = "full" }, func(r *projectcanary.Record) { r.Image = "ghcr.io/thaynes43/dev-env:latest" }, func(r *projectcanary.Record) { r.Prompt = strings.Repeat("p", projectcanary.MaxPromptBytes+1) }} {
		r := claude
		change(&r)
		bad, _ := json.Marshal([]projectcanary.Record{r})
		if _, err := projectcanary.Parse(string(bad)); err == nil {
			t.Fatalf("invalid record accepted: %+v", r)
		}
	}
	for _, records := range [][]projectcanary.Record{{claude, claude}, {claude, codex, claude}} {
		bad, _ := json.Marshal(records)
		if _, err := projectcanary.Parse(string(bad)); err == nil {
			t.Fatal("duplicate/unbounded attempts accepted")
		}
	}
	r := claude
	r.Prompt += " Changed reviewed instruction."
	if r.SessionName() != claude.SessionName() || r.Digest() == claude.Digest() {
		t.Fatal("policy revision rearmed deterministic identity or lost binding")
	}
	r = claude
	r.Timeout = "3m0s"
	if r.Digest() != claude.Digest() {
		t.Fatal("equivalent timeout changed immutable policy digest")
	}
}

func TestTemplateAndCatalogAuthorityRefuseDrift(t *testing.T) {
	f := testutil.New(t, "codex", "dev/dev-env")
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.Catalog).Build()
	if _, err := projectcanary.ResolveCatalog(context.Background(), c, testutil.Owner, f.Record); err != nil {
		t.Fatal(err)
	}
	if _, err := projectcanary.ResolveCatalog(context.Background(), c, "foreign", f.Record); err == nil {
		t.Fatal("foreign clone authority accepted")
	}
	changed := f.Record
	changed.RulesRevision = strings.Repeat("0", 64)
	if _, err := projectcanary.ResolveCatalog(context.Background(), c, testutil.Owner, changed); err == nil {
		t.Fatal("rule drift accepted")
	}
	for _, mutate := range []func(*templates.Templates){func(t *templates.Templates) { t.Env = append(t.Env, corev1.EnvVar{Name: "GIT_DIR", Value: "/outside"}) }, func(t *templates.Templates) {
		p := t.Profiles["canary"]
		p.EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "arbitrary"}}}}
		t.Profiles["canary"] = p
	}, func(t *templates.Templates) {
		p := t.Profiles["canary"]
		p.Mounts = p.Mounts[:1]
		t.Profiles["canary"] = p
	}, func(t *templates.Templates) {
		s := t.Sizes[v1alpha1.SizeS]
		delete(s.Limits, corev1.ResourceCPU)
		t.Sizes[v1alpha1.SizeS] = s
	}, func(t *templates.Templates) {
		s := t.Sizes[v1alpha1.SizeS]
		s.Limits[corev1.ResourceCPU] = resource.MustParse("3")
		t.Sizes[v1alpha1.SizeS] = s
	}, func(t *templates.Templates) {
		t.Workspace = &templates.Workspace{Enabled: true, Claim: "shared", ID: "shared"}
	}} {
		fresh := testutil.New(t, "codex", "dev/dev-env")
		mutate(fresh.Templates)
		if fresh.Record.ValidateTemplate(fresh.Templates, testutil.Owner) == nil {
			t.Fatal("unsafe private profile accepted")
		}
	}
}

func TestRetainedBindingAndPromptDoNotSupplyRuleMarker(t *testing.T) {
	f := testutil.New(t, "claude", "dev/dev-env")
	if strings.Contains(f.Record.Prompt, "COMMON_RULE_MARKER") {
		t.Fatal("validation marker leaked into prompt")
	}
	if f.Record.MatchSession(f.Session, testutil.Owner) != nil {
		t.Fatal("original retained binding refused")
	}
	if _, err := projectcanary.BoundRecord(f.Session); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*v1alpha1.AgentSession){func(s *v1alpha1.AgentSession) { s.Spec.Prompt += "caller override" }, func(s *v1alpha1.AgentSession) {
		s.Spec.ProjectCanary.CatalogUID = "replacement"
		s.Annotations[v1alpha1.AnnotationProjectSnapshot] += " "
	}, func(s *v1alpha1.AgentSession) { s.Finalizers = nil }, func(s *v1alpha1.AgentSession) { s.Spec.Limits.MaxTurns = 4 }, func(s *v1alpha1.AgentSession) { s.Spec.Restore = "archive" }, func(s *v1alpha1.AgentSession) { s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "shared"} }} {
		s := f.Session.DeepCopy()
		mutate(s)
		if f.Record.MatchSession(s, testutil.Owner) == nil {
			t.Fatal("altered original canary accepted")
		}
	}
}
