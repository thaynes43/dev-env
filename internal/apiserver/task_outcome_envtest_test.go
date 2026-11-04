package apiserver

// The retained task outcome on its real path (OP402): the original pod's
// authenticated heartbeat writes status.taskOutcome through the API
// (recordTaskOutcome, task_outcome.go) into a real kube-apiserver with the
// AgentSession CRD installed, and the API's named Session read serves it to a
// reader that opts in (server.go, withoutTaskOutcome). These cases run the
// real agent-run client against that API too.
//
// envtest has no reconciler, kubelet or garbage collector. The suite plays
// the operator's part by hand, as envtest_test.go does: it creates the pod,
// adds the rescue finalizer, deletes the pod, sets the phase, and removes the
// finalizer the way releaseSession does once a reaped Session has no pod and
// no volume. Those steps are synthetic. The fixtures' usage carries no served
// model, so nothing here attests a real served model.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
)

const (
	outcomeSummary = "Ready for lead review"
	outcomePR      = "https://github.com/thaynes43/dev-env/pull/402"
)

var outcomeHead = strings.Repeat("c", 40)

// outcomeTask is one task Session created through the API, with the pod the
// suite plays for it and that pod's bound agent token.
type outcomeTask struct {
	name  string
	uid   types.UID
	pod   *corev1.Pod
	agent string
}

func newOutcomeTask(t *testing.T, l *live, human string) outcomeTask {
	t.Helper()
	ctx := context.Background()
	created := unmarshal[apiv1.Session](t, l.want(http.MethodPost, apiv1.SessionsPath, human, task(), http.StatusCreated))
	var sess v1alpha1.AgentSession
	if err := l.k8s.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: created.Name}, &sess); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: sess.Name, Namespace: sessionNS,
			Labels:          map[string]string{v1alpha1.LabelProfile: "dev"},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&sess, v1alpha1.GroupVersion.WithKind("AgentSession"))}},
		Spec: corev1.PodSpec{ServiceAccountName: controller.ServiceAccountName, Containers: []corev1.Container{{Name: "agent", Image: "agent"}}},
	}
	if err := l.k8s.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	controllerutil.AddFinalizer(&sess, controller.Finalizer)
	if err := l.k8s.Update(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	return outcomeTask{name: sess.Name, uid: sess.UID, pod: pod, agent: l.token(sessionNS+"/"+controller.ServiceAccountName, []string{apiv1.TokenAudience}, pod)}
}

// exitHeartbeat is the original pod's heartbeat after its task exited: the
// exit bound to this Session, pod and attempt, the matching initial usage
// observation (no served model), and, when record is not nil, the outcome
// file's snapshot.
func exitHeartbeat(o outcomeTask, exit int, record *protocol.TaskOutcomeRecord) protocol.Status {
	st := status(o.name)
	finished := st.Agent.Task.FinishedAt
	u := protocol.UnknownUsage()
	u.Source = protocol.UsageSource{SessionUID: string(o.uid), PodUID: string(o.pod.UID), Provider: protocol.AgentClaude, RequestedModel: task().Model, AttemptID: strings.Repeat("a", 64)}
	u.ID, u.ObservedAt, u.ResultState = protocol.UsageObservationID(u.Source), finished, "Final"
	n := int64(0)
	u.InputTokens = protocol.TokenObservation{State: protocol.UsageReported, Amount: &n}
	u.Cost = protocol.CostObservation{State: protocol.UsageReported, Amount: "0", Unit: "USD", Kind: protocol.UsageCLIEstimate}
	st.UsageObservation = &u
	st.Agent.Attached = 0
	st.Agent.Task.ExitCode = exit
	if exit != 0 {
		st.Agent.Task.Subtype, st.Agent.Task.IsError = "error_during_execution", true
	}
	st.Agent.Task.ExitSource = &u.Source
	if record != nil {
		st.TaskOutcome = &protocol.TaskOutcome{SessionUID: string(o.uid), PodUID: string(o.pod.UID), Record: *record}
	}
	return st
}

