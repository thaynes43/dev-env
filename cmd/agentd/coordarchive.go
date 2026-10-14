package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const coordArchiveUsage = "usage: agentd ctl coord-archive put [--mark] | mark | lookup | get --resource R --name N --uid U --resource-version V --sha256 H\n"

// Private constructor seam for local command tests; no public mount bypass.
var newCoordArchive = agentd.NewCoordArchive

func coordArchiveCtl(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, coordArchiveUsage)
		return exitUsage
	}
	fs := flag.NewFlagSet("coord-archive "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var q protocol.CoordArchiveExactQuery
	var mark bool
	switch args[0] {
	case "put":
		fs.BoolVar(&mark, "mark", false, "")
	case "mark", "lookup":
	case "get":
		fs.StringVar(&q.Resource, "resource", "", "")
		fs.StringVar(&q.Name, "name", "", "")
		fs.StringVar(&q.UID, "uid", "", "")
		fs.StringVar(&q.ResourceVersion, "resource-version", "", "")
		fs.StringVar(&q.SHA256, "sha256", "", "")
	default:
		_, _ = fmt.Fprint(stderr, coordArchiveUsage)
		return exitUsage
	}
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		_, _ = fmt.Fprint(stderr, coordArchiveUsage)
		return exitUsage
	}
	s, err := agentd.LoadSettings(getenv)
	if err != nil {
		return historyFail(stdout, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "%v", err))
	}
	a := newCoordArchive(s)
	var out any
	if args[0] != "get" {
		raw, readErr := io.ReadAll(io.LimitReader(stdin, protocol.MaxCoordArchiveRequestBytes+1))
		if readErr != nil || len(raw) > protocol.MaxCoordArchiveRequestBytes || !utf8.Valid(raw) {
			return historyFail(stdout, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "request read, byte bound or UTF-8 refused"))
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		var extra any
		var request protocol.CoordArchivePutRequest
		var identities []protocol.CoordArchiveIdentity
		var events []protocol.CoordArchiveMarkRequest
		var target any = &request
		if args[0] == "lookup" {
			target = &identities
		} else if args[0] == "mark" {
			target = &events
		}
		if err := dec.Decode(target); err != nil || !errors.Is(dec.Decode(&extra), io.EOF) {
			return historyFail(stdout, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "one complete JSON request required"))
		}
		if args[0] == "lookup" {
			out, err = a.Lookup(identities)
		} else if args[0] == "mark" {
			out, err = a.Mark(events)
		} else {
			if mark != (request.Event != nil) {
				return historyFail(stdout, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "--mark and absent event must be supplied together"))
			}
			out, err = a.PutRecord(request.Manifest, bytes.NewReader(request.Object), request.Event)
		}
	} else {
		out, err = a.GetExact(q)
	}
	if err != nil {
		return historyFail(stdout, err)
	}
	data, err := protocol.CanonicalJSON(out)
	if err != nil {
		return historyFail(stdout, err)
	}
	if _, err := stdout.Write(append(data, '\n')); err != nil {
		return exitFailure
	}
	return exitOK
}
