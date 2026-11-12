package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
)

const reportSession = "rem-report-test"

func reportFixture(t *testing.T, lane v1alpha1.Lane) (*fixture, string) {
	t.Helper()
	f := newFixture(t, summonTestPolicy())
	tok := f.sessionPod(reportSession, "ops", 0)
	s := f.session(reportSession)
	s.Spec.Caller, s.Spec.Lane = summonTestPolicy().Name, lane
	s.Spec.Summon = &v1alpha1.SessionSummon{OnUnreported: v1alpha1.SummonOnUnreportedFailed}
	if lane == v1alpha1.LaneRemediation {
		s.Spec.Summon.OnUnreported = v1alpha1.SummonOnUnreportedEscalated
	}
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return f, tok
}

func reportPath() string { return apiv1.SessionPath(reportSession) + "/outcome" }
func reportBody(verb, note string) map[string]string {
	return map[string]string{"state": verb, "note": note}
}

func outcomeStdout(t *testing.T) func() string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "outcome-stdout")
	file, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = file
	t.Cleanup(func() { os.Stdout = previous; _ = file.Close() })
	return func() string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
}

// Read the wire representation so these proofs also fail against the old status schema.
func summonRecord(t *testing.T, s *v1alpha1.AgentSession) map[string]any {
	t.Helper()
	b, err := json.Marshal(s.Status)
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err := json.Unmarshal(b, &status); err != nil {
		t.Fatal(err)
	}
	record, _ := status["summon"].(map[string]any)
	return record
}

func requireClosed(t *testing.T, s *v1alpha1.AgentSession, source, reason string, filing bool) {
	t.Helper()
	record := summonRecord(t, s)
	closed, _ := record["closed"].(map[string]any)
	if closed == nil || closed["source"] != source || closed["reason"] != reason || closed["at"] == nil {
		t.Fatalf("missing atomic close: %#v", record)
	}
	escalation, _ := record["escalation"].(map[string]any)
	if filing {
		if escalation == nil || escalation["state"] != "Pending" || escalation["name"] != "esc-"+s.Name {
			t.Fatalf("missing pending filing: %#v", record)
		}
	} else if escalation != nil {
		t.Fatalf("unexpected filing: %#v", escalation)
	}
}

func countOutcomeWrites(f *fixture, count *int) client.Client {
	return interceptor.NewClient(f.c, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		s, ok := obj.(*v1alpha1.AgentSession)
		isOutcome := ok && s.Status.Outcome != nil
		err := c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		if err == nil && isOutcome {
			*count++
		}
		return err
	}})
}

func closeReportFixture(t *testing.T, f *fixture, c client.Client) {
	t.Helper()
	r := &controller.Reconciler{Client: c, APIReader: f.c, Now: func() time.Time { return f.now }}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: sessionNS, Name: reportSession}}); err != nil {
		t.Fatal(err)
	}
}

func TestSummonReportVerbs(t *testing.T) {
	for verb, want := range map[string]v1alpha1.OutcomeState{"working": v1alpha1.OutcomeRunning, "done": v1alpha1.OutcomeDone, "failed": v1alpha1.OutcomeFailed, "escalate": v1alpha1.OutcomeEscalated} {
		t.Run(verb, func(t *testing.T) {
			f, tok := reportFixture(t, v1alpha1.LaneCuration)
			if w := f.do(http.MethodPost, reportPath(), tok, reportBody(verb, "work result")); w.Code != http.StatusOK {
				t.Fatalf("report: %d %s", w.Code, w.Body.String())
			}
			got := f.session(reportSession)
			if got.Status.Outcome == nil || got.Status.Outcome.State != want || got.Status.Outcome.Note != "work result" || got.Status.Outcome.At == nil || !got.Status.Outcome.At.Time.Equal(f.now) {
				t.Fatalf("outcome: %+v", got.Status.Outcome)
			}
			if verb == "working" {
				if summonRecord(t, got)["closed"] != nil {
					t.Fatal("working closed the session")
				}
				f.now = f.now.Add(time.Minute)
				if w := f.do(http.MethodPost, reportPath(), tok, reportBody(verb, "progress")); w.Code != http.StatusOK {
					t.Fatal(w.Code)
				}
				if got := f.session(reportSession).Status.Outcome; !got.At.Time.Equal(f.now) || got.Note != "progress" {
					t.Fatal("working did not move at")
				}
			} else {
				requireClosed(t, got, "report", verb, false)
			}
		})
	}
}

