package agentd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestSyntheticNativeInstructionConsumptionCorrelates(t *testing.T) {
	home := t.TempDir()
	s := Settings{Home: home, StateDir: filepath.Join(home, ".agentd"), PodUID: "pod-1"}
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binding := nativeBinding{
		InvocationID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", LaunchDigest: strings.Repeat("ab", 32),
		Session: "child", SessionUID: "session-1", PodUID: "pod-1", BootID: "boot-1", Generation: 1,
	}
	writeInvocation := func(phase string) {
		t.Helper()
		inv := nativeInvocation{Version: 1, Binding: binding, Phase: phase, StartedAt: time.Now().UTC()}
		if phase != "Running" {
			inv.FinishedAt = inv.StartedAt
			inv.LeaderReaped = true
			inv.GroupAbsent = true
		}
		raw, err := json.Marshal(inv)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.statePath(nativeInvocationFile), append(raw, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeInvocation("Running")
	got, err := ActiveInstructionAttempt(s)
	if err != nil || got != binding {
		t.Fatalf("active attempt %v %v", got, err)
	}
	var calls int
	nonce := strings.Repeat("cd", 32)
	digest := strings.Repeat("ef", 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatalf("body")
		}
		if _, ok := body["sender"]; ok || body["from"] != nil {
			t.Fatal("request carried a sender")
		}
		switch r.URL.Path {
		case apiv1.SessionInstructionAttemptPath("child"):
			if body["attemptID"] != binding.InvocationID || body["ended"] == true {
				t.Fatalf("attempt body %#v", body)
			}
			writeTestInstructionJSON(w, apiv1.CoordinatorInstructionAttemptResult{AttemptID: binding.InvocationID, Phase: apiv1.InstructionPhaseActive, SessionUID: binding.SessionUID, PodUID: binding.PodUID})
		case apiv1.SessionInstructionConsumePath("child", "correction-1"):
			if body["attemptID"] != binding.InvocationID || body["nonce"] != nonce || body["targetSessionUID"] != binding.SessionUID || body["targetPodUID"] != binding.PodUID {
				t.Fatalf("consume body %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			writeTestInstructionJSON(w, apiv1.CoordinatorInstructionConsumeACK{
				Outcome: apiv1.InstructionOutcomeConsumed, Key: "correction-1", MessageUID: "message-uid", AttemptID: binding.InvocationID,
				Nonce: nonce, TextSHA256: digest, TargetSessionUID: binding.SessionUID, TargetPodUID: binding.PodUID,
			})
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	token := filepath.Join(home, "token")
	if err := os.WriteFile(token, []byte("synthetic-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &InboxClient{URL: srv.URL, TokenFile: token, HTTP: srv.Client()}
	if _, err := client.RegisterInstructionAttempt(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	ack, err := client.ConsumeInstruction(context.Background(), s, "correction-1", nonce, "message-uid", digest)
	if err != nil || ack.Outcome != apiv1.InstructionOutcomeConsumed || ack.AttemptID != binding.InvocationID || ack.Nonce != nonce {
		t.Fatalf("ack %+v %v", ack, err)
	}
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeTestInstructionJSON(w, apiv1.CoordinatorInstructionConsumeACK{
			Outcome: apiv1.InstructionOutcomeConsumed, Key: "correction-1", MessageUID: "message-uid", AttemptID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
			Nonce: nonce, TextSHA256: digest, TargetSessionUID: binding.SessionUID, TargetPodUID: binding.PodUID,
		})
	})
	if _, err := client.ConsumeInstruction(context.Background(), s, "correction-1", nonce, "message-uid", digest); err == nil || !strings.Contains(err.Error(), "not correlated") {
		t.Fatalf("uncorrelated ack accepted: %v", err)
	}
	before := calls
	writeInvocation("Exited")
	if _, err := ActiveInstructionAttempt(s); err == nil {
		t.Fatal("ended invocation stayed active")
	}
	if _, err := client.ConsumeInstruction(context.Background(), s, "correction-1", nonce, "message-uid", digest); err == nil {
		t.Fatal("ended invocation consumed")
	}
	if calls != before {
		t.Fatal("ended invocation contacted the API")
	}
}

func TestSyntheticNativeInstructionReadAckUsesExactRead(t *testing.T) {
	home := t.TempDir()
	s := Settings{Home: home, StateDir: filepath.Join(home, ".agentd"), PodUID: "pod-1"}
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binding := nativeBinding{
		InvocationID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", LaunchDigest: strings.Repeat("ab", 32),
		Session: "child", SessionUID: "session-1", PodUID: "pod-1", BootID: "boot-1", Generation: 1,
	}
	write := func(phase string) {
		t.Helper()
		inv := nativeInvocation{Version: 1, Binding: binding, Phase: phase, StartedAt: time.Now().UTC()}
		if phase == "Exited" {
			inv.FinishedAt = inv.StartedAt
			inv.LeaderReaped, inv.GroupAbsent = true, true
		}
		if phase == "Uncertain" {
			inv.FinishedAt = inv.StartedAt
			inv.LeaderReaped = true
		}
		raw, err := json.Marshal(inv)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.statePath(nativeInvocationFile), append(raw, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("Running")
	if err := publishNativeInstructionLifecycle(nil, false); err != nil {
		t.Fatal(err)
	}
	if err := publishNativeInstructionLifecycle(&nativeInvocationLease{s: Settings{}}, true); err != nil {
		t.Fatal(err)
	}
	if err := publishNativeInstructionLifecycle(&nativeInvocationLease{s: Settings{APIURL: "http://insecure.example"}}, false); err == nil {
		t.Fatal("insecure origin registered an attempt")
	}
	nonce := strings.Repeat("cd", 32)
	digest := strings.Repeat("ef", 32)
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		var body map[string]any
		if r.Body != nil && r.ContentLength != 0 {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		switch r.URL.Path {
		case apiv1.SessionInstructionPath("child", "correction-1"):
			writeTestInstructionJSON(w, apiv1.CoordinatorInstructionView{
				CoordinatorInstructionReceipt: apiv1.CoordinatorInstructionReceipt{
					Outcome: apiv1.InstructionOutcomeConfirmed, Key: "correction-1", MessageUID: "message-uid", TextSHA256: digest,
					TargetSessionUID: binding.SessionUID, TargetPodUID: binding.PodUID, AttemptID: binding.InvocationID,
					Project: "sample", Repository: "alias", TaskKey: "task:coord-instruction",
				},
				Text: "clarify the assigned outcome\n", Nonce: nonce,
			})
		case apiv1.SessionInstructionConsumePath("child", "correction-1"):
			if body["nonce"] != nonce || body["attemptID"] != binding.InvocationID {
				t.Fatalf("consume body %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			writeTestInstructionJSON(w, apiv1.CoordinatorInstructionConsumeACK{
				Outcome: apiv1.InstructionOutcomeConsumed, Key: "correction-1", MessageUID: "message-uid", AttemptID: binding.InvocationID,
				Nonce: nonce, TextSHA256: digest, TargetSessionUID: binding.SessionUID, TargetPodUID: binding.PodUID,
			})
		case apiv1.SessionInstructionAttemptPath("child"):
			if body["ended"] != true || body["attemptID"] != binding.InvocationID {
				t.Fatalf("end body %#v", body)
			}
			writeTestInstructionJSON(w, apiv1.CoordinatorInstructionAttemptResult{AttemptID: binding.InvocationID, Phase: apiv1.InstructionPhaseEnded, SessionUID: binding.SessionUID, PodUID: binding.PodUID})
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	token := filepath.Join(home, "token")
	if err := os.WriteFile(token, []byte("synthetic-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &InboxClient{URL: srv.URL, TokenFile: token, HTTP: srv.Client()}
	if _, err := client.EndInstructionAttempt(context.Background(), s); err == nil {
		t.Fatal("running attempt ended")
	}
	if len(paths) != 0 {
		t.Fatalf("running end called the API: %v", paths)
	}
	if _, err := client.AcknowledgeReadInstruction(context.Background(), s, "correction-1", "", digest); err == nil || len(paths) != 1 || !strings.HasPrefix(paths[0], http.MethodGet) {
		t.Fatalf("empty acknowledgement paths %v err %v", paths, err)
	}
	if _, err := client.AcknowledgeReadInstruction(context.Background(), s, "correction-1", "message-uid", strings.Repeat("ab", 32)); err == nil || len(paths) != 2 {
		t.Fatalf("mismatched digest consumed: %v %v", paths, err)
	}
	ack, err := client.AcknowledgeReadInstruction(context.Background(), s, "correction-1", "message-uid", digest)
	if err != nil || ack.MessageUID != "message-uid" || ack.TextSHA256 != digest || ack.Nonce != nonce || len(paths) != 4 {
		t.Fatalf("ack %+v err %v paths %v", ack, err, paths)
	}
	if paths[2] != http.MethodGet+" "+apiv1.SessionInstructionPath("child", "correction-1") || paths[3] != http.MethodPost+" "+apiv1.SessionInstructionConsumePath("child", "correction-1") {
		t.Fatalf("ack paths %v", paths)
	}
	write("Uncertain")
	before := len(paths)
	if _, err := client.EndInstructionAttempt(context.Background(), s); err == nil || len(paths) != before {
		t.Fatalf("uncertain end contacted the API: %v", err)
	}
	write("Exited")
	ended, err := client.EndInstructionAttempt(context.Background(), s)
	if err != nil || ended.Phase != apiv1.InstructionPhaseEnded || ended.AttemptID != binding.InvocationID {
		t.Fatalf("ended %+v %v", ended, err)
	}
}

func writeTestInstructionJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
