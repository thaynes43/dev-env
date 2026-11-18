package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/codexhostpool"
)

const (
	codexCapacityName           = "dev-env-codex-host-capacity"
	codexCapacityNamespace      = "dev-env-system"
	codexCapacityAttempt        = v1alpha1.LabelPrefix + "codex-host-create-attempt"
	codexCapacityReason         = v1alpha1.LabelPrefix + "codex-host-create-reason"
	codexCapacityRequester      = v1alpha1.LabelPrefix + "codex-host-create-requester"
	codexHostCreateTimeout      = 30 * time.Second
	codexCapacityCleanupTimeout = 10 * time.Second
)

// CAS adds counted reservations across API replicas. No TTL: a delayed create
// cannot arrive after its slot expired. One uncertain write occupies one slot,
// rather than preventing every other profile from admitting a host.
// A lost response keeps the slot until its exact attempt is found in the API.
// The record holds only names/profile/random attempt, never credentials.
type codexCapacityPending struct {
	Name       string `json:"name"`
	Profile    string `json:"profile"`
	Attempt    string `json:"attempt"`
	RequestKey string `json:"requestKey,omitempty"`
}

func (s *Server) codexCapacity(ctx context.Context) (*corev1.ConfigMap, []codexCapacityPending, error) {
	var cm corev1.ConfigMap
	namespace := s.PolicyNamespace
	if namespace == "" {
		namespace = codexCapacityNamespace
	}
	key := types.NamespacedName{Namespace: namespace, Name: codexCapacityName}
	if s.Live == nil {
		return nil, nil, capacityLedgerUnavailable(key, "unreadable")
	}
	if err := s.Live.Get(ctx, key, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, capacityLedgerUnavailable(key, "missing")
		}
		if apierrors.IsForbidden(err) {
			return nil, nil, capacityLedgerUnavailable(key, "forbidden")
		}
		return nil, nil, capacityLedgerUnavailable(key, "unreadable")
	}
	var pending []codexCapacityPending
	if raw := cm.Data["pending"]; raw != "" {
		if len(raw) > 64<<10 || json.Unmarshal([]byte(raw), &pending) != nil || pending == nil {
			return nil, nil, capacityLedgerUnavailable(key, "malformed")
		}
		for _, p := range pending {
			if p.Name == "" || p.Profile == "" || p.Attempt == "" {
				return nil, nil, capacityLedgerUnavailable(key, "malformed")
			}
		}
	}
	return &cm, pending, nil
}

// Read the reservations BEFORE listing sessions. If a creator commits and
// releases during the list, the snapshot still counts its reservation. Reading
// reservations after the list could miss both that new host and its old slot.
func withPendingCodexHosts(hosts []codexhostpool.Host, pending []codexCapacityPending) []codexhostpool.Host {
	for _, p := range pending {
		found := false
		for _, h := range hosts {
			if h.Name == p.Name && h.CreateAttempt == p.Attempt {
				found = true
				break
			}
		}
		if !found {
			hosts = append(hosts, codexhostpool.Host{Name: p.Name, ReservationAttempt: p.Attempt, Agent: "codex", Mode: "remote", Phase: "Running", ResolvedProfile: p.Profile, AccountResolved: true})
		}
	}
	return hosts
}

func (s *Server) reserveCodexCapacity(ctx context.Context, want codexCapacityPending) error {
	for range codexHostLeaseTries {
		cm, pending, err := s.codexCapacity(ctx)
		if err != nil {
			return err
		}
		retained := pending[:0]
		cell := 0
		for _, p := range pending {
			var made v1alpha1.AgentSession
			err := s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: p.Name}, &made)
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			if err == nil && made.Annotations[codexCapacityAttempt] == p.Attempt {
				continue
			}
			if want.RequestKey != "" && p.RequestKey == want.RequestKey {
				return unconfirmedCodexCreates([]codexCapacityPending{p})
			}
			// Missing or a different attempt is not proof this create stopped.
			retained = append(retained, p)
			if p.Profile == want.Profile {
				cell++
			}
		}
		fleetCap := codexhostpool.DefaultMaxRunning
		if s.CodexHosts != nil && s.CodexHosts.MaxRunning > 0 {
			fleetCap = s.CodexHosts.MaxRunning
		}
		if cell >= codexhostpool.MaxHostsPerProfile || len(retained) >= fleetCap {
			return unconfirmedCodexCreates(retained)
		}
		retained = append(retained, want)
		raw, err := json.Marshal(retained)
		if err != nil {
			return err
		}
		if len(raw) > 64<<10 {
			return conflictReason(codexhostpool.ReasonAtCapacity, "Codex host capacity reservations have reached their storage bound.")
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data["pending"] = string(raw)
		if err := s.Client.Update(ctx, cm); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			if apierrors.IsForbidden(err) {
				return capacityLedgerUnavailable(types.NamespacedName{Namespace: cm.Namespace, Name: cm.Name}, "forbidden")
			}
			if apierrors.IsNotFound(err) {
				return capacityLedgerUnavailable(types.NamespacedName{Namespace: cm.Namespace, Name: cm.Name}, "missing")
			}
			return err
		}
		return nil
	}
	return conflictReason(codexhostpool.ReasonHostBusy, "Codex host capacity changed during admission; try again.")
}

