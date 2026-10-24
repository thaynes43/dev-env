// Command stopped_handoff checks one synthetic stopped-predecessor record.
// Default check reads that record and writes nothing. Explicit fixture
// subcommands stay inside the directories they are given. The command does
// not stop a live executor, read private history, assign work, or mutate a
// TaskClaim.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, in io.Reader, out, errw io.Writer) int {
	if len(args) == 0 {
		return usage(out, errw)
	}
	switch args[0] {
	case "check":
		return cmdCheck(args[1:], in, out, errw)
	case "preflight":
		if len(args) != 1 {
			return usage(out, errw)
		}
		return emit(out, preflight("."))
	case "materialize":
		if len(args) != 2 {
			return usage(out, errw)
		}
		report, err := materialize(args[1])
		if err != nil {
			return failFixture(out, errw, err)
		}
		return emit(out, report)
	case "verify":
		return cmdVerify(args[1:], out, errw)
	case "restore":
		return cmdRestore(args[1:], out, errw)
	default:
		return usage(out, errw)
	}
}

func usage(out, errw io.Writer) int {
	msg := "usage: stopped_handoff check [--verify-root dir] <file>|- | preflight | materialize <dir> | verify --root dir --manifest file | restore --record file --from dir --into dir"
	_, _ = fmt.Fprintln(errw, msg)
	res := refused(msg)
	_ = writeResult(out, res)
	return 2
}

func cmdCheck(args []string, in io.Reader, out, errw io.Writer) int {
	verifyRoot := ""
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--verify-root" {
			if i+1 >= len(args) {
				return usage(out, errw)
			}
			verifyRoot = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	if len(rest) != 1 {
		return usage(out, errw)
	}
	data, err := readInput(rest[0], in)
	if err != nil {
		res := refused(err.Error())
		_ = writeResult(out, res)
		_, _ = fmt.Fprintln(errw, err.Error())
		return 2
	}
	res, code := check(data)
	if verifyRoot != "" {
		res = applyVerify(res, verifyRoot, data)
		code = res.ExitCode
	}
	if err := writeResult(out, res); err != nil {
		_, _ = fmt.Fprintln(errw, err.Error())
		return 2
	}
	return code
}

func readInput(path string, in io.Reader) ([]byte, error) {
	if path == "-" {
		return readBounded(in)
	}
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

func applyVerify(res result, root string, data []byte) result {
	start := time.Now()
	if res.ExitCode != 0 || !res.RestoreAuthorized {
		res.FixtureVerified = false
		res.Phases = append(res.Phases, phase{Name: "fixture-verify", Duration: time.Since(start).String()})
		return res
	}
	doc, err := parseDocument(data)
	if err != nil {
		res.FixtureVerified = false
		res.RestoreAuthorized = false
		res.BlockedCustody = true
		res.Outcome = "blocked-custody"
		res.BlockedReason = err.Error()
		res.ExitCode = 0
		res.Phases = append(res.Phases, phase{Name: "fixture-verify", Duration: time.Since(start).String()})
		return res
	}
	if err := verifyFixture(root, doc.Manifest); err != nil {
		res.FixtureVerified = false
		res.RestoreAuthorized = false
		res.BlockedCustody = true
		res.Outcome = "blocked-custody"
		res.BlockedReason = "fixture bytes do not match the manifest: " + err.Error()
		res.NextAction = blockedNext("correct the synthetic fixture or the manifest")
		res.Phases = append(res.Phases, phase{Name: "fixture-verify", Duration: time.Since(start).String()})
		return res
	}
	res.FixtureVerified = true
	res.Phases = append(res.Phases, phase{Name: "fixture-verify", Duration: time.Since(start).String()})
	return res
}

func cmdVerify(args []string, out, errw io.Writer) int {
	root, manifestPath, ok := pairFlags(args, "--root", "--manifest")
	if !ok {
		return usage(out, errw)
	}
	man, err := readManifest(manifestPath)
	if err != nil {
		return failFixture(out, errw, err)
	}
	if err := verifyFixture(root, man); err != nil {
		return failFixture(out, errw, err)
	}
	return emit(out, fixtureReport{Schema: fixtureSchema, Verified: true, OriginalsPreserved: "not-checked"})
}

func cmdRestore(args []string, out, errw io.Writer) int {
	if len(args) != 6 {
		return usage(out, errw)
	}
	flags := map[string]string{}
	for i := 0; i < len(args); i += 2 {
		switch args[i] {
		case "--record", "--from", "--into":
			if i+1 >= len(args) {
				return usage(out, errw)
			}
			flags[args[i]] = args[i+1]
		default:
			return usage(out, errw)
		}
	}
	record, from, into := flags["--record"], flags["--from"], flags["--into"]
	if record == "" || from == "" || into == "" {
		return usage(out, errw)
	}
	data, err := readInput(record, nil)
	if err != nil {
		res := refused(err.Error())
		_ = writeResult(out, res)
		return 2
	}
	res, code := check(data)
	if code != 0 || !res.RestoreAuthorized {
		res.FixtureVerified = false
		res.OriginalsPreserved = "not-checked"
		if err := writeResult(out, res); err != nil {
			return 2
		}
		return code
	}
	doc, err := parseDocument(data)
	if err != nil {
		return failFixture(out, errw, err)
	}
	if err := restoreFixture(from, into, doc.Manifest); err != nil {
		res.FixtureVerified = false
		res.RestoreAuthorized = false
		res.BlockedCustody = true
		res.Outcome = "blocked-custody"
		res.BlockedReason = err.Error()
		res.OriginalsPreserved = "unknown"
		if errors.Is(err, errOriginalAltered) {
			res.OriginalsPreserved = "altered"
		}
		res.ExitCode = 2
		res.Status = "refused"
		_ = writeResult(out, res)
		_, _ = fmt.Fprintln(errw, err.Error())
		return 2
	}
	res.FixtureVerified = true
	res.OriginalsPreserved = "preserved"
	if err := writeResult(out, res); err != nil {
		return 2
	}
	return 0
}

func pairFlags(args []string, a, b string) (string, string, bool) {
	if len(args) != 4 {
		return "", "", false
	}
	vals := map[string]string{}
	for i := 0; i < len(args); i += 2 {
		if args[i] != a && args[i] != b {
			return "", "", false
		}
		vals[args[i]] = args[i+1]
	}
	if vals[a] == "" || vals[b] == "" {
		return "", "", false
	}
	return vals[a], vals[b], true
}

func failFixture(out, errw io.Writer, err error) int {
	_, _ = fmt.Fprintln(errw, err.Error())
	_ = writeJSON(out, fixtureReport{Schema: fixtureSchema, Verified: false, Error: err.Error(), OriginalsPreserved: "not-checked"})
	return 2
}

func writeResult(out io.Writer, res result) error {
	res.normalize()
	return writeJSON(out, res)
}

func emit(out io.Writer, v any) int {
	if err := writeJSON(out, v); err != nil {
		return 2
	}
	return 0
}

func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
