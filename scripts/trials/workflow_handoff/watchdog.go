package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

const (
	watchdogSchema = "dev-env.workflow-watchdog-plan/v1"
	dryRunEvidence = "dry-run-recommendation"
)

// A watchdog plan is a finite dry-run recommendation. It reuses check and does
// not wake, notify, schedule, grant, or complete work.
type watchdogPlan struct {
	Schema            string           `json:"schema"`
	Status            string           `json:"status"`
	DryRun            bool             `json:"dryRun"`
	Executed          bool             `json:"executed"`
	CheckerAuthority  string           `json:"checkerAuthority"`
	OverallDone       bool             `json:"overallDone"`
	ObservedAt        string           `json:"observedAt,omitempty"`
	Publication       string           `json:"publication"`
	Deployment        string           `json:"deployment"`
	Story             string           `json:"story,omitempty"`
	Repository        string           `json:"repository,omitempty"`
	PullRequest       int              `json:"pullRequest"`
	Head              string           `json:"head,omitempty"`
	Coordinator       string           `json:"coordinator,omitempty"`
	Task              string           `json:"task,omitempty"`
	UID               string           `json:"uid,omitempty"`
	HandoffStatus     string           `json:"handoffStatus,omitempty"`
	HandoffOutcome    string           `json:"handoffOutcome,omitempty"`
	HandoffExitCode   int              `json:"handoffExitCode"`
	HandoffCompletion bool             `json:"handoffCompletion"`
	BlockedCustody    bool             `json:"blockedCustody"`
	Reasons           []string         `json:"reasons"`
	Obligations       []obligationView `json:"obligations"`
	Receipts          []receiptView    `json:"receipts"`
	NextReceipts      []receiptView    `json:"nextReceipts"`
	Intents           []intentView     `json:"intents"`
}

type obligationView struct {
	ID                   string `json:"id"`
	Kind                 string `json:"kind"`
	Owner                string `json:"owner"`
	State                string `json:"state"`
	DueAt                string `json:"dueAt,omitempty"`
	ResolutionEvidence   string `json:"resolutionEvidence,omitempty"`
	WaitUntil            string `json:"waitUntil,omitempty"`
	WaitReason           string `json:"waitReason,omitempty"`
	OwnerEscalationDueAt string `json:"ownerEscalationDueAt,omitempty"`
}

type receiptView struct {
	Obligation  string `json:"obligation"`
	Owner       string `json:"owner"`
	Stage       string `json:"stage"`
	State       string `json:"state"`
	Evidence    string `json:"evidence"`
	RecordedAt  string `json:"recordedAt"`
	Repository  string `json:"repository"`
	PullRequest int    `json:"pullRequest"`
	Head        string `json:"head"`
	Task        string `json:"task"`
	UID         string `json:"uid"`
	Kind        string `json:"kind"`
}

type intentView struct {
	Obligation       string `json:"obligation"`
	Owner            string `json:"owner"`
	Stage            string `json:"stage"`
	Kind             string `json:"kind"`
	Task             string `json:"task"`
	UID              string `json:"uid"`
	Repository       string `json:"repository"`
	PullRequest      int    `json:"pullRequest"`
	Head             string `json:"head"`
	DryRun           bool   `json:"dryRun"`
	Executed         bool   `json:"executed"`
	CheckerAuthority string `json:"checkerAuthority"`
}

type watchdogDocument struct {
	Schema      string            `json:"schema"`
	ObservedAt  string            `json:"observedAt"`
	Handoff     json.RawMessage   `json:"handoff"`
	Obligations []json.RawMessage `json:"obligations"`
	Receipts    []json.RawMessage `json:"receipts"`
}

type obligationIn struct {
	ID                   string  `json:"id"`
	Kind                 string  `json:"kind"`
	Owner                string  `json:"owner"`
	DueAt                *string `json:"dueAt"`
	ResolutionEvidence   *string `json:"resolutionEvidence"`
	WaitUntil            *string `json:"waitUntil"`
	WaitReason           *string `json:"waitReason"`
	OwnerEscalationDueAt *string `json:"ownerEscalationDueAt"`
}

