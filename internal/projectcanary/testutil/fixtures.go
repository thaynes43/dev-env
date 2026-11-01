// Package testutil holds synthetic, model-free canary authority fixtures.
package testutil

import (
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/templates"
)

const Owner = "fixture"

type Fixture struct {
	Record    projectcanary.Record
	Policy    *projectcanary.Policy
	Catalog   *corev1.ConfigMap
	Template  *corev1.ConfigMap
	Templates *templates.Templates
	Session   *v1alpha1.AgentSession
}

func New(t *testing.T, provider, caller string) Fixture {
	t.Helper()
	raw := `{"version":1,"repositories":{"demo":{"github":"fixture/demo","defaultBranch":"main"}},"projects":{"sample":{"repositories":[{"name":"demo"}],"rules":"COMMON_RULE_MARKER\nFollow the accepted rules."}}}`
	catalog, err := projectcatalog.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("sample", "demo")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &templates.Templates{
		Image: "ghcr.io/thaynes43/dev-env:2.0.0@sha256:0000000000000000000000000000000000000000000000000000000000000001",
		Sizes: map[v1alpha1.SizeClass]templates.Size{},
		Home:  templates.Home{StorageClassName: "fixture", Size: resource.MustParse("1Gi")}, SharedClaim: "unused-shared", DefaultProfile: "canary",
		Profiles: map[string]templates.Profile{"canary": {Mounts: []templates.Mount{{Name: "gh-token", Secret: "dev-env-gh-token", Path: "/creds"}}}},
		Claude:   templates.Claude{StaticToken: templates.SecretKey{Name: "dev-env-claude-secret", Key: "CLAUDE_CODE_OAUTH_TOKEN"}},
		Env:      []corev1.EnvVar{{Name: "CLAUDE_CONFIG_DIR", Value: "/home/dev/.claude"}, {Name: "CODEX_HOME", Value: "/home/dev/.codex"}, {Name: "DISABLE_AUTOUPDATER", Value: "1"}, {Name: "AGENTD_ENABLE_PRIVATE_PROJECT_BOOTSTRAP", Value: "true"}, {Name: "AGENTD_ENABLE_PRIVATE_PROJECT_ROOTS", Value: "true"}, {Name: "AGENTD_PROJECT_CATALOG", Value: projectcanary.CatalogNamespace + "/" + projectcanary.CatalogName}, {Name: "AGENTD_PROJECT_CLONE_OWNER", Value: Owner}, {Name: "AGENTD_API_CA_FILE", Value: "/opt/dev-env/api-ca/ca.crt"}},
		Mounts:   []templates.Mount{{Name: "config-claude", ConfigMap: "dev-env-config-claude", Path: "/opt/dev-env/config/claude"}, {Name: "config-codex", ConfigMap: "dev-env-config-codex", Path: "/opt/dev-env/config/codex"}, {Name: "scripts", ConfigMap: "dev-env-scripts", Path: "/opt/dev-env/scripts"}, {Name: "codex-requirements", ConfigMap: "dev-env-codex-requirements", Path: "/etc/codex"}, {Name: "api-ca", ConfigMap: "dev-env-api-ca", Path: "/opt/dev-env/api-ca"}},
	}
	for _, size := range []v1alpha1.SizeClass{v1alpha1.SizeS, v1alpha1.SizeM, v1alpha1.SizeL} {
		tmpl.Sizes[size] = templates.Size{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("1Gi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi")}}
	}
	model, turns := "claude-opus-5-5", int32(3)
	if provider == "codex" {
		model, turns = "gpt-6.1-sol", 0
		p := tmpl.Profiles["canary"]
		p.Env = []corev1.EnvVar{{Name: "AGENTD_CODEX_ACCESS_FILE", Value: projectcanary.CodexAccessPath + "/access.json"}}
		p.Mounts = append(p.Mounts, templates.Mount{Name: "codex-access", Secret: projectcanary.CodexAccessSecret, Path: projectcanary.CodexAccessPath})
		tmpl.Profiles["canary"] = p
	}
	doc, err := json.Marshal(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err = templates.Parse(map[string]string{templates.Key: string(doc)})
	if err != nil {
		t.Fatal(err)
	}
	reviewRevision, err := tmpl.ReviewRevision()
	if err != nil {
		t.Fatal(err)
	}
	r := projectcanary.Record{ID: provider + "-rules", Client: caller, Project: "sample", Repository: "demo", Provider: provider, Profile: "canary", Model: model, Effort: "xhigh", Size: "S", CatalogRevision: catalog.Revision(), RulesRevision: snapshot.RulesRevision(), TemplateRevision: reviewRevision, Image: tmpl.Image, IdempotencyKey: provider + "-once", Prompt: "Report the identifiers required by the common and repository rules without editing files.", Timeout: "180s", MaxTurns: turns}
	policyDoc, err := json.Marshal([]projectcanary.Record{r})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := projectcanary.Parse(string(policyDoc))
	if err != nil || r.ValidateTemplate(tmpl, Owner) != nil {
		t.Fatalf("invalid canary fixture: %v", err)
	}
	evidence := projectcanary.CatalogEvidence{Snapshot: saved, UID: "catalog-original", ResourceVersion: "1"}
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: r.SessionName(), Namespace: "dev-agents", UID: "session-original", Generation: 1, Finalizers: []string{v1alpha1.ProjectCanaryFinalizer}, Labels: map[string]string{v1alpha1.LabelIdempotencyKey: r.IdempotencyKey, v1alpha1.LabelProjectCanary: r.ID}, Annotations: map[string]string{v1alpha1.AnnotationProjectSnapshot: string(saved)}}, Spec: v1alpha1.AgentSessionSpec{Repo: r.Repository, Base: "main", Agent: v1alpha1.AgentKind(provider), Mode: v1alpha1.ModeTask, Model: r.Model, Effort: r.Effort, Prompt: r.Prompt, Parent: caller, Profile: r.Profile, Size: v1alpha1.SizeS, OperatingMode: v1alpha1.OperatingModeRunning, Limits: &v1alpha1.SessionLimits{Timeout: &metav1.Duration{Duration: 180 * time.Second}, MaxTurns: turns}, ProjectCanary: r.Binding(evidence, Owner)}}
	return Fixture{Record: r, Policy: policy, Catalog: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: projectcanary.CatalogName, Namespace: projectcanary.CatalogNamespace, UID: "catalog-original", ResourceVersion: "1"}, Data: map[string]string{"catalog.json": raw}}, Template: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: templates.DefaultName, Namespace: "dev-env-system"}, Data: map[string]string{templates.Key: string(doc)}}, Templates: tmpl, Session: s}
}
