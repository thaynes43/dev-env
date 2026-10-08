package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/go-logr/logr"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const credentialCanary = "PRIVATE-CREDENTIAL-CANARY-0123456789"
const testGrantUID types.UID = "10000000-0000-4000-8000-000000000001"
const testSessionUID types.UID = "20000000-0000-4000-8000-000000000001"
const testJobUID types.UID = "30000000-0000-4000-8000-000000000001"
const testPodUID types.UID = "40000000-0000-4000-8000-000000000001"

type testSSHProvider struct {
	creates, deletes         int
	present                  bool
	info                     tokenInfo
	failBefore, loseResponse bool
	afterCreate              func()
}

func (*testSSHProvider) Ready() error { return nil }
func (s *testSSHProvider) Run(_ context.Context, command string) ([]byte, bool, error) {
	switch {
	case strings.Contains(command, " pvesh "):
		return nil, false, errors.New("unexpected unqualified command")
	case strings.Contains(command, " create "):
		if s.failBefore {
			return nil, false, errors.New(credentialCanary)
		}
		s.creates++
		s.present = true
		if s.afterCreate != nil {
			s.afterCreate()
		}
		if s.loseResponse {
			return nil, true, errors.New(credentialCanary)
		}
		out, _ := json.Marshal(mintedWire{Info: s.info, ID: providerID(testGrantUID), Value: credentialCanary})
		return out, true, nil
	case strings.Contains(command, " delete "):
		s.deletes++
		s.present = false
		return []byte("null"), true, nil
	default:
		rows := []map[string]any{}
		if s.present {
			rows = append(rows, map[string]any{"tokenid": providerName(testGrantUID), "expire": s.info.Expire, "privsep": s.info.Privsep, "comment": s.info.Comment})
		}
		out, _ := json.Marshal(rows)
		return out, true, nil
	}
}

type testCredentialInstaller struct {
	installs, removes int
	material          []string
	incompatible      bool
}

func (i *testCredentialInstaller) Install(_ context.Context, _ *corev1.Pod, e credentialEntry) error {
	i.installs++
	i.material = append(i.material, e.TokenSecret.Reveal())
	if i.incompatible {
		return errCredentialIncompatible
	}
	return nil
}
func (i *testCredentialInstaller) Remove(context.Context, *corev1.Pod, credentialEntry) error {
	i.removes++
	return nil
}

type credentialFixture struct {
	w         *credentialWorker
	c         client.Client
	job       *v1alpha1.CredentialJob
	provider  *testSSHProvider
	installer *testCredentialInstaller
	clock     *clocktesting.FakeClock
}

