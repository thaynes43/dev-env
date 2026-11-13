package apiserver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/sessionraise"
	"github.com/thaynes43/dev-env/internal/templates"
)

func (f *fixture) installRaiseProfiles() {
	f.t.Helper()
	full := f.tmpl.Profiles["full"]
	full.ServiceAccount = templates.WorkbenchServiceAccount
	f.tmpl.Profiles["workbench"] = full
	lead := f.tmpl.Profiles["full"]
	lead.ServiceAccount = "dev-env-coordinator-lead"
	f.tmpl.Profiles["lead"] = lead
}

func (f *fixture) raiseReady(dispatch bool, profiles ...string) {
	f.t.Helper()
	f.installRaiseProfiles()
	ctx := context.Background()
	ns := sessionraise.ObjectNamespace
	for _, obj := range []client.Object{
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: sessionraise.OperatorSecretName}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: sessionraise.OperatorPublicName}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: sessionraise.LedgerName}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: sessionraise.KeeperPublicName}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: sessionraise.NoticeLedgerName}},
	} {
		if err := f.c.Create(ctx, obj); err != nil {
			f.t.Fatal(err)
		}
	}
	f.srv.Raise = &RaiseConfig{Dispatch: dispatch, Profiles: profiles}
	if _, err := sessionraise.Ensure(ctx, f.srv.raiseStore(f.srv.Raise.filled()), sessionraise.OperatorSecretName, sessionraise.OperatorPublicName, true); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) workbench(name string) string {
	f.t.Helper()
	tok := f.sessionPod(name, "workbench", 0)
	s := f.session(name)
	s.Spec.Profile = "workbench"
	if err := f.c.Update(context.Background(), s); err != nil {
		f.t.Fatal(err)
	}
	return tok
}

func (f *fixture) remoteTarget(name string, depth int) *v1alpha1.AgentSession {
	f.t.Helper()
	f.sessionPod(name, "full", depth)
	s := f.session(name)
	s.Spec.Mode = v1alpha1.ModeRemote
	s.Spec.Prompt = ""
	s.Spec.Agent = v1alpha1.AgentClaude
	if err := f.c.Update(context.Background(), s); err != nil {
		f.t.Fatal(err)
	}
	return f.session(name)
}

func (f *fixture) raiseReq(uid, profile, words, where string, start bool) apiv1.RaiseRequest {
	return apiv1.RaiseRequest{
		ExpectUID: uid, Profile: profile, StartSessions: start,
		Owner: apiv1.RaiseOwner{Words: words, Where: where, SaidAt: f.now.Add(-time.Minute).Format(time.RFC3339)},
	}
}

func wantSentence(t *testing.T, w *httptest.ResponseRecorder, status int, sentence string) {
	t.Helper()
	code := apiv1.CodeForbidden
	switch status {
	case http.StatusUnprocessableEntity:
		code = apiv1.CodeInvalid
	case http.StatusConflict:
		code = apiv1.CodeConflict
	case http.StatusTooManyRequests:
		code = apiv1.CodeLimitExceeded
	case http.StatusServiceUnavailable:
		code = apiv1.CodeUnavailable
	case http.StatusNotFound:
		code = apiv1.CodeNotFound
	}
	e := wantError(t, w, status, code)
	if e.Message != sentence {
		t.Fatalf("message %q, want %q", e.Message, sentence)
	}
}

func TestRaiseFlagsOffRefusePostAndHonourNothing(t *testing.T) {
	f := newFixture(t)
	target := f.remoteTarget("plain", 0)
	body := f.raiseReq(string(target.UID), "workbench", "please", sessionraise.WherePhone, false)
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("plain"), tokHuman, body), http.StatusForbidden, raiseOff)
	f.srv.Raise = &RaiseConfig{}
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("plain"), tokHuman, body), http.StatusForbidden, raiseOff)
	got := f.do(http.MethodGet, apiv1.SessionPath("plain"), tokHuman, nil)
	if got.Code != http.StatusOK || strings.Contains(got.Body.String(), `"raise"`) {
		t.Fatalf("unraised view: %d %s", got.Code, got.Body.String())
	}
}

func TestUnraisedSessionViewHasNoRaiseKey(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true, "workbench")
	w := f.do(http.MethodGet, apiv1.SessionPath("missing"), tokHuman, nil)
	if w.Code == http.StatusOK {
		t.Fatal("missing session was found")
	}
	f.sessionPod("quiet", "full", 0)
	body := f.do(http.MethodGet, apiv1.SessionPath("quiet"), tokHuman, nil).Body.String()
	if strings.Contains(body, `"raise"`) {
		t.Fatalf("unraised session gained a raise key: %s", body)
	}
	list := f.do(http.MethodGet, apiv1.SessionsPath, tokHuman, nil).Body.String()
	if strings.Contains(list, `"raise"`) {
		t.Fatalf("list gained a raise key: %s", list)
	}
	fleet := f.do(http.MethodGet, apiv1.FleetPath, tokHuman, nil).Body.String()
	if strings.Contains(fleet, `"raise"`) {
		t.Fatalf("fleet gained a raise key: %s", fleet)
	}
}

