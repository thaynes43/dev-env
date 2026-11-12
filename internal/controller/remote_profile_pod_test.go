package controller

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
	"github.com/thaynes43/dev-env/internal/templates"
)

func opsTemplatesWithRemote(t *testing.T) *templates.Templates {
	t.Helper()
	b, err := os.ReadFile("../templates/testdata/templates.yaml")
	if err != nil {
		t.Fatal(err)
	}
	const old = "  ops:\n    env:\n"
	block := "  ops:\n" +
		"    remote:\n" +
		"      env:\n" +
		"        - { name: AGENTD_CLAUDE_LIVE_FILE, value: /opt/dev-env/claude-access/access.json }\n" +
		"      mounts:\n" +
		"        - { name: claude-access, secret: dev-env-claude-live, path: /opt/dev-env/claude-access, defaultMode: 0440 }\n" +
		"    env:\n"
	if !strings.Contains(string(b), old) {
		t.Fatal("the example ops profile moved")
	}
	doc := strings.Replace(string(b), old, block, 1)
	tmpl, err := templates.Parse(map[string]string{templates.Key: doc})
	if err != nil {
		t.Fatal(err)
	}
	return tmpl
}

func sessionMode(mode v1alpha1.SessionMode) *v1alpha1.AgentSession {
	s := taskSession()
	s.Spec.Profile = "ops"
	s.Spec.Mode = mode
	if mode != v1alpha1.ModeTask {
		s.Spec.Prompt, s.Spec.Limits = "", nil
	}
	return s
}

func remoteProjectionPresent(pod *corev1.Pod) (env, volume, mount bool) {
	c := pod.Spec.Containers[0]
	if e := envOf(c, "AGENTD_CLAUDE_LIVE_FILE"); e != nil && e.Value == "/opt/dev-env/claude-access/access.json" && e.ValueFrom == nil {
		env = true
	}
	for _, v := range pod.Spec.Volumes {
		if v.Name == "claude-access" && v.Secret != nil && v.Secret.SecretName == "dev-env-claude-live" && v.Secret.DefaultMode != nil && *v.Secret.DefaultMode == 0o440 {
			volume = true
		}
	}
	for _, m := range c.VolumeMounts {
		if m.Name == "claude-access" && m.MountPath == "/opt/dev-env/claude-access" && m.ReadOnly {
			mount = true
		}
	}
	return env, volume, mount
}

func TestRemoteBlockReachesRemoteAgentPodOnly(t *testing.T) {
	tmpl := opsTemplatesWithRemote(t)
	for _, mode := range []v1alpha1.SessionMode{v1alpha1.ModeTask, v1alpha1.ModeLocal, v1alpha1.ModeRemote} {
		t.Run(string(mode), func(t *testing.T) {
			pod, err := buildPod(sessionMode(mode), tmpl, "")
			if err != nil {
				t.Fatal(err)
			}
			env, volume, mount := remoteProjectionPresent(pod)
			want := mode == v1alpha1.ModeRemote
			if env != want || volume != want || mount != want {
				t.Fatalf("%s pod projection env=%v volume=%v mount=%v, want %v", mode, env, volume, mount, want)
			}
			c := pod.Spec.Containers[0]
			if envOf(c, "CIGAR_JOURNAL_TOKEN") == nil {
				t.Fatal("the profile's own env was dropped")
			}
			if !volumeNamed(pod, "gh-token") {
				t.Fatal("the profile's own mount was dropped")
			}
		})
	}
	t.Run("hold of a remote session", func(t *testing.T) {
		pod, err := buildHoldPod(sessionMode(v1alpha1.ModeRemote), tmpl)
		if err != nil {
			t.Fatal(err)
		}
		env, volume, mount := remoteProjectionPresent(pod)
		if env || volume || mount {
			t.Fatalf("hold pod has the remote block: env=%v volume=%v mount=%v", env, volume, mount)
		}
		if !volumeNamed(pod, "gh-token") {
			t.Fatal("rescue lost the profile mount at /creds")
		}
	})
}

func volumeNamed(pod *corev1.Pod, name string) bool {
	for _, v := range pod.Spec.Volumes {
		if v.Name == name {
			return true
		}
	}
	return false
}

