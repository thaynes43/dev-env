package agentd

// This file is deliberately unreachable from every production dispatcher, API,
// provider enum and command. It scaffolds the existing packaged helper contract
// for synthetic qualification; it grants no Cursor or credential capability.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	cursorScaffoldHelper       = "/usr/local/lib/dev-env-cursor-acp/cursor-acp-session.py"
	cursorScaffoldPython       = "/usr/bin/python3"
	cursorScaffoldPromptLimit  = 16384
	cursorScaffoldFrameLimit   = 65536
	cursorScaffoldReceiptLimit = 1 << 20
	cursorScaffoldHelperGrace  = 4 * time.Second // helper finally: TERM grace1s + wait2s
	cursorScaffoldKillWait     = 2 * time.Second
)

var (
	errCursorScaffoldRefused = errors.New("cursor scaffold input refused")
	errCursorScaffoldReplay  = errors.New("cursor scaffold invocation already attempted")
	errCursorScaffoldReceipt = errors.New("cursor scaffold receipt unconfirmed")
	errCursorScaffoldProcess = errors.New("cursor scaffold helper exit unconfirmed")
	cursorScaffoldLabel      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

type cursorScaffoldPlan struct {
	invocation, client, worktree, stateRoot, model, prompt string
	timeout                                                time.Duration
}

// newCursorScaffoldPlan does not launch anything or create a platform Launch.
// In particular it cannot register a provider or reuse a Claude/Codex login.
func newCursorScaffoldPlan(invocation, client, worktree, stateRoot, model, prompt string, timeout time.Duration) (cursorScaffoldPlan, error) {
	if !cursorScaffoldLabel.MatchString(invocation) || model != "grok-4.7-xhigh" ||
		len(prompt) == 0 || len(prompt) > cursorScaffoldPromptLimit || !utf8.ValidString(prompt) ||
		timeout < time.Second || timeout > time.Hour || timeout%time.Second != 0 {
		return cursorScaffoldPlan{}, errCursorScaffoldRefused
	}
	for _, path := range []string{client, worktree, stateRoot} {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || resolved != path {
			return cursorScaffoldPlan{}, errCursorScaffoldRefused
		}
	}
	ci, e1 := os.Lstat(client)
	wi, e2 := os.Lstat(worktree)
	si, e3 := os.Lstat(stateRoot)
	if e1 != nil || e2 != nil || e3 != nil || !ci.Mode().IsRegular() || ci.Mode().Perm()&0o111 == 0 ||
		!wi.IsDir() || !si.IsDir() || si.Mode().Perm()&0o077 != 0 {
		return cursorScaffoldPlan{}, errCursorScaffoldRefused
	}
	return cursorScaffoldPlan{invocation, client, worktree, stateRoot, model, prompt, timeout}, nil
}

type cursorScaffoldCommand struct {
	name      string
	args, env []string
	dir       string
	decisions *os.File // descriptor only; the prompt is never decision stdin
}

func (p cursorScaffoldPlan) command(promptPath, privateHome, privateTmp string, decisions *os.File) cursorScaffoldCommand {
	return cursorScaffoldCommand{name: cursorScaffoldPython, dir: p.worktree, decisions: decisions,
		args: []string{cursorScaffoldHelper, "run", "--client", p.client, "--worktree", p.worktree,
			"--model", p.model, "--prompt-file", promptPath, "--timeout", strconv.Itoa(int(p.timeout / time.Second))},
		env: []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + privateHome, "TMPDIR=" + privateTmp}}
}

type cursorScaffoldResult struct {
	requestedModel, servedModel, sessionID, stopReason, promptSHA256 string
	promptComplete                                                   bool
	// There is intentionally no assistant answer or tool-payload field: the
	// existing helper does not expose those receipts. Completion is not a task
	// deliverable, successful tool execution or served-model assertion.
}

type cursorScaffoldReceipts struct {
	plan              cursorScaffoldPlan
	result            cursorScaffoldResult
	started, terminal bool
	count             int
}

func cursorScaffoldObject(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errCursorScaffoldReceipt
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, errCursorScaffoldReceipt
		}
		if _, found := fields[key]; found {
			return nil, errCursorScaffoldReceipt
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, errCursorScaffoldReceipt
		}
		fields[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return nil, errCursorScaffoldReceipt
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errCursorScaffoldReceipt
	}
	return fields, nil
}