type receiptIn struct {
	Obligation  string `json:"obligation"`
	Owner       string `json:"owner"`
	Stage       string `json:"stage"`
	State       string `json:"state"`
	Evidence    string `json:"evidence"`
	RecordedAt  string `json:"recordedAt"`
	Repository  string `json:"repository"`
	PullRequest int    `json:"pullRequest"`
	Head        string `json:"head"`
	Task        string `json:"task"`
	UID         string `json:"uid"`
	Kind        string `json:"kind"`
}

type parsedObligation struct {
	id         string
	kind       string
	owner      string
	due        *time.Time
	resolution string
	wait       *time.Time
	waitReason string
	escalation *time.Time
}

type parsedReceipt struct {
	obligation  string
	owner       string
	stage       string
	state       string
	evidence    string
	at          time.Time
	repository  string
	pullRequest int
	head        string
	task        string
	uid         string
	kind        string
}

type receiptKey struct {
	repository  string
	pullRequest int
	head        string
	task        string
	uid         string
	kind        string
	obligation  string
	owner       string
	stage       string
}

type watchdogInput struct {
	observed    time.Time
	handoff     json.RawMessage
	obligations []parsedObligation
	receipts    []parsedReceipt
}

func planWatchdog(data []byte) (watchdogPlan, int) {
	parsed, err := parseWatchdog(data)
	if err != nil {
		return refusedWatchdog(err.Error()), 2
	}
	handoff, _ := check(parsed.handoff)
	if err := matchReceiptBindings(parsed, handoff); err != nil {
		return refusedWatchdog(err.Error()), 2
	}
	history, effective, foldErr := foldReceipts(parsed.receipts)
	plan := planFromHandoff(parsed, handoff)
	plan.Receipts = receiptViews(history)
	plan.Obligations = obligationViews(parsed, handoff.Coordinator)
	reasons := handoffGates(handoff, parsed.obligations)
	if foldErr != "" {
		reasons = append(reasons, foldErr)
	}
	if msg := resolutionMasquerade(parsed.obligations, history); msg != "" {
		reasons = append(reasons, msg)
	}
	plan.Reasons = capReasons(reasons)
	plan.OverallDone = len(plan.Reasons) == 0 && handoffDone(handoff, parsed.obligations)
	if len(plan.Reasons) > 0 {
		plan.Status = "blocked"
		plan.normalize()
		return plan, 0
	}
	task, uid, _, _ := pickedTask(handoff)
	intents := planIntents(parsed, effective, boundID{
		repository:  copyPhrase(handoff.Repository),
		pullRequest: handoff.PullRequest,
		head:        copyPhrase(handoff.Head),
		coordinator: copyPhrase(handoff.Coordinator),
		task:        copyPhrase(task),
		uid:         copyPhrase(uid),
	})
	plan.Intents = intents
	plan.NextReceipts = nextReceipts(intents, parsed.observed)
	plan.Status = planStatus(plan.Obligations, len(intents))
	plan.normalize()
	return plan, 0
}

type boundID struct {
	repository  string
	pullRequest int
	head        string
	coordinator string
	task        string
	uid         string
}

func refusedWatchdog(msg string) watchdogPlan {
	plan := watchdogPlan{
		Schema:           watchdogSchema,
		Status:           "refused",
		DryRun:           true,
		Executed:         false,
		CheckerAuthority: checkerAuthority,
		Publication:      "distinct-from-deployment",
		Deployment:       "not-performed",
		Reasons:          []string{boundReason(msg)},
	}
	plan.normalize()
	return plan
}

func (p *watchdogPlan) normalize() {
	p.Reasons = fill(p.Reasons)
	if p.Obligations == nil {
		p.Obligations = []obligationView{}
	}
	if p.Receipts == nil {
		p.Receipts = []receiptView{}
	}
	if p.NextReceipts == nil {
		p.NextReceipts = []receiptView{}
	}
	if p.Intents == nil {
		p.Intents = []intentView{}
	}
}

func writeWatchdog(out io.Writer, plan watchdogPlan) error {
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(plan)
}

