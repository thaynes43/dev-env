// Package broker is the access broker (DESIGN-001 6.12, D-25, D-61): the second
// mode of the operator binary, `dev-env-operator broker`, deployed as its own
// Deployment with its own ServiceAccount, so the operator that serves agents
// holds none of its rights.
//
// It decides AccessGrants, makes what a grant gives and revokes it. A grant
// passes through these phases, all written to the status subresource with the
// grant's resourceVersion:
//
//   - "" -> Pending on first sight, after the finalizer goes on.
//   - Pending -> Active when a GrantPolicy matches (approvedBy policy/<name>), or
//     when Tom approves (Decide); -> Denied when Tom denies, when the broker's
//     checks fail, when the session ended, or after 30 minutes unanswered
//     (deniedBy timeout); -> Released when the session gives it back.
//   - Active -> Expired at expiresAt, Released on spec.release or when the
//     session ends, Denied when a check fails, Failed when it cannot be made.
//     Each of these revokes first.
//   - An ended grant is deleted 90 days after it ended. A deleted grant is
//     revoked before its finalizer comes off.
//
// Plan 07 step 3 makes kube and break-glass grants: a ServiceAccount named after
// the grant, and a RoleBinding per granted namespace or one ClusterRoleBinding,
// to a role of the catalog. Step 4 installs the token in the session's pod
// (Installer), step 5 makes egress grants, step 6 is the approval page (Decide)
// and Pushover (Notifier).
//
// 5.1 holds here too: the broker keeps nothing outside AccessGrant objects, so
// a restart changes nothing for an active grant. The envtest suite in this
// package runs the broker under exactly the RBAC and admission policy that
// haynes-ops gives it.
package broker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const (
	// Finalizer holds a grant until what it made is revoked.
	Finalizer = v1alpha1.LabelPrefix + "grant-revoke"
	// PendingTimeout is how long a grant waits for an answer before it ends
	// Denied (DESIGN-001 6.12).
	PendingTimeout = 30 * time.Minute
	// Retention is how long an ended grant is kept as the audit record
	// (DESIGN-001 6.12).
	Retention = 90 * 24 * time.Hour
	// MinTokenSeconds is the shortest token TokenRequest issues.
	MinTokenSeconds = 600

	// DeniedByBroker and DeniedByTimeout are status.deniedBy when a check, or
	// the 30-minute timeout, denied a grant.
	DeniedByBroker  = "broker"
	DeniedByTimeout = "timeout"
	// PolicyApprover prefixes status.approvedBy for a standing policy.
	PolicyApprover = "policy/"

	// warnOnlyTokenSeconds is the one expiry the API server extends to a year
	// for a kubelet-style token (--service-account-extend-token-expiration).
	// The broker never asks for it.
	warnOnlyTokenSeconds = 3607
	// maxMessage is status.message's schema limit.
	maxMessage = 1024
)

// Broker reconciles AccessGrants one at a time. Build it with NewManager.
type Broker struct {
	// Client reads AccessGrants, AgentSessions, pods and GrantPolicies from the
	// manager's cache, and writes to the API server. ServiceAccounts and
	// bindings are never cached (NewManager disables them).
	Client client.Client
	// APIReader reads from the API server, past the cache: each grant at the
	// start of its reconcile, ServiceAccounts and bindings by name, and a
	// session the cache does not have yet.
	APIReader client.Reader
	// SessionNamespace holds the sessions, their pods, the grants and the grant
	// ServiceAccounts (D-54).
	SessionNamespace string
	// PolicyNamespace holds the GrantPolicies and the broker's Lease.
	PolicyNamespace string
	// Clock is the broker's time; tests step a fake one.
	Clock clock.PassiveClock
	// Notifier tells Tom a grant waits for him; nil tells no one.
	Notifier Notifier
	// Installer puts a grant's token in the session's pod (plan 07 step 4);
	// nil installs nothing and mints no token.
	Installer Installer
	// Recorder emits Events on the grant; nil emits none.
	Recorder events.EventRecorder

	// reconciled, when set, hears of every finished reconcile (tests).
	reconciled func(types.NamespacedName)
}