func TestRaisePerformsAndRefuses(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true, "workbench")
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	path := apiv1.SessionRaisePath("target")
	body := f.raiseReq(string(target.UID), "workbench", "raise workbench please", sessionraise.WherePhone, true)
	w := f.do(http.MethodPost, path, tok, body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("raise: %d %s", w.Code, w.Body.String())
	}
	view := decode[apiv1.Session](t, w)
	if view.Profile != "" || view.Raise == nil || view.Raise.State != sessionraise.StatePending || view.Raise.PerformedBy != "session/bench" || view.Raise.Ignored != "" || view.Raise.Owner.Words != body.Owner.Words {
		t.Fatalf("view %+v", view.Raise)
	}
	if f.session("target").Generation != 1 && f.session("target").Generation != 0 {
		t.Fatalf("generation moved: %d", f.session("target").Generation)
	}
	again := f.do(http.MethodPost, path, tok, body)
	if again.Code != http.StatusOK {
		t.Fatalf("same raise: %d %s", again.Code, again.Body.String())
	}
	led, err := f.srv.readLedger(context.Background(), f.srv.Raise.filled())
	if err != nil || len(led.Words) != 1 {
		t.Fatalf("word spent twice: %v %+v", err, led)
	}

	other := f.remoteTarget("other", 0)
	folded := f.raiseReq(string(other.UID), "workbench", "Raise workbench please.", sessionraise.WherePhone, false)
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("other"), tok, folded), http.StatusConflict,
		"session/bench already used these words for a raise. A new raise needs a new word from Tom.")
	wide := f.raiseReq(string(other.UID), "workbench", "Ｒａｉｓｅ ｗｏｒｋｂｅｎｃｈ ｐｌｅａｓｅ", sessionraise.WherePhone, false)
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("other"), tok, wide), http.StatusConflict,
		"session/bench already used these words for a raise. A new raise needs a new word from Tom.")

	fresh := f.raiseReq(string(other.UID), "workbench", "a different word", sessionraise.WherePhone, false)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("other"), tok, fresh); w.Code != http.StatusAccepted {
		t.Fatalf("second target: %d %s", w.Code, w.Body.String())
	}
	back := f.do(http.MethodDelete, apiv1.SessionRaisePath("other"), tok, apiv1.TakeBackRequest{ExpectUID: string(other.UID)})
	if back.Code != http.StatusOK {
		t.Fatalf("take-back inside the minute: %d %s", back.Code, back.Body.String())
	}
	if decode[apiv1.Session](t, back).Raise.State != sessionraise.StateTakenBack {
		t.Fatalf("take-back view: %s", back.Body.String())
	}
}