func parseWatchdog(data []byte) (watchdogInput, error) {
	if len(data) > maxDocumentBytes {
		return watchdogInput{}, errors.New("document exceeds 128KiB")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return watchdogInput{}, err
	}
	if err := rejectWatchdogCaseVariants(data); err != nil {
		return watchdogInput{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var doc watchdogDocument
	if err := dec.Decode(&doc); err != nil {
		return watchdogInput{}, publicJSONError(err)
	}
	if err := requireEOF(dec); err != nil {
		return watchdogInput{}, err
	}
	if doc.Schema != watchdogSchema {
		return watchdogInput{}, errors.New("schema must be dev-env.workflow-watchdog-plan/v1")
	}
	observed, err := parseInstant("observedAt", doc.ObservedAt)
	if err != nil {
		return watchdogInput{}, err
	}
	if len(bytes.TrimSpace(doc.Handoff)) == 0 {
		return watchdogInput{}, errors.New("handoff is required")
	}
	if doc.Obligations == nil {
		return watchdogInput{}, errors.New("obligations is required")
	}
	if len(doc.Obligations) > maxList {
		return watchdogInput{}, errors.New("obligations exceeds 8")
	}
	if doc.Receipts == nil {
		return watchdogInput{}, errors.New("receipts is required")
	}
	if len(doc.Receipts) > maxEvents {
		return watchdogInput{}, errors.New("receipts exceeds 48")
	}
	obligations, ids, err := parseObligations(doc.Obligations)
	if err != nil {
		return watchdogInput{}, err
	}
	receipts, err := parseReceipts(doc.Receipts, ids, observed)
	if err != nil {
		return watchdogInput{}, err
	}
	return watchdogInput{
		observed:    observed,
		handoff:     doc.Handoff,
		obligations: obligations,
		receipts:    receipts,
	}, nil
}

func parseObligations(raw []json.RawMessage) ([]parsedObligation, map[string]string, error) {
	out := make([]parsedObligation, 0, len(raw))
	ids := map[string]string{}
	for _, item := range raw {
		var in obligationIn
		if err := decodeStrict(item, &in); err != nil {
			return nil, nil, publicJSONError(err)
		}
		ob, err := validateObligation(in)
		if err != nil {
			return nil, nil, err
		}
		if prev, ok := ids[ob.id]; ok {
			if prev != ob.kind {
				return nil, nil, errors.New("obligation id collides across kinds")
			}
			return nil, nil, errors.New("obligation id is duplicated")
		}
		ids[ob.id] = ob.kind
		out = append(out, ob)
	}
	return out, ids, nil
}

func validateObligation(in obligationIn) (parsedObligation, error) {
	if !validToken(in.ID) || !validToken(in.Owner) || !knownObligationKind(in.Kind) {
		return parsedObligation{}, errors.New("obligation fields are invalid")
	}
	due, err := optionalInstant("dueAt", in.DueAt)
	if err != nil {
		return parsedObligation{}, err
	}
	resolution, err := optionalReference("resolutionEvidence", in.ResolutionEvidence)
	if err != nil {
		return parsedObligation{}, err
	}
	wait, waitReason, err := pairedWait(in.WaitUntil, in.WaitReason)
	if err != nil {
		return parsedObligation{}, err
	}
	escalation, err := optionalInstant("ownerEscalationDueAt", in.OwnerEscalationDueAt)
	if err != nil {
		return parsedObligation{}, err
	}
	return parsedObligation{
		id:         in.ID,
		kind:       in.Kind,
		owner:      in.Owner,
		due:        due,
		resolution: resolution,
		wait:       wait,
		waitReason: waitReason,
		escalation: escalation,
	}, nil
}

func parseReceipts(raw []json.RawMessage, ids map[string]string, observed time.Time) ([]parsedReceipt, error) {
	out := make([]parsedReceipt, 0, len(raw))
	for _, item := range raw {
		var in receiptIn
		if err := decodeStrict(item, &in); err != nil {
			return nil, publicJSONError(err)
		}
		if _, ok := ids[in.Obligation]; !ok {
			return nil, errors.New("receipt obligation is not in this plan")
		}
		if !validToken(in.Owner) || !knownStage(in.Stage) || !knownReceiptState(in.State) || !validReference(in.Evidence) {
			return nil, errors.New("receipt fields are invalid")
		}
		if !validReceiptBinding(in) {
			return nil, errors.New("receipt binding is invalid")
		}
		if in.Kind != ids[in.Obligation] {
			return nil, errors.New("receipt kind does not match the obligation")
		}
		at, err := parseInstant("recordedAt", in.RecordedAt)
		if err != nil {
			return nil, err
		}
		if recordedAfterObservation(at, observed) {
			return nil, errors.New("receipt recordedAt is after observedAt")
		}
		out = append(out, parsedReceipt{
			obligation:  in.Obligation,
			owner:       in.Owner,
			stage:       in.Stage,
			state:       in.State,
			evidence:    in.Evidence,
			at:          at,
			repository:  in.Repository,
			pullRequest: in.PullRequest,
			head:        in.Head,
			task:        in.Task,
			uid:         in.UID,
			kind:        in.Kind,
		})
	}
	return out, nil
}

func validReceiptBinding(in receiptIn) bool {
	if in.PullRequest <= 0 || !validHead(in.Head) || !validToken(in.Repository) || !validToken(in.Task) || !validToken(in.UID) || !knownObligationKind(in.Kind) {
		return false
	}
	lower := strings.ToLower(in.Repository)
	return !strings.Contains(in.Repository, "://") && !strings.Contains(lower, "http")
}

func matchReceiptBindings(parsed watchdogInput, handoff result) error {
	if len(parsed.receipts) == 0 || handoff.Status == "refused" {
		return nil
	}
	task, uid, ok, ambiguous := pickedTask(handoff)
	if ambiguous || !ok {
		return errors.New("receipt binding is ambiguous")
	}
	for _, rc := range parsed.receipts {
		if rc.repository != handoff.Repository || rc.pullRequest != handoff.PullRequest || rc.head != handoff.Head || rc.task != task || rc.uid != uid {
			return errors.New("receipt binding does not match this handoff")
		}
	}
	return nil
}

func decodeStrict(raw json.RawMessage, dest any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return err
	}
	return requireEOF(dec)
}

func recordedAfterObservation(recorded, observed time.Time) bool {
	return recorded.After(observed)
}

func parseInstant(field, value string) (time.Time, error) {
	t, err := parseStamp(value)
	if err != nil {
		return time.Time{}, errors.New(field + " must be RFC3339")
	}
	return t, nil
}

func optionalInstant(field string, value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	t, err := parseInstant(field, *value)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func optionalReference(field string, value *string) (string, error) {
	if value == nil {
		return "", nil
	}
	if !validReference(*value) {
		return "", errors.New(field + " is not a bounded reference")
	}
	return *value, nil
}

func pairedWait(until, reason *string) (*time.Time, string, error) {
	if (until == nil) != (reason == nil) {
		return nil, "", errors.New("waitUntil and waitReason must be paired")
	}
	if until == nil {
		return nil, "", nil
	}
	at, err := parseInstant("waitUntil", *until)
	if err != nil {
		return nil, "", err
	}
	if !validText(*reason, 400) || strings.Contains(*reason, "://") || strings.Contains(strings.ToLower(*reason), "http") {
		return nil, "", errors.New("waitReason is invalid")
	}
	return &at, *reason, nil
}

func knownObligationKind(kind string) bool {
	switch kind {
	case "start_ack", "progress", "pickup", "user_question":
		return true
	default:
		return false
	}
}

func knownStage(stage string) bool {
	return stage == "owner_wake" || stage == "user_escalation"
}

func knownReceiptState(state string) bool {
	switch state {
	case "planned", "accepted", "acknowledged", "rejected", "ambiguous", "resolved":
		return true
	default:
		return false
	}
}

func validReference(value string) bool {
	if !validToken(value) {
		return false
	}
	lower := strings.ToLower(value)
	return !strings.Contains(value, "://") && !strings.Contains(lower, "http")
}

var (
	watchdogWrapperKeys    = distinctFolds([]string{"schema", "observedAt", "handoff", "obligations", "receipts"})
	watchdogObligationKeys = distinctFolds([]string{"id", "kind", "owner", "dueAt", "resolutionEvidence", "waitUntil", "waitReason", "ownerEscalationDueAt"})
	watchdogReceiptKeys    = distinctFolds([]string{"obligation", "owner", "stage", "state", "evidence", "recordedAt", "repository", "pullRequest", "head", "task", "uid", "kind"})
)

func distinctFolds(keys []string) []string {
	for i, key := range keys {
		for _, other := range keys[:i] {
			if strings.EqualFold(key, other) {
				panic("watchdog keys fold together")
			}
		}
	}
	return keys
}

func rejectWatchdogCaseVariants(data []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	if err := rejectFoldedKeys(root, watchdogWrapperKeys); err != nil {
		return err
	}
	if raw, ok := root["obligations"]; ok {
		if err := rejectFoldedElements(raw, watchdogObligationKeys); err != nil {
			return err
		}
	}
	if raw, ok := root["receipts"]; ok {
		if err := rejectFoldedElements(raw, watchdogReceiptKeys); err != nil {
			return err
		}
	}
	return nil
}

func rejectFoldedElements(raw json.RawMessage, keys []string) error {
	if string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	for _, item := range items {
		if string(bytes.TrimSpace(item)) == "null" {
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(item, &obj); err != nil {
			return err
		}
		if err := rejectFoldedKeys(obj, keys); err != nil {
			return err
		}
	}
	return nil
}

func rejectFoldedKeys(obj map[string]json.RawMessage, keys []string) error {
	for key := range obj {
		for _, canon := range keys {
			if strings.EqualFold(key, canon) && key != canon {
				return fmt.Errorf("case-variant JSON key %q for canonical %q", key, canon)
			}
		}
	}
	return nil
}

func publicJSONError(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "://") || strings.Contains(strings.ToLower(msg), "http") {
		return errors.New("watchdog document is malformed")
	}
	return err
}

func planFromHandoff(parsed watchdogInput, handoff result) watchdogPlan {
	plan := watchdogPlan{
		Schema:            watchdogSchema,
		DryRun:            true,
		Executed:          false,
		CheckerAuthority:  checkerAuthority,
		ObservedAt:        instantKey(parsed.observed),
		Publication:       handoff.Publication,
		Deployment:        handoff.Deployment,
		Story:             copyPhrase(handoff.Story),
		Repository:        copyPhrase(handoff.Repository),
		PullRequest:       handoff.PullRequest,
		Head:              copyPhrase(handoff.Head),
		Coordinator:       copyPhrase(handoff.Coordinator),
		HandoffStatus:     handoff.Status,
		HandoffOutcome:    handoff.Outcome,
		HandoffExitCode:   handoff.ExitCode,
		HandoffCompletion: handoff.Completion,
		BlockedCustody:    handoff.BlockedCustody,
	}
	if task, uid, ok, _ := pickedTask(handoff); ok {
		plan.Task = copyPhrase(task)
		plan.UID = copyPhrase(uid)
	}
	return plan
}

func pickedTask(res result) (string, string, bool, bool) {
	task, uid := "", ""
	ok := false
	for _, rev := range res.Reviews {
		if !rev.Picked {
			continue
		}
		if !validToken(rev.Task) || !validToken(rev.UID) {
			return "", "", false, true
		}
		if ok && (rev.Task != task || rev.UID != uid) {
			return "", "", false, true
		}
		task, uid = rev.Task, rev.UID
		ok = true
	}
	return task, uid, ok, false
}

func handoffGates(res result, obs []parsedObligation) []string {
	if res.Status == "refused" {
		return []string{"handoff record was refused"}
	}
	var reasons []string
	if res.Status == "conflict" || res.ManualDisposition || len(res.Conflicts) > 0 {
		reasons = append(reasons, "handoff record conflicts")
	}
	if res.BlockedCustody {
		msg := "handoff custody is blocked"
		if phrase := safePhrase(res.BlockedReason); phrase != "" {
			msg += ": " + phrase
		}
		reasons = append(reasons, msg)
	}
	for _, hold := range res.Holds {
		if hold.State != "pending" {
			continue
		}
		if validToken(hold.Action) {
			reasons = append(reasons, "unresolved hold "+hold.Action)
			continue
		}
		reasons = append(reasons, "unresolved hold")
	}
	if res.PullRequest <= 0 {
		reasons = append(reasons, "pull request is not positive")
	}
	if !validHead(res.Head) {
		reasons = append(reasons, "head is not an exact pull request head")
	}
	if _, _, ok, ambiguous := pickedTask(res); ambiguous {
		reasons = append(reasons, "picked review task is ambiguous")
	} else if !ok {
		reasons = append(reasons, "no picked bound review task")
	}
	if droppedIdentity(res) {
		reasons = append(reasons, "handoff identity is not a bounded reference")
	}
	if !validToken(res.Coordinator) {
		reasons = append(reasons, "current coordinator is unavailable")
		return reasons
	}
	for _, ob := range obs {
		if ob.owner != res.Coordinator {
			reasons = append(reasons, "obligation "+ob.id+" owner does not match the current coordinator")
		}
	}
	return reasons
}

func handoffDone(res result, obs []parsedObligation) bool {
	if !res.Completion || len(obs) == 0 || !validToken(res.Coordinator) {
		return false
	}
	for _, ob := range obs {
		if ob.resolution == "" || ob.owner != res.Coordinator {
			return false
		}
	}
	return true
}

func obligationViews(parsed watchdogInput, coordinator string) []obligationView {
	views := make([]obligationView, 0, len(parsed.obligations))
	for _, ob := range parsed.obligations {
		view := obligationView{
			ID:                 ob.id,
			Kind:               ob.kind,
			Owner:              ob.owner,
			State:              obligationState(ob, coordinator, parsed.observed),
			ResolutionEvidence: ob.resolution,
			WaitReason:         ob.waitReason,
		}
		if ob.due != nil {
			view.DueAt = instantKey(*ob.due)
		}
		if ob.wait != nil {
			view.WaitUntil = instantKey(*ob.wait)
		}
		if ob.escalation != nil {
			view.OwnerEscalationDueAt = instantKey(*ob.escalation)
		}
		views = append(views, view)
	}
	slices.SortFunc(views, func(a, b obligationView) int {
		return strings.Compare(a.ID, b.ID)
	})
	return views
}

func obligationState(ob parsedObligation, coordinator string, observed time.Time) string {
	if ob.owner != coordinator {
		return "blocked"
	}
	if ob.resolution != "" {
		return "resolved"
	}
	if ob.wait != nil && observed.Before(*ob.wait) {
		return "waiting"
	}
	if ob.due == nil {
		return "unknown"
	}
	return "current"
}

func planIntents(parsed watchdogInput, effective map[receiptKey]parsedReceipt, id boundID) []intentView {
	obs := append([]parsedObligation(nil), parsed.obligations...)
	slices.SortFunc(obs, func(a, b parsedObligation) int {
		return strings.Compare(a.id, b.id)
	})
	intents := []intentView{}
	for _, ob := range obs {
		if ob.owner != id.coordinator || ob.resolution != "" {
			continue
		}
		if ob.wait != nil && parsed.observed.Before(*ob.wait) {
			continue
		}
		wakeKey := boundReceiptKey(id, ob.id, ob.kind, "owner_wake")
		escKey := boundReceiptKey(id, ob.id, ob.kind, "user_escalation")
		wake, wakeExists := effective[wakeKey]
		dueReached := ob.due != nil && !parsed.observed.Before(*ob.due)
		if !wakeExists && dueReached {
			intents = append(intents, newIntent(id, ob, "owner_wake"))
			continue
		}
		if !wakeExists || !ownerWakeCanEscalate(wake.state) {
			continue
		}
		if ob.escalation == nil || parsed.observed.Before(*ob.escalation) {
			continue
		}
		if _, exists := effective[escKey]; exists {
			continue
		}
		intents = append(intents, newIntent(id, ob, "user_escalation"))
	}
	slices.SortFunc(intents, func(a, b intentView) int {
		if c := strings.Compare(a.Obligation, b.Obligation); c != 0 {
			return c
		}
		return strings.Compare(a.Stage, b.Stage)
	})
	return intents
}

func newIntent(id boundID, ob parsedObligation, stage string) intentView {
	return intentView{
		Obligation:       ob.id,
		Owner:            ob.owner,
		Stage:            stage,
		Kind:             ob.kind,
		Task:             id.task,
		UID:              id.uid,
		Repository:       id.repository,
		PullRequest:      id.pullRequest,
		Head:             id.head,
		DryRun:           true,
		Executed:         false,
		CheckerAuthority: checkerAuthority,
	}
}

func nextReceipts(intents []intentView, observed time.Time) []receiptView {
	out := make([]receiptView, 0, len(intents))
	for _, intent := range intents {
		out = append(out, receiptView{
			Obligation:  intent.Obligation,
			Owner:       intent.Owner,
			Stage:       intent.Stage,
			State:       "planned",
			Evidence:    dryRunEvidence,
			RecordedAt:  instantKey(observed),
			Repository:  intent.Repository,
			PullRequest: intent.PullRequest,
			Head:        intent.Head,
			Task:        intent.Task,
			UID:         intent.UID,
			Kind:        intent.Kind,
		})
	}
	return out
}

func planStatus(obs []obligationView, intents int) string {
	if intents > 0 {
		return "planned"
	}
	if len(obs) == 0 {
		return "current"
	}
	allResolved := true
	anyWaiting := false
	anyUnknown := false
	anyCurrent := false
	anyBlocked := false
	for _, ob := range obs {
		switch ob.State {
		case "resolved":
		case "waiting":
			allResolved = false
			anyWaiting = true
		case "unknown":
			allResolved = false
			anyUnknown = true
		case "blocked":
			allResolved = false
			anyBlocked = true
		case "current":
			allResolved = false
			anyCurrent = true
		default:
			allResolved = false
		}
	}
	switch {
	case anyBlocked:
		return "blocked"
	case allResolved:
		return "resolved"
	case anyCurrent:
		return "current"
	case anyWaiting:
		return "waiting"
	case anyUnknown:
		return "unknown"
	default:
		return "current"
	}
}

func foldReceipts(items []parsedReceipt) ([]parsedReceipt, map[receiptKey]parsedReceipt, string) {
	groups := map[receiptKey][]parsedReceipt{}
	keys := []receiptKey{}
	for _, item := range items {
		key := receiptKeyOf(item)
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], item)
	}
	slices.SortFunc(keys, compareReceiptKeys)
	history := []parsedReceipt{}
	effective := map[receiptKey]parsedReceipt{}
	conflict := ""
	for _, key := range keys {
		kept, eff, msg := foldGroup(groups[key])
		history = append(history, kept...)
		if msg != "" {
			if conflict == "" {
				conflict = msg
			}
			continue
		}
		if len(kept) > 0 {
			effective[key] = eff
		}
	}
	sortReceipts(history)
	if conflict != "" {
		return history, nil, conflict
	}
	return history, effective, ""
}

