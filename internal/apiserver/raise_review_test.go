package apiserver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/keeper"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
	"github.com/thaynes43/dev-env/internal/sessionraise"
	"github.com/thaynes43/dev-env/internal/templates"
)

const raisedChildSentence = "A raised session's child never runs as the workbench or a named coordinator; name another profile."

func (f *fixture) workbenchAt(name string, depth int) string {
	f.t.Helper()
	f.srv.Policy.Clients = append(f.srv.Policy.Clients, sessionNS+"/"+templates.WorkbenchServiceAccount)
	f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
	tok := f.sessionPod(name, "workbench", depth)
	sess := f.session(name)
	sess.Spec.Profile = "workbench"
	if err := f.c.Update(context.Background(), sess); err != nil {
		f.t.Fatal(err)
	}
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &pod); err != nil {
		f.t.Fatal(err)
	}
	pod.Spec.ServiceAccountName = templates.WorkbenchServiceAccount
	if err := f.c.Update(context.Background(), &pod); err != nil {
		f.t.Fatal(err)
	}
	id := saIdentity(sessionNS + "/" + templates.WorkbenchServiceAccount)
	id.PodName, id.PodUID = name, string(pod.UID)
	f.auth[tok] = id
	return tok
}

func (f *fixture) resign(name, state, depth string, start bool) {
	f.t.Helper()
	ctx := context.Background()
	sess := f.session(name)
	cfg := f.srv.Raise.filled()
	led, err := f.srv.readLedger(ctx, cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	row := led.Find(string(sess.UID))
	if row == nil {
		f.t.Fatal("no row")
	}
	row.State = state
	row.Depth = depth
	row.StartSessions = start
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
	signed, err := sessionraise.Sign(recordFrom(*row, "please start"), priv)
	if err != nil {
		f.t.Fatal(err)
	}
	encoded, err := signed.Encode()
	if err != nil {
		f.t.Fatal(err)
	}
	current := f.session(name)
	current.Annotations[v1alpha1.AnnotationRaise] = string(encoded)
	if err := f.c.Update(ctx, current); err != nil {
		f.t.Fatal(err)
	}
}

func TestRaisedChildNeverRunsAsWorkbenchOrCoordinator(t *testing.T) {
	sentence := raisedChildSentence
	t.Run("named workbench", func(t *testing.T) {
		f := newFixture(t)
		f.raiseReady(true)
		tok := f.workbenchAt("creator", 2)
		f.putActive(f.session("creator"), true)
		req := task()
		req.Profile = "workbench"
		wantSentence(t, f.do(http.MethodPost, apiv1.SessionsPath, tok, req), http.StatusForbidden, sentence)
	})
	t.Run("default runs as workbench", func(t *testing.T) {
		f := newFixture(t)
		f.raiseReady(true)
		full := f.tmpl.Profiles["full"]
		full.ServiceAccount = templates.WorkbenchServiceAccount
		f.tmpl.Profiles["full"] = full
		tok := f.workbenchAt("creator", 2)
		f.putActive(f.session("creator"), true)
		req := task()
		req.Profile = ""
		wantSentence(t, f.do(http.MethodPost, apiv1.SessionsPath, tok, req), http.StatusForbidden, sentence)
	})
	t.Run("named coordinator", func(t *testing.T) {
		f := newFixture(t)
		f.raiseReady(true)
		tok := f.workbenchAt("creator", 2)
		f.putActive(f.session("creator"), true)
		req := task()
		req.Profile = "lead"
		wantSentence(t, f.do(http.MethodPost, apiv1.SessionsPath, tok, req), http.StatusForbidden, sentence)
	})
	t.Run("templates cannot be read", func(t *testing.T) {
		f := newFixture(t)
		f.raiseReady(true)
		tok := f.workbenchAt("creator", 2)
		f.putActive(f.session("creator"), true)
		f.tmplOK = false
		req := task()
		req.Profile = "dev"
		wantSentence(t, f.do(http.MethodPost, apiv1.SessionsPath, tok, req), http.StatusServiceUnavailable, "The templates cannot be read, so the operator cannot tell what profile the child runs as.")
	})
	t.Run("ordinary profile", func(t *testing.T) {
		f := newFixture(t)
		f.raiseReady(true)
		tok := f.workbenchAt("creator", 2)
		f.putActive(f.session("creator"), true)
		req := task()
		req.Profile = "dev"
		w := f.do(http.MethodPost, apiv1.SessionsPath, tok, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("child: %d %s", w.Code, w.Body.String())
		}
		child := f.session(decode[apiv1.Session](t, w).Name)
		if child.Labels[v1alpha1.LabelDepth] != "3" {
			t.Fatalf("depth %s", child.Labels[v1alpha1.LabelDepth])
		}
	})
}

func TestDepthExemptionRefusesEveryRecordThatDoesNotGrantIt(t *testing.T) {
	cases := []struct {
		name string
		prep func(t *testing.T, f *fixture)
	}{
		{name: "pending", prep: func(t *testing.T, f *fixture) { f.putPending(f.session("creator")) }},
		{name: "unannounced", prep: func(t *testing.T, f *fixture) {
			f.putActive(f.session("creator"), true)
			f.resign("creator", sessionraise.StateUnannounced, "2", true)
		}},
		{name: "taken back through DELETE", prep: func(t *testing.T, f *fixture) {
			sess := f.session("creator")
			f.putActive(sess, true)
			w := f.do(http.MethodDelete, apiv1.SessionRaisePath("creator"), tokHuman, apiv1.TakeBackRequest{ExpectUID: string(sess.UID)})
			if w.Code != http.StatusOK {
				t.Fatalf("take-back: %d %s", w.Code, w.Body.String())
			}
			rec, err := sessionraise.Parse(f.session("creator").Annotations[v1alpha1.AnnotationRaise])
			if err != nil || rec.State != sessionraise.StateTakenBack {
				t.Fatalf("annotation was not re-signed taken-back: %v %+v", err, rec.State)
			}
		}},
		{name: "active profile only", prep: func(t *testing.T, f *fixture) { f.putActive(f.session("creator"), false) }},
		{name: "dispatch off", prep: func(t *testing.T, f *fixture) {
			f.srv.Raise.Dispatch = false
			f.putActive(f.session("creator"), true)
		}},
		{name: "ledger deleted", prep: func(t *testing.T, f *fixture) {
			f.putActive(f.session("creator"), true)
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: sessionraise.ObjectNamespace, Name: sessionraise.LedgerName}}
			if err := f.c.Delete(context.Background(), cm); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "record depth says 1", prep: func(t *testing.T, f *fixture) {
			f.putActive(f.session("creator"), true)
			f.resign("creator", sessionraise.StateActive, "1", true)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.raiseReady(true)
			tok := f.sessionPod("creator", "full", 2)
			tc.prep(t, f)
			w := f.do(http.MethodPost, apiv1.SessionsPath, tok, task())
			if w.Code != http.StatusForbidden {
				t.Fatalf("create: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestRaiseCallerKindIsTheSameForAMissingName(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	f.remoteTarget("target", 0)
	other := f.sessionPod("other", "full", 0)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		got := f.do(method, apiv1.SessionRaisePath("target"), other, apiv1.RaiseRequest{})
		missing := f.do(method, apiv1.SessionRaisePath("missing-name"), other, apiv1.RaiseRequest{})
		if got.Code != missing.Code || got.Body.String() != missing.Body.String() || !strings.Contains(got.Body.String(), raiseOtherCaller) {
			t.Fatalf("%s existing %d %s missing %d %s", method, got.Code, got.Body.String(), missing.Code, missing.Body.String())
		}
	}

	same := func(t *testing.T, c *caller, want string) {
		t.Helper()
		for _, name := range []string{"target", "missing-name"} {
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
				req := httptest.NewRequest(method, "/v1/sessions/"+name+"/raise", strings.NewReader(`{}`))
				req.SetPathValue("name", name)
				var err error
				switch method {
				case http.MethodGet:
					_, _, err = f.srv.getRaise(context.Background(), nil, req, c)
				case http.MethodPost:
					_, _, err = f.srv.postRaise(context.Background(), nil, req, c)
				default:
					_, _, err = f.srv.deleteRaise(context.Background(), nil, req, c)
				}
				var ae *apiError
				if !errors.As(err, &ae) || ae.Error() != want {
					t.Fatalf("%s %s: %v", method, name, err)
				}
			}
		}
	}
	same(t, &caller{kind: kindPolicy}, forbidden("%s", raisePolicy).Error())
	same(t, &caller{kind: kindSummon}, forbidden("%s", raiseOtherCaller).Error())
	same(t, &caller{kind: kindPortalReader}, forbidden("%s", raiseOtherCaller).Error())

	cf := coordinatorFixture(t)
	cf.remoteTarget("target", 0)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		got := cf.do(method, apiv1.SessionRaisePath("target"), tokCoordinator, nil)
		missing := cf.do(method, apiv1.SessionRaisePath("missing-name"), tokCoordinator, nil)
		if got.Code != missing.Code || got.Body.String() != missing.Body.String() || !strings.Contains(got.Body.String(), "coordinator access is limited to its direct task children") {
			t.Fatalf("coordinator %s existing %d %s missing %d %s", method, got.Code, got.Body.String(), missing.Code, missing.Body.String())
		}
	}

	pf := newFixture(t)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal-pod", Namespace: sessionNS, UID: "portal-pod-uid"}, Spec: corev1.PodSpec{ServiceAccountName: "dev-env-v2-portal", Containers: []corev1.Container{{Name: "portal", Image: "x"}}}}
	if err := pf.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	if err := pf.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: pod.Name}, pod); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(portalReaderRef)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	pf.auth["tok-portal"] = id
	pf.srv.Policy.PortalReaderEnabled = true
	pf.remoteTarget("target", 0)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		got := pf.do(method, apiv1.SessionRaisePath("target"), "tok-portal", nil)
		missing := pf.do(method, apiv1.SessionRaisePath("missing-name"), "tok-portal", nil)
		if got.Code != missing.Code || got.Body.String() != missing.Body.String() || !strings.Contains(got.Body.String(), raiseOtherCaller) {
			t.Fatalf("portal %s existing %d %s missing %d %s", method, got.Code, got.Body.String(), missing.Code, missing.Body.String())
		}
	}
}

