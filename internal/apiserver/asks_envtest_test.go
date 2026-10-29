package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/asks"
	"github.com/thaynes43/dev-env/internal/controller"
)

func TestEnvtestAskRoutes(t *testing.T) {
	cfg := envConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	k8s, err := client.New(cfg, client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatal(err)
	}
	if err := k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: sessionNS}}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	t.Cleanup(func() { deleteAskFixtures(k8s) })
	api := &askAPI{t: t, k8s: k8s, auth: fakeAuth{}, now: time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC)}
	api.auth[tokHuman] = saIdentity(humanSA)
	owner := api.session("ask-lane-owner")
	other := api.session("ask-lane-other")
	portal := api.portal()
	api.srv = &Server{
		Client: k8s, Live: k8s, Auth: api.auth,
		Policy: Policy{
			Human: humanSA, Clients: []string{clientSA},
			SessionNamespace: sessionNS, SessionServiceAccount: controller.ServiceAccountName,
			PortalReaderEnabled: true,
		},
		Asks: &asks.KubeStore{Client: k8s, Live: k8s, Namespace: asks.Namespace, Log: logr.Discard()},
		Log:  logr.Discard(),
		Now:  func() time.Time { return api.now },
	}
	api.h = api.srv.Handler()

	wantError(t, api.do(http.MethodPost, apiv1.AsksPath, tokHuman, askInfo("request-h1")), http.StatusForbidden, apiv1.CodeForbidden)
	wantError(t, api.do(http.MethodPost, apiv1.AsksPath, portal, askInfo("request-p1")), http.StatusForbidden, apiv1.CodeForbidden)
	wantError(t, api.do(http.MethodGet, apiv1.AsksPath, owner, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	wantError(t, api.do(http.MethodGet, apiv1.AsksPath+"?mine=true&limit=101", owner, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	wantError(t, api.do(http.MethodGet, apiv1.PortalAsksPath, portal, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	bad := askInfo("request-bad")
	bad.Question = ""
	wantError(t, api.do(http.MethodPost, apiv1.AsksPath, owner, bad), http.StatusBadRequest, apiv1.CodeBadRequest)

	created := api.post(owner, askInfo("request-a1"), http.StatusCreated)
	if !apiv1.ValidAskID(created.ID) || created.State != apiv1.AskStateOpen || created.Context.SessionName != "ask-lane-owner" || created.Delivery != nil {
		t.Fatalf("%+v", created)
	}
	replay := api.post(owner, askInfo("request-a1"), http.StatusOK)
	if replay.ID != created.ID || !replay.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("replay %+v", replay)
	}
	changed := askInfo("request-a1")
	changed.Question = "A different question?"
	wantError(t, api.do(http.MethodPost, apiv1.AsksPath, owner, changed), http.StatusConflict, apiv1.CodeConflict)
	wantError(t, api.do(http.MethodGet, apiv1.AskPath(created.ID), other, nil), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, api.do(http.MethodPost, apiv1.AskWithdrawPath(created.ID), other, nil), http.StatusNotFound, apiv1.CodeNotFound)
	if got := api.get(owner, apiv1.AskPath(created.ID)); got.ID != created.ID {
		t.Fatal(got.ID)
	}

	api.now = api.now.Add(time.Second)
	second := api.post(owner, askInfo("request-a2"), http.StatusCreated)
	page := api.list(owner, apiv1.AsksPath+"?mine=true&limit=1")
	if len(page.Asks) != 1 || page.Asks[0].ID != second.ID || page.Complete || page.NextCursor == "" {
		t.Fatalf("newest %+v", page)
	}
	older := api.list(owner, apiv1.AsksPath+"?mine=true&limit=1&cursor="+page.NextCursor)
	if len(older.Asks) != 1 || older.Asks[0].ID != created.ID || !older.Complete {
		t.Fatalf("older %+v", older)
	}
	if api.list(other, apiv1.AsksPath+"?mine=true").Asks == nil || len(api.list(other, apiv1.AsksPath+"?mine=true").Asks) != 0 {
		t.Fatal("the other session saw an ask")
	}
	open := api.list(portal, apiv1.PortalAsksPath+"?state=open&limit=100")
	if !askIDs(open).has(created.ID) || !askIDs(open).has(second.ID) {
		t.Fatalf("portal open %+v", open.Asks)
	}
	if got := api.get(portal, apiv1.PortalAskPath(created.ID)); got.Question != created.Question {
		t.Fatal(got.Question)
	}

	wantError(t, api.do(http.MethodPost, apiv1.AsksPath+"/"+created.ID+"/answer", owner, askChoice("a")), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, api.do(http.MethodPost, apiv1.PortalAskAnswerPath(created.ID), owner, askChoice("a")), http.StatusForbidden, apiv1.CodeForbidden)
	var before coord.InboxMessageList
	if err := k8s.List(ctx, &before); err != nil {
		t.Fatal(err)
	}
	answered := decode[apiv1.Ask](t, api.want(http.MethodPost, apiv1.PortalAskAnswerPath(created.ID), portal, askChoice("a"), http.StatusOK))
	if answered.State != apiv1.AskStateAnswered || answered.Answer == nil || answered.Answer.AnsweredBy.Issuer == "" || answered.Delivery != nil {
		t.Fatalf("%+v", answered)
	}
	sessionRaw := api.want(http.MethodGet, apiv1.AskPath(created.ID), owner, nil, http.StatusOK).Body.String()
	if strings.Contains(sessionRaw, "https://ask-issuer.example") || strings.Contains(sessionRaw, "portal-subject-256") || strings.Contains(sessionRaw, `"delivery"`) || !strings.Contains(sessionRaw, "Board user") {
		t.Fatal(sessionRaw)
	}
	replayRaw := api.want(http.MethodPost, apiv1.AsksPath, owner, askInfo("request-a1"), http.StatusOK).Body.String()
	if strings.Contains(replayRaw, "https://ask-issuer.example") || strings.Contains(replayRaw, "portal-subject-256") || strings.Contains(replayRaw, `"delivery"`) || !strings.Contains(replayRaw, "Board user") {
		t.Fatal(replayRaw)
	}
	listRaw := api.want(http.MethodGet, apiv1.AsksPath+"?mine=true&limit=100", owner, nil, http.StatusOK).Body.String()
	if strings.Contains(listRaw, "https://ask-issuer.example") || strings.Contains(listRaw, "portal-subject-256") || strings.Contains(listRaw, `"delivery"`) || !strings.Contains(listRaw, "Board user") {
		t.Fatal(listRaw)
	}
	portalRaw := api.want(http.MethodGet, apiv1.PortalAskPath(created.ID), portal, nil, http.StatusOK).Body.String()
	if !strings.Contains(portalRaw, "https://ask-issuer.example") || !strings.Contains(portalRaw, "portal-subject-256") || strings.Contains(portalRaw, `"delivery"`) {
		t.Fatal(portalRaw)
	}
	again := decode[apiv1.Ask](t, api.want(http.MethodPost, apiv1.PortalAskAnswerPath(created.ID), portal, askChoice("a"), http.StatusOK))
	if again.Answer == nil || !again.Answer.AnsweredAt.Equal(answered.Answer.AnsweredAt) {
		t.Fatal("identical answer changed the record")
	}
	otherActor := askChoice("a")
	otherActor.Actor.Subject = "other-subject-256"
	wantError(t, api.do(http.MethodPost, apiv1.PortalAskAnswerPath(created.ID), portal, otherActor), http.StatusConflict, apiv1.CodeConflict)
	renamed := askChoice("a")
	renamed.Actor.DisplayName = "Renamed user"
	keptName := decode[apiv1.Ask](t, api.want(http.MethodPost, apiv1.PortalAskAnswerPath(created.ID), portal, renamed, http.StatusOK))
	if keptName.Answer == nil || !keptName.Answer.AnsweredAt.Equal(answered.Answer.AnsweredAt) || keptName.Answer.AnsweredBy.DisplayName != "Board user" {
		t.Fatalf("display name retry %+v", keptName.Answer)
	}
	wantError(t, api.do(http.MethodPost, apiv1.PortalAskAnswerPath(created.ID), portal, askChoice("b")), http.StatusConflict, apiv1.CodeConflict)
	var after coord.InboxMessageList
	if err := k8s.List(ctx, &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != len(before.Items) {
		t.Fatalf("inbox messages %d to %d", len(before.Items), len(after.Items))
	}

	withdrawRaw := api.want(http.MethodPost, apiv1.AskWithdrawPath(second.ID), owner, nil, http.StatusOK).Body.String()
	if strings.Contains(withdrawRaw, "https://ask-issuer.example") || strings.Contains(withdrawRaw, "portal-subject-256") || strings.Contains(withdrawRaw, `"delivery"`) || strings.Contains(withdrawRaw, `"issuer"`) {
		t.Fatal(withdrawRaw)
	}
	var withdrawn apiv1.Ask
	if err := json.Unmarshal([]byte(withdrawRaw), &withdrawn); err != nil || withdrawn.State != apiv1.AskStateWithdrawn {
		t.Fatal(err, withdrawRaw)
	}
	wantError(t, api.do(http.MethodPost, apiv1.AskWithdrawPath(second.ID), owner, nil), http.StatusConflict, apiv1.CodeConflict)
	wantError(t, api.do(http.MethodPost, apiv1.PortalAsksPath+"/"+second.ID+"/withdraw", portal, nil), http.StatusNotFound, apiv1.CodeNotFound)
	if askIDs(api.list(portal, apiv1.PortalAsksPath+"?state=open&limit=100")).has(second.ID) {
		t.Fatal("withdrawn ask stayed on the open board")
	}

	approval := api.post(owner, askApproval("request-ap", api.now.Add(2*time.Hour)), http.StatusCreated)
	if approval.Scope == nil || !approval.ExpiresAt.Equal(approval.Scope.ExpiresAt) || approval.ExpiresAt.Nanosecond() != 0 {
		t.Fatalf("approval expiry %+v", approval)
	}
	fraction := askApproval("request-fr", api.now.Add(2*time.Hour+time.Millisecond))
	wantError(t, api.do(http.MethodPost, apiv1.AsksPath, owner, fraction), http.StatusBadRequest, apiv1.CodeBadRequest)

	short := askInfo("request-ex")
	short.ExpiresIn = "1m"
	lapsed := api.post(owner, short, http.StatusCreated)
	api.now = lapsed.ExpiresAt
	wantError(t, api.do(http.MethodPost, apiv1.PortalAskAnswerPath(lapsed.ID), portal, askChoice("a")), http.StatusGone, "expired")
	wantError(t, api.do(http.MethodPost, apiv1.AskWithdrawPath(lapsed.ID), owner, nil), http.StatusConflict, apiv1.CodeConflict)

	yes := true
	no := false
	race := api.post(owner, askApproval("request-race", api.now.Add(2*time.Hour)), http.StatusCreated)
	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, 2)
	bodies := make([]string, 2)
	for i, ok := range []bool{yes, no} {
		wg.Add(1)
		go func(i int, ok bool) {
			defer wg.Done()
			<-start
			w := api.do(http.MethodPost, apiv1.PortalAskAnswerPath(race.ID), portal, askDecision(ok))
			codes[i], bodies[i] = w.Code, w.Body.String()
		}(i, ok)
	}
	close(start)
	wg.Wait()
	wins := 0
	for i, code := range codes {
		switch code {
		case http.StatusOK:
			wins++
		case http.StatusConflict:
		default:
			t.Fatalf("race status %d body %s", code, bodies[i])
		}
	}
	if wins != 1 {
		t.Fatalf("race wins %d bodies %v", wins, bodies)
	}

	scopeAt := api.now.Add(2 * time.Hour)
	retryBody := askApproval("request-retry1", scopeAt)
	retryBody.ExpiresIn = "2h"
	retryAsk := api.post(owner, retryBody, http.StatusCreated)
	api.now = api.now.Add(time.Second)
	if again := api.post(owner, retryBody, http.StatusOK); again.ID != retryAsk.ID {
		t.Fatalf("retry id %s", again.ID)
	}
	api.now = scopeAt.Add(-time.Minute + time.Second)
	if late := api.post(owner, retryBody, http.StatusOK); late.ID != retryAsk.ID {
		t.Fatalf("late retry id %s", late.ID)
	}

	var src coord.Ask
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: sessionNS, Name: retryAsk.ID}, &src); err != nil {
		t.Fatal(err)
	}
	bare := cloneAsk(src, "ask-0123456789aaa001", "request-bare01")
	if err := k8s.Create(ctx, bare); err != nil {
		t.Fatal(err)
	}
	forged := cloneAsk(src, "ask-0123456789aaa002", "request-forge01")
	forged.Spec.Context.SessionName = "other-session"
	forged.Spec.Context.SessionUID = "other-uid"
	if err := k8s.Create(ctx, forged); err != nil {
		t.Fatal(err)
	}
	mine := api.list(owner, apiv1.AsksPath+"?mine=true&limit=100")
	if !askIDs(mine).has(bare.Name) || askIDs(mine).has(forged.Name) {
		t.Fatalf("mine bare %v forged %v", askIDs(mine).has(bare.Name), askIDs(mine).has(forged.Name))
	}
	if got := api.get(portal, apiv1.PortalAskPath(bare.Name)); got.State != apiv1.AskStateOpen {
		t.Fatalf("absent status state %s", got.State)
	}
	if openPage := api.list(portal, apiv1.PortalAsksPath+"?state=open&limit=100"); !askIDs(openPage).has(bare.Name) {
		t.Fatal("portal open omitted the ask with no status")
	}
	if allPage := api.list(portal, apiv1.PortalAsksPath+"?state=all&limit=100"); !askIDs(allPage).has(bare.Name) || !askIDs(allPage).has(forged.Name) {
		t.Fatal("portal all omitted a readable ask")
	}
	unreadable := cloneAsk(src, "ask-0123456789aaa003", "request-badrec1")
	unreadable.Spec.ExpiresAt = unreadable.Spec.CreatedAt
	if err := k8s.Create(ctx, unreadable); err != nil {
		t.Fatal(err)
	}
	broken := api.list(owner, apiv1.AsksPath+"?mine=true&limit=100")
	if broken.Complete || !askIDs(broken).has(bare.Name) || askIDs(broken).has(unreadable.Name) {
		t.Fatalf("mine complete %v has bare %v has bad %v", broken.Complete, askIDs(broken).has(bare.Name), askIDs(broken).has(unreadable.Name))
	}
	if portalBroken := api.list(portal, apiv1.PortalAsksPath+"?state=all&limit=100"); portalBroken.Complete || askIDs(portalBroken).has(unreadable.Name) {
		t.Fatal("portal all failed closed or kept the unreadable ask")
	}

	api.srv.Asks.Limits = asks.Limits{OpenPerRequester: 1, TotalPerRequester: 64, PerNamespace: 4096}
	capTok := api.session("ask-lane-cap")
	api.post(capTok, askInfo("request-cap1"), http.StatusCreated)
	wantError(t, api.do(http.MethodPost, apiv1.AsksPath, capTok, askInfo("request-cap2")), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)

	live := api.srv.Asks.Live
	api.srv.Asks.Live = askCountFail{Client: k8s}
	t.Cleanup(func() { api.srv.Asks.Live = live })
	wantError(t, api.do(http.MethodPost, apiv1.AsksPath, capTok, askInfo("request-cap3")), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
}

type askAPI struct {
	t    *testing.T
	k8s  client.Client
	auth fakeAuth
	srv  *Server
	h    http.Handler
	now  time.Time
}

func (a *askAPI) session(name string) string {
	a.t.Helper()
	ctx := context.Background()
	s := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS},
		Spec:       v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p"},
	}
	if err := a.k8s.Create(ctx, s); err != nil {
		a.t.Fatal(err)
	}
	controlled := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, OwnerReferences: []metav1.OwnerReference{{
			APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentSession", Name: s.Name, UID: s.UID, Controller: &controlled,
		}}},
		Spec: corev1.PodSpec{ServiceAccountName: controller.ServiceAccountName, Containers: []corev1.Container{{Name: "agent", Image: "agent"}}},
	}
	if err := a.k8s.Create(ctx, pod); err != nil {
		a.t.Fatal(err)
	}
	if err := a.k8s.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
		a.t.Fatal(err)
	}
	tok := "tok-" + name
	id := saIdentity(sessionNS + "/" + controller.ServiceAccountName)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	a.auth[tok] = id
	return tok
}

