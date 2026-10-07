// Package podexec runs a command in a session pod's container through the API
// server's pods/exec subresource (D-08: commands go operator to pod by exec),
// over WebSocket with the SPDY fallback. The operator's rescue (D-51), the
// API's log and message routes (D-65) use it; the operator's RBAC grants
// pods/exec in dev-agents only (DESIGN-001 6.11).
package podexec

import (
	"context"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	clientscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/streaming/pkg/httpstream"
)

// Executor runs commands in pods.
type Executor struct {
	config *rest.Config
	client rest.Interface
}

// New returns an Executor that execs into pods with cfg.
func New(cfg *rest.Config) (*Executor, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Executor{config: cfg, client: cs.CoreV1().RESTClient()}, nil
}

// Run runs cmd in the pod's container, with stdin when it is not nil, and
// copies its output to stdout and stderr. A command that exits non-zero
// returns an error that k8s.io/client-go/util/exec.ExitError matches, with its
// exit status.
func (e *Executor) Run(ctx context.Context, namespace, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	req := e.client.Post().Resource("pods").Namespace(namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Container: container, Command: cmd, Stdin: stdin != nil, Stdout: true, Stderr: true}, clientscheme.ParameterCodec)
	ws, err := remotecommand.NewWebSocketExecutor(e.config, "GET", req.URL().String())
	if err != nil {
		return err
	}
	spdy, err := remotecommand.NewSPDYExecutor(e.config, "POST", req.URL())
	if err != nil {
		return err
	}
	ex, err := remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return err
	}
	return ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Stderr: stderr})
}
