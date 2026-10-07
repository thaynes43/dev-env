package broker

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

type fakeGrantExecutor struct {
	stream func(context.Context, remotecommand.StreamOptions) error
}

func (f fakeGrantExecutor) Stream(o remotecommand.StreamOptions) error {
	return f.stream(context.Background(), o)
}

func (f fakeGrantExecutor) StreamWithContext(ctx context.Context, o remotecommand.StreamOptions) error {
	return f.stream(ctx, o)
}

func TestExecInstallerTokenOnlyOnStdin(t *testing.T) {
	e, err := NewExecInstaller(&rest.Config{Host: "https://api.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "session-one", Namespace: "dev-agents", UID: "pod-one"}}
	g := &v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant-one"}, Spec: v1alpha1.AccessGrantSpec{
		Kube: &v1alpha1.KubeGrant{Role: v1alpha1.RoleWorkloads, Namespaces: []string{"frontend"}},
	}}
	const token = "fixture-private-token"
	install := true
	e.executor = func(_ *rest.Config, u *url.URL) (remotecommand.Executor, error) {
		if strings.Contains(u.String(), token) || u.Path != "/api/v1/namespaces/dev-agents/pods/session-one/exec" {
			t.Fatal("exec exposed the token in its request URL or used the wrong target")
		}
		cmd := strings.Join(u.Query()["command"], " ")
		if !strings.Contains(cmd, "--pod-uid pod-one") || u.Query().Get("container") != "agent" || u.Query().Get("tty") == "true" {
			t.Fatal("exec must fence the agent container by pod UID, without a TTY")
		}
		return fakeGrantExecutor{stream: func(_ context.Context, o remotecommand.StreamOptions) error {
			if o.Stdout != io.Discard || o.Stderr != io.Discard || o.Tty {
				t.Fatal("grant exec must discard both output streams")
			}
			if install {
				b, err := io.ReadAll(o.Stdin)
				if err != nil || string(b) != token+"\n" || !strings.Contains(cmd, "grant-install") || u.Query().Get("stdin") != "true" {
					t.Fatal("install did not send its token solely on stdin")
				}
			} else if o.Stdin != nil || !strings.Contains(cmd, "grant-remove") || u.Query().Get("stdin") == "true" {
				t.Fatal("remove must use no stdin")
			}
			_, _ = io.WriteString(o.Stdout, token)
			_, _ = io.WriteString(o.Stderr, token)
			return nil
		}}, nil
	}
	if err := e.Install(context.Background(), pod, g, token, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	install = false
	if err := e.Remove(context.Background(), pod, g); err != nil {
		t.Fatal(err)
	}
}

func TestExecInstallerScrubsEveryError(t *testing.T) {
	e, err := NewExecInstaller(&rest.Config{Host: "https://api.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "session-one", Namespace: "dev-agents", UID: "pod-one"}}
	g := &v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant-one"}, Spec: v1alpha1.AccessGrantSpec{Kube: &v1alpha1.KubeGrant{Role: v1alpha1.RoleNodes}}}
	const token = "fixture-private-token"
	for _, tc := range []struct {
		name string
		err  error
		old  bool
	}{
		{"legacy command", utilexec.CodeExitError{Err: errors.New(token), Code: 2}, true},
		{"no volume", utilexec.CodeExitError{Err: errors.New(token), Code: 3}, true},
		{"command failure", utilexec.CodeExitError{Err: errors.New(token), Code: 1}, false},
		{"transport failure", errors.New(token), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.executor = func(*rest.Config, *url.URL) (remotecommand.Executor, error) {
				return fakeGrantExecutor{stream: func(_ context.Context, o remotecommand.StreamOptions) error {
					_, _ = io.WriteString(o.Stderr, token)
					return tc.err
				}}, nil
			}
			err := e.Install(context.Background(), pod, g, token, time.Now().Add(time.Hour))
			if err == nil || strings.Contains(err.Error(), token) || errors.Is(err, ErrIncompatiblePod) != tc.old {
				t.Fatal("exec errors exposed credentials or misclassified the old pod")
			}
		})
	}
	e.executor = func(*rest.Config, *url.URL) (remotecommand.Executor, error) { return nil, errors.New(token) }
	if err := e.Install(context.Background(), pod, g, token, time.Now().Add(time.Hour)); err == nil || strings.Contains(err.Error(), token) {
		t.Fatal("executor construction error exposed a token")
	}
}
