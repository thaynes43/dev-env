package inbox

import (
	"context"
	"strings"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

// ParentNoteLabel identifies the parent route's records in the shared inbox.
// It grants no access; the stored sender and recipient identities decide that.
const ParentNoteLabel = "coordination.dev-env.haynesops.com/parent-note"

func IsParentNote(m *coord.InboxMessage) bool {
	return m != nil && m.Labels[ParentNoteLabel] == "true"
}

func peerMessageSelector(match map[string]string) (client.MatchingLabelsSelector, error) {
	req, err := labels.NewRequirement(ParentNoteLabel, selection.DoesNotExist, nil)
	if err != nil {
		return client.MatchingLabelsSelector{}, err
	}
	return client.MatchingLabelsSelector{Selector: labels.SelectorFromSet(match).Add(*req)}, nil
}

func validParentNoteSpec(spec coord.InboxMessageSpec) bool {
	return ValidSession(spec.Sender) && ValidSession(spec.Recipient) &&
		spec.CallerSender == nil && spec.CallerRecipient == nil &&
		((spec.InReplyTo == nil && (spec.Kind == KindQuestion || spec.Kind == KindResult || spec.Kind == KindBlocker)) ||
			(spec.Kind == KindReply && spec.InReplyTo != nil && ValidRef(*spec.InReplyTo)))
}

func parentNoteLabels(spec coord.InboxMessageSpec) map[string]string {
	l := messageLabels(spec)
	l[ParentNoteLabel] = "true"
	return l
}

// CreateParentNote uses the same sender/key name and limits as peer messages.
// A concurrent peer create can win that name; callers must check IsParentNote
// on the returned record before confirming a parent-note receipt.
func (s KubeStore) CreateParentNote(ctx context.Context, spec coord.InboxMessageSpec) (*coord.InboxMessage, bool, error) {
	if !validParentNoteSpec(spec) {
		return nil, false, ErrUnavailable
	}
	return s.create(ctx, spec, true)
}

// GetParentNote checks the stored parties before validating the record, so
// an unrelated caller cannot distinguish an invalid hidden note from absence.
func (s KubeStore) GetParentNote(ctx context.Context, name string, party coord.SessionIdentity) (*coord.InboxMessage, error) {
	if !s.ready() || !ValidSession(party) {
		return nil, ErrUnavailable
	}
	if !ValidMessageName(name) {
		return nil, ErrNotFound
	}
	var m coord.InboxMessage
	if err := s.Live.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, &m); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, ErrUnavailable
	}
	if !IsParentNote(&m) || (m.Spec.Sender != party && m.Spec.Recipient != party) {
		return nil, ErrNotFound
	}
	if !s.validMessage(&m) {
		return nil, ErrUnavailable
	}
	return &m, nil
}

func parentReplyLabels(q *coord.InboxMessage) map[string]string {
	return map[string]string{
		ParentNoteLabel: "true", taskclaim.ProjectLabel: q.Spec.Project,
		KindLabel: KindReply, InReplyToLabel: inReplyToLabel(Ref(q)),
	}
}

// ValidParentReply checks the full stored binding, not just the watch labels.
func (s KubeStore) ValidParentReply(q, reply *coord.InboxMessage) bool {
	return IsParentNote(reply) && s.validMessage(reply) && reply.Spec.Kind == KindReply &&
		reply.Spec.Project == q.Spec.Project && reply.Spec.InReplyTo != nil && *reply.Spec.InReplyTo == Ref(q) &&
		reply.Spec.Sender == q.Spec.Recipient && reply.Spec.Recipient == q.Spec.Sender
}

// ParentReplies returns a bounded snapshot and its resourceVersion. Starting
// the watch at that version cannot lose an answer between the list and watch.
// Like peer replies, a race can store multiple answers; the oldest wins.
func (s KubeStore) ParentReplies(ctx context.Context, q *coord.InboxMessage) ([]coord.InboxMessage, string, error) {
	if !s.ready() || !IsParentNote(q) || !s.validMessage(q) || q.Spec.Kind == KindReply {
		return nil, "", ErrUnavailable
	}
	var list coord.InboxMessageList
	if s.Live.List(ctx, &list, client.InNamespace(s.Namespace), client.MatchingLabels(parentReplyLabels(q)), client.Limit(MaxReplies+1)) != nil || list.Continue != "" {
		return nil, "", ErrUnavailable
	}
	for i := range list.Items {
		if !s.ValidParentReply(q, &list.Items[i]) {
			return nil, "", ErrUnavailable
		}
	}
	replies, err := RepliesOf(q, list.Items)
	return replies, list.ResourceVersion, err
}

