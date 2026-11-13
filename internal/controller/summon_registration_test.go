package controller

// The controller proofs of story 794 (DESIGN-064 6.10; coordinator rulings
// O1, O2 and O3 on item 745 comments 7680 and 7685): the leader's watch pass
// settles a remote escalation's registration once, with escalation-started
// in the same write. Each test runs on the fake API server and the world's
// fake clock; writes are interleaved by client hooks, never by goroutine
// timing.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// rcFor is a Remote Control record in state, under name; registered carries
// a verified link.
func rcFor(name, state string) *v1alpha1.RemoteControlStatus {
	rc := &v1alpha1.RemoteControlStatus{Name: name, State: state}
	if state == protocol.RemoteRegistered {
		rc.SessionID = "session_01Reg" + strings.ReplaceAll(name, "-", "_")
		rc.URL = "https://claude.ai/code/" + rc.SessionID
	}
	return rc
}

// report stores one heartbeat as the API's route does: status.agent with a
// fresh lastHeartbeat by the world's clock, and status.remoteControl
// replaced whole.
func (w *summonWorld) report(name, agent string, rc *v1alpha1.RemoteControlStatus) {
	w.t.Helper()
	s := w.get(name)
	at := metav1.NewTime(w.now)
	s.Status.Agent = &v1alpha1.AgentStatus{Status: agent, LastHeartbeat: &at, LastActivity: &at}
	s.Status.RemoteControl = rc
	if err := w.base.Status().Update(context.Background(), s); err != nil {
		w.t.Fatal(err)
	}
}

func (w *summonWorld) registration(name string) *v1alpha1.SummonRegistrationStatus {
	if st := w.get(name).Status.Summon; st != nil {
		return st.Registration
	}
	return nil
}

func startedPages(pages []v1alpha1.SummonPage) []v1alpha1.SummonPage {
	var out []v1alpha1.SummonPage
	for _, p := range pages {
		if p.Event == v1alpha1.PageEscalationStarted {
			out = append(out, p)
		}
	}
	return out
}

// regWrites is the new leader's status writes that touch the registration
// record or the page events, each as sent. before, when set, runs before such
// a write reaches the API server.
type regWrites struct{ bodies []string }

func (w *summonWorld) countingLeader(before func(obj client.Object)) *regWrites {
	rw := &regWrites{}
	w.r = w.leader(&interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		data, err := patch.Data(obj)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(`"registration"`)) || bytes.Contains(data, []byte(`"pages"`)) {
			rw.bodies = append(rw.bodies, obj.GetName()+" "+string(data))
			if before != nil {
				before(obj)
			}
		}
		return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
	}})
	return rw
}

// step moves the clock to at, stores one heartbeat and runs one pass.
func (w *summonWorld) step(name string, at time.Duration, agent string, rc *v1alpha1.RemoteControlStatus) {
	w.t.Helper()
	w.now = summonT0.Add(at)
	w.report(name, agent, rc)
	w.pass()
}

func wantSettled(t *testing.T, w *summonWorld, name string, result v1alpha1.SummonRegistrationResult, reason v1alpha1.SummonRegistrationReason, at time.Time) {
	t.Helper()
	reg := w.registration(name)
	if reg == nil || reg.Result != result || reg.Reason != reason || reg.At == nil || !reg.At.Time.Equal(at) {
		t.Fatalf("%s: registration %+v, want %s %q at %s", name, reg, result, reason, at)
	}
	started := startedPages(w.pages(name))
	if len(started) != 1 || !started[0].At.Time.Equal(at) {
		t.Fatalf("%s: escalation-started pages %+v, want one at %s", name, started, at)
	}
}

