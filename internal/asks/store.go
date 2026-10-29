// Package asks stores agent asks for the operator API (Epic #254).
// The routes exist only when the operator is started with --enable-asks.
// v1 does not deliver an answer through the inbox: the requester reads the ask.
package asks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	// Namespace is the only namespace asks are stored in.
	Namespace = "dev-agents"
	// RequesterLabel is the digest of the creating session.
	RequesterLabel = "coordination.dev-env.haynesops.com/ask-requester"
	// StateLabel is the stored lifecycle, used to count open asks.
	StateLabel = "coordination.dev-env.haynesops.com/ask-state"

	// DefaultOpenPerRequester is the cap on stored-open asks for one session.
	// An open ask that has lapsed still counts until its state label changes;
	// the expiry sweeper is a later lane.
	DefaultOpenPerRequester int64 = 16
	// DefaultTotalPerRequester is the lifetime cap for one session.
	DefaultTotalPerRequester int64 = 512
	// DefaultPerNamespace is the cap on asks in the namespace.
	DefaultPerNamespace int64 = 4096
)

var (
	// ErrNotFound means the id is unknown, or it is not the requester's ask.
	ErrNotFound = errors.New("ask not found")
	// ErrConflict means this request id already created a different body, or
	// the ask kept changing across retries.
	ErrConflict = errors.New("ask requestId was already used for a different body")
	// ErrLimit means a create would pass a count bound. A retry of an existing
	// ask is not a create and is not refused.
	ErrLimit = errors.New("ask limit reached")
	// ErrUnavailable means the store could not confirm a read or a write.
	ErrUnavailable = errors.New("asks are unavailable")
)

// LimitError is a refused create. Scope is LimitOpen, LimitRequester or LimitNamespace.
type LimitError struct{ Scope string }

func (e *LimitError) Error() string { return "ask limit reached: " + e.Scope }
func (e *LimitError) Unwrap() error { return ErrLimit }

// Limits are the count bounds. A zero field takes the default.
type Limits struct {
	OpenPerRequester  int64
	TotalPerRequester int64
	PerNamespace      int64
}

func (l Limits) effective() Limits {
	if l.OpenPerRequester <= 0 {
		l.OpenPerRequester = DefaultOpenPerRequester
	}
	if l.TotalPerRequester <= 0 {
		l.TotalPerRequester = DefaultTotalPerRequester
	}
	if l.PerNamespace <= 0 {
		l.PerNamespace = DefaultPerNamespace
	}
	return l
}

// KubeStore reads asks from the API server (Live) and writes them through Client.
// Counts follow the taskclaim.CheckRoom pattern: an uncached metadata list,
// then create. Racing creates can pass the same count.
type KubeStore struct {
	Client    client.Client
	Live      client.Reader
	Namespace string
	Limits    Limits
	Log       logr.Logger
}

func (s KubeStore) ready() error {
	if s.Client == nil || s.Live == nil || s.Namespace != Namespace {
		return ErrUnavailable
	}
	return nil
}

func (s KubeStore) log() logr.Logger {
	if s.Log.GetSink() == nil {
		return logr.Discard()
	}
	return s.Log
}

func (s KubeStore) logWrite(err error, id string) {
	s.log().Error(nil, "ask write failed", "id", id, "reason", string(apierrors.ReasonForError(err)))
}

