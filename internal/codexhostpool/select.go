package codexhostpool

import "time"

// Actions are the section 5 result. A named host is returned as use: today's
// thread start runs on that host, and selection does not resume or create.
const (
	ActionUse    = "use"
	ActionResume = "resume"
	ActionCreate = "create"
	ActionReport = "report"
)

// Reason codes are DESIGN-063 section 7, plus four that section 7 does not
// name. not_codex_host is the existing thread-start refusal. not_running and
// not_resumable are the section 4 shape rules (phase, operating mode, pod,
// hold, deletion, archive). not_chosen is a host that lost the section 5
// order without failing a rule.
const (
	ReasonLoginRequired      = "login_required"
	ReasonCallerCannotCreate = "caller_cannot_create"
	ReasonAtCapacity         = "at_capacity"
	ReasonHostBusy           = "host_busy"
	ReasonCallerCannotResume = "caller_cannot_resume"
	ReasonRemoteUnavailable  = "remote_unavailable"
	ReasonProtected          = "protected"
	ReasonRescueBlocked      = "rescue_blocked"
	ReasonPairingUnknown     = "pairing_unknown"
	ReasonUnpaired           = "unpaired"
	ReasonImageOutdated      = "image_outdated"
	ReasonProfileMismatch    = "profile_mismatch"
	ReasonRepoNotReady       = "repo_not_ready"
	ReasonUnpushedWork       = "unpushed_work"
	ReasonFolderTrustPending = "folder_trust_pending"
	ReasonNotCodexHost       = "not_codex_host"
	ReasonNotRunning         = "not_running"
	ReasonNotResumable       = "not_resumable"
	ReasonNotChosen          = "not_chosen"
)

// Caller kinds match the operator API. client is a trusted client, the
// workbench and the v1 pod. A session with Workbench set is the workbench
// session, which may create a host and may not name a profile.
const (
	KindHuman       = "human"
	KindClient      = "client"
	KindSession     = "session"
	KindCoordinator = "coordinator"
)

const (
	PhaseRunning   = "Running"
	PhaseSuspended = "Suspended"

	ModeRunning   = "Running"
	ModeSuspended = "Suspended"

	AgentCodex = "codex"
	ModeRemote = "remote"

	StatePairCodeIssued = "pair-code-issued"
	StateUnpaired       = "unpaired"
	StateLoginRequired  = "login-required"
	StateOffline        = "offline"
	StateStarting       = "starting"
	StateErrored        = "errored"

	RescueCleanAndPushed = "CleanAndPushed"
	RescueVerified       = "Verified"
	RescueFailed         = "Failed"

	// DefaultProfile is the templates' default, used when a human or trusted
	// client omits the profile, and for a workbench session, which may not
	// name one.
	DefaultProfile = "full"
	// DefaultMaxRunningPerCell and DefaultMaxRunning are the section 6 flag
	// defaults. A non-positive cap on Input selects them.
	DefaultMaxRunningPerCell = 1
	DefaultMaxRunning        = 2

	// projectionLead is the SyncCodexAccess test: the access expiry is more
	// than five minutes ahead.
	projectionLead = 5 * time.Minute
)

// Host is one Codex host as facts. Select does not read the cluster.
//
// UID is the session UID. This slice does not write a lease, so the choice
// does not read it. SpecRepo is the host's default repository: the fit test
// only when the request names none. SuspendedBy is who suspended the host;
// eligibility does not depend on it.
type Host struct {
	Name            string
	UID             string
	Agent           string
	Mode            string
	Phase           string
	OperatingMode   string
	Deletion        bool
	PodReady        bool
	HoldPod         bool
	ResolvedProfile string
	ReadyRepos      []string
	SpecRepo        string
	// HandsOff is the configured hands-off list. ProtectedAnnotation is
	// dev-env.haynesops.com/protected=true.
	HandsOff            bool
	ProtectedAnnotation bool
	RescueFailed        bool
	RemovalBlocked      bool
	RescueState         string
	RescueSuperseded    bool
	UnpushedRefs        int
	OmittedRefs         int
	RemoteState         string
	LeaseHolder         string
	LeaseExpiry         time.Time
	ImageCurrent        bool
	SuspendedBy         string
	SuspendedAt         time.Time
	LastActivity        time.Time
	Pool                bool
	Archived            bool
	Parent              string
	TrustedRepos        []string
}

