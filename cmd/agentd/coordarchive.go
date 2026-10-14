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

const coordArchiveUsage = "usage: agentd ctl coord-archive put | get --resource R --name N --uid U --resource-version V --sha256 H\n"

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
	switch args[0] {
	case "put":
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
	if args[0] == "put" {
		raw, readErr := io.ReadAll(io.LimitReader(stdin, protocol.MaxCoordArchiveRequestBytes+1))
		if readErr != nil || len(raw) > protocol.MaxCoordArchiveRequestBytes || !utf8.Valid(raw) {
			return historyFail(stdout, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "put request read, byte bound or UTF-8 refused"))
		}
		var request protocol.CoordArchivePutRequest
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		var extra any
		if err := dec.Decode(&request); err != nil || !errors.Is(dec.Decode(&extra), io.EOF) {
			return historyFail(stdout, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "one manifest/object request required"))
		}
		out, err = a.PutUnmarked(request.Manifest, bytes.NewReader(request.Object))
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
