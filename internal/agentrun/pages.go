package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/pages"
)

// pagesInputLimit is the most pages reads: a ConfigMap holds 1 MiB, and the
// JSON string inside it is escaped, so twice that is room to spare.
const pagesInputLimit = 2 * pages.MaxDocumentBytes

const pagesDefaultLimit = 20

// pages looks up the Pushover pages dev-env's own senders recorded (DESIGN-031).
// It is offline: it reads the keeper's notice ConfigMap as JSON from a file or
// stdin, calls no API and holds no Kubernetes client.
//
//	kubectl -n dev-env-system get cm dev-env-keeper-notices -o json | agent-run pages list
func (a *app) pages(ctx context.Context, args []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		return a.help([]string{"pages"})
	}
	verb := args[0]
	if verb != "list" && verb != "show" {
		return usageError("pages takes list or show, not %q; agent-run help pages says more", verb)
	}
	var from, sender, state, since, output string
	var limit int
	fs := flag.NewFlagSet("pages "+verb, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	fs.StringVar(&from, "from-file", "-", "")
	fs.StringVar(&output, "o", "", "")
	fs.StringVar(&output, "output", "", "")
	if verb == "list" {
		fs.StringVar(&sender, "sender", "", "")
		fs.StringVar(&state, "state", "", "")
		fs.StringVar(&since, "since", "", "")
		fs.IntVar(&limit, "limit", pagesDefaultLimit, "")
	}
	// Flags and the one positional may come in any order, as elsewhere in agent-run.
	var pos []string
	rest := args[1:]
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				_, _ = io.WriteString(a.env.Stdout, commandHelp["pages"])
				return &exitError{code: ExitOK}
			}
			return usageError("pages %s: %v; agent-run help pages lists its flags", verb, err)
		}
		left := fs.Args()
		consumed := len(rest) - len(left)
		if consumed > 0 && rest[consumed-1] == "--" {
			pos = append(pos, left...)
			break
		}
		if len(left) == 0 {
			break
		}
		pos = append(pos, left[0])
		rest = left[1:]
	}
	if output != "" && output != outputJSON {
		return usageError("pages: -o %s is not a format it prints; it takes -o json", output)
	}
	var f pages.Filter
	switch verb {
	case "list":
		if len(pos) != 0 {
			return usageError("pages list takes no argument, got %q", pos[0])
		}
		if state != "" {
			f.State = pages.State(state)
			if !f.State.Valid() {
				return usageError("pages list: --state is recorded, sent, failed or ambiguous, not %q", state)
			}
		}
		if limit < 1 {
			return usageError("pages list: --limit must be at least 1")
		}
		f.Sender, f.Limit = sender, limit
		if since != "" {
			t, err := parsePagesSince(since, a.now())
			if err != nil {
				return usageError("pages list: %v", err)
			}
			f.Since = t
		}
	case "show":
		if len(pos) != 1 {
			return usageError("pages show takes one page key; agent-run pages list prints them")
		}
	}
	doc, unknown, err := a.readPagesInput(from)
	if err != nil {
		return usageError("pages %s: %v", verb, err)
	}
	if verb == "show" {
		return a.pagesShow(doc, unknown, pos[0], output == outputJSON)
	}
	return a.pagesList(doc, unknown, f, output == outputJSON)
}

func (a *app) now() time.Time {
	if a.env.Now != nil {
		return a.env.Now()
	}
	return time.Now()
}

