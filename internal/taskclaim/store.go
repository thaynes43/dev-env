// Package taskclaim implements immutable explicit ownership. Kubernetes Create
// is the only mutation; readers must bypass the manager's informer cache.
package taskclaim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

const ProjectLabel = "coordination.dev-env.haynesops.com/project"

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
}

func (s KubeStore) ready() bool {
	return s.Client != nil && s.Live != nil && len(validation.IsDNS1123Label(s.Namespace)) == 0
}

func (s KubeStore) valid(c *coord.TaskClaim) bool {
	return c.Namespace == s.Namespace && c.Name == Name(c.Spec.Identity) && ValidSpec(c.Spec) &&
		c.UID != "" && c.ResourceVersion != "" && !c.CreationTimestamp.IsZero() && c.DeletionTimestamp.IsZero() &&
		len(c.OwnerReferences) == 0 && len(c.Finalizers) == 0 && len(c.Annotations) == 0 &&
		len(c.Labels) == 1 && c.Labels[ProjectLabel] == c.Spec.Identity.Project
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
func (s KubeStore) Create(ctx context.Context, spec coord.TaskClaimSpec) (*coord.TaskClaim, bool, error) {
	if !s.ready() || !ValidSpec(spec) {
		return nil, false, ErrUnavailable
	}
	c := &coord.TaskClaim{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: Name(spec.Identity),
		Labels: map[string]string{ProjectLabel: spec.Identity.Project}}, Spec: spec}
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
