package keeper

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// OP796: all sends use newSummonFix's fake endpoint, synthetic credentials
// and fake clock. These tests start no binary and never sleep.
func startedSession(f *summonFix, name string, result v1alpha1.SummonRegistrationResult) *v1alpha1.AgentSession {
	s := f.session(name, v1alpha1.LaneEscalation)
	s.Spec.Mode = v1alpha1.ModeRemote
	at := metav1.NewTime(f.clock.Now())
	s.Status.Summon = &v1alpha1.SummonStatus{
		Pages:        []v1alpha1.SummonPage{{Event: v1alpha1.PageEscalationStarted, At: at}},
		Registration: &v1alpha1.SummonRegistrationStatus{Result: result, At: &at},
	}
	if result == v1alpha1.RegistrationUnavailable {
		s.Status.Summon.Registration.Reason = v1alpha1.RegistrationReasonOffline
	}
	return s
}

func TestSummonStartedWaitsForSettlement(t *testing.T) {
	for _, shape := range []string{"absent", "agent-seen", "no-time", "zero-time", "unknown-result", "no-event", "task", "upgrade"} {
		t.Run(shape, func(t *testing.T) {
			f := newSummonFix(t)
			s := startedSession(f, "esc-1", v1alpha1.RegistrationRegistered)
			s.Status.RemoteControl = &v1alpha1.RemoteControlStatus{State: "registered", Name: s.Name, URL: "https://claude.ai/code/session_verified"}
			switch shape {
			case "absent":
				s.Status.Summon.Registration = nil
			case "agent-seen":
				s.Status.Summon.Registration = &v1alpha1.SummonRegistrationStatus{AgentSeenAt: s.Status.Summon.Registration.At}
			case "no-time":
				s.Status.Summon.Registration.At = nil
			case "zero-time":
				s.Status.Summon.Registration.At = &metav1.Time{}
			case "unknown-result":
				s.Status.Summon.Registration.Result = "injected"
			case "no-event":
				s.Status.Summon.Pages = nil
			case "task":
				s.Spec.Mode = v1alpha1.ModeTask
			case "upgrade":
				s.Spec.Lane = v1alpha1.LaneUpgrade
			}
			f.add(s)
			f.tick()
			f.restart()
			f.tick()
			if f.push.count() != 0 {
				t.Fatal("an unsettled or ineligible escalation paged")
			}
			if _, seen := f.ledger().State.Events[summonKey(s, "escalation-started")]; seen {
				t.Fatal("unsettled event consumed its attempt")
			}
		})
	}
	f := newSummonFix(t)
	s := startedSession(f, "esc-1", v1alpha1.RegistrationRegistered)
	reg := s.Status.Summon.Registration
	s.Status.Summon.Registration = nil
	f.add(s)
	f.tick()
	s.Status.Summon.Registration = reg
	f.update(s)
	f.tick()
	f.restart()
	f.clock.Step(time.Minute)
	f.tick()
	if f.push.count() != 1 {
		t.Fatalf("settlement sent %d pages, want one", f.push.count())
	}
}

