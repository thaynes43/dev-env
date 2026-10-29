package asks

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/testenv"
)

func TestEnvtestAskStore(t *testing.T) {
	env, err := testenv.Start()
	if err != nil {
		t.Fatalf("envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Log("envtest stop:", err)
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
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC)
	store := KubeStore{Client: c, Live: c, Namespace: Namespace, Log: logr.Discard()}
	req := Requester{Namespace: Namespace, Name: "ask-sess-a", UID: "uid-ask-a"}
	actor := apiv1.AskActor{Issuer: "https://portal.example/issuer", Subject: "portal-subject-256", DisplayName: "Board user"}

	first, created, err := store.Create(ctx, req, infoCreate("request-a1"), now)
	if err != nil || !created || first.State != apiv1.AskStateOpen || first.Context.SessionName != req.Name || first.Delivery != nil {
		t.Fatalf("create %+v created %v err %v", first, created, err)
	}
	if err := apiv1.ValidateAsk(first); err != nil {
		t.Fatal(err)
	}
	again, created, err := store.Create(ctx, req, infoCreate("request-a1"), now.Add(time.Minute))
	if err != nil || created || again.ID != first.ID || !again.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("replay %+v created %v err %v", again, created, err)
	}
	changed := infoCreate("request-a1")
	changed.Question = "A different question?"
	if _, _, err := store.Create(ctx, req, changed, now); !errors.Is(err, ErrConflict) {
		t.Fatal("changed body was not a conflict", err)
	}

	var stored coord.Ask
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: first.ID}, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.CreatedAt.Nanosecond() != 0 || stored.Spec.ExpiresAt.Nanosecond() != 0 {
		t.Fatalf("stored times %v %v", stored.Spec.CreatedAt, stored.Spec.ExpiresAt)
	}
	stored.Spec.Question = "mutated"
	if err := c.Update(ctx, &stored); err == nil {
		t.Fatal("spec update was accepted")
	}

	second, _, err := store.Create(ctx, req, infoCreate("request-a2"), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	mine, _, err := store.ListByRequester(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	page, err := Page(mine, PageMine, 1, "")
	if err != nil || len(page.Asks) != 1 || page.Asks[0].ID != second.ID || page.Complete {
		t.Fatalf("newest page %+v err %v", page, err)
	}
	page, err = Page(mine, PageMine, 1, page.NextCursor)
	if err != nil || len(page.Asks) != 1 || page.Asks[0].ID != first.ID || !page.Complete {
		t.Fatalf("older page %+v err %v", page, err)
	}

	other := Requester{Namespace: Namespace, Name: "ask-sess-b", UID: "uid-ask-b"}
	if _, err := store.Withdraw(ctx, first.ID, other, now); !errors.Is(err, ErrNotFound) {
		t.Fatal("other session withdrew", err)
	}
	withdrawn, err := store.Withdraw(ctx, first.ID, req, now)
	if err != nil || withdrawn.State != apiv1.AskStateWithdrawn {
		t.Fatal(err, withdrawn.State)
	}
	if _, err := store.Withdraw(ctx, first.ID, req, now); !errors.Is(err, apiv1.ErrAskNotOpen) {
		t.Fatal("second withdraw", err)
	}

	approvalIn := infoCreate("request-a3")
	approvalIn.Type = apiv1.AskTypeApproval
	approvalIn.ExpiresIn = ""
	approvalIn.Options = nil
	approvalIn.Recommended = ""
	approvalIn.AllowFreeText = false
	approvalIn.Question = "Deploy the change?"
	approvalIn.Scope = &apiv1.AskScope{Action: "deploy", Target: "dev-env", ExpiresAt: now.Add(2 * time.Hour)}
	approval, _, err := store.Create(ctx, req, approvalIn, now)
	if err != nil || approval.Scope == nil || !approval.ExpiresAt.Equal(approval.Scope.ExpiresAt) {
		t.Fatalf("approval %+v err %v", approval, err)
	}
	var before coord.InboxMessageList
	if err := c.List(ctx, &before); err != nil {
		t.Fatal(err)
	}
	yes := true
	answered, err := store.Answer(ctx, approval.ID, apiv1.AskAnswerRequest{Approved: &yes, Text: "ship\r\nit", Actor: actor}, now)
	if err != nil || answered.State != apiv1.AskStateAnswered || answered.Answer == nil || answered.Answer.Text != "ship\nit" || answered.Delivery != nil {
		t.Fatalf("answer %+v err %v", answered, err)
	}
	replay, err := store.Answer(ctx, approval.ID, apiv1.AskAnswerRequest{Approved: &yes, Text: "ship\nit", Actor: actor}, now)
	if err != nil || replay.Answer == nil || !replay.Answer.AnsweredAt.Equal(answered.Answer.AnsweredAt) {
		t.Fatal("identical answer", err)
	}
	renamed := actor
	renamed.DisplayName = "Renamed user"
	sameName, err := store.Answer(ctx, approval.ID, apiv1.AskAnswerRequest{Approved: &yes, Text: "ship\nit", Actor: renamed}, now)
	if err != nil || sameName.Answer == nil || !sameName.Answer.AnsweredAt.Equal(answered.Answer.AnsweredAt) || sameName.Answer.AnsweredBy.DisplayName != actor.DisplayName {
		t.Fatal("display name is not part of the retry", err, sameName.Answer)
	}
	otherActor := actor
	otherActor.Subject = "other-subject"
	if _, err := store.Answer(ctx, approval.ID, apiv1.AskAnswerRequest{Approved: &yes, Text: "ship\nit", Actor: otherActor}, now); !errors.Is(err, apiv1.ErrAskNotOpen) {
		t.Fatal("other subject was treated as the same answer", err)
	}
	no := false
	if _, err := store.Answer(ctx, approval.ID, apiv1.AskAnswerRequest{Approved: &no, Actor: actor}, now); !errors.Is(err, apiv1.ErrAskNotOpen) {
		t.Fatal("replaced answer", err)
	}
	var after coord.InboxMessageList
	if err := c.List(ctx, &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != len(before.Items) {
		t.Fatalf("inbox messages %d to %d", len(before.Items), len(after.Items))
	}

	raceIn := approvalIn
	raceIn.RequestID = "request-a4"
	race, _, err := store.Create(ctx, req, raceIn, now)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)
	for i, ok := range []bool{true, false} {
		wg.Add(1)
		go func(i int, ok bool) {
			defer wg.Done()
			<-start
			approved := ok
			_, errs[i] = store.Answer(ctx, race.ID, apiv1.AskAnswerRequest{Approved: &approved, Actor: actor}, now)
		}(i, ok)
	}
	close(start)
	wg.Wait()
	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, apiv1.ErrAskNotOpen), errors.Is(err, ErrConflict):
		default:
			t.Fatal("race", err)
		}
	}
	if wins != 1 {
		t.Fatalf("race wins %d errs %v", wins, errs)
	}

	short := infoCreate("request-a5")
	short.ExpiresIn = "1m"
	lapsed, _, err := store.Create(ctx, req, short, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Answer(ctx, lapsed.ID, apiv1.AskAnswerRequest{OptionID: "a", Actor: actor}, now.Add(time.Minute)); !errors.Is(err, apiv1.ErrAskExpired) {
		t.Fatal("expired answer", err)
	}
	if _, err := store.Withdraw(ctx, lapsed.ID, req, now.Add(time.Minute)); !errors.Is(err, apiv1.ErrAskNotOpen) {
		t.Fatal("expired withdraw", err)
	}
	fraction := approvalIn
	fraction.RequestID = "request-a6"
	fraction.Scope = &apiv1.AskScope{Action: "deploy", Target: "dev-env", ExpiresAt: now.Add(2*time.Hour + time.Millisecond)}
	if _, _, err := store.Create(ctx, req, fraction, now); !errors.Is(err, apiv1.ErrAskInvalid) {
		t.Fatal("fractional scope stored", err)
	}

	tightReq := Requester{Namespace: Namespace, Name: "ask-sess-c", UID: "uid-ask-c"}
	tight := store
	tight.Limits = Limits{OpenPerRequester: 5, TotalPerRequester: 8, PerNamespace: 4096}
	beforeNear := counterValue(LimitOpen)
	for _, id := range []string{"request-c1", "request-c2", "request-c3", "request-c4"} {
		if _, _, err := tight.Create(ctx, tightReq, infoCreate(id), now); err != nil {
			t.Fatal(id, err)
		}
	}
	if _, _, err := tight.Create(ctx, tightReq, infoCreate("request-c5"), now); err != nil {
		t.Fatal(err)
	}
	if counterValue(LimitOpen) <= beforeNear {
		t.Fatal("80 percent was not recorded")
	}
	if _, _, err := tight.Create(ctx, tightReq, infoCreate("request-c6"), now); !errors.Is(err, ErrLimit) {
		t.Fatal("open cap", err)
	}
	one := store
	one.Limits = Limits{OpenPerRequester: 1, TotalPerRequester: 8}
	slot := Requester{Namespace: Namespace, Name: "ask-sess-d", UID: "uid-ask-d"}
	held, _, err := one.Create(ctx, slot, infoCreate("request-d1"), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := one.Create(ctx, slot, infoCreate("request-d2"), now); !errors.Is(err, ErrLimit) {
		t.Fatal("second open ask", err)
	}
	if _, err := one.Withdraw(ctx, held.ID, slot, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := one.Create(ctx, slot, infoCreate("request-d2"), now); err != nil {
		t.Fatal("open slot did not free", err)
	}
	life := store
	life.Limits = Limits{OpenPerRequester: 16, TotalPerRequester: 1}
	lifeReq := Requester{Namespace: Namespace, Name: "ask-sess-e", UID: "uid-ask-e"}
	lifeAsk, _, err := life.Create(ctx, lifeReq, infoCreate("request-e1"), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := life.Withdraw(ctx, lifeAsk.ID, lifeReq, now); err != nil {
		t.Fatal(err)
	}
	_, _, err = life.Create(ctx, lifeReq, infoCreate("request-e2"), now)
	var lim *LimitError
	if !errors.As(err, &lim) || lim.Scope != LimitRequester {
		t.Fatal("total cap freed on withdraw", err)
	}
	var existing coord.AskList
	if err := c.List(ctx, &existing, client.InNamespace(Namespace)); err != nil {
		t.Fatal(err)
	}
	full := store
	full.Limits = Limits{PerNamespace: int64(len(existing.Items))}
	nsReq := Requester{Namespace: Namespace, Name: "ask-sess-f", UID: "uid-ask-f"}
	_, _, err = full.Create(ctx, nsReq, infoCreate("request-f1"), now)
	if !errors.As(err, &lim) || lim.Scope != LimitNamespace {
		t.Fatal("namespace cap", err)
	}

	scopeAt := now.Add(2 * time.Hour)
	retryReq := Requester{Namespace: Namespace, Name: "ask-sess-i", UID: "uid-ask-i"}
	retryBody := apiv1.AskCreateRequest{
		RequestID: "request-i1", Type: apiv1.AskTypeApproval, Question: "Deploy the change?",
		Scope:     &apiv1.AskScope{Action: "deploy", Target: "dev-env", ExpiresAt: scopeAt},
		ExpiresIn: "2h",
	}
	retryAsk, created, err := store.Create(ctx, retryReq, retryBody, now)
	if err != nil || !created {
		t.Fatal("approval create", err, created)
	}
	retried, created, err := store.Create(ctx, retryReq, retryBody, now.Add(time.Second))
	if err != nil || created || retried.ID != retryAsk.ID {
		t.Fatalf("retry after 1s %+v created %v err %v", retried, created, err)
	}
	late := scopeAt.Add(-time.Minute + time.Second)
	third, created, err := store.Create(ctx, retryReq, retryBody, late)
	if err != nil || created || third.ID != retryAsk.ID {
		t.Fatalf("retry past scope window %+v created %v err %v", third, created, err)
	}

	staleReq := Requester{Namespace: Namespace, Name: "ask-sess-g", UID: "uid-ask-g"}
	stale := store
	stale.Limits = Limits{OpenPerRequester: 1, TotalPerRequester: 8, PerNamespace: 4096}
	staleHeld, _, err := stale.Create(ctx, staleReq, infoCreate("request-g1"), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stale.Answer(ctx, staleHeld.ID, apiv1.AskAnswerRequest{OptionID: "a", Actor: actor}, now); err != nil {
		t.Fatal(err)
	}
	forceStateLabel(t, c, ctx, staleHeld.ID, string(apiv1.AskStateOpen))
	if _, created, err := stale.Create(ctx, staleReq, infoCreate("request-g2"), now); err != nil || !created {
		t.Fatal("stale open label locked the session", err, created)
	}
	var repaired coord.Ask
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: staleHeld.ID}, &repaired); err != nil {
		t.Fatal(err)
	}
	if repaired.Labels[StateLabel] != string(apiv1.AskStateAnswered) {
		t.Fatalf("label stayed %q", repaired.Labels[StateLabel])
	}

	lapseReq := Requester{Namespace: Namespace, Name: "ask-sess-h", UID: "uid-ask-h"}
	lapseStore := store
	lapseStore.Limits = Limits{OpenPerRequester: 1, TotalPerRequester: 8, PerNamespace: 4096}
	shortLife := infoCreate("request-h1")
	shortLife.ExpiresIn = "1m"
	lapsedAsk, _, err := lapseStore.Create(ctx, lapseReq, shortLife, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := lapseStore.Create(ctx, lapseReq, infoCreate("request-h2"), now); !errors.Is(err, ErrLimit) {
		t.Fatal("open slot was not full", err)
	}
	if _, created, err := lapseStore.Create(ctx, lapseReq, infoCreate("request-h2"), now.Add(time.Minute)); err != nil || !created {
		t.Fatal("lapsed ask still consumed the open slot", err, created)
	}
	var lapsedStored coord.Ask
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: lapsedAsk.ID}, &lapsedStored); err != nil {
		t.Fatal(err)
	}
	if lapsedStored.Labels[StateLabel] != string(apiv1.AskStateExpired) {
		t.Fatalf("lapsed label %q", lapsedStored.Labels[StateLabel])
	}

	t.Run("unconfirmed open recount", func(t *testing.T) {
		unconfirmedReq := Requester{Namespace: Namespace, Name: "ask-sess-k", UID: "uid-ask-k"}
		unconfirmed := store
		unconfirmed.Limits = Limits{OpenPerRequester: 1, TotalPerRequester: 8, PerNamespace: 4096}
		bad := storedAsk(unconfirmedReq, "ask-0123456789aaa004", "request-k1", now, now)
		if err := c.Create(ctx, bad); err != nil {
			t.Fatal(err)
		}
		if _, _, err := unconfirmed.Create(ctx, unconfirmedReq, infoCreate("request-k2"), now); !errors.Is(err, ErrUnavailable) {
			t.Fatal("unconfirmed open count did not refuse the create", err)
		}
	})

	nilReq := Requester{Namespace: Namespace, Name: "ask-sess-j", UID: "uid-ask-j"}
	nilAsk, _, err := store.Create(ctx, nilReq, infoCreate("request-j1"), now)
	if err != nil {
		t.Fatal(err)
	}
	var unlabeled coord.Ask
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: nilAsk.ID}, &unlabeled); err != nil {
		t.Fatal(err)
	}
	if err := c.Patch(ctx, &unlabeled, client.RawPatch(types.JSONPatchType, []byte(`[{"op":"remove","path":"/metadata/labels"}]`))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Answer(ctx, nilAsk.ID, apiv1.AskAnswerRequest{OptionID: "a", Actor: actor}, now); err != nil {
		t.Fatal("nil labels panicked or refused the answer", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: nilAsk.ID}, &unlabeled); err != nil {
		t.Fatal(err)
	}
	if unlabeled.Labels[StateLabel] != string(apiv1.AskStateAnswered) {
		t.Fatalf("label after nil map %v", unlabeled.Labels)
	}

	ownedBody := infoCreate("request-own1")
	norm, err := normalizeCreate(ownedBody)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := requestDigest(norm)
	if err != nil {
		t.Fatal(err)
	}
	ownedName := Name(req, ownedBody.RequestID)
	foreign := &coord.Ask{
		ObjectMeta: metav1.ObjectMeta{
			Name: ownedName, Namespace: Namespace,
			Labels: map[string]string{RequesterLabel: "not-the-owner", StateLabel: string(apiv1.AskStateOpen)},
		},
		Spec: coord.AskSpec{
			ID: ownedName, Type: "info", Question: norm.Question,
			Options: []coord.AskOption{{ID: "a", Label: "Alpha"}}, AllowFreeText: true, Recommended: "a",
			Context:       coord.AskContext{SessionName: "other-session", SessionUID: "other-uid"},
			CreatedAt:     metav1.NewTime(now),
			ExpiresAt:     metav1.NewTime(now.Add(time.Hour)),
			RequestID:     ownedBody.RequestID,
			RequestDigest: digest,
		},
	}
	if err := c.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create(ctx, req, ownedBody, now); !errors.Is(err, ErrConflict) {
		t.Fatal("retry of an ask this session does not own", err)
	}

	bare := storedAsk(req, "ask-0123456789aaa001", "request-bare1", now, now.Add(time.Hour))
	if err := c.Create(ctx, bare); err != nil {
		t.Fatal(err)
	}
	forged := storedAsk(req, "ask-0123456789aaa002", "request-forged1", now, now.Add(time.Hour))
	forged.Spec.Context = coord.AskContext{SessionName: "other-session", SessionUID: "other-uid"}
	if err := c.Create(ctx, forged); err != nil {
		t.Fatal(err)
	}
	bad := storedAsk(req, "ask-0123456789aaa003", "request-badrec1", now, now)
	if err := c.Create(ctx, bad); err != nil {
		t.Fatal(err)
	}
	listed, incomplete, err := store.ListByRequester(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !incomplete || !hasAsk(listed, bare.Name) || hasAsk(listed, forged.Name) || hasAsk(listed, bad.Name) {
		t.Fatalf("mine incomplete %v ids %v", incomplete, askIDsOf(listed))
	}
	if got := askByID(listed, bare.Name); got.State != apiv1.AskStateOpen {
		t.Fatalf("absent status state %q", got.State)
	}
	openListed, openIncomplete, err := store.ListOpen(ctx)
	if err != nil || !openIncomplete || !hasAsk(openListed, bare.Name) {
		t.Fatal("open list", err, openIncomplete, hasAsk(openListed, bare.Name))
	}
	allListed, allIncomplete, err := store.ListNamespace(ctx)
	if err != nil || !allIncomplete || !hasAsk(allListed, bare.Name) || hasAsk(allListed, bad.Name) {
		t.Fatal("namespace list", err, allIncomplete)
	}
}

func forceStateLabel(t *testing.T, c client.Client, ctx context.Context, name, state string) {
	t.Helper()
	var obj coord.Ask
	if err := c.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: name}, &obj); err != nil {
		t.Fatal(err)
	}
	base := obj.DeepCopy()
	if obj.Labels == nil {
		obj.Labels = map[string]string{}
	}
	obj.Labels[StateLabel] = state
	if err := c.Patch(ctx, &obj, client.MergeFrom(base)); err != nil {
		t.Fatal(err)
	}
}

