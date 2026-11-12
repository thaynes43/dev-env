package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// dispatchSummon is the part of a summoning create after the request is
// checked against the policy (DESIGN-064 6.4 "Idempotency" and "Limits",
// 5.9 "The create ledger" and its correction of 2026-11-12). It runs under the
// create lock, in this order:
//
//  1. The repeat. The newest of this caller's sessions of the key, by
//     generation, if unfinished and with no terminal outcome, returns with 200,
//     or 409 when its mode, lane or requested name differ. Nothing is charged
//     or written. A generation that cannot be read is 503.
//  2. The name: the requested one if free, else, for a re-fired key, the
//     first free suffix -2 to -99. A name held by any other session is 409,
//     unless the key's dispatch already holds a charge, whose name it creates.
//  3. The dispatch id, chosen before anything is written, and the agentd
//     transport bound on the longest name with that id. createSummonSession
//     ran the same bound before the switch, with a placeholder id.
//  4. The pool. With --enable-summoned-lanes off: 429 past it. On, nothing
//     here: the create is Queued and the leader admits it (OP777, OP778).
//  5. The ledger write that decides the binding: a new charge (429 when the
//     hour is full, then, with the lanes on, 429 when its queue is full,
//     counted in that same write: summon_queue.go), an attempt mark or a
//     re-charge. A charge of the id with another hash is 409, and a session
//     of the id is returned with 200.
//  6. Each create attempt: first the mark, a compare-and-set ledger write
//     that succeeds only while the charge binds the id to the name about to
//     be created (otherwise the dispatch follows the binding it returns), then
//     the create of that name, carrying the id and the generation, under one
//     context done summonCreateDeadline after the mark. On AlreadyExists the
//     API reads that session under the same context: the same id is this
//     request's (200, or 409 if it is not this caller's or has another hash);
//     any other is a name clash, and the charge is rebound to the next free
//     suffix by one more ledger write. A NotFound there retries the name once,
//     after its own mark.
//
// So every refusal before step 5 (422, 403, 409, and 429 by the pool) spends
// nothing, nor does a 429 of the hour or of a queue at step 5's first write
// for a new charge, and every create attempt follows its own ledger write naming its
// id and its name. A failure after step 5's write keeps the charge, whatever
// its status (409, 422, 429, 503 or another): there is no refund, and normal
// pruning applies. The 503 for a charge that has already left the ledger
// carries no retention sentence. One request makes at most
// summonMaxCreateAttempts session creates. What a client cannot close, a
// create the API server persists after its charge was pruned and a stale
// destination after a rebind, is stated at the top of summon_ledger.go;
// admission in story 741 enforces the rest.
func (s *Server) dispatchSummon(ctx context.Context, w http.ResponseWriter, c *caller, req summonWire, entry *v1alpha1.CallerPolicySummonSession, sess *v1alpha1.AgentSession, brief func(name string) string) (int, any, error) {
	if s.Live == nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "sessions cannot be read")
	}
	var list v1alpha1.AgentSessionList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace)); err != nil {
		return 0, nil, fromKubeError(err, "list sessions")
	}
	key := req.IdempotencyKey
	hash := sess.Annotations[v1alpha1.AnnotationRequestHash]
	var previous *v1alpha1.AgentSession
	var generation int64
	var mine map[types.UID]bool
	if key != "" {
		own := summonKeySessions(c, key, list.Items)
		var err error
		previous, generation, err = newestSummonSession(own)
		if err != nil {
			return 0, nil, err
		}
		if previous != nil && summonLive(previous) {
			if previous.Annotations[v1alpha1.AnnotationRequestHash] != hash {
				return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict,
					"idempotency key %q already started session %s with a different mode, lane or name", key, previous.Name)
			}
			w.Header().Set("Location", apiv1.SessionPath(previous.Name))
			return http.StatusOK, view(previous, false), nil
		}
		mine = make(map[types.UID]bool, len(own))
		for _, it := range own {
			mine[it.UID] = true
		}
	}
	names, nameErr := summonNames(req.Name, key, list.Items, mine)

	id := summonDispatchID(c.parent, key, previous)
	if key == "" {
		if nameErr != nil {
			return 0, nil, nameErr
		}
		var err error
		if id, err = newSummonDispatchID(); err != nil {
			s.Log.Error(err, "summon dispatch id")
			return 0, nil, internal("the dispatch id could not be drawn")
		}
	} else if nameErr != nil {
		// A refused name spends nothing, unless this dispatch is already
		// charged: then it creates the charge's name, not one of its own.
		held, err := s.summonChargeHeld(ctx, c, id, hash)
		if err != nil {
			return 0, nil, err
		}
		if !held {
			return 0, nil, nameErr
		}
	}
	sess.Spec.Summon.DispatchID = id
	if sess.Annotations == nil {
		sess.Annotations = map[string]string{}
	}
	sess.Annotations[v1alpha1.AnnotationSummonGeneration] = strconv.FormatInt(generation+1, 10)
	// The longest name this create may take, with its brief and its dispatch
	// id, so a later suffix cannot add a transport refusal after the charge.
	// createSummonSession ran the same check before the switch with a
	// placeholder id of this length; this is the exact one.
	candidates := summonNameSequence(req.Name)
	if err := checkSummonTransport(sess, req.Name, id, brief); err != nil {
		return 0, nil, err
	}

	urgent := entry.Priority == v1alpha1.SummonPriorityUrgent
	limit, inPool, err := summonPool(c, entry, list.Items)
	if err != nil {
		return 0, nil, err
	}
	// With --enable-summoned-lanes the pool is the leader's: a create past it
	// is Queued, and its queue is counted inside the ledger write below.
	if !s.SummonedLanes && inPool >= limit {
		if urgent {
			if err := s.countSummonUrgentRefusal(ctx, c); err != nil {
				return 0, nil, err
			}
		}
		return 0, nil, tooManyRequests("caller %s has %d unfinished summoned sessions in the %s pool, its limit; one must finish first (DESIGN-064 6.4)",
			c.policy.Name, inPool, summonPoolName(entry))
	}
	first := ""
	if len(names) > 0 {
		first = names[0]
	}
	b, err := s.bindSummonCharge(ctx, c, id, hash, first, nameErr, urgent, c.policy.Spec.Summon.Limits.CreatesPerHour)
	if err != nil {
		return 0, nil, err
	}
	switch b.result {
	case summonNameRefused:
		return 0, nil, nameErr
	case summonHourFull:
		return 0, nil, tooManyRequests("caller %s has made %d charges in the 65 minute window by the entry's clock, its createsPerHour (DESIGN-064 5.9)",
			c.policy.Name, c.policy.Spec.Summon.Limits.CreatesPerHour)
	case summonQueueFull:
		return 0, nil, summonQueueRefusal(c, b, false)
	case summonGrantsUnreadable:
		return 0, nil, summonGrantsFault()
	}
	return s.createSummonCharged(ctx, w, c, req, sess, b, brief, candidates, urgent, c.policy.Spec.Summon.Limits.CreatesPerHour)
}

