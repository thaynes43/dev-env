package agentd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func cursorScaffoldFixture(t *testing.T) cursorScaffoldPlan {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work, state, client := filepath.Join(root, "work"), filepath.Join(root, "state"), filepath.Join(root, "fake-client")
	if os.Mkdir(work, 0o700) != nil || os.Mkdir(state, 0o700) != nil || os.WriteFile(client, []byte("synthetic-not-executed"), 0o700) != nil {
		t.Fatal("fixture creation")
	}
	p, err := newCursorScaffoldPlan("invocation-1", client, work, state, "grok-4.7-xhigh", "private bounded work order", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func cursorScaffoldDecisionPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = read.Close(); _ = write.Close() })
	return read, write
}

func cursorScaffoldFrames(p cursorScaffoldPlan) []string {
	hash := sha256.Sum256([]byte(p.prompt))
	return []string{fmt.Sprintf(`{"event":"starting","modelRequested":%q,"servedModel":"Unknown","promptSHA256":%q}`, p.model, hex.EncodeToString(hash[:])), `{"event":"session-created","sessionID":"synthetic-session"}`, `{"event":"prompt-complete","sessionID":"synthetic-session","stopReason":"end_turn"}`}
}

func cursorScaffoldStream(frames []string) string { return strings.Join(frames, "\n") + "\n" }

type cursorScaffoldFakePeer struct {
	reader   io.ReadCloser
	done     chan error
	mu       sync.Mutex
	signals  []os.Signal
	onSignal func(os.Signal)
}

func (p *cursorScaffoldFakePeer) output() io.ReadCloser { return p.reader }
func (p *cursorScaffoldFakePeer) wait() error           { return <-p.done }
func (p *cursorScaffoldFakePeer) signal(s os.Signal) error {
	p.mu.Lock()
	p.signals = append(p.signals, s)
	p.mu.Unlock()
	if p.onSignal != nil {
		p.onSignal(s)
	}
	return nil
}
func cursorScaffoldExited(stream string, exit error) *cursorScaffoldFakePeer {
	p := &cursorScaffoldFakePeer{reader: io.NopCloser(strings.NewReader(stream)), done: make(chan error, 1)}
	p.done <- exit
	return p
}

func TestCursorScaffoldExactPlanAndPrivatePrompt(t *testing.T) {
	p := cursorScaffoldFixture(t)
	decisions, writer := cursorScaffoldDecisionPipe(t)
	if _, err := writer.Write([]byte("synthetic decision input\n")); err != nil {
		t.Fatal(err)
	}
	var promptPath, privateHome, privateTmp string
	start := func(c cursorScaffoldCommand) (cursorScaffoldPeer, error) {
		want := []string{cursorScaffoldHelper, "run", "--client", p.client, "--worktree", p.worktree, "--model", p.model, "--prompt-file", c.args[9], "--timeout", "1"}
		if c.name != cursorScaffoldPython || !reflect.DeepEqual(c.args, want) || c.dir != p.worktree || c.decisions != decisions {
			t.Fatalf("command differs: %#v", c)
		}
		promptPath = c.args[9]
		data, err := os.ReadFile(promptPath)
		if err != nil || string(data) != p.prompt {
			t.Fatal("private prompt missing")
		}
		info, err := os.Stat(promptPath)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("prompt mode")
		}
		if strings.Contains(strings.Join(c.args, " "), p.prompt) {
			t.Fatal("prompt leaked into argv")
		}
		got := make([]byte, len("synthetic decision input\n"))
		if _, err := io.ReadFull(c.decisions, got); err != nil || string(got) == p.prompt {
			t.Fatal("prompt became decisions")
		}
		if len(c.env) != 3 || c.env[0] != "PATH=/usr/local/bin:/usr/bin:/bin" {
			t.Fatal("environment inherited")
		}
		privateHome = strings.TrimPrefix(c.env[1], "HOME=")
		privateTmp = strings.TrimPrefix(c.env[2], "TMPDIR=")
		for _, dir := range []string{privateHome, privateTmp} {
			info, err := os.Stat(dir)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatal("private runtime root")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatal("provider home inherited files")
			}
		}
		return cursorScaffoldExited(cursorScaffoldStream(cursorScaffoldFrames(p)), nil), nil
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "NEVER-PASS")
	t.Setenv("CURSOR_API_KEY", "NEVER-PASS")
	t.Setenv("HOME", "/not-used")
	result, err := runCursorScaffold(context.Background(), p, decisions, start)
	if err != nil || !result.promptComplete || result.servedModel != "Unknown" || result.requestedModel != p.model || result.sessionID != "synthetic-session" {
		t.Fatalf("bad result: %#v %v", result, err)
	}
	for _, path := range []string{promptPath, privateHome, privateTmp} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("private invocation not cleaned")
		}
	}
	fence := filepath.Join(p.stateRoot, "cursor-scaffold-"+p.invocation+".json")
	data, err := os.ReadFile(fence)
	if err != nil || bytes.Contains(data, []byte(p.prompt)) {
		t.Fatal("missing/private-content fence")
	}
	info, _ := os.Stat(fence)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("fence mode")
	}
}

