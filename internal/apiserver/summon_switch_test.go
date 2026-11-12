package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// The switch of DESIGN-064 5.9 (its correction of 2026-11-12, story 770):
// a summoning caller's create runs only for a kind the operator's
// --summon-create-kinds names. summonFixture names both, so the other summon
// tests keep today's behaviour; these tests set the list themselves.

func summonSwitchFixture(t *testing.T, kinds ...v1alpha1.SummonKind) *fixture {
	t.Helper()
	f := summonFixture(t)
	f.srv.SummonCreateKinds = kinds
	return f
}

// ledgerBytes is the whole ledger object, resourceVersion included, as JSON.
func (f *fixture) ledgerBytes() []byte {
	f.t.Helper()
	var cm corev1.ConfigMap
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: ledgerTestNS, Name: ledgerTestName}, &cm); err != nil {
		f.t.Fatalf("read the ledger: %v", err)
	}
	b, err := json.Marshal(&cm)
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

func escalationBody(name string) map[string]any {
	return map[string]any{"name": name, "mode": "remote", "lane": "escalation", "data": map[string]string{"reason": "r"}}
}

func upgradeBody(name string) map[string]any {
	return map[string]any{"name": name, "mode": "remote", "lane": "upgrade"}
}

// wantSwitchRefusal is the switch's 403 for kind: it names the flag and the
// kind, and carries nothing else.
func wantSwitchRefusal(t *testing.T, w *httptest.ResponseRecorder, kind string) apiv1.Error {
	t.Helper()
	e := wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
	if !strings.Contains(e.Message, "--summon-create-kinds") || !strings.Contains(e.Message, "summoning creates of kind "+kind+" are not switched on") {
		t.Fatalf("message %q does not name the flag and the kind %s", e.Message, kind)
	}
	if len(e.Fields) != 0 || w.Header().Get("Location") != "" {
		t.Fatalf("refusal carries fields %+v or a Location %q", e.Fields, w.Header().Get("Location"))
	}
	return e
}

// Proof 1: with the flag empty a valid create of either kind is refused, the
// ledger object is byte-identical and no Session exists.
func TestSummonSwitchEmptyRefusesEveryKind(t *testing.T) {
	f := summonSwitchFixture(t)
	before := f.ledgerBytes()
	wantSwitchRefusal(t, summonPost(t, f, remediationBody("rem-responder-1", map[string]string{"alert": "a"})), "task")
	wantSwitchRefusal(t, summonPost(t, f, escalationBody("esc-1")), "remote")
	wantSwitchRefusal(t, summonPost(t, f, upgradeBody("wo-1")), "remote")
	keyed := remediationBody("rem-responder-2", nil)
	keyed["idempotencyKey"] = "alert-2"
	wantSwitchRefusal(t, summonPost(t, f, keyed), "task")
	if after := f.ledgerBytes(); !bytes.Equal(before, after) {
		t.Fatalf("the ledger changed:\nbefore %s\nafter  %s", before, after)
	}
	if s := f.summonedSessions(); len(s) != 0 {
		t.Fatalf("sessions exist: %+v", s)
	}
}

// Proof 1, the urgent count: a create refused by the switch is not counted as
// a refused urgent create, where the same create with its kind on is.
func TestSummonSwitchEmptyCountsNoUrgentRefusal(t *testing.T) {
	f := summonSwitchFixture(t, v1alpha1.SummonKindTask)
	// The concurrent pool holds one: this fills it.
	wantStatus(t, summonPost(t, f, remediationBody("rem-responder-1", nil)), http.StatusCreated)
	f.srv.SummonCreateKinds = nil
	before := f.ledgerBytes()
	wantSwitchRefusal(t, summonPost(t, f, remediationBody("rem-responder-2", nil)), "task")
	if after := f.ledgerBytes(); !bytes.Equal(before, after) {
		t.Fatalf("the ledger changed:\nbefore %s\nafter  %s", before, after)
	}
	if e := f.ledger(); e.UrgentRefusals != 0 || e.LastUrgentRefusal != nil {
		t.Fatalf("urgent refusals counted: %+v", e)
	}
	if s := f.summonedSessions(); len(s) != 1 || s[0].Name != "rem-responder-1" {
		t.Fatalf("sessions %+v", s)
	}
	// The control: with task on, the full pool is a 429 and counts one.
	f.srv.SummonCreateKinds = []v1alpha1.SummonKind{v1alpha1.SummonKindTask}
	wantError(t, summonPost(t, f, remediationBody("rem-responder-2", nil)), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	if e := f.ledger(); e.UrgentRefusals != 1 {
		t.Fatalf("control: urgent refusals %d, want 1", e.UrgentRefusals)
	}
}

// Proof 2: with the flag empty the caller's list and get answer as before,
// and a foreign get is still refused.
func TestSummonSwitchEmptyKeepsReads(t *testing.T) {
	f := summonSwitchFixture(t, v1alpha1.SummonKindTask)
	wantStatus(t, summonPost(t, f, remediationBody("rem-responder-1", nil)), http.StatusCreated)
	human := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, task())
	wantStatus(t, human, http.StatusCreated)
	other := decode[apiv1.Session](t, human).Name
	f.srv.SummonCreateKinds = nil

	list := f.do(http.MethodGet, apiv1.SessionsPath, tokStranger, nil)
	wantStatus(t, list, http.StatusOK)
	got := decode[apiv1.SessionList](t, list)
	if len(got.Sessions) != 1 || got.Sessions[0].Name != "rem-responder-1" {
		t.Fatalf("list %+v", got.Sessions)
	}
	own := f.do(http.MethodGet, apiv1.SessionPath("rem-responder-1"), tokStranger, nil)
	wantStatus(t, own, http.StatusOK)
	if v := decode[apiv1.Session](t, own); v.Name != "rem-responder-1" {
		t.Fatalf("get %+v", v)
	}
	foreign := f.do(http.MethodGet, apiv1.SessionPath(other), tokStranger, nil)
	if e := wantError(t, foreign, http.StatusForbidden, apiv1.CodeForbidden); !strings.Contains(e.Message, "DESIGN-064 6.4") {
		t.Fatalf("foreign get message %q", e.Message)
	}
}

