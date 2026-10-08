package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/go-logr/logr"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// ProxmoxGrantOptions enables only the ratified PVE backend. General hardware
// certificate grants are deliberately absent from this worker.
type ProxmoxGrantOptions struct {
	Enabled                            bool
	SessionNamespace                   string
	JournalSecret                      string
	CADir, TargetsFile, KnownHostsFile string
}

var credentialUID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var credentialDNS = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func validUID(uid string) bool { return credentialUID.MatchString(uid) }
func checkCredentialSpec(s v1alpha1.CredentialJobSpec) error {
	for _, ref := range []v1alpha1.CredentialObjectReference{s.Grant, s.Session} {
		if len(ref.Name) > 63 || !credentialDNS.MatchString(ref.Name) || len(ref.Namespace) > 63 || !credentialDNS.MatchString(ref.Namespace) || !validUID(string(ref.UID)) {
			return errors.New("invalid credential object reference")
		}
	}
	if !strings.HasPrefix(s.Grant.Name, "grant-") || s.Credential != v1alpha1.CredentialProxmox || s.ExpiresAt.IsZero() {
		return errors.New("invalid credential request")
	}
	return nil
}
func isConflict(err error) bool { return apierrors.IsConflict(err) }
func sameCredentialRequest(a, b v1alpha1.CredentialJobSpec) bool {
	a.Release = false
	b.Release = false
	return reflect.DeepEqual(a, b)
}

// LeaseFence fails closed unless this exact keeper still owns a fresh Lease.
// It cannot retract a remote operation already accepted; the durable uncertain
// journal and provider expiry handle that ambiguity without reminting.
type LeaseFence struct {
	Reader   client.Reader
	Lease    types.NamespacedName
	Identity string
	Clock    clock.PassiveClock
	MaxAge   time.Duration
}

func (f *LeaseFence) Check(ctx context.Context) error {
	if ctx.Err() != nil {
		return errors.New("credential leadership context was cancelled")
	}
	var lease coordinationv1.Lease
	if f.Reader.Get(ctx, f.Lease, &lease) != nil || lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != f.Identity || lease.Spec.RenewTime == nil || lease.Spec.LeaseDurationSeconds == nil {
		return errors.New("credential leadership fence refused the operation")
	}
	maxAge := f.MaxAge
	if maxAge <= 0 {
		maxAge = 10 * time.Second
	}
	maxAge = min(maxAge, time.Duration(*lease.Spec.LeaseDurationSeconds)*time.Second)
	now := f.Clock.Now()
	if !now.Before(lease.Spec.RenewTime.Add(maxAge)) {
		return errors.New("credential leadership lease is stale")
	}
	return nil
}

type credentialInstaller interface {
	Install(context.Context, *corev1.Pod, credentialEntry) error
	Remove(context.Context, *corev1.Pod, credentialEntry) error
}

var errCredentialIncompatible = errors.New("session pod does not support credential grants")

// credentialWorker polls bounded job/journal state on a timer under the same
// leader context as the GitHub keeper. Polling avoids a Secret informer/list
// permission and recovers journal orphans even if their CR was forcibly removed.
type credentialWorker struct {
	Enabled                     bool
	Client                      client.Client
	Namespace, SessionNamespace string
	Journal                     *credentialJournal
	Provider                    *proxmoxProvider
	Installer                   credentialInstaller
	Fence                       func(context.Context) error
	Clock                       clock.Clock
	Log                         logr.Logger
}

