// Package inbox retains immutable API messages and explicit read acknowledgements.
// Create stores a message. The parent-notice mark is a metadata annotation
// patch; the spec stays immutable. Observations use the uncached reader.
package inbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

const (
	MaxTextBytes    = 16 << 10
	MaxRequestBytes = 6*MaxTextBytes + 4096
	RecipientLabel  = "coordination.dev-env.haynesops.com/recipient"
	// KindLabel and InReplyToLabel mark questions and replies (DESIGN-014,
	// 2026-10-31). A peer plain message carries neither. A parent plain
	// message from the caller carries KindPlain so the child's inbox can
	// select it; the stored kind stays empty.
	KindLabel      = "coordination.dev-env.haynesops.com/kind"
	InReplyToLabel = "coordination.dev-env.haynesops.com/in-reply-to"

	KindQuestion = "question"
	KindResult   = "result"
	KindBlocker  = "blocker"
	KindReply    = "reply"
	KindPlain    = "plain"
	// StaleAfter is the fixed age past which an unanswered question reads
	// Stale. It is a display bound, not an expiry: nothing is deleted or resent.
	StaleAfter = 30 * time.Minute
	// MaxReplies bounds one reply lookup. The API refuses a second distinct
	// reply, so more than one only follows a race between two replies.
	MaxReplies = 8
	// replyPageSize is the page size of a thread list's one reply index.
	replyPageSize = 100
)

// ErrLimit means a new message would exceed its sender's or its project's
// bound. Text is at most 16 KiB and nothing is deleted, so DefaultLimits keeps
// one project's retained text near 64 MiB at most.
var ErrLimit = taskclaim.ErrLimit

var DefaultLimits = taskclaim.Limits{PerSession: 512, PerProject: 4096}

var (
	ErrUnavailable = errors.New("inbox observation is unconfirmed")
	// ErrSchemaNotUpgraded means the cluster's InboxMessage schema prunes a
	// field the record needs, so storing it would leave a record that stays
	// invalid. Nothing was written.
	ErrSchemaNotUpgraded = errors.New("inbox schema not upgraded")
	ErrNotFound          = errors.New("inbox record not found")
	keyPattern           = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,126}[a-z0-9])?$`)
	uidPattern           = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
	modelPattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,127}$`)
)

