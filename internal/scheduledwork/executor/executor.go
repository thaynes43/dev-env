// Package executor is DESIGN-054's scheduled-work executor (S3b, first part):
// a leader-only runnable in the operator, behind --enable-scheduled-work, that
// dispatches each due occurrence of a ScheduledWork at most once and records
// what it did in the item's status.
//
// The ScheduledWork status is the state of record. Every decision is one
// compare-and-set status write on the item's resourceVersion, so a restart, a
// failover or two leaders that overlap read the state back and repeat nothing:
//
//   - an occurrence is claimed (Pending to Claimed) by such a write, after the
//     global caps and the live-owner proof allow it;
//   - its session is created through the API's fixed-name entry
//     (apiserver.Server.CreateScheduledSession), under the derived name
//     scheduledwork.SessionName, so a second create of the same occurrence
//     cannot succeed: it adopts the first one's session, and a session of that
//     name that this occurrence did not create makes it Ambiguous;
//   - a Claimed occurrence without a recorded session is checked against the
//     API server (uncached) before any new create. The session carries the
//     scheduled-work finalizer from its atomic Create, and the releaser in the
//     AgentSession reconciler (S3a) records the occurrence's outcome before it
//     lifts it, so an absent session with no recorded outcome was never
//     created: it is created again while the occurrence is inside its grace,
//     and Skipped after.
//
// The executor reads ScheduledWorks and sessions with the uncached API reader
// only and starts no informer or watch, so the operator starts without the
// ScheduledWork CRD. It never suspends, deletes or changes a session; a
// headless session's end (Running and the terminal states) is the releaser's.
//
// The authority basis and the dispatch templates come in through Authorizer
// and TemplateResolver. The operator wires Sources (S3b2) for both: the
// dispatch templates ConfigMap and the owner session, read uncached, decided
// by internal/scheduledwork/authority. Without the ConfigMap, or without a
// basis its template admits, an occurrence parks and nothing is dispatched.
//
// Pages (S4b, paging.go) go through Pager: a transition that pages records
// its intent in its own status write, and a later step creates the keyed
// page Reminder and records it. A page never holds a state change.
package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/reminders"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
	"github.com/thaynes43/dev-env/internal/scheduledwork/pages"
)

// The defaults of DESIGN-054.
const (
	// DefaultEvery is how often the executor looks.
	DefaultEvery = 30 * time.Second
	// DefaultHeadlessGrace and DefaultInteractiveGrace are how late an
	// occurrence may still start: beyond them it is Skipped, never run late
	// without a person.
	DefaultHeadlessGrace    = 15 * time.Minute
	DefaultInteractiveGrace = 2 * time.Hour
	// DefaultMaxHeadless and DefaultMaxInteractive are the global caps on
	// unfinished occurrences, checked before the claim.
	DefaultMaxHeadless    = 2
	DefaultMaxInteractive = 1
	// DefaultDecisionWindow is how long a Parked occurrence waits for a human
	// approve, retry or cancel, unless the item's next occurrence comes due
	// first.
	DefaultDecisionWindow = 24 * time.Hour
	// DefaultIdleSuspendAfter suspends an interactive session nobody joined,
	// so it frees the interactive slot, when its template sets no value.
	DefaultIdleSuspendAfter = 4 * time.Hour
)

// maxSteps bounds the status writes one item gets in one pass; the next pass
// goes on from what is stored.
const maxSteps = 16

// maxMissed is the schema's bound on one Skipped record's missed count.
const maxMissed = 100000

// The reasons an occurrence's record gives (DESIGN-054 "Occurrence lifecycle").
const (
	// The reasons a due occurrence stays Pending before its claim.
	ReasonAtCapacity      = "at capacity"
	ReasonOwnerUnproven   = "the owner session is not proven live"
	ReasonAuthorityUnread = "the authority could not be checked"

	ReasonPreviousRunning         = "previous still running"
	ReasonNotAfter                = "notAfter passed"
	ReasonCancelledBeforeClaim    = scheduledwork.ReasonCancelledBeforeClaim
	ReasonCancelledAtRevalidation = scheduledwork.ReasonCancelledAtRevalidation
	ReasonCancelledWhileParked    = scheduledwork.ReasonCancelledWhileParked
	ReasonNotCreated              = "not created before grace ended"
	ReasonSpecChanged             = "spec changed after claim"
	ReasonParkedExpired           = "parked, not approved in time"
	ReasonCollision               = "a session of the derived name exists that this occurrence did not create"
	ReasonNoAuthority             = "no standing authority"
	ReasonReplanned               = "the schedule was edited before it was claimed"
	// ReasonNoTemplateDigest parks an occurrence whose decision or template
	// carries no digest: the two cannot be matched, so nothing is built.
	ReasonNoTemplateDigest = "no template digest to match the decision against"
)

// Basis is the authority an occurrence dispatches on (DESIGN-054
// "Authority"). The values are also what a template admits.
type Basis string

const (
	// BasisNone is no basis: the occurrence parks.
	BasisNone Basis = ""
	// BasisHuman is a human owner who wrote the current spec; the session is
	// created as the fixed client identity.
	BasisHuman Basis = "human"
	// BasisApproved is a human approval of the current spec's hash; the
	// session is created as the fixed client identity.
	BasisApproved Basis = "approved"
	// BasisSession is a live session owner who wrote the current spec; the
	// session is created as its child.
	BasisSession Basis = "session"
)

// Authority is an Authorizer's answer for one item at one time.
type Authority struct {
	// Basis is the authority the occurrence may dispatch on, or BasisNone.
	Basis Basis
	// Owner is the live owner session, for BasisSession.
	Owner *scheduledwork.SessionRef
	// Unproven is an owner session whose liveness cannot be proven now. It
	// never grants authority. Before the claim the occurrence stays Pending
	// and parks when its grace ends; after the claim it parks.
	Unproven bool
	// Reason says why there is no basis, for the Parked record.
	Reason string
	// TemplateDigest is the digest of the template the basis was decided
	// under (Template.Digest). Revalidation check 3 builds the session only
	// from a template of the same, non-empty digest: it tries again when they
	// differ, so an edit between the two reads never builds from a template
	// the decision did not see, and it parks when either is empty
	// (ReasonNoTemplateDigest). An Authority with no basis needs none.
	TemplateDigest string
}

// Authorizer decides an item's authority basis at the due time, from what is
// true now, never from anything stored at creation (DESIGN-054 R7). Sources
// implements it.
type Authorizer interface {
	// Authorize returns w's basis at now. An error is a failed read, not a
	// refusal: the executor tries again while the occurrence is inside its
	// grace.
	Authorize(ctx context.Context, w *v1alpha1.ScheduledWork, now time.Time) (Authority, error)
}

// Template is one dispatch template: what a session of it runs, fixed by the
// template and never raised by the request (DESIGN-054 "Dispatch templates").
type Template struct {
	Agent, Model, Effort, Size, Profile string
	// Repos are the repositories the template allows.
	Repos []string
	// MaxDuration bounds a headless task; it is required for headless work.
	MaxDuration time.Duration
	// IdleSuspendAfter suspends an interactive session once idle this long;
	// zero is DefaultIdleSuspendAfter.
	IdleSuspendAfter time.Duration
	// Admits are the bases the template admits.
	Admits []Basis
	// Modes are the modes of work the template serves: headless,
	// interactive, or both (v1alpha1.ScheduledWorkHeadless and
	// ScheduledWorkInteractive). An item whose mode is not among them parks.
	Modes []string
	// Digest identifies the template's content (authority.TemplateDigest).
	// It is required: check 3 builds only when it equals the Authority's
	// TemplateDigest, parks the occurrence (ReasonNoTemplateDigest) when
	// either is empty, and tries again when they differ.
	Digest string
}