func newCredentialFixture(t *testing.T) credentialFixture {
	t.Helper()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	job := &v1alpha1.CredentialJob{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: v1alpha1.CredentialJobName(testGrantUID), UID: testJobUID}, Spec: v1alpha1.CredentialJobSpec{Grant: v1alpha1.CredentialObjectReference{Namespace: "dev-agents", Name: "grant-example", UID: testGrantUID}, Session: v1alpha1.CredentialObjectReference{Namespace: "dev-agents", Name: "session-example", UID: testSessionUID}, Credential: v1alpha1.CredentialProxmox, ExpiresAt: metav1.NewTime(now.Add(time.Hour))}}
	grant := &v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: "grant-example", UID: testGrantUID, Annotations: map[string]string{v1alpha1.LabelPrefix + "credential-job-uid": string(testJobUID)}}, Spec: v1alpha1.AccessGrantSpec{Type: v1alpha1.GrantCredential, Credential: &v1alpha1.CredentialGrant{Name: v1alpha1.CredentialProxmox}, Requester: v1alpha1.GrantRequester{Session: "session-example", SessionUID: testSessionUID}}, Status: v1alpha1.AccessGrantStatus{Phase: v1alpha1.GrantActive, ExpiresAt: &job.Spec.ExpiresAt}}
	session := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: "session-example", UID: testSessionUID}}
	owner := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: "session-example", UID: testPodUID, Labels: map[string]string{v1alpha1.LabelSession: "session-example"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentSession", Name: "session-example", UID: testSessionUID, Controller: &owner}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: DefaultCredentialJournalSecret}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(job, grant, pod).WithObjects(job, grant, session, pod, secret).Build()
	clk := clocktesting.NewFakeClock(now)
	provider := &testSSHProvider{info: tokenInfo{Expire: job.Spec.ExpiresAt.Unix(), Privsep: 0, Comment: "dev-env:" + string(testGrantUID) + ":" + string(testJobUID)}}
	installer := &testCredentialInstaller{}
	w := &credentialWorker{Enabled: true, Client: c, Namespace: "dev-env-system", SessionNamespace: "dev-agents", Journal: &credentialJournal{Client: c, Secret: types.NamespacedName{Namespace: "dev-env-system", Name: DefaultCredentialJournalSecret}}, Provider: &proxmoxProvider{SSH: provider}, Installer: installer, Fence: func(ctx context.Context) error { return ctx.Err() }, Clock: clk, Log: logr.Discard()}
	return credentialFixture{w, c, job, provider, installer, clk}
}
func (f credentialFixture) reconcile(t *testing.T) error {
	t.Helper()
	var job v1alpha1.CredentialJob
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.job), &job); err != nil {
		t.Fatal(err)
	}
	return f.w.reconcile(context.Background(), &job)
}
func (f credentialFixture) status(t *testing.T) v1alpha1.CredentialJobStatus {
	t.Helper()
	var job v1alpha1.CredentialJob
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.job), &job); err != nil {
		t.Fatal(err)
	}
	return job.Status
}
func (f credentialFixture) entry(t *testing.T) (credentialEntry, bool) {
	t.Helper()
	_, entries, err := f.w.Journal.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	e, ok := entries[string(testJobUID)]
	return e, ok
}

func TestCredentialRestartReinstallsSameMaterialAndRelease(t *testing.T) {
	f := newCredentialFixture(t)
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t); got.Phase != v1alpha1.CredentialInstalled || got.InstalledPodUID != string(testPodUID) {
		t.Fatalf("receipt %+v", got)
	}
	e, ok := f.entry(t)
	if !ok || e.Stage != journalMinted || e.TokenSecret.Reveal() != credentialCanary {
		t.Fatal("minted value was not persisted before installation")
	}
	// Simulate a new worker and a replacement session pod.
	copyWorker := *f.w
	f.w = &copyWorker
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: "dev-agents", Name: "session-example"}, &pod); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	pod.ResourceVersion = ""
	pod.UID = "40000000-0000-4000-8000-000000000002"
	if err := f.c.Create(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.provider.creates != 1 || f.installer.installs != 2 || f.installer.material[0] != f.installer.material[1] {
		t.Fatal("restart reminted or lost credential material")
	}
	var job v1alpha1.CredentialJob
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.job), &job); err != nil {
		t.Fatal(err)
	}
	job.Spec.Release = true
	if err := f.c.Update(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.status(t).Phase != v1alpha1.CredentialRevoked || f.provider.present || f.installer.removes != 1 {
		t.Fatal("release did not revoke provider before client cleanup")
	}
	if _, ok = f.entry(t); ok {
		t.Fatal("revoked journal entry remained")
	}
}

