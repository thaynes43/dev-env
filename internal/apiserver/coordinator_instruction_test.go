package apiserver

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
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
	parentUID := string(f.session("parent").UID)
	parentView := decode[apiv1.CoordinatorInstructionView](t, f.do(http.MethodGet, apiv1.SessionInstructionPath("child", req.Key), parentTok, nil))
	if parentView.Nonce != "" || parentView.Text != "" || parentView.ReadAck || parentView.Consumed || parentView.MessageUID != receipt.MessageUID || parentView.SenderUID != parentUID {
		t.Fatalf("parent read %+v", parentView)
	}
	childView := decode[apiv1.CoordinatorInstructionView](t, f.do(http.MethodGet, apiv1.SessionInstructionPath("child", req.Key), childTok, nil))
	if childView.Text != instructionText || len(childView.Nonce) != 64 || childView.ReadAck || childView.Consumed || childView.SenderUID != parentUID {
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
	return instructionBound(t, f, name, parent, taskKey, instructionAttempt)
}

func instructionBound(t *testing.T, f *fixture, name, parent, taskKey, attempt string) instructionChildBinding {
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
	if attempt != "" {
		if w := f.do(http.MethodPost, apiv1.SessionInstructionAttemptPath(name), token, apiv1.CoordinatorInstructionAttemptRequest{AttemptID: attempt}); w.Code != http.StatusOK {
			t.Fatalf("attempt %s %d %s", name, w.Code, w.Body.String())
		}
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

func TestSyntheticInstructionCheckpointAfterLaunch(t *testing.T) {
	const text = "synthetic correction posted after launch\n"
	f, parentTok, childTok, childUID, podUID := instructionReady(t, "parent", "child", "task:coord-instruction")
	parentUID := string(f.session("parent").UID)
	empty := decode[apiv1.CoordinatorInstructionCheckpoint](t, f.do(http.MethodGet, apiv1.SessionInstructionsPath("child"), childTok, nil))
	if empty.Outcome != apiv1.InstructionOutcomeConfirmed || empty.AttemptID != instructionAttempt || len(empty.Pending) != 0 || empty.SessionUID != childUID || empty.PodUID != podUID {
		t.Fatalf("checkpoint before a correction %+v", empty)
	}
	wantError(t, f.do(http.MethodGet, apiv1.SessionInstructionsPath("child"), parentTok, nil), http.StatusNotFound, apiv1.CodeNotFound)

	var consumePosts int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/consume") {
			consumePosts++
		}
		if r.Method == http.MethodPost && r.URL.Path == apiv1.SessionInstructionsPath("child") && (strings.Contains(string(body), `"sender"`) || strings.Contains(string(body), `"from"`)) {
			t.Errorf("send body carried a sender: %s", body)
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		f.h.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	state := filepath.Join(home, ".agentd")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "ca.crt")
	tokenPath := filepath.Join(dir, "helper-token")
	cliToken := filepath.Join(dir, "cli-token")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"version": 1,
		"binding": map[string]any{
			"invocationID": instructionAttempt, "launchDigest": strings.Repeat("ab", 32),
			"session": "child", "sessionUID": childUID, "podUID": podUID,
			"bootID": "boot-synthetic", "threadID": "", "generation": 1,
		},
		"phase": "Running", "startedAt": time.Now().UTC(), "finishedAt": time.Time{},
		"leaderReaped": false, "groupAbsent": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "native-invocation.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte(childTok), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := agentd.Settings{Home: home, StateDir: state, PodUID: podUID, APIURL: server.URL, APITokenFile: tokenPath, APICAFile: caPath}
	helper, err := agentd.NewInboxClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := helper.RegisterInstructionAttempt(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	before, err := helper.CheckpointInstructions(context.Background(), settings)
	if err != nil || len(before.Pending) != 0 || consumePosts != 0 {
		t.Fatalf("native helper checkpoint before send %+v posts %d err %v", before, consumePosts, err)
	}
	run := func(token, stdin string, want int, args ...string) string {
		t.Helper()
		if err := os.WriteFile(cliToken, []byte(token), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr strings.Builder
		env := agentrun.DefaultEnv()
		env.Stdout, env.Stderr = &stdout, &stderr
		env.Stdin = strings.NewReader(stdin)
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
		args = append(append([]string{}, args...), "--api-url", server.URL, "--token-file", cliToken, "--ca-file", caPath)
		if code := agentrun.Run(context.Background(), args, env); code != want {
			t.Fatalf("%v exit %d want %d\nstdout %s\nstderr %s", args, code, want, stdout.String(), stderr.String())
		}
		return stdout.String()
	}
	send := run(parentTok, text, agentrun.ExitOK, "instruction", "send", "--session", "child", "--key", "after-launch", "--project", "sample", "--repository", "alias", "--task-key", "task:coord-instruction", "--expect-uid", childUID, "--expect-pod", podUID, "--attempt", instructionAttempt)
	if strings.Contains(send, text) || strings.Contains(send, `"nonce"`) || strings.Contains(send, "sender") {
		t.Fatalf("send disclosed text, nonce or sender: %s", send)
	}
	if f.session("child").Status.CoordinatorInstructions[0].Consumed != nil || consumePosts != 0 {
		t.Fatal("delivery or checkpoint consumed the correction")
	}
	found, err := helper.CheckpointInstructions(context.Background(), settings)
	if err != nil || len(found.Pending) != 1 || found.Pending[0].Key != "after-launch" || found.Pending[0].SenderUID != parentUID || found.Pending[0].Text != text || found.Pending[0].Consumed || consumePosts != 0 {
		t.Fatalf("native helper missed the posted correction %+v posts %d err %v", found, consumePosts, err)
	}
	listed := run(childTok, "", agentrun.ExitOK, "instruction", "checkpoint", "--session", "child")
	var checkpoint apiv1.CoordinatorInstructionCheckpoint
	if json.Unmarshal([]byte(listed), &checkpoint) != nil || len(checkpoint.Pending) != 1 || checkpoint.Pending[0].MessageUID != found.Pending[0].MessageUID || checkpoint.Pending[0].TextSHA256 != found.Pending[0].TextSHA256 || checkpoint.Pending[0].Nonce != found.Pending[0].Nonce || checkpoint.Pending[0].SenderUID != parentUID {
		t.Fatalf("cli checkpoint %s", listed)
	}
	if consumePosts != 0 {
		t.Fatal("checkpoint consumed")
	}
	item := checkpoint.Pending[0]
	run(childTok, "", agentrun.ExitOK, "instruction", "consume", "--session", "child", "--key", item.Key, "--attempt", item.AttemptID, "--nonce", item.Nonce, "--message-uid", item.MessageUID, "--text-sha256", item.TextSHA256, "--expect-uid", item.TargetSessionUID, "--expect-pod", item.TargetPodUID)
	if consumePosts != 1 || f.session("child").Status.CoordinatorInstructions[0].Consumed == nil {
		t.Fatalf("explicit consume did not store the receipt posts %d", consumePosts)
	}
	after, err := helper.CheckpointInstructions(context.Background(), settings)
	if err != nil || len(after.Pending) != 0 {
		t.Fatalf("consumed correction stayed pending %+v %v", after, err)
	}
	run(parentTok, "", agentrun.ExitNotFound, "instruction", "checkpoint", "--session", "child")
}

func TestInstructionAttemptRejectsActiveSwitchAndEndedReplay(t *testing.T) {
	f, _, childTok, _, _ := instructionReady(t, "parent", "child", "task:coord-instruction")
	path := apiv1.SessionInstructionAttemptPath("child")
	wantError(t, f.do(http.MethodPost, path, childTok, apiv1.CoordinatorInstructionAttemptRequest{AttemptID: instructionAttemptNext}), http.StatusNotFound, apiv1.CodeNotFound)
	if got := f.session("child").Status.CoordinatorInstructionAttempt; got == nil || got.AttemptID != instructionAttempt || got.Phase != apiv1.InstructionPhaseActive {
		t.Fatalf("active attempt was replaced: %+v", got)
	}
	if w := f.do(http.MethodPost, path, childTok, apiv1.CoordinatorInstructionAttemptRequest{AttemptID: instructionAttempt, Ended: true}); w.Code != http.StatusOK {
		t.Fatalf("end %d %s", w.Code, w.Body.String())
	}
	ended := f.session("child")
	if ended.Status.CoordinatorInstructionAttempt.Phase != apiv1.InstructionPhaseEnded || !instructionLedgerHas(ended, instructionAttempt) {
		t.Fatalf("end did not retain the attempt: %+v ledger %+v", ended.Status.CoordinatorInstructionAttempt, ended.Status.CoordinatorInstructionEndedAttempts)
	}
	wantError(t, f.do(http.MethodPost, path, childTok, apiv1.CoordinatorInstructionAttemptRequest{AttemptID: instructionAttempt}), http.StatusNotFound, apiv1.CodeNotFound)
	if w := f.do(http.MethodPost, path, childTok, apiv1.CoordinatorInstructionAttemptRequest{AttemptID: instructionAttemptNext}); w.Code != http.StatusOK {
		t.Fatalf("next attempt %d %s", w.Code, w.Body.String())
	}
	if got := f.session("child").Status.CoordinatorInstructionAttempt; got.AttemptID != instructionAttemptNext || got.Phase != apiv1.InstructionPhaseActive || !instructionLedgerHas(f.session("child"), instructionAttempt) {
		t.Fatalf("switch dropped the ended id: %+v ledger %+v", got, f.session("child").Status.CoordinatorInstructionEndedAttempts)
	}
	wantError(t, f.do(http.MethodPost, path, childTok, apiv1.CoordinatorInstructionAttemptRequest{AttemptID: instructionAttempt}), http.StatusNotFound, apiv1.CodeNotFound)
}

func TestInstructionEndedLedgerRefusesWhenFull(t *testing.T) {
	f, _, childTok, _, _ := instructionReady(t, "parent", "child", "task:coord-instruction")
	path := apiv1.SessionInstructionAttemptPath("child")
	ids := []string{instructionAttempt, instructionAttemptNext, "cccccccc-cccc-cccc-cccc-cccccccccccc", "dddddddd-dddd-dddd-dddd-dddddddddddd"}
	for i, id := range ids {
		if i > 0 {
			if w := f.do(http.MethodPost, path, childTok, apiv1.CoordinatorInstructionAttemptRequest{AttemptID: id}); w.Code != http.StatusOK {
				t.Fatalf("register %s %d %s", id, w.Code, w.Body.String())
			}
		}
		if w := f.do(http.MethodPost, path, childTok, apiv1.CoordinatorInstructionAttemptRequest{AttemptID: id, Ended: true}); w.Code != http.StatusOK {
			t.Fatalf("end %s %d %s", id, w.Code, w.Body.String())
		}
	}
	if len(f.session("child").Status.CoordinatorInstructionEndedAttempts) != 4 {
		t.Fatalf("ledger %+v", f.session("child").Status.CoordinatorInstructionEndedAttempts)
	}
	wantError(t, f.do(http.MethodPost, path, childTok, apiv1.CoordinatorInstructionAttemptRequest{AttemptID: "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"}), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodPost, path, childTok, apiv1.CoordinatorInstructionAttemptRequest{AttemptID: instructionAttempt}), http.StatusNotFound, apiv1.CodeNotFound)
	if len(f.session("child").Status.CoordinatorInstructionEndedAttempts) != 4 {
		t.Fatal("a refused attempt changed the ledger")
	}
}

func TestInstructionAddressUsesParentScopeAndHidesText(t *testing.T) {
	const text = "addressed correction\n"
	f, parentTok, childTok, childUID, podUID := instructionReady(t, "parent", "child", "task:coord-instruction")
	w := f.do(http.MethodGet, apiv1.SessionInstructionAttemptPath("child"), parentTok, nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "nonce") || strings.Contains(w.Body.String(), "text") {
		t.Fatalf("address %d %s", w.Code, w.Body.String())
	}
	got := decode[apiv1.CoordinatorInstructionAttemptResult](t, w)
	if got.AttemptID != instructionAttempt || got.Phase != apiv1.InstructionPhaseActive || got.SessionUID != childUID || got.PodUID != podUID {
		t.Fatalf("address %+v", got)
	}
	req := instructionRequest(got.SessionUID, got.PodUID, "from-address", "task:coord-instruction", text)
	req.AttemptID = got.AttemptID
	if w := f.do(http.MethodPost, apiv1.SessionInstructionsPath("child"), parentTok, req); w.Code != http.StatusCreated {
		t.Fatalf("send from address %d %s", w.Code, w.Body.String())
	}
	wantError(t, f.do(http.MethodGet, apiv1.SessionInstructionsPath("child"), parentTok, nil), http.StatusNotFound, apiv1.CodeNotFound)
	peer := instructionChild(t, f, "peer", "parent", "task:peer-task")
	wantError(t, f.do(http.MethodGet, apiv1.SessionInstructionAttemptPath("child"), peer.token, nil), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodGet, apiv1.SessionInstructionAttemptPath("child"), tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodGet, apiv1.SessionInstructionAttemptPath("child"), childTok, nil), http.StatusNotFound, apiv1.CodeNotFound)
	parent := f.session("parent")
	delete(parent.Annotations, v1alpha1.AnnotationProjectSnapshot)
	if err := f.c.Update(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodGet, apiv1.SessionInstructionAttemptPath("child"), parentTok, nil), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionsPath("child"), parentTok, req), http.StatusNotFound, apiv1.CodeNotFound)
}