// Proof 9: no escalation-started before a settlement. Passes with a pending
// session write nothing, and ten passes with the same inputs write the
// status once, the agent seen.
func TestSummonRegistrationNothingBeforeASettlement(t *testing.T) {
	w, s := remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-pending")
	writes := w.countingLeader(nil)
	for i := range 10 {
		w.step(s.Name, time.Duration(i)*50*time.Second, protocol.AgentPending, rcFor(s.Name, protocol.RemoteStarting))
	}
	if len(writes.bodies) != 0 || w.registration(s.Name) != nil || len(w.pages(s.Name)) != 0 {
		t.Fatalf("a pending session: writes %v registration %+v pages %+v", writes.bodies, w.registration(s.Name), w.pages(s.Name))
	}
	for i := range 10 {
		w.step(s.Name, 8*time.Minute+time.Duration(i)*20*time.Second, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteStarting))
	}
	reg := w.registration(s.Name)
	if len(writes.bodies) != 1 || reg == nil || reg.AgentSeenAt == nil || !reg.AgentSeenAt.Time.Equal(summonT0.Add(8*time.Minute)) || reg.Result != "" || reg.At != nil {
		t.Fatalf("ten passes with the agent busy: writes %v registration %+v", writes.bodies, reg)
	}
	if strings.Contains(writes.bodies[0], "pages") {
		t.Fatalf("the agent-seen write touched the pages: %s", writes.bodies[0])
	}
	if len(w.pages(s.Name)) != 0 {
		t.Fatalf("an event before a settlement: %+v", w.pages(s.Name))
	}
	if w.closed(s.Name) != nil {
		t.Fatal("the session closed")
	}
}

// Proof 10: a registered session gets its result, its time and one
// escalation-started in one write, and ten more passes write nothing. When
// the agent was seen first, agentSeenAt stays; when it was not, the
// settlement does not invent one.
func TestSummonRegistrationRegisteredOnceInOneWrite(t *testing.T) {
	for _, seenFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "not seen first", true: "seen first"}[seenFirst], func(t *testing.T) {
			w, s := remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-registered")
			writes := w.countingLeader(nil)
			want := 1
			if seenFirst {
				w.step(s.Name, 30*time.Second, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteStarting))
				want = 2
			}
			w.step(s.Name, 90*time.Second, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteRegistered))
			at := summonT0.Add(90 * time.Second)
			wantSettled(t, w, s.Name, v1alpha1.RegistrationRegistered, "", at)
			if len(writes.bodies) != want {
				t.Fatalf("writes %v, want %d", writes.bodies, want)
			}
			last := writes.bodies[len(writes.bodies)-1]
			if !strings.Contains(last, `"result":"registered"`) || !strings.Contains(last, string(v1alpha1.PageEscalationStarted)) {
				t.Fatalf("the result and the event are not one write: %s", last)
			}
			reg := w.registration(s.Name)
			if seenFirst != (reg.AgentSeenAt != nil) || seenFirst && !reg.AgentSeenAt.Time.Equal(summonT0.Add(30*time.Second)) {
				t.Fatalf("agentSeenAt %v, seen first %v", reg.AgentSeenAt, seenFirst)
			}
			settled := w.get(s.Name).Status.Summon.DeepCopy()
			for i := range 10 {
				rc := rcFor(s.Name, protocol.RemoteRegistered)
				if i%2 == 1 {
					rc = rcFor(s.Name, protocol.RemoteOffline)
				}
				w.step(s.Name, 2*time.Minute+time.Duration(i)*time.Minute, protocol.AgentIdle, rc)
			}
			if len(writes.bodies) != want || !apiequality.Semantic.DeepEqual(w.get(s.Name).Status.Summon, settled) {
				t.Fatalf("ten more passes wrote: %v", writes.bodies[want:])
			}
		})
	}
}

// Proof 11: one event per session across leader restarts. A new leader reads
// the stored agentSeenAt and settles at its bound once; later leaders write
// nothing.
func TestSummonRegistrationOnceAcrossLeaderRestarts(t *testing.T) {
	w, s := remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-restart")
	w.step(s.Name, time.Minute, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteStarting))
	w.r = w.leader(nil)
	w.step(s.Name, 3*time.Minute, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteOffline))
	if reg := w.registration(s.Name); reg == nil || reg.AgentSeenAt == nil || !reg.AgentSeenAt.Time.Equal(summonT0.Add(time.Minute)) || reg.Result != "" {
		t.Fatalf("after a restart: %+v", reg)
	}
	w.r = w.leader(nil)
	w.step(s.Name, 6*time.Minute-time.Second, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteOffline))
	if reg := w.registration(s.Name); reg.Result != "" || len(w.pages(s.Name)) != 0 {
		t.Fatalf("a second before the bound: %+v %+v", reg, w.pages(s.Name))
	}
	w.r = w.leader(nil)
	w.step(s.Name, 6*time.Minute, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteOffline))
	wantSettled(t, w, s.Name, v1alpha1.RegistrationUnavailable, v1alpha1.RegistrationReasonOffline, summonT0.Add(6*time.Minute))
	for i := range 3 {
		w.r = w.leader(nil)
		w.step(s.Name, 7*time.Minute+time.Duration(i)*time.Minute, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteRegistered))
	}
	wantSettled(t, w, s.Name, v1alpha1.RegistrationUnavailable, v1alpha1.RegistrationReasonOffline, summonT0.Add(6*time.Minute))
	if p := w.pages(s.Name); len(p) != 1 {
		t.Fatalf("pages %+v", p)
	}
}

