package maintenancecontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var errStop = errors.New("stop walk")

var (
	idPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	namePattern     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	shaPattern      = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	versionPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	labelKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	labelValPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	resourcePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9./_-]{0,127}$`)
	symptomPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	modelPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{2,127}$`)
)

var (
	actionsAllowed = map[string]bool{"inspect": true, "reconcile": true, "postcheck": true, "rollback": true, "cleanup": true}
	evidenceKinds  = map[string]bool{"work-package": true, "baseline": true, "postcheck": true, "authorization": true, "rollback": true, "note": true}
	ownerStates    = map[string]bool{"active": true, "stale": true, "failed": true, "stopped": true, "lost": true, "unknown": true}
	opStates       = map[string]bool{"planned": true, "active": true, "completed": true, "rolled_back": true, "failed": true, "held": true}
	terminalStates = map[string]bool{"completed": true, "rolled_back": true, "failed": true, "held": true}
	severities     = map[string]bool{"info": true, "warning": true, "critical": true}
	healthStates   = map[string]bool{"healthy": true, "degraded": true, "unknown": true}
)

type collector struct {
	fields []FieldError
}

func (c *collector) add(path, message string) {
	c.fields = append(c.fields, FieldError{Path: path, Message: message})
}

func (c *collector) err() error {
	if len(c.fields) == 0 {
		return nil
	}
	out := make([]FieldError, len(c.fields))
	copy(out, c.fields)
	return &InvalidInputError{Fields: out}
}

// Check parses data and evaluates it at now. now is the injected clock.
func Check(data []byte, now time.Time) (Result, error) {
	doc, err := parse(data, now)
	if err != nil {
		return Result{}, err
	}
	return evaluate(doc, now.UTC()), nil
}

// CanonicalDigest is the sha256 of the parsed envelope. It covers the
// envelope only, so a later snapshot is not a payload change. The same
// attempt with a different digest conflicts and is not merged.
func CanonicalDigest(data []byte, now time.Time) (string, error) {
	doc, err := parse(data, now)
	if err != nil {
		return "", err
	}
	return doc.digest, nil
}

func parse(data []byte, now time.Time) (parsed, error) {
	if now.IsZero() {
		return parsed{}, &InvalidInputError{Fields: []FieldError{{Path: "clock", Message: "an injected clock is required"}}}
	}
	now = now.UTC()
	var c collector
	if len(data) == 0 {
		c.add("$", "document is empty")
		return parsed{}, c.err()
	}
	if len(data) > MaxDocumentBytes {
		c.add("$", fmt.Sprintf("document is %d bytes; the proposed limit is %d", len(data), MaxDocumentBytes))
		return parsed{}, c.err()
	}
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) || !utf8.Valid(data) {
		c.add("$", "document must be UTF-8 without a byte order mark")
		return parsed{}, c.err()
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := walk(dec, "$", &c); err != nil && !errors.Is(err, errStop) {
		c.add("$", "invalid JSON: "+cleanJSON(err))
	}
	if err := c.err(); err != nil {
		return parsed{}, err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		c.add("$", "trailing JSON after the first document")
		return parsed{}, c.err()
	}
	var doc checkDoc
	body := json.NewDecoder(bytes.NewReader(data))
	body.DisallowUnknownFields()
	if err := body.Decode(&doc); err != nil {
		if key, ok := unknownField(err); ok {
			c.add(key, "unrecognized field \""+key+"\"")
			return parsed{}, c.err()
		}
		c.add("$", "invalid JSON: "+cleanJSON(err))
		return parsed{}, c.err()
	}
	if body.More() {
		c.add("$", "trailing JSON after the first document")
		return parsed{}, c.err()
	}
	when := validate(&doc, now, &c)
	if err := c.err(); err != nil {
		return parsed{}, err
	}
	normalize(&doc, when)
	sum, err := canonicalBytes(doc.Envelope)
	if err != nil {
		return parsed{}, err
	}
	digest := sha256.Sum256(sum)
	return parsed{doc: doc, when: when, digest: "sha256:" + hex.EncodeToString(digest[:])}, nil
}

