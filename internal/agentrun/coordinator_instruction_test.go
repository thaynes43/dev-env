package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestInstructionCLIDoesNotTreatDeliveryAsConsumption(t *testing.T) {
	h := newHarness(t)
	var bodies []map[string]any
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		var got map[string]any
		if len(body) > 0 && json.Unmarshal(body, &got) == nil {
			bodies = append(bodies, got)
		}
		if _, ok := got["sender"]; ok || got["from"] != nil {
			t.Fatal("CLI sent a sender")
		}
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/instructions"):
			writeTestJSON(w, http.StatusCreated, apiv1.CoordinatorInstructionReceipt{
				Outcome: apiv1.InstructionOutcomeCreated, Key: "correction-1", MessageName: "message." + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32),
				MessageUID: "m-uid", TextSHA256: strings.Repeat("ab", 32), TargetSessionUID: "session-1", TargetPodUID: "pod-1",
				AttemptID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Project: "sample", Repository: "alias", TaskKey: "task:coord-instruction",
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/consume"):
			writeTestJSON(w, http.StatusCreated, apiv1.CoordinatorInstructionConsumeACK{
				Outcome: apiv1.InstructionOutcomeConsumed, Key: "correction-1", MessageUID: "m-uid", AttemptID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
				Nonce: strings.Repeat("cd", 32), TextSHA256: strings.Repeat("ab", 32), TargetSessionUID: "session-1", TargetPodUID: "pod-1",
			})
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}
	h.env.Stdin = strings.NewReader("clarify the assigned outcome\n")
	h.mustRun(ExitOK, "instruction", "send", "--session", "child", "--key", "correction-1", "--project", "sample", "--repository", "alias", "--task-key", "task:coord-instruction", "--expect-uid", "session-1", "--expect-pod", "pod-1", "--attempt", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	if strings.Contains(h.stdout.String(), `"nonce"`) || strings.Contains(h.stdout.String(), `"consumed": true`) || !strings.Contains(h.stdout.String(), `"consumed":false`) && !strings.Contains(h.stdout.String(), `"consumed": false`) {
		t.Fatalf("delivery stdout %s", h.stdout.String())
	}
	h.stdout.Reset()
	h.mustRun(ExitOK, "instruction", "consume", "--session", "child", "--key", "correction-1", "--attempt", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "--nonce", strings.Repeat("cd", 32), "--message-uid", "m-uid", "--text-sha256", strings.Repeat("ab", 32), "--expect-uid", "session-1", "--expect-pod", "pod-1")
	if !strings.Contains(h.stdout.String(), `"outcome": "Consumed"`) && !strings.Contains(h.stdout.String(), `"outcome":"Consumed"`) {
		t.Fatalf("consume stdout %s", h.stdout.String())
	}
	if len(bodies) != 2 || bodies[0]["text"] != "clarify the assigned outcome\n" || bodies[1]["nonce"] != strings.Repeat("cd", 32) || bodies[1]["attemptID"] != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" {
		t.Fatalf("bodies %#v", bodies)
	}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		receipt := apiv1.CoordinatorInstructionReceipt{Outcome: apiv1.InstructionOutcomeCreated, Key: "correction-1", MessageUID: "m-uid", TextSHA256: strings.Repeat("ab", 32), TargetSessionUID: "session-1", TargetPodUID: "pod-1", AttemptID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Project: "sample", Repository: "alias", TaskKey: "task:coord-instruction"}
		raw, _ := json.Marshal(receipt)
		var withNonce map[string]any
		_ = json.Unmarshal(raw, &withNonce)
		withNonce["nonce"] = strings.Repeat("cd", 32)
		writeTestJSON(w, http.StatusCreated, withNonce)
	}
	h.env.Stdin = strings.NewReader("clarify the assigned outcome\n")
	h.stdout.Reset()
	h.mustRun(ExitUnconfirmed, "instruction", "send", "--session", "child", "--key", "correction-1", "--project", "sample", "--repository", "alias", "--task-key", "task:coord-instruction", "--expect-uid", "session-1", "--expect-pod", "pod-1", "--attempt", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeTestJSON(w, http.StatusCreated, apiv1.CoordinatorInstructionConsumeACK{
			Outcome: apiv1.InstructionOutcomeConsumed, Key: "correction-1", MessageUID: "m-uid", AttemptID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
			Nonce: strings.Repeat("cd", 32), TextSHA256: strings.Repeat("ab", 32), TargetSessionUID: "session-1", TargetPodUID: "pod-1",
		})
	}
	h.mustRun(ExitUnconfirmed, "instruction", "consume", "--session", "child", "--key", "correction-1", "--attempt", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "--nonce", strings.Repeat("cd", 32), "--message-uid", "m-uid", "--text-sha256", strings.Repeat("ab", 32), "--expect-uid", "session-1", "--expect-pod", "pod-1")
}

