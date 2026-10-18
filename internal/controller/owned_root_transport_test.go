package controller

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	kexec "k8s.io/client-go/util/exec"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/taskbudget"
)

type phaseExec struct {
	status  int
	output  string
	failure error
}

func (e phaseExec) Run(_ context.Context, _, _, _ string, _ []string, _ io.Reader, out, stderr io.Writer) error {
	_, _ = io.WriteString(out, e.output)
	_, _ = io.WriteString(stderr, e.output)
	if e.failure != nil {
		return e.failure
	}
	if e.status == 0 {
		return nil
	}
	return kexec.CodeExitError{Err: errors.New("remote process exited"), Code: e.status}
}

func TestOwnedRootCRIStatusDoesNotRequireRootOwnedOutput(t *testing.T) {
	// Non-TTY exec uses a runtime-created pipe. Reproduce the UID1000 FIFO
	// shape locally without a model, pod, chown, kernel change or privilege.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()
	var st unix.Stat_t
	if err = unix.Fstat(int(w.Fd()), &st); err != nil {
		t.Fatal(err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFIFO || st.Uid != uint32(os.Getuid()) {
		t.Fatal("unexpected pipe", st)
	}
	b := taskbudget.Binding{TaskUID: "task", HostID: "host", PodUID: "pod", Epoch: 1, Deadline: time.Now().Add(time.Minute)}
	p := OwnedRootProber{Exec: phaseExec{status: 40, output: `{"stage":"Ready","rootWaitObserved":true}`}}
	// Current transport rejects this legitimate dedicated exit status instead
	// of observing Prepared; the correction must ignore the forged stdout.
	got, err := p.Observe(context.Background(), client.ObjectKey{Namespace: "dev-agents", Name: "root"}, "owned-root", b, "home", strings.Repeat("a", 64))
	if err != nil || got != 40 {
		t.Fatalf("UID%d FIFO valid CRI Prepared status rejected: phase=%d err=%v", st.Uid, got, err)
	}
}

func TestOwnedRootRejectsMissingOrGenericStatus(t *testing.T) {
	b := taskbudget.Binding{TaskUID: "task", HostID: "host", PodUID: "pod", Epoch: 1, Deadline: time.Now().Add(time.Minute)}
	for _, code := range []int{0, 1, 2, 39, 44, 126, 127, 137, 143, 255} {
		p := OwnedRootProber{Exec: phaseExec{status: code, output: `{"stage":"Ready"}`}}
		if _, err := p.Observe(context.Background(), client.ObjectKey{}, "owned-root", b, "home", strings.Repeat("a", 64)); err == nil {
			t.Fatalf("unconfirmed code%d accepted", code)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (OwnedRootProber{Exec: phaseExec{status: 42}}).Observe(ctx, client.ObjectKey{}, "owned-root", b, "home", strings.Repeat("a", 64)); err == nil {
		t.Fatal("canceled observation accepted")
	}
}

// A sibling can open the UID-owned output pipe before the observer hardens.
// Its injected bytes do not control the immutable process's wait status.
func TestOwnedRootPreopenedProcFDCannotForgeExitStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	observer := exec.CommandContext(ctx, "/bin/sh", "-c", "printf 'open\\n'; read answer; exit 40")
	observer.Env = []string{"PATH=/usr/bin:/bin"}
	stdout, err := observer.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := observer.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = observer.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observer.Process.Kill(); _ = observer.Wait() }()
	rd := bufio.NewReader(stdout)
	if line, e := rd.ReadString('\n'); e != nil || line != "open\n" {
		t.Fatal("observer did not reach pre-hardening interval", line, e)
	}
	attacker := exec.CommandContext(ctx, "/bin/sh", "-c", `printf 'forged Ready\n' > "$1"`, "-", fmt.Sprintf("/proc/%d/fd/1", observer.Process.Pid))
	attacker.Env = []string{"PATH=/usr/bin:/bin"}
	if err = attacker.Run(); err != nil {
		t.Fatal("sibling proc-FD attack did not reproduce", err)
	}
	if line, e := rd.ReadString('\n'); e != nil || line != "forged Ready\n" {
		t.Fatal("forged stdout not observed", line, e)
	}
	if _, err = io.WriteString(stdin, "finish\n"); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	var exited *exec.ExitError
	if err = observer.Wait(); !errors.As(err, &exited) || exited.ExitCode() != 40 {
		t.Fatal("forged stdout altered real status", err)
	}
}

type unexitedPhase struct{}

func (unexitedPhase) Error() string   { return "not an exited process" }
func (unexitedPhase) String() string  { return "not an exited process" }
func (unexitedPhase) Exited() bool    { return false }
func (unexitedPhase) ExitStatus() int { return 40 }
func TestOwnedRootRejectsGenericOrUnexitedError(t *testing.T) {
	b := taskbudget.Binding{TaskUID: "task", HostID: "host", PodUID: "pod", Epoch: 1, Deadline: time.Now().UTC().Add(time.Minute)}
	for _, failure := range []error{io.EOF, errors.New("command terminated with exit code 40"), context.Canceled, unexitedPhase{}} {
		p := OwnedRootProber{Exec: phaseExec{status: 40, failure: failure}}
		if _, err := p.Observe(context.Background(), client.ObjectKey{}, "owned-root", b, "home", strings.Repeat("a", 64)); err == nil {
			t.Fatalf("unconfirmed error accepted: %v", failure)
		}
	}
}
