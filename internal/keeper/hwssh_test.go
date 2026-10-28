package keeper

// hw-ssh lease tests (D-89). Every key here is synthetic and generated at test
// time; nothing touches real hardware, the real key, or a live pod.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const hwsshSecretName = "dev-env-hw-ssh-secret"

// syntheticKey returns an OpenSSH ed25519 private key file and its fingerprint.
func syntheticKey(t *testing.T, passphrase string) (file []byte, fingerprint string) {
	t.Helper()
	return syntheticKeyComment(t, passphrase, "synthetic")
}

// syntheticKeyComment is a valid-shaped key with a chosen comment, to size it.
func syntheticKeyComment(t *testing.T, passphrase, comment string) (file []byte, fingerprint string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, comment)
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, comment, []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block), ssh.FingerprintSHA256(sp)
}

// Each size bound bites on a key that is otherwise valid.
func TestHWSSHKeyBoundsBiteOnValidShapedKeys(t *testing.T) {
	enc := func(b []byte) []byte { return []byte(base64.StdEncoding.EncodeToString(b)) }
	roomy, _ := syntheticKeyComment(t, "", strings.Repeat("c", 2000)) // about 2.5 KiB decoded
	if _, err := parseHWSSHKey(enc(roomy)); err != nil {
		t.Fatalf("a key inside the bounds was refused: %v", err)
	}
	big, _ := syntheticKeyComment(t, "", strings.Repeat("c", 5000)) // decoded over 4 KiB, encoded under 16 KiB
	if len(big) <= hwsshMaxKey || len(enc(big)) > hwsshMaxEncoded {
		t.Fatalf("fixture sizes: %d decoded, %d encoded", len(big), len(enc(big)))
	}
	if _, err := parseHWSSHKey(enc(big)); err == nil || !strings.Contains(err.Error(), "not bounded base64") {
		t.Fatalf("the decoded-size bound did not bite: %v", err)
	}
	huge, _ := syntheticKeyComment(t, "", strings.Repeat("c", 14000)) // encoded over 16 KiB
	if len(enc(huge)) <= hwsshMaxEncoded {
		t.Fatalf("fixture sizes: %d encoded", len(enc(huge)))
	}
	if _, err := parseHWSSHKey(enc(huge)); err == nil || !strings.Contains(err.Error(), "missing or oversized") {
		t.Fatalf("the encoded-size bound did not bite: %v", err)
	}
}

// The installer never follows a symlinked lease root or directory.
func TestHWSSHScriptRefusesSymlinkedLeasePaths(t *testing.T) {
	key, fp := syntheticKey(t, "")
	const grant, guid, puid = "grant-1028-000000-abcd", "10000000-0000-4000-8000-000000000001", "40000000-0000-4000-8000-000000000001"
	exp := "2026-10-28T01:00:00Z"
	install := func(xdg string) int { return runScript(t, xdg, puid, key, "install", grant, guid, puid, exp, fp) }
	remove := func(xdg string) int { return runScript(t, xdg, puid, nil, "remove", grant, guid, puid) }
	empty := func(t *testing.T, dir string) {
		t.Helper()
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Fatalf("%s was written through a symlink: %v", dir, entries)
		}
	}

	t.Run("symlinked root", func(t *testing.T) {
		xdg, target := t.TempDir(), t.TempDir()
		if err := os.Symlink(target, filepath.Join(xdg, "dev-env-hwssh")); err != nil {
			t.Fatal(err)
		}
		if code := install(xdg); code != 1 {
			t.Fatalf("install exit %d", code)
		}
		empty(t, target)
		if code := remove(xdg); code != 0 {
			t.Fatalf("remove exit %d", code)
		}
		empty(t, target)
	})
	t.Run("symlinked grant directory", func(t *testing.T) {
		xdg, target := t.TempDir(), t.TempDir()
		root := filepath.Join(xdg, "dev-env-hwssh")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, grant)); err != nil {
			t.Fatal(err)
		}
		if code := install(xdg); code != 1 {
			t.Fatalf("install exit %d", code)
		}
		empty(t, target)
		if code := remove(xdg); code != 0 {
			t.Fatalf("remove exit %d", code)
		}
		empty(t, target) // the symlink is unlinked, its target is not touched
		if _, err := os.Lstat(filepath.Join(root, grant)); err == nil {
			t.Fatal("the symlink was left in place")
		}
	})
}