// permanent marks an error that retrying cannot fix: the grant fails.
type permanent struct{ error }

func (p permanent) Unwrap() error { return p.error }

func (b *Broker) now() metav1.Time {
	return metav1.NewTime(b.Clock.Now().Truncate(time.Second))
}

// Reconcile moves one grant along (package doc). It reads the grant from the API
// server: a cached copy that has not yet seen the broker's own last write could
// make an identity for a grant that has already ended.
func (b *Broker) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if b.reconciled != nil {
		defer b.reconciled(req.NamespacedName)
	}
	var g v1alpha1.AccessGrant
	if err := b.APIReader.Get(ctx, req.NamespacedName, &g); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !g.DeletionTimestamp.IsZero() {
		return b.finalize(ctx, &g)
	}
	if !controllerutil.ContainsFinalizer(&g, Finalizer) {
		orig := g.DeepCopy()
		controllerutil.AddFinalizer(&g, Finalizer)
		if err := b.Client.Patch(ctx, &g, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
			return retryOn(err, "add the finalizer")
		}
	}

	now := b.now()
	if g.Status.Phase.Ended() {
		return b.retain(ctx, &g, now)
	}
	if err := check(&g, b.SessionNamespace, b.PolicyNamespace); err != nil {
		return b.end(ctx, &g, v1alpha1.GrantDenied, DeniedByBroker, err.Error(), now)
	}
	sess, err := b.session(ctx, g.Spec.Requester.Session)
	if err != nil {
		return ctrl.Result{}, err
	}
	if sess == nil {
		if g.Status.Phase == v1alpha1.GrantActive {
			return b.end(ctx, &g, v1alpha1.GrantReleased, "", "the session ended", now)
		}
		return b.end(ctx, &g, v1alpha1.GrantDenied, DeniedByBroker, "the session ended", now)
	}

	switch g.Status.Phase {
	case "":
		g.Status.Phase = v1alpha1.GrantPending
		_, err := b.writeStatus(ctx, &g)
		return ctrl.Result{}, err
	case v1alpha1.GrantPending:
		return b.pending(ctx, &g, now)
	case v1alpha1.GrantActive:
		return b.active(ctx, &g, sess, now)
	}
	return ctrl.Result{}, nil
}

