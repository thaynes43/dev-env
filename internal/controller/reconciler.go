// Package controller reconciles AgentSession resources into their pods and
// volumes (DESIGN-001 3.1, 3.2, 7; D-44).
//
// The rules of DESIGN-001 5.1 shape everything here, and the envtest suite in
// this package proves them:
//
//   - The pod and the volume have one owner reference, to their AgentSession,
//     and never one to the operator's Deployment (D-03).
//   - The operator never deletes or updates a session's pod. It creates a pod
//     when the session has none and reports what it sees. Suspend and drain are
//     the only transitions that will ever delete a pod (5.1), and they arrive
//     with rescue (plan 01 step 5) and drain (plan 04).
//   - Reconcile is level-based: everything comes from the AgentSession, its pod,
//     its volume and the templates, so a fresh operator resumes where the last
//     one stopped. Nothing lives only in memory.
package controller

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// Condition types the reconciler sets.
const (
	// ConditionPodReady says whether the session's pod is Ready, and if not,
	// why: the scheduler's reason, a container's waiting reason, the
	// templates, or a pod that ended.
	ConditionPodReady = "PodReady"
	// ConditionOutdated is True when the pod runs an older template revision
	// than the current one (DESIGN-001 5.2). Plan 04 drains on it; until then
	// it is reported only.
	ConditionOutdated = "Outdated"
)

// CacheOptions scopes the manager's cache to what the operator may read
// (DESIGN-001 6.11, D-44): the sessions' namespace, and the one templates
// ConfigMap in its own namespace.
func CacheOptions(sessionNamespace string, tmpl types.NamespacedName) cache.Options {
	return cache.Options{
		DefaultNamespaces: map[string]cache.Config{sessionNamespace: {}},
		ByObject: map[client.Object]cache.ByObject{
			&corev1.ConfigMap{}: {
				Namespaces: map[string]cache.Config{tmpl.Namespace: {}},
				Field:      fields.OneTermEqualSelector("metadata.name", tmpl.Name),
			},
		},
	}
}

// Reconciler builds and watches session pods and volumes.
type Reconciler struct {
	// Client reads from the manager's cache and writes to the API server.
	Client client.Client
	// Templates is the dev-env-templates ConfigMap.
	Templates types.NamespacedName
	// APIURL is the operator API's base URL, given to agentd for its heartbeat
	// (D-41). Empty until the API is served (plan 01 step 3).
	APIURL string
}

// SetupWithManager registers the reconciler. It watches sessions, the pods and
// volumes they control, and the templates ConfigMap, whose changes reach every
// session that still needs a pod.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, opts ...func(*ctrl.Builder)) error {
	b := ctrl.NewControllerManagedBy(mgr).
		Named("agentsession").
		For(&v1alpha1.AgentSession{}).
		Owns(&corev1.Pod{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.sessionsForTemplates))
	for _, o := range opts {
		o(b)
	}
	return b.Complete(r)
}

func (r *Reconciler) sessionsForTemplates(ctx context.Context, o client.Object) []reconcile.Request {
	if o.GetNamespace() != r.Templates.Namespace || o.GetName() != r.Templates.Name {
		return nil
	}
	var list v1alpha1.AgentSessionList
	if err := r.Client.List(ctx, &list); err != nil {
		log.FromContext(ctx).Error(err, "list sessions after a templates change")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}

// Reconcile creates what a session lacks and records what it sees. It never
// deletes or updates a pod or a volume.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var s v1alpha1.AgentSession
	if err := r.Client.Get(ctx, req.NamespacedName, &s); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	pod, err := getOwned(ctx, r.Client, &s, s.Name, &corev1.Pod{})
	if err != nil {
		return ctrl.Result{}, err
	}
	claim, err := getOwned(ctx, r.Client, &s, HomeClaimName(s.Name), &corev1.PersistentVolumeClaim{})
	if err != nil {
		return ctrl.Result{}, err
	}

	t, tErr := r.loadTemplates(ctx)
	obs := observation{pod: pod, claim: claim, templates: t, templatesErr: tErr}

	if s.DeletionTimestamp.IsZero() && s.Spec.OperatingMode != v1alpha1.OperatingModeSuspended && tErr == nil {
		if err := r.ensure(ctx, &s, t, &obs); err != nil {
			return ctrl.Result{}, err
		}
	}

	next := s.Status.DeepCopy()
	observe(&s, obs, next)
	if !apiequality.Semantic.DeepEqual(&s.Status, next) {
		s.Status = *next
		if err := r.Client.Status().Update(ctx, &s); err != nil {
			if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
				// A newer version of the session is on its way through
				// the cache; its event reconciles again.
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// ensure creates the volume and the pod when the session has no pod. It builds
// the pod first, so a session that can never start (one agentd would refuse, or
// one the templates cannot serve) gets no volume either. A pod or volume of the
// session's names that the session does not control is left alone and reported.
func (r *Reconciler) ensure(ctx context.Context, s *v1alpha1.AgentSession, t *templates.Templates, obs *observation) error {
	if !obs.pod.missing {
		return nil
	}
	pod, err := buildPod(s, t, r.APIURL)
	if err != nil {
		obs.buildErr = err
		return nil
	}
	if obs.claim.missing {
		claim, err := buildHomeClaim(s, t)
		if err != nil {
			obs.buildErr = err
			return nil
		}
		if err := r.create(ctx, "volume", claim); err != nil {
			return err
		}
		log.FromContext(ctx).Info("created the session volume", "claim", claim.Name, "storageClass", *claim.Spec.StorageClassName)
		obs.claim = owned[*corev1.PersistentVolumeClaim]{obj: claim}
	}
	if obs.claim.foreign != "" || volumeTerminating(*obs) {
		return nil
	}
	if err := r.create(ctx, "pod", pod); err != nil {
		return err
	}
	log.FromContext(ctx).Info("created the session pod", "pod", pod.Name, "revision", t.Revision())
	obs.pod = owned[*corev1.Pod]{obj: pod}
	return nil
}

// create creates an object. AlreadyExists means the cache had not caught up, or
// a foreign object took the name; the next reconcile sees which.
func (r *Reconciler) create(ctx context.Context, kind string, o client.Object) error {
	if err := r.Client.Create(ctx, o); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("%s %s already exists; retrying once the cache has it", kind, o.GetName())
		}
		return fmt.Errorf("create %s %s: %w", kind, o.GetName(), err)
	}
	return nil
}

func (r *Reconciler) loadTemplates(ctx context.Context) (*templates.Templates, error) {
	var cm corev1.ConfigMap
	if err := r.Client.Get(ctx, r.Templates, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("templates: ConfigMap %s not found", r.Templates)
		}
		return nil, fmt.Errorf("templates: %w", err)
	}
	t, err := templates.Parse(cm.Data)
	if err != nil {
		return nil, fmt.Errorf("templates %s: %w", r.Templates, err)
	}
	return t, nil
}

