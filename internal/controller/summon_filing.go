package controller

// The leader's platform filing (DESIGN-064 6.6, OP801). The caller's entry
// and the admission grants share the ConfigMap CAS. Every retry reads the
// records again; no in-memory flag authorizes a charge or a create.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/summonbrief"
	"github.com/thaynes43/dev-env/internal/summonledger"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

const (
	summonFilingDeadline = 2 * time.Minute
	summonFilingAttempts = 5
	// Coordinator ruling 5 on item 768 comment 7702. Story 747 must confirm
	// or replace this inert text before a remediation caller moves.
	summonFilingTemplate = "An autonomous remediation could not finish and the platform filed this escalation. Tom will join this session. Report what the data block says in one message. Change nothing until Tom speaks."
)

// SummonFilingClock measures the create's deadline independently of the
// joined ledger clock. Tests advance it without sleeps.
type SummonFilingClock interface {
	Now() time.Time
	WithDeadline(context.Context, time.Time) (context.Context, context.CancelFunc)
}

type filingProcessClock struct{}

func (filingProcessClock) Now() time.Time { return time.Now() }
func (filingProcessClock) WithDeadline(ctx context.Context, at time.Time) (context.Context, context.CancelFunc) {
	return context.WithDeadline(ctx, at)
}

func (r *Reconciler) filingClock() SummonFilingClock {
	if r.SummonFilingClock != nil {
		return r.SummonFilingClock
	}
	return filingProcessClock{}
}

func summonFilingDue(s *v1alpha1.AgentSession) bool {
	st := s.Status.Summon
	return s.Spec.Caller != "" && s.Spec.Lane == v1alpha1.LaneRemediation && s.Spec.Summon != nil &&
		st != nil && st.Closed != nil && st.Escalation != nil && st.Escalation.State == v1alpha1.SummonEscalationPending
}

// The initial list can predate a close in summonWatch. Re-read every listed
// remediation, not only the ones that were already Pending in that list.
func (r *Reconciler) summonFilings(ctx context.Context, items []v1alpha1.AgentSession) []error {
	var errs []error
	for i := range items {
		s := &items[i]
		if s.Spec.Caller == "" || s.Spec.Lane != v1alpha1.LaneRemediation {
			continue
		}
		var live v1alpha1.AgentSession
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &live); err != nil {
			if !apierrors.IsNotFound(err) {
				errs = append(errs, err)
			}
			continue
		}
		if live.UID != s.UID || !summonFilingDue(&live) {
			continue
		}
		if err := r.summonFile(ctx, &live); err != nil && !apierrors.IsConflict(err) && !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("file escalation for %s: %w", live.Name, err))
		}
	}
	return errs
}

func (r *Reconciler) filingPolicy(ctx context.Context, s *v1alpha1.AgentSession) (*v1alpha1.CallerPolicy, v1alpha1.SummonFilingReason, error) {
	if !slices.Contains(r.SummonCreateKinds, v1alpha1.SummonKindRemote) {
		return nil, summonoutcome.FilingRemoteOff, nil
	}
	p, err := r.summonPolicy(ctx, s)
	if err != nil {
		return nil, summonoutcome.FilingLedgerUnavailable, err
	}
	if p == nil {
		return nil, summonoutcome.FilingPolicyGone, nil
	}
	if p.Spec.Summon.Limits.Escalations == nil || *p.Spec.Summon.Limits.Escalations < 1 {
		return nil, summonoutcome.FilingPolicyNoEscalations, nil
	}
	return p, "", nil
}

