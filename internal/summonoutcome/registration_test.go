package summonoutcome

// Proofs 1 to 8 of story 794 (DESIGN-064 6.10; coordinator rulings O3 and
// O13 on item 745 comments 7680 and 7685): the one pure function that
// settles a remote escalation's registration, on a fixed clock.

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const regName = "esc-responder-1"

var (
	regAdmitted = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	regLink     = "https://claude.ai/code/session_01AbCdEf"
)

func regTime(t time.Time) *metav1.Time {
	m := metav1.NewTime(t)
	return &m
}

// regInputs is one pass's reading: an admitted session, a heartbeat at
// beatAt with agent word, and a Remote Control record (nil: none).
type regInputs struct {
	rc      *v1alpha1.RemoteControlStatus
	agent   *v1alpha1.AgentStatus
	summon  *v1alpha1.SummonStatus
	now     time.Time
	session string
}

func regRead(now time.Time, agent string, rc *v1alpha1.RemoteControlStatus) regInputs {
	return regInputs{
		rc:      rc,
		agent:   &v1alpha1.AgentStatus{Status: agent, LastHeartbeat: regTime(now)},
		summon:  &v1alpha1.SummonStatus{AdmittedAt: regTime(regAdmitted)},
		now:     now,
		session: regName,
	}
}

func (in regInputs) seenAt(at time.Time) regInputs {
	in.summon.Registration = &v1alpha1.SummonRegistrationStatus{AgentSeenAt: regTime(at)}
	return in
}

func (in regInputs) decide() RegistrationDecision {
	return Registration(in.session, in.rc, in.agent, in.summon, in.now)
}

func registeredRC() *v1alpha1.RemoteControlStatus {
	return &v1alpha1.RemoteControlStatus{Name: regName, State: protocol.RemoteRegistered, URL: regLink, SessionID: "session_01AbCdEf"}
}

func rcState(state string) *v1alpha1.RemoteControlStatus {
	return &v1alpha1.RemoteControlStatus{Name: regName, State: state}
}

var (
	nothingYet = RegistrationDecision{}
	agentSeen  = RegistrationDecision{Step: RegistrationAgentSeen}
	registered = RegistrationDecision{Step: RegistrationSettled, Result: v1alpha1.RegistrationRegistered}
)

func unavailable(reason v1alpha1.SummonRegistrationReason) RegistrationDecision {
	return RegistrationDecision{Step: RegistrationSettled, Result: v1alpha1.RegistrationUnavailable, Reason: reason}
}

// The words this package reads are agentd's (internal/agentd/protocol).
func TestRegistrationWordsAreAgentds(t *testing.T) {
	for got, want := range map[string]string{
		agentBusy: protocol.AgentBusy, agentIdle: protocol.AgentIdle, agentWaiting: protocol.AgentWaiting,
		remoteRegistered: protocol.RemoteRegistered, remoteStarting: protocol.RemoteStarting,
		remoteOffline: protocol.RemoteOffline, remoteLoginRequired: protocol.RemoteLoginRequired,
	} {
		if got != want {
			t.Errorf("word %q, agentd's %q", got, want)
		}
	}
}

