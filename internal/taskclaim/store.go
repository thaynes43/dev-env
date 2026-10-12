// Package taskclaim implements immutable explicit ownership. Kubernetes Create
// is the only mutation; readers must bypass the manager's informer cache.
package taskclaim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

const (
	ProjectLabel = "coordination.dev-env.haynesops.com/project"
	// SourceLabel names the Session a record counts against: a TaskClaim's
	// owner, a PublishedContext's source or an InboxMessage's sender. The value
	// is a digest, because a Session identity exceeds a label value's bound.
	SourceLabel = "coordination.dev-env.haynesops.com/source"
	// WorkLabel names a TaskClaim's work independent of the catalog's
	// repository key: project, lowercased GitHub repository and task key.
	WorkLabel = "coordination.dev-env.haynesops.com/work"
)

// Limits bounds how many records of one kind one Session and one project may
// create. Records are immutable and never deleted, so both are lifetime
// bounds. A zero field takes the kind's default.
type Limits struct {
	PerSession int64
	PerProject int64
}

// DefaultLimits bounds TaskClaims: small records, so generous counts.
var DefaultLimits = Limits{PerSession: 512, PerProject: 4096}

// ErrLimit means a new record would exceed a per-session or per-project
// bound. Nothing was created; a retry of an existing record is never refused.
var ErrLimit = errors.New("coordination record limit reached")

func labelDigest(v any) string {
	data, _ := json.Marshal(v)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])[:63]
}

// SessionLabel is the SourceLabel value for one exact Session identity.
func SessionLabel(s coord.SessionIdentity) string { return labelDigest(s) }

// WorkLabelValue is the WorkLabel value: the catalog may rename a repository
// key, but the GitHub repository an issue number belongs to stays the same.
func WorkLabelValue(project, github, taskKey string) string {
	return labelDigest([]string{project, strings.ToLower(github), taskKey})
}

// Effective fills zero fields from defaults.
func (l Limits) Effective(defaults Limits) Limits {
	if l.PerSession <= 0 {
		l.PerSession = defaults.PerSession
	}
	if l.PerProject <= 0 {
		l.PerProject = defaults.PerProject
	}
	return l
}

// CheckRoom counts existing records of listKind (for example "TaskClaimList")
// with uncached, metadata-only, label-selected lists. It returns ErrLimit when
// the source Session or the project has reached its bound, and the read error
// when the count is unconfirmed. Records created before SourceLabel existed
// count toward the project bound only. Concurrent creates may pass the same
// count, so a bound can be exceeded by at most the number of racing creates.
func CheckRoom(ctx context.Context, r client.Reader, listKind, namespace, project string, source coord.SessionIdentity, limits Limits) error {
	gvk := coord.GroupVersion.WithKind(listKind)
	for _, bound := range []struct {
		match map[string]string
		max   int64
	}{
		{map[string]string{ProjectLabel: project, SourceLabel: SessionLabel(source)}, limits.PerSession},
		{map[string]string{ProjectLabel: project}, limits.PerProject},
	} {
		n, err := countAtMost(ctx, r, gvk, namespace, bound.match, bound.max)
		if err != nil {
			return err
		}
		if n >= bound.max {
			return ErrLimit
		}
	}
	return nil
}