func digest(v any) string {
	data, _ := json.Marshal(v)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
func fullName(prefix string, v any) string {
	d := digest(v)
	return prefix + "." + d[:32] + "." + d[32:]
}
func MessageName(sender coord.SessionIdentity, key string) string {
	return fullName("message", struct {
		Sender coord.SessionIdentity
		Key    string
	}{sender, key})
}

// PolicyMessageName names a message whose sender is the Meatball account.
// The shape differs from MessageName, so the same key yields a different name.
func PolicyMessageName(serviceAccount, policyUID, key string) string {
	return fullName("message", struct {
		ServiceAccount string
		PolicyUID      string
		Key            string
	}{serviceAccount, policyUID, key})
}

// MessageNameFor returns the name of the sender form that is set.
// A session sender keeps MessageName. A caller sender uses PolicyMessageName.
func MessageNameFor(spec coord.InboxMessageSpec) string {
	if spec.CallerSender != nil && spec.Sender == (coord.SessionIdentity{}) {
		return PolicyMessageName(spec.CallerSender.ServiceAccount, spec.CallerSender.PolicyUID, spec.Key)
	}
	return MessageName(spec.Sender, spec.Key)
}

// RecipientFormUID returns the session UID when recipient is set, and the
// CallerPolicy UID when callerRecipient is set.
func RecipientFormUID(spec coord.InboxMessageSpec) string {
	if spec.CallerRecipient != nil && spec.Recipient == (coord.SessionIdentity{}) {
		return spec.CallerRecipient.PolicyUID
	}
	return spec.Recipient.UID
}
func AckName(messageUID, recipientUID string) string {
	return fullName("read-ack", []string{messageUID, recipientUID})
}
func ValidMessageName(name string) bool { return validName(name, "message") }
func validName(name, prefix string) bool {
	parts := strings.Split(name, ".")
	return len(parts) == 3 && parts[0] == prefix && len(parts[1]) == 32 && len(parts[2]) == 32 && regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(parts[1]+parts[2])
}
func ValidSession(s coord.SessionIdentity) bool {
	return len(validation.IsDNS1123Label(s.Namespace)) == 0 && len(validation.IsDNS1123Subdomain(s.Name)) == 0 && uidPattern.MatchString(s.UID)
}
func ValidKey(key string) bool { return keyPattern.MatchString(key) }
func ValidText(text string) bool {
	return len(text) > 0 && len(text) <= MaxTextBytes && utf8.ValidString(text)
}
func recipientLabel(s coord.SessionIdentity) string { return digest(s)[:63] }

// callerLabel is the room and list key for one CallerPolicy identity.
// It is not a Session identity, so a caller sender does not share an empty
// session's room.
func callerLabel(c *coord.InboxCaller) string {
	return digest(struct {
		Caller         string
		ServiceAccount string
		PolicyUID      string
	}{"caller", c.ServiceAccount, c.PolicyUID})[:63]
}
func inReplyToLabel(r coord.InboxMessageRef) string { return digest(r)[:63] }

func ValidMessage(s coord.InboxMessageSpec) bool {
	return validMessageSpec(s, false)
}

// ValidArchiveMetadata validates immutable inbox attribution after the shelf
// has verified the complete object and the API has stripped spec.text. It is
// intentionally not a replacement for ValidMessage's live text validation.
func ValidArchiveMetadata(s coord.InboxMessageSpec) bool {
	return validMessageSpec(s, true)
}

func validMessageSpec(s coord.InboxMessageSpec, archived bool) bool {
	textOK := ValidText(s.Text)
	if archived {
		textOK = s.Text == ""
	}
	if !textOK || !ValidKey(s.Key) || !validEnds(s) || !validCorrelation(s) || !validProvider(s) {
		return false
	}
	// Reuse bounded attribution and provenance validation without sharing claim
	// ACLs. TaskClaim refuses a policy actor, so a caller sender is checked
	// here and the provenance reuse sees a session actor.
	owner, actor := s.Sender, s.Actor
	if s.CallerSender != nil {
		actor.Kind = "session"
		if owner == (coord.SessionIdentity{}) {
			owner = coord.SessionIdentity{Namespace: "dev-agents", Name: "provenance", UID: "provenance"}
		}
	}
	claim := coord.TaskClaimSpec{Identity: coord.TaskIdentity{Project: s.Project, Repository: s.Repository, TaskKey: "task:inbox"}, Owner: owner, Actor: actor, Provenance: s.Provenance}
	return taskclaim.ValidSpec(claim)
}

func validProvider(s coord.InboxMessageSpec) bool {
	return (s.Provider == "claude" || s.Provider == "codex" || s.Provider == "cursor" || s.Provider == "opencode") && modelPattern.MatchString(s.RequestedModel)
}

func validEnds(s coord.InboxMessageSpec) bool {
	sender, callerSender := s.Sender != (coord.SessionIdentity{}), s.CallerSender != nil
	recipient, callerRecipient := s.Recipient != (coord.SessionIdentity{}), s.CallerRecipient != nil
	if sender == callerSender || recipient == callerRecipient {
		return false
	}
	if !validInboxActor(s) {
		return false
	}
	if s.Kind == "" && callerSender && !recipient {
		return false
	}
	if sender && !ValidSession(s.Sender) {
		return false
	}
	if recipient && !ValidSession(s.Recipient) {
		return false
	}
	if callerSender && !validCaller(s.CallerSender) {
		return false
	}
	if callerRecipient && !validCaller(s.CallerRecipient) {
		return false
	}
	return true
}

func validCaller(c *coord.InboxCaller) bool {
	return c != nil && c.ServiceAccount == coord.MeatballServiceAccount && uidPattern.MatchString(c.PolicyUID)
}

func validInboxActor(s coord.InboxMessageSpec) bool {
	if !validActor(s.Actor) {
		return false
	}
	switch s.Actor.Kind {
	case "session":
		return s.Sender != (coord.SessionIdentity{})
	case "policy":
		return s.CallerSender != nil && s.Actor.ServiceAccount == coord.MeatballServiceAccount && s.CallerSender.ServiceAccount == coord.MeatballServiceAccount
	default:
		return false
	}
}

// validCorrelation mirrors the schema: every reply, and only a reply, names one
// exact message. A question, result or blocker names none. Which message a
// reply may name is the API's check.
func validCorrelation(s coord.InboxMessageSpec) bool {
	switch s.Kind {
	case "", KindQuestion, KindResult, KindBlocker:
		return s.InReplyTo == nil
	case KindReply:
		return s.InReplyTo != nil && ValidRef(*s.InReplyTo)
	}
	return false
}

// ValidRef reports whether r names one message by full-digest name and UID.
func ValidRef(r coord.InboxMessageRef) bool {
	return ValidMessageName(r.Name) && ValidUID(r.UID)
}

// ValidUID reports whether uid has the Kubernetes UID shape the inbox stores.
func ValidUID(uid string) bool { return uidPattern.MatchString(uid) }

// Ref names m exactly.
func Ref(m *coord.InboxMessage) coord.InboxMessageRef {
	return coord.InboxMessageRef{Name: m.Name, UID: string(m.UID)}
}

// SameIntent compares request intent and saved sender provenance. Resumed Pod
// attribution and reported heartbeat observations keep their original values.
func SameIntent(a, b coord.InboxMessageSpec) bool {
	a.Actor, b.Actor = coord.ClaimActor{}, coord.ClaimActor{}
	a.Provenance.ReportedHead, b.Provenance.ReportedHead = "", ""
	a.Provenance.ReportedAt, b.Provenance.ReportedAt = nil, nil
	return reflect.DeepEqual(a, b)
}

func validActor(a coord.ClaimActor) bool {
	ns, sa, ok := strings.Cut(a.ServiceAccount, "/")
	kindOK := a.Kind == "session" || a.Kind == "policy"
	return kindOK && ok && len(validation.IsDNS1123Label(ns)) == 0 && len(validation.IsDNS1123Subdomain(sa)) == 0 && len(validation.IsDNS1123Subdomain(a.PodName)) == 0 && uidPattern.MatchString(a.PodUID)
}
func ValidAck(s coord.InboxReadAckSpec) bool {
	return projectcatalog.ValidName(s.Project) && ValidMessageName(s.MessageName) && uidPattern.MatchString(s.MessageUID) && ValidSession(s.Recipient) && s.Actor.Kind == "session" && validActor(s.Actor)
}

type KubeStore struct {
	Client client.Client
	Live   client.Reader
	// Watcher uses the same cluster and identity as Live, without its cache.
	// Nil refuses held parent-note reads; it never creates an implicit client.
	Watcher   client.WithWatch
	Namespace string
	Limits    taskclaim.Limits // zero fields take DefaultLimits; they bound messages, not acks
}

func (s KubeStore) ready() bool {
	return s.Client != nil && s.Live != nil && len(validation.IsDNS1123Label(s.Namespace)) == 0
}
func (s KubeStore) validObject(o client.Object, name string, labels ...map[string]string) bool {
	created := o.GetCreationTimestamp()
	return o.GetNamespace() == s.Namespace && o.GetName() == name && o.GetUID() != "" && o.GetResourceVersion() != "" && !created.IsZero() && o.GetDeletionTimestamp().IsZero() &&
		len(o.GetOwnerReferences()) == 0 && len(o.GetFinalizers()) == 0 && annotationsOK(o) &&
		slices.ContainsFunc(labels, func(want map[string]string) bool { return maps.Equal(o.GetLabels(), want) })
}
func recipientLabels(project string, recipient coord.SessionIdentity) map[string]string {
	return map[string]string{taskclaim.ProjectLabel: project, RecipientLabel: recipientLabel(recipient)}
}
func messageLabels(spec coord.InboxMessageSpec) map[string]string {
	recipientValue := recipientLabel(spec.Recipient)
	if spec.CallerRecipient != nil && spec.Recipient == (coord.SessionIdentity{}) {
		recipientValue = callerLabel(spec.CallerRecipient)
	}
	l := map[string]string{taskclaim.ProjectLabel: spec.Project, RecipientLabel: recipientValue}
	l[taskclaim.SourceLabel] = taskclaim.SessionLabel(spec.Sender)
	if spec.CallerSender != nil && spec.Sender == (coord.SessionIdentity{}) {
		l[taskclaim.SourceLabel] = callerLabel(spec.CallerSender)
	}
	if spec.Kind != "" {
		l[KindLabel] = spec.Kind
	} else if spec.CallerSender != nil && spec.Sender == (coord.SessionIdentity{}) && spec.InReplyTo == nil {
		l[KindLabel] = KindPlain
	}
	if spec.InReplyTo != nil {
		l[InReplyToLabel] = inReplyToLabel(*spec.InReplyTo)
	}
	return l
}

// A plain message from before the sender's source label carries project and
// recipient labels only; it stays valid. Questions and replies are newer and
// always carry their full label set, which their lookups select on.
func (s KubeStore) validMessage(m *coord.InboxMessage) bool {
	if m == nil || !ValidMessage(m.Spec) {
		return false
	}
	name := MessageNameFor(m.Spec)
	if IsParentNote(m) {
		return validParentNoteSpec(m.Spec) && s.validObject(m, name, parentNoteLabels(m.Spec))
	}
	if m.Spec.Kind != "" {
		return s.validObject(m, name, messageLabels(m.Spec))
	}
	return s.validObject(m, name, recipientLabels(m.Spec.Project, m.Spec.Recipient), messageLabels(m.Spec))
}
func (s KubeStore) validAck(a *coord.InboxReadAck) bool {
	return ValidAck(a.Spec) && s.validObject(a, AckName(a.Spec.MessageUID, a.Spec.Recipient.UID), recipientLabels(a.Spec.Project, a.Spec.Recipient))
}

func (s KubeStore) Get(ctx context.Context, name string) (*coord.InboxMessage, error) {
	if !s.ready() || !ValidMessageName(name) {
		return nil, ErrUnavailable
	}
	var m coord.InboxMessage
	if err := s.Live.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, &m); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, ErrUnavailable
	}
	if !s.validMessage(&m) {
		return nil, ErrUnavailable
	}
	return &m, nil
}
func ambiguous(err error) bool {
	return !apierrors.IsInvalid(err) && !apierrors.IsForbidden(err) && !apierrors.IsUnauthorized(err) && !apierrors.IsBadRequest(err)
}
func (s KubeStore) Create(ctx context.Context, spec coord.InboxMessageSpec) (*coord.InboxMessage, bool, error) {
	return s.create(ctx, spec, false)
}