// createSummonCharged creates the name the ledger binds the dispatch to. Each
// attempt is preceded by its own mark (markSummonAttempt), and runs under one
// context, done at the mark's absolute deadline: the create, and the read
// after its AlreadyExists. A mark that finds the binding moved follows it. A
// clash with another dispatch's session rebinds the charge by one more ledger
// write; a NotFound after AlreadyExists retries the name once. The request
// stops after summonMaxCreateAttempts session creates.
func (s *Server) createSummonCharged(ctx context.Context, w http.ResponseWriter, c *caller, req summonWire, sess *v1alpha1.AgentSession, b summonBinding, brief func(string) string, candidates []string, urgent bool, perHour int32) (int, any, error) {
	id := sess.Spec.Summon.DispatchID
	hash := sess.Annotations[v1alpha1.AnnotationRequestHash]
	retried := false
	creates := 0
	for {
		switch b.result {
		case summonSessionExists:
			return s.summonExisting(w, c, req.IdempotencyKey, hash, b.existing)
		case summonOtherRequest:
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict,
				"idempotency key %q is already charged for a create with a different mode, lane or name (DESIGN-064 5.9)", req.IdempotencyKey)
		case summonNoFreeName:
			return 0, nil, summonChargedFailure(newError(http.StatusConflict, apiv1.CodeConflict,
				"no free session name: %s and its suffixes to -%d are taken", req.Name, summonMaxSuffix))
		case summonChargeGone:
			// The charge has already left. This 503 carries no retention sentence.
			return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable,
				"the charge for this dispatch left the ledger during its create; no session was created")
		case summonHourFull:
			// Past its window, and not charged again. There is no refund, and
			// normal pruning applies; this write may already have dropped it.
			return 0, nil, tooManyRequests(
				"caller %s has made %d charges in the 65 minute window by the entry's clock, its createsPerHour; this dispatch's charge is past its window and was not charged again; there is no refund and normal pruning applies; no session was created (DESIGN-064 5.9)",
				c.policy.Name, perHour)
		case summonQueueFull:
			// A mark or rebind of a charge whose attempt had passed the hold,
			// into a queue that filled meanwhile: the charge stays.
			return 0, nil, summonQueueRefusal(c, b, true)
		case summonGrantsUnreadable:
			return 0, nil, summonChargedFailure(summonGrantsFault())
		}
		m, err := s.markSummonAttempt(ctx, c, id, hash, b.name, urgent, perHour)
		if err != nil {
			return 0, nil, summonChargedFailure(err)
		}
		if m.result != summonMarked && m.result != summonRecharged {
			if m.result == summonMoved {
				retried = false
			}
			b = m
			continue
		}
		if creates >= summonMaxCreateAttempts {
			return 0, nil, summonChargedFailure(newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable,
				"this request made %d session create attempts; no session was created", summonMaxCreateAttempts))
		}
		creates++
		sess.Name = m.name
		sess.ResourceVersion = ""
		sess.Spec.Prompt = brief(m.name)
		// One context, done at the mark's absolute deadline, for the create
		// and the read after its AlreadyExists: a call still held in the
		// client (its rate limiter, a slow connection, this process paused)
		// is refused by the client once it is done. A request the API server
		// has already received, and a process frozen before it sends, are
		// out of the client's reach: the first stated limit.
		callCtx, cancel := s.summonClock().WithDeadline(ctx, m.deadline)
		if callCtx.Err() != nil {
			cancel()
			return 0, nil, summonChargedFailure(newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable,
				"the session create could not be issued within %s of the ledger write naming its dispatch id; no session was created", summonCreateDeadline))
		}
		err = s.Client.Create(callCtx, sess, client.FieldValidation(metav1.FieldValidationStrict))
		if err == nil {
			cancel()
			charge := b.result
			if m.result == summonRecharged {
				charge = m.result
			}
			s.Log.Info("session created", "session", sess.Name, "caller", c.String(), "repo", sess.Spec.Repo,
				"agent", sess.Spec.Agent, "mode", sess.Spec.Mode, "model", sess.Spec.Model, "lane", sess.Spec.Lane,
				"dispatchID", id, "generation", sess.Annotations[v1alpha1.AnnotationSummonGeneration], "charge", summonChargeLog(charge))
			w.Header().Set("Location", apiv1.SessionPath(sess.Name))
			return http.StatusCreated, view(sess, false), nil
		}
		if !apierrors.IsAlreadyExists(err) {
			late := callCtx.Err() != nil
			cancel()
			if late {
				return 0, nil, summonDeadlinePassed()
			}
			// There is no refund, and normal pruning applies. A keyed retry maps
			// to the same id and is not charged again while the charge remains.
			return 0, nil, summonChargedFailure(fromKubeError(err, "create session"))
		}
		var held v1alpha1.AgentSession
		err = s.Live.Get(callCtx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: m.name}, &held)
		late := callCtx.Err() != nil
		cancel()
		if err != nil {
			if late {
				return 0, nil, summonDeadlinePassed()
			}
			if !apierrors.IsNotFound(err) {
				return 0, nil, summonChargedFailure(fromKubeError(err, "session "+m.name))
			}
			if retried {
				return 0, nil, summonChargedFailure(newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable,
					"session %s was reported as existing and then not found, twice; no session was created", m.name))
			}
			retried = true
			b = m
			continue
		}
		if held.Spec.Summon != nil && held.Spec.Summon.DispatchID == id {
			return s.summonExisting(w, c, req.IdempotencyKey, hash, &held)
		}
		if b, err = s.rebindSummonCharge(ctx, c, id, hash, m.name, candidates, urgent, perHour); err != nil {
			return 0, nil, summonChargedFailure(err)
		}
		retried = false
	}
}