func TestSummonStartedText(t *testing.T) {
	const link = "https://claude.ai/code/session_verified"
	for _, tc := range []struct {
		name      string
		result    v1alpha1.SummonRegistrationResult
		rc        *v1alpha1.RemoteControlStatus
		reason    v1alpha1.SummonRegistrationReason
		head, url string
	}{
		{"registered", v1alpha1.RegistrationRegistered, &v1alpha1.RemoteControlStatus{State: "registered", URL: link}, "", "escalation esc-1 is running and registered with the Claude app.", link},
		{"gone", v1alpha1.RegistrationRegistered, &v1alpha1.RemoteControlStatus{State: "offline"}, "", "escalation esc-1 registered, but its link is gone (Remote Control offline).", ""},
		{"unverified", v1alpha1.RegistrationRegistered, &v1alpha1.RemoteControlStatus{State: "registered", URL: link + "?injected=1"}, "", "escalation esc-1 registered, but its link is gone (Remote Control registered).", ""},
		{"missing", v1alpha1.RegistrationRegistered, nil, "", "escalation esc-1 registered, but its link is gone (Remote Control unknown).", ""},
		{"hostile-state", v1alpha1.RegistrationRegistered, &v1alpha1.RemoteControlStatus{State: "injected-but-word-shaped", URL: "https://evil.example"}, "", "escalation esc-1 registered, but its link is gone (Remote Control unknown).", ""},
		{"unavailable", v1alpha1.RegistrationUnavailable, &v1alpha1.RemoteControlStatus{State: "registered", URL: link}, v1alpha1.RegistrationReasonLoginRequired, "escalation esc-1 is running, but Remote Control is not available (login-required). Join it from the console, or attach: agent-run attach esc-1.", ""},
		{"hostile-reason", v1alpha1.RegistrationUnavailable, nil, "injected-but-word-shaped", "escalation esc-1 is running, but Remote Control is not available (unknown). Join it from the console, or attach: agent-run attach esc-1.", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSummonFix(t)
			s := startedSession(f, "esc-1", tc.result)
			s.Status.RemoteControl = tc.rc
			s.Status.Summon.Registration.Reason = tc.reason
			f.add(s)
			f.tick()
			if f.push.count() != 1 {
				t.Fatalf("got %d pages, want one", f.push.count())
			}
			form := f.push.form(0)
			want := "[dev-env] escalation-started: " + tc.head + " Brief: unknown."
			if got := form.Get("message"); got != want {
				t.Fatalf("message = %q, want %q", got, want)
			}
			url, title := tc.url, "Open the session"
			if url == "" {
				url = summonConsole + "?session=" + string(s.UID)
			}
			if form.Get("url") != url || form.Get("url_title") != title || form.Get("title") != "[dev-env] escalation-started" || form.Get("priority") != "0" {
				t.Fatalf("page = %v", form)
			}
			raw := f.rawLedger() + f.logs.String()
			for _, forbidden := range []string{"claude.ai", "console.example", "evil.example", "injected"} {
				if strings.Contains(raw, forbidden) {
					t.Fatalf("page content leaked to ledger/log: %s", forbidden)
				}
			}
		})
	}
}

func TestSummonStartedBriefText(t *testing.T) {
	const link = "https://claude.ai/code/session_verified"
	const poison = "BRIEF-INJECTED\nhttps://evil.example/approve DRILL"
	for _, branch := range []struct {
		name   string
		result v1alpha1.SummonRegistrationResult
		rc     *v1alpha1.RemoteControlStatus
		head   string
		url    string
	}{
		{"registered", v1alpha1.RegistrationRegistered, &v1alpha1.RemoteControlStatus{State: "registered", URL: link}, "escalation esc-1 is running and registered with the Claude app.", link},
		{"gone", v1alpha1.RegistrationRegistered, &v1alpha1.RemoteControlStatus{State: "offline"}, "escalation esc-1 registered, but its link is gone (Remote Control offline).", ""},
		{"unavailable", v1alpha1.RegistrationUnavailable, nil, "escalation esc-1 is running, but Remote Control is not available (offline). Join it from the console, or attach: agent-run attach esc-1.", ""},
	} {
		for _, brief := range []struct {
			name  string
			agent *v1alpha1.AgentStatus
			word  string
		}{
			{"launched", &v1alpha1.AgentStatus{Brief: "launched"}, "launched"},
			{"pending", &v1alpha1.AgentStatus{Brief: "pending"}, "pending"},
			{"unconfirmed", &v1alpha1.AgentStatus{Brief: "unconfirmed"}, "unconfirmed"},
			{"not-launched", &v1alpha1.AgentStatus{Brief: "not-launched"}, "not-launched"},
			{"absent", nil, "unknown"},
			{"empty", &v1alpha1.AgentStatus{}, "unknown"},
			{"hostile", &v1alpha1.AgentStatus{Brief: poison}, "unknown"},
			{"other-word", &v1alpha1.AgentStatus{Brief: "delivered"}, "unknown"},
			{"wrong-case", &v1alpha1.AgentStatus{Brief: "Launched"}, "unknown"},
			{"whitespace", &v1alpha1.AgentStatus{Brief: "launched "}, "unknown"},
		} {
			t.Run(branch.name+"/"+brief.name, func(t *testing.T) {
				f := newSummonFix(t)
				s := startedSession(f, "esc-1", branch.result)
				s.Status.RemoteControl = branch.rc
				_, recordBefore := f.p.render(summonJob{session: s, event: "escalation-started"})
				s.Status.Agent = brief.agent
				n, recordAfter := f.p.render(summonJob{session: s, event: "escalation-started"})
				if !reflect.DeepEqual(recordBefore, recordAfter) {
					t.Fatal("brief claim changed the content-free page record")
				}
				f.add(s)
				f.tick()
				if f.push.count() != 1 {
					t.Fatalf("pages=%d, want one", f.push.count())
				}
				form := f.push.form(0)
				want := "[dev-env] escalation-started: " + branch.head + " Brief: " + brief.word + "."
				if got := form.Get("message"); got != want {
					t.Fatalf("message = %q, want %q", got, want)
				}
				url := branch.url
				if url == "" {
					url = summonConsole + "?session=" + string(s.UID)
				}
				if form.Get("title") != "[dev-env] escalation-started" || form.Get("url") != url || form.Get("url_title") != "Open the session" || form.Get("priority") != "0" {
					t.Fatal("brief claim changed the title, link, link label or priority")
				}
				// Inspect the rendered notice and record as well as the sent
				// fields and persisted ledger; JSON escaping cannot hide a leak.
				raw := fmt.Sprintf("%+v %+v", n, recordAfter) + form.Get("title") + form.Get("message") + form.Get("url") + form.Get("url_title") + f.rawLedger() + f.logs.String()
				for _, forbidden := range []string{"BRIEF-INJECTED", "evil.example", "DRILL"} {
					if strings.Contains(raw, forbidden) {
						t.Fatalf("hostile brief reached page, record or log: %s", forbidden)
					}
				}
			})
		}
	}
}

