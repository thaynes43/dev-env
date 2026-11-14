package main

import (
	"bytes"
	"context"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"io"
	"strings"
	"testing"
)

func TestCtlCoordinatorThreadMetadataAndTargetFences(t *testing.T) {
	for _, fault := range []string{"none", "session-uid", "pod-uid", "host", "ordinary-intent", "missing-metadata"} {
		t.Run(fault, func(t *testing.T) {
			env := map[string]string{
				"HOME": t.TempDir(), "DEV_ENV_WORKBENCH": "1", "DEV_ENV_POD_UID": "pod-uid",
				"AGENTD_CODEX_COORDINATOR_HOST": "reserved", "AGENTD_CODEX_COORDINATOR_SESSION_UID": "session-uid",
				"AGENTD_SESSION": `{"name":"reserved","sessionUID":"session-uid","repo":"dev-env","agent":"codex","mode":"remote","model":"gpt-6.1-sol","effort":"medium"}`,
			}
			expectedSession, expectedPod, host := "session-uid", "pod-uid", "reserved"
			switch fault {
			case "session-uid":
				expectedSession = "other"
			case "pod-uid":
				expectedPod = "other"
			case "host":
				host = "other"
			case "missing-metadata":
				delete(env, "AGENTD_CODEX_COORDINATOR_HOST")
			}
			args := []string{"--model", "gpt-6.1-sol", "--effort", "medium", "--name", "fresh-unique", "--host", host, "--expected-session-uid", expectedSession, "--expected-pod-uid", expectedPod}
			if fault != "ordinary-intent" {
				args = append(args, "--coordinator")
			}
			var out bytes.Buffer
			code := threadStartCtl(context.Background(), args, strings.NewReader("Make a worktree."), &out, io.Discard, func(k string) string { return env[k] })
			result, err := apiv1.DecodeThreadLaunch(bytes.TrimSpace(out.Bytes()))
			if err != nil || code != apiv1.ThreadLaunchExit(result.Outcome) {
				t.Fatalf("invalid ctl result: %d %s %v", code, &out, err)
			}
			want := "coordinator_required"
			if fault == "none" {
				want = "login_required"
			}
			if result.Error == nil || result.Error.Code != want {
				t.Fatalf("%+v want=%s", result, want)
			}
		})
	}
}
