package keeper

// hw-ssh cleanup (D-89): the job is Revoked, and its finalizer lifted, only after
// the lease is removed or its pod is gone. For hw-ssh, Revoked means only that.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func (h *hwsshHarness) release(t *testing.T) {
	t.Helper()
	var job v1alpha1.CredentialJob
	if err := h.c.Get(context.Background(), client.ObjectKeyFromObject(h.job), &job); err != nil {
		t.Fatal(err)
	}
	job.Spec.Release = true
	if err := h.c.Update(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
}

func (h *hwsshHarness) live(t *testing.T) v1alpha1.CredentialJob {
	t.Helper()
	var job v1alpha1.CredentialJob
	if err := h.c.Get(context.Background(), client.ObjectKeyFromObject(h.job), &job); err != nil {
		t.Fatal(err)
	}
	return job
}

func (h *hwsshHarness) removeCommands() [][]string {
	var out [][]string
	for _, c := range h.exec.commands {
		if len(c) > 4 && c[4] == "remove" {
			out = append(out, c)
		}
	}
	return out
}

// The first removal fails: the job stays CleanupPending with its finalizer, a retry
// inside the backoff does not hammer the pod, and only a successful retry ends it.
func TestHWSSHFailedRemovalIsRetriedBeforeRevoked(t *testing.T) {
	h := newHWSSHHarness(t)
	if err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	h.release(t)
	h.exec.failRemoves = 1
	if err := h.reconcile(t); err == nil {
		t.Fatal("a failed removal was reported as done")
	}
	st := h.status(t)
	job := h.live(t)
	if st.Phase != v1alpha1.CredentialCleanupPending || st.RevokedAt != nil || !controllerutil.ContainsFinalizer(&job, v1alpha1.CredentialCleanupFinalizer) {
		t.Fatalf("after a failed removal: %+v finalizer %v", st, job.Finalizers)
	}
	if len(h.removeCommands()) != 1 {
		t.Fatalf("removes %d", len(h.removeCommands()))
	}
	// Inside the backoff: no new exec, still pending.
	if err := h.reconcile(t); err == nil || len(h.removeCommands()) != 1 || h.status(t).Phase != v1alpha1.CredentialCleanupPending {
		t.Fatalf("retried inside the backoff or finished early: err %v removes %d", err, len(h.removeCommands()))
	}
	h.clock.Step(10 * time.Minute)
	if err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	st, job = h.status(t), h.live(t)
	if st.Phase != v1alpha1.CredentialRevoked || st.RevokedAt == nil || controllerutil.ContainsFinalizer(&job, v1alpha1.CredentialCleanupFinalizer) || len(h.removeCommands()) != 2 {
		t.Fatalf("after the retry: %+v finalizer %v removes %d", st, job.Finalizers, len(h.removeCommands()))
	}
}

