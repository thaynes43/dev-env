package v1alpha1

// The labels and annotations the operator puts on a session's pod and volume
// (D-44). Clients select on the labels (the network policies, the broker's
// standing policies, `agent-run fleet`); no template profile may set them.
const (
	// ProjectCanaryFinalizer protects the deterministic Session at atomic Create,
	// before the private-history latch is established. This slice never releases it.
	ProjectCanaryFinalizer = LabelPrefix + "project-canary-retained"
	LabelProjectCanary     = LabelPrefix + "project-canary"
	// LabelPrefix starts every label and annotation key the operator owns.
	LabelPrefix = "dev-env.haynesops.com/"

	// LabelSession is the session's name, on its pod and its volume.
	LabelSession = LabelPrefix + "session"
	// LabelAgent is spec.agent.
	LabelAgent = LabelPrefix + "agent"
	// LabelMode is spec.mode.
	LabelMode = LabelPrefix + "mode"
	// LabelSize is the size class the pod was built with.
	LabelSize = LabelPrefix + "size"
	// LabelProfile is the profile the pod was built with: spec.profile, or the
	// templates' default. The egress tiers select on it (D-18, D-24).
	LabelProfile = LabelPrefix + "profile"
	// LabelRevision is the template revision the pod was built from
	// (DESIGN-001 5.2). Pods only.
	LabelRevision = LabelPrefix + "revision"
	// LabelLane is a summoned session's lane (DESIGN-001 3.7).
	LabelLane = LabelPrefix + "lane"
	// LabelHold marks a session's rescue pod (D-55), value "true": it runs
	// `agentd hold`, holds the session's volume for the operator's rescue, and
	// runs no agent. Pods only.
	LabelHold = LabelPrefix + "hold"

	// LabelGrant is an AccessGrant's name, on what the broker makes for it: the
	// grant's ServiceAccount and bindings, and its network policy (D-61). The
	// broker deletes an object only when it carries this label with the
	// grant's name. Such objects also carry LabelSession, the requester.
	LabelGrant = LabelPrefix + "grant"

	// AnnotationRepo is spec.repo. It is an annotation because a repository
	// name can be longer than a label value.
	AnnotationRepo = LabelPrefix + "repo"
	// AnnotationProjectSnapshot is authored only by the accepted catalog resolver.
	AnnotationProjectSnapshot = LabelPrefix + "project-snapshot"
	// AnnotationCaller is a summoned session's CallerPolicy, also too long for
	// a label value.
	AnnotationCaller = LabelPrefix + "caller"
	// AnnotationSuspendedBy says who suspended the session (D-60): the API's
	// caller name or the operator's own marker (idle timer, terminal task
	// reclaim or summon-close). The API removes it on a resume.
	AnnotationSuspendedBy = LabelPrefix + "suspended-by"
	// AnnotationReapedBy says who reaped a summoned session: the API caller,
	// recorded once by the authorized reap route for spec.caller only. It is
	// the stop; the leader closes the outcome before deleting (DESIGN-064 6.6,
	// OP779 fix round 1). A delete without this marker is unattributed.
	AnnotationReapedBy = LabelPrefix + "reaped-by"
	// AnnotationResumedAt is when the API last resumed the session (RFC 3339,
	// D-60). The idle timer counts from it too, so a resume that lands while
	// an idle suspend's rescue still runs, and keeps the old pod, is not
	// suspended again at once.
	AnnotationResumedAt = LabelPrefix + "resumed-at"
	// AnnotationHomeExpiry is Tom's owner override on a private Session's HOME
	// expiry (DESIGN-025 section 9): HomeExpiryHold or HomeExpiryReleaseNow.
	// No dev-env code writes it; an admission policy (H7) refuses it to every
	// ServiceAccount. A hold also blocks the stop request (section 4), and the
	// operator reads any value but release-now as a hold there.
	AnnotationHomeExpiry = LabelPrefix + "home-expiry"
	// HomeExpiryHold and HomeExpiryReleaseNow are AnnotationHomeExpiry's values.
	HomeExpiryHold       = "hold"
	HomeExpiryReleaseNow = "release-now"

	// LabelAppName and LabelManagedBy are the well-known labels; session pods
	// and volumes carry AppNameSession and ManagedByOperator.
	LabelAppName      = "app.kubernetes.io/name"
	LabelManagedBy    = "app.kubernetes.io/managed-by"
	AppNameSession    = "dev-env-session"
	ManagedByOperator = "dev-env-operator"
)

