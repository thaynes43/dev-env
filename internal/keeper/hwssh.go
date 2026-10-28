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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
// ($0 is the label dev-env-hwssh; the parameters are $1 and up.) The manifest is
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
