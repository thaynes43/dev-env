package coordretention

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/shelf"
)

const (
	// DefaultGrace is DESIGN-024's 30-day floor. Flags may only lengthen it.
	DefaultGrace = 720 * time.Hour
	firstWait    = 10 * time.Minute
	everyWait    = 6 * time.Hour
	passDeadline = 30 * time.Minute
	defaultCap   = 256
)

// Runnable is the leader-only coordination retention pass. Enabled stays false
// unless the operator was started with --enable-coordination-retention.
type Runnable struct {
	Enabled    bool
	Grace      time.Duration
	First      time.Duration
	Every      time.Duration
	Deadline   time.Duration
	Now        func() time.Time
	After      func(time.Duration) <-chan time.Time
	Client     client.Client
	Reader     client.Reader
	Namespace  string
	Reconciled map[string]bool
	Kinds      map[Kind]bool
	Pods       PodSource
	Archive    Archive
	Slots      *shelf.ExecSlots
	Log        logr.Logger
	Registerer prometheus.Registerer

	maxMarks    int
	maxReleases int
	resume      orderKey
	blocked     map[string]struct{}
	metrics     *retentionMetrics
	registered  []prometheus.Collector
}

// NeedLeaderElection makes the pass run in the leader only.
func (r *Runnable) NeedLeaderElection() bool { return true }

// Start waits, then runs a pass until ctx ends. A disabled runnable returns
// without a pass, a shelf call, or a metric.
func (r *Runnable) Start(ctx context.Context) error {
	if !r.Enabled {
		return nil
	}
	if r.Grace < DefaultGrace {
		return errors.New("coordination retention grace is under 720h")
	}
	if err := r.registerMetrics(); err != nil {
		return err
	}
	defer r.unregisterMetrics()
	r.metrics.LastProgress.Set(float64(r.now().Unix()))
	wait := r.First
	if wait <= 0 {
		wait = firstWait
	}
	every := r.Every
	if every <= 0 {
		every = everyWait
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.after(wait):
		}
		wait = every
		limit := r.Deadline
		if limit <= 0 {
			limit = passDeadline
		}
		passCtx, cancel := context.WithTimeout(ctx, limit)
		r.Pass(passCtx)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
	}
}

// Pass runs one retention pass. A disabled runnable does nothing.
func (r *Runnable) Pass(ctx context.Context) string {
	if !r.Enabled {
		return ""
	}
	if r.Grace < DefaultGrace {
		return r.finishPass(resultFailed, nil)
	}
	if err := ctx.Err(); err != nil {
		return r.finishPass(resultPartial, nil)
	}
	if err := r.shelfReady(ctx); err != nil {
		return r.finishPass(r.shelfResult(err), nil)
	}
	summaries, err := Summaries(ctx, ListConfig{Reader: r.Reader, Namespace: r.Namespace, Enabled: r.Kinds})
	if err != nil {
		return r.finishPass(r.stepResult(err), nil)
	}
	evaluated := Evaluate(ctx, EvalConfig{Reader: r.Reader, OperatorNamespace: r.Namespace, ReconciledNamespaces: r.Reconciled}, summaries)
	looked, err := Lookup(ctx, r.Namespace, r.Slots, r.Archive, evaluated)
	if err != nil {
		return r.finishPass(r.stepResult(err), nil)
	}
	pending := pendingCounts(looked, r.now(), r.Grace)
	if err := r.resetKept(ctx, looked); err != nil {
		return r.finishPass(r.stepResult(err), nil)
	}
	if err := ctx.Err(); err != nil {
		return r.finishPass(resultPartial, nil)
	}
	unknowns := 0
	for _, item := range looked {
		if classify(item, r.now(), r.Grace) == "unknown" {
			unknowns++
		}
	}
	result := r.finishPass(r.mutate(ctx, looked), pending)
	r.Log.Info("coordination retention pass", "result", result, "unknown", unknowns)
	return result
}

func (r *Runnable) shelfResult(err error) string {
	if deadlineErr(err) {
		return resultPartial
	}
	if errors.Is(err, ErrLookupBusy) {
		return resultFailed
	}
	return resultShelfUnavailable
}

func (r *Runnable) stepResult(err error) string {
	switch {
	case deadlineErr(err):
		return resultPartial
	case historyCode(err) == protocol.HistoryErrNoArchive || errors.Is(err, shelf.ErrNoShelf):
		return resultShelfUnavailable
	default:
		return resultFailed
	}
}

