package asks

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/testenv"
)

func TestEnvtestAskSweeper(t *testing.T) {
	env, err := testenv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Log(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	scheme := runtime.NewScheme()
	if err := coord.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(env.Config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	clk := clocktesting.NewFakeClock(now)
	req := Requester{Namespace: Namespace, Name: "sweeper-session", UID: "sweeper-uid"}
	store := KubeStore{Client: c, Live: c, Namespace: Namespace}
	makeAsk := func(id string) *coord.Ask {
		t.Helper()
		a := storedAsk(req, id, "request-sweep", now, now.Add(time.Minute))
		if err := c.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	load := func(id string) coord.Ask {
		t.Helper()
		var a coord.Ask
		if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: id}, &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	open := makeAsk("ask-0000000000000001") // status absent means open
	approval := storedAsk(req, "ask-0000000000000002", "request-approval", now, now.Add(2*time.Minute))
	approval.Spec.Type = "approval"
	approval.Spec.Scope = &coord.AskScope{Action: "deploy", Target: "fixture", ExpiresAt: metav1.NewTime(now.Add(time.Minute))}
	// The schema permits a legacy scope before the Ask deadline; EffectiveState
	// must honor that earlier scope without inspecting any question/answer text.
	if err := c.Create(ctx, approval); err != nil {
		t.Fatal(err)
	}
	answered := makeAsk("ask-0000000000000003")
	actor := apiv1.AskActor{Issuer: "https://portal.example", Subject: "fixture-human"}
	answer := apiv1.AskAnswerRequest{OptionID: "a", Actor: actor}
	if _, err := store.Answer(ctx, answered.Name, answer, now); err != nil {
		t.Fatal(err)
	}
	withdrawn := makeAsk("ask-0000000000000004")
	if _, err := store.Withdraw(ctx, withdrawn.Name, req, now); err != nil {
		t.Fatal(err)
	}
	stale := makeAsk("ask-0000000000000005")
	stale.Status.State = "expired"
	if err := c.Status().Update(ctx, stale); err != nil {
		t.Fatal(err)
	}
	forceStateLabel(t, c, ctx, open.Name, "expired")
	before := load(open.Name)
	disabledReader := &countAskReader{Reader: c}
	off := Sweeper{Store: KubeStore{Client: c, Live: disabledReader, Namespace: Namespace}, Clock: clk}
	off.Sweep(ctx)
	if err := off.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if disabledReader.reads != 0 || load(open.Name).ResourceVersion != before.ResourceVersion {
		t.Fatal("disabled sweeper touched Kubernetes")
	}
	s := Sweeper{Enabled: true, Store: store, Clock: clk}
	if !s.NeedLeaderElection() {
		t.Fatal("sweeper is not leader only")
	}
	clk.Step(59 * time.Second)
	s.Sweep(ctx)
	got := load(open.Name)
	if got.Status.State != "" || got.Labels[StateLabel] != "open" {
		t.Fatalf("premature expiry or failed open label repair: %+v", got.Status)
	}
	if load(stale.Name).Labels[StateLabel] != "expired" {
		t.Fatal("terminal expiry label was not repaired")
	}
	answeredBefore, withdrawnBefore := load(answered.Name), load(withdrawn.Name)
	clk.Step(time.Second)
	s.Sweep(ctx)
	for _, id := range []string{open.Name, approval.Name} {
		got := load(id)
		if got.Status.State != "expired" || got.Labels[StateLabel] != "expired" || got.Status.Answer != nil {
			t.Fatalf("not expired on time: %s %+v", id, got.Status)
		}
	}
	if got := load(answered.Name); got.ResourceVersion != answeredBefore.ResourceVersion || got.Status.State != "answered" {
		t.Fatal("answered Ask was changed")
	}
	if got := load(withdrawn.Name); got.ResourceVersion != withdrawnBefore.ResourceVersion || got.Status.State != "withdrawn" {
		t.Fatal("withdrawn Ask was changed")
	}

	race := makeAsk("ask-0000000000000006")
	// An answer already validated just before expiry commits after the
	// sweeper's read. The real API server must reject the stale status write.
	writes := &answerBeforeExpiryClient{Client: c, id: race.Name, answer: func() {
		if _, err := store.Answer(ctx, race.Name, answer, now.Add(59*time.Second)); err != nil {
			t.Fatal(err)
		}
	}}
	s.Store.Client = writes
	s.Sweep(ctx)
	got = load(race.Name)
	if writes.conflicts != 1 || got.Status.State != "answered" || got.Status.Answer == nil || got.Labels[StateLabel] != "answered" {
		t.Fatalf("answer race lost: conflicts=%d state=%s label=%s", writes.conflicts, got.Status.State, got.Labels[StateLabel])
	}
}

type countAskReader struct {
	client.Reader
	reads int
}

func (r *countAskReader) Get(ctx context.Context, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
	r.reads++
	return r.Reader.Get(ctx, k, o, opts...)
}
func (r *countAskReader) List(ctx context.Context, l client.ObjectList, opts ...client.ListOption) error {
	r.reads++
	return r.Reader.List(ctx, l, opts...)
}

type answerBeforeExpiryClient struct {
	client.Client
	id        string
	answer    func()
	conflicts int
}

func (c *answerBeforeExpiryClient) Status() client.SubResourceWriter {
	return &answerBeforeExpiryStatus{SubResourceWriter: c.Client.Status(), parent: c}
}

type answerBeforeExpiryStatus struct {
	client.SubResourceWriter
	parent *answerBeforeExpiryClient
}

func (s *answerBeforeExpiryStatus) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if obj.GetName() == s.parent.id && s.parent.answer != nil {
		fn := s.parent.answer
		s.parent.answer = nil
		fn()
	}
	err := s.SubResourceWriter.Update(ctx, obj, opts...)
	if apierrors.IsConflict(err) {
		s.parent.conflicts++
	}
	return err
}
