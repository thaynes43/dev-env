package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

const usageAggregateAllowancePath = "/v1/provider-allowances"

const usageCommandUsage = "usage: expected -o json [--from-file FILE|- | --claude-statusline | --read-current | --publish-file FILE|-]"

const usageAggregatePublishedLine = "published provider-allowances status=204\n"

func (a *app) usage(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var output, from, publishFile string
	var claude, readCurrent bool
	fs.StringVar(&output, "o", "", "")
	fs.StringVar(&from, "from-file", "", "")
	fs.StringVar(&publishFile, "publish-file", "", "")
	fs.BoolVar(&claude, "claude-statusline", false, "")
	fs.BoolVar(&readCurrent, "read-current", false, "")
	counts := flagCounts(args, "from-file", "claude-statusline", "read-current", "publish-file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(a.env.Stdout, commandHelp["usage"])
			return err
		}
		return usageError("usage: invalid arguments")
	}
	fromSpecified := false
	publishSpecified := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "from-file" {
			fromSpecified = true
		}
		if f.Name == "publish-file" {
			publishSpecified = true
		}
	})
	if counts["read-current"] > 1 || counts["publish-file"] > 1 || output != "json" || fs.NArg() != 0 || (fromSpecified && from == "") || (publishSpecified && publishFile == "") || (fromSpecified && (claude || publishSpecified)) || (readCurrent && (fromSpecified || claude || publishSpecified)) || (publishSpecified && claude) {
		return usageError(usageCommandUsage)
	}
	if ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage unavailable"}
	}
	if publishSpecified {
		return a.publishAggregateAllowance(ctx, publishFile)
	}
	if readCurrent {
		return a.readAggregateAllowance(ctx)
	}
	var read providerallowance.ReadResponse
	if from == "" {
		now := time.Now
		if a.env.Now != nil {
			now = a.env.Now
		}
		captured := now().UTC()
		at := captured.Format(time.RFC3339Nano)
		read = providerallowance.ReadResponse{Schema: providerallowance.SchemaRead, SnapshotRevision: "local-preflight", ServedAt: at, Items: []providerallowance.ReadItem{}, SourceStatuses: []providerallowance.SourceStatus{}}
		for _, v := range []struct{ provider, code string }{{"cursor", "local_unconfigured_cursor_no_provider_request"}, {"anthropic", "local_unconfigured_claude_no_provider_request"}, {"openai", "local_unconfigured_codex_no_provider_request"}} {
			code := v.code
			read.SourceStatuses = append(read.SourceStatuses, providerallowance.SourceStatus{Provider: v.provider, ProducerRef: "agent-run-local-preflight", Availability: providerallowance.AvailabilityUnconfigured, LastAttempt: providerallowance.LastAttempt{AttemptedAt: at, CompletedAt: &at, Result: providerallowance.AttemptUnavailable, Code: &code}, Freshness: providerallowance.Freshness{State: providerallowance.FreshUnknown, AsOf: at, Basis: providerallowance.BasisNone, Reason: "local configuration check; no provider request"}, Validation: providerallowance.ValidationRecord{State: providerallowance.ValidationUnverified, ValidationRefs: []string{}}, Binding: providerallowance.BindingRecord{State: providerallowance.BindingUnknown}})
		}
		if claude {
			if a.env.Stdin == nil {
				return &exitError{code: ExitFailed, msg: "usage input unavailable"}
			}
			raw, err := io.ReadAll(io.LimitReader(a.env.Stdin, int64(providerallowance.FixtureParseLimits.MaxBytes+1)))
			if err != nil {
				return &exitError{code: ExitFailed, msg: "usage input unavailable"}
			}
			item, err := providerallowance.ClaudeStatuslineRead(raw, captured)
			if err != nil {
				return &exitError{code: ExitFailed, msg: "usage input unavailable"}
			}
			read.SnapshotRevision = item.Observation.ObservationID
			read.Items = append(read.Items, item)
			for i := range read.SourceStatuses {
				if read.SourceStatuses[i].Provider == "anthropic" {
					status := &read.SourceStatuses[i]
					status.ProducerRef = "agent-run-claude-statusline"
					status.Availability = providerallowance.AvailabilityPartial
					status.LastAttempt = providerallowance.LastAttempt{AttemptedAt: item.ReceivedAt, CompletedAt: &item.ReceivedAt, Result: providerallowance.AttemptSuccess}
					status.Freshness = item.Freshness
					status.Validation = item.Validation
					status.Binding = item.Binding
				}
			}
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

// readAggregateAllowance GETs one cached providerallowance.read.v1 document.
// The default local preflight does not call it. The Codex current-source slot
// is a different command. Redirects are not followed and the GET is not retried.
func (a *app) readAggregateAllowance(ctx context.Context) error {
	if ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage unavailable"}
	}
	if a.env.Getenv == nil {
		return &exitError{code: ExitFailed, msg: "usage read unavailable"}
	}
	c, err := a.connect(ctx, connOpts{})
	if err != nil || c == nil || c.http == nil || c.url == "" || ctx.Err() != nil {
		if ctx.Err() != nil {
			return &exitError{code: ExitFailed, msg: "usage unavailable"}
		}
		var ee *exitError
		if errors.As(err, &ee) && ee.code == ExitUsage {
			return ee
		}
		return &exitError{code: ExitFailed, msg: "usage read unavailable"}
	}
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *c
	one.http = &hc
	one.rejectOversizeResponse = true
	status, raw, err := a.send(ctx, &one, http.MethodGet, c.url+usageAggregateAllowancePath, nil)
	if err != nil || ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage read unavailable"}
	}
	if status != http.StatusOK {
		return usageAggregateStatus(status)
	}
	if len(raw) == 0 || len(raw) > providerallowance.FixtureParseLimits.MaxBytes {
		return &exitError{code: ExitFailed, msg: "usage read unavailable"}
	}
	if _, err = providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits); err != nil {
		return &exitError{code: ExitFailed, msg: "usage read unavailable"}
	}
	encoded := raw
	if encoded[len(encoded)-1] != '\n' {
		encoded = append(append([]byte(nil), raw...), '\n')
	}
	if n, werr := a.env.Stdout.Write(encoded); werr != nil || n != len(encoded) {
		return &exitError{code: ExitFailed, msg: "usage output unavailable"}
	}
	return nil
}