// Proof 1, the pattern (ruling O13): a 1 and a 120 character id are a
// verified link; 0, 121, another host, http, a trailing slash, a query and
// a fragment are not.
func TestVerifiedRemoteControlLink(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://claude.ai/code/session_a", true},
		{"https://claude.ai/code/session_" + strings.Repeat("A", 120), true},
		{"https://claude.ai/code/session_Ab-_09", true},
		{"https://claude.ai/code/session_", false},
		{"https://claude.ai/code/session_" + strings.Repeat("A", 121), false},
		{"https://claude.example/code/session_a", false},
		{"https://claudeXai/code/session_a", false},
		{"https://evil.claude.ai/code/session_a", false},
		{"http://claude.ai/code/session_a", false},
		{"https://claude.ai/code/session_a/", false},
		{"https://claude.ai/code/session_a?x=1", false},
		{"https://claude.ai/code/session_a#frag", false},
		{"https://claude.ai/code/session_a\n", false},
		{"https://claude.ai/code/other_a", false},
		{"", false},
	} {
		if got := VerifiedRemoteControlLink(tc.url); got != tc.want {
			t.Errorf("VerifiedRemoteControlLink(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
	if RemoteControlLinkPattern != `^https://claude\.ai/code/session_[A-Za-z0-9_-]{1,120}$` {
		t.Fatalf("pattern %q is not the shared one (ruling O13)", RemoteControlLinkPattern)
	}
}

// Proof 1: registered only with all three of the state, a matching URL and
// the session's name. Each one missing alone is not registered; once the
// bound has passed, it says which one.
func TestRegistrationNeedsStateLinkAndName(t *testing.T) {
	now := regAdmitted.Add(time.Minute)
	if got := regRead(now, protocol.AgentBusy, registeredRC()).decide(); got != registered {
		t.Fatalf("all three: %+v, want registered", got)
	}
	if got := regRead(now, protocol.AgentPending, registeredRC()).decide(); got != registered {
		t.Fatalf("registered before the agent was seen: %+v, want registered", got)
	}
	late := regAdmitted.Add(20 * time.Minute)
	for _, tc := range []struct {
		name   string
		edit   func(*v1alpha1.RemoteControlStatus)
		reason v1alpha1.SummonRegistrationReason
	}{
		{"state", func(rc *v1alpha1.RemoteControlStatus) { rc.State = protocol.RemoteStarting }, v1alpha1.RegistrationReasonStarting},
		{"url", func(rc *v1alpha1.RemoteControlStatus) { rc.URL = "" }, v1alpha1.RegistrationReasonLinkUnverified},
		{"url-pattern", func(rc *v1alpha1.RemoteControlStatus) { rc.URL = "https://claude.ai/code/session_a/" }, v1alpha1.RegistrationReasonLinkUnverified},
		{"name", func(rc *v1alpha1.RemoteControlStatus) { rc.Name = "esc-responder-2" }, v1alpha1.RegistrationReasonNameMismatch},
		{"name-empty", func(rc *v1alpha1.RemoteControlStatus) { rc.Name = "" }, v1alpha1.RegistrationReasonNameMismatch},
		{"name-and-url", func(rc *v1alpha1.RemoteControlStatus) { rc.Name, rc.URL = "esc-responder-2", "" }, v1alpha1.RegistrationReasonNameMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := registeredRC()
			tc.edit(rc)
			if got := regRead(now, protocol.AgentPending, rc).decide(); got != nothingYet {
				t.Fatalf("inside the bound: %+v, want nothing yet", got)
			}
			if got := regRead(late, protocol.AgentPending, rc).decide(); got != unavailable(tc.reason) {
				t.Fatalf("past the bound: %+v, want unavailable %s", got, tc.reason)
			}
		})
	}
}

// Proof 2: no heartbeat, or one 3 minutes and 1 second old, is nothing yet,
// whatever else says registered or past a bound. Exactly 3 minutes old is
// read.
func TestRegistrationReadsOnlyAFreshHeartbeat(t *testing.T) {
	now := regAdmitted.Add(time.Hour) // past both bounds
	for _, tc := range []struct {
		name  string
		agent *v1alpha1.AgentStatus
		want  RegistrationDecision
	}{
		{"no agent", nil, nothingYet},
		{"no heartbeat", &v1alpha1.AgentStatus{Status: protocol.AgentBusy}, nothingYet},
		{"3m1s old", &v1alpha1.AgentStatus{Status: protocol.AgentBusy, LastHeartbeat: regTime(now.Add(-3*time.Minute - time.Second))}, nothingYet},
		{"3m old", &v1alpha1.AgentStatus{Status: protocol.AgentBusy, LastHeartbeat: regTime(now.Add(-3 * time.Minute))}, registered},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The case's heartbeat, with the reading's own agent word.
			beat := func(in regInputs) regInputs {
				if tc.agent == nil {
					in.agent = nil
					return in
				}
				a := tc.agent.DeepCopy()
				a.Status = in.agent.Status
				in.agent = a
				return in
			}
			for _, in := range []regInputs{regRead(now, protocol.AgentBusy, registeredRC()), regRead(now, protocol.AgentBusy, registeredRC()).seenAt(regAdmitted)} {
				if got := beat(in).decide(); got != tc.want {
					t.Fatalf("registered: %+v, want %+v", got, tc.want)
				}
			}
			for _, in := range []regInputs{regRead(now, protocol.AgentBusy, rcState(protocol.RemoteOffline)).seenAt(regAdmitted),
				regRead(now, protocol.AgentPending, rcState(protocol.RemoteLoginRequired))} {
				in = beat(in)
				want := tc.want
				if want == registered {
					want = unavailable(v1alpha1.SummonRegistrationReason(in.rc.State))
				}
				if got := in.decide(); got != want {
					t.Fatalf("past a bound with %s: %+v, want %+v", in.rc.State, got, want)
				}
			}
		})
	}
}

