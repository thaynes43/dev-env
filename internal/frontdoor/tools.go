package frontdoor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// tool descriptions stay at or under 30 words. Their total stays under 300.
var frontDoorTools = []map[string]any{
	tool("backlog_find", "Find work items by text, status, or parent epic. Read only.", map[string]any{
		"text":   map[string]any{"type": "string"},
		"status": map[string]any{"type": "string"},
		"epic":   map[string]any{"type": "integer", "minimum": 1},
	}, nil),
	tool("backlog_show", "Read one work item, including scope, acceptance, and constraints.", map[string]any{
		"id": map[string]any{"type": "integer", "minimum": 1},
	}, []string{"id"}),
	tool("backlog_create", "Create a story under an epic. Search first. The write is attributed to meatball, not Tom.", map[string]any{
		"epic":       map[string]any{"type": "integer", "minimum": 1},
		"title":      map[string]any{"type": "string"},
		"scope":      map[string]any{"type": "string"},
		"acceptance": map[string]any{"type": "string"},
	}, []string{"epic", "title", "scope"}),
	tool("backlog_groom", "Replace an item's scope, acceptance, or constraints, then read the item back.", map[string]any{
		"id": map[string]any{"type": "integer", "minimum": 1},
		"fields": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"description": map[string]any{"type": "string"},
			"acceptance":  map[string]any{"type": "string"},
			"constraints": map[string]any{"type": "string"},
		}},
	}, []string{"id", "fields"}),
	tool("backlog_order", "Set an item's priority, which decides what goes next, then read it back.", map[string]any{
		"id":       map[string]any{"type": "integer", "minimum": 1},
		"priority": map[string]any{"type": "string"},
	}, []string{"id", "priority"}),
	tool("backlog_close", "Close an item with evidence. An explicit hold stays a hold.", map[string]any{
		"id":       map[string]any{"type": "integer", "minimum": 1},
		"evidence": map[string]any{"type": "string"},
	}, []string{"id", "evidence"}),
	tool("coordinator_start", "Start one coordinator for an item. Model is required and is the full id Tom named. No effort.", map[string]any{
		"item":    map[string]any{"type": "integer", "minimum": 1},
		"repo":    map[string]any{"type": "string"},
		"project": map[string]any{"type": "string"},
		"model":   map[string]any{"type": "string"},
		"hours":   map[string]any{"type": "integer", "minimum": 1, "maximum": 4},
		"workers": map[string]any{"type": "integer", "minimum": 1, "maximum": 4},
	}, []string{"item", "model", "hours", "workers"}),
	tool("session_status", "Read one session this caller started, by name or item, including any retained outcome.", map[string]any{
		"name": map[string]any{"type": "string"},
		"item": map[string]any{"type": "integer", "minimum": 1},
	}, nil),
	tool("session_stop", "Stop one session this caller started. A refusal from the operator is reported plainly.", map[string]any{
		"name": map[string]any{"type": "string"},
	}, []string{"name"}),
	tool("waiting_on_tom", "List open Asks for this caller's sessions and items that need Tom's approval or answer.", map[string]any{}, nil),
}

func tool(name, description string, properties map[string]any, required []string) map[string]any {
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if required != nil {
		schema["required"] = required
	}
	return map[string]any{"name": name, "description": description, "inputSchema": schema}
}

func (h *handler) callTool(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	args, err := decodeArgs(raw)
	if err != nil {
		return "", err
	}
	switch name {
	case "backlog_find":
		return h.backlogFind(ctx, args)
	case "backlog_show":
		return h.backlogShow(ctx, args)
	case "backlog_create":
		return h.backlogCreate(ctx, args)
	case "backlog_groom":
		return h.backlogGroom(ctx, args)
	case "backlog_order":
		return h.backlogOrder(ctx, args)
	case "backlog_close":
		return h.backlogClose(ctx, args)
	case "coordinator_start":
		return h.coordinatorStart(ctx, args)
	case "session_status":
		return h.sessionStatus(ctx, args)
	case "session_stop":
		return h.sessionStop(ctx, args)
	case "waiting_on_tom":
		return h.waitingOnTom(ctx, args)
	default:
		return "", fmt.Errorf("unknown tool %s", name)
	}
}

