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

// errUsageSourceInput is internal. Callers replace it with a fixed message
// that does not include the path or the bytes that were read.
var errUsageSourceInput = errors.New("usage-source input unavailable")

const usageSourceMaxBytes = 16 << 10

// usageSourcePublishPath is the only publication target. It is not a flag.
const usageSourcePublishPath = "/v1/provider-source-reports/codex-current-source"

// usageSourcePublishedLine is the only success text. It is not a server body.
const usageSourcePublishedLine = "published codex-current-source status=204\n"

const usageSourceUsage = "usage-source: expected -o json --incarnation ID --generation N --max-age DURATION (--codex-tool-report FILE|- | --codex-raw-tool-report FILE|- --observed-at RFC3339) [--publish]"

// usageSourceFlags is the explicit input mode. Flat and raw are never mixed.
// observedAt is the caller's original retrieval spelling for a raw envelope.
// It is not the clock, a file time, or the provider reset.
type usageSourceFlags struct {
	path        string
	raw         bool
	observedAt  string
	incarnation string
	generation  int64
	maxAge      time.Duration
	publish     bool
}

// usageSource reads one caller-supplied Codex tool report and writes the
// sanitized current-source JSON. Flat input stays the flattened observation.
// Raw input is the nested tool envelope plus --observed-at. Publication is
// off unless --publish is set. The command does not grant, impersonate, or
// treat a parsed report as account entitlement.
func (a *app) usageSource(ctx context.Context, args []string) error {
	in, err := parseUsageSourceArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		_, werr := io.WriteString(a.env.Stdout, commandHelp["usage-source"])
		return werr
	}
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage-source unavailable"}
	}
	raw, err := a.readCodexToolReport(in.path)
	if err != nil {
		return &exitError{code: ExitFailed, msg: "usage-source input unavailable"}
	}
	if ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage-source unavailable"}
	}
	now := time.Now
	if a.env.Now != nil {
		now = a.env.Now
	}
	at := now()
	selected := providerallowance.CurrentSourceSelection{
		PublisherRef:        providerallowance.CurrentSourcePublisher,
		ProducerIncarnation: in.incarnation,
		SourceGeneration:    in.generation,
		MaxAge:              in.maxAge,
	}
	var report providerallowance.CurrentSourceReport
	if in.raw {
		report, err = providerallowance.CodexRawToolReport(raw, in.observedAt, selected, at)
	} else {
		report, err = providerallowance.CodexToolReport(raw, selected, at)
	}
	if err != nil {
		return &exitError{code: ExitFailed, msg: "usage-source input unavailable"}
	}
	if err := providerallowance.ValidateCurrentSource(report, selected, at); err != nil {
		return &exitError{code: ExitFailed, msg: "usage-source input unavailable"}
	}
	body, err := json.Marshal(report)
	if err != nil || len(body) == 0 || body[len(body)-1] == '\n' {
		return &exitError{code: ExitFailed, msg: "usage-source unavailable"}
	}
	if _, err := providerallowance.DecodeCurrentSource(body); err != nil || ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage-source unavailable"}
	}
	if in.publish {
		return a.publishCurrentSource(ctx, body)
	}
	encoded := append(body, '\n')
	if n, err := a.env.Stdout.Write(encoded); err != nil || n != len(encoded) {
		return &exitError{code: ExitFailed, msg: "usage-source output unavailable"}
	}
	return nil
}