// Deleting the job (the orphan-like path: the CR goes while the finalizer holds it)
// also waits for a successful removal before the finalizer comes off.
func TestHWSSHDeletedJobKeepsItsFinalizerUntilTheLeaseIsRemoved(t *testing.T) {
	h := newHWSSHHarness(t)
	if err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	job := h.live(t)
	if err := h.c.Delete(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	h.exec.failRemoves = 2
	for range 2 {
		if err := h.reconcile(t); err == nil {
			t.Fatal("a failed removal was reported as done")
		}
		if now := h.live(t); !controllerutil.ContainsFinalizer(&now, v1alpha1.CredentialCleanupFinalizer) {
			t.Fatal("the finalizer was lifted before the lease was removed")
		}
		h.clock.Step(10 * time.Minute)
	}
	if err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	var gone v1alpha1.CredentialJob
	if err := h.c.Get(context.Background(), client.ObjectKeyFromObject(h.job), &gone); err == nil {
		t.Fatalf("the job remained: finalizers %v", gone.Finalizers)
	}
	if len(h.removeCommands()) != 3 {
		t.Fatalf("removes %d", len(h.removeCommands()))
	}
}

// A gone pod, another session's pod and a terminal pod count as complete with no exec;
// a deleting or not-yet-running pod waits; a replacement pod is cleaned, not skipped.
func TestHWSSHRemovalCompletesWhenThePodIsGoneAndWaitsOtherwise(t *testing.T) {
	podOf := func(t *testing.T, h *hwsshHarness) corev1.Pod {
		var p corev1.Pod
		if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: "dev-agents", Name: "session-example"}, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Run("pod gone", func(t *testing.T) {
		h := newHWSSHHarness(t)
		if err := h.reconcile(t); err != nil {
			t.Fatal(err)
		}
		p := podOf(t, h)
		if err := h.c.Delete(context.Background(), &p); err != nil {
			t.Fatal(err)
		}
		h.release(t)
		if err := h.reconcile(t); err != nil {
			t.Fatal(err)
		}
		if h.status(t).Phase != v1alpha1.CredentialRevoked || len(h.removeCommands()) != 0 {
			t.Fatalf("status %+v removes %d", h.status(t), len(h.removeCommands()))
		}
	})
	t.Run("terminal pod", func(t *testing.T) {
		h := newHWSSHHarness(t)
		if err := h.reconcile(t); err != nil {
			t.Fatal(err)
		}
		p := podOf(t, h)
		p.Status.Phase = corev1.PodFailed
		if err := h.c.Status().Update(context.Background(), &p); err != nil {
			t.Fatal(err)
		}
		h.release(t)
		if err := h.reconcile(t); err != nil || h.status(t).Phase != v1alpha1.CredentialRevoked || len(h.removeCommands()) != 0 {
			t.Fatalf("err %v status %+v", err, h.status(t))
		}
	})
	t.Run("another session's pod of that name", func(t *testing.T) {
		h := newHWSSHHarness(t)
		if err := h.reconcile(t); err != nil {
			t.Fatal(err)
		}
		p := podOf(t, h)
		p.OwnerReferences[0].UID = "20000000-0000-4000-8000-0000000000ff"
		if err := h.c.Update(context.Background(), &p); err != nil {
			t.Fatal(err)
		}
		h.release(t)
		if err := h.reconcile(t); err != nil || h.status(t).Phase != v1alpha1.CredentialRevoked || len(h.removeCommands()) != 0 {
			t.Fatalf("err %v status %+v", err, h.status(t))
		}
	})
	t.Run("pod being deleted waits", func(t *testing.T) {
		h := newHWSSHHarness(t)
		if err := h.reconcile(t); err != nil {
			t.Fatal(err)
		}
		p := podOf(t, h)
		p.Finalizers = []string{"test/hold"}
		if err := h.c.Update(context.Background(), &p); err != nil {
			t.Fatal(err)
		}
		if err := h.c.Delete(context.Background(), &p); err != nil {
			t.Fatal(err)
		}
		h.release(t)
		if err := h.reconcile(t); err == nil || h.status(t).Phase != v1alpha1.CredentialCleanupPending {
			t.Fatalf("a terminating pod ended the job: %v %+v", err, h.status(t))
		}
	})
	t.Run("replaced pod is cleaned, not skipped", func(t *testing.T) {
		h := newHWSSHHarness(t)
		if err := h.reconcile(t); err != nil {
			t.Fatal(err)
		}
		h.release(t)
		h.exec.failRemoves = 1
		if err := h.reconcile(t); err == nil {
			t.Fatal("a failed removal was reported as done")
		}
		p := podOf(t, h)
		if err := h.c.Delete(context.Background(), &p); err != nil {
			t.Fatal(err)
		}
		p.ResourceVersion = ""
		p.UID = "40000000-0000-4000-8000-000000000002"
		if err := h.c.Create(context.Background(), &p); err != nil {
			t.Fatal(err)
		}
		h.clock.Step(10 * time.Minute)
		if err := h.reconcile(t); err != nil {
			t.Fatal(err)
		}
		rm := h.removeCommands()
		if h.status(t).Phase != v1alpha1.CredentialRevoked || len(rm) != 2 || rm[1][7] != "40000000-0000-4000-8000-000000000002" {
			t.Fatalf("status %+v removes %v", h.status(t), rm)
		}
	})
}

// A keeper crash after the install but before the status write leaves no receipt and
// no journal entry. The next reconcile, on release, still removes the lease.
func TestHWSSHCrashBetweenInstallAndStatusStillEndsInRemoval(t *testing.T) {
	h := newHWSSHHarness(t)
	var pod corev1.Pod
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: "dev-agents", Name: "session-example"}, &pod); err != nil {
		t.Fatal(err)
	}
	// The exec ran; the keeper died before writing Installed.
	if err := h.w.Installer.Install(context.Background(), &pod, credentialEntry{JobUID: testJobUID, Spec: h.job.Spec}); err != nil {
		t.Fatal(err)
	}
	if st := h.status(t); st.Phase != "" {
		t.Fatalf("setup: %+v", st)
	}
	// A restarted keeper (a new worker) finds the job released.
	restarted := *h.w
	restarted.leases = nil
	h.w = &restarted
	h.release(t)
	if err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if h.status(t).Phase != v1alpha1.CredentialRevoked || len(h.removeCommands()) != 1 {
		t.Fatalf("status %+v removes %d", h.status(t), len(h.removeCommands()))
	}
}