func (r *Reconciler) summonFile(ctx context.Context, s *v1alpha1.AgentSession) error {
	if _, reason, err := r.filingPolicy(ctx, s); reason != "" {
		return errors.Join(err, r.blockSummonFiling(ctx, s, reason))
	}
	var fence [8]byte
	if _, err := rand.Read(fence[:]); err != nil {
		return err
	}
	boundary := hex.EncodeToString(fence[:])
	candidates := summonledger.NameSequence(summonoutcome.EscalationName(s))
	preview, err := summonFilingChild(s, candidates[len(candidates)-1], boundary)
	if err != nil {
		return err
	}
	// Both the brief and the complete serialized agentd document must fit.
	// The schema's bounded note/snapshot make this true for valid inputs.
	if err := CheckAgentdSession(preview); err != nil {
		return errors.New("the filing snapshot does not fit agentd's session contract")
	}
	b, err := r.filingLedgerStep(ctx, s, filingCharge, "")
	if b.reason != "" {
		return errors.Join(err, r.blockSummonFiling(ctx, s, b.reason))
	}
	if err != nil || b.done {
		return err
	}
	if s.Status.Summon.Escalation.ChargedAt == nil {
		base := s.DeepCopy()
		at := metav1.NewTime(b.charge.At.UTC().Truncate(time.Second))
		s.Status.Summon.Escalation.ChargedAt = &at
		if err := r.patchSummonFiling(ctx, base, s); err != nil {
			return err
		}
		logSummonFiling(s, "recorded", "")
	}
	// At most one pass through the bounded candidate sequence. A missing
	// object after AlreadyExists waits for the next pass, without rebinding.
	for range summonledger.MaxSuffix {
		child, err := r.findSummonFiling(ctx, s)
		if err != nil {
			return errors.Join(err, r.blockSummonFiling(ctx, s, summonoutcome.FilingLedgerUnavailable))
		}
		if child != nil {
			return r.finishSummonFiling(ctx, s, child)
		}
		m, err := r.filingLedgerStep(ctx, s, filingMark, "")
		if m.reason != "" {
			return errors.Join(err, r.blockSummonFiling(ctx, s, m.reason))
		}
		if err != nil || m.done {
			return err
		}
		if m.child != nil {
			return r.finishSummonFiling(ctx, s, m.child)
		}
		child, err = summonFilingChild(s, m.charge.Name, boundary)
		if err != nil {
			return err
		}
		callCtx, cancel := r.filingClock().WithDeadline(ctx, m.marked.Add(summonFilingDeadline))
		if err := callCtx.Err(); err != nil {
			cancel()
			return err
		}
		err = r.Client.Create(callCtx, child, client.FieldValidation(metav1.FieldValidationStrict))
		if err == nil {
			cancel()
			logSummonFilingName(s, "created", child.Name, "")
			return r.finishSummonFiling(ctx, s, child)
		}
		if !apierrors.IsAlreadyExists(err) {
			cancel()
			// An unknown result is never retried under another name. Next
			// pass's label list recovers a create whose response was lost.
			return err
		}
		var held v1alpha1.AgentSession
		err = r.APIReader.Get(callCtx, client.ObjectKeyFromObject(child), &held)
		cancel()
		if err != nil {
			return err
		}
		if summonFiledFor(&held, s) {
			return r.finishSummonFiling(ctx, s, &held)
		}
		b, err = r.filingLedgerStep(ctx, s, filingRebind, child.Name)
		if b.reason != "" {
			return errors.Join(err, r.blockSummonFiling(ctx, s, b.reason))
		}
		if err != nil || b.done {
			return err
		}
		if b.child != nil {
			return r.finishSummonFiling(ctx, s, b.child)
		}
	}
	return r.blockSummonFiling(ctx, s, summonoutcome.FilingNoFreeName)
}

func summonFiledFor(child, rem *v1alpha1.AgentSession) bool {
	ss := child.Spec.Summon
	return child.UID != "" && child.Namespace == rem.Namespace && child.Spec.Caller == rem.Spec.Caller &&
		child.Spec.Parent == rem.Spec.Parent && child.Spec.Lane == v1alpha1.LaneEscalation && child.Spec.Mode == v1alpha1.ModeRemote &&
		ss != nil && ss.FiledFor != nil && ss.FiledFor.UID == string(rem.UID) && ss.FiledFor.Name == rem.Name && ss.DispatchID == summonledger.FilingID(rem.UID)
}

func (r *Reconciler) findSummonFiling(ctx context.Context, s *v1alpha1.AgentSession) (*v1alpha1.AgentSession, error) {
	var list v1alpha1.AgentSessionList
	if err := r.APIReader.List(ctx, &list, client.InNamespace(s.Namespace), client.MatchingLabels{summonoutcome.LabelFiledForUID: string(s.UID)}); err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	if len(list.Items) != 1 || !summonFiledFor(&list.Items[0], s) {
		return nil, errors.New("the filing label does not identify one child of this remediation")
	}
	return &list.Items[0], nil
}

