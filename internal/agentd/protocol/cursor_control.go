package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"time"
)

const MaxCursorControlReceiptBytes = 64 << 10

// CursorModelProviderRequested labels agent configuration metadata only.
// It never establishes which model served an inference result.
const CursorModelProviderRequested = "ProviderRequested"

var cursorControlLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var cursorControlRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var cursorControlAgent = regexp.MustCompile(`^bc-[A-Za-z0-9-]{1,100}$`)
var cursorControlRun = regexp.MustCompile(`^run-[A-Za-z0-9-]{1,100}$`)

// CursorBinding keeps API agent/run IDs separate from local ACP session IDs.
// Repository, cwd and manifest are caller pins, not provider tool attestations.
type CursorBinding struct {
	Surface            string `json:"surface"`
	AgentID            string `json:"agentID,omitempty"`
	RunID              string `json:"runID,omitempty"`
	LocalSessionID     string `json:"localSessionID,omitempty"`
	CWD                string `json:"cwd"`
	WorkerName         string `json:"workerName,omitempty"`
	WorkerID           string `json:"workerID,omitempty"`
	Repository         string `json:"repository"`
	ToolManifestSHA256 string `json:"toolManifestSHA256"`
}

type CursorControlProvenance struct {
	Source     string    `json:"source"`
	ReceiptID  string    `json:"receiptID"`
	ObservedAt time.Time `json:"observedAt"`
}

type CursorAPIAgentMetadata struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	LatestRunID string `json:"latestRunId"`
	Env         struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		WorkerID string `json:"workerId,omitempty"`
	} `json:"env"`
	WorkerID string `json:"workerId,omitempty"`
	Model    *struct {
		ID string `json:"id"`
	} `json:"model,omitempty"`
}

type CursorAPIRunMetadata struct {
	ID      string `json:"id"`
	AgentID string `json:"agentId"`
	Status  string `json:"status"`
}

// CursorAPIReadReceipt is a sanitized projection of the two already observed
// GETs. A future adapter must remove all prose/history/auth before using it.
type CursorAPIReadReceipt struct {
	Version         int                     `json:"version"`
	Provenance      CursorControlProvenance `json:"provenance"`
	AgentPath       string                  `json:"agentPath"`
	RunPath         string                  `json:"runPath"`
	AgentHTTPStatus int                     `json:"agentHTTPStatus"`
	RunHTTPStatus   int                     `json:"runHTTPStatus"`
	Agent           CursorAPIAgentMetadata  `json:"agent"`
	Run             CursorAPIRunMetadata    `json:"run"`
}

type CursorACPParams struct {
	ProtocolVersion int    `json:"protocolVersion,omitempty"`
	CWD             string `json:"cwd,omitempty"`
}

type CursorACPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  CursorACPParams `json:"params"`
}

type CursorACPCapabilities struct {
	LoadSession         bool `json:"loadSession"`
	SessionCapabilities struct {
		List *struct{} `json:"list,omitempty"`
	} `json:"sessionCapabilities"`
	MCPCapabilities struct {
		HTTP bool `json:"http"`
		SSE  bool `json:"sse"`
	} `json:"mcpCapabilities"`
}

type CursorACPInitializeResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Result  *struct {
		ProtocolVersion   int                   `json:"protocolVersion"`
		AgentCapabilities CursorACPCapabilities `json:"agentCapabilities"`
	} `json:"result"`
}

type CursorLocalSessionMetadata struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
}

type CursorACPListResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Result  *struct {
		Sessions   *[]CursorLocalSessionMetadata `json:"sessions"`
		NextCursor *string                       `json:"nextCursor,omitempty"`
	} `json:"result"`
}

type CursorACPReadReceipt struct {
	Version            int                         `json:"version"`
	Provenance         CursorControlProvenance     `json:"provenance"`
	InitializeRequest  CursorACPRequest            `json:"initializeRequest"`
	InitializeResponse CursorACPInitializeResponse `json:"initializeResponse"`
	ListRequest        CursorACPRequest            `json:"listRequest"`
	ListResponse       CursorACPListResponse       `json:"listResponse"`
}

type CursorCapabilityAdvertisements struct {
	List    string `json:"list"`
	Load    string `json:"load"`
	MCPHTTP string `json:"mcpHTTP"`
	MCPSSE  string `json:"mcpSSE"`
}