func walk(dec *json.Decoder, path string, c *collector) error {
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
		seen := map[string]bool{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("%s has a non-string key", path)
			}
			child := key
			if path != "$" {
				child = path + "." + key
			}
			if seen[key] {
				c.add(child, "duplicate key")
				return errStop
			}
			seen[key] = true
			if selfAuth(key) {
				c.add(child, "unrecognized field \""+key+"\" cannot grant authority or defer an alert")
				return errStop
			}
			if err := walk(dec, child, c); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("%s is not closed", path)
		}
	case '[':
		i := 0
		for dec.More() {
			if err := walk(dec, fmt.Sprintf("%s[%d]", path, i), c); err != nil {
				return err
			}
			i++
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("%s is not closed", path)
		}
	default:
		return fmt.Errorf("%s has an unexpected delimiter", path)
	}
	return nil
}

func selfAuth(key string) bool {
	switch strings.ToLower(key) {
	case "authorized", "authorised", "selfauthorized", "self_authorized":
		return true
	default:
		return false
	}
}

func unknownField(err error) (string, bool) {
	const marker = "unknown field "
	msg := err.Error()
	i := strings.Index(msg, marker)
	if i < 0 {
		return "", false
	}
	rest := msg[i+len(marker):]
	rest = strings.Trim(rest, "\"")
	if cut := strings.IndexAny(rest, "\" "); cut >= 0 {
		rest = rest[:cut]
	}
	return rest, rest != ""
}

func cleanJSON(err error) string {
	msg := strings.TrimPrefix(err.Error(), "json: ")
	return msg
}

func validate(doc *checkDoc, now time.Time, c *collector) instants {
	env := &doc.Envelope
	if env.ContractVersion != ContractVersion {
		c.add("envelope.contractVersion", "must be "+ContractVersion)
	}
	c.id("envelope.operationId", env.OperationID)
	c.id("envelope.attemptId", env.AttemptID)
	if env.PredecessorAttemptID != "" {
		c.id("envelope.predecessorAttemptId", env.PredecessorAttemptID)
		if env.PredecessorAttemptID == env.AttemptID {
			c.add("envelope.predecessorAttemptId", "must name a different attempt")
		}
	}
	c.https("envelope.workPackageURL", env.WorkPackageURL)
	if env.Role.Version != RoleVersion {
		c.add("envelope.role.version", "must be "+RoleVersion)
	}
	c.digest("envelope.role.contentDigest", env.Role.ContentDigest)
	validateRepository(env, c)
	c.serviceName("envelope.session.namespace", env.Session.Namespace)
	c.serviceName("envelope.session.name", env.Session.Name)
	c.id("envelope.session.uid", env.Session.UID)
	c.id("envelope.claim.reference", env.Claim.Reference)
	c.id("envelope.claim.ownerUID", env.Claim.OwnerUID)
	c.id("envelope.executor.identity", env.Executor.Identity)
	c.list("envelope.custody.handoffEvidence", env.Custody.HandoffEvidence, c.https)
	validateProvenance(env.Provenance, c)
	validateActions(env.Actions, c)
	validateTargets(env.Targets, c)
	if !opStates[env.State] {
		c.add("envelope.state", "must be planned, active, completed, rolled_back, failed, or held")
	}
	when := validateTimes(doc, now, c)
	validateBaselines(env, c)
	validateExpected(env, c)
	validateObservation(env, c)
	validateRollback(env, c)
	validateTerminal(env, c)
	c.evidenceList("envelope.evidence", env.Evidence)
	validateSnapshot(doc, c)
	validatePrior(doc, c)
	return when
}

func validateRepository(env *envelope, c *collector) {
	if !identityPattern.MatchString(env.Repository.Identity) || strings.Contains(env.Repository.Identity, "*") {
		c.add("envelope.repository.identity", "must be one repository identity without a wildcard")
	}
	if len(env.Repository.DesiredRevisions) == 0 || len(env.Repository.DesiredRevisions) > MaxEntries {
		c.add("envelope.repository.desiredRevisions", fmt.Sprintf("must contain 1 to %d revisions", MaxEntries))
	}
	seen := map[string]bool{}
	for i, rev := range env.Repository.DesiredRevisions {
		path := fmt.Sprintf("envelope.repository.desiredRevisions[%d]", i)
		if !shaPattern.MatchString(rev) {
			c.add(path, "must be a 40 or 64 character lowercase hex revision")
		}
		if seen[rev] {
			c.add(path, "duplicate revision")
		}
		seen[rev] = true
	}
	ad := env.Repository.Adapter
	c.token("envelope.repository.adapter.name", ad.Name)
	c.version("envelope.repository.adapter.version", ad.Version)
	c.digest("envelope.repository.adapter.digest", ad.Digest)
	if ad.Runtime != "applicable" && ad.Runtime != "not-applicable" {
		c.add("envelope.repository.adapter.runtime", "must be applicable or not-applicable")
	}
	if ad.Name != "" && lastSegment(env.Repository.Identity) != ad.Name {
		c.add("envelope.repository.identity", "must end in the adapter name")
	}
}