func TestSummonReportOwnPodOnly(t *testing.T) {
	for _, caller := range []string{"another-session", "caller", "person", "client", "unsummoned", "old-owner-uid", "replaced-after-resolution"} {
		t.Run(caller, func(t *testing.T) {
			f, tok := reportFixture(t, v1alpha1.LaneRemediation)
			switch caller {
			case "another-session":
				tok = f.sessionPod("other", "ops", 0)
			case "caller":
				tok = tokStranger
			case "person":
				tok = tokHuman
			case "client":
				tok = tokClient
			case "unsummoned":
				s := f.session(reportSession)
				s.Spec.Caller = ""
				if err := f.c.Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			case "old-owner-uid":
				var p corev1.Pod
				if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: reportSession}, &p); err != nil {
					t.Fatal(err)
				}
				p.OwnerReferences[0].UID = "earlier-session"
				if err := f.c.Update(context.Background(), &p); err != nil {
					t.Fatal(err)
				}
			case "replaced-after-resolution":
				f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
					err := c.Get(ctx, k, o, opts...)
					if s, ok := o.(*v1alpha1.AgentSession); ok && err == nil {
						s.UID = "replaced-session"
					}
					return err
				}})
			}
			before := f.session(reportSession).Status.DeepCopy()
			writes := 0
			f.srv.Client = countOutcomeWrites(f, &writes)
			if w := f.do(http.MethodPost, reportPath(), tok, reportBody("done", "private result")); w.Code != http.StatusForbidden {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
			if writes != 0 || !reflect.DeepEqual(before, &f.session(reportSession).Status) {
				t.Fatal("forbidden report wrote status")
			}
		})
	}
}

func TestSummonReportTerminalIsFinal(t *testing.T) {
	for _, repeat := range []struct {
		verb, note string
		code       int
	}{{"done", "finished", 200}, {"failed", "finished", 409}, {"done", "changed", 409}, {"working", "finished", 409}} {
		t.Run(repeat.verb+repeat.note, func(t *testing.T) {
			logs := outcomeStdout(t)
			f, tok := reportFixture(t, v1alpha1.LaneCuration)
			writes := 0
			f.srv.Client = countOutcomeWrites(f, &writes)
			if w := f.do(http.MethodPost, reportPath(), tok, reportBody("done", "finished")); w.Code != 200 {
				t.Fatal(w.Code)
			}
			before := f.session(reportSession).Status.DeepCopy()
			f.now = f.now.Add(time.Hour)
			w := f.do(http.MethodPost, reportPath(), tok, reportBody(repeat.verb, repeat.note))
			if w.Code != repeat.code {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
			var out apiv1.Outcome
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.State != "done" || out.Note != "finished" {
				t.Fatalf("stored outcome missing: %s", w.Body.String())
			}
			if !reflect.DeepEqual(before, &f.session(reportSession).Status) || writes != 1 || strings.Count(logs(), "event=outcome ") != 1 {
				t.Fatal("terminal report was rewritten or relogged")
			}
		})
	}
}

func TestSummonReportPromotionAndAtomicFiling(t *testing.T) {
	for _, lane := range []v1alpha1.Lane{v1alpha1.LaneRemediation, v1alpha1.LaneEscalation, v1alpha1.LaneCuration} {
		for _, verb := range []string{"failed", "escalate"} {
			t.Run(string(lane)+verb, func(t *testing.T) {
				logs := outcomeStdout(t)
				f, tok := reportFixture(t, lane)
				patches := 0
				f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
					s := o.(*v1alpha1.AgentSession)
					patches++
					data, err := p.Data(o)
					if err != nil {
						t.Fatal(err)
					}
					wire := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: s.Name}}
					if err := json.Unmarshal(data, wire); err != nil {
						t.Fatal(err)
					}
					if wire.ResourceVersion == "" {
						t.Fatal("outcome patch omitted optimistic lock")
					}
					requireClosed(t, wire, "report", verb, lane == v1alpha1.LaneRemediation)
					if wire.Status.Outcome == nil || wire.Status.Outcome.State != s.Status.Outcome.State || wire.Status.Outcome.Note != s.Status.Outcome.Note || wire.Status.Outcome.At == nil || !wire.Status.Outcome.At.Equal(s.Status.Outcome.At) {
						t.Fatal("atomic wire patch omitted or changed the outcome")
					}
					return c.SubResource(sub).Patch(ctx, o, p, opts...)
				}})
				if w := f.do(http.MethodPost, reportPath(), tok, reportBody(verb, "needs help")); w.Code != 200 {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
				s := f.session(reportSession)
				want := v1alpha1.OutcomeEscalated
				note := "needs help"
				if verb == "failed" {
					if lane == v1alpha1.LaneRemediation {
						note = "reported failed: " + note
					} else {
						want = v1alpha1.OutcomeFailed
					}
				}
				if s.Status.Outcome.State != want || s.Status.Outcome.Note != note || patches != 1 {
					t.Fatalf("outcome %+v, writes %d", s.Status.Outcome, patches)
				}
				if w := f.do(http.MethodPost, reportPath(), tok, reportBody(verb, "needs help")); w.Code != http.StatusOK {
					t.Fatal("identical repeat refused", w.Code)
				}
				if lane == v1alpha1.LaneRemediation && verb == "failed" {
					if w := f.do(http.MethodPost, reportPath(), tok, reportBody("escalate", "reported failed: needs help")); w.Code != http.StatusOK {
						t.Fatal("equal resulting terminal report refused", w.Code)
					}
					if !strings.Contains(logs(), "state=escalated source=report reason=failed") {
						t.Fatal("promoted report lost its original reason")
					}
				}
				if patches != 1 {
					t.Fatal("repeat wrote the promoted outcome")
				}

				filings := 0
				if lane == v1alpha1.LaneRemediation {
					filings = 1
				}
				if strings.Count(logs(), "event=filing ") != filings {
					t.Fatal("wrong pending filing log count")
				}
				var sessions v1alpha1.AgentSessionList
				if err := f.c.List(context.Background(), &sessions); err != nil {
					t.Fatal(err)
				}
				if len(sessions.Items) != 1 {
					t.Fatal("slice a created a filing session")
				}
			})
		}
	}
}

