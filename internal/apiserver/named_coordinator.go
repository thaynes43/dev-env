package apiserver

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// The launch rules of a named coordinator identity (DESIGN-042 6.4, S4). A
// profile that names dev-env-coordinator-<name> runs its sessions as that account,
// and only these rules decide which sessions those are:
//
//   - only Tom, a client or a top-level workbench session launches one, in one of
//     the shapes NamedCoordinatorLaunchAllowed lists; no ordinary session, named
//     coordinator session, coordinator host, deeper workbench session or portal
//     does, so none launches its own delegate;
//   - only a Claude session runs as one (DESIGN-042 6.4 serves the Claude lead);
//   - a child of a session on such a profile runs on the default profile, which may
//     not name one (templates.Validate), so the identity is never inherited;
//   - at most one session at a time holds each account; the standby never does.
//
// The API records an admitted launch with templates.CoordinatorIdentityLabel, and
// the controller runs a session as the account only with that record.

// NamedCoordinatorLauncher is the caller class that launched a session as a named
// coordinator identity. The API records it on the session
// (NamedCoordinatorLauncherAnnotation), beside the admission label.
type NamedCoordinatorLauncher string

const (
	// LauncherHuman is Tom's ServiceAccount.
	LauncherHuman NamedCoordinatorLauncher = "human"
	// LauncherClient is a trusted client's ServiceAccount, the workbench's
	// client token among them.
	LauncherClient NamedCoordinatorLauncher = "client"
	// LauncherWorkbenchSession is a top-level workbench session's pod: Tom's
	// terminal or the standby the operator keeps for his phone.
	LauncherWorkbenchSession NamedCoordinatorLauncher = "workbench-session"
)

// NamedCoordinatorLauncherAnnotation records the launcher of an admitted
// session. Only the API writes AgentSessions in the session namespace, so it is
// as trustworthy as the admission label.
const NamedCoordinatorLauncherAnnotation = v1alpha1.LabelPrefix + "coordinator-launcher"

// NamedCoordinatorLaunchAllowed is the one launch-eligibility rule of a named
// coordinator identity (DESIGN-042 6.4, S4). Admission (newSession) applies it
// to the caller, and NamedCoordinatorSessionEligible applies it to an existing
// session's record, so the API's admission and a later decision-time check (S2)
// cannot drift. parent is the session's spec.parent and depth its depth label.
// It admits exactly three shapes:
//
//   - launcher human: parent is the human ServiceAccount, at depth 0;
//   - launcher client: parent is a configured client ServiceAccount (not the
//     human's), at depth 0;
//   - launcher workbench-session: parent is a session's name, at depth 1, which is
//     a child of a depth-0 workbench session. A deeper workbench session, a child
//     of Tom's terminal or of the standby, may not launch one.
//
// Everything else is refused, and so is every shape while the policy is off.
func (p Policy) NamedCoordinatorLaunchAllowed(launcher NamedCoordinatorLauncher, parent string, depth int) bool {
	if !p.NamedCoordinatorIdentities || parent == "" {
		return false
	}
	switch launcher {
	case LauncherHuman:
		return depth == 0 && p.Human != "" && parent == p.Human
	case LauncherClient:
		return depth == 0 && parent != p.Human && slices.Contains(p.Clients, parent)
	case LauncherWorkbenchSession:
		return depth == 1 && p.WorkbenchServiceAccount != "" && !strings.Contains(parent, "/") &&
			len(validation.IsDNS1123Subdomain(parent)) == 0
	}
	return false
}

// NamedCoordinatorSessionEligible applies NamedCoordinatorLaunchAllowed to an
// existing session's record: the admission label, the recorded launcher, the
// spec.parent and the depth label. A session without the label or the launcher,
// or whose depth label is missing or not a number, is not eligible. It reads
// nothing else, so it is pure and gives the same answer at admission, at a resume
// and at a decision.
func (p Policy) NamedCoordinatorSessionEligible(s *v1alpha1.AgentSession) bool {
	if s == nil || !templates.IsCoordinatorServiceAccount(s.Labels[templates.CoordinatorIdentityLabel]) {
		return false
	}
	raw, ok := s.Labels[v1alpha1.LabelDepth]
	if !ok {
		return false
	}
	depth, err := strconv.Atoi(raw)
	if err != nil || strconv.Itoa(depth) != raw {
		return false
	}
	return p.NamedCoordinatorLaunchAllowed(NamedCoordinatorLauncher(s.Annotations[NamedCoordinatorLauncherAnnotation]), s.Spec.Parent, depth)
}

// namedCoordinatorLauncher is the launcher class of a caller, or "" for one that
// is none: an ordinary or named coordinator session, a coordinator host, the
// portal.
func (s *Server) namedCoordinatorLauncher(c *caller) NamedCoordinatorLauncher {
	switch {
	case c.kind == kindHuman:
		return LauncherHuman
	case c.kind == kindClient:
		return LauncherClient
	case s.isWorkbenchSession(c):
		return LauncherWorkbenchSession
	}
	return ""
}