// summonDeadlinePassed is the refusal when the create's context was done
// before the create returned. The client sent nothing it knows of after the
// deadline, but a request the API server had already received may still be
// persisted: a keyed retry maps to the same id and returns that session.
func summonDeadlinePassed() error {
	return summonChargedFailure(newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable,
		"the session create did not complete within %s of the ledger write naming its dispatch id; if the Kubernetes API server had already received it, the session may still be created, and a retry with the same idempotency key returns it", summonCreateDeadline))
}

// summonExisting answers for the session that already carries this
// dispatch's id: 200 if it is this caller's and has this request's hash,
// else 409.
func (s *Server) summonExisting(w http.ResponseWriter, c *caller, key, hash string, held *v1alpha1.AgentSession) (int, any, error) {
	if !summonOwns(c, held) || held.Annotations[v1alpha1.AnnotationRequestHash] != hash {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict,
			"session %s carries this dispatch id for another caller or another mode, lane or name (idempotency key %q)", held.Name, key)
	}
	w.Header().Set("Location", apiv1.SessionPath(held.Name))
	return http.StatusOK, view(held, false), nil
}

// summonChargedFailure marks a refusal made after the dispatch's ledger
// write, whatever its status. There is no refund, and normal pruning applies,
// since another replica may be using the same dispatch (DESIGN-064 5.9, the
// correction's charged failures and its amendment). A charge that has already
// left is not this function's: that 503 carries no retention sentence.
func summonChargedFailure(err error) error {
	var ae *apiError
	if !errors.As(err, &ae) {
		ae = internal("%v", err)
	}
	out := *ae
	out.body.Message += "; there is no refund and normal pruning applies (DESIGN-064 5.9)"
	return &out
}