func TestSummonGuaranteedCloseInputsAndOrder(t *testing.T) {
	cases := []struct {
		name, agent, reason string
		result              *v1alpha1.TaskResultStatus
		pod                 bool
	}{
		{"result-succeeded", "", "agent-exited", &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultSucceeded}, false},
		{"result-timeout", "failed", "timed-out", &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultTimedOut}, true},
		{"result-unknown", "exited", "exit-unknown", &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultUnknown}, true},
		{"exited", "exited", "agent-exited", nil, false},
		{"failed", "failed", "agent-failed", nil, false},
		{"interrupted", "interrupted", "agent-interrupted", nil, false},
		{"pod", "busy", "pod-terminated", nil, true},
		{"exited-before-pod", "exited", "agent-exited", nil, true},
		{"failed-before-pod", "failed", "agent-failed", nil, true},
		{"interrupted-before-pod", "interrupted", "agent-interrupted", nil, true},
	}
	for _, lane := range []v1alpha1.Lane{v1alpha1.LaneRemediation, v1alpha1.LaneCuration} {
		for _, tc := range cases {
			t.Run(string(lane)+"/"+tc.name, func(t *testing.T) {
				logs := outcomeStdout(t)
				f, _ := reportFixture(t, lane)
				s := f.session(reportSession)
				s.Status.TaskResult = tc.result
				s.Status.Agent = &v1alpha1.AgentStatus{Status: tc.agent, LastHeartbeat: &metav1.Time{Time: f.now}}
				if err := f.c.Status().Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
				if tc.pod {
					var p corev1.Pod
					if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(s), &p); err != nil {
						t.Fatal(err)
					}
					p.Status.Phase = corev1.PodFailed
					if err := f.c.Status().Update(context.Background(), &p); err != nil {
						t.Fatal(err)
					}
				}
				writes := 0
				c := countOutcomeWrites(f, &writes)
				closeReportFixture(t, f, c)
				got := f.session(reportSession)
				if got.Status.Outcome == nil || got.Status.Outcome.State != v1alpha1.OutcomeState(got.Spec.Summon.OnUnreported) || got.Status.Outcome.Note != "unreported: "+tc.reason {
					t.Fatalf("outcome %+v", got.Status.Outcome)
				}
				if !reflect.DeepEqual(got.Status.TaskResult, tc.result) {
					t.Fatal("guaranteed close changed taskResult")
				}
				requireClosed(t, got, "unreported", tc.reason, lane == v1alpha1.LaneRemediation)
				var outcomeLine string
				for _, line := range strings.Split(logs(), "\n") {
					if strings.HasPrefix(line, "ops-event: event=outcome ") {
						outcomeLine = line
					}
				}
				fields := map[string]string{}
				for _, field := range strings.Fields(strings.TrimPrefix(outcomeLine, "ops-event: ")) {
					key, value, ok := strings.Cut(field, "=")
					if !ok {
						t.Fatalf("invalid close event field: %q", field)
					}
					fields[key] = value
				}
				wantFields := map[string]string{"event": "outcome", "session": reportSession, "caller": s.Spec.Caller, "lane": string(lane), "state": string(got.Status.Outcome.State), "source": "unreported", "reason": tc.reason, "note_bytes": strconv.Itoa(len(got.Status.Outcome.Note))}
				if !reflect.DeepEqual(fields, wantFields) {
					t.Fatalf("close event fields: got=%v want=%v", fields, wantFields)
				}
				before := got.Status.Outcome.DeepCopy()
				record := summonRecord(t, got)
				// Each call constructs a fresh reconciler, also proving a restarted leader.
				closeReportFixture(t, f, c)
				f.now = f.now.Add(time.Hour)
				closeReportFixture(t, f, c)
				got = f.session(reportSession)
				if writes != 1 || !reflect.DeepEqual(before, got.Status.Outcome) || !reflect.DeepEqual(record, summonRecord(t, got)) || strings.Count(logs(), "event=outcome ") != 1 {
					t.Fatal("close repeated after observation/restart")
				}
			})
		}
	}
}