func summonFilingChild(s *v1alpha1.AgentSession, name, boundary string) (*v1alpha1.AgentSession, error) {
	e := s.Spec.Summon.Escalation
	if e == nil || s.Status.Outcome == nil || s.Status.Summon.Closed == nil {
		return nil, errors.New("the closed remediation lacks its filing snapshot or outcome")
	}
	c := s.Status.Summon.Closed
	data := map[string]string{"remediation": s.Name, "caller": s.Spec.Caller, "source": c.Source, "reason": c.Reason, "closed": c.At.UTC().Format(time.RFC3339), "note": s.Status.Outcome.Note}
	return &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace,
			Labels:      map[string]string{v1alpha1.LabelDepth: "0", summonoutcome.LabelSummoned: summonoutcome.LabelSummonedValue, summonoutcome.LabelFiledForUID: string(s.UID)},
			Annotations: map[string]string{v1alpha1.AnnotationRequestHash: summonFilingHash(s)}},
		Spec: v1alpha1.AgentSessionSpec{Caller: s.Spec.Caller, Parent: s.Spec.Parent, Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeRemote,
			Lane: v1alpha1.LaneEscalation, Repo: e.Repo, Profile: e.Profile, Model: e.Model, Effort: e.Effort, OperatingMode: v1alpha1.OperatingModeRunning,
			Prompt: summonbrief.Render(name, s.Spec.Caller, string(v1alpha1.LaneEscalation), summonFilingTemplate,
				[]string{"remediation", "caller", "source", "reason", "closed", "note"}, data, boundary),
			Summon: &v1alpha1.SessionSummon{DispatchID: summonledger.FilingID(s.UID), Priority: v1alpha1.SummonPriorityUrgent,
				FallbackModel: e.Fallback, OnUnreported: v1alpha1.SummonOnUnreportedFailed,
				FiledFor: &v1alpha1.SummonFiledFor{Name: s.Name, UID: string(s.UID)}}},
	}, nil
}

func summonFilingHash(s *v1alpha1.AgentSession) string {
	return summonledger.RequestHash("remote", "escalation", summonoutcome.EscalationName(s), v1alpha1.SummonPriorityUrgent)
}

type filingLedgerAction int

const (
	filingCharge filingLedgerAction = iota
	filingMark
	filingRebind
)

type filingBinding struct {
	charge summonledger.Charge
	marked time.Time
	child  *v1alpha1.AgentSession
	reason v1alpha1.SummonFilingReason
	done   bool
}