// summonRequestHash is what a repeat of a key must match: mode, lane and the
// requested name, not data. A re-fire with fresher data returns the session
// already running and does not change its brief. The requested name, not the
// name the session took, so the repeat of a create that took a suffix still
// matches.
func summonRequestHash(req summonWire) string {
	sum := sha256.Sum256([]byte("dev-env.summon.request.v1\x00" + req.Mode + "\x00" + req.Lane + "\x00" + req.Name))
	return hex.EncodeToString(sum[:])
}

// summonDispatchID is a keyed create's dispatch id (DESIGN-064 5.9, Q-21): a
// hash of the caller's account, the key and the UID of the key's newest
// earlier session, or "none". A retry of one create sees the same newest
// earlier session, so it maps to the same id; a re-fire after that session
// ended gets a new one. It is 32 hex characters.
func summonDispatchID(account, key string, previous *v1alpha1.AgentSession) string {
	prev := "none"
	if previous != nil {
		prev = string(previous.UID)
	}
	sum := sha256.Sum256([]byte("dev-env.summon.dispatch.v1\x00" + account + "\x00" + key + "\x00" + prev))
	return hex.EncodeToString(sum[:16])
}

// summonKeySessions are the caller's own sessions that carry key.
func summonKeySessions(c *caller, key string, items []v1alpha1.AgentSession) []*v1alpha1.AgentSession {
	var out []*v1alpha1.AgentSession
	for i := range items {
		it := &items[i]
		if summonOwns(c, it) && it.Spec.IdempotencyKey == key && it.Labels[v1alpha1.LabelIdempotencyKey] == key {
			out = append(out, it)
		}
	}
	return out
}

