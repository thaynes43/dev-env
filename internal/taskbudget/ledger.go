// Package taskbudget implements retained, deterministic logical task admission.
// A ledger is not a supervisor: integration must stop its exact owned executor
// after observing a latch, and supply independently checked stop evidence.
package taskbudget

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	StallLimit   = 60 * time.Minute
	FailureLimit = 3
	MaxEvents    = 512
	MaxWorkers   = 64
)

var (
	ErrDenied      = errors.New("task budget admission denied")
	ErrConflict    = errors.New("task budget authority changed")
	ErrUnavailable = errors.New("task budget authority unavailable")
	identifier     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)
)

type Binding = apiv1.TaskBudgetBinding
type Spec = apiv1.TaskBudgetSpec

type Worker struct {
	ID           string    `json:"id"`
	Active       bool      `json:"active"`
	StartedAt    time.Time `json:"startedAt"`
	StoppedAt    time.Time `json:"stoppedAt,omitempty"`
	StopEvidence string    `json:"stopEvidence,omitempty"`
}

type Event struct {
	Request apiv1.TaskBudgetEvent `json:"request"`
	At      time.Time             `json:"at"`
}

type Extension struct {
	Request apiv1.TaskBudgetExtension `json:"request"`
	Owner   string                    `json:"owner"`
	At      time.Time                 `json:"at"`
}

// Ledger has no owner reference to ephemeral sessions or Pods. Prior events,
// blockers, workers and decisions survive every epoch and executor change.
type Ledger struct {
	Version                   int                          `json:"version"`
	Binding                   Binding                      `json:"binding"`
	Spec                      Spec                         `json:"spec"`
	Parent                    string                       `json:"parent"`
	CreatedAt                 time.Time                    `json:"createdAt"`
	AccountedAt               time.Time                    `json:"accountedAt"`
	EffortMilliseconds        int64                        `json:"effortMilliseconds"`
	EffortCeilingMilliseconds int64                        `json:"effortCeilingMilliseconds"`
	LastProgress              time.Time                    `json:"lastProgress"`
	StallAnchor               time.Time                    `json:"stallAnchor"`
	NextCheckpoint            time.Time                    `json:"nextCheckpoint"`
	CheckpointSeconds         int64                        `json:"checkpointSeconds"`
	Latched                   bool                         `json:"latched"`
	Reason                    string                       `json:"reason,omitempty"`
	Escalation                *apiv1.TaskBudgetEscalation  `json:"escalation,omitempty"`
	Escalations               []apiv1.TaskBudgetEscalation `json:"escalations,omitempty"`
	Workers                   []Worker                     `json:"workers,omitempty"`
	Events                    []Event                      `json:"events,omitempty"`
	Extensions                []Extension                  `json:"extensions,omitempty"`
	// FailureAllowance records an explicit owner-authorized extra failed attempt;
	// it never removes failures or resolves an old blocker.
	FailureAllowance map[string]int `json:"failureAllowance,omitempty"`
}

// Store's Update is a resource-version compare-and-swap. Unknown write results
// must be returned, never blindly replayed; only typed conflicts may retry.
type Store interface {
	Create(context.Context, *Ledger) error
	Read(context.Context, string) (*Ledger, string, error)
	Update(context.Context, *Ledger, string) error
}

// Validator resolves a bound receipt under independent supervisor/resource
// authority. No runtime validator is supplied by default. Agents cannot validate
// their own progress, failure classification, stop, phone delivery or budget
// extensions. For failures it checks the stable attempt and canonical blocker
// against the receipt, including when a caller proposes a different technique.
type Validator interface {
	Validate(context.Context, *Ledger, apiv1.TaskBudgetEvent) error
	ValidateOwnerDecision(context.Context, *Ledger, apiv1.TaskBudgetExtension, string) error
}

