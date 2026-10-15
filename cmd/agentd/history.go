package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const historyUsage = `usage:
  ctl history-export plan   --session S --claude-config-dir D --codex-home D [--rescue SESSION/STAMP]
  ctl history-export stream --session S --claude-config-dir D --codex-home D [--rescue SESSION/STAMP] --plan-sha256 H
  ctl history-archive put [--archived-by WHO]       (the content line, then the tar stream, on stdin)
  ctl history-archive verify --session-uid U --content-sha256 H
  ctl history-archive get    --session-uid U [--content-sha256 H]
  ctl history-archive list   --project P [--limit N] [--after CURSOR]
  ctl history-archive manifest --session-uid U [--content-sha256 H] [--offset N] [--limit N]
  ctl history-archive read   --session-uid U --content-sha256 H --file ROOT/PATH --offset N
`

// historyCtl is `ctl history-export plan|stream` (DESIGN-025 6.3), run in a
// session or hold pod over its private HOME, and `ctl history-archive
// put|verify|get|list|manifest|read` (DESIGN-025 7), run in the shelf. A failure is one
// JSON object, {"error": code, "message": ...}, with exit status 1: on stdout,
// except for stream, whose stdout is the tar stream, so its error goes to
// stderr.
func historyCtl(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) < 2 {
		_, _ = fmt.Fprint(stderr, historyUsage)
		return exitUsage
	}
	cmd, sub := args[0], args[1]
	fs := flag.NewFlagSet("ctl "+cmd+" "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		o           agentd.HistoryExportOptions
		planSHA     string
		archivedBy  string
		sessionUID  string
		contentSHA  string
		project     string
		limit       int
		after       string
		file        string
		offset      int64
		pageOffset  int
		pageLimit   int
		projectSeen bool
	)
	switch cmd + " " + sub {
	case "history-export plan", "history-export stream":
		fs.StringVar(&o.Session, "session", "", "")
		fs.StringVar(&o.ClaudeConfigDir, "claude-config-dir", "", "")
		fs.StringVar(&o.CodexHome, "codex-home", "", "")
		fs.StringVar(&o.Rescue, "rescue", "", "")
		if sub == "stream" {
			fs.StringVar(&planSHA, "plan-sha256", "", "")
		}
	case "history-archive put":
		fs.StringVar(&archivedBy, "archived-by", "", "")
	case "history-archive verify", "history-archive get", "history-archive read":
		fs.StringVar(&sessionUID, "session-uid", "", "")
		fs.StringVar(&contentSHA, "content-sha256", "", "")
		if sub == "read" {
			fs.StringVar(&file, "file", "", "")
			fs.Int64Var(&offset, "offset", 0, "")
		}
	case "history-archive manifest":
		fs.StringVar(&sessionUID, "session-uid", "", "")
		fs.StringVar(&contentSHA, "content-sha256", "", "")
		fs.IntVar(&pageOffset, "offset", 0, "")
		fs.IntVar(&pageLimit, "limit", protocol.MaxHistoryManifestPage, "")
	case "history-archive list":
		fs.Func("project", "", func(v string) error { project, projectSeen = v, true; return nil })
		fs.IntVar(&limit, "limit", 0, "")
		fs.StringVar(&after, "after", "", "")
	default:
		_, _ = fmt.Fprint(stderr, historyUsage)
		return exitUsage
	}
	if err := fs.Parse(args[2:]); err != nil || fs.NArg() != 0 ||
		(sub == "stream" && planSHA == "") ||
		((sub == "verify" || sub == "read") && contentSHA == "") ||
		(sub == "list" && !projectSeen) ||
		(sub == "read" && file == "") {
		_, _ = fmt.Fprint(stderr, historyUsage)
		return exitUsage
	}
	s, err := agentd.LoadSettings(getenv)
	if err != nil {
		return historyFail(stdout, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "%v", err))
	}
	a := agentd.NewHistoryArchive(s)
	var out any
	switch sub {
	case "plan":
		plan, err := agentd.PlanHistoryExport(s, o)
		if err != nil {
			return historyFail(stdout, err)
		}
		_, data, err := agentd.HistoryPlanSHA256(plan)
		if err != nil {
			return historyFail(stdout, err)
		}
		if _, err := stdout.Write(append(data, '\n')); err != nil {
			return exitFailure
		}
		return exitOK
	case "stream":
		if err := agentd.StreamHistoryExport(s, o, planSHA, stdout); err != nil {
			return historyFail(stderr, err)
		}
		return exitOK
	case "put":
		out, err = a.Put(stdin, archivedBy)
	case "verify":
		out, err = a.Verify(sessionUID, contentSHA)
	case "get":
		out, err = a.Get(sessionUID, contentSHA)
	case "list":
		out, err = a.List(project, limit, after)
	case "manifest":
		out, err = a.Manifest(sessionUID, contentSHA, pageOffset, pageLimit)
	case "read":
		out, err = a.Read(sessionUID, contentSHA, file, offset)
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

// historyFail prints a failure as one JSON object and returns exit 1. An
// error that is not a refusal is io_error: Unknown for the operator.
func historyFail(w io.Writer, err error) int {
	var he *protocol.HistoryError
	if !errors.As(err, &he) {
		he = protocol.HistoryErrorf(protocol.HistoryErrIO, "", "%v", err)
	}
	data, merr := protocol.CanonicalJSON(he)
	if merr != nil {
		data = []byte(`{"error":"io_error","message":"the error could not be encoded"}`)
	}
	_, _ = w.Write(append(data, '\n'))
	return exitFailure
}

// historyShelfCheck is `agentd shelf --history-archive`'s start check: the
// archive must be mounted. It is a private command seam for shelf tests.
var historyShelfCheck = func(s agentd.Settings) error {
	return agentd.NewHistoryArchive(s).CheckMounted()
}