func TestHWSSHKeyIsBoundedAndValidatedBeforeAnyExec(t *testing.T) {
	file, fp := syntheticKey(t, "")
	good, err := parseHWSSHKey([]byte(base64.StdEncoding.EncodeToString(file)))
	if err != nil || good.fingerprint != fp || !bytes.Equal(good.file, file) {
		t.Fatalf("valid key refused: %v", err)
	}
	if strings.Contains(fmt.Sprintf("%v %+v %#v", good, good, good), string(file[40:80])) {
		t.Fatal("the key formats itself")
	}
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	rsaBlock, _ := ssh.MarshalPrivateKey(rsaKey, "")
	protected, _ := syntheticKey(t, "pass")
	for name, in := range map[string][]byte{
		"empty":            nil,
		"oversized secret": bytes.Repeat([]byte("A"), hwsshMaxEncoded+1),
		"oversized key":    []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("A"), hwsshMaxKey+1))),
		"not base64":       []byte("!!not base64!!"),
		"not an OpenSSH":   []byte(base64.StdEncoding.EncodeToString([]byte("hello"))),
		"rsa":              []byte(base64.StdEncoding.EncodeToString(pem.EncodeToMemory(rsaBlock))),
		"passphrase":       []byte(base64.StdEncoding.EncodeToString(protected)),
		"truncated":        []byte(base64.StdEncoding.EncodeToString(file[:len(file)/2])),
		"nul byte":         []byte(base64.StdEncoding.EncodeToString(append([]byte("-----BEGIN OPENSSH PRIVATE KEY-----\x00"), file...))),
	} {
		if _, err := parseHWSSHKey(in); err == nil {
			t.Errorf("%s was accepted", name)
		} else if strings.Contains(err.Error(), string(file[40:80])) {
			t.Errorf("%s error leaks key text", name)
		}
	}
}