func (r *Runnable) registerMetrics() error {
	if r.Registerer == nil {
		return errors.New("coordination retention metrics registerer is required")
	}
	m := newRetentionMetrics()
	m.prime()
	var registered []prometheus.Collector
	for _, collector := range m.collectors() {
		if err := r.Registerer.Register(collector); err != nil {
			for _, done := range registered {
				r.Registerer.Unregister(done)
			}
			return err
		}
		registered = append(registered, collector)
	}
	r.metrics = m
	r.registered = registered
	return nil
}

func (r *Runnable) unregisterMetrics() {
	for _, collector := range r.registered {
		r.Registerer.Unregister(collector)
	}
	r.registered = nil
}

func (r *Runnable) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Runnable) after(d time.Duration) <-chan time.Time {
	if r.After != nil {
		return r.After(d)
	}
	return time.After(d)
}

func (r *Runnable) markCap() int {
	if r.maxMarks > 0 {
		return r.maxMarks
	}
	return defaultCap
}

func (r *Runnable) releaseCap() int {
	if r.maxReleases > 0 {
		return r.maxReleases
	}
	return defaultCap
}

type orderKey struct {
	kind               Kind
	project, name, uid string
}

func (k orderKey) less(o orderKey) bool {
	if k.kind != o.kind {
		return k.kind < o.kind
	}
	if k.project != o.project {
		return k.project < o.project
	}
	if k.name != o.name {
		return k.name < o.name
	}
	return k.uid < o.uid
}

func (k orderKey) empty() bool {
	return k.kind == "" && k.project == "" && k.name == "" && k.uid == ""
}

func (k orderKey) String() string {
	return string(k.kind) + "\x00" + k.project + "\x00" + k.name + "\x00" + k.uid
}

func orderOf(s Summary) orderKey {
	return orderKey{kind: s.Kind, project: s.Project, name: s.Name, uid: s.UID}
}

type stepStatus struct {
	stop    string
	refused bool
	skipped bool
	deleted bool
	done    bool
	marked  bool
}

type candidate struct {
	item   LookupResult
	ack    *LookupResult
	action string
	key    orderKey
}

func graceOver(now, start time.Time, grace time.Duration) bool {
	if start.IsZero() || grace <= 0 {
		return false
	}
	return !now.Before(start.UTC().Add(grace))
}

func classify(item LookupResult, now time.Time, grace time.Duration) string {
	if !item.Result.Summary.Valid || item.Result.Decision == Unknown || item.Record == nil {
		return "unknown"
	}
	marked := item.Record.GraceStart != nil
	switch item.Result.Decision {
	case Released:
		if !marked {
			return "mark"
		}
		if !graceOver(now, *item.Record.GraceStart, grace) {
			return "pending"
		}
		return "release"
	case Kept:
		if marked {
			return "reset"
		}
		return "none"
	default:
		return "unknown"
	}
}

func pendingCounts(items []LookupResult, now time.Time, grace time.Duration) map[Kind]int {
	out := map[Kind]int{}
	for _, kind := range metricKinds {
		out[kind] = 0
	}
	for _, item := range items {
		if classify(item, now, grace) == "pending" {
			out[item.Result.Summary.Kind]++
		}
	}
	return out
}

func (r *Runnable) block(key orderKey) {
	if r.blocked == nil {
		r.blocked = map[string]struct{}{}
	}
	r.blocked[key.String()] = struct{}{}
}

func (r *Runnable) isBlocked(key orderKey) bool {
	_, ok := r.blocked[key.String()]
	return ok
}

func (r *Runnable) unblock(key orderKey) {
	delete(r.blocked, key.String())
}