func storedAsk(req Requester, name, requestID string, created, expires time.Time) *coord.Ask {
	return &coord.Ask{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: Namespace,
			Labels: map[string]string{RequesterLabel: requesterValue(req), StateLabel: string(apiv1.AskStateOpen)},
		},
		Spec: coord.AskSpec{
			ID: name, Type: "info", Question: "Which option?",
			Options: []coord.AskOption{{ID: "a", Label: "Alpha"}}, AllowFreeText: true, Recommended: "a",
			Context:       coord.AskContext{SessionName: req.Name, SessionUID: req.UID},
			CreatedAt:     metav1.NewTime(created),
			ExpiresAt:     metav1.NewTime(expires),
			RequestID:     requestID,
			RequestDigest: strings.Repeat("ab", 32),
		},
	}
}

func hasAsk(items []apiv1.Ask, id string) bool {
	return askByID(items, id).ID == id
}

func askByID(items []apiv1.Ask, id string) apiv1.Ask {
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	return apiv1.Ask{}
}

func askIDsOf(items []apiv1.Ask) []string {
	out := make([]string, len(items))
	for i := range items {
		out[i] = items[i].ID
	}
	return out
}

func counterValue(scope string) float64 {
	var metric dto.Metric
	if err := NearLimit.WithLabelValues(scope).Write(&metric); err != nil || metric.Counter == nil {
		return 0
	}
	return metric.Counter.GetValue()
}