// runScript executes the real installer script, exactly as the pod's sh would.
func runScript(t *testing.T, xdg, podEnv string, stdin []byte, args ...string) int {
	t.Helper()
	cmd := exec.Command("sh", append([]string{"-c", hwsshScript, "dev-env-hwssh"}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "XDG_RUNTIME_DIR=" + xdg}
	if podEnv != "" {
		cmd.Env = append(cmd.Env, "DEV_ENV_POD_UID="+podEnv)
	}
	cmd.Stdin = bytes.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if out.Len() != 0 {
		t.Fatalf("the script printed output: %q", out.String())
	}
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	t.Fatal(err)
	return -1
}

func TestHWSSHScriptLeasesToPrivateTmpfsFiles(t *testing.T) {
	xdg := t.TempDir()
	key, fp := syntheticKey(t, "")
	const grant, guid, puid = "grant-1028-000000-abcd", "10000000-0000-4000-8000-000000000001", "40000000-0000-4000-8000-000000000001"
	exp := "2026-10-28T01:00:00Z"
	dir := filepath.Join(xdg, "dev-env-hwssh", grant)
	install := func(pod string, stdin []byte) int {
		return runScript(t, xdg, pod, stdin, "install", grant, guid, puid, exp, fp)
	}
	// The in-pod fence: the wrong or unset pod UID writes nothing.
	if code := install("40000000-0000-4000-8000-000000000999", key); code != 1 {
		t.Fatalf("pod mismatch exit %d", code)
	}
	if code := install("", key); code != 3 {
		t.Fatalf("no DEV_ENV_POD_UID exit %d", code)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("a refused install left a lease")
	}
	if code := install(puid, key); code != 0 {
		t.Fatalf("install exit %d", code)
	}
	for path, mode := range map[string]os.FileMode{filepath.Dir(dir): 0o700, dir: 0o700, filepath.Join(dir, "key"): 0o600, filepath.Join(dir, "manifest.json"): 0o600} {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm() != mode {
			t.Fatalf("%s: %v mode %v, want %v", path, err, fi, mode)
		}
	}
	got, _ := os.ReadFile(filepath.Join(dir, "key"))
	if !bytes.Equal(got, key) {
		t.Fatal("the leased key differs")
	}
	var m map[string]any
	raw, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if json.Unmarshal(raw, &m) != nil || m["grantUID"] != guid || m["podUID"] != puid || m["expires"] != exp || m["fingerprint"] != fp || m["grant"] != grant {
		t.Fatalf("manifest %s", raw)
	}
	if bytes.Contains(raw, key[40:80]) {
		t.Fatal("the manifest holds key material")
	}
	// The lease follows XDG_RUNTIME_DIR (tmpfs in the pod); the /dev/shm/run-<uid>
	// fallback is only for an unset variable, so nothing went there.
	if _, err := os.Stat(filepath.Join("/dev/shm", fmt.Sprintf("run-%d", os.Getuid()), "dev-env-hwssh", grant)); err == nil {
		t.Fatal("the lease was written to the fallback directory although XDG_RUNTIME_DIR was set")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Fatalf("stray files: %v", entries)
	}
	if code := install(puid, []byte("different")); code != 0 {
		t.Fatalf("repeat install exit %d", code)
	}
	if again, _ := os.ReadFile(filepath.Join(dir, "key")); !bytes.Equal(again, key) {
		t.Fatal("a repeat install replaced the key")
	}
	// Bad metadata and an empty stream never lease.
	if code := runScript(t, xdg, puid, key, "install", "not-a-grant", guid, puid, exp, fp); code != 2 {
		t.Fatalf("bad name exit %d", code)
	}
	if code := runScript(t, xdg, puid, key, "install", grant+"x", guid, puid, "tomorrow", fp); code != 2 {
		t.Fatalf("bad expiry exit %d", code)
	}
	if code := runScript(t, xdg, puid, nil, "install", "grant-empty", guid, puid, exp, fp); code != 1 {
		t.Fatalf("empty key exit %d", code)
	}
	if _, err := os.Stat(filepath.Join(xdg, "dev-env-hwssh", "grant-empty")); err == nil {
		t.Fatal("an empty key left a lease")
	}
	// Remove: fenced by pod, then by grant UID; idempotent.
	if code := runScript(t, xdg, "40000000-0000-4000-8000-000000000999", nil, "remove", grant, guid, puid); code != 1 {
		t.Fatalf("remove from a replacement pod exit %d", code)
	}
	if code := runScript(t, xdg, puid, nil, "remove", grant, "10000000-0000-4000-8000-0000000000ff", puid); code != 0 {
		t.Fatalf("remove for another grant UID exit %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "key")); err != nil {
		t.Fatal("a stale remove deleted another grant UID's lease")
	}
	for range 2 {
		if code := runScript(t, xdg, puid, nil, "remove", grant, guid, puid); code != 0 {
			t.Fatalf("remove exit %d", code)
		}
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("remove left the lease")
	}
}

type hwsshExec struct {
	mu          sync.Mutex
	commands    [][]string
	stdins      [][]byte
	err         error
	cur         []string // the command being streamed
	failRemoves int      // this many remove commands fail before one succeeds
	exit        int      // when set, a failed command exits with this code
}

func (e *hwsshExec) Stream(o remotecommand.StreamOptions) error {
	return e.StreamWithContext(context.Background(), o)
}
func (e *hwsshExec) StreamWithContext(_ context.Context, o remotecommand.StreamOptions) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	var in []byte
	if o.Stdin != nil {
		in, _ = io.ReadAll(o.Stdin)
	}
	e.stdins = append(e.stdins, in)
	if len(e.cur) > 4 && e.cur[4] == "remove" && e.failRemoves > 0 {
		e.failRemoves--
		return errors.New("remove failed: transport broke")
	}
	if e.exit != 0 && len(e.cur) > 4 && (e.cur[4] == "install" || e.cur[4] == "remove") {
		return utilexec.CodeExitError{Err: errors.New("exit"), Code: e.exit}
	}
	return e.err
}

// hwsshHarness is the worker with the real hw-ssh installer over a fake exec.
type hwsshHarness struct {
	credentialFixture
	exec    *hwsshExec
	key     []byte
	secret  string // the base64 Secret value
	logs    *bytes.Buffer
	execURL []*url.URL
}

func newHWSSHHarness(t *testing.T) *hwsshHarness {
	t.Helper()
	f := newCredentialFixture(t)
	ctx := context.Background()
	key, _ := syntheticKey(t, "")
	h := &hwsshHarness{credentialFixture: f, exec: &hwsshExec{}, key: key, secret: base64.StdEncoding.EncodeToString(key), logs: &bytes.Buffer{}}
	// Make the job and grant hw-ssh.
	var job v1alpha1.CredentialJob
	if err := f.c.Get(ctx, client.ObjectKeyFromObject(f.job), &job); err != nil {
		t.Fatal(err)
	}
	job.Spec.Credential = v1alpha1.CredentialHWSSH
	if err := f.c.Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	f.job.Spec = job.Spec
	var grant v1alpha1.AccessGrant
	if err := f.c.Get(ctx, types.NamespacedName{Namespace: "dev-agents", Name: "grant-example"}, &grant); err != nil {
		t.Fatal(err)
	}
	grant.Spec.Credential.Name = v1alpha1.CredentialHWSSH
	if err := f.c.Update(ctx, &grant); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: hwsshSecretName}, Data: map[string][]byte{HWSSHKeyField: []byte(h.secret)}}); err != nil {
		t.Fatal(err)
	}
	ex, err := newCredentialExecInstaller(&rest.Config{Host: "https://api.example"})
	if err != nil {
		t.Fatal(err)
	}
	ex.executor = func(_ *rest.Config, u *url.URL) (remotecommand.Executor, error) {
		h.execURL = append(h.execURL, u)
		h.exec.commands = append(h.exec.commands, u.Query()["command"])
		h.exec.cur = u.Query()["command"]
		return h.exec, nil
	}
	hw := &hwsshInstaller{exec: ex, reader: f.c, keySecret: types.NamespacedName{Namespace: "dev-agents", Name: hwsshSecretName}}
	h.w.HWSSHEnabled = true
	h.w.Enabled = false // the PVE backend is off; hw-ssh must not need it
	h.w.Installer = routedInstaller{proxmox: f.installer, hwssh: hw}
	h.w.Log = funcr.New(func(prefix, args string) { h.logs.WriteString(prefix + args + "\n") }, funcr.Options{Verbosity: 9})
	return h
}