// Create stores an ask, or returns the one this requester and request id already
// stored. created is false when the returned ask already existed. now is
// truncated to whole seconds and used as createdAt.
func (s KubeStore) Create(ctx context.Context, req Requester, in apiv1.AskCreateRequest, now time.Time) (apiv1.Ask, bool, error) {
	if err := s.ready(); err != nil {
		return apiv1.Ask{}, false, err
	}
	made, err := prepare(req, in, now)
	if err != nil {
		return apiv1.Ask{}, false, err
	}
	existing, err := s.load(ctx, made.Name)
	if err == nil {
		return s.finishExisting(ctx, existing, made.Spec.RequestDigest)
	}
	if !errors.Is(err, ErrNotFound) {
		return apiv1.Ask{}, false, err
	}
	if err := s.checkRoom(ctx, req); err != nil {
		return apiv1.Ask{}, false, err
	}
	obj := &coord.Ask{
		TypeMeta:   metav1.TypeMeta{APIVersion: coord.GroupVersion.String(), Kind: "Ask"},
		ObjectMeta: metav1.ObjectMeta{Name: made.Name, Namespace: s.Namespace, Labels: made.Labels},
		Spec:       made.Spec,
	}
	if err := s.Client.Create(ctx, obj); err != nil {
		if apierrors.IsAlreadyExists(err) {
			existing, rerr := s.load(ctx, made.Name)
			if rerr != nil {
				return apiv1.Ask{}, false, rerr
			}
			return s.finishExisting(ctx, existing, made.Spec.RequestDigest)
		}
		s.logWrite(err, made.Name)
		return apiv1.Ask{}, false, fmt.Errorf("%w: ask was not stored", ErrUnavailable)
	}
	if err := s.ensureOpen(ctx, obj); err != nil {
		return apiv1.Ask{}, false, err
	}
	fresh, err := s.load(ctx, obj.Name)
	if err != nil {
		return apiv1.Ask{}, false, err
	}
	wire, err := toWire(fresh)
	if err != nil {
		return apiv1.Ask{}, false, err
	}
	return wire, true, nil
}

func (s KubeStore) finishExisting(ctx context.Context, obj *coord.Ask, digest string) (apiv1.Ask, bool, error) {
	if obj.Spec.RequestDigest != digest {
		return apiv1.Ask{}, false, ErrConflict
	}
	if err := s.ensureOpen(ctx, obj); err != nil {
		return apiv1.Ask{}, false, err
	}
	wire, err := toWire(obj)
	return wire, false, err
}

// Get returns one ask by id. It does not apply the caller: the route does.
func (s KubeStore) Get(ctx context.Context, id string) (apiv1.Ask, error) {
	if err := s.ready(); err != nil {
		return apiv1.Ask{}, err
	}
	obj, err := s.load(ctx, id)
	if err != nil {
		return apiv1.Ask{}, err
	}
	return toWire(obj)
}

// ListByRequester returns the requester's asks, unsorted.
func (s KubeStore) ListByRequester(ctx context.Context, req Requester) ([]apiv1.Ask, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if req.Namespace != Namespace || req.Name == "" || req.UID == "" {
		return nil, ErrUnavailable
	}
	return s.list(ctx, client.MatchingLabels{RequesterLabel: requesterValue(req)})
}

// ListOpen returns asks whose stored state label is open. A lapsed ask stays
// in this set until its label changes. Callers apply EffectiveState.
func (s KubeStore) ListOpen(ctx context.Context) ([]apiv1.Ask, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.list(ctx, client.MatchingLabels{StateLabel: string(apiv1.AskStateOpen)})
}

// ListNamespace returns every ask in the namespace, unsorted.
func (s KubeStore) ListNamespace(ctx context.Context) ([]apiv1.Ask, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.list(ctx, nil)
}

// Withdraw sets an open ask to withdrawn. The requester must own it. An ask
// that is not effectively open, including one that has lapsed, is refused
// with ErrAskNotOpen (409). now is truncated to whole seconds.
func (s KubeStore) Withdraw(ctx context.Context, id string, req Requester, now time.Time) (apiv1.Ask, error) {
	if err := s.ready(); err != nil {
		return apiv1.Ask{}, err
	}
	if req.Namespace != Namespace || req.Name == "" || req.UID == "" {
		return apiv1.Ask{}, ErrUnavailable
	}
	now = whole(now)
	for attempt := 0; attempt < 4; attempt++ {
		obj, err := s.load(ctx, id)
		if err != nil {
			return apiv1.Ask{}, err
		}
		if !owns(obj, req) {
			return apiv1.Ask{}, ErrNotFound
		}
		wire, err := toWire(obj)
		if err != nil {
			return apiv1.Ask{}, err
		}
		if wire.EffectiveState(now) != apiv1.AskStateOpen {
			return apiv1.Ask{}, apiv1.ErrAskNotOpen
		}
		obj.Status.State = string(apiv1.AskStateWithdrawn)
		if err := s.Client.Status().Update(ctx, obj); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			s.logWrite(err, id)
			return apiv1.Ask{}, fmt.Errorf("%w: ask was not withdrawn", ErrUnavailable)
		}
		s.syncLabel(ctx, id, string(apiv1.AskStateWithdrawn))
		fresh, err := s.load(ctx, id)
		if err != nil {
			return apiv1.Ask{}, err
		}
		return toWire(fresh)
	}
	return apiv1.Ask{}, fmt.Errorf("%w: the ask kept changing", ErrConflict)
}

