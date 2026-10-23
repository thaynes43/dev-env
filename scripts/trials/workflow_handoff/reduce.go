package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	schemaName         = "dev-env.workflow-handoff/v1"
	maxDocumentBytes   = 128 * 1024
	maxEvents          = 48
	maxList            = 8
	checkerAuthority   = "none"
	dispositionSource  = "externally-established-trusted-review"
	taskClaimUnchanged = "unchanged"
)

type document struct {
	Schema          string            `json:"schema"`
	Story           string            `json:"story"`
	RelatedStories  []string          `json:"relatedStories"`
	Repository      string            `json:"repository"`
	PullRequest     int               `json:"pullRequest"`
	Head            string            `json:"head"`
	Coordinator     string            `json:"coordinator"`
	ReviewBinding   *reviewBinding    `json:"reviewBinding"`
	ModelProvenance *provenanceIn     `json:"modelProvenance"`
	Events          []json.RawMessage `json:"events"`
}

type reviewBinding struct {
	Task string `json:"task"`
	UID  string `json:"uid"`
}

type provenanceIn struct {
	Serving      string `json:"serving"`
	Cost         string `json:"cost"`
	ConfiguredUI string `json:"configuredUI"`
}

type provenanceOut struct {
	Serving      string `json:"serving"`
	Cost         string `json:"cost"`
	ConfiguredUI string `json:"configuredUI"`
}

type result struct {
	Schema                 string        `json:"schema"`
	Status                 string        `json:"status"`
	ExitCode               int           `json:"exitCode"`
	Outcome                string        `json:"outcome"`
	NextAction             string        `json:"nextAction"`
	Story                  string        `json:"story"`
	RelatedStories         []string      `json:"relatedStories"`
	Repository             string        `json:"repository"`
	PullRequest            int           `json:"pullRequest"`
	Head                   string        `json:"head"`
	Coordinator            string        `json:"coordinator"`
	Executor               executorView  `json:"executor"`
	ActiveSuccessor        string        `json:"activeSuccessor"`
	RecordedSuccessorCount int           `json:"recordedSuccessorCount"`
	BlockedCustody         bool          `json:"blockedCustody"`
	BlockedReason          string        `json:"blockedReason"`
	TaskClaim              string        `json:"taskClaim"`
	Completion             bool          `json:"completion"`
	WritesRecommended      bool          `json:"writesRecommended"`
	DeliveryReceipts       []string      `json:"deliveryReceipts"`
	AcknowledgedDeliveries []string      `json:"acknowledgedDeliveries"`
	Holds                  []holdView    `json:"holds"`
	CurrentScope           *scopeView    `json:"currentScope"`
	RetainedScope          []scopeView   `json:"retainedScope"`
	Reviews                []reviewView  `json:"reviews"`
	Passes                 []passView    `json:"passes"`
	Recommendations        []string      `json:"recommendations"`
	Refusals               []string      `json:"refusals"`
	Conflicts              []string      `json:"conflicts"`
	MergeRelease           string        `json:"mergeRelease"`
	MergeBlockers          []string      `json:"mergeBlockers"`
	Publication            string        `json:"publication"`
	Deployment             string        `json:"deployment"`
	ManualDisposition      bool          `json:"manualDisposition"`
	ModelProvenance        provenanceOut `json:"modelProvenance"`
	IgnoredReplayCount     int           `json:"ignoredReplayCount"`
	CheckerAuthority       string        `json:"checkerAuthority"`
}

type executorView struct {
	Identity      string `json:"identity"`
	State         string `json:"state"`
	VerifiedStop  bool   `json:"verifiedStop"`
	StopAmbiguous bool   `json:"stopAmbiguous"`
	TurnEnded     bool   `json:"turnEnded"`
}

type holdView struct {
	Identity string `json:"identity"`
	Action   string `json:"action"`
	State    string `json:"state"`
	Reason   string `json:"reason"`
}

type scopeView struct {
	Identity   string   `json:"identity"`
	Scope      string   `json:"scope"`
	Reference  string   `json:"reference"`
	Summary    string   `json:"summary"`
	Verbatim   string   `json:"verbatim"`
	SourceTime string   `json:"sourceTime"`
	Supersedes []string `json:"supersedes"`
	Superseded bool     `json:"superseded"`
}

type reviewView struct {
	Task        string `json:"task"`
	UID         string `json:"uid"`
	Head        string `json:"head"`
	Verdict     string `json:"verdict"`
	Observation string `json:"observation"`
	Source      string `json:"source"`
	SourceTime  string `json:"sourceTime"`
	Picked      bool   `json:"picked"`
}

type passView struct {
	Identity         string `json:"identity"`
	Executor         string `json:"executor"`
	Budget           string `json:"budget"`
	Deadline         string `json:"deadline"`
	AuthorizationRef string `json:"authorizationRef"`
}

type header struct {
	Kind       string `json:"kind"`
	Identity   string `json:"identity"`
	SourceTime string `json:"sourceTime"`
}

type passEvent struct {
	header
	Executor         string   `json:"executor"`
	Deadline         string   `json:"deadline"`
	Budget           string   `json:"budget"`
	Authorized       bool     `json:"authorized"`
	AuthorizationRef string   `json:"authorizationRef"`
	Preserves        []string `json:"preserves"`
	WritesAllowed    bool     `json:"writesAllowed"`
}

type execEvent struct {
	header
	Executor string `json:"executor"`
	State    string `json:"state"`
}

type deadlineEvent struct {
	header
	Executor string `json:"executor"`
}

type stopEvent struct {
	header
	Executor  string `json:"executor"`
	Verified  bool   `json:"verified"`
	Ambiguous bool   `json:"ambiguous"`
}

type ackEvent struct {
	header
	Delivery string `json:"delivery"`
}

type reviewEvent struct {
	header
	Task    string `json:"task"`
	UID     string `json:"uid"`
	Head    string `json:"head"`
	Source  string `json:"source"`
	Phase   string `json:"phase"`
	Verdict string `json:"verdict"`
}

type scopeEvent struct {
	header
	Reference  string   `json:"reference"`
	Summary    string   `json:"summary"`
	Verbatim   string   `json:"verbatim"`
	Scope      string   `json:"scope"`
	Supersedes []string `json:"supersedes"`
}

type holdEvent struct {
	header
	Action string `json:"action"`
	Reason string `json:"reason"`
	State  string `json:"state"`
}