// checkMessageRoom counts a session sender with CheckRoom. A caller sender
// has an empty session identity, so its room is its own caller key.
func (s KubeStore) checkMessageRoom(ctx context.Context, spec coord.InboxMessageSpec) error {
	limits := s.Limits.Effective(DefaultLimits)
	if spec.CallerSender != nil && spec.Sender == (coord.SessionIdentity{}) {
		return checkLabeledRoom(ctx, s.Live, s.Namespace, spec.Project, callerLabel(spec.CallerSender), limits)
	}
	return taskclaim.CheckRoom(ctx, s.Live, "InboxMessageList", s.Namespace, spec.Project, spec.Sender, limits)
}

func checkLabeledRoom(ctx context.Context, r client.Reader, namespace, project, source string, limits taskclaim.Limits) error {
	for _, bound := range []struct {
		match map[string]string
		max   int64
	}{
		// The caller is not bound to one project. The sender cap is the source alone.
		{map[string]string{taskclaim.SourceLabel: source}, limits.PerSession},
		{map[string]string{taskclaim.ProjectLabel: project}, limits.PerProject},
	} {
		n, err := countLabeled(ctx, r, namespace, bound.match, bound.max)
		if err != nil {
			return err
		}
		if n >= bound.max {
			return ErrLimit
		}
	}
	return nil
}