// Proof 11: a write conflict. The stored object changes between the pass's
// read and its patch, so that pass writes nothing; the next pass settles
// once. The same holds for the agent-seen write.
func TestSummonRegistrationOnceAcrossAConflict(t *testing.T) {
	for _, first := range []string{"settle", "agent-seen"} {
		t.Run(first, func(t *testing.T) {
			w, s := remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-conflict")
			raced := false
			writes := w.countingLeader(func(client.Object) {
				if raced {
					return
				}
				raced = true
				// Another heartbeat lands first, as the API's route would.
				live := w.get(s.Name)
				live.Status.Agent.Attached = 1
				if err := w.base.Status().Update(context.Background(), live); err != nil {
					t.Fatal(err)
				}
			})
			rc := rcFor(s.Name, protocol.RemoteRegistered)
			if first == "agent-seen" {
				rc = rcFor(s.Name, protocol.RemoteStarting)
			}
			w.step(s.Name, time.Minute, protocol.AgentBusy, rc)
			if len(writes.bodies) != 1 || w.registration(s.Name) != nil || len(w.pages(s.Name)) != 0 {
				t.Fatalf("the conflicting pass wrote: writes %v registration %+v pages %+v", writes.bodies, w.registration(s.Name), w.pages(s.Name))
			}
			if first == "agent-seen" {
				w.step(s.Name, 2*time.Minute, protocol.AgentBusy, rc)
				if reg := w.registration(s.Name); reg == nil || reg.AgentSeenAt == nil || !reg.AgentSeenAt.Time.Equal(summonT0.Add(2*time.Minute)) {
					t.Fatalf("agent seen after the conflict: %+v", reg)
				}
			}
			w.step(s.Name, 3*time.Minute, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteRegistered))
			wantSettled(t, w, s.Name, v1alpha1.RegistrationRegistered, "", summonT0.Add(3*time.Minute))
			n := len(writes.bodies)
			for i := range 3 {
				w.step(s.Name, 4*time.Minute+time.Duration(i)*time.Minute, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteRegistered))
			}
			if len(writes.bodies) != n || len(w.pages(s.Name)) != 1 {
				t.Fatalf("after the settlement: writes %v pages %+v", writes.bodies[n:], w.pages(s.Name))
			}
		})
	}
}

// Proof 12: a settled session is never written again. After unavailable, a
// later registered reading changes nothing; after registered, a later
// offline, or no record at all, changes nothing.
func TestSummonRegistrationSettledNeverChanges(t *testing.T) {
	t.Run("unavailable, then registered", func(t *testing.T) {
		w, s := remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-late")
		writes := w.countingLeader(nil)
		w.step(s.Name, 10*time.Minute-time.Second, protocol.AgentLoginRequired, rcFor(s.Name, protocol.RemoteLoginRequired))
		if reg := w.registration(s.Name); reg != nil {
			t.Fatalf("a second before the admission bound: %+v", reg)
		}
		w.step(s.Name, 10*time.Minute, protocol.AgentLoginRequired, rcFor(s.Name, protocol.RemoteLoginRequired))
		wantSettled(t, w, s.Name, v1alpha1.RegistrationUnavailable, v1alpha1.RegistrationReasonLoginRequired, summonT0.Add(10*time.Minute))
		settled := w.get(s.Name).Status.Summon.DeepCopy()
		if settled.Registration.AgentSeenAt != nil {
			t.Fatalf("agentSeenAt invented: %v", settled.Registration.AgentSeenAt)
		}
		for i := range 5 {
			w.step(s.Name, 11*time.Minute+time.Duration(i)*time.Minute, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteRegistered))
		}
		if len(writes.bodies) != 1 || !apiequality.Semantic.DeepEqual(w.get(s.Name).Status.Summon, settled) {
			t.Fatalf("a late registration wrote: %v", writes.bodies)
		}
	})
	t.Run("registered, then offline", func(t *testing.T) {
		w, s := remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-gone")
		writes := w.countingLeader(nil)
		w.step(s.Name, time.Minute, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteRegistered))
		wantSettled(t, w, s.Name, v1alpha1.RegistrationRegistered, "", summonT0.Add(time.Minute))
		settled := w.get(s.Name).Status.Summon.DeepCopy()
		for i, rc := range []*v1alpha1.RemoteControlStatus{rcFor(s.Name, protocol.RemoteOffline), nil, rcFor(s.Name, protocol.RemoteStarting), rcFor(s.Name, protocol.RemoteOffline)} {
			w.step(s.Name, 2*time.Minute+time.Duration(i)*5*time.Minute, protocol.AgentIdle, rc)
		}
		if len(writes.bodies) != 1 || !apiequality.Semantic.DeepEqual(w.get(s.Name).Status.Summon, settled) {
			t.Fatalf("a link that went wrote: %v", writes.bodies)
		}
	})
}