func TestSummonStartedBriefDrill(t *testing.T) {
	f := newSummonFix(t)
	s := startedSession(f, "esc-1", v1alpha1.RegistrationUnavailable)
	s.Spec.Caller = summonoutcome.DrillCaller
	s.Status.Agent = &v1alpha1.AgentStatus{Brief: "launched"}
	f.add(s)
	f.tick()
	if f.push.count() != 1 {
		t.Fatalf("pages=%d, want one", f.push.count())
	}
	form := f.push.form(0)
	want := "DRILL [dev-env] escalation-started: escalation esc-1 is running, but Remote Control is not available (offline). Join it from the console, or attach: agent-run attach esc-1. Brief: launched."
	if form.Get("title") != "DRILL [dev-env] escalation-started" || form.Get("message") != want {
		t.Fatalf("drill title=%q message=%q", form.Get("title"), form.Get("message"))
	}
	if form.Get("url") != summonConsole+"?session="+string(s.UID) || form.Get("url_title") != "Open the session" || form.Get("priority") != "0" {
		t.Fatal("drill link, link label or priority changed")
	}
	if got := f.ledger().Pages.Records[0].Title; got != "DRILL [dev-env] escalation-started" {
		t.Fatalf("record title=%q", got)
	}
}

func TestSummonStartedBriefChangeDoesNotResend(t *testing.T) {
	f := newSummonFix(t)
	s := startedSession(f, "esc-1", v1alpha1.RegistrationUnavailable)
	s.Status.Agent = &v1alpha1.AgentStatus{Brief: "launched"}
	f.add(s)
	f.tick()
	if f.push.count() != 1 || !strings.HasSuffix(f.push.form(0).Get("message"), " Brief: launched.") {
		t.Fatal("first page did not carry the claim recorded when built")
	}
	recordBefore := f.ledger().Pages.Records[0]
	s.Status.Agent.Brief = "unconfirmed"
	f.update(s)
	f.tick()
	f.restart()
	f.clock.Step(time.Hour + time.Minute)
	f.tick()
	if f.push.count() != 1 || !reflect.DeepEqual(recordBefore, f.ledger().Pages.Records[0]) {
		t.Fatal("changed brief claim resent the page or changed its record")
	}
}

