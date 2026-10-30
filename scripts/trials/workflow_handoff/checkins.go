package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

const checkinsSchema = "dev-env.workflow-checkins/v1"
const contextsSchema = "dev-env.workflow-checkin-contexts/v1"

// The export is display evidence. In particular, reasons are opwork's output,
// not an instruction language or a second reason engine.
type checkinsExport struct {
	Page                   int               `json:"page"`
	PageSize               int               `json:"page_size"`
	HasMore                *bool             `json:"has_more"`
	OwnerFilterScope       string            `json:"owner_filter_scope"`
	OwnerMatchTotal        json.RawMessage   `json:"owner_match_total"`
	TotalBeforeOwnerFilter json.RawMessage   `json:"total_before_owner_filter"`
	Items                  []json.RawMessage `json:"items"`
}

type checkinItem struct {
	ID              int      `json:"id"`
	Owner           string   `json:"owner"`
	CheckAfter      *string  `json:"check_after"`
	Reasons         []string `json:"reasons"`
	HistoryComplete *bool    `json:"history_complete"`
}

type checkinContexts struct {
	Schema     string            `json:"schema"`
	ObservedAt string            `json:"observedAt"`
	Contexts   []json.RawMessage `json:"contexts"`
}

type checkinContext struct {
	ID                   int               `json:"id"`
	Owner                string            `json:"owner"`
	Story                string            `json:"story"`
	Repository           string            `json:"repository"`
	PullRequest          int               `json:"pullRequest"`
	Head                 string            `json:"head"`
	Coordinator          string            `json:"coordinator"`
	Task                 string            `json:"task"`
	UID                  string            `json:"uid"`
	Kind                 string            `json:"kind"`
	Handoff              json.RawMessage   `json:"handoff"`
	Receipts             []json.RawMessage `json:"receipts"`
	OwnerEscalationDueAt *string           `json:"ownerEscalationDueAt,omitempty"`
}

type checkinsPlan struct {
	Schema           string          `json:"schema"`
	Status           string          `json:"status"`
	DryRun           bool            `json:"dryRun"`
	Executed         bool            `json:"executed"`
	CheckerAuthority string          `json:"checkerAuthority"`
	ObservedAt       string          `json:"observedAt,omitempty"`
	Visibility       json.RawMessage `json:"visibility"`
	Reasons          []string        `json:"reasons"`
	Items            []checkinView   `json:"items"`
}

type checkinView struct {
	ID               int    `json:"id"`
	Status           string `json:"status"`
	Bound            bool   `json:"bound"`
	DryRun           bool   `json:"dryRun"`
	Executed         bool   `json:"executed"`
	CheckerAuthority string `json:"checkerAuthority"`
	// Sources retains all distinct variants when an item ID conflicts.
	Sources []json.RawMessage `json:"sources"`
	Reasons []string          `json:"reasons"`
	Plan    *watchdogPlan     `json:"plan,omitempty"`
}

func newCheckinsPlan() checkinsPlan {
	return checkinsPlan{Schema: checkinsSchema, Status: "reduced", DryRun: true, CheckerAuthority: checkerAuthority, Visibility: json.RawMessage("null"), Reasons: []string{}, Items: []checkinView{}}
}

func refusedCheckins(msg string) (checkinsPlan, int) {
	p := newCheckinsPlan()
	p.Status = "refused"
	p.Reasons = append(p.Reasons, msg)
	return p, 2
}

func runCheckins(args []string, in io.Reader, out, errw io.Writer) int {
	var p checkinsPlan
	var code int
	if len(args) != 4 || args[0] != "--checkins" || args[2] != "--contexts" || args[3] == "-" || strings.HasPrefix(args[3], "-") || args[3] == "" || (args[1] != "-" && (args[1] == "" || strings.HasPrefix(args[1], "-"))) {
		p, code = refusedCheckins("usage: workflow_handoff --checkins <export.json|-> --contexts <contexts.json>")
	} else {
		var data []byte
		var err error
		if args[1] == "-" {
			data, err = readBounded(in)
		} else {
			data, err = readFile(args[1])
		}
		if err == nil {
			var contexts []byte
			contexts, err = readFile(args[3])
			if err == nil {
				p, code = planCheckins(data, contexts)
			}
		}
		if err != nil {
			p, code = refusedCheckins(err.Error())
		}
	}
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(p); err != nil {
		_, _ = fmt.Fprintln(errw, err)
		return 2
	}
	if code != 0 && len(p.Reasons) > 0 {
		_, _ = fmt.Fprintln(errw, p.Reasons[0])
	}
	return code
}