func countAtMost(ctx context.Context, r client.Reader, gvk schema.GroupVersionKind, namespace string, match map[string]string, bound int64) (int64, error) {
	if r == nil || bound < 1 {
		return 0, ErrUnavailable
	}
	var n int64
	cursor := ""
	for n < bound {
		page := &metav1.PartialObjectMetadataList{}
		page.SetGroupVersionKind(gvk)
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

var (
	ErrUnavailable = errors.New("task ownership is unconfirmed")
	ErrNotFound    = errors.New("task claim not found")
	keyPattern     = regexp.MustCompile(`^(issue:[1-9][0-9]{0,18}|task:[a-z0-9]([a-z0-9._-]{0,126}[a-z0-9])?)$`)
	digestPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	headPattern    = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)
	uidPattern     = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
)

func ValidIdentity(i coord.TaskIdentity) bool {
	return projectcatalog.ValidName(i.Project) && projectcatalog.ValidName(i.Repository) && keyPattern.MatchString(i.TaskKey)
}

// Name retains all 256 bits, split into DNS labels. Revisions are deliberately
// excluded, so new catalog/rule/source metadata cannot create another owner.
func Name(i coord.TaskIdentity) string {
	data, _ := json.Marshal(i)
	h := sha256.Sum256(data)
	d := hex.EncodeToString(h[:])
	return "task-claim." + d[:32] + "." + d[32:]
}

func ValidName(name string) bool {
	parts := strings.Split(name, ".")
	return len(parts) == 3 && parts[0] == "task-claim" && len(parts[1]) == 32 && len(parts[2]) == 32 && digestPattern.MatchString(parts[1]+parts[2])
}

func ValidSpec(s coord.TaskClaimSpec) bool {
	ns, sa, ok := strings.Cut(s.Actor.ServiceAccount, "/")
	p := s.Provenance
	githubOwner, _, _ := strings.Cut(p.GitHub, "/")
	return ValidIdentity(s.Identity) && len(validation.IsDNS1123Label(s.Owner.Namespace)) == 0 &&
		len(validation.IsDNS1123Subdomain(s.Owner.Name)) == 0 && uidPattern.MatchString(s.Owner.UID) &&
		(s.Actor.Kind == "session" || s.Actor.Kind == "coordinator") && ok && len(validation.IsDNS1123Label(ns)) == 0 &&
		len(validation.IsDNS1123Subdomain(sa)) == 0 && len(validation.IsDNS1123Subdomain(s.Actor.PodName)) == 0 && uidPattern.MatchString(s.Actor.PodUID) &&
		digestPattern.MatchString(p.CatalogRevision) && digestPattern.MatchString(p.RulesRevision) &&
		regexpGitHub.MatchString(p.GitHub) && !strings.Contains(githubOwner, "--") && !strings.HasSuffix(p.GitHub, "/.") && !strings.HasSuffix(p.GitHub, "/..") && !strings.HasSuffix(p.GitHub, ".git") &&
		projectcatalog.ValidBranch(p.DefaultBranch) && p.InitialPinState == "Unknown" &&
		((p.ReportedHead == "" && p.ReportedAt == nil) || (headPattern.MatchString(p.ReportedHead) && p.ReportedAt != nil && !p.ReportedAt.IsZero()))
}

var regexpGitHub = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,37}[a-z0-9])?/[a-z0-9_.-]{1,100}$`)

type KubeStore struct {
	Client    client.Client
	Live      client.Reader
	Namespace string
	Limits    Limits // zero fields take DefaultLimits
}

func (s KubeStore) ready() bool {
	return s.Client != nil && s.Live != nil && len(validation.IsDNS1123Label(s.Namespace)) == 0
}

func claimLabels(spec coord.TaskClaimSpec) map[string]string {
	return map[string]string{ProjectLabel: spec.Identity.Project, SourceLabel: SessionLabel(spec.Owner),
		WorkLabel: WorkLabelValue(spec.Identity.Project, spec.Provenance.GitHub, spec.Identity.TaskKey)}
}

// A claim created before the source and work labels carries the project label
// alone; it stays valid. Any other label set is refused.
func (s KubeStore) valid(c *coord.TaskClaim) bool {
	return c.Namespace == s.Namespace && c.Name == Name(c.Spec.Identity) && ValidSpec(c.Spec) &&
		c.UID != "" && c.ResourceVersion != "" && !c.CreationTimestamp.IsZero() && c.DeletionTimestamp.IsZero() &&
		len(c.OwnerReferences) == 0 && len(c.Finalizers) == 0 && len(c.Annotations) == 0 &&
		(maps.Equal(c.Labels, map[string]string{ProjectLabel: c.Spec.Identity.Project}) || maps.Equal(c.Labels, claimLabels(c.Spec)))
}

func (s KubeStore) Get(ctx context.Context, name string) (*coord.TaskClaim, error) {
	if !s.ready() || !ValidName(name) {
		return nil, ErrUnavailable
	}
	var c coord.TaskClaim
	if err := s.Live.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, &c); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, ErrUnavailable
	}
	if !s.valid(&c) {
		return nil, ErrUnavailable
	}
	return &c, nil
}

// Create confirms every result using Live, including a lost acknowledgement.
// A different owner is returned unchanged, never replaced or adopted.
//
// A new identity is first checked for the same work under another repository
// key (a catalog rename keeps the GitHub repository): that existing claim is
// returned unchanged, so the caller sees its owner. Then the per-owner and
// per-project bounds apply (ErrLimit). Neither check refuses a retry.
func (s KubeStore) Create(ctx context.Context, spec coord.TaskClaimSpec) (*coord.TaskClaim, bool, error) {
	if !s.ready() || !ValidSpec(spec) {
		return nil, false, ErrUnavailable
	}
	if _, err := s.Get(ctx, Name(spec.Identity)); errors.Is(err, ErrNotFound) {
		other, err := s.sameWork(ctx, spec)
		if err != nil {
			return nil, false, ErrUnavailable
		}
		if other != nil {
			return other, false, nil
		}
		if err := CheckRoom(ctx, s.Live, "TaskClaimList", s.Namespace, spec.Identity.Project, spec.Owner, s.Limits.Effective(DefaultLimits)); err != nil {
			if errors.Is(err, ErrLimit) {
				return nil, false, ErrLimit
			}
			return nil, false, ErrUnavailable
		}
	} else if err != nil {
		return nil, false, ErrUnavailable
	}
	c := &coord.TaskClaim{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: Name(spec.Identity), Labels: claimLabels(spec)}, Spec: spec}
	createErr := s.Client.Create(ctx, c)
	observed, err := s.Get(ctx, c.Name)
	if err != nil || observed.Spec.Identity != spec.Identity {
		return nil, false, ErrUnavailable
	}
	if createErr == nil {
		if observed.UID != c.UID || !reflect.DeepEqual(observed.Spec, spec) {
			return nil, false, ErrUnavailable
		}
		return observed, true, nil
	}
	// Only an atomic conflict or an ambiguous transport result may recover a
	// recorded owner. Definite authorization/schema failures are not success.
	if !apierrors.IsAlreadyExists(createErr) && (apierrors.IsInvalid(createErr) || apierrors.IsForbidden(createErr) || apierrors.IsUnauthorized(createErr)) {
		return nil, false, ErrUnavailable
	}
	return observed, false, nil
}

// sameWork finds a claim on the same project, GitHub repository and task key
// under another repository key. Labelled claims are found by their work label.
// Claims from before that label (a set that can no longer grow) are scanned,
// up to the project bound: past it, CheckRoom refuses the create anyway.
func (s KubeStore) sameWork(ctx context.Context, spec coord.TaskClaimSpec) (*coord.TaskClaim, error) {
	work := claimLabels(spec)[WorkLabel]
	same := func(c *coord.TaskClaim) bool {
		return c.Spec.Identity != spec.Identity && c.Spec.Identity.Project == spec.Identity.Project && c.Spec.Identity.TaskKey == spec.Identity.TaskKey &&
			strings.EqualFold(c.Spec.Provenance.GitHub, spec.Provenance.GitHub)
	}
	// One work label holds a claim per repository key the GitHub repository has
	// had, so a page of 8 is plenty. A further page is unread, not absent: it
	// could hold the same work, so the lookup is unconfirmed.
	var list coord.TaskClaimList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Namespace), client.MatchingLabels{ProjectLabel: spec.Identity.Project, WorkLabel: work}, client.Limit(8)); err != nil || len(list.Items) > 8 || list.Continue != "" {
		return nil, ErrUnavailable
	}
	for i := range list.Items {
		c := &list.Items[i]
		if !s.valid(c) || claimLabels(c.Spec)[WorkLabel] != work {
			return nil, ErrUnavailable
		}
		if same(c) {
			return c, nil
		}
	}
	legacy, err := labels.Parse(ProjectLabel + "=" + spec.Identity.Project + ",!" + WorkLabel)
	if err != nil {
		return nil, ErrUnavailable
	}
	bound, scanned, cursor := s.Limits.Effective(DefaultLimits).PerProject, int64(0), ""
	for scanned < bound {
		var page coord.TaskClaimList
		if err := s.Live.List(ctx, &page, client.InNamespace(s.Namespace), client.MatchingLabelsSelector{Selector: legacy}, client.Limit(min(bound-scanned, 100)), client.Continue(cursor)); err != nil || len(page.Continue) > 4096 {
			return nil, ErrUnavailable
		}
		for i := range page.Items {
			c := &page.Items[i]
			if !s.valid(c) || c.Spec.Identity.Project != spec.Identity.Project {
				return nil, ErrUnavailable
			}
			if same(c) {
				return c, nil
			}
		}
		scanned += int64(len(page.Items))
		if page.Continue == "" {
			break
		}
		cursor = page.Continue
	}
	return nil, nil
}

func (s KubeStore) List(ctx context.Context, project string, limit int64, cursor string) ([]coord.TaskClaim, string, error) {
	if !s.ready() || !projectcatalog.ValidName(project) || limit < 1 || limit > 100 || len(cursor) > 4096 {
		return nil, "", ErrUnavailable
	}
	var list coord.TaskClaimList
	if s.Live.List(ctx, &list, client.InNamespace(s.Namespace), client.MatchingLabels{ProjectLabel: project}, client.Limit(limit), client.Continue(cursor)) != nil || int64(len(list.Items)) > limit {
		return nil, "", ErrUnavailable
	}
	for i := range list.Items {
		if !s.valid(&list.Items[i]) || list.Items[i].Spec.Identity.Project != project {
			return nil, "", ErrUnavailable
		}
	}
	return list.Items, list.Continue, nil
}