func validateProvenance(p provenance, c *collector) {
	c.token("envelope.provenance.requested.provider", p.Requested.Provider)
	c.model("envelope.provenance.requested.model", p.Requested.Model, false)
	if p.Requested.Effort != "" {
		c.version("envelope.provenance.requested.effort", p.Requested.Effort)
	}
	if p.Requested.State != "" && p.Requested.State != "configured" {
		c.add("envelope.provenance.requested.state", "must be empty or configured")
	}
	if p.Actual.Provider != "" && p.Actual.Provider != ProvenanceUnknown {
		c.token("envelope.provenance.actual.provider", p.Actual.Provider)
	}
	if p.Actual.Model != "" {
		c.model("envelope.provenance.actual.model", p.Actual.Model, true)
	}
	if p.Actual.State != "" && p.Actual.State != ProvenanceUnknown && !versionPattern.MatchString(p.Actual.State) {
		c.add("envelope.provenance.actual.state", "must be empty, Unknown, or a single token")
	}
	if p.Actual.Effort != "" {
		c.version("envelope.provenance.actual.effort", p.Actual.Effort)
	}
}

func validateActions(a actions, c *collector) {
	if len(a.Allowed) == 0 || len(a.Allowed) > MaxEntries {
		c.add("envelope.actions.allowed", fmt.Sprintf("must contain 1 to %d actions", MaxEntries))
	}
	seen := map[string]bool{}
	for i, action := range a.Allowed {
		path := fmt.Sprintf("envelope.actions.allowed[%d]", i)
		if !actionsAllowed[action] {
			c.add(path, "must be inspect, reconcile, postcheck, rollback, or cleanup")
		}
		if seen[action] {
			c.add(path, "duplicate action")
		}
		seen[action] = true
	}
	if len(a.AuthorizationEvidence) > MaxEntries {
		c.add("envelope.actions.authorizationEvidence", fmt.Sprintf("exceeds the proposed limit of %d", MaxEntries))
	}
	for i, ref := range a.AuthorizationEvidence {
		c.https(fmt.Sprintf("envelope.actions.authorizationEvidence[%d]", i), ref)
	}
}

func validateTargets(t targets, c *collector) {
	if len(t.Services) == 0 || len(t.Services) > MaxEntries {
		c.add("envelope.targets.services", fmt.Sprintf("must contain 1 to %d services", MaxEntries))
	}
	seen := map[string]bool{}
	for i, svc := range t.Services {
		path := fmt.Sprintf("envelope.targets.services[%d]", i)
		c.serviceName(path, svc)
		if seen[svc] {
			c.add(path, "duplicate service")
		}
		seen[svc] = true
	}
	if len(t.Dependencies) > MaxEntries {
		c.add("envelope.targets.dependencies", fmt.Sprintf("exceeds the proposed limit of %d", MaxEntries))
	}
	seen = map[string]bool{}
	for i, dep := range t.Dependencies {
		path := fmt.Sprintf("envelope.targets.dependencies[%d]", i)
		c.serviceName(path, dep)
		if seen[dep] {
			c.add(path, "duplicate dependency")
		}
		seen[dep] = true
	}
}

