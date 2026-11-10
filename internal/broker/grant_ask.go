package broker

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/asks"
	"github.com/thaynes43/dev-env/internal/grantattest"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

const (
	// grantAskOriginBroker is the ask-origin label value the broker sets
	// on the create. A session create cannot set it.
	grantAskOriginBroker = asks.BrokerOrigin
	// grantAskRecheck is how often a grant selected for Tom is reconciled
	// while its Ask is open. An answer is also enqueued from the Ask watch.
	grantAskRecheck = 30 * time.Second
	// grantAskUnreadLimit is how long a routed grant waits when its Ask
	// cannot be read. Past that, the grant is denied. It is not given
	// another path.
	grantAskUnreadLimit = 2 * time.Minute
	// grantAskMsgRemake is the denial when the stored scope text is not the
	// text this build renders for the grant.
	grantAskMsgRemake = "the request must be made again"
	// grantAskMsgGone, grantAskMsgForeign and grantAskMsgUnread are the
	// denials for a routed grant whose Ask is missing, not this grant's, or
	// unreadable.
	grantAskMsgGone    = "the grant ask is gone"
	grantAskMsgForeign = "the grant ask is not this grant's"
	grantAskMsgUnread  = "the grant ask could not be read"
	// grantAskQuestionFallback is the question when spec.reason fails the
	// Ask text rules. The grant is still opened.
	grantAskQuestionFallback = "The session asked for access. The reason could not be shown on the board."
	// grantAskWorkPackageMax is the Ask field's maximum OpenProject id.
	grantAskWorkPackageMax = 2_000_000_000
)