// Answer records the portal's answer once. now is truncated to whole seconds
// and stored as answeredAt. The same issuer, subject and answer retry returns
// the stored ask. A different answer, or a withdrawn ask, returns ErrAskNotOpen.
// A lapsed ask returns ErrAskExpired.
func (s KubeStore) Answer(ctx context.Context, id string, in apiv1.AskAnswerRequest, now time.Time) (apiv1.Ask, error) {
	if err := s.ready(); err != nil {
		return apiv1.Ask{}, err
	}
	now = whole(now)
	in.Text = apiv1.NormalizeAskText(in.Text)
	if err := in.Validate(); err != nil {
		return apiv1.Ask{}, err
	}
	for attempt := 0; attempt < 4; attempt++ {
		obj, err := s.load(ctx, id)
		if err != nil {
			return apiv1.Ask{}, err
		}
		wire, err := toWire(obj)
		if err != nil {
			return apiv1.Ask{}, err
		}
		if err := in.ValidateForAt(wire, now); err != nil {
			if errors.Is(err, apiv1.ErrAskNotOpen) && sameAnswer(obj, in) {
				s.syncLabel(ctx, id, string(apiv1.AskStateAnswered))
				return wire, nil
			}
			return apiv1.Ask{}, err
		}
		applyAnswer(obj, in, now)
		if _, nerr := toWire(obj); nerr != nil {
			return apiv1.Ask{}, nerr
		}
		if err := s.Client.Status().Update(ctx, obj); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			s.logWrite(err, id)
			return apiv1.Ask{}, fmt.Errorf("%w: ask answer was not stored", ErrUnavailable)
		}
		s.syncLabel(ctx, id, string(apiv1.AskStateAnswered))
		fresh, err := s.load(ctx, id)
		if err != nil {
			return apiv1.Ask{}, err
		}
		return toWire(fresh)
	}
	return apiv1.Ask{}, fmt.Errorf("%w: the ask kept changing", ErrConflict)
}

func applyAnswer(obj *coord.Ask, in apiv1.AskAnswerRequest, now time.Time) {
	obj.Status.State = string(apiv1.AskStateAnswered)
	obj.Status.Answer = &coord.AskAnswer{
		OptionID: in.OptionID, Text: in.Text, AnsweredAt: metav1.NewTime(now),
		AnsweredBy: coord.AskActor{Issuer: in.Actor.Issuer, Subject: in.Actor.Subject, DisplayName: in.Actor.DisplayName},
	}
	if in.Approved != nil {
		v := *in.Approved
		obj.Status.Answer.Approved = &v
	}
}

func owns(obj *coord.Ask, req Requester) bool {
	return obj.Namespace == req.Namespace && obj.Labels[RequesterLabel] == requesterValue(req) &&
		obj.Spec.Context.SessionName == req.Name && obj.Spec.Context.SessionUID == req.UID
}

func sameAnswer(obj *coord.Ask, in apiv1.AskAnswerRequest) bool {
	if obj.Status.State != string(apiv1.AskStateAnswered) || obj.Status.Answer == nil {
		return false
	}
	got := obj.Status.Answer
	return got.OptionID == in.OptionID && got.Text == in.Text && sameBool(got.Approved, in.Approved) &&
		got.AnsweredBy.Issuer == in.Actor.Issuer && got.AnsweredBy.Subject == in.Actor.Subject &&
		got.AnsweredBy.DisplayName == in.Actor.DisplayName
}

