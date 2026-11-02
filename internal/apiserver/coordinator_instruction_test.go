package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const instructionAttempt = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
const instructionAttemptNext = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
const instructionText = "clarify the assigned outcome\n"

func TestCoordinatorInstructionParentBoundAtCreate(t *testing.T) {
	post := func(f *fixture, token string, body any) *v1alpha1.AgentSession {
		t.Helper()
		w := f.do(http.MethodPost, apiv1.SessionsPath, token, body)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %d %s", w.Code, w.Body.String())
		}
		return f.session(decode[apiv1.Session](t, w).Name)
	}
	f := newFixture(t)
	f.srv.ChildUsageCapture = false
	token := f.sessionPod("cap-parent", "dev", 0)
	child := post(f, token, task())
	if child.Spec.CoordinatorInstructionParentUID != string(f.session("cap-parent").UID) || child.Spec.UsageCaptureParentUID != "" || child.Spec.Parent != "cap-parent" {
		t.Fatalf("binding parent %q usage %q name %q", child.Spec.CoordinatorInstructionParentUID, child.Spec.UsageCaptureParentUID, child.Spec.Parent)
	}
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, token, `{"repo":"haynes-ops","agent":"claude","mode":"task","model":"claude-opus-5-5","prompt":"p","coordinatorInstructionParentUID":"forged"}`), http.StatusBadRequest, apiv1.CodeBadRequest)
	if post(f, tokHuman, task()).Spec.CoordinatorInstructionParentUID != "" || post(f, tokClient, task()).Spec.CoordinatorInstructionParentUID != "" {
		t.Fatal("human or client create stored an instruction parent")
	}
}

func TestCoordinatorInstructionDeliveryAndConsumption(t *testing.T) {
	f, parentTok, childTok, childUID, podUID := instructionReady(t, "parent", "child", "task:coord-instruction")
	req := instructionRequest(childUID, podUID, "correction-1", "task:coord-instruction", instructionText)
	w := f.do(http.MethodPost, apiv1.SessionInstructionsPath("child"), parentTok, req)
	if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), `"nonce"`) || strings.Contains(w.Body.String(), `"text"`) || strings.Contains(w.Body.String(), "sender") {
		t.Fatalf("delivery %d %s", w.Code, w.Body.String())
	}
	receipt := decode[apiv1.CoordinatorInstructionReceipt](t, w)
	if receipt.Outcome != apiv1.InstructionOutcomeCreated || receipt.Consumed || receipt.Key != req.Key || receipt.MessageUID == "" || receipt.AttemptID != instructionAttempt || receipt.TargetSessionUID != childUID || receipt.TargetPodUID != podUID || receipt.TaskKey != req.TaskKey {
		t.Fatalf("receipt %+v", receipt)
	}
	again := f.do(http.MethodPost, apiv1.SessionInstructionsPath("child"), parentTok, req)
	if again.Code != http.StatusOK || decode[apiv1.CoordinatorInstructionReceipt](t, again).MessageUID != receipt.MessageUID || decode[apiv1.CoordinatorInstructionReceipt](t, again).Consumed {
		t.Fatalf("replay %d %s", again.Code, again.Body.String())
	}
	changed := req
	changed.Text = "a different correction\n"
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionsPath("child"), parentTok, changed), http.StatusConflict, apiv1.CodeConflict)
	parentView := decode[apiv1.CoordinatorInstructionView](t, f.do(http.MethodGet, apiv1.SessionInstructionPath("child", req.Key), parentTok, nil))
	if parentView.Nonce != "" || parentView.Text != "" || parentView.ReadAck || parentView.Consumed || parentView.MessageUID != receipt.MessageUID {
		t.Fatalf("parent read %+v", parentView)
	}
	childView := decode[apiv1.CoordinatorInstructionView](t, f.do(http.MethodGet, apiv1.SessionInstructionPath("child", req.Key), childTok, nil))
	if childView.Text != instructionText || len(childView.Nonce) != 64 || childView.ReadAck || childView.Consumed {
		t.Fatalf("child read %+v", childView)
	}
	ackPath := apiv1.InboxPath + "/" + receipt.MessageName + "/ack?project=sample"
	if w := f.do(http.MethodGet, ackPath, childTok, nil); w.Code != http.StatusNotFound {
		t.Fatalf("read created an inbox ack %d %s", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodPost, ackPath, childTok, struct{}{}); w.Code != http.StatusCreated {
		t.Fatalf("inbox ack %d %s", w.Code, w.Body.String())
	}
	afterAck := decode[apiv1.CoordinatorInstructionView](t, f.do(http.MethodGet, apiv1.SessionInstructionPath("child", req.Key), childTok, nil))
	if !afterAck.ReadAck || afterAck.Consumed || afterAck.Nonce != childView.Nonce {
		t.Fatalf("inbox ack was treated as consumption %+v", afterAck)
	}
	bad := apiv1.CoordinatorInstructionConsumeRequest{AttemptID: instructionAttempt, Nonce: strings.Repeat("ab", 32), TargetSessionUID: childUID, TargetPodUID: podUID}
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionConsumePath("child", req.Key), childTok, bad), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionConsumePath("child", req.Key), parentTok, apiv1.CoordinatorInstructionConsumeRequest{AttemptID: instructionAttempt, Nonce: childView.Nonce, TargetSessionUID: childUID, TargetPodUID: podUID}), http.StatusNotFound, apiv1.CodeNotFound)
	if decode[apiv1.CoordinatorInstructionView](t, f.do(http.MethodGet, apiv1.SessionInstructionPath("child", req.Key), childTok, nil)).Consumed {
		t.Fatal("refused consume stored a receipt")
	}
	consume := apiv1.CoordinatorInstructionConsumeRequest{AttemptID: instructionAttempt, Nonce: childView.Nonce, TargetSessionUID: childUID, TargetPodUID: podUID}
	w = f.do(http.MethodPost, apiv1.SessionInstructionConsumePath("child", req.Key), childTok, consume)
	if w.Code != http.StatusCreated {
		t.Fatalf("consume %d %s", w.Code, w.Body.String())
	}
	ack := decode[apiv1.CoordinatorInstructionConsumeACK](t, w)
	if ack.Outcome != apiv1.InstructionOutcomeConsumed || ack.Key != req.Key || ack.MessageUID != receipt.MessageUID || ack.AttemptID != instructionAttempt || ack.Nonce != childView.Nonce || ack.TextSHA256 != receipt.TextSHA256 || ack.TargetSessionUID != childUID || ack.TargetPodUID != podUID {
		t.Fatalf("ack %+v", ack)
	}
	w = f.do(http.MethodPost, apiv1.SessionInstructionConsumePath("child", req.Key), childTok, consume)
	if w.Code != http.StatusOK || decode[apiv1.CoordinatorInstructionConsumeACK](t, w).Outcome != apiv1.InstructionOutcomeConfirmed || decode[apiv1.CoordinatorInstructionConsumeACK](t, w).MessageUID != ack.MessageUID {
		t.Fatalf("consume replay %d %s", w.Code, w.Body.String())
	}
	if !decode[apiv1.CoordinatorInstructionView](t, f.do(http.MethodGet, apiv1.SessionInstructionPath("child", req.Key), parentTok, nil)).Consumed {
		t.Fatal("parent read lost consumption")
	}
}

