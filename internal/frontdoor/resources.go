package frontdoor

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/commonrules"
	kjson "sigs.k8s.io/json"
)

const (
	catalogURI       = "dev-env://catalog/v1"
	rulesURI         = "dev-env://operating-rules/v1"
	handoffURI       = "dev-env://handoff/v1"
	handoffPrefix    = handoffURI + "/"
	handoffTemplate  = handoffPrefix + "{item}"
	rulesRevision    = "2026-11-23.2"
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

var handoffOwnerPattern = regexp.MustCompile(`^agent \| worktree ([a-z0-9][a-z0-9-]{0,79}) \| branch agent/([a-z0-9][a-z0-9-]{0,79})$`)

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
		if params := resourceParams(raw); params == nil || len(params) != 0 {
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

func resourceParams(raw json.RawMessage) map[string]json.RawMessage {
	var params map[string]json.RawMessage
	if json.Unmarshal(raw, &params) != nil {
		return nil
	}
	delete(params, "_meta")
	return params
}

func resourceTarget(raw json.RawMessage) (string, int64, *rpcError) {
	invalid := &rpcError{invalidParams, "invalid resource URI"}
	params := resourceParams(raw)
	if len(params) != 1 {
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
	id := strconv.FormatInt(item, 10)
	text, err := h.work.callStdout(ctx, "show", id, "--handoff-json")
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
	// childEnv supplies no OPWORK_BASE_URL, so opwork uses its public default.
	url := "https://work.haynesnetwork.com/projects/" + h.work.project + "/work_packages/" + id
	data := validatedHandoff(text, h.work.project, item, url, bearer)
	if data == nil {
		return nil, unavailable
	}
	return resourceText(uri, jsonMIME, string(data)), nil
}

func validatedHandoff(text, project string, item int64, url, bearer string) []byte {
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
	strict, err := kjson.UnmarshalStrict([]byte(text), &envelope)
	if err != nil || len(strict) != 0 || envelope.Schema != "dev-env.work-item-handoff" ||
		envelope.Version != 1 || envelope.TargetScope.Item != item || envelope.TargetScope.Project != project ||
		envelope.Source.System != "OpenProject" || envelope.Source.URL != url ||
		envelope.Source.LockVersion == nil || *envelope.Source.LockVersion < 0 || envelope.Source.UpdatedAt.IsZero() ||
		envelope.Evidence == nil || len(envelope.Evidence) > 2 || envelope.CapturedAt.IsZero() ||
		scrub(text, bearer) != text {
		return nil
	}
	if envelope.Owner != "Unknown" {
		parts := handoffOwnerPattern.FindStringSubmatch(envelope.Owner)
		if len(parts) != 3 || parts[1] != parts[2] {
			return nil
		}
	}
	// These sets mirror the metadata-only projection in opwork.py.
	if !slices.Contains([]string{"present", "absent", "malformed", "incomplete", "owner-unconfirmed", "blocked", "stale"}, envelope.Source.Checkpoint) ||
		!slices.Contains([]string{"Ungroomed", "Proposed", "Ready", "In progress", "In review", "Accepted", "Rejected", "Closed", "Cancelled", "Blocked", "Needs answer", "Needs approval", "Paused", "Unknown"}, envelope.WorkState.Status) ||
		!slices.Contains([]string{
			"Keep the recorded hold and confirm its disposition with the owner.",
			"Keep the recorded end state and use the item evidence for any authorized follow-up.",
			"Keep the work paused until its current state and next action are verified.",
			"A checkpoint carries the recorded owner's label. Its author is unverified. Confirm custody with the recorded owner before acting.",
			"Confirm an accountable owner and a durable checkpoint before pickup.",
			"Confirm custody and the next action with the recorded owner before pickup.",
		}, envelope.NextAction) {
		return nil
	}
	switch string(envelope.WorkState.Hold) {
	case "true", "false", "null":
	default:
		return nil
	}
	for _, ref := range envelope.Evidence {
		if ref.ActivityID < 1 || ref.CreatedAt.IsZero() || (ref.Kind != "structured-checkpoint" && ref.Kind != "visible-journal") {
			return nil
		}
	}
	data, err := json.Marshal(envelope)
	if err != nil || scrub(string(data), bearer) != string(data) {
		return nil
	}
	return data
}
