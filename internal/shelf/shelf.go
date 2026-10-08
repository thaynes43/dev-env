// Package shelf is the operator's side of the shelf pod (D-67): a pod in the
// session namespace that mounts only the shared volume and runs `agentd
// shelf`. The operator finds it by label and lists and prunes the rescues on
// the shared volume there by exec, as it rescues a session (D-51).
package shelf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const (
	// AppName is the shelf pod's app.kubernetes.io/name label; haynes-ops'
	// Deployment sets it.
	AppName = "dev-env-shelf"
	// Container is the shelf pod's container that runs agentd.
	Container = "shelf"

	// maxOutput caps what an exec may print: a list of rescues is a few
	// hundred bytes a rescue.
	maxOutput = 8 << 20
	// defaultTimeout bounds one exec.
	defaultTimeout = time.Minute
)

// ErrNoShelf is the answer while no shelf pod runs and is ready.
var ErrNoShelf = errors.New("no shelf pod (app.kubernetes.io/name=" + AppName + ") is running and ready")

// Executor runs a command in a pod's container (podexec.Executor).
type Executor interface {
	Run(ctx context.Context, namespace, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error
}

// Shelf reaches the shelf pod.
type Shelf struct {
	// Reader lists pods from the API server: the operator's cache holds
	// session pods only.
	Reader client.Reader
	Exec   Executor
	// Namespace is the session namespace, where the shelf runs.
	Namespace string
	// Timeout bounds one exec; zero means a minute.
	Timeout time.Duration
}

// Pod is the shelf pod to exec into: Running, Ready, not being deleted, the
// newest if a rollout leaves two.
func (s *Shelf) Pod(ctx context.Context) (string, error) {
	var pods corev1.PodList
	if err := s.Reader.List(ctx, &pods, client.InNamespace(s.Namespace),
		client.MatchingLabels{"app.kubernetes.io/name": AppName}); err != nil {
		return "", fmt.Errorf("list the shelf pods: %w", err)
	}
	var best *corev1.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning || !ready(p) {
			continue
		}
		if best == nil || p.CreationTimestamp.After(best.CreationTimestamp.Time) {
			best = p
		}
	}
	if best == nil {
		return "", ErrNoShelf
	}
	return best.Name, nil
}

func ready(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// List is `agentd ctl rescues [--session S]` in the shelf.
func (s *Shelf) List(ctx context.Context, session string) (protocol.RescueList, error) {
	cmd := []string{"agentd", "ctl", "rescues"}
	if session != "" {
		cmd = append(cmd, "--session", session)
	}
	var list protocol.RescueList
	err := s.run(ctx, cmd, nil, &list)
	return list, err
}

// Hold is `agentd ctl hold-rescue <id>` in the shelf: a restore's check,
// which also keeps the rescue from the next prune (D-67).
func (s *Shelf) Hold(ctx context.Context, id string) (protocol.HoldResult, error) {
	var res protocol.HoldResult
	err := s.run(ctx, []string{"agentd", "ctl", "hold-rescue", id}, nil, &res)
	return res, err
}

// Prune is `agentd ctl prune` in the shelf, with the request on stdin.
func (s *Shelf) Prune(ctx context.Context, req protocol.PruneRequest) (protocol.PruneReport, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return protocol.PruneReport{}, err
	}
	var rep protocol.PruneReport
	err = s.run(ctx, []string{"agentd", "ctl", "prune"}, bytes.NewReader(body), &rep)
	return rep, err
}

func (s *Shelf) run(ctx context.Context, cmd []string, stdin io.Reader, out any) error {
	if s.Exec == nil {
		return errors.New("this operator cannot exec into pods")
	}
	pod, err := s.Pod(ctx)
	if err != nil {
		return err
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr := &capped{max: maxOutput}, &capped{max: 4 << 10}
	if err := s.Exec.Run(ctx, s.Namespace, pod, Container, cmd, stdin, stdout, stderr); err != nil {
		return fmt.Errorf("%s in the shelf pod %s: %w: %s", strings.Join(cmd[1:], " "), pod, err, strings.TrimSpace(stderr.String()))
	}
	if stdout.over {
		return fmt.Errorf("%s in the shelf pod %s printed more than %d bytes", strings.Join(cmd[1:], " "), pod, maxOutput)
	}
	if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
		return fmt.Errorf("%s in the shelf pod %s: the output is not JSON: %w", strings.Join(cmd[1:], " "), pod, err)
	}
	return nil
}

// capped keeps the first max bytes written and notes that more came. It has
// no WriteString, so io.WriteString cannot pass the cap.
type capped struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room < len(p) {
		c.over = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *capped) Bytes() []byte  { return c.buf.Bytes() }
func (c *capped) String() string { return c.buf.String() }