func (r *Runnable) resetKept(ctx context.Context, items []LookupResult) error {
	acks := acksByMessage(items)
	groups := map[string][]protocol.CoordArchiveMarkRequest{}
	kinds := map[string]Kind{}
	var projects []string
	for _, item := range items {
		if classify(item, r.now(), r.Grace) != "reset" {
			continue
		}
		if item.Result.Summary.Kind == InboxReadAck && liveMessages(items)[item.Result.Summary.Message.UID] {
			continue
		}
		ev, err := presentEvent(item.Result.Summary.Resource, r.now(), item.Result.Observations)
		if err != nil {
			r.block(orderOf(item.Result.Summary))
			continue
		}
		r.queueReset(groups, &projects, kinds, item.Result.Summary, ev)
		if item.Result.Summary.Kind == InboxMessage {
			if ack, ok := acks[item.Result.Summary.UID]; ok && ack.Record != nil && ack.Record.GraceStart != nil {
				ackEvent, ackErr := presentEvent("inboxreadacks", r.now(), []protocol.CoordArchiveObservation{{
					Role: "message", Namespace: item.Result.Summary.Namespace, Name: item.Result.Summary.Name,
					UID: item.Result.Summary.UID, Observed: "present",
				}})
				if ackErr != nil {
					r.block(orderOf(ack.Result.Summary))
					continue
				}
				r.queueReset(groups, &projects, kinds, ack.Result.Summary, ackEvent)
			}
		}
	}
	sort.Strings(projects)
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch := groups[project]
		if err := r.markBatch(ctx, holdRequests(batch)); err != nil {
			for _, req := range batch {
				r.block(orderKey{kind: kinds[lookupKey(req.CoordArchiveIdentity)], project: req.Project, name: req.Name, uid: req.UID})
			}
			if deadlineErr(err) || historyCode(err) == protocol.HistoryErrNoArchive || errors.Is(err, shelf.ErrNoShelf) || errors.Is(err, ErrLookupBusy) {
				return err
			}
			continue
		}
		if err := r.markBatch(ctx, batch); err != nil {
			if deadlineErr(err) || historyCode(err) == protocol.HistoryErrNoArchive || errors.Is(err, shelf.ErrNoShelf) || errors.Is(err, ErrLookupBusy) {
				for _, req := range batch {
					r.block(orderKey{kind: kinds[lookupKey(req.CoordArchiveIdentity)], project: req.Project, name: req.Name, uid: req.UID})
				}
				return err
			}
			// A failed present leaves the old absent event in place. Try to
			// clear it now. If that reset also fails, this leader blocks the
			// record, and a later leader clears any grace it did not witness.
			for _, req := range batch {
				if clearErr := r.clearStaleGrace(ctx, req.CoordArchiveIdentity); clearErr != nil {
					r.block(orderKey{kind: kinds[lookupKey(req.CoordArchiveIdentity)], project: req.Project, name: req.Name, uid: req.UID})
					if deadlineErr(clearErr) || historyCode(clearErr) == protocol.HistoryErrNoArchive || errors.Is(clearErr, shelf.ErrNoShelf) || errors.Is(clearErr, ErrLookupBusy) {
						return clearErr
					}
					continue
				}
				r.unblock(orderKey{kind: kinds[lookupKey(req.CoordArchiveIdentity)], project: req.Project, name: req.Name, uid: req.UID})
			}
			continue
		}
		for _, req := range batch {
			kind := kinds[lookupKey(req.CoordArchiveIdentity)]
			r.countGrace(kind)
			r.unblock(orderKey{kind: kind, project: req.Project, name: req.Name, uid: req.UID})
		}
	}
	return nil
}

func (r *Runnable) queueReset(groups map[string][]protocol.CoordArchiveMarkRequest, projects *[]string, kinds map[string]Kind, s Summary, ev protocol.CoordArchiveEvent) {
	if _, ok := groups[s.Project]; !ok {
		*projects = append(*projects, s.Project)
	}
	id := identityOf(s)
	groups[s.Project] = append(groups[s.Project], protocol.CoordArchiveMarkRequest{CoordArchiveIdentity: id, Event: ev})
	kinds[lookupKey(id)] = s.Kind
}

func liveMessages(items []LookupResult) map[string]bool {
	out := map[string]bool{}
	for _, item := range items {
		if item.Result.Summary.Kind == InboxMessage && item.Result.Summary.Valid {
			out[item.Result.Summary.UID] = true
		}
	}
	return out
}

func acksByMessage(items []LookupResult) map[string]LookupResult {
	out := map[string]LookupResult{}
	for _, item := range items {
		if item.Result.Summary.Kind == InboxReadAck && item.Result.Summary.Valid {
			out[item.Result.Summary.Message.UID] = item
		}
	}
	return out
}