// ValidateNamedCoordinatorPolicy rejects a policy whose human or client
// ServiceAccount is a named coordinator identity: such an account is a session's,
// and a client entry would make its session pods clients.
func ValidateNamedCoordinatorPolicy(p Policy) error {
	if !p.NamedCoordinatorIdentities {
		return nil
	}
	shaped := func(ref string) bool {
		ns, name, ok := strings.Cut(ref, "/")
		return ok && ns == p.SessionNamespace && templates.IsCoordinatorServiceAccount(name)
	}
	if shaped(p.Human) || slices.ContainsFunc(p.Clients, shaped) {
		return fmt.Errorf("a named coordinator identity (%s<name> in %s) is a session's account, never a human's or a client's",
			templates.CoordinatorServiceAccountPrefix, p.SessionNamespace)
	}
	return nil
}

// profileIdentity returns the named coordinator identity profile runs its
// sessions as, or "" when it names none or the templates cannot say. An empty
// profile is the default one.
func (s *Server) profileIdentity(ctx context.Context, profile string) string {
	if !s.Policy.NamedCoordinatorIdentities || s.Templates == nil {
		return ""
	}
	t, err := s.Templates(ctx)
	if err != nil {
		return ""
	}
	_, p, err := t.Profile(profile)
	if err != nil || !templates.IsCoordinatorServiceAccount(p.ServiceAccount) {
		return ""
	}
	return p.ServiceAccount
}

// mayLaunchNamedCoordinator applies NamedCoordinatorLaunchAllowed to a caller
// creating a session: its launcher class, the parent it records and the depth
// the new session gets. Never an ordinary or named coordinator session, a
// coordinator host or the portal.
func (s *Server) mayLaunchNamedCoordinator(c *caller) bool {
	return s.Policy.NamedCoordinatorLaunchAllowed(s.namedCoordinatorLauncher(c), c.parent, c.depth)
}

// admitNamedCoordinator applies the launch rule (NamedCoordinatorSessionEligible)
// and the one-holder rule to a session the API labelled. Each create path calls
// it under the create lock, after an idempotent repeat has returned its session,
// and so does a resume.
func (s *Server) admitNamedCoordinator(ctx context.Context, sess *v1alpha1.AgentSession) error {
	account := sess.Labels[templates.CoordinatorIdentityLabel]
	if account == "" {
		return nil
	}
	if !s.Policy.NamedCoordinatorSessionEligible(sess) {
		return forbidden("session %s is not eligible to run as %s/%s: its launcher, parent and depth are not an admitted shape (DESIGN-042 6.4)",
			sess.Name, s.Policy.SessionNamespace, account)
	}
	return s.namedCoordinatorFree(ctx, account, sess.UID)
}

// namedCoordinatorFree refuses a launch or resume while another session holds
// the account: at most one session runs as each named coordinator identity
// (holdsNamedCoordinator says which do). Every read is live, never cached. Each
// create path and the resume call it under the replica's create lock, so on one
// replica a check and its write cannot interleave with another's. Two that race
// through two API replicas can both pass; the delegate decision route (S2) must
// recheck that one session holds the account before it trusts it.
func (s *Server) namedCoordinatorFree(ctx context.Context, account string, self types.UID) error {
	if s.Live == nil {
		return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the sessions holding %s cannot be read", account)
	}
	var list v1alpha1.AgentSessionList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace),
		client.MatchingLabels{templates.CoordinatorIdentityLabel: account}); err != nil {
		return fromKubeError(err, "the sessions holding "+account)
	}
	for i := range list.Items {
		o := &list.Items[i]
		if self != "" && o.UID == self {
			continue
		}
		held, err := s.holdsNamedCoordinator(ctx, o)
		if err != nil {
			return err
		}
		if held {
			return newError(http.StatusConflict, apiv1.CodeConflict,
				"session %s holds %s/%s; one session at a time may (DESIGN-042 6.4)", o.Name, s.Policy.SessionNamespace, account)
		}
	}
	return nil
}

// holdsNamedCoordinator reports whether an admitted session still holds its
// account. An Archived or Failed session does not. A Suspended one does not only
// when its operating mode is Suspended too, and a live read finds no pod it
// controls that has not ended. The mode covers a resume the reconciler has not
// acted on yet: the phase still says Suspended. The pod read covers a pod that
// still runs while its session reports Suspended, as a private-history session
// does while its stop evidence is pending (DESIGN-025). Every other session holds
// it, whatever its phase.
func (s *Server) holdsNamedCoordinator(ctx context.Context, o *v1alpha1.AgentSession) (bool, error) {
	switch o.Status.Phase {
	case v1alpha1.PhaseArchived, v1alpha1.PhaseFailed:
		return false, nil
	case v1alpha1.PhaseSuspended:
		if o.Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
			return true, nil
		}
		var pod corev1.Pod
		err := s.Live.Get(ctx, types.NamespacedName{Namespace: o.Namespace, Name: o.Name}, &pod)
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, fromKubeError(err, "the pod of session "+o.Name)
		}
		ended := pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
		return metav1.IsControlledBy(&pod, o) && !ended, nil
	}
	return true, nil
}