func planCheckins(data, contextData []byte) (checkinsPlan, int) {
	export, contexts, visibility, err := parseCheckins(data, contextData)
	if err != nil {
		return refusedCheckins(err.Error())
	}
	p := newCheckinsPlan()
	p.ObservedAt = contexts.ObservedAt
	p.Visibility = visibility
	partial := *export.HasMore || export.OwnerFilterScope == "selected page"
	if partial {
		p.Reasons = append(p.Reasons, "partial-page visibility; recommendations withheld")
	}
	items := map[int][]json.RawMessage{}
	for _, raw := range export.Items {
		var item checkinItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return refusedCheckins("invalid check-in item")
		}
		if item.ID <= 0 || item.Reasons == nil {
			return refusedCheckins("check-in requires positive id and reasons list")
		}
		items[item.ID] = appendDistinctJSON(items[item.ID], raw)
	}
	bound := map[int][]json.RawMessage{}
	for _, raw := range contexts.Contexts {
		var ctx checkinContext
		if err := decodeCheckinContext(raw, &ctx); err != nil {
			return refusedCheckins(err.Error())
		}
		if ctx.ID <= 0 {
			return refusedCheckins("context id must be positive")
		}
		bound[ctx.ID] = appendDistinctJSON(bound[ctx.ID], raw)
	}
	ids := make([]int, 0, len(items))
	for id := range items {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	code := 0
	for _, id := range ids {
		sources := items[id]
		slices.SortFunc(sources, func(a, b json.RawMessage) int { return bytes.Compare(a, b) })
		view := checkinView{ID: id, Status: "unbound", DryRun: true, CheckerAuthority: checkerAuthority, Sources: sources, Reasons: []string{}}
		if len(sources) > 1 || len(bound[id]) > 1 {
			view.Status = "conflict"
			view.Reasons = append(view.Reasons, "conflicting item or context for tracker id; no binding selected")
			code = 3
		} else {
			var item checkinItem
			_ = json.Unmarshal(sources[0], &item)
			view = planCheckin(view, item, bound[id], contexts.ObservedAt, partial)
			if view.Plan != nil && view.Plan.Status == "refused" && code != 3 {
				code = 2
			}
		}
		p.Items = append(p.Items, view)
	}
	if code == 3 {
		p.Status = "conflict"
	} else if code == 2 {
		p.Status = "refused"
	}
	return p, code
}

func planCheckin(view checkinView, item checkinItem, raw []json.RawMessage, observed string, partial bool) checkinView {
	if len(raw) == 0 {
		view.Reasons = append(view.Reasons, "no local context for tracker id")
		return view
	}
	var ctx checkinContext
	_ = json.Unmarshal(raw[0], &ctx)
	// Match the raw header, then let the existing reducer judge its contents.
	// A refused or ambiguous reducer result can never supply a picked binding.
	var doc document
	_ = json.Unmarshal(ctx.Handoff, &doc)
	if ctx.Owner == "" || ctx.Owner == "Unknown" || ctx.Owner != item.Owner || ctx.Story != strconv.Itoa(item.ID) || ctx.Story != doc.Story || ctx.Repository != doc.Repository || ctx.PullRequest != doc.PullRequest || ctx.Head != doc.Head || ctx.Coordinator != doc.Coordinator || doc.ReviewBinding == nil || ctx.Task != doc.ReviewBinding.Task || ctx.UID != doc.ReviewBinding.UID || !validToken(ctx.Task) || !validToken(ctx.UID) || !validHead(ctx.Head) || !validToken(ctx.Coordinator) {
		view.Reasons = append(view.Reasons, "exact owner/story/task/UID/repository/PR/head/coordinator binding does not match")
		return view
	}
	if ctx.PullRequest <= 0 {
		view.Reasons = append(view.Reasons, "pre-PR context remains unbound")
		return view
	}
	view.Bound = true
	if partial || item.HistoryComplete == nil || !*item.HistoryComplete {
		view.Status = "incomplete"
		view.Reasons = append(view.Reasons, "incomplete history or partial-page visibility; recommendations withheld")
		return view
	}
	ob := obligationIn{ID: "checkin-" + strconv.Itoa(item.ID), Kind: ctx.Kind, Owner: ctx.Coordinator, OwnerEscalationDueAt: ctx.OwnerEscalationDueAt}
	// Unknown dates remain unknown. No parsing of opwork reasons or status.
	if item.CheckAfter != nil && *item.CheckAfter != "Unknown" && *item.CheckAfter != "" {
		ob.DueAt = item.CheckAfter
	}
	obData, _ := json.Marshal(ob)
	wrapper := watchdogDocument{Schema: watchdogSchema, ObservedAt: observed, Handoff: ctx.Handoff, Obligations: []json.RawMessage{obData}, Receipts: ctx.Receipts}
	data, _ := json.Marshal(wrapper)
	plan, _ := planWatchdog(data)
	view.Status = plan.Status
	view.Plan = &plan
	return view
}