func (a *askAPI) portal() string {
	a.t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "ask-lane-portal", Namespace: sessionNS},
		Spec:       corev1.PodSpec{ServiceAccountName: portalReaderAccount, Containers: []corev1.Container{{Name: "portal", Image: "portal"}}},
	}
	if err := a.k8s.Create(context.Background(), pod); err != nil {
		a.t.Fatal(err)
	}
	tok := "tok-ask-portal"
	id := saIdentity(portalReaderRef)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	a.auth[tok] = id
	return tok
}

func (a *askAPI) do(method, path, token string, body any) *httptest.ResponseRecorder {
	a.t.Helper()
	var rd *bytes.Reader
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	default:
		j, err := json.Marshal(b)
		if err != nil {
			a.t.Fatal(err)
		}
		rd = bytes.NewReader(j)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	a.h.ServeHTTP(w, req)
	return w
}

func (a *askAPI) want(method, path, token string, body any, status int) *httptest.ResponseRecorder {
	a.t.Helper()
	w := a.do(method, path, token, body)
	if w.Code != status {
		a.t.Fatalf("%s %s: status %d, want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	return w
}

func (a *askAPI) post(token string, body apiv1.AskCreateRequest, status int) apiv1.Ask {
	a.t.Helper()
	return decode[apiv1.Ask](a.t, a.want(http.MethodPost, apiv1.AsksPath, token, body, status))
}

func (a *askAPI) get(token, path string) apiv1.Ask {
	a.t.Helper()
	return decode[apiv1.Ask](a.t, a.want(http.MethodGet, path, token, nil, http.StatusOK))
}

func (a *askAPI) list(token, path string) apiv1.AskList {
	a.t.Helper()
	return decode[apiv1.AskList](a.t, a.want(http.MethodGet, path, token, nil, http.StatusOK))
}

func askInfo(id string) apiv1.AskCreateRequest {
	return apiv1.AskCreateRequest{
		RequestID: id, Type: apiv1.AskTypeInfo, Question: "Which option?",
		Options:     []apiv1.AskOption{{ID: "a", Label: "Alpha"}, {ID: "b", Label: "Beta"}},
		Recommended: "a", AllowFreeText: true, ExpiresIn: "1h",
	}
}

func askApproval(id string, exp time.Time) apiv1.AskCreateRequest {
	return apiv1.AskCreateRequest{
		RequestID: id, Type: apiv1.AskTypeApproval, Question: "Deploy the change?",
		Scope: &apiv1.AskScope{Action: "deploy", Target: "dev-env", ExpiresAt: exp},
	}
}

func askActor() apiv1.AskActor {
	return apiv1.AskActor{Issuer: "https://ask-issuer.example/board", Subject: "portal-subject-256", DisplayName: "Board user"}
}

func askChoice(option string) apiv1.AskAnswerRequest {
	return apiv1.AskAnswerRequest{OptionID: option, Actor: askActor()}
}

func askDecision(ok bool) apiv1.AskAnswerRequest {
	return apiv1.AskAnswerRequest{Approved: &ok, Actor: askActor()}
}

type askIDSet map[string]bool

func cloneAsk(src coord.Ask, name, requestID string) *coord.Ask {
	obj := src.DeepCopy()
	obj.Name = name
	obj.ResourceVersion = ""
	obj.UID = ""
	obj.Generation = 0
	obj.ManagedFields = nil
	obj.Status = coord.AskStatus{}
	obj.Spec.ID = name
	obj.Spec.RequestID = requestID
	if obj.Labels == nil {
		obj.Labels = map[string]string{}
	}
	obj.Labels[asks.StateLabel] = string(apiv1.AskStateOpen)
	return obj
}

type askCountFail struct{ client.Client }

func (askCountFail) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return apierrors.NewServiceUnavailable("unconfirmed")
}

func askIDs(page apiv1.AskList) askIDSet {
	out := askIDSet{}
	for _, a := range page.Asks {
		out[a.ID] = true
	}
	return out
}

func (s askIDSet) has(id string) bool { return s[id] }

func deleteAskFixtures(k8s client.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var asks coord.AskList
	if err := k8s.List(ctx, &asks, client.InNamespace(sessionNS)); err == nil {
		for i := range asks.Items {
			name := asks.Items[i].Spec.Context.SessionName
			if name == "ask-lane-owner" || name == "ask-lane-other" || name == "ask-lane-cap" || name == "other-session" {
				_ = k8s.Delete(ctx, &asks.Items[i])
			}
		}
	}
	for _, name := range []string{"ask-0123456789aaa001", "ask-0123456789aaa002", "ask-0123456789aaa003"} {
		_ = k8s.Delete(ctx, &coord.Ask{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS}})
	}
	for _, name := range []string{"ask-lane-owner", "ask-lane-other", "ask-lane-cap", "ask-lane-portal"} {
		_ = k8s.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS}})
	}
	for _, name := range []string{"ask-lane-owner", "ask-lane-other", "ask-lane-cap"} {
		_ = k8s.Delete(ctx, &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS}})
	}
}
