package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/templates"
)

type rcExec struct {
	calls       []string
	fail        string
	beforeStart func()
	now         time.Time
}

func (e *rcExec) Run(_ context.Context, ns, pod, container string, args []string, in io.Reader, out, stderr io.Writer) error {
	if ns != sessionNS || pod != "rc-host" || container != "agent" || len(args) != 4 || strings.Join(args[:3], " ") != "agentd ctl codex-thread" {
		panic("unexpected exec")
	}
	e.calls = append(e.calls, args[3])
	var d apiv1.CodexRCDispatch
	if json.NewDecoder(in).Decode(&d) != nil {
		panic("bad stdin")
	}
	if args[3] == "start" && e.beforeStart != nil {
		e.beforeStart()
	}
	if args[3] == e.fail {
		return errors.New("SECRET PROVIDER STDERR")
	}
	rec := apiv1.CodexRCReceipt{RequestID: d.Request.RequestID, ClientSHA256: d.ClientSHA256, DispatchSHA256: apiv1.CodexRCHash(d), Target: d.Target, Model: d.Request.Model, Effort: d.Request.Effort, State: "qualified", Reason: "qualified", StartedAt: e.now, FinishedAt: e.now}
	if args[3] == "start" {
		rec.State, rec.Reason, rec.ThreadID, rec.TurnID = "confirmed", "thread_and_turn_acknowledged", "fresh-thread", "first-turn"
	}
	return json.NewEncoder(out).Encode(rec)
}
func rcFixture(t *testing.T) (*fixture, apiv1.CodexRCRequest, *rcExec) {
	t.Helper()
	f := coordinatorFixture(t)
	f.now = time.Now().UTC()
	bindCatalogFixture(t, f)
	f.sessionPod("rc-host", "dev", 0)
	key := types.NamespacedName{Namespace: sessionNS, Name: "rc-host"}
	var sess v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), key, &sess); err != nil {
		t.Fatal(err)
	}
	sess.Spec.Agent, sess.Spec.Mode, sess.Spec.Prompt = "codex", "remote", ""
	if err := f.c.Update(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), key, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := f.c.Status().Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := f.srv.Projects.snapshot(context.Background(), f.c, "sample", "alias")
	if err != nil {
		t.Fatal(err)
	}
	// Hash the canonical accepted snapshot, not a caller object.
	target := apiv1.CodexRCTarget{SessionName: sess.Name, SessionUID: string(sess.UID), PodUID: string(pod.UID), Project: "sample", Repository: "alias", SnapshotSHA256: apiv1.CodexRCHash(json.RawMessage(snapshot)), Root: "/home/dev/work/accepted", RootDevice: 1, RootInode: 2, PreparationSHA256: strings.Repeat("a", 64), PID: 123, BootID: "boot", StartTicks: 42, InstallationID: "installation", EnvironmentID: "environment", PairingSource: "supported-client", PairingSHA256: strings.Repeat("b", 64), VerifiedAt: f.now.Add(-time.Minute), ExpiresAt: f.now.Add(5 * time.Minute)}
	f.srv.CodexRCEnabled = true
	f.srv.CodexRCTarget = &target
	e := &rcExec{now: f.now}
	f.srv.Exec = e
	f.h = f.srv.Handler()
	req := apiv1.CodexRCRequest{RequestID: "fresh-request", SessionUID: target.SessionUID, PodUID: target.PodUID, Project: target.Project, Repository: target.Repository, Model: "gpt-6.1-sol", Effort: "medium", Handoff: "bounded initial handoff", Deadline: f.now.Add(25 * time.Second)}
	return f, req, e
}

const rcPath = "/v1/sessions/rc-host/codex-thread"

