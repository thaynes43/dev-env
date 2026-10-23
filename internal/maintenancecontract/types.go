package maintenancecontract

import (
	"strings"
	"time"
)

// FieldError names one invalid input field.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// InvalidInputError is malformed input. It is returned before a decision.
type InvalidInputError struct {
	Fields []FieldError `json:"fields"`
}

func (e *InvalidInputError) Error() string {
	var b strings.Builder
	b.WriteString("invalid maintenance input")
	for _, field := range e.Fields {
		b.WriteByte('\n')
		b.WriteString(field.Path)
		b.WriteString(": ")
		b.WriteString(field.Message)
	}
	return b.String()
}

// Predicate is one explained correlation failure.
type Predicate struct {
	Name   string `json:"name"`
	Effect string `json:"effect"`
	Detail string `json:"detail"`
}

// Condition is evidence that stays visible and is not itself a grant.
type Condition struct {
	Kind    string `json:"kind"`
	Service string `json:"service,omitempty"`
	Detail  string `json:"detail"`
}

// MatchedIDs are the operation facts a result is talking about.
type MatchedIDs struct {
	OperationID string   `json:"operationId"`
	AttemptID   string   `json:"attemptId"`
	Revision    string   `json:"revision"`
	Services    []string `json:"services"`
	Alerts      []int    `json:"alerts"`
}

// ModelOut is requested or actual provider provenance.
type ModelOut struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	State    string `json:"state"`
}

// ProvenanceOut keeps requested and actual provenance separate.
type ProvenanceOut struct {
	Requested ModelOut `json:"requested"`
	Actual    ModelOut `json:"actual"`
}

// Result is one pure correlation. Retry is always false. The authorization
// flags are always false, including when Decision is candidate.
type Result struct {
	Decision                       string        `json:"decision"`
	MatchedIDs                     MatchedIDs    `json:"matchedIds"`
	FailedPredicates               []Predicate   `json:"failedPredicates"`
	VisibleConditions              []Condition   `json:"visibleConditions"`
	Provenance                     ProvenanceOut `json:"provenance"`
	NextReevaluationDeadline       string        `json:"nextReevaluationDeadline"`
	Retry                          bool          `json:"retry"`
	Replay                         bool          `json:"replay"`
	UnverifiedInput                bool          `json:"unverifiedInput"`
	OfflineEvidence                string        `json:"offlineEvidence"`
	CandidateIsOperationalDeferral bool          `json:"candidateIsOperationalDeferral"`
	AuthorizationGranted           bool          `json:"authorizationGranted"`
	Notice                         string        `json:"notice"`
}

type checkDoc struct {
	Envelope     envelope      `json:"envelope"`
	Snapshot     snapshot      `json:"snapshot"`
	PriorAttempt *priorAttempt `json:"priorAttempt,omitempty"`
}

type priorAttempt struct {
	AttemptID     string `json:"attemptId"`
	PayloadDigest string `json:"payloadDigest"`
}

type envelope struct {
	ContractVersion      string          `json:"contractVersion"`
	OperationID          string          `json:"operationId"`
	AttemptID            string          `json:"attemptId"`
	PredecessorAttemptID string          `json:"predecessorAttemptId"`
	WorkPackageURL       string          `json:"workPackageURL"`
	Role                 roleRef         `json:"role"`
	Repository           repository      `json:"repository"`
	Session              sessionRef      `json:"session"`
	Claim                claimRef        `json:"claim"`
	Executor             executorRef     `json:"executor"`
	Custody              custody         `json:"custody"`
	Provenance           provenance      `json:"provenance"`
	Actions              actions         `json:"actions"`
	Targets              targets         `json:"targets"`
	Activity             activityRef     `json:"activity"`
	Task                 taskRef         `json:"task"`
	Authorization        authorization   `json:"authorization"`
	Baselines            []baseline      `json:"baselines"`
	ExpectedAlerts       []expectedAlert `json:"expectedAlerts"`
	Observation          observation     `json:"observation"`
	Rollback             rollback        `json:"rollback"`
	Terminal             terminal        `json:"terminal"`
	Evidence             []evidenceRef   `json:"evidence"`
	State                string          `json:"state"`
}

type roleRef struct {
	Version       string `json:"version"`
	ContentDigest string `json:"contentDigest"`
}

type repository struct {
	Identity         string     `json:"identity"`
	DesiredRevisions []string   `json:"desiredRevisions"`
	Adapter          adapterRef `json:"adapter"`
}

type adapterRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	Runtime string `json:"runtime"`
}

type sessionRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

type claimRef struct {
	Reference string `json:"reference"`
	OwnerUID  string `json:"ownerUID"`
}