func sameBool(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (s KubeStore) ensureOpen(ctx context.Context, obj *coord.Ask) error {
	if obj.Status.State != "" {
		return nil
	}
	obj.Status.State = string(apiv1.AskStateOpen)
	if err := s.Client.Status().Update(ctx, obj); err != nil {
		if apierrors.IsConflict(err) {
			fresh, gerr := s.load(ctx, obj.Name)
			if gerr != nil {
				return gerr
			}
			*obj = *fresh
			if obj.Status.State == "" {
				return fmt.Errorf("%w: ask status was not stored", ErrUnavailable)
			}
			return nil
		}
		s.logWrite(err, obj.Name)
		return fmt.Errorf("%w: ask status was not stored", ErrUnavailable)
	}
	return nil
}

func (s KubeStore) syncLabel(ctx context.Context, name, state string) {
	if err := s.setStateLabel(ctx, name, state); err != nil {
		s.log().Error(nil, "ask state label was not updated", "id", name, "state", state, "reason", string(apierrors.ReasonForError(err)))
	}
}

func (s KubeStore) setStateLabel(ctx context.Context, name, state string) error {
	for attempt := 0; attempt < 4; attempt++ {
		obj, err := s.load(ctx, name)
		if err != nil {
			return err
		}
		if obj.Labels[StateLabel] == state {
			return nil
		}
		base := obj.DeepCopy()
		obj.Labels[StateLabel] = state
		// A label-only merge leaves the immutable spec out of the write. A full
		// update reserializes its times and the spec equality rule can refuse it.
		if err := s.Client.Patch(ctx, obj, client.MergeFrom(base)); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return err
		}
		return nil
	}
	return ErrConflict
}

func (s KubeStore) load(ctx context.Context, name string) (*coord.Ask, error) {
	if !apiv1.ValidAskID(name) {
		return nil, ErrNotFound
	}
	var obj coord.Ask
	if err := s.Live.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, &obj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrNotFound
		}
		s.logWrite(err, name)
		return nil, fmt.Errorf("%w: ask was not read", ErrUnavailable)
	}
	if obj.DeletionTimestamp != nil || obj.Name != obj.Spec.ID || obj.Namespace != s.Namespace {
		return nil, ErrNotFound
	}
	return &obj, nil
}

func (s KubeStore) list(ctx context.Context, sel client.ListOption) ([]apiv1.Ask, error) {
	var out []apiv1.Ask
	cursor := ""
	for page := 0; page < 20; page++ {
		list := &coord.AskList{}
		opts := []client.ListOption{client.InNamespace(s.Namespace), client.Limit(500)}
		if sel != nil {
			opts = append(opts, sel)
		}
		if cursor != "" {
			opts = append(opts, client.Continue(cursor))
		}
		if err := s.Live.List(ctx, list, opts...); err != nil {
			s.log().Error(nil, "ask list failed", "reason", string(apierrors.ReasonForError(err)))
			return nil, fmt.Errorf("%w: ask list failed", ErrUnavailable)
		}
		for i := range list.Items {
			wire, err := toWire(&list.Items[i])
			if err != nil {
				return nil, err
			}
			out = append(out, wire)
		}
		cursor = list.Continue
		if cursor == "" {
			return out, nil
		}
	}
	return nil, fmt.Errorf("%w: ask list did not finish", ErrUnavailable)
}

