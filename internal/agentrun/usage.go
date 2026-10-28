package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func (a *app) usage(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var output, from string
	fs.StringVar(&output, "o", "", "")
	fs.StringVar(&from, "from-file", "", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(a.env.Stdout, commandHelp["usage"])
			return err
		}
		return usageError("usage: invalid arguments")
	}
	fromSpecified := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "from-file" {
			fromSpecified = true
		}
	})
	if output != "json" || fs.NArg() != 0 || (fromSpecified && from == "") {
		return usageError("usage: expected -o json [--from-file FILE|-]")
	}
	if ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage unavailable"}
	}
	var read providerallowance.ReadResponse
	if from == "" {
		now := time.Now
		if a.env.Now != nil {
			now = a.env.Now
		}
		at := now().UTC().Format(time.RFC3339Nano)
		read = providerallowance.ReadResponse{Schema: providerallowance.SchemaRead, SnapshotRevision: "local-preflight", ServedAt: at, Items: []providerallowance.ReadItem{}, SourceStatuses: []providerallowance.SourceStatus{}}
		for _, v := range []struct{ provider, code string }{{"cursor", "local_unconfigured_cursor_no_provider_request"}, {"anthropic", "local_unconfigured_claude_no_provider_request"}, {"openai", "local_unconfigured_codex_no_provider_request"}} {
			code := v.code
			read.SourceStatuses = append(read.SourceStatuses, providerallowance.SourceStatus{Provider: v.provider, ProducerRef: "agent-run-local-preflight", Availability: providerallowance.AvailabilityUnconfigured, LastAttempt: providerallowance.LastAttempt{AttemptedAt: at, CompletedAt: &at, Result: providerallowance.AttemptUnavailable, Code: &code}, Freshness: providerallowance.Freshness{State: providerallowance.FreshUnknown, AsOf: at, Basis: providerallowance.BasisNone, Reason: "local configuration check; no provider request"}, Validation: providerallowance.ValidationRecord{State: providerallowance.ValidationUnverified, ValidationRefs: []string{}}, Binding: providerallowance.BindingRecord{State: providerallowance.BindingUnknown}})
		}
	} else {
		r := a.env.Stdin
		if from != "-" {
			fd, err := unix.Open(from, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
			if err != nil {
				return &exitError{code: ExitFailed, msg: "usage input unavailable"}
			}
			f := os.NewFile(uintptr(fd), "usage-cache")
			defer func() { _ = f.Close() }()
			st, err := f.Stat()
			if err != nil || !st.Mode().IsRegular() {
				return &exitError{code: ExitFailed, msg: "usage input unavailable"}
			}
			r = f
		}
		if r == nil {
			return &exitError{code: ExitFailed, msg: "usage input unavailable"}
		}
		raw, err := io.ReadAll(io.LimitReader(r, int64(providerallowance.FixtureParseLimits.MaxBytes+1)))
		if err != nil {
			return &exitError{code: ExitFailed, msg: "usage input unavailable"}
		}
		read, err = providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
		if err != nil {
			return &exitError{code: ExitFailed, msg: "usage input unavailable"}
		}
	}
	raw, err := json.Marshal(read)
	if err != nil {
		return &exitError{code: ExitFailed, msg: "usage unavailable"}
	}
	if _, err = providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits); err != nil || ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage unavailable"}
	}
	encoded := append(raw, '\n')
	if n, err := a.env.Stdout.Write(encoded); err != nil || n != len(encoded) {
		return &exitError{code: ExitFailed, msg: "usage output unavailable"}
	}
	return nil
}