func countLabeled(ctx context.Context, r client.Reader, namespace string, match map[string]string, bound int64) (int64, error) {
	if r == nil || bound < 1 {
		return 0, ErrUnavailable
	}
	var n int64
	cursor := ""
	for n < bound {
		page := &metav1.PartialObjectMetadataList{}
		page.SetGroupVersionKind(coord.GroupVersion.WithKind("InboxMessageList"))
		if err := r.List(ctx, page, client.InNamespace(namespace), client.MatchingLabels(match), client.Limit(min(bound-n, 500)), client.Continue(cursor)); err != nil {
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

// sameSenderForm confirms the sender the create asked for. A session sender
// is the stored session identity. A caller sender has an empty session, so
// the stored name is the confirmation.
func sameSenderForm(observed, spec coord.InboxMessageSpec) bool {
	if spec.CallerSender != nil && spec.Sender == (coord.SessionIdentity{}) {
		return MessageNameFor(observed) == MessageNameFor(spec)
	}
	return observed.Sender == spec.Sender
}

func (s KubeStore) create(ctx context.Context, spec coord.InboxMessageSpec, parentNote bool) (*coord.InboxMessage, bool, error) {
	if !s.ready() || !ValidMessage(spec) {
		return nil, false, ErrUnavailable
	}
	// Only a new message counts against the bounds; a retry still confirms.
	if _, err := s.Get(ctx, MessageNameFor(spec)); errors.Is(err, ErrNotFound) {
		if err := s.checkMessageRoom(ctx, spec); err != nil {
			if errors.Is(err, ErrLimit) {
				return nil, false, ErrLimit
			}
			return nil, false, ErrUnavailable
		}
		// A plain peer message has no new field to prune. A caller sender or
		// recipient does, even when kind is absent.
		if spec.Kind != "" || spec.CallerSender != nil || spec.CallerRecipient != nil {
			if err := s.checkSchema(ctx, spec); err != nil {
				return nil, false, err
			}
		}
	} else if err != nil {
		return nil, false, ErrUnavailable
	}
	labels := messageLabels(spec)
	if parentNote {
		labels = parentNoteLabels(spec)
	}
	m := &coord.InboxMessage{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: MessageNameFor(spec), Labels: labels}, Spec: spec}
	createErr := s.Client.Create(ctx, m)
	observed, err := s.Get(ctx, m.Name)
	if err != nil || observed.Spec.Key != spec.Key || !sameSenderForm(observed.Spec, spec) {
		return nil, false, ErrUnavailable
	}
	if createErr == nil {
		if observed.UID != m.UID || !reflect.DeepEqual(observed.Spec, spec) {
			return nil, false, ErrUnavailable
		}
		return observed, true, nil
	}
	if !apierrors.IsAlreadyExists(createErr) && !ambiguous(createErr) {
		return nil, false, ErrUnavailable
	}
	return observed, false, nil
}

// checkSchema dry-runs the create of a question or reply and compares what the
// API server would store. A CRD from before kind and inReplyTo prunes both
// without an error, and the stored record would fail every later read. The
// dry-run uses the create permission the real create already needs. Its answer
// is read into an unstructured object, which a response replaces whole, so a
// pruned field cannot survive from the request.
func (s KubeStore) checkSchema(ctx context.Context, spec coord.InboxMessageSpec) error {
	probe := &coord.InboxMessage{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: MessageNameFor(spec), Labels: messageLabels(spec)}, Spec: spec}
	fields, err := runtime.DefaultUnstructuredConverter.ToUnstructured(probe)
	if err != nil {
		return ErrUnavailable
	}
	u := &unstructured.Unstructured{Object: fields}
	u.SetGroupVersionKind(coord.GroupVersion.WithKind("InboxMessage"))
	switch err := s.Client.Create(ctx, u, client.DryRunAll); {
	case apierrors.IsAlreadyExists(err):
		// A concurrent create of the same intent; the real create confirms it.
		return nil
	case err != nil:
		return ErrUnavailable
	}
	kind, _, _ := unstructured.NestedString(u.Object, "spec", "kind")
	_, hasReplyTo, _ := unstructured.NestedMap(u.Object, "spec", "inReplyTo")
	_, hasCallerSender, _ := unstructured.NestedMap(u.Object, "spec", "callerSender")
	_, hasCallerRecipient, _ := unstructured.NestedMap(u.Object, "spec", "callerRecipient")
	if kind != spec.Kind || hasReplyTo != (spec.InReplyTo != nil) || hasCallerSender != (spec.CallerSender != nil) || hasCallerRecipient != (spec.CallerRecipient != nil) {
		return ErrSchemaNotUpgraded
	}
	return nil
}
func (s KubeStore) ReadAck(ctx context.Context, m *coord.InboxMessage) (*coord.InboxReadAck, error) {
	if !s.ready() || !s.validMessage(m) {
		return nil, ErrUnavailable
	}
	var a coord.InboxReadAck
	if err := s.Live.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: AckName(string(m.UID), m.Spec.Recipient.UID)}, &a); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, ErrUnavailable
	}
	if !s.validAck(&a) || a.Spec.MessageName != m.Name || a.Spec.MessageUID != string(m.UID) || a.Spec.Project != m.Spec.Project || a.Spec.Recipient != m.Spec.Recipient {
		return nil, ErrUnavailable
	}
	return &a, nil
}
func (s KubeStore) Ack(ctx context.Context, m *coord.InboxMessage, actor coord.ClaimActor) (*coord.InboxReadAck, bool, error) {
	if !s.ready() || !s.validMessage(m) || !validActor(actor) {
		return nil, false, ErrUnavailable
	}
	a := &coord.InboxReadAck{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: AckName(string(m.UID), m.Spec.Recipient.UID), Labels: recipientLabels(m.Spec.Project, m.Spec.Recipient)}, Spec: coord.InboxReadAckSpec{Project: m.Spec.Project, MessageName: m.Name, MessageUID: string(m.UID), Recipient: m.Spec.Recipient, Actor: actor}}
	createErr := s.Client.Create(ctx, a)
	observed, err := s.ReadAck(ctx, m)
	if err != nil {
		return nil, false, ErrUnavailable
	}
	if createErr == nil {
		if observed.UID != a.UID || !reflect.DeepEqual(observed.Spec, a.Spec) {
			return nil, false, ErrUnavailable
		}
		return observed, true, nil
	}
	if !apierrors.IsAlreadyExists(createErr) && !ambiguous(createErr) {
		return nil, false, ErrUnavailable
	}
	return observed, false, nil
}
func (s KubeStore) List(ctx context.Context, project string, recipient *coord.SessionIdentity, limit int64, cursor string) ([]coord.InboxMessage, string, error) {
	if !s.ready() || !projectcatalog.ValidName(project) || limit < 1 || limit > 100 || len(cursor) > 4096 {
		return nil, "", ErrUnavailable
	}
	labels := map[string]string{taskclaim.ProjectLabel: project}
	if recipient != nil {
		if !ValidSession(*recipient) {
			return nil, "", ErrUnavailable
		}
		labels[RecipientLabel] = recipientLabel(*recipient)
	}
	selector, err := peerMessageSelector(labels)
	if err != nil {
		return nil, "", ErrUnavailable
	}
	var list coord.InboxMessageList
	if s.Live.List(ctx, &list, client.InNamespace(s.Namespace), selector, client.Limit(limit), client.Continue(cursor)) != nil || int64(len(list.Items)) > limit || len(list.Continue) > 4096 {
		return nil, "", ErrUnavailable
	}
	for i := range list.Items {
		m := &list.Items[i]
		if !s.validMessage(m) || m.Spec.Project != project || (recipient != nil && m.Spec.Recipient != *recipient) {
			return nil, "", ErrUnavailable
		}
	}
	return list.Items, list.Continue, nil
}

