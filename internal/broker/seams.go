package broker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The seams later steps of plan 07 plug into (D-61): Decide for the approval
// page (step 6), Notifier for Pushover (step 6), Installer for the token in the
// session's pod (step 4).

// Notifier tells Tom that a grant waits for him. The broker calls it once per
// pending grant that no policy approved, then records status.notifiedAt. An
// error is retried with backoff.
type Notifier interface {
	NotifyPending(ctx context.Context, g *v1alpha1.AccessGrant) error
}

// LogNotifier only logs. It stands in until Pushover (plan 07 step 6).
type LogNotifier struct{ Log logr.Logger }

// NotifyPending implements Notifier.
func (n LogNotifier) NotifyPending(_ context.Context, g *v1alpha1.AccessGrant) error {
	n.Log.Info("a grant waits for Tom; no notifier is configured", grantFields(g)...)
	return nil
}

// Installer puts a kube grant's token in the session's pod and takes it out
// (plan 07 step 4). Install is called with a freshly minted token whenever the
// session's Running pod is not the one in status.installedPodUID, or before a
// shortened TokenRequest expires; Remove at revoke, best effort (D-63).
type Installer interface {
	Install(ctx context.Context, pod *corev1.Pod, g *v1alpha1.AccessGrant, token string, expires time.Time) error
	Remove(ctx context.Context, pod *corev1.Pod, g *v1alpha1.AccessGrant) error
}

// Decision is Tom's answer to a pending grant, from the approval page.
type Decision struct {
	// Approve approves the grant; false denies it.
	Approve bool
	// By is who decided: "authentik/<username>". A policy approves only
	// through the broker's own match, never through Decide.
	By string
	// TTL approves for less time than the grant asked: zero means spec.ttl.
	// Approvals only.
	TTL time.Duration
	// Reason is why it was denied, shown in status.message. Denials only.
	Reason string
}

// Errors Decide returns, for the approval page to show.
var (
	// ErrNotPending: the grant was already answered, timed out, released or
	// deleted.
	ErrNotPending = errors.New("the grant is not pending")
	// ErrRefused: the decision itself is not allowed.
	ErrRefused = errors.New("decision refused")
)

// Decide records Tom's decision on a pending grant (D-26). It refuses a grant
// that is not pending, a TTL longer than the grant asked or shorter than 10
// minutes, an approver that is a policy or empty, and an approval of a grant
// that fails the broker's checks. It writes status with the grant's
// resourceVersion; the reconciler then makes or ends the grant. Any replica
// may call it.
func (b *Broker) Decide(ctx context.Context, grantName string, d Decision) error {
	key := types.NamespacedName{Namespace: b.SessionNamespace, Name: grantName}
	for range 5 {
		var g v1alpha1.AccessGrant
		if err := b.APIReader.Get(ctx, key, &g); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("%w: grant %s does not exist", ErrNotPending, grantName)
			}
			return err
		}
		now := b.now()
		ttl, err := b.decisionAllowed(&g, d, now.Time)
		if err != nil {
			return err
		}
		msg := ""
		if d.Approve {
			approve(&g, d.By, ttl, now)
		} else {
			g.Status.Phase = v1alpha1.GrantDenied
			g.Status.DeniedBy = d.By
			msg = d.Reason
			if msg == "" {
				msg = "denied by " + d.By
			}
			g.Status.Message = truncate(msg)
			g.Status.EndedAt = &now
		}
		err = b.Client.Status().Update(ctx, &g)
		if err == nil {
			b.decided(log.IntoContext(ctx, b.logger()), &g, msg)
			return nil
		}
		if !apierrors.IsConflict(err) {
			return fmt.Errorf("record the decision: %w", err)
		}
	}
	return fmt.Errorf("record the decision: the grant kept changing")
}

// decisionAllowed checks a decision against the grant and returns the TTL an
// approval gives.
func (b *Broker) decisionAllowed(g *v1alpha1.AccessGrant, d Decision, now time.Time) (time.Duration, error) {
	switch {
	case !g.DeletionTimestamp.IsZero():
		return 0, fmt.Errorf("%w: grant %s is being deleted", ErrNotPending, g.Name)
	case g.Status.Phase != "" && g.Status.Phase != v1alpha1.GrantPending:
		return 0, fmt.Errorf("%w: grant %s is %s", ErrNotPending, g.Name, g.Status.Phase)
	case g.Spec.Release:
		return 0, fmt.Errorf("%w: grant %s was released", ErrNotPending, g.Name)
	case !now.Before(g.CreationTimestamp.Add(PendingTimeout)):
		return 0, fmt.Errorf("%w: grant %s waited more than %s", ErrNotPending, g.Name, PendingTimeout)
	case d.By == "":
		return 0, fmt.Errorf("%w: no approver", ErrRefused)
	case strings.HasPrefix(d.By, PolicyApprover):
		return 0, fmt.Errorf("%w: %s is a policy; Tom decides on the page, and break-glass only ever by him (D-27)", ErrRefused, d.By)
	}
	if !d.Approve {
		return 0, nil
	}
	if err := check(g, b.SessionNamespace, b.PolicyNamespace); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrRefused, err)
	}
	ttl := d.TTL
	if ttl == 0 {
		ttl = g.Spec.TTL.Duration
	}
	switch {
	case ttl > g.Spec.TTL.Duration:
		return 0, fmt.Errorf("%w: %s is longer than the %s the grant asked for", ErrRefused, ttl, g.Spec.TTL.Duration)
	case ttl < MinTTL:
		return 0, fmt.Errorf("%w: %s is shorter than the shortest grant, %s", ErrRefused, ttl, MinTTL)
	}
	return ttl, nil
}

func (b *Broker) logger() logr.Logger {
	return log.Log.WithName("broker")
}

// logDecision is the one log line of a decision.
func logDecision(l logr.Logger, g *v1alpha1.AccessGrant, msg string) {
	f := grantFields(g)
	if msg != "" {
		f = append(f, "message", msg)
	}
	l.Info("decided the grant", f...)
}