func TestSecondRaiseIsAConflictUnlessItIsTheSameOne(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true, "workbench")
	a := f.workbench("bench-a")
	b := f.workbench("bench-b")
	target := f.remoteTarget("target", 0)
	body := f.raiseReq(string(target.UID), "workbench", "please raise this", sessionraise.WherePhone, false)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), a, body); w.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), a, body); w.Code != http.StatusOK {
		t.Fatalf("same raise: %d %s", w.Code, w.Body.String())
	}
	f.now = f.now.Add(2 * time.Minute)
	// A session may record only a phone prompt, so a different where is the
	// ledger test. Another performer, with the same words and the same where,
	// is used words rather than the same raise.
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("target"), b, body), http.StatusConflict, aCaller(f, b)+" already used these words for a raise. A new raise needs a new word from Tom.")
	different := f.raiseReq(string(target.UID), "workbench", "a different word", sessionraise.WherePhone, false)
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("target"), a, different), http.StatusConflict, "Session target already has a raise that is pending. Take it back first: agent-run raise target --take-back")
}

func aCaller(f *fixture, tok string) string {
	f.t.Helper()
	c, err := f.srv.resolveCaller(context.Background(), f.auth[tok])
	if err != nil {
		f.t.Fatal(err)
	}
	return c.String()
}

type staleLive struct {
	client.Reader
	name string
	sess v1alpha1.AgentSession
	n    int
}

func (r *staleLive) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	if s, ok := obj.(*v1alpha1.AgentSession); ok && key.Name == r.name {
		r.n++
		*s = r.sess
		return nil
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func TestDepthExemptReadsTheLiveSession(t *testing.T) {
	exempt := func(t *testing.T, liveSess *v1alpha1.AgentSession, cached *v1alpha1.AgentSession) bool {
		t.Helper()
		f := newFixture(t)
		f.raiseReady(true)
		f.sessionPod("creator", "full", 2)
		f.putActive(f.session("creator"), true)
		if liveSess.UID != cached.UID {
			f.putActive(liveSess, true)
			stored := f.session("creator")
			liveSess.Annotations = stored.Annotations
		}
		f.srv.Live = &staleLive{Reader: f.c, name: "creator", sess: *liveSess}
		c := &caller{kind: kindSession, depth: 3, parent: "creator", session: cached}
		return f.srv.depthExempt(context.Background(), c)
	}
	f := newFixture(t)
	f.raiseReady(true)
	f.sessionPod("creator", "full", 2)
	f.putActive(f.session("creator"), true)
	cached := f.session("creator").DeepCopy()
	broken := cached.DeepCopy()
	broken.Annotations[v1alpha1.AnnotationRaise] = "not-a-record"
	live := &staleLive{Reader: f.c, name: "creator", sess: *broken}
	f.srv.Live = live
	c := &caller{kind: kindSession, depth: 3, parent: "creator", session: cached}
	if f.srv.depthExempt(context.Background(), c) {
		t.Fatal("depthExempt honoured the cached annotation")
	}
	if live.n == 0 {
		t.Fatal("depthExempt did not read the live session")
	}
	other := cached.DeepCopy()
	other.UID = "uid-replaced"
	if exempt(t, other, cached) {
		t.Fatal("a live session with another UID was exempt")
	}
	relabelled := cached.DeepCopy()
	relabelled.Labels[v1alpha1.LabelDepth] = "3"
	f.srv.Live = &staleLive{Reader: f.c, name: "creator", sess: *relabelled}
	if f.srv.depthExempt(context.Background(), c) {
		t.Fatal("a live session labelled 3 was exempt")
	}
}

type activationReads struct {
	client.Reader
	configMaps int
	sessions   int
}

func (r *activationReads) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	switch obj.(type) {
	case *corev1.ConfigMap:
		r.configMaps++
	case *v1alpha1.AgentSession:
		r.sessions++
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func (r *activationReads) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*v1alpha1.AgentSessionList); ok {
		r.sessions++
	}
	return r.Reader.List(ctx, list, opts...)
}

func TestActivationPassReadsTheLedgerOnce(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	for i := 0; i < 50; i++ {
		f.sessionPod(fmt.Sprintf("idle-%02d", i), "full", 0)
	}
	// Settled rows (active with active, taken-back with taken-back) are
	// skipped from the hint. The pass still reads the ledger once and does
	// not read a live session. Removing the skip, or reading the hint
	// through the live reader, counts a session read or a second ConfigMap.
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("settled-%02d", i)
		f.sessionPod(name, "full", 0)
		f.putActive(f.session(name), true)
		if i%2 == 0 {
			continue
		}
		f.setRaiseState(string(f.session(name).UID), sessionraise.StateTakenBack)
		f.rewriteAnnotation(name, func(rec *sessionraise.Record) {
			rec.State = sessionraise.StateTakenBack
			rec.TakenBackBy = "human/" + humanSA
			rec.TakenBackAt = f.now.UTC().Format(time.RFC3339)
		})
	}
	reads := &activationReads{Reader: f.c}
	f.srv.Live = reads
	f.srv.activateRaises(context.Background())
	if reads.configMaps != 1 || reads.sessions != 0 {
		t.Fatalf("configmaps %d sessions %d", reads.configMaps, reads.sessions)
	}
}

// TestTakeBackOnTheFirstLiveGetStaysTakenBack is reviewer A's probe. The
// ledger row is active and the annotation is still pending. The take-back
// lands on the first live Get. After one pass the annotation is taken-back.
func TestTakeBackOnTheFirstLiveGetStaysTakenBack(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	f.sessionPod("creator", "full", 2)
	f.putPending(f.session("creator"))
	uid := string(f.session("creator").UID)
	f.setRaiseState(uid, sessionraise.StateActive)
	inner := f.c
	var wroteActive bool
	f.srv.Client = sessionHook{Client: inner, hook: func(obj client.Object) error {
		sess, ok := obj.(*v1alpha1.AgentSession)
		if !ok {
			return nil
		}
		rec, err := sessionraise.Parse(sess.Annotations[v1alpha1.AnnotationRaise])
		if err == nil && rec.State == sessionraise.StateActive {
			wroteActive = true
		}
		return nil
	}}
	f.srv.Live = &takeBackOnGet{Reader: inner, f: f}
	f.raiseSent(string(f.session("creator").UID))
	f.srv.activateRaises(context.Background())
	hook := f.srv.Live.(*takeBackOnGet)
	if !hook.fired {
		t.Fatal("the take-back did not land on the first live Get")
	}
	if wroteActive {
		t.Fatal("the pass wrote an active annotation over the take-back")
	}
	if got := f.ledgerEntry(uid); got.State != sessionraise.StateTakenBack {
		t.Fatalf("ledger %s", got.State)
	}
	ann, err := sessionraise.Parse(f.session("creator").Annotations[v1alpha1.AnnotationRaise])
	if err != nil || ann.State != sessionraise.StateTakenBack {
		t.Fatalf("annotation %v %s", err, ann.State)
	}
}