type Service struct {
	Store     Store
	Validator Validator
	Now       func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func validSpec(spec Spec) bool {
	return len(spec.SuccessCondition) > 0 && len(spec.SuccessCondition) <= 1024 &&
		spec.OverallSeconds > 0 && spec.OverallSeconds <= 86400 &&
		spec.EffortSeconds > 0 && spec.EffortSeconds <= 86400 &&
		spec.CheckpointSeconds > 0 && spec.CheckpointSeconds <= spec.OverallSeconds
}

func validBinding(b Binding) bool {
	return identifier.MatchString(b.TaskUID) && identifier.MatchString(b.HostID) &&
		identifier.MatchString(b.PodUID) && b.Epoch > 0 && !b.Deadline.IsZero()
}

func (s *Service) Create(ctx context.Context, taskUID, hostID, podUID, parent string, spec Spec) (*Ledger, error) {
	if s == nil || s.Store == nil {
		return nil, ErrUnavailable
	}
	if !identifier.MatchString(taskUID) || !identifier.MatchString(hostID) || !identifier.MatchString(podUID) || parent == "" || !validSpec(spec) {
		return nil, ErrDenied
	}
	now := s.now()
	l := &Ledger{Version: 1, Binding: Binding{TaskUID: taskUID, HostID: hostID, PodUID: podUID, Epoch: 1,
		Deadline: now.Add(time.Duration(spec.OverallSeconds) * time.Second)}, Spec: spec, Parent: parent,
		CreatedAt: now, AccountedAt: now, LastProgress: now, StallAnchor: now,
		NextCheckpoint: now.Add(time.Duration(spec.CheckpointSeconds) * time.Second), CheckpointSeconds: spec.CheckpointSeconds, EffortCeilingMilliseconds: spec.EffortSeconds * 1000}
	if err := s.Store.Create(ctx, l); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Ledger) checkBinding(b Binding) error {
	if !validBinding(b) || b.TaskUID != l.Binding.TaskUID || b.Epoch != l.Binding.Epoch ||
		b.HostID != l.Binding.HostID || b.PodUID != l.Binding.PodUID || !b.Deadline.Equal(l.Binding.Deadline) {
		return ErrDenied
	}
	return nil
}

func (l *Ledger) account(now time.Time) error {
	if now.Before(l.AccountedAt) {
		return ErrUnavailable
	}
	// Clock-boundary subtraction retains fractional intervals across frequent
	// observations; truncating each elapsed duration would undercount effort.
	delta := now.UnixMilli() - l.AccountedAt.UnixMilli()
	for _, w := range l.Workers {
		if w.Active {
			l.EffortMilliseconds += delta
		}
	}
	l.AccountedAt = now
	return nil
}

func (l *Ledger) latch(now time.Time, reason string) {
	if l.Latched {
		return
	}
	l.Latched, l.Reason = true, reason
	l.Escalation = &apiv1.TaskBudgetEscalation{ID: fmt.Sprintf("%s:%d", l.Binding.TaskUID, l.Binding.Epoch),
		Epoch: l.Binding.Epoch, Reason: reason, At: now, Delivery: "Pending"}
}

func (l *Ledger) evaluate(now time.Time) {
	if l.Latched {
		return
	}
	switch {
	case !now.Before(l.Binding.Deadline):
		l.latch(now, "OverallBudget")
	case l.EffortMilliseconds >= l.EffortCeilingMilliseconds:
		l.latch(now, "EffortBudget")
	case !now.Before(l.StallAnchor.Add(StallLimit)):
		l.latch(now, "Stalled")
	case !now.Before(l.NextCheckpoint):
		l.latch(now, "Checkpoint")
	case len(l.Events) >= MaxEvents || len(l.Workers) >= MaxWorkers:
		l.latch(now, "HistoryBound")
	}
	counts := map[string]int{}
	resolved := map[string]bool{}
	for _, e := range l.Events {
		if e.Request.Kind == "failure" {
			counts[e.Request.BlockerID]++
			resolved[e.Request.BlockerID] = false
		}
		if e.Request.Kind == "resolved" {
			resolved[e.Request.BlockerID] = true
		}
	}
	for blocker, n := range counts {
		if !resolved[blocker] && n >= FailureLimit+l.FailureAllowance[blocker] {
			l.latch(now, "RepeatedFailure")
			break
		}
	}
}

func (s *Service) mutate(ctx context.Context, b Binding, fn func(*Ledger, time.Time) error) (*Ledger, error) {
	if s == nil || s.Store == nil || !validBinding(b) {
		return nil, ErrUnavailable
	}
	for range 5 {
		l, rv, err := s.Store.Read(ctx, b.TaskUID)
		if err != nil {
			return nil, err
		}
		if err := l.checkBinding(b); err != nil {
			return nil, err
		}
		now := s.now()
		if err := l.account(now); err != nil {
			return nil, err
		}
		l.evaluate(now)
		// Refusals are persisted too: an expired task must latch before any
		// caller requests a stop. A stale binding never changes the new epoch.
		operationErr := fn(l, now)
		l.evaluate(now)
		if err := s.Store.Update(ctx, l, rv); err != nil {
			if errors.Is(err, ErrConflict) {
				continue
			}
			return nil, err
		}
		return l, operationErr
	}
	return nil, ErrConflict
}

func (s *Service) Observe(ctx context.Context, b Binding) (*Ledger, error) {
	return s.mutate(ctx, b, func(*Ledger, time.Time) error { return nil })
}

// Admit never clears a latch. Its exact confirmation is necessary before
// model work, continuation, child dispatch or executor creation.
func (s *Service) Admit(ctx context.Context, b Binding) (*Ledger, error) {
	return s.mutate(ctx, b, func(l *Ledger, now time.Time) error {
		if l.Latched {
			return ErrDenied
		}
		if s.Validator == nil {
			return ErrUnavailable
		}
		id := fmt.Sprintf("host:%s:%d", b.HostID, b.Epoch)
		for _, w := range l.Workers {
			if w.ID == id {
				if !w.Active {
					return ErrDenied
				}
				return nil
			}
		}
		l.Workers = append(l.Workers, Worker{ID: id, Active: true, StartedAt: now})
		return nil
	})
}

func validEvent(e apiv1.TaskBudgetEvent) bool {
	if !identifier.MatchString(e.ID) {
		return false
	}
	switch e.Kind {
	case "activity", "heartbeat":
		return true
	case "failure":
		return identifier.MatchString(e.AttemptID) && identifier.MatchString(e.BlockerID) && validEvidence(e.Evidence)
	case "worker-start":
		return identifier.MatchString(e.WorkerID)
	case "progress", "resolved", "worker-stop", "notification-delivered":
		return validEvidence(e.Evidence) &&
			(e.Kind != "resolved" || identifier.MatchString(e.BlockerID)) &&
			(e.Kind != "worker-stop" || identifier.MatchString(e.WorkerID))
	case "notification-failed":
		return true
	}
	return false
}

func validEvidence(e apiv1.TaskBudgetEvidence) bool {
	return identifier.MatchString(e.ID) && len(e.Reference) > 0 && len(e.Reference) <= 256 && len(e.Kind) > 0 && len(e.Kind) <= 64
}

func (s *Service) Record(ctx context.Context, e apiv1.TaskBudgetEvent) (*Ledger, error) {
	if !validEvent(e) {
		return nil, ErrDenied
	}
	return s.mutate(ctx, e.Binding, func(l *Ledger, now time.Time) error {
		for _, prior := range l.Events {
			if prior.Request.ID == e.ID || (e.Kind == "failure" && prior.Request.Kind == "failure" && prior.Request.AttemptID == e.AttemptID) {
				if reflect.DeepEqual(prior.Request, e) {
					return nil
				}
				return ErrConflict
			}
			if (e.Kind == "progress" || e.Kind == "resolved") && (prior.Request.Kind == "progress" || prior.Request.Kind == "resolved") &&
				(prior.Request.Evidence.ID == e.Evidence.ID || prior.Request.Evidence.Reference == e.Evidence.Reference) {
				return ErrConflict
			}
		}
		if len(l.Events) >= MaxEvents {
			return ErrDenied
		}
		if l.Latched && (e.Kind == "worker-start" || e.Kind == "progress" || e.Kind == "resolved") {
			return ErrDenied
		}
		if e.Kind == "worker-start" && s.Validator == nil {
			return ErrUnavailable
		}
		if e.Kind == "failure" || e.Kind == "progress" || e.Kind == "resolved" || e.Kind == "worker-stop" || e.Kind == "notification-delivered" {
			if s.Validator == nil {
				return ErrUnavailable
			}
			if err := s.Validator.Validate(ctx, l, e); err != nil {
				return ErrDenied
			}
		}
		switch e.Kind {
		case "worker-start":
			for _, w := range l.Workers {
				if w.ID == e.WorkerID {
					return ErrDenied
				}
			}
			if len(l.Workers) >= MaxWorkers {
				return ErrDenied
			}
			l.Workers = append(l.Workers, Worker{ID: e.WorkerID, Active: true, StartedAt: now})
		case "worker-stop":
			found := false
			for i := range l.Workers {
				if l.Workers[i].ID == e.WorkerID && l.Workers[i].Active {
					l.Workers[i].Active = false
					l.Workers[i].StoppedAt = now
					l.Workers[i].StopEvidence = e.Evidence.Reference
					found = true
				}
			}
			if !found {
				return ErrDenied
			}
		case "progress", "resolved":
			l.LastProgress, l.StallAnchor = now, now
			l.NextCheckpoint = now.Add(time.Duration(l.CheckpointSeconds) * time.Second)
			if l.NextCheckpoint.After(l.Binding.Deadline) {
				l.NextCheckpoint = l.Binding.Deadline
			}
		case "notification-delivered", "notification-failed":
			if l.Escalation == nil || l.Escalation.Delivery != "Pending" {
				return ErrDenied
			}
			if e.Kind == "notification-delivered" {
				l.Escalation.Delivery = "Delivered"
			} else {
				l.Escalation.Delivery = "Failed"
			}
		}
		l.Events = append(l.Events, Event{Request: e, At: now})
		return nil
	})
}

func (l *Ledger) stopped() bool {
	if len(l.Workers) == 0 {
		return false
	}
	for _, w := range l.Workers {
		if w.Active || w.StopEvidence == "" {
			return false
		}
	}
	return true
}

// Extend requires an authenticated owner and an independently validated exact
// decision. This seam stays unusable until real native question provenance and
// phone-delivery evidence are integrated; a free-form request is not authority.
func (s *Service) Extend(ctx context.Context, e apiv1.TaskBudgetExtension, authenticatedOwner string) (*Ledger, error) {
	if authenticatedOwner == "" || !identifier.MatchString(e.DecisionID) || !validEvidence(e.Evidence) || len(e.NextStep) == 0 || len(e.NextStep) > 1024 ||
		!validSpec(Spec{SuccessCondition: e.NextStep, OverallSeconds: e.OverallSeconds, EffortSeconds: e.EffortSeconds, CheckpointSeconds: e.CheckpointSeconds}) {
		return nil, ErrDenied
	}
	return s.mutate(ctx, e.Binding, func(l *Ledger, now time.Time) error {
		if !l.Latched || !l.stopped() || l.Escalation == nil || l.Escalation.Delivery != "Delivered" || s.Validator == nil {
			return ErrDenied
		}
		if err := s.Validator.ValidateOwnerDecision(ctx, l, e, authenticatedOwner); err != nil {
			return ErrDenied
		}
		for _, prior := range l.Extensions {
			if prior.Request.DecisionID == e.DecisionID {
				return ErrDenied
			}
		}
		if len(l.Extensions) >= 32 {
			return ErrDenied
		}
		l.Extensions = append(l.Extensions, Extension{Request: e, Owner: authenticatedOwner, At: now})
		l.Escalations = append(l.Escalations, *l.Escalation)
		l.Binding.Epoch++
		l.Binding.Deadline = now.Add(time.Duration(e.OverallSeconds) * time.Second)
		l.EffortCeilingMilliseconds = l.EffortMilliseconds + e.EffortSeconds*1000
		l.CheckpointSeconds = e.CheckpointSeconds
		l.NextCheckpoint = now.Add(time.Duration(e.CheckpointSeconds) * time.Second)
		l.StallAnchor = now // authorized time window; LastProgress is retained.
		if l.FailureAllowance == nil {
			l.FailureAllowance = map[string]int{}
		}
		counts := map[string]int{}
		for _, prior := range l.Events {
			if prior.Request.Kind == "failure" {
				counts[prior.Request.BlockerID]++
			}
		}
		for blocker, n := range counts {
			if n >= FailureLimit+l.FailureAllowance[blocker] {
				l.FailureAllowance[blocker] = n - FailureLimit + 1
			}
		}
		l.Latched = false
		l.Reason = ""
		l.Escalation = nil
		// Stop history remains intact. A new epoch uses new worker IDs.
		return nil
	})
}

func (l *Ledger) Status() apiv1.TaskBudgetStatus {
	st := apiv1.TaskBudgetStatus{Observed: true, Binding: l.Binding, Latched: l.Latched, Reason: l.Reason,
		EffortMilliseconds: l.EffortMilliseconds, UsageAccounting: "SupervisorElapsedEstimate; provider usage unavailable",
		LastProgress: l.LastProgress, NextCheckpoint: l.NextCheckpoint, Escalation: l.Escalation, StopConfirmed: l.stopped()}
	blockers := map[string]apiv1.TaskBudgetBlocker{}
	for _, e := range l.Events {
		if e.Request.Kind == "failure" || e.Request.Kind == "resolved" {
			b := blockers[e.Request.BlockerID]
			b.ID = e.Request.BlockerID
			if e.Request.Kind == "failure" {
				b.FailedAttempts++
				b.Resolved = false
			} else {
				b.Resolved = true
			}
			blockers[b.ID] = b
		}
		if e.Request.Evidence.Reference != "" {
			proof := e.Request.Evidence
			st.LastEvidence = &proof
		}
	}
	ids := make([]string, 0, len(blockers))
	for id := range blockers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		st.Blockers = append(st.Blockers, blockers[id])
	}
	return st
}