func (s *cursorScaffoldReceipts) line(raw []byte) error {
	s.count++
	if len(raw) == 0 || len(raw)+1 > cursorScaffoldFrameLimit || s.count > 256 || s.terminal {
		return errCursorScaffoldReceipt
	}
	fields, err := cursorScaffoldObject(raw)
	if err != nil {
		return err
	}
	text := func(key string) string {
		var v string
		if json.Unmarshal(fields[key], &v) != nil {
			return ""
		}
		return v
	}
	keys := func(names ...string) bool {
		if len(fields) != len(names) {
			return false
		}
		for _, n := range names {
			if _, ok := fields[n]; !ok {
				return false
			}
		}
		return true
	}
	switch text("event") {
	case "starting":
		hash := sha256.Sum256([]byte(s.plan.prompt))
		if s.started || !keys("event", "modelRequested", "servedModel", "promptSHA256") || text("modelRequested") != s.plan.model || text("servedModel") != "Unknown" || text("promptSHA256") != hex.EncodeToString(hash[:]) {
			return errCursorScaffoldReceipt
		}
		s.started = true
		s.result = cursorScaffoldResult{requestedModel: s.plan.model, servedModel: "Unknown", promptSHA256: text("promptSHA256")}
	case "session-created":
		if !s.started || s.result.sessionID != "" || !keys("event", "sessionID") || !cursorScaffoldLabel.MatchString(text("sessionID")) {
			return errCursorScaffoldReceipt
		}
		s.result.sessionID = text("sessionID")
	case "prompt-complete":
		if !s.started || s.result.sessionID == "" || !keys("event", "sessionID", "stopReason") || text("sessionID") != s.result.sessionID || !cursorScaffoldLabel.MatchString(text("stopReason")) {
			return errCursorScaffoldReceipt
		}
		s.terminal = true
		s.result.stopReason = text("stopReason")
	case "failed":
		return errCursorScaffoldProcess
	default:
		// Permission/question handling remains the existing helper's contract;
		// this scaffold supplies no approval fallback or new callback adapter.
		return errCursorScaffoldReceipt
	}
	return nil
}

func readCursorScaffoldReceipts(input io.Reader, plan cursorScaffoldPlan) (cursorScaffoldResult, error) {
	s := cursorScaffoldReceipts{plan: plan, result: cursorScaffoldResult{requestedModel: plan.model, servedModel: "Unknown"}}
	limited := &io.LimitedReader{R: input, N: cursorScaffoldReceiptLimit + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), cursorScaffoldFrameLimit+1)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if index := bytes.IndexByte(data, '\n'); index >= 0 {
			return index + 1, data[:index], nil
		}
		if atEOF && len(data) > 0 {
			return 0, nil, errCursorScaffoldReceipt
		}
		return 0, nil, nil
	})
	for scanner.Scan() {
		if err := s.line(scanner.Bytes()); err != nil {
			return s.result, err
		}
	}
	if scanner.Err() != nil || limited.N <= 0 || !s.started || s.result.sessionID == "" || !s.terminal {
		return s.result, errCursorScaffoldReceipt
	}
	return s.result, nil
}

type cursorScaffoldPeer interface {
	output() io.ReadCloser
	wait() error
	signal(os.Signal) error
}

type cursorScaffoldStart func(cursorScaffoldCommand) (cursorScaffoldPeer, error)

type cursorScaffoldExecPeer struct {
	cmd    *exec.Cmd
	reader *os.File
}

func (p *cursorScaffoldExecPeer) output() io.ReadCloser      { return p.reader }
func (p *cursorScaffoldExecPeer) wait() error                { return p.cmd.Wait() }
func (p *cursorScaffoldExecPeer) signal(sig os.Signal) error { return p.cmd.Process.Signal(sig) }

// startCursorScaffoldHelper is not registered anywhere. No CommandContext hard
// kill: the Python helper owns a different vendor process group and must receive
// SIGTERM plus its finally/stop_group allowance before a bounded own-child kill.
func startCursorScaffoldHelper(c cursorScaffoldCommand) (cursorScaffoldPeer, error) {
	read, write, err := os.Pipe()
	if err != nil {
		return nil, errCursorScaffoldProcess
	}
	cmd := exec.Command(c.name, c.args...)
	cmd.Dir, cmd.Env, cmd.Stdin = c.dir, c.env, c.decisions
	cmd.Stdout, cmd.Stderr = write, io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = cursorScaffoldKillWait
	if cmd.Start() != nil {
		_ = read.Close()
		_ = write.Close()
		return nil, errCursorScaffoldProcess
	}
	_ = write.Close()
	return &cursorScaffoldExecPeer{cmd: cmd, reader: read}, nil
}