type CursorControlObservation struct {
	Binding           CursorBinding                  `json:"binding"`
	BindingSource     string                         `json:"bindingSource"`
	Provenance        CursorControlProvenance        `json:"provenance"`
	ReceiptSHA256     string                         `json:"receiptSHA256"`
	ObservationSHA256 string                         `json:"observationSHA256"`
	AgentState        string                         `json:"agentState"`
	RunState          string                         `json:"runState"`
	WorkerIDState     string                         `json:"workerIDState"`
	Model             string                         `json:"model,omitempty"`
	ModelState        string                         `json:"modelState"`
	ActualModelState  string                         `json:"actualModelState"`
	Cost              CostObservation                `json:"cost"`
	ToolAccess        string                         `json:"toolAccess"`
	PhoneMirroring    string                         `json:"phoneMirroring"`
	RuntimeAuthority  string                         `json:"runtimeAuthority"`
	Advertisements    CursorCapabilityAdvertisements `json:"advertisements"`
	LocalSessions     []CursorLocalSessionMetadata   `json:"localSessions"`
	ListComplete      bool                           `json:"listComplete"`
}

type CursorControlOperation string

const (
	CursorControlLaunch  CursorControlOperation = "launch"
	CursorControlList    CursorControlOperation = "list"
	CursorControlObserve CursorControlOperation = "observe"
	CursorControlSteer   CursorControlOperation = "steer"
	CursorControlCancel  CursorControlOperation = "cancel"
)

type CursorControlPlan struct {
	Operation      CursorControlOperation   `json:"operation"`
	Observation    CursorControlObservation `json:"observation"`
	State          string                   `json:"state"`
	Reason         string                   `json:"reason"`
	OfflineRequest *CursorACPRequest        `json:"offlineRequest,omitempty"`
}

func validCursorBinding(b CursorBinding) bool {
	cwd := len(b.CWD) <= 256 && path.IsAbs(b.CWD) && path.Clean(b.CWD) == b.CWD
	if !cwd || len(b.Repository) > 160 || !cursorControlRepo.MatchString(b.Repository) || !usageDigest.MatchString(b.ToolManifestSHA256) {
		return false
	}
	switch b.Surface {
	case "api":
		return cursorControlAgent.MatchString(b.AgentID) && cursorControlRun.MatchString(b.RunID) &&
			b.LocalSessionID == "" && cursorControlLabel.MatchString(b.WorkerName) &&
			(b.WorkerID == "" || cursorControlLabel.MatchString(b.WorkerID))
	case "acp":
		return b.AgentID == "" && b.RunID == "" && b.WorkerID == "" && b.WorkerName == "" &&
			(b.LocalSessionID == "" || cursorControlLabel.MatchString(b.LocalSessionID))
	default:
		return false
	}
}

func validCursorProvenance(p CursorControlProvenance, source string, now time.Time, maxAge time.Duration) bool {
	return p.Source == source && cursorControlLabel.MatchString(p.ReceiptID) && !p.ObservedAt.IsZero() &&
		!now.IsZero() && maxAge > 0 && maxAge <= 24*time.Hour && !p.ObservedAt.After(now) && now.Sub(p.ObservedAt) <= maxAge
}

func newCursorObservation(b CursorBinding, p CursorControlProvenance) CursorControlObservation {
	return CursorControlObservation{Binding: b, BindingSource: "CallerPinned", Provenance: p, AgentState: UsageUnknown, RunState: UsageUnknown,
		WorkerIDState: UsageUnknown, ModelState: UsageUnknown, ActualModelState: UsageUnknown, Cost: CostObservation{State: UsageUnknown, Kind: UsageUnknown},
		ToolAccess: UsageUnknown, PhoneMirroring: UsageUnknown, RuntimeAuthority: UsageUnknown,
		Advertisements: CursorCapabilityAdvertisements{UsageUnknown, UsageUnknown, UsageUnknown, UsageUnknown}, LocalSessions: []CursorLocalSessionMetadata{}}
}