func (*credentialWorker) NeedLeaderElection() bool { return true }
func (w *credentialWorker) Start(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := w.tick(ctx); err != nil && ctx.Err() == nil {
			w.Log.Error(err, "credential reconciliation failed")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-w.Clock.After(credentialPoll):
		}
	}
}
func (w *credentialWorker) tick(ctx context.Context) error {
	var jobs v1alpha1.CredentialJobList
	if w.Client.List(ctx, &jobs, client.InNamespace(w.Namespace)) != nil {
		return errors.New("could not list credential jobs")
	}
	present := map[types.UID]bool{}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		present[job.UID] = true
		if err := w.reconcile(ctx, job); err != nil && ctx.Err() == nil {
			w.Log.Error(err, "credential job will retry", "job", job.Name)
		}
	}
	_, entries, err := w.Journal.load(ctx)
	if err != nil {
		return err
	}
	for uid, e := range entries {
		if present[e.JobUID] {
			continue
		}
		// An orphan has no grant audit to update, but its durable provider identity
		// must still be revoked. Keep an uncertain tombstone through fixed expiry.
		if e.Stage != journalCleanup && e.Stage != journalRevoked {
			e.Stage = journalCleanup
			e.TokenSecret = newSecretValue("")
			if err = w.Journal.update(ctx, e.JobUID, &e); err != nil {
				return err
			}
		}
		gone, eerr := w.cleanupProvider(ctx, e)
		if eerr == nil && gone {
			if err = w.Journal.update(ctx, types.UID(uid), nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *credentialWorker) reconcile(ctx context.Context, job *v1alpha1.CredentialJob) error {
	if job.Status.Phase == v1alpha1.CredentialRevoked {
		return w.finish(ctx, job)
	}
	if !controllerutil.ContainsFinalizer(job, v1alpha1.CredentialCleanupFinalizer) {
		original := job.DeepCopy()
		controllerutil.AddFinalizer(job, v1alpha1.CredentialCleanupFinalizer)
		if w.Client.Patch(ctx, job, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})) != nil {
			return errors.New("could not add credential cleanup finalizer")
		}
	}
	_, entries, err := w.Journal.load(ctx)
	if err != nil {
		return err
	}
	e, exists := entries[string(job.UID)]
	if exists && !sameCredentialRequest(e.Spec, job.Spec) {
		return errors.New("credential job differs from its private journal")
	}
	if !exists {
		if checkCredentialSpec(job.Spec) != nil || job.Name != v1alpha1.CredentialJobName(job.Spec.Grant.UID) || job.Spec.Grant.Namespace != w.SessionNamespace || job.Spec.Session.Namespace != w.SessionNamespace {
			return w.noMint(ctx, job, v1alpha1.CredentialInvalidRequest)
		}
		e = credentialEntry{JobUID: job.UID, Spec: job.Spec, Stage: journalIntent}
		// A receipt survives private value loss. Reconstruct only a cleanup
		// tombstone; this execution must never dispatch a second create.
		if job.Status.ProviderID != "" || job.Status.Phase == v1alpha1.CredentialInstalled || job.Status.Phase == v1alpha1.CredentialCleanupPending {
			e.Stage = journalCleanup
			e.Uncertain = true
			e.Failure = v1alpha1.CredentialAmbiguousMint
			if err = w.Journal.update(ctx, job.UID, &e); err != nil {
				return err
			}
			exists = true
		} else if !w.Enabled {
			return w.noMint(ctx, job, v1alpha1.CredentialBackendUnavailable)
		}
	}
	active, pod, err := w.activeTarget(ctx, job)
	if err != nil {
		return err
	}
	if exists && e.Stage == journalRevoked {
		return w.revoked(ctx, job, e)
	}
	if !w.Enabled || job.Spec.Release || !job.DeletionTimestamp.IsZero() || !active || entryExpired(e, w.Clock.Now()) || (exists && (e.Stage == journalCleanup || e.Failure != "")) {
		if !exists {
			return w.noMint(ctx, job, "")
		}
		return w.cleanup(ctx, job, e)
	}
	if e.Stage == journalIntent {
		if exists && e.Uncertain {
			e.Failure = v1alpha1.CredentialAmbiguousMint
			return w.cleanup(ctx, job, e)
		}
		if pod == nil {
			return w.pending(ctx, job)
		}
		if w.Provider.SSH.Ready() != nil {
			return w.noMint(ctx, job, v1alpha1.CredentialBackendUnavailable)
		}
		if err = w.guard(ctx); err != nil {
			return err
		}
		// The intent is conservative: a crash anywhere after this write is an
		// uncertain create. A proved pre-dispatch failure can safely clear it.
		e.Uncertain = true
		if err = w.Journal.update(ctx, job.UID, &e); err != nil {
			return err
		}
		// Publish a nonsecret dispatch marker before the remote call. If the
		// private entry is later lost, the surviving receipt forbids reminting.
		if job.Status.ProviderID == "" {
			job.Status.Phase = v1alpha1.CredentialPending
			job.Status.ProviderID = providerID(e.Spec.Grant.UID)
			if w.Client.Status().Update(ctx, job) != nil {
				return errors.New("could not persist credential dispatch marker")
			}
		}
		opctx, cancel := w.operationContext(ctx)
		result, merr := w.Provider.mint(withSSHCreateGuard(opctx, w.createDispatchGuard(job)), e)
		cancel()
		if merr != nil {
			if !result.PossibleDispatch {
				e.Uncertain = false
				if err = w.Journal.update(ctx, job.UID, &e); err != nil {
					return err
				}
				return errors.New("credential mint did not dispatch and will retry")
			}
			e.Failure = v1alpha1.CredentialAmbiguousMint
			return w.cleanup(ctx, job, e)
		}
		e.TokenID = result.TokenID
		e.TokenSecret = result.TokenSecret
		e.Stage = journalMinted
		e.Uncertain = false
		if err = w.Journal.update(ctx, job.UID, &e); err != nil {
			return err
		}
	}
	// Recheck after SSH/persistence: release or pod replacement may have raced.
	active, pod, err = w.activeTarget(ctx, job)
	if err != nil {
		return err
	}
	var fresh v1alpha1.CredentialJob
	if w.Client.Get(ctx, client.ObjectKeyFromObject(job), &fresh) != nil || fresh.UID != job.UID {
		return errors.New("credential job changed before installation")
	}
	if fresh.Spec.Release || !fresh.DeletionTimestamp.IsZero() || !active || entryExpired(e, w.Clock.Now()) {
		return w.cleanup(ctx, &fresh, e)
	}
	job = &fresh
	if pod == nil || (job.Status.Phase == v1alpha1.CredentialInstalled && job.Status.InstalledPodUID == string(pod.UID)) {
		return nil
	}
	if err = w.guard(ctx); err != nil {
		return err
	}
	opctx, cancel := w.operationContext(ctx)
	err = w.Installer.Install(opctx, pod, e)
	cancel()
	if err != nil {
		if !errors.Is(err, errCredentialIncompatible) {
			return errors.New("credential installation will retry")
		}
		if e.InstallPodUID != string(pod.UID) {
			e.InstallFailures = 0
			e.InstallPodUID = string(pod.UID)
		}
		e.InstallFailures++
		if err = w.Journal.update(ctx, job.UID, &e); err != nil {
			return err
		}
		if e.InstallFailures >= 3 {
			e.Failure = v1alpha1.CredentialIncompatiblePod
			return w.cleanup(ctx, job, e)
		}
		return errors.New("credential installation found an incompatible pod")
	}
	job.Status.Phase = v1alpha1.CredentialInstalled
	job.Status.ProviderID = e.TokenID
	job.Status.InstalledPodUID = string(pod.UID)
	job.Status.InstalledAt = &metav1.Time{Time: w.Clock.Now().UTC().Truncate(time.Second)}
	if w.Client.Status().Update(ctx, job) != nil {
		return errors.New("could not persist credential installation receipt")
	}
	return nil
}