func TestCursorScaffoldRefusesInputAndNoCredentialFallback(t *testing.T) {
	p := cursorScaffoldFixture(t)
	for _, change := range []func(*cursorScaffoldPlan){func(p *cursorScaffoldPlan) { p.model = "claude-opus-5-5" }, func(p *cursorScaffoldPlan) { p.model = "grok" }, func(p *cursorScaffoldPlan) { p.prompt = "" }, func(p *cursorScaffoldPlan) { p.prompt = strings.Repeat("x", cursorScaffoldPromptLimit+1) }, func(p *cursorScaffoldPlan) { p.prompt = string([]byte{255}) }, func(p *cursorScaffoldPlan) { p.invocation = "../other" }, func(p *cursorScaffoldPlan) { p.timeout = 0 }, func(p *cursorScaffoldPlan) { p.client = "relative" }} {
		q := p
		change(&q)
		if _, err := newCursorScaffoldPlan(q.invocation, q.client, q.worktree, q.stateRoot, q.model, q.prompt, q.timeout); err == nil {
			t.Fatal("bad plan accepted")
		}
	}
	calls := 0
	start := func(cursorScaffoldCommand) (cursorScaffoldPeer, error) { calls++; return nil, nil }
	if _, err := runCursorScaffold(context.Background(), p, nil, start); !errors.Is(err, errCursorScaffoldRefused) || calls != 0 {
		t.Fatal("nil decision input acquired a fallback")
	}
	link := filepath.Join(p.stateRoot, "client-link")
	if os.Symlink(p.client, link) != nil {
		t.Fatal("symlink fixture")
	}
	if _, err := newCursorScaffoldPlan(p.invocation, link, p.worktree, p.stateRoot, p.model, p.prompt, p.timeout); err == nil {
		t.Fatal("symlink client accepted")
	}
}

func TestCursorScaffoldReceiptCorrelationAndBounds(t *testing.T) {
	p := cursorScaffoldFixture(t)
	good := cursorScaffoldFrames(p)
	cases := map[string]string{
		"empty": "", "missing-start": cursorScaffoldStream(good[1:]), "missing-session": cursorScaffoldStream([]string{good[0], good[2]}),
		"missing-terminal": cursorScaffoldStream(good[:2]), "duplicate-start": cursorScaffoldStream(append([]string{good[0]}, good...)),
		"duplicate-terminal": cursorScaffoldStream(append(append([]string{}, good...), good[2])),
		"wrong-session":      cursorScaffoldStream([]string{good[0], good[1], strings.ReplaceAll(good[2], "synthetic-session", "other")}),
		"duplicate-key":      cursorScaffoldStream([]string{`{"event":"starting","event":"session-created"}`}),
		"wrong-model":        cursorScaffoldStream([]string{strings.ReplaceAll(good[0], p.model, "other")}),
		"served-inference":   cursorScaffoldStream([]string{strings.ReplaceAll(good[0], "Unknown", p.model)}),
		"bad-json":           "{\n", "no-final-newline": strings.Join(good, "\n"),
		"assistant-payload": cursorScaffoldStream([]string{good[0], `{"event":"assistant","text":"do not capture"}`}),
		"permission":        cursorScaffoldStream([]string{good[0], good[1], `{"event":"permission-required","sessionID":"synthetic-session"}`}),
		"frame-limit":       strings.Repeat("x", cursorScaffoldFrameLimit) + "\n",
	}
	for name, stream := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := readCursorScaffoldReceipts(strings.NewReader(stream), p); err == nil {
				t.Fatal("unconfirmed receipts accepted")
			}
		})
	}
	if result, err := readCursorScaffoldReceipts(strings.NewReader(cursorScaffoldStream(good)), p); err != nil || result.stopReason != "end_turn" {
		t.Fatal("valid receipts refused")
	}
}