func TestSummonStartedAttemptSurvivesRestart(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newSummonFix(t)
			f.push.status = status
			s := startedSession(f, "esc-1", v1alpha1.RegistrationUnavailable)
			f.add(s)
			f.push.before = func() {
				if got := f.ledger().State.Events[summonKey(s, "escalation-started")].State; got != string(pages.StateRecorded) {
					t.Errorf("send preceded its reservation: state=%q", got)
				}
			}
			f.tick()
			f.restart()
			f.clock.Step(time.Hour + time.Minute)
			f.tick()
			if f.push.count() != 1 {
				t.Fatalf("retried event: %d attempts", f.push.count())
			}
			want := map[int]pages.State{200: pages.StateSent, 400: pages.StateFailed, 500: pages.StateAmbiguous}[status]
			if got := f.ledger().State.Events[summonKey(s, "escalation-started")].State; got != string(want) {
				t.Fatalf("state=%q want %q", got, want)
			}
		})
	}
	// A crash after the dispatch but before the outcome write leaves recorded.
	f := newSummonFix(t)
	s := startedSession(f, "esc-1", v1alpha1.RegistrationRegistered)
	f.add(s)
	f.failUpdate = func(n int) bool { return n == 2 }
	f.tick()
	if got := f.ledger().State.Events[summonKey(s, "escalation-started")].State; got != string(pages.StateRecorded) {
		t.Fatalf("crash left %q", got)
	}
	f.failUpdate = nil
	f.restart()
	f.tick()
	if f.push.count() != 1 {
		t.Fatal("recorded attempt retried after crash")
	}
}

func TestSummonStartedUnconfirmedReservation(t *testing.T) {
	f := newSummonFix(t)
	s := startedSession(f, "esc-1", v1alpha1.RegistrationRegistered)
	f.add(s)
	f.commitThenFail = func(n int) bool { return n == 1 }
	f.tick()
	f.commitThenFail = nil
	f.restart()
	f.tick()
	if f.push.count() != 0 {
		t.Fatal("an unconfirmed reservation dispatched or retried")
	}
	if got := f.ledger().State.Events[summonKey(s, "escalation-started")].State; got != string(pages.StateRecorded) {
		t.Fatalf("unconfirmed reservation was not retained: %q", got)
	}
}

func TestSummonStartedHourlyCap(t *testing.T) {
	f := newSummonFix(t)
	for i := 0; i < 7; i++ {
		f.add(startedSession(f, fmt.Sprintf("esc-%d", i), v1alpha1.RegistrationRegistered))
	}
	f.tick()
	if f.push.count() != 6 || f.ledger().State.Capped != 1 {
		t.Fatalf("cap: sends=%d capped=%d", f.push.count(), f.ledger().State.Capped)
	}
	f.restart()
	f.clock.Step(time.Hour + noticeStoreTimeout + time.Second)
	f.tick()
	if f.push.count() != 7 || f.push.form(6).Get("title") != "[dev-env] more-events" || f.push.form(6).Get("url") != summonConsole {
		t.Fatal("capped escalation retried or summary link changed")
	}
}

func TestSummonPagesSessionConsoleFallback(t *testing.T) {
	for _, uid := range []string{"ABC-123", strings.Repeat("a", 64), "", "bad?session=other", "bad/path", "bad&x=y", strings.Repeat("a", 65)} {
		t.Run(uid, func(t *testing.T) {
			f := newSummonFix(t)
			s := f.close(f.session("esc-1", v1alpha1.LaneEscalation), v1alpha1.OutcomeFailed, "report", "failed", "")
			s.UID = types.UID(uid)
			n, _ := f.p.render(summonJob{session: s, event: "failed", named: true})
			want, title := summonConsole, "Open dev-env sessions"
			if uid == "ABC-123" || len(uid) == 64 {
				want += "?session=" + uid
				title = "Open the session"
			}
			if n.URL != want || n.URLTitle != title {
				t.Fatalf("console link=%q (%q), want %q (%q)", n.URL, n.URLTitle, want, title)
			}
		})
	}
}