// TestPendingTakeBackDuringThePassIsNotLoggedAsUnverified takes a pending
// row back on the first live Get. The pass must not log that the annotation
// is not a verified pending record: the ledger row is taken-back.
func TestPendingTakeBackDuringThePassIsNotLoggedAsUnverified(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	sink := &lineSink{}
	f.srv.Log = logr.New(sink)
	f.sessionPod("creator", "full", 2)
	f.putPending(f.session("creator"))
	uid := string(f.session("creator").UID)
	f.srv.Live = &takeBackOnGet{Reader: f.c, f: f, anyState: true}
	f.srv.activateRaises(context.Background())
	hook := f.srv.Live.(*takeBackOnGet)
	if !hook.fired {
		t.Fatal("the take-back did not land on the first live Get")
	}
	if got := f.ledgerEntry(uid); got.State != sessionraise.StateTakenBack {
		t.Fatalf("ledger %s", got.State)
	}
	ann, err := sessionraise.Parse(f.session("creator").Annotations[v1alpha1.AnnotationRaise])
	if err != nil || ann.State != sessionraise.StateTakenBack {
		t.Fatalf("annotation %v %s", err, ann.State)
	}
	joined := strings.Join(sink.msgs, "\n")
	if strings.Contains(joined, "not a verified pending record") {
		t.Fatalf("logged a pending refusal for a taken-back row: %s", joined)
	}
}

// TestTakeBackInsideTheRepairWriteStaysTakenBack takes the raise back inside
// the repair branch's annotation Update. The annotation ends taken-back.
// Passing the row from before that write into the conflict retry, instead
// of nil, leaves the annotation active.
func TestTakeBackInsideTheRepairWriteStaysTakenBack(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	f.sessionPod("creator", "full", 2)
	f.putPending(f.session("creator"))
	uid := string(f.session("creator").UID)
	f.setRaiseState(uid, sessionraise.StateActive)
	inner := f.c
	var fired bool
	f.srv.Client = sessionHook{Client: inner, hook: func(obj client.Object) error {
		sess, ok := obj.(*v1alpha1.AgentSession)
		if !ok || fired {
			return nil
		}
		rec, err := sessionraise.Parse(sess.Annotations[v1alpha1.AnnotationRaise])
		if err != nil || rec.State != sessionraise.StateActive {
			return nil
		}
		fired = true
		cfg := f.srv.Raise.filled()
		led, err := f.srv.readLedger(context.Background(), cfg)
		if err != nil {
			return err
		}
		if _, err := led.TakeBack(string(sess.UID), "human/"+humanSA, f.now.Format(time.RFC3339), f.now); err != nil {
			return err
		}
		raw, err := led.Marshal()
		if err != nil {
			return err
		}
		cm := &corev1.ConfigMap{}
		if err := inner.Get(context.Background(), types.NamespacedName{Namespace: cfg.Namespace, Name: cfg.Ledger}, cm); err != nil {
			return err
		}
		cm.Data[sessionraise.LedgerField] = string(raw)
		if err := inner.Update(context.Background(), cm); err != nil {
			return err
		}
		priv, err := sessionraise.Load(context.Background(), f.srv.raiseStore(cfg), cfg.OperatorSecret, cfg.OperatorPublic)
		if err != nil {
			return err
		}
		row := led.Find(string(sess.UID))
		signed, err := sessionraise.Sign(recordFrom(*row, "please start"), priv)
		if err != nil {
			return err
		}
		encoded, err := signed.Encode()
		if err != nil {
			return err
		}
		live := &v1alpha1.AgentSession{}
		if err := inner.Get(context.Background(), types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}, live); err != nil {
			return err
		}
		live.Annotations[v1alpha1.AnnotationRaise] = string(encoded)
		if err := inner.Update(context.Background(), live); err != nil {
			return err
		}
		return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "agentsessions"}, sess.Name, errors.New("taken back"))
	}}
	f.raiseSent(string(f.session("creator").UID))
	f.srv.activateRaises(context.Background())
	if !fired {
		t.Fatal("the repair did not write the annotation")
	}
	if got := f.ledgerEntry(uid); got.State != sessionraise.StateTakenBack {
		t.Fatalf("ledger %s", got.State)
	}
	ann, err := sessionraise.Parse(f.session("creator").Annotations[v1alpha1.AnnotationRaise])
	if err != nil || ann.State != sessionraise.StateTakenBack {
		t.Fatalf("annotation %v %s", err, ann.State)
	}
}

func TestPendingRaiseStillActivates(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	f.sessionPod("creator", "full", 2)
	f.putPending(f.session("creator"))
	entry := f.ledgerEntry(string(f.session("creator").UID))
	sentAt := entry.PerformedAt
	notice, err := sessionraise.SignNotice(sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: 1,
		Outcome: sessionraise.OutcomeSent, At: sentAt, SentAt: sentAt,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	f.putNotice(notice)
	at, err := time.Parse(time.RFC3339, sentAt)
	if err != nil {
		t.Fatal(err)
	}
	f.now = at.Add(sessionraise.EffectDelay)
	f.srv.activateRaises(context.Background())
	if got := f.ledgerEntry(entry.UID); got.State != sessionraise.StateActive {
		t.Fatalf("pending row stayed %s", got.State)
	}
}

func TestTakenBackAnnotationIsRepaired(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	f.sessionPod("creator", "full", 2)
	sess := f.session("creator")
	f.putActive(sess, true)
	cfg := f.srv.Raise.filled()
	led, err := f.srv.readLedger(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	row := led.Find(string(sess.UID))
	row.State = sessionraise.StateTakenBack
	row.TakenBackBy = "human/" + humanSA
	row.TakenBackAt = f.now.UTC().Format(time.RFC3339)
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
	f.srv.activateRaises(context.Background())
	got, err := sessionraise.Parse(f.session("creator").Annotations[v1alpha1.AnnotationRaise])
	if err != nil || got.State != sessionraise.StateTakenBack {
		t.Fatalf("annotation %v %s", err, got.State)
	}
}

// takeBackOnGet writes a take-back of creator once the ledger row is active,
// before the Get that follows Finish returns.
type takeBackOnGet struct {
	client.Reader
	f        *fixture
	fired    bool
	anyState bool
}

func (h *takeBackOnGet) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*v1alpha1.AgentSession); ok && key.Name == "creator" && !h.fired {
		cfg := h.f.srv.Raise.filled()
		cm := &corev1.ConfigMap{}
		if err := h.Reader.Get(ctx, types.NamespacedName{Namespace: cfg.Namespace, Name: cfg.Ledger}, cm); err != nil {
			return err
		}
		led, err := sessionraise.Unmarshal([]byte(cm.Data[sessionraise.LedgerField]))
		if err != nil {
			return err
		}
		uid := string(h.f.session("creator").UID)
		row := led.Find(uid)
		if row != nil && (h.anyState || row.State == sessionraise.StateActive) {
			h.fired = true
			if _, err := led.TakeBack(uid, "human/"+humanSA, h.f.now.Format(time.RFC3339), h.f.now); err != nil {
				return err
			}
			raw, err := led.Marshal()
			if err != nil {
				return err
			}
			cm.Data[sessionraise.LedgerField] = string(raw)
			if err := h.f.c.Update(ctx, cm); err != nil {
				return err
			}
			priv, err := sessionraise.Load(ctx, sessionraise.Kube{Reader: h.Reader, Writer: h.f.c, Namespace: cfg.Namespace, Secrets: map[string]bool{cfg.OperatorSecret: true}}, cfg.OperatorSecret, cfg.OperatorPublic)
			if err != nil {
				return err
			}
			back := led.Find(uid)
			signed, err := sessionraise.Sign(recordFrom(*back, "please start"), priv)
			if err != nil {
				return err
			}
			encoded, err := signed.Encode()
			if err != nil {
				return err
			}
			sess := h.f.session("creator")
			sess.Annotations[v1alpha1.AnnotationRaise] = string(encoded)
			if err := h.f.c.Update(ctx, sess); err != nil {
				return err
			}
		}
	}
	return h.Reader.Get(ctx, key, obj, opts...)
}

func TestTakeBackBeforeTheActivationGetStaysTakenBack(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	keeperPub, keeperPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(keeperPub)
	f.sessionPod("creator", "full", 2)
	f.putPending(f.session("creator"))
	entry := f.ledgerEntry(string(f.session("creator").UID))
	notice, err := sessionraise.SignNotice(sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: 1,
		Outcome: sessionraise.OutcomeSent, At: entry.PerformedAt, SentAt: entry.PerformedAt,
	}, keeperPriv)
	if err != nil {
		t.Fatal(err)
	}
	f.putNotice(notice)
	at, err := time.Parse(time.RFC3339, entry.PerformedAt)
	if err != nil {
		t.Fatal(err)
	}
	f.now = at.Add(sessionraise.EffectDelay)
	f.srv.Live = &takeBackOnGet{Reader: f.c, f: f}
	f.srv.activateRaises(context.Background())
	hook := f.srv.Live.(*takeBackOnGet)
	if !hook.fired {
		t.Fatal("the get after Finish did not see an active ledger row")
	}
	if got := f.ledgerEntry(entry.UID); got.State != sessionraise.StateTakenBack {
		t.Fatalf("ledger %s", got.State)
	}
	ann, err := sessionraise.Parse(f.session("creator").Annotations[v1alpha1.AnnotationRaise])
	if err != nil || ann.State != sessionraise.StateTakenBack {
		t.Fatalf("annotation %v %s", err, ann.State)
	}
}

