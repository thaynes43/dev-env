package agentrun

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/pages"
)

const alertmanagerPagesHelp = `Pages · Alertmanager — current snapshot, not notification or delivery history

Usage: agent-run pages list --source alertmanager [--search TEXT] [--status active|suppressed|unprocessed] [--from-file FILE|-] [--orders-from-file CM.json --orders-observed-at RFC3339] [-o json|html]
       agent-run pages show FINGERPRINT --source alertmanager [--from-file FILE|-] [--orders-from-file CM.json --orders-observed-at RFC3339] [-o json|html]

Reads a caller-supplied Alertmanager API v2 alert array or snapshot envelope
from a file or stdin. Source and observation time are caller assertions;
disposition and recurrence are Unknown. Invalid input is Unavailable. No
network requests are made. Optional V1 orders require a caller-asserted capture
time, taken after the read; observations older than five minutes are Stale. A
malformed orders file or a future capture time is Invalid and exits 1. An order joins
only on exact decoded equality of its alertIdentity fingerprint and startsAt with
the alert's, created no earlier than startsAt minus 30 seconds. Multiple orders
require one explicit parentOrder chain; the leaf joins and orderChain lists its
keys from root to leaf. Broken, malformed or duplicate lineage is Unjoined.
Real joins need the held producer drafts merged and installed. See docs/alertmanager-pages-poc.md.
`