func (s KubeStore) checkRoom(ctx context.Context, req Requester) error {
	lim := s.Limits.effective()
	who := requesterValue(req)
	checks := []struct {
		scope string
		match map[string]string
		max   int64
	}{
		{LimitOpen, map[string]string{RequesterLabel: who, StateLabel: string(apiv1.AskStateOpen)}, lim.OpenPerRequester},
		{LimitRequester, map[string]string{RequesterLabel: who}, lim.TotalPerRequester},
		{LimitNamespace, nil, lim.PerNamespace},
	}
	for _, check := range checks {
		n, err := s.countAtMost(ctx, check.match, check.max)
		if err != nil {
			s.log().Error(nil, "ask count failed", "scope", check.scope, "reason", string(apierrors.ReasonForError(err)))
			return fmt.Errorf("%w: ask count failed", ErrUnavailable)
		}
		if atEighty(n, check.max) {
			NearLimit.WithLabelValues(check.scope).Inc()
			s.log().Info("ask count is at or above 80 percent of its bound", "scope", check.scope, "count", n, "limit", check.max)
		}
		if n >= check.max {
			return &LimitError{Scope: check.scope}
		}
	}
	return nil
}

// countAtMost is the CheckRoom read: uncached metadata, label selected, and it
// stops once the bound is reached so a full namespace is not pulled past the cap.
func (s KubeStore) countAtMost(ctx context.Context, match map[string]string, bound int64) (int64, error) {
	if s.Live == nil || bound < 1 {
		return 0, ErrUnavailable
	}
	var n int64
	cursor := ""
	for n < bound {
		page := &metav1.PartialObjectMetadataList{}
		page.SetGroupVersionKind(coord.GroupVersion.WithKind("AskList"))
		opts := []client.ListOption{client.InNamespace(s.Namespace), client.Limit(min(bound-n, 500)), client.Continue(cursor)}
		if len(match) > 0 {
			opts = append(opts, client.MatchingLabels(match))
		}
		if err := s.Live.List(ctx, page, opts...); err != nil {
			return 0, err
		}
		n += int64(len(page.Items))
		if page.Continue == "" {
			break
		}
		cursor = page.Continue
	}
	return n, nil
}

func toWire(obj *coord.Ask) (apiv1.Ask, error) {
	spec := obj.Spec
	state := apiv1.AskState(obj.Status.State)
	if state == "" {
		return apiv1.Ask{}, fmt.Errorf("%w: ask status is missing", ErrUnavailable)
	}
	out := apiv1.Ask{
		ID: spec.ID, Type: apiv1.AskType(spec.Type), Question: spec.Question,
		Recommended: spec.Recommended, AllowFreeText: spec.AllowFreeText,
		Context: apiv1.AskContext{
			SessionName: spec.Context.SessionName, SessionUID: spec.Context.SessionUID,
			WorkPackage: spec.Context.WorkPackage, Links: slices.Clone(spec.Context.Links),
		},
		CreatedAt: spec.CreatedAt.UTC(), ExpiresAt: spec.ExpiresAt.UTC(), State: state,
	}
	if len(spec.Options) > 0 {
		out.Options = make([]apiv1.AskOption, len(spec.Options))
		for i, opt := range spec.Options {
			out.Options[i] = apiv1.AskOption{ID: opt.ID, Label: opt.Label, Description: opt.Description}
		}
	}
	if spec.Scope != nil {
		out.Scope = &apiv1.AskScope{Action: spec.Scope.Action, Target: spec.Scope.Target, ExpiresAt: spec.Scope.ExpiresAt.UTC()}
	}
	if obj.Status.Answer != nil {
		src := obj.Status.Answer
		ans := apiv1.AskAnswer{
			OptionID: src.OptionID, Text: src.Text, AnsweredAt: src.AnsweredAt.UTC(),
			AnsweredBy: apiv1.AskActor{Issuer: src.AnsweredBy.Issuer, Subject: src.AnsweredBy.Subject, DisplayName: src.AnsweredBy.DisplayName},
		}
		if src.Approved != nil {
			v := *src.Approved
			ans.Approved = &v
		}
		out.Answer = &ans
	}
	if err := apiv1.ValidateAsk(out); err != nil {
		return apiv1.Ask{}, fmt.Errorf("%w: stored ask failed validation: %s", ErrUnavailable, err.Error())
	}
	return out, nil
}