func validateTimes(doc *checkDoc, now time.Time, c *collector) instants {
	env := &doc.Envelope
	var when instants
	when.start = c.bound("envelope.activity.start", env.Activity.Start)
	when.expiry = c.bound("envelope.activity.expiry", env.Activity.Expiry)
	when.deadline = c.bound("envelope.task.deadline", env.Task.Deadline)
	when.authLimit = c.bound("envelope.authorization.limitExpiry", env.Authorization.LimitExpiry)
	when.observation = c.observed("envelope.observation.observedAt", env.Observation.ObservedAt, now)
	when.progress = c.observed("envelope.observation.progressAt", env.Observation.ProgressAt, now)
	when.snapshotObservation = c.observed("snapshot.owner.observedAt", doc.Snapshot.Owner.ObservedAt, now)
	if !when.start.IsZero() && !when.observation.IsZero() && when.observation.Before(when.start) {
		c.add("envelope.observation.observedAt", "is before the activity start")
	}
	if !when.start.IsZero() && !when.snapshotObservation.IsZero() && when.snapshotObservation.Before(when.start) {
		c.add("snapshot.owner.observedAt", "is before the activity start")
	}
	if !when.observation.IsZero() && !when.progress.IsZero() && when.progress.After(when.observation.Add(ClockTolerance)) {
		c.add("envelope.observation.progressAt", "is after the owner observation beyond the 5s tolerance")
	}
	if !when.start.IsZero() && !when.expiry.IsZero() {
		if !when.expiry.After(when.start) {
			c.add("envelope.activity.expiry", "must be after the activity start")
		} else if when.expiry.Sub(when.start) > ActivityAPIMaximum {
			c.add("envelope.activity.expiry", "exceeds the existing 8h Activity maximum; this source contract does not extend it")
		}
	}
	if !when.start.IsZero() && !when.deadline.IsZero() && !when.deadline.After(when.start) {
		c.add("envelope.task.deadline", "must be after the activity start")
	}
	if !when.start.IsZero() && !when.authLimit.IsZero() && !when.authLimit.After(when.start) {
		c.add("envelope.authorization.limitExpiry", "must be after the activity start")
	}
	if !when.expiry.IsZero() && !when.deadline.IsZero() && when.expiry.After(when.deadline) {
		c.add("envelope.activity.expiry", "is after the task deadline; the shorter limit wins")
	}
	if !when.expiry.IsZero() && !when.authLimit.IsZero() && when.expiry.After(when.authLimit) {
		c.add("envelope.activity.expiry", "is after the authorization limit; the shorter limit wins")
	}
	if !when.start.IsZero() && when.start.After(now.Add(ClockTolerance)) {
		c.add("envelope.activity.start", "is in the future beyond the 5s clock tolerance")
	}
	for i := range env.Baselines {
		c.observed(fmt.Sprintf("envelope.baselines[%d].observedAt", i), env.Baselines[i].ObservedAt, now)
	}
	for i := range doc.Snapshot.Alerts {
		c.observed(fmt.Sprintf("snapshot.alerts[%d].startedAt", i), doc.Snapshot.Alerts[i].StartedAt, now)
	}
	if terminalStates[env.State] {
		when.receipt = c.observed("envelope.terminal.receiptAt", env.Terminal.ReceiptAt, now)
	} else if env.Terminal.ReceiptAt != "" {
		c.observed("envelope.terminal.receiptAt", env.Terminal.ReceiptAt, now)
	}
	hb := env.Observation.HeartbeatIntervalSeconds
	if hb < 1 || hb > int(MaxStaleness/time.Second) {
		c.add("envelope.observation.heartbeatIntervalSeconds", "must be from 1 to 120 seconds; 120 is a proposed source-contract default")
	}
	return when
}

func validateBaselines(env *envelope, c *collector) {
	if len(env.Baselines) > MaxEntries {
		c.add("envelope.baselines", fmt.Sprintf("exceeds the proposed limit of %d", MaxEntries))
	}
	seen := map[string]bool{}
	for i, b := range env.Baselines {
		path := fmt.Sprintf("envelope.baselines[%d]", i)
		c.serviceName(path+".service", b.Service)
		if seen[b.Service] {
			c.add(path+".service", "duplicate baseline")
		}
		seen[b.Service] = true
		if !healthStates[b.Health] {
			c.add(path+".health", "must be healthy, degraded, or unknown")
		}
		if !shaPattern.MatchString(b.Revision) {
			c.add(path+".revision", "must be a 40 or 64 character lowercase hex revision")
		}
		switch b.Health {
		case "degraded":
			c.symptom(path+".symptom", b.Symptom)
			if !severities[b.Severity] {
				c.add(path+".severity", "must be info, warning, or critical")
			}
		case "healthy", "unknown":
			if b.Symptom != "" || b.Severity != "" {
				c.add(path+".symptom", "must be empty unless health is degraded")
			}
		}
	}
}