func TestInstructionAddressRefusesEndedStaleAndPlainAttempt(t *testing.T) {
	f, parentTok, childTok, _, podUID := instructionReady(t, "parent", "child", "task:coord-instruction")
	hexAttempt := strings.Repeat("ab", 32)
	hexChild := instructionBound(t, f, "hex-child", "parent", "task:hex-task", hexAttempt)
	if f.session("hex-child").Status.CoordinatorInstructionAttempt.AttemptID != hexAttempt {
		t.Fatal("platform attempt was not stored")
	}
	w := f.do(http.MethodGet, apiv1.SessionInstructionAttemptPath("hex-child"), parentTok, nil)
	got := decode[apiv1.CoordinatorInstructionAttemptResult](t, w)
	if w.Code != http.StatusOK || got.AttemptID != hexAttempt || got.PodUID != hexChild.pod || got.SessionUID != hexChild.uid {
		t.Fatalf("hex address %d %+v", w.Code, got)
	}
	wantError(t, f.do(http.MethodPost, apiv1.SessionInstructionAttemptPath("child"), childTok, apiv1.CoordinatorInstructionAttemptRequest{AttemptID: "ABCDEF"}), http.StatusBadRequest, apiv1.CodeBadRequest)
	session := f.session("child")
	session.Status.CoordinatorInstructionAttempt.PodUID = "stale-pod-uid"
	if err := f.c.Status().Update(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodGet, apiv1.SessionInstructionAttemptPath("child"), parentTok, nil), http.StatusNotFound, apiv1.CodeNotFound)
	ended := instructionBound(t, f, "ended-child", "parent", "task:ended-address", instructionAttempt)
	if w := f.do(http.MethodPost, apiv1.SessionInstructionAttemptPath("ended-child"), ended.token, apiv1.CoordinatorInstructionAttemptRequest{AttemptID: instructionAttempt, Ended: true}); w.Code != http.StatusOK {
		t.Fatalf("end %d %s", w.Code, w.Body.String())
	}
	wantError(t, f.do(http.MethodGet, apiv1.SessionInstructionAttemptPath("ended-child"), parentTok, nil), http.StatusNotFound, apiv1.CodeNotFound)
	if podUID == "" {
		t.Fatal("missing pod")
	}
}

func TestClaimSnapshotStillRejectsMissingAndWorkspace(t *testing.T) {
	catalog, err := projectcatalog.Parse([]byte(apiProjectCatalog))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := catalog.Snapshot("sample", "alias")
	if err != nil {
		t.Fatal(err)
	}
	data, err := snap.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	shared := &v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{Repo: "alias", Workspace: &v1alpha1.WorkspaceSpec{ID: "projects"}}, Annotations: map[string]string{v1alpha1.AnnotationProjectSnapshot: string(data)}}
	if _, err := claimSnapshot(shared); err == nil {
		t.Fatal("shared workspace snapshot was accepted")
	}
	missing := &v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{Repo: "alias"}}
	if _, err := claimSnapshot(missing); err == nil {
		t.Fatal("missing snapshot was accepted")
	}
}

func instructionLedgerHas(s *v1alpha1.AgentSession, id string) bool {
	for _, ended := range s.Status.CoordinatorInstructionEndedAttempts {
		if ended.AttemptID == id && ended.SessionUID == string(s.UID) {
			return true
		}
	}
	return false
}