func TestRaiseRefusalSentences(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true, "workbench")
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 0)
	path := apiv1.SessionRaisePath("target")
	ok := f.raiseReq(string(target.UID), "workbench", "raise workbench please", sessionraise.WherePhone, false)

	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("bench"), tok, f.raiseReq(string(f.session("bench").UID), "workbench", "raise myself please", sessionraise.WherePhone, false)), http.StatusForbidden, raiseSelf)

	childTok := f.sessionPod("child", "full", 1)
	child := f.session("child")
	child.Spec.Parent = "target"
	child.Spec.Profile = "workbench"
	if err := f.c.Update(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	wantSentence(t, f.do(http.MethodPost, path, childTok, f.raiseReq(string(target.UID), "workbench", "from under", sessionraise.WherePhone, false)), http.StatusForbidden,
		"child works under target, so it may not raise it.")

	lost := f.sessionPod("lost", "full", 0)
	lostSess := f.session("lost")
	lostSess.Spec.Parent = "missing-parent"
	lostSess.Spec.Profile = "workbench"
	if err := f.c.Update(context.Background(), lostSess); err != nil {
		t.Fatal(err)
	}
	wantSentence(t, f.do(http.MethodPost, path, lost, f.raiseReq(string(target.UID), "workbench", "lost parent", sessionraise.WherePhone, false)), http.StatusForbidden,
		"The parent chain of lost cannot be read, so the operator cannot tell that it is not under target. Try again.")

	ordinary := f.sessionPod("ordinary", "full", 0)
	wantSentence(t, f.do(http.MethodPost, path, ordinary, ok), http.StatusForbidden, raiseOtherCaller)
	wantSentence(t, f.do(http.MethodPost, path, tokClient, f.raiseReq(string(target.UID), "workbench", "from a client", sessionraise.WhereOwn, false)), http.StatusForbidden, raiseOtherCaller)

	humanWrong := f.raiseReq(string(target.UID), "workbench", "tom said this", sessionraise.WherePhone, false)
	wantSentence(t, f.do(http.MethodPost, path, tokHuman, humanWrong), http.StatusUnprocessableEntity,
		"A human records only words Tom gave it directly (own-call); have the agent he told perform the raise.")
	sessionWrong := f.raiseReq(string(target.UID), "workbench", "tom said this", sessionraise.WhereOwn, false)
	wantSentence(t, f.do(http.MethodPost, path, tok, sessionWrong), http.StatusUnprocessableEntity,
		"A session records only words Tom gave it directly (phone-prompt); have the agent he told perform the raise.")

	empty := ok
	empty.Owner.Words = ""
	wantSentence(t, f.do(http.MethodPost, path, tok, empty), http.StatusUnprocessableEntity, raiseNeedsWords)
	long := ok
	long.Owner.Words = strings.Repeat("a", 301)
	wantSentence(t, f.do(http.MethodPost, path, tok, long), http.StatusUnprocessableEntity, raiseWordsLong)
	stale := ok
	stale.Owner.SaidAt = f.now.Add(-25 * time.Hour).Format(time.RFC3339)
	wantSentence(t, f.do(http.MethodPost, path, tok, stale), http.StatusUnprocessableEntity, raiseWordsTime)
	future := ok
	future.Owner.SaidAt = f.now.Add(10 * time.Minute).Format(time.RFC3339)
	wantSentence(t, f.do(http.MethodPost, path, tok, future), http.StatusUnprocessableEntity, raiseWordsTime)
	control := ok
	control.Owner.Words = "go\nnow"
	wantSentence(t, f.do(http.MethodPost, path, tok, control), http.StatusUnprocessableEntity, raiseWordsControl)

	shapes := []struct{ words, shape string }{
		{"use ghp_" + strings.Repeat("a", 20), "a GitHub token"},
		{"-----BEGIN PRIVATE KEY-----", "a private key"},
		{"eyJabcdefgh.abcdefgh.", "a JSON web token"},
		{"https://user:pass@example.com/a", "a URL with credentials"},
		{"Bearer abcdefghijkl", "a bearer credential"},
		{"password=hunter", "a credential assignment"},
		{"xoxb-" + strings.Repeat("a", 10), "a Slack token"},
		{"AKIA1234567890ABCDEF", "an AWS key"},
		{"call 10.1.2.3 now", "a private network address"},
		{"host api.svc today", "an in-cluster name"},
		{"opapi-" + strings.Repeat("a", 16), "an OpenProject token"},
	}
	for _, shape := range shapes {
		req := ok
		req.Owner.Words = shape.words
		wantSentence(t, f.do(http.MethodPost, path, tok, req), http.StatusUnprocessableEntity,
			"Tom's words look like they hold "+shape.shape+". Leave it out and send his words again.")
	}

	replaced := ok
	replaced.ExpectUID = "00000000-0000-0000-0000-000000000000"
	wantSentence(t, f.do(http.MethodPost, path, tok, replaced), http.StatusConflict,
		"Session target was replaced: its UID is not the one you read. Read it again.")
	missingUID := ok
	missingUID.ExpectUID = ""
	wantSentence(t, f.do(http.MethodPost, path, tok, missingUID), http.StatusUnprocessableEntity, raiseNeedsUID)
	neither := ok
	neither.Profile = ""
	neither.StartSessions = false
	wantSentence(t, f.do(http.MethodPost, path, tok, neither), http.StatusUnprocessableEntity, raiseNeedsOne)
	wantSentence(t, f.do(http.MethodPost, path, tok, f.raiseReq(string(target.UID), "ops", "some other word", sessionraise.WherePhone, false)), http.StatusUnprocessableEntity,
		"This caller may not raise sessions to profile ops on this operator.")
	wantSentence(t, f.do(http.MethodPost, path, tok, f.raiseReq(string(target.UID), "lead", "some other word", sessionraise.WherePhone, false)), http.StatusUnprocessableEntity,
		"Profile lead runs as a named coordinator identity; a raise never gives one.")

	shallow := f.remoteTarget("shallow", 0)
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("shallow"), tok, f.raiseReq(string(shallow.UID), "", "start the shallow one", sessionraise.WherePhone, true)), http.StatusUnprocessableEntity,
		"Session shallow sits at depth 0 and may already start sessions.")
	deep := f.remoteTarget("deep", 1)
	deep.Labels[v1alpha1.LabelDepth] = "3"
	if err := f.c.Update(context.Background(), deep); err != nil {
		t.Fatal(err)
	}
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("deep"), tok, f.raiseReq(string(deep.UID), "", "start the deep one", sessionraise.WherePhone, true)), http.StatusUnprocessableEntity,
		"Only a session at depth 2 may be raised to start sessions.")

	summoned := f.remoteTarget("summoned", 0)
	summoned.Spec.Caller = "alert-responder"
	if err := f.c.Update(context.Background(), summoned); err != nil {
		t.Fatal(err)
	}
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("summoned"), tok, f.raiseReq(string(summoned.UID), "workbench", "summoned word", sessionraise.WherePhone, false)), http.StatusConflict,
		"Session summoned is a summoned session; summoned sessions are never raised.")
}

func TestRaiseRefusesEachCallerKind(t *testing.T) {
	f := meatballFixture(t, meatballPolicy(), summonTestPolicy())
	f.raiseReady(true, "workbench")
	target := f.remoteTarget("target", 0)
	path := apiv1.SessionRaisePath("target")
	body := f.raiseReq(string(target.UID), "workbench", "from this caller", sessionraise.WhereChat, false)
	wantSentence(t, f.do(http.MethodPost, path, tokMeatball, body), http.StatusForbidden, raisePolicy)

	f.srv.Policy.PortalReaderEnabled = true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal-pod", Namespace: "dev-agents", UID: "portal-pod"},
		Spec: corev1.PodSpec{ServiceAccountName: "dev-env-v2-portal", Containers: []corev1.Container{{Name: "p", Image: "x"}}}}
	if err := f.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(portalReaderRef)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	f.auth["tok-portal"] = id
	wantSentence(t, f.do(http.MethodPost, path, "tok-portal", body), http.StatusForbidden, raiseOtherCaller)
	wantSentence(t, f.do(http.MethodPost, path, tokStranger, f.raiseReq(string(target.UID), "workbench", "summon caller", sessionraise.WherePhone, false)), http.StatusForbidden, raiseOtherCaller)
}