func validateExpected(env *envelope, c *collector) {
	if len(env.ExpectedAlerts) == 0 || len(env.ExpectedAlerts) > MaxEntries {
		c.add("envelope.expectedAlerts", fmt.Sprintf("must contain 1 to %d predicates", MaxEntries))
	}
	for i, alert := range env.ExpectedAlerts {
		path := fmt.Sprintf("envelope.expectedAlerts[%d]", i)
		c.serviceName(path+".service", alert.Service)
		if alert.Service != "" && !contains(env.Targets.Services, alert.Service) {
			c.add(path+".service", "must be one of the target services")
		}
		c.labels(path+".labels", alert.Labels)
		c.resource(path+".resource", alert.Resource)
		if !severities[alert.Severity] {
			c.add(path+".severity", "must be info, warning, or critical")
		}
		if severities[alert.Severity] && severities[env.Task.Impact.MaxSeverity] && rank(alert.Severity) > rank(env.Task.Impact.MaxSeverity) {
			c.add(path+".severity", "is above the recorded impact severity")
		}
		c.symptom(path+".symptom", alert.Symptom)
		if alert.ImpactIntervalSeconds < 1 || alert.ImpactIntervalSeconds > env.Task.Impact.WindowSeconds {
			c.add(path+".impactIntervalSeconds", "must fit inside the recorded impact window")
		}
	}
}

func validateObservation(env *envelope, c *collector) {
	c.id("envelope.observation.ownerUID", env.Observation.OwnerUID)
	if !ownerStates[env.Observation.State] {
		c.add("envelope.observation.state", "must be active, stale, failed, stopped, lost, or unknown")
	}
	if !shaPattern.MatchString(env.Observation.Revision) {
		c.add("envelope.observation.revision", "must be a 40 or 64 character lowercase hex revision")
	}
	c.id("envelope.observation.attemptId", env.Observation.AttemptID)
	window := env.Task.Impact.WindowSeconds
	if window < 1 || window > int(ActivityAPIMaximum/time.Second) {
		c.add("envelope.task.impact.windowSeconds", "must be from 1 second through the existing 8h Activity maximum")
	}
	if env.Task.Impact.MaxServices < 1 || env.Task.Impact.MaxServices > MaxEntries {
		c.add("envelope.task.impact.maxServices", fmt.Sprintf("must be from 1 to %d", MaxEntries))
	}
	if !severities[env.Task.Impact.MaxSeverity] {
		c.add("envelope.task.impact.maxSeverity", "must be info, warning, or critical")
	}
	c.id("envelope.activity.uid", env.Activity.UID)
}

func validateRollback(env *envelope, c *collector) {
	c.token("envelope.rollback.trigger", env.Rollback.Trigger)
	c.id("envelope.rollback.owner", env.Rollback.Owner)
	if len(env.Rollback.Scope) == 0 || len(env.Rollback.Scope) > MaxEntries {
		c.add("envelope.rollback.scope", "must name the services a recorded rollback may touch")
	}
	for i, svc := range env.Rollback.Scope {
		path := fmt.Sprintf("envelope.rollback.scope[%d]", i)
		c.serviceName(path, svc)
		if svc != "" && !contains(env.Targets.Services, svc) {
			c.add(path, "must be one of the target services")
		}
	}
}

func validateTerminal(env *envelope, c *collector) {
	term := env.Terminal
	if terminalStates[env.State] {
		if term.Outcome != env.State {
			c.add("envelope.terminal.outcome", "must equal the terminal state")
		}
		if term.ActivityCleanup != "succeeded" && term.ActivityCleanup != "failed" {
			c.add("envelope.terminal.activityCleanup", "must be succeeded or failed")
		}
		if (env.State == "completed" || env.State == "rolled_back") && len(term.PostcheckEvidence) == 0 {
			c.add("envelope.terminal.postcheckEvidence", "is required for completion or rollback")
		}
		if (env.State == "failed" || env.State == "held") && len(term.UnresolvedSymptoms) == 0 {
			c.add("envelope.terminal.unresolvedSymptoms", "is required when the operation fails or is held")
		}
	} else if term.Outcome != "" || term.ActivityCleanup != "" || term.ReceiptAt != "" || len(term.PostcheckEvidence) != 0 || len(term.UnresolvedSymptoms) != 0 {
		c.add("envelope.terminal.outcome", "must be empty while the operation is not terminal")
	}
	if len(term.PostcheckEvidence) > MaxEntries {
		c.add("envelope.terminal.postcheckEvidence", fmt.Sprintf("exceeds the proposed limit of %d", MaxEntries))
	}
	for i, ref := range term.PostcheckEvidence {
		c.https(fmt.Sprintf("envelope.terminal.postcheckEvidence[%d]", i), ref)
	}
	if len(term.UnresolvedSymptoms) > MaxEntries {
		c.add("envelope.terminal.unresolvedSymptoms", fmt.Sprintf("exceeds the proposed limit of %d", MaxEntries))
	}
	for i, symptom := range term.UnresolvedSymptoms {
		c.symptom(fmt.Sprintf("envelope.terminal.unresolvedSymptoms[%d]", i), symptom)
	}
}

