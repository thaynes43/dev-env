package keeper

// hw-ssh credential grants (DESIGN-001 D-89): the keeper leases the EXISTING static
// shared ed25519 hw-ssh key into the requesting session's pod, for the grant's TTL.
//
// What this is not. It is not an SSH certificate (the hosts trust no CA) and it is
// not revocable: the key is valid on every host with no cryptographic expiry.
// Removing the lease at expiry, release or session end is neither containment nor
// revocation, because anything in the pod can copy the key while it is installed.
// Real revocation means rotating the key on every host, out of band.
//
// The key is read from one named Secret at install time and is never written to
// the journal, a CredentialJob status, an Event or a log. It is installed by a fixed
// shell script the keeper execs, on stdin, into a per-grant lease directory on the
// pod's tmpfs ($XDG_RUNTIME_DIR/dev-env-hwssh/<grant>/). The script needs no agentd
// support, so it works in older session pods too, and it uses a directory agentd
// never reads, so it cannot disturb agentd's own proxmox credential store.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const (
	// DefaultHWSSHKeySecret is the Secret (in the session namespace) that holds
	// the static key, base64 of the key file, under HWSSHKeyField.
	DefaultHWSSHKeySecret = "dev-env-hw-ssh-secret"
	HWSSHKeyField         = "HW_SSH_PRIVATE_KEY_B64"

	hwsshMaxEncoded = 16 << 10 // the Secret value
	hwsshMaxKey     = 4 << 10  // the decoded key file; an ed25519 OpenSSH key is about 400 bytes
)

// HWSSHGrantOptions enable the static-key lease. Off by default.
type HWSSHGrantOptions struct {
	Enabled bool
	// KeySecret names the key's Secret; its namespace is the session namespace.
	KeySecret string
}

// hwsshKey is the validated key file. Its formatting is redacted.
type hwsshKey struct {
	file        []byte
	fingerprint string
}

func (hwsshKey) String() string   { return redacted }
func (hwsshKey) GoString() string { return redacted }

// loadHWSSHKey reads and checks the key before anything is executed: bounded,
// base64, an unencrypted OpenSSH ed25519 private key. Every error is a fixed
// string; none carries a value.
func loadHWSSHKey(ctx context.Context, r client.Reader, name types.NamespacedName) (hwsshKey, error) {
	var s corev1.Secret
	if r.Get(ctx, name, &s) != nil {
		return hwsshKey{}, errors.New("the hw-ssh key Secret could not be read")
	}
	return parseHWSSHKey(s.Data[HWSSHKeyField])
}