// Proof 13: with pages[] at its 8 entries the step logs as summonRecordPage
// does and writes nothing, neither the event nor the result.
func TestSummonRegistrationFullPagesRecordNothing(t *testing.T) {
	w, s := remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-full")
	live := w.get(s.Name)
	for i := range summonMaxPages {
		ev := v1alpha1.PageEscalationHeld
		if i%2 == 1 {
			ev = v1alpha1.PageEscalationReminder
		}
		live.Status.Summon.Pages = append(live.Status.Summon.Pages, v1alpha1.SummonPage{Event: ev, At: metav1.NewTime(summonT0)})
	}
	if err := w.base.Status().Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	full := w.get(s.Name).Status.Summon.Pages
	writes := w.countingLeader(nil)
	var logs strings.Builder
	ctx := logr.NewContext(context.Background(), funcr.New(func(prefix, args string) { logs.WriteString(prefix + args + "\n") }, funcr.Options{}))
	w.now = summonT0.Add(time.Minute)
	w.report(s.Name, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteRegistered))
	if err := w.r.SummonAdmissionPass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(writes.bodies) != 0 || w.registration(s.Name) != nil || !apiequality.Semantic.DeepEqual(w.pages(s.Name), full) {
		t.Fatalf("a full pages[] was written: writes %v registration %+v pages %+v", writes.bodies, w.registration(s.Name), w.pages(s.Name))
	}
	line := logs.String()
	if !strings.Contains(line, "no room for a page event") || !strings.Contains(line, string(v1alpha1.PageEscalationStarted)) || !strings.Contains(line, s.Name) {
		t.Fatalf("no log line for the full pages[]: %q", line)
	}
	if strings.Contains(line, "claude.ai") {
		t.Fatalf("a log line carries the link: %q", line)
	}
}

// Every write logs one line with the session, the caller, the lane and the
// result or reason word, and never the URL.
func TestSummonRegistrationLogsWordsNeverTheLink(t *testing.T) {
	w, s := remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-logged")
	var logs strings.Builder
	ctx := logr.NewContext(context.Background(), funcr.New(func(prefix, args string) { logs.WriteString(prefix + args + "\n") }, funcr.Options{}))
	pass := func(at time.Duration, agent string, rc *v1alpha1.RemoteControlStatus) {
		w.now = summonT0.Add(at)
		w.report(s.Name, agent, rc)
		if err := w.r.SummonAdmissionPass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	pass(time.Minute, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteStarting))
	pass(2*time.Minute, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteRegistered))
	var lines []string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "registration") || strings.Contains(l, "agent was seen") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("want one line per write, got %q", lines)
	}
	for _, l := range lines {
		for _, want := range []string{s.Name, callerA, string(v1alpha1.LaneEscalation)} {
			if !strings.Contains(l, want) {
				t.Errorf("line %q lacks %q", l, want)
			}
		}
	}
	if !strings.Contains(lines[1], `"result"="registered"`) {
		t.Errorf("the settlement line lacks its result: %q", lines[1])
	}
	if strings.Contains(logs.String(), "claude.ai") || strings.Contains(logs.String(), "session_01Reg") {
		t.Fatalf("a log line carries the link: %q", logs.String())
	}
}