// TemplateResolver reads a dispatch template at the due time. Sources
// implements it over the dev-env-scheduled-work ConfigMap.
type TemplateResolver interface {
	// Resolve returns the named template. A missing or invalid templates
	// document, or a missing or invalid template in it, is a *Refusal, which
	// parks the occurrence. Only a failed read (the ConfigMap could not be
	// read) is a plain error, tried again while the occurrence is inside its
	// grace.
	Resolve(ctx context.Context, name string) (Template, error)
}

// Refusal is a definite refusal from a TemplateResolver.
type Refusal struct {
	Reason string
}

func (r *Refusal) Error() string { return r.Reason }

// Creator creates an occurrence's session through the API's fixed-name entry:
// apiserver.Server.CreateScheduledSession implements it.
type Creator interface {
	CreateScheduledSession(ctx context.Context, req apiv1.CreateSessionRequest, d scheduledwork.Dispatch) (name string, uid types.UID, adopted bool, err error)
}

// DenyAll grants no basis, so every claimed occurrence parks with its reason
// and nothing is dispatched. It was the operator's Authorizer before S3b2
// wired Sources, and stays for tests.
type DenyAll struct{}

// DenyAllReason is the reason DenyAll gives.
const DenyAllReason = ReasonNoAuthority + ": the authority checks are not wired yet (DESIGN-054 S3b2)"

// Authorize implements Authorizer.
func (DenyAll) Authorize(context.Context, *v1alpha1.ScheduledWork, time.Time) (Authority, error) {
	return Authority{Reason: DenyAllReason}, nil
}

// NoTemplates refuses every template, so the executor dispatches nothing. It
// was the operator's TemplateResolver before S3b2 wired Sources, and stays for
// tests.
type NoTemplates struct{}

// NoTemplatesReason is the refusal NoTemplates gives.
const NoTemplatesReason = "no dispatch templates are configured (DESIGN-054 S3b2)"

// Resolve implements TemplateResolver.
func (NoTemplates) Resolve(context.Context, string) (Template, error) {
	return Template{}, &Refusal{Reason: NoTemplatesReason}
}

// Executor dispatches due occurrences, in the leader only.
type Executor struct {
	// Client writes the ScheduledWork status. It is never used to read: a
	// cached read would start an informer on a kind S6 may not have
	// installed yet.
	Client client.Client
	// Reader lists and gets ScheduledWorks and sessions from the API server,
	// never a cache.
	Reader client.Reader
	// Namespace holds the ScheduledWorks: the operator's own, where the
	// releaser reads them (Reconciler.ScheduledWorkNamespace).
	Namespace string
	// SessionNamespace holds the sessions.
	SessionNamespace string
	Creator          Creator
	Authorizer       Authorizer
	Templates        TemplateResolver
	// Every is the polling period; zero is DefaultEvery.
	Every time.Duration
	// HeadlessGrace and InteractiveGrace are each mode's grace; zero takes
	// the default.
	HeadlessGrace, InteractiveGrace time.Duration
	// MaxHeadless and MaxInteractive are the global caps; zero takes the
	// default.
	MaxHeadless, MaxInteractive int
	// DecisionWindow is how long a Parked occurrence waits; zero is
	// DefaultDecisionWindow.
	DecisionWindow time.Duration
	// SchemaMissing counts the passes and writes that found the CRD or the
	// stored item without a field the executor needs (MetricSchemaMissing);
	// SchemaReadFailures the preflight's failed CRD reads
	// (MetricSchemaReadFailures). Nil counts nothing.
	SchemaMissing      prometheus.Counter
	SchemaReadFailures prometheus.Counter
	// Pager creates the page Reminders (DESIGN-054 Decision 5, S4b); NewPager
	// builds the operator's. Nil pages nothing and records no page.
	Pager *pages.Pager
	// PageTimeout bounds one page's Ensure; zero is DefaultPageTimeout.
	PageTimeout time.Duration
	// MaxUnconfirmedPageTries bounds the passes an unconfirmed page is tried
	// in; zero is DefaultMaxUnconfirmedPageTries.
	MaxUnconfirmedPageTries int
	Log                     logr.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	kindLogged bool
	// The schema preflight's last verdict, the CRD resourceVersion and time
	// it was judged at, and whether a verdict was logged.
	schemaOK, schemaLogged bool
	schemaRV               string
	schemaAt               time.Time
}

// NeedLeaderElection makes the executor run in the leader only.
func (e *Executor) NeedLeaderElection() bool { return true }

