package agentrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// history list, show and read (DESIGN-025 8): the archived provider history
// of private Sessions whose HOME expired, under DESIGN-013's payload-read
// scope. Every answer is the archive's copy (archived: true); nothing here can
// change it.

func (a *app) history(ctx context.Context, args []string) error {
	if len(args) == 0 {
		_, _ = io.WriteString(a.env.Stderr, commandHelp["history"])
		return &exitError{code: ExitUsage}
	}
	switch args[0] {
	case "list":
		return a.historyList(ctx, args[1:])
	case "show":
		return a.historyShow(ctx, args[1:])
	case "read":
		return a.historyRead(ctx, args[1:])
	case "help", "-h", "--help":
		_, _ = io.WriteString(a.env.Stdout, commandHelp["history"])
		return nil
	}
	_, _ = io.WriteString(a.env.Stderr, commandHelp["history"])
	return usageError("unknown history command %q: it is list, show or read", args[0])
}

// historyNotFound adds what a 404 may mean to the API's own words: the
// history routes answer 404 for anything outside the caller's scope, with the
// reads off, and on an operator that has no such routes.
func historyNotFound(err error) error {
	var ee *exitError
	if errors.As(err, &ee) && ee.code == ExitNotFound {
		return fail(ExitNotFound, "%s\nno archived history answered: none exists within your payload scope, or this operator's archived-history reads are off or older than DESIGN-025 H6", ee.msg)
	}
	return err
}

