package broker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	clientscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	agentprotocol "github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// ErrIncompatiblePod is an agentd without the grant commands (exit 2), or a
// pod without the grants volume (exit 3). The broker fails it after three tries.
var ErrIncompatiblePod = errors.New("the pod does not support kube grants; a new session pod is needed")

// ExecInstaller installs tokens only through pods/exec stdin (D-63). It keeps
// no token, captures no command output, and never returns an exec's raw error:
// the target process or transport may have echoed the token into that error.
type ExecInstaller struct {
	config   *rest.Config
	client   rest.Interface
	timeout  time.Duration
	executor func(*rest.Config, *url.URL) (remotecommand.Executor, error)
}

// NewExecInstaller uses the broker's pods/exec create right: SPDY over POST,
// without a WebSocket GET that would also need pods/exec get permission.
func NewExecInstaller(cfg *rest.Config) (*ExecInstaller, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &ExecInstaller{
		config: cfg, client: cs.CoreV1().RESTClient(), timeout: 30 * time.Second,
		executor: func(cfg *rest.Config, u *url.URL) (remotecommand.Executor, error) {
			return remotecommand.NewSPDYExecutor(cfg, "POST", u)
		},
	}, nil
}

// Install implements Installer. The pod UID is checked by agentd before it
// reads the token, fencing a replacement pod that has the same name.
func (e *ExecInstaller) Install(ctx context.Context, pod *corev1.Pod, g *v1alpha1.AccessGrant, token string, expires time.Time) error {
	if g.Spec.Kube == nil || token == "" {
		return errors.New("the kube grant or its token is missing")
	}
	cmd := agentprotocol.GrantInstallCommand(g.Name, g.Spec.Kube.Role, g.Spec.Kube.Namespaces, expires, string(pod.UID))
	return e.run(ctx, pod, cmd, strings.NewReader(token+"\n"))
}

// Remove implements Installer; it fences the target by UID too.
func (e *ExecInstaller) Remove(ctx context.Context, pod *corev1.Pod, g *v1alpha1.AccessGrant) error {
	return e.run(ctx, pod, agentprotocol.GrantRemoveCommand(g.Name, string(pod.UID)), nil)
}

func (e *ExecInstaller) run(ctx context.Context, pod *corev1.Pod, command []string, stdin io.Reader) error {
	if pod.UID == "" {
		return errors.New("the exec target has no pod UID")
	}
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	req := e.client.Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Container: "agent", Command: command, Stdin: stdin != nil, Stdout: true, Stderr: true}, clientscheme.ParameterCodec)
	ex, err := e.executor(e.config, req.URL())
	if err != nil {
		return errors.New("could not open the grant exec stream")
	}
	err = ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil {
		return nil
	}
	var code utilexec.ExitError
	if errors.As(err, &code) {
		if code.ExitStatus() == 2 || code.ExitStatus() == agentprotocol.ExitNoGrantsDir {
			return ErrIncompatiblePod
		}
		return fmt.Errorf("grant exec exited with code %d", code.ExitStatus())
	}
	if ctx.Err() != nil {
		return fmt.Errorf("grant exec: %w", ctx.Err())
	}
	return errors.New("grant exec failed; command output was discarded")
}