func isAlertmanagerSource(args []string) bool {
	for i := 1; i < len(args); i++ {
		token := args[i]
		if token == "--" {
			break
		}
		if !strings.HasPrefix(token, "-") || token == "-" {
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(token, "-"), "=")
		if name == "source" {
			if hasValue {
				if value == "alertmanager" {
					return true
				}
			} else if i+1 < len(args) {
				i++
				if args[i] == "alertmanager" {
					return true
				}
			}
			continue
		}
		switch name {
		case "from-file", "orders-from-file", "orders-observed-at", "o", "output", "sender", "state", "since", "limit", "search", "status":
			if !hasValue && i+1 < len(args) {
				i++
			}
		}
	}
	return false
}
func (a *app) pagesAlertmanager(ctx context.Context, args []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	verb := args[0]
	if verb != "list" && verb != "show" {
		return usageError("pages: expected list or show")
	}
	fs := flag.NewFlagSet("pages "+verb, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var source, from, output, search, status, ordersFrom, ordersObserved string
	fs.StringVar(&source, "source", "", "")
	fs.StringVar(&from, "from-file", "-", "")
	fs.StringVar(&ordersFrom, "orders-from-file", "", "")
	fs.StringVar(&ordersObserved, "orders-observed-at", "", "")
	fs.StringVar(&output, "o", "", "")
	fs.StringVar(&output, "output", "", "")
	fs.StringVar(&search, "search", "", "")
	fs.StringVar(&status, "status", "", "")
	var pos []string
	for rest := args[1:]; len(rest) > 0; {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				_, _ = io.WriteString(a.env.Stdout, alertmanagerPagesHelp)
				return &exitError{code: ExitOK}
			}
			return usageError("pages alertmanager: %v", err)
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
	if source != "alertmanager" || (output != "" && output != "json" && output != "html") || (verb == "list" && len(pos) != 0) || (verb == "show" && len(pos) != 1) || (verb == "show" && (search != "" || status != "")) || (status != "" && status != "active" && status != "suppressed" && status != "unprocessed") {
		return usageError("pages alertmanager: invalid arguments")
	}
	var ordersAt time.Time
	if ordersFrom != "" || ordersObserved != "" {
		var parseErr error
		ordersAt, parseErr = time.Parse(time.RFC3339Nano, ordersObserved)
		if ordersFrom == "" || ordersFrom == "-" || parseErr != nil {
			return usageError("pages alertmanager: --orders-from-file needs a file and --orders-observed-at RFC3339 capture time")
		}
	}
	now := a.now()
	sources, ordersErr := readPagesOrderSource(ordersFrom, ordersAt)
	if ordersErr == nil && ordersFrom != "" && ordersAt.After(now) {
		// The projection reports Invalid; fail the exit like a malformed file.
		ordersErr = errors.New("V1 work-order capture time is in the future")
	}
	snapshot := pages.UnavailableAlertmanager()
	var r io.Reader
	if from == "-" {
		r = a.env.Stdin
	} else {
		f, err := os.Open(from)
		if err == nil {
			defer func() { _ = f.Close() }()
			r = f
		}
	}
	var err error
	if r == nil {
		err = errors.New("input unavailable")
	} else {
		var raw []byte
		raw, err = io.ReadAll(io.LimitReader(r, pages.AlertmanagerMaxBytes+1))
		if err == nil {
			snapshot, err = pages.ParseAlertmanager(raw)
		}
	}
	if err != nil {
		snapshot = pages.UnavailableAlertmanager()
	} else {
		identities := make([]pages.AlertIdentity, 0, len(snapshot.Alerts))
		for _, alert := range snapshot.Alerts {
			identities = append(identities, pages.AlertIdentity{Fingerprint: alert.Fingerprint, StartsAt: alert.StartsAt})
		}
		responses := pages.ProjectResponses(ctx, identities, sources, now)
		for i, identity := range identities {
			response := responses[identity]
			snapshot.Alerts[i].Response = &response
		}
		filtered := make([]pages.AlertmanagerAlert, 0, len(snapshot.Alerts))
		for _, alert := range snapshot.Alerts {
			if verb == "show" && alert.Fingerprint != pos[0] {
				continue
			}
			if status != "" && alert.Status != status {
				continue
			}
			if search != "" && !strings.Contains(strings.ToLower(alert.Name+" "+alert.Summary+" "+alert.Description+" "+alert.Fingerprint), strings.ToLower(search)) {
				continue
			}
			filtered = append(filtered, alert)
		}
		snapshot.Alerts = filtered
		snapshot.Selection = "All current alerts"
		if verb == "show" {
			snapshot.Selection = "Fingerprint: " + pos[0]
		}
		if search != "" {
			snapshot.Selection += "; search: " + search
		}
		if status != "" {
			snapshot.Selection += "; status: " + status
		}
	}
	switch output {
	case "json":
		if e := a.printJSON(snapshot); e != nil {
			return e
		}
	case "html":
		if e := pages.RenderAlertmanagerHTML(a.env.Stdout, snapshot); e != nil {
			return e
		}
	default:
		if e := pages.RenderAlertmanagerText(a.env.Stdout, snapshot); e != nil {
			return e
		}
	}
	if err != nil || ordersErr != nil {
		return &exitError{code: ExitFailed}
	}
	if verb == "show" && len(snapshot.Alerts) == 0 {
		return &exitError{code: ExitNotFound}
	}
	return nil
}

// offlinePageOrders preserves failed file reads as Unavailable, distinct from
// a supplied but invalid ConfigMap. Errors never echo file paths or contents.
type offlinePageOrders struct {
	err error
}

func (s offlinePageOrders) FindOrders(_ context.Context, _ []pages.AlertIdentity) (map[pages.AlertIdentity]pages.OrderObservation, error) {
	return nil, s.err
}

func readPagesOrderSource(from string, observedAt time.Time) (pages.ResponseSources, error) {
	if from == "" {
		return pages.ResponseSources{}, nil
	}
	sources := pages.ResponseSources{MaxAge: 5 * time.Minute}
	unavailable := errors.New("V1 work-order snapshot unavailable")
	sources.Orders = offlinePageOrders{err: unavailable}
	f, err := os.Open(from)
	if err != nil {
		return sources, unavailable
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, pages.V1OrdersMaxBytes+1))
	if err != nil {
		return sources, unavailable
	}
	snapshot, err := pages.ParseV1ResponderOrders(raw, observedAt)
	sources.Orders = snapshot
	return sources, err
}