// A valid approval reaches installation: the exec carries metadata in argv and the
// key only on stdin, the receipt names the lease, and no PVE call happens.
func TestHWSSHJobReachesInstallationWithoutAProviderMint(t *testing.T) {
	h := newHWSSHHarness(t)
	if err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	st := h.status(t)
	if st.Phase != v1alpha1.CredentialInstalled || st.InstalledPodUID != string(testPodUID) || st.ProviderID != "hw-ssh-lease-"+string(testGrantUID) {
		t.Fatalf("receipt %+v", st)
	}
	if h.provider.creates != 0 || h.provider.deletes != 0 || h.installer.installs != 0 {
		t.Fatal("the PVE path ran for an hw-ssh job")
	}
	if len(h.exec.commands) != 1 || len(h.exec.stdins) != 1 {
		t.Fatalf("exec calls %d", len(h.exec.commands))
	}
	cmd := h.exec.commands[0]
	_, fp := parseFP(t, h.key)
	want := []string{"sh", "-c", hwsshScript, "dev-env-hwssh", "install", "grant-example", string(testGrantUID), string(testPodUID), h.job.Spec.ExpiresAt.UTC().Format(time.RFC3339), fp}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("argv %q", cmd)
	}
	if !bytes.Equal(h.exec.stdins[0], h.key) {
		t.Fatal("the key did not go on stdin")
	}
	if tty := h.execURL[0].Query().Get("tty"); tty == "true" {
		t.Fatal("a terminal was requested")
	}
	if _, ok := h.entry(t); ok {
		t.Fatal("an hw-ssh lease wrote an entry in the shared proxmox journal")
	}
}