// Projection is the one current keeper Codex login fact.
type Projection struct {
	Decodes   bool
	ExpiresAt time.Time
}

// Caller is who is asking. ParentRef is the identity stored on a child's
// spec.parent. A coordinator may resume only a host whose parent equals it.
// Workbench is the session-pod workbench (isWorkbenchSession).
type Caller struct {
	Kind      string
	Name      string
	Profile   string
	ParentRef string
	Workbench bool
}

// Request is the profile and repository to fit, and an optional named host.
// A named host does not enter selection.
type Request struct {
	Profile    string
	Repository string
	NamedHost  string
}

// Input is everything Select may see. Now is the clock.
type Input struct {
	Hosts             []Host
	Projection        Projection
	Caller            Caller
	Request           Request
	Now               time.Time
	MaxRunningPerCell int
	MaxRunning        int
}

// Skip is one host selection did not choose.
type Skip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Result is the section 5 object. Ready is false, and Reason is
// folder_trust_pending, when a chosen host is suitable or eligible and story
// 695 has not recorded trust for the repository.
type Result struct {
	Host    string `json:"host,omitempty"`
	Action  string `json:"action,omitempty"`
	Ready   bool   `json:"ready"`
	Reason  string `json:"reason,omitempty"`
	Skipped []Skip `json:"skipped,omitempty"`
}

type judged struct {
	index  int
	host   Host
	reason string
	filler bool
}

// Select returns the section 5 choice. Until slice 6, a suspended host's
// pairing test is its last remote-control state. The durable record from
// ruling E is not an input yet.
func Select(in Input) Result {
	if in.Request.NamedHost != "" {
		return Result{Host: in.Request.NamedHost, Action: ActionUse}
	}
	if !projectionCurrent(in.Projection, in.Now) {
		return Result{Reason: ReasonLoginRequired}
	}

	requested := requestedProfile(in.Caller, in.Request)
	repo := in.Request.Repository
	perCell, fleetCap := caps(in)
	rows := make([]judged, 0, len(in.Hosts))
	var suitable, busy, eligible, fillers []judged
	for i, h := range in.Hosts {
		reason, okUse, okResume := judge(h, in.Caller, requested, repo, in.Now)
		row := judged{index: i, host: h, reason: reason}
		if inCell(h, requested) && !okUse && reason != ReasonHostBusy {
			row.filler = true
		}
		switch {
		case okUse:
			suitable = append(suitable, row)
		case reason == ReasonHostBusy:
			busy = append(busy, row)
		case okResume:
			eligible = append(eligible, row)
		}
		if row.filler {
			fillers = append(fillers, row)
		}
		rows = append(rows, row)
	}

	cellN := count(in.Hosts, func(h Host) bool { return inCell(h, requested) })
	fleetN := count(in.Hosts, inFleet)
	room := fleetN+1 <= fleetCap && cellN+1 <= perCell

	if len(suitable) > 0 {
		best := pickRunning(suitable)
		ready, why := readiness(best.host, repo)
		return finish(rows, best.index, ActionUse, ready, why)
	}
	if len(busy) > 0 {
		return finish(rows, -1, "", false, ReasonHostBusy)
	}
	if len(eligible) > 0 && room {
		best := pickSuspended(eligible)
		ready, why := readiness(best.host, repo)
		return finish(rows, best.index, ActionResume, ready, why)
	}
	if len(fillers) > 0 && cellN >= perCell {
		best := pickRunning(fillers)
		return finish(rows, best.index, ActionReport, false, best.reason)
	}
	if (len(eligible) > 0 && !room) || cellN >= perCell || fleetN >= fleetCap {
		return finish(rows, -1, "", false, ReasonAtCapacity)
	}
	if !mayCreate(in.Caller) {
		return finish(rows, -1, "", false, ReasonCallerCannotCreate)
	}
	return finish(rows, -1, ActionCreate, false, "")
}