// Proof 3: the agent is seen for busy, idle and waiting; not for pending,
// login-required, exited, failed, interrupted or an empty or unknown word.
func TestRegistrationAgentSeenWords(t *testing.T) {
	now := regAdmitted.Add(time.Minute)
	for _, word := range []string{protocol.AgentBusy, protocol.AgentIdle, protocol.AgentWaiting} {
		if got := regRead(now, word, rcState(protocol.RemoteStarting)).decide(); got != agentSeen {
			t.Errorf("%q: %+v, want agent seen", word, got)
		}
	}
	for _, word := range []string{protocol.AgentPending, protocol.AgentLoginRequired, protocol.AgentExited, protocol.AgentFailed, protocol.AgentInterrupted, "", "Busy", "running"} {
		if got := regRead(now, word, rcState(protocol.RemoteStarting)).decide(); got != nothingYet {
			t.Errorf("%q: %+v, want nothing yet", word, got)
		}
	}
	// Seen once is seen: a second reading changes nothing inside the bound.
	if got := regRead(now, protocol.AgentBusy, rcState(protocol.RemoteStarting)).seenAt(regAdmitted).decide(); got != nothingYet {
		t.Errorf("already seen: %+v, want nothing yet", got)
	}
}

// Proof 4: after the agent was seen, one second before agentSeenAt plus
// 5 minutes is nothing yet, and at it the registration is unavailable, with
// the reason read at that pass.
func TestRegistrationUnavailableFiveMinutesAfterSeen(t *testing.T) {
	seen := regAdmitted.Add(2 * time.Minute)
	bound := seen.Add(5 * time.Minute)
	if got := regRead(bound.Add(-time.Second), protocol.AgentBusy, rcState(protocol.RemoteStarting)).seenAt(seen).decide(); got != nothingYet {
		t.Fatalf("a second before the bound: %+v, want nothing yet", got)
	}
	rcLink := registeredRC()
	rcLink.URL = "https://claude.ai/code/session_a?b"
	rcName := registeredRC()
	rcName.Name = "someone-else"
	for _, tc := range []struct {
		rc   *v1alpha1.RemoteControlStatus
		want v1alpha1.SummonRegistrationReason
	}{
		{rcState(protocol.RemoteStarting), v1alpha1.RegistrationReasonStarting},
		{rcState(protocol.RemoteOffline), v1alpha1.RegistrationReasonOffline},
		{rcState(protocol.RemoteLoginRequired), v1alpha1.RegistrationReasonLoginRequired},
		{rcLink, v1alpha1.RegistrationReasonLinkUnverified},
		{rcName, v1alpha1.RegistrationReasonNameMismatch},
		{nil, v1alpha1.RegistrationReasonNotReported},
		{rcState(""), v1alpha1.RegistrationReasonNotReported},
		{rcState("bridged"), v1alpha1.RegistrationReasonNotReported},
		{rcState("Registered"), v1alpha1.RegistrationReasonNotReported},
	} {
		got := regRead(bound, protocol.AgentIdle, tc.rc).seenAt(seen).decide()
		if got != unavailable(tc.want) {
			t.Errorf("rc %+v: %+v, want unavailable %s", tc.rc, got, tc.want)
		}
	}
	// The bound holds whatever the agent says now, with a fresh heartbeat.
	if got := regRead(bound, protocol.AgentInterrupted, rcState(protocol.RemoteOffline)).seenAt(seen).decide(); got != unavailable(v1alpha1.RegistrationReasonOffline) {
		t.Fatalf("seen, then interrupted: %+v", got)
	}
}

// Proof 5: never seen, one second before admittedAt plus 10 minutes is
// nothing yet with login-required or starting; at it, unavailable with
// that word.
func TestRegistrationUnavailableTenMinutesAfterAdmission(t *testing.T) {
	bound := regAdmitted.Add(10 * time.Minute)
	for _, state := range []string{protocol.RemoteLoginRequired, protocol.RemoteStarting} {
		agent := protocol.AgentPending
		if state == protocol.RemoteLoginRequired {
			agent = protocol.AgentLoginRequired
		}
		if got := regRead(bound.Add(-time.Second), agent, rcState(state)).decide(); got != nothingYet {
			t.Errorf("%s a second before the bound: %+v, want nothing yet", state, got)
		}
		if got := regRead(bound, agent, rcState(state)).decide(); got != unavailable(v1alpha1.SummonRegistrationReason(state)) {
			t.Errorf("%s at the bound: %+v, want unavailable %s", state, got, state)
		}
	}
}

