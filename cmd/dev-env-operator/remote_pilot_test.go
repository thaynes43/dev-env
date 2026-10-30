package main

import (
	"strings"
	"testing"
)

func TestRemotePilotAgentFlags(t *testing.T) {
	const digest = "@sha256:0000000000000000000000000000000000000000000000000000000000000005"
	const image = "ghcr.io/thaynes43/dev-env:2.69.0" + digest
	o, err := parseFlags(nil)
	if err != nil || o.remotePilotAgent != nil {
		t.Fatalf("remote pilot must be off by default: %+v %v", o.remotePilotAgent, err)
	}
	for _, parent := range []string{"dev/dev-env", "dev-env-1027-111645", "dev/dev-env.client", "dev/" + strings.Repeat("s", 249)} {
		for _, img := range []string{image, "ghcr.io/thaynes43/dev-env" + digest} {
			o, err := parseFlags([]string{"--remote-pilot-agent-image=" + img, "--remote-pilot-agent-session=" + parent + "|remote-pilot_1.A"})
			if err != nil || o.remotePilotAgent == nil || o.remotePilotAgent.Image != img || o.remotePilotAgent.Parent != parent || o.remotePilotAgent.Key != "remote-pilot_1.A" {
				t.Fatalf("valid remote pilot refused or misread: %+v %v", o.remotePilotAgent, err)
			}
		}
	}
	for name, args := range map[string][]string{
		"image only":   {"--remote-pilot-agent-image=" + image},
		"session only": {"--remote-pilot-agent-session=dev/dev-env|k"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseFlags(args); err == nil || !strings.Contains(err.Error(), "must be set together") {
				t.Fatalf("incomplete pair did not fail clearly: %v", err)
			}
		})
	}
	for name, session := range map[string]string{
		"missing separator":  "dev/dev-env/k",
		"two separators":     "dev/dev-env|k|other",
		"separator parent":   "dev|dev-env|k",
		"empty parent":       "|k",
		"empty key":          "dev/dev-env|",
		"empty namespace":    "/dev-env|k",
		"empty SA":           "dev/|k",
		"two slashes":        "dev/dev-env/other|k",
		"uppercase parent":   "Dev/dev-env|k",
		"dotted namespace":   "dev.ns/dev-env|k",
		"dotted session":     "dev.env|k",
		"invalid parent":     "dev/dev_env|k",
		"parent whitespace":  "dev/dev-env |k",
		"bad key":            "dev/dev-env|-bad",
		"key slash":          "dev/dev-env|a/b",
		"key whitespace":     "dev/dev-env|a b",
		"key too long":       "dev/dev-env|" + strings.Repeat("k", 64),
		"namespace too long": strings.Repeat("n", 64) + "/dev-env|k",
		"SA too long":        "dev/" + strings.Repeat("s", 254) + "|k",
		"parent too long":    "dev/" + strings.Repeat("s", 250) + "|k",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseFlags([]string{"--remote-pilot-agent-image=" + image, "--remote-pilot-agent-session=" + session})
			if err == nil || !strings.Contains(err.Error(), "--remote-pilot-agent-session") {
				t.Fatalf("bad remote selector did not fail clearly: %v", err)
			}
		})
	}
	for name, img := range map[string]string{
		"tag only":         "ghcr.io/thaynes43/dev-env:2.69.0",
		"latest tag":       "ghcr.io/thaynes43/dev-env:latest",
		"no tag or digest": "ghcr.io/thaynes43/dev-env",
		"short digest":     "ghcr.io/thaynes43/dev-env@sha256:abc",
		"uppercase digest": "ghcr.io/thaynes43/dev-env@sha256:" + strings.Repeat("A", 64),
		"other algorithm":  "ghcr.io/thaynes43/dev-env@sha512:" + strings.Repeat("a", 64),
		"other repository": "ghcr.io/thaynes43/other" + digest,
		"other registry":   "docker.io/thaynes43/dev-env" + digest,
		"other major":      "ghcr.io/thaynes43/dev-env:3.0.0" + digest,
		"trailing text":    image + "x",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseFlags([]string{"--remote-pilot-agent-image=" + img, "--remote-pilot-agent-session=dev/dev-env|k"})
			if err == nil || !strings.Contains(err.Error(), "--remote-pilot-agent-image") {
				t.Fatalf("bad remote image did not fail clearly: %v", err)
			}
		})
	}
}

func TestRemotePilotAgentFlagsCoexist(t *testing.T) {
	const image = "ghcr.io/thaynes43/dev-env:2.69.0@sha256:0000000000000000000000000000000000000000000000000000000000000005"
	args := []string{
		"--pilot-agent-image=" + image, "--pilot-agent-session=dev-env-1027-111645/usage-pilot-1",
		"--task-pilot-agent-image=" + image, "--task-pilot-agent-session=dev-env-1019-205125/task-pilot-1",
	}
	before, err := parseFlags(args)
	if err != nil {
		t.Fatal(err)
	}
	after, err := parseFlags(append(args, "--remote-pilot-agent-image="+image, "--remote-pilot-agent-session=dev/dev-env|remote-pilot-1"))
	if err != nil || after.remotePilotAgent == nil || after.remotePilotAgent.Parent != "dev/dev-env" || after.remotePilotAgent.Key != "remote-pilot-1" {
		t.Fatalf("independent remote pair failed: %+v %v", after.remotePilotAgent, err)
	}
	if *before.pilotAgent != *after.pilotAgent || *before.taskPilotAgent != *after.taskPilotAgent {
		t.Fatal("remote flags changed an existing pilot tuple")
	}
}

// Both pairs are parsed before comparison, so argv order cannot change refusal.
func TestRemotePilotAgentFlagsRefuseCollisions(t *testing.T) {
	const image = "ghcr.io/thaynes43/dev-env:2.69.0@sha256:0000000000000000000000000000000000000000000000000000000000000005"
	for _, flag := range []string{"--pilot-agent-", "--task-pilot-agent-"} {
		for _, remoteFirst := range []bool{false, true} {
			t.Run(flag+map[bool]string{false: "legacy-first", true: "remote-first"}[remoteFirst], func(t *testing.T) {
				remote := []string{"--remote-pilot-agent-image=" + image, "--remote-pilot-agent-session=parent|key"}
				legacy := []string{flag + "image=" + image, flag + "session=parent/key"}
				args := append(append([]string{}, legacy...), remote...)
				if remoteFirst {
					args = append(remote, legacy...)
				}
				if _, err := parseFlags(args); err == nil || !strings.Contains(err.Error(), "--remote-pilot-agent-session must not equal "+flag+"session (same parent and key)") {
					t.Fatalf("collision accepted or unclear: %v", err)
				}
				// Either differing half permits coexistence.
				for _, tuple := range []string{"other|key", "parent|other"} {
					args := append(append([]string{}, legacy...), "--remote-pilot-agent-image="+image, "--remote-pilot-agent-session="+tuple)
					if _, err := parseFlags(args); err != nil {
						t.Fatal("distinct tuple refused", err)
					}
				}
			})
		}
	}
}
