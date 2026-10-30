// Command workflow_handoff validates one bounded coordinator handoff record.
// It prints the recorded state and the next manual action. With --watchdog it
// prints a dry-run PR-bound decision plan for that record. It does not
// dispatch work, grant authority, notify, watch a service, or write tracker state.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, in io.Reader, out, errw io.Writer) int {
	if len(args) >= 1 && args[0] == "--checkins" {
		return runCheckins(args, in, out, errw)
	}
	if len(args) >= 1 && args[0] == "--watchdog" {
		if len(args) != 2 {
			return usage(out, errw)
		}
		return runWatchdog(args[1], in, out, errw)
	}
	if len(args) != 1 || (strings.HasPrefix(args[0], "-") && args[0] != "-") {
		return usage(out, errw)
	}
	var (
		data []byte
		err  error
	)
	if args[0] == "-" {
		data, err = readBounded(in)
	} else {
		data, err = readFile(args[0])
	}
	if err != nil {
		res := refused(err.Error())
		_ = writeResult(out, res)
		_, _ = fmt.Fprintln(errw, err.Error())
		return 2
	}
	res, code := check(data)
	if err := writeResult(out, res); err != nil {
		_, _ = fmt.Fprintln(errw, err.Error())
		return 2
	}
	return code
}

func usage(out, errw io.Writer) int {
	msg := "usage: workflow_handoff <file> | workflow_handoff - | workflow_handoff --watchdog <file> | workflow_handoff --watchdog -"
	_, _ = fmt.Fprintln(errw, msg)
	_ = writeResult(out, refused(msg))
	return 2
}

func runWatchdog(path string, in io.Reader, out, errw io.Writer) int {
	if path != "-" && (path == "" || strings.HasPrefix(path, "-")) {
		plan := refusedWatchdog("usage: workflow_handoff --watchdog <file> | workflow_handoff --watchdog -")
		return emitWatchdog(out, errw, plan, 2)
	}
	var (
		data []byte
		err  error
	)
	if path == "-" {
		data, err = readBounded(in)
	} else {
		data, err = readFile(path)
	}
	if err != nil {
		return emitWatchdog(out, errw, refusedWatchdog(err.Error()), 2)
	}
	plan, code := planWatchdog(data)
	return emitWatchdog(out, errw, plan, code)
}

func emitWatchdog(out, errw io.Writer, plan watchdogPlan, code int) int {
	if err := writeWatchdog(out, plan); err != nil {
		_, _ = fmt.Fprintln(errw, err.Error())
		return 2
	}
	if code != 0 && len(plan.Reasons) > 0 {
		_, _ = fmt.Fprintln(errw, plan.Reasons[0])
	}
	return code
}

func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, rerr := readBounded(f)
	cerr := f.Close()
	if rerr != nil {
		return nil, rerr
	}
	if cerr != nil {
		return nil, cerr
	}
	return data, nil
}

func readBounded(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDocumentBytes {
		return nil, fmt.Errorf("document exceeds 128KiB")
	}
	return data, nil
}

func writeResult(out io.Writer, res result) error {
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}