// Proof 14: nothing is written for lane upgrade, a summoned task, an
// ordinary remote session without spec.caller, a Queued session, a session
// with a terminal outcome, a session with status.summon.closed, or a session
// the same pass closes.
func TestSummonRegistrationOnlyForAnOpenAdmittedRemoteEscalation(t *testing.T) {
	untouched := func(t *testing.T, w *summonWorld, name string, writes *regWrites) {
		t.Helper()
		for _, b := range writes.bodies {
			if strings.HasPrefix(b, name+" ") && strings.Contains(b, `"registration"`) {
				t.Fatalf("%s was written: %s", name, b)
			}
		}
		if reg := w.registration(name); reg != nil || len(startedPages(w.pages(name))) != 0 {
			t.Fatalf("%s: registration %+v pages %+v", name, reg, w.pages(name))
		}
	}
	// beats runs passes past both bounds with a registered entry, and then an
	// offline one, so any reader would have settled.
	beats := func(w *summonWorld, name string) {
		for i, at := range []time.Duration{time.Minute, 2 * time.Minute, 12 * time.Minute, 13 * time.Minute} {
			rc := rcFor(name, protocol.RemoteRegistered)
			if i >= 2 {
				rc = rcFor(name, protocol.RemoteOffline)
			}
			w.step(name, at, protocol.AgentBusy, rc)
		}
	}
	t.Run("lane upgrade", func(t *testing.T) {
		w, s := remoteAdmitted(t, v1alpha1.LaneUpgrade, "wo-quiet")
		writes := w.countingLeader(nil)
		beats(w, s.Name)
		untouched(t, w, s.Name, writes)
		if len(w.pages(s.Name)) != 0 {
			t.Fatalf("an upgrade got a page event: %+v", w.pages(s.Name))
		}
	})
	t.Run("summoned task", func(t *testing.T) {
		s := summonedFor("rem-task", callerA, v1alpha1.LaneRemediation, 0)
		w := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](1)))
		w.queue(s)
		w.admitDirect(s)
		writes := w.countingLeader(nil)
		beats(w, s.Name)
		untouched(t, w, s.Name, writes)
	})
	t.Run("ordinary remote session", func(t *testing.T) {
		w, s := remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-beside")
		ordinary := summonedFor("ordinary-remote", callerA, v1alpha1.LaneEscalation, 0)
		ordinary.Spec.Caller, ordinary.Spec.Summon = "", nil
		w.add(ordinary)
		live := w.get(ordinary.Name)
		at := metav1.NewTime(summonT0)
		live.Status.Summon = &v1alpha1.SummonStatus{AdmittedAt: &at}
		if err := w.base.Status().Update(context.Background(), live); err != nil {
			t.Fatal(err)
		}
		writes := w.countingLeader(nil)
		for _, at := range []time.Duration{time.Minute, 12 * time.Minute} {
			w.now = summonT0.Add(at)
			w.report(ordinary.Name, protocol.AgentBusy, rcFor(ordinary.Name, protocol.RemoteRegistered))
			w.report(s.Name, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteRegistered))
			w.pass()
		}
		untouched(t, w, ordinary.Name, writes)
		// The pass did run: the summoned escalation beside it settled.
		wantSettled(t, w, s.Name, v1alpha1.RegistrationRegistered, "", summonT0.Add(time.Minute))
	})
	t.Run("queued", func(t *testing.T) {
		busy := summonedFor("esc-busy", callerA, v1alpha1.LaneEscalation, time.Hour)
		queued := summonedFor("esc-queued", callerA, v1alpha1.LaneEscalation, 0)
		w := newSummonWorld(t, summonPolicyObject(callerA, 1, ptr.To[int32](1)))
		w.queue(busy)
		w.admitDirect(busy)
		w.queue(queued)
		writes := w.countingLeader(nil)
		for _, at := range []time.Duration{time.Minute, 12 * time.Minute} {
			w.now = summonT0.Add(at)
			w.report(queued.Name, protocol.AgentBusy, rcFor(queued.Name, protocol.RemoteRegistered))
			w.report(busy.Name, protocol.AgentBusy, rcFor(busy.Name, protocol.RemoteRegistered))
			w.pass()
		}
		if w.admitted(queued.Name) {
			t.Fatal("the queued escalation was admitted")
		}
		untouched(t, w, queued.Name, writes)
		wantSettled(t, w, busy.Name, v1alpha1.RegistrationRegistered, "", summonT0.Add(time.Minute))
	})
	t.Run("terminal outcome", func(t *testing.T) {
		w, s := remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-done")
		live := w.get(s.Name)
		at := metav1.NewTime(summonT0)
		live.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeDone, Note: "done", At: &at}
		if err := w.base.Status().Update(context.Background(), live); err != nil {
			t.Fatal(err)
		}
		writes := w.countingLeader(nil)
		beats(w, s.Name)
		untouched(t, w, s.Name, writes)
	})
	t.Run("closed", func(t *testing.T) {
		w, s := remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-closed")
		live := w.get(s.Name)
		at := metav1.NewTime(summonT0)
		live.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeRunning, At: &at}
		live.Status.Summon.Closed = &v1alpha1.SummonClosedStatus{At: at, Source: summonoutcome.Report, Reason: "done"}
		if err := w.base.Status().Update(context.Background(), live); err != nil {
			t.Fatal(err)
		}
		writes := w.countingLeader(nil)
		beats(w, s.Name)
		untouched(t, w, s.Name, writes)
	})
	t.Run("closed by the same pass", func(t *testing.T) {
		w, s := remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-gone")
		writes := w.countingLeader(nil)
		w.step(s.Name, 12*time.Minute, protocol.AgentInterrupted, rcFor(s.Name, protocol.RemoteRegistered))
		w.wantClosed(s.Name, summonoutcome.ReasonAgentInterrupted, v1alpha1.OutcomeFailed)
		untouched(t, w, s.Name, writes)
		w.step(s.Name, 13*time.Minute, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteRegistered))
		untouched(t, w, s.Name, writes)
	})
}