func parseHWSSHKey(encoded []byte) (hwsshKey, error) {
	if len(encoded) == 0 || len(encoded) > hwsshMaxEncoded {
		return hwsshKey{}, errors.New("the hw-ssh key is missing or oversized")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || len(raw) == 0 || len(raw) > hwsshMaxKey || bytes.ContainsRune(raw, 0) {
		return hwsshKey{}, errors.New("the hw-ssh key is not bounded base64")
	}
	if !bytes.HasPrefix(raw, []byte("-----BEGIN OPENSSH PRIVATE KEY-----")) {
		return hwsshKey{}, errors.New("the hw-ssh key is not an OpenSSH private key")
	}
	parsed, err := ssh.ParseRawPrivateKey(raw)
	if err != nil {
		return hwsshKey{}, errors.New("the hw-ssh key is malformed or passphrase protected")
	}
	priv, ok := parsed.(*ed25519.PrivateKey)
	if !ok {
		return hwsshKey{}, errors.New("the hw-ssh key is not ed25519")
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		return hwsshKey{}, errors.New("the hw-ssh key is malformed")
	}
	if raw[len(raw)-1] != '\n' {
		raw = append(raw, '\n')
	}
	return hwsshKey{file: raw, fingerprint: ssh.FingerprintSHA256(pub)}, nil
}

// hwsshScript installs or removes one lease. It reads the key on stdin, takes only
// metadata as positional parameters (never interpolated into the text) and prints
// nothing. Exit 1: retry (pod or lease conflict). Exit 2 or 3: this pod cannot
// take a lease (the keeper counts that as incompatible and gives up after three).
//
//	sh -c <script> dev-env-hwssh install <grant> <grantUID> <podUID> <expiresRFC3339> <fingerprint>
//	sh -c <script> dev-env-hwssh remove  <grant> <grantUID> <podUID>
//
// ($0 is the label dev-env-hwssh; the parameters are $1 and up.) A symlinked lease
// root or directory is refused on install (exit 1) and ignored or unlinked on
// remove, never followed. The manifest is
// written last and is the commit marker: a lease without one is not valid and the
// next install or remove clears it.
const hwsshScript = `set -eu
umask 077
[ "$#" -ge 4 ] && [ "$0" = dev-env-hwssh ] || exit 2
act=$1 name=$2 guid=$3 puid=$4
[ -n "${DEV_ENV_POD_UID:-}" ] || exit 3
[ "$DEV_ENV_POD_UID" = "$puid" ] || exit 1
case $name in grant-*) ;; *) exit 2 ;; esac
case $name$guid$puid in *[!a-z0-9-]*) exit 2 ;; esac
base=${XDG_RUNTIME_DIR:-/dev/shm/run-$(id -u)}/dev-env-hwssh
dir=$base/$name
if [ -L "$base" ]; then
  [ "$act" = remove ] && exit 0
  exit 1
fi
if [ -L "$dir" ]; then
  [ "$act" = remove ] && { rm -f "$dir"; exit 0; }
  exit 1
fi
mine() { [ -f "$dir/manifest.json" ] && grep -q "\"grantUID\":\"$guid\"" "$dir/manifest.json" && grep -q "\"podUID\":\"$puid\"" "$dir/manifest.json"; }
wipe() { rm -f "$dir/key" "$dir"/.key.* "$dir/manifest.json" "$dir"/.manifest.* && rmdir "$dir"; }
case $act in
remove)
  [ -d "$dir" ] || exit 0
  if [ -f "$dir/manifest.json" ]; then mine || exit 0; fi
  wipe
  ;;
install)
  [ "$#" -eq 6 ] || exit 2
  exp=$5 fp=$6
  case $exp in ????-??-??T??:??:??Z) ;; *) exit 2 ;; esac
  case $fp in SHA256:*) ;; *) exit 2 ;; esac
  case $exp$fp in *[!A-Za-z0-9:+/=_-]*) exit 2 ;; esac
  mkdir -p "$base"; chmod 700 "$base"
  if [ -d "$dir" ]; then
    if mine && [ -s "$dir/key" ]; then exit 0; fi
    if [ -f "$dir/manifest.json" ]; then exit 1; fi
    wipe
  fi
  mkdir -m 700 "$dir"
  cat > "$dir/.key.$$"
  [ -s "$dir/.key.$$" ] || { wipe; exit 1; }
  chmod 600 "$dir/.key.$$"
  mv "$dir/.key.$$" "$dir/key"
  printf '{"v":1,"grant":"%s","grantUID":"%s","podUID":"%s","expires":"%s","installedAt":"%s","fingerprint":"%s"}\n' \
    "$name" "$guid" "$puid" "$exp" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$fp" > "$dir/.manifest.$$"
  mv "$dir/.manifest.$$" "$dir/manifest.json"
  ;;
*) exit 2 ;;
esac
`

// hwsshInstaller installs the lease through the keeper's existing exec path. Two
// fences guard the exec: the keeper re-reads the pod from the API right before it
// and checks its UID, and the script checks $DEV_ENV_POD_UID inside the pod.
type hwsshInstaller struct {
	exec      *credentialExecInstaller
	reader    client.Reader // uncached: the Secret and the pod are read by name
	keySecret types.NamespacedName
}

func (i *hwsshInstaller) fencePod(ctx context.Context, pod *corev1.Pod, e credentialEntry) error {
	var live corev1.Pod
	if i.reader.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, &live) != nil {
		return errors.New("credential target pod could not be re-read")
	}
	owner := metav1.GetControllerOf(&live)
	if live.UID == "" || live.UID != pod.UID || owner == nil || owner.UID != e.Spec.Session.UID || live.Labels[v1alpha1.LabelSession] != e.Spec.Session.Name ||
		live.Labels[v1alpha1.LabelHold] == "true" || !live.DeletionTimestamp.IsZero() || live.Status.Phase != corev1.PodRunning {
		return errors.New("credential target pod changed")
	}
	return nil
}

func (i *hwsshInstaller) Install(ctx context.Context, pod *corev1.Pod, e credentialEntry) error {
	if e.Spec.Credential != v1alpha1.CredentialHWSSH {
		return errors.New("not an hw-ssh credential")
	}
	key, err := loadHWSSHKey(ctx, i.reader, i.keySecret)
	if err != nil {
		return err
	}
	if err := i.fencePod(ctx, pod, e); err != nil {
		return err
	}
	cmd := []string{"sh", "-c", hwsshScript, "dev-env-hwssh", "install", e.Spec.Grant.Name, string(e.Spec.Grant.UID), string(pod.UID),
		e.Spec.ExpiresAt.UTC().Format(time.RFC3339), key.fingerprint}
	return i.exec.run(ctx, pod, cmd, bytes.NewReader(key.file))
}

