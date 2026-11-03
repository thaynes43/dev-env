package allowancecollector

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/internal/usagecollector"
)

type Executor interface {
	Run(context.Context, string, string, string, []string, io.Reader, io.Writer, io.Writer) error
}

type podRemote struct {
	exec   Executor
	binary []byte
	sha    string
}

func (p *podRemote) HelperSHA256() string { return p.sha }

// NewRemote loads only the source-matched public helper in the operator image.
// It does not exec or inspect the selected host until the leader dispatches.
func NewRemote(executor Executor, path string) (Remote, error) {
	if executor == nil || !filepath.IsAbs(path) {
		return nil, ErrCollector
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrCollector
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() < 1 || info.Size() > 32<<20 {
		return nil, ErrCollector
	}
	raw, err := io.ReadAll(io.LimitReader(f, (32<<20)+1))
	if err != nil || len(raw) > 32<<20 || int64(len(raw)) != info.Size() {
		return nil, ErrCollector
	}
	return &podRemote{exec: executor, binary: raw, sha: digest(raw)}, nil
}

// bootstrap is fixed code, not a shell or provider adapter. It copies exactly
// the public binary length, verifies its digest, seals an anonymous memfd and
// execs the sole internal command. The remaining stdin is the bounded request.
// No filesystem binary, credential, token, auth configuration or user process is
// installed/changed. A stalled stream before helper execution dies in 30s.
const bootstrap = `import os,sys,hashlib,fcntl,signal
signal.alarm(30)
n=int(sys.argv[1]); expected=sys.argv[2]
if n < 1 or n > 33554432: sys.exit(1)
fd=os.memfd_create("dev-env-allowance-helper",os.MFD_CLOEXEC|os.MFD_ALLOW_SEALING)
h=hashlib.sha256()
while n:
 b=os.read(0,min(n,65536))
 if not b: sys.exit(1)
 h.update(b); n-=len(b)
 view=memoryview(b)
 while view:
  written=os.write(fd,view)
  if written < 1: sys.exit(1)
  view=view[written:]
if h.hexdigest()!=expected: sys.exit(1)
fcntl.fcntl(fd,fcntl.F_ADD_SEALS,fcntl.F_SEAL_SEAL|fcntl.F_SEAL_SHRINK|fcntl.F_SEAL_GROW|fcntl.F_SEAL_WRITE)
os.fchmod(fd,0o500)
signal.alarm(0)
os.execve('/proc/self/fd/'+str(fd),['agent-run','internal-allowance-capture'],os.environ)
`

func (p *podRemote) Run(ctx context.Context, host Host, request usagecollector.RemoteRequest) ([]byte, error) {
	if p == nil || p.exec == nil || ctx == nil || ctx.Err() != nil {
		return nil, ErrCollector
	}
	raw, err := json.Marshal(request)
	if err != nil || len(raw) > 16<<10 {
		return nil, ErrCollector
	}
	out := &boundedBuffer{max: usagecollector.RemoteMaxBytes}
	diagnostics := &boundedBuffer{max: usagecollector.DiagnosticMaxBytes, discardOverflow: true}
	err = p.exec.Run(ctx, Namespace, host.Name, "agent", []string{"/usr/bin/python3", "-c", bootstrap, strconv.Itoa(len(p.binary)), p.sha}, io.MultiReader(bytes.NewReader(p.binary), bytes.NewReader(raw)), out, diagnostics)
	if !diagnostics.failed {
		for _, d := range usagecollector.DecodeAcquisitionDiagnostics(diagnostics.Bytes()) {
			log.FromContext(ctx).Info("allowance acquisition diagnostic", "cycle", request.Cycle, "provider", d.Provider, "stage", d.Stage, "reason", d.Reason, "clientVersion", d.ClientVersion)
		}
	}
	if err != nil || out.failed {
		return nil, ErrCollector
	}
	return append([]byte(nil), out.Bytes()...), nil
}

type boundedBuffer struct {
	sync.Mutex
	buffer          bytes.Buffer
	max             int
	failed          bool
	discardOverflow bool
}

func (b *boundedBuffer) Write(raw []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	if b.failed || len(raw) > b.max-b.buffer.Len() {
		b.failed = true
		if b.discardOverflow {
			return len(raw), nil
		}
		return 0, ErrCollector
	}
	return b.buffer.Write(raw)
}

func (b *boundedBuffer) Bytes() []byte {
	b.Lock()
	defer b.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}
