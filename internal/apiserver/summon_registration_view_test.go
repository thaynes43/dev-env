package apiserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func registrationSession() *v1alpha1.AgentSession {
	return &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "esc-registration", Namespace: sessionNS, UID: "11111111-1111-1111-1111-111111111111", CreationTimestamp: metav1.NewTime(time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC))},
		Spec:       v1alpha1.AgentSessionSpec{Repo: "dev-env", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeRemote, Model: "claude-opus-5-5", Prompt: "test escalation brief"},
		Status:     v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseRunning, RemoteControl: &v1alpha1.RemoteControlStatus{State: "starting"}},
	}
}

func TestSessionRegistrationDetail(t *testing.T) {
	seen := metav1.NewTime(time.Date(2026, 11, 14, 1, 1, 0, 0, time.FixedZone("test", 3600)))
	at := metav1.NewTime(seen.Add(5 * time.Minute))
	for _, tc := range []struct {
		name   string
		record v1alpha1.SummonRegistrationStatus
		want   string
	}{
		{"registered before agent seen", v1alpha1.SummonRegistrationStatus{Result: v1alpha1.RegistrationRegistered, At: &at}, `{"result":"registered","at":"2026-11-14T00:06:00Z"}`},
		{"registered", v1alpha1.SummonRegistrationStatus{AgentSeenAt: &seen, Result: v1alpha1.RegistrationRegistered, At: &at}, `{"agentSeenAt":"2026-11-14T00:01:00Z","result":"registered","at":"2026-11-14T00:06:00Z"}`},
		{"unavailable", v1alpha1.SummonRegistrationStatus{AgentSeenAt: &seen, Result: v1alpha1.RegistrationUnavailable, Reason: v1alpha1.RegistrationReasonLoginRequired, At: &at}, `{"agentSeenAt":"2026-11-14T00:01:00Z","result":"unavailable","reason":"login-required","at":"2026-11-14T00:06:00Z"}`},
		{"seen not settled", v1alpha1.SummonRegistrationStatus{AgentSeenAt: &seen}, `{"agentSeenAt":"2026-11-14T00:01:00Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := registrationSession()
			s.Spec.Caller, s.Spec.Lane = "test-caller", v1alpha1.LaneEscalation
			s.Status.Summon = &v1alpha1.SummonStatus{Registration: &tc.record}
			// The current heartbeat may disagree with the stored settlement.
			// Neither its state nor its link belongs in the new member.
			s.Status.RemoteControl.State = "registered"
			s.Status.RemoteControl.URL = "https://" + "claude.ai/code/session_" + "registration_fixture"
			f := newFixture(t, s)
			w := f.do(http.MethodGet, apiv1.SessionPath(s.Name), tokHuman, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("detail status %d", w.Code)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
				t.Fatal(err)
			}
			if got := string(raw["summonRegistration"]); got != tc.want {
				t.Fatalf("registration = %s, want %s", got, tc.want)
			}
			got := decode[apiv1.Session](t, w)
			if got.RemoteControl == nil || got.RemoteControl.URL != s.Status.RemoteControl.URL || got.RemoteControl.State != "registered" {
				t.Fatal("detail changed the existing Remote Control view")
			}
		})
	}
}

func TestSessionWithoutRegistrationGolden(t *testing.T) {
	for _, kind := range []string{"ordinary", "summoned"} {
		t.Run(kind, func(t *testing.T) {
			s := registrationSession()
			if kind == "summoned" {
				s.Spec.Caller, s.Spec.Lane = "test-caller", v1alpha1.LaneEscalation
				s.Status.Summon = &v1alpha1.SummonStatus{}
			}
			for _, detail := range []bool{false, true} {
				name := "list"
				if detail {
					name = "detail"
				}
				raw, err := json.Marshal(view(s, detail))
				if err != nil {
					t.Fatal(err)
				}
				if string(raw) != registrationViewGoldens[kind+"/"+name] {
					t.Fatalf("%s view changed without a registration record", name)
				}
			}
		})
	}
}

func TestSessionRegistrationIsDetailOnly(t *testing.T) {
	s := registrationSession()
	s.Spec.Caller, s.Spec.Lane = "test-caller", v1alpha1.LaneEscalation
	f := newFixture(t, s)
	paths := []string{apiv1.SessionsPath, apiv1.SessionsPath + "?caller=test-caller&lane=escalation", apiv1.FleetPath}
	before := make(map[string][]byte)
	for _, path := range paths {
		w := f.do(http.MethodGet, path, tokHuman, nil)
		if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(s.Name)) {
			t.Fatalf("list fixture missing at %s", path)
		}
		before[path] = append([]byte(nil), w.Body.Bytes()...)
	}
	portalBefore, err := json.Marshal(portalRegistrationList(s, f.now))
	if err != nil {
		t.Fatal(err)
	}
	s = f.session(s.Name)
	s.Status.Summon = &v1alpha1.SummonStatus{Registration: &v1alpha1.SummonRegistrationStatus{AgentSeenAt: &s.CreationTimestamp}}
	f.mustStatus(s)
	for _, path := range paths {
		w := f.do(http.MethodGet, path, tokHuman, nil)
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), before[path]) || bytes.Contains(w.Body.Bytes(), []byte(`"summonRegistration"`)) {
			t.Fatalf("registration changed list %s", path)
		}
	}
	portalAfter, err := json.Marshal(portalRegistrationList(s, f.now))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(portalBefore, portalAfter) || bytes.Contains(portalAfter, []byte(`"summonRegistration"`)) {
		t.Fatal("registration changed the portal list projection")
	}
	if got := view(s, true); got.SummonRegistration == nil {
		t.Fatal("the detail fixture must carry a registration record")
	}
	// A stray status record cannot add fields to an ordinary or task session.
	s.Spec.Caller = ""
	if view(s, true).SummonRegistration != nil {
		t.Fatal("ordinary session gained a registration member")
	}
	s.Spec.Caller, s.Spec.Mode = "test-caller", v1alpha1.ModeTask
	if view(s, true).SummonRegistration != nil {
		t.Fatal("task session gained a registration member")
	}
}

func portalRegistrationList(s *v1alpha1.AgentSession, now time.Time) apiv1.PortalSessionPage {
	summary := portalSummary(s, now)
	summary.Activity = portalActivity(s, now)
	return apiv1.PortalSessionPage{Sessions: []apiv1.PortalSessionSummary{summary}}
}

// Captured before the change at main 83f8a5bbca438eda74945bb3699fbbe322e7feec.
var registrationViewGoldens = map[string]string{
	"ordinary/detail": `{"uid":"11111111-1111-1111-1111-111111111111","name":"esc-registration","repo":"dev-env","agent":"claude","mode":"remote","model":"claude-opus-5-5","prompt":"test escalation brief","createdAt":"2026-11-14T00:00:00Z","phase":"Running","remoteControl":{"state":"starting"}}`,
	"ordinary/list":   `{"uid":"11111111-1111-1111-1111-111111111111","name":"esc-registration","repo":"dev-env","agent":"claude","mode":"remote","model":"claude-opus-5-5","createdAt":"2026-11-14T00:00:00Z","phase":"Running","remoteControl":{"state":"starting"}}`,
	"summoned/detail": `{"uid":"11111111-1111-1111-1111-111111111111","name":"esc-registration","repo":"dev-env","agent":"claude","mode":"remote","model":"claude-opus-5-5","caller":"test-caller","lane":"escalation","prompt":"test escalation brief","createdAt":"2026-11-14T00:00:00Z","phase":"Running","remoteControl":{"state":"starting"}}`,
	"summoned/list":   `{"uid":"11111111-1111-1111-1111-111111111111","name":"esc-registration","repo":"dev-env","agent":"claude","mode":"remote","model":"claude-opus-5-5","caller":"test-caller","lane":"escalation","createdAt":"2026-11-14T00:00:00Z","phase":"Running","remoteControl":{"state":"starting"}}`,
}
