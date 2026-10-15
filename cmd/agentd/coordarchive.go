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

const coordArchiveUsage = "usage: agentd ctl coord-archive put [--mark] | mark | lookup | get --resource R --name N [--uid U] [--resource-version V --sha256 H] | get-item --resource R --name N [--uid U] | list --resource R --project P [--recipient-uid U] [--limit N] [--after C]\n"

// Private constructor seam for local command tests; no public mount bypass.
var newCoordArchive = agentd.NewCoordArchive

func coordArchiveCtl(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, coordArchiveUsage)
		return exitUsage
	}
	fs := flag.NewFlagSet("coord-archive "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var q protocol.CoordArchiveGetQuery
	var list protocol.CoordArchiveListQuery
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
	case "get-item":
		fs.StringVar(&q.Resource, "resource", "", "")
		fs.StringVar(&q.Name, "name", "", "")
		fs.StringVar(&q.UID, "uid", "", "")
	case "list":
		fs.StringVar(&list.Resource, "resource", "", "")
		fs.StringVar(&list.Project, "project", "", "")
		fs.StringVar(&list.RecipientUID, "recipient-uid", "", "")
		fs.IntVar(&list.Limit, "limit", protocol.MaxCoordArchiveListItems, "")
		fs.StringVar(&list.After, "after", "", "")
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
	switch args[0] {
	case "put", "mark", "lookup":
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
		switch args[0] {
		case "lookup":
			target = &identities
		case "mark":
			target = &events
		}
		if err := dec.Decode(target); err != nil || !errors.Is(dec.Decode(&extra), io.EOF) {
			return historyFail(stdout, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "one complete JSON request required"))
		}
		switch args[0] {
		case "lookup":
			out, err = a.Lookup(identities)
		case "mark":
			out, err = a.Mark(events)
		default:
			if mark != (request.Event != nil) {
				return historyFail(stdout, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "--mark and absent event must be supplied together"))
			}
			out, err = a.PutRecord(request.Manifest, bytes.NewReader(request.Object), request.Event)
		}
	case "get":
		out, err = a.GetSelected(q)
	case "get-item":
		out, err = a.GetItemSelected(q)
	case "list":
		out, err = a.List(list)
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