func TestScheduledPathWithAnActiveRaiseStaysRefused(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	f.sessionPod("owner", "full", 2)
	sess := f.session("owner")
	f.putActive(sess, true)
	d := swDispatch()
	d.Owner = &scheduledwork.SessionRef{Name: "owner", UID: sess.UID}
	req := swRequest()
	req.Profile = ""
	_, _, _, err := f.srv.CreateScheduledSession(context.Background(), req, d)
	wantRefused(t, err, "two levels is the limit")
}

func TestExemptCreateStopsAtTheChildCap(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.sessionPod("creator", "full", 2)
	f.putActive(f.session("creator"), true)
	for range maxRunningChildrenPerAgent {
		w := f.do(http.MethodPost, apiv1.SessionsPath, tok, task())
		if w.Code != http.StatusCreated {
			t.Fatalf("child: %d %s", w.Code, w.Body.String())
		}
	}
	w := f.do(http.MethodPost, apiv1.SessionsPath, tok, task())
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("cap: %d %s", w.Code, w.Body.String())
	}
}

func TestFlagsOffRefuseTheCreateAndLeavePendingPending(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(false)
	tok := f.sessionPod("creator", "full", 2)
	f.putActive(f.session("creator"), true)
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tok, task()); w.Code != http.StatusForbidden {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	view := f.do(http.MethodGet, apiv1.SessionPath("creator"), tokHuman, nil)
	if !strings.Contains(view.Body.String(), "raising is switched off") || strings.Contains(view.Body.String(), `"state":"active"`) {
		t.Fatalf("view: %s", view.Body.String())
	}
	g := newFixture(t)
	g.raiseReady(false)
	g.sessionPod("creator", "full", 2)
	g.putPending(g.session("creator"))
	f = g
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	entry := f.ledgerEntry(string(f.session("creator").UID))
	sentAt := entry.PerformedAt
	notice, err := sessionraise.SignNotice(sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: 1,
		Outcome: sessionraise.OutcomeSent, At: sentAt, SentAt: sentAt,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	f.putNotice(notice)
	at, err := time.Parse(time.RFC3339, sentAt)
	if err != nil {
		t.Fatal(err)
	}
	f.now = at.Add(sessionraise.EffectDelay)
	f.srv.activateRaises(context.Background())
	if got := f.ledgerEntry(entry.UID); got.State != sessionraise.StatePending {
		t.Fatalf("flags off activated: %s", got.State)
	}
}

func TestStrippedSameHashIsNeverSignedActive(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	f.sessionPod("creator", "full", 2)
	f.putPending(f.session("creator"))
	current := f.session("creator")
	rec, err := sessionraise.Parse(current.Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		t.Fatal(err)
	}
	rec.Owner.Words = "please, start"
	if sessionraise.WordsHash(rec.Owner.Words) != rec.Owner.WordsHash {
		t.Fatal("the edited words do not share the normalised hash")
	}
	rec.Sig = ""
	raw, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	current.Annotations[v1alpha1.AnnotationRaise] = string(raw)
	if err := f.c.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	entry := f.ledgerEntry(string(current.UID))
	notice, err := sessionraise.SignNotice(sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: 1,
		Outcome: sessionraise.OutcomeSent, At: entry.PerformedAt, SentAt: entry.PerformedAt,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	f.putNotice(notice)
	at, err := time.Parse(time.RFC3339, entry.PerformedAt)
	if err != nil {
		t.Fatal(err)
	}
	f.now = at.Add(sessionraise.EffectDelay)
	f.srv.activateRaises(context.Background())
	got, err := sessionraise.Parse(f.session("creator").Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		t.Fatal(err)
	}
	op, err := sessionraise.ReadPublic(context.Background(), f.srv.raiseStore(f.srv.Raise.filled()), sessionraise.OperatorPublicName)
	if err != nil {
		t.Fatal(err)
	}
	if sessionraise.Verify(got, op) || got.State == sessionraise.StateActive {
		t.Fatalf("stripped words were signed active: %+v", got.State)
	}
	if f.ledgerEntry(entry.UID).State != sessionraise.StatePending {
		t.Fatal("the ledger row did not stand")
	}
	c := &caller{kind: kindSession, depth: 3, parent: "creator", session: f.session("creator")}
	if f.srv.depthExempt(context.Background(), c) {
		t.Fatal("the exemption was granted")
	}
}

func TestSentBeforeTheRaiseStaysPending(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	f.sessionPod("creator", "full", 2)
	f.putPending(f.session("creator"))
	entry := f.ledgerEntry(string(f.session("creator").UID))
	at, err := time.Parse(time.RFC3339, entry.PerformedAt)
	if err != nil {
		t.Fatal(err)
	}
	sentAt := at.Add(-time.Minute).UTC().Format(time.RFC3339)
	notice, err := sessionraise.SignNotice(sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: 1,
		Outcome: sessionraise.OutcomeSent, At: sentAt, SentAt: sentAt,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	f.putNotice(notice)
	f.now = at.Add(-time.Minute).Add(sessionraise.EffectDelay)
	f.srv.activateRaises(context.Background())
	if got := f.ledgerEntry(entry.UID); got.State != sessionraise.StatePending {
		t.Fatalf("early sentAt activated: %s", got.State)
	}
	w := f.do(http.MethodGet, apiv1.SessionPath("creator"), tokHuman, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("view: %d %s", w.Code, w.Body.String())
	}
	view := decode[apiv1.Session](t, w)
	if view.Raise == nil || view.Raise.ActiveFrom != "" || view.Raise.Ignored != noticeBeforeRaise || view.Raise.State == sessionraise.StateActive {
		t.Fatalf("early notice view: %+v", view.Raise)
	}
}

func TestTamperedNoticeSignatureStaysPending(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	f.sessionPod("creator", "full", 2)
	f.putPending(f.session("creator"))
	entry := f.ledgerEntry(string(f.session("creator").UID))
	at, err := time.Parse(time.RFC3339, entry.PerformedAt)
	if err != nil {
		t.Fatal(err)
	}
	later := at.Add(time.Second).UTC().Format(time.RFC3339)
	notice, err := sessionraise.SignNotice(sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: 1,
		Outcome: sessionraise.OutcomeSent, At: entry.PerformedAt, SentAt: entry.PerformedAt,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	notice.SentAt = later
	f.putNotice(notice)
	f.now = at.Add(time.Second).Add(sessionraise.EffectDelay)
	f.srv.activateRaises(context.Background())
	if got := f.ledgerEntry(entry.UID); got.State != sessionraise.StatePending {
		t.Fatalf("a notice with a broken signature activated: %s", got.State)
	}
}

func TestLowEncodedLimitSpendsNoWord(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true, "workbench")
	restore := sessionraise.SetEncodedLimitForTest(64)
	defer restore()
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 0)
	body := f.raiseReq(string(target.UID), "workbench", "please raise this", sessionraise.WherePhone, false)
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body), http.StatusUnprocessableEntity, raiseTooBig)
	led, err := f.srv.readLedger(context.Background(), f.srv.Raise.filled())
	if err != nil {
		t.Fatal(err)
	}
	if len(led.Words) != 0 || len(led.Sessions) != 0 {
		t.Fatalf("a refused raise spent a word: %+v", led)
	}
}

type lineSink struct {
	n    int
	msgs []string
}

func (s *lineSink) Init(logr.RuntimeInfo) {}
func (s *lineSink) Enabled(int) bool      { return true }
func (s *lineSink) Info(_ int, msg string, keys ...any) {
	s.n++
	var b strings.Builder
	b.WriteString(msg)
	for i := 0; i+1 < len(keys); i += 2 {
		fmt.Fprintf(&b, " %v=%v", keys[i], keys[i+1])
	}
	s.msgs = append(s.msgs, strings.ReplaceAll(b.String(), "\n", " "))
}

func assertNoRaiseSignature(t *testing.T, joined string, rec sessionraise.Record) {
	t.Helper()
	if strings.Contains(joined, "sig=") || strings.Contains(joined, "keyId=") || (rec.Sig != "" && strings.Contains(joined, rec.Sig)) || (rec.KeyID != "" && strings.Contains(joined, rec.KeyID)) {
		t.Fatalf("log carries a signature: %s", joined)
	}
}
func (s *lineSink) Error(error, string, ...any)    {}
func (s *lineSink) WithValues(...any) logr.LogSink { return s }
func (s *lineSink) WithName(string) logr.LogSink   { return s }

func TestRaiseKeyOwnerRetriesQuietly(t *testing.T) {
	f := newFixture(t)
	f.srv.Raise = &RaiseConfig{Dispatch: true}
	sink := &lineSink{}
	f.srv.Log = logr.New(sink)
	owner := &RaiseKeyOwner{Server: f.srv}
	if owner.interval() != 5*time.Minute {
		t.Fatalf("interval %s", owner.interval())
	}
	owner.ensure(context.Background())
	owner.ensure(context.Background())
	if sink.n != 1 {
		t.Fatalf("logs %d", sink.n)
	}
	f.now = f.now.Add(time.Hour)
	owner.ensure(context.Background())
	if sink.n != 2 {
		t.Fatalf("logs after an hour %d", sink.n)
	}
}