// newestSummonSession is the newest of one key's sessions: the highest
// generation (v1alpha1.AnnotationSummonGeneration; none counts as 0), and
// its generation. Creation times and names do not order. A tie on the
// highest comes from sessions without the annotation, a hand edit, or the
// stale-destination limit (two sessions of one dispatch, so of one
// generation; see summon_ledger.go), and goes to the greater UID, so every
// replica picks the same session and so the same dispatch id (coordinator,
// answer to op758-fix1-q-1). A generation that is not a non-negative integer
// is 503: nothing is charged or created.
func newestSummonSession(own []*v1alpha1.AgentSession) (*v1alpha1.AgentSession, int64, error) {
	var best *v1alpha1.AgentSession
	var bestGen int64
	for _, it := range own {
		g, err := summonGeneration(it)
		if err != nil {
			return nil, 0, err
		}
		if best == nil || g > bestGen || (g == bestGen && it.UID > best.UID) {
			best, bestGen = it, g
		}
	}
	return best, bestGen, nil
}

// summonGeneration reads a session's generation: absent is 0. The supported
// range is 0 to math.MaxInt64-1; the maximum is refused like an unreadable
// value, since the next generation would not fit, and so is any value with a
// sign or another character.
func summonGeneration(s *v1alpha1.AgentSession) (int64, error) {
	v, ok := s.Annotations[v1alpha1.AnnotationSummonGeneration]
	if !ok {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || v == "" || strings.Trim(v, "0123456789") != "" || n == math.MaxInt64 {
		return 0, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable,
			"session %s has a generation that is not a non-negative integer; nothing was charged or created (DESIGN-064 5.9)", s.Name)
	}
	return n, nil
}

// summonLive is a session a repeat of its key returns: unfinished, and no
// terminal outcome.
func summonLive(s *v1alpha1.AgentSession) bool {
	return unfinished(s) && !summonTerminalOutcome(s)
}

// summonTerminalOutcome is a reported or guaranteed terminal outcome.
func summonTerminalOutcome(s *v1alpha1.AgentSession) bool {
	return summonoutcome.Terminal(s.Status.Outcome)
}

// summonNames are the names a create may take, in order, from the first one
// free in items. The requested name comes first. When another session holds
// it, a re-fired key whose own earlier session holds it goes on to the
// suffixes -2 to -99; any other holder is 409, as is a keyless create's. mine
// is the key's own sessions. The later names are tried only if the create
// meets a name another request took after this read.
func summonNames(requested, key string, items []v1alpha1.AgentSession, mine map[types.UID]bool) ([]string, error) {
	held := make(map[string]*v1alpha1.AgentSession, len(items))
	for i := range items {
		held[items[i].Name] = &items[i]
	}
	all := summonNameSequence(requested)
	if h := held[requested]; h != nil {
		if key == "" || !mine[h.UID] {
			return nil, newError(http.StatusConflict, apiv1.CodeConflict,
				"session %s already exists and is not an earlier session of this idempotency key", requested)
		}
	}
	for i, name := range all {
		if held[name] == nil {
			return all[i:], nil
		}
	}
	return nil, newError(http.StatusConflict, apiv1.CodeConflict,
		"no free session name: %s and its suffixes to -%d are taken", requested, summonMaxSuffix)
}

// summonDispatchIDPlaceholder is as long as both dispatch id forms, 32 hex
// characters (summonDispatchID and newSummonDispatchID). The transport bound
// before the switch uses it, since the id is chosen only after the switch.
var summonDispatchIDPlaceholder = strings.Repeat("0", 32)