func (w *credentialWorker) pending(ctx context.Context, job *v1alpha1.CredentialJob) error {
	if job.Status.Phase == v1alpha1.CredentialPending {
		return nil
	}
	job.Status.Phase = v1alpha1.CredentialPending
	if w.Client.Status().Update(ctx, job) != nil {
		return errors.New("could not persist credential pending receipt")
	}
	return nil
}
func (w *credentialWorker) noMint(ctx context.Context, job *v1alpha1.CredentialJob, failure v1alpha1.CredentialFailureCode) error {
	job.Status.Phase = v1alpha1.CredentialRevoked
	job.Status.FailureCode = failure
	job.Status.RevokedAt = &metav1.Time{Time: w.Clock.Now().UTC().Truncate(time.Second)}
	if w.Client.Status().Update(ctx, job) != nil {
		return errors.New("could not persist credential refusal receipt")
	}
	return w.finish(ctx, job)
}
func (w *credentialWorker) cleanup(ctx context.Context, job *v1alpha1.CredentialJob, e credentialEntry) error {
	e.Stage = journalCleanup
	e.TokenSecret = newSecretValue("")
	if err := w.Journal.update(ctx, job.UID, &e); err != nil {
		return err
	}
	if job.Status.Phase != v1alpha1.CredentialCleanupPending || job.Status.FailureCode != e.Failure {
		job.Status.Phase = v1alpha1.CredentialCleanupPending
		job.Status.FailureCode = e.Failure
		job.Status.ProviderID = providerID(e.Spec.Grant.UID)
		if w.Client.Status().Update(ctx, job) != nil {
			return errors.New("could not persist credential cleanup receipt")
		}
	}
	gone, err := w.cleanupProvider(ctx, e)
	if err != nil {
		return err
	}
	if !gone {
		return nil
	}
	// Provider revocation comes first. Client removal is best effort and fenced
	// against both a replacement session and a replacement pod/grant.
	if pod, perr := w.targetPod(ctx, e.Spec.Session); perr == nil && pod != nil && w.guard(ctx) == nil {
		opctx, cancel := w.operationContext(ctx)
		_ = w.Installer.Remove(opctx, pod, e)
		cancel()
	}
	e.Stage = journalRevoked
	if err = w.Journal.update(ctx, job.UID, &e); err != nil {
		return err
	}
	return w.revoked(ctx, job, e)
}
func (w *credentialWorker) cleanupProvider(ctx context.Context, e credentialEntry) (bool, error) {
	if e.Stage == journalRevoked {
		return true, nil
	}
	if err := w.guard(ctx); err != nil {
		return false, err
	}
	opctx, cancel := w.operationContext(ctx)
	defer cancel()
	gone, err := w.Provider.remove(opctx, e)
	if err != nil {
		return false, errors.New("credential provider cleanup will retry")
	}
	// An old command may still complete after an absent read. Its fixed expiry
	// bounds that uncertainty; do not claim an early hard revocation.
	return gone && (!e.Uncertain || entryExpired(e, w.Clock.Now())), nil
}
func (w *credentialWorker) revoked(ctx context.Context, job *v1alpha1.CredentialJob, e credentialEntry) error {
	job.Status.Phase = v1alpha1.CredentialRevoked
	job.Status.FailureCode = e.Failure
	job.Status.ProviderID = providerID(e.Spec.Grant.UID)
	job.Status.RevokedAt = &metav1.Time{Time: w.Clock.Now().UTC().Truncate(time.Second)}
	if w.Client.Status().Update(ctx, job) != nil {
		return errors.New("could not persist credential revocation receipt")
	}
	return w.finish(ctx, job)
}
func (w *credentialWorker) finish(ctx context.Context, job *v1alpha1.CredentialJob) error {
	if err := w.Journal.update(ctx, job.UID, nil); err != nil {
		return err
	}
	if !controllerutil.ContainsFinalizer(job, v1alpha1.CredentialCleanupFinalizer) {
		return nil
	}
	original := job.DeepCopy()
	controllerutil.RemoveFinalizer(job, v1alpha1.CredentialCleanupFinalizer)
	if w.Client.Patch(ctx, job, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})) != nil {
		return errors.New("could not lift credential cleanup finalizer")
	}
	return nil
}
func (w *credentialWorker) activeTarget(ctx context.Context, job *v1alpha1.CredentialJob) (bool, *corev1.Pod, error) {
	if checkCredentialSpec(job.Spec) != nil {
		return false, nil, nil
	}
	var grant v1alpha1.AccessGrant
	err := w.Client.Get(ctx, types.NamespacedName{Namespace: job.Spec.Grant.Namespace, Name: job.Spec.Grant.Name}, &grant)
	if apierrors.IsNotFound(err) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, errors.New("could not verify the live credential grant")
	}
	if grant.UID != job.Spec.Grant.UID || grant.Status.Phase != v1alpha1.GrantActive || grant.Status.ExpiresAt == nil || !grant.Status.ExpiresAt.Equal(&job.Spec.ExpiresAt) || grant.Spec.Type != v1alpha1.GrantCredential || grant.Spec.Credential == nil || grant.Spec.Credential.Name != v1alpha1.CredentialProxmox || grant.Spec.Requester.Session != job.Spec.Session.Name || grant.Spec.Requester.SessionUID != job.Spec.Session.UID || grant.Spec.Release || !grant.DeletionTimestamp.IsZero() {
		return false, nil, nil
	}
	var session v1alpha1.AgentSession
	err = w.Client.Get(ctx, types.NamespacedName{Namespace: job.Spec.Session.Namespace, Name: job.Spec.Session.Name}, &session)
	if apierrors.IsNotFound(err) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, errors.New("could not verify the credential session")
	}
	if session.UID != job.Spec.Session.UID || !session.DeletionTimestamp.IsZero() {
		return false, nil, nil
	}
	// Broker pins the accepted execution UID before keeper may mint or install.
	// An absent pin is a create-before-pin crash window; wait without dispatch.
	pin := grant.Annotations[v1alpha1.LabelPrefix+"credential-job-uid"]
	if pin == "" {
		return true, nil, nil
	}
	if pin != string(job.UID) {
		return false, nil, nil
	}
	pod, err := w.targetPod(ctx, job.Spec.Session)
	return true, pod, err
}
func (w *credentialWorker) targetPod(ctx context.Context, ref v1alpha1.CredentialObjectReference) (*corev1.Pod, error) {
	var pod corev1.Pod
	err := w.Client.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &pod)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("could not read credential target pod")
	}
	owner := metav1.GetControllerOf(&pod)
	if owner == nil || owner.UID != ref.UID || pod.Labels[v1alpha1.LabelSession] != ref.Name || pod.Labels[v1alpha1.LabelHold] == "true" || !pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning || pod.UID == "" {
		return nil, nil
	}
	return &pod, nil
}