func validateSnapshot(doc *checkDoc, c *collector) {
	snap := &doc.Snapshot
	if len(snap.Alerts) > MaxEntries {
		c.add("snapshot.alerts", fmt.Sprintf("exceeds the proposed limit of %d", MaxEntries))
	}
	for i, alert := range snap.Alerts {
		path := fmt.Sprintf("snapshot.alerts[%d]", i)
		c.serviceName(path+".service", alert.Service)
		c.labels(path+".labels", alert.Labels)
		c.resource(path+".resource", alert.Resource)
		if !severities[alert.Severity] {
			c.add(path+".severity", "must be info, warning, or critical")
		}
		c.symptom(path+".symptom", alert.Symptom)
	}
	c.id("snapshot.owner.uid", snap.Owner.UID)
	c.id("snapshot.owner.sessionUID", snap.Owner.SessionUID)
	c.id("snapshot.owner.claimReference", snap.Owner.ClaimReference)
	if !ownerStates[snap.Owner.State] {
		c.add("snapshot.owner.state", "must be active, stale, failed, stopped, lost, or unknown")
	}
	c.id("snapshot.owner.attemptId", snap.Owner.AttemptID)
	if !shaPattern.MatchString(snap.Revision) {
		c.add("snapshot.revision", "must be a 40 or 64 character lowercase hex revision")
	}
	if snap.Metadata != "available" && snap.Metadata != "unavailable" {
		c.add("snapshot.metadata", "must be available or unavailable")
	}
	if snap.Launch != "ok" && snap.Launch != "denied" && snap.Launch != "uncertain" {
		c.add("snapshot.launch", "must be ok, denied, or uncertain")
	}
	if !actionsAllowed[snap.RequestedAction] {
		c.add("snapshot.requestedAction", "must be inspect, reconcile, postcheck, rollback, or cleanup")
	}
	if snap.Impact.AffectedServices < 0 || snap.Impact.AffectedServices > MaxCounter {
		c.add("snapshot.impact.affectedServices", "is outside the proposed counter bounds")
	}
	if snap.Impact.IntervalSeconds < 0 || snap.Impact.IntervalSeconds > MaxCounter {
		c.add("snapshot.impact.intervalSeconds", "is outside the proposed counter bounds")
	}
	if !severities[snap.Impact.Severity] {
		c.add("snapshot.impact.severity", "must be info, warning, or critical")
	}
	validateAdapter(doc, c)
}

func validateAdapter(doc *checkDoc, c *collector) {
	ad := doc.Envelope.Repository.Adapter
	snap := doc.Snapshot.Adapter
	if snap.Repository != ad.Name {
		c.add("snapshot.adapter.repository", "must match the envelope adapter name")
	}
	if snap.Provider != doc.Envelope.Provenance.Requested.Provider {
		c.add("snapshot.adapter.provider", "must match the requested provider; this contract does not switch providers")
	}
	switch ad.Runtime {
	case "applicable":
		if snap.RuntimeHealth != "healthy" && snap.RuntimeHealth != "degraded" && snap.RuntimeHealth != "unknown" {
			c.add("snapshot.adapter.runtimeHealth", "must be healthy, degraded, or unknown when runtime is applicable")
		}
		if snap.Reconciliation != "ready" && snap.Reconciliation != "not-ready" && snap.Reconciliation != "unknown" {
			c.add("snapshot.adapter.reconciliation", "must be ready, not-ready, or unknown when runtime is applicable")
		}
		if !shaPattern.MatchString(snap.FluxRevision) {
			c.add("snapshot.adapter.fluxRevision", "must be a lowercase hex revision when runtime is applicable")
		}
		if snap.GatusStatus != "healthy" && snap.GatusStatus != "degraded" && snap.GatusStatus != "unknown" {
			c.add("snapshot.adapter.gatusStatus", "must be healthy, degraded, or unknown when runtime is applicable")
		}
		if snap.RenovateTier != "patch" && snap.RenovateTier != "minor" && snap.RenovateTier != "major" && snap.RenovateTier != "one-way" && snap.RenovateTier != "unknown" {
			c.add("snapshot.adapter.renovateTier", "must be patch, minor, major, one-way, or unknown")
		}
		if snap.SourceCI != "" || snap.NativeReview != "" {
			c.add("snapshot.adapter.sourceCI", "a platform adapter does not carry source-only review fields")
		}
	case "not-applicable":
		if snap.RuntimeHealth != "NotApplicable" {
			c.add("snapshot.adapter.runtimeHealth", "a source-only adapter marks runtimeHealth NotApplicable")
		}
		if snap.Reconciliation != "NotApplicable" {
			c.add("snapshot.adapter.reconciliation", "a source-only adapter marks reconciliation NotApplicable")
		}
		if snap.FluxRevision != "" || snap.GatusStatus != "" || snap.RenovateTier != "" {
			c.add("snapshot.adapter.fluxRevision", "a source-only adapter does not carry Flux, Gatus, or Renovate observations")
		}
		if snap.SourceCI != "" && snap.SourceCI != "success" && snap.SourceCI != "failed" && snap.SourceCI != "unknown" {
			c.add("snapshot.adapter.sourceCI", "must be success, failed, unknown, or empty")
		}
		if snap.NativeReview != "" && snap.NativeReview != "recorded" && snap.NativeReview != "missing" && snap.NativeReview != "unknown" {
			c.add("snapshot.adapter.nativeReview", "must be recorded, missing, unknown, or empty")
		}
	}
}

