package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	schemaName         = "dev-env.stopped-handoff-validation/v1"
	maxDocumentBytes   = 128 * 1024
	maxEvents          = 32
	maxEntries         = 8
	maxList            = 8
	checkerAuthority   = "none"
	taskClaimUnchanged = "unchanged"
	advisoryChecker    = "scripts/trials/workflow_handoff"
	blockedOperation   = "installed disposable predecessor stop and supported read/restore/assignment"
	blockedOwner       = "separate reviewed installed-drill stage"
)

type document struct {
	Schema          string      `json:"schema"`
	Story           string      `json:"story"`
	Synthetic       bool        `json:"synthetic"`
	Predecessor     predecessor `json:"predecessor"`
	Recipient       recipient   `json:"recipient"`
	Observed        observed    `json:"observed"`
	Manifest        manifest    `json:"manifest"`
	Excerpt         excerpt     `json:"excerpt"`
	Events          []event     `json:"events"`
	TaskClaim       string      `json:"taskClaim"`
	Continuation    string      `json:"continuation"`
	Assignment      assignment  `json:"assignment"`
	AdvisoryChecker string      `json:"advisoryChecker"`
}

type predecessor struct {
	Identity      string `json:"identity"`
	UID           string `json:"uid"`
	Head          string `json:"head"`
	SourceTime    string `json:"sourceTime"`
	RulesRevision string `json:"rulesRevision"`
	ScopeRef      string `json:"scopeRef"`
	AuthorityRef  string `json:"authorityRef"`
	ExecutorState string `json:"executorState"`
	StopVerified  bool   `json:"stopVerified"`
	StopAmbiguous bool   `json:"stopAmbiguous"`
}

type recipient struct {
	Identity string `json:"identity"`
	UID      string `json:"uid"`
}

type observed struct {
	UID            string `json:"uid"`
	Head           string `json:"head"`
	ContextPresent bool   `json:"contextPresent"`
	ContextChanged bool   `json:"contextChanged"`
	ContextHash    string `json:"contextHash"`
	WIPPresent     bool   `json:"wipPresent"`
	WIPChanged     bool   `json:"wipChanged"`
	WIPHash        string `json:"wipHash"`
}

type manifest struct {
	Algorithm string          `json:"algorithm"`
	Entries   []manifestEntry `json:"entries"`
}

type manifestEntry struct {
	Path     string `json:"path"`
	GitState string `json:"gitState"`
	SHA256   string `json:"sha256"`
}

type excerpt struct {
	Kind            string   `json:"kind"`
	ContentHash     string   `json:"contentHash"`
	PendingFindings []string `json:"pendingFindings"`
	Holds           []string `json:"holds"`
	ConsumedBudget  string   `json:"consumedBudget"`
	Attempts        int      `json:"attempts"`
	NativeHistory   bool     `json:"nativeHistory"`
	NativeImport    bool     `json:"nativeImport"`
}

type event struct {
	Kind        string `json:"kind"`
	Identity    string `json:"identity"`
	SourceTime  string `json:"sourceTime"`
	Owner       string `json:"owner"`
	Note        string `json:"note"`
	Successor   string `json:"successor"`
	ContentHash string `json:"contentHash"`
}

type assignment struct {
	Authorized bool   `json:"authorized"`
	Supported  bool   `json:"supported"`
	Successor  string `json:"successor"`
}

type phase struct {
	Name     string `json:"name"`
	Duration string `json:"duration"`
}