// pending answers a grant that waits: released, timed out, approved by a
// policy, or still waiting for Tom, who is told once.
func (b *Broker) pending(ctx context.Context, g *v1alpha1.AccessGrant, now metav1.Time) (ctrl.Result, error) {
	if g.Spec.Release {
		return b.end(ctx, g, v1alpha1.GrantReleased, "", "released before an answer", now)
	}
	deadline := g.CreationTimestamp.Add(PendingTimeout)
	if !now.Time.Before(deadline) {
		return b.end(ctx, g, v1alpha1.GrantDenied, DeniedByTimeout, "no answer in 30 minutes", now)
	}

	var policies v1alpha1.GrantPolicyList
	if err := b.Client.List(ctx, &policies, client.InNamespace(b.PolicyNamespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("list the grant policies: %w", err)
	}
	if p := Match(g, policies.Items); p != nil {
		approve(g, PolicyApprover+p.Name, g.Spec.TTL.Duration, now)
		written, err := b.writeStatus(ctx, g)
		if written {
			b.decided(ctx, g, "approved by a standing policy")
		}
		return ctrl.Result{}, err
	}

	wait := ctrl.Result{RequeueAfter: deadline.Sub(now.Time)}
	if g.Status.NotifiedAt != nil || b.Notifier == nil {
		return wait, nil
	}
	// At least once: a lost status write sends the message again, which is
	// better than a request Tom never hears of.
	if err := b.Notifier.NotifyPending(ctx, g); err != nil {
		b.event(g, corev1.EventTypeWarning, "Notify", "NotifyFailed", "could not tell Tom: "+err.Error())
		return ctrl.Result{}, fmt.Errorf("notify: %w", err)
	}
	g.Status.NotifiedAt = &now
	if written, err := b.writeStatus(ctx, g); !written || err != nil {
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).Info("grant waits for Tom", grantFields(g)...)
	b.event(g, corev1.EventTypeNormal, "Notify", "WaitsForTom", "no standing policy matches; Tom was asked")
	return wait, nil
}

// approve records an approval: Active from now for ttl.
func approve(g *v1alpha1.AccessGrant, by string, ttl time.Duration, now metav1.Time) {
	g.Status.Phase = v1alpha1.GrantActive
	g.Status.ApprovedBy = by
	g.Status.ApprovedAt = &now
	g.Status.ApprovedTTL = &metav1.Duration{Duration: ttl}
	g.Status.ExpiresAt = ptr.To(metav1.NewTime(now.Add(ttl)))
	g.Status.Message = ""
}

// active ends an active grant that expired or was released, and otherwise makes
// sure what it grants exists, installs it in a new session pod, and looks again
// at expiresAt.
func (b *Broker) active(ctx context.Context, g *v1alpha1.AccessGrant, sess *v1alpha1.AgentSession, now metav1.Time) (ctrl.Result, error) {
	if g.Status.ExpiresAt == nil {
		return b.end(ctx, g, v1alpha1.GrantFailed, "", "active with no expiresAt", now)
	}
	expires := g.Status.ExpiresAt.Time
	if !now.Time.Before(expires) {
		return b.end(ctx, g, v1alpha1.GrantExpired, "", "", now)
	}
	if g.Spec.Release {
		return b.end(ctx, g, v1alpha1.GrantReleased, "", "released by its session", now)
	}

	made, err := b.ensure(ctx, g)
	if err != nil {
		if p := (permanent{}); errors.As(err, &p) {
			return b.end(ctx, g, v1alpha1.GrantFailed, "", "could not be made: "+err.Error(), now)
		}
		return ctrl.Result{}, err
	}
	if len(made) > 0 {
		log.FromContext(ctx).Info("made the grant", append(grantFields(g), "made", made)...)
		b.event(g, corev1.EventTypeNormal, "Make", "Made", "made "+strings.Join(made, ", "))
	}
	res := ctrl.Result{RequeueAfter: expires.Sub(now.Time)}
	changed := g.Status.ServiceAccount != g.Name
	if b.Installer != nil {
		if pod := b.sessionPod(ctx, sess, true); pod != nil {
			var installed bool
			res, installed, err = b.install(ctx, g, pod, now)
			if err != nil || g.Status.Phase != v1alpha1.GrantActive {
				return res, err
			}
			if !installed && g.Status.InstalledPodUID != string(pod.UID) {
				return res, nil
			}
			changed = changed || installed
		} else {
			res.RequeueAfter = min(installRetry, res.RequeueAfter)
		}
	}
	// The metadata patch in install reads back the server's status. Set the
	// identity afterwards so that patch cannot discard it.
	g.Status.ServiceAccount = g.Name
	if changed {
		if _, err := b.writeStatus(ctx, g); err != nil {
			return ctrl.Result{}, err
		}
	}
	return res, nil
}

// end gives a grant its final phase. An active grant is revoked first, so the
// record never says ended while anything of it stands.
func (b *Broker) end(ctx context.Context, g *v1alpha1.AccessGrant, phase v1alpha1.GrantPhase, deniedBy, msg string, now metav1.Time) (ctrl.Result, error) {
	if g.Status.Phase == v1alpha1.GrantActive {
		if err := b.revoke(ctx, g, string(phase)); err != nil {
			return ctrl.Result{}, err
		}
	}
	g.Status.Phase = phase
	if deniedBy != "" {
		g.Status.DeniedBy = deniedBy
	}
	g.Status.Message = truncate(msg)
	g.Status.EndedAt = &now
	written, err := b.writeStatus(ctx, g)
	if written {
		b.decided(ctx, g, msg)
		return ctrl.Result{RequeueAfter: Retention}, nil
	}
	return ctrl.Result{}, err
}

// retain deletes an ended grant once it has been kept 90 days.
func (b *Broker) retain(ctx context.Context, g *v1alpha1.AccessGrant, now metav1.Time) (ctrl.Result, error) {
	ended := g.CreationTimestamp
	if g.Status.EndedAt != nil {
		ended = *g.Status.EndedAt
	}
	deadline := ended.Add(Retention)
	if now.Time.Before(deadline) {
		return ctrl.Result{RequeueAfter: deadline.Sub(now.Time)}, nil
	}
	if err := b.Client.Delete(ctx, g, client.Preconditions{UID: &g.UID}); err != nil {
		return retryOn(err, "delete the grant after its retention")
	}
	log.FromContext(ctx).Info("deleted the grant after 90 days", grantFields(g)...)
	return ctrl.Result{}, nil
}

// finalize revokes a deleted grant and lets it go.
func (b *Broker) finalize(ctx context.Context, g *v1alpha1.AccessGrant) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(g, Finalizer) {
		return ctrl.Result{}, nil
	}
	if err := b.revoke(ctx, g, "deleted"); err != nil {
		return ctrl.Result{}, err
	}
	orig := g.DeepCopy()
	controllerutil.RemoveFinalizer(g, Finalizer)
	if err := b.Client.Patch(ctx, g, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
		return retryOn(err, "lift the finalizer")
	}
	return ctrl.Result{}, nil
}

