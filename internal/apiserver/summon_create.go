package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
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
// 5.9 "The create ledger"). It runs under the create lock, in this order:
//
//  1. The repeat. A key whose newest session of this caller is unfinished
//     and has no terminal outcome returns that session with 200, or 409 when
//     its mode, lane or requested name differ. Nothing is charged or written.
//  2. The name: the requested one if free, else, for a re-fired key, the
//     first free suffix -2 to -99. A name held by any other session is 409.
//  3. The dispatch id, chosen before anything is written.
//  4. The pool, until story 741's queues: 429 past it.
//  5. The charge, committed in the ledger; 429 when the hour is full.
//  6. The create, carrying the id. On AlreadyExists the API reads that
//     session: the same id is this request's (200), any other takes the next
//     suffix.
//
// So every 422, 403 and 409 comes before the charge and spends nothing, and
// no session is created before its charge is committed.
func (s *Server) dispatchSummon(ctx context.Context, w http.ResponseWriter, c *caller, req summonWire, entry *v1alpha1.CallerPolicySummonSession, sess *v1alpha1.AgentSession) (int, any, error) {
	if s.Live == nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "sessions cannot be read")
	}
	var list v1alpha1.AgentSessionList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace)); err != nil {
		return 0, nil, fromKubeError(err, "list sessions")
	}
	key := req.IdempotencyKey
	var previous *v1alpha1.AgentSession
	var mine map[types.UID]bool
	if key != "" {
		own := summonKeySessions(c, key, list.Items)
		previous = newestSummonSession(c.parent, key, own)
		if previous != nil && summonLive(previous) {
			if previous.Annotations[v1alpha1.AnnotationRequestHash] != sess.Annotations[v1alpha1.AnnotationRequestHash] {
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
	names, err := summonNames(req.Name, key, list.Items, mine)
	if err != nil {
		return 0, nil, err
	}

	id := summonDispatchID(c.parent, key, previous)
	if key == "" {
		if id, err = newSummonDispatchID(); err != nil {
			s.Log.Error(err, "summon dispatch id")
			return 0, nil, internal("the dispatch id could not be drawn")
		}
	}
	sess.Spec.Summon.DispatchID = id
	// The longest name this create may take, so a later suffix cannot add a
	// transport refusal after the charge.
	preview := sess.DeepCopy()
	preview.Name = names[len(names)-1]
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
	result, err := s.chargeSummon(ctx, c, id, urgent, c.policy.Spec.Summon.Limits.CreatesPerHour)
	if err != nil {
		return 0, nil, err
	}
	if result == summonHourFull {
		return 0, nil, tooManyRequests("caller %s has made %d summoning creates in the last hour, its createsPerHour (DESIGN-064 5.9)",
			c.policy.Name, c.policy.Spec.Summon.Limits.CreatesPerHour)
	}

	for _, name := range names {
		sess.Name = name
		sess.ResourceVersion = ""
		err := s.Client.Create(ctx, sess, client.FieldValidation(metav1.FieldValidationStrict))
		if err == nil {
			s.Log.Info("session created", "session", sess.Name, "caller", c.String(), "repo", sess.Spec.Repo,
				"agent", sess.Spec.Agent, "mode", sess.Spec.Mode, "model", sess.Spec.Model, "lane", sess.Spec.Lane,
				"dispatchID", id, "charge", summonChargeLog(result))
			w.Header().Set("Location", apiv1.SessionPath(sess.Name))
			return http.StatusCreated, view(sess, false), nil
		}
		if !apierrors.IsAlreadyExists(err) {
			// The charge stays: a spent slot of its hour. A keyed retry maps
			// to the same id and is not charged again.
			return 0, nil, fromKubeError(err, "create session")
		}
		var held v1alpha1.AgentSession
		if err := s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: name}, &held); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return 0, nil, fromKubeError(err, "session "+name)
		}
		if held.Spec.Summon != nil && held.Spec.Summon.DispatchID == id && summonOwns(c, &held) {
			w.Header().Set("Location", apiv1.SessionPath(held.Name))
			return http.StatusOK, view(&held, false), nil
		}
	}
	return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict,
		"no free session name: %s and its suffixes to -%d are taken", req.Name, summonMaxSuffix)
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

// newestSummonSession is the newest of one key's sessions by creation time.
// Two created in one second are ordered by their ids: a session whose id is
// the hash over the other's UID came after it. The name decides the rest.
func newestSummonSession(account, key string, own []*v1alpha1.AgentSession) *v1alpha1.AgentSession {
	var best *v1alpha1.AgentSession
	for _, it := range own {
		if best == nil || summonNewer(account, key, it, best) {
			best = it
		}
	}
	return best
}

func summonNewer(account, key string, a, b *v1alpha1.AgentSession) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return b.CreationTimestamp.Before(&a.CreationTimestamp)
	}
	if summonDispatchOf(a) == summonDispatchID(account, key, b) {
		return true
	}
	if summonDispatchOf(b) == summonDispatchID(account, key, a) {
		return false
	}
	return a.Name > b.Name
}

func summonDispatchOf(s *v1alpha1.AgentSession) string {
	if s.Spec.Summon == nil {
		return ""
	}
	return s.Spec.Summon.DispatchID
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
	all := make([]string, 0, summonMaxSuffix)
	all = append(all, requested)
	for i := 2; i <= summonMaxSuffix; i++ {
		all = append(all, fmt.Sprintf("%s-%d", requested, i))
	}
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

// summonPool counts the caller's unfinished summoned sessions in the entry's
// pool, from an uncached list (DESIGN-064 6.4 "Limits", until story 741 adds
// the queues): limits.escalations for the escalation entry, and
// limits.concurrent for every other entry.
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
	if r == summonChargedBefore {
		return "already charged"
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