func cursorReceiptHash(b CursorBinding, receipt any) (string, error) {
	raw, err := CanonicalJSON(struct {
		Binding CursorBinding
		Receipt any
	}{b, receipt})
	if err != nil {
		return "", errors.New("invalid Cursor receipt hash")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// NormalizeCursorAPIRead binds both GET paths and response identities, without
// implying that the API reports repository/tool access or account billing.
func NormalizeCursorAPIRead(raw []byte, want CursorBinding, now time.Time, maxAge time.Duration) (CursorControlObservation, error) {
	var r CursorAPIReadReceipt
	if err := decodeCursorControlReceipt(raw, &r); err != nil {
		return CursorControlObservation{}, err
	}
	if !validCursorBinding(want) || want.Surface != "api" || r.Version != 1 || !validCursorProvenance(r.Provenance, "api-agent-run-projection", now, maxAge) ||
		r.AgentHTTPStatus != 200 || r.RunHTTPStatus != 200 || r.AgentPath != "/v1/agents/"+want.AgentID || r.RunPath != r.AgentPath+"/runs/"+want.RunID ||
		r.Agent.ID != want.AgentID || r.Agent.LatestRunID != want.RunID || r.Run.ID != want.RunID || r.Run.AgentID != want.AgentID ||
		r.Agent.Env.Type != "machine" || r.Agent.Env.Name != want.WorkerName || !cursorControlLabel.MatchString(r.Agent.Status) || len(r.Agent.Status) > 32 ||
		!cursorControlLabel.MatchString(r.Run.Status) || len(r.Run.Status) > 32 {
		return CursorControlObservation{}, errors.New("cursor API receipt binding or freshness refused")
	}
	worker := r.Agent.WorkerID
	if r.Agent.Env.WorkerID != "" {
		if worker != "" && worker != r.Agent.Env.WorkerID {
			return CursorControlObservation{}, errors.New("conflicting Cursor worker IDs")
		}
		worker = r.Agent.Env.WorkerID
	}
	if worker != want.WorkerID {
		return CursorControlObservation{}, errors.New("cursor worker ID absent or mismatched")
	}
	out := newCursorObservation(want, r.Provenance)
	out.AgentState = r.Agent.Status
	out.RunState = r.Run.Status
	if worker != "" {
		out.WorkerIDState = UsageProviderReported
	}
	if r.Agent.Model != nil {
		if !ValidUsageModelID(r.Agent.Model.ID) {
			return CursorControlObservation{}, errors.New("invalid Cursor model metadata")
		}
		out.Model = r.Agent.Model.ID
		out.ModelState = CursorModelProviderRequested
	}
	var err error
	out.ReceiptSHA256, err = cursorReceiptHash(want, r)
	if err != nil {
		return CursorControlObservation{}, err
	}
	return sealCursorControlObservation(out)
}

// BuildCursorACPRequest renders only offline initialize/list request DATA.
// There is no transport or approval Boolean. All execution-bearing methods
// refuse here, including load even when initialize advertised it.
func BuildCursorACPRequest(method string, id int64, b CursorBinding) (CursorACPRequest, error) {
	if method != "initialize" && method != "session/list" {
		return CursorACPRequest{}, errors.New("cursor method held: execution decision required")
	}
	if !validCursorBinding(b) || b.Surface != "acp" || id < 1 || id > 1_000_000_000 {
		return CursorACPRequest{}, errors.New("invalid offline Cursor request binding")
	}
	r := CursorACPRequest{JSONRPC: "2.0", ID: id, Method: method}
	if method == "initialize" {
		r.Params.ProtocolVersion = 1
	} else {
		r.Params.CWD = b.CWD
	}
	return r, nil
}

// NormalizeCursorACPRead accepts metadata projections, not a new live probe.
func NormalizeCursorACPRead(raw []byte, want CursorBinding, now time.Time, maxAge time.Duration) (CursorControlObservation, error) {
	var r CursorACPReadReceipt
	if err := decodeCursorControlReceipt(raw, &r); err != nil {
		return CursorControlObservation{}, err
	}
	if !validCursorBinding(want) || want.Surface != "acp" || r.Version != 1 || !validCursorProvenance(r.Provenance, "acp-initialize-list-projection", now, maxAge) {
		return CursorControlObservation{}, errors.New("cursor ACP receipt binding or freshness refused")
	}
	initReq, e1 := BuildCursorACPRequest("initialize", r.InitializeRequest.ID, want)
	listReq, e2 := BuildCursorACPRequest("session/list", r.ListRequest.ID, want)
	if e1 != nil || e2 != nil || r.InitializeRequest != initReq || r.ListRequest != listReq || initReq.ID == listReq.ID ||
		r.InitializeResponse.JSONRPC != "2.0" || r.InitializeResponse.ID != initReq.ID || r.InitializeResponse.Result == nil || r.InitializeResponse.Result.ProtocolVersion != 1 ||
		r.ListResponse.JSONRPC != "2.0" || r.ListResponse.ID != listReq.ID || r.ListResponse.Result == nil || r.ListResponse.Result.Sessions == nil {
		return CursorControlObservation{}, errors.New("cursor ACP request-response correlation refused")
	}
	sessions := *r.ListResponse.Result.Sessions
	if len(sessions) > 64 {
		return CursorControlObservation{}, errors.New("cursor local session bound exceeded")
	}
	seen := map[string]bool{}
	for _, s := range sessions {
		if !cursorControlLabel.MatchString(s.SessionID) || s.CWD != want.CWD || seen[s.SessionID] {
			return CursorControlObservation{}, errors.New("cursor local session identity refused")
		}
		seen[s.SessionID] = true
	}
	if want.LocalSessionID != "" && !seen[want.LocalSessionID] {
		return CursorControlObservation{}, errors.New("requested Cursor local session absent")
	}
	out := newCursorObservation(want, r.Provenance)
	out.LocalSessions = append(out.LocalSessions, sessions...)
	out.ListComplete = r.ListResponse.Result.NextCursor == nil
	if next := r.ListResponse.Result.NextCursor; next != nil && !cursorControlLabel.MatchString(*next) {
		return CursorControlObservation{}, errors.New("invalid Cursor list cursor")
	}
	caps := r.InitializeResponse.Result.AgentCapabilities
	if caps.LoadSession {
		out.Advertisements.Load = "Advertised"
	}
	if caps.SessionCapabilities.List != nil {
		out.Advertisements.List = "Advertised"
	}
	if caps.MCPCapabilities.HTTP {
		out.Advertisements.MCPHTTP = "Advertised"
	}
	if caps.MCPCapabilities.SSE {
		out.Advertisements.MCPSSE = "Advertised"
	}
	var err error
	out.ReceiptSHA256, err = cursorReceiptHash(want, r)
	if err != nil {
		return CursorControlObservation{}, err
	}
	return sealCursorControlObservation(out)
}

// The normalized digest detects accidental rebinding after serialization. It
// authenticates neither the caller nor runtime authority. ReceiptSHA256 remains
// the separate projection/binding fingerprint; this digest covers every field.
func cursorControlObservationDigest(o CursorControlObservation) (string, error) {
	o.ObservationSHA256 = ""
	raw, err := CanonicalJSON(o)
	if err != nil || len(raw) > MaxCursorControlReceiptBytes {
		return "", errors.New("invalid normalized cursor observation")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func sealCursorControlObservation(o CursorControlObservation) (CursorControlObservation, error) {
	digest, err := cursorControlObservationDigest(o)
	if err != nil {
		return CursorControlObservation{}, err
	}
	o.ObservationSHA256 = digest
	return o, nil
}

func validCursorObservation(o CursorControlObservation) bool {
	if !validCursorBinding(o.Binding) || o.BindingSource != "CallerPinned" || len(o.LocalSessions) > 64 ||
		!cursorControlLabel.MatchString(o.AgentState) || len(o.AgentState) > 32 || !cursorControlLabel.MatchString(o.RunState) || len(o.RunState) > 32 ||
		o.ActualModelState != UsageUnknown || (o.WorkerIDState != UsageUnknown && o.WorkerIDState != UsageProviderReported) {
		return false
	}
	if o.ModelState == UsageUnknown {
		if o.Model != "" {
			return false
		}
	} else if o.ModelState != CursorModelProviderRequested || !ValidUsageModelID(o.Model) {
		return false
	}
	for _, v := range []string{o.Advertisements.List, o.Advertisements.Load, o.Advertisements.MCPHTTP, o.Advertisements.MCPSSE} {
		if v != UsageUnknown && v != "Advertised" {
			return false
		}
	}
	seen := map[string]bool{}
	for _, v := range o.LocalSessions {
		if !cursorControlLabel.MatchString(v.SessionID) || v.CWD != o.Binding.CWD || seen[v.SessionID] {
			return false
		}
		seen[v.SessionID] = true
	}
	if o.Binding.LocalSessionID != "" && !seen[o.Binding.LocalSessionID] {
		return false
	}
	if (o.Binding.WorkerID == "") != (o.WorkerIDState == UsageUnknown) {
		return false
	}
	if o.Binding.Surface == "api" && (len(o.LocalSessions) != 0 || o.ListComplete ||
		o.Advertisements != (CursorCapabilityAdvertisements{UsageUnknown, UsageUnknown, UsageUnknown, UsageUnknown})) {
		return false
	}
	if o.Binding.Surface == "acp" && (o.AgentState != UsageUnknown || o.RunState != UsageUnknown || o.WorkerIDState != UsageUnknown || o.ModelState != UsageUnknown) {
		return false
	}
	return true
}

// PlanCursorControl classifies shared verbs without performing them. No held
// plan contains even an offline request; no read plan authorizes transport.
func PlanCursorControl(op CursorControlOperation, observation CursorControlObservation, requestID int64, now time.Time, maxAge time.Duration) (CursorControlPlan, error) {
	source := "api-agent-run-projection"
	if observation.Binding.Surface == "acp" {
		source = "acp-initialize-list-projection"
	}
	if !validCursorObservation(observation) || !usageDigest.MatchString(observation.ReceiptSHA256) ||
		!validCursorProvenance(observation.Provenance, source, now, maxAge) || observation.ToolAccess != UsageUnknown ||
		observation.RuntimeAuthority != UsageUnknown || observation.PhoneMirroring != UsageUnknown || observation.Cost.State != UsageUnknown || !observation.Cost.Valid() {
		return CursorControlPlan{}, errors.New("cursor observation unconfirmed")
	}
	digest, err := cursorControlObservationDigest(observation)
	if err != nil || !usageDigest.MatchString(observation.ObservationSHA256) || digest != observation.ObservationSHA256 {
		return CursorControlPlan{}, errors.New("cursor observation content binding refused")
	}
	observation.LocalSessions = append([]CursorLocalSessionMetadata{}, observation.LocalSessions...)
	p := CursorControlPlan{Operation: op, Observation: observation, State: "NeedsDecision", Reason: "execution-not-authorized"}
	switch op {
	case CursorControlLaunch, CursorControlSteer, CursorControlCancel:
		return p, nil
	case CursorControlObserve:
		p.State = "ReusedReceipt"
		p.Reason = "metadata-only"
	case CursorControlList:
		if observation.Binding.Surface == "api" {
			p.Reason = "cloud-list-read-unproved"
			return p, nil
		}
		r, err := BuildCursorACPRequest("session/list", requestID, observation.Binding)
		if err != nil {
			return CursorControlPlan{}, err
		}
		p.State = "OfflineOnly"
		p.Reason = "request-data-is-not-transport-authority"
		p.OfflineRequest = &r
	default:
		return CursorControlPlan{}, errors.New("unsupported Cursor control operation")
	}
	return p, nil
}

// Fixed v1 projection keys avoid encoding/json's case-insensitive aliasing.
// Unknown fields fail before decoding and no partial receipt escapes on error.
func decodeCursorControlReceipt(raw []byte, into any) error {
	if len(raw) == 0 || len(raw) > MaxCursorControlReceiptBytes {
		return errors.New("cursor receipt size refused")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := cursorControlJSONValue(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing Cursor receipt")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return errors.New("malformed Cursor receipt")
	}
	return nil
}

func cursorControlJSONValue(d *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("cursor receipt nesting refused")
	}
	t, err := d.Token()
	if err != nil {
		return errors.New("malformed Cursor receipt")
	}
	if delim, ok := t.(json.Delim); ok {
		if delim != '{' && delim != '[' {
			return errors.New("malformed Cursor receipt")
		}
		seen := map[string]bool{}
		for d.More() {
			if delim == '{' {
				k, e := d.Token()
				if e != nil {
					return errors.New("malformed Cursor receipt")
				}
				s, ok := k.(string)
				if !ok || seen[s] {
					return errors.New("duplicate Cursor receipt key")
				}
				switch s {
				case "version", "provenance", "source", "receiptID", "observedAt", "agentPath", "runPath", "agentHTTPStatus", "runHTTPStatus", "agent", "run", "id", "status", "latestRunId", "env", "type", "name", "workerId", "model", "agentId", "initializeRequest", "initializeResponse", "listRequest", "listResponse", "jsonrpc", "method", "params", "protocolVersion", "cwd", "result", "agentCapabilities", "loadSession", "sessionCapabilities", "list", "mcpCapabilities", "http", "sse", "sessions", "sessionId", "nextCursor":
				default:
					return errors.New("noncanonical Cursor receipt key")
				}
				seen[s] = true
			}
			if err := cursorControlJSONValue(d, depth+1); err != nil {
				return err
			}
		}
		end, e := d.Token()
		if e != nil || (delim == '{' && end != json.Delim('}')) || (delim == '[' && end != json.Delim(']')) {
			return errors.New("malformed Cursor receipt")
		}
	}
	return nil
}