// writeStatus writes the grant's status with its resourceVersion. A conflict
// or a missing grant is not an error: the newer version's event reconciles
// again. written reports whether this write landed.
func (b *Broker) writeStatus(ctx context.Context, g *v1alpha1.AccessGrant) (written bool, err error) {
	if err := b.Client.Status().Update(ctx, g); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("write the grant's status: %w", err)
	}
	return true, nil
}

// retryOn turns a conflict or a missing object into a quick retry.
func retryOn(err error, what string) (ctrl.Result, error) {
	if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	return ctrl.Result{}, fmt.Errorf("%s: %w", what, err)
}

// session returns the requesting session, or nil when it is gone or being
// deleted. A session the cache does not have is read from the API server
// before the grant is ended for it: a session created a moment ago may not
// have reached the cache.
func (b *Broker) session(ctx context.Context, name string) (*v1alpha1.AgentSession, error) {
	s, err := b.lookupSession(ctx, name)
	if err != nil || s == nil || !s.DeletionTimestamp.IsZero() {
		return nil, err
	}
	return s, nil
}

// lookupSession returns the session, being deleted or not, or nil when it is
// gone.
func (b *Broker) lookupSession(ctx context.Context, name string) (*v1alpha1.AgentSession, error) {
	key := types.NamespacedName{Namespace: b.SessionNamespace, Name: name}
	var s v1alpha1.AgentSession
	err := b.Client.Get(ctx, key, &s)
	if apierrors.IsNotFound(err) {
		err = b.APIReader.Get(ctx, key, &s)
	}
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read session %s: %w", name, err)
	}
	return &s, nil
}

// sessionPod returns the session's own pod from the API server: named after the
// session, controlled by it, not a hold pod (D-55), not being deleted, and
// Running when running is true. Otherwise nil.
func (b *Broker) sessionPod(ctx context.Context, s *v1alpha1.AgentSession, running bool) *corev1.Pod {
	var pod corev1.Pod
	if err := b.APIReader.Get(ctx, types.NamespacedName{Namespace: b.SessionNamespace, Name: s.Name}, &pod); err != nil {
		return nil
	}
	ref := metav1.GetControllerOf(&pod)
	switch {
	case ref == nil || ref.UID != s.UID:
		return nil
	case pod.Labels[v1alpha1.LabelSession] != s.Name || pod.Labels[v1alpha1.LabelHold] == "true":
		return nil
	case !pod.DeletionTimestamp.IsZero():
		return nil
	case running && pod.Status.Phase != corev1.PodRunning:
		return nil
	}
	return &pod
}