type result struct {
	Schema                 string   `json:"schema"`
	Status                 string   `json:"status"`
	ExitCode               int      `json:"exitCode"`
	Outcome                string   `json:"outcome"`
	NextAction             string   `json:"nextAction"`
	ProofClass             string   `json:"proofClass"`
	InstalledJourney       bool     `json:"installedJourney"`
	ExplicitContextProof   string   `json:"explicitContextProof"`
	FullHistoryProof       string   `json:"fullHistoryProof"`
	NativeImport           string   `json:"nativeImport"`
	TaskClaim              string   `json:"taskClaim"`
	CheckerAuthority       string   `json:"checkerAuthority"`
	AdvisoryChecker        string   `json:"advisoryChecker"`
	AdvisoryAuthority      string   `json:"advisoryAuthority"`
	PredecessorIdentity    string   `json:"predecessorIdentity"`
	RecipientIdentity      string   `json:"recipientIdentity"`
	SourceUID              string   `json:"sourceUID"`
	SourceHead             string   `json:"sourceHead"`
	ObservedUID            string   `json:"observedUID"`
	ObservedHead           string   `json:"observedHead"`
	SourceTime             string   `json:"sourceTime"`
	RulesRevision          string   `json:"rulesRevision"`
	ScopeRef               string   `json:"scopeRef"`
	AuthorityRef           string   `json:"authorityRef"`
	ExecutorState          string   `json:"executorState"`
	AcceptedStop           bool     `json:"acceptedStop"`
	Holds                  []string `json:"holds"`
	PendingFindings        []string `json:"pendingFindings"`
	ConsumedBudget         string   `json:"consumedBudget"`
	Attempts               int      `json:"attempts"`
	RestoreAuthorized      bool     `json:"restoreAuthorized"`
	FixtureVerified        bool     `json:"fixtureVerified"`
	OriginalsPreserved     string   `json:"originalsPreserved"`
	RecordedSuccessor      string   `json:"recordedSuccessor"`
	RecordedSuccessorCount int      `json:"recordedSuccessorCount"`
	SuccessorAction        string   `json:"successorAction"`
	BlockedCustody         bool     `json:"blockedCustody"`
	BlockedReason          string   `json:"blockedReason"`
	NextOwner              string   `json:"nextOwner"`
	PickupRecorded         bool     `json:"pickupRecorded"`
	AcknowledgmentRecorded bool     `json:"acknowledgmentRecorded"`
	LateResult             string   `json:"lateResult"`
	ResultCount            int      `json:"resultCount"`
	IgnoredReplayCount     int      `json:"ignoredReplayCount"`
	ManualDisposition      bool     `json:"manualDisposition"`
	Refusals               []string `json:"refusals"`
	Conflicts              []string `json:"conflicts"`
	Phases                 []phase  `json:"phases"`
	Publication            string   `json:"publication"`
	Deployment             string   `json:"deployment"`
	InstalledDrill         string   `json:"installedDrill"`
	BlockedOperation       string   `json:"blockedOperation"`
	BlockedOperationOwner  string   `json:"blockedOperationOwner"`
}

func check(data []byte) (result, int) {
	parseStart := time.Now()
	if len(data) > maxDocumentBytes {
		res := refused("document exceeds 128KiB")
		res.Phases = []phase{{Name: "parse", Duration: time.Since(parseStart).String()}}
		return res, 2
	}
	doc, err := parseDocument(data)
	parseDur := time.Since(parseStart)
	if err != nil {
		res := refused(err.Error())
		res.Phases = []phase{{Name: "parse", Duration: parseDur.String()}}
		return res, 2
	}
	validStart := time.Now()
	if err := validate(doc); err != nil {
		res := refused(err.Error())
		copySource(&res, doc)
		res.Phases = []phase{
			{Name: "parse", Duration: parseDur.String()},
			{Name: "validate", Duration: time.Since(validStart).String()},
		}
		return res, 2
	}
	validDur := time.Since(validStart)
	reduceStart := time.Now()
	res := reduce(doc)
	res.Phases = []phase{
		{Name: "parse", Duration: parseDur.String()},
		{Name: "validate", Duration: validDur.String()},
		{Name: "reduce", Duration: time.Since(reduceStart).String()},
	}
	return res, res.ExitCode
}