type recSender struct {
	n    int
	last keeper.Notice
	sent chan struct{}
}

func (s *recSender) Send(context.Context, keeper.Notice) error { return nil }

func (s *recSender) SendReceipt(_ context.Context, n keeper.Notice) (keeper.Receipt, error) {
	s.n++
	s.last = n
	if s.sent != nil {
		select {
		case s.sent <- struct{}{}:
		default:
		}
	}
	return keeper.Receipt{Status: 1, Confirmed: true}, nil
}

type sessionHook struct {
	client.Client
	hook func(client.Object) error
}

func (h sessionHook) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if err := h.hook(obj); err != nil {
		return err
	}
	return h.Client.Update(ctx, obj, opts...)
}

func TestTakeBackRacesTheActivationWrite(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	keeperPub, keeperPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(keeperPub)
	f.sessionPod("creator", "full", 2)
	f.putPending(f.session("creator"))
	entry := f.ledgerEntry(string(f.session("creator").UID))
	notice, err := sessionraise.SignNotice(sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: 1,
		Outcome: sessionraise.OutcomeSent, At: entry.PerformedAt, SentAt: entry.PerformedAt,
	}, keeperPriv)
	if err != nil {
		t.Fatal(err)
	}
	f.putNotice(notice)
	at, err := time.Parse(time.RFC3339, entry.PerformedAt)
	if err != nil {
		t.Fatal(err)
	}
	f.now = at.Add(sessionraise.EffectDelay)
	inner := f.c
	var fired bool
	f.srv.Client = sessionHook{Client: inner, hook: func(obj client.Object) error {
		sess, ok := obj.(*v1alpha1.AgentSession)
		if !ok || fired {
			return nil
		}
		rec, err := sessionraise.Parse(sess.Annotations[v1alpha1.AnnotationRaise])
		if err != nil || rec.State != sessionraise.StateActive {
			return nil
		}
		fired = true
		cfg := f.srv.Raise.filled()
		led, err := f.srv.readLedger(context.Background(), cfg)
		if err != nil {
			return err
		}
		if _, err := led.TakeBack(string(sess.UID), "human/"+humanSA, f.now.Format(time.RFC3339), f.now); err != nil {
			return err
		}
		raw, err := led.Marshal()
		if err != nil {
			return err
		}
		cm := &corev1.ConfigMap{}
		if err := inner.Get(context.Background(), types.NamespacedName{Namespace: cfg.Namespace, Name: cfg.Ledger}, cm); err != nil {
			return err
		}
		cm.Data[sessionraise.LedgerField] = string(raw)
		if err := inner.Update(context.Background(), cm); err != nil {
			return err
		}
		priv, err := sessionraise.Load(context.Background(), f.srv.raiseStore(cfg), cfg.OperatorSecret, cfg.OperatorPublic)
		if err != nil {
			return err
		}
		row := led.Find(string(sess.UID))
		signed, err := sessionraise.Sign(recordFrom(*row, "please start"), priv)
		if err != nil {
			return err
		}
		encoded, err := signed.Encode()
		if err != nil {
			return err
		}
		live := &v1alpha1.AgentSession{}
		if err := inner.Get(context.Background(), types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}, live); err != nil {
			return err
		}
		live.Annotations[v1alpha1.AnnotationRaise] = string(encoded)
		if err := inner.Update(context.Background(), live); err != nil {
			return err
		}
		return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "agentsessions"}, sess.Name, errors.New("taken back"))
	}}
	f.srv.activateRaises(context.Background())
	if !fired {
		t.Fatal("the activation did not write the annotation")
	}
	if got := f.ledgerEntry(entry.UID); got.State != sessionraise.StateTakenBack {
		t.Fatalf("ledger %s", got.State)
	}
	got, err := sessionraise.Parse(f.session("creator").Annotations[v1alpha1.AnnotationRaise])
	if err != nil || got.State != sessionraise.StateTakenBack {
		t.Fatalf("annotation %v %s", err, got.State)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: sessionraise.ObjectNamespace, Name: sessionraise.KeeperSecretName},
		Data:       map[string][]byte{sessionraise.SecretKey: []byte(keeperPriv)},
	}
	if err := inner.Create(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	sender := &recSender{sent: make(chan struct{}, 1)}
	sink := &lineSink{}
	p := &keeper.RaisePager{
		Client: inner, Live: inner, Namespace: sessionraise.ObjectNamespace, SessionNamespace: sessionNS,
		Sender: sender, Now: func() time.Time { return f.now }, Log: logr.New(sink),
	}
	// Start announces once, then waits. A cancelled context makes that one
	// read fail, so cancel only after the synchronous announce has run.
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- p.Start(ctx) }()
	select {
	case <-sender.sent:
	case <-time.After(2 * time.Second):
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the pager did not stop")
	}
	if sender.n != 1 || !strings.Contains(sender.last.Title, "taken back") {
		row := f.ledgerEntry(entry.UID)
		t.Fatalf("notices %d %q logs %v row %+v", sender.n, sender.last.Title, sink.msgs, row)
	}
}

func TestTakeBackStillSuspendsWhenTheAnnotationWriteFails(t *testing.T) {
	f := newFixture(t)
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
	grant := f.activeWorkbenchGrant("target")
	inner := f.c
	f.srv.Client = sessionHook{Client: inner, hook: func(obj client.Object) error {
		sess, ok := obj.(*v1alpha1.AgentSession)
		if !ok {
			return nil
		}
		rec, err := sessionraise.Parse(sess.Annotations[v1alpha1.AnnotationRaise])
		if err == nil && rec.State == sessionraise.StateTakenBack {
			return errors.New("annotation write failed")
		}
		return nil
	}}
	w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, apiv1.TakeBackRequest{ExpectUID: string(target.UID)})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("take-back: %d %s", w.Code, w.Body.String())
	}
	if f.ledgerEntry(string(target.UID)).State != sessionraise.StateTakenBack {
		t.Fatal("ledger was not taken back")
	}
	if f.session("target").Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		t.Fatal("the suspend did not run")
	}
	if !f.grant(grant).Spec.Release {
		t.Fatal("the grant was not released")
	}
}

func TestActivationRepairsAVerifiedPendingAnnotation(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	f.sessionPod("creator", "full", 2)
	f.putPending(f.session("creator"))
	f.resign("creator", sessionraise.StateActive, "2", true)
	// resign re-signs the annotation too. Put the verified pending bytes back
	// while the ledger row stays active, which is the repair the activator owes.
	pending := f.session("creator")
	rec, err := sessionraise.Parse(pending.Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		t.Fatal(err)
	}
	rec.State = sessionraise.StatePending
	rec.NoticeSentAt = ""
	rec.ActiveFrom = ""
	rec.Sig = ""
	priv, err := sessionraise.Load(context.Background(), f.srv.raiseStore(f.srv.Raise.filled()), sessionraise.OperatorSecretName, sessionraise.OperatorPublicName)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := sessionraise.Sign(rec, priv)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signed.Encode()
	if err != nil {
		t.Fatal(err)
	}
	pending.Annotations[v1alpha1.AnnotationRaise] = string(raw)
	if err := f.c.Update(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	f.raiseSent(string(f.session("creator").UID))
	f.srv.activateRaises(context.Background())
	got, err := sessionraise.Parse(f.session("creator").Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		t.Fatal(err)
	}
	op, err := sessionraise.ReadPublic(context.Background(), f.srv.raiseStore(f.srv.Raise.filled()), sessionraise.OperatorPublicName)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != sessionraise.StateActive || !sessionraise.Verify(got, op) {
		t.Fatalf("annotation was not repaired: %s verified %v", got.State, sessionraise.Verify(got, op))
	}
}

func TestRaiseLogNamesTheRecordOnceAndNotTheSignature(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true, "workbench")
	sink := &lineSink{}
	f.srv.Log = logr.New(sink)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 0)
	body := f.raiseReq(string(target.UID), "workbench", "please raise this", sessionraise.WherePhone, false)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusAccepted {
		t.Fatalf("raise: %d %s", w.Code, w.Body.String())
	}
	raw := f.session("target").Annotations[v1alpha1.AnnotationRaise]
	rec, err := sessionraise.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(sink.msgs, "\n")
	if strings.Count(joined, "Raised target") != 1 || !strings.Contains(joined, f.ledgerEntry(string(target.UID)).ID) || strings.Contains(joined, raw) {
		t.Fatalf("log: %s", joined)
	}
	assertNoRaiseSignature(t, joined, rec)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: sessionraise.ObjectNamespace, Name: sessionraise.LedgerName}}
	if err := f.c.Delete(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodGet, apiv1.SessionPath("target"), tokHuman, nil); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if w := f.do(http.MethodGet, apiv1.SessionPath("target"), tokHuman, nil); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	ignored := 0
	for _, msg := range sink.msgs {
		if strings.Contains(msg, "raise record ignored") {
			ignored++
		}
	}
	if ignored != 1 {
		t.Fatalf("ignored record logged %d times: %v", ignored, sink.msgs)
	}
}