func (s *Server) releaseCodexCapacity(ctx context.Context, attempt string) error {
	for range codexHostLeaseTries {
		cm, pending, err := s.codexCapacity(ctx)
		if err != nil || cm == nil {
			return err
		}
		retained := pending[:0]
		for _, p := range pending {
			if p.Attempt != attempt {
				retained = append(retained, p)
			}
		}
		if len(retained) == len(pending) {
			return nil
		}
		raw, err := json.Marshal(retained)
		if err != nil {
			return err
		}
		cm.Data["pending"] = string(raw)
		if err := s.Client.Update(ctx, cm); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return err
		}
		return nil
	}
	return conflictReason(codexhostpool.ReasonHostBusy, "The Codex host capacity reservation could not be released.")
}

// createOrdinaryCodexHost retains the legacy write for other session kinds.
// Codex remote creation has an atomic reservation even when selection is off:
// direct API clients cannot bypass the fixed retained-host bound.
func (s *Server) createOrdinaryCodexHost(ctx context.Context, sess *v1alpha1.AgentSession, c *caller, req apiv1.CreateSessionRequest) error {
	if !codexRemoteSession(sess) {
		return s.createWithCallerCap(ctx, sess, c)
	}
	// newSession already pinned the profile/account for this HTTP request.
	// Reuse that same template snapshot at the final admission boundary.
	account := s.codexHostProfile(ctx, sess.Spec.Profile)
	memo, ok := ctx.Value(codexHostAdmissionKey{}).(*codexHostAdmission)
	if !ok || memo.templates == nil {
		return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "Codex host admission templates are unavailable")
	}
	tmpl := memo.templates
	if !account.Available() || account.Workbench() {
		return forbidden("An ordinary Codex host needs a resolved ordinary profile.")
	}
	profile := sess.Spec.Profile
	if profile == "" {
		profile = tmpl.DefaultProfile
	}
	pending := codexCapacityPending{Name: sess.Name, Profile: profile, Attempt: string(uuid.NewUUID())}
	if req.IdempotencyKey != "" {
		digest := sha256.Sum256([]byte(c.parent + "\x00" + req.IdempotencyKey))
		pending.RequestKey = hex.EncodeToString(digest[:])
	}
	if err := s.reserveCodexCapacity(ctx, pending); err != nil {
		return err
	}
	// Everything before the session write is a definite non-create. Release
	// only our attempt; never clear another replica's later reservation.
	attempted := false
	defer func() {
		if !attempted {
			s.finishCodexCapacity(ctx, pending.Attempt)
		}
	}()
	if req.IdempotencyKey != "" {
		// Another replica may have committed after createSessionMode's first
		// dedupe read. Same-key in-flight reservations were fenced above.
		prior, err := s.byIdempotencyKey(ctx, c.parent, req.IdempotencyKey)
		if err != nil {
			return err
		}
		if prior != nil {
			return conflictReason("create_replayed", "The create committed during admission; retry with the same idempotency key.")
		}
	}
	hosts, login, err := s.codexHostFacts(ctx, tmpl)
	if err != nil {
		return err
	}
	// The admission's own pending record is not an existing host.
	filtered := hosts[:0]
	for _, h := range hosts {
		if h.ReservationAttempt != pending.Attempt {
			filtered = append(filtered, h)
		}
	}
	if codexhostpool.RetainedInProfile(filtered, profile) >= codexhostpool.MaxHostsPerProfile {
		if unknown := unconfirmedCodexHosts(filtered, profile); unknown != nil {
			return unknown
		}
		return conflictReason(codexhostpool.ReasonAtCapacity, "The profile already has two retained ordinary Codex hosts.")
	}
	fleetCap := codexhostpool.DefaultMaxRunning
	if s.CodexHosts != nil && s.CodexHosts.MaxRunning > 0 {
		fleetCap = s.CodexHosts.MaxRunning
	}
	if codexhostpool.RunningHosts(filtered) >= fleetCap {
		if unknown := unconfirmedCodexHosts(filtered, ""); unknown != nil {
			return unknown
		}
		return conflictReason(codexhostpool.ReasonAtCapacity, "The running Codex host fleet cap is reached.")
	}
	if s.CodexHosts != nil && sess.Spec.Restore == "" {
		res := codexhostpool.Select(codexhostpool.Input{Hosts: filtered, Login: login, Caller: s.codexHostCaller(c), Request: codexhostpool.Request{Repository: sess.Spec.Repo, FreshHost: req.FreshHost}, ResolvedProfile: profile, DefaultProfile: tmpl.DefaultProfile, Now: s.now(), MaxRunningPerCell: s.CodexHosts.MaxRunningPerCell, MaxRunning: s.CodexHosts.MaxRunning})
		if res.Action != codexhostpool.ActionCreate {
			if res.Reason == codexhostpool.ReasonCreateUnconfirmed {
				return unconfirmedCodexHosts(filtered, "")
			}
			if res.Action == "" {
				return conflictReason(res.Reason, "Codex host selection refused creation (%s); no host was created.", res.Reason)
			}
			return conflictReason(res.Reason, "Codex host selection returned %s for %s; no host was created.", res.Action, res.Host)
		}
		if res.Reason != "" {
			if sess.Annotations == nil {
				sess.Annotations = map[string]string{}
			}
			sess.Annotations[codexCapacityReason] = res.Reason
			sess.Annotations[codexCapacityRequester] = c.String()
		}
	}
	if sess.Annotations == nil {
		sess.Annotations = map[string]string{}
	}
	sess.Annotations[codexCapacityAttempt] = pending.Attempt
	attempted = true
	// A client hang-up must not turn an otherwise conclusive create into an
	// unknown outcome. The write retains request values but owns its deadline.
	createCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexHostCreateTimeout)
	defer cancel()
	err = s.createWithCallerCap(createCtx, sess, c)
	if err == nil || definiteCodexCreateRefusal(err) {
		s.finishCodexCapacity(ctx, pending.Attempt)
		return err
	}
	s.Log.Error(err, "Codex host create outcome unknown", "attempt", pending.Attempt, "session", pending.Name, "requester", c.String())
	// One authoritative name read can resolve a lost create response. A
	// different attempt or any read error is still unknown and keeps the slot.
	readCtx, readCancel := context.WithTimeout(context.WithoutCancel(ctx), codexCapacityCleanupTimeout)
	defer readCancel()
	var made v1alpha1.AgentSession
	if readErr := s.Live.Get(readCtx, types.NamespacedName{Namespace: sess.Namespace, Name: pending.Name}, &made); readErr == nil && made.Annotations[codexCapacityAttempt] == pending.Attempt {
		s.finishCodexCapacity(ctx, pending.Attempt)
	}
	refusal := unconfirmedCodexCreates([]codexCapacityPending{pending})
	refusal.status, refusal.body.Code = http.StatusServiceUnavailable, apiv1.CodeUnavailable
	return refusal
}