func foldGroup(items []parsedReceipt) ([]parsedReceipt, parsedReceipt, string) {
	sorted := append([]parsedReceipt(nil), items...)
	slices.SortFunc(sorted, func(a, b parsedReceipt) int {
		if a.at.Before(b.at) {
			return -1
		}
		if b.at.Before(a.at) {
			return 1
		}
		if c := strings.Compare(a.state, b.state); c != 0 {
			return c
		}
		return strings.Compare(a.evidence, b.evidence)
	})
	kept := make([]parsedReceipt, 0, len(sorted))
	for _, item := range sorted {
		if len(kept) > 0 {
			prev := kept[len(kept)-1]
			if item.at.Equal(prev.at) && item.state == prev.state && item.evidence == prev.evidence {
				continue
			}
		}
		kept = append(kept, item)
	}
	for i := 1; i < len(kept); i++ {
		if kept[i].at.Equal(kept[i-1].at) {
			return kept, parsedReceipt{}, "equal-time conflicting receipt"
		}
	}
	for i := 1; i < len(kept); i++ {
		prev, cur := kept[i-1], kept[i]
		if cur.state == prev.state {
			if cur.evidence == prev.evidence {
				continue
			}
			return kept, parsedReceipt{}, "receipt evidence conflicts"
		}
		if !receiptAdvances(prev.state, cur.state) {
			return kept, parsedReceipt{}, "receipt state does not advance"
		}
	}
	if len(kept) == 0 {
		return nil, parsedReceipt{}, ""
	}
	return kept, kept[len(kept)-1], ""
}

