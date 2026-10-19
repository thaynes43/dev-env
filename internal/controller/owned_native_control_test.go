package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/go-logr/logr"
	kexec "k8s.io/client-go/util/exec"
	"log/slog"
	"sigs.k8s.io/controller-runtime/pkg/log"
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

func TestNativeRefusalLogsFixedStatusAndNoRemoteContent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		stage string
	}{
		{"timeout", kexec.CodeExitError{Err: errors.New("NEVER_LOG_NATIVE_BYTES"), Code: 85}, "operation-timeout"},
		{"rejected", kexec.CodeExitError{Err: errors.New("NEVER_LOG_NATIVE_BYTES"), Code: 86}, "operation-rejected"},
		{"protocol", kexec.CodeExitError{Err: errors.New("NEVER_LOG_NATIVE_BYTES"), Code: 87}, "operation-protocol"},
		{"transport", errors.New("NEVER_LOG_NATIVE_BYTES"), "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			ctx := log.IntoContext(context.Background(), logr.FromSlogHandler(slog.NewTextHandler(&output, nil)))
			logNativeControlRefusal(ctx, ctx, "thread/start", "exec", tc.err, time.Now())
			got := output.String()
			if strings.Contains(got, "NEVER_LOG_NATIVE_BYTES") || !strings.Contains(got, "nativeStage="+tc.stage) || !strings.Contains(got, "typedExit=") || !strings.Contains(got, "elapsedMs=") {
				t.Fatal("invalid fixed diagnostic", got)
			}
		})
	}
}