// mint asks the API server for a token of the grant's ServiceAccount that
// lasts until the grant expires, and at least TokenRequest's 10 minutes. The
// token is not bound to the session's pod: the API server binds a token only
// to a pod that runs as the same ServiceAccount, and session pods run as
// dev-env-agent. Deleting the ServiceAccount at revoke is what ends every token
// of the grant at once: the API server checks the ServiceAccount's UID on each
// request (D-61).
func (b *Broker) mint(ctx context.Context, g *v1alpha1.AccessGrant, now metav1.Time) (string, time.Time, error) {
	secs := tokenSeconds(g.Status.ExpiresAt.Sub(now.Time))
	tr := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &secs}}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: b.SessionNamespace, Name: g.Name}}
	if err := b.Client.SubResource("token").Create(ctx, sa, tr); err != nil {
		return "", time.Time{}, fmt.Errorf("mint a token for %s: %w", g.Name, err)
	}
	return tr.Status.Token, tr.Status.ExpirationTimestamp.Time, nil
}

// tokenSeconds is a token's lifetime for the time a grant has left: rounded up
// to whole seconds, at least MinTokenSeconds, and never warnOnlyTokenSeconds.
func tokenSeconds(left time.Duration) int64 {
	secs := int64(math.Ceil(left.Seconds()))
	if secs < MinTokenSeconds {
		secs = MinTokenSeconds
	}
	if secs == warnOnlyTokenSeconds {
		secs++
	}
	return secs
}

func earliest(a, b time.Time) time.Time {
	if a.IsZero() || b.Before(a) {
		return b
	}
	return a
}

// decided logs a decision and records it as an Event on the grant.
func (b *Broker) decided(ctx context.Context, g *v1alpha1.AccessGrant, msg string) {
	logDecision(log.FromContext(ctx), g, msg)
	typ := corev1.EventTypeNormal
	if g.Status.Phase == v1alpha1.GrantDenied || g.Status.Phase == v1alpha1.GrantFailed {
		typ = corev1.EventTypeWarning
	}
	note := string(g.Status.Phase)
	switch {
	case g.Status.ApprovedBy != "" && g.Status.Phase == v1alpha1.GrantActive:
		note += " by " + g.Status.ApprovedBy + " until " + g.Status.ExpiresAt.UTC().Format(time.RFC3339)
	case g.Status.DeniedBy != "":
		note += " by " + g.Status.DeniedBy
	}
	if msg != "" {
		note += ": " + msg
	}
	b.event(g, typ, "Decide", string(g.Status.Phase), note)
}

func (b *Broker) event(g *v1alpha1.AccessGrant, typ, action, reason, note string) {
	if b.Recorder == nil {
		return
	}
	b.Recorder.Eventf(g, nil, typ, reason, action, "%s", truncate(note))
}

// grantFields are the log fields of every decision and revoke line. Never a
// token.
func grantFields(g *v1alpha1.AccessGrant) []any {
	f := []any{"grant", g.Name, "session", g.Spec.Requester.Session, "type", g.Spec.Type, "phase", g.Status.Phase}
	if k := g.Spec.Kube; k != nil {
		f = append(f, "role", k.Role, "namespaces", k.Namespaces)
	}
	if g.Status.ApprovedBy != "" {
		f = append(f, "approvedBy", g.Status.ApprovedBy)
	}
	if g.Status.DeniedBy != "" {
		f = append(f, "deniedBy", g.Status.DeniedBy)
	}
	if g.Status.ExpiresAt != nil {
		f = append(f, "expiresAt", g.Status.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return f
}

func truncate(s string) string {
	if len(s) <= maxMessage {
		return s
	}
	return s[:maxMessage-3] + "..."
}

// grantLabels go on everything the broker makes for a grant.
func grantLabels(g *v1alpha1.AccessGrant) map[string]string {
	return map[string]string{
		v1alpha1.LabelGrant:     g.Name,
		v1alpha1.LabelSession:   g.Spec.Requester.Session,
		v1alpha1.LabelManagedBy: ManagedBy,
	}
}

// ManagedBy is app.kubernetes.io/managed-by on what the broker makes.
const ManagedBy = "dev-env-broker"

// subject is the grant's ServiceAccount as a binding subject.
func (b *Broker) subject(g *v1alpha1.AccessGrant) rbacv1.Subject {
	return rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: b.SessionNamespace, Name: g.Name}
}
