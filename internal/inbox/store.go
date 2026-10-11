// Package inbox retains immutable API messages and explicit read acknowledgements.
// Only Create mutates storage; all observations use the uncached reader.
package inbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
)

var (
	ErrUnavailable = errors.New("inbox observation is unconfirmed")
	ErrNotFound    = errors.New("inbox record not found")
	keyPattern     = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,126}[a-z0-9])?$`)
	uidPattern     = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
	modelPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,127}$`)
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

func ValidMessage(s coord.InboxMessageSpec) bool {
	// Reuse bounded attribution/provenance validation without sharing claim ACLs.
	claim := coord.TaskClaimSpec{Identity: coord.TaskIdentity{Project: s.Project, Repository: s.Repository, TaskKey: "task:inbox"}, Owner: s.Sender, Actor: s.Actor, Provenance: s.Provenance}
	return taskclaim.ValidSpec(claim) && s.Actor.Kind == "session" && ValidSession(s.Recipient) && ValidKey(s.Key) && ValidText(s.Text) &&
		(s.Provider == "claude" || s.Provider == "codex" || s.Provider == "opencode") && modelPattern.MatchString(s.RequestedModel)
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
	return a.Kind == "session" && ok && len(validation.IsDNS1123Label(ns)) == 0 && len(validation.IsDNS1123Subdomain(sa)) == 0 && len(validation.IsDNS1123Subdomain(a.PodName)) == 0 && uidPattern.MatchString(a.PodUID)
}
func ValidAck(s coord.InboxReadAckSpec) bool {
	return projectcatalog.ValidName(s.Project) && ValidMessageName(s.MessageName) && uidPattern.MatchString(s.MessageUID) && ValidSession(s.Recipient) && validActor(s.Actor)
}

type KubeStore struct {
	Client    client.Client
	Live      client.Reader
	Namespace string
}

func (s KubeStore) ready() bool {
	return s.Client != nil && s.Live != nil && len(validation.IsDNS1123Label(s.Namespace)) == 0
}
func (s KubeStore) validObject(o client.Object, name, project string) bool {
	created := o.GetCreationTimestamp()
	return o.GetNamespace() == s.Namespace && o.GetName() == name && o.GetUID() != "" && o.GetResourceVersion() != "" && !created.IsZero() && o.GetDeletionTimestamp().IsZero() &&
		len(o.GetOwnerReferences()) == 0 && len(o.GetFinalizers()) == 0 && len(o.GetAnnotations()) == 0 && len(o.GetLabels()) == 2 && o.GetLabels()[taskclaim.ProjectLabel] == project
}
func (s KubeStore) validMessage(m *coord.InboxMessage) bool {
	return m != nil && ValidMessage(m.Spec) && s.validObject(m, MessageName(m.Spec.Sender, m.Spec.Key), m.Spec.Project) && m.Labels[RecipientLabel] == recipientLabel(m.Spec.Recipient)
}
func (s KubeStore) validAck(a *coord.InboxReadAck) bool {
	return ValidAck(a.Spec) && s.validObject(a, AckName(a.Spec.MessageUID, a.Spec.Recipient.UID), a.Spec.Project) && a.Labels[RecipientLabel] == recipientLabel(a.Spec.Recipient)
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
	if !s.ready() || !ValidMessage(spec) {
		return nil, false, ErrUnavailable
	}
	m := &coord.InboxMessage{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: MessageName(spec.Sender, spec.Key), Labels: map[string]string{taskclaim.ProjectLabel: spec.Project, RecipientLabel: recipientLabel(spec.Recipient)}}, Spec: spec}
	createErr := s.Client.Create(ctx, m)
	observed, err := s.Get(ctx, m.Name)
	if err != nil || observed.Spec.Sender != spec.Sender || observed.Spec.Key != spec.Key {
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
	a := &coord.InboxReadAck{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: AckName(string(m.UID), m.Spec.Recipient.UID), Labels: map[string]string{taskclaim.ProjectLabel: m.Spec.Project, RecipientLabel: recipientLabel(m.Spec.Recipient)}}, Spec: coord.InboxReadAckSpec{Project: m.Spec.Project, MessageName: m.Name, MessageUID: string(m.UID), Recipient: m.Spec.Recipient, Actor: actor}}
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
	var list coord.InboxMessageList
	if s.Live.List(ctx, &list, client.InNamespace(s.Namespace), client.MatchingLabels(labels), client.Limit(limit), client.Continue(cursor)) != nil || int64(len(list.Items)) > limit || len(list.Continue) > 4096 {
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
