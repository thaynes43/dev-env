package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

const usageCaptureUsage = "usage capture: expected -o json --input FILE|- --trust-file FILE"

func (a *app) usageCapture(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("usage capture", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var output, input, trustFile string
	fs.StringVar(&output, "o", "", "")
	fs.StringVar(&input, "input", "", "")
	fs.StringVar(&trustFile, "trust-file", "", "")
	counts := flagCounts(args, "o", "input", "trust-file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(a.env.Stdout, usageCaptureUsage+"\n")
			return err
		}
		return usageError(usageCaptureUsage)
	}
	if fs.NArg() != 0 || output != "json" || input == "" || trustFile == "" || trustFile == "-" || counts["o"] > 1 || counts["input"] > 1 || counts["trust-file"] > 1 {
		return usageError(usageCaptureUsage)
	}
	fail := func() error { return &exitError{code: ExitFailed, msg: "usage capture unavailable"} }
	if ctx.Err() != nil {
		return fail()
	}
	// Waiting for stdin EOF is finite and honours signal cancellation. This
	// deadline is input IO only; it does not acquire or schedule a provider.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := readCaptureInput(ctx, a, input)
	if err != nil {
		return fail()
	}
	selected, err := readCaptureInput(ctx, a, trustFile)
	if err != nil || ctx.Err() != nil {
		return fail()
	}
	trust, err := providerallowance.ParseAggregateTrust(selected)
	if err != nil {
		return fail()
	}
	now := time.Now
	if a.env.Now != nil {
		now = a.env.Now
	}
	doc, err := providerallowance.AssembleCaptureBundle(raw, trust, now())
	if err != nil || ctx.Err() != nil {
		return fail()
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return fail()
	}
	body = append(body, '\n')
	if n, err := a.env.Stdout.Write(body); err != nil || n != len(body) {
		return fail()
	}
	return nil
}

func readCaptureInput(ctx context.Context, a *app, path string) ([]byte, error) {
	r := a.env.Stdin
	if path != "-" {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return nil, err
		}
		f := os.NewFile(uintptr(fd), "usage-capture")
		defer func() { _ = f.Close() }()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() {
			return nil, errors.New("capture input unavailable")
		}
		r = f
	}
	if r == nil {
		return nil, errors.New("capture input unavailable")
	}
	var raw []byte
	var err error
	switch source := r.(type) {
	case *os.File:
		raw, err = readCaptureFD(ctx, source)
	case *bytes.Buffer, *bytes.Reader, *strings.Reader:
		raw, err = io.ReadAll(io.LimitReader(source, int64(providerallowance.FixtureParseLimits.MaxBytes+1)))
	case io.ReadCloser:
		// Injected streaming readers must unblock Read on Close. Real CLI stdin
		// uses the fd poll path, so even a blocking inherited pipe is cancellable.
		done := make(chan struct{})
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			select {
			case <-ctx.Done():
				_ = source.Close()
			case <-done:
			}
		}()
		raw, err = io.ReadAll(io.LimitReader(source, int64(providerallowance.FixtureParseLimits.MaxBytes+1)))
		close(done)
		<-stopped
	default:
		return nil, errors.New("capture input is not cancellable")
	}
	if ctx.Err() != nil || err != nil || len(raw) == 0 || len(raw) > providerallowance.FixtureParseLimits.MaxBytes {
		return nil, errors.New("capture input unavailable")
	}
	return raw, nil
}

func readCaptureFD(ctx context.Context, file *os.File) ([]byte, error) {
	var out []byte
	chunk := make([]byte, 32<<10)
	for len(out) <= providerallowance.FixtureParseLimits.MaxBytes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fds := []unix.PollFd{{Fd: int32(file.Fd()), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		if fds[0].Revents&(unix.POLLNVAL|unix.POLLERR) != 0 {
			return nil, errors.New("capture input unavailable")
		}
		limit := min(len(chunk), providerallowance.FixtureParseLimits.MaxBytes+1-len(out))
		read, err := file.Read(chunk[:limit])
		out = append(out, chunk[:read]...)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
	return nil, errors.New("capture input unavailable")
}