// filingLedgerStep charges, marks, or moves the name. Only filingCharge may
// append a charge, and only while chargedAt is absent. Neither other action
// changes At. All writes use the shared Keep rule and preserve other keys.
func (r *Reconciler) filingLedgerStep(ctx context.Context, s *v1alpha1.AgentSession, action filingLedgerAction, from string) (filingBinding, error) {
	unavailable := filingBinding{reason: summonoutcome.FilingLedgerUnavailable}
	for range summonFilingAttempts {
		var live v1alpha1.AgentSession
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &live); err != nil {
			return unavailable, err
		}
		if live.UID != s.UID || !summonFilingDue(&live) {
			return filingBinding{done: true}, nil
		}
		policy, reason, err := r.filingPolicy(ctx, &live)
		if reason != "" {
			return filingBinding{reason: reason}, err
		}
		var cm corev1.ConfigMap
		if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: summonledger.Namespace, Name: summonledger.Name}, &cm); err != nil {
			return unavailable, err
		}
		if cm.ResourceVersion == "" || !cm.DeletionTimestamp.IsZero() || cm.Immutable != nil && *cm.Immutable {
			return unavailable, nil
		}
		raw, present := cm.Data[s.Spec.Caller]
		entry, err := summonledger.Decode(raw, present)
		if err != nil {
			return unavailable, err
		}
		// Read after the ledger, so a concurrent API charge conflicts with
		// this update instead of escaping either queue or hourly accounting.
		var sessions v1alpha1.AgentSessionList
		if err := r.APIReader.List(ctx, &sessions, client.InNamespace(s.Namespace)); err != nil {
			return unavailable, err
		}
		id, hash := summonledger.FilingID(s.UID), summonFilingHash(s)
		byID := map[string]*v1alpha1.AgentSession{}
		names := map[string]bool{}
		for i := range sessions.Items {
			it := &sessions.Items[i]
			names[it.Name] = true
			if it.Spec.Summon != nil {
				byID[it.Spec.Summon.DispatchID] = it
			}
		}
		i := entry.Index(id)
		if i >= 0 && entry.Charges[i].Hash != hash {
			return unavailable, errors.New("the filing charge has a different request hash")
		}
		chargedAt := live.Status.Summon.Escalation.ChargedAt
		if i >= 0 && chargedAt != nil && !chargedAt.Time.Equal(entry.Charges[i].At.Truncate(time.Second)) {
			return unavailable, errors.New("the filing charge time differs from chargedAt")
		}
		if action == filingCharge && i >= 0 {
			return filingBinding{charge: entry.Charges[i]}, nil
		}
		if action == filingCharge && chargedAt != nil {
			// Recovery by label still runs. A missing charge after chargedAt
			// must never become a second charge; a mark will refuse it.
			return filingBinding{}, nil
		}
		if action != filingCharge {
			if child := byID[id]; child != nil {
				if !summonFiledFor(child, s) {
					return unavailable, errors.New("the filing dispatch id identifies another session")
				}
				return filingBinding{child: child}, nil
			}
			if i < 0 {
				return unavailable, nil
			}
		}
		mono := r.filingClock().Now()
		now := entry.Now(r.now())
		if action == filingCharge && entry.InWindow(now) >= int(policy.Spec.Summon.Limits.CreatesPerHour) {
			return filingBinding{reason: summonoutcome.FilingHourFull}, nil
		}
		raw, present = cm.Data[summonledger.GrantsKey]
		grants, err := summonledger.DecodeGrants(raw, present)
		if err != nil {
			return unavailable, err
		}
		bound := int32(4)
		if q := policy.Spec.Summon.Limits.QueuedUrgent; q != nil {
			bound = *q
		}
		q := summonledger.Queue{Urgent: true, Escalations: true}
		if summonledger.QueueCount(s.Spec.Caller, policy, entry.Charges, sessions.Items, byID, grants, q, id, now) >= int(bound) {
			return filingBinding{reason: summonoutcome.FilingQueueFull}, nil
		}
		if action == filingCharge {
			entry.Charges = append(entry.Charges, summonledger.Charge{ID: id, Hash: hash, Name: summonoutcome.EscalationName(s), At: now, Attempt: now})
			i = len(entry.Charges) - 1
		}
		ch := &entry.Charges[i]
		candidates := summonledger.NameSequence(summonoutcome.EscalationName(s))
		index := slices.Index(candidates, ch.Name)
		if index < 0 {
			return unavailable, errors.New("the filing charge binds a name outside its sequence")
		}
		if action == filingRebind && ch.Name == from {
			next := ""
			for _, name := range candidates[index+1:] {
				if !names[name] {
					next = name
					break
				}
			}
			if next == "" {
				return filingBinding{reason: summonoutcome.FilingNoFreeName}, nil
			}
			ch.Name = next
		}
		// This mark never changes At, even after the charge's hour expires.
		ch.Attempt = now
		b := filingBinding{charge: *ch, marked: mono}
		entry.Clock = now
		entry.Charges = summonledger.Prune(entry.Charges, now, summonledger.Keep(sessions.Items))
		encoded, err := summonledger.Encode(entry)
		if err != nil {
			return filingBinding{reason: summonoutcome.FilingEntryFull}, nil
		}
		if err := r.writeSummonFilingLedger(ctx, &cm, s.Spec.Caller, encoded); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return unavailable, err // Unknown write result: never create.
		}
		if action == filingCharge {
			logSummonFiling(s, "charged", "")
		}
		return b, nil
	}
	return unavailable, errors.New("the filing ledger is busy")
}