var (
	// errGrantAskLater means this pass opens nothing and does not deny.
	// The next reconcile tries again.
	errGrantAskLater = errors.New("grant ask will be retried")
	// errGrantAskStop means this pass must not approve and must not deny.
	errGrantAskStop = errors.New("grant ask stops without a decision")
	// errGrantAskTooLarge means the complete scope does not fit an Ask.
	// The broker opens nothing and the grant keeps the 30-minute deny.
	errGrantAskTooLarge = errors.New("grant ask scope does not fit")
	// errGrantAskForeign means an Ask already sits at the broker's name
	// and it is not this grant's. It is not labelled, not read, and the
	// route is not set.
	errGrantAskForeign = errors.New("grant ask was not opened by the broker")
	// errGrantAskRouteReread means the route write conflicted or the grant
	// changed. The caller reads the grant again and does not create an Ask.
	errGrantAskRouteReread = errors.New("grant ask route must be read again")
	// errGrantAskRouteForeign means the route is set and the object there
	// is not this grant's Ask. The caller denies. It does not open another.
	errGrantAskRouteForeign = errors.New("grant ask at the route is not this grant's")

	grantAskDigestPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	grantAskTTLPattern      = regexp.MustCompile(`^([0-9]+(h|m|s))+$`)
	grantAskUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@+-]{0,62}$`)
)

const (
	grantAskWait = iota
	grantAskStop
	grantAskDenyScope
	grantAskDenyDetail
	grantAskDenyUsername
	grantAskDenyUnusable
	grantAskDenyWithdrawn
	grantAskApply
)

const (
	grantAskNone = iota
	grantAskUnread
	grantAskForeign
	grantAskBroker
)

// grantAskCreator is the part of asks.KubeStore the broker calls. A nil
// grantAsks field uses the broker's own client.
type grantAskCreator interface {
	Create(ctx context.Context, req asks.Requester, in apiv1.AskCreateRequest, now time.Time) (apiv1.Ask, bool, error)
}

func (b *Broker) askCreator() grantAskCreator {
	if b.grantAsks != nil {
		return b.grantAsks
	}
	return b.askStore()
}

func (b *Broker) askStore() asks.KubeStore {
	return asks.KubeStore{Client: b.Client, Live: b.APIReader, Namespace: asks.Namespace}
}

// applyLoadedGrantAnswer acts on an Ask this pass already loaded. done is
// false while the Ask is still open, so the caller notifies Tom and waits.
// A pre-check failure does not call Decide.
func (b *Broker) applyLoadedGrantAnswer(ctx context.Context, g *v1alpha1.AccessGrant, ask *coord.Ask, now metav1.Time) (bool, reconcile.Result, error) {
	switch grantAskAction(g, ask) {
	case grantAskWait:
		return false, reconcile.Result{}, nil
	case grantAskStop:
		return true, reconcile.Result{RequeueAfter: grantAskRecheck}, nil
	case grantAskDenyScope:
		res, err := b.denyScopeMismatch(ctx, g, now)
		return true, res, err
	case grantAskDenyDetail:
		res, err := b.denyPendingGrant(ctx, g, grantAskMsgRemake, now)
		return true, res, err
	case grantAskDenyUsername:
		res, err := b.denyPendingGrant(ctx, g, "the answer has no usable username", now)
		return true, res, err
	case grantAskDenyUnusable:
		res, err := b.denyPendingGrant(ctx, g, "the answer could not be applied", now)
		return true, res, err
	case grantAskDenyWithdrawn:
		res, err := b.denyPendingGrant(ctx, g, "the grant ask was withdrawn", now)
		return true, res, err
	case grantAskApply:
		res, err := b.decideGrantAsk(ctx, g, ask, now)
		return true, res, err
	default:
		return true, reconcile.Result{RequeueAfter: grantAskRecheck}, nil
	}
}

// grantAskDecides reports whether this decision is Tom's grant-Ask answer.
// A policy, a delegate and the maintenance human keep the 30-minute age
// check and do not take the Ask re-check.
func (b *Broker) grantAskDecides(d Decision) bool {
	if !b.EnableGrantAsks || d.By == "" || strings.HasPrefix(d.By, PolicyApprover) || strings.HasPrefix(d.By, DelegateApprover) {
		return false
	}
	return d.By != b.maintenanceHumanApprover()
}

// recheckGrantAsk re-reads the Ask inside Decide. A read error is returned
// as itself so the caller retries. Anything else that does not bind is a
// refusal. A By with no matching answer does not pass.
func (b *Broker) recheckGrantAsk(ctx context.Context, g *v1alpha1.AccessGrant, d Decision) error {
	if d.TTL != 0 {
		return fmt.Errorf("%w: a grant ask approves for the ttl asked", ErrRefused)
	}
	ask, err := b.loadDecisionAsk(ctx, g)
	if errors.Is(err, errGrantAskLater) || apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: no matching grant ask", ErrRefused)
	}
	if err != nil {
		return err
	}
	if !grantAskIdentityOK(g, ask) || !grantAskScopeOK(g, ask) {
		return fmt.Errorf("%w: the grant ask does not bind this grant", ErrRefused)
	}
	if !grantAskDetailOK(g, ask) {
		return fmt.Errorf("%w: %s", ErrRefused, grantAskMsgRemake)
	}
	if ask.Status.State != string(apiv1.AskStateAnswered) || ask.Status.Answer == nil || ask.Status.Answer.Approved == nil {
		return fmt.Errorf("%w: the grant ask has no usable answer", ErrRefused)
	}
	user := ask.Status.Answer.AnsweredBy.Username
	if !grantAskUsernameOK(user) || d.By != "authentik/"+user || d.Approve != *ask.Status.Answer.Approved {
		return fmt.Errorf("%w: the decision does not match the grant ask", ErrRefused)
	}
	return nil
}

func (b *Broker) decideGrantAsk(ctx context.Context, g *v1alpha1.AccessGrant, ask *coord.Ask, now metav1.Time) (reconcile.Result, error) {
	if ask.Status.Answer == nil || ask.Status.Answer.Approved == nil || !grantAskUsernameOK(ask.Status.Answer.AnsweredBy.Username) {
		return b.denyPendingGrant(ctx, g, "the answer could not be applied", now)
	}
	approved := *ask.Status.Answer.Approved
	d := Decision{Approve: approved, By: "authentik/" + ask.Status.Answer.AnsweredBy.Username}
	if !approved {
		d.Reason = ask.Status.Answer.Text
	}
	err := b.decideGrant(ctx, g.Name, d)
	switch {
	case err == nil, errors.Is(err, ErrNotPending):
		return reconcile.Result{}, nil
	case errors.Is(err, ErrRefused):
		return b.denyPendingGrant(ctx, g, "the answer could not be applied", now)
	default:
		log.FromContext(ctx).Error(err, "grant ask answer was not recorded", "grant", g.Name)
		return reconcile.Result{RequeueAfter: time.Second}, nil
	}
}

func (b *Broker) decideGrant(ctx context.Context, name string, d Decision) error {
	if b.decideFn != nil {
		return b.decideFn(ctx, name, d)
	}
	return b.Decide(ctx, name, d)
}

func (b *Broker) denyScopeMismatch(ctx context.Context, g *v1alpha1.AccessGrant, now metav1.Time) (reconcile.Result, error) {
	fresh, err := b.readGrant(ctx, g.Name)
	if apierrors.IsNotFound(err) {
		return reconcile.Result{}, nil
	}
	if err != nil {
		log.FromContext(ctx).Error(err, "grant was not read before a deny", "grant", g.Name)
		return reconcile.Result{RequeueAfter: time.Second}, nil
	}
	if !grantStillPending(g, fresh) {
		return reconcile.Result{}, nil
	}
	ask, err := b.loadDecisionAsk(ctx, fresh)
	if apierrors.IsNotFound(err) || errors.Is(err, errGrantAskLater) {
		return reconcile.Result{RequeueAfter: grantAskRecheck}, nil
	}
	if err != nil {
		log.FromContext(ctx).Error(err, "grant ask was not read before a deny", "grant", g.Name)
		return reconcile.Result{RequeueAfter: time.Second}, nil
	}
	if !grantAskIdentityOK(fresh, ask) {
		return reconcile.Result{RequeueAfter: grantAskRecheck}, nil
	}
	if grantAskScopeOK(fresh, ask) {
		return reconcile.Result{RequeueAfter: time.Second}, nil
	}
	return b.end(ctx, fresh, v1alpha1.GrantDenied, DeniedByBroker, "the scope no longer matches", now)
}

func (b *Broker) denyPendingGrant(ctx context.Context, g *v1alpha1.AccessGrant, msg string, now metav1.Time) (reconcile.Result, error) {
	fresh, err := b.readGrant(ctx, g.Name)
	if apierrors.IsNotFound(err) {
		return reconcile.Result{}, nil
	}
	if err != nil {
		log.FromContext(ctx).Error(err, "grant was not read before a deny", "grant", g.Name)
		return reconcile.Result{RequeueAfter: time.Second}, nil
	}
	if !grantStillPending(g, fresh) {
		return reconcile.Result{}, nil
	}
	return b.end(ctx, fresh, v1alpha1.GrantDenied, DeniedByBroker, msg, now)
}

func grantStillPending(seen, fresh *v1alpha1.AccessGrant) bool {
	if fresh == nil || seen == nil || fresh.UID != seen.UID || !fresh.DeletionTimestamp.IsZero() || fresh.Spec.Release {
		return false
	}
	return fresh.Status.Phase == "" || fresh.Status.Phase == v1alpha1.GrantPending
}

func (b *Broker) readGrant(ctx context.Context, name string) (*v1alpha1.AccessGrant, error) {
	if b.APIReader == nil {
		return nil, errGrantAskLater
	}
	var fresh v1alpha1.AccessGrant
	if err := b.APIReader.Get(ctx, types.NamespacedName{Namespace: b.SessionNamespace, Name: name}, &fresh); err != nil {
		return nil, err
	}
	return &fresh, nil
}

func grantAskAction(g *v1alpha1.AccessGrant, ask *coord.Ask) int {
	if !grantAskIdentityOK(g, ask) {
		return grantAskStop
	}
	// A withdrawn broker Ask is a cancelled request. It is never left
	// approvable, and it is not treated as still waiting.
	if ask.Status.State == string(apiv1.AskStateWithdrawn) {
		return grantAskDenyWithdrawn
	}
	if !grantAskScopeOK(g, ask) {
		return grantAskDenyScope
	}
	if !grantAskDetailOK(g, ask) {
		return grantAskDenyDetail
	}
	if ask.Status.State != string(apiv1.AskStateAnswered) || ask.Status.Answer == nil {
		return grantAskWait
	}
	if !grantAskUsernameOK(ask.Status.Answer.AnsweredBy.Username) {
		return grantAskDenyUsername
	}
	if ask.Status.Answer.Approved == nil {
		return grantAskDenyUnusable
	}
	return grantAskApply
}

func grantAskIdentityOK(g *v1alpha1.AccessGrant, ask *coord.Ask) bool {
	if ask == nil || ask.Labels[asks.OriginLabel] != grantAskOriginBroker {
		return false
	}
	if ask.Spec.Type != string(apiv1.AskTypeGrant) || ask.Spec.Grant == nil {
		return false
	}
	id, ok := grantAskRequestID(g.UID)
	if !ok || ask.Spec.RequestID != id {
		return false
	}
	if ask.Spec.Context.SessionUID != string(g.Spec.Requester.SessionUID) || ask.Spec.Context.SessionName != g.Spec.Requester.Session {
		return false
	}
	return ask.Spec.Grant.UID == string(g.UID)
}

func grantAskScopeOK(g *v1alpha1.AccessGrant, ask *coord.Ask) bool {
	if ask == nil || ask.Spec.Grant == nil {
		return false
	}
	gr := ask.Spec.Grant
	target, ok := grantAskTarget(g)
	if !ok || gr.Target != target || gr.Type != string(g.Spec.Type) || gr.ScopeDigest != grantattest.ScopeDigest(g) {
		return false
	}
	if len(gr.TTL) < 2 || len(gr.TTL) > apiv1.AskGrantTTLMaxLen || !grantAskTTLPattern.MatchString(gr.TTL) {
		return false
	}
	parsed, err := time.ParseDuration(gr.TTL)
	return err == nil && parsed == g.Spec.TTL.Duration
}

// grantAskDetailOK reports whether the stored scope text is the text this
// build renders for the grant. A text that does not fit is not equal.
func grantAskDetailOK(g *v1alpha1.AccessGrant, ask *coord.Ask) bool {
	if ask == nil || ask.Spec.Grant == nil {
		return false
	}
	detail, fits := grantAskDetail(g)
	if !fits {
		return false
	}
	return ask.Spec.Grant.Detail == detail
}

func grantAskUsernameOK(user string) bool {
	return grantAskUsernamePattern.MatchString(user)
}

func (b *Broker) grantAskHoldError(ctx context.Context, g *v1alpha1.AccessGrant, err error) (reconcile.Result, error) {
	switch {
	case errors.Is(err, asks.ErrLimit):
		b.event(g, corev1.EventTypeWarning, "GrantAsk", "AskBoardFull", "the asks board is full; the grant stays pending")
		log.FromContext(ctx).Error(err, "grant ask was not opened", "grant", g.Name)
	case errors.Is(err, asks.ErrUnavailable):
		// The count was not confirmed. That is not the cap.
		b.event(g, corev1.EventTypeWarning, "GrantAsk", "AskCountUnavailable", "the ask count could not be confirmed; the grant stays pending")
		log.FromContext(ctx).Error(err, "grant ask was not opened", "grant", g.Name)
	case errors.Is(err, errGrantAskLater):
		// A UID, target or body that cannot be stored. Retry, and say so on
		// the grant. Do not deny.
		b.event(g, corev1.EventTypeWarning, "GrantAsk", "AskNotOpened", "the grant ask could not be opened; the grant stays pending")
		log.FromContext(ctx).Error(err, "grant ask was not opened", "grant", g.Name)
	case errors.Is(err, errGrantAskStop):
		// Pending, and not approved. The next pass looks again. Nothing here
		// waits on a person to clear a lock.
		return reconcile.Result{RequeueAfter: grantAskRecheck}, nil
	default:
		log.FromContext(ctx).Error(err, "grant ask was not opened", "grant", g.Name)
	}
	return reconcile.Result{RequeueAfter: grantAskRecheck}, nil
}

func (b *Broker) noteGrantAsk(ctx context.Context, g *v1alpha1.AccessGrant, now metav1.Time) (reconcile.Result, error) {
	wait := reconcile.Result{RequeueAfter: grantAskRecheck}
	if g.Status.NotifiedAt != nil || b.Notifier == nil {
		return wait, nil
	}
	if err := b.Notifier.NotifyPending(ctx, g); err != nil {
		b.event(g, corev1.EventTypeWarning, "Notify", "NotifyFailed", "could not tell Tom: "+err.Error())
		return reconcile.Result{}, fmt.Errorf("notify: %w", err)
	}
	g.Status.NotifiedAt = &now
	if written, err := b.writeStatus(ctx, g); !written || err != nil {
		return reconcile.Result{}, err
	}
	log.FromContext(ctx).Info("grant waits for Tom", grantFields(g)...)
	b.event(g, corev1.EventTypeNormal, "Notify", "WaitsForTom", "no standing policy matches; a grant ask was opened")
	return wait, nil
}

// ensureGrantAsk claims the route and creates the one Ask. The scope text is
// rendered first: a text that does not fit never takes a route. An Ask that
// is already this grant's is claimed and not created again. Any other object
// at the name is left alone and the route is not set.
func (b *Broker) ensureGrantAsk(ctx context.Context, g *v1alpha1.AccessGrant, sess *v1alpha1.AgentSession, now metav1.Time) error {
	if b.SessionNamespace != asks.Namespace || sess == nil {
		return errGrantAskLater
	}
	if g.Spec.Requester.SessionUID == "" || string(sess.UID) != string(g.Spec.Requester.SessionUID) || sess.Name != g.Spec.Requester.Session {
		return errGrantAskLater
	}
	id, ok := grantAskRequestID(g.UID)
	if !ok {
		return errGrantAskLater
	}
	if _, err := b.grantAskBody(ctx, g, sess, id, now.Time); err != nil {
		return err
	}
	existing, loadErr := b.loadGrantAsk(ctx, g)
	switch {
	case loadErr == nil && grantAskIdentityOK(g, existing):
		return b.claimGrantAskRoute(ctx, g)
	case loadErr == nil:
		b.noteForeignGrantAsk(g)
		return errGrantAskForeign
	case !apierrors.IsNotFound(loadErr):
		return loadErr
	}
	if err := b.claimGrantAskRoute(ctx, g); err != nil {
		return err
	}
	b.noteGrantAskCreateOutstanding(g.UID)
	return b.createGrantedAsk(ctx, g, sess, now)
}

// claimGrantAskRoute writes status.ask with the grant's resourceVersion
// before the Ask exists. A conflict or a route that is already something
// else returns errGrantAskRouteReread. The caller reads again and does not
// create.
func (b *Broker) claimGrantAskRoute(ctx context.Context, g *v1alpha1.AccessGrant) error {
	name, ok := grantAskObjectName(b.SessionNamespace, g)
	if !ok {
		return errGrantAskLater
	}
	if g.Status.Ask == name {
		return nil
	}
	if g.Status.Ask != "" {
		return errGrantAskRouteReread
	}
	fresh, err := b.readGrant(ctx, g.Name)
	if apierrors.IsNotFound(err) {
		return errGrantAskRouteReread
	}
	if err != nil {
		return err
	}
	if fresh.UID != g.UID || (fresh.Status.Phase != "" && fresh.Status.Phase != v1alpha1.GrantPending) || !fresh.DeletionTimestamp.IsZero() || fresh.Spec.Release || fresh.Status.Ask != "" {
		*g = *fresh
		return errGrantAskRouteReread
	}
	fresh.Status.Ask = name
	if err := b.Client.Status().Update(ctx, fresh); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return errGrantAskRouteReread
		}
		return err
	}
	*g = *fresh
	return nil
}

// createGrantedAsk stores the Ask named by the route. It does not choose a
// second name. A create this process has not seen succeed stays outstanding.
func (b *Broker) createGrantedAsk(ctx context.Context, g *v1alpha1.AccessGrant, sess *v1alpha1.AgentSession, now metav1.Time) error {
	name, ok := grantAskObjectName(b.SessionNamespace, g)
	if !ok || g.Status.Ask != name {
		return errGrantAskRouteForeign
	}
	id, ok := grantAskRequestID(g.UID)
	if !ok {
		return errGrantAskLater
	}
	body, err := b.grantAskBody(ctx, g, sess, id, now.Time)
	if err != nil {
		return err
	}
	req := asks.Requester{
		Namespace: b.SessionNamespace, Name: g.Spec.Requester.Session, UID: string(g.Spec.Requester.SessionUID),
		Origin: grantAskOriginBroker,
	}
	wire, created, err := b.askCreator().Create(ctx, req, body, now.Time)
	if errors.Is(err, asks.ErrConflict) {
		b.clearGrantAskCreateOutstanding(g.UID)
		return errGrantAskRouteForeign
	}
	if err != nil {
		return err
	}
	b.clearGrantAskCreateOutstanding(g.UID)
	stored, loadErr := b.loadAskByName(ctx, g.Status.Ask)
	if loadErr != nil {
		return loadErr
	}
	if !grantAskIdentityOK(g, stored) {
		return errGrantAskRouteForeign
	}
	if created {
		log.FromContext(ctx).Info("opened a grant ask", "grant", g.Name, "ask", wire.ID)
	}
	return nil
}

// followGrantAskRoute is the only path for a grant whose route is set.
// The Ask is re-read here. Missing, not this grant's, or unreadable past
// the short bound denies the grant. A create this process has not seen
// succeed is retried at the same name.
func (b *Broker) followGrantAskRoute(ctx context.Context, g *v1alpha1.AccessGrant, sess *v1alpha1.AgentSession, now metav1.Time) (reconcile.Result, error) {
	if g.Status.Ask == "" {
		return reconcile.Result{RequeueAfter: time.Second}, nil
	}
	ask, err := b.loadAskByName(ctx, g.Status.Ask)
	switch {
	case apierrors.IsNotFound(err):
		if b.grantAskCreateOutstanding(g.UID) {
			if cerr := b.createGrantedAsk(ctx, g, sess, now); cerr != nil {
				if errors.Is(cerr, errGrantAskRouteForeign) {
					b.noteForeignGrantAsk(g)
					return b.denyRoutedGrant(ctx, g, grantAskMsgForeign, now)
				}
				return b.grantAskHoldError(ctx, g, cerr)
			}
			ask, err = b.loadAskByName(ctx, g.Status.Ask)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return b.grantAskHoldError(ctx, g, errGrantAskLater)
				}
				return b.routedUnread(ctx, g, now)
			}
		} else {
			return b.denyRoutedGrant(ctx, g, grantAskMsgGone, now)
		}
	case err != nil:
		return b.routedUnread(ctx, g, now)
	}
	b.clearGrantAskUnread(g.UID)
	b.clearGrantAskCreateOutstanding(g.UID)
	if !grantAskIdentityOK(g, ask) {
		b.noteForeignGrantAsk(g)
		return b.denyRoutedGrant(ctx, g, grantAskMsgForeign, now)
	}
	if done, res, aerr := b.applyLoadedGrantAnswer(ctx, g, ask, now); done {
		return res, aerr
	}
	return b.noteGrantAsk(ctx, g, now)
}

func (b *Broker) routedUnread(ctx context.Context, g *v1alpha1.AccessGrant, now metav1.Time) (reconcile.Result, error) {
	if b.grantAskUnreadTooLong(g.UID, now.Time) {
		return b.denyRoutedGrant(ctx, g, grantAskMsgUnread, now)
	}
	return reconcile.Result{RequeueAfter: time.Second}, nil
}

func (b *Broker) denyRoutedGrant(ctx context.Context, g *v1alpha1.AccessGrant, msg string, now metav1.Time) (reconcile.Result, error) {
	b.clearGrantAskUnread(g.UID)
	b.clearGrantAskCreateOutstanding(g.UID)
	return b.denyPendingGrant(ctx, g, msg, now)
}

// classifyUnroutedGrantAsk reports whether the Ask at this grant's computed
// name is this grant's, something else, missing, or unreadable. The origin
// label alone is not enough.
func (b *Broker) classifyUnroutedGrantAsk(ctx context.Context, g *v1alpha1.AccessGrant) int {
	ask, err := b.loadGrantAsk(ctx, g)
	switch {
	case errors.Is(err, errGrantAskLater), apierrors.IsNotFound(err):
		return grantAskNone
	case err != nil:
		return grantAskUnread
	case grantAskIdentityOK(g, ask):
		return grantAskBroker
	default:
		return grantAskForeign
	}
}

// attestedPolicyCouldMatch reports whether a non-deleting policy with an
// attestation requirement fits this grant. The statement need not be present
// yet. A failed policy read reports a match, so this pass opens no Ask.
func (b *Broker) attestedPolicyCouldMatch(ctx context.Context, g *v1alpha1.AccessGrant) bool {
	if !b.EnableAttestedGrants {
		return false
	}
	var policies v1alpha1.GrantPolicyList
	if err := b.Client.List(ctx, &policies, client.InNamespace(b.PolicyNamespace)); err != nil {
		return true
	}
	for i := range policies.Items {
		p := &policies.Items[i]
		if p.DeletionTimestamp.IsZero() && MatchesAttested(p, g) {
			return true
		}
	}
	return false
}

func (b *Broker) noteGrantAskCreateOutstanding(uid types.UID) {
	b.grantAskMu.Lock()
	defer b.grantAskMu.Unlock()
	if b.grantAskCreateWait == nil {
		b.grantAskCreateWait = map[types.UID]struct{}{}
	}
	b.grantAskCreateWait[uid] = struct{}{}
}

func (b *Broker) clearGrantAskCreateOutstanding(uid types.UID) {
	b.grantAskMu.Lock()
	defer b.grantAskMu.Unlock()
	delete(b.grantAskCreateWait, uid)
}

func (b *Broker) grantAskCreateOutstanding(uid types.UID) bool {
	b.grantAskMu.Lock()
	defer b.grantAskMu.Unlock()
	_, ok := b.grantAskCreateWait[uid]
	return ok
}

func (b *Broker) grantAskUnreadTooLong(uid types.UID, now time.Time) bool {
	b.grantAskMu.Lock()
	defer b.grantAskMu.Unlock()
	if b.grantAskUnreadAt == nil {
		b.grantAskUnreadAt = map[types.UID]time.Time{}
	}
	started, ok := b.grantAskUnreadAt[uid]
	if !ok {
		b.grantAskUnreadAt[uid] = now
		return false
	}
	return !now.Before(started.Add(grantAskUnreadLimit))
}

func (b *Broker) clearGrantAskUnread(uid types.UID) {
	b.grantAskMu.Lock()
	defer b.grantAskMu.Unlock()
	delete(b.grantAskUnreadAt, uid)
}

func (b *Broker) noteForeignGrantAsk(g *v1alpha1.AccessGrant) {
	b.event(g, corev1.EventTypeWarning, "GrantAsk", "AskNotBroker", "an ask at this grant's name was not opened by the broker")
}

// withdrawOpenGrantAsk withdraws the broker's open grant Ask when the session
// releases the grant. A missing Ask is not an error. An Ask that is not this
// grant's is left alone. The session-ended path does not call this: that card
// stays open for Dismiss.
func (b *Broker) withdrawOpenGrantAsk(ctx context.Context, g *v1alpha1.AccessGrant) error {
	obj, err := b.loadDecisionAsk(ctx, g)
	if apierrors.IsNotFound(err) || errors.Is(err, errGrantAskLater) {
		return nil
	}
	if err != nil {
		return err
	}
	if !grantAskIdentityOK(g, obj) {
		return nil
	}
	if obj.Status.State != "" && obj.Status.State != string(apiv1.AskStateOpen) {
		return nil
	}
	req := asks.Requester{Namespace: b.SessionNamespace, Name: g.Spec.Requester.Session, UID: string(g.Spec.Requester.SessionUID)}
	if _, err := b.askStore().Withdraw(ctx, obj.Name, req, b.now().Time); err != nil {
		if errors.Is(err, asks.ErrNotFound) || errors.Is(err, apiv1.ErrAskNotOpen) {
			return nil
		}
		return err
	}
	return nil
}

func (b *Broker) loadGrantAsk(ctx context.Context, g *v1alpha1.AccessGrant) (*coord.Ask, error) {
	name, ok := grantAskObjectName(b.SessionNamespace, g)
	if !ok {
		return nil, errGrantAskLater
	}
	return b.loadAskByName(ctx, name)
}

func (b *Broker) loadDecisionAsk(ctx context.Context, g *v1alpha1.AccessGrant) (*coord.Ask, error) {
	if g.Status.Ask != "" {
		return b.loadAskByName(ctx, g.Status.Ask)
	}
	return b.loadGrantAsk(ctx, g)
}

func (b *Broker) loadAskByName(ctx context.Context, name string) (*coord.Ask, error) {
	if name == "" || b.APIReader == nil {
		return nil, errGrantAskLater
	}
	var obj coord.Ask
	if err := b.APIReader.Get(ctx, client.ObjectKey{Namespace: asks.Namespace, Name: name}, &obj); err != nil {
		return nil, err
	}
	return &obj, nil
}

func grantAskObjectName(namespace string, g *v1alpha1.AccessGrant) (string, bool) {
	if namespace != asks.Namespace || g.Spec.Requester.Session == "" || g.Spec.Requester.SessionUID == "" {
		return "", false
	}
	id, ok := grantAskRequestID(g.UID)
	if !ok {
		return "", false
	}
	return asks.Name(asks.Requester{
		Namespace: namespace,
		Name:      g.Spec.Requester.Session,
		UID:       string(g.Spec.Requester.SessionUID),
	}, id), true
}

func grantAskRequestID(uid types.UID) (string, bool) {
	id := apiv1.AskGrantRequestPrefix + string(uid)
	if !apiv1.ValidAskRequestID(id) {
		return "", false
	}
	return id, true
}

func (b *Broker) grantAskBody(ctx context.Context, g *v1alpha1.AccessGrant, sess *v1alpha1.AgentSession, id string, now time.Time) (apiv1.AskCreateRequest, error) {
	target, ok := grantAskTarget(g)
	if !ok {
		return apiv1.AskCreateRequest{}, errGrantAskLater
	}
	ttl, ok := grantAskTTL(g.Spec.TTL.Duration)
	if !ok {
		return apiv1.AskCreateRequest{}, errGrantAskLater
	}
	digest := grantattest.ScopeDigest(g)
	if !grantAskDigestPattern.MatchString(digest) {
		return apiv1.AskCreateRequest{}, errGrantAskLater
	}
	detail, fits := grantAskDetail(g)
	if !fits {
		return apiv1.AskCreateRequest{}, errGrantAskTooLarge
	}
	body := apiv1.AskCreateRequest{
		RequestID: id,
		Type:      apiv1.AskTypeGrant,
		Grant: &apiv1.AskGrant{
			UID: string(g.UID), ScopeDigest: digest, Type: string(g.Spec.Type),
			Target: target, TTL: ttl, Detail: detail,
		},
		Context: apiv1.AskRequestContext{WorkPackage: b.grantAskWorkPackage(ctx, sess)},
	}
	body.Question = grantAskQuestion(g.Spec.Reason, body, now)
	if err := body.ValidateAt(now); err != nil {
		// The complete scope is kept. A detail that does not fit is not
		// shortened or dropped, and no Ask is opened.
		if detail != "" {
			return apiv1.AskCreateRequest{}, errGrantAskTooLarge
		}
		return apiv1.AskCreateRequest{}, errGrantAskLater
	}
	return body, nil
}

func grantAskQuestion(reason string, body apiv1.AskCreateRequest, now time.Time) string {
	q := truncateRunes(apiv1.NormalizeAskText(reason), apiv1.AskQuestionMaxRunes)
	trial := body
	if trial.Grant != nil {
		g := *trial.Grant
		g.Detail = ""
		trial.Grant = &g
	}
	trial.Question = q
	if trial.ValidateAt(now) == nil {
		return q
	}
	return grantAskQuestionFallback
}

func grantAskTarget(g *v1alpha1.AccessGrant) (string, bool) {
	var target string
	switch g.Spec.Type {
	case v1alpha1.GrantKube, v1alpha1.GrantBreakglass:
		if g.Spec.Kube == nil {
			return "", false
		}
		target = g.Spec.Kube.Role
	case v1alpha1.GrantCredential:
		if g.Spec.Credential == nil {
			return "", false
		}
		target = string(g.Spec.Credential.Name)
	case v1alpha1.GrantEgress:
		target = "egress"
	case v1alpha1.GrantMaintenance:
		target = "maintenance"
	default:
		return "", false
	}
	n := utf8.RuneCountInString(target)
	if n < 1 || n > apiv1.AskScopeFieldMaxRunes {
		return "", false
	}
	return target, true
}

func grantAskTTL(d time.Duration) (string, bool) {
	s := d.String()
	if len(s) < 2 || len(s) > apiv1.AskGrantTTLMaxLen || !grantAskTTLPattern.MatchString(s) {
		return "", false
	}
	parsed, err := time.ParseDuration(s)
	if err != nil || parsed != d {
		return "", false
	}
	return s, true
}

// grantAskDetail is the complete access the scope digest covers, beyond the
// type, target and TTL already on the Ask. fits is false when that text
// does not fit the Ask detail bound. The text is never shortened.
func grantAskDetail(g *v1alpha1.AccessGrant) (string, bool) {
	var detail string
	switch g.Spec.Type {
	case v1alpha1.GrantKube, v1alpha1.GrantBreakglass:
		detail = kubeAskDetail(g.Spec.Kube)
	case v1alpha1.GrantEgress:
		detail = egressAskDetail(g.Spec.Egress)
	}
	if utf8.RuneCountInString(detail) > apiv1.AskScopeFieldMaxRunes {
		return "", false
	}
	return detail, true
}

func kubeAskDetail(k *v1alpha1.KubeGrant) string {
	if k == nil {
		return ""
	}
	var parts []string
	if k.AllNamespaces {
		parts = append(parts, "allNamespaces:true")
	} else {
		parts = append(parts, "namespaces:["+strings.Join(sortedCopy(k.Namespaces), ",")+"]")
	}
	if k.Reserved {
		parts = append(parts, "reserved:true")
	}
	if len(k.Secrets) > 0 {
		parts = append(parts, "secrets:["+strings.Join(sortedCopy(k.Secrets), ",")+"]")
	}
	if len(k.StorageTargets) > 0 {
		items := make([]string, len(k.StorageTargets))
		for i, target := range k.StorageTargets {
			items[i] = target.Kind + "/" + target.Name + "/" + target.UID
		}
		slices.Sort(items)
		parts = append(parts, "storage:["+strings.Join(items, ",")+"]")
	}
	return strings.Join(parts, " ")
}

func egressAskDetail(e *v1alpha1.EgressGrant) string {
	if e == nil {
		return ""
	}
	var parts []string
	for _, name := range sortedCopy(e.FQDNs) {
		parts = append(parts, "fqdn:"+name)
	}
	for _, cidr := range sortedCopy(e.CIDRs) {
		parts = append(parts, "cidr:"+cidr)
	}
	type endpointText struct {
		namespace string
		labels    string
	}
	endpoints := make([]endpointText, 0, len(e.Endpoints))
	for _, item := range e.Endpoints {
		keys := make([]string, 0, len(item.MatchLabels))
		for key, value := range item.MatchLabels {
			keys = append(keys, key+"="+string(value))
		}
		slices.Sort(keys)
		labels := ""
		if len(keys) > 0 {
			labels = "[" + strings.Join(keys, ",") + "]"
		}
		endpoints = append(endpoints, endpointText{namespace: item.Namespace, labels: labels})
	}
	slices.SortFunc(endpoints, func(a, b endpointText) int {
		if a.namespace != b.namespace {
			return strings.Compare(a.namespace, b.namespace)
		}
		return strings.Compare(a.labels, b.labels)
	})
	for _, item := range endpoints {
		parts = append(parts, "endpoint:"+item.namespace+item.labels)
	}
	if len(e.Ports) > 0 {
		type portText struct {
			port     int32
			protocol string
		}
		ports := make([]portText, len(e.Ports))
		for i, port := range e.Ports {
			protocol := string(port.Protocol)
			if protocol == "" {
				protocol = string(v1alpha1.ProtocolTCP)
			}
			ports[i] = portText{port: port.Port, protocol: protocol}
		}
		slices.SortFunc(ports, func(a, b portText) int {
			if a.port != b.port {
				return int(a.port - b.port)
			}
			return strings.Compare(a.protocol, b.protocol)
		})
		shown := make([]string, len(ports))
		for i, port := range ports {
			shown[i] = fmt.Sprintf("%d/%s", port.port, port.protocol)
		}
		parts = append(parts, "ports:"+strings.Join(shown, ","))
	}
	return strings.Join(parts, " ")
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

// grantAskWorkPackage is the one OpenProject issue id on the session, or 0
// when there is not exactly one. A failed list omits the link and the Ask is
// still created.
func (b *Broker) grantAskWorkPackage(ctx context.Context, sess *v1alpha1.AgentSession) int {
	if sess == nil || sess.UID == "" || b.APIReader == nil || b.SessionNamespace != asks.Namespace {
		return 0
	}
	who := taskclaim.SessionLabel(coord.SessionIdentity{
		Namespace: b.SessionNamespace, Name: sess.Name, UID: string(sess.UID),
	})
	var list coord.TaskClaimList
	if err := b.APIReader.List(ctx, &list, client.InNamespace(b.SessionNamespace), client.MatchingLabels{taskclaim.SourceLabel: who}); err != nil {
		log.FromContext(ctx).Error(err, "task claims were not read; the grant ask has no story link", "session", sess.Name)
		return 0
	}
	if list.Continue != "" {
		log.FromContext(ctx).Error(errors.New("task claim list did not finish"), "task claims were not read; the grant ask has no story link", "session", sess.Name)
		return 0
	}
	ids := map[int]struct{}{}
	for i := range list.Items {
		item := &list.Items[i]
		if !item.DeletionTimestamp.IsZero() {
			continue
		}
		id, ok := openProjectIssue(item.Spec.Identity.TaskKey)
		if !ok {
			continue
		}
		ids[id] = struct{}{}
	}
	if len(ids) != 1 {
		return 0
	}
	for id := range ids {
		return id
	}
	return 0
}

func openProjectIssue(taskKey string) (int, bool) {
	rest, ok := strings.CutPrefix(taskKey, "issue:")
	if !ok || rest == "" || rest[0] < '1' || rest[0] > '9' {
		return 0, false
	}
	for _, c := range rest[1:] {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n <= 0 || n > grantAskWorkPackageMax || strconv.Itoa(n) != rest {
		return 0, false
	}
	return n, true
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// enqueueGrantAsk wakes the grant an answered Ask names. Flag off, the watch
// is not registered. A grant that is already decided is left alone.
func (b *Broker) enqueueGrantAsk(ctx context.Context, o client.Object) []reconcile.Request {
	if !b.EnableGrantAsks {
		return nil
	}
	ask, ok := o.(*coord.Ask)
	if !ok || ask.Spec.Grant == nil || ask.Labels[asks.OriginLabel] != grantAskOriginBroker {
		return nil
	}
	if b.APIReader == nil {
		return nil
	}
	var list v1alpha1.AccessGrantList
	if err := b.APIReader.List(ctx, &list, client.InNamespace(b.SessionNamespace)); err != nil {
		return nil
	}
	var out []reconcile.Request
	for i := range list.Items {
		g := &list.Items[i]
		if string(g.UID) != ask.Spec.Grant.UID {
			continue
		}
		if g.Status.Phase != "" && g.Status.Phase != v1alpha1.GrantPending {
			continue
		}
		out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(g)})
	}
	return out
}