func parseCheckins(data, contextData []byte) (checkinsExport, checkinContexts, json.RawMessage, error) {
	var export checkinsExport
	var contexts checkinContexts
	for _, raw := range [][]byte{data, contextData} {
		if len(raw) > maxDocumentBytes {
			return export, contexts, nil, errors.New("document exceeds 128KiB")
		}
		if err := rejectDuplicateKeys(raw); err != nil {
			return export, contexts, nil, err
		}
	}
	// Unknown export fields are retained, not rejected. Local contexts are strict.
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return export, contexts, nil, errors.New("export must be a JSON object")
	}
	if err := rejectFoldedKeys(root, []string{"page", "page_size", "has_more", "owner_filter_scope", "owner_match_total", "total_before_owner_filter", "items"}); err != nil {
		return export, contexts, nil, err
	}
	if err := rejectFoldedElements(root["items"], []string{"id", "owner", "check_after", "reasons", "history_complete"}); err != nil {
		return export, contexts, nil, err
	}
	if err := json.Unmarshal(data, &export); err != nil {
		return export, contexts, nil, errors.New("invalid check-ins export")
	}
	if export.Page < 1 || export.PageSize < 1 || export.HasMore == nil || export.OwnerFilterScope == "" || len(export.OwnerMatchTotal) == 0 || len(export.TotalBeforeOwnerFilter) == 0 || export.Items == nil || len(export.Items) > maxEvents {
		return export, contexts, nil, errors.New("export requires pagination/visibility fields and at most 48 items")
	}
	if err := decodeStrict(contextData, &contexts); err != nil {
		return export, contexts, nil, publicJSONError(err)
	}
	var cr map[string]json.RawMessage
	_ = json.Unmarshal(contextData, &cr)
	if err := rejectFoldedKeys(cr, []string{"schema", "observedAt", "contexts"}); err != nil {
		return export, contexts, nil, err
	}
	if contexts.Schema != contextsSchema || contexts.Contexts == nil || len(contexts.Contexts) > maxEvents {
		return export, contexts, nil, errors.New("contexts schema/list invalid (maximum 48)")
	}
	at, err := parseInstant("observedAt", contexts.ObservedAt)
	if err != nil {
		return export, contexts, nil, err
	}
	contexts.ObservedAt = instantKey(at)
	delete(root, "items")
	vis, _ := json.Marshal(root)
	return export, contexts, appendDistinctJSON(nil, vis)[0], nil
}

func decodeCheckinContext(raw json.RawMessage, ctx *checkinContext) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return errors.New("context must be an object")
	}
	if err := rejectFoldedKeys(obj, []string{"id", "owner", "story", "repository", "pullRequest", "head", "coordinator", "task", "uid", "kind", "handoff", "receipts", "ownerEscalationDueAt"}); err != nil {
		return err
	}
	return decodeStrict(raw, ctx)
}

func appendDistinctJSON(items []json.RawMessage, raw json.RawMessage) []json.RawMessage {
	// Canonical map ordering and number preservation make object key order irrelevant.
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&value)
	canonical, _ := json.Marshal(value)
	for _, prev := range items {
		if bytes.Equal(prev, canonical) {
			return items
		}
	}
	return append(items, canonical)
}