func TestCursorScaffoldProcessOutcomeAndReplay(t *testing.T) {
	for _, kind := range []string{"exit-error", "start-error", "missing-terminal", "cancelled-terminal"} {
		t.Run(kind, func(t *testing.T) {
			p := cursorScaffoldFixture(t)
			decisions, _ := cursorScaffoldDecisionPipe(t)
			frames := cursorScaffoldFrames(p)
			start := func(cursorScaffoldCommand) (cursorScaffoldPeer, error) {
				if kind == "start-error" {
					return nil, errors.New("sensitive process diagnostic")
				}
				var exit error
				if kind == "exit-error" {
					exit = errors.New("sensitive diagnostic")
				}
				if kind == "missing-terminal" {
					frames = frames[:2]
				}
				if kind == "cancelled-terminal" {
					frames[2] = strings.ReplaceAll(frames[2], "end_turn", "cancelled")
				}
				return cursorScaffoldExited(cursorScaffoldStream(frames), exit), nil
			}
			result, err := runCursorScaffold(context.Background(), p, decisions, start)
			if err == nil || result.promptComplete || strings.Contains(err.Error(), "sensitive") {
				t.Fatal("uncertain exit promoted")
			}
			if kind == "cancelled-terminal" && result.stopReason != "cancelled" {
				t.Fatal("terminal stopReason discarded")
			}
			calls := 0
			_, err = runCursorScaffold(context.Background(), p, decisions, func(cursorScaffoldCommand) (cursorScaffoldPeer, error) { calls++; return nil, nil })
			if !errors.Is(err, errCursorScaffoldReplay) || calls != 0 {
				t.Fatal("attempt replayed")
			}
		})
	}
}

