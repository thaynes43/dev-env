package controller

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func TestProjectPodTransportStrictBindingAndDefaultDisabled(t *testing.T) {
	tmpl := exampleTemplates(t)
	s := taskSession()
	s.Spec.Base = "main"
	catalog, err := projectcatalog.Parse([]byte(`{"version":1,"repositories":{"haynes-ops":{"github":"thaynes43/haynes-ops"}},"projects":{"sample":{"repositories":[{"name":"haynes-ops"}],"rules":"exact project rules"}}}`))
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
	s.Annotations = map[string]string{v1alpha1.AnnotationProjectSnapshot: string(raw)}
	tmpl.Env = append(tmpl.Env, corev1.EnvVar{Name: "AGENTD_ENABLE_CODEX_TASKS", Value: "true"})
	for _, enabled := range []bool{false, true} {
		pod, err := buildPod(s, tmpl, "", enabled)
		if err != nil {
			t.Fatal(err)
		}
		c := pod.Spec.Containers[0]
		lastGate := ""
		for _, e := range c.Env {
			if e.Name == "AGENTD_ENABLE_CODEX_TASKS" {
				lastGate = e.Value
			}
		}
		if lastGate != map[bool]string{false: "false", true: "true"}[enabled] {
			t.Fatal("profile configuration widened the controller-owned managed gate")
		}
		doc := envOf(c, protocol.SessionEnv)
		parsed, err := protocol.ParseSession([]byte(doc.Value))
		if err != nil || parsed.SessionUID != string(s.UID) || string(parsed.ProjectSnapshot) != string(raw) {
			t.Fatal("controller transport lost exact snapshot or Session UID")
		}
	}
	for _, kind := range []string{"base", "repo", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			changed := s.DeepCopy()
			switch kind {
			case "base":
				changed.Spec.Base = "foreign"
			case "repo":
				changed.Spec.Repo = "foreign"
			case "unknown":
				changed.Annotations[v1alpha1.AnnotationProjectSnapshot] = `{"unknown":true}`
			}
			if _, err := buildPod(changed, tmpl, "", true); err == nil {
				t.Fatal("changed snapshot binding produced an executor")
			}
		})
	}
	legacy := taskSession()
	pod, err := buildPod(legacy, exampleTemplates(t), "")
	if err != nil || envOf(pod.Spec.Containers[0], "AGENTD_ENABLE_CODEX_TASKS").Value != "false" {
		t.Fatal("legacy Claude launch no longer works with features off")
	}
}
