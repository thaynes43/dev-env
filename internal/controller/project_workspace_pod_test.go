package controller

import (
	"encoding/json"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/templates"
)

func TestProjectBothProvidersCarryBoundSharedWorkspaceAndSnapshot(t *testing.T) {
	catalog, err := projectcatalog.Parse([]byte(`{"version":1,"repositories":{"haynes-ops":{"github":"thaynes43/haynes-ops"}},"projects":{"sample":{"repositories":[{"name":"haynes-ops"}],"rules":"accepted rules"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("sample", "haynes-ops")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []v1alpha1.AgentKind{v1alpha1.AgentClaude, v1alpha1.AgentCodex} {
		t.Run(string(provider), func(t *testing.T) {
			s := taskSession()
			s.Spec.Agent, s.Spec.Base = provider, "main"
			if provider == v1alpha1.AgentCodex {
				s.Spec.Model, s.Spec.Limits = "gpt-6.1-sol", nil
			}
			s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "accepted-projects"}
			s.Annotations = map[string]string{v1alpha1.AnnotationProjectSnapshot: string(raw)}
			tmpl := exampleTemplates(t)
			tmpl.Workspace = &templates.Workspace{Enabled: true, ID: s.Spec.Workspace.ID, Claim: "shared-projects"}
			pod, err := buildPod(s, tmpl, "", true)
			if err != nil {
				t.Fatal(err)
			}
			mounts := 0
			for _, mount := range pod.Spec.Containers[0].VolumeMounts {
				if mount.Name == "workspace" {
					mounts++
				}
			}
			doc := envOf(pod.Spec.Containers[0], protocol.SessionEnv)
			actual, err := protocol.ParseSession([]byte(doc.Value))
			if err != nil || mounts != 4 || actual.Workspace == nil || actual.Workspace.ID != s.Spec.Workspace.ID || actual.Workspace.SessionUID != string(s.UID) || actual.SessionUID != string(s.UID) || string(actual.ProjectSnapshot) != string(raw) {
				t.Fatal("project executor silently lost shared mounts, Session UID or snapshot")
			}
		})
	}
}