// An install refused as incompatible (exit 2 or 3) wrote nothing, so the removal's own
// incompatible answer counts as complete: one install, one removal, no repeats.
func TestHWSSHIncompatibleInstallEndsOnceWithAFailureCode(t *testing.T) {
	for _, exit := range []int{2, 3} {
		h := newHWSSHHarness(t)
		h.exec.exit = exit
		if err := h.reconcile(t); err != nil {
			t.Fatalf("exit %d: %v", exit, err)
		}
		st, job := h.status(t), h.live(t)
		if st.Phase != v1alpha1.CredentialRevoked || st.FailureCode != v1alpha1.CredentialIncompatiblePod || controllerutil.ContainsFinalizer(&job, v1alpha1.CredentialCleanupFinalizer) {
			t.Fatalf("exit %d: status %+v finalizers %v", exit, st, job.Finalizers)
		}
		if len(h.exec.commands) != 2 || h.exec.commands[0][4] != "install" || h.exec.commands[1][4] != "remove" {
			t.Fatalf("exit %d: execs %v", exit, h.exec.commands)
		}
		h.clock.Step(time.Hour)
		for range 3 {
			if err := h.reconcile(t); err != nil {
				t.Fatal(err)
			}
		}
		if len(h.exec.commands) != 2 {
			t.Fatalf("exit %d: the finished job exec'd again: %d", exit, len(h.exec.commands))
		}
	}
}

// A lease WAS installed in this pod, and the removal now answers incompatible: that is
// unexpected, so it is not counted as removed. The job stays CleanupPending, keeps its
// finalizer, says why, and retries with backoff; a later success ends it.
func TestHWSSHIncompatibleRemovalAfterAnInstallIsNotCountedAsRemoved(t *testing.T) {
	h := newHWSSHHarness(t)
	if err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if st := h.status(t); st.Phase != v1alpha1.CredentialInstalled || st.InstalledPodUID != string(testPodUID) {
		t.Fatalf("setup: %+v", st)
	}
	h.release(t)
	h.exec.exit = 3
	if err := h.reconcile(t); !errors.Is(err, errRemovalRefused) {
		t.Fatalf("error %v", err)
	}
	st, job := h.status(t), h.live(t)
	if st.Phase != v1alpha1.CredentialCleanupPending || st.FailureCode != v1alpha1.CredentialRemovalRefused || st.RevokedAt != nil || !controllerutil.ContainsFinalizer(&job, v1alpha1.CredentialCleanupFinalizer) {
		t.Fatalf("status %+v finalizers %v", st, job.Finalizers)
	}
	if !strings.Contains(h.logs.String(), "refused by a pod that holds a lease") {
		t.Fatal("the refusal was not logged")
	}
	removes := len(h.removeCommands())
	if err := h.reconcile(t); err == nil || len(h.removeCommands()) != removes {
		t.Fatalf("retried inside the backoff: %v", err)
	}
	h.clock.Step(10 * time.Minute)
	if err := h.reconcile(t); !errors.Is(err, errRemovalRefused) || len(h.removeCommands()) != removes+1 {
		t.Fatalf("no retry after the backoff: %v removes %d", err, len(h.removeCommands()))
	}
	h.exec.exit = 0
	h.clock.Step(10 * time.Minute)
	if err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	st = h.status(t)
	if st.Phase != v1alpha1.CredentialRevoked || st.FailureCode != "" {
		t.Fatalf("after the removal succeeded: %+v", st)
	}
}

// A pod that never ran cannot hold a lease: its removal needs no exec.
func TestHWSSHRemovalOfAPodThatNeverRanIsComplete(t *testing.T) {
	h := newHWSSHHarness(t)
	var pod corev1.Pod
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: "dev-agents", Name: "session-example"}, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodPending
	if err := h.c.Status().Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	h.release(t)
	if err := h.reconcile(t); err != nil || h.status(t).Phase != v1alpha1.CredentialRevoked || len(h.exec.commands) != 0 {
		t.Fatalf("err %v status %+v execs %d", err, h.status(t), len(h.exec.commands))
	}
}