func TestBadKeyRefusesPost(t *testing.T) {
	f := newFixture(t)
	f.installRaiseProfiles()
	target := f.remoteTarget("target", 0)
	tok := f.workbench("bench")
	body := f.raiseReq(string(target.UID), "workbench", "please", sessionraise.WherePhone, false)
	f.srv.Raise = &RaiseConfig{Dispatch: true, Profiles: []string{"workbench"}}
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body), http.StatusServiceUnavailable, raiseKeyUnreadable)

	ns := sessionraise.ObjectNamespace
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: sessionraise.OperatorSecretName}}
	public := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: sessionraise.OperatorPublicName}}
	ledger := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: sessionraise.LedgerName}}
	if err := f.c.Create(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Create(context.Background(), public); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Create(context.Background(), ledger); err != nil {
		t.Fatal(err)
	}
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body), http.StatusServiceUnavailable, raiseNoKey)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret.Data = map[string][]byte{sessionraise.SecretKey: priv}
	if err := f.c.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	public.Data = map[string]string{sessionraise.PublicKeyField: base64.StdEncoding.EncodeToString(other)}
	if err := f.c.Update(context.Background(), public); err != nil {
		t.Fatal(err)
	}
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body), http.StatusServiceUnavailable, raiseKeyMismatch)
}

func TestUnreadableLedgerRefusesRaiseAndExemption(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true, "workbench")
	if err := f.c.Delete(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: sessionraise.ObjectNamespace, Name: sessionraise.LedgerName}}); err != nil {
		t.Fatal(err)
	}
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	body := f.raiseReq(string(target.UID), "", "please start", sessionraise.WherePhone, true)
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body), http.StatusServiceUnavailable, raiseLedgerUnread)

	creator := f.sessionPod("creator", "full", 2)
	if w := f.do(http.MethodPost, apiv1.SessionsPath, creator, task()); w.Code != http.StatusForbidden {
		t.Fatalf("exemption without a ledger: %d %s", w.Code, w.Body.String())
	}
}