func TestCredentialUnknownDispatchRetainsTombstoneForLateCreate(t *testing.T) {
	f := newCredentialFixture(t)
	e := credentialEntry{JobUID: testJobUID, Spec: f.job.Spec, Stage: journalIntent, Uncertain: true}
	if err := f.w.Journal.update(context.Background(), testJobUID, &e); err != nil {
		t.Fatal(err)
	}
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.status(t).Phase != v1alpha1.CredentialCleanupPending || f.status(t).FailureCode != v1alpha1.CredentialAmbiguousMint {
		t.Fatal("absent metadata incorrectly proved revocation")
	}
	if _, ok := f.entry(t); !ok {
		t.Fatal("uncertain tombstone removed before fixed expiry")
	}
	// The old command completes after our earlier absent read.
	f.provider.present = true
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.provider.present || f.provider.deletes != 1 || f.status(t).Phase != v1alpha1.CredentialCleanupPending || f.provider.creates != 0 {
		t.Fatal("late create was not cleaned without reminting")
	}
	f.clock.Step(time.Hour)
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.status(t).Phase != v1alpha1.CredentialRevoked || f.provider.creates != 0 {
		t.Fatal("expiry did not finish uncertain cleanup")
	}
}

func TestCredentialLostResponseFailsClosed(t *testing.T) {
	f := newCredentialFixture(t)
	f.provider.loseResponse = true
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.installer.installs != 0 || f.provider.creates != 1 || f.provider.present {
		t.Fatal("unknown mint material was installed or not cleaned")
	}
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.provider.creates != 1 || f.status(t).Phase != v1alpha1.CredentialCleanupPending {
		t.Fatal("unknown dispatch was retried")
	}
}
func TestCredentialPredispatchFailureCanRetry(t *testing.T) {
	f := newCredentialFixture(t)
	f.provider.failBefore = true
	if err := f.reconcile(t); err == nil || strings.Contains(err.Error(), credentialCanary) {
		t.Fatal("pre-dispatch failure leaked or was ignored")
	}
	e, ok := f.entry(t)
	if !ok || e.Uncertain {
		t.Fatal("proved pre-dispatch failure remained uncertain")
	}
	f.provider.failBefore = false
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.provider.creates != 1 {
		t.Fatal("pre-dispatch retry failed")
	}
}
func TestCredentialReleaseDuringMintNeverInstalls(t *testing.T) {
	f := newCredentialFixture(t)
	f.provider.afterCreate = func() {
		var grant v1alpha1.AccessGrant
		key := types.NamespacedName{Namespace: "dev-agents", Name: "grant-example"}
		if err := f.c.Get(context.Background(), key, &grant); err != nil {
			t.Fatal(err)
		}
		grant.Spec.Release = true
		if err := f.c.Update(context.Background(), &grant); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.installer.installs != 0 || f.provider.present || f.status(t).Phase != v1alpha1.CredentialRevoked {
		t.Fatal("release race installed or retained provider credential")
	}
}
func TestCredentialProviderOwnershipAndIncompatiblePod(t *testing.T) {
	t.Run("foreign provider", func(t *testing.T) {
		f := newCredentialFixture(t)
		f.provider.info.Comment = "foreign"
		f.provider.loseResponse = true
		if err := f.reconcile(t); err == nil {
			t.Fatal("ownership mismatch was accepted")
		}
		if f.provider.deletes != 0 || !f.provider.present {
			t.Fatal("foreign provider token was deleted")
		}
	})
	t.Run("incompatible pod", func(t *testing.T) {
		f := newCredentialFixture(t)
		f.installer.incompatible = true
		for range 3 {
			_ = f.reconcile(t)
		}
		if f.status(t).FailureCode != v1alpha1.CredentialIncompatiblePod || f.provider.creates != 1 || f.provider.present {
			t.Fatal("compatibility attempts reminted or failed to revoke")
		}
	})
}
func TestCredentialLeaseRefusalDispatchesNothing(t *testing.T) {
	f := newCredentialFixture(t)
	f.w.Fence = func(context.Context) error { return errors.New("lease lost") }
	if err := f.reconcile(t); err == nil {
		t.Fatal("missing leader was allowed")
	}
	if f.provider.creates != 0 || f.installer.installs != 0 {
		t.Fatal("operation ran after lease refusal")
	}
}
func TestCredentialJournalBoundsAndRedaction(t *testing.T) {
	f := newCredentialFixture(t)
	entries := map[string]credentialEntry{}
	for n := 0; n < journalMaxEntries; n++ {
		uid := types.UID(fmt.Sprintf("30000000-0000-4000-8000-%012x", n))
		entries[string(uid)] = credentialEntry{JobUID: uid, Spec: f.job.Spec, Stage: journalIntent}
	}
	if _, err := encodeJournal(entries); err != nil {
		t.Fatal(err)
	}
	entries["30000000-0000-4000-8000-000000001000"] = credentialEntry{JobUID: "30000000-0000-4000-8000-000000001000", Spec: f.job.Spec, Stage: journalIntent}
	if _, err := encodeJournal(entries); !errors.Is(err, errJournalFull) {
		t.Fatal("entry limit did not backpressure")
	}
	delete(entries, "30000000-0000-4000-8000-000000001000")
	for key, e := range entries {
		e.Stage = journalMinted
		e.TokenID = providerID(testGrantUID)
		e.TokenSecret = newSecretValue(strings.Repeat("S", 4096))
		entries[key] = e
	}
	if _, err := encodeJournal(entries); !errors.Is(err, errJournalFull) {
		t.Fatal("serialized byte limit did not backpressure")
	}
	e := credentialEntry{JobUID: testJobUID, Spec: f.job.Spec, Stage: journalMinted, TokenID: providerID(testGrantUID), TokenSecret: newSecretValue(credentialCanary)}
	raw, _ := json.Marshal(e)
	for _, printed := range []string{string(raw), fmt.Sprintf("%+v %#v %x", e, e, e), fmt.Sprintf("%+v", mintResult{TokenSecret: newSecretValue(credentialCanary)})} {
		if strings.Contains(printed, credentialCanary) {
			t.Fatal("generic credential formatting exposed material")
		}
	}
}

func TestCredentialDisabledBackendStillRevokesExistingJournal(t *testing.T) {
	f := newCredentialFixture(t)
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	f.w.Enabled = false
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.provider.present || f.status(t).Phase != v1alpha1.CredentialRevoked || f.provider.creates != 1 {
		t.Fatal("disabling backend stopped existing cleanup")
	}
}
func TestCredentialRequesterUIDMismatchRefusesMint(t *testing.T) {
	f := newCredentialFixture(t)
	var grant v1alpha1.AccessGrant
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: "dev-agents", Name: "grant-example"}, &grant); err != nil {
		t.Fatal(err)
	}
	grant.Spec.Requester.SessionUID = "20000000-0000-4000-8000-000000000002"
	if err := f.c.Update(context.Background(), &grant); err != nil {
		t.Fatal(err)
	}
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.provider.creates != 0 || f.installer.installs != 0 || f.status(t).Phase != v1alpha1.CredentialRevoked {
		t.Fatal("replacement requester gained original grant")
	}
}

func TestCredentialWaitsForBrokerExecutionUIDPin(t *testing.T) {
	f := newCredentialFixture(t)
	grant := &v1alpha1.AccessGrant{}
	key := types.NamespacedName{Namespace: "dev-agents", Name: "grant-example"}
	if err := f.c.Get(context.Background(), key, grant); err != nil {
		t.Fatal(err)
	}
	grant.Annotations = nil
	if err := f.c.Update(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.provider.creates != 0 || f.status(t).Phase != v1alpha1.CredentialPending {
		t.Fatal("unfenced job dispatched")
	}
	if err := f.c.Get(context.Background(), key, grant); err != nil {
		t.Fatal(err)
	}
	grant.Annotations = map[string]string{v1alpha1.LabelPrefix + "credential-job-uid": string(testSessionUID)}
	if err := f.c.Update(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.provider.creates != 0 || f.status(t).Phase != v1alpha1.CredentialRevoked {
		t.Fatal("replacement execution dispatched")
	}
}