func receiptAdvances(from, to string) bool {
	switch from {
	case "planned":
		return to == "accepted" || to == "acknowledged" || to == "rejected" || to == "ambiguous" || to == "resolved"
	case "accepted":
		return to == "acknowledged" || to == "rejected" || to == "ambiguous" || to == "resolved"
	case "acknowledged":
		return to == "resolved"
	default:
		return false
	}
}

func ownerWakeCanEscalate(state string) bool {
	return state == "accepted" || state == "acknowledged"
}

func boundReceiptKey(id boundID, obligation, kind, stage string) receiptKey {
	return receiptKey{
		repository:  id.repository,
		pullRequest: id.pullRequest,
		head:        id.head,
		task:        id.task,
		uid:         id.uid,
		kind:        kind,
		obligation:  obligation,
		owner:       id.coordinator,
		stage:       stage,
	}
}

func receiptKeyOf(item parsedReceipt) receiptKey {
	return receiptKey{
		repository:  item.repository,
		pullRequest: item.pullRequest,
		head:        item.head,
		task:        item.task,
		uid:         item.uid,
		kind:        item.kind,
		obligation:  item.obligation,
		owner:       item.owner,
		stage:       item.stage,
	}
}

func compareReceiptKeys(a, b receiptKey) int {
	if c := strings.Compare(a.repository, b.repository); c != 0 {
		return c
	}
	if a.pullRequest != b.pullRequest {
		if a.pullRequest < b.pullRequest {
			return -1
		}
		return 1
	}
	pairs := [][2]string{
		{a.head, b.head},
		{a.task, b.task},
		{a.uid, b.uid},
		{a.kind, b.kind},
		{a.obligation, b.obligation},
		{a.owner, b.owner},
		{a.stage, b.stage},
	}
	for _, pair := range pairs {
		if c := strings.Compare(pair[0], pair[1]); c != 0 {
			return c
		}
	}
	return 0
}