func TestCoordinatorInstructionRefusals(t *testing.T) {
	f, parentTok, _, childUID, podUID := instructionReady(t, "parent", "child", "task:coord-instruction")
	req := instructionRequest(childUID, podUID, "correction-1", "task:coord-instruction", instructionText)
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionsPath("child"), parentTok, `{"key":"correction-1","project":"sample","repository":"alias","taskKey":"task:coord-instruction","targetSessionUID":"`+childUID+`","targetPodUID":"`+podUID+`","attemptID":"`+instructionAttempt+`","text":"x","sender":"parent"}`), http.StatusBadRequest, apiv1.CodeBadRequest)
	peer := instructionChild(t, f, "peer", "parent", "task:peer-task")
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionsPath("child"), peer.token, req), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionsPath("child"), tokHuman, req), http.StatusNotFound, apiv1.CodeNotFound)
	other := instructionChild(t, f, "other", "parent", "task:other-child")
	cross := instructionRequest(other.uid, other.pod, "correction-1", "task:coord-instruction", instructionText)
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionsPath("other"), parentTok, cross), http.StatusNotFound, apiv1.CodeNotFound)
	wider := req
	wider.Key = "wider"
	wider.TaskKey = "task:wider-task"
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionsPath("child"), parentTok, wider), http.StatusNotFound, apiv1.CodeNotFound)
	repo := req
	repo.Key = "repo"
	repo.Repository = "second"
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionsPath("child"), parentTok, repo), http.StatusNotFound, apiv1.CodeNotFound)
	staleTarget := req
	staleTarget.Key = "stale-target"
	staleTarget.TargetSessionUID = "stale-session-uid"
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionsPath("child"), parentTok, staleTarget), http.StatusNotFound, apiv1.CodeNotFound)
	stalePod := req
	stalePod.Key = "stale-pod"
	stalePod.TargetPodUID = "stale-pod-uid"
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionsPath("child"), parentTok, stalePod), http.StatusNotFound, apiv1.CodeNotFound)
	staleAttempt := req
	staleAttempt.Key = "stale-attempt"
	staleAttempt.AttemptID = instructionAttemptNext
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionsPath("child"), parentTok, staleAttempt), http.StatusNotFound, apiv1.CodeNotFound)
	orphan := instructionChild(t, f, "orphan", "parent", "task:orphan-task")
	stored := f.session("orphan")
	stored.Spec.CoordinatorInstructionParentUID = "stale-parent-uid"
	if err := f.c.Update(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionsPath("orphan"), parentTok, instructionRequest(orphan.uid, orphan.pod, "correction-1", "task:orphan-task", instructionText)), http.StatusNotFound, apiv1.CodeNotFound)
	absent := instructionChild(t, f, "absent", "parent", "task:absent-task")
	cleared := f.session("absent")
	cleared.Spec.CoordinatorInstructionParentUID = ""
	if err := f.c.Update(context.Background(), cleared); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionsPath("absent"), parentTok, instructionRequest(absent.uid, absent.pod, "correction-1", "task:absent-task", instructionText)), http.StatusNotFound, apiv1.CodeNotFound)
	ended := instructionChild(t, f, "ended", "parent", "task:ended-task")
	if w := f.do(http.MethodPost, apiv1.SessionInstructionAttemptPath("ended"), ended.token, apiv1.CoordinatorInstructionAttemptRequest{AttemptID: instructionAttempt, Ended: true}); w.Code != http.StatusOK {
		t.Fatalf("end attempt %d %s", w.Code, w.Body.String())
	}
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionsPath("ended"), parentTok, instructionRequest(ended.uid, ended.pod, "correction-1", "task:ended-task", instructionText)), http.StatusNotFound, apiv1.CodeNotFound)
	stopped := instructionChild(t, f, "stopped", "parent", "task:stopped-task")
	session := f.session("stopped")
	session.Status.Agent.Status = protocol.AgentExited
	if err := f.c.Status().Update(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionsPath("stopped"), parentTok, instructionRequest(stopped.uid, stopped.pod, "correction-1", "task:stopped-task", instructionText)), http.StatusNotFound, apiv1.CodeNotFound)
	if len(f.session("child").Status.CoordinatorInstructions) != 0 {
		t.Fatal("refused requests stored a delivery")
	}
}

