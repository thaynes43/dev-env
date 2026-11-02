package controller

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/taskbudget"
	"github.com/thaynes43/dev-env/internal/templates"
)

func ordinaryOptInSession(t *testing.T) *v1alpha1.AgentSession {
	t.Helper()
	s := taskSession()
	s.Spec.Profile = "dev"
	s.Spec.Base = "main"
	s.Spec.Parent = "parent-session"
	s.Spec.CoordinatorInstructionParentUID = "parent-uid"
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
	return s
}

func instructionValues(pod *corev1.Pod) []string {
	if pod == nil {
		return nil
	}
	var got []string
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == coordinatorInstructionsEnv {
			got = append(got, e.Value)
		}
	}
	return got
}

func TestCoordinatorInstructionPodEnvDefaultOffAndSingleOptIn(t *testing.T) {
	tmpl := exampleTemplates(t)
	plain := taskSession()
	base, err := buildPod(plain, tmpl, "https://api")
	if err != nil {
		t.Fatal(err)
	}
	again, err := buildPod(taskSession(), tmpl, "https://api")
	if err != nil {
		t.Fatal(err)
	}
	if base.UID != "" || again.UID != "" || !apiequality.Semantic.DeepEqual(base.Spec, again.Spec) {
		t.Fatal("default pod build assigned a UID or changed an existing spec")
	}
	explicit := taskSession()
	explicit.Spec.CoordinatorInstructions = false
	off, err := buildPod(explicit, tmpl, "https://api")
	if err != nil || !apiequality.Semantic.DeepEqual(base.Spec, off.Spec) || len(instructionValues(off)) != 0 {
		t.Fatal("explicit false changed the pod or enabled instructions")
	}
	if e := envOf(base.Spec.Containers[0], protocol.PodUIDEnv); e == nil || e.ValueFrom == nil || e.ValueFrom.FieldRef == nil || e.ValueFrom.FieldRef.FieldPath != "metadata.uid" {
		t.Fatal("active pod UID still comes from the downward API")
	}
	if base.OwnerReferences[0].UID != plain.UID || base.Spec.ServiceAccountName == "" || base.Spec.Containers[0].Image == "" {
		t.Fatal("default pod lost its owner, account, or image")
	}

	parentOnly := ordinaryOptInSession(t)
	parentPod, err := buildPod(parentOnly, tmpl, "https://api")
	if err != nil {
		t.Fatal(err)
	}
	if len(instructionValues(parentPod)) != 0 {
		t.Fatal("parent UID without the opt-in enabled instructions")
	}
	opt := ordinaryOptInSession(t)
	opt.Spec.CoordinatorInstructions = true
	pod, err := buildPod(opt, tmpl, "https://api")
	if err != nil {
		t.Fatal(err)
	}
	if got := instructionValues(pod); len(got) != 1 || got[0] != "true" {
		t.Fatalf("opt-in env %v", got)
	}
	if pod.UID != "" || pod.OwnerReferences[0].UID != opt.UID || pod.Spec.ServiceAccountName != parentPod.Spec.ServiceAccountName || pod.Spec.Containers[0].Image != parentPod.Spec.Containers[0].Image {
		t.Fatal("opt-in changed pod UID, owner, account, or image")
	}
	if !apiequality.Semantic.DeepEqual(pod.Spec.Volumes, parentPod.Spec.Volumes) || !apiequality.Semantic.DeepEqual(pod.Spec.Containers[0].VolumeMounts, parentPod.Spec.Containers[0].VolumeMounts) {
		t.Fatal("opt-in changed volumes or mounts")
	}
	filtered := pod.DeepCopy()
	env := filtered.Spec.Containers[0].Env[:0]
	for _, e := range filtered.Spec.Containers[0].Env {
		if e.Name != coordinatorInstructionsEnv {
			env = append(env, e)
		}
	}
	filtered.Spec.Containers[0].Env = env
	if !apiequality.Semantic.DeepEqual(filtered.Spec, parentPod.Spec) {
		t.Fatal("opt-in changed more than the one instruction env value")
	}

	widened := exampleTemplates(t)
	widened.Env = append(widened.Env, corev1.EnvVar{Name: coordinatorInstructionsEnv, Value: "true"}, corev1.EnvVar{Name: coordinatorInstructionsEnv, Value: "TRUE"})
	stripped, err := buildPod(ordinaryOptInSession(t), widened, "https://api")
	if err != nil || len(instructionValues(stripped)) != 0 {
		t.Fatal("template env enabled instructions while the spec flag was off")
	}
	enabled, err := buildPod(opt, widened, "https://api")
	if err != nil {
		t.Fatal(err)
	}
	if got := instructionValues(enabled); len(got) != 1 || got[0] != "true" {
		t.Fatalf("duplicate template keys survived: %v", got)
	}
}

func TestCoordinatorInstructionPodEnvRejectsOutOfScope(t *testing.T) {
	tmpl := exampleTemplates(t)
	tmpl.Workspace = &templates.Workspace{Enabled: true, Claim: "retained-projects", ID: "projects-v2"}
	cases := []struct {
		name   string
		edit   func(*v1alpha1.AgentSession)
		wantOK bool
	}{
		{name: "shared", wantOK: true, edit: func(s *v1alpha1.AgentSession) {
			s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"}
		}},
		{name: "canary", edit: func(s *v1alpha1.AgentSession) {
			s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{}
		}},
		{name: "pilot", edit: func(s *v1alpha1.AgentSession) {
			s.Annotations[projectcanary.PilotAnnotation] = "marked"
		}},
		{name: "budget", edit: func(s *v1alpha1.AgentSession) {
			s.Annotations[taskbudget.UIDAnnotation] = "budget-uid"
		}},
		{name: "remote", edit: func(s *v1alpha1.AgentSession) {
			s.Spec.Mode = v1alpha1.ModeRemote
			s.Spec.Prompt = ""
		}},
		{name: "no parent uid", wantOK: true, edit: func(s *v1alpha1.AgentSession) {
			s.Spec.CoordinatorInstructionParentUID = ""
		}},
		{name: "no snapshot", wantOK: true, edit: func(s *v1alpha1.AgentSession) {
			delete(s.Annotations, v1alpha1.AnnotationProjectSnapshot)
		}},
		{name: "hold", wantOK: true, edit: func(*v1alpha1.AgentSession) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := ordinaryOptInSession(t)
			s.Spec.CoordinatorInstructions = true
			tc.edit(s)
			var (
				pod *corev1.Pod
				err error
			)
			if tc.name == "hold" {
				pod, err = buildHoldPod(s, tmpl)
			} else {
				pod, err = buildPod(s, tmpl, "https://api")
			}
			if tc.wantOK && err != nil {
				t.Fatal(err)
			}
			if pod != nil && len(instructionValues(pod)) != 0 {
				t.Fatalf("%s enabled instructions: %v", tc.name, instructionValues(pod))
			}
			if pod != nil && pod.UID != "" {
				t.Fatal("builder assigned a pod UID")
			}
		})
	}
}