type executorRef struct {
	Identity string `json:"identity"`
}

type custody struct {
	HandoffEvidence []string `json:"handoffEvidence"`
}

type provenance struct {
	Requested modelIn `json:"requested"`
	Actual    modelIn `json:"actual"`
}

type modelIn struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	State    string `json:"state"`
}

type actions struct {
	Allowed               []string `json:"allowed"`
	AuthorizationEvidence []string `json:"authorizationEvidence"`
}

type targets struct {
	Services     []string `json:"services"`
	Dependencies []string `json:"dependencies"`
}

type activityRef struct {
	UID    string `json:"uid"`
	Start  string `json:"start"`
	Expiry string `json:"expiry"`
}

type taskRef struct {
	Deadline string `json:"deadline"`
	Impact   impact `json:"impact"`
}

type impact struct {
	MaxServices   int    `json:"maxServices"`
	MaxSeverity   string `json:"maxSeverity"`
	WindowSeconds int    `json:"windowSeconds"`
}

type authorization struct {
	LimitExpiry string `json:"limitExpiry"`
}

type baseline struct {
	Service    string `json:"service"`
	ObservedAt string `json:"observedAt"`
	Health     string `json:"health"`
	Revision   string `json:"revision"`
	Symptom    string `json:"symptom"`
	Severity   string `json:"severity"`
}

type expectedAlert struct {
	Service               string            `json:"service"`
	Labels                map[string]string `json:"labels"`
	Resource              string            `json:"resource"`
	Severity              string            `json:"severity"`
	Symptom               string            `json:"symptom"`
	ImpactIntervalSeconds int               `json:"impactIntervalSeconds"`
}

type observation struct {
	OwnerUID                 string `json:"ownerUID"`
	ObservedAt               string `json:"observedAt"`
	ProgressAt               string `json:"progressAt"`
	HeartbeatIntervalSeconds int    `json:"heartbeatIntervalSeconds"`
	State                    string `json:"state"`
	Revision                 string `json:"revision"`
	AttemptID                string `json:"attemptId"`
}

type rollback struct {
	Trigger string   `json:"trigger"`
	Owner   string   `json:"owner"`
	Scope   []string `json:"scope"`
}

type terminal struct {
	Outcome            string   `json:"outcome"`
	PostcheckEvidence  []string `json:"postcheckEvidence"`
	ActivityCleanup    string   `json:"activityCleanup"`
	UnresolvedSymptoms []string `json:"unresolvedSymptoms"`
	ReceiptAt          string   `json:"receiptAt"`
}

type evidenceRef struct {
	URL  string `json:"url"`
	Kind string `json:"kind"`
}

type snapshot struct {
	Alerts          []observedAlert `json:"alerts"`
	Owner           ownerSnap       `json:"owner"`
	Revision        string          `json:"revision"`
	Metadata        string          `json:"metadata"`
	Launch          string          `json:"launch"`
	RequestedAction string          `json:"requestedAction"`
	Impact          impactSnap      `json:"impact"`
	Adapter         adapterSnap     `json:"adapter"`
}

type observedAlert struct {
	Service   string            `json:"service"`
	Labels    map[string]string `json:"labels"`
	Resource  string            `json:"resource"`
	Severity  string            `json:"severity"`
	Symptom   string            `json:"symptom"`
	StartedAt string            `json:"startedAt"`
}

type ownerSnap struct {
	UID            string `json:"uid"`
	SessionUID     string `json:"sessionUID"`
	ClaimReference string `json:"claimReference"`
	State          string `json:"state"`
	ObservedAt     string `json:"observedAt"`
	AttemptID      string `json:"attemptId"`
}

type impactSnap struct {
	AffectedServices int    `json:"affectedServices"`
	Severity         string `json:"severity"`
	IntervalSeconds  int    `json:"intervalSeconds"`
}

type adapterSnap struct {
	Repository     string `json:"repository"`
	Provider       string `json:"provider"`
	RuntimeHealth  string `json:"runtimeHealth"`
	Reconciliation string `json:"reconciliation"`
	FluxRevision   string `json:"fluxRevision"`
	GatusStatus    string `json:"gatusStatus"`
	RenovateTier   string `json:"renovateTier"`
	SourceCI       string `json:"sourceCI"`
	NativeReview   string `json:"nativeReview"`
}

type instants struct {
	start, expiry, deadline, authLimit time.Time
	observation, progress              time.Time
	snapshotObservation                time.Time
	receipt                            time.Time
}

type parsed struct {
	doc    checkDoc
	when   instants
	digest string
}