func parseFP(t *testing.T, file []byte) (hwsshKey, string) {
	t.Helper()
	k, err := parseHWSSHKey([]byte(base64.StdEncoding.EncodeToString(file)))
	if err != nil {
		t.Fatal(err)
	}
	return k, k.fingerprint
}

// The keeper re-reads the pod from the API right before the exec.
func TestHWSSHInstallRefusesAReplacedOrChangedPodBeforeTheExec(t *testing.T) {
	for name, edit := range map[string]func(*corev1.Pod){
		"replaced pod UID": func(p *corev1.Pod) { p.UID = "40000000-0000-4000-8000-000000000002" },
		"deleting":         func(p *corev1.Pod) { p.Finalizers = []string{"x"} }, // deleted below
		"not running":      func(p *corev1.Pod) { p.Status.Phase = corev1.PodPending },
		"hold pod":         func(p *corev1.Pod) { p.Labels[v1alpha1.LabelHold] = "true" },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHWSSHHarness(t)
			ex := h.w.Installer.(routedInstaller).hwssh.(*hwsshInstaller)
			stale := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: "session-example", UID: testPodUID}}
			var live corev1.Pod
			if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: "dev-agents", Name: "session-example"}, &live); err != nil {
				t.Fatal(err)
			}
			edit(&live)
			status := live.Status
			if err := h.c.Update(context.Background(), &live); err != nil {
				t.Fatal(err)
			}
			live.Status = status
			if err := h.c.Status().Update(context.Background(), &live); err != nil {
				t.Fatal(err)
			}
			if name == "deleting" {
				if err := h.c.Delete(context.Background(), &live); err != nil {
					t.Fatal(err)
				}
			}
			e := credentialEntry{JobUID: testJobUID, Spec: h.job.Spec, Stage: journalMinted, TokenID: "hw-ssh-lease-" + string(testGrantUID)}
			if err := ex.Install(context.Background(), stale, e); err == nil {
				t.Fatal("installed into a pod the API no longer shows as the target")
			}
			if err := ex.Remove(context.Background(), stale, e); err == nil {
				t.Fatal("removed through a pod the API no longer shows as the target")
			}
			if len(h.exec.commands) != 0 {
				t.Fatal("the exec ran after a failed pod fence")
			}
		})
	}
}

func TestHWSSHInstallRefusesABadKeyBeforeTheExec(t *testing.T) {
	for name, value := range map[string]string{"oversized": strings.Repeat("A", hwsshMaxEncoded+1), "malformed": "bm90IGEga2V5", "missing": ""} {
		t.Run(name, func(t *testing.T) {
			h := newHWSSHHarness(t)
			var s corev1.Secret
			if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: "dev-agents", Name: hwsshSecretName}, &s); err != nil {
				t.Fatal(err)
			}
			s.Data[HWSSHKeyField] = []byte(value)
			if err := h.c.Update(context.Background(), &s); err != nil {
				t.Fatal(err)
			}
			if err := h.reconcile(t); err == nil {
				t.Fatal("a bad key was leased")
			}
			if len(h.exec.commands) != 0 || h.status(t).Phase == v1alpha1.CredentialInstalled {
				t.Fatal("the exec ran with a bad key")
			}
		})
	}
}