// parsePagesSince takes 36h, 7d or an RFC 3339 time.
func parsePagesSince(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && n > 0 && n < 3650 {
			return now.Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("--since is a duration such as 36h or 7d, or an RFC 3339 time, not %q", s)
}

// readPagesInput reads the ConfigMap JSON (or a bare pages.json). When the
// input is fine but holds no record, the document is nil and unknown says why;
// that is a normal answer, never a claim that no page was sent.
func (a *app) readPagesInput(from string) (doc *pages.Document, unknown string, err error) {
	var r io.Reader
	if from == "-" {
		if a.env.Stdin == nil {
			return nil, "", errors.New("--from-file - needs stdin")
		}
		if f, ok := a.env.Stdin.(*os.File); ok {
			if fi, serr := f.Stat(); serr == nil && fi.Mode()&os.ModeCharDevice != 0 {
				return nil, "", errors.New("reads the keeper's ConfigMap as JSON from stdin or --from-file: kubectl -n dev-env-system get cm dev-env-keeper-notices -o json | agent-run pages list")
			}
		}
		r = a.env.Stdin
	} else {
		f, oerr := os.Open(from)
		if oerr != nil {
			return nil, "", fmt.Errorf("cannot open --from-file: %w", oerr)
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	raw, err := io.ReadAll(io.LimitReader(r, pagesInputLimit+1))
	if err != nil {
		return nil, "", fmt.Errorf("cannot read the input: %w", err)
	}
	if len(raw) > pagesInputLimit {
		return nil, "", errors.New("the input is larger than a ConfigMap can be")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, "", errors.New("the input is not a JSON object (expected the ConfigMap from kubectl get cm -o json)")
	}
	body := raw
	if _, isCM := top["kind"]; isCM || top["data"] != nil {
		var cm struct {
			Kind string            `json:"kind"`
			Data map[string]string `json:"data"`
		}
		if err := json.Unmarshal(raw, &cm); err != nil {
			return nil, "", errors.New("the ConfigMap's data is not a map of strings")
		}
		if cm.Kind != "" && cm.Kind != "ConfigMap" {
			return nil, "", fmt.Errorf("the input is a %s, not the ConfigMap", cm.Kind)
		}
		text, ok := cm.Data[pages.ConfigMapKey]
		if !ok || strings.TrimSpace(text) == "" {
			return nil, "the ConfigMap has no " + pages.ConfigMapKey + " yet: the keeper has not recorded a page, or does not run the code that records them", nil
		}
		body = []byte(text)
	}
	d, perr := pages.Parse(body)
	if perr != nil {
		return nil, "", fmt.Errorf("the pages record cannot be read (%w); treat every page as Unknown", perr)
	}
	return d, "", nil
}

type pageJSON struct {
	pages.Record
	RecordedAtDisplay    string `json:"recordedAtDisplay"`
	LastAttemptAtDisplay string `json:"lastAttemptAtDisplay"`
}

type coverageJSON struct {
	Banner           string     `json:"banner"`
	Statement        []string   `json:"statement"`
	NotCovered       []string   `json:"notCovered"`
	RecordingSince   *time.Time `json:"recordingSince,omitempty"`
	DroppedCount     int        `json:"droppedCount"`
	OldestRetainedAt *time.Time `json:"oldestRetainedAt,omitempty"`
}

type pagesListJSON struct {
	Coverage coverageJSON `json:"coverage"`
	// Unknown is set when there is no record to read; Records is then empty and
	// that says nothing about whether pages were sent.
	Unknown string     `json:"unknown,omitempty"`
	Records []pageJSON `json:"records"`
}

func toPageJSON(rs []pages.Record) []pageJSON {
	out := make([]pageJSON, 0, len(rs))
	for _, r := range rs {
		out = append(out, pageJSON{Record: r, RecordedAtDisplay: pages.FormatTime(r.RecordedAt), LastAttemptAtDisplay: pages.FormatTime(r.LastAttemptAt)})
	}
	return out
}

func coverageOf(d *pages.Document) coverageJSON {
	c := coverageJSON{Banner: pages.Banner, Statement: pages.Coverage(d), NotCovered: pages.NotCovered}
	if d != nil {
		c.RecordingSince, c.DroppedCount, c.OldestRetainedAt = d.RecordingSince, d.DroppedCount, d.OldestRetainedAt
	}
	return c
}

func (a *app) pagesList(d *pages.Document, unknown string, f pages.Filter, asJSON bool) error {
	var recs []pages.Record
	if d != nil {
		recs = d.Select(f)
	}
	if asJSON {
		return a.printJSON(pagesListJSON{Coverage: coverageOf(d), Unknown: unknown, Records: toPageJSON(recs)})
	}
	w := a.env.Stdout
	for _, line := range pages.Coverage(d) {
		_, _ = fmt.Fprintln(w, line)
	}
	_, _ = fmt.Fprintln(w)
	switch {
	case d == nil:
		_, _ = fmt.Fprintln(w, "Unknown: "+unknown+".")
	case len(recs) == 0:
		_, _ = fmt.Fprintf(w, "No recorded page matches (%d records kept). That is not proof that no page was sent.\n", len(d.Records))
	default:
		for _, r := range recs {
			_, _ = fmt.Fprintf(w, "%s\n    key %s\n", r.Summary(), r.Key)
		}
		if d.DroppedCount == 0 && len(recs) == f.Limit {
			_, _ = fmt.Fprintf(w, "(showing the newest %d; --limit raises it)\n", f.Limit)
		}
	}
	return nil
}

func (a *app) pagesShow(d *pages.Document, unknown, key string, asJSON bool) error {
	var rec *pages.Record
	if d != nil {
		rec = d.Find(key)
	}
	if rec == nil {
		if asJSON {
			if err := a.printJSON(pagesListJSON{Coverage: coverageOf(d), Unknown: "no record for this key; the page is Unknown", Records: []pageJSON{}}); err != nil {
				return err
			}
		} else {
			for _, line := range pages.Coverage(d) {
				_, _ = fmt.Fprintln(a.env.Stdout, line)
			}
			why := unknown
			if why == "" {
				why = "no record has this key"
			}
			_, _ = fmt.Fprintf(a.env.Stdout, "\nUnknown: %s. That is not proof that no such page was sent.\n", why)
		}
		return &exitError{code: ExitNotFound}
	}
	if asJSON {
		return a.printJSON(pagesListJSON{Coverage: coverageOf(d), Records: toPageJSON([]pages.Record{*rec})})
	}
	for _, line := range pages.Coverage(d) {
		_, _ = fmt.Fprintln(a.env.Stdout, line)
	}
	_, _ = fmt.Fprintln(a.env.Stdout)
	_, _ = io.WriteString(a.env.Stdout, rec.Detail())
	return nil
}
