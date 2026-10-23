// Command workflow_handoff validates one bounded coordinator handoff record.
// It prints the recorded state and the next manual action. It does not
// dispatch work, grant authority, watch a service, or write tracker state.
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
	if len(args) != 1 || (strings.HasPrefix(args[0], "-") && args[0] != "-") {
		fmt.Fprintln(errw, "usage: workflow_handoff <file> | workflow_handoff -")
		res := refused("usage: workflow_handoff <file> | workflow_handoff -")
		_ = writeResult(out, res)
		return 2
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
		fmt.Fprintln(errw, err.Error())
		return 2
	}
	res, code := check(data)
	if err := writeResult(out, res); err != nil {
		fmt.Fprintln(errw, err.Error())
		return 2
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