func TestRemoteBlockSkippedForCanaryAndPilot(t *testing.T) {
	remote := sessionMode(v1alpha1.ModeRemote)
	if !remoteProfileContent(remote, false) {
		t.Fatal("a remote agent pod lost the block")
	}
	if remoteProfileContent(remote, true) {
		t.Fatal("a hold pod receives the block")
	}
	for _, mode := range []v1alpha1.SessionMode{v1alpha1.ModeTask, v1alpha1.ModeLocal} {
		if remoteProfileContent(sessionMode(mode), false) {
			t.Fatalf("%s receives the block", mode)
		}
	}
	canary := sessionMode(v1alpha1.ModeRemote)
	canary.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{}
	if remoteProfileContent(canary, false) {
		t.Fatal("a canary receives the block")
	}
	pilot := sessionMode(v1alpha1.ModeRemote)
	pilot.Annotations = map[string]string{projectcanary.PilotAnnotation: "1"}
	if remoteProfileContent(pilot, false) {
		t.Fatal("a pilot receives the block")
	}
	if remoteProfileContent(nil, false) {
		t.Fatal("a nil session receives the block")
	}
}

// A rendered canary and a rendered pilot executor, on a copy of each fixture's
// templates document with a remote block added, carry neither the block's
// variable nor its volume or mount. The shared fixture is left unchanged.
func TestRemoteBlockAbsentFromRenderedCanaryAndPilot(t *testing.T) {
	canary := testutil.New(t, "claude", "dev/dev-env")
	canaryDoc := canary.Template.Data[templates.Key]
	canaryTmpl := copyWithRemoteBlock(t, canaryDoc, canary.Record.Profile)
	rev, err := canaryTmpl.ReviewRevision()
	if err != nil {
		t.Fatal(err)
	}
	rec := canary.Record
	rec.TemplateRevision = rev
	canary.Session.Spec.ProjectCanary.TemplateRevision = rev
	canary.Session.Spec.ProjectCanary.PolicyDigest = rec.Digest()
	pod, err := buildManagedPod(canary.Session, canaryTmpl, "https://operator.example", true, true, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pod.Labels[v1alpha1.LabelProjectCanary] == "" {
		t.Fatal("the rendered pod is not a project canary")
	}
	assertRemoteBlockAbsent(t, pod)

	pilotFix := testutil.NewPilot(t)
	pilotDoc := pilotFix.Template.Data[templates.Key]
	pilotTmpl := copyWithRemoteBlock(t, pilotDoc, projectcanary.PilotProfile)
	rev, err = pilotTmpl.ReviewRevision()
	if err != nil {
		t.Fatal(err)
	}
	policy := *pilotFix.Policy
	policy.TemplateRevision = rev
	pilotFix.Policy = &policy
	session := pilotFix.Session(t, 0)
	pod, err = buildManagedPod(session, pilotTmpl, "https://operator.example", true, false, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !projectcanary.IsPilot(session) || pod.Spec.RestartPolicy != corev1.RestartPolicyNever || pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != projectcanary.PilotActiveDeadlineSeconds {
		t.Fatal("the rendered pod is not a pilot executor")
	}
	assertRemoteBlockAbsent(t, pod)
	if canary.Template.Data[templates.Key] != canaryDoc || pilotFix.Template.Data[templates.Key] != pilotDoc {
		t.Fatal("the shared fixture document was modified")
	}
}

func copyWithRemoteBlock(t *testing.T, doc, profile string) *templates.Templates {
	t.Helper()
	if strings.Contains(doc, "AGENTD_CLAUDE_LIVE_FILE") {
		t.Fatal("the fixture document already carries the block")
	}
	var decoded templates.Templates
	if err := json.Unmarshal([]byte(doc), &decoded); err != nil {
		t.Fatal(err)
	}
	p, ok := decoded.Profiles[profile]
	if !ok {
		t.Fatalf("fixture profile %q is missing", profile)
	}
	mode := int32(0o440)
	p.Remote = &templates.ProfileRemote{
		Env:    []corev1.EnvVar{{Name: "AGENTD_CLAUDE_LIVE_FILE", Value: "/opt/dev-env/claude-access/access.json"}},
		Mounts: []templates.Mount{{Name: "claude-access", Secret: "dev-env-claude-live", Path: "/opt/dev-env/claude-access", DefaultMode: &mode}},
	}
	decoded.Profiles[profile] = p
	raw, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := templates.Parse(map[string]string{templates.Key: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := tmpl.Profile(profile)
	if err != nil || got.Remote == nil || len(got.Remote.Env) != 1 || len(got.Remote.Mounts) != 1 {
		t.Fatalf("the copy has no remote block: %+v %v", got.Remote, err)
	}
	return tmpl
}

func assertRemoteBlockAbsent(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	c := pod.Spec.Containers[0]
	if envOf(c, "AGENTD_CLAUDE_LIVE_FILE") != nil {
		t.Fatal("the pod has the block's variable")
	}
	if volumeNamed(pod, "claude-access") {
		t.Fatal("the pod has the block's volume")
	}
	for _, m := range c.VolumeMounts {
		if m.Name == "claude-access" || m.MountPath == "/opt/dev-env/claude-access" {
			t.Fatal("the pod has the block's mount")
		}
	}
}