// Default off: with the hw-ssh flag off a new job is refused and nothing is installed or
// read, even with the PVE backend on. The only exec is the idempotent removal that
// settles a crash between an install and its status write.
func TestHWSSHJobIsRefusedWhenOnlyTheProxmoxFlagIsOn(t *testing.T) {
	h := newHWSSHHarness(t)
	h.w.HWSSHEnabled, h.w.Enabled = false, true
	if err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	st := h.status(t)
	if st.Phase != v1alpha1.CredentialRevoked || st.FailureCode != v1alpha1.CredentialBackendUnavailable {
		t.Fatalf("flag off: %+v", st)
	}
	for _, c := range h.exec.commands {
		if c[4] != "remove" {
			t.Fatalf("a flag-off job ran %q", c[4])
		}
	}
	for _, in := range h.exec.stdins {
		if len(in) != 0 {
			t.Fatal("key bytes went to a pod with the flag off")
		}
	}
	if h.provider.creates != 0 || h.installer.installs != 0 {
		t.Fatal("the PVE path ran for an hw-ssh job")
	}
}

// Proxmox on, hw-ssh off: a proxmox job is unaffected.
func TestProxmoxJobIsUnaffectedWhenHWSSHIsOff(t *testing.T) {
	f := newCredentialFixture(t)
	f.w.Installer = routedInstaller{proxmox: f.installer, hwssh: &hwsshInstaller{}}
	f.w.Enabled, f.w.HWSSHEnabled = true, false
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if st := f.status(t); st.Phase != v1alpha1.CredentialInstalled || f.provider.creates != 1 || f.installer.installs != 1 {
		t.Fatalf("proxmox with hw-ssh off: %+v creates %d", st, f.provider.creates)
	}
}

// Expiry, release and session end each remove the lease; none touches the hosts.
func TestHWSSHCleanupOnReleaseExpiryAndSessionEnd(t *testing.T) {
	removes := func(h *hwsshHarness) int {
		n := 0
		for _, c := range h.exec.commands {
			if len(c) > 4 && c[4] == "remove" {
				n++
			}
		}
		return n
	}
	for name, end := range map[string]func(*testing.T, *hwsshHarness){
		"release": func(t *testing.T, h *hwsshHarness) {
			var job v1alpha1.CredentialJob
			if err := h.c.Get(context.Background(), client.ObjectKeyFromObject(h.job), &job); err != nil {
				t.Fatal(err)
			}
			job.Spec.Release = true
			if err := h.c.Update(context.Background(), &job); err != nil {
				t.Fatal(err)
			}
		},
		"expiry": func(t *testing.T, h *hwsshHarness) { h.clock.Step(2 * time.Hour) },
		"session end": func(t *testing.T, h *hwsshHarness) {
			var g v1alpha1.AccessGrant
			if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: "dev-agents", Name: "grant-example"}, &g); err != nil {
				t.Fatal(err)
			}
			g.Status.Phase = v1alpha1.GrantReleased // the broker ends the grant when the session ends
			if err := h.c.Status().Update(context.Background(), &g); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHWSSHHarness(t)
			if err := h.reconcile(t); err != nil {
				t.Fatal(err)
			}
			end(t, h)
			if err := h.reconcile(t); err != nil {
				t.Fatal(err)
			}
			if st := h.status(t); st.Phase != v1alpha1.CredentialRevoked || st.RevokedAt == nil {
				t.Fatalf("not cleaned: %+v", st)
			}
			if removes(h) != 1 || h.provider.deletes != 0 || h.provider.creates != 0 {
				t.Fatalf("removes %d, PVE deletes %d creates %d", removes(h), h.provider.deletes, h.provider.creates)
			}
			if _, ok := h.entry(t); ok {
				t.Fatal("an hw-ssh job has a journal entry")
			}
			var done v1alpha1.CredentialJob
			if err := h.c.Get(context.Background(), client.ObjectKeyFromObject(h.job), &done); err != nil {
				t.Fatal(err)
			}
			if controllerutil.ContainsFinalizer(&done, v1alpha1.CredentialCleanupFinalizer) {
				t.Fatal("the finalizer was kept after a successful removal")
			}
		})
	}
}

