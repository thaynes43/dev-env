// home-retention-dry-run classifies a finite offline snapshot. It cannot collect
// live evidence, activate cleanup, launch commands, or delete anything.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/thaynes43/dev-env/internal/homeretention"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("home-retention-dry-run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	nowFlag := flags.String("now", "", "required evaluation time, RFC3339")
	inputFlag := flags.String("input", "-", "offline snapshot file or - for stdin (maximum 4MiB, 1000 records)")
	trustFlag := flags.String("trust", "", "separate trusted public-key JSON file (optional; no trust means no immediate candidates)")
	warningFlag := flags.Duration("warning-lead", homeretention.DefaultWarningLead, "warning lead, positive and at most 168h")
	freshnessFlag := flags.Duration("observation-max-age", homeretention.DefaultObservationMaxAge, "maximum age of signed current observations, positive and at most 1h")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	fail := func(err error) int { _, _ = fmt.Fprintln(stderr, err); return 2 }
	if flags.NArg() != 0 {
		return fail(errors.New("unexpected positional argument"))
	}
	now, err := time.Parse(time.RFC3339Nano, *nowFlag)
	if err != nil || now.IsZero() {
		return fail(errors.New("--now requires an explicit RFC3339 evaluation time"))
	}
	policy := homeretention.Policy{WarningLead: *warningFlag, ObservationMaxAge: *freshnessFlag}
	// Reject policy flags before opening any input.
	if _, err := homeretention.Evaluate(homeretention.Snapshot{Version: homeretention.Version, Records: []homeretention.Record{}}, now, policy, nil); err != nil {
		return fail(err)
	}
	var trust *homeretention.TrustStore
	if *trustFlag != "" {
		file, err := os.Open(*trustFlag)
		if err != nil {
			return fail(fmt.Errorf("open trust: %w", err))
		}
		trust, err = homeretention.DecodeTrust(file)
		closeErr := file.Close()
		if err != nil {
			return fail(fmt.Errorf("decode trust: %w", err))
		}
		if closeErr != nil {
			return fail(closeErr)
		}
	}
	input := stdin
	if *inputFlag != "-" {
		file, err := os.Open(*inputFlag)
		if err != nil {
			return fail(fmt.Errorf("open snapshot: %w", err))
		}
		defer func() { _ = file.Close() }()
		input = file
	}
	snapshot, err := homeretention.DecodeSnapshot(input)
	if err != nil {
		return fail(err)
	}
	report, err := homeretention.Evaluate(snapshot, now, policy, trust)
	if err != nil {
		return fail(err)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fail(err)
	}
	return 0
}
