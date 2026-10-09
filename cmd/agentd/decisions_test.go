package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

type unreadDecisionInput struct{ read bool }

func (r *unreadDecisionInput) Read([]byte) (int, error) { r.read = true; return 0, io.EOF }

func TestDecisionTargetUIDFencePrecedesInputAndPrivateContext(t *testing.T) {
	env := map[string]string{"HOME": t.TempDir(), "DEV_ENV_POD_UID": "pod-current", "AGENTD_SESSION": `{"sessionUID":"session-current","name":"r-1","repo":"r","agent":"claude","mode":"task","model":"claude-opus-5-5","prompt":"p"}`}
	for _, target := range []struct{ pod, session string }{{"pod-old", "session-current"}, {"pod-current", "session-old"}, {"", "session-current"}, {"pod-current", ""}} {
		for _, action := range []string{"decision-read", "decision-answer"} {
			r := &unreadDecisionInput{}
			var out, stderr bytes.Buffer
			code := run(context.Background(), []string{"ctl", action, "--expected-pod-uid", target.pod, "--expected-session-uid", target.session}, r, &out, &stderr, func(k string) string { return env[k] }, noRunner{})
			if code == exitOK || r.read || out.Len() != 0 {
				t.Fatal("foreign target read input or private question context")
			}
		}
	}
}

func TestDecisionCLIRefusesModelIdentityClaimsAndDisabledFeature(t *testing.T) {
	env := map[string]string{"HOME": t.TempDir()}
	for _, raw := range []string{`{"question":"synthetic?","sessionUID":"forged"}`, `{"question":"synthetic?","path":"/tmp/forged"}`, `{"question":"synthetic?"}`} {
		var out, stderr bytes.Buffer
		code := run(context.Background(), []string{"ask-decision"}, strings.NewReader(raw), &out, &stderr, func(k string) string { return env[k] }, noRunner{})
		if code == exitOK || out.Len() != 0 {
			t.Fatal("model identity claim or disabled feature created question")
		}
	}
}