// owned is the result of looking up a session's pod or volume by name.
type owned[T client.Object] struct {
	obj T
	// missing: no object has the name.
	missing bool
	// foreign: an object has the name but the session does not control it;
	// the text says what owns it.
	foreign string
}

func getOwned[T client.Object](ctx context.Context, c client.Client, s *v1alpha1.AgentSession, name string, obj T) (owned[T], error) {
	if err := c.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return owned[T]{missing: true}, nil
		}
		return owned[T]{}, err
	}
	if !metav1.IsControlledBy(obj, s) {
		return owned[T]{foreign: describeOwner(obj)}, nil
	}
	return owned[T]{obj: obj}, nil
}

func describeOwner(o metav1.Object) string {
	if ref := metav1.GetControllerOf(o); ref != nil {
		return fmt.Sprintf("%s %s (uid %s)", ref.Kind, ref.Name, ref.UID)
	}
	return "no controller"
}

// observation is what one reconcile found.
type observation struct {
	pod          owned[*corev1.Pod]
	claim        owned[*corev1.PersistentVolumeClaim]
	templates    *templates.Templates
	templatesErr error
	buildErr     error
}

// observe writes the session's status from what the reconcile found. It is a
// pure function of its inputs, so a fresh operator computes the same status.
func observe(s *v1alpha1.AgentSession, obs observation, st *v1alpha1.AgentSessionStatus) {
	st.PodName, st.NodeName, st.Revision = "", "", ""
	pod := obs.pod.obj
	hasPod := obs.pod.foreign == "" && !obs.pod.missing
	if hasPod {
		st.PodName = pod.Name
		st.NodeName = pod.Spec.NodeName
		st.Revision = pod.Labels[v1alpha1.LabelRevision]
	}

	phase, ready := podPhase(s, obs)
	ready.Reason = conditionReason(ready.Reason)
	ready.Message = truncate(ready.Message)
	st.Phase = phase
	st.PendingReason = ""
	if phase == v1alpha1.PhasePending {
		st.PendingReason = ready.Message
	}
	ready.Type = ConditionPodReady
	ready.ObservedGeneration = s.Generation
	meta.SetStatusCondition(&st.Conditions, ready)

	switch {
	case hasPod && obs.templates != nil:
		cur := obs.templates.Revision()
		c := metav1.Condition{Type: ConditionOutdated, Status: metav1.ConditionFalse, Reason: "CurrentRevision",
			Message: "the pod runs the current template revision " + cur, ObservedGeneration: s.Generation}
		if st.Revision != cur {
			c.Status, c.Reason = metav1.ConditionTrue, "OlderRevision"
			c.Message = fmt.Sprintf("the pod runs template revision %s; the current one is %s", st.Revision, cur)
		}
		meta.SetStatusCondition(&st.Conditions, c)
	case hasPod:
		meta.SetStatusCondition(&st.Conditions, metav1.Condition{Type: ConditionOutdated, Status: metav1.ConditionUnknown,
			Reason: "TemplatesInvalid", Message: truncate("the current template revision is unknown: " + obs.templatesErr.Error()), ObservedGeneration: s.Generation})
	default:
		meta.RemoveStatusCondition(&st.Conditions, ConditionOutdated)
	}
}