func (i *hwsshInstaller) Remove(ctx context.Context, pod *corev1.Pod, e credentialEntry) error {
	if err := i.fencePod(ctx, pod, e); err != nil {
		return err
	}
	return i.exec.run(ctx, pod, []string{"sh", "-c", hwsshScript, "dev-env-hwssh", "remove", e.Spec.Grant.Name, string(e.Spec.Grant.UID), string(pod.UID)}, nil)
}

// leaseRetry is the in-memory backoff of one failed lease install or removal. It
// only delays; the job's status and finalizer are the durable state.
type leaseRetry struct {
	failures int
	next     time.Time
}

func (w *credentialWorker) leaseWait(uid types.UID) bool {
	r, ok := w.leases[uid]
	return ok && w.Clock.Now().Before(r.next)
}

func (w *credentialWorker) leaseFailed(uid types.UID) {
	if w.leases == nil {
		w.leases = map[types.UID]leaseRetry{}
	}
	r := w.leases[uid]
	r.failures++
	d := credentialPoll << min(r.failures, 5) // 30s, 1m, 2m, 4m, 8m, then 8m
	r.next = w.Clock.Now().Add(min(d, 8*time.Minute))
	w.leases[uid] = r
}

func (w *credentialWorker) leaseOK(uid types.UID) { delete(w.leases, uid) }

// reconcileLease runs one hw-ssh job. It keeps no journal entry: the key is read
// from its Secret at install time, nothing exists at a provider, and the install
// and remove are idempotent, so the job's status and its cleanup finalizer carry
// crash recovery. A crash between the install and the status write is cleaned up
// by the next reconcile, which removes the lease from the live pod on release,
// expiry or session end whatever the recorded phase. The finalizer is lifted, and
// the phase becomes Revoked, only after the lease is removed or its pod is gone.
func (w *credentialWorker) reconcileLease(ctx context.Context, job *v1alpha1.CredentialJob) error {
	if job.Status.Phase == v1alpha1.CredentialRevoked {
		return w.finishLease(ctx, job)
	}
	if !controllerutil.ContainsFinalizer(job, v1alpha1.CredentialCleanupFinalizer) {
		original := job.DeepCopy()
		controllerutil.AddFinalizer(job, v1alpha1.CredentialCleanupFinalizer)
		if w.Client.Patch(ctx, job, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})) != nil {
			return errors.New("could not add credential cleanup finalizer")
		}
	}
	if checkCredentialSpec(job.Spec) != nil || job.Name != v1alpha1.CredentialJobName(job.Spec.Grant.UID) || job.Spec.Grant.Namespace != w.SessionNamespace || job.Spec.Session.Namespace != w.SessionNamespace {
		return w.leaseRevoked(ctx, job, v1alpha1.CredentialInvalidRequest)
	}
	active, pod, err := w.activeTarget(ctx, job)
	if err != nil {
		return err
	}
	ended := !w.HWSSHEnabled || job.Spec.Release || !job.DeletionTimestamp.IsZero() || !active || !w.Clock.Now().Before(job.Spec.ExpiresAt.Time)
	if ended || job.Status.Phase == v1alpha1.CredentialCleanupPending {
		return w.leaseCleanup(ctx, job, !w.HWSSHEnabled && job.Status.Phase == "")
	}
	if pod == nil {
		return w.pending(ctx, job)
	}
	if job.Status.Phase == v1alpha1.CredentialInstalled && job.Status.InstalledPodUID == string(pod.UID) {
		return nil
	}
	if w.leaseWait(job.UID) {
		return nil
	}
	// Recheck right before the exec: release or a replacement may have raced.
	var fresh v1alpha1.CredentialJob
	if w.Client.Get(ctx, client.ObjectKeyFromObject(job), &fresh) != nil || fresh.UID != job.UID {
		return errors.New("credential job changed before installation")
	}
	if fresh.Spec.Release || !fresh.DeletionTimestamp.IsZero() {
		return w.leaseCleanup(ctx, &fresh, false)
	}
	job = &fresh
	if err = w.guard(ctx); err != nil {
		return err
	}
	opctx, cancel := w.operationContext(ctx)
	err = w.Installer.Install(opctx, pod, credentialEntry{JobUID: job.UID, Spec: job.Spec})
	cancel()
	if err != nil {
		if errors.Is(err, errCredentialIncompatible) {
			// Exit 2 or 3 is deterministic: this pod cannot take a lease.
			job.Status.FailureCode = v1alpha1.CredentialIncompatiblePod
			return w.leaseCleanup(ctx, job, false)
		}
		w.leaseFailed(job.UID)
		return errors.New("credential installation will retry")
	}
	w.leaseOK(job.UID)
	job.Status.Phase = v1alpha1.CredentialInstalled
	job.Status.FailureCode = ""
	job.Status.ProviderID = providerIDOf(job.Spec)
	job.Status.InstalledPodUID = string(pod.UID)
	job.Status.InstalledAt = &metav1.Time{Time: w.Clock.Now().UTC().Truncate(time.Second)}
	if w.Client.Status().Update(ctx, job) != nil {
		return errors.New("could not persist credential installation receipt")
	}
	return nil
}