// onceRaiseLive splits one fact across two readings. Session reads of name
// return pendingAnn until the operator ledger has been read once; every
// later session read returns the stored annotation, which stays active.
// A create live-reads the caller before the depth exemption, so the flip
// is keyed off the ledger read the exemption itself performs, not off the
// first session read of the request. The first ledger read fails when
// failLedger is set, and later ledger reads succeed.
type onceRaiseLive struct {
	client.Reader
	name       string
	pendingAnn string
	failLedger bool
	ledgers    int
}

func (r *onceRaiseLive) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.ConfigMap); ok && key.Name == sessionraise.LedgerName {
		r.ledgers++
		if r.failLedger && r.ledgers == 1 {
			return fmt.Errorf("timeout")
		}
	}
	if err := r.Reader.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if s, ok := obj.(*v1alpha1.AgentSession); ok && key.Name == r.name && r.pendingAnn != "" && r.ledgers == 0 {
		if s.Annotations == nil {
			s.Annotations = map[string]string{}
		}
		s.Annotations[v1alpha1.AnnotationRaise] = r.pendingAnn
	}
	return nil
}

// signedAnnotation is a signed record for the session's ledger row with
// state substituted. It does not write the row or the annotation.
func (f *fixture) signedAnnotation(name, state string) string {
	f.t.Helper()
	ctx := context.Background()
	sess := f.session(name)
	cfg := f.srv.Raise.filled()
	led, err := f.srv.readLedger(ctx, cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	row := led.Find(string(sess.UID))
	if row == nil {
		f.t.Fatal("no row")
	}
	copy := *row
	copy.State = state
	if state != sessionraise.StateActive {
		copy.NoticeSentAt = ""
		copy.ActiveFrom = ""
	}
	priv, err := sessionraise.Load(ctx, f.srv.raiseStore(cfg), cfg.OperatorSecret, cfg.OperatorPublic)
	if err != nil {
		f.t.Fatal(err)
	}
	signed, err := sessionraise.Sign(recordFrom(copy, "please start"), priv)
	if err != nil {
		f.t.Fatal(err)
	}
	encoded, err := signed.Encode()
	if err != nil {
		f.t.Fatal(err)
	}
	return string(encoded)
}

func TestDepthExemptionIsDecidedOnce(t *testing.T) {
	const depthSentence = "session creator is 2 levels deep and may not start sessions: two levels is the limit (DESIGN-001 3.4). A session at depth 2 can be raised to start sessions (agent-run help raise)."
	const (
		flipAnnotation = "flip"
		failLedger     = "ledger"
		clean          = "clean"
	)
	cases := []struct {
		name             string
		mode             string
		profile          string
		defaultWorkbench bool
		want             int
	}{
		{name: "pending then active, named workbench", mode: flipAnnotation, profile: "workbench", want: http.StatusForbidden},
		{name: "ledger fails once, named workbench", mode: failLedger, profile: "workbench", want: http.StatusForbidden},
		{name: "pending then active, default workbench", mode: flipAnnotation, defaultWorkbench: true, want: http.StatusForbidden},
		{name: "ledger fails once, default workbench", mode: failLedger, defaultWorkbench: true, want: http.StatusForbidden},
		{name: "ordinary profile, every read good", mode: clean, profile: "dev", want: http.StatusCreated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.raiseReady(true)
			if tc.defaultWorkbench {
				full := f.tmpl.Profiles["full"]
				full.ServiceAccount = templates.WorkbenchServiceAccount
				f.tmpl.Profiles["full"] = full
			}
			tok := f.workbenchAt("creator", 2)
			f.putActive(f.session("creator"), true)
			if tc.mode != clean {
				live := &onceRaiseLive{Reader: f.c, name: "creator"}
				switch tc.mode {
				case flipAnnotation:
					live.pendingAnn = f.signedAnnotation("creator", sessionraise.StatePending)
				case failLedger:
					live.failLedger = true
				}
				f.srv.Live = live
			}
			req := task()
			req.Profile = tc.profile
			w := f.do(http.MethodPost, apiv1.SessionsPath, tok, req)
			if tc.want == http.StatusForbidden {
				wantSentence(t, w, http.StatusForbidden, depthSentence)
			} else {
				if w.Code != http.StatusCreated {
					t.Fatalf("child: %d %s", w.Code, w.Body.String())
				}
				child := f.session(decode[apiv1.Session](t, w).Name)
				if child.Labels[v1alpha1.LabelDepth] != "3" {
					t.Fatalf("depth %s", child.Labels[v1alpha1.LabelDepth])
				}
			}
			var list v1alpha1.AgentSessionList
			if err := f.c.List(context.Background(), &list); err != nil {
				t.Fatal(err)
			}
			wantN := 1
			if tc.want == http.StatusCreated {
				wantN = 2
			}
			if len(list.Items) != wantN {
				names := make([]string, len(list.Items))
				for i := range list.Items {
					names[i] = list.Items[i].Name
				}
				t.Fatalf("sessions %v, want %d", names, wantN)
			}
		})
	}
}

func TestDepthHintIsOnlyWhenDispatchIsOn(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(false)
	tok := f.sessionPod("creator", "full", 2)
	w := f.do(http.MethodPost, apiv1.SessionsPath, tok, task())
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "can be raised") {
		t.Fatalf("hint while dispatch is off: %d %s", w.Code, w.Body.String())
	}
	f.srv.Raise.Dispatch = true
	w = f.do(http.MethodPost, apiv1.SessionsPath, tok, task())
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "can be raised") {
		t.Fatalf("no hint while dispatch is on: %d %s", w.Code, w.Body.String())
	}
}

func (f *fixture) activeWorkbenchGrant(name string) string {
	f.t.Helper()
	g := f.putGrant(name, func(g *v1alpha1.AccessGrant) {
		g.Spec.Requester.Profile = "workbench"
	})
	f.setGrantStatus(g.Name, v1alpha1.GrantActive, nil)
	return g.Name
}

func (f *fixture) setRaiseState(uid, state string) {
	f.t.Helper()
	cfg := f.srv.Raise.filled()
	led, err := f.srv.readLedger(context.Background(), cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	row := led.Find(uid)
	if row == nil {
		f.t.Fatal("no ledger row")
	}
	row.State = state
	raw, err := led.Marshal()
	if err != nil {
		f.t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: cfg.Namespace, Name: cfg.Ledger}, cm); err != nil {
		f.t.Fatal(err)
	}
	cm.Data[sessionraise.LedgerField] = string(raw)
	if err := f.c.Update(context.Background(), cm); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) rewriteAnnotation(name string, edit func(*sessionraise.Record)) {
	f.t.Helper()
	rec, err := sessionraise.Parse(f.session(name).Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		f.t.Fatal(err)
	}
	edit(&rec)
	rec.Sig = ""
	cfg := f.srv.Raise.filled()
	priv, err := sessionraise.Load(context.Background(), f.srv.raiseStore(cfg), cfg.OperatorSecret, cfg.OperatorPublic)
	if err != nil {
		f.t.Fatal(err)
	}
	signed, err := sessionraise.Sign(rec, priv)
	if err != nil {
		f.t.Fatal(err)
	}
	raw, err := signed.Encode()
	if err != nil {
		f.t.Fatal(err)
	}
	sess := f.session(name)
	sess.Annotations[v1alpha1.AnnotationRaise] = string(raw)
	if err := f.c.Update(context.Background(), sess); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) sentNoticeDue(entry sessionraise.SessionEntry) {
	f.t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		f.t.Fatal(err)
	}
	f.publishKeeper(pub)
	notice, err := sessionraise.SignNotice(sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: 1,
		Outcome: sessionraise.OutcomeSent, At: entry.PerformedAt, SentAt: entry.PerformedAt,
	}, priv)
	if err != nil {
		f.t.Fatal(err)
	}
	f.putNotice(notice)
	at, err := time.Parse(time.RFC3339, entry.PerformedAt)
	if err != nil {
		f.t.Fatal(err)
	}
	f.now = at.Add(sessionraise.EffectDelay)
}