func capacityLedgerUnavailable(key types.NamespacedName, state string) *apiError {
	e := newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "Codex host capacity ConfigMap %s is %s; counted create or resume is unavailable.", key, state)
	e.body.Reason = "capacity_ledger_unavailable"
	return e
}

func unconfirmedCodexCreates(pending []codexCapacityPending) *apiError {
	parts := make([]string, 0, len(pending))
	for _, p := range pending {
		parts = append(parts, p.Name+" (attempt "+p.Attempt+")")
	}
	return conflictReason(codexhostpool.ReasonCreateUnconfirmed, "Unconfirmed Codex host create occupies capacity: %s; its reservation is retained.", strings.Join(parts, "; "))
}

func unconfirmedCodexHosts(hosts []codexhostpool.Host, profile string) *apiError {
	var pending []codexCapacityPending
	for _, h := range hosts {
		if h.ReservationAttempt != "" && (profile == "" || h.ResolvedProfile == profile) {
			pending = append(pending, codexCapacityPending{Name: h.Name, Attempt: h.ReservationAttempt})
		}
	}
	if len(pending) == 0 {
		return nil
	}
	return unconfirmedCodexCreates(pending)
}

func definiteCodexCreateRefusal(err error) bool {
	var refused *apiError
	if errors.As(err, &refused) || apierrors.IsAlreadyExists(err) || apierrors.IsInvalid(err) || apierrors.IsForbidden(err) || apierrors.IsBadRequest(err) {
		return true
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		switch status.Status().Code {
		case 429, 404, 405, 406, 413, 415:
			return true
		}
	}
	return false
}

// Retry transient read/update failures as well as the inner CAS conflicts.
// No sleep, expiry, or reset: exhaustion leaves this exact attempt counted.
func (s *Server) finishCodexCapacity(ctx context.Context, attempt string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexCapacityCleanupTimeout)
	defer cancel()
	var err error
	for range codexHostLeaseTries {
		err = s.releaseCodexCapacity(cleanupCtx, attempt)
		if err == nil || cleanupCtx.Err() != nil {
			break
		}
	}
	if err != nil {
		s.Log.Error(err, "release Codex capacity reservation", "attempt", attempt)
	}
}
