package apiserver

import (
	"context"
	"encoding/json"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/codexhostpool"
)

const (
	codexCapacityName      = "dev-env-codex-host-capacity"
	codexCapacityNamespace = "dev-env-system"
	codexCapacityAttempt   = v1alpha1.LabelPrefix + "codex-host-create-attempt"
	codexCapacityReason    = v1alpha1.LabelPrefix + "codex-host-create-reason"
	codexCapacityRequester = v1alpha1.LabelPrefix + "codex-host-create-requester"
)

// One global CAS reservation serializes the list/decide/create boundary across
// API replicas. No TTL: a delayed create cannot arrive after its slot expired.
// A lost response keeps the slot until its exact attempt is found in the API.
// The record holds only names/profile/random attempt, never credentials.
type codexCapacityPending struct {
	Name    string `json:"name"`
	Profile string `json:"profile"`
	Attempt string `json:"attempt"`
}

func (s *Server) codexCapacity(ctx context.Context) (*corev1.ConfigMap, *codexCapacityPending, error) {
	var cm corev1.ConfigMap
	key := types.NamespacedName{Namespace: codexCapacityNamespace, Name: codexCapacityName}
	if err := s.Live.Get(ctx, key, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var pending *codexCapacityPending
	if raw := cm.Data["pending"]; raw != "" {
		if json.Unmarshal([]byte(raw), &pending) != nil || pending == nil || pending.Name == "" || pending.Profile == "" || pending.Attempt == "" {
			return nil, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "Codex host capacity reservation needs review")
		}
	}
	return &cm, pending, nil
}

func (s *Server) withPendingCodexHost(ctx context.Context, hosts []codexhostpool.Host) ([]codexhostpool.Host, error) {
	_, pending, err := s.codexCapacity(ctx)
	if err != nil || pending == nil {
		return hosts, err
	}
	for _, h := range hosts {
		if h.Name == pending.Name {
			return hosts, nil
		}
	}
	return append(hosts, codexhostpool.Host{Name: pending.Name, Agent: "codex", Mode: "remote", Phase: "Running", ResolvedProfile: pending.Profile, AccountResolved: true}), nil
}

func (s *Server) reserveCodexCapacity(ctx context.Context, want codexCapacityPending) error {
	for range codexHostLeaseTries {
		cm, pending, err := s.codexCapacity(ctx)
		if err != nil {
			return err
		}
		if cm == nil {
			cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: codexCapacityNamespace, Name: codexCapacityName}}
			if err := s.Client.Create(ctx, cm); err != nil && !apierrors.IsAlreadyExists(err) {
				return err
			}
			continue
		}
		if pending != nil {
			var made v1alpha1.AgentSession
			if err := s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: pending.Name}, &made); err != nil {
				if !apierrors.IsNotFound(err) {
					return err
				}
				return conflictReason(codexhostpool.ReasonHostBusy, "A Codex host create is pending or unconfirmed; its reservation is retained.")
			}
			if made.Annotations[codexCapacityAttempt] != pending.Attempt {
				return conflictReason(codexhostpool.ReasonHostBusy, "The Codex host reservation needs review before another create.")
			}
			// Seeing the exact committed attempt proves it can now be counted.
		}
		raw, err := json.Marshal(want)
		if err != nil {
			return err
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
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
	return conflictReason(codexhostpool.ReasonHostBusy, "Codex host capacity changed during admission; try again.")
}

func (s *Server) releaseCodexCapacity(ctx context.Context, attempt string) error {
	for range codexHostLeaseTries {
		cm, pending, err := s.codexCapacity(ctx)
		if err != nil || cm == nil || pending == nil || pending.Attempt != attempt {
			return err
		}
		delete(cm.Data, "pending")
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
	if err := s.reserveCodexCapacity(ctx, pending); err != nil {
		return err
	}
	// Everything before the session write is a definite non-create. Release
	// only our attempt; never clear another replica's later reservation.
	attempted := false
	defer func() {
		if !attempted {
			if err := s.releaseCodexCapacity(context.WithoutCancel(ctx), pending.Attempt); err != nil {
				s.Log.Error(err, "release Codex capacity reservation")
			}
		}
	}()
	if req.IdempotencyKey != "" {
		// Another replica may have committed after createSessionMode's first
		// dedupe read, but before this global admission reservation.
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
		if h.Name != pending.Name {
			filtered = append(filtered, h)
		}
	}
	if codexhostpool.RetainedInProfile(filtered, profile) >= codexhostpool.MaxHostsPerProfile {
		return conflictReason(codexhostpool.ReasonAtCapacity, "The profile already has two retained ordinary Codex hosts.")
	}
	fleetCap := codexhostpool.DefaultMaxRunning
	if s.CodexHosts != nil && s.CodexHosts.MaxRunning > 0 {
		fleetCap = s.CodexHosts.MaxRunning
	}
	if codexhostpool.RunningHosts(filtered) >= fleetCap {
		return conflictReason(codexhostpool.ReasonAtCapacity, "The running Codex host fleet cap is reached.")
	}
	if s.CodexHosts != nil {
		res := codexhostpool.Select(codexhostpool.Input{Hosts: filtered, Login: login, Caller: s.codexHostCaller(c), Request: codexhostpool.Request{Repository: sess.Spec.Repo, FreshHost: req.FreshHost}, ResolvedProfile: profile, DefaultProfile: tmpl.DefaultProfile, Now: s.now(), MaxRunningPerCell: s.CodexHosts.MaxRunningPerCell, MaxRunning: s.CodexHosts.MaxRunning})
		if res.Action != codexhostpool.ActionCreate {
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
	err = s.createWithCallerCap(ctx, sess, c)
	// A definite refusal cannot commit later. A transport or unknown error can.
	if err == nil || apierrors.IsAlreadyExists(err) || apierrors.IsInvalid(err) || apierrors.IsForbidden(err) || apierrors.IsBadRequest(err) {
		if releaseErr := s.releaseCodexCapacity(context.WithoutCancel(ctx), pending.Attempt); releaseErr != nil {
			s.Log.Error(releaseErr, "release Codex capacity reservation")
		}
	}
	return err
}