func validatePrior(doc *checkDoc, c *collector) {
	if doc.PriorAttempt == nil {
		return
	}
	c.id("priorAttempt.attemptId", doc.PriorAttempt.AttemptID)
	c.digest("priorAttempt.payloadDigest", doc.PriorAttempt.PayloadDigest)
	if doc.PriorAttempt.AttemptID != "" && doc.PriorAttempt.AttemptID != doc.Envelope.AttemptID {
		c.add("priorAttempt.attemptId", "must equal the envelope attempt id; a later attempt uses predecessorAttemptId")
	}
}

func (c *collector) id(path, value string) {
	if !idPattern.MatchString(value) || strings.ContainsAny(value, "*[](){}|^$") {
		c.add(path, "must be a single-line identifier of at most 128 characters")
	}
}

func (c *collector) token(path, value string) {
	if !namePattern.MatchString(value) {
		c.add(path, "must be a lowercase token")
	}
	c.rejectWildcard(path, value)
}

func (c *collector) version(path, value string) {
	if !versionPattern.MatchString(value) || strings.ContainsAny(value, "\r\n\t") {
		c.add(path, "must be a single-line version token")
	}
}

func (c *collector) digest(path, value string) {
	if !digestPattern.MatchString(value) {
		c.add(path, "must be sha256: and 64 lowercase hex characters")
	}
}

func (c *collector) serviceName(path, value string) {
	if !namePattern.MatchString(value) {
		c.add(path, "must be a lowercase service or name token")
	}
	c.rejectWildcard(path, value)
}

func (c *collector) symptom(path, value string) {
	if !symptomPattern.MatchString(value) {
		c.add(path, "must be a lowercase symptom token")
	}
	c.rejectWildcard(path, value)
}

func (c *collector) resource(path, value string) {
	if !resourcePattern.MatchString(value) {
		c.add(path, "must be a resource identity without a wildcard")
	}
	c.rejectWildcard(path, value)
}

func (c *collector) rejectWildcard(path, value string) {
	if value == "cluster" || strings.ContainsAny(value, `*\[](){}|^$`) {
		c.add(path, "substring, regex, and cluster wildcard cannot justify deferral")
	}
}

func (c *collector) model(path, value string, allowUnknown bool) {
	if allowUnknown && value == ProvenanceUnknown {
		return
	}
	if !modelPattern.MatchString(value) || !containsDigit(value) {
		c.add(path, "must be a full model id, not an alias")
	}
}