// createDispatchGuard rechecks approval and immutable identities after SSH
// setup, so a release, replacement or expiry during handshake prevents create.
// The closure holds a copy of this request and is used only for this operation.
func (w *credentialWorker) createDispatchGuard(job *v1alpha1.CredentialJob) func(context.Context) error {
	expected := job.DeepCopy()
	return func(ctx context.Context) error {
		var current v1alpha1.CredentialJob
		if w.Client.Get(ctx, client.ObjectKeyFromObject(expected), &current) != nil || current.UID != expected.UID || !sameCredentialRequest(current.Spec, expected.Spec) || current.Spec.Release || !current.DeletionTimestamp.IsZero() || current.Status.Phase == v1alpha1.CredentialCleanupPending || current.Status.Phase == v1alpha1.CredentialRevoked {
			return errors.New("credential execution request changed before create")
		}
		active, pod, err := w.activeTarget(ctx, &current)
		if err != nil || !active || pod == nil || !w.Clock.Now().Before(current.Spec.ExpiresAt.Time) {
			return errors.New("credential approval or session changed before create")
		}
		return w.guard(ctx)
	}
}

func (w *credentialWorker) guard(ctx context.Context) error {
	if ctx.Err() != nil || w.Fence == nil {
		return errors.New("credential leadership context is unavailable")
	}
	return w.Fence(ctx)
}

