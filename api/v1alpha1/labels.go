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

	// AnnotationRepo is spec.repo. It is an annotation because a repository
	// name can be longer than a label value.
	AnnotationRepo = LabelPrefix + "repo"
	// AnnotationCaller is a summoned session's CallerPolicy, also too long for
	// a label value.
	AnnotationCaller = LabelPrefix + "caller"

	// LabelAppName and LabelManagedBy are the well-known labels; session pods
	// and volumes carry AppNameSession and ManagedByOperator.
	LabelAppName      = "app.kubernetes.io/name"
	LabelManagedBy    = "app.kubernetes.io/managed-by"
	AppNameSession    = "dev-env-session"
	ManagedByOperator = "dev-env-operator"
)
