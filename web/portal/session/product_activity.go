package session

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// sessionUsageNote states the Sessions view's usage coverage on every connected read.
const sessionUsageNote = "Usage is each task session's first managed invocation, as its provider reported it. " +
	"Interactive and remote sessions, later turns and account allowance are Unknown, not zero. " +
	"Cache read tokens can overlap input tokens and are not added to them. " +
	"Cost is a CLI-equivalent list-price estimate, not a charge."

// decodeSessionActivity accepts exactly the operator's view=activity object.
// lastActivity is the one optional key; a null, an unknown key or a word outside
// its fixed set makes the whole read unavailable. Usage covers task sessions
// only, so any other mode must carry an Unknown usage scope, which in turn
// carries no reported value.
func decodeSessionActivity(raw json.RawMessage, now time.Time) (apiv1.PortalSessionActivity, error) {
	var a apiv1.PortalSessionActivity
	obj, err := jsonObject(raw)
	if err != nil {
		return a, err
	}
	if !exactKeys(obj, []string{"mode", "agentState", "requestedModel", "usage"}, "lastActivity") {
		return a, portal.ErrUnavailable
	}
	mode, ok1 := jsonString(obj["mode"])
	state, ok2 := jsonString(obj["agentState"])
	model, ok3 := jsonString(obj["requestedModel"])
	if !ok1 || !ok2 || !ok3 || !oneOf(mode, "task", "local", "remote", protocol.UsageUnknown) ||
		!oneOf(state, protocol.AgentPending, protocol.AgentBusy, protocol.AgentIdle, protocol.AgentWaiting, protocol.AgentExited, protocol.AgentFailed, protocol.AgentInterrupted, protocol.UsageUnknown) ||
		(model != protocol.UsageUnknown && !protocol.ValidUsageModelID(model)) {
		return a, portal.ErrUnavailable
	}
	if rawTime, ok := obj["lastActivity"]; ok {
		at, err := observationTime(rawTime, now)
		if err != nil {
			return a, err
		}
		a.LastActivity = &at
	}
	usage, err := decodeSessionUsage(obj["usage"])
	if err != nil {
		return a, err
	}
	if mode != "task" && usage.Scope != protocol.UsageUnknown {
		return a, portal.ErrUnavailable
	}
	a.Mode, a.AgentState, a.RequestedModel, a.Usage = mode, state, model, usage
	return a, nil
}

// decodeSessionUsage keeps Unknown distinct from an explicit zero. An Unknown
// scope must carry no reported value.
func decodeSessionUsage(raw json.RawMessage) (apiv1.PortalSessionUsage, error) {
	var u apiv1.PortalSessionUsage
	obj, err := jsonObject(raw)
	if err != nil {
		return u, err
	}
	if !exactKeys(obj, []string{"scope", "resultState", "servedModels", "inputTokens", "outputTokens", "cacheReadInputTokens", "cacheCreationInputTokens", "cost"}) {
		return u, portal.ErrUnavailable
	}
	scope, ok1 := jsonString(obj["scope"])
	result, ok2 := jsonString(obj["resultState"])
	if !ok1 || !ok2 || !oneOf(scope, protocol.UsageInitial, protocol.UsageUnknown) || !oneOf(result, "Final", "Partial", protocol.UsageUnknown) {
		return u, portal.ErrUnavailable
	}
	models, ok := jsonArray(obj["servedModels"])
	if !ok || len(models) > protocol.MaxServedModels {
		return u, portal.ErrUnavailable
	}
	u.ServedModels = make([]string, 0, len(models))
	seen := map[string]struct{}{}
	for _, raw := range models {
		model, ok := jsonString(raw)
		if _, dup := seen[model]; !ok || dup || !protocol.ValidUsageModelID(model) {
			return u, portal.ErrUnavailable
		}
		seen[model] = struct{}{}
		u.ServedModels = append(u.ServedModels, model)
	}
	var okTokens [4]bool
	u.InputTokens, okTokens[0] = decodeTokens(obj["inputTokens"])
	u.OutputTokens, okTokens[1] = decodeTokens(obj["outputTokens"])
	u.CacheReadInputTokens, okTokens[2] = decodeTokens(obj["cacheReadInputTokens"])
	u.CacheCreationInputTokens, okTokens[3] = decodeTokens(obj["cacheCreationInputTokens"])
	cost, okCost := decodeCost(obj["cost"])
	if okTokens != [4]bool{true, true, true, true} || !okCost {
		return u, portal.ErrUnavailable
	}
	u.Scope, u.ResultState, u.Cost = scope, result, cost
	if scope == protocol.UsageUnknown {
		for _, m := range []protocol.TokenObservation{u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens} {
			if m.State != protocol.UsageUnknown {
				return u, portal.ErrUnavailable
			}
		}
		if result != protocol.UsageUnknown || cost.State != protocol.UsageUnknown || len(u.ServedModels) != 0 {
			return u, portal.ErrUnavailable
		}
	} else if result == protocol.UsageUnknown {
		return u, portal.ErrUnavailable
	}
	return u, nil
}