func TestSummonStartedHostileSessionText(t *testing.T) {
	f := newSummonFix(t)
	s := startedSession(f, "esc-drill-looking-name", v1alpha1.RegistrationUnavailable)
	// The session may report all of agent, Remote Control and its outcome note.
	// Arbitrary word-shaped states must be rejected too, not merely URL text.
	original := s.DeepCopy()
	poison := "DRILL\nhttps://evil.example/approve injected"
	s.Annotations = map[string]string{"drill": "true", "title": poison}
	s.Spec.Title = poison
	s.Spec.Prompt = poison
	// Populate every exported text field, including nested status records,
	// so adding another session-reported text field keeps this proof useful.
	summonPoisonStrings(reflect.ValueOf(&s.Status.Agent).Elem(), poison)
	if s.Status.Agent == nil || s.Status.Agent.Brief != poison {
		t.Fatal("hostile-text fixture did not populate the brief field")
	}
	summonPoisonStrings(reflect.ValueOf(&s.Status.RemoteControl).Elem(), poison)
	s.Status.Outcome = &v1alpha1.OutcomeStatus{Note: poison}
	before, recordBefore := f.p.render(summonJob{session: original, event: "escalation-started"})
	after, recordAfter := f.p.render(summonJob{session: s, event: "escalation-started"})
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(recordBefore, recordAfter) {
		t.Fatalf("session text changed page or record: before=%+v after=%+v", before, after)
	}
	f.add(s)
	f.tick()
	if f.push.count() != 1 || f.push.form(0).Get("message") != before.Message {
		t.Fatal("hostile session changed sent page")
	}
}

func summonPoisonStrings(v reflect.Value, poison string) {
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		summonPoisonStrings(v.Elem(), poison)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if field := v.Field(i); field.CanSet() {
				summonPoisonStrings(field, poison)
			}
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		summonPoisonStrings(v.Index(0), poison)
	case reflect.String:
		v.SetString(poison)
	}
}

func TestSummonPagesDrillOnlyFromCaller(t *testing.T) {
	// The API's account-to-policy derivation, also exercised through the real
	// request path by TestSummonDrillCallerDerivedFromAccount. Admission tests
	// prove this account and the ordinary account below can have summon policies.
	derived := strings.ReplaceAll("dev-env-drill/summon-drill", "/", ".")
	callers := []struct {
		name, caller string
		drill        bool
	}{
		{"derived drill", derived, true},
		{"ordinary admitted caller", summonTestCaller, false},
		{"other namespace", "other.summon-drill", false},
		{"prefixed namespace", "x-" + summonoutcome.DrillCaller, false},
		{"suffix", summonoutcome.DrillCaller + "-extra", false},
		{"case", strings.Replace(summonoutcome.DrillCaller, "summon", "Summon", 1), false},
		{"old forbidden caller", "dev-env-system.summon-drill", false},
	}
	for _, event := range []string{"escalation-started", "escalation-held", "failed"} {
		for _, tc := range callers {
			t.Run(event+"/"+tc.name, func(t *testing.T) {
				f := newSummonFix(t)
				s := startedSession(f, summonoutcome.DrillCaller, v1alpha1.RegistrationUnavailable)
				s.Spec.Caller = tc.caller
				s.Annotations = map[string]string{"drill": "false"}
				s.Spec.Prompt = "DRILL"
				s.Spec.Title = summonoutcome.DrillCaller
				if event == "failed" {
					f.close(s, v1alpha1.OutcomeFailed, "report", "failed", "")
					s.Status.Summon.Pages = nil
				} else {
					s.Status.Summon.Pages[0].Event = v1alpha1.SummonPageEvent(event)
				}
				f.add(s)
				f.tick()
				if f.push.count() != 1 {
					t.Fatalf("pages=%d", f.push.count())
				}
				form := f.push.form(0)
				if strings.HasPrefix(form.Get("title"), "DRILL ") != tc.drill || strings.HasPrefix(form.Get("message"), "DRILL ") != tc.drill {
					t.Fatalf("DRILL followed name or session text: %v", form)
				}
			})
		}
	}
}
