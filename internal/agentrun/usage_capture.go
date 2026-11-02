package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

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
	raw, err := readUsagePublish(a, input)
	if err != nil {
		return fail()
	}
	selected, err := readUsagePublish(a, trustFile)
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
