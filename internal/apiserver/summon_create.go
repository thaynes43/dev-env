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
//  3. The dispatch id, chosen before anything is written.
//  4. The pool, until story 741's queues: 429 past it.
//  5. The ledger write that names the id: a new charge (429 when the hour is
//     full), an attempt mark or a re-charge. A charge of the id with another
//     hash is 409, and a session of the id is returned with 200.
//  6. The create of the charge's name, within summonCreateDeadline of that
//     write, carrying the id and the generation. On AlreadyExists the API
//     reads that session: the same id is this request's (200, or 409 if it is
//     not this caller's or has another hash); any other is a name clash, and
//     the charge is rebound to the next free suffix by one more ledger write
//     before the next create.
//
// So every 422, 403 and 409 before step 5 spends nothing, and no session is
// created unless a ledger write naming its id came first. A create refused
// after its charge (no free name left, or the API server refusing the object)
// is a charged failure, and its text says so.
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
	// The longest name this create may take, with its brief, so a later
	// suffix cannot add a transport refusal after the charge.
	candidates := summonNameSequence(req.Name)
	preview := sess.DeepCopy()
	preview.Name = candidates[len(candidates)-1]
	preview.Spec.Prompt = brief(preview.Name)
	if err := checkAgentd(preview); err != nil {
		return 0, nil, err
	}

	urgent := entry.Priority == v1alpha1.SummonPriorityUrgent
	limit, inPool, err := summonPool(c, entry, list.Items)
	if err != nil {
		return 0, nil, err
	}
	if inPool >= limit {
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
		return 0, nil, tooManyRequests("caller %s has made %d summoning creates in the last hour, its createsPerHour (DESIGN-064 5.9)",
			c.policy.Name, c.policy.Spec.Summon.Limits.CreatesPerHour)
	}
	return s.createSummonCharged(ctx, w, c, req, sess, b, brief, candidates)
}

// createSummonCharged creates the name a ledger write bound the dispatch to:
// within summonCreateDeadline of that write, and once more if a NotFound
// follows its AlreadyExists. A clash with another dispatch's session rebinds
// the charge by one more ledger write, and the next create is of its name.
func (s *Server) createSummonCharged(ctx context.Context, w http.ResponseWriter, c *caller, req summonWire, sess *v1alpha1.AgentSession, b summonBinding, brief func(string) string, candidates []string) (int, any, error) {
	id := sess.Spec.Summon.DispatchID
	hash := sess.Annotations[v1alpha1.AnnotationRequestHash]
	retried := false
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
			return 0, nil, summonChargedFailure(newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable,
				"the charge for this dispatch left the ledger during its create; no session was created"))
		}
		remaining := summonCreateDeadline - s.summonMonotonic().Sub(b.written)
		if remaining <= 0 {
			return 0, nil, summonChargedFailure(newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable,
				"the session create could not be issued within %s of the ledger write naming its dispatch id; no session was created", summonCreateDeadline))
		}
		sess.Name = b.name
		sess.ResourceVersion = ""
		sess.Spec.Prompt = brief(b.name)
		// The create, and the read after its AlreadyExists, run under what is
		// left of the deadline, so a call held in the client's rate limiter or
		// on a slow connection cannot reach the API server after the charge's
		// attempt hold (the API server bounds its own handling of a request).
		callCtx, cancel := context.WithTimeout(ctx, remaining)
		err := s.Client.Create(callCtx, sess, client.FieldValidation(metav1.FieldValidationStrict))
		if err == nil {
			cancel()
			s.Log.Info("session created", "session", sess.Name, "caller", c.String(), "repo", sess.Spec.Repo,
				"agent", sess.Spec.Agent, "mode", sess.Spec.Mode, "model", sess.Spec.Model, "lane", sess.Spec.Lane,
				"dispatchID", id, "generation", sess.Annotations[v1alpha1.AnnotationSummonGeneration], "charge", summonChargeLog(b.result))
			w.Header().Set("Location", apiv1.SessionPath(sess.Name))
			return http.StatusCreated, view(sess, false), nil
		}
		if !apierrors.IsAlreadyExists(err) {
			cancel()
			// The charge stays: a spent slot of its hour. A keyed retry maps
			// to the same id and is not charged again.
			return 0, nil, summonChargedFailure(fromKubeError(err, "create session"))
		}
		var held v1alpha1.AgentSession
		err = s.Live.Get(callCtx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: b.name}, &held)
		cancel()
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return 0, nil, summonChargedFailure(fromKubeError(err, "session "+b.name))
			}
			if retried {
				return 0, nil, summonChargedFailure(newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable,
					"session %s was reported as existing and then not found, twice; no session was created", b.name))
			}
			retried = true
			continue
		}
		if held.Spec.Summon != nil && held.Spec.Summon.DispatchID == id {
			return s.summonExisting(w, c, req.IdempotencyKey, hash, &held)
		}
		if b, err = s.rebindSummonCharge(ctx, c, id, hash, b.name, candidates); err != nil {
			return 0, nil, summonChargedFailure(err)
		}
		retried = false
	}
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

// summonChargedFailure marks a refusal made after the dispatch's charge was
// written: the hour's slot is spent, and no refund is made, since another
// replica may be using the same dispatch (DESIGN-064 5.9, the correction's
// charged failures).
func summonChargedFailure(err error) error {
	var ae *apiError
	if !errors.As(err, &ae) {
		ae = internal("%v", err)
	}
	out := *ae
	out.body.Message += "; the hour's slot was spent and the charge for this dispatch stays (DESIGN-064 5.9)"
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
// highest cannot come from this code, which binds one dispatch id to one
// name; it comes only from sessions without the annotation or a hand edit,
// and goes to the greater UID, so every replica picks the same session and
// so the same dispatch id (coordinator, answer to op758-fix1-q-1). A
// generation that is not a non-negative integer is 503: nothing is charged
// or created.
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

// summonGeneration reads a session's generation: absent is 0.
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

// summonTerminalOutcome is a reported outcome of done, failed or escalated.
func summonTerminalOutcome(s *v1alpha1.AgentSession) bool {
	if s.Status.Outcome == nil {
		return false
	}
	switch s.Status.Outcome.State {
	case v1alpha1.OutcomeDone, v1alpha1.OutcomeFailed, v1alpha1.OutcomeEscalated:
		return true
	}
	return false
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
// pool, from an uncached list (DESIGN-064 6.4 "Limits", until story 741 adds
// the queues): limits.escalations for the escalation entry, and
// limits.concurrent for every other entry. It is not a reservation: each
// replica counts under its own create lock, so replicas that each have a
// create in flight can pass the same count. Until story 741, a pool can be
// exceeded by at most the number of API replicas minus one (with two
// replicas, one).
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