func TestSummonReportRacesGuaranteedClose(t *testing.T) {
	for _, winner := range []string{"report", "unreported"} {
		t.Run(winner, func(t *testing.T) {
			logs := outcomeStdout(t)
			f, tok := reportFixture(t, v1alpha1.LaneRemediation)
			s := f.session(reportSession)
			s.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentFailed}
			if err := f.c.Status().Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			writes, conflicts := 0, 0
			entered := false
			competitor := countOutcomeWrites(f, &writes)
			racing := interceptor.NewClient(f.c, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
				if !entered {
					entered = true
					if winner == "report" {
						if w := f.do(http.MethodPost, reportPath(), tok, reportBody("escalate", "report won")); w.Code != 200 {
							t.Fatal(w.Code)
						}
					} else {
						closeReportFixture(t, f, competitor)
					}
				}
				err := c.SubResource(sub).Patch(ctx, o, p, opts...)
				if apierrors.IsConflict(err) {
					conflicts++
				}
				if err == nil {
					writes++
				}
				return err
			}})
			if winner == "report" {
				f.srv.Client = competitor
				closeReportFixture(t, f, racing)
			} else {
				f.srv.Client = racing
				w := f.do(http.MethodPost, reportPath(), tok, reportBody("escalate", "report won"))
				if w.Code != 409 {
					t.Fatalf("race report %d %s", w.Code, w.Body.String())
				}
			}
			s = f.session(reportSession)
			reason := "escalate"
			note := "report won"
			if winner == "unreported" {
				reason = "agent-failed"
				note = "unreported: agent-failed"
			}
			if s.Status.Outcome == nil || s.Status.Outcome.Note != note || writes != 1 || conflicts != 1 {
				t.Fatalf("winner lost: outcome=%+v writes=%d conflicts=%d", s.Status.Outcome, writes, conflicts)
			}
			requireClosed(t, s, winner, reason, true)
			if strings.Count(logs(), "event=outcome ") != 1 || strings.Count(logs(), "event=filing ") != 1 {
				t.Fatal("racing loser logged a decision")
			}
		})
	}
}