// reasonPattern is metav1.Condition's rule for a reason. Reasons from the
// kubelet and the scheduler are CamelCase words, but a status the API server
// refuses would stall the session's every update, so a stray one is replaced.
var reasonPattern = regexp.MustCompile(`^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$`)

func conditionReason(r string) string {
	if len(r) > 1024 || !reasonPattern.MatchString(r) {
		return "NotReady"
	}
	return r
}

// maxMessage keeps a condition message well inside the API's limit (32768).
const maxMessage = 4096

func truncate(msg string) string {
	if len(msg) <= maxMessage {
		return msg
	}
	return msg[:maxMessage-3] + "..."
}

// volumeTerminating reports whether the session's volume exists and is being
// deleted. A new pod would never start on it.
func volumeTerminating(obs observation) bool {
	return !obs.claim.missing && obs.claim.foreign == "" && !obs.claim.obj.DeletionTimestamp.IsZero()
}

// podPhase maps the session's pod to a phase and the PodReady condition.
func podPhase(s *v1alpha1.AgentSession, obs observation) (v1alpha1.SessionPhase, metav1.Condition) {
	notReady := func(reason, msg string) metav1.Condition {
		return metav1.Condition{Status: metav1.ConditionFalse, Reason: reason, Message: msg}
	}
	pod := obs.pod.obj
	switch {
	case obs.pod.foreign != "":
		return v1alpha1.PhasePending, notReady("NameTaken", fmt.Sprintf("a pod named %s exists and its controller is %s, not this session; the operator leaves it alone", s.Name, obs.pod.foreign))
	case obs.claim.foreign != "":
		return v1alpha1.PhasePending, notReady("NameTaken", fmt.Sprintf("a volume named %s exists and its controller is %s, not this session; the operator leaves it alone", HomeClaimName(s.Name), obs.claim.foreign))
	case obs.pod.missing && s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended:
		return v1alpha1.PhaseSuspended, notReady("Suspended", "the session is suspended; its volume is kept")
	case obs.pod.missing && !s.DeletionTimestamp.IsZero():
		return v1alpha1.PhasePending, notReady("Deleting", "the session is being deleted and has no pod")
	case obs.pod.missing && volumeTerminating(obs):
		return v1alpha1.PhasePending, notReady("VolumeTerminating", fmt.Sprintf("the volume %s is being deleted, so no pod starts on it", HomeClaimName(s.Name)))
	case obs.pod.missing && obs.templatesErr != nil:
		return v1alpha1.PhasePending, notReady("TemplatesInvalid", obs.templatesErr.Error())
	case obs.pod.missing && errors.As(obs.buildErr, new(sessionInvalidError)):
		return v1alpha1.PhaseFailed, notReady("SessionInvalid", obs.buildErr.Error())
	case obs.pod.missing && obs.buildErr != nil:
		return v1alpha1.PhasePending, notReady("PodSpecInvalid", obs.buildErr.Error())
	case obs.pod.missing:
		return v1alpha1.PhasePending, notReady("Creating", "creating the pod")
	case !pod.DeletionTimestamp.IsZero():
		return v1alpha1.PhasePending, notReady("PodTerminating", "the pod is terminating; a new one starts on the same volume once it is gone")
	case pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded:
		reason := pod.Status.Reason
		if reason == "" {
			reason = string(pod.Status.Phase)
		}
		return v1alpha1.PhaseFailed, notReady("PodEnded", fmt.Sprintf("the pod ended (%s): %s; its volume is kept", reason, pod.Status.Message))
	case pod.Status.Phase == corev1.PodRunning && podReady(pod):
		return v1alpha1.PhaseRunning, metav1.Condition{Status: metav1.ConditionTrue, Reason: "PodReady", Message: "the pod is Ready on " + pod.Spec.NodeName}
	default:
		reason, msg := pendingReason(pod)
		return v1alpha1.PhasePending, notReady(reason, msg)
	}
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// pendingReason is the scheduler's reason when the pod is not placed (D-21),
// else the first container's waiting reason.
func pendingReason(p *corev1.Pod) (string, string) {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			reason := c.Reason
			if reason == "" {
				reason = "Unschedulable"
			}
			return reason, c.Message
		}
	}
	if p.Spec.NodeName == "" {
		return "Scheduling", "waiting for the scheduler to place the pod"
	}
	for _, cs := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
		if w := cs.State.Waiting; w != nil {
			msg := w.Reason
			if w.Message != "" {
				msg += ": " + w.Message
			}
			return w.Reason, msg
		}
		if t := cs.State.Terminated; t != nil {
			return "ContainerTerminated", fmt.Sprintf("container %s exited %d (%s); the kubelet restarts it", cs.Name, t.ExitCode, t.Reason)
		}
	}
	return "Starting", "the pod is placed on " + p.Spec.NodeName + " and starting"
}