func resolutionMasquerade(obs []parsedObligation, history []parsedReceipt) string {
	evidence := map[string]string{}
	for _, ob := range obs {
		evidence[ob.id] = ob.resolution
	}
	for _, item := range history {
		if item.state != "resolved" {
			continue
		}
		if evidence[item.obligation] == "" || item.evidence != evidence[item.obligation] {
			return "resolved receipt is not resolution evidence"
		}
	}
	return ""
}

func receiptViews(items []parsedReceipt) []receiptView {
	views := make([]receiptView, 0, len(items))
	for _, item := range items {
		views = append(views, receiptView{
			Obligation:  item.obligation,
			Owner:       item.owner,
			Stage:       item.stage,
			State:       item.state,
			Evidence:    item.evidence,
			RecordedAt:  instantKey(item.at),
			Repository:  item.repository,
			PullRequest: item.pullRequest,
			Head:        item.head,
			Task:        item.task,
			UID:         item.uid,
			Kind:        item.kind,
		})
	}
	return views
}

func sortReceipts(items []parsedReceipt) {
	slices.SortFunc(items, func(a, b parsedReceipt) int {
		return strings.Compare(receiptSortKey(a), receiptSortKey(b))
	})
}

func receiptSortKey(item parsedReceipt) string {
	return strings.Join([]string{item.repository, item.head, item.task, item.uid, item.kind, item.obligation, item.owner, item.stage, instantKey(item.at), item.state, item.evidence, fmt.Sprintf("%d", item.pullRequest)}, "\x00")
}

