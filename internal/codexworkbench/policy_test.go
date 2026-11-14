package codexworkbench

import (
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/templates"
)

func TestCodexWorkbenchSetting(t *testing.T) {
	for _, raw := range []string{"", "named-host", "a"} {
		if p, err := Parse(raw, "sessions"); err != nil || p.Name != raw || p.Namespace != "sessions" {
			t.Fatalf("valid %q: %+v %v", raw, p, err)
		}
	}
	for _, raw := range []string{"a,a", "a,b", "*", "a*", " a", "a ", "a,", ",", "A", "a/b", "a.b", strings.Repeat("a", 64)} {
		if _, err := Parse(raw, "sessions"); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func host() *v1alpha1.AgentSession {
	return &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "named-host", Namespace: "sessions", Labels: map[string]string{v1alpha1.LabelDepth: "0"}}, Spec: v1alpha1.AgentSessionSpec{Agent: v1alpha1.AgentCodex, Mode: v1alpha1.ModeRemote, Profile: "workbench", Parent: "flux-system/kustomize-controller"}}
}
func tmpl() *templates.Templates {
	return &templates.Templates{DefaultProfile: "dev", Profiles: map[string]templates.Profile{"dev": {}, "workbench": {ServiceAccount: templates.WorkbenchServiceAccount}, "alias": {ServiceAccount: templates.WorkbenchServiceAccount}}}
}
func reason(err error) string {
	var e *Refusal
	if errors.As(err, &e) {
		return e.Reason
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestCodexWorkbenchPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*v1alpha1.AgentSession, *templates.Templates, *Policy)
		want string
	}{
		{"named", func(*v1alpha1.AgentSession, *templates.Templates, *Policy) {}, ""},
		{"empty-setting", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) { p.Name = "" }, NotNamed},
		{"unlisted", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) { s.Name = "another-host" }, NotNamed},
		{"alias", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) { s.Spec.Profile = "alias" }, ""},
		{"default", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Spec.Profile = ""
			t.DefaultProfile = "workbench"
		}, ""},
		{"default-unlisted", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Spec.Profile = ""
			t.DefaultProfile = "workbench"
			s.Name = "other"
		}, NotNamed},
		{"missing-profile", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) { s.Spec.Profile = "absent" }, TemplatesUnavailable},
		{"unreadable-account", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			t.Profiles["workbench"] = templates.Profile{ServiceAccount: "unknown"}
		}, TemplatesUnavailable},
		{"namespace", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) { s.Namespace = "other" }, InvalidShape},
		{"missing-namespace", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) { p.Namespace = ""; s.Namespace = "" }, InvalidShape},
		{"parent", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) { s.Spec.Parent = "human/account" }, InvalidShape},
		{"depth", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) { s.Labels[v1alpha1.LabelDepth] = "1" }, InvalidShape},
		{"missing-depth", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			delete(s.Labels, v1alpha1.LabelDepth)
		}, InvalidShape},
		{"noncanonical-depth", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Labels[v1alpha1.LabelDepth] = "00"
		}, InvalidShape},
		{"caller", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) { s.Spec.Caller = "some.caller" }, InvalidShape},
		{"summon", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Spec.Summon = &v1alpha1.SessionSummon{}
		}, InvalidShape},
		{"canary", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{}
		}, InvalidShape},
		{"pilot", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Spec.PrivatePilot = &v1alpha1.PrivatePilotBinding{}
		}, InvalidShape},
		{"workspace", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Spec.Workspace = &v1alpha1.WorkspaceSpec{}
		}, InvalidShape},
		{"directory", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Spec.SessionDirectory = &v1alpha1.SessionDirectoryEnrollment{}
		}, InvalidShape},
		{"project", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Annotations = map[string]string{v1alpha1.AnnotationProjectSnapshot: "{}"}
		}, InvalidShape},
		{"scheduled-label", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Labels[v1alpha1.LabelScheduledWork] = "uid"
		}, InvalidShape},
		{"scheduled-name", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Annotations = map[string]string{v1alpha1.AnnotationScheduledWorkName: ""}
		}, InvalidShape},
		{"scheduled-occurrence", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Annotations = map[string]string{v1alpha1.AnnotationScheduledWorkOccurrence: ""}
		}, InvalidShape},
		{"scheduled-deadline", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Annotations = map[string]string{v1alpha1.AnnotationScheduledWorkStartDeadline: ""}
		}, InvalidShape},
		{"scheduled-finalizer", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Finalizers = []string{v1alpha1.ScheduledWorkFinalizer}
		}, InvalidShape},
		{"reserved-account", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) { s.Spec.Profile = "dev" }, InvalidShape},
		{"reserved-agent", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) { s.Spec.Agent = v1alpha1.AgentClaude }, InvalidShape},
		{"reserved-mode", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) { s.Spec.Mode = v1alpha1.ModeLocal }, InvalidShape},
		{"ordinary-host", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Name = "ordinary"
			s.Spec.Profile = "dev"
		}, ""},
		{"claude", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Name = "ordinary"
			s.Spec.Agent = v1alpha1.AgentClaude
		}, ""},
		{"codex-task", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Name = "ordinary"
			s.Spec.Mode = v1alpha1.ModeTask
		}, ""},
		{"codex-local", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Name = "ordinary"
			s.Spec.Mode = v1alpha1.ModeLocal
		}, ""},
		{"claims-no-exemption", func(s *v1alpha1.AgentSession, t *templates.Templates, p *Policy) {
			s.Name = "other"
			s.Spec.Title = "coordinator"
			s.Spec.Model = "gpt-6.1-sol"
			s.Labels[templates.CoordinatorIdentityLabel] = templates.WorkbenchServiceAccount
			s.Annotations = map[string]string{"coordinator": "true"}
		}, NotNamed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, tm, p := host(), tmpl(), Policy{Name: "named-host", Namespace: "sessions"}
			tc.edit(s, tm, &p)
			err := p.Check(s, Resolve(tm, s.Spec.Profile))
			if got := reason(err); got != tc.want {
				t.Fatalf("reason %q, want %q: %v", got, tc.want, err)
			}
			if tc.want == NotNamed && err.Error() != NotNamedMessage {
				t.Fatal(err)
			}
		})
	}
}

func TestCodexWorkbenchResolutionIsStable(t *testing.T) {
	s, tm, p := host(), tmpl(), Policy{Name: "named-host", Namespace: "sessions"}
	r := Resolve(tm, s.Spec.Profile)
	tm.Profiles["workbench"] = templates.Profile{}
	if err := p.Check(s, r); err != nil || !r.Workbench() {
		t.Fatalf("second template answer used: %v", err)
	}
	s.Spec.Profile = "dev"
	if got := reason(p.Check(s, r)); got != TemplatesUnavailable {
		t.Fatal(got)
	}
	if got := reason(p.Check(host(), Resolve(nil, "workbench"))); got != TemplatesUnavailable {
		t.Fatal(got)
	}
}