func finish(rows []judged, chosen int, action string, ready bool, reason string) Result {
	res := Result{Action: action, Ready: ready, Reason: reason}
	if chosen >= 0 {
		res.Host = rows[chosen].host.Name
	}
	for _, row := range rows {
		if row.index == chosen {
			continue
		}
		why := row.reason
		if why == "" {
			if reason == ReasonAtCapacity {
				why = ReasonAtCapacity
			} else {
				why = ReasonNotChosen
			}
		}
		res.Skipped = append(res.Skipped, Skip{Name: row.host.Name, Reason: why})
	}
	return res
}

// judge's first matching reason is the skip reason. Safety (rescue, then the
// hands-off list and the protected annotation) is recorded before profile, so
// a protected host is not described as only the wrong profile.
func judge(h Host, c Caller, requested, repo string, now time.Time) (reason string, suitable, eligible bool) {
	if h.Agent != AgentCodex || h.Mode != ModeRemote {
		return ReasonNotCodexHost, false, false
	}
	if rescueBlocked(h) {
		return ReasonRescueBlocked, false, false
	}
	if h.HandsOff || h.ProtectedAnnotation {
		return ReasonProtected, false, false
	}
	if !profileFits(c, h, requested) {
		return ReasonProfileMismatch, false, false
	}
	if !repoReady(h, repo) {
		return ReasonRepoNotReady, false, false
	}
	if h.Phase == PhaseRunning && !h.Deletion && !h.Archived {
		if h.OperatingMode != ModeRunning || !h.PodReady || h.HoldPod {
			return ReasonNotRunning, false, false
		}
		if why := runningRemoteReason(h.RemoteState); why != "" {
			return why, false, false
		}
		if !h.ImageCurrent {
			return ReasonImageOutdated, false, false
		}
		if leaseHeldByOther(h, c.Name, now) {
			return ReasonHostBusy, false, false
		}
		return "", true, false
	}
	if h.Deletion || h.Archived || h.Phase != PhaseSuspended || h.OperatingMode != ModeSuspended {
		return ReasonNotResumable, false, false
	}
	if !rescueClean(h) {
		return ReasonUnpushedWork, false, false
	}
	// Until slice 6. After it, eligibility uses the durable pairing record
	// with a current keeper login, which this slice does not take as a fact.
	if h.RemoteState != StatePairCodeIssued {
		return ReasonPairingUnknown, false, false
	}
	if !callerMayResume(c, h) {
		return ReasonCallerCannotResume, false, false
	}
	return "", false, true
}

func projectionCurrent(p Projection, now time.Time) bool {
	return p.Decodes && !p.ExpiresAt.IsZero() && p.ExpiresAt.After(now.Add(projectionLead))
}

func requestedProfile(c Caller, r Request) string {
	// A workbench session may not name a profile. The host it starts is the
	// default profile, so that is the cell it fits.
	if c.Kind == KindSession && c.Workbench {
		return DefaultProfile
	}
	if c.Kind == KindSession || c.Kind == KindCoordinator {
		return c.Profile
	}
	if r.Profile != "" {
		return r.Profile
	}
	return DefaultProfile
}

func profileFits(c Caller, h Host, requested string) bool {
	if !profileMatches(h.ResolvedProfile, requested) {
		return false
	}
	// An ordinary session fits a host of its own profile. Ancestry does not
	// pull a host out of another profile: a dev caller does not fit a full
	// host, and a full caller does not fit the dev host. A workbench session
	// is aimed at the default profile above, not at its own profile label.
	if c.Kind == KindSession && !c.Workbench {
		return c.Profile != "" && c.Profile == h.ResolvedProfile
	}
	return true
}

func profileMatches(hostProfile, requested string) bool {
	return hostProfile != "" && hostProfile == requested
}

// fitRepo is the repository the host must have Ready. The request's
// repository wins. When the request names none, the host's spec.repo is the
// default, which is what resolveCwd launches. spec.repo is not a fit test
// when the request names a repository.
func fitRepo(h Host, requested string) string {
	if requested != "" {
		return requested
	}
	return h.SpecRepo
}