// Proof 6: at or after the admission bound, never seen, and the agent is
// busy at this pass: agent seen, not unavailable.
func TestRegistrationSeenAtTheBoundIsSeen(t *testing.T) {
	for _, at := range []time.Duration{10 * time.Minute, time.Hour} {
		if got := regRead(regAdmitted.Add(at), protocol.AgentBusy, rcState(protocol.RemoteOffline)).decide(); got != agentSeen {
			t.Errorf("busy at admittedAt+%s: %+v, want agent seen", at, got)
		}
	}
}

// Proof 7: a transient offline inside the bound, then registered: registered.
func TestRegistrationTransientOfflineThenRegistered(t *testing.T) {
	seen := regAdmitted.Add(time.Minute)
	for _, at := range []time.Duration{2 * time.Minute, 3 * time.Minute, 5*time.Minute + 59*time.Second} {
		if got := regRead(regAdmitted.Add(at), protocol.AgentBusy, rcState(protocol.RemoteOffline)).seenAt(seen).decide(); got != nothingYet {
			t.Fatalf("offline at +%s: %+v, want nothing yet", at, got)
		}
	}
	if got := regRead(regAdmitted.Add(6*time.Minute-time.Second), protocol.AgentBusy, registeredRC()).seenAt(seen).decide(); got != registered {
		t.Fatalf("then registered: %+v", got)
	}
	// A pending login inside the admission bound, then registered.
	if got := regRead(regAdmitted.Add(9*time.Minute), protocol.AgentLoginRequired, rcState(protocol.RemoteLoginRequired)).decide(); got != nothingYet {
		t.Fatalf("pending login: %+v", got)
	}
	if got := regRead(regAdmitted.Add(9*time.Minute+30*time.Second), protocol.AgentBusy, registeredRC()).decide(); got != registered {
		t.Fatalf("login, then registered: %+v", got)
	}
}

// Proof 8: a record with a result is nothing, whatever the inputs.
func TestRegistrationSettledIsFinal(t *testing.T) {
	at := regTime(regAdmitted.Add(5 * time.Minute))
	for _, rec := range []*v1alpha1.SummonRegistrationStatus{
		{Result: v1alpha1.RegistrationRegistered, At: at},
		{Result: v1alpha1.RegistrationUnavailable, Reason: v1alpha1.RegistrationReasonOffline, At: at, AgentSeenAt: regTime(regAdmitted)},
	} {
		for _, in := range []regInputs{
			regRead(regAdmitted.Add(time.Hour), protocol.AgentBusy, registeredRC()),
			regRead(regAdmitted.Add(time.Hour), protocol.AgentIdle, rcState(protocol.RemoteOffline)),
			regRead(regAdmitted.Add(time.Minute), protocol.AgentPending, nil),
		} {
			in.summon.Registration = rec.DeepCopy()
			if got := in.decide(); got != nothingYet {
				t.Errorf("settled %s, then %+v: %+v, want nothing", rec.Result, in.rc, got)
			}
		}
	}
	// Not admitted, or no summon record: nothing.
	in := regRead(regAdmitted.Add(time.Hour), protocol.AgentBusy, registeredRC())
	in.summon = &v1alpha1.SummonStatus{}
	if got := in.decide(); got != nothingYet {
		t.Errorf("not admitted: %+v", got)
	}
	in.summon = nil
	if got := in.decide(); got != nothingYet {
		t.Errorf("no summon record: %+v", got)
	}
}

// beatAt is the reading with its heartbeat taken at at, the agent word kept.
func (in regInputs) beatAt(at time.Time) regInputs {
	a := in.agent.DeepCopy()
	a.LastHeartbeat = regTime(at)
	in.agent = a
	return in
}

