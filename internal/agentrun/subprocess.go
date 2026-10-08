package agentrun

import (
	"context"
	"errors"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cappedOutput limits captured subprocess output. In particular, kubectl's
// credential response stays in memory and never reaches the caller's terminal.
type cappedOutput struct {
	data     []byte
	overflow bool
}

func (b *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxResponse - len(b.data)
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func commandOutput(ctx context.Context, argv []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var out cappedOutput
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return nil, errors.New("command failed")
	}
	if out.overflow {
		return nil, errors.New("command output exceeded limit")
	}
	return out.data, nil
}

// forwardOutput recognizes only kubectl's loopback readiness line. All other
// output is discarded, and a line can never allocate more than 4 KiB.
type forwardOutput struct {
	line  []byte
	ready chan string
}

func (w *forwardOutput) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			line := strings.TrimSpace(string(w.line))
			w.line = w.line[:0]
			const prefix = "Forwarding from "
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			address, target, ok := strings.Cut(strings.TrimPrefix(line, prefix), " -> ")
			host, port, err := net.SplitHostPort(address)
			n, portErr := strconv.Atoi(port)
			if ok && target == "8443" && err == nil && host == "127.0.0.1" && portErr == nil && n > 0 && n <= 65535 {
				select {
				case w.ready <- "https://" + address:
				default:
				}
			}
		} else if len(w.line) < 4096 {
			w.line = append(w.line, b)
		}
	}
	return len(p), nil
}

func startPortForward(ctx context.Context, argv []string) (string, func(), error) {
	return portForwardWithin(ctx, argv, requestTimeout)
}

func portForwardWithin(ctx context.Context, argv []string, readinessTimeout time.Duration) (string, func(), error) {
	childCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(childCtx, argv[0], argv[1:]...)
	ready := make(chan string, 1)
	cmd.Stdout, cmd.Stderr = &forwardOutput{ready: ready}, io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		return "", nil, errors.New("port-forward could not start")
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done // CommandContext kills the process; Wait reaps it.
		})
	}
	timer := time.NewTimer(readinessTimeout)
	defer timer.Stop()
	select {
	case address := <-ready:
		select {
		case <-done:
			stop()
			return "", nil, errors.New("port-forward exited at readiness")
		default:
			return address, stop, nil
		}
	case <-done:
		stop()
		return "", nil, errors.New("port-forward exited before readiness")
	case <-ctx.Done():
		stop()
		return "", nil, errors.New("port-forward canceled")
	case <-timer.C:
		stop()
		return "", nil, errors.New("port-forward readiness timed out")
	}
}