func (c *collector) https(path, value string) {
	if len(value) == 0 || len(value) > MaxURLBytes || strings.ContainsAny(value, "\r\n\t *") {
		c.add(path, "must be one https URL of at most 512 characters")
		return
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || strings.Contains(u.Host, "*") {
		c.add(path, "must be an https URL without user info or a wildcard")
	}
}

func (c *collector) labels(path string, labels map[string]string) {
	if len(labels) == 0 || len(labels) > MaxLabels {
		c.add(path, fmt.Sprintf("must contain 1 to %d labels, including alertname", MaxLabels))
		return
	}
	if _, ok := labels["alertname"]; !ok {
		c.add(path, "must include alertname")
	}
	for key, value := range labels {
		if !labelKeyPattern.MatchString(key) {
			c.add(path+"."+key, "has an invalid label key")
		}
		if !labelValPattern.MatchString(value) {
			c.add(path+"."+key, "has an invalid label value")
		}
		c.rejectWildcard(path+"."+key, value)
	}
}

func (c *collector) list(path string, values []string, check func(string, string)) {
	if len(values) > MaxEntries {
		c.add(path, fmt.Sprintf("exceeds the proposed limit of %d", MaxEntries))
	}
	for i, value := range values {
		check(fmt.Sprintf("%s[%d]", path, i), value)
	}
}

func (c *collector) evidenceList(path string, values []evidenceRef) {
	if len(values) > MaxEntries {
		c.add(path, fmt.Sprintf("exceeds the proposed limit of %d", MaxEntries))
	}
	for i, value := range values {
		base := fmt.Sprintf("%s[%d]", path, i)
		c.https(base+".url", value.URL)
		if !evidenceKinds[value.Kind] {
			c.add(base+".kind", "must be work-package, baseline, postcheck, authorization, rollback, or note")
		}
	}
}

func (c *collector) bound(path, value string) time.Time {
	if value == "" {
		c.add(path, "is required")
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		c.add(path, "must be an RFC3339 timestamp")
		return time.Time{}
	}
	return t.UTC()
}

func (c *collector) observed(path, value string, now time.Time) time.Time {
	t := c.bound(path, value)
	if !t.IsZero() && t.After(now.Add(ClockTolerance)) {
		c.add(path, "is in the future beyond the 5s clock tolerance")
	}
	return t
}

func normalize(doc *checkDoc, when instants) {
	env := &doc.Envelope
	env.Activity.Start = formatTime(when.start)
	env.Activity.Expiry = formatTime(when.expiry)
	env.Task.Deadline = formatTime(when.deadline)
	env.Authorization.LimitExpiry = formatTime(when.authLimit)
	env.Observation.ObservedAt = formatTime(when.observation)
	env.Observation.ProgressAt = formatTime(when.progress)
	doc.Snapshot.Owner.ObservedAt = formatTime(when.snapshotObservation)
	if !when.receipt.IsZero() {
		env.Terminal.ReceiptAt = formatTime(when.receipt)
	}
	for i := range env.Baselines {
		if t, err := time.Parse(time.RFC3339Nano, env.Baselines[i].ObservedAt); err == nil {
			env.Baselines[i].ObservedAt = formatTime(t)
		}
	}
	for i := range doc.Snapshot.Alerts {
		if t, err := time.Parse(time.RFC3339Nano, doc.Snapshot.Alerts[i].StartedAt); err == nil {
			doc.Snapshot.Alerts[i].StartedAt = formatTime(t)
		}
	}
	env.Custody.HandoffEvidence = empty(env.Custody.HandoffEvidence)
	env.Actions.Allowed = empty(env.Actions.Allowed)
	env.Actions.AuthorizationEvidence = empty(env.Actions.AuthorizationEvidence)
	env.Targets.Services = empty(env.Targets.Services)
	env.Targets.Dependencies = empty(env.Targets.Dependencies)
	env.Repository.DesiredRevisions = empty(env.Repository.DesiredRevisions)
	if env.Baselines == nil {
		env.Baselines = []baseline{}
	}
	if env.ExpectedAlerts == nil {
		env.ExpectedAlerts = []expectedAlert{}
	}
	env.Rollback.Scope = empty(env.Rollback.Scope)
	env.Terminal.PostcheckEvidence = empty(env.Terminal.PostcheckEvidence)
	env.Terminal.UnresolvedSymptoms = empty(env.Terminal.UnresolvedSymptoms)
	if env.Evidence == nil {
		env.Evidence = []evidenceRef{}
	}
	if doc.Snapshot.Alerts == nil {
		doc.Snapshot.Alerts = []observedAlert{}
	}
	for i := range env.ExpectedAlerts {
		if env.ExpectedAlerts[i].Labels == nil {
			env.ExpectedAlerts[i].Labels = map[string]string{}
		}
	}
	for i := range doc.Snapshot.Alerts {
		if doc.Snapshot.Alerts[i].Labels == nil {
			doc.Snapshot.Alerts[i].Labels = map[string]string{}
		}
	}
}

func empty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func canonicalBytes(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func containsDigit(value string) bool {
	for _, r := range value {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

func lastSegment(value string) string {
	if i := strings.LastIndex(value, "/"); i >= 0 {
		return value[i+1:]
	}
	return value
}

func rank(severity string) int {
	switch severity {
	case "info":
		return 1
	case "warning":
		return 2
	case "critical":
		return 3
	default:
		return 100
	}
}