type dispEvent struct {
	header
	Action                 string `json:"action"`
	PermitsReconsideration bool   `json:"permitsReconsideration"`
	Source                 string `json:"source"`
}

type assignEvent struct {
	header
	AssignmentKey         string `json:"assignmentKey"`
	Predecessor           string `json:"predecessor"`
	Successor             string `json:"successor"`
	Authorized            bool   `json:"authorized"`
	AuthorizationRef      string `json:"authorizationRef"`
	SupportedContinuation bool   `json:"supportedContinuation"`
}

type leaseEvent struct {
	header
	Executor string `json:"executor"`
}

type reconnectEvent struct {
	header
	Executor string `json:"executor"`
}

type gateEvent struct {
	header
	Head                string `json:"head"`
	AgentOpened         bool   `json:"agentOpened"`
	RequiredCI          string `json:"requiredCI"`
	NativeReviewVerdict string `json:"nativeReviewVerdict"`
	ReviewJobSuccess    bool   `json:"reviewJobSuccess"`
	Findings            string `json:"findings"`
	HeadUnchanged       bool   `json:"headUnchanged"`
}

type claimEvent struct {
	header
	Note string `json:"note"`
}

type parsed struct {
	index     int
	at        time.Time
	kind      string
	identity  string
	canonical string
	body      any
}

type observation struct {
	ev    reviewEvent
	at    time.Time
	index int
}

type scopeRec struct {
	ev         scopeEvent
	at         time.Time
	superseded bool
}

type holdRec struct {
	view holdView
	at   time.Time
}

type succRec struct {
	key       string
	successor string
	at        time.Time
}

type execState struct {
	identity   string
	state      string
	verified   bool
	ambiguous  bool
	stopAt     time.Time
	turnEnded  bool
	deadline   bool
	deadlineAt time.Time
}

type gateState struct {
	set      bool
	conflict bool
	at       time.Time
	ev       gateEvent
}

type reducer struct {
	doc              document
	prov             provenanceOut
	exec             execState
	passes           []passView
	passIDs          map[string]bool
	receipts         []string
	receiptSet       map[string]bool
	acks             []string
	holds            []holdRec
	scopes           []scopeRec
	observations     []observation
	successor        *succRec
	blocked          bool
	blockedReason    string
	conflicts        []string
	refusals         []string
	recs             []string
	replays          int
	gate             gateState
	writes           bool
	suppressedAssign map[string]bool
}