func stopCursorScaffoldPeer(peer cursorScaffoldPeer, waited <-chan error, grace, killWait time.Duration) error {
	select {
	case waitErr := <-waited:
		return waitErr
	default:
	}
	_ = peer.signal(syscall.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case waitErr := <-waited:
		return waitErr
	case <-timer.C:
	}
	// This stops the owned helper, not proof that an escaped vendor group is
	// gone. Any forced/uncertain stop stays failed and its fence forbids replay.
	_ = peer.signal(syscall.SIGKILL)
	timer.Reset(killWait)
	select {
	case waitErr := <-waited:
		return waitErr
	case <-timer.C:
		return errCursorScaffoldProcess
	}
}

func prepareCursorScaffoldInvocation(p cursorScaffoldPlan) (cursorScaffoldCommand, func(), error) {
	// Consumed before any possible helper start. An existing or partial fence,
	// including a previous process failure, is not silently resumed or replaced.
	fence := filepath.Join(p.stateRoot, "cursor-scaffold-"+p.invocation+".json")
	file, err := os.OpenFile(fence, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return cursorScaffoldCommand{}, nil, errCursorScaffoldReplay
	}
	if err != nil {
		return cursorScaffoldCommand{}, nil, errCursorScaffoldRefused
	}
	hash := sha256.Sum256([]byte(p.prompt))
	data, _ := json.Marshal(map[string]any{"version": 1, "phase": "attempted", "requestedModel": p.model, "promptSHA256": hex.EncodeToString(hash[:])})
	_, writeErr := file.Write(append(data, '\n'))
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return cursorScaffoldCommand{}, nil, errCursorScaffoldRefused
	}
	dir, err := os.MkdirTemp(p.stateRoot, ".cursor-scaffold-")
	if err != nil {
		return cursorScaffoldCommand{}, nil, errCursorScaffoldRefused
	}
	cleanup := func() { _ = os.RemoveAll(dir) } // only the directory this call made
	home, tmp, prompt := filepath.Join(dir, "home"), filepath.Join(dir, "tmp"), filepath.Join(dir, "prompt.txt")
	if os.Mkdir(home, 0o700) != nil || os.Mkdir(tmp, 0o700) != nil || os.WriteFile(prompt, []byte(p.prompt), 0o600) != nil {
		cleanup()
		return cursorScaffoldCommand{}, nil, errCursorScaffoldRefused
	}
	return p.command(prompt, home, tmp, nil), cleanup, nil
}

// runCursorScaffold has no production caller. It supervises only helper receipts;
// no Launch/provider/API dispatch invokes it, no credential is discovered and no
// model answer/tool payload can be reconstructed from this contract.
func runCursorScaffold(ctx context.Context, p cursorScaffoldPlan, decisions *os.File, start cursorScaffoldStart) (cursorScaffoldResult, error) {
	result := cursorScaffoldResult{requestedModel: p.model, servedModel: "Unknown"}
	validated, err := newCursorScaffoldPlan(p.invocation, p.client, p.worktree, p.stateRoot, p.model, p.prompt, p.timeout)
	if err != nil || start == nil || decisions == nil {
		return result, errCursorScaffoldRefused
	}
	p = validated
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	command, cleanup, err := prepareCursorScaffoldInvocation(p)
	if err != nil {
		return result, err
	}
	defer cleanup()
	command.decisions = decisions
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	peer, err := start(command)
	if peer == nil {
		return result, errCursorScaffoldProcess
	}
	output := peer.output()
	waited := make(chan error, 1)
	go func() { waited <- peer.wait() }()
	waitDone := false
	defer func() {
		if !waitDone {
			_ = stopCursorScaffoldPeer(peer, waited, cursorScaffoldHelperGrace, cursorScaffoldKillWait)
		}
		if output != nil {
			_ = output.Close()
		}
	}()
	if err != nil || output == nil {
		return result, errCursorScaffoldProcess
	}
	type readResult struct {
		result cursorScaffoldResult
		err    error
	}
	read := make(chan readResult, 1)
	go func() { value, readErr := readCursorScaffoldReceipts(output, p); read <- readResult{value, readErr} }()
	readDone := false
	for !waitDone || !readDone {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case err = <-waited:
			waitDone = true
			if err != nil {
				return result, errCursorScaffoldProcess
			}
		case value := <-read:
			readDone, result = true, value.result
			if value.err != nil {
				return result, value.err
			}
		}
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if result.stopReason != "end_turn" {
		return result, errCursorScaffoldReceipt
	}
	result.promptComplete = true
	return result, nil
}