func (r *Reconciler) writeSummonFilingLedger(ctx context.Context, cm *corev1.ConfigMap, caller, encoded string) error {
	if cm == nil || cm.Namespace != summonledger.Namespace || cm.Name != summonledger.Name || cm.ResourceVersion == "" {
		return errors.New("filing requires the named ledger's uncached resourceVersion")
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[caller] = encoded
	return r.Client.Update(ctx, cm)
}

func (r *Reconciler) blockSummonFiling(ctx context.Context, s *v1alpha1.AgentSession, reason v1alpha1.SummonFilingReason) error {
	switch reason {
	case summonoutcome.FilingPolicyGone, summonoutcome.FilingPolicyNoEscalations,
		summonoutcome.FilingHourFull, summonoutcome.FilingQueueFull, summonoutcome.FilingRemoteOff,
		summonoutcome.FilingLedgerUnavailable, summonoutcome.FilingNoFreeName, summonoutcome.FilingEntryFull:
	default:
		return errors.New("unknown filing blocked word")
	}
	if !summonFilingDue(s) {
		return nil
	}
	// Do not interpret the condition: its message is for a person. This
	// equality check only suppresses an identical write (coordinator Q-1).
	c := meta.FindStatusCondition(s.Status.Conditions, summonoutcome.ConditionEscalationPending)
	event := false
	for _, page := range s.Status.Summon.Pages {
		event = event || page.Event == v1alpha1.PageEscalationPending
	}
	if c != nil && c.Status == metav1.ConditionTrue && c.Reason == "Blocked" && c.Message == string(reason) && event {
		return nil
	}
	if !event && len(s.Status.Summon.Pages) >= summonMaxPages {
		return errors.New("the filing has no room for its pending event")
	}
	base := s.DeepCopy()
	at := metav1.NewTime(r.now().UTC().Truncate(time.Second))
	meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{Type: summonoutcome.ConditionEscalationPending, Status: metav1.ConditionTrue,
		Reason: "Blocked", Message: string(reason), ObservedGeneration: s.Generation, LastTransitionTime: at})
	if !event {
		s.Status.Summon.Pages = append(s.Status.Summon.Pages, v1alpha1.SummonPage{Event: v1alpha1.PageEscalationPending, At: at})
	}
	if err := r.patchSummonFiling(ctx, base, s); err != nil {
		return err
	}
	logSummonFiling(s, "blocked", reason)
	return nil
}

func (r *Reconciler) finishSummonFiling(ctx context.Context, s, child *v1alpha1.AgentSession) error {
	if !summonFiledFor(child, s) || s.Status.Summon.Escalation.ChargedAt == nil {
		return errors.New("cannot record a filing without its child identity and charge time")
	}
	base := s.DeepCopy()
	st := s.Status.Summon.Escalation
	st.State, st.Name, st.UID = v1alpha1.SummonEscalationFiled, child.Name, string(child.UID)
	meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{Type: summonoutcome.ConditionEscalationPending, Status: metav1.ConditionFalse,
		Reason: "Filed", Message: "the escalation child is recorded", ObservedGeneration: s.Generation,
		LastTransitionTime: metav1.NewTime(r.now().UTC().Truncate(time.Second))})
	if err := r.patchSummonFiling(ctx, base, s); err != nil {
		return err
	}
	logSummonFiling(s, "filed", "")
	return nil
}

// Decode into a separate target, then prove the stored response kept the
// fields. An old CRD that prunes a successful patch cannot authorize create.
func (r *Reconciler) patchSummonFiling(ctx context.Context, base, desired *v1alpha1.AgentSession) error {
	patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
	data, err := patch.Data(desired)
	if err != nil {
		return err
	}
	stored := base.DeepCopy()
	if err := r.Client.Status().Patch(ctx, desired, client.RawPatch(patch.Type(), data), client.WithSubResourceBody(stored)); err != nil {
		*desired = *base
		return err
	}
	if !apiequality.Semantic.DeepEqual(stored.Status.Summon, desired.Status.Summon) ||
		!apiequality.Semantic.DeepEqual(meta.FindStatusCondition(stored.Status.Conditions, summonoutcome.ConditionEscalationPending),
			meta.FindStatusCondition(desired.Status.Conditions, summonoutcome.ConditionEscalationPending)) {
		*desired = *base
		return errors.New("the filing patch was not retained; install the current AgentSession CRD before the operator")
	}
	*desired = *stored
	return nil
}

func logSummonFiling(s *v1alpha1.AgentSession, step string, reason v1alpha1.SummonFilingReason) {
	logSummonFilingName(s, step, s.Status.Summon.Escalation.Name, reason)
}

func logSummonFilingName(s *v1alpha1.AgentSession, step, name string, reason v1alpha1.SummonFilingReason) {
	st := s.Status.Summon.Escalation
	_, _ = fmt.Fprintf(os.Stdout, "ops-event: event=filing session=%s caller=%s lane=remediation step=%s state=%s name=%s reason=%s by=operator\n", s.Name, s.Spec.Caller, step, st.State, name, reason)
}
