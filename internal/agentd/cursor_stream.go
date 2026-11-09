package agentd

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// cursorEvent is the part of a Cursor CLI stream-json event agentd reads
// (`agent -p --output-format stream-json`,
// https://cursor.com/docs/cli/reference/output-format). Unknown events and
// fields are ignored, as that page asks of a consumer.
type cursorEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	Model     string `json:"model"`
	SessionID string `json:"session_id"`
	Message   *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	ToolCall map[string]json.RawMessage `json:"tool_call"`
	// The result event. Both are pointers: an absent field is not a value.
	IsError *bool   `json:"is_error"`
	Result  *string `json:"result"`
}

// cursorStream reads one Cursor task's stdout. It is its own parser, not a
// Claude parser with exceptions: Cursor's events, tools and result differ.
type cursorStream struct {
	// result is set by the first result event; a second one is a conflict.
	result bool
}

// cursorSubtype bounds a result subtype before it reaches the task result.
var cursorSubtype = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// line writes a readable form of one stream-json line to w and records the
// result event in res. Cursor reports no usage, cost, turn count or served
// model, so res carries an all-Unknown observation: never an estimate. The
// init event's model is what the CLI says it will use, never a provider
// report of what served the task.
func (c *cursorStream) line(line []byte, w io.Writer, res *streamResult) {
	if len(line) == 0 {
		return
	}
	var ev cursorEvent
	if err := json.Unmarshal(line, &ev); err != nil || ev.Type == "" {
		_, _ = fmt.Fprintf(w, "%s\n", line)
		return
	}
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			_, _ = fmt.Fprintf(w, "[agentd] cursor session %s, CLI-reported model %s; actual model Unknown\n", cursorField(ev.SessionID, 128), cursorField(ev.Model, 128))
		}
	case "assistant":
		if ev.Message == nil {
			return
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(ev.Message.Content, &blocks) != nil {
			return
		}
		for _, b := range blocks {
			if t := strings.TrimSpace(b.Text); b.Type == "text" && t != "" {
				_, _ = fmt.Fprintf(w, "%s\n", t)
			}
		}
	case "tool_call":
		if ev.Subtype == "started" {
			_, _ = fmt.Fprintf(w, "-> %s\n", cursorToolLine(ev.ToolCall))
		}
	case "result":
		if c.result {
			// A replayed or second result never replaces the first.
			res.UsageConflict = true
			return
		}
		c.result = true
		subtype := ev.Subtype
		if !cursorSubtype.MatchString(subtype) {
			subtype = "unrecognized"
		}
		// The page documents is_error false on success and nothing else, so
		// an absent flag is not read as success.
		isError := ev.IsError == nil || *ev.IsError
		observation := protocol.UnknownUsage()
		observation.ResultState = "Final"
		if isError {
			observation.ResultState = "Partial"
		}
		res.Seen, res.Subtype, res.IsError, res.NumTurns, res.Observation = true, subtype, isError, 0, observation
		res.Usage = protocol.Usage{}
		_, _ = fmt.Fprintf(w, "[agentd] result %s, is_error %t; usage, cost and served model Unknown (Cursor reports none)\n", subtype, isError)
		if isError && ev.Result != nil {
			if t := strings.TrimSpace(*ev.Result); t != "" {
				_, _ = fmt.Fprintf(w, "[agentd] Cursor's result: %s\n", cursorField(t, 2048))
			}
		}
	}
}

// cursorToolLine is one short line about a started tool call. The call is an
// object keyed by its tool kind (readToolCall, writeToolCall and so on), or a
// function object with a name; the line names the kind and the path or
// command when there is one.
func cursorToolLine(call map[string]json.RawMessage) string {
	kinds := make([]string, 0, len(call))
	for k := range call {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		var body struct {
			Name string         `json:"name"`
			Args map[string]any `json:"args"`
		}
		if json.Unmarshal(call[kind], &body) != nil {
			continue
		}
		name := strings.TrimSuffix(kind, "ToolCall")
		if kind == "function" && body.Name != "" {
			name = body.Name
		}
		for _, k := range []string{"command", "path", "pattern", "url", "query"} {
			if v, ok := body.Args[k].(string); ok && v != "" {
				return cursorField(name, 64) + ": " + cursorField(strings.Join(strings.Fields(v), " "), 160)
			}
		}
		return cursorField(name, 64)
	}
	return "tool call"
}

// cursorField makes a CLI-supplied string safe for one log line: no control
// characters, at most max bytes on a rune boundary.
func cursorField(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