// The labels and annotations the /v1 API puts on an AgentSession when it creates
// one (D-46). They are the API's own bookkeeping: agents cannot write
// AgentSession objects (DESIGN-001 6.11), so a value here is the API's.
const (
	// LabelIdempotencyKey is the create's idempotency key (DESIGN-001 3.4, V-03).
	// It is a label so the API finds a caller's earlier session with a label
	// selector (D-39); the API then matches spec.parent, so a key is scoped to its
	// caller.
	LabelIdempotencyKey = LabelPrefix + "idempotency-key"
	// LabelStandby marks the operator's standby remote session (value "true"),
	// the one the standby runnable keeps ready (Story110 T25, plan 03). The
	// runnable finds its session by this label and touches no other.
	LabelStandby = LabelPrefix + "standby"
	// AnnotationRequestHash is the SHA-256 of the create request that carried the
	// idempotency key. A repeat of the key with a different request is refused.
	AnnotationRequestHash = LabelPrefix + "request-hash"
	// AnnotationSummonGeneration is a summoned session's generation, a decimal
	// integer the API writes in the create (DESIGN-064 5.9): 1 for a key's first
	// session, else the newest earlier session's generation plus one. The newest
	// session of a key is the one with the highest; a session without it counts
	// as 0.
	AnnotationSummonGeneration = LabelPrefix + "summon-generation"
	// LabelDepth is how many sessions stand between this one and the human or
	// client that started the chain: 0 for a session Tom or a client created, 1
	// for its child, 2 for a grandchild. A session at depth 2 creates no children
	// (DESIGN-001 3.4: two levels deep).
	LabelDepth = LabelPrefix + "depth"
)

// The finalizer, label and annotations a scheduled session carries (DESIGN-054
// "Occurrence lifecycle"). The executor sets all of them in the session's
// atomic Create; nothing changes them afterwards. The releaser in the
// AgentSession reconciler reads them and is the one place that removes the
// finalizer (TestOnlyTheGuardDeletes).
const (
	// ScheduledWorkFinalizer holds a scheduled session until its occurrence's
	// terminal state and result are in the ScheduledWork's status, so a session
	// with the derived name never vanishes unrecorded.
	ScheduledWorkFinalizer = LabelPrefix + "scheduled-work-retained"
	// LabelScheduledWork is the full UID of the ScheduledWork that dispatched
	// the session. The releaser checks it against the item it reads by name,
	// because a Get by UID does not exist: a mismatch is an item deleted and
	// recreated under the same name.
	LabelScheduledWork = LabelPrefix + "scheduled-work"
	// AnnotationScheduledWorkName is the name of that ScheduledWork. The item
	// lives in the operator's own namespace (dev-env-system, beside the
	// Reminders), not the session's: the releaser reads it there
	// (Reconciler.ScheduledWorkNamespace), and the executor (S3b) and the API
	// (S5) must create it there too.
	AnnotationScheduledWorkName = LabelPrefix + "scheduled-work-name"
	// AnnotationScheduledWorkOccurrence is the occurrence's instant (its `at`)
	// in Unix seconds, decimal.
	AnnotationScheduledWorkOccurrence = LabelPrefix + "scheduled-work-occurrence"
	// AnnotationScheduledWorkStartDeadline is the latest time the session's
	// agent may first launch, RFC 3339 UTC ending in Z: the due time plus grace,
	// or retriedAt plus grace for a retried occurrence. agentd enforces it
	// (DESIGN-054 S3c).
	AnnotationScheduledWorkStartDeadline = LabelPrefix + "scheduled-work-start-deadline"
)