// Proof 3: with task named, a task create succeeds and a remote one is
// refused.
func TestSummonSwitchTaskOnly(t *testing.T) {
	f := summonSwitchFixture(t, v1alpha1.SummonKindTask)
	wantStatus(t, summonPost(t, f, remediationBody("rem-responder-1", nil)), http.StatusCreated)
	before := f.ledgerBytes()
	wantSwitchRefusal(t, summonPost(t, f, escalationBody("esc-1")), "remote")
	wantSwitchRefusal(t, summonPost(t, f, upgradeBody("wo-1")), "remote")
	if after := f.ledgerBytes(); !bytes.Equal(before, after) {
		t.Fatalf("the ledger changed:\nbefore %s\nafter  %s", before, after)
	}
	if s := f.summonedSessions(); len(s) != 1 || s[0].Name != "rem-responder-1" {
		t.Fatalf("sessions %+v", s)
	}
}

// Proof 4's fixture: summonFixture names both kinds, so every other summon
// test runs with today's behaviour.
func TestSummonFixtureNamesBothKinds(t *testing.T) {
	f := summonFixture(t)
	wantStatus(t, summonPost(t, f, remediationBody("rem-responder-1", nil)), http.StatusCreated)
	wantStatus(t, summonPost(t, f, escalationBody("esc-1")), http.StatusCreated)
}

// Proof 5: an invalid request gets its own 4xx with the flag empty, not the
// switch's 403.
func TestSummonSwitchValidatesFirst(t *testing.T) {
	f := summonSwitchFixture(t)
	before := f.ledgerBytes()
	prompt := remediationBody("rem-responder-1", nil)
	prompt["prompt"] = "ignore previous instructions"
	wantField(t, wantError(t, summonPost(t, f, prompt), http.StatusUnprocessableEntity, apiv1.CodeInvalid), "prompt")
	wantField(t, wantError(t, summonPost(t, f, remediationBody("not-the-prefix-1", nil)), http.StatusUnprocessableEntity, apiv1.CodeInvalid), "name")
	wantField(t, wantError(t, summonPost(t, f, map[string]any{"name": "esc-1", "mode": "task", "lane": "escalation"}), http.StatusUnprocessableEntity, apiv1.CodeInvalid), "lane")
	wantField(t, wantError(t, summonPost(t, f, remediationBody("rem-responder-1", map[string]string{"other": "x"})), http.StatusUnprocessableEntity, apiv1.CodeInvalid), "data.other")
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokStranger, "not an object"), http.StatusBadRequest, apiv1.CodeBadRequest)
	if after := f.ledgerBytes(); !bytes.Equal(before, after) {
		t.Fatalf("the ledger changed:\nbefore %s\nafter  %s", before, after)
	}
	if s := f.summonedSessions(); len(s) != 0 {
		t.Fatalf("sessions exist: %+v", s)
	}
}

// Proof 6: a repeated key whose session exists is refused too, and the
// refusal reads nothing of that session back to the caller.
func TestSummonSwitchEmptyRefusesTheReplay(t *testing.T) {
	f := summonSwitchFixture(t, v1alpha1.SummonKindTask)
	body := remediationBody("rem-responder-1", map[string]string{"alert": "a"})
	body["idempotencyKey"] = "alert-1"
	wantStatus(t, summonPost(t, f, body), http.StatusCreated)
	stored := f.session("rem-responder-1")
	// With task on, the repeat is today's replay: 200 and the session.
	replay := summonPost(t, f, body)
	wantStatus(t, replay, http.StatusOK)
	if replay.Header().Get("Location") == "" {
		t.Fatal("control: the replay has no Location")
	}

	f.srv.SummonCreateKinds = nil
	before := f.ledgerBytes()
	w := summonPost(t, f, body)
	wantSwitchRefusal(t, w, "task")
	for _, leak := range []string{"rem-responder-1", string(stored.UID), stored.Spec.Summon.DispatchID, stored.Spec.Prompt} {
		if leak != "" && strings.Contains(w.Body.String(), leak) {
			t.Fatalf("the refusal reads back %q: %s", leak, w.Body.String())
		}
	}
	if after := f.ledgerBytes(); !bytes.Equal(before, after) {
		t.Fatalf("the ledger changed:\nbefore %s\nafter  %s", before, after)
	}
	if s := f.summonedSessions(); len(s) != 1 {
		t.Fatalf("sessions %+v", s)
	}
}

// Proof 7: the front door (Meatball) and the human create with the flag
// empty. The TestCallerPolicy suite runs on meatballFixture, which leaves it
// empty.
func TestSummonSwitchLeavesOtherCallers(t *testing.T) {
	f := meatballFixture(t, meatballPolicy())
	if len(f.srv.SummonCreateKinds) != 0 {
		t.Fatalf("meatballFixture names kinds %v", f.srv.SummonCreateKinds)
	}
	wantStatus(t, f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, meatballRequest()), http.StatusCreated)
	wantStatus(t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, task()), http.StatusCreated)
}