func decodeTokens(raw json.RawMessage) (protocol.TokenObservation, bool) {
	var m protocol.TokenObservation
	obj, err := jsonObject(raw)
	if err != nil || !exactKeys(obj, []string{"state"}, "amount") {
		return m, false
	}
	state, ok := jsonString(obj["state"])
	if !ok {
		return m, false
	}
	m.State = state
	if rawAmount, present := obj["amount"]; present {
		n, ok := jsonInt(rawAmount)
		if !ok {
			return m, false
		}
		amount := int64(n)
		m.Amount = &amount
	}
	return m, m.Valid()
}

func decodeCost(raw json.RawMessage) (protocol.CostObservation, bool) {
	var c protocol.CostObservation
	obj, err := jsonObject(raw)
	if err != nil || !exactKeys(obj, []string{"state", "kind"}, "amount", "unit") {
		return c, false
	}
	var ok [4]bool
	c.State, ok[0] = jsonString(obj["state"])
	c.Kind, ok[1] = jsonString(obj["kind"])
	ok[2], ok[3] = true, true
	if rawAmount, present := obj["amount"]; present {
		c.Amount, ok[2] = jsonString(rawAmount)
	}
	if rawUnit, present := obj["unit"]; present {
		c.Unit, ok[3] = jsonString(rawUnit)
	}
	return c, ok == [4]bool{true, true, true, true} && c.Valid()
}

// exactKeys reports whether obj has every required key and nothing beyond the optional ones.
func exactKeys(obj map[string]json.RawMessage, required []string, optional ...string) bool {
	for _, key := range required {
		if _, ok := obj[key]; !ok {
			return false
		}
	}
	for key := range obj {
		if !oneOf(key, required...) && !oneOf(key, optional...) {
			return false
		}
	}
	return true
}

// jsonString accepts one JSON string; null and every other type are refused.
func jsonString(raw json.RawMessage) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var value any
	if dec.Decode(&value) != nil {
		return "", false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

func oneOf(value string, set ...string) bool {
	for _, item := range set {
		if value == item {
			return true
		}
	}
	return false
}

// sessionRow renders one summary. Requested and served models stay separate,
// each counter keeps Unknown apart from zero, and nothing is summed. A row
// without activity renders every activity field Unknown.
func sessionRow(row apiv1.PortalSessionSummary) portal.ProductRow {
	a := row.Activity
	if a == nil {
		a = &apiv1.PortalSessionActivity{Mode: protocol.UsageUnknown, AgentState: protocol.UsageUnknown, RequestedModel: protocol.UsageUnknown, Usage: unknownSessionUsage()}
	}
	u := a.Usage
	return portal.ProductRow{Title: shown(row.Name), Fields: []portal.ProductField{
		{Label: "Phase", Value: shown(row.Phase)},
		{Label: "Agent state", Value: shown(a.AgentState)},
		{Label: "Last activity", Value: activityTime(a.LastActivity)},
		{Label: "Mode", Value: shown(a.Mode)},
		{Label: "Requested model", Value: shown(a.RequestedModel)},
		{Label: "Served model (provider reported)", Value: servedModelsText(u.ServedModels)},
		{Label: "Usage scope", Value: usageScopeText(u)},
		{Label: "Input tokens", Value: tokenText(u.InputTokens)},
		{Label: "Output tokens", Value: tokenText(u.OutputTokens)},
		{Label: "Cache read tokens", Value: tokenText(u.CacheReadInputTokens)},
		{Label: "Cache create tokens", Value: tokenText(u.CacheCreationInputTokens)},
		{Label: "Cost estimate", Value: costText(u.Cost)},
		{Label: "Freshness", Value: shown(row.Freshness)},
		{Label: "UID", Value: shown(row.UID)},
	}}
}

func activityTime(at *time.Time) string {
	if at == nil || at.IsZero() {
		return "Unknown"
	}
	return at.UTC().Format(time.RFC3339)
}

func servedModelsText(models []string) string {
	if len(models) == 0 {
		return "Unknown"
	}
	return strings.Join(models, ", ")
}

func usageScopeText(u apiv1.PortalSessionUsage) string {
	if u.Scope != protocol.UsageInitial {
		return "Unknown"
	}
	if u.ResultState == "Partial" {
		return "First managed invocation (partial result)"
	}
	return "First managed invocation"
}

func tokenText(m protocol.TokenObservation) string {
	if m.State != protocol.UsageReported || !m.Valid() {
		return "Unknown"
	}
	return groupDigits(strconv.FormatInt(*m.Amount, 10))
}

func costText(c protocol.CostObservation) string {
	if c.State != protocol.UsageReported || !c.Valid() {
		return "Unknown"
	}
	return c.Amount + " USD (CLI-equivalent estimate, not a charge)"
}

// groupDigits writes a non-negative decimal integer with thousands separators.
func groupDigits(digits string) string {
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// unknownSessionUsage is usage with nothing measured.
func unknownSessionUsage() apiv1.PortalSessionUsage {
	unknown := protocol.TokenObservation{State: protocol.UsageUnknown}
	return apiv1.PortalSessionUsage{Scope: protocol.UsageUnknown, ResultState: protocol.UsageUnknown, ServedModels: []string{},
		InputTokens: unknown, OutputTokens: unknown, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown,
		Cost: protocol.CostObservation{State: protocol.UsageUnknown, Kind: protocol.UsageUnknown}}
}