func repoReady(h Host, requested string) bool {
	repo := fitRepo(h, requested)
	if repo == "" {
		return false
	}
	return contains(h.ReadyRepos, repo)
}

func mayCreate(c Caller) bool {
	switch c.Kind {
	case KindHuman, KindClient:
		return true
	case KindSession:
		return c.Workbench
	default:
		return false
	}
}

func callerMayResume(c Caller, h Host) bool {
	if c.Kind != KindCoordinator {
		return true
	}
	return c.ParentRef != "" && h.Parent == c.ParentRef
}

func rescueBlocked(h Host) bool {
	return h.RescueState == RescueFailed || h.RescueFailed || h.RemovalBlocked
}

func rescueClean(h Host) bool {
	if h.RescueSuperseded || h.UnpushedRefs != 0 || h.OmittedRefs != 0 {
		return false
	}
	return h.RescueState == RescueCleanAndPushed || h.RescueState == RescueVerified
}

func runningRemoteReason(state string) string {
	switch state {
	case StatePairCodeIssued:
		return ""
	case StateUnpaired:
		return ReasonUnpaired
	default:
		return ReasonRemoteUnavailable
	}
}

func leaseHeldByOther(h Host, caller string, now time.Time) bool {
	// Absent, expired, or already this caller's: not busy. A heartbeat bumps
	// the session version and leaves the lease like that, so the choice stays
	// on this host. Another caller's unexpired lease is the only reason to
	// move on. The compare-and-swap retry is the later slice that writes the
	// lease; this function only reads the lease it was given.
	return h.LeaseHolder != "" && h.LeaseHolder != caller && h.LeaseExpiry.After(now)
}

func readiness(h Host, requested string) (bool, string) {
	if contains(h.TrustedRepos, fitRepo(h, requested)) {
		return true, ""
	}
	return false, ReasonFolderTrustPending
}

func caps(in Input) (perCell, fleet int) {
	perCell, fleet = in.MaxRunningPerCell, in.MaxRunning
	if perCell <= 0 {
		perCell = DefaultMaxRunningPerCell
	}
	if fleet <= 0 {
		fleet = DefaultMaxRunning
	}
	return perCell, fleet
}

func inCell(h Host, requested string) bool {
	return runningUnprotected(h) && profileMatches(h.ResolvedProfile, requested)
}

func inFleet(h Host) bool {
	return runningUnprotected(h)
}

func runningUnprotected(h Host) bool {
	if h.Agent != AgentCodex || h.Mode != ModeRemote {
		return false
	}
	if h.Phase != PhaseRunning || h.Deletion || h.Archived {
		return false
	}
	if rescueBlocked(h) || h.HandsOff || h.ProtectedAnnotation {
		return false
	}
	return true
}

func count(hosts []Host, keep func(Host) bool) int {
	n := 0
	for _, h := range hosts {
		if keep(h) {
			n++
		}
	}
	return n
}

func pickRunning(rows []judged) judged {
	best := rows[0]
	for _, row := range rows[1:] {
		if runningBetter(row.host, best.host) {
			best = row
		}
	}
	return best
}

func runningBetter(a, b Host) bool {
	if a.Pool != b.Pool {
		return a.Pool
	}
	if a.LastActivity.After(b.LastActivity) {
		return true
	}
	if b.LastActivity.After(a.LastActivity) {
		return false
	}
	return a.Name < b.Name
}

func pickSuspended(rows []judged) judged {
	best := rows[0]
	for _, row := range rows[1:] {
		if suspendedLonger(row.host, best.host) {
			best = row
		}
	}
	return best
}

func suspendedLonger(a, b Host) bool {
	switch {
	case a.SuspendedAt.IsZero() && b.SuspendedAt.IsZero():
		return a.Name < b.Name
	case a.SuspendedAt.IsZero():
		return false
	case b.SuspendedAt.IsZero():
		return true
	case a.SuspendedAt.Before(b.SuspendedAt):
		return true
	case b.SuspendedAt.Before(a.SuspendedAt):
		return false
	default:
		return a.Name < b.Name
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
