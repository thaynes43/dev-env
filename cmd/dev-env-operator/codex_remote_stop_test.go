package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/codexworkbench"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCodexRemoteStopFlags(t *testing.T) {
	names := make([]string, codexworkbench.MaxRemoteStopHosts)
	for i := range names {
		names[i] = fmt.Sprintf("host-%d", i)
	}
	for _, args := range [][]string{nil, {"--codex-remote-stop-hosts="}, {"--codex-remote-stop-hosts=host-0"}, {"--codex-remote-stop-hosts=" + strings.Join(names, ","), "--session-namespace=elsewhere"}} {
		o, err := parseFlags(args)
		if err != nil {
			t.Fatalf("valid %v: %v", args, err)
		}
		r := reconcilerForOptions(o)
		for i, name := range names {
			s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: o.sessionNamespace}, Spec: v1alpha1.AgentSessionSpec{Agent: v1alpha1.AgentCodex, Mode: v1alpha1.ModeRemote}}
			want := len(args) > 0 && strings.Contains(args[0], "host-") && (i == 0 || len(args) == 2)
			if r.CodexRemoteStopHosts.Names(s) != want {
				t.Fatalf("flag not wired: %v %s", args, name)
			}
			s.Namespace = "wrong-namespace"
			if r.CodexRemoteStopHosts.Names(s) {
				t.Fatal("names cross namespace")
			}
		}
	}
	for _, raw := range []string{strings.Join(append(names, "one-too-many"), ","), "a,a", "a,,b", "a,", ",a", "a,b a", " a", "A", "*", "a/b", strings.Repeat("a", 64)} {
		if _, err := parseFlags([]string{"--codex-remote-stop-hosts=" + raw}); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	if _, err := parseFlags([]string{"--codex-remote-stop-hosts=a", "--codex-remote-stop-hosts=a"}); err == nil {
		t.Fatal("accepted repeated flag")
	}
}