// A small monitor cancels an in-flight operation as soon as the Lease fence
// fails, rather than letting a command run through manager shutdown grace.
func (w *credentialWorker) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, credentialAttemptTimeout)
	nextFence := w.Clock.After(time.Second)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-nextFence:
				if w.guard(ctx) != nil {
					cancel()
					return
				}
				nextFence = w.Clock.After(time.Second)
			}
		}
	}()
	return ctx, cancel
}

// credentialExecInstaller uses only stdin, captures no output, and never
// propagates raw transport errors, which may quote payload material.
type credentialExecInstaller struct {
	cfg      *rest.Config
	rest     rest.Interface
	executor func(*rest.Config, *url.URL) (remotecommand.Executor, error)
}

func newCredentialExecInstaller(cfg *rest.Config) (*credentialExecInstaller, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &credentialExecInstaller{cfg: cfg, rest: cs.CoreV1().RESTClient(), executor: func(c *rest.Config, u *url.URL) (remotecommand.Executor, error) {
		return remotecommand.NewSPDYExecutor(c, "POST", u)
	}}, nil
}
func (i *credentialExecInstaller) Install(ctx context.Context, pod *corev1.Pod, e credentialEntry) error {
	if e.TokenSecret.Empty() {
		return errors.New("credential material is missing")
	}
	payload, _ := json.Marshal(struct {
		Version     int    `json:"version"`
		Credential  string `json:"credential"`
		TokenID     string `json:"tokenID"`
		TokenSecret string `json:"tokenSecret"`
	}{1, "proxmox", e.TokenID, e.TokenSecret.Reveal()})
	cmd := []string{"agentd", "ctl", "credential-install", "--name", e.Spec.Grant.Name, "--grant-uid", string(e.Spec.Grant.UID), "--pod-uid", string(pod.UID), "--expires", e.Spec.ExpiresAt.UTC().Format(time.RFC3339)}
	return i.run(ctx, pod, cmd, strings.NewReader(string(payload)))
}
func (i *credentialExecInstaller) Remove(ctx context.Context, pod *corev1.Pod, e credentialEntry) error {
	return i.run(ctx, pod, []string{"agentd", "ctl", "credential-remove", "--name", e.Spec.Grant.Name, "--grant-uid", string(e.Spec.Grant.UID), "--pod-uid", string(pod.UID)}, nil)
}
func (i *credentialExecInstaller) run(ctx context.Context, pod *corev1.Pod, cmd []string, stdin io.Reader) error {
	if pod.UID == "" {
		return errors.New("credential exec target has no UID")
	}
	req := i.rest.Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: "agent", Command: cmd, Stdin: stdin != nil, Stdout: true, Stderr: true}, clientscheme.ParameterCodec)
	exec, err := i.executor(i.cfg, req.URL())
	if err != nil {
		return errors.New("could not open credential exec stream")
	}
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil {
		return nil
	}
	var code utilexec.ExitError
	if errors.As(err, &code) {
		if code.ExitStatus() == 2 || code.ExitStatus() == 3 {
			return errCredentialIncompatible
		}
		return fmt.Errorf("credential exec exited with code %d", code.ExitStatus())
	}
	return errors.New("credential exec failed; output discarded")
}