func TestCodexRCDefaultOffAndCallerFence(t *testing.T) {
	f, req, e := rcFixture(t)
	f.srv.CodexRCEnabled = false
	f.h = f.srv.Handler()
	if w := f.do("POST", rcPath, tokHuman, req); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(e.calls) != 0 {
		t.Fatal(e.calls)
	}
	for _, who := range []string{"human", "client", "workbench", "worker", "coordinator"} {
		t.Run(who, func(t *testing.T) {
			f, req, e := rcFixture(t)
			token := tokHuman
			switch who {
			case "client":
				token = tokClient
			case "worker":
				token = f.sessionPod("worker", "dev", 0)
			case "coordinator":
				token = tokCoordinator
			case "workbench":
				token = f.sessionPod("workbench", "workbench", 0)
				id := f.auth[token]
				id.ServiceAccount = templates.WorkbenchServiceAccount
				f.auth[token] = id
				var pod corev1.Pod
				_ = f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "workbench"}, &pod)
				pod.Spec.ServiceAccountName = templates.WorkbenchServiceAccount
				if err := f.c.Update(context.Background(), &pod); err != nil {
					t.Fatal(err)
				}
				f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
			}
			f.h = f.srv.Handler()
			w := f.do("POST", rcPath, token, req)
			if who == "worker" || who == "coordinator" {
				if w.Code != 403 || len(e.calls) != 0 {
					t.Fatal(w.Code, w.Body.String(), e.calls)
				}
				return
			}
			if w.Code != 201 || strings.Join(e.calls, ",") != "preflight,start" {
				t.Fatal(w.Code, w.Body.String(), e.calls)
			}
		})
	}
}
func TestCodexRCTargetAndMalformedRefusals(t *testing.T) {
	for _, mode := range []string{"uid", "pod", "repo", "catalog", "pairing", "expired", "mode", "unknown", "duplicate", "huge", "model", "effort", "deadline", "empty"} {
		t.Run(mode, func(t *testing.T) {
			f, req, e := rcFixture(t)
			var body any = req
			switch mode {
			case "uid":
				req.SessionUID = "wrong"
			case "pod":
				req.PodUID = "wrong"
			case "repo":
				req.Repository = "second"
			case "catalog":
				f.srv.CodexRCTarget.SnapshotSHA256 = strings.Repeat("c", 64)
			case "pairing":
				f.srv.CodexRCTarget.PairingSource = "connected"
			case "expired":
				f.srv.CodexRCTarget.ExpiresAt = f.now
			case "mode":
				var sess v1alpha1.AgentSession
				key := types.NamespacedName{Namespace: sessionNS, Name: "rc-host"}
				_ = f.c.Get(context.Background(), key, &sess)
				sess.Spec.Mode = "task"
				_ = f.c.Update(context.Background(), &sess)
			case "unknown":
				body = `{"cwd":"/tmp"}`
			case "duplicate":
				body = `{"requestID":"a","requestID":"b"}`
			case "huge":
				body = strings.Repeat(" ", apiv1.CodexRCMaxRequest+1)
			case "model":
				req.Model = "sol"
			case "effort":
				req.Effort = "none"
			case "deadline":
				req.Deadline = f.now.Add(time.Hour)
			case "empty":
				req.Handoff = ""
			}
			if _, ok := body.(apiv1.CodexRCRequest); ok {
				body = req
			}
			w := f.do("POST", rcPath, tokHuman, body)
			if w.Code < 400 || len(e.calls) != 0 {
				t.Fatal(w.Code, w.Body.String(), e.calls)
			}
		})
	}
}
func TestCodexRCOneUseSurvivesServerRestartAndUncertainty(t *testing.T) {
	for _, fail := range []string{"", "start"} {
		t.Run("lost-"+fail, func(t *testing.T) {
			f, req, e := rcFixture(t)
			e.fail = fail
			w := f.do("POST", rcPath, tokHuman, req)
			if fail == "" && w.Code != 201 || fail != "" && w.Code != 503 {
				t.Fatal(w.Code, w.Body.String())
			}
			var sess v1alpha1.AgentSession
			key := types.NamespacedName{Namespace: sessionNS, Name: "rc-host"}
			if err := f.c.Get(context.Background(), key, &sess); err != nil {
				t.Fatal(err)
			}
			if string(sess.UID) != req.SessionUID || sess.Annotations[apiv1.CodexRCIntentAnnotation] == "" {
				t.Fatal("identity/fence lost")
			}
			// A new handler and changed target readiness must still use the retained receipt.
			f.srv.Now = func() time.Time { return f.now.Add(time.Hour) }
			f.h = f.srv.Handler()
			w = f.do("POST", rcPath, tokHuman, req)
			want := 200
			if fail != "" {
				want = 409
			}
			if w.Code != want || len(e.calls) != 2 {
				t.Fatal(w.Code, w.Body.String(), e.calls)
			}
			req.Handoff += "changed"
			if w = f.do("POST", rcPath, tokHuman, req); w.Code != 409 {
				t.Fatal(w.Code)
			}
			req.RequestID = "new-key"
			if w = f.do("POST", rcPath, tokHuman, req); w.Code != 409 || len(e.calls) != 2 {
				t.Fatal(w.Code, e.calls)
			}
		})
	}
}