func fullRecord() *protocol.TaskOutcomeRecord {
	return &protocol.TaskOutcomeRecord{Summary: outcomeSummary, PRURLs: []string{outcomePR}, HeadSHAs: []string{outcomeHead}}
}

// readSession is one named Session read, with or without the outcome opt-in.
func (l *live) readSession(name, token string, optIn bool) (int, []byte) {
	l.t.Helper()
	req, err := http.NewRequest(http.MethodGet, l.base+apiv1.SessionPath(name), nil)
	if err != nil {
		l.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if optIn {
		req.Header.Set(apiv1.TaskOutcomeHeader, apiv1.TaskOutcomeV1)
	}
	resp, err := l.http.Do(req)
	if err != nil {
		l.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		l.t.Fatal(err)
	}
	return resp.StatusCode, b
}

// awaitView reads the Session until ok accepts the answer. The API reads
// through the manager's cache first, which follows the store with a lag.
func (l *live) awaitView(name, token string, ok func(code int, s apiv1.Session) bool) apiv1.Session {
	l.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		code, b := l.readSession(name, token, true)
		var s apiv1.Session
		if code == http.StatusOK {
			s = unmarshal[apiv1.Session](l.t, b)
		}
		if ok(code, s) {
			return s
		}
		if time.Now().After(deadline) {
			l.t.Fatalf("session %s never reached the expected view: %d %s", name, code, b)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// show runs the real agent-run show against this API, as a person would.
func (l *live) show(token string, args ...string) (int, string) {
	l.t.Helper()
	dir := l.t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		l.t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	env := agentrun.DefaultEnv()
	env.Stdout, env.Stderr = &stdout, &stderr
	env.Stdin = strings.NewReader("")
	env.Getenv = func(string) string { return "" }
	env.SessionTokenFile = filepath.Join(dir, "absent-session")
	env.ServiceAccountDir = filepath.Join(dir, "absent-sa")
	env.LookPath = func(string) (string, error) { return "", errors.New("synthetic: no host command") }
	env.Run = func(context.Context, []string, io.Reader) (int, error) {
		return 0, errors.New("synthetic host process refused")
	}
	env.Output = func(context.Context, []string) ([]byte, error) {
		return nil, errors.New("synthetic host process refused")
	}
	env.ExecOutput = func(context.Context, []string) (int, string, string, error) {
		return 0, "", "", errors.New("synthetic host process refused")
	}
	env.PortForward = func(context.Context, []string) (string, func(), error) {
		return "", func() {}, errors.New("synthetic port-forward refused")
	}
	args = append(append([]string{"show"}, args...), "--api-url", l.base, "--token-file", tokenPath, "--ca-file", l.caFile)
	code := agentrun.Run(context.Background(), args, env)
	return code, stdout.String() + stderr.String()
}

func sameOutcome(got *protocol.TaskOutcome, o outcomeTask, r *protocol.TaskOutcomeRecord) bool {
	return got != nil && got.SessionUID == string(o.uid) && got.PodUID == string(o.pod.UID) && got.Record.Summary == r.Summary &&
		strings.Join(got.Record.PRURLs, ",") == strings.Join(r.PRURLs, ",") && strings.Join(got.Record.HeadSHAs, ",") == strings.Join(r.HeadSHAs, ",")
}

func setOutcomePhase(t *testing.T, l *live, name string, phase v1alpha1.SessionPhase) {
	t.Helper()
	var s v1alpha1.AgentSession
	if err := l.k8s.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	s.Status.Phase = phase
	if err := l.k8s.Status().Update(context.Background(), &s); err != nil {
		t.Fatalf("set phase %s: %v", phase, err)
	}
}

// TestEnvtestTaskOutcomeRetention: (a) a restarted API still serves the
// outcome; (b) it survives the pod's deletion and the Suspended and Archived
// phases, and a reap until the Session is released; (c) a missing, partial or
// contradicted outcome reads as exactly that; (d) the first snapshot cannot be
// replaced through the heartbeat or by a direct status write.
func TestEnvtestTaskOutcomeRetention(t *testing.T) {
	ctx := context.Background()
	var done, failed, missing, partial outcomeTask

	// The first API server records the outcomes, then stops: its manager,
	// cache and listener end with this subtest.
	t.Run("the original pods' heartbeats record the outcomes", func(t *testing.T) {
		l := startLive(t)
		human := l.token(humanSA, []string{apiv1.TokenAudience}, nil)
		done, failed, missing, partial = newOutcomeTask(t, l, human), newOutcomeTask(t, l, human), newOutcomeTask(t, l, human), newOutcomeTask(t, l, human)
		partialRecord := &protocol.TaskOutcomeRecord{Summary: "Blocked: no PR yet"}
		for _, c := range []struct {
			o      outcomeTask
			exit   int
			record *protocol.TaskOutcomeRecord
		}{{done, 0, fullRecord()}, {failed, 1, fullRecord()}, {missing, 0, nil}, {partial, 0, partialRecord}} {
			l.want(http.MethodPost, protocol.HeartbeatPath(c.o.name), c.o.agent, exitHeartbeat(c.o, c.exit, c.record), http.StatusNoContent)
		}
		var s v1alpha1.AgentSession
		if err := l.k8s.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: done.name}, &s); err != nil {
			t.Fatal(err)
		}
		if o := s.Status.TaskOutcome; o == nil || o.Record.Summary != outcomeSummary || o.PodUID != string(done.pod.UID) {
			t.Fatalf("status.taskOutcome in the store: %+v", s.Status.TaskOutcome)
		}

		// (d) A later heartbeat from the same original pod, with a different
		// record, does not replace the first snapshot.
		replacement := fullRecord()
		replacement.Summary = "replacement"
		l.want(http.MethodPost, protocol.HeartbeatPath(done.name), done.agent, exitHeartbeat(done, 0, replacement), http.StatusNoContent)
		if err := l.k8s.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: done.name}, &s); err != nil {
			t.Fatal(err)
		}
		if s.Status.TaskOutcome == nil || s.Status.TaskOutcome.Record.Summary != outcomeSummary {
			t.Fatalf("a later heartbeat replaced the first snapshot: %+v", s.Status.TaskOutcome)
		}
	})
	if t.Failed() {
		return
	}

	// (a) A new manager, cache, API server and listener over the same store.
	l := startLive(t)
	human := l.token(humanSA, []string{apiv1.TokenAudience}, nil)

	t.Run("a restarted API serves the first snapshot to a reader that asks", func(t *testing.T) {
		v := l.awaitView(done.name, human, func(code int, s apiv1.Session) bool { return code == http.StatusOK })
		if !sameOutcome(v.TaskOutcome, done, fullRecord()) {
			t.Fatalf("restarted API outcome: %+v", v.TaskOutcome)
		}
		if code, b := l.readSession(done.name, human, false); code != http.StatusOK || strings.Contains(string(b), `"taskOutcome"`) {
			t.Fatalf("a reader that did not ask got the outcome: %d %s", code, b)
		}
		code, out := l.show(human, done.name)
		if code != agentrun.ExitOK {
			t.Fatalf("agent-run show: exit %d\n%s", code, out)
		}
		contains := func(want ...string) {
			t.Helper()
			for _, w := range want {
				if !strings.Contains(out, w) {
					t.Errorf("show lacks %q:\n%s", w, out)
				}
			}
		}
		contains("task result:", "Succeeded (TaskSucceeded)", "Task outcome:", outcomeSummary, outcomePR, outcomeHead, "Actual model:", "Unknown", "Requested model:", task().Model)
	})

	// (b) The pod is deleted, then the phase moves to Suspended and Archived.
	t.Run("the outcome outlives the pod, suspend and archive", func(t *testing.T) {
		zero := int64(0)
		if err := l.k8s.Delete(ctx, done.pod, &client.DeleteOptions{GracePeriodSeconds: &zero}); err != nil {
			t.Fatal(err)
		}
		for _, phase := range []v1alpha1.SessionPhase{v1alpha1.PhaseSuspended, v1alpha1.PhaseArchived} {
			setOutcomePhase(t, l, done.name, phase)
			v := l.awaitView(done.name, human, func(code int, s apiv1.Session) bool { return code == http.StatusOK && s.Phase == string(phase) })
			if !sameOutcome(v.TaskOutcome, done, fullRecord()) {
				t.Fatalf("%s: outcome %+v", phase, v.TaskOutcome)
			}
			code, out := l.show(human, done.name)
			if code != agentrun.ExitOK || !strings.Contains(out, "phase:") || !strings.Contains(out, string(phase)) || !strings.Contains(out, "Task outcome:") || !strings.Contains(out, outcomeHead) {
				t.Fatalf("%s: agent-run show exit %d:\n%s", phase, code, out)
			}
		}
	})

	// (c) Missing, partial and contradicted evidence reads as exactly that.
	t.Run("missing, partial and failed evidence stays honest", func(t *testing.T) {
		v := l.awaitView(missing.name, human, func(code int, s apiv1.Session) bool { return code == http.StatusOK })
		if v.TaskOutcome != nil {
			t.Fatalf("no outcome file, yet an outcome: %+v", v.TaskOutcome)
		}
		code, out := l.show(human, missing.name)
		if code != agentrun.ExitOK || strings.Contains(out, "Task outcome") || !strings.Contains(out, "Succeeded (TaskSucceeded)") {
			t.Fatalf("missing outcome: exit %d\n%s", code, out)
		}

		v = l.awaitView(partial.name, human, func(code int, s apiv1.Session) bool { return code == http.StatusOK })
		if v.TaskOutcome == nil || v.TaskOutcome.Record.Summary != "Blocked: no PR yet" || len(v.TaskOutcome.Record.PRURLs) != 0 || len(v.TaskOutcome.Record.HeadSHAs) != 0 {
			t.Fatalf("partial outcome: %+v", v.TaskOutcome)
		}
		if code, out = l.show(human, partial.name); code != agentrun.ExitOK || !strings.Contains(out, "Blocked: no PR yet") || strings.Contains(out, "  PR:") || strings.Contains(out, "  head:") {
			t.Fatalf("partial outcome show: exit %d\n%s", code, out)
		}

		// The task failed while its outcome file claimed readiness: both are
		// shown, and the verdict stays Failed.
		v = l.awaitView(failed.name, human, func(code int, s apiv1.Session) bool { return code == http.StatusOK })
		if !sameOutcome(v.TaskOutcome, failed, fullRecord()) || v.AgentStatus == nil || v.AgentStatus.Task == nil || v.AgentStatus.Task.ExitCode != 1 {
			t.Fatalf("failed task view: %+v %+v", v.TaskOutcome, v.AgentStatus)
		}
		if code, out = l.show(human, failed.name); code != agentrun.ExitOK || !strings.Contains(out, "Failed (TaskExitedNonZero)") || strings.Contains(out, "Succeeded") || !strings.Contains(out, outcomeSummary) {
			t.Fatalf("failed task show: exit %d\n%s", code, out)
		}
	})

	// (d) The CRD refuses a direct status write that replaces or erases the
	// first snapshot (the full schema set is TestTaskOutcomeSchema).
	t.Run("a direct status write cannot replace or erase the first snapshot", func(t *testing.T) {
		for _, kind := range []string{"replace", "erase"} {
			var s v1alpha1.AgentSession
			if err := l.k8s.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: done.name}, &s); err != nil {
				t.Fatal(err)
			}
			if kind == "replace" {
				s.Status.TaskOutcome.Record.Summary = "replacement"
			} else {
				s.Status.TaskOutcome = nil
			}
			if err := l.k8s.Status().Update(ctx, &s); !apierrors.IsInvalid(err) {
				t.Fatalf("%s: the schema allowed it: %v", kind, err)
			}
		}
		v := l.awaitView(done.name, human, func(code int, s apiv1.Session) bool { return code == http.StatusOK })
		if !sameOutcome(v.TaskOutcome, done, fullRecord()) {
			t.Fatalf("outcome after refused writes: %+v", v.TaskOutcome)
		}
	})

	// (b) A reap keeps the outcome readable while the finalizer holds. Once
	// the Session is released, as releaseSession does for a Session that is
	// not private-history protected, the outcome is gone with it: 404, and
	// agent-run show exits 4. Nothing reads it as success.
	t.Run("a reap keeps it until the Session is released, then it is gone", func(t *testing.T) {
		r := unmarshal[apiv1.Session](t, l.want(http.MethodDelete, apiv1.SessionPath(done.name), human, nil, http.StatusAccepted))
		if !r.Reaping {
			t.Fatalf("reap answer %+v", r)
		}
		v := l.awaitView(done.name, human, func(code int, s apiv1.Session) bool { return code == http.StatusOK && s.Reaping })
		if !sameOutcome(v.TaskOutcome, done, fullRecord()) {
			t.Fatalf("reaping: outcome %+v", v.TaskOutcome)
		}
		var s v1alpha1.AgentSession
		if err := l.k8s.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: done.name}, &s); err != nil {
			t.Fatal(err)
		}
		controllerutil.RemoveFinalizer(&s, controller.Finalizer)
		if err := l.k8s.Update(ctx, &s); err != nil {
			t.Fatal(err)
		}
		l.awaitView(done.name, human, func(code int, _ apiv1.Session) bool { return code == http.StatusNotFound })
		code, out := l.show(human, done.name)
		if code != agentrun.ExitNotFound || strings.Contains(out, "Task outcome") || strings.Contains(out, "Succeeded") {
			t.Fatalf("released session: exit %d\n%s", code, out)
		}
	})

	// Release the other Sessions the suite held.
	for _, o := range []outcomeTask{failed, missing, partial} {
		var s v1alpha1.AgentSession
		if err := l.k8s.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: o.name}, &s); err == nil {
			controllerutil.RemoveFinalizer(&s, controller.Finalizer)
			if err := l.k8s.Update(ctx, &s); err != nil && !apierrors.IsNotFound(err) {
				t.Error(err)
			}
		}
		_ = l.k8s.Delete(ctx, o.pod)
	}
}