func (h *handler) backlogFind(ctx context.Context, args map[string]any) (string, error) {
	if err := only(args, "text", "status", "epic"); err != nil {
		return "", err
	}
	text, _, err := strArg(args, "text")
	if err != nil {
		return "", err
	}
	status, _, err := strArg(args, "status")
	if err != nil {
		return "", err
	}
	epic, _, err := intArg(args, "epic")
	if err != nil {
		return "", err
	}
	argv := []string{"search"}
	if status != "" {
		argv = append(argv, opt("status", status))
	}
	if epic > 0 {
		argv = append(argv, opt("parent", strconv.FormatInt(epic, 10)))
	}
	if strings.TrimSpace(text) != "" {
		argv = append(argv, "--", text)
	}
	return h.work.call(ctx, argv...)
}

func (h *handler) backlogShow(ctx context.Context, args map[string]any) (string, error) {
	if err := only(args, "id"); err != nil {
		return "", err
	}
	id, ok, err := intArg(args, "id")
	if err != nil || !ok {
		return "", need(err, "id")
	}
	return h.work.call(ctx, "show", strconv.FormatInt(id, 10))
}

func (h *handler) backlogCreate(ctx context.Context, args map[string]any) (string, error) {
	if err := only(args, "epic", "title", "scope", "acceptance"); err != nil {
		return "", err
	}
	epic, ok, err := intArg(args, "epic")
	if err != nil || !ok {
		return "", need(err, "epic")
	}
	title, ok, err := strArg(args, "title")
	if err != nil || !ok || strings.TrimSpace(title) == "" {
		return "", need(err, "title")
	}
	scope, ok, err := strArg(args, "scope")
	if err != nil || !ok || strings.TrimSpace(scope) == "" {
		return "", need(err, "scope")
	}
	acceptance, _, err := strArg(args, "acceptance")
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(title, "\r\n") || len(title) > 255 || len(scope) > maxField || len(acceptance) > maxField {
		return "", errors.New("title, scope or acceptance is empty, too long, or title spans lines")
	}
	sum := sha256.Sum256([]byte(strconv.FormatInt(epic, 10) + "\n" + title))
	argv := []string{"create", opt("epic", strconv.FormatInt(epic, 10)), opt("subject", title), opt("source-key", fmt.Sprintf("meatball-%d-%x", epic, sum[:8])), opt("description", scope)}
	if strings.TrimSpace(acceptance) != "" {
		argv = append(argv, opt("acceptance", acceptance))
	}
	return h.work.call(ctx, argv...)
}

func (h *handler) backlogGroom(ctx context.Context, args map[string]any) (string, error) {
	if err := only(args, "id", "fields"); err != nil {
		return "", err
	}
	id, ok, err := intArg(args, "id")
	if err != nil || !ok {
		return "", need(err, "id")
	}
	fields, ok, err := objectArg(args, "fields")
	if err != nil || !ok {
		return "", need(err, "fields")
	}
	if err := only(fields, "description", "acceptance", "constraints"); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "frontdoor-")
	if err != nil {
		return "", errors.New("could not store the groom text")
	}
	defer func() { _ = os.RemoveAll(dir) }()
	argv := []string{"groom", strconv.FormatInt(id, 10)}
	wrote := 0
	for _, name := range []string{"description", "acceptance", "constraints"} {
		text, present, err := strArg(fields, name)
		if err != nil {
			return "", err
		}
		if !present {
			continue
		}
		if strings.TrimSpace(text) == "" || len(text) > maxField {
			return "", fmt.Errorf("%s is empty or too long", name)
		}
		path, err := writeField(dir, name, text)
		if err != nil {
			return "", errors.New("could not store the groom text")
		}
		argv = append(argv, opt(name, path))
		wrote++
	}
	if wrote == 0 {
		return "", errors.New("groom needs description, acceptance, or constraints")
	}
	return h.work.call(ctx, argv...)
}