// ListSent pages the messages the sender Session published in project. Plain
// messages from before the source label are not found here.
func (s KubeStore) ListSent(ctx context.Context, project string, sender coord.SessionIdentity, limit int64, cursor string) ([]coord.InboxMessage, string, error) {
	if !s.ready() || !projectcatalog.ValidName(project) || !ValidSession(sender) || limit < 1 || limit > 100 || len(cursor) > 4096 {
		return nil, "", ErrUnavailable
	}
	labels := map[string]string{taskclaim.ProjectLabel: project, taskclaim.SourceLabel: taskclaim.SessionLabel(sender)}
	selector, err := peerMessageSelector(labels)
	if err != nil {
		return nil, "", ErrUnavailable
	}
	var list coord.InboxMessageList
	if s.Live.List(ctx, &list, client.InNamespace(s.Namespace), selector, client.Limit(limit), client.Continue(cursor)) != nil || int64(len(list.Items)) > limit || len(list.Continue) > 4096 {
		return nil, "", ErrUnavailable
	}
	for i := range list.Items {
		m := &list.Items[i]
		if !s.validMessage(m) || m.Spec.Project != project || m.Spec.Sender != sender {
			return nil, "", ErrUnavailable
		}
	}
	return list.Items, list.Continue, nil
}

// Replies returns the live replies to question q, oldest first. Each must come
// from q's recipient, go to q's sender and name q exactly; anything else, or
// more than MaxReplies, is ErrUnavailable rather than a guess.
func (s KubeStore) Replies(ctx context.Context, q *coord.InboxMessage) ([]coord.InboxMessage, error) {
	if !s.ready() || !s.validMessage(q) || q.Spec.Kind != KindQuestion {
		return nil, ErrUnavailable
	}
	labels := map[string]string{taskclaim.ProjectLabel: q.Spec.Project, KindLabel: KindReply, InReplyToLabel: inReplyToLabel(Ref(q))}
	var list coord.InboxMessageList
	if s.Live.List(ctx, &list, client.InNamespace(s.Namespace), client.MatchingLabels(labels), client.Limit(MaxReplies+1)) != nil || list.Continue != "" {
		return nil, ErrUnavailable
	}
	for i := range list.Items {
		if !s.validMessage(&list.Items[i]) {
			return nil, ErrUnavailable
		}
	}
	return RepliesOf(q, list.Items)
}

