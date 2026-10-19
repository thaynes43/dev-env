package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestDirectNativeHelperRequiresExactOneUseOwnerConsumption(t *testing.T) {
	for _, scenario := range []string{"unreserved", "hash-mismatch", "lost-consume-ack", "lost-native-ack", "once", "read"} {
		t.Run(scenario, func(t *testing.T) {
			req := apiv1.NativeControlRequest{Binding: apiv1.TaskBudgetBinding{TaskUID: "task", HostID: "host", PodUID: "pod", Epoch: 1, Deadline: time.Now().Add(time.Minute)}, ID: "human-once", Operation: "turn/start", ThreadID: "thread-a", Text: "owner payload"}
			reserved := apiv1.NativeControlDigest(req)
			consumed := false
			calls := 0
			if scenario == "hash-mismatch" {
				req.Text = "substituted model payload"
			}
			if scenario == "read" {
				req.Operation = "thread/read"
				req.Text = ""
			}
			consume := func(_ context.Context, r apiv1.NativeControlRequest) error {
				if scenario == "unreserved" || reserved != apiv1.NativeControlDigest(r) || consumed {
					return errors.New("not exact unused human reservation")
				}
				consumed = true
				if scenario == "lost-consume-ack" {
					return errors.New("lost acknowledgement after commit")
				}
				return nil
			}
			dispatch := func() (json.RawMessage, error) {
				calls++
				if scenario == "lost-native-ack" {
					return nil, errors.New("lost native acknowledgement")
				}
				return json.RawMessage(`{"turn":{"id":"turn-a"}}`), nil
			}
			run := func() (json.RawMessage, error) {
				if err := authorizeOwnedNativeControl(context.Background(), req, consume); err != nil {
					return nil, err
				}
				return dispatch()
			}
			_, err := run()
			wantRPC := scenario == "once" || scenario == "read" || scenario == "lost-native-ack"
			if (calls == 1) != wantRPC {
				t.Fatal("unreserved/unknown helper reached native", calls, err)
			}
			if wantRPC && scenario != "lost-native-ack" && err != nil {
				t.Fatal(err)
			}
			if scenario != "read" {
				_, _ = run()
				if calls > 1 {
					t.Fatal("direct helper replayed native mutation")
				}
			}
		})
	}
}