func check(data []byte) (result, int) {
	if len(data) > maxDocumentBytes {
		return refused("document exceeds 128KiB"), 2
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return refused(err.Error()), 2
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var doc document
	if err := dec.Decode(&doc); err != nil {
		return refused(err.Error()), 2
	}
	if dec.More() {
		return refused("trailing JSON value"), 2
	}
	items, prov, err := validate(doc)
	if err != nil {
		return refused(err.Error()), 2
	}
	res := reduce(doc, prov, items)
	return res, res.ExitCode
}

func refused(msg string) result {
	res := result{
		Schema:           schemaName,
		Status:           "refused",
		ExitCode:         2,
		Outcome:          "refused",
		NextAction:       "correct the handoff record; the checker did not apply it: " + msg,
		TaskClaim:        taskClaimUnchanged,
		Publication:      "distinct-from-deployment",
		Deployment:       "not-performed",
		CheckerAuthority: checkerAuthority,
		ModelProvenance:  provenanceOut{Serving: "Unknown", Cost: "Unknown"},
		Refusals:         []string{msg},
		MergeRelease:     "not-evaluated",
	}
	res.normalize()
	return res
}

func (r *result) normalize() {
	r.RelatedStories = fill(r.RelatedStories)
	r.DeliveryReceipts = fill(r.DeliveryReceipts)
	r.AcknowledgedDeliveries = fill(r.AcknowledgedDeliveries)
	r.Holds = fillHolds(r.Holds)
	r.RetainedScope = fillScopes(r.RetainedScope)
	r.Reviews = fillReviews(r.Reviews)
	r.Passes = fillPasses(r.Passes)
	r.Recommendations = fill(r.Recommendations)
	r.Refusals = fill(r.Refusals)
	r.Conflicts = fill(r.Conflicts)
	r.MergeBlockers = fill(r.MergeBlockers)
	if r.CurrentScope != nil {
		r.CurrentScope.Supersedes = fill(r.CurrentScope.Supersedes)
	}
}

func fill(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func fillHolds(in []holdView) []holdView {
	if in == nil {
		return []holdView{}
	}
	return in
}

func fillScopes(in []scopeView) []scopeView {
	if in == nil {
		return []scopeView{}
	}
	for i := range in {
		in[i].Supersedes = fill(in[i].Supersedes)
	}
	return in
}

func fillReviews(in []reviewView) []reviewView {
	if in == nil {
		return []reviewView{}
	}
	return in
}

func fillPasses(in []passView) []passView {
	if in == nil {
		return []passView{}
	}
	return in
}

func validate(doc document) ([]parsed, provenanceOut, error) {
	if doc.Schema != schemaName {
		return nil, provenanceOut{}, errors.New("schema must be dev-env.workflow-handoff/v1")
	}
	if !validText(doc.Story, 64) || !validToken(doc.Story) {
		return nil, provenanceOut{}, errors.New("story is invalid")
	}
	if doc.RelatedStories == nil {
		return nil, provenanceOut{}, errors.New("relatedStories is required")
	}
	if len(doc.RelatedStories) > maxList {
		return nil, provenanceOut{}, errors.New("relatedStories exceeds 8")
	}
	for _, story := range doc.RelatedStories {
		if !validToken(story) {
			return nil, provenanceOut{}, errors.New("related story is invalid")
		}
	}
	if !validText(doc.Repository, 256) {
		return nil, provenanceOut{}, errors.New("repository is invalid")
	}
	if doc.PullRequest < 0 {
		return nil, provenanceOut{}, errors.New("pullRequest is invalid")
	}
	if !validHead(doc.Head) {
		return nil, provenanceOut{}, errors.New("head must be 40 lowercase hex characters")
	}
	if !validToken(doc.Coordinator) {
		return nil, provenanceOut{}, errors.New("coordinator is invalid")
	}
	if doc.ReviewBinding != nil {
		if !validToken(doc.ReviewBinding.Task) || !validToken(doc.ReviewBinding.UID) {
			return nil, provenanceOut{}, errors.New("reviewBinding is invalid")
		}
	}
	prov, err := validateProvenance(doc.ModelProvenance)
	if err != nil {
		return nil, provenanceOut{}, err
	}
	if doc.Events == nil {
		return nil, provenanceOut{}, errors.New("events is required")
	}
	if len(doc.Events) > maxEvents {
		return nil, provenanceOut{}, errors.New("events exceeds 48")
	}
	items := make([]parsed, 0, len(doc.Events))
	for i, raw := range doc.Events {
		item, err := parseEvent(i, raw)
		if err != nil {
			return nil, provenanceOut{}, err
		}
		items = append(items, item)
	}
	return items, prov, nil
}

func validateProvenance(in *provenanceIn) (provenanceOut, error) {
	out := provenanceOut{Serving: "Unknown", Cost: "Unknown"}
	if in == nil {
		return out, nil
	}
	serving := in.Serving
	if serving == "" {
		serving = "Unknown"
	}
	cost := in.Cost
	if cost == "" {
		cost = "Unknown"
	}
	if serving != "Unknown" && serving != "Reported" {
		return provenanceOut{}, errors.New("model serving must be Unknown or Reported")
	}
	if cost != "Unknown" && cost != "Reported" {
		return provenanceOut{}, errors.New("model cost must be Unknown or Reported")
	}
	if in.ConfiguredUI != "" && !validText(in.ConfiguredUI, 128) {
		return provenanceOut{}, errors.New("configuredUI is invalid")
	}
	out.Serving = serving
	out.Cost = cost
	out.ConfiguredUI = in.ConfiguredUI
	return out, nil
}

func parseEvent(index int, raw json.RawMessage) (parsed, error) {
	var peek header
	if err := json.Unmarshal(raw, &peek); err != nil {
		return parsed{}, err
	}
	body, kind, identity, when, err := decodeKind(raw)
	if err != nil {
		return parsed{}, err
	}
	canon, err := json.Marshal(body)
	if err != nil {
		return parsed{}, err
	}
	return parsed{
		index:     index,
		at:        when,
		kind:      kind,
		identity:  identity,
		canonical: string(canon),
		body:      body,
	}, nil
}

func decodeKind(raw json.RawMessage) (any, string, string, time.Time, error) {
	var peek header
	if err := json.Unmarshal(raw, &peek); err != nil {
		return nil, "", "", time.Time{}, err
	}
	switch peek.Kind {
	case "worker-pass":
		ev, err := decode[passEvent](raw)
		if err != nil {
			return nil, "", "", time.Time{}, err
		}
		return ev, ev.Kind, ev.Identity, mustTime(ev.SourceTime), validatePass(ev)
	case "executor-state":
		ev, err := decode[execEvent](raw)
		if err != nil {
			return nil, "", "", time.Time{}, err
		}
		return ev, ev.Kind, ev.Identity, mustTime(ev.SourceTime), validateExec(ev)
	case "deadline-reached":
		ev, err := decode[deadlineEvent](raw)
		if err != nil {
			return nil, "", "", time.Time{}, err
		}
		return ev, ev.Kind, ev.Identity, mustTime(ev.SourceTime), validateDeadline(ev)
	case "stop-release":
		ev, err := decode[stopEvent](raw)
		if err != nil {
			return nil, "", "", time.Time{}, err
		}
		return ev, ev.Kind, ev.Identity, mustTime(ev.SourceTime), validateStop(ev)
	case "delivery-receipt":
		ev, err := decode[header](raw)
		if err != nil {
			return nil, "", "", time.Time{}, err
		}
		return ev, ev.Kind, ev.Identity, mustTime(ev.SourceTime), validateHeader(ev)
	case "acknowledgment":
		ev, err := decode[ackEvent](raw)
		if err != nil {
			return nil, "", "", time.Time{}, err
		}
		return ev, ev.Kind, ev.Identity, mustTime(ev.SourceTime), validateAck(ev)
	case "review-observation":
		ev, err := decode[reviewEvent](raw)
		if err != nil {
			return nil, "", "", time.Time{}, err
		}
		return ev, ev.Kind, ev.Identity, mustTime(ev.SourceTime), validateReview(ev)
	case "scope-instruction":
		ev, err := decode[scopeEvent](raw)
		if err != nil {
			return nil, "", "", time.Time{}, err
		}
		return ev, ev.Kind, ev.Identity, mustTime(ev.SourceTime), validateScope(ev)
	case "hold":
		ev, err := decode[holdEvent](raw)
		if err != nil {
			return nil, "", "", time.Time{}, err
		}
		return ev, ev.Kind, ev.Identity, mustTime(ev.SourceTime), validateHold(ev)
	case "hold-disposition":
		ev, err := decode[dispEvent](raw)
		if err != nil {
			return nil, "", "", time.Time{}, err
		}
		return ev, ev.Kind, ev.Identity, mustTime(ev.SourceTime), validateDisp(ev)
	case "successor-assignment":
		ev, err := decode[assignEvent](raw)
		if err != nil {
			return nil, "", "", time.Time{}, err
		}
		return ev, ev.Kind, ev.Identity, mustTime(ev.SourceTime), validateAssign(ev)
	case "lease-expiry":
		ev, err := decode[leaseEvent](raw)
		if err != nil {
			return nil, "", "", time.Time{}, err
		}
		return ev, ev.Kind, ev.Identity, mustTime(ev.SourceTime), validateLease(ev)
	case "reconnect":
		ev, err := decode[reconnectEvent](raw)
		if err != nil {
			return nil, "", "", time.Time{}, err
		}
		return ev, ev.Kind, ev.Identity, mustTime(ev.SourceTime), validateReconnect(ev)
	case "gate-evidence":
		ev, err := decode[gateEvent](raw)
		if err != nil {
			return nil, "", "", time.Time{}, err
		}
		return ev, ev.Kind, ev.Identity, mustTime(ev.SourceTime), validateGate(ev)
	case "taskclaim-mutation":
		ev, err := decode[claimEvent](raw)
		if err != nil {
			return nil, "", "", time.Time{}, err
		}
		return ev, ev.Kind, ev.Identity, mustTime(ev.SourceTime), validateClaim(ev)
	default:
		return nil, "", "", time.Time{}, errors.New("unknown event kind")
	}
}

func decode[T any](raw json.RawMessage) (T, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var out T
	if err := dec.Decode(&out); err != nil {
		return out, err
	}
	if dec.More() {
		return out, errors.New("trailing event value")
	}
	return out, nil
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func validateHeader(ev header) error {
	if ev.Kind == "" || !validToken(ev.Identity) {
		return errors.New("event identity is invalid")
	}
	if _, err := parseStamp(ev.SourceTime); err != nil {
		return err
	}
	return nil
}

func validatePass(ev passEvent) error {
	if err := validateHeader(ev.header); err != nil {
		return err
	}
	if !validToken(ev.Executor) || !validToken(ev.Budget) {
		return errors.New("worker-pass fields are invalid")
	}
	if _, err := parseStamp(ev.Deadline); err != nil {
		return errors.New("worker-pass deadline is invalid")
	}
	if ev.AuthorizationRef != "" && !validToken(ev.AuthorizationRef) {
		return errors.New("worker-pass authorizationRef is invalid")
	}
	if len(ev.Preserves) > maxList {
		return errors.New("worker-pass preserves exceeds 8")
	}
	for _, id := range ev.Preserves {
		if !validToken(id) {
			return errors.New("worker-pass preserves entry is invalid")
		}
	}
	return nil
}

func validateExec(ev execEvent) error {
	if err := validateHeader(ev.header); err != nil {
		return err
	}
	if !validToken(ev.Executor) {
		return errors.New("executor-state executor is invalid")
	}
	if ev.State != "running" && ev.State != "exited" {
		return errors.New("executor-state must be running or exited")
	}
	return nil
}

func validateDeadline(ev deadlineEvent) error {
	if err := validateHeader(ev.header); err != nil {
		return err
	}
	if !validToken(ev.Executor) {
		return errors.New("deadline-reached executor is invalid")
	}
	return nil
}

func validateStop(ev stopEvent) error {
	if err := validateHeader(ev.header); err != nil {
		return err
	}
	if !validToken(ev.Executor) {
		return errors.New("stop-release executor is invalid")
	}
	if ev.Verified && ev.Ambiguous {
		return errors.New("stop-release cannot be both verified and ambiguous")
	}
	return nil
}

func validateAck(ev ackEvent) error {
	if err := validateHeader(ev.header); err != nil {
		return err
	}
	if !validToken(ev.Delivery) {
		return errors.New("acknowledgment delivery is invalid")
	}
	return nil
}

func validateReview(ev reviewEvent) error {
	if err := validateHeader(ev.header); err != nil {
		return err
	}
	if !validToken(ev.Task) || !validToken(ev.UID) || !validToken(ev.Source) || !validHead(ev.Head) {
		return errors.New("review-observation fields are invalid")
	}
	switch ev.Phase {
	case "running":
		if ev.Verdict != "" {
			return errors.New("running review observation cannot carry a verdict")
		}
	case "terminal":
		switch ev.Verdict {
		case "approved", "changes-requested", "rejected", "unavailable":
		default:
			return errors.New("terminal review verdict is invalid")
		}
	default:
		return errors.New("review phase must be running or terminal")
	}
	return nil
}

func validateScope(ev scopeEvent) error {
	if err := validateHeader(ev.header); err != nil {
		return err
	}
	if !validToken(ev.Reference) || !validToken(ev.Scope) || !validText(ev.Summary, 400) {
		return errors.New("scope-instruction fields are invalid")
	}
	if ev.Verbatim != "" && !validText(ev.Verbatim, 1000) {
		return errors.New("scope verbatim is invalid")
	}
	if len(ev.Supersedes) > maxList {
		return errors.New("scope supersedes exceeds 8")
	}
	for _, id := range ev.Supersedes {
		if !validToken(id) {
			return errors.New("scope supersedes entry is invalid")
		}
	}
	return nil
}

func validateHold(ev holdEvent) error {
	if err := validateHeader(ev.header); err != nil {
		return err
	}
	if !validToken(ev.Action) || !validText(ev.Reason, 400) {
		return errors.New("hold fields are invalid")
	}
	if ev.State != "pending" {
		return errors.New("hold state must be pending")
	}
	return nil
}

func validateDisp(ev dispEvent) error {
	if err := validateHeader(ev.header); err != nil {
		return err
	}
	if !validToken(ev.Action) || !validToken(ev.Source) {
		return errors.New("hold-disposition fields are invalid")
	}
	return nil
}

func validateAssign(ev assignEvent) error {
	if err := validateHeader(ev.header); err != nil {
		return err
	}
	if !validToken(ev.AssignmentKey) || !validToken(ev.Predecessor) || !validToken(ev.Successor) {
		return errors.New("successor-assignment fields are invalid")
	}
	if ev.AuthorizationRef != "" && !validToken(ev.AuthorizationRef) {
		return errors.New("successor authorizationRef is invalid")
	}
	return nil
}

func validateLease(ev leaseEvent) error {
	if err := validateHeader(ev.header); err != nil {
		return err
	}
	if ev.Executor != "" && !validToken(ev.Executor) {
		return errors.New("lease-expiry executor is invalid")
	}
	return nil
}

func validateReconnect(ev reconnectEvent) error {
	if err := validateHeader(ev.header); err != nil {
		return err
	}
	if !validToken(ev.Executor) {
		return errors.New("reconnect executor is invalid")
	}
	return nil
}

func validateGate(ev gateEvent) error {
	if err := validateHeader(ev.header); err != nil {
		return err
	}
	if !validHead(ev.Head) {
		return errors.New("gate head is invalid")
	}
	switch ev.RequiredCI {
	case "passed", "failed", "missing":
	default:
		return errors.New("requiredCI is invalid")
	}
	switch ev.NativeReviewVerdict {
	case "approved", "changes-requested", "rejected", "missing", "job-succeeded":
	default:
		return errors.New("nativeReviewVerdict is invalid")
	}
	switch ev.Findings {
	case "disposed", "pending", "missing":
	default:
		return errors.New("findings is invalid")
	}
	return nil
}

func validateClaim(ev claimEvent) error {
	if err := validateHeader(ev.header); err != nil {
		return err
	}
	if !validText(ev.Note, 400) {
		return errors.New("taskclaim note is invalid")
	}
	return nil
}

func parseStamp(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil || t.Format(time.RFC3339) != s {
		return time.Time{}, errors.New("sourceTime must be RFC3339")
	}
	return t, nil
}

func validHead(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > 'f' || (c > '9' && c < 'a') {
			return false
		}
	}
	return true
}

func validToken(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i, c := range s {
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == ':' || c == '/' || c == '-'
		if i == 0 && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
		if !ok {
			return false
		}
	}
	return true
}

func validText(s string, max int) bool {
	if s == "" || len(s) > max || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func sortByTime(items []parsed) {
	for i := 1; i < len(items); i++ {
		j := i
		for j > 0 && earlier(items[j], items[j-1]) {
			items[j], items[j-1] = items[j-1], items[j]
			j--
		}
	}
}

func earlier(a, b parsed) bool {
	if a.at.Equal(b.at) {
		return a.index < b.index
	}
	return a.at.Before(b.at)
}

func dedup(items []parsed) ([]parsed, int, []string) {
	seen := map[string]string{}
	out := make([]parsed, 0, len(items))
	replays := 0
	conflicts := []string{}
	for _, item := range items {
		if prev, ok := seen[item.identity]; ok {
			if prev == item.canonical {
				replays++
				continue
			}
			conflicts = append(conflicts, "identity "+item.identity+" reused with a different payload")
			continue
		}
		seen[item.identity] = item.canonical
		out = append(out, item)
	}
	return out, replays, conflicts
}

func reduce(doc document, prov provenanceOut, items []parsed) result {
	items, replays, idConflicts := dedup(items)
	sortByTime(items)
	r := &reducer{
		doc:              doc,
		prov:             prov,
		passIDs:          map[string]bool{},
		receiptSet:       map[string]bool{},
		suppressedAssign: map[string]bool{},
		replays:          replays,
	}
	for _, msg := range idConflicts {
		r.conflict(msg)
	}
	r.prescan(items)
	for _, item := range items {
		r.apply(item)
	}
	return r.finish()
}

func (r *reducer) prescan(items []parsed) {
	verdicts := map[string]string{}
	emitted := map[string]bool{}
	successors := map[string]string{}
	emittedAssign := map[string]bool{}
	for _, item := range items {
		ev, ok := item.body.(reviewEvent)
		if ok && ev.Phase == "terminal" && ev.Head == r.doc.Head && r.bindingOK(ev) {
			subject := ev.Task + "\x00" + ev.UID
			key := subject + "\x00" + ev.SourceTime
			if prev, seen := verdicts[key]; seen && prev != ev.Verdict {
				if !emitted[subject] {
					r.conflict("equal-time conflicting review verdicts for task " + ev.Task + " uid " + ev.UID)
					emitted[subject] = true
				}
			} else if !seen {
				verdicts[key] = ev.Verdict
			}
		}
		as, ok := item.body.(assignEvent)
		if !ok {
			continue
		}
		key := as.AssignmentKey + "\x00" + as.SourceTime
		if prev, seen := successors[key]; seen && prev != as.Successor {
			r.suppressedAssign[key] = true
			if !emittedAssign[as.AssignmentKey] {
				r.conflict("equal-time conflicting successor assignments for key " + as.AssignmentKey)
				emittedAssign[as.AssignmentKey] = true
			}
			r.blocked = true
			r.blockedReason = "equal-time conflicting successor assignments require manual disposition"
		} else if !seen {
			successors[key] = as.Successor
		}
	}
}

func (r *reducer) bindingOK(ev reviewEvent) bool {
	if r.doc.ReviewBinding == nil {
		return true
	}
	return ev.Task == r.doc.ReviewBinding.Task && ev.UID == r.doc.ReviewBinding.UID
}

func (r *reducer) apply(item parsed) {
	switch ev := item.body.(type) {
	case passEvent:
		r.applyPass(ev)
	case execEvent:
		r.applyExec(ev, item.at)
	case deadlineEvent:
		r.applyDeadline(ev, item.at)
	case stopEvent:
		r.applyStop(ev, item.at)
	case header:
		r.applyDelivery(ev)
	case ackEvent:
		r.applyAck(ev)
	case reviewEvent:
		r.applyReview(ev, item)
	case scopeEvent:
		r.scopes = append(r.scopes, scopeRec{ev: ev, at: item.at})
	case holdEvent:
		r.holds = append(r.holds, holdRec{at: item.at, view: holdView{Identity: ev.Identity, Action: ev.Action, State: "pending", Reason: ev.Reason}})
	case dispEvent:
		r.applyDisp(ev, item.at)
	case assignEvent:
		r.applyAssign(ev, item.at)
	case leaseEvent:
		r.refusals = append(r.refusals, "lease-expiry "+ev.Identity+" does not transfer ownership or create a successor")
	case reconnectEvent:
		r.refusals = append(r.refusals, "reconnect "+ev.Identity+" does not create a successor")
	case gateEvent:
		r.applyGate(ev, item.at)
	case claimEvent:
		r.refusals = append(r.refusals, "TaskClaim mutation "+ev.Identity+" refused; TaskClaim unchanged")
	}
}

func (r *reducer) applyPass(ev passEvent) {
	if r.exec.deadline && (!ev.Authorized || ev.AuthorizationRef == "") {
		r.refusals = append(r.refusals, "no automatic new allowance after deadline "+ev.Identity)
		return
	}
	if !ev.Authorized || ev.AuthorizationRef == "" {
		r.refusals = append(r.refusals, "worker pass "+ev.Identity+" is not externally authorized")
		return
	}
	for _, id := range ev.Preserves {
		if !r.passIDs[id] {
			r.refusals = append(r.refusals, "worker pass "+ev.Identity+" does not preserve unknown pass "+id)
			return
		}
	}
	if r.exec.identity == "" {
		r.exec.identity = ev.Executor
	}
	r.passes = append(r.passes, passView{
		Identity:         ev.Identity,
		Executor:         ev.Executor,
		Budget:           ev.Budget,
		Deadline:         ev.Deadline,
		AuthorizationRef: ev.AuthorizationRef,
	})
	r.passIDs[ev.Identity] = true
	if ev.WritesAllowed && !r.exec.deadline {
		r.writes = true
	}
	if r.exec.deadline {
		r.writes = false
	}
}

func (r *reducer) applyExec(ev execEvent, at time.Time) {
	if r.exec.identity != "" && ev.Executor != r.exec.identity {
		r.refusals = append(r.refusals, "executor change "+ev.Identity+" refused")
		return
	}
	if r.exec.deadline && ev.State == "running" {
		r.refusals = append(r.refusals, "running state "+ev.Identity+" does not reopen a turn ended by deadline")
		return
	}
	r.exec.identity = ev.Executor
	if r.exec.state == "ended-pending-verified-stop" && ev.State == "exited" {
		return
	}
	r.exec.state = ev.State
	_ = at
}

func (r *reducer) applyDeadline(ev deadlineEvent, at time.Time) {
	if r.exec.identity != "" && ev.Executor != r.exec.identity {
		r.refusals = append(r.refusals, "deadline executor mismatch "+ev.Identity)
		return
	}
	if r.exec.deadline && !at.After(r.exec.deadlineAt) {
		r.refusals = append(r.refusals, "stale deadline "+ev.Identity)
		return
	}
	r.exec.identity = ev.Executor
	r.exec.deadline = true
	r.exec.deadlineAt = at
	r.exec.turnEnded = true
	r.exec.state = "ended-pending-verified-stop"
	r.writes = false
}

func (r *reducer) applyStop(ev stopEvent, at time.Time) {
	if r.exec.identity != "" && ev.Executor != r.exec.identity {
		r.refusals = append(r.refusals, "stop-release executor does not match "+ev.Identity)
		return
	}
	if r.exec.identity == "" {
		r.exec.identity = ev.Executor
	}
	if ev.Ambiguous {
		if r.exec.verified && !at.After(r.exec.stopAt) {
			r.refusals = append(r.refusals, "stale ambiguous stop "+ev.Identity+" does not overwrite a verified release")
			return
		}
		if r.exec.verified && at.After(r.exec.stopAt) {
			r.exec.verified = false
			r.exec.ambiguous = true
			r.exec.stopAt = at
			r.conflict("later ambiguous stop conflicts with a verified release")
			return
		}
		r.exec.ambiguous = true
		r.exec.verified = false
		r.exec.stopAt = at
		return
	}
	if !ev.Verified {
		r.refusals = append(r.refusals, "unverified stop "+ev.Identity+" is not a release")
		return
	}
	if r.exec.ambiguous && at.Before(r.exec.stopAt) {
		r.refusals = append(r.refusals, "stale verified stop "+ev.Identity+" does not overwrite a later ambiguous release")
		return
	}
	if r.exec.ambiguous && at.Equal(r.exec.stopAt) {
		r.conflict("equal-time ambiguous and verified stop require manual disposition")
		r.exec.verified = false
		return
	}
	r.exec.verified = true
	r.exec.ambiguous = false
	r.exec.stopAt = at
	if r.exec.state != "ended-pending-verified-stop" {
		r.exec.state = "exited"
	}
}

func (r *reducer) applyDelivery(ev header) {
	if r.receiptSet[ev.Identity] {
		return
	}
	r.receipts = append(r.receipts, ev.Identity)
	r.receiptSet[ev.Identity] = true
}

func (r *reducer) applyAck(ev ackEvent) {
	if !r.receiptSet[ev.Delivery] {
		r.refusals = append(r.refusals, "acknowledgment "+ev.Identity+" does not create delivery, ownership, or completion")
		return
	}
	for _, id := range r.acks {
		if id == ev.Delivery {
			return
		}
	}
	r.acks = append(r.acks, ev.Delivery)
}

func (r *reducer) applyReview(ev reviewEvent, item parsed) {
	if r.doc.ReviewBinding != nil && (ev.Task != r.doc.ReviewBinding.Task || ev.UID != r.doc.ReviewBinding.UID) {
		r.refusals = append(r.refusals, "wrong-uid observation "+ev.Identity)
		return
	}
	if ev.Head != r.doc.Head {
		r.refusals = append(r.refusals, "wrong-head observation "+ev.Identity)
		return
	}
	r.observations = append(r.observations, observation{ev: ev, at: item.at, index: item.index})
}

func (r *reducer) applyDisp(ev dispEvent, at time.Time) {
	if ev.Source != dispositionSource || !ev.PermitsReconsideration {
		r.refusals = append(r.refusals, "hold disposition "+ev.Identity+" does not permit reconsideration")
		return
	}
	n := 0
	for i := range r.holds {
		if r.holds[i].view.Action == ev.Action && r.holds[i].view.State == "pending" && !r.holds[i].at.After(at) {
			r.holds[i].view.State = "reconsidered"
			n++
		}
	}
	if n == 0 {
		r.refusals = append(r.refusals, "hold disposition "+ev.Identity+" matches no pending hold")
	}
}

func (r *reducer) applyAssign(ev assignEvent, at time.Time) {
	key := ev.AssignmentKey + "\x00" + ev.SourceTime
	if r.suppressedAssign[key] {
		r.refusals = append(r.refusals, "conflicting assignment "+ev.Identity+" not recorded")
		r.blocked = true
		r.blockedReason = "equal-time conflicting successor assignments require manual disposition"
		if r.successor != nil && r.successor.at.Equal(at) && r.successor.key == ev.AssignmentKey {
			r.successor = nil
		}
		return
	}
	if r.exec.identity != "" && ev.Predecessor != r.exec.identity {
		r.blocked = true
		r.blockedReason = "predecessor does not match the recorded executor"
		return
	}
	if reason := r.assignBlockReason(ev); reason != "" {
		r.blocked = true
		r.blockedReason = reason
		return
	}
	if r.successor == nil {
		r.successor = &succRec{key: ev.AssignmentKey, successor: ev.Successor, at: at}
		if len(r.conflicts) == 0 {
			r.blocked = false
			r.blockedReason = ""
		}
		r.recommend("recorded exactly one active successor " + ev.Successor + "; TaskClaim unchanged")
		return
	}
	if r.successor.key == ev.AssignmentKey && r.successor.successor == ev.Successor {
		r.refusals = append(r.refusals, "duplicate successor assignment "+ev.Identity+" does not create another owner")
		return
	}
	r.blocked = true
	r.blockedReason = "changed same-key assignment does not replace the recorded successor"
}

func (r *reducer) assignBlockReason(ev assignEvent) string {
	if r.exec.ambiguous {
		return "stop-release is ambiguous"
	}
	if r.exec.state == "running" || r.exec.state == "" {
		return "predecessor is still running"
	}
	if !r.exec.verified {
		if r.exec.deadline || r.exec.state == "ended-pending-verified-stop" {
			return "deadline is not a verified stop"
		}
		return "executor exit does not release parent outcome or TaskClaim"
	}
	if !ev.Authorized || ev.AuthorizationRef == "" {
		return "successor assignment is not externally authorized"
	}
	if !ev.SupportedContinuation {
		return "supported headless continuation is unavailable"
	}
	return ""
}

func (r *reducer) applyGate(ev gateEvent, at time.Time) {
	if r.gate.conflict {
		r.refusals = append(r.refusals, "gate "+ev.Identity+" not applied after equal-time conflict")
		return
	}
	if r.gate.set && at.Before(r.gate.at) {
		r.refusals = append(r.refusals, "stale gate "+ev.Identity+" does not overwrite later gate evidence")
		return
	}
	if r.gate.set && at.Equal(r.gate.at) {
		if sameGatePayload(r.gate.ev, ev) {
			return
		}
		r.gate.set = false
		r.gate.conflict = true
		r.conflict("equal-time conflicting gate evidence requires manual disposition")
		return
	}
	r.gate = gateState{set: true, at: at, ev: ev}
}

func sameGatePayload(a, b gateEvent) bool {
	return a.Head == b.Head &&
		a.AgentOpened == b.AgentOpened &&
		a.RequiredCI == b.RequiredCI &&
		a.NativeReviewVerdict == b.NativeReviewVerdict &&
		a.ReviewJobSuccess == b.ReviewJobSuccess &&
		a.Findings == b.Findings &&
		a.HeadUnchanged == b.HeadUnchanged
}

func (r *reducer) recommend(msg string) {
	for _, have := range r.recs {
		if have == msg {
			return
		}
	}
	r.recs = append(r.recs, msg)
}

func (r *reducer) conflict(msg string) {
	for _, have := range r.conflicts {
		if have == msg {
			return
		}
	}
	r.conflicts = append(r.conflicts, msg)
}

func (r *reducer) finish() result {
	reviews := r.resolveReviews()
	current, retained := r.resolveScope()
	merge, blockers := r.evalMerge()
	active := ""
	count := 0
	if r.successor != nil {
		active = r.successor.successor
		count = 1
	}
	writes := r.writes && !r.exec.deadline
	res := result{
		Schema:         schemaName,
		Story:          r.doc.Story,
		RelatedStories: append([]string{}, r.doc.RelatedStories...),
		Repository:     r.doc.Repository,
		PullRequest:    r.doc.PullRequest,
		Head:           r.doc.Head,
		Coordinator:    r.doc.Coordinator,
		Executor: executorView{
			Identity:      r.exec.identity,
			State:         r.exec.state,
			VerifiedStop:  r.exec.verified,
			StopAmbiguous: r.exec.ambiguous,
			TurnEnded:     r.exec.turnEnded,
		},
		ActiveSuccessor:        active,
		RecordedSuccessorCount: count,
		BlockedCustody:         r.blocked,
		BlockedReason:          r.blockedReason,
		TaskClaim:              taskClaimUnchanged,
		Completion:             false,
		WritesRecommended:      writes,
		DeliveryReceipts:       append([]string{}, r.receipts...),
		AcknowledgedDeliveries: append([]string{}, r.acks...),
		Holds:                  holdViews(r.holds),
		CurrentScope:           current,
		RetainedScope:          retained,
		Reviews:                reviews,
		Passes:                 append([]passView{}, r.passes...),
		Recommendations:        append([]string{}, r.recs...),
		Refusals:               append([]string{}, r.refusals...),
		Conflicts:              append([]string{}, r.conflicts...),
		Publication:            "distinct-from-deployment",
		Deployment:             "not-performed",
		ModelProvenance:        r.prov,
		IgnoredReplayCount:     r.replays,
		CheckerAuthority:       checkerAuthority,
	}
	switch {
	case len(r.conflicts) > 0:
		res.Status = "conflict"
		res.ExitCode = 3
		res.Outcome = "manual-disposition"
		res.ManualDisposition = true
		res.NextAction = "manual disposition required: " + strings.Join(r.conflicts, "; ")
	case r.blocked:
		res.Status = "reduced"
		res.ExitCode = 0
		res.Outcome = "blocked-custody"
		res.NextAction = "blocked custody: " + r.blockedReason + "; coordinator " + r.doc.Coordinator + " retains the outcome and the next manual action"
	case merge == "recommend-normal-workflow":
		res.Status = "reduced"
		res.ExitCode = 0
		res.Outcome = "recommend-normal-merge-release"
		res.NextAction = "recommend existing normal dev-env merge and release workflow; publication is distinct from deployment; checker performs neither"
		res.Recommendations = append(res.Recommendations, "recommend existing normal dev-env merge and release workflow; publication is distinct from deployment; checker does not merge or deploy")
	case merge == "blocked":
		res.Status = "reduced"
		res.ExitCode = 0
		res.Outcome = "merge-blocked"
		res.NextAction = "merge blocked: " + strings.Join(blockers, "; ")
	case r.exec.deadline && r.successor == nil:
		res.Status = "reduced"
		res.ExitCode = 0
		res.Outcome = "executor-ended-pending-verified-stop"
		res.NextAction = "coordinator " + r.doc.Coordinator + " owns unresolved review, repair, CI and outcome; executor turn ended pending verified stop; no further write recommendation and no automatic new allowance"
	default:
		res.Status = "reduced"
		res.ExitCode = 0
		res.Outcome = "pending-coordinator"
		res.NextAction = "coordinator " + r.doc.Coordinator + " retains the outcome; the checker authorizes no action"
	}
	res.MergeRelease = merge
	res.MergeBlockers = blockers
	res.normalize()
	return res
}

func (r *reducer) resolveReviews() []reviewView {
	order := []string{}
	grouped := map[string][]observation{}
	for _, obs := range r.observations {
		key := obs.ev.Task + "\x00" + obs.ev.UID
		if _, ok := grouped[key]; !ok {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], obs)
	}
	views := []reviewView{}
	for _, key := range order {
		parts := strings.Split(key, "\x00")
		group := grouped[key]
		var terminals []observation
		var running []observation
		for _, obs := range group {
			if obs.ev.Phase == "terminal" {
				terminals = append(terminals, obs)
			} else {
				running = append(running, obs)
			}
		}
		if len(terminals) == 0 {
			for _, obs := range running {
				r.refusals = append(r.refusals, "running observation "+obs.ev.Identity+" is not a verdict")
			}
			views = append(views, reviewView{Task: parts[0], UID: parts[1], Picked: false})
			continue
		}
		maxAt := terminals[0].at
		for _, obs := range terminals[1:] {
			if obs.at.After(maxAt) {
				maxAt = obs.at
			}
		}
		var tops []observation
		for _, obs := range terminals {
			if obs.at.Equal(maxAt) {
				tops = append(tops, obs)
			}
		}
		distinct := []string{}
		seenVerdict := map[string]bool{}
		for _, obs := range tops {
			if !seenVerdict[obs.ev.Verdict] {
				seenVerdict[obs.ev.Verdict] = true
				distinct = append(distinct, obs.ev.Verdict)
			}
		}
		if len(distinct) > 1 {
			views = append(views, reviewView{Task: parts[0], UID: parts[1], Picked: false})
			continue
		}
		picked := tops[0]
		for _, obs := range terminals {
			if obs.index == picked.index {
				continue
			}
			if obs.at.Equal(maxAt) {
				r.refusals = append(r.refusals, "duplicate terminal observation "+obs.ev.Identity+" does not add a second verdict")
				continue
			}
			r.refusals = append(r.refusals, "stale terminal observation "+obs.ev.Identity+" does not overwrite "+picked.ev.Identity)
		}
		for _, obs := range running {
			if !obs.at.After(picked.at) {
				r.refusals = append(r.refusals, "stale running observation "+obs.ev.Identity+" does not overwrite terminal "+picked.ev.Identity)
				continue
			}
			r.refusals = append(r.refusals, "running observation "+obs.ev.Identity+" does not overwrite terminal "+picked.ev.Identity)
		}
		views = append(views, reviewView{
			Task:        picked.ev.Task,
			UID:         picked.ev.UID,
			Head:        picked.ev.Head,
			Verdict:     picked.ev.Verdict,
			Observation: picked.ev.Identity,
			Source:      picked.ev.Source,
			SourceTime:  picked.ev.SourceTime,
			Picked:      true,
		})
		r.recommend("picked review verdict " + picked.ev.Verdict + " once for task " + picked.ev.Task + " uid " + picked.ev.UID)
	}
	return views
}

func (r *reducer) resolveScope() (*scopeView, []scopeView) {
	for i := range r.scopes {
		for j := range r.scopes {
			if i == j {
				continue
			}
			if !r.scopes[j].at.Before(r.scopes[i].at) && contains(r.scopes[j].ev.Supersedes, r.scopes[i].ev.Identity) {
				r.scopes[i].superseded = true
			}
		}
	}
	retained := make([]scopeView, 0, len(r.scopes))
	for _, sc := range r.scopes {
		retained = append(retained, scopeViewOf(sc))
	}
	var max time.Time
	have := false
	for _, sc := range r.scopes {
		if sc.superseded {
			continue
		}
		if !have || sc.at.After(max) {
			max = sc.at
			have = true
		}
	}
	tops := []scopeRec{}
	if have {
		for _, sc := range r.scopes {
			if !sc.superseded && sc.at.Equal(max) {
				tops = append(tops, sc)
			}
		}
	}
	if len(tops) > 1 {
		r.conflict("equal-time conflicting scope instructions require manual disposition")
		return nil, retained
	}
	if len(tops) == 1 {
		view := scopeViewOf(tops[0])
		return &view, retained
	}
	return nil, retained
}

func scopeViewOf(sc scopeRec) scopeView {
	return scopeView{
		Identity:   sc.ev.Identity,
		Scope:      sc.ev.Scope,
		Reference:  sc.ev.Reference,
		Summary:    sc.ev.Summary,
		Verbatim:   sc.ev.Verbatim,
		SourceTime: sc.ev.SourceTime,
		Supersedes: append([]string{}, sc.ev.Supersedes...),
		Superseded: sc.superseded,
	}
}

func holdViews(in []holdRec) []holdView {
	out := make([]holdView, 0, len(in))
	for _, h := range in {
		out = append(out, h.view)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func (r *reducer) evalMerge() (string, []string) {
	if r.gate.conflict {
		return "blocked", []string{"equal-time conflicting gate evidence"}
	}
	if !r.gate.set {
		return "not-evaluated", []string{}
	}
	g := r.gate.ev
	blockers := []string{}
	if g.Head != r.doc.Head {
		blockers = append(blockers, "gate head does not match the record head")
	}
	if !g.AgentOpened {
		blockers = append(blockers, "pull request is not recorded as agent-opened")
	}
	if g.RequiredCI != "passed" {
		blockers = append(blockers, "required CI is "+g.RequiredCI)
	}
	if g.NativeReviewVerdict != "approved" {
		if g.NativeReviewVerdict == "job-succeeded" {
			blockers = append(blockers, "review job success is not a native review verdict")
		} else {
			blockers = append(blockers, "native review verdict is "+g.NativeReviewVerdict)
		}
	}
	if g.Findings != "disposed" {
		blockers = append(blockers, "findings disposition is "+g.Findings)
	}
	if !g.HeadUnchanged {
		blockers = append(blockers, "unchanged-head preflight is not recorded")
	}
	for _, h := range r.holds {
		if h.view.State == "pending" && blocksMerge(h.view.Action) {
			blockers = append(blockers, "action-specific hold "+h.view.Identity+" on "+h.view.Action)
		}
	}
	if r.blocked {
		blockers = append(blockers, "blocked custody")
	}
	if len(r.conflicts) > 0 {
		blockers = append(blockers, "manual disposition is required")
	}
	if len(blockers) == 0 {
		return "recommend-normal-workflow", []string{}
	}
	return "blocked", blockers
}

func blocksMerge(action string) bool {
	switch action {
	case "merge", "release", "merge-release", "source-merge", "source-pr-merge":
		return true
	default:
		return false
	}
}

func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := consumeValue(dec); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing JSON value")
	}
	return nil
}

func consumeValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("object not closed")
		}
		return nil
	case '[':
		for dec.More() {
			if err := consumeValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("array not closed")
		}
		return nil
	default:
		return errors.New("unexpected delimiter")
	}
}