func (h *handler) backlogOrder(ctx context.Context, args map[string]any) (string, error) {
	if err := only(args, "id", "priority"); err != nil {
		return "", err
	}
	id, ok, err := intArg(args, "id")
	if err != nil || !ok {
		return "", need(err, "id")
	}
	priority, ok, err := strArg(args, "priority")
	if err != nil || !ok || strings.TrimSpace(priority) == "" {
		return "", need(err, "priority")
	}
	return h.work.call(ctx, "order", strconv.FormatInt(id, 10), opt("priority", priority))
}

func (h *handler) backlogClose(ctx context.Context, args map[string]any) (string, error) {
	if err := only(args, "id", "evidence"); err != nil {
		return "", err
	}
	id, ok, err := intArg(args, "id")
	if err != nil || !ok {
		return "", need(err, "id")
	}
	evidence, ok, err := strArg(args, "evidence")
	if err != nil || !ok || strings.TrimSpace(evidence) == "" {
		return "", need(err, "evidence")
	}
	if len(evidence) > maxField {
		return "", errors.New("evidence is too long")
	}
	return h.work.call(ctx, "complete", strconv.FormatInt(id, 10), opt("evidence", evidence))
}

func (h *handler) coordinatorStart(ctx context.Context, args map[string]any) (string, error) {
	if _, ok := args["effort"]; ok {
		return "", errors.New("effort is not a setting; the model uses its preset")
	}
	if err := only(args, "item", "repo", "project", "model", "hours", "workers"); err != nil {
		return "", err
	}
	item, ok, err := intArg(args, "item")
	if err != nil || !ok {
		return "", need(err, "item")
	}
	repo, _, err := strArg(args, "repo")
	if err != nil {
		return "", err
	}
	project, _, err := strArg(args, "project")
	if err != nil {
		return "", err
	}
	model, _, err := strArg(args, "model")
	if err != nil {
		return "", err
	}
	hours, hoursOK, err := intArg(args, "hours")
	if err != nil || !hoursOK {
		return "", need(err, "hours")
	}
	workers, workersOK, err := intArg(args, "workers")
	if err != nil || !workersOK {
		return "", need(err, "workers")
	}
	// PrepareCoordinatorStart refuses a missing model before Execute talks to the operator.
	in := agentrun.CoordinatorStartInput{Item: item, Repo: repo, Project: project, Model: strings.TrimSpace(model), Hours: int(hours), Workers: int(workers)}
	out, err := agentrun.ExecuteCoordinatorStart(ctx, h.op, in)
	if err != nil {
		return "", plainRefusal(err)
	}
	return formatSession(out), nil
}

func (h *handler) sessionStatus(ctx context.Context, args map[string]any) (string, error) {
	if err := only(args, "name", "item"); err != nil {
		return "", err
	}
	name, hasName, err := strArg(args, "name")
	if err != nil {
		return "", err
	}
	item, hasItem, err := intArg(args, "item")
	if err != nil {
		return "", err
	}
	if hasName == hasItem {
		return "", errors.New("session_status takes a session name or an item, not both and not neither")
	}
	in := agentrun.CoordinatorStatusInput{Name: name}
	if hasItem {
		in.Item = item
	}
	out, err := agentrun.ExecuteCoordinatorStatus(ctx, h.op, in)
	if err != nil {
		return "", plainRefusal(err)
	}
	return formatSession(out), nil
}

func (h *handler) sessionStop(ctx context.Context, args map[string]any) (string, error) {
	if err := only(args, "name"); err != nil {
		return "", err
	}
	name, ok, err := strArg(args, "name")
	if err != nil || !ok || strings.TrimSpace(name) == "" {
		return "", need(err, "name")
	}
	sess, err := agentrun.ExecuteCoordinatorStop(ctx, h.op, name)
	if err != nil {
		return "", plainRefusal(err)
	}
	text := fmt.Sprintf("name: %s\nstate: %s\n", sess.Name, sess.Phase)
	if sess.Reaping {
		text += "reaping: true\n"
	}
	return text, nil
}