func TestSummonDecisionWritersDoNotOwnOutcome(t *testing.T) {
	for _, action := range []string{"discover", "clear", "reserve"} {
		t.Run(action, func(t *testing.T) {
			f, ex, sess, _ := decisionAPIFixture(t)
			s := f.session(sess.Name)
			s.Spec.Caller = "summoning-policy"
			if err := f.c.Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			var result protocol.DecisionResult
			_ = json.Unmarshal([]byte(ex.out), &result)
			record := *result.Decision
			record.Answer = "SYNTHETIC-ANSWER-A"
			authority := v1alpha1.DecisionAnswerStatus(protocol.AnswerAuthority(record, record.Answer))
			s = f.session(sess.Name)
			s.Status.DecisionAnswer = &authority
			if action != "discover" {
				s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeEscalated, Note: "decision/" + record.ID}
			}
			if err := f.c.Status().Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			before := s.Status.Outcome.DeepCopy()
			if action == "reserve" {
				record.ID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
				record.Answer = ""
				data, _ := json.Marshal(protocol.DecisionResult{Decision: &record})
				ex.out = string(data)
				if w := f.do(http.MethodPost, apiv1.SessionDecisionPath(s.Name), tokCoordinator, protocol.DecisionAnswer{ID: record.ID, Text: "SYNTHETIC-ANSWER-B"}); w.Code != http.StatusAccepted {
					t.Fatalf("decision %d %s", w.Code, w.Body.String())
				}
			} else {
				st := status(s.Name)
				state := "Open"
				if action == "clear" {
					state = "Delivered"
				}
				st.Decision = &protocol.DecisionOutcome{ID: record.ID, SessionUID: record.SessionUID, PodUID: record.PodUID, ThreadID: record.ThreadID, WriterGeneration: record.WriterGeneration, State: state, AnswerDigest: authority.Digest, At: f.now}
				if w := f.do(http.MethodPost, protocol.HeartbeatPath(s.Name), "tok-session-"+s.Name, st); w.Code != 204 {
					t.Fatal(w.Code)
				}
			}
			if !reflect.DeepEqual(before, f.session(s.Name).Status.Outcome) {
				t.Fatal("decision path wrote or cleared summoned outcome")
			}
		})
	}
}

func TestSummonOutcomeLogFieldsExcludeNote(t *testing.T) {
	logs := outcomeStdout(t)
	f, tok := reportFixture(t, v1alpha1.LaneCuration)
	note := "SYNTHETIC-PRIVATE-NOTE event=forged"
	if w := f.do(http.MethodPost, reportPath(), tok, reportBody("done", note)); w.Code != 200 {
		t.Fatal(w.Code)
	}
	text := logs()
	for _, field := range []string{"ops-event: event=outcome ", "session=" + reportSession, "caller=" + summonTestPolicy().Name, "lane=curation", "state=done", "source=report", "reason=done", "note_bytes=35"} {
		if !strings.Contains(text, field) {
			t.Errorf("missing %q in %q", field, text)
		}
	}
	if strings.Contains(text, "SYNTHETIC-PRIVATE-NOTE") || strings.Contains(text, "event=forged") {
		t.Fatal("note text leaked into log")
	}
}

func TestSummonReportNoteByteLimit(t *testing.T) {
	for _, tc := range []struct {
		name, note, verb string
		lane             v1alpha1.Lane
		code             int
	}{
		{"1024", strings.Repeat("a", 1024), "done", v1alpha1.LaneCuration, 200},
		{"1025", strings.Repeat("a", 1025), "done", v1alpha1.LaneCuration, 422},
		{"unicode-1024", strings.Repeat("é", 512), "done", v1alpha1.LaneCuration, 200},
		{"unicode-1025", strings.Repeat("é", 512) + "a", "done", v1alpha1.LaneCuration, 422},
		{"promoted-1024", strings.Repeat("a", 1007), "failed", v1alpha1.LaneRemediation, 200},
		{"promoted-1025", strings.Repeat("a", 1008), "failed", v1alpha1.LaneRemediation, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, tok := reportFixture(t, tc.lane)
			w := f.do(http.MethodPost, reportPath(), tok, reportBody(tc.verb, tc.note))
			if w.Code != tc.code {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			got := f.session(reportSession).Status.Outcome
			if tc.code == 200 {
				if got == nil || len(got.Note) != 1024 {
					t.Fatal("note not preserved")
				}
			} else if got != nil {
				t.Fatal("oversized note wrote outcome")
			}
		})
	}
}

func TestSummonWorkingDoesNotLogOutcome(t *testing.T) {
	logs := outcomeStdout(t)
	f, tok := reportFixture(t, v1alpha1.LaneCuration)
	if w := f.do(http.MethodPost, reportPath(), tok, reportBody("working", "progress")); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if strings.Contains(logs(), "ops-event:") {
		t.Fatal("progress logged an outcome decision")
	}
}