// The state rule's evidence, with the real script: every input that makes a removal
// refuse as incompatible (exit 2 or 3) makes the install refuse the same way first
// and write nothing, because the checks before any action are shared. So a removal
// refused that way proves this job never leased this pod.
func TestHWSSHRemovalRefusalImpliesTheInstallWroteNothing(t *testing.T) {
	key, fp := syntheticKey(t, "")
	const guid, puid = "10000000-0000-4000-8000-000000000001", "40000000-0000-4000-8000-000000000001"
	exp := "2026-10-28T01:00:00Z"
	for name, c := range map[string]struct {
		podEnv string
		grant  string
		guid   string
		puid   string
		want   int
	}{
		"pod UID variable unset":      {"", "grant-1028-000000-abcd", guid, puid, 3},
		"grant name not a grant":      {puid, "not-a-grant", guid, puid, 2},
		"upper case in an identifier": {puid, "grant-1028-000000-ABCD", guid, puid, 2},
		"unsafe grant UID":            {puid, "grant-1028-000000-abcd", "x;y", puid, 2},
		"unsafe pod UID":              {"p$d", "grant-1028-000000-abcd", guid, "p$d", 2},
	} {
		t.Run(name, func(t *testing.T) {
			xdg := t.TempDir()
			rm := runScript(t, xdg, c.podEnv, nil, "remove", c.grant, c.guid, c.puid)
			in := runScript(t, xdg, c.podEnv, key, "install", c.grant, c.guid, c.puid, exp, fp)
			if rm != c.want || in != c.want {
				t.Fatalf("remove exit %d, install exit %d, want %d for both", rm, in, c.want)
			}
			if entries, _ := os.ReadDir(xdg); len(entries) != 0 {
				t.Fatalf("a refused install wrote %v", entries)
			}
		})
	}
	// A pod-UID mismatch is not an incompatibility (exit 1: retry), for both actions.
	xdg := t.TempDir()
	if rm, in := runScript(t, xdg, "40000000-0000-4000-8000-000000000999", nil, "remove", "grant-1028-000000-abcd", guid, puid),
		runScript(t, xdg, "40000000-0000-4000-8000-000000000999", key, "install", "grant-1028-000000-abcd", guid, puid, exp, fp); rm != 1 || in != 1 {
		t.Fatalf("pod mismatch: remove %d install %d", rm, in)
	}
}

// A failed install is retried with backoff and never marks the job Installed.
func TestHWSSHFailedInstallRetriesWithBackoff(t *testing.T) {
	h := newHWSSHHarness(t)
	h.exec.err = errors.New("stream broke")
	if err := h.reconcile(t); err == nil || h.status(t).Phase == v1alpha1.CredentialInstalled {
		t.Fatalf("a failed install was accepted: %v", err)
	}
	installs := func() int {
		n := 0
		for _, c := range h.exec.commands {
			if c[4] == "install" {
				n++
			}
		}
		return n
	}
	before := installs()
	if err := h.reconcile(t); err != nil || installs() != before {
		t.Fatalf("retried inside the backoff: %v", err)
	}
	h.exec.err = nil
	h.clock.Step(10 * time.Minute)
	if err := h.reconcile(t); err != nil || h.status(t).Phase != v1alpha1.CredentialInstalled {
		t.Fatalf("retry failed: %v %+v", err, h.status(t))
	}
}

// A keeper that predates hw-ssh loads the shared proxmox journal untouched: an hw-ssh
// lease never writes an entry, and the journal refuses to hold one.
func TestHWSSHNeverTouchesTheSharedProxmoxJournal(t *testing.T) {
	h := newHWSSHHarness(t)
	if err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	h.release(t)
	if err := h.reconcile(t); err != nil {
		t.Fatal(err)
	}
	var s corev1.Secret
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: "dev-env-system", Name: DefaultCredentialJournalSecret}, &s); err != nil {
		t.Fatal(err)
	}
	if raw := string(s.Data[journalKey]); strings.Contains(raw, "hw-ssh") || strings.Contains(raw, string(testJobUID)) {
		t.Fatalf("the journal mentions the lease: %q", raw)
	}
	_, entries, err := h.w.Journal.load(context.Background())
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries %d err %v", len(entries), err)
	}
	// Every entry that could be there passes the proxmox-only validator that the
	// previous release shipped (a frozen copy of its two checks).
	for _, e := range entries {
		if !legacyValidEntry(e) {
			t.Fatal("the previous release's journal validator would reject an entry")
		}
	}
	hw := credentialEntry{JobUID: testJobUID, Spec: h.job.Spec, Stage: journalIntent}
	if validEntry(hw) {
		t.Fatal("the journal accepts an hw-ssh entry")
	}
	if _, err := encodeJournal(map[string]credentialEntry{string(testJobUID): hw}); err == nil {
		t.Fatal("the journal encodes an hw-ssh entry")
	}
	if legacyValidEntry(hw) {
		t.Fatal("the frozen previous validator accepts hw-ssh; the copy is wrong")
	}
}

// legacyValidEntry and legacyCheckCredentialSpec are the proxmox-only checks as they
// stood before hw-ssh (main 78a5337, credential_jobs.go and credential_journal.go).
func legacyCheckCredentialSpec(s v1alpha1.CredentialJobSpec) error {
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

func legacyValidEntry(e credentialEntry) bool {
	if !validUID(string(e.JobUID)) || legacyCheckCredentialSpec(e.Spec) != nil || e.InstallFailures < 0 {
		return false
	}
	switch e.Stage {
	case journalIntent, journalMinted, journalCleanup, journalRevoked:
	default:
		return false
	}
	if e.Stage == journalMinted && (e.TokenID != providerID(e.Spec.Grant.UID) || e.TokenSecret.Empty()) {
		return false
	}
	return true
}

var _ = metav1.Now