func (h *handler) waitingOnTom(ctx context.Context, args map[string]any) (string, error) {
	if err := only(args); err != nil {
		return "", err
	}
	var b strings.Builder
	asks, err := h.op.ListAsks(ctx)
	switch {
	case err == nil:
		b.WriteString(formatAsks(asks))
	case agentrun.OperatorRefused(err):
		b.WriteString("The operator has no route that lists open Asks for sessions this caller started.\n")
	default:
		fmt.Fprintf(&b, "The operator did not list Asks: %s\n", err.Error())
	}
	text, err := h.work.call(ctx, "search", "--status=Needs approval,Needs answer")
	if text != "" {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	if err != nil {
		return b.String(), err
	}
	return b.String(), nil
}

func plainRefusal(err error) error {
	if agentrun.OperatorRefused(err) {
		return fmt.Errorf("refused: %s", err.Error())
	}
	return err
}

func formatSession(out agentrun.CoordinatorOutcome) string {
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\nstate: %s\n", out.Session.Name, out.Session.Phase)
	if out.Item > 0 {
		fmt.Fprintf(&b, "item: https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/%d\n", out.Item)
	}
	if out.Session.Reaping {
		b.WriteString("reaping: true\n")
	}
	if v := out.Session.TaskVerdict(); v != nil {
		fmt.Fprintf(&b, "task: %s (%s): %s\n", v.State, v.Reason, v.Message)
	}
	if o := out.Session.TaskOutcome; o != nil && o.Validate() == nil {
		fmt.Fprintf(&b, "summary: %s\n", o.Record.Summary)
		for _, pr := range o.Record.PRURLs {
			fmt.Fprintf(&b, "PR: %s\n", pr)
		}
		for _, head := range o.Record.HeadSHAs {
			fmt.Fprintf(&b, "head: %s\n", head)
		}
	}
	return b.String()
}

func formatAsks(list apiv1.AskList) string {
	var b strings.Builder
	n := 0
	for _, ask := range list.Asks {
		if ask.State != apiv1.AskStateOpen {
			continue
		}
		n++
		question := strings.Join(strings.Fields(ask.Question), " ")
		if len(question) > 240 {
			question = question[:240] + "..."
		}
		fmt.Fprintf(&b, "ask %s session %s item %d: %s\n", ask.ID, ask.Context.SessionName, ask.Context.WorkPackage, question)
	}
	if n == 0 {
		b.WriteString("open asks: none\n")
	}
	if !list.Complete {
		b.WriteString("More asks may exist beyond this page.\n")
	}
	return b.String()
}

const maxField = 100_000

func opt(name, value string) string { return "--" + name + "=" + value }

func need(err error, name string) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("%s is required", name)
}

func decodeArgs(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, errors.New("arguments must be an object")
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

func only(args map[string]any, keys ...string) error {
	allow := make(map[string]bool, len(keys))
	for _, key := range keys {
		allow[key] = true
	}
	for key := range args {
		if !allow[key] {
			return fmt.Errorf("unknown argument %s", key)
		}
	}
	return nil
}

func strArg(args map[string]any, key string) (string, bool, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return "", false, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", true, fmt.Errorf("%s must be a string", key)
	}
	return text, true, nil
}

func intArg(args map[string]any, key string) (int64, bool, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return 0, false, nil
	}
	number, ok := value.(float64)
	if !ok || number != math.Trunc(number) || number < 1 || number > 1_000_000_000 {
		return 0, true, fmt.Errorf("%s must be a positive whole number", key)
	}
	return int64(number), true, nil
}

func objectArg(args map[string]any, key string) (map[string]any, bool, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return nil, false, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, true, fmt.Errorf("%s must be an object", key)
	}
	return object, true, nil
}

func writeField(dir, name, text string) (string, error) {
	f, err := os.CreateTemp(dir, name+"-")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return "", err
	}
	_, err = f.WriteString(text)
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	return path, closeErr
}
