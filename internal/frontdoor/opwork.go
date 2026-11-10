package frontdoor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const opworkTimeout = 90 * time.Second
const opworkOutputCap = 24_000

// opapiToken is the OpenProject token shape. It is stripped from tool text.
var opapiToken = regexp.MustCompile(`opapi-[0-9A-Za-z]{16,}`)

// opwork runs the tracker CLI with a fixed argument vector. The credential
// stays in the file named by tokenFile; it is not an argument and not logged.
type opwork struct {
	path      string
	tokenFile string
	project   string
}

func (o opwork) call(ctx context.Context, args ...string) (string, error) {
	if o.path == "" {
		return "", errors.New("opwork is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, opworkTimeout)
	defer cancel()
	argv := append([]string{"--project=" + o.project}, args...)
	cmd := exec.CommandContext(ctx, o.path, argv...)
	cmd.Env = childEnv(o.tokenFile, o.project)
	var stdout, stderr capBuffer
	stdout.max, stderr.max = opworkOutputCap, opworkOutputCap
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	text := joinOutput(stdout.String(), stderr.String(), stdout.truncated || stderr.truncated)
	text = scrub(text, "")
	if err == nil {
		return text, nil
	}
	if ctx.Err() != nil {
		return text, errors.New("opwork timed out")
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return text, fmt.Errorf("opwork exited %d", exit.ExitCode())
	}
	return text, errors.New("opwork could not be started")
}

func childEnv(tokenFile, project string) []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	env := []string{"PATH=" + path, "OPWORK_WRITE_ORIGIN=meatball", "OPWORK_SESSION=meatball", "OPWORK_AGENT=meatball", "OPWORK_TOKEN_FILE=" + tokenFile, "OPWORK_PROJECT=" + project}
	for _, key := range []string{"HOME", "LANG", "LC_ALL", "TMPDIR", "TZ"} {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func joinOutput(stdout, stderr string, truncated bool) string {
	stdout, stderr = strings.TrimRight(stdout, "\n"), strings.TrimRight(stderr, "\n")
	text := stdout
	if stderr != "" {
		if text != "" {
			text += "\n"
		}
		text += stderr
	}
	if truncated {
		text += "\n[truncated]"
	}
	return text
}

func scrub(text, bearer string) string {
	text = opapiToken.ReplaceAllString(text, "[redacted]")
	if bearer != "" && strings.Contains(text, bearer) {
		text = strings.ReplaceAll(text, bearer, "[redacted]")
	}
	return text
}

// capBuffer keeps the first max bytes and reports when later bytes were dropped.
type capBuffer struct {
	buf       bytes.Buffer
	max       int
	n         int
	truncated bool
}

func (w *capBuffer) Write(p []byte) (int, error) {
	w.n += len(p)
	if w.buf.Len() < w.max {
		room := w.max - w.buf.Len()
		chunk := p
		if len(chunk) > room {
			chunk = chunk[:room]
			w.truncated = true
		}
		_, _ = w.buf.Write(chunk)
	} else if len(p) > 0 {
		w.truncated = true
	}
	return len(p), nil
}

func (w *capBuffer) String() string { return w.buf.String() }
