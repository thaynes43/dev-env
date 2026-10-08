package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

type captureCredentialExec struct {
	payload []byte
	err     error
}

func (e *captureCredentialExec) Stream(opts remotecommand.StreamOptions) error {
	return e.StreamWithContext(context.Background(), opts)
}
func (e *captureCredentialExec) StreamWithContext(_ context.Context, opts remotecommand.StreamOptions) error {
	if opts.Stdin != nil {
		e.payload, _ = io.ReadAll(opts.Stdin)
	}
	if opts.Stdout != nil {
		_, _ = io.WriteString(opts.Stdout, credentialCanary)
	}
	if opts.Stderr != nil {
		_, _ = io.WriteString(opts.Stderr, credentialCanary)
	}
	return e.err
}
func TestCredentialExecUsesTypedStdinAndFencedArgv(t *testing.T) {
	fixture := newCredentialFixture(t)
	installer, err := newCredentialExecInstaller(&rest.Config{Host: "https://api.example"})
	if err != nil {
		t.Fatal(err)
	}
	capture := &captureCredentialExec{}
	var request *url.URL
	installer.executor = func(_ *rest.Config, u *url.URL) (remotecommand.Executor, error) { request = u; return capture, nil }
	e := credentialEntry{JobUID: testJobUID, Spec: fixture.job.Spec, Stage: journalMinted, TokenID: providerID(testGrantUID), TokenSecret: newSecretValue(credentialCanary)}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: "session-example", UID: testPodUID}}
	if err := installer.Install(context.Background(), pod, e); err != nil {
		t.Fatal(err)
	}
	want := []string{"agentd", "ctl", "credential-install", "--name", e.Spec.Grant.Name, "--grant-uid", string(testGrantUID), "--pod-uid", string(testPodUID), "--expires", e.Spec.ExpiresAt.UTC().Format(time.RFC3339)}
	if !reflect.DeepEqual(request.Query()["command"], want) || strings.Contains(request.String(), credentialCanary) || request.Query().Get("tty") == "true" {
		t.Fatal("credential exec argv or terminal contract changed")
	}
	var payload struct {
		Version                          int `json:"version"`
		Credential, TokenID, TokenSecret string
	}
	if json.Unmarshal(capture.payload, &payload) != nil || payload.Version != 1 || payload.Credential != "proxmox" || payload.TokenID != e.TokenID || payload.TokenSecret != credentialCanary {
		t.Fatal("typed private payload changed")
	}
	capture.err = errors.New(credentialCanary)
	if err := installer.Install(context.Background(), pod, e); err == nil || strings.Contains(err.Error(), credentialCanary) {
		t.Fatal("raw exec transport error escaped")
	}
	capture.err = nil
	capture.payload = nil
	if err := installer.Remove(context.Background(), pod, e); err != nil {
		t.Fatal(err)
	}
	if len(capture.payload) != 0 || !reflect.DeepEqual(request.Query()["command"], []string{"agentd", "ctl", "credential-remove", "--name", e.Spec.Grant.Name, "--grant-uid", string(testGrantUID), "--pod-uid", string(testPodUID)}) {
		t.Fatal("cleanup is not fenced")
	}
}

type failCredentialPatch struct {
	client.Client
	fail bool
}

func (c *failCredentialPatch) Patch(ctx context.Context, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
	if _, ok := obj.(*corev1.Secret); ok && c.fail {
		return errors.New(credentialCanary)
	}
	return c.Client.Patch(ctx, obj, p, opts...)
}
func TestCredentialLostJournalWriteNeverInstallsOrRemints(t *testing.T) {
	f := newCredentialFixture(t)
	failing := &failCredentialPatch{Client: f.c}
	f.w.Journal.Client = failing
	f.provider.afterCreate = func() { failing.fail = true }
	if err := f.reconcile(t); err == nil || strings.Contains(err.Error(), credentialCanary) {
		t.Fatal("journal failure escaped or was ignored")
	}
	if f.provider.creates != 1 || f.installer.installs != 0 {
		t.Fatal("unpersisted value installed")
	}
	failing.fail = false
	f.provider.afterCreate = nil
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.provider.creates != 1 || f.installer.installs != 0 || f.status(t).Phase != v1alpha1.CredentialCleanupPending {
		t.Fatal("uncertain response loss reminted or ended early")
	}
}