func TestInstructionCLISendReadConsumeAndConfirmedRetry(t *testing.T) {
	h := newHarness(t)
	nonce := strings.Repeat("cd", 32)
	digest := strings.Repeat("ab", 32)
	attempt := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	text := "clarify the assigned outcome\n"
	var sends int
	var kinds []string
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/instructions"):
			sends++
			kinds = append(kinds, "send")
			receipt := apiv1.CoordinatorInstructionReceipt{
				Key: "correction-1", MessageUID: "m-uid", TextSHA256: digest, TargetSessionUID: "session-1", TargetPodUID: "pod-1",
				AttemptID: attempt, Project: "sample", Repository: "alias", TaskKey: "task:coord-instruction",
			}
			if sends == 1 {
				receipt.Outcome = apiv1.InstructionOutcomeCreated
				writeTestJSON(w, http.StatusCreated, receipt)
				return
			}
			receipt.Outcome = apiv1.InstructionOutcomeConfirmed
			receipt.Consumed = true
			writeTestJSON(w, http.StatusOK, receipt)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/instructions/"):
			kinds = append(kinds, "read")
			writeTestJSON(w, http.StatusOK, apiv1.CoordinatorInstructionView{
				CoordinatorInstructionReceipt: apiv1.CoordinatorInstructionReceipt{
					Outcome: apiv1.InstructionOutcomeConfirmed, Key: "correction-1", MessageUID: "m-uid", TextSHA256: digest,
					TargetSessionUID: "session-1", TargetPodUID: "pod-1", AttemptID: attempt, Project: "sample", Repository: "alias", TaskKey: "task:coord-instruction",
				},
				Text: text, Nonce: nonce,
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/consume"):
			kinds = append(kinds, "consume")
			writeTestJSON(w, http.StatusCreated, apiv1.CoordinatorInstructionConsumeACK{
				Outcome: apiv1.InstructionOutcomeConsumed, Key: "correction-1", MessageUID: "m-uid", AttemptID: attempt,
				Nonce: nonce, TextSHA256: digest, TargetSessionUID: "session-1", TargetPodUID: "pod-1",
			})
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}
	send := []string{"instruction", "send", "--session", "child", "--key", "correction-1", "--project", "sample", "--repository", "alias", "--task-key", "task:coord-instruction", "--expect-uid", "session-1", "--expect-pod", "pod-1", "--attempt", attempt}
	h.env.Stdin = strings.NewReader(text)
	h.mustRun(ExitOK, send...)
	if strings.Contains(h.stdout.String(), `"consumed": true`) || !strings.Contains(h.stdout.String(), `"outcome": "Created"`) {
		t.Fatalf("created stdout %s", h.stdout.String())
	}
	h.mustRun(ExitOK, "instruction", "read", "--session", "child", "--key", "correction-1")
	if !strings.Contains(h.stdout.String(), `"messageUID": "m-uid"`) || !strings.Contains(h.stdout.String(), digest) || !strings.Contains(h.stdout.String(), `"project": "sample"`) || !strings.Contains(h.stdout.String(), "clarify the assigned outcome") {
		t.Fatalf("read stdout %s", h.stdout.String())
	}
	h.mustRun(ExitOK, "instruction", "consume", "--session", "child", "--key", "correction-1", "--attempt", attempt, "--nonce", nonce, "--message-uid", "m-uid", "--text-sha256", digest, "--expect-uid", "session-1", "--expect-pod", "pod-1")
	h.env.Stdin = strings.NewReader(text)
	h.mustRun(ExitOK, send...)
	if !strings.Contains(h.stdout.String(), `"outcome": "Confirmed"`) || !strings.Contains(h.stdout.String(), `"consumed": true`) {
		t.Fatalf("confirmed retry stdout %s", h.stdout.String())
	}
	if strings.Join(kinds, ",") != "send,read,consume,send" {
		t.Fatalf("sequence %v", kinds)
	}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeTestJSON(w, http.StatusCreated, apiv1.CoordinatorInstructionReceipt{
			Outcome: apiv1.InstructionOutcomeCreated, Key: "correction-1", MessageUID: "m-uid", TextSHA256: digest, Consumed: true,
			TargetSessionUID: "session-1", TargetPodUID: "pod-1", AttemptID: attempt, Project: "sample", Repository: "alias", TaskKey: "task:coord-instruction",
		})
	}
	h.env.Stdin = strings.NewReader(text)
	h.mustRun(ExitUnconfirmed, send...)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeTestJSON(w, http.StatusCreated, apiv1.CoordinatorInstructionConsumeACK{
			Outcome: apiv1.InstructionOutcomeConsumed, Key: "correction-1", MessageUID: "other-uid", AttemptID: attempt,
			Nonce: nonce, TextSHA256: digest, TargetSessionUID: "session-1", TargetPodUID: "pod-1",
		})
	}
	h.mustRun(ExitUnconfirmed, "instruction", "consume", "--session", "child", "--key", "correction-1", "--attempt", attempt, "--nonce", nonce, "--message-uid", "m-uid", "--text-sha256", digest, "--expect-uid", "session-1", "--expect-pod", "pod-1")
}

func TestInstructionCLIConflictIsNotRetried(t *testing.T) {
	h := newHarness(t)
	var calls int
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		calls++
		writeTestJSON(w, http.StatusConflict, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeConflict, Message: "different intent"}})
	}
	h.env.Stdin = strings.NewReader("clarify\n")
	h.mustRun(ExitFailed, "instruction", "send", "--session", "child", "--key", "correction-1", "--project", "sample", "--repository", "alias", "--task-key", "task:coord-instruction", "--expect-uid", "session-1", "--expect-pod", "pod-1", "--attempt", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	if calls != 1 {
		t.Fatalf("conflict retried %d", calls)
	}
}
