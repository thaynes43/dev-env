// Package contextpublication stores immutable deliberately published excerpts.
// Create is its only mutation; all observations bypass informer caches.
package contextpublication

import (
	"context"
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
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

var ErrUnavailable = errors.New("context publication is unconfirmed")
var ErrNotFound = errors.New("published context not found")
var ErrConflict = errors.New("publication identity already has different content or provenance")
var namePattern = regexp.MustCompile(`^published-context\.[a-f0-9]{32}\.[a-f0-9]{32}$`)

func ValidName(name string) bool          { return namePattern.MatchString(name) }
func Name(i coord.ContextIdentity) string { return apiv1.ContextName(i.SessionUID, i.Kind, i.Key) }

func boundedText(s string, bound int) bool {
	return len(s) > 0 && len(s) <= bound && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}

func ValidSpec(s coord.PublishedContextSpec) bool {
	i := s.Identity
	// Reuse existing server-derived bounded provenance validation without changing
	// TaskClaim semantics. Publication currently has no observed HEAD/model proof.
	c := coord.TaskClaimSpec{Identity: coord.TaskIdentity{Project: s.Project, Repository: s.Repository, TaskKey: "task:context"}, Owner: s.Source, Actor: s.Actor, Provenance: s.Provenance}
	return taskclaim.ValidSpec(c) && s.Actor.Kind == "session" && apiv1.ValidContextUID(i.SessionUID) && s.Source.UID == i.SessionUID &&
		apiv1.ValidContextRequest(apiv1.PublishContextRequest{Kind: i.Kind, Key: i.Key, Text: s.Text}) && s.SourceGeneration >= 1 && boundedText(s.SourceParent, 317) &&
		len(validation.IsDNS1123Label(s.Attribution.Provider)) == 0 && boundedText(s.Attribution.RequestedModel, 256) &&
		s.Attribution.ObservedModelState == "Unknown" && s.Attribution.UsageState == "Unknown" && s.Attribution.CostState == "Unknown" &&
		s.Provenance.ReportedHead == "" && s.Provenance.ReportedAt == nil && validOrigin(s) && s.StopState == "Unknown" &&
		s.ContentSHA256 == apiv1.ContextHash(s.Text) && int(s.ByteCount) == len(s.Text)
}

type KubeStore struct {
	Client    client.Client
	Live      client.Reader
	Namespace string
}

func (s KubeStore) ready() bool {
	return s.Client != nil && s.Live != nil && len(validation.IsDNS1123Label(s.Namespace)) == 0
}
func (s KubeStore) valid(c *coord.PublishedContext) bool {
	return c.Namespace == s.Namespace && c.Name == Name(c.Spec.Identity) && ValidSpec(c.Spec) && c.UID != "" && c.ResourceVersion != "" &&
		!c.CreationTimestamp.IsZero() && c.DeletionTimestamp.IsZero() && len(c.OwnerReferences) == 0 && len(c.Finalizers) == 0 && len(c.Annotations) == 0 &&
		len(c.Labels) == 1 && c.Labels[taskclaim.ProjectLabel] == c.Spec.Project
}
func (s KubeStore) Get(ctx context.Context, name string) (*coord.PublishedContext, error) {
	return s.get(ctx, name, "")
}

// GetForProject masks even malformed foreign resources before validating them,
// so a payload reader's named probes do not disclose another project's objects.
func (s KubeStore) GetForProject(ctx context.Context, name, project string) (*coord.PublishedContext, error) {
	if len(validation.IsDNS1123Label(project)) != 0 {
		return nil, ErrUnavailable
	}
	return s.get(ctx, name, project)
}
func (s KubeStore) get(ctx context.Context, name, project string) (*coord.PublishedContext, error) {
	if !s.ready() || !ValidName(name) {
		return nil, ErrUnavailable
	}
	var c coord.PublishedContext
	if err := s.Live.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, &c); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, ErrUnavailable
	}
	if project != "" && c.Spec.Project != project {
		return nil, ErrNotFound
	}
	if !s.valid(&c) {
		return nil, ErrUnavailable
	}
	return &c, nil
}

// Exact acknowledgement confirmation never adopts different content or actor.
func (s KubeStore) Create(ctx context.Context, spec coord.PublishedContextSpec) (*coord.PublishedContext, bool, error) {
	if !s.ready() || !ValidSpec(spec) {
		return nil, false, ErrUnavailable
	}
	c := &coord.PublishedContext{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: Name(spec.Identity), Labels: map[string]string{taskclaim.ProjectLabel: spec.Project}}, Spec: spec}
	createErr := s.Client.Create(ctx, c)
	if createErr != nil && (apierrors.IsInvalid(createErr) || apierrors.IsForbidden(createErr) || apierrors.IsUnauthorized(createErr)) {
		return nil, false, ErrUnavailable
	}
	observed, err := s.Get(ctx, c.Name)
	if err != nil {
		return nil, false, ErrUnavailable
	}
	if !reflect.DeepEqual(observed.Spec, spec) {
		return nil, false, ErrConflict
	}
	if createErr == nil && observed.UID != c.UID {
		return nil, false, ErrUnavailable
	}
	return observed, createErr == nil, nil
}
func (s KubeStore) List(ctx context.Context, project string, limit int64, cursor string) ([]coord.PublishedContext, string, error) {
	if !s.ready() || len(validation.IsDNS1123Label(project)) != 0 || limit < 1 || limit > 20 || len(cursor) > 4096 {
		return nil, "", ErrUnavailable
	}
	var list coord.PublishedContextList
	if s.Live.List(ctx, &list, client.InNamespace(s.Namespace), client.MatchingLabels{taskclaim.ProjectLabel: project}, client.Limit(limit), client.Continue(cursor)) != nil || int64(len(list.Items)) > limit {
		return nil, "", ErrUnavailable
	}
	for i := range list.Items {
		if !s.valid(&list.Items[i]) || list.Items[i].Spec.Project != project {
			return nil, "", ErrUnavailable
		}
	}
	return list.Items, list.Continue, nil
}

func validOrigin(s coord.PublishedContextSpec) bool {
	if s.ManagedOutput == nil {
		return s.Origin == "ExplicitPublished" && s.Completeness == "ExplicitExcerpt"
	}
	m := s.ManagedOutput
	assertion := &apiv1.ManagedOutputAssertion{Invocation: m.Invocation, Ordinal: m.Ordinal, ConversationID: m.ConversationID}
	return s.Origin == "ManagedOutputAssertion" && (s.Completeness == "PartialManagedOutput" || s.Completeness == "Unknown") && apiv1.ValidManagedOutput(assertion) && m.Invocation == apiv1.ManagedInvocation(s.Source.UID, s.Actor.PodUID) && s.Identity.Kind == "transcript-excerpt" && s.Identity.Key == apiv1.ManagedOutputKey(m.Invocation, m.Ordinal) && (s.Attribution.Provider == "claude" || s.Attribution.Provider == "codex")
}
