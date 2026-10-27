package session

import (
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// The portal's operator identity may list session summaries only
// (internal/apiserver/server.go), and no operator route takes a message from a
// portal user: POST /v1/sessions/{name}/messages sends as its operator caller.
// So the message box reads and sends nothing, and says so.
const (
	messagesHistory = "Message history is unavailable here. The portal cannot read this session's messages or inbox."
	messagesGate    = "Sending from the portal is unavailable. The portal reads session summaries only, and no operator route accepts a message from a portal user. Nothing is sent or queued."
)

// sessionMessages is a session's message box, from the summary fields the
// portal already reads. Route is what agentd's delivery (D-16, D-65) would do
// with a message from the existing CLI route: a headless task reads none, an
// agent that is not running takes none, and a running interactive session may
// take one. Delivery is decided when a message is sent, so even that state
// makes no promise. Only a session the route may reach gets a draft box.
func sessionMessages(row apiv1.PortalSessionSummary, a *apiv1.PortalSessionActivity) *portal.ProductMessages {
	provider, model := shown(a.Provider), shown(a.RequestedModel)
	if provider == protocol.UsageUnknown {
		provider = "provider Unknown"
	}
	if model == protocol.UsageUnknown {
		model = "model Unknown"
	}
	m := &portal.ProductMessages{Agent: provider + " · " + model, History: messagesHistory, Gate: messagesGate, Session: row.Name, SessionUID: row.UID}
	switch {
	case a.Mode == "task":
		m.Route = portal.ProductStatus{Text: "Takes no messages", Tone: "muted"}
		m.RouteNote = "A headless task reads no messages. It reports through its log and its PR."
	case a.Mode != "remote" && a.Mode != "local":
		m.Route = portal.ProductStatus{Text: "Route Unknown", Tone: "muted"}
		m.RouteNote = "How this session runs is Unknown, so whether it takes messages is Unknown."
	case row.Phase != "Running":
		m.Route = portal.ProductStatus{Text: "Not running", Tone: "muted"}
		m.RouteNote = "The session is " + shown(row.Phase) + ". agentd takes no message while its agent is not running."
	case oneOf(a.AgentState, protocol.AgentBusy, protocol.AgentWaiting, protocol.AgentIdle):
		m.Draftable = true
		m.Placeholder = "Draft a follow-up for " + shown(row.Name) + "…"
		m.Route = portal.ProductStatus{Text: "CLI route", Tone: "info"}
		m.RouteNote = "The existing route runs from a session or the workbench. agentd decides delivery when it is sent; this page cannot observe it:"
		m.Command = "agent-run msg " + shown(row.Name) + ` "<text>"`
	case a.AgentState == protocol.AgentPending:
		m.Route = portal.ProductStatus{Text: "Starting", Tone: "muted"}
		m.RouteNote = "agentd takes no message until the agent starts."
	case oneOf(a.AgentState, protocol.AgentExited, protocol.AgentFailed, protocol.AgentInterrupted):
		m.Route = portal.ProductStatus{Text: "Not running", Tone: "muted"}
		m.RouteNote = "The agent is " + a.AgentState + ". agentd takes no message while it is not running."
	default:
		m.Route = portal.ProductStatus{Text: "Route Unknown", Tone: "muted"}
		m.RouteNote = "The agent's state is Unknown, so whether it takes messages is Unknown."
	}
	if !m.Draftable {
		m.Placeholder = m.Route.Text + ". Drafting is off for this session."
	}
	return m
}