func TestRecordIgnoredUnlessUIDSignatureWordsHashAndLedgerAgree(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true, "workbench")
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 0)
	body := f.raiseReq(string(target.UID), "workbench", "raise workbench please", sessionraise.WherePhone, false)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusAccepted {
		t.Fatalf("raise: %d %s", w.Code, w.Body.String())
	}
	sess := f.session("target")
	rec, err := sessionraise.Parse(sess.Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		t.Fatal(err)
	}
	forged := rec
	forged.Sig = strings.Repeat("A", len(rec.Sig))
	raw, err := forged.Encode()
	if err != nil {
		t.Fatal(err)
	}
	sess.Annotations[v1alpha1.AnnotationRaise] = string(raw)
	if err := f.c.Update(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	view := decode[apiv1.Session](t, f.do(http.MethodGet, apiv1.SessionPath("target"), tokHuman, nil))
	if view.Raise == nil || view.Raise.Ignored != sessionraise.IgnoreSignature {
		t.Fatalf("forged: %+v", view.Raise)
	}

	sess = f.session("target")
	edited := rec
	edited.Owner.Words = "no"
	raw, err = edited.Encode()
	if err != nil {
		t.Fatal(err)
	}
	sess.Annotations[v1alpha1.AnnotationRaise] = string(raw)
	if err := f.c.Update(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	view = decode[apiv1.Session](t, f.do(http.MethodGet, apiv1.SessionPath("target"), tokHuman, nil))
	if view.Raise == nil || view.Raise.Ignored != sessionraise.IgnoreWords {
		t.Fatalf("edited words: %+v", view.Raise)
	}

	sess = f.session("target")
	good, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	sess.Annotations[v1alpha1.AnnotationRaise] = string(good)
	if err := f.c.Update(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	led, err := f.srv.readLedger(context.Background(), f.srv.Raise.filled())
	if err != nil {
		t.Fatal(err)
	}
	row := led.Find(string(target.UID))
	row.State = sessionraise.StateActive
	saved, err := led.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionraise.ObjectNamespace, Name: sessionraise.LedgerName}, cm); err != nil {
		t.Fatal(err)
	}
	cm.Data = map[string]string{sessionraise.LedgerField: string(saved)}
	if err := f.c.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	view = decode[apiv1.Session](t, f.do(http.MethodGet, apiv1.SessionPath("target"), tokHuman, nil))
	if view.Raise == nil || view.Raise.Ignored != sessionraise.IgnoreLedger {
		t.Fatalf("ledger state: %+v", view.Raise)
	}
	row.State = sessionraise.StatePending
	row.ID = "0123456789abcdef"
	saved, _ = led.Marshal()
	cm.Data[sessionraise.LedgerField] = string(saved)
	if err := f.c.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	view = decode[apiv1.Session](t, f.do(http.MethodGet, apiv1.SessionPath("target"), tokHuman, nil))
	if view.Raise == nil || view.Raise.Ignored != sessionraise.IgnoreLedger {
		t.Fatalf("ledger id: %+v", view.Raise)
	}

	other := f.remoteTarget("other", 0)
	if other.Annotations == nil {
		other.Annotations = map[string]string{}
	}
	other.Annotations[v1alpha1.AnnotationRaise] = string(good)
	if err := f.c.Update(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	view = decode[apiv1.Session](t, f.do(http.MethodGet, apiv1.SessionPath("other"), tokHuman, nil))
	if view.Raise == nil || view.Raise.Ignored != sessionraise.IgnoreUID {
		t.Fatalf("wrong uid: %+v", view.Raise)
	}
}

func TestRateLimitsHoldAcrossRestart(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true, "workbench")
	tok := f.workbench("bench")
	for i, word := range []string{"one word", "two word", "three word"} {
		name := "rate-" + string(rune('a'+i))
		target := f.remoteTarget(name, 0)
		body := f.raiseReq(string(target.UID), "workbench", word, sessionraise.WherePhone, false)
		if w := f.do(http.MethodPost, apiv1.SessionRaisePath(name), tok, body); w.Code != http.StatusAccepted {
			t.Fatalf("raise %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	next := &Server{
		Client: f.srv.Client, Live: f.srv.Live, Auth: f.srv.Auth, Policy: f.srv.Policy,
		Templates: f.srv.Templates, Log: f.srv.Log, Now: f.srv.Now, Raise: f.srv.Raise,
		TitleRename: f.srv.TitleRename,
	}
	f.srv = next
	f.h = next.Handler()
	fourth := f.remoteTarget("fourth", 0)
	body := f.raiseReq(string(fourth.UID), "workbench", "four word", sessionraise.WherePhone, false)
	w := f.do(http.MethodPost, apiv1.SessionRaisePath("fourth"), tok, body)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "three raises in the last hour") {
		t.Fatalf("after restart: %d %s", w.Code, w.Body.String())
	}
}

func TestSelfTakeBackWritesLedgerAndRecordBeforeSuspend(t *testing.T) {
	var order []string
	f := newFixtureWith(t, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			switch o := obj.(type) {
			case *corev1.ConfigMap:
				if o.Name == sessionraise.LedgerName && strings.Contains(o.Data[sessionraise.LedgerField], sessionraise.StateTakenBack) {
					order = append(order, "ledger")
				}
			case *v1alpha1.AgentSession:
				if strings.Contains(o.Annotations[v1alpha1.AnnotationRaise], sessionraise.StateTakenBack) {
					order = append(order, "record")
				}
			}
			return c.Update(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if sess, ok := obj.(*v1alpha1.AgentSession); ok && sess.Spec.OperatingMode == v1alpha1.OperatingModeSuspended {
				order = append(order, "suspend")
			}
			return c.Patch(ctx, obj, patch, opts...)
		},
	})
	f.raiseReady(true, "workbench")
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 0)
	body := f.raiseReq(string(target.UID), "workbench", "raise then stop", sessionraise.WherePhone, false)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusAccepted {
		t.Fatalf("raise: %d %s", w.Code, w.Body.String())
	}
	pod := &corev1.Pod{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "target"}, pod); err != nil {
		t.Fatal(err)
	}
	pod.Spec.ServiceAccountName = templates.WorkbenchServiceAccount
	if err := f.c.Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(sessionNS + "/" + controller.ServiceAccountName)
	id.PodName = "target"
	var live corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "target"}, &live); err != nil {
		t.Fatal(err)
	}
	id.PodUID = string(live.UID)
	f.auth["tok-self"] = id
	order = nil
	w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), "tok-self", apiv1.TakeBackRequest{ExpectUID: string(target.UID)})
	if w.Code != http.StatusOK {
		t.Fatalf("take-back: %d %s", w.Code, w.Body.String())
	}
	if strings.Join(order, ",") != "ledger,record,suspend" {
		t.Fatalf("order %v", order)
	}
	if f.session("target").Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		t.Fatal("pod account was not suspended")
	}
}

func TestDepth2RaiseCreatesChildLabelled3(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	creator := f.sessionPod("creator", "full", 2)
	sess := f.session("creator")
	f.putActive(sess, true)
	w := f.do(http.MethodPost, apiv1.SessionsPath, creator, task())
	if w.Code != http.StatusCreated {
		t.Fatalf("child: %d %s", w.Code, w.Body.String())
	}
	child := f.session(decode[apiv1.Session](t, w).Name)
	if child.Labels[v1alpha1.LabelDepth] != "3" || child.Spec.Parent != "creator" {
		t.Fatalf("child label %s parent %s", child.Labels[v1alpha1.LabelDepth], child.Spec.Parent)
	}
	if err := f.srv.admitCreate(context.Background(), &caller{kind: kindSession, depth: 3, parent: "creator", session: f.session("creator")}, &v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{Agent: v1alpha1.AgentClaude}}); err == nil {
		t.Fatal("scheduled admission used the exemption")
	}
	plain := f.sessionPod("plain", "full", 2)
	if w := f.do(http.MethodPost, apiv1.SessionsPath, plain, task()); w.Code != http.StatusForbidden {
		t.Fatalf("depth 3 without a raise: %d %s", w.Code, w.Body.String())
	}
}

func (f *fixture) putActive(sess *v1alpha1.AgentSession, start bool) {
	f.putActiveProfile(sess, start, "")
}