// checkSummonTransport is the agentd transport bound for a summoning create
// (DESIGN-064 5.9, the correction of 2026-11-12, and ruling D4 on item 741):
// the session document of the longest name the create may take, with that
// name's brief and dispatch id id, must fit agentd's bounds. It reads nothing.
func checkSummonTransport(sess *v1alpha1.AgentSession, requested, id string, brief func(string) string) error {
	candidates := summonNameSequence(requested)
	preview := sess.DeepCopy()
	preview.Name = candidates[len(candidates)-1]
	preview.Spec.Prompt = brief(preview.Name)
	if preview.Spec.Summon != nil {
		preview.Spec.Summon.DispatchID = id
	}
	return checkAgentd(preview)
}

// summonNameSequence is every name a create for requested may take, in
// order: requested, then -2 to -99.
func summonNameSequence(requested string) []string {
	all := make([]string, 0, summonMaxSuffix)
	all = append(all, requested)
	for i := 2; i <= summonMaxSuffix; i++ {
		all = append(all, fmt.Sprintf("%s-%d", requested, i))
	}
	return all
}

// summonPool counts the caller's unfinished summoned sessions in the entry's
// pool, from an uncached list (DESIGN-064 6.4 "Limits"): limits.escalations
// for the escalation entry, and limits.concurrent for every other entry. Its
// count refuses a create only with --enable-summoned-lanes off; with the lanes
// on the leader's admission decides the pool, and the create is Queued
// (summon_queue.go). Either way it refuses a policy whose pool limit is not
// valid. It is not a reservation: each replica counts under its own create
// lock, so replicas that each have a create in flight can pass the same
// count, and a pool can be exceeded by at most the number of API replicas
// minus one (with two replicas, one).
func summonPool(c *caller, entry *v1alpha1.CallerPolicySummonSession, items []v1alpha1.AgentSession) (limit, n int, err error) {
	limits := c.policy.Spec.Summon.Limits
	escalation := entry.Lane == v1alpha1.LaneEscalation
	if escalation {
		if limits.Escalations == nil || *limits.Escalations < 1 {
			return 0, 0, forbidden("CallerPolicy entry is not a valid summon entry")
		}
		limit = int(*limits.Escalations)
	} else {
		if limits.Concurrent < 1 {
			return 0, 0, forbidden("CallerPolicy entry is not a valid summon entry")
		}
		limit = int(limits.Concurrent)
	}
	for i := range items {
		it := &items[i]
		if summonOwns(c, it) && it.Spec.Summon != nil && unfinished(it) && (it.Spec.Lane == v1alpha1.LaneEscalation) == escalation {
			n++
		}
	}
	return limit, n, nil
}

func summonPoolName(entry *v1alpha1.CallerPolicySummonSession) string {
	if entry.Lane == v1alpha1.LaneEscalation {
		return "escalations"
	}
	return "concurrent"
}

func summonChargeLog(r summonChargeResult) string {
	switch r {
	case summonMarked:
		return "already charged, marked"
	case summonRecharged:
		return "charged again"
	case summonRebound:
		return "rebound"
	case summonMoved:
		return "rebound by another replica"
	}
	return "charged"
}

// summonLogValue bounds a request value for a log line.
func summonLogValue(v string) string {
	if len(v) > 32 {
		return v[:32]
	}
	return v
}

// logSummonRefusal logs one refused summoning create: the caller, the lane
// and the reason. A 422 logs the names of its fields, not their messages,
// since a schema's message can quote a value; the API's own messages quote
// none.
func (s *Server) logSummonRefusal(c *caller, lane string, err error) {
	var ae *apiError
	if !errors.As(err, &ae) {
		s.Log.Info("summon create refused", "caller", c.String(), "lane", lane, "status", http.StatusInternalServerError)
		return
	}
	reason := ae.body.Message
	if ae.status == http.StatusUnprocessableEntity {
		names := make([]string, 0, len(ae.body.Fields))
		for _, f := range ae.body.Fields {
			names = append(names, summonLogValue(f.Field))
		}
		reason = "invalid: " + strings.Join(names, ", ")
	}
	if len(reason) > 256 {
		reason = reason[:256]
	}
	s.Log.Info("summon create refused", "caller", c.String(), "lane", lane, "status", ae.status, "code", ae.body.Code, "reason", reason)
}