func (r *Runnable) candidates(items []LookupResult) []candidate {
	live := liveMessages(items)
	acks := acksByMessage(items)
	var out []candidate
	for _, item := range items {
		action := classify(item, r.now(), r.Grace)
		if item.Result.Summary.Kind == InboxReadAck && live[item.Result.Summary.Message.UID] {
			continue
		}
		if action != "mark" && action != "release" {
			continue
		}
		c := candidate{item: item, action: action, key: orderOf(item.Result.Summary)}
		if item.Result.Summary.Kind == InboxMessage {
			if ack, ok := acks[item.Result.Summary.UID]; ok {
				copyAck := ack
				c.ack = &copyAck
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key.less(out[j].key) })
	return out
}

func (r *Runnable) mutate(ctx context.Context, items []LookupResult) string {
	ring := r.candidates(items)
	if len(ring) == 0 {
		r.resume = orderKey{}
		return resultComplete
	}
	start := 0
	if !r.resume.empty() {
		start = len(ring)
		for i, item := range ring {
			if r.resume.less(item.key) {
				start = i
				break
			}
		}
		if start == len(ring) {
			start = 0
		}
	}
	marks, releases := 0, 0
	var last orderKey
	advanced := false
	for n := 0; n < len(ring); n++ {
		if err := ctx.Err(); err != nil {
			if advanced {
				r.resume = last
			}
			return resultPartial
		}
		item := ring[(start+n)%len(ring)]
		if (item.action == "mark" && marks >= r.markCap()) || (item.action == "release" && releases >= r.releaseCap()) {
			if advanced {
				r.resume = last
			}
			return resultCapped
		}
		status := r.perform(ctx, item)
		if status.stop != "" {
			if advanced {
				r.resume = last
			}
			return status.stop
		}
		last = item.key
		advanced = true
		switch {
		case item.action == "mark" && !status.skipped:
			marks++
		case item.action == "release" && !status.skipped && !status.done:
			releases++
		}
	}
	r.resume = orderKey{}
	return resultComplete
}

func (r *Runnable) perform(ctx context.Context, c candidate) stepStatus {
	if c.action == "release" && r.isBlocked(c.key) {
		if err := r.forceReset(ctx, c); err != nil {
			return r.refuseArchive(c.item.Result.Summary.Kind, err)
		}
		r.unblock(c.key)
		return stepStatus{skipped: true}
	}
	// A hold is the durable mark that the owner was seen and the open grace
	// must not be used. It survives a leader change. Clear it, and count the
	// clear against the release cap. Do not delete on this pass.
	if c.action == "release" && c.item.Record != nil && c.item.Record.LatestState == "hold" {
		if err := r.forceReset(ctx, c); err != nil {
			return r.refuseArchive(c.item.Result.Summary.Kind, err)
		}
		return stepStatus{}
	}
	if c.action == "mark" {
		return r.markReleased(ctx, c)
	}
	return r.release(ctx, c)
}

func holdRequests(batch []protocol.CoordArchiveMarkRequest) []protocol.CoordArchiveMarkRequest {
	out := make([]protocol.CoordArchiveMarkRequest, len(batch))
	for i, req := range batch {
		req.Event.State = "hold"
		out[i] = req
	}
	return out
}

func (r *Runnable) clearStaleGrace(ctx context.Context, id protocol.CoordArchiveIdentity) error {
	ev, err := resetEvent(id.Resource, r.now())
	if err != nil {
		return errArchiveInvalid
	}
	return r.markBatch(ctx, []protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: id, Event: ev}})
}

func (r *Runnable) forceReset(ctx context.Context, c candidate) error {
	ev, err := resetEvent(c.item.Result.Summary.Resource, r.now())
	if err != nil {
		return errArchiveInvalid
	}
	return r.markBatch(ctx, []protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: identityOf(c.item.Result.Summary), Event: ev}})
}

func (r *Runnable) markReleased(ctx context.Context, c candidate) stepStatus {
	if needsCompanionAck(c.item.Result.Observations) && c.ack == nil {
		return stepStatus{skipped: true}
	}
	rec, err := r.readRecord(ctx, c.item.Result.Summary)
	if apierrors.IsNotFound(err) || (err == nil && rec.summary.UID != c.item.Result.Summary.UID) {
		return stepStatus{skipped: true}
	}
	if err != nil {
		return r.refuseArchive(c.item.Result.Summary.Kind, err)
	}
	if !rec.summary.Valid || rec.summary.ResourceVersion != c.item.Result.Summary.ResourceVersion {
		return stepStatus{skipped: true}
	}
	ev, err := absentEvent(rec.summary.Resource, r.now(), c.item.Result.Observations)
	if err != nil {
		r.countRefused(rec.summary.Kind, reasonUnknown)
		return stepStatus{refused: true}
	}
	if err := r.putArchive(ctx, rec, &ev); err != nil {
		return r.refuseArchive(rec.summary.Kind, err)
	}
	r.countMarked(rec.summary.Kind)
	if c.ack != nil {
		ackRec, ackErr := r.readRecord(ctx, c.ack.Result.Summary)
		if ackErr != nil || !ackRec.summary.Valid || ackRec.summary.UID != c.ack.Result.Summary.UID || ackRec.summary.ResourceVersion != c.ack.Result.Summary.ResourceVersion {
			return stepStatus{marked: true}
		}
		if err := r.ensureAckMark(ctx, ackRec, rec.summary, r.now()); err != nil {
			if stop := r.refuseArchive(ackRec.summary.Kind, err); stop.stop != "" {
				return stop
			}
		}
	}
	return stepStatus{marked: true}
}