// Start runs a pass at once and then every period until ctx ends. It
// implements controller-runtime's Runnable.
func (e *Executor) Start(ctx context.Context) error {
	every := e.Every
	if every <= 0 {
		every = DefaultEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		e.Tick(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Executor) grace(mode string) time.Duration {
	if mode == v1alpha1.ScheduledWorkInteractive {
		return positiveOr(e.InteractiveGrace, DefaultInteractiveGrace)
	}
	return positiveOr(e.HeadlessGrace, DefaultHeadlessGrace)
}

func (e *Executor) limit(mode string) int {
	if mode == v1alpha1.ScheduledWorkInteractive {
		return positiveOr(e.MaxInteractive, DefaultMaxInteractive)
	}
	return positiveOr(e.MaxHeadless, DefaultMaxHeadless)
}

func positiveOr[T time.Duration | int](v, def T) T {
	if v > 0 {
		return v
	}
	return def
}

// Tick makes one pass over every ScheduledWork, oldest first.
func (e *Executor) Tick(ctx context.Context) {
	var list v1alpha1.ScheduledWorkList
	if err := e.Reader.List(ctx, &list, client.InNamespace(e.Namespace)); err != nil {
		if meta.IsNoMatchError(err) {
			if !e.kindLogged {
				e.Log.Info("the ScheduledWork kind is not served; nothing to dispatch", "error", err.Error())
				e.kindLogged = true
			}
			return
		}
		e.Log.Error(err, "list the scheduled work")
		return
	}
	e.kindLogged = false
	if !e.schemaReady(ctx) {
		return
	}
	items := list.Items
	slices.SortFunc(items, func(a, b v1alpha1.ScheduledWork) int {
		if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	p := &pass{e: e}
	sessions, err := e.scheduledSessions(ctx)
	if err != nil {
		e.Log.Error(err, "list the scheduled sessions for the caps")
	}
	p.count(items, sessions)
	for i := range items {
		if ctx.Err() != nil {
			return
		}
		p.item(ctx, &items[i])
	}
}

// slots counts an item's occurrences that hold a cap slot: claimed and not
// ended. Pending holds none (it waits for one) and Parked none (it never
// blocks the next occurrence). An occurrence with a session counts in its
// session's own mode (modes, by session name); one without a session yet in
// the item's current mode.
type slots struct{ headless, interactive int }

func slotsOf(w *v1alpha1.ScheduledWork, modes map[string]v1alpha1.SessionMode) slots {
	var s slots
	for i := range w.Status.Occurrences {
		o := &w.Status.Occurrences[i]
		if !holdsSlot(o.State) {
			continue
		}
		interactive := w.Spec.Mode == v1alpha1.ScheduledWorkInteractive
		if m, ok := modes[o.Session]; ok && o.Session != "" {
			interactive = m != v1alpha1.ModeTask
		}
		if interactive {
			s.interactive++
		} else {
			s.headless++
		}
	}
	return s
}

// scheduledSessions lists the sessions that carry the scheduled-work label,
// from the API server.
func (e *Executor) scheduledSessions(ctx context.Context) ([]v1alpha1.AgentSession, error) {
	var list v1alpha1.AgentSessionList
	if err := e.Reader.List(ctx, &list, client.InNamespace(e.SessionNamespace), client.HasLabels{v1alpha1.LabelScheduledWork}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// count sets the pass's slots from items and the scheduled sessions: each
// item's slot-holding occurrences, in their sessions' own modes, and every
// scheduled session that has not ended by the session-end rule and that no
// slot-holding occurrence covers (one waiting to be adopted into a Parked
// occurrence, or one that slipped past its record). A session whose end only
// its pod could prove counts until the pod-free rule shows it. With no session
// listing (nil), the slots count in each item's current mode only.
func (p *pass) count(items []v1alpha1.ScheduledWork, sessions []v1alpha1.AgentSession) {
	p.modes = make(map[string]v1alpha1.SessionMode, len(sessions))
	for i := range sessions {
		p.modes[sessions[i].Name] = sessions[i].Spec.Mode
	}
	p.slots = make(map[types.UID]slots, len(items))
	byName := make(map[string]*v1alpha1.ScheduledWork, len(items))
	for i := range items {
		p.slots[items[i].UID] = slotsOf(&items[i], p.modes)
		byName[items[i].Name] = &items[i]
	}
	p.extra, p.extraNames = slots{}, map[string]bool{}
	now := p.e.now()
	for i := range sessions {
		s := &sessions[i]
		if ended, _ := scheduledwork.SessionEnded(s, nil, now); ended {
			continue
		}
		if w := byName[s.Annotations[v1alpha1.AnnotationScheduledWorkName]]; w != nil && string(w.UID) == s.Labels[v1alpha1.LabelScheduledWork] {
			if at, err := scheduledwork.OccurrenceAt(s); err == nil {
				if o := scheduledwork.Find(w, at); o != nil && holdsSlot(o.State) && (o.Session == "" || o.Session == s.Name) {
					continue
				}
			}
		}
		p.extraNames[s.Name] = true
		if s.Spec.Mode == v1alpha1.ModeTask {
			p.extra.headless++
		} else {
			p.extra.interactive++
		}
	}
}

// adoptable reports whether adopting the running session name into a Parked
// occurrence keeps its mode within the cap, counted afresh as slotFree counts.
// The session already counts among the extra sessions, so adopting it adds
// nothing; it waits only while the cap is exceeded with it.
func (p *pass) adoptable(ctx context.Context, mode, name string, lg logr.Logger) bool {
	if !p.recount(ctx, lg) {
		return false
	}
	n := p.used(mode)
	if p.extraNames[name] {
		n--
	}
	return n < p.e.limit(mode)
}

// slotFree reports whether a claim (or an adoption) of an occurrence in mode
// fits the cap, counted from a listing of every item and scheduled session
// read from the API server just before the claim's compare-and-set, not the
// pass's earlier listing: another leader may have claimed since. Two leaders
// that check at the same moment can still each take the last slot, so the cap
// can be exceeded by one per overlapping leader. A failed listing is not
// free.
func (p *pass) slotFree(ctx context.Context, mode string, lg logr.Logger) bool {
	return p.recount(ctx, lg) && p.used(mode) < p.e.limit(mode)
}

// recount sets the pass's slots from a fresh uncached listing of the items and
// the scheduled sessions; false when either read fails.
func (p *pass) recount(ctx context.Context, lg logr.Logger) bool {
	var list v1alpha1.ScheduledWorkList
	if err := p.e.Reader.List(ctx, &list, client.InNamespace(p.e.Namespace)); err != nil {
		lg.Error(err, "list the scheduled work again before a claim")
		return false
	}
	sessions, err := p.e.scheduledSessions(ctx)
	if err != nil {
		lg.Error(err, "list the scheduled sessions again before a claim")
		return false
	}
	p.count(list.Items, sessions)
	return true
}

func holdsSlot(state string) bool {
	switch state {
	case v1alpha1.OccurrenceClaimed, v1alpha1.OccurrenceDispatched, v1alpha1.OccurrenceDelivered,
		v1alpha1.OccurrenceOpened, v1alpha1.OccurrenceRunning:
		return true
	}
	return false
}

// pass is one Tick: the cap slots every item holds, kept current as the pass
// writes, and the occurrences whose create failed in it, which it does not
// try again until the next pass.
type pass struct {
	e     *Executor
	slots map[types.UID]slots
	modes map[string]v1alpha1.SessionMode
	// extra are the running scheduled sessions no slot-holding record
	// covers; extraNames names them.
	extra      slots
	extraNames map[string]bool
	tried      map[int64]bool
	// pagesTried are the item's pages whose Ensure could not be confirmed in
	// this pass; they stay pending and are not tried again until the next.
	pagesTried map[string]bool
}

func (p *pass) used(mode string) int {
	n := 0
	for _, s := range append([]slots{p.extra}, slices.Collect(maps.Values(p.slots))...) {
		if mode == v1alpha1.ScheduledWorkInteractive {
			n += s.interactive
		} else {
			n += s.headless
		}
	}
	return n
}

// item runs one item's steps until none writes. A conflict reads the item
// again: another leader, the releaser or the API wrote it.
func (p *pass) item(ctx context.Context, w *v1alpha1.ScheduledWork) {
	if !w.DeletionTimestamp.IsZero() {
		return
	}
	p.tried, p.pagesTried = map[int64]bool{}, map[string]bool{}
	lg := p.e.Log.WithValues("scheduledWork", w.Name)
	for range maxSteps {
		wrote, err := p.step(ctx, w, lg)
		switch {
		case apierrors.IsConflict(err):
			lg.V(1).Info("the ScheduledWork changed under the executor; reading it again")
			fresh := &v1alpha1.ScheduledWork{}
			if err := p.e.Reader.Get(ctx, client.ObjectKeyFromObject(w), fresh); err != nil {
				if !apierrors.IsNotFound(err) {
					lg.Error(err, "read the ScheduledWork again")
				}
				return
			}
			*w = *fresh
			p.slots[w.UID] = slotsOf(w, p.modes)
			continue
		case apierrors.IsNotFound(err), errors.Is(err, errSchemaMissing):
			return
		case err != nil:
			lg.Error(err, "write the ScheduledWork status")
			return
		}
		p.slots[w.UID] = slotsOf(w, p.modes)
		if !wrote {
			return
		}
	}
	lg.Info("the item took the most writes one pass gives it; the next pass goes on", "writes", maxSteps)
}

// step takes the item's next action and reports whether the item changed: it
// wrote the status, or it found the stored item newer than w and took it. It
// writes at most once. The order: plan the next due time, then each stored
// occurrence that is not final (oldest first), then the next due occurrence.
func (p *pass) step(ctx context.Context, w *v1alpha1.ScheduledWork, lg logr.Logger) (bool, error) {
	e := p.e
	now := e.now()
	grace := e.grace(w.Spec.Mode)
	sch, err := reminders.ScheduleOfWork(w.Spec)
	if err != nil {
		// The API validates the schedule (S5); a stored item that does not
		// parse plans nothing, and its stored occurrences still end.
		lg.V(1).Info("the schedule does not parse; no occurrence is planned", "error", err.Error())
		sch = nil
	}
	if sch != nil && w.Status.PlanHash != planHash(w.Spec) {
		if o := plan(w, sch, now); o != nil {
			p.intendFor(w, o)
		}
		lg.Info("planned the next occurrence", "nextDue", timeOrNone(w.Status.NextDue))
		return true, e.write(ctx, w)
	}
	// The pages of transitions already stored go out before the next one.
	if wrote, err := p.deliver(ctx, w, now, lg); wrote || err != nil {
		return wrote, err
	}
	for i := range w.Status.Occurrences {
		o := w.Status.Occurrences[i]
		at := o.At.Time
		var wrote bool
		var err error
		switch {
		case o.State == v1alpha1.OccurrencePending:
			wrote, err = p.pending(ctx, w, at, now, grace, lg)
		case o.State == v1alpha1.OccurrenceClaimed && o.Session == "" && !p.tried[at.Unix()]:
			wrote, err = p.claimed(ctx, w, at, now, grace, lg)
		case o.State == v1alpha1.OccurrenceParked && o.Session == "":
			wrote, err = p.parked(ctx, w, at, sch, now, lg)
		}
		// Dispatched, Delivered, Opened and Running end through the
		// releaser (S3a) when the session ends.
		// TODO(S4): observe Dispatched to Running, the interactive
		// Delivered and Opened states, and Failed when a session is not
		// Running (headless) or Ready (interactive) by the start deadline.
		// TODO(S3b2): the start-deadline suspend of a session whose agent
		// never started, before its Failed record.
		if wrote || err != nil {
			return wrote, err
		}
	}
	if sch != nil && w.Status.NextDue != nil && !w.Status.NextDue.After(now) {
		return p.due(ctx, w, sch, now, grace, lg)
	}
	if w.Spec.Cancelled && w.Status.NextDue != nil {
		// Cancel stops the occurrences not yet due.
		w.Status.NextDue = nil
		lg.Info("the item is cancelled; no later occurrence is planned")
		return true, e.write(ctx, w)
	}
	return false, nil
}

// due decides the occurrence at NextDue, which has no record yet, and moves
// NextDue on, in one write.
func (p *pass) due(ctx context.Context, w *v1alpha1.ScheduledWork, sch *reminders.Schedule, now time.Time, grace time.Duration, lg logr.Logger) (bool, error) {
	e := p.e
	at := w.Status.NextDue.Time
	if w.Spec.Cancelled {
		if !now.After(at.Add(grace)) {
			record(w, v1alpha1.ScheduledWorkOccurrence{At: metav1.NewTime(at), State: v1alpha1.OccurrenceCancelled, Reason: ReasonCancelledBeforeClaim})
			w.Status.Counts.Cancelled++
		}
		w.Status.NextDue = nil
		logOccurrence(lg, at, "", v1alpha1.OccurrenceCancelled, ReasonCancelledBeforeClaim)
		return true, e.write(ctx, w)
	}
	if now.After(at.Add(grace)) {
		// Missed beyond grace: the executor was not running. Every missed
		// occurrence of the item collapses into one Skipped record.
		n, last := 1, at
		next, ok := nextAfter(w, sch, at)
		for ok && now.After(next.Add(grace)) && n < maxMissed {
			n, last = n+1, next
			next, ok = nextAfter(w, sch, next)
		}
		rec := v1alpha1.ScheduledWorkOccurrence{At: metav1.NewTime(at), State: v1alpha1.OccurrenceSkipped, Reason: missedReason(n, last, grace)}
		if n > 1 {
			rec.Missed = int32(n)
		}
		p.intendFor(w, &rec)
		record(w, rec)
		w.Status.Counts.Skipped += int32(n)
		setNext(w, next, ok)
		logOccurrence(lg, at, "", v1alpha1.OccurrenceSkipped, rec.Reason)
		return true, e.write(ctx, w)
	}
	next, ok := nextAfter(w, sch, at)
	rec := v1alpha1.ScheduledWorkOccurrence{At: metav1.NewTime(at)}
	switch {
	case now.After(w.Spec.NotAfter.Time):
		rec.State, rec.Reason = v1alpha1.OccurrenceSkipped, ReasonNotAfter
		w.Status.Counts.Skipped++
		ok = false
	case previousUnfinished(w, at):
		rec.State, rec.Reason = v1alpha1.OccurrenceSkipped, ReasonPreviousRunning
		w.Status.Counts.Skipped++
	default:
		if reason, blocked := p.preClaim(ctx, w, now, lg); blocked {
			rec.State, rec.Reason = v1alpha1.OccurrencePending, reason
		} else if !p.slotFree(ctx, w.Spec.Mode, lg) {
			rec.State, rec.Reason = v1alpha1.OccurrencePending, ReasonAtCapacity
		} else {
			claimedAt := metav1.NewTime(now)
			rec.State, rec.ClaimedAt, rec.SpecHash = v1alpha1.OccurrenceClaimed, &claimedAt, scheduledwork.SpecHash(w.Spec)
		}
	}
	p.intendFor(w, &rec)
	record(w, rec)
	setNext(w, next, ok)
	logOccurrence(lg, at, "", rec.State, rec.Reason)
	return true, e.write(ctx, w)
}

// preClaim checks what must hold before an occurrence is claimed: a free cap
// slot for its mode, and an owner session proven live when the item's basis
// is the live owner. It returns the Pending reason when it is blocked.
func (p *pass) preClaim(ctx context.Context, w *v1alpha1.ScheduledWork, now time.Time, lg logr.Logger) (string, bool) {
	if p.used(w.Spec.Mode) >= p.e.limit(w.Spec.Mode) {
		return ReasonAtCapacity, true
	}
	a, err := p.e.Authorizer.Authorize(ctx, w, now)
	switch {
	case err != nil:
		lg.Error(err, "check the authority before the claim")
		return ReasonAuthorityUnread, true
	case a.Unproven:
		return ReasonOwnerUnproven, true
	}
	return "", false
}

// pending decides a stored Pending occurrence: claim it once the caps and the
// owner proof allow, or end it when its grace ends.
func (p *pass) pending(ctx context.Context, w *v1alpha1.ScheduledWork, at, now time.Time, grace time.Duration, lg logr.Logger) (bool, error) {
	o := scheduledwork.Find(w, at)
	from := o.State
	switch {
	case w.Spec.Cancelled:
		o.State, o.Reason = v1alpha1.OccurrenceCancelled, ReasonCancelledBeforeClaim
		w.Status.Counts.Cancelled++
	case now.After(w.Spec.NotAfter.Time):
		o.State, o.Reason = v1alpha1.OccurrenceSkipped, ReasonNotAfter
		w.Status.Counts.Skipped++
	case now.After(windowStart(o).Add(grace)):
		switch o.Reason {
		case ReasonOwnerUnproven, ReasonAuthorityUnread:
			o.State = v1alpha1.OccurrenceParked
			w.Status.Counts.Parked++
		case ReasonAtCapacity:
			o.State = v1alpha1.OccurrenceSkipped
			w.Status.Counts.Skipped++
		default:
			// It never waited on a cap or the owner: no pass reached it
			// inside its grace (a retried occurrence while the executor was
			// down, say).
			o.State, o.Reason = v1alpha1.OccurrenceSkipped, graceMissedReason(o, grace)
			w.Status.Counts.Skipped++
		}
	case previousUnfinished(w, at):
		o.State, o.Reason = v1alpha1.OccurrenceSkipped, ReasonPreviousRunning
		w.Status.Counts.Skipped++
	default:
		reason, blocked := p.preClaim(ctx, w, now, lg)
		if !blocked && !p.slotFree(ctx, w.Spec.Mode, lg) {
			reason, blocked = ReasonAtCapacity, true
		}
		hash := scheduledwork.SpecHash(w.Spec)
		switch {
		case blocked && reason == o.Reason:
			return false, nil
		case blocked:
			o.Reason = reason
		case o.SpecHash != "" && o.SpecHash != hash:
			o.State, o.Reason = v1alpha1.OccurrenceSkipped, ReasonSpecChanged
			w.Status.Counts.Skipped++
		default:
			claimedAt := metav1.NewTime(now)
			o.State, o.Reason, o.ClaimedAt = v1alpha1.OccurrenceClaimed, "", &claimedAt
			if o.SpecHash == "" {
				o.SpecHash = hash
			}
		}
	}
	if o.State != from {
		p.intendFor(w, o)
	}
	logOccurrence(lg, at, from, o.State, o.Reason)
	return true, p.e.write(ctx, w)
}

// claimed dispatches a Claimed occurrence that has no recorded session: it
// adopts the derived-name session when there is one, and otherwise revalidates
// (DESIGN-054 "Due-time revalidation") and creates it while the occurrence is
// inside its grace.
func (p *pass) claimed(ctx context.Context, w *v1alpha1.ScheduledWork, at, now time.Time, grace time.Duration, lg logr.Logger) (bool, error) {
	e := p.e
	o := scheduledwork.Find(w, at)
	name, err := scheduledwork.SessionName(string(w.UID), at)
	if err != nil {
		park(w, o, err.Error())
		p.intendFor(w, o)
		logOccurrence(lg, at, v1alpha1.OccurrenceClaimed, o.State, o.Reason)
		return true, e.write(ctx, w)
	}
	// The API server's answer, never a cache: a create a moment ago counts.
	var sess v1alpha1.AgentSession
	err = e.Reader.Get(ctx, types.NamespacedName{Namespace: e.SessionNamespace, Name: name}, &sess)
	switch {
	case err == nil && scheduledwork.CreatedFor(&sess, w.Name, w.UID, at) && o.Ending && scheduledwork.RefusedBeforeLaunch(&sess):
		return p.endNaming(ctx, w, o, &sess, lg)
	case err == nil && scheduledwork.CreatedFor(&sess, w.Name, w.UID, at):
		return p.dispatched(ctx, w, o, name, sess.UID, true, lg)
	case err == nil:
		ambiguous(w, o)
		p.intendFor(w, o)
		logOccurrence(lg, at, v1alpha1.OccurrenceClaimed, o.State, o.Reason)
		return true, e.write(ctx, w)
	case !apierrors.IsNotFound(err):
		lg.Error(err, "read the occurrence's session", "session", name)
		p.tried[at.Unix()] = true
		return false, nil
	}
	// No session of the derived name now. Read the item again, after that
	// read: the finalizer holds a created session until the releaser has
	// recorded its outcome, so an item that still shows this occurrence with
	// no outcome means no session was ever created. The listing this pass
	// started from may be older than a record. Any change since restarts
	// the step on the stored state.
	fresh := &v1alpha1.ScheduledWork{}
	if err := e.Reader.Get(ctx, client.ObjectKeyFromObject(w), fresh); err != nil {
		if apierrors.IsNotFound(err) {
			return false, err
		}
		lg.Error(err, "read the ScheduledWork again before the create")
		p.tried[at.Unix()] = true
		return false, nil
	}
	if fresh.ResourceVersion != w.ResourceVersion {
		*w = *fresh
		return true, nil
	}
	if o.Ending {
		// The mark is stored and the session read above came after it:
		// the occurrence is committed to end, with the reason the mark
		// carries.
		return p.end(ctx, w, o, endingState(o.Reason), o.Reason, lg)
	}
	hash := scheduledwork.SpecHash(w.Spec)
	var state, reason string
	switch {
	case now.After(windowStart(o).Add(grace)):
		state, reason = v1alpha1.OccurrenceSkipped, ReasonNotCreated
	// Check 1 ends the occurrence; a later check parks it.
	case w.Spec.Cancelled:
		state, reason = v1alpha1.OccurrenceCancelled, ReasonCancelledAtRevalidation
	case now.After(w.Spec.NotAfter.Time):
		state, reason = v1alpha1.OccurrenceSkipped, ReasonNotAfter
	case o.SpecHash != "" && o.SpecHash != hash:
		// The occurrence runs on the spec it was claimed with, which is no
		// longer stored; an edit applies from the next occurrence.
		state, reason = v1alpha1.OccurrenceSkipped, ReasonSpecChanged
	default:
		return p.create(ctx, w, o, name, hash, now, grace, lg)
	}
	return p.end(ctx, w, o, state, reason, lg)
}

// end writes a final record without a session (Skipped or Cancelled) for a
// Claimed or Parked occurrence, in two steps, because a second leader's create
// may land at any time. First it writes the ending mark by itself, with the
// final reason; from then on the launch gate refuses a session the occurrence
// does not name (scheduledwork.LaunchRefusal), and the occurrence is committed
// to end. Its caller reads the session of the derived name again, uncached,
// after the mark is stored, and adopts one it finds: a gate read that passed
// came before the mark, so its create came before that read. Only when the
// read finds none does end write the final record. Every step reads the
// item afresh, so the mark and the record are each a compare-and-set.
func (p *pass) end(ctx context.Context, w *v1alpha1.ScheduledWork, o *v1alpha1.ScheduledWorkOccurrence, state, reason string, lg logr.Logger) (bool, error) {
	at := o.At.Time
	from := o.State
	if !o.Ending {
		o.Ending, o.Reason = true, clip(reason, 256)
		lg.Info("scheduled work occurrence", "occurrence", at.UTC().Format(time.RFC3339), "from", from, "to", from, "ending", true, "reason", o.Reason)
		if err := p.e.write(ctx, w); err != nil {
			return true, err
		}
		return true, p.storedHas(ctx, w, at, "ending", lg)
	}
	o.State, o.Reason = state, reason
	if state == v1alpha1.OccurrenceCancelled {
		w.Status.Counts.Cancelled++
	} else {
		w.Status.Counts.Skipped++
	}
	p.intendFor(w, o)
	logOccurrence(lg, at, from, o.State, o.Reason)
	return true, p.e.write(ctx, w)
}

// endNaming ends an occurrence that carries the ending mark, and whose
// late session was refused before its first pod, with the mark's reason (not
// Failed): the session never ran. The record names that session and its UID.
func (p *pass) endNaming(ctx context.Context, w *v1alpha1.ScheduledWork, o *v1alpha1.ScheduledWorkOccurrence, sess *v1alpha1.AgentSession, lg logr.Logger) (bool, error) {
	if o.Session == "" {
		o.Session = sess.Name
	}
	if o.SessionUID == "" {
		o.SessionUID = string(sess.UID)
	}
	return p.end(ctx, w, o, endingState(o.Reason), o.Reason, lg)
}

// endingState is the final state an ending mark's reason stands for.
func endingState(reason string) string { return scheduledwork.EndingState(reason) }

// create runs revalidation checks 2 to 5 and creates the session.
func (p *pass) create(ctx context.Context, w *v1alpha1.ScheduledWork, o *v1alpha1.ScheduledWorkOccurrence, name, hash string, now time.Time, grace time.Duration, lg logr.Logger) (bool, error) {
	e := p.e
	at := o.At.Time
	parked := func(reason string) (bool, error) {
		park(w, o, reason)
		p.intendFor(w, o)
		logOccurrence(lg, at, v1alpha1.OccurrenceClaimed, o.State, o.Reason)
		return true, e.write(ctx, w)
	}
	retry := func(err error, what string) (bool, error) {
		lg.Error(err, what, "occurrence", at.UTC().Format(time.RFC3339))
		p.tried[at.Unix()] = true
		return false, nil
	}
	// Check 2: the authority basis, again after the claim.
	a, err := e.Authorizer.Authorize(ctx, w, now)
	switch {
	case err != nil:
		return retry(err, "check the authority")
	case a.Unproven:
		return parked(ReasonOwnerUnproven)
	case a.Basis == BasisNone:
		if a.Reason == "" {
			return parked(ReasonNoAuthority)
		}
		return parked(a.Reason)
	case a.Basis == BasisSession && a.Owner == nil:
		return parked("the live owner session is not named")
	}
	// Check 3: the template, its basis, its mode and its repo, now. The create checks
	// the profile against dev-env-templates and the model id's form and
	// effort; the resolver (Sources) reads the template now and refuses a
	// model that is not a full id, so the model is the one the template
	// holds at the due time.
	t, err := e.Templates.Resolve(ctx, w.Spec.Work.Template)
	var refusal *Refusal
	switch {
	case errors.As(err, &refusal):
		return parked(fmt.Sprintf("template %s: %s", w.Spec.Work.Template, refusal.Reason))
	case err != nil:
		return retry(err, "read the dispatch template")
	case t.Digest == "" || a.TemplateDigest == "":
		// A source that cannot name its template's content is a defect, not
		// an edit: it parks with the reason rather than retry until grace.
		return parked(fmt.Sprintf("template %s: %s", w.Spec.Work.Template, ReasonNoTemplateDigest))
	case t.Digest != a.TemplateDigest:
		// An edit between the decision and this read: decide again.
		return retry(fmt.Errorf("template %s changed after the authority was decided", w.Spec.Work.Template), "check the dispatch template against the decision")
	case !slices.Contains(t.Admits, a.Basis):
		return parked(fmt.Sprintf("template %s does not admit %s work", w.Spec.Work.Template, a.Basis))
	case !slices.Contains(t.Modes, w.Spec.Mode):
		return parked(fmt.Sprintf("template %s does not serve %s work", w.Spec.Work.Template, w.Spec.Mode))
	case !slices.Contains(t.Repos, w.Spec.Work.Repo):
		return parked(fmt.Sprintf("template %s does not allow repo %s", w.Spec.Work.Template, w.Spec.Work.Repo))
	}
	req, reason := request(w, t)
	if reason != "" {
		return parked(reason)
	}
	// Check 4, provider availability, is not predicted: the operator reads no
	// login or quota state, and the session reports a wall itself.
	// Check 5: the normal creation limits of the basis, in the create.
	d := scheduledwork.Dispatch{ItemName: w.Name, ItemUID: w.UID, At: at, StartDeadline: windowStart(o).Add(grace)}
	if a.Basis == BasisSession {
		d.Owner = a.Owner
	}
	if o.SpecHash == "" {
		o.SpecHash = hash
	}
	got, uid, adopted, err := e.Creator.CreateScheduledSession(ctx, req, d)
	var refused *scheduledwork.RefusedError
	switch {
	case err == nil && got != name:
		return retry(fmt.Errorf("the creation entry returned session %q, not the derived %q", got, name), "create the occurrence's session")
	case err == nil:
		return p.dispatched(ctx, w, o, name, uid, adopted, lg)
	case errors.Is(err, scheduledwork.ErrPastStartDeadline):
		return p.end(ctx, w, o, v1alpha1.OccurrenceSkipped, ReasonNotCreated, lg)
	case errors.Is(err, scheduledwork.ErrCollision):
		ambiguous(w, o)
		p.intendFor(w, o)
		logOccurrence(lg, at, v1alpha1.OccurrenceClaimed, o.State, o.Reason)
		return true, e.write(ctx, w)
	case errors.As(err, &refused):
		return parked(refused.Reason)
	}
	return retry(err, "create the occurrence's session")
}

// dispatched records the occurrence's session, its UID and Dispatched. The
// UID is what the launch gate later holds a second session object of the same
// name against.
func (p *pass) dispatched(ctx context.Context, w *v1alpha1.ScheduledWork, o *v1alpha1.ScheduledWorkOccurrence, name string, uid types.UID, adopted bool, lg logr.Logger) (bool, error) {
	from := o.State
	o.Session, o.State, o.Reason = name, v1alpha1.OccurrenceDispatched, ""
	if o.SessionUID == "" {
		o.SessionUID = string(uid)
	}
	p.intendFor(w, o)
	// TODO(S4): for interactive work, deliver the work order once the pod is
	// Ready and page the link once Remote Control is registered.
	at := o.At.Time
	lg.Info("scheduled work occurrence", "occurrence", at.UTC().Format(time.RFC3339), "from", from,
		"to", v1alpha1.OccurrenceDispatched, "session", name, "adopted", adopted)
	if err := p.e.write(ctx, w); err != nil {
		return true, err
	}
	if uid == "" {
		return true, nil
	}
	return true, p.storedHas(ctx, w, at, "sessionUID", lg)
}

// storedHas reads the item again from the API server, into a fresh object,
// and checks that the occurrence at carries the field just written. The
// written copy cannot show it: the client decodes the answer into the
// existing occurrences in place, so a field the API server pruned keeps its
// written value there. The schema preflight is the first check; this one
// catches a CRD that changed between it and the write. A failed read stops
// the item's pass too: the next pass reads the item from scratch.
func (p *pass) storedHas(ctx context.Context, w *v1alpha1.ScheduledWork, at time.Time, field string, lg logr.Logger) error {
	fresh := &v1alpha1.ScheduledWork{}
	if err := p.e.Reader.Get(ctx, client.ObjectKeyFromObject(w), fresh); err != nil {
		lg.Error(err, "read the ScheduledWork back after a write", "field", field)
		return errSchemaMissing
	}
	o := scheduledwork.Find(fresh, at)
	switch {
	case field == "ending" && o != nil && o.Ending:
		return nil
	case field == "sessionUID" && o != nil && o.SessionUID != "":
		return nil
	}
	return p.schemaMissing(lg, field)
}

// SchemaResync is how long a verified CRD schema is trusted before the
// executor reads the CRD in full again; a changed resourceVersion is read at
// once.
const SchemaResync = 5 * time.Minute

// scheduledWorkCRD is the CustomResourceDefinition the preflight reads; S6
// grants get on this one object.
const scheduledWorkCRD = "scheduledworks.dev-env.haynesops.com"

var crdKind = schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}

// schemaReady is the schema preflight, run before any work in a pass. It
// reads the ScheduledWork CRD uncached and requires its served v1alpha1 status
// schema to carry the occurrence fields the guarantee rests on, ending and
// sessionUID. An older CRD would prune them silently: the two-phase end would
// become one phase and the session UID would be lost. Without them, or when
// the CRD cannot be read, the pass does nothing at all; a missing field is
// counted on every such pass and logged once. A verified schema is trusted
// until the CRD's resourceVersion changes or SchemaResync passes.
func (e *Executor) schemaReady(ctx context.Context) bool {
	key := client.ObjectKey{Name: scheduledWorkCRD}
	head := &metav1.PartialObjectMetadata{}
	head.SetGroupVersionKind(crdKind)
	if err := e.Reader.Get(ctx, key, head); err != nil {
		e.schemaReadFailed(err)
		return false
	}
	now := e.now()
	if e.schemaOK && head.ResourceVersion == e.schemaRV && now.Sub(e.schemaAt) < SchemaResync {
		return true
	}
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(crdKind)
	if err := e.Reader.Get(ctx, key, crd); err != nil {
		e.schemaReadFailed(err)
		return false
	}
	e.schemaRV, e.schemaAt = crd.GetResourceVersion(), now
	if missing := missingStatusFields(crd); len(missing) > 0 {
		if e.SchemaMissing != nil {
			e.SchemaMissing.Inc()
		}
		e.schemaVerdict(false, "the installed ScheduledWork CRD lacks occurrence fields the executor needs; dispatching nothing", "missing", missing)
		return false
	}
	e.schemaVerdict(true, "the installed ScheduledWork CRD carries the occurrence fields the executor needs")
	return true
}

// schemaReadFailed counts a failed read of the CRD (a missing grant, for
// example), logs it once, and leaves the verdict not ready.
func (e *Executor) schemaReadFailed(err error) {
	if e.SchemaReadFailures != nil {
		e.SchemaReadFailures.Inc()
	}
	e.schemaVerdict(false, "the ScheduledWork CRD cannot be read; dispatching nothing", "error", err.Error())
}

// schemaVerdict records the preflight's verdict and logs it when it changes.
func (e *Executor) schemaVerdict(ok bool, msg string, kv ...any) {
	if ok != e.schemaOK || !e.schemaLogged {
		if ok {
			e.Log.Info(msg, kv...)
		} else {
			e.Log.Error(errSchemaMissing, msg, kv...)
		}
		e.schemaLogged = true
	}
	e.schemaOK = ok
}

// missingStatusFields lists the occurrence fields the served v1alpha1 status
// schema of crd lacks.
func missingStatusFields(crd *unstructured.Unstructured) []string {
	want := []string{"ending", "sessionUID"}
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	for _, v := range versions {
		version, ok := v.(map[string]any)
		if !ok || version["name"] != v1alpha1.GroupVersion.Version {
			continue
		}
		props, _, _ := unstructured.NestedMap(version, "schema", "openAPIV3Schema", "properties", "status", "properties", "occurrences", "items", "properties")
		var missing []string
		for _, f := range want {
			if _, ok := props[f]; !ok {
				missing = append(missing, f)
			}
		}
		return missing
	}
	return want
}

// errSchemaMissing stops an item's pass: the API server's stored object
// lacks a field the executor just wrote, so the installed CRD is older than
// the executor and prunes it. The guarantee rests on those fields, so the
// executor writes nothing more for the item in this pass, and never loops
// rewriting it.
var errSchemaMissing = errors.New("the stored ScheduledWork lacks a field the executor wrote: the installed CRD is older than the executor")

// schemaMissing logs and counts a field the stored object lacks.
func (p *pass) schemaMissing(lg logr.Logger, field string) error {
	lg.Error(errSchemaMissing, "stopping the item's pass", "field", field)
	if p.e.SchemaMissing != nil {
		p.e.SchemaMissing.Inc()
	}
	return errSchemaMissing
}

// MetricSchemaMissing counts the writes whose stored object lacked a field the
// executor wrote (an older CRD).
const MetricSchemaMissing = "dev_env_scheduled_work_schema_missing_total"

// MetricSchemaReadFailures counts the schema preflight's failed reads of the
// ScheduledWork CRD.
const MetricSchemaReadFailures = "dev_env_scheduled_work_schema_read_failures_total"

// NewSchemaReadFailuresCounter returns the counter of
// MetricSchemaReadFailures. Register it once per process.
func NewSchemaReadFailuresCounter() prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{
		Name: MetricSchemaReadFailures,
		Help: "Failed reads of the ScheduledWork CRD by the scheduled-work executor's schema preflight, such as a missing get grant (DESIGN-054). The executor dispatches nothing in such a pass.",
	})
}

// NewSchemaMissingCounter returns the counter of MetricSchemaMissing.
// Register it once per process.
func NewSchemaMissingCounter() prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{
		Name: MetricSchemaMissing,
		Help: "Scheduled-work status writes whose stored object lacked a field the executor wrote (the ending mark or the session UID): the installed ScheduledWork CRD is older than the executor (DESIGN-054). The executor stops that item's pass.",
	})
}

// parked ends a Parked occurrence nobody acted on: Cancelled when the item is
// cancelled, Skipped once its decision window ends. The window is
// DecisionWindow from the due time (or retriedAt), or until the item's next
// occurrence is due, whichever comes first.
func (p *pass) parked(ctx context.Context, w *v1alpha1.ScheduledWork, at time.Time, sch *reminders.Schedule, now time.Time, lg logr.Logger) (bool, error) {
	o := scheduledwork.Find(w, at)
	// A second leader's create can land after this occurrence parked: its
	// session is adopted, so the releaser records it and it holds its slot.
	if name, err := scheduledwork.SessionName(string(w.UID), at); err == nil {
		var sess v1alpha1.AgentSession
		err := p.e.Reader.Get(ctx, types.NamespacedName{Namespace: p.e.SessionNamespace, Name: name}, &sess)
		switch {
		case err == nil && scheduledwork.CreatedFor(&sess, w.Name, w.UID, at) && o.Ending && scheduledwork.RefusedBeforeLaunch(&sess):
			return p.endNaming(ctx, w, o, &sess, lg)
		case err == nil && scheduledwork.CreatedFor(&sess, w.Name, w.UID, at):
			mode := v1alpha1.ScheduledWorkHeadless
			if sess.Spec.Mode != v1alpha1.ModeTask {
				mode = v1alpha1.ScheduledWorkInteractive
			}
			if !p.adoptable(ctx, mode, name, lg) {
				// Adopting takes a slot. At the cap the occurrence stays
				// Parked, and does not expire while its session exists: the
				// releaser records the session's outcome into it either way.
				lg.Info("a parked occurrence has its session; adopting it waits for a free slot", "occurrence", at.UTC().Format(time.RFC3339), "session", name)
				return false, nil
			}
			return p.dispatched(ctx, w, o, name, sess.UID, true, lg)
		case err != nil && !apierrors.IsNotFound(err):
			lg.Error(err, "read the parked occurrence's session", "session", name)
			return false, nil
		}
	}
	switch {
	case o.Ending:
		// The mark is stored and the session read above came after it.
		// TODO(S5): an approve or retry must not reopen an ending occurrence.
		return p.end(ctx, w, o, endingState(o.Reason), o.Reason, lg)
	case w.Spec.Cancelled:
		return p.end(ctx, w, o, v1alpha1.OccurrenceCancelled, ReasonCancelledWhileParked, lg)
	}
	end := windowStart(o).Add(positiveOr(p.e.DecisionWindow, DefaultDecisionWindow))
	if sch != nil {
		if next, ok := sch.Next(at.Add(time.Nanosecond)); ok && next.At.Before(end) {
			end = next.At
		}
	}
	if now.Before(end) {
		// TODO(S5): a human approve or retry claims it again, from
		// retriedAt. The parked page went out with the park.
		return false, nil
	}
	return p.end(ctx, w, o, v1alpha1.OccurrenceSkipped, ReasonParkedExpired, lg)
}

// request is the session the template makes for w's mode, or the reason it
// cannot make one.
func request(w *v1alpha1.ScheduledWork, t Template) (apiv1.CreateSessionRequest, string) {
	req := apiv1.CreateSessionRequest{Repo: w.Spec.Work.Repo, Agent: t.Agent, Model: t.Model, Effort: t.Effort, Size: t.Size, Profile: t.Profile}
	switch w.Spec.Mode {
	case v1alpha1.ScheduledWorkHeadless:
		if t.MaxDuration <= 0 {
			return req, fmt.Sprintf("template %s sets no maximum duration, which headless work needs", w.Spec.Work.Template)
		}
		req.Mode, req.Prompt = string(v1alpha1.ModeTask), w.Spec.Work.Prompt
		req.Limits = &apiv1.Limits{Timeout: t.MaxDuration.String()}
	case v1alpha1.ScheduledWorkInteractive:
		// A remote session takes no prompt. TODO(S4): deliver the work order
		// through the message route once the pod is Ready.
		req.Mode = string(v1alpha1.ModeRemote)
		req.Lifecycle = &apiv1.Lifecycle{IdleSuspendAfter: positiveOr(t.IdleSuspendAfter, DefaultIdleSuspendAfter).String()}
	default:
		return req, fmt.Sprintf("mode %q is neither headless nor interactive", w.Spec.Mode)
	}
	return req, ""
}

func (e *Executor) write(ctx context.Context, w *v1alpha1.ScheduledWork) error {
	return e.Client.Status().Update(ctx, w)
}

// park parks an occurrence with a reason; a human approve, retry or cancel,
// or the end of its decision window, decides it. counts.parked counts each
// time an occurrence parks: an expired park is then counted Skipped too.
func park(w *v1alpha1.ScheduledWork, o *v1alpha1.ScheduledWorkOccurrence, reason string) {
	o.State, o.Reason = v1alpha1.OccurrenceParked, clip(reason, 256)
	w.Status.Counts.Parked++
}

func ambiguous(w *v1alpha1.ScheduledWork, o *v1alpha1.ScheduledWorkOccurrence) {
	o.State, o.Reason = v1alpha1.OccurrenceAmbiguous, ReasonCollision
	w.Status.Counts.Ambiguous++
}

// windowStart is where an occurrence's grace counts from: its retriedAt after
// a human approve or retry, else its due time.
func windowStart(o *v1alpha1.ScheduledWorkOccurrence) time.Time {
	if o.RetriedAt != nil {
		return o.RetriedAt.Time
	}
	return o.At.Time
}

// previousUnfinished reports another occurrence of w that is not final and not
// Parked: one unfinished occurrence per item.
func previousUnfinished(w *v1alpha1.ScheduledWork, at time.Time) bool {
	for i := range w.Status.Occurrences {
		o := &w.Status.Occurrences[i]
		if !o.At.Time.Equal(at) && !v1alpha1.OccurrenceTerminal(o.State) && o.State != v1alpha1.OccurrenceParked {
			return true
		}
	}
	return false
}

// record adds occurrence o to w's status, in instant order, and prunes the
// oldest terminal occurrences beyond ScheduledWorkTerminalKept. It never
// prunes one that is not final.
func record(w *v1alpha1.ScheduledWork, o v1alpha1.ScheduledWorkOccurrence) {
	occ := w.Status.Occurrences
	i, _ := slices.BinarySearchFunc(occ, o.At.Time, func(x v1alpha1.ScheduledWorkOccurrence, t time.Time) int {
		return x.At.Compare(t)
	})
	occ = slices.Insert(occ, i, o)
	terminal := 0
	for j := range occ {
		if v1alpha1.OccurrenceTerminal(occ[j].State) {
			terminal++
		}
	}
	for j := 0; terminal > v1alpha1.ScheduledWorkTerminalKept && j < len(occ); {
		if v1alpha1.OccurrenceTerminal(occ[j].State) {
			occ = slices.Delete(occ, j, j+1)
			terminal--
			continue
		}
		j++
	}
	w.Status.Occurrences = occ
}

// plan computes NextDue after an edit of the schedule or notAfter, or for an
// item never planned. A new item plans from the minute it was created, so the
// occurrences missed while the executor was down are found and none from
// before the item existed is run. An edit re-plans from now; an occurrence
// that was due and not yet decided is recorded Skipped first, so every
// occurrence keeps a record. It returns that Skipped record, or nil.
func plan(w *v1alpha1.ScheduledWork, sch *reminders.Schedule, now time.Time) *v1alpha1.ScheduledWorkOccurrence {
	notBefore := now
	var replanned *metav1.Time
	if w.Status.PlanHash == "" {
		notBefore = w.CreationTimestamp.Truncate(time.Minute)
	} else if d := w.Status.NextDue; d != nil && !d.After(now) && scheduledwork.Find(w, d.Time) == nil {
		replanned = d.DeepCopy()
		record(w, v1alpha1.ScheduledWorkOccurrence{At: *replanned, State: v1alpha1.OccurrenceSkipped, Reason: ReasonReplanned})
		w.Status.Counts.Skipped++
	}
	w.Status.PlanHash = planHash(w.Spec)
	next, ok := firstFrom(w, sch, notBefore)
	setNext(w, next, ok)
	if replanned == nil {
		return nil
	}
	return scheduledwork.Find(w, replanned.Time)
}

// planHash identifies the fields NextDue is computed from: the shared schedule
// fields and notAfter.
func planHash(spec v1alpha1.ScheduledWorkSpec) string {
	sum := sha256.Sum256([]byte(reminders.PlanHashOf(spec.Due, spec.TimeZone, spec.Recurrence) + "|notAfter=" + spec.NotAfter.UTC().Format(time.RFC3339)))
	return hex.EncodeToString(sum[:16])
}

// firstFrom is w's first occurrence at or after notBefore, no later than
// notAfter, that has no record yet.
func firstFrom(w *v1alpha1.ScheduledWork, sch *reminders.Schedule, notBefore time.Time) (time.Time, bool) {
	for {
		o, ok := sch.Next(notBefore)
		if !ok || o.At.After(w.Spec.NotAfter.Time) {
			return time.Time{}, false
		}
		if scheduledwork.Find(w, o.At) == nil {
			return o.At, true
		}
		notBefore = o.At.Add(time.Nanosecond)
	}
}

// nextAfter is w's first occurrence after at that firstFrom accepts.
func nextAfter(w *v1alpha1.ScheduledWork, sch *reminders.Schedule, at time.Time) (time.Time, bool) {
	return firstFrom(w, sch, at.Add(time.Nanosecond))
}

func setNext(w *v1alpha1.ScheduledWork, next time.Time, ok bool) {
	if !ok {
		w.Status.NextDue = nil
		return
	}
	w.Status.NextDue = &metav1.Time{Time: next.UTC()}
}

// graceMissedReason is the reason of a Pending occurrence no pass reached
// inside its grace, which counts from retriedAt for a retried one.
func graceMissedReason(o *v1alpha1.ScheduledWorkOccurrence, grace time.Duration) string {
	reason := missedReason(1, o.At.Time, grace)
	if o.RetriedAt != nil {
		reason += " after a retry"
	}
	return reason
}

func missedReason(n int, last time.Time, grace time.Duration) string {
	if n == 1 {
		return fmt.Sprintf("missed beyond grace (%s)", grace)
	}
	return fmt.Sprintf("missed beyond grace (%s): %d occurrences, the last at %s", grace, n, last.UTC().Format(time.RFC3339))
}

func logOccurrence(lg logr.Logger, at time.Time, from, to, reason string) {
	if from == "" {
		from = "due"
	}
	lg.Info("scheduled work occurrence", "occurrence", at.UTC().Format(time.RFC3339), "from", from, "to", to, "reason", reason)
}

func timeOrNone(t *metav1.Time) string {
	if t == nil {
		return "none"
	}
	return t.UTC().Format(time.RFC3339)
}

// clip keeps s within n bytes, cut at a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