func parseDocument(data []byte) (document, error) {
	if err := rejectDuplicateKeys(data); err != nil {
		return document{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var doc document
	if err := dec.Decode(&doc); err != nil {
		return document{}, err
	}
	if err := requireEOF(dec); err != nil {
		return document{}, err
	}
	return doc, nil
}

func validate(doc document) error {
	if doc.Schema != schemaName {
		return fmt.Errorf("schema must be %s", schemaName)
	}
	if doc.Story != "101" {
		return errors.New("story must be 101")
	}
	if !doc.Synthetic {
		return errors.New("record must be synthetic")
	}
	if doc.TaskClaim != taskClaimUnchanged {
		return errors.New("task claim mutation is refused and was not applied")
	}
	if doc.AdvisoryChecker != advisoryChecker {
		return fmt.Errorf("advisoryChecker must be %s", advisoryChecker)
	}
	if doc.Continuation != "available" && doc.Continuation != "unavailable" {
		return errors.New("continuation must be available or unavailable")
	}
	if err := validateParty(doc.Predecessor, doc.Recipient); err != nil {
		return err
	}
	if err := validateExcerpt(doc.Excerpt); err != nil {
		return err
	}
	if err := validateManifest(doc.Manifest); err != nil {
		return err
	}
	if err := validateObserved(doc.Observed, doc.Excerpt, doc.Manifest); err != nil {
		return err
	}
	if len(doc.Events) > maxEvents {
		return fmt.Errorf("events exceed %d", maxEvents)
	}
	for _, ev := range doc.Events {
		if err := validateEvent(ev); err != nil {
			return err
		}
	}
	return nil
}

func validateParty(pred predecessor, recip recipient) error {
	if err := ident(pred.Identity, "predecessor identity"); err != nil {
		return err
	}
	if err := ident(recip.Identity, "recipient identity"); err != nil {
		return err
	}
	if err := ident(pred.UID, "predecessor uid"); err != nil {
		return err
	}
	if err := ident(recip.UID, "recipient uid"); err != nil {
		return err
	}
	if pred.Identity == recip.Identity || pred.UID == recip.UID {
		return errors.New("predecessor and recipient must be distinct identities")
	}
	if !validHead(pred.Head) {
		return errors.New("head must be 40 lowercase hex characters")
	}
	if err := stamp(pred.SourceTime, "predecessor sourceTime"); err != nil {
		return err
	}
	if err := short(pred.RulesRevision, "rulesRevision"); err != nil {
		return err
	}
	if err := short(pred.ScopeRef, "scopeRef"); err != nil {
		return err
	}
	if err := short(pred.AuthorityRef, "authorityRef"); err != nil {
		return err
	}
	switch pred.ExecutorState {
	case "stopped", "running", "uncertain", "exited", "unavailable":
	default:
		return fmt.Errorf("executorState %q is unknown", pred.ExecutorState)
	}
	return nil
}

func validateExcerpt(ex excerpt) error {
	if ex.Kind != "public-handoff-excerpt" {
		return errors.New("excerpt kind must be public-handoff-excerpt")
	}
	if ex.NativeHistory {
		return errors.New("public excerpt is not native history")
	}
	if ex.NativeImport {
		return errors.New("public excerpt is not native import")
	}
	if !validSHA(ex.ContentHash) {
		return errors.New("excerpt contentHash must be sha256")
	}
	if ex.Attempts < 0 {
		return errors.New("attempts must be zero or greater")
	}
	if err := short(ex.ConsumedBudget, "consumedBudget"); err != nil {
		return err
	}
	if err := stringsOf(ex.PendingFindings, "pending finding"); err != nil {
		return err
	}
	return stringsOf(ex.Holds, "hold")
}

func validateManifest(man manifest) error {
	if man.Algorithm != "sha256" {
		return errors.New("manifest algorithm must be sha256")
	}
	if len(man.Entries) == 0 || len(man.Entries) > maxEntries {
		return fmt.Errorf("manifest needs 1 to %d entries", maxEntries)
	}
	seen := map[string]bool{}
	for _, entry := range man.Entries {
		if seen[entry.Path] {
			return fmt.Errorf("duplicate manifest path %q", entry.Path)
		}
		seen[entry.Path] = true
		if _, err := cleanRel(entry.Path); err != nil {
			return err
		}
		switch entry.GitState {
		case "committed", "staged", "unstaged", "untracked":
		default:
			return fmt.Errorf("gitState %q is unknown", entry.GitState)
		}
		if !validSHA(entry.SHA256) {
			return fmt.Errorf("manifest sha256 for %s is invalid", entry.Path)
		}
	}
	return nil
}

func validateObserved(ob observed, ex excerpt, man manifest) error {
	if !validHead(ob.Head) {
		return errors.New("observed head must be 40 lowercase hex characters")
	}
	if err := ident(ob.UID, "observed uid"); err != nil {
		return err
	}
	digest, err := wipDigest(man)
	if err != nil {
		return err
	}
	if ob.ContextChanged && !ob.ContextPresent {
		return errors.New("changed context requires a present context")
	}
	if ob.WIPChanged && !ob.WIPPresent {
		return errors.New("changed WIP requires present WIP")
	}
	if !ob.ContextPresent {
		if ob.ContextHash != "" {
			return errors.New("missing context cannot carry a hash")
		}
	} else if !validSHA(ob.ContextHash) {
		return errors.New("contextHash must be sha256")
	} else if ob.ContextChanged == (ob.ContextHash == ex.ContentHash) {
		return errors.New("contextChanged does not match the context hash")
	}
	if !ob.WIPPresent {
		if ob.WIPHash != "" {
			return errors.New("missing WIP cannot carry a hash")
		}
		return nil
	}
	if !validSHA(ob.WIPHash) {
		return errors.New("wipHash must be sha256")
	}
	if ob.WIPChanged == (ob.WIPHash == digest) {
		return errors.New("wipChanged does not match the manifest digest")
	}
	return nil
}

func validateEvent(ev event) error {
	if err := ident(ev.Identity, "event identity"); err != nil {
		return err
	}
	if err := stamp(ev.SourceTime, "event sourceTime"); err != nil {
		return err
	}
	switch ev.Kind {
	case "pickup", "acknowledgment", "reconnect", "delayed-running":
		if ev.Owner != "" || ev.Successor != "" || ev.ContentHash != "" || ev.Note == "" {
			return fmt.Errorf("event %s fields do not match %s", ev.Identity, ev.Kind)
		}
		return bounded(ev.Note, "note")
	case "next-owner":
		if ev.Note != "" || ev.Successor != "" || ev.ContentHash != "" || ev.Owner == "" {
			return fmt.Errorf("event %s is not a next-owner record", ev.Identity)
		}
		return ident(ev.Owner, "next owner")
	case "successor-request":
		if ev.Owner != "" || ev.Note != "" || ev.ContentHash != "" || ev.Successor == "" {
			return fmt.Errorf("event %s is not a successor request", ev.Identity)
		}
		return ident(ev.Successor, "successor")
	case "result-after-wait":
		if ev.Owner != "" || ev.Note != "" || ev.Successor != "" || !validSHA(ev.ContentHash) {
			return fmt.Errorf("event %s is not a late result", ev.Identity)
		}
		return nil
	default:
		return fmt.Errorf("unknown event kind %q", ev.Kind)
	}
}

func wipDigest(man manifest) (string, error) {
	var b strings.Builder
	n := 0
	for _, entry := range man.Entries {
		if entry.GitState == "committed" {
			continue
		}
		fmt.Fprintf(&b, "%s\n%s\n%s\n", entry.GitState, entry.Path, entry.SHA256)
		n++
	}
	if n == 0 {
		return "", errors.New("manifest has no WIP entries")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:]), nil
}

type gathered struct {
	pickup     bool
	ack        bool
	nextOwner  string
	owners     int
	successors []string
	late       string
	lateCount  int
	replays    int
	refusals   []string
	conflicts  []string
}

func gather(events []event) gathered {
	seen := map[string]string{}
	var g gathered
	for _, ev := range events {
		canon := canonicalEvent(ev)
		if prev, ok := seen[ev.Identity]; ok {
			if prev != canon {
				g.conflicts = append(g.conflicts, fmt.Sprintf("identity %s reused with a different payload", ev.Identity))
			} else {
				g.replays++
			}
			continue
		}
		seen[ev.Identity] = canon
		switch ev.Kind {
		case "pickup":
			g.pickup = true
		case "acknowledgment":
			g.ack = true
		case "next-owner":
			g.owners++
			if g.nextOwner == "" {
				g.nextOwner = ev.Owner
			} else if g.nextOwner != ev.Owner {
				g.conflicts = append(g.conflicts, "conflicting next-owner records")
			}
		case "successor-request":
			g.successors = append(g.successors, ev.Successor)
		case "result-after-wait":
			g.lateCount++
			if g.late == "" {
				g.late = ev.ContentHash
			} else if g.late != ev.ContentHash {
				g.refusals = append(g.refusals, "additional result ignored")
			}
		case "reconnect":
			g.refusals = append(g.refusals, "reconnect does not create a successor or another result")
		case "delayed-running":
			g.refusals = append(g.refusals, "delayed running observation does not replace a verified stop or create another result")
		}
	}
	return g
}

func reduce(doc document) result {
	res := baseResult()
	copySource(&res, doc)
	g := gather(doc.Events)
	res.IgnoredReplayCount = g.replays
	res.Refusals = append(res.Refusals, g.refusals...)
	res.LateResult = g.late
	res.ResultCount = 0
	if g.late != "" {
		res.ResultCount = 1
	}
	if len(g.conflicts) > 0 {
		res.Status = "conflict"
		res.ExitCode = 3
		res.Outcome = "conflict"
		res.ManualDisposition = true
		res.Conflicts = g.conflicts
		res.BlockedCustody = true
		res.BlockedReason = "manual disposition is required"
		res.LateResult = ""
		res.ResultCount = 0
		res.NextAction = "resolve the conflicting synthetic record; no successor or restore was authorized"
		res.ProofClass = "source-synthetic"
		return res
	}
	res.PickupRecorded = g.pickup
	res.AcknowledgmentRecorded = g.ack
	res.NextOwner = g.nextOwner
	reasons := custodyReasons(doc)
	if !g.pickup || !g.ack || g.nextOwner == "" {
		reasons = append(reasons, "explicit pickup, acknowledgment, or next owner is missing")
	}
	restoreOK := len(reasons) == 0
	successor, successorRefusals := decideSuccessor(doc, g, restoreOK)
	res.Refusals = append(res.Refusals, successorRefusals...)
	res.RecordedSuccessor = successor
	if successor != "" {
		res.RecordedSuccessorCount = 1
	}
	res.AcceptedStop = stoppedClean(doc.Predecessor)
	if doc.Observed.ContextPresent && !doc.Observed.ContextChanged {
		res.ExplicitContextProof = "source-record"
	}
	if !restoreOK {
		res.RestoreAuthorized = false
		res.BlockedCustody = true
		res.BlockedReason = strings.Join(reasons, "; ")
		if onlyMissingAccount(reasons) {
			res.Outcome = "partial"
			res.NextAction = "record explicit pickup, acknowledgment, and next owner; do not invent them"
		} else {
			res.Outcome = "blocked-custody"
			res.NextAction = blockedNext("keep the recorded owner and do not restore or assign")
		}
		return res
	}
	res.RestoreAuthorized = true
	res.Outcome = "source-record-accepted"
	if doc.Continuation != "available" || successor == "" {
		res.BlockedCustody = true
		res.BlockedReason = "supported continuation unavailable"
		if doc.Continuation == "available" && successor == "" {
			res.BlockedReason = "supported successor assignment is not recorded"
		}
		res.NextAction = fmt.Sprintf("recorded next owner %s; synthetic restore is authorized; successor action was not performed; %s", res.NextOwner, blockedNext("wait for a reviewed installed drill"))
		return res
	}
	res.BlockedCustody = false
	res.NextAction = fmt.Sprintf("recorded successor %s as evidence only; %s", successor, blockedNext("do not treat this as an assignment"))
	return res
}

func custodyReasons(doc document) []string {
	var reasons []string
	switch doc.Predecessor.ExecutorState {
	case "running":
		reasons = append(reasons, "predecessor is running")
	case "uncertain":
		reasons = append(reasons, "predecessor is uncertain")
	case "unavailable":
		reasons = append(reasons, "predecessor is unavailable")
	case "exited":
		reasons = append(reasons, "exited process is not a verified stop")
	case "stopped":
		if doc.Predecessor.StopAmbiguous {
			reasons = append(reasons, "stop is ambiguous")
		} else if !doc.Predecessor.StopVerified {
			reasons = append(reasons, "verified stop is required")
		}
	}
	if doc.Observed.UID != doc.Predecessor.UID {
		reasons = append(reasons, "wrong source UID")
	}
	if doc.Observed.Head != doc.Predecessor.Head {
		reasons = append(reasons, "wrong source head")
	}
	if !doc.Observed.ContextPresent {
		reasons = append(reasons, "context is missing")
	} else if doc.Observed.ContextChanged {
		reasons = append(reasons, "context hash changed")
	}
	if !doc.Observed.WIPPresent {
		reasons = append(reasons, "WIP is missing")
	} else if doc.Observed.WIPChanged {
		reasons = append(reasons, "WIP hash changed")
	}
	return reasons
}

func onlyMissingAccount(reasons []string) bool {
	return len(reasons) == 1 && reasons[0] == "explicit pickup, acknowledgment, or next owner is missing"
}

func stoppedClean(pred predecessor) bool {
	return pred.ExecutorState == "stopped" && pred.StopVerified && !pred.StopAmbiguous
}

func decideSuccessor(doc document, g gathered, restoreOK bool) (string, []string) {
	if len(g.successors) == 0 {
		return "", nil
	}
	if !restoreOK || !stoppedClean(doc.Predecessor) {
		return "", []string{"successor request ignored"}
	}
	if doc.Continuation != "available" || !doc.Assignment.Authorized || !doc.Assignment.Supported {
		return "", []string{"successor request ignored"}
	}
	first := g.successors[0]
	if doc.Assignment.Successor != first || first == doc.Predecessor.Identity || first == doc.Recipient.Identity {
		return "", []string{"successor request ignored"}
	}
	var refusals []string
	for _, other := range g.successors[1:] {
		if other != first {
			refusals = append(refusals, "duplicate successor does not replace the recorded successor")
		}
	}
	return first, refusals
}

func copySource(res *result, doc document) {
	res.PredecessorIdentity = doc.Predecessor.Identity
	res.RecipientIdentity = doc.Recipient.Identity
	res.SourceUID = doc.Predecessor.UID
	res.SourceHead = doc.Predecessor.Head
	res.ObservedUID = doc.Observed.UID
	res.ObservedHead = doc.Observed.Head
	res.SourceTime = doc.Predecessor.SourceTime
	res.RulesRevision = doc.Predecessor.RulesRevision
	res.ScopeRef = doc.Predecessor.ScopeRef
	res.AuthorityRef = doc.Predecessor.AuthorityRef
	res.ExecutorState = doc.Predecessor.ExecutorState
	res.Holds = append([]string(nil), doc.Excerpt.Holds...)
	res.PendingFindings = append([]string(nil), doc.Excerpt.PendingFindings...)
	res.ConsumedBudget = doc.Excerpt.ConsumedBudget
	res.Attempts = doc.Excerpt.Attempts
	res.AdvisoryChecker = doc.AdvisoryChecker
}

func baseResult() result {
	return result{
		Schema:                schemaName,
		Status:                "reduced",
		ExitCode:              0,
		ProofClass:            "source-synthetic",
		InstalledJourney:      false,
		ExplicitContextProof:  "not-established",
		FullHistoryProof:      "not-claimed",
		NativeImport:          "not-performed",
		TaskClaim:             taskClaimUnchanged,
		CheckerAuthority:      checkerAuthority,
		AdvisoryAuthority:     checkerAuthority,
		SuccessorAction:       "not-performed",
		OriginalsPreserved:    "not-checked",
		Publication:           "not-performed",
		Deployment:            "not-performed",
		InstalledDrill:        "not-performed",
		BlockedOperation:      blockedOperation,
		BlockedOperationOwner: blockedOwner,
		Holds:                 []string{},
		PendingFindings:       []string{},
		Refusals:              []string{},
		Conflicts:             []string{},
		Phases:                []phase{},
	}
}

func refused(msg string) result {
	res := baseResult()
	res.Status = "refused"
	res.ExitCode = 2
	res.Outcome = "refused"
	res.ProofClass = "none"
	res.NextAction = "correct the record; nothing was applied: " + msg
	res.Refusals = []string{msg}
	res.BlockedCustody = true
	res.BlockedReason = msg
	return res
}

func blockedNext(lead string) string {
	return lead + "; blocked operation: " + blockedOperation + "; owner: " + blockedOwner
}

func (r *result) normalize() {
	r.Holds = fill(r.Holds)
	r.PendingFindings = fill(r.PendingFindings)
	r.Refusals = fill(r.Refusals)
	r.Conflicts = fill(r.Conflicts)
	if r.Phases == nil {
		r.Phases = []phase{}
	}
}

func fill(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func canonicalEvent(ev event) string {
	b, _ := json.Marshal(event{
		Kind: ev.Kind, Identity: ev.Identity, SourceTime: ev.SourceTime,
		Owner: ev.Owner, Note: ev.Note, Successor: ev.Successor, ContentHash: ev.ContentHash,
	})
	return string(b)
}

func ident(v, name string) error {
	if len(v) == 0 || len(v) > 80 {
		return fmt.Errorf("%s length is invalid", name)
	}
	for _, c := range v {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' && c != ':' {
			return fmt.Errorf("%s contains unsupported characters", name)
		}
	}
	return nil
}

func short(v, name string) error {
	if len(v) == 0 || len(v) > 120 {
		return fmt.Errorf("%s length is invalid", name)
	}
	return bounded(v, name)
}

func bounded(v, name string) error {
	if len(v) > 200 {
		return fmt.Errorf("%s is too long", name)
	}
	for _, c := range v {
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("%s contains a control character", name)
		}
	}
	return nil
}

func stringsOf(vals []string, name string) error {
	if len(vals) > maxList {
		return fmt.Errorf("%s list is too long", name)
	}
	for _, v := range vals {
		if err := short(v, name); err != nil {
			return err
		}
	}
	return nil
}

func stamp(v, name string) error {
	if strings.Contains(v, ".") {
		return fmt.Errorf("%s fractional seconds are refused", name)
	}
	if _, err := time.Parse(time.RFC3339, v); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func validHead(v string) bool {
	if len(v) != 40 {
		return false
	}
	for _, c := range v {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validSHA(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, c := range v {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := consumeValue(dec); err != nil {
		return err
	}
	return requireEOF(dec)
}

func requireEOF(dec *json.Decoder) error {
	if _, err := dec.Token(); err == io.EOF {
		return nil
	}
	return errors.New("trailing JSON data after root value")
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