// publishAggregateAllowance sends one canonical cached read. Redirects are not
// followed and the PUT is not retried. The command does not acquire from a provider.
func (a *app) publishAggregateAllowance(ctx context.Context, from string) error {
	if ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage unavailable"}
	}
	body, err := readUsagePublish(a, from)
	if err != nil || ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage publication unavailable"}
	}
	doc, err := providerallowance.DecodeReadResponse(body, providerallowance.FixtureParseLimits)
	if err != nil {
		return &exitError{code: ExitFailed, msg: "usage publication unavailable"}
	}
	canonical, err := json.Marshal(doc)
	if err != nil || len(canonical) == 0 || len(canonical) > providerallowance.FixtureParseLimits.MaxBytes || canonical[len(canonical)-1] == '\n' {
		return &exitError{code: ExitFailed, msg: "usage publication unavailable"}
	}
	if _, err = providerallowance.DecodeReadResponse(canonical, providerallowance.FixtureParseLimits); err != nil {
		return &exitError{code: ExitFailed, msg: "usage publication unavailable"}
	}
	if a.env.Getenv == nil {
		return &exitError{code: ExitFailed, msg: "usage publication unavailable"}
	}
	c, err := a.connect(ctx, connOpts{})
	if err != nil || c == nil || c.http == nil || c.url == "" || ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage publication unavailable"}
	}
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *c
	one.http = &hc
	one.rejectOversizeResponse = true
	status, _, err := a.send(ctx, &one, http.MethodPut, c.url+usageAggregateAllowancePath, canonical)
	if err != nil || ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage publication unavailable"}
	}
	switch status {
	case http.StatusNoContent:
		if n, werr := a.env.Stdout.Write([]byte(usageAggregatePublishedLine)); werr != nil || n != len(usageAggregatePublishedLine) {
			return &exitError{code: ExitFailed, msg: "usage output unavailable"}
		}
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return &exitError{code: ExitAuth, msg: "usage publication refused"}
	case http.StatusNotFound:
		return &exitError{code: ExitNotFound, msg: "usage publication unavailable"}
	case http.StatusConflict:
		return &exitError{code: ExitFailed, msg: "usage publication conflict"}
	default:
		if status >= 300 && status < 400 {
			return &exitError{code: ExitFailed, msg: "usage publication refused"}
		}
		return &exitError{code: ExitFailed, msg: "usage publication unavailable"}
	}
}

func readUsagePublish(a *app, from string) ([]byte, error) {
	r := a.env.Stdin
	if from != "-" {
		fd, err := unix.Open(from, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return nil, err
		}
		f := os.NewFile(uintptr(fd), "usage-publish")
		defer func() { _ = f.Close() }()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() {
			return nil, errors.New("usage publication unavailable")
		}
		r = f
	}
	if r == nil {
		return nil, errors.New("usage publication unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(r, int64(providerallowance.FixtureParseLimits.MaxBytes+1)))
	if err != nil || len(raw) == 0 || len(raw) > providerallowance.FixtureParseLimits.MaxBytes {
		return nil, errors.New("usage publication unavailable")
	}
	return raw, nil
}

func usageAggregateStatus(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &exitError{code: ExitAuth, msg: "usage read refused"}
	case http.StatusNotFound:
		return &exitError{code: ExitNotFound, msg: "usage read unavailable"}
	default:
		if status >= 300 && status < 400 {
			return &exitError{code: ExitFailed, msg: "usage read refused"}
		}
		return &exitError{code: ExitFailed, msg: "usage read unavailable"}
	}
}