func parseUsageSourceArgs(args []string) (usageSourceFlags, error) {
	fs := flag.NewFlagSet("usage-source", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flatPath, rawPath, observedAt, incarnation, maxAgeText, output string
	var generation int64
	var publish bool
	fs.StringVar(&flatPath, "codex-tool-report", "", "")
	fs.StringVar(&rawPath, "codex-raw-tool-report", "", "")
	fs.StringVar(&observedAt, "observed-at", "", "")
	fs.StringVar(&incarnation, "incarnation", "", "")
	fs.Int64Var(&generation, "generation", 0, "")
	fs.StringVar(&maxAgeText, "max-age", "", "")
	fs.StringVar(&output, "o", "", "")
	fs.BoolVar(&publish, "publish", false, "")
	counts := flagCounts(args, "codex-tool-report", "codex-raw-tool-report", "observed-at", "incarnation", "generation", "max-age", "o", "publish")
	parseErr := fs.Parse(args)
	if errors.Is(parseErr, flag.ErrHelp) {
		return usageSourceFlags{}, flag.ErrHelp
	}
	if parseErr != nil || fs.NArg() != 0 || output != "json" || incarnation == "" || maxAgeText == "" {
		return usageSourceFlags{}, usageError(usageSourceUsage)
	}
	for _, name := range []string{"incarnation", "generation", "max-age", "o"} {
		if counts[name] != 1 {
			return usageSourceFlags{}, usageError(usageSourceUsage)
		}
	}
	if counts["publish"] > 1 || counts["codex-tool-report"] > 1 || counts["codex-raw-tool-report"] > 1 || counts["observed-at"] > 1 {
		return usageSourceFlags{}, usageError(usageSourceUsage)
	}
	flatN := counts["codex-tool-report"]
	rawN := counts["codex-raw-tool-report"]
	obsN := counts["observed-at"]
	var path string
	raw := false
	switch {
	case flatN == 1 && rawN == 0 && obsN == 0:
		path = flatPath
	case flatN == 0 && rawN == 1 && obsN == 1:
		path = rawPath
		raw = true
	default:
		return usageSourceFlags{}, usageError(usageSourceUsage)
	}
	if path == "" || (raw && observedAt == "") {
		return usageSourceFlags{}, usageError(usageSourceUsage)
	}
	maxAge, err := time.ParseDuration(maxAgeText)
	if err != nil {
		return usageSourceFlags{}, usageError(usageSourceUsage)
	}
	return usageSourceFlags{
		path:        path,
		raw:         raw,
		observedAt:  observedAt,
		incarnation: incarnation,
		generation:  generation,
		maxAge:      maxAge,
		publish:     publish,
	}, nil
}

// publishCurrentSource sends one canonical report to the fixed slot.
// The body is json.Marshal bytes with no added newline. Redirects are not
// followed. A conflict, denial, miss, timeout, or any other non-204 answer
// is not retried and is not copied into the error. Success prints only the
// fixed metadata line. connect and send stay the configured client; this
// command adds no URL, identity, or credential of its own.
func (a *app) publishCurrentSource(ctx context.Context, body []byte) error {
	if ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage-source unavailable"}
	}
	if len(body) == 0 || body[len(body)-1] == '\n' || a.env.Getenv == nil {
		return &exitError{code: ExitFailed, msg: "usage-source publication unavailable"}
	}
	c, err := a.connect(ctx, connOpts{})
	if err != nil || c == nil || c.http == nil || c.url == "" || ctx.Err() != nil {
		return &exitError{code: ExitFailed, msg: "usage-source publication unavailable"}
	}
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *c
	one.http = &hc
	status, _, err := a.send(ctx, &one, http.MethodPut, c.url+usageSourcePublishPath, body)
	if err != nil {
		return &exitError{code: ExitFailed, msg: "usage-source publication unavailable"}
	}
	switch status {
	case http.StatusNoContent:
		n, werr := a.env.Stdout.Write([]byte(usageSourcePublishedLine))
		if werr != nil || n != len(usageSourcePublishedLine) {
			return &exitError{code: ExitFailed, msg: "usage-source output unavailable"}
		}
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return &exitError{code: ExitAuth, msg: "usage-source publication refused"}
	case http.StatusNotFound:
		return &exitError{code: ExitNotFound, msg: "usage-source publication unavailable"}
	case http.StatusConflict:
		return &exitError{code: ExitFailed, msg: "usage-source publication conflict"}
	default:
		if status >= 300 && status < 400 {
			return &exitError{code: ExitFailed, msg: "usage-source publication refused"}
		}
		return &exitError{code: ExitFailed, msg: "usage-source publication unavailable"}
	}
}

// readCodexToolReport reads one flat or nested report. The byte cap, regular
// file check, and symlink refusal are the same for both modes.
func (a *app) readCodexToolReport(path string) ([]byte, error) {
	r := a.env.Stdin
	if path != "-" {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return nil, errUsageSourceInput
		}
		f := os.NewFile(uintptr(fd), "usage-source")
		defer func() { _ = f.Close() }()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() {
			return nil, errUsageSourceInput
		}
		r = f
	}
	if r == nil {
		return nil, errUsageSourceInput
	}
	raw, err := io.ReadAll(io.LimitReader(r, int64(usageSourceMaxBytes)+1))
	if err != nil || len(raw) == 0 || len(raw) > usageSourceMaxBytes {
		return nil, errUsageSourceInput
	}
	return raw, nil
}
