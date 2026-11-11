package controller

import (
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
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
