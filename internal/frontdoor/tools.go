package frontdoor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// descriptionWordBound is the measured total of the tool descriptions.
// Each description is at most 30 words.
const descriptionWordBound = 142

// noStandingAuthority is the whole success result when no authority file is installed.
const noStandingAuthority = "No standing authority is installed for this caller. Ask the owner once, and record the answer on the work item."

// authorityMaxBytes is the largest authority file standing_authority will return.
const authorityMaxBytes = 4096

// tool descriptions stay at or under 30 words. Their total is descriptionWordBound.
var frontDoorTools = []map[string]any{
	// Read-only tracker search: a repeat writes nothing, deletes nothing, and does not leave the project.
	tool("backlog_find", "Find work items by text, status, or parent epic. Read only.", hints(true, false, true, false), map[string]any{
		"text":   map[string]any{"type": "string"},
		"status": map[string]any{"type": "string"},
		"epic":   map[string]any{"type": "integer", "minimum": 1},
	}, nil),
	// Read-only item read: a repeat writes nothing, deletes nothing, and does not leave the project.
	tool("backlog_show", "Read one work item, including scope, acceptance, and constraints.", hints(true, false, true, false), map[string]any{
		"id": map[string]any{"type": "integer", "minimum": 1},
	}, []string{"id"}),
	// Creates a kept item. A repeat with the same epic and title reuses that source key and writes nothing when the text still matches.
	tool("backlog_create", "Create a story under an epic. Search first. The write is attributed to meatball, not Tom.", hints(false, false, true, false), map[string]any{
		"epic":       map[string]any{"type": "integer", "minimum": 1},
		"title":      map[string]any{"type": "string"},
		"scope":      map[string]any{"type": "string"},
		"acceptance": map[string]any{"type": "string"},
	}, []string{"epic", "title", "scope"}),
	// Sets field text. opwork groom patches and adds a journal note on every call, so a repeat is not the same state. Nothing is deleted.
	tool("backlog_groom", "Replace an item's scope, acceptance, or constraints, then read the item back.", hints(false, false, false, false), map[string]any{
		"id": map[string]any{"type": "integer", "minimum": 1},
		"fields": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"description": map[string]any{"type": "string"},
			"acceptance":  map[string]any{"type": "string"},
			"constraints": map[string]any{"type": "string"},
		}},
	}, []string{"id", "fields"}),
	// Sets priority. opwork order writes nothing when that priority already matches. Nothing is deleted.
	tool("backlog_order", "Set an item's priority, which decides what goes next, then read it back.", hints(false, false, true, false), map[string]any{
		"id":       map[string]any{"type": "integer", "minimum": 1},
		"priority": map[string]any{"type": "string"},
	}, []string{"id", "priority"}),
	// Closes an item and keeps it. opwork complete writes nothing once the item is already Accepted.
	tool("backlog_close", "Close an item with evidence. An explicit hold stays a hold.", hints(false, false, true, false), map[string]any{
		"id":       map[string]any{"type": "integer", "minimum": 1},
		"evidence": map[string]any{"type": "string"},
	}, []string{"id", "evidence"}),
	// Starts an agent that can reach the web. A repeat returns the retained session for coordinator-<item> and does not post another create while that record exists.
	tool("coordinator_start", "Start one coordinator for an item. Model is required and is the full id Tom named. No effort.", hints(false, false, true, true), map[string]any{
		"item":    map[string]any{"type": "integer", "minimum": 1},
		"repo":    map[string]any{"type": "string"},
		"project": map[string]any{"type": "string"},
		"model":   map[string]any{"type": "string"},
		"hours":   map[string]any{"type": "integer", "minimum": 1, "maximum": 4},
		"workers": map[string]any{"type": "integer", "minimum": 1, "maximum": 4},
	}, []string{"item", "model", "hours", "workers"}),
	// Read-only session read: a repeat writes nothing, deletes nothing, and talks only to the operator.
	tool("session_status", "Read one session this caller started, by name or item, including any retained outcome.", hints(true, false, true, false), map[string]any{
		"name": map[string]any{"type": "string"},
		"item": map[string]any{"type": "integer", "minimum": 1},
	}, nil),
	// Reaps a session. Clean-up then removes the task and keeps only rescued unpushed work. A second reap during that delete changes nothing.
	tool("session_stop", "Stop one session this caller started. A refusal from the operator is reported plainly.", hints(false, true, true, false), map[string]any{
		"name": map[string]any{"type": "string"},
	}, []string{"name"}),
	// Read-only list of Asks and held items: a repeat writes nothing, deletes nothing, and does not leave those systems.
	tool("waiting_on_tom", "List open Asks for this caller's sessions and items that need Tom's approval or answer.", hints(true, false, true, false), map[string]any{}, nil),
	// Read-only read of the configured authority file: a repeat writes nothing, deletes nothing, and reads no other path.
	tool("standing_authority", "Read the owner's standing instruction installed for this caller.", hints(true, false, true, false), map[string]any{}, nil),
}

// hints fills Tool.annotations. The four fields are ToolAnnotations in the MCP
// schema for protocol 2025-03-26 and for 2025-11-25. The names and the nesting
// under annotations are the same in both, and this server follows that object.
func hints(readOnly, destructive, idempotent, openWorld bool) map[string]any {
	return map[string]any{
		"readOnlyHint":    readOnly,
		"destructiveHint": destructive,
		"idempotentHint":  idempotent,
		"openWorldHint":   openWorld,
	}
}

func tool(name, description string, annotations, properties map[string]any, required []string) map[string]any {
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if required != nil {
		schema["required"] = required
	}
	return map[string]any{"name": name, "description": description, "inputSchema": schema, "annotations": annotations}
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
	case "standing_authority":
		return h.standingAuthority(args)
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

func (h *handler) standingAuthority(args map[string]any) (string, error) {
	if err := only(args); err != nil {
		return "", err
	}
	return readStandingAuthority(h.authorityFile)
}

// readStandingAuthority reads the file named by the process flag. The path is
// never taken from the tool call. A larger file is refused whole, not truncated.
func readStandingAuthority(path string) (string, error) {
	if path == "" {
		return noStandingAuthority, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return noStandingAuthority, nil
		}
		return "", errors.New("the standing authority file cannot be read")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("the standing authority file cannot be read")
	}
	if info.Size() > authorityMaxBytes {
		return "", errors.New("the standing authority file is larger than 4096 bytes")
	}
	b, err := io.ReadAll(io.LimitReader(f, authorityMaxBytes+1))
	if err != nil {
		return "", errors.New("the standing authority file cannot be read")
	}
	if len(b) > authorityMaxBytes {
		return "", errors.New("the standing authority file is larger than 4096 bytes")
	}
	if len(b) == 0 {
		return noStandingAuthority, nil
	}
	if !utf8.Valid(b) {
		return "", errors.New("the standing authority file is not UTF-8 text")
	}
	for _, r := range string(b) {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return "", errors.New("the standing authority file contains a control character")
		}
	}
	return string(b), nil
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