func TestSummonReportNoteCharacters(t *testing.T) {
	for _, tc := range []struct {
		name, note string
		code       int
	}{
		{"newline", "line\nbreak", http.StatusUnprocessableEntity},
		{"tab", "a\tb", http.StatusUnprocessableEntity},
		{"nul", "a\x00b", http.StatusUnprocessableEntity},
		{"escape", "\x1b[2J", http.StatusUnprocessableEntity},
		{"del", "\x7f", http.StatusUnprocessableEntity},
		{"c1-start", "\u0080", http.StatusUnprocessableEntity},
		{"c1-end", "\u009f", http.StatusUnprocessableEntity},
		{"bidi-format", "\u202e", http.StatusUnprocessableEntity},
		{"zero-width-format", "\u200b", http.StatusUnprocessableEntity},
		{"control-before-length", strings.Repeat("a", 1025) + "\n", http.StatusUnprocessableEntity},
		{"accent", "é", http.StatusOK},
	} {
		for _, verb := range []string{"working", "done", "failed", "escalate"} {
			t.Run(tc.name+"/"+verb, func(t *testing.T) {
				logs := outcomeStdout(t)
				f, tok := reportFixture(t, v1alpha1.LaneRemediation)
				before := f.session(reportSession).DeepCopy()
				writes := 0
				f.srv.Client = countOutcomeWrites(f, &writes)
				w := f.do(http.MethodPost, reportPath(), tok, reportBody(verb, tc.note))
				if w.Code != tc.code {
					t.Errorf("character validation: got %d want %d", w.Code, tc.code)
				}
				got := f.session(reportSession)
				if tc.code == http.StatusUnprocessableEntity {
					if writes != 0 || !reflect.DeepEqual(before, got) || strings.Contains(logs(), "ops-event:") {
						t.Error("refused note wrote status or emitted an event")
					}
					if !strings.Contains(w.Body.String(), "control or format") {
						t.Error("character refusal did not precede the length check")
					}
				} else {
					wantNote := tc.note
					if verb == "failed" {
						wantNote = "reported failed: " + tc.note
					}
					if writes != 1 || got.Status.Outcome == nil || got.Status.Outcome.Note != wantNote {
						t.Fatal("printable Unicode note not preserved")
					}
				}
			})
		}
	}
}

func TestSummonReportEqualUnreportedOutcomeConflicts(t *testing.T) {
	logs := outcomeStdout(t)
	f, tok := reportFixture(t, v1alpha1.LaneCuration)
	s := f.session(reportSession)
	s.Status.Agent = &v1alpha1.AgentStatus{Status: "exited"}
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	closeReportFixture(t, f, f.c)
	before := f.session(reportSession).DeepCopy()
	beforeLogs := logs()
	writes := 0
	f.srv.Client = countOutcomeWrites(f, &writes)
	w := f.do(http.MethodPost, reportPath(), tok, reportBody("failed", "unreported: agent-exited"))
	if w.Code != http.StatusConflict {
		t.Errorf("matching an unreported close: got %d want 409", w.Code)
	}
	var got apiv1.Outcome
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || !reflect.DeepEqual(&got, outcomeView(before.Status.Outcome)) {
		t.Fatalf("missing stored outcome: %s", w.Body.String())
	}
	if writes != 0 || !reflect.DeepEqual(before, f.session(reportSession)) || logs() != beforeLogs {
		t.Fatal("matching report changed or logged the guaranteed close")
	}
}

func TestSummonReportSessionReplacedBeforePatch(t *testing.T) {
	logs := outcomeStdout(t)
	f, tok := reportFixture(t, v1alpha1.LaneCuration)
	var replacement *v1alpha1.AgentSession
	writes, conflicts := 0, 0
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		if replacement == nil {
			old := f.session(reportSession)
			old.Finalizers = nil
			if err := c.Update(ctx, old); err != nil {
				t.Fatal(err)
			}
			if err := c.Delete(ctx, old); err != nil {
				t.Fatal(err)
			}
			replacement = old.DeepCopy()
			replacement.UID = "replacement-session"
			replacement.ResourceVersion = ""
			if err := c.Create(ctx, replacement); err != nil {
				t.Fatal(err)
			}
			// The fake client resets versions on create; ensure replacement gets
			// a different version, as it would from the real API server.
			if replacement.ResourceVersion == old.ResourceVersion {
				if err := c.Update(ctx, replacement); err != nil {
					t.Fatal(err)
				}
			}
			replacement = f.session(reportSession).DeepCopy()
		}
		err := c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		if apierrors.IsConflict(err) {
			conflicts++
		}
		if err == nil {
			writes++
		}
		return err
	}})
	w := f.do(http.MethodPost, reportPath(), tok, reportBody("done", "previous session's result"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("replacement report: %d %s", w.Code, w.Body.String())
	}
	if conflicts != 1 || writes != 0 || !reflect.DeepEqual(replacement, f.session(reportSession)) || strings.Contains(logs(), "ops-event:") {
		t.Fatalf("replacement modified: conflicts=%d writes=%d", conflicts, writes)
	}
}
