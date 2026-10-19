package controller

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestNativeContentOverflowCannotAcceptValidPrefix(t *testing.T) {
	r := apiv1.NativeControlRequest{Binding: apiv1.TaskBudgetBinding{TaskUID: "t", HostID: "h", PodUID: "p", Epoch: 1, Deadline: time.Now().Add(time.Minute)}, ID: "once", Operation: "thread/read", ThreadID: "thread"}
	body, _ := json.Marshal(apiv1.NativeControlResponse{Binding: r.Binding, ID: r.ID, Operation: r.Operation, Result: json.RawMessage(`{"thread":{"id":"thread"}}`)})
	out := &nativeOutput{}
	if _, err := out.Write(body); err != nil {
		t.Fatal(err)
	}
	if _, err := out.response(r); err != nil {
		t.Fatal("valid bounded native content refused", err)
	}
	// client-go may ignore writer errors while draining the CRI stream. A
	// successful process status must not make this valid prefix complete.
	if _, err := out.Write([]byte(strings.Repeat("x", hostexecutor.NativeControlLimit))); err == nil {
		t.Fatal("overflow accepted")
	}
	if _, err := out.response(r); err == nil {
		t.Fatal("accepted truncated valid JSON prefix")
	}
}