// leaseCleanup removes the lease and only then ends the job. A failed removal keeps
// the phase CleanupPending and the finalizer, and is retried with backoff. For
// hw-ssh, Revoked means only that the lease was removed from the pod (or the pod is
// gone); it is not revocation of the key (D-89).
func (w *credentialWorker) leaseCleanup(ctx context.Context, job *v1alpha1.CredentialJob, neverRan bool) error {
	failure := job.Status.FailureCode
	if neverRan {
		failure = v1alpha1.CredentialBackendUnavailable
	}
	if job.Status.Phase != v1alpha1.CredentialCleanupPending || job.Status.FailureCode != failure {
		job.Status.Phase = v1alpha1.CredentialCleanupPending
		job.Status.FailureCode = failure
		job.Status.ProviderID = providerIDOf(job.Spec)
		if w.Client.Status().Update(ctx, job) != nil {
			return errors.New("could not persist credential cleanup receipt")
		}
	}
	done, err := w.removeLease(ctx, job)
	if err != nil {
		return err
	}
	if !done {
		return errors.New("lease removal is pending and will retry")
	}
	return w.leaseRevoked(ctx, job, failure)
}

// removeLease reports whether the lease is gone from every pod it could be in. It
// asks the API about the session's pod and counts the removal complete only when
// the exec succeeds, or the pod is gone (not found, another session's, or in a
// terminal phase). It always targets the live pod, never the UID recorded at the
// last receipt: after a lost receipt the lease may be in a replacement pod.
func (w *credentialWorker) removeLease(ctx context.Context, job *v1alpha1.CredentialJob) (bool, error) {
	ref := job.Spec.Session
	var pod corev1.Pod
	err := w.Client.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &pod)
	if apierrors.IsNotFound(err) {
		w.leaseOK(job.UID)
		return true, nil
	}
	if err != nil {
		return false, errors.New("could not read credential target pod")
	}
	owner := metav1.GetControllerOf(&pod)
	if owner == nil || owner.UID != ref.UID || pod.Labels[v1alpha1.LabelSession] != ref.Name || pod.Labels[v1alpha1.LabelHold] == "true" ||
		pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		w.leaseOK(job.UID)
		return true, nil
	}
	if !pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning || pod.UID == "" || w.leaseWait(job.UID) {
		return false, nil
	}
	if err := w.guard(ctx); err != nil {
		return false, err
	}
	opctx, cancel := w.operationContext(ctx)
	err = w.Installer.Remove(opctx, &pod, credentialEntry{JobUID: job.UID, Spec: job.Spec})
	cancel()
	if err != nil {
		w.leaseFailed(job.UID)
		return false, nil
	}
	w.leaseOK(job.UID)
	return true, nil
}

func (w *credentialWorker) leaseRevoked(ctx context.Context, job *v1alpha1.CredentialJob, failure v1alpha1.CredentialFailureCode) error {
	job.Status.Phase = v1alpha1.CredentialRevoked
	job.Status.FailureCode = failure
	job.Status.ProviderID = providerIDOf(job.Spec)
	job.Status.RevokedAt = &metav1.Time{Time: w.Clock.Now().UTC().Truncate(time.Second)}
	if w.Client.Status().Update(ctx, job) != nil {
		return errors.New("could not persist credential revocation receipt")
	}
	return w.finishLease(ctx, job)
}

func (w *credentialWorker) finishLease(ctx context.Context, job *v1alpha1.CredentialJob) error {
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

// routedInstaller sends a job to the installer of its credential.
type routedInstaller struct{ proxmox, hwssh credentialInstaller }

func (r routedInstaller) pick(e credentialEntry) credentialInstaller {
	if e.Spec.Credential == v1alpha1.CredentialHWSSH && r.hwssh != nil {
		return r.hwssh
	}
	return r.proxmox
}
func (r routedInstaller) Install(ctx context.Context, p *corev1.Pod, e credentialEntry) error {
	return r.pick(e).Install(ctx, p, e)
}
func (r routedInstaller) Remove(ctx context.Context, p *corev1.Pod, e credentialEntry) error {
	return r.pick(e).Remove(ctx, p, e)
}