type instructionChildBinding struct {
	token, uid, pod string
}

func instructionReady(t *testing.T, parent, child, taskKey string) (*fixture, string, string, string, string) {
	t.Helper()
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	enableClaims(f)
	parentTok := claimSession(t, f, parent, humanSA)
	got := instructionChild(t, f, child, parent, taskKey)
	return f, parentTok, got.token, got.uid, got.pod
}

func instructionChild(t *testing.T, f *fixture, name, parent, taskKey string) instructionChildBinding {
	t.Helper()
	token := claimSession(t, f, name, parent)
	session := f.session(name)
	session.Spec.CoordinatorInstructionParentUID = string(f.session(parent).UID)
	if err := f.c.Update(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	session = f.session(name)
	session.Status.Agent.Status = protocol.AgentBusy
	if err := f.c.Status().Update(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodPost, apiv1.TaskClaimsPath, token, claimRequest(taskKey)); w.Code != http.StatusCreated {
		t.Fatalf("claim %s %d %s", name, w.Code, w.Body.String())
	}
	if w := f.do(http.MethodPost, apiv1.SessionInstructionAttemptPath(name), token, apiv1.CoordinatorInstructionAttemptRequest{AttemptID: instructionAttempt}); w.Code != http.StatusOK {
		t.Fatalf("attempt %s %d %s", name, w.Code, w.Body.String())
	}
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &pod); err != nil {
		t.Fatal(err)
	}
	return instructionChildBinding{token: token, uid: string(f.session(name).UID), pod: string(pod.UID)}
}

func instructionRequest(uid, pod, key, taskKey, text string) apiv1.CoordinatorInstructionRequest {
	return apiv1.CoordinatorInstructionRequest{
		Key: key, Project: "sample", Repository: "alias", TaskKey: taskKey,
		TargetSessionUID: uid, TargetPodUID: pod, AttemptID: instructionAttempt, Text: text,
	}
}