func (a *app) historyList(ctx context.Context, args []string) error {
	cmd := newCommand("history", outputJSON)
	var project, cont string
	var limit int
	cmd.fs.StringVar(&project, "project", "", "")
	cmd.fs.IntVar(&limit, "limit", 0, "")
	cmd.fs.StringVar(&cont, "continue", "", "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 || project == "" {
		return usageError("history list takes --project <project> and no arguments; agent-run help history")
	}
	q := url.Values{"project": {project}}
	if limit != 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if cont != "" {
		q.Set("continue", cont)
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var out apiv1.HistoryList
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.HistoryPath, q, nil, &out); err != nil {
		return historyNotFound(err)
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(out)
	}
	if len(out.Items) == 0 {
		a.outf("no archived history in project %s\n", project)
	} else {
		tw := a.table()
		_, _ = fmt.Fprintln(tw, "SESSION\tUID\tPROVIDER\tARCHIVED\tFILES\tSIZE\tTERMINAL")
		for _, it := range out.Items {
			if it.Error != "" {
				_, _ = fmt.Fprintf(tw, "-\t%s\t-\t%s\t-\t-\t%s\n", it.SessionUID, it.ArchivedAt.UTC().Format("2006-01-02 15:04"), it.Error)
				continue
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", firstOf(it.Session, "-"), it.SessionUID, firstOf(it.Provider, "-"),
				it.ArchivedAt.UTC().Format("2006-01-02 15:04"), it.Files, humanBytes(it.Bytes), historyTerminal(it.Terminal))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	if out.Continue != "" {
		a.outf("more: agent-run history list --project %s --continue %s\n", project, out.Continue)
	}
	return nil
}

func historyTerminal(t *apiv1.HistoryTerminal) string {
	if t == nil {
		return "-"
	}
	if t.Reason == "" {
		return t.Phase
	}
	return t.Phase + " (" + t.Reason + ")"
}

func (a *app) historyShow(ctx context.Context, args []string) error {
	cmd := newCommand("history", outputJSON)
	var content, cont string
	var limit int
	cmd.fs.StringVar(&content, "content", "", "")
	cmd.fs.IntVar(&limit, "limit", 0, "")
	cmd.fs.StringVar(&cont, "continue", "", "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("history show takes one Session UID; agent-run help history")
	}
	uid := pos[0]
	q := url.Values{}
	if content != "" {
		q.Set("content", content)
	}
	if limit != 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if cont != "" {
		q.Set("continue", cont)
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var m apiv1.HistoryManifest
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.HistoryEntryPath(uid), q, nil, &m); err != nil {
		return historyNotFound(err)
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(m)
	}
	t := m.Terminal
	a.outf("session:    %s (%s)\n", m.Session.Name, m.Session.UID)
	a.outf("project:    %s\n", m.Project)
	a.outf("provider:   %s\n", m.Provider)
	a.outf("archived:   %s (the archive's copy; the HOME is gone or going)\n", m.ArchivedAt.UTC().Format("2006-01-02 15:04 MST"))
	a.outf("content:    %s\n", m.ContentSHA256)
	a.outf("terminal:   %s, observed %s\n", historyTerminal(&t), t.ObservedAt.UTC().Format("2006-01-02 15:04 MST"))
	gitFiles := 0
	gitDir := "-"
	if m.Git != nil {
		gitFiles, gitDir = len(m.Git.Files), m.Git.Rescue
	}
	a.outf("rescue:     %s %s, Git files in %s: %d\n", m.Rescue.Stamp, m.Rescue.Result, gitDir, gitFiles)
	a.outf("files:      %d (%s)\n", m.FileCount, humanBytes(m.Bytes))
	a.outf("excluded:   %d\n", m.ExcludedCount)
	if len(m.Files) > 0 {
		a.outf("\n")
		tw := a.table()
		_, _ = fmt.Fprintln(tw, "FILE\tSIZE\tSHA256")
		for _, f := range m.Files {
			_, _ = fmt.Fprintf(tw, "%s/%s\t%s\t%s\n", f.Root, f.Path, humanBytes(f.Bytes), f.SHA256)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	if len(m.Excluded) > 0 {
		a.outf("\n")
		tw := a.table()
		_, _ = fmt.Fprintln(tw, "EXCLUDED\tCLASS\tSIZE")
		for _, e := range m.Excluded {
			size := humanBytes(e.Bytes)
			if e.Partial {
				size += "+"
			}
			_, _ = fmt.Fprintf(tw, "%s/%s\t%s\t%s\n", e.Root, e.Path, e.Class, size)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	if m.Continue != "" {
		a.outf("more: agent-run history show %s --content %s --continue %s\n", uid, m.ContentSHA256, m.Continue)
	}
	return nil
}

func (a *app) historyRead(ctx context.Context, args []string) error {
	cmd := newCommand("history", outputJSON)
	var content string
	var offset int64
	var all bool
	cmd.fs.StringVar(&content, "content", "", "")
	cmd.fs.Int64Var(&offset, "offset", 0, "")
	cmd.fs.BoolVar(&all, "all", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	switch {
	case len(pos) != 2:
		return usageError("history read takes a Session UID and a file's ROOT/PATH; agent-run help history")
	case offset < 0:
		return usageError("history read: --offset must be 0 or more")
	case all && cmd.c.output == outputJSON:
		return usageError("history read: --all writes the file's bytes; -o json prints one page")
	}
	uid, file := pos[0], pos[1]
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var sum hash.Hash
	if all && offset == 0 {
		sum = sha256.New()
	}
	for pages := 0; ; pages++ {
		q := url.Values{"path": {file}, "offset": {strconv.FormatInt(offset, 10)}}
		if content != "" {
			q.Set("content", content)
		}
		var p apiv1.HistoryFilePage
		if _, err := a.call(ctx, c, http.MethodGet, apiv1.HistoryFilePath(uid), q, nil, &p); err != nil {
			return historyNotFound(err)
		}
		if cmd.c.output == outputJSON {
			return a.printJSON(p)
		}
		if p.Offset != offset || p.Next != p.Offset+int64(len(p.Data)) || len(p.Data) > apiv1.MaxHistoryFilePage || p.Next > p.Bytes || (!p.EOF && len(p.Data) == 0) {
			return fail(ExitFailed, "the API answered a page of %s at offset %d that does not follow from offset %d", file, p.Offset, offset)
		}
		if _, err := a.env.Stdout.Write(p.Data); err != nil {
			return err
		}
		if sum != nil {
			sum.Write(p.Data)
		}
		if !all || p.EOF {
			if sum != nil && hex.EncodeToString(sum.Sum(nil)) != p.FileSHA256 {
				return fail(ExitFailed, "the bytes of %s do not hash to the archive's SHA-256 %s", file, p.FileSHA256)
			}
			return nil
		}
		// Pinned to the entry the first page came from, whatever is archived
		// later.
		content, offset = p.ContentSHA256, p.Next
		if pages > int(p.Bytes/apiv1.MaxHistoryFilePage)+2 {
			return fail(ExitFailed, "the API kept answering pages of %s past its %d bytes", file, p.Bytes)
		}
	}
}
