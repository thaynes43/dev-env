package frontdoor

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/commonrules"
)

const (
	catalogURI       = "dev-env://catalog/v1"
	rulesURI         = "dev-env://operating-rules/v1"
	handoffURI       = "dev-env://handoff/v1"
	handoffPrefix    = handoffURI + "/"
	handoffTemplate  = handoffPrefix + "{item}"
	rulesRevision    = "2026-11-23.1"
	markdownMIME     = "text/markdown"
	jsonMIME         = "application/json"
	invalidParams    = -32602
	resourceNotFound = -32002
	resourceRefused  = -32003
	resourceDown     = -32004
	resourceBusy     = -32005
)

//go:embed catalog.md
var catalogText string

//go:embed operating-rules.md
var rulesText string

const handoffIndex = "Read dev-env://handoff/v1/{item} with resources/read. Replace {item} with a positive OpenProject item ID. The service checks its configured tracker project. The handoff includes tracker state, recorded owner, journal references and pickup guidance. Verify custody and action-specific authority before acting."

var resourceURIPattern = regexp.MustCompile(`^dev-env://[a-z][a-z0-9-]*(/[a-zA-Z0-9-]+)+$`)

type resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MIMEType    string `json:"mimeType"`
}

var resources = []resource{
	{catalogURI, "Dot's catalog and runbook", "Tools, resource parameters, operating limits and acceptance checks.", markdownMIME},
	{rulesURI, "Operating rules", "Packaged operating rules with version, revision and content digest.", jsonMIME},
	{handoffURI, "Work-item handoff", "Lookup instructions and scope for a durable tracker handoff.", markdownMIME},
}

func listResources(method string, raw json.RawMessage) (any, *rpcError) {
	if len(raw) > 0 {
		var params map[string]json.RawMessage
		if json.Unmarshal(raw, &params) != nil || params == nil || len(params) != 0 {
			return nil, &rpcError{invalidParams, "invalid resource list parameters"}
		}
	}
	if method == "resources/templates/list" {
		return map[string]any{"resourceTemplates": []map[string]string{{
			"uriTemplate": handoffTemplate,
			"name":        "Work-item handoff",
			"description": "Read the current handoff for one work item in the configured tracker project.",
			"mimeType":    jsonMIME,
		}}}, nil
	}
	return map[string]any{"resources": resources}, nil
}

func (h *handler) readResource(ctx context.Context, raw json.RawMessage, bearer string) (result any, rpcErr *rpcError) {
	label := "unknown"
	defer func() {
		h.log.Printf("rpc resources/read %s %s", label, resourceOutcome(rpcErr))
	}()
	uri, item, err := resourceTarget(raw)
	if err != nil {
		return nil, err
	}
	switch uri {
	case catalogURI:
		label = "catalog"
	case rulesURI:
		label = "operating-rules"
	case handoffURI:
		label = "handoff-index"
	default:
		if item == 0 {
			return nil, &rpcError{resourceNotFound, "resource not found"}
		}
		label = "handoff"
	}
	if !h.admit() {
		return nil, &rpcError{resourceBusy, "busy, try again"}
	}
	defer h.release()
	ctx, cancel := context.WithTimeout(ctx, toolDeadline)
	defer cancel()
	switch label {
	case "catalog":
		return resourceText(uri, markdownMIME, scrub(catalogText, bearer)), nil
	case "operating-rules":
		text := scrub(rulesText, bearer)
		rules := struct {
			Schema   string `json:"schema"`
			Version  int    `json:"version"`
			Revision string `json:"revision"`
			SHA256   string `json:"sha256"`
			Text     string `json:"text"`
		}{"dev-env.operating-rules", 1, rulesRevision, commonrules.Digest([]byte(text)), text}
		data, err := json.Marshal(rules)
		if err != nil {
			return nil, &rpcError{-32603, "internal error"}
		}
		return resourceText(uri, jsonMIME, string(data)), nil
	case "handoff-index":
		return resourceText(uri, markdownMIME, scrub(handoffIndex, bearer)), nil
	default:
		return h.readHandoff(ctx, uri, item, bearer)
	}
}

