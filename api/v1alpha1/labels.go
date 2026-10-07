package v1alpha1

// The labels and annotations the operator puts on a session's pod and volume
// (D-44). Clients select on the labels (the network policies, the broker's
// standing policies, `agent-run fleet`); no template profile may set them.
const (
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

	// AnnotationRepo is spec.repo. It is an annotation because a repository
	// name can be longer than a label value.
	AnnotationRepo = LabelPrefix + "repo"
	// AnnotationCaller is a summoned session's CallerPolicy, also too long for
	// a label value.
	AnnotationCaller = LabelPrefix + "caller"
	// AnnotationSuspendedBy says who suspended the session (D-60): "idle-timer"
	// for the operator's idle timer, else the API caller. The API removes it on
	// a resume.
	AnnotationSuspendedBy = LabelPrefix + "suspended-by"
	// AnnotationResumedAt is when the API last resumed the session (RFC 3339,
	// D-60). The idle timer counts from it too, so a resume that lands while
	// an idle suspend's rescue still runs, and keeps the old pod, is not
	// suspended again at once.
	AnnotationResumedAt = LabelPrefix + "resumed-at"

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
	// AnnotationRequestHash is the SHA-256 of the create request that carried the
	// idempotency key. A repeat of the key with a different request is refused.
	AnnotationRequestHash = LabelPrefix + "request-hash"
	// LabelDepth is how many sessions stand between this one and the human or
	// client that started the chain: 0 for a session Tom or a client created, 1
	// for its child, 2 for a grandchild. A session at depth 2 creates no children
	// (DESIGN-001 3.4: two levels deep).
	LabelDepth = LabelPrefix + "depth"
)