// WatchParentReplies opens exactly one stream. A closed or expired stream is
// unconfirmed; callers do not silently reconnect or poll.
func (s KubeStore) WatchParentReplies(ctx context.Context, q *coord.InboxMessage, version string) (watch.Interface, error) {
	if s.Watcher == nil || version == "" || !s.ready() || !IsParentNote(q) || !s.validMessage(q) || q.Spec.Kind == KindReply {
		return nil, ErrUnavailable
	}
	stream, err := s.Watcher.Watch(ctx, &coord.InboxMessageList{}, client.InNamespace(s.Namespace),
		client.MatchingLabels(parentReplyLabels(q)), &client.ListOptions{Raw: &metav1.ListOptions{ResourceVersion: version}})
	if err != nil {
		return nil, ErrUnavailable
	}
	return stream, nil
}

// Parent-note pagination visits questions/blockers, then results. The prefix
// records the phase; the rest is the Kubernetes continuation, opaque to us.
// Each phase uses the exact caller identity and the parent-note label.
func ValidParentNoteCursor(cursor string) bool {
	return cursor == "" || (len(cursor) >= 2 && len(cursor) <= 4096 &&
		(cursor[:2] == "0:" || cursor[:2] == "1:") && utf8.ValidString(cursor) && !strings.ContainsAny(cursor, "\x00\r\n"))
}

func (s KubeStore) ListParentNotes(ctx context.Context, party coord.SessionIdentity, sent bool, limit int64, cursor string) ([]coord.InboxMessage, string, error) {
	if !s.ready() || !ValidSession(party) || limit < 1 || limit > 100 || !ValidParentNoteCursor(cursor) {
		return nil, "", ErrUnavailable
	}
	phase, continuation := 0, ""
	if cursor != "" {
		phase, continuation = int(cursor[0]-'0'), cursor[2:]
	}
	partyKey, partyValue := RecipientLabel, recipientLabel(party)
	if sent {
		partyKey, partyValue = taskclaim.SourceLabel, taskclaim.SessionLabel(party)
	}
	out := make([]coord.InboxMessage, 0, limit)
	// At most two list calls, each bounded by the remaining page size. There
	// is no cross-project scan and no read of another party's notes.
	for ; phase < 2; phase++ {
		kinds := []string{KindQuestion, KindBlocker}
		if phase == 1 {
			kinds = []string{KindResult}
		}
		kindRequirement, err := labels.NewRequirement(KindLabel, selection.In, kinds)
		if err != nil {
			return nil, "", ErrUnavailable
		}
		selector := labels.SelectorFromSet(map[string]string{ParentNoteLabel: "true", partyKey: partyValue}).Add(*kindRequirement)
		remaining := limit - int64(len(out))
		var page coord.InboxMessageList
		if s.Live.List(ctx, &page, client.InNamespace(s.Namespace), client.MatchingLabelsSelector{Selector: selector}, client.Limit(remaining), client.Continue(continuation)) != nil ||
			int64(len(page.Items)) > remaining || len(page.Continue) > 4094 {
			return nil, "", ErrUnavailable
		}
		for i := range page.Items {
			m := &page.Items[i]
			matchesParty := m.Spec.Recipient == party
			if sent {
				matchesParty = m.Spec.Sender == party
			}
			matchesPhase := m.Spec.Kind == KindQuestion || m.Spec.Kind == KindBlocker
			if phase == 1 {
				matchesPhase = m.Spec.Kind == KindResult
			}
			if !IsParentNote(m) || !s.validMessage(m) || !matchesParty || !matchesPhase {
				return nil, "", ErrUnavailable
			}
			out = append(out, *m)
		}
		if page.Continue != "" {
			next := string(rune('0'+phase)) + ":" + page.Continue
			if !ValidParentNoteCursor(next) {
				return nil, "", ErrUnavailable
			}
			return out, next, nil
		}
		if int64(len(out)) == limit && phase == 0 {
			return out, "1:", nil
		}
		continuation = ""
	}
	return out, "", nil
}