func (f *fixture) putActiveProfile(sess *v1alpha1.AgentSession, start bool, profile string) {
	f.t.Helper()
	ctx := context.Background()
	cfg := f.srv.Raise.filled()
	priv, err := sessionraise.Load(ctx, f.srv.raiseStore(cfg), cfg.OperatorSecret, cfg.OperatorPublic)
	if err != nil {
		f.t.Fatal(err)
	}
	now := f.now.UTC().Truncate(time.Second)
	hash := sessionraise.WordsHash("please start")
	at := now.Format(time.RFC3339)
	id := sessionraise.RecordID(string(sess.UID), at, hash)
	entry := sessionraise.SessionEntry{
		Name: sess.Name, UID: string(sess.UID), ID: id, State: sessionraise.StateActive, ChangedAt: now,
		Profile: profile, StartSessions: start, Depth: "2", WordsHash: hash, PerformedBy: "human/" + humanSA,
		PerformedAt: at, Where: sessionraise.WhereOwn, SaidAt: at,
		NoticeSentAt: at, ActiveFrom: now.Add(sessionraise.EffectDelay).Format(time.RFC3339),
	}
	led, err := f.srv.readLedger(ctx, cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	led.Sessions = append(led.Sessions, entry)
	raw, err := led.Marshal()
	if err != nil {
		f.t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := f.c.Get(ctx, types.NamespacedName{Namespace: cfg.Namespace, Name: cfg.Ledger}, cm); err != nil {
		f.t.Fatal(err)
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[sessionraise.LedgerField] = string(raw)
	if err := f.c.Update(ctx, cm); err != nil {
		f.t.Fatal(err)
	}
	rec := recordFrom(entry, "please start")
	signed, err := sessionraise.Sign(rec, priv)
	if err != nil {
		f.t.Fatal(err)
	}
	encoded, err := signed.Encode()
	if err != nil {
		f.t.Fatal(err)
	}
	current := f.session(sess.Name)
	if current.Annotations == nil {
		current.Annotations = map[string]string{}
	}
	current.Annotations[v1alpha1.AnnotationRaise] = string(encoded)
	if err := f.c.Update(ctx, current); err != nil {
		f.t.Fatal(err)
	}
}

func TestCachedActiveRecordAfterTakeBackDoesNotPass(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	f.sessionPod("creator", "full", 2)
	sess := f.session("creator")
	f.putActive(sess, true)
	cfg := f.srv.Raise.filled()
	led, err := f.srv.readLedger(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := led.TakeBack(string(sess.UID), "human/"+humanSA, f.now.Format(time.RFC3339), f.now); err != nil {
		t.Fatal(err)
	}
	raw, err := led.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: cfg.Namespace, Name: cfg.Ledger}, cm); err != nil {
		t.Fatal(err)
	}
	cm.Data[sessionraise.LedgerField] = string(raw)
	if err := f.c.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	c := &caller{kind: kindSession, depth: 3, parent: "creator", session: f.session("creator")}
	if f.srv.depthExempt(context.Background(), c) {
		t.Fatal("a cached active annotation passed after the ledger was taken back")
	}
}

func TestRaiseStaysPendingUntilSignedSentAndTwoMinutes(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	f.sessionPod("creator", "full", 2)
	sess := f.session("creator")
	f.putPending(sess)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	sentAt := f.now.UTC().Format(time.RFC3339)
	entry := f.ledgerEntry(string(sess.UID))
	notice, err := sessionraise.SignNotice(sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: 1,
		Outcome: sessionraise.OutcomeSent, At: sentAt, SentAt: sentAt,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	f.putNotice(notice)
	f.srv.activateRaises(context.Background())
	if got := f.ledgerEntry(string(sess.UID)); got.State != sessionraise.StatePending {
		t.Fatalf("activated before two minutes: %s", got.State)
	}
	f.now = f.now.Add(sessionraise.EffectDelay)
	f.srv.activateRaises(context.Background())
	if got := f.ledgerEntry(string(sess.UID)); got.State != sessionraise.StateActive {
		t.Fatalf("not active at two minutes: %s", got.State)
	}
}

func (f *fixture) publishKeeper(pub ed25519.PublicKey) {
	f.t.Helper()
	cm := &corev1.ConfigMap{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionraise.ObjectNamespace, Name: sessionraise.KeeperPublicName}, cm); err != nil {
		f.t.Fatal(err)
	}
	cm.Data = map[string]string{
		sessionraise.PublicKeyField: base64.StdEncoding.EncodeToString(pub),
		sessionraise.PublicIDField:  sessionraise.KeyID(pub),
	}
	if err := f.c.Update(context.Background(), cm); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) putNotice(n sessionraise.Notice) {
	f.t.Helper()
	led := &sessionraise.NoticeLedger{}
	led.Put(n)
	raw, err := led.Marshal()
	if err != nil {
		f.t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionraise.ObjectNamespace, Name: sessionraise.NoticeLedgerName}, cm); err != nil {
		f.t.Fatal(err)
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[sessionraise.LedgerField] = string(raw)
	if err := f.c.Update(context.Background(), cm); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) ledgerEntry(uid string) sessionraise.SessionEntry {
	f.t.Helper()
	led, err := f.srv.readLedger(context.Background(), f.srv.Raise.filled())
	if err != nil {
		f.t.Fatal(err)
	}
	cur := led.Find(uid)
	if cur == nil {
		f.t.Fatal("no ledger row")
	}
	return *cur
}

func (f *fixture) putPending(sess *v1alpha1.AgentSession) {
	f.t.Helper()
	f.putActive(sess, true)
	ctx := context.Background()
	cfg := f.srv.Raise.filled()
	led, err := f.srv.readLedger(ctx, cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	row := led.Find(string(sess.UID))
	row.State = sessionraise.StatePending
	row.NoticeSentAt = ""
	row.ActiveFrom = ""
	raw, err := led.Marshal()
	if err != nil {
		f.t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := f.c.Get(ctx, types.NamespacedName{Namespace: cfg.Namespace, Name: cfg.Ledger}, cm); err != nil {
		f.t.Fatal(err)
	}
	cm.Data[sessionraise.LedgerField] = string(raw)
	if err := f.c.Update(ctx, cm); err != nil {
		f.t.Fatal(err)
	}
	priv, err := sessionraise.Load(ctx, f.srv.raiseStore(cfg), cfg.OperatorSecret, cfg.OperatorPublic)
	if err != nil {
		f.t.Fatal(err)
	}
	rec := recordFrom(*row, "please start")
	signed, err := sessionraise.Sign(rec, priv)
	if err != nil {
		f.t.Fatal(err)
	}
	encoded, err := signed.Encode()
	if err != nil {
		f.t.Fatal(err)
	}
	current := f.session(sess.Name)
	current.Annotations[v1alpha1.AnnotationRaise] = string(encoded)
	if err := f.c.Update(ctx, current); err != nil {
		f.t.Fatal(err)
	}
}

func TestRaiseStaysPendingOnFailedAmbiguousAndBadSignature(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	f.sessionPod("creator", "full", 2)
	sess := f.session("creator")
	f.putPending(sess)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	entry := f.ledgerEntry(string(sess.UID))
	at := f.now.UTC().Format(time.RFC3339)
	for _, n := range []sessionraise.Notice{
		{ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: 1, Outcome: sessionraise.OutcomeFailed, At: at},
		{ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: 1, Outcome: sessionraise.OutcomeAmbiguous, At: at},
		{ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: 1, Outcome: sessionraise.OutcomeSent, At: at, SentAt: at},
	} {
		signed, err := sessionraise.SignNotice(n, priv)
		if err != nil {
			t.Fatal(err)
		}
		if n.Outcome == sessionraise.OutcomeSent {
			signed.Sig = ""
		}
		f.putNotice(signed)
		f.now = f.now.Add(sessionraise.EffectDelay)
		f.srv.activateRaises(context.Background())
		if got := f.ledgerEntry(string(sess.UID)); got.State != sessionraise.StatePending {
			t.Fatalf("%s moved the row to %s", n.Outcome, got.State)
		}
	}
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := sessionraise.SignNotice(sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: 1,
		Outcome: sessionraise.OutcomeSent, At: at, SentAt: at,
	}, other)
	if err != nil {
		t.Fatal(err)
	}
	f.putNotice(wrong)
	f.srv.activateRaises(context.Background())
	if got := f.ledgerEntry(string(sess.UID)); got.State != sessionraise.StatePending {
		t.Fatalf("wrong key activated: %s", got.State)
	}
}

func TestUnannouncedAfterTwelveAttempts(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	f.sessionPod("creator", "full", 2)
	sess := f.session("creator")
	f.putPending(sess)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	entry := f.ledgerEntry(string(sess.UID))
	at := f.now.UTC().Format(time.RFC3339)
	held, err := sessionraise.SignNotice(sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: sessionraise.NoticeAttempts,
		Outcome: sessionraise.OutcomeReserved, At: at,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	f.putNotice(held)
	f.srv.activateRaises(context.Background())
	if got := f.ledgerEntry(string(sess.UID)); got.State != sessionraise.StatePending {
		t.Fatalf("a reservation unannounced: %s", got.State)
	}
	failed, err := sessionraise.SignNotice(sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: sessionraise.NoticeAttempts,
		Outcome: sessionraise.OutcomeFailed, At: at,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	f.putNotice(failed)
	f.srv.activateRaises(context.Background())
	if got := f.ledgerEntry(string(sess.UID)); got.State != sessionraise.StateUnannounced {
		t.Fatalf("twelve failures left %s", got.State)
	}
}

func TestTakeBackBeforeActivationWinsOverLaterSent(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	f.sessionPod("creator", "full", 2)
	sess := f.session("creator")
	f.putPending(sess)
	cfg := f.srv.Raise.filled()
	led, err := f.srv.readLedger(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := led.TakeBack(string(sess.UID), "human/"+humanSA, f.now.Format(time.RFC3339), f.now); err != nil {
		t.Fatal(err)
	}
	raw, err := led.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: cfg.Namespace, Name: cfg.Ledger}, cm); err != nil {
		t.Fatal(err)
	}
	cm.Data[sessionraise.LedgerField] = string(raw)
	if err := f.c.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	entry := f.ledgerEntry(string(sess.UID))
	at := f.now.Add(-sessionraise.EffectDelay).UTC().Format(time.RFC3339)
	notice, err := sessionraise.SignNotice(sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: 1,
		Outcome: sessionraise.OutcomeSent, At: at, SentAt: at,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	f.putNotice(notice)
	f.srv.activateRaises(context.Background())
	if got := f.ledgerEntry(string(sess.UID)); got.State != sessionraise.StateTakenBack {
		t.Fatalf("a later Sent overwrote the take-back: %s", got.State)
	}
}

func TestActivationDoesNotSignAnEditedAnnotation(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	keeper, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(keeper)
	f.sessionPod("creator", "full", 2)
	sess := f.session("creator")
	f.putPending(sess)
	current := f.session("creator")
	rec, err := sessionraise.Parse(current.Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		t.Fatal(err)
	}
	rec.Owner.Words = "edited words"
	rec.Owner.WordsHash = sessionraise.WordsHash("edited words")
	rec.Profile = "workbench"
	rec.Sig = ""
	raw, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	current.Annotations[v1alpha1.AnnotationRaise] = string(raw)
	if err := f.c.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	cfg := f.srv.Raise.filled()
	led, err := f.srv.readLedger(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	row := led.Find(string(sess.UID))
	row.State = sessionraise.StateActive
	saved, err := led.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: cfg.Namespace, Name: cfg.Ledger}, cm); err != nil {
		t.Fatal(err)
	}
	cm.Data[sessionraise.LedgerField] = string(saved)
	if err := f.c.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	f.srv.activateRaises(context.Background())
	got, err := sessionraise.Parse(f.session("creator").Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		t.Fatal(err)
	}
	pub, err := sessionraise.ReadPublic(context.Background(), f.srv.raiseStore(cfg), cfg.OperatorPublic)
	if err != nil {
		t.Fatal(err)
	}
	if sessionraise.Verify(got, pub) {
		t.Fatalf("edited annotation was signed: profile %s words %s", got.Profile, got.Owner.Words)
	}
	rec.Owner.Words = "please start"
	rec.Owner.WordsHash = sessionraise.WordsHash("please start")
	rec.Profile = "workbench"
	rec.Sig = ""
	raw, err = rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	current = f.session("creator")
	current.Annotations[v1alpha1.AnnotationRaise] = string(raw)
	if err := f.c.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	f.srv.activateRaises(context.Background())
	got, err = sessionraise.Parse(f.session("creator").Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "" || !sessionraise.Verify(got, pub) {
		t.Fatalf("ledger profile was not what got signed: profile %q verified %v", got.Profile, sessionraise.Verify(got, pub))
	}
}

func TestTakeBackStandsWhenTheKeyIsMissing(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	body := f.raiseReq(string(target.UID), "", "please start sessions", sessionraise.WherePhone, true)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusAccepted {
		t.Fatalf("raise: %d %s", w.Code, w.Body.String())
	}
	sec := &corev1.Secret{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionraise.ObjectNamespace, Name: sessionraise.OperatorSecretName}, sec); err != nil {
		t.Fatal(err)
	}
	sec.Data = map[string][]byte{}
	if err := f.c.Update(context.Background(), sec); err != nil {
		t.Fatal(err)
	}
	w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tok, apiv1.TakeBackRequest{ExpectUID: string(target.UID)})
	if w.Code != http.StatusOK {
		t.Fatalf("take-back: %d %s", w.Code, w.Body.String())
	}
	if got := f.ledgerEntry(string(target.UID)); got.State != sessionraise.StateTakenBack {
		t.Fatalf("ledger: %s", got.State)
	}
}

func TestSameRaiseKeepsWhenItTookEffect(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	body := f.raiseReq(string(target.UID), "", "please start sessions", sessionraise.WherePhone, true)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusAccepted {
		t.Fatalf("raise: %d %s", w.Code, w.Body.String())
	}
	cfg := f.srv.Raise.filled()
	led, err := f.srv.readLedger(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	row := led.Find(string(target.UID))
	sent := f.now.Add(-3 * time.Minute).UTC().Truncate(time.Second).Format(time.RFC3339)
	from := f.now.Add(-time.Minute).UTC().Truncate(time.Second).Format(time.RFC3339)
	row.State = sessionraise.StateActive
	row.NoticeSentAt = sent
	row.ActiveFrom = from
	saved, err := led.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: cfg.Namespace, Name: cfg.Ledger}, cm); err != nil {
		t.Fatal(err)
	}
	cm.Data[sessionraise.LedgerField] = string(saved)
	if err := f.c.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	current := f.session("target")
	delete(current.Annotations, v1alpha1.AnnotationRaise)
	if err := f.c.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body)
	if w.Code != http.StatusOK {
		t.Fatalf("same raise: %d %s", w.Code, w.Body.String())
	}
	got, err := sessionraise.Parse(f.session("target").Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		t.Fatal(err)
	}
	if got.State != sessionraise.StateActive || got.NoticeSentAt != sent || got.ActiveFrom != from {
		t.Fatalf("retry dropped when the raise took effect: state %s sent %s from %s", got.State, got.NoticeSentAt, got.ActiveFrom)
	}
}

func TestRaisedToWorkbenchMayNotRaiseOthers(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true, "workbench")
	tok := f.sessionPod("raised", "full", 0)
	f.putActiveProfile(f.session("raised"), false, "workbench")
	target := f.remoteTarget("target", 0)
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, f.raiseReq(string(target.UID), "workbench", "from a raised session", sessionraise.WherePhone, false)), http.StatusForbidden,
		"raised was raised to workbench, not started on it, so it may not raise others.")
}

func TestDepth3CallerIsRefused(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.sessionPod("deep", "full", 3)
	f.putActive(f.session("deep"), true)
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tok, task()); w.Code != http.StatusForbidden {
		t.Fatalf("depth 3 created a child: %d %s", w.Code, w.Body.String())
	}
}

func TestScheduledWorkStaysRefused(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	f.sessionPod("creator", "full", 2)
	sess := f.session("creator")
	f.putActive(sess, true)
	if err := f.srv.admitCreate(context.Background(), &caller{kind: kindSession, depth: 3, parent: "creator", session: sess}, &v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{Agent: v1alpha1.AgentClaude}}); err == nil {
		t.Fatal("scheduled work used the raise exemption")
	}
}