func TestCredentialLeaseFenceAndCancellation(t *testing.T) {
	f := newCredentialFixture(t)
	holder := "keeper-test"
	duration := int32(15)
	renew := metav1.NewMicroTime(f.clock.Now())
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: LeaderElectionID}, Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &duration, RenewTime: &renew}}
	if err := f.c.Create(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	fence := &LeaseFence{Reader: f.c, Lease: client.ObjectKeyFromObject(lease), Identity: holder, Clock: f.clock, MaxAge: 10 * time.Second}
	if err := fence.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.clock.Step(10 * time.Second)
	if err := fence.Check(context.Background()); err == nil {
		t.Fatal("stale lease accepted")
	}
	f.clock.SetTime(renew.Time)
	fence.Identity = "new-holder"
	if err := fence.Check(context.Background()); err == nil {
		t.Fatal("other lease holder accepted")
	}
	fence.Identity = holder
	f.w.Fence = fence.Check
	// Use a real timer only for the inexpensive cancellation monitor; no remote
	// command or busy-loop is needed to prove it interrupts an in-flight context.
	f.w.Clock = clocktesting.NewFakeClock(f.clock.Now())
	opctx, cancel := f.w.operationContext(context.Background())
	defer cancel()
	monitorClock := f.w.Clock.(*clocktesting.FakeClock)
	fence.Identity = "lost-holder"
	monitorClock.Step(time.Second)
	select {
	case <-opctx.Done():
	case <-time.After(time.Second):
		t.Fatal("leadership loss did not cancel operation")
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if err := fence.Check(cancelled); err == nil {
		t.Fatal("cancelled leader context accepted")
	}
}

type providerResponse struct{ raw []byte }

func (*providerResponse) Ready() error { return nil }
func (r *providerResponse) Run(context.Context, string) ([]byte, bool, error) {
	return r.raw, true, nil
}
func TestCredentialProviderMetadataRequiresExplicitIsolation(t *testing.T) {
	f := newCredentialFixture(t)
	e := credentialEntry{JobUID: testJobUID, Spec: f.job.Spec, Stage: journalIntent}
	for _, privsep := range []any{false, 0, true, 1, nil, "0"} {
		info := map[string]any{"expire": e.Spec.ExpiresAt.Unix(), "comment": providerComment(e)}
		if privsep != nil {
			info["privsep"] = privsep
		}
		raw, _ := json.Marshal(map[string]any{"info": info, "value": credentialCanary, "full-tokenid": providerID(testGrantUID)})
		p := &proxmoxProvider{SSH: &providerResponse{raw: raw}}
		result, err := p.mint(context.Background(), e)
		wantValid := privsep == false || privsep == 0
		if (err == nil) != wantValid || !result.PossibleDispatch {
			t.Fatalf("isolation %v validation changed", privsep)
		}
		if err != nil && strings.Contains(err.Error(), credentialCanary) {
			t.Fatal("provider response material escaped")
		}
	}
}

func TestCredentialLostPrivateValueReconstructsOnlyCleanup(t *testing.T) {
	f := newCredentialFixture(t)
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if err := f.w.Journal.update(context.Background(), testJobUID, nil); err != nil {
		t.Fatal(err)
	}
	f.w.Enabled = false
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.provider.creates != 1 || f.provider.present || f.status(t).Phase != v1alpha1.CredentialCleanupPending {
		t.Fatal("lost material caused remint or false revocation")
	}
	if e, ok := f.entry(t); !ok || !e.Uncertain || e.Stage != journalCleanup {
		t.Fatal("lost material uncertainty was not retained")
	}
}

func TestCredentialMarkerFailureResetsProvenPredispatchIntent(t *testing.T) {
	for _, test := range []struct {
		name     string
		commit   bool
		conflict bool
	}{
		{"conflict", false, true}, {"transient error", false, false}, {"committed before response loss", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCredentialFixture(t)
			failed := false
			f.w.Client = interceptor.NewClient(f.c.(client.WithWatch), interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				j, ok := obj.(*v1alpha1.CredentialJob)
				if !failed && sub == "status" && ok && j.Status.Phase == v1alpha1.CredentialPending && j.Status.ProviderID != "" {
					failed = true
					if test.commit {
						if err := c.SubResource(sub).Update(ctx, obj, opts...); err != nil {
							return err
						}
					}
					if test.conflict {
						return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "credentialjobs"}, j.Name, errors.New(credentialCanary))
					}
					return errors.New(credentialCanary)
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			}})
			if err := f.reconcile(t); err == nil || strings.Contains(err.Error(), credentialCanary) {
				t.Fatal("marker failure ignored or raw error exposed")
			}
			if !failed || f.provider.creates != 0 || f.installer.installs != 0 {
				t.Fatal("marker failure dispatched provider or installed")
			}
			if e, ok := f.entry(t); !ok || e.Stage != journalIntent || e.Uncertain {
				t.Fatal("proven pre-dispatch intent was not durably reset")
			}
			if (f.status(t).ProviderID != "") != test.commit {
				t.Fatal("committed-marker fixture failed")
			}
			if err := f.reconcile(t); err != nil {
				t.Fatal(err)
			}
			if f.provider.creates != 1 || f.installer.installs != 1 || f.status(t).Phase != v1alpha1.CredentialInstalled {
				t.Fatal("safe marker retry failed to install exactly once")
			}
		})
	}
}

func TestCredentialMarkerFailedResetRemainsCleanupOnly(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed=%v", commit), func(t *testing.T) {
			f := newCredentialFixture(t)
			failingJournal := &failCredentialPatch{Client: f.c}
			f.w.Journal.Client = failingJournal
			failed := false
			f.w.Client = interceptor.NewClient(f.c.(client.WithWatch), interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				j, ok := obj.(*v1alpha1.CredentialJob)
				if !failed && sub == "status" && ok && j.Status.Phase == v1alpha1.CredentialPending && j.Status.ProviderID != "" {
					failed = true
					if commit {
						if err := c.SubResource(sub).Update(ctx, obj, opts...); err != nil {
							return err
						}
					}
					failingJournal.fail = true
					return errors.New(credentialCanary)
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			}})
			if err := f.reconcile(t); err == nil || strings.Contains(err.Error(), credentialCanary) {
				t.Fatal("failed reset was ignored or leaked")
			}
			if !failed || f.provider.creates != 0 || f.installer.installs != 0 {
				t.Fatal("marker failure dispatched")
			}
			if e, ok := f.entry(t); !ok || e.Stage != journalIntent || !e.Uncertain {
				t.Fatal("failed reset lost conservative uncertainty")
			}
			failingJournal.fail = false
			if err := f.reconcile(t); err != nil {
				t.Fatal(err)
			}
			if f.provider.creates != 0 || f.installer.installs != 0 || f.status(t).Phase != v1alpha1.CredentialCleanupPending || f.status(t).FailureCode != v1alpha1.CredentialAmbiguousMint {
				t.Fatal("failed reset allowed create or ended uncertainty early")
			}
			if e, ok := f.entry(t); !ok || e.Stage != journalCleanup || !e.Uncertain {
				t.Fatal("uncertain tombstone was not retained")
			}
		})
	}
}