func resourceTarget(raw json.RawMessage) (string, int64, *rpcError) {
	invalid := &rpcError{invalidParams, "invalid resource URI"}
	var params map[string]json.RawMessage
	if json.Unmarshal(raw, &params) != nil || len(params) != 1 {
		return "", 0, invalid
	}
	var uri string
	if json.Unmarshal(params["uri"], &uri) != nil || len(uri) > 256 || !resourceURIPattern.MatchString(uri) {
		return "", 0, invalid
	}
	id, isHandoff := strings.CutPrefix(uri, handoffPrefix)
	if !isHandoff {
		return uri, 0, nil
	}
	item, err := strconv.ParseInt(id, 10, 64)
	if err != nil || item < 1 || strconv.FormatInt(item, 10) != id {
		return "", 0, invalid
	}
	return uri, item, nil
}

func resourceOutcome(err *rpcError) string {
	if err == nil {
		return "ok"
	}
	switch err.Code {
	case invalidParams:
		return "invalid"
	case resourceNotFound:
		return "missing"
	case resourceRefused:
		return "refused"
	case resourceBusy:
		return "busy"
	default:
		return "unavailable"
	}
}

func resourceText(uri, mimeType, text string) map[string]any {
	return map[string]any{"contents": []map[string]string{{"uri": uri, "mimeType": mimeType, "text": text}}}
}

// readHandoff uses opwork's scoped, metadata-only JSON mode. Its fixed exit
// codes distinguish project/auth refusal, absence, and source failure.
func (h *handler) readHandoff(ctx context.Context, uri string, item int64, bearer string) (any, *rpcError) {
	text, err := h.work.call(ctx, "show", strconv.FormatInt(item, 10), "--handoff-json")
	unavailable := &rpcError{resourceDown, "handoff tracker unavailable"}
	if err != nil {
		var failure *opworkFailure
		if errors.As(err, &failure) {
			switch failure.code {
			case 3:
				return nil, &rpcError{resourceRefused, "handoff read refused"}
			case 4:
				return nil, &rpcError{resourceNotFound, "resource not found"}
			}
		}
		return nil, unavailable
	}
	var envelope struct {
		Schema  string `json:"schema"`
		Version int    `json:"version"`
		Owner   string `json:"owner"`
		Source  struct {
			System      string    `json:"system"`
			URL         string    `json:"url"`
			LockVersion *int64    `json:"lockVersion"`
			UpdatedAt   time.Time `json:"updatedAt"`
			Checkpoint  string    `json:"checkpoint"`
		} `json:"source"`
		TargetScope struct {
			Project string `json:"project"`
			Item    int64  `json:"item"`
		} `json:"targetScope"`
		WorkState struct {
			Status string          `json:"status"`
			Hold   json.RawMessage `json:"hold"`
		} `json:"workState"`
		Evidence []struct {
			Kind       string    `json:"kind"`
			ActivityID int64     `json:"activityId"`
			CreatedAt  time.Time `json:"createdAt"`
		} `json:"evidence"`
		NextAction string    `json:"nextAction"`
		CapturedAt time.Time `json:"capturedAt"`
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if !json.Valid([]byte(text)) || decoder.Decode(&envelope) != nil || envelope.Schema != "dev-env.work-item-handoff" ||
		envelope.Version != 1 || envelope.TargetScope.Item != item || envelope.TargetScope.Project != h.work.project ||
		envelope.Owner == "" || envelope.Source.System != "OpenProject" || envelope.Source.URL == "" ||
		envelope.Source.LockVersion == nil || *envelope.Source.LockVersion < 0 || envelope.Source.UpdatedAt.IsZero() ||
		envelope.Source.Checkpoint == "" || envelope.WorkState.Status == "" || envelope.Evidence == nil ||
		envelope.NextAction == "" || envelope.CapturedAt.IsZero() || scrub(text, bearer) != text {
		return nil, unavailable
	}
	switch string(envelope.WorkState.Hold) {
	case "true", "false", "null":
	default:
		return nil, unavailable
	}
	for _, ref := range envelope.Evidence {
		if ref.ActivityID < 1 || ref.CreatedAt.IsZero() || (ref.Kind != "structured-checkpoint" && ref.Kind != "visible-journal") {
			return nil, unavailable
		}
	}
	return resourceText(uri, jsonMIME, text), nil
}