// A replacement pod of the same session gets its own lease.
func TestHWSSHReinstallsInAReplacementPod(t *testing.T) {
	h := newHWSSHHarness(t)
	if err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: "dev-agents", Name: "session-example"}, &pod); err != nil {
		t.Fatal(err)
	}
	if err := h.c.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	pod.ResourceVersion = ""
	pod.UID = "40000000-0000-4000-8000-000000000002"
	if err := h.c.Create(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	if err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if len(h.exec.commands) != 2 || h.exec.commands[1][7] != "40000000-0000-4000-8000-000000000002" || h.status(t).InstalledPodUID != "40000000-0000-4000-8000-000000000002" {
		t.Fatalf("commands %v", h.exec.commands)
	}
}

// The key never appears in keeper logs, errors, the journal, the job status or
// events, on success, on an exec failure that echoes it, or on cleanup.
func TestHWSSHKeyNeverLeaksFromTheKeeper(t *testing.T) {
	h := newHWSSHHarness(t)
	body := string(h.key[strings.Index(string(h.key), "\n")+1:])
	body = strings.SplitN(body, "\n", 2)[0] // a line of the base64 body
	needles := []string{body, h.secret, base64.StdEncoding.EncodeToString(h.key), string(h.key), base64.RawStdEncoding.EncodeToString(h.key), url.QueryEscape(body)}
	var errs []string
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}
	collect(h.reconcile(t))
	// A transport failure whose message quotes the key.
	h.exec.err = errors.New("stream broke: " + string(h.key))
	h.status(t) // read only
	var j v1alpha1.CredentialJob
	if err := h.c.Get(context.Background(), client.ObjectKeyFromObject(h.job), &j); err != nil {
		t.Fatal(err)
	}
	j.Status = v1alpha1.CredentialJobStatus{}
	if err := h.c.Status().Update(context.Background(), &j); err != nil {
		t.Fatal(err)
	}
	collect(h.reconcile(t))
	h.exec.err = nil
	j.Spec.Release = true
	if err := h.c.Update(context.Background(), &j); err != nil {
		t.Fatal(err)
	}
	collect(h.reconcile(t))
	// Everything that persists or reports.
	var dump bytes.Buffer
	dump.WriteString(h.logs.String())
	dump.WriteString(strings.Join(errs, "\n"))
	var secrets corev1.SecretList
	if err := h.c.List(context.Background(), &secrets, client.InNamespace("dev-env-system")); err != nil {
		t.Fatal(err)
	}
	for _, s := range secrets.Items {
		for _, v := range s.Data {
			dump.Write(v)
		}
	}
	var jobs v1alpha1.CredentialJobList
	if err := h.c.List(context.Background(), &jobs); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(jobs)
	dump.Write(b)
	var events corev1.EventList
	if err := h.c.List(context.Background(), &events); err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(events)
	dump.Write(b)
	var grants v1alpha1.AccessGrantList
	if err := h.c.List(context.Background(), &grants); err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(grants)
	dump.Write(b)
	for _, n := range needles {
		if n != "" && strings.Contains(dump.String(), n) {
			t.Fatalf("the key (or its base64) appears in logs, errors, journal, status or events")
		}
	}
	if h.logs.Len() == 0 {
		t.Log("note: the keeper logged nothing on these paths")
	}
	_ = logr.Discard
}

// The old proxmox path is not routed to the hw-ssh installer.
func TestProxmoxJobsStillUseTheAgentdInstaller(t *testing.T) {
	f := newCredentialFixture(t)
	hw := &hwsshInstaller{}
	f.w.Installer = routedInstaller{proxmox: f.installer, hwssh: hw}
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.installer.installs != 1 || f.provider.creates != 1 || f.status(t).ProviderID != providerID(testGrantUID) {
		t.Fatalf("proxmox path changed: installs %d creates %d status %+v", f.installer.installs, f.provider.creates, f.status(t))
	}
}