// RepliesOf picks question q's replies out of candidates, oldest first, with
// Replies' checks. A candidate that names q from the wrong party, or more than
// MaxReplies, is ErrUnavailable.
func RepliesOf(q *coord.InboxMessage, candidates []coord.InboxMessage) ([]coord.InboxMessage, error) {
	ref := Ref(q)
	var out []coord.InboxMessage
	for i := range candidates {
		r := &candidates[i]
		if r.Spec.InReplyTo == nil || *r.Spec.InReplyTo != ref {
			continue
		}
		if r.Spec.Kind != KindReply || r.Spec.Project != q.Spec.Project || r.Spec.Sender != q.Spec.Recipient || r.Spec.Recipient != q.Spec.Sender {
			return nil, ErrUnavailable
		}
		out = append(out, *r)
	}
	if len(out) > MaxReplies {
		return nil, ErrUnavailable
	}
	slices.SortFunc(out, func(a, b coord.InboxMessage) int {
		if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

// ReplyEntry is one reply in a thread list's index. It is cut from the reply's
// metadata alone, so it holds no text and no spec: a fixed-size record per
// reply, whatever the replies weigh. The reply itself is read by Name only for
// a question that matches it.
type ReplyEntry struct {
	Name string
	UID  types.UID
	// InReplyTo is the reply's in-reply-to label, the digest of its question's
	// exact reference.
	InReplyTo string
}

// ReplyIndex lists, in pages of 100 up to the project's message limit, the
// live replies in project sent by from or addressed to to (either may be nil),
// so one thread list page needs one bounded scan instead of one per question.
// The scan lists metadata only (object meta and labels), through the same
// uncached reader and list permission, so it never holds a reply's text;
// RepliesIn reads the matched ones. Replies are messages, so the project limit
// bounds them; the scan covers it whole. If live replies exceed the configured
// limit (for example after it was lowered), or an entry's metadata is not that
// of a valid reply, the index is ErrUnavailable (state Unknown), not a guess.
func (s KubeStore) ReplyIndex(ctx context.Context, project string, from, to *coord.SessionIdentity) ([]ReplyEntry, error) {
	if !s.ready() || !projectcatalog.ValidName(project) || (from != nil && !ValidSession(*from)) || (to != nil && !ValidSession(*to)) {
		return nil, ErrUnavailable
	}
	labels := map[string]string{taskclaim.ProjectLabel: project, KindLabel: KindReply}
	if from != nil {
		labels[taskclaim.SourceLabel] = taskclaim.SessionLabel(*from)
	}
	if to != nil {
		labels[RecipientLabel] = recipientLabel(*to)
	}
	var out []ReplyEntry
	cursor := ""
	pages := (s.Limits.Effective(DefaultLimits).PerProject + replyPageSize - 1) / replyPageSize
	for page := int64(0); page < pages; page++ {
		list := &metav1.PartialObjectMetadataList{}
		list.SetGroupVersionKind(coord.GroupVersion.WithKind("InboxMessageList"))
		if s.Live.List(ctx, list, client.InNamespace(s.Namespace), client.MatchingLabels(labels), client.Limit(replyPageSize), client.Continue(cursor)) != nil || len(list.Items) > replyPageSize || len(list.Continue) > 4096 {
			return nil, ErrUnavailable
		}
		for i := range list.Items {
			r := &list.Items[i]
			if !s.validReplyMetadata(r, project, from, to) {
				return nil, ErrUnavailable
			}
			out = append(out, ReplyEntry{Name: r.Name, UID: r.UID, InReplyTo: r.Labels[InReplyToLabel]})
		}
		if cursor = list.Continue; cursor == "" {
			return out, nil
		}
	}
	return nil, ErrUnavailable
}

// validReplyMetadata is validObject for a metadata-only reply: the object
// shape and the exact label set a reply carries, matching the selected parties.
// The spec-level checks run when RepliesIn reads a matched reply.
func (s KubeStore) validReplyMetadata(m *metav1.PartialObjectMetadata, project string, from, to *coord.SessionIdentity) bool {
	l := m.Labels
	return ValidMessageName(m.Name) && m.Namespace == s.Namespace && m.UID != "" && m.ResourceVersion != "" && !m.CreationTimestamp.IsZero() && m.DeletionTimestamp.IsZero() &&
		len(m.OwnerReferences) == 0 && len(m.Finalizers) == 0 && len(m.Annotations) == 0 &&
		len(l) == 5 && l[taskclaim.ProjectLabel] == project && l[KindLabel] == KindReply &&
		len(l[RecipientLabel]) == 63 && len(l[taskclaim.SourceLabel]) == 63 && len(l[InReplyToLabel]) == 63 &&
		(from == nil || l[taskclaim.SourceLabel] == taskclaim.SessionLabel(*from)) && (to == nil || l[RecipientLabel] == recipientLabel(*to))
}

// RepliesIn picks question q's live replies out of a ReplyIndex, oldest first,
// reading only the ones the index matches to q, by name, with Replies' checks:
// each must come from q's recipient, go to q's sender and name q exactly.
// More than MaxReplies matches, or a matched reply that is not what its index
// entry said, is ErrUnavailable.
func (s KubeStore) RepliesIn(ctx context.Context, q *coord.InboxMessage, index []ReplyEntry) ([]coord.InboxMessage, error) {
	if !s.ready() || !s.validMessage(q) || q.Spec.Kind != KindQuestion {
		return nil, ErrUnavailable
	}
	want := inReplyToLabel(Ref(q))
	var matched []ReplyEntry
	for _, e := range index {
		if e.InReplyTo == want {
			matched = append(matched, e)
		}
	}
	if len(matched) > MaxReplies {
		return nil, ErrUnavailable
	}
	got := make([]coord.InboxMessage, 0, len(matched))
	for _, e := range matched {
		r, err := s.Get(ctx, e.Name)
		if err != nil || r.UID != e.UID {
			return nil, ErrUnavailable
		}
		got = append(got, *r)
	}
	return RepliesOf(q, got)
}