// TestEnvtestTaskOutcomeAfterReleaseGap is the gap, RED on purpose: after a
// reap releases a Session that is not private-history protected, no API route
// still serves its retained outcome. The Session object is the only durable
// copy (status.taskOutcome); releaseSession (internal/controller/reconciler.go)
// lifts the last finalizer and the object, with the outcome, is deleted. The
// bounded proposal is in docs/workflow-guide.md, "Where the retained outcome
// lives". Unskip this when a supported route keeps it.
func TestEnvtestTaskOutcomeAfterReleaseGap(t *testing.T) {
	t.Skip("gap (OP402): a released Session's retained outcome is deleted with it; no supported route serves it after release")
	ctx := context.Background()
	l := startLive(t)
	human := l.token(humanSA, []string{apiv1.TokenAudience}, nil)
	o := newOutcomeTask(t, l, human)
	l.want(http.MethodPost, protocol.HeartbeatPath(o.name), o.agent, exitHeartbeat(o, 0, fullRecord()), http.StatusNoContent)
	zero := int64(0)
	if err := l.k8s.Delete(ctx, o.pod, &client.DeleteOptions{GracePeriodSeconds: &zero}); err != nil {
		t.Fatal(err)
	}
	l.want(http.MethodDelete, apiv1.SessionPath(o.name), human, nil, http.StatusAccepted)
	var s v1alpha1.AgentSession
	if err := l.k8s.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: o.name}, &s); err != nil {
		t.Fatal(err)
	}
	controllerutil.RemoveFinalizer(&s, controller.Finalizer)
	if err := l.k8s.Update(ctx, &s); err != nil {
		t.Fatal(err)
	}
	if err := l.k8s.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: o.name}, &s); !apierrors.IsNotFound(err) {
		t.Fatalf("the released Session still exists: %v", err)
	}
	time.Sleep(time.Second) // let the API's cache see the delete
	code, b := l.readSession(o.name, human, true)
	var v apiv1.Session
	if code == http.StatusOK {
		_ = json.Unmarshal(b, &v)
	}
	if !sameOutcome(v.TaskOutcome, o, fullRecord()) {
		t.Fatalf("after release the retained outcome is not served: %d %s", code, b)
	}
}