func capReasons(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(in))
	for _, reason := range in {
		if reason == "" {
			continue
		}
		out = append(out, boundReason(reason))
	}
	if len(out) > maxList {
		out = append(out[:maxList-1], "additional reasons omitted")
	}
	if len(out) == 0 {
		return []string{}
	}
	return out
}

func boundReason(msg string) string {
	if msg == "" {
		return "watchdog document was refused"
	}
	lower := strings.ToLower(msg)
	if len(msg) > 200 || strings.Contains(msg, "://") || strings.Contains(lower, "http") {
		return "watchdog document was refused"
	}
	return msg
}

func copyPhrase(value string) string {
	if value == "" || safePhrase(value) == "" {
		return ""
	}
	return value
}

func droppedIdentity(res result) bool {
	for _, value := range []string{res.Story, res.Repository, res.Head, res.Coordinator} {
		if value != "" && safePhrase(value) == "" {
			return true
		}
	}
	for _, rev := range res.Reviews {
		if rev.Picked && (safePhrase(rev.Task) == "" || safePhrase(rev.UID) == "") {
			return true
		}
	}
	return false
}

func safePhrase(msg string) string {
	if msg == "" || len(msg) > 200 || strings.Contains(msg, "://") || strings.Contains(strings.ToLower(msg), "http") {
		return ""
	}
	for _, r := range msg {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return msg
}
