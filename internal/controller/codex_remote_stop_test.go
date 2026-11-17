package controller

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/codexworkbench"
)

func TestCodexRemoteStopPodDecision(t *testing.T) {
	for _, kind := range []string{"host", "claude", "task", "local"} {
		for _, named := range []bool{false, true} {
			for _, hold := range []bool{false, true} {
				for _, spoof := range []bool{false, true} {
					t.Run(kind+"/named="+boolText(named)+"/hold="+boolText(hold)+"/spoof="+boolText(spoof), func(t *testing.T) {
						var logs bytes.Buffer
						oldLog := log.Log
						log.Log = logr.FromSlogHandler(slog.NewTextHandler(&logs, nil))
						t.Cleanup(func() { log.Log = oldLog })
						s := remotePilotTestSession()
						switch kind {
						case "claude":
							s.Spec.Agent, s.Spec.Model = v1alpha1.AgentClaude, "claude-opus-5-5"
						case "task":
							s.Spec.Mode, s.Spec.Prompt = v1alpha1.ModeTask, "synthetic"
						case "local":
							s.Spec.Agent, s.Spec.Mode, s.Spec.Model = v1alpha1.AgentClaude, v1alpha1.ModeLocal, "claude-opus-5-5"
						}
						tm := exampleTemplates(t)
						r := &Reconciler{}
						if named {
							var err error
							r.CodexRemoteStopHosts, err = codexworkbench.ParseRemoteStopHosts(s.Name, s.Namespace)
							if err != nil {
								t.Fatal(err)
							}
						}
						build := func(r *Reconciler) *corev1.Pod {
							t.Helper()
							var p *corev1.Pod
							var err error
							if hold {
								p, err = r.holdPod(s, tm)
							} else {
								p, err = r.sessionPod(s, tm, r.resolveCodexHost(s, tm))
							}
							if err != nil {
								t.Fatal(err)
							}
							return p
						}
						baseline := build(&Reconciler{})
						if spoof {
							tm.Env = append(tm.Env, corev1.EnvVar{Name: protocol.CodexRemoteStopEnv, Value: "template-spoof"})
							profile := tm.Profiles[tm.DefaultProfile]
							profile.Env = append(profile.Env, corev1.EnvVar{Name: protocol.CodexRemoteStopEnv, Value: "profile-spoof"})
							// Environment sources remain present; Kubernetes gives explicit env priority.
							profile.EnvFrom = append(profile.EnvFrom, corev1.EnvFromSource{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "stray-stop-decision"}}})
							tm.Profiles[tm.DefaultProfile] = profile
							// Match the intentionally added envFrom source in the comparison only.
							if !hold {
								baseline.Spec.Containers[0].EnvFrom = append(baseline.Spec.Containers[0].EnvFrom, profile.EnvFrom[len(profile.EnvFrom)-1])
							}
						}
						pod := build(r)
						wantLog := 0
						if named && kind != "host" {
							wantLog = 1
						}
						if got := strings.Count(logs.String(), "name has no effect on this session"); got != wantLog {
							t.Fatalf("non-host notice count=%d want=%d: %s", got, wantLog, logs.String())
						}
						c := &pod.Spec.Containers[0]
						count := 0
						for _, env := range c.Env {
							if env.Name == protocol.CodexRemoteStopEnv {
								count++
								if !named || kind != "host" || env.Value != string(s.UID) || env.ValueFrom != nil {
									t.Fatalf("wrong stop decision: %+v", env)
								}
							}
						}
						want := 0
						if named && kind == "host" {
							want = 1
						}
						if count != want {
							t.Fatalf("stop decision count=%d want=%d", count, want)
						}
						// Removing the one controller decision must restore every other pod byte.
						kept := make([]corev1.EnvVar, 0, len(c.Env))
						for _, env := range c.Env {
							if env.Name != protocol.CodexRemoteStopEnv {
								kept = append(kept, env)
							}
						}
						c.Env = kept
						a, _ := json.Marshal(pod)
						b, _ := json.Marshal(baseline)
						if !reflect.DeepEqual(a, b) {
							t.Fatalf("other pod bytes changed:\ngot %s\nwant %s", a, b)
						}
					})
				}
			}
		}
	}
}

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