func TestRaiseIsLoggedWhenTheAnnotationWriteFails(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	sink := &lineSink{}
	f.srv.Log = logr.New(sink)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	f.srv.Client = sessionHook{Client: f.c, hook: func(obj client.Object) error {
		sess, ok := obj.(*v1alpha1.AgentSession)
		if ok && sess.Annotations[v1alpha1.AnnotationRaise] != "" {
			return errors.New("annotation write failed")
		}
		return nil
	}}
	body := f.raiseReq(string(target.UID), "", "please start sessions", sessionraise.WherePhone, true)
	w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("raise: %d %s", w.Code, w.Body.String())
	}
	joined := strings.Join(sink.msgs, "\n")
	if strings.Count(joined, "Raised target") != 1 || !strings.Contains(joined, "The raise was recorded") || strings.Contains(joined, "Tom is being told") || !strings.Contains(joined, "please start sessions") {
		t.Fatalf("log: %s", joined)
	}
	assertNoRaiseSignature(t, joined, sessionraise.Record{})
}

func TestTakeBackLogKeepsTheVerifiedWords(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	sink := &lineSink{}
	f.srv.Log = logr.New(sink)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	body := f.raiseReq(string(target.UID), "", "please start sessions", sessionraise.WherePhone, true)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusAccepted {
		t.Fatalf("raise: %d %s", w.Code, w.Body.String())
	}
	f.srv.Client = sessionHook{Client: f.c, hook: func(obj client.Object) error {
		sess, ok := obj.(*v1alpha1.AgentSession)
		if !ok {
			return nil
		}
		rec, err := sessionraise.Parse(sess.Annotations[v1alpha1.AnnotationRaise])
		if err == nil && rec.State == sessionraise.StateTakenBack {
			return errors.New("annotation write failed")
		}
		return nil
	}}
	w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, apiv1.TakeBackRequest{ExpectUID: string(target.UID)})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("take-back: %d %s", w.Code, w.Body.String())
	}
	joined := strings.Join(sink.msgs, "\n")
	if strings.Count(joined, "took back a raise of target") != 1 || !strings.Contains(joined, "please start sessions") {
		t.Fatalf("log: %s", joined)
	}
	assertNoRaiseSignature(t, joined, sessionraise.Record{})
}

func TestPlainTakeBackLogsNoIgnoredRecord(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	sink := &lineSink{}
	f.srv.Log = logr.New(sink)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	body := f.raiseReq(string(target.UID), "", "please start sessions", sessionraise.WherePhone, true)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusAccepted {
		t.Fatalf("raise: %d %s", w.Code, w.Body.String())
	}
	w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, apiv1.TakeBackRequest{ExpectUID: string(target.UID)})
	if w.Code != http.StatusOK {
		t.Fatalf("take-back: %d %s", w.Code, w.Body.String())
	}
	ann, err := sessionraise.Parse(f.session("target").Annotations[v1alpha1.AnnotationRaise])
	if err != nil || ann.State != sessionraise.StateTakenBack {
		t.Fatalf("annotation %v %s", err, ann.State)
	}
	joined := strings.Join(sink.msgs, "\n")
	if strings.Contains(joined, "raise record ignored") {
		t.Fatalf("a plain take-back logged an ignored record: %s", joined)
	}
}

func TestLostRaiseAnswerIsLoggedOnTheRetry(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	sink := &lineSink{}
	f.srv.Log = logr.New(sink)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	inner := f.c
	var fired bool
	f.srv.Client = sessionHook{Client: inner, hook: func(obj client.Object) error {
		cm, ok := obj.(*corev1.ConfigMap)
		if !ok || cm.Name != sessionraise.LedgerName || fired {
			return nil
		}
		fired = true
		if err := inner.Update(context.Background(), cm); err != nil {
			return err
		}
		return errors.New("timeout")
	}}
	body := f.raiseReq(string(target.UID), "", "please start sessions", sessionraise.WherePhone, true)
	w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("lost answer: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(strings.Join(sink.msgs, "\n"), "Raised target") {
		t.Fatal("the lost answer was logged before the retry")
	}
	f.srv.Client = inner
	w = f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body)
	if w.Code != http.StatusAccepted && w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	joined := strings.Join(sink.msgs, "\n")
	if strings.Count(joined, "Raised target") != 1 || !strings.Contains(joined, "The raise was recorded") || strings.Contains(joined, "Tom is being told") {
		t.Fatalf("log: %s", joined)
	}
}

func TestTakeBackLogFollowsOnlyTheWritingAttempt(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	body := f.raiseReq(string(target.UID), "", "please start sessions", sessionraise.WherePhone, true)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusAccepted {
		t.Fatalf("raise: %d %s", w.Code, w.Body.String())
	}
	sink := &lineSink{}
	f.srv.Log = logr.New(sink)
	inner := f.c
	var fired bool
	f.srv.Client = sessionHook{Client: inner, hook: func(obj client.Object) error {
		cm, ok := obj.(*corev1.ConfigMap)
		if !ok || cm.Name != sessionraise.LedgerName || fired {
			return nil
		}
		fired = true
		cfg := f.srv.Raise.filled()
		led, err := sessionraise.Unmarshal([]byte(cm.Data[sessionraise.LedgerField]))
		if err != nil {
			return err
		}
		if _, err := led.TakeBack(string(target.UID), "human/"+humanSA, f.now.Format(time.RFC3339), f.now); err != nil {
			return err
		}
		raw, err := led.Marshal()
		if err != nil {
			return err
		}
		stored := &corev1.ConfigMap{}
		if err := inner.Get(context.Background(), types.NamespacedName{Namespace: cfg.Namespace, Name: cfg.Ledger}, stored); err != nil {
			return err
		}
		stored.Data[sessionraise.LedgerField] = string(raw)
		if err := inner.Update(context.Background(), stored); err != nil {
			return err
		}
		return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, cm.Name, errors.New("other replica"))
	}}
	w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, apiv1.TakeBackRequest{ExpectUID: string(target.UID)})
	if w.Code != http.StatusOK {
		t.Fatalf("take-back: %d %s", w.Code, w.Body.String())
	}
	if !fired {
		t.Fatal("the ledger write did not conflict")
	}
	if f.ledgerEntry(string(target.UID)).State != sessionraise.StateTakenBack {
		t.Fatal("ledger was not taken back")
	}
	joined := strings.Join(sink.msgs, "\n")
	if strings.Contains(joined, "took back") {
		t.Fatalf("logged a take-back this attempt did not write: %s", joined)
	}
}

func TestRepairLogsTheActiveState(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	sink := &lineSink{}
	f.srv.Log = logr.New(sink)
	f.sessionPod("creator", "full", 2)
	f.putPending(f.session("creator"))
	f.setRaiseState(string(f.session("creator").UID), sessionraise.StateActive)
	f.raiseSent(string(f.session("creator").UID))
	f.srv.activateRaises(context.Background())
	ann, err := sessionraise.Parse(f.session("creator").Annotations[v1alpha1.AnnotationRaise])
	if err != nil || ann.State != sessionraise.StateActive {
		t.Fatalf("annotation %v %s", err, ann.State)
	}
	if strings.Count(strings.Join(sink.msgs, "\n"), "raise active") != 1 {
		t.Fatalf("log: %s", strings.Join(sink.msgs, "\n"))
	}
}

func TestExemptCreateStoresTheResolvedProfile(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.workbenchAt("creator", 2)
	f.putActive(f.session("creator"), true)
	w := f.do(http.MethodPost, apiv1.SessionsPath, tok, task())
	if w.Code != http.StatusCreated {
		t.Fatalf("child: %d %s", w.Code, w.Body.String())
	}
	child := f.session(decode[apiv1.Session](t, w).Name)
	if child.Spec.Profile != f.tmpl.DefaultProfile || child.Spec.Profile == "" {
		t.Fatalf("unnamed child profile %q, default %q", child.Spec.Profile, f.tmpl.DefaultProfile)
	}
	req := task()
	req.Profile = "dev"
	w = f.do(http.MethodPost, apiv1.SessionsPath, tok, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("named child: %d %s", w.Code, w.Body.String())
	}
	named := f.session(decode[apiv1.Session](t, w).Name)
	if named.Spec.Profile != "dev" {
		t.Fatalf("named child profile %q", named.Spec.Profile)
	}
}

func TestExemptCreateResolvesTheProfileOnce(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	calls := 0
	real := f.srv.Templates
	f.srv.Templates = func(ctx context.Context) (*templates.Templates, error) {
		calls++
		got, err := real(ctx)
		if err != nil || got == nil || calls < 2 {
			return got, err
		}
		next := *got
		profiles := make(map[string]templates.Profile, len(got.Profiles))
		for name, profile := range got.Profiles {
			profiles[name] = profile
		}
		def := profiles[got.DefaultProfile]
		def.ServiceAccount = templates.WorkbenchServiceAccount
		profiles[got.DefaultProfile] = def
		next.Profiles = profiles
		return &next, nil
	}
	tok := f.workbenchAt("creator", 2)
	f.putActive(f.session("creator"), true)
	w := f.do(http.MethodPost, apiv1.SessionsPath, tok, task())
	if w.Code != http.StatusCreated {
		t.Fatalf("child: %d %s (templates read %d times)", w.Code, w.Body.String(), calls)
	}
	child := f.session(decode[apiv1.Session](t, w).Name)
	if child.Spec.Profile != "full" {
		t.Fatalf("profile %q after %d template reads", child.Spec.Profile, calls)
	}
}