// Story 794 fix round 1, ruling 1 (coordinator rulings in PR 1132 comment
// 6856616130 and item 745 comment 7855; not a ruling by Tom): unavailable
// needs a heartbeat taken at or after its bound (agentSeenAt plus 5 minutes,
// or admittedAt plus 10 minutes when the agent was never seen), as well as
// now at or after that bound. An older reading, fresh or not, is nothing yet.
// registered keeps the 3-minute window.
func TestRegistrationUnavailableNeedsAReadingFromItsBound(t *testing.T) {
	seen := regAdmitted.Add(2 * time.Minute)
	seenBound := seen.Add(5 * time.Minute)
	for _, tc := range []struct {
		name  string
		read  func(now time.Time) regInputs
		bound time.Time
		want  RegistrationDecision
	}{
		{"seen, starting", func(now time.Time) regInputs {
			return regRead(now, protocol.AgentBusy, rcState(protocol.RemoteStarting)).seenAt(seen)
		}, seenBound, unavailable(v1alpha1.RegistrationReasonStarting)},
		{"never seen, login-required", func(now time.Time) regInputs {
			return regRead(now, protocol.AgentLoginRequired, rcState(protocol.RemoteLoginRequired))
		}, regAdmitted.Add(10 * time.Minute), unavailable(v1alpha1.RegistrationReasonLoginRequired)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.read(tc.bound).beatAt(tc.bound.Add(-150 * time.Second)).decide(); got != nothingYet {
				t.Errorf("now at the bound, the reading 2m30s before it: %+v, want nothing yet", got)
			}
			if got := tc.read(tc.bound.Add(time.Minute)).beatAt(tc.bound.Add(-time.Second)).decide(); got != nothingYet {
				t.Errorf("now past the bound, the reading a second before it: %+v, want nothing yet", got)
			}
			if got := tc.read(tc.bound).beatAt(tc.bound).decide(); got != tc.want {
				t.Errorf("now and the reading at the bound: %+v, want %+v", got, tc.want)
			}
			if got := tc.read(tc.bound.Add(2 * time.Minute)).beatAt(tc.bound.Add(time.Minute)).decide(); got != tc.want {
				t.Errorf("a reading after the bound: %+v, want %+v", got, tc.want)
			}
			if got := tc.read(tc.bound.Add(-10 * time.Second)).beatAt(tc.bound).decide(); got != nothingYet {
				t.Errorf("a reading at the bound, now 10 seconds before it: %+v, want nothing yet", got)
			}
		})
	}
	// registered reads any heartbeat at most 3 minutes old, from before a
	// bound or not.
	for _, in := range []regInputs{
		regRead(regAdmitted.Add(4*time.Minute), protocol.AgentBusy, registeredRC()).beatAt(regAdmitted.Add(time.Minute)),
		regRead(seenBound.Add(time.Minute), protocol.AgentBusy, registeredRC()).seenAt(seen).beatAt(seenBound.Add(-2 * time.Minute)),
	} {
		if got := in.decide(); got != registered {
			t.Errorf("registered, a reading 3 minutes old, at %s: %+v, want registered", in.now, got)
		}
	}
}

// Story 794 fix round 1, ruling 3: a heartbeat stamped more than 30 seconds
// after now is no reading, for every answer; 30 seconds ahead is read.
func TestRegistrationHeartbeatAheadIsNotRead(t *testing.T) {
	if RegistrationHeartbeatAhead != 30*time.Second {
		t.Fatalf("RegistrationHeartbeatAhead is %s, want 30s (ruling 3)", RegistrationHeartbeatAhead)
	}
	now := regAdmitted.Add(time.Minute)
	for _, tc := range []struct {
		name string
		in   regInputs
		want RegistrationDecision
	}{
		{"registered", regRead(now, protocol.AgentBusy, registeredRC()), registered},
		{"agent seen", regRead(now, protocol.AgentBusy, rcState(protocol.RemoteStarting)), agentSeen},
	} {
		if got := tc.in.beatAt(now.Add(31 * time.Second)).decide(); got != nothingYet {
			t.Errorf("%s, now+31s: %+v, want nothing yet", tc.name, got)
		}
		if got := tc.in.beatAt(now.Add(30 * time.Second)).decide(); got != tc.want {
			t.Errorf("%s, now+30s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
	late := regAdmitted.Add(time.Hour) // past both bounds
	for _, in := range []regInputs{
		regRead(late, protocol.AgentBusy, rcState(protocol.RemoteOffline)).seenAt(regAdmitted),
		regRead(late, protocol.AgentPending, rcState(protocol.RemoteLoginRequired)),
		regRead(late, protocol.AgentBusy, registeredRC()),
	} {
		if got := in.beatAt(late.Add(4 * time.Minute)).decide(); got != nothingYet {
			t.Errorf("%s past a bound, now+4m: %+v, want nothing yet", in.rc.State, got)
		}
		want := registered
		if in.rc.State != protocol.RemoteRegistered {
			want = unavailable(v1alpha1.SummonRegistrationReason(in.rc.State))
		}
		if got := in.beatAt(late.Add(30 * time.Second)).decide(); got != want {
			t.Errorf("%s past a bound, now+30s: %+v, want %+v", in.rc.State, got, want)
		}
	}
}