// An error other than a conflict or a not-found is collected as the
// watchdog's are, and the other sessions in the pass still settle.
func TestSummonRegistrationErrorIsCollected(t *testing.T) {
	w, s := remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-failing")
	other := summonedFor("esc-other", callerA, v1alpha1.LaneEscalation, 0)
	w.queue(other)
	w.admitDirect(other)
	refused := apierrors.NewInternalError(context.DeadlineExceeded)
	w.r = w.leader(&interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		data, err := patch.Data(obj)
		if err != nil {
			return err
		}
		if obj.GetName() == s.Name && bytes.Contains(data, []byte(`"registration"`)) {
			return refused
		}
		return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
	}})
	w.now = summonT0.Add(time.Minute)
	w.report(s.Name, protocol.AgentBusy, rcFor(s.Name, protocol.RemoteRegistered))
	w.report(other.Name, protocol.AgentBusy, rcFor(other.Name, protocol.RemoteRegistered))
	err := w.r.SummonAdmissionPass(context.Background())
	if err == nil || !strings.Contains(err.Error(), s.Name) || !apierrors.IsInternalError(err) {
		t.Fatalf("the pass hid the failing write: %v", err)
	}
	if w.registration(s.Name) != nil {
		t.Fatalf("the failing write stored: %+v", w.registration(s.Name))
	}
	wantSettled(t, w, other.Name, v1alpha1.RegistrationRegistered, "", summonT0.Add(time.Minute))
}

// Proof 16 (R39): a summoned remote escalation is never drained, even when
// it is idle and outdated. TestDrainLeavesASummonedSession covers a
// remediation task; this is the remote escalation case.
func TestDrainLeavesASummonedRemoteEscalation(t *testing.T) {
	tmpl := currentTemplates(t)
	s := drainFixture("esc-summoned")
	s.Spec.Caller, s.Spec.Lane, s.Spec.Mode = "alert-responder", v1alpha1.LaneEscalation, v1alpha1.ModeRemote
	pod := readyDrainPod(s.Name, "old-revision")
	if drainEligible(s, pod, tmpl.Revision()) {
		t.Fatal("a summoned remote escalation was eligible to drain")
	}
	s.Status.Phase = v1alpha1.PhaseDraining
	s.Status.Restart = &v1alpha1.RestartStatus{ConversationID: "conv-1", PodUID: string(pod.UID), Generation: s.Generation}
	if err := podRemovalAllowed(s, pod); !errors.Is(err, errSummonedNotDrained) {
		t.Fatalf("podRemovalAllowed = %v, want the summoned refusal", err)
	}
	s.Status.Phase, s.Status.Restart = v1alpha1.PhaseRunning, nil
	if phase, _ := podPhase(s, observation{pod: owned[*corev1.Pod]{obj: pod}, templates: tmpl}); phase == v1alpha1.PhaseDraining {
		t.Fatal("a summoned remote escalation was marked Draining")
	}
}