func TestCursorScaffoldNilOutputOwnedCleanup(t *testing.T) {
	p := cursorScaffoldFixture(t)
	decisions, _ := cursorScaffoldDecisionPipe(t)
	peer := &cursorScaffoldFakePeer{done: make(chan error, 1)}
	peer.onSignal = func(s os.Signal) {
		if s == syscall.SIGTERM {
			peer.done <- nil
		}
	}
	_, err := runCursorScaffold(context.Background(), p, decisions, func(cursorScaffoldCommand) (cursorScaffoldPeer, error) { return peer, nil })
	if !errors.Is(err, errCursorScaffoldProcess) {
		t.Fatal("nil output accepted")
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if !reflect.DeepEqual(peer.signals, []os.Signal{syscall.SIGTERM}) {
		t.Fatal("owned nil-output peer not stopped")
	}
}

func TestCursorScaffoldCancellationGraceAndForcedUncertainty(t *testing.T) {
	p := cursorScaffoldFixture(t)
	decisions, _ := cursorScaffoldDecisionPipe(t)
	read, write := io.Pipe()
	peer := &cursorScaffoldFakePeer{reader: read, done: make(chan error, 1)}
	peer.onSignal = func(s os.Signal) {
		if s == syscall.SIGTERM {
			_ = write.Close()
			peer.done <- nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := runCursorScaffold(ctx, p, decisions, func(cursorScaffoldCommand) (cursorScaffoldPeer, error) { calls++; return peer, nil })
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("cancelled context spawned")
	}
	waited := make(chan error, 1)
	forced := &cursorScaffoldFakePeer{done: make(chan error, 1)}
	forced.onSignal = func(s os.Signal) {
		if s == syscall.SIGKILL {
			waited <- nil
		}
	}
	if err := stopCursorScaffoldPeer(forced, waited, time.Millisecond, time.Second); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(forced.signals, []os.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatal("bounded forced stop order")
	}
	_ = read.Close()
	_ = write.Close()
}

func TestCursorScaffoldResultDoesNotExposeAnswerPayload(t *testing.T) {
	typeOf := reflect.TypeOf(cursorScaffoldResult{})
	for i := 0; i < typeOf.NumField(); i++ {
		name := strings.ToLower(typeOf.Field(i).Name)
		if strings.Contains(name, "answer") || strings.Contains(name, "payload") || strings.Contains(name, "tool") {
			t.Fatal("capture contract broadened")
		}
	}
	var fields map[string]any
	if json.Unmarshal([]byte(`{"event":"failed","reason":"SENSITIVE"}`), &fields) != nil {
		t.Fatal("fixture")
	}
}

// startedDeadline is a context whose deadline passes when done is closed.
type startedDeadline struct {
	context.Context
	done chan struct{}
}

func (c *startedDeadline) Done() <-chan struct{} { return c.done }
func (c *startedDeadline) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func TestCursorScaffoldActiveDeadline(t *testing.T) {
	p := cursorScaffoldFixture(t)
	decisions, _ := cursorScaffoldDecisionPipe(t)
	reader, writer := io.Pipe()
	peer := &cursorScaffoldFakePeer{reader: reader, done: make(chan error, 1)}
	peer.onSignal = func(s os.Signal) {
		if s == syscall.SIGTERM {
			_ = writer.Close()
			peer.done <- nil
		}
	}
	// The deadline must pass while the helper runs. runCursorScaffold fsyncs its
	// fence before it starts the helper, which takes more than 10ms on a session
	// pod's volume, so a deadline fixed at the start of the test could expire
	// first and the helper would never start. Arm it when the helper starts.
	ctx := &startedDeadline{Context: context.Background(), done: make(chan struct{})}
	result, err := runCursorScaffold(ctx, p, decisions, func(cursorScaffoldCommand) (cursorScaffoldPeer, error) {
		time.AfterFunc(10*time.Millisecond, func() { close(ctx.done) })
		return peer, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || result.promptComplete {
		t.Fatal("deadline became completion")
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if !reflect.DeepEqual(peer.signals, []os.Signal{syscall.SIGTERM}) {
		t.Fatal("helper did not receive graceful termination")
	}
}

func TestCursorScaffoldOwnedPeerWithStartError(t *testing.T) {
	p := cursorScaffoldFixture(t)
	decisions, _ := cursorScaffoldDecisionPipe(t)
	reader, writer := io.Pipe()
	peer := &cursorScaffoldFakePeer{reader: reader, done: make(chan error, 1)}
	peer.onSignal = func(s os.Signal) {
		if s == syscall.SIGTERM {
			_ = writer.Close()
			peer.done <- nil
		}
	}
	_, err := runCursorScaffold(context.Background(), p, decisions, func(cursorScaffoldCommand) (cursorScaffoldPeer, error) { return peer, errors.New("uncertain start") })
	if !errors.Is(err, errCursorScaffoldProcess) {
		t.Fatal("uncertain owned start accepted")
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if !reflect.DeepEqual(peer.signals, []os.Signal{syscall.SIGTERM}) {
		t.Fatal("owned uncertain peer not stopped")
	}
}

// TestCursorScaffoldSyntheticNestedPeer is also a finite local synthetic child
// entry. It never invokes Python, Cursor, a provider, network, login or actor.
func TestCursorScaffoldSyntheticNestedPeer(t *testing.T) {
	mode := os.Getenv("CURSOR_SCAFFOLD_SYNTHETIC_MODE")
	if mode == "" {
		return
	}
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	defer signal.Stop(term)
	if mode == "vendor" {
		select {
		case <-term:
		case <-time.After(6 * time.Second):
		}
		os.Exit(0)
	}
	if mode != "helper" {
		os.Exit(2)
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	child := exec.Command(executable, "-test.run=^TestCursorScaffoldSyntheticNestedPeer$")
	child.Env = []string{"CURSOR_SCAFFOLD_SYNTHETIC_MODE=vendor"}
	child.Stdin, child.Stdout, child.Stderr = nil, io.Discard, io.Discard
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if child.Start() != nil {
		os.Exit(2)
	}
	childGroup, err := syscall.Getpgid(child.Process.Pid)
	if err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		os.Exit(2)
	}
	helperGroup, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		os.Exit(2)
	}
	metadata, _ := json.Marshal(map[string]int{"helperGroup": helperGroup, "vendorGroup": childGroup, "vendorPID": child.Process.Pid})
	if os.WriteFile(os.Getenv("CURSOR_SCAFFOLD_SYNTHETIC_PID"), metadata, 0o600) != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		os.Exit(2)
	}
	fmt.Printf("{\"event\":\"starting\",\"modelRequested\":%q,\"servedModel\":\"Unknown\",\"promptSHA256\":%q}\n", os.Getenv("CURSOR_SCAFFOLD_SYNTHETIC_MODEL"), os.Getenv("CURSOR_SCAFFOLD_SYNTHETIC_HASH"))
	fmt.Println(`{"event":"session-created","sessionID":"synthetic-session"}`)
	select {
	case <-term:
	case <-time.After(8 * time.Second):
	}
	// Synthetic stand-in for Python's finally/stop_group, across a different PGID.
	_ = child.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	var childErr error
	select {
	case childErr = <-done:
	case <-time.After(time.Second):
		_ = child.Process.Kill()
		childErr = <-done
	}
	if childErr != nil {
		var exited *exec.ExitError
		if !errors.As(childErr, &exited) {
			os.Exit(2)
		}
	}
	if os.WriteFile(os.Getenv("CURSOR_SCAFFOLD_SYNTHETIC_CLEANED"), []byte("nested-owned-child-reaped"), 0o600) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestCursorScaffoldNestedGroupCancellationAllowsHelperFinally(t *testing.T) {
	p := cursorScaffoldFixture(t)
	p.timeout = 5 * time.Second
	decisions, _ := cursorScaffoldDecisionPipe(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidPath, cleanedPath := filepath.Join(p.stateRoot, "synthetic-pids.json"), filepath.Join(p.stateRoot, "synthetic-cleaned")
	hash := sha256.Sum256([]byte(p.prompt))
	start := func(c cursorScaffoldCommand) (cursorScaffoldPeer, error) {
		c.name = executable
		c.args = []string{"-test.run=^TestCursorScaffoldSyntheticNestedPeer$"}
		c.env = []string{"CURSOR_SCAFFOLD_SYNTHETIC_MODE=helper", "CURSOR_SCAFFOLD_SYNTHETIC_PID=" + pidPath, "CURSOR_SCAFFOLD_SYNTHETIC_CLEANED=" + cleanedPath, "CURSOR_SCAFFOLD_SYNTHETIC_MODEL=" + p.model, "CURSOR_SCAFFOLD_SYNTHETIC_HASH=" + hex.EncodeToString(hash[:])}
		return startCursorScaffoldHelper(c)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		result, err := runCursorScaffold(ctx, p, decisions, start)
		if result.promptComplete {
			done <- errors.New("cancelled peer completed")
		} else {
			done <- err
		}
	}()
	var metadata map[string]int
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		if b, err := os.ReadFile(pidPath); err == nil {
			if json.Unmarshal(b, &metadata) != nil {
				t.Fatal("synthetic PID fixture")
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err = <-done:
	case <-time.After(7 * time.Second):
		t.Fatal("bounded synthetic cleanup did not finish")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel outcome: %v", err)
	}
	if metadata["helperGroup"] <= 0 || metadata["vendorGroup"] <= 0 || metadata["helperGroup"] == metadata["vendorGroup"] {
		t.Fatal("fixture did not establish separate process groups")
	}
	if b, err := os.ReadFile(cleanedPath); err != nil || string(b) != "nested-owned-child-reaped" {
		t.Fatal("helper finally was bypassed by hard kill")
	}
	if err := syscall.Kill(metadata["vendorPID"], 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatal("synthetic nested child still present")
	}
}

func TestCursorScaffoldExistingPythonHelperWithSyntheticACP(t *testing.T) {
	// Existing interpreters only. Failure to find a supported interpreter is an
	// honest fixture setup failure, never a download/install or skipped pass.
	python := ""
	for _, candidate := range []string{"python3.12", "python3", "/opt/homebrew/bin/python3.12"} {
		path, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cmd := exec.CommandContext(ctx, path, "-c", "import sys; sys.exit(0 if sys.version_info.major == 3 and sys.version_info.minor >= 11 else 2)")
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		err = cmd.Run()
		cancel()
		if err == nil {
			python = path
			break
		}
	}
	if python == "" {
		t.Fatal("synthetic helper fixture requires existing Python >=3.11; no installation attempted")
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("fixture source path unavailable")
	}
	helper := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "scripts", "cursor-acp-session.py"))
	if _, err := os.Stat(helper); err != nil {
		t.Fatal("existing canonical helper unavailable to fixture")
	}
	p := cursorScaffoldFixture(t)
	p.timeout = 5 * time.Second
	decisions, _ := cursorScaffoldDecisionPipe(t)
	requestLog := filepath.Join(p.stateRoot, "synthetic-provider-observed.json")
	fake := "#!" + python + "\n" + fmt.Sprintf(`import sys,json,hashlib
seen=[]
def read(method,ident):
    value=json.loads(sys.stdin.readline())
    if value.get('method')!=method or value.get('id')!=ident:
        raise RuntimeError('synthetic request mismatch')
    seen.append(method)
    return value
def send(ident,result):
    print(json.dumps({'jsonrpc':'2.0','id':ident,'result':result}),flush=True)
read('initialize',1);send(1,{'protocolVersion':1})
read('session/new',2);send(2,{'sessionId':'synthetic-session'})
q=read('session/prompt',3)
print(json.dumps({'jsonrpc':'2.0','method':'session/update','params':{'sessionId':'synthetic-session','update':{'sessionUpdate':'agent_message_chunk','content':{'type':'text','text':'DO-NOT-CAPTURE-ASSISTANT-ANSWER'}}}}),flush=True)
with open(%q,'w') as f:
    json.dump({'methods':seen,'argv':sys.argv[1:],'promptSHA256':hashlib.sha256(q['params']['prompt'][0]['text'].encode()).hexdigest()},f)
send(3,{'stopReason':'end_turn'})
`, requestLog)
	if os.WriteFile(p.client, []byte(fake), 0o700) != nil {
		t.Fatal("synthetic ACP peer creation")
	}
	start := func(c cursorScaffoldCommand) (cursorScaffoldPeer, error) {
		// Substitute only the existing interpreter/canonical source helper in
		// this synthetic test, never a vendor executable or production path.
		c.name = python
		c.args[0] = helper
		return startCursorScaffoldHelper(c)
	}
	result, err := runCursorScaffold(context.Background(), p, decisions, start)
	if err != nil || !result.promptComplete || result.servedModel != "Unknown" || result.sessionID != "synthetic-session" {
		t.Fatalf("synthetic actual-helper result: %#v %v", result, err)
	}
	data, err := os.ReadFile(requestLog)
	if err != nil {
		t.Fatal("synthetic observation missing")
	}
	var observed struct {
		Methods      []string `json:"methods"`
		Argv         []string `json:"argv"`
		PromptSHA256 string   `json:"promptSHA256"`
	}
	if json.Unmarshal(data, &observed) != nil || !reflect.DeepEqual(observed.Methods, []string{"initialize", "session/new", "session/prompt"}) || !reflect.DeepEqual(observed.Argv, []string{"--model", p.model, "acp"}) || observed.PromptSHA256 != result.promptSHA256 {
		t.Fatal("canonical helper transport contract changed")
	}
}

func TestCursorScaffoldCleanupWaitErrorsRemainExplicit(t *testing.T) {
	for _, waitErr := range []error{nil, errCursorScaffoldProcess} {
		peer := &cursorScaffoldFakePeer{done: make(chan error, 1)}
		waited := make(chan error, 1)
		waited <- waitErr
		if got := stopCursorScaffoldPeer(peer, waited, time.Millisecond, time.Millisecond); !errors.Is(got, waitErr) {
			t.Fatal("cleanup wait status lost")
		}
		if len(peer.signals) != 0 {
			t.Fatal("already waited peer signalled")
		}
	}
	peer := &cursorScaffoldFakePeer{done: make(chan error, 1)}
	if err := stopCursorScaffoldPeer(peer, make(chan error), time.Millisecond, time.Millisecond); !errors.Is(err, errCursorScaffoldProcess) {
		t.Fatal("uncertain cleanup promoted")
	}
}