func TestRaiseActiveIsLoggedBeforeTheAnnotationWrite(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	sink := &lineSink{}
	f.srv.Log = logr.New(sink)
	f.sessionPod("creator", "full", 2)
	f.putPending(f.session("creator"))
	f.sentNoticeDue(f.ledgerEntry(string(f.session("creator").UID)))
	f.srv.Client = &conflictOnce{Client: f.c}
	f.srv.activateRaises(context.Background())
	joined := strings.Join(sink.msgs, "\n")
	if strings.Count(joined, "raise active") != 1 {
		t.Fatalf("log: %s", joined)
	}
	if got := f.ledgerEntry(string(f.session("creator").UID)); got.State != sessionraise.StateActive {
		t.Fatalf("ledger %s", got.State)
	}
}

type conflictOnce struct {
	client.Client
	fired bool
}

func (h *conflictOnce) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	sess, ok := obj.(*v1alpha1.AgentSession)
	if ok && !h.fired {
		rec, err := sessionraise.Parse(sess.Annotations[v1alpha1.AnnotationRaise])
		if err == nil && rec.State == sessionraise.StateActive {
			h.fired = true
			return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "agentsessions"}, sess.Name, errors.New("conflict"))
		}
	}
	return h.Client.Update(ctx, obj, opts...)
}

func TestDepthExemptLogsARecordItDoesNotHonour(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	sink := &lineSink{}
	f.srv.Log = logr.New(sink)
	f.sessionPod("creator", "full", 2)
	f.putPending(f.session("creator"))
	c := &caller{kind: kindSession, depth: 3, parent: "creator", session: f.session("creator")}
	if f.srv.depthExempt(context.Background(), c) {
		t.Fatal("a pending record was exempt")
	}
	if f.srv.depthExempt(context.Background(), c) {
		t.Fatal("a pending record was exempt")
	}
	n := 0
	for _, msg := range sink.msgs {
		if strings.Contains(msg, "does not grant start sessions") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("logged %d times: %v", n, sink.msgs)
	}
}

func TestAnnotationCountsLogsARecordItDoesNotHonour(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	sink := &lineSink{}
	f.srv.Log = logr.New(sink)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	body := f.raiseReq(string(target.UID), "", "please start sessions", sessionraise.WherePhone, true)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusAccepted {
		t.Fatalf("raise: %d %s", w.Code, w.Body.String())
	}
	f.rewriteAnnotation("target", func(rec *sessionraise.Record) {
		rec.SessionUID = "other-uid"
	})
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusOK {
		t.Fatalf("second retry: %d %s", w.Code, w.Body.String())
	}
	n := 0
	for _, msg := range sink.msgs {
		if strings.Contains(msg, "raise record ignored") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("logged %d times: %v", n, sink.msgs)
	}
}

func TestSameBodyOnAFinishedRaiseIsUsedWords(t *testing.T) {
	for _, state := range []string{sessionraise.StateTakenBack, sessionraise.StateUnannounced} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t)
			f.raiseReady(true)
			tok := f.workbench("bench")
			target := f.remoteTarget("target", 2)
			body := f.raiseReq(string(target.UID), "", "please start sessions", sessionraise.WherePhone, true)
			if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusAccepted {
				t.Fatalf("raise: %d %s", w.Code, w.Body.String())
			}
			f.setRaiseState(string(target.UID), state)
			wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body), http.StatusConflict,
				"session/bench already used these words for a raise. A new raise needs a new word from Tom.")
		})
	}
}

func TestStartSessionsRaiseRefusedWhenDispatchIsOff(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(false, "workbench")
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	body := f.raiseReq(string(target.UID), "", "please start sessions", sessionraise.WherePhone, true)
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body), http.StatusForbidden, raiseOff)
}

func TestVerifiedNonPendingAnnotationDoesNotActivate(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	f.sessionPod("creator", "full", 2)
	f.putPending(f.session("creator"))
	f.rewriteAnnotation("creator", func(rec *sessionraise.Record) {
		rec.State = sessionraise.StateActive
	})
	entry := f.ledgerEntry(string(f.session("creator").UID))
	f.sentNoticeDue(entry)
	f.srv.activateRaises(context.Background())
	if got := f.ledgerEntry(entry.UID); got.State != sessionraise.StatePending {
		t.Fatalf("a non-pending annotation activated the row: %s", got.State)
	}
}

func TestPendingAnnotationWithOtherWordsDoesNotActivate(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	f.sessionPod("creator", "full", 2)
	f.putPending(f.session("creator"))
	f.rewriteAnnotation("creator", func(rec *sessionraise.Record) {
		rec.Owner.Words = "other words please"
		rec.Owner.WordsHash = sessionraise.WordsHash(rec.Owner.Words)
	})
	entry := f.ledgerEntry(string(f.session("creator").UID))
	f.sentNoticeDue(entry)
	f.srv.activateRaises(context.Background())
	if got := f.ledgerEntry(entry.UID); got.State != sessionraise.StatePending {
		t.Fatalf("words that do not match the ledger activated the row: %s", got.State)
	}
}

func TestTakeBackReleasesAndSuspendsWhenTheKeyIsUnreadable(t *testing.T) {
	f := newFixture(t)
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
	grant := f.activeWorkbenchGrant("target")
	sec := &corev1.Secret{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionraise.ObjectNamespace, Name: sessionraise.OperatorSecretName}, sec); err != nil {
		t.Fatal(err)
	}
	sec.Data = map[string][]byte{}
	if err := f.c.Update(context.Background(), sec); err != nil {
		t.Fatal(err)
	}
	w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, apiv1.TakeBackRequest{ExpectUID: string(target.UID)})
	if w.Code != http.StatusOK {
		t.Fatalf("take-back: %d %s", w.Code, w.Body.String())
	}
	if f.ledgerEntry(string(target.UID)).State != sessionraise.StateTakenBack {
		t.Fatal("ledger was not taken back")
	}
	if f.session("target").Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		t.Fatal("the suspend did not run")
	}
	if !f.grant(grant).Spec.Release {
		t.Fatal("the grant was not released")
	}
}

func TestTakeBackReleasesAndSuspendsWhenTheAnnotationDoesNotCount(t *testing.T) {
	f := newFixture(t)
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
	grant := f.activeWorkbenchGrant("target")
	sess := f.session("target")
	sess.Annotations[v1alpha1.AnnotationRaise] = "not-a-record"
	if err := f.c.Update(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, apiv1.TakeBackRequest{ExpectUID: string(target.UID)})
	if w.Code != http.StatusOK {
		t.Fatalf("take-back: %d %s", w.Code, w.Body.String())
	}
	if f.session("target").Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		t.Fatal("the suspend did not run")
	}
	if !f.grant(grant).Spec.Release {
		t.Fatal("the grant was not released")
	}
}

func TestWordsThatExpandPastTheNoticeAreRefusedBeforeTheWordIsSpent(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	words := strings.TrimSpace(strings.Repeat("a.io ", 60))
	if sessionraise.WordsProblem(words) != "" {
		t.Fatalf("words rejected before the notice check: %s", sessionraise.WordsProblem(words))
	}
	body := f.raiseReq(string(target.UID), "", words, sessionraise.WherePhone, true)
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body), http.StatusUnprocessableEntity, raiseNoticeLong)
	led, err := f.srv.readLedger(context.Background(), f.srv.Raise.filled())
	if err != nil {
		t.Fatal(err)
	}
	if len(led.Words) != 0 || len(led.Sessions) != 0 {
		t.Fatalf("a refused raise spent a word: %+v", led)
	}
	wantSentence(t, f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body), http.StatusUnprocessableEntity, raiseNoticeLong)
	short := f.raiseReq(string(target.UID), "", "please start sessions", sessionraise.WherePhone, true)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, short); w.Code != http.StatusAccepted {
		t.Fatalf("short raise: %d %s", w.Code, w.Body.String())
	}
}

func TestViewShowsTheAgedReservation(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	f.sessionPod("creator", "full", 2)
	f.putPending(f.session("creator"))
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	entry := f.ledgerEntry(string(f.session("creator").UID))
	aged, err := sessionraise.SignNotice(sessionraise.Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: sessionraise.KindRaise, Attempt: 7,
		Outcome: sessionraise.OutcomeReserved, At: f.now.Add(-sessionraise.NoticeRetry).Format(time.RFC3339),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	f.putNotice(aged)
	w := f.do(http.MethodGet, apiv1.SessionPath("creator"), tokHuman, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("view: %d %s", w.Code, w.Body.String())
	}
	view := decode[apiv1.Session](t, w)
	if view.Raise == nil || view.Raise.NoticeAttempt != 7 {
		t.Fatalf("attempt %+v", view.Raise)
	}
}
