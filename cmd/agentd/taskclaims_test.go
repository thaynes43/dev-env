package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

func TestTaskClaimCommandsIntentConflictAndRefusal(t *testing.T) {
	identity := apiv1.TaskClaimIdentity{Project: "sample", Repository: "repo", TaskKey: "task:pilot"}
	claim := apiv1.TaskClaim{Name: taskclaim.Name(coord.TaskIdentity{Project: "sample", Repository: "repo", TaskKey: "task:pilot"}), UID: "11111111-1111-4111-8111-111111111111", Identity: identity, Owner: apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "owner", UID: "22222222-2222-4222-8222-222222222222"}}
	calls := 0
	status := 201
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(status)
		if status == 403 || status == 503 {
			_, _ = io.WriteString(w, `{"error":{"code":"refused"}}`)
			return
		}
		if r.Method == "POST" {
			body, _ := io.ReadAll(r.Body)
			var req apiv1.CreateTaskClaimRequest
			if json.Unmarshal(body, &req) != nil || req.Owner != nil || req.Identity != identity {
				t.Error("spoofed intent")
			}
			outcome := "Created"
			if status == 409 {
				outcome = "Conflict"
			}
			if status == 200 {
				outcome = "Confirmed"
			}
			_ = json.NewEncoder(w).Encode(apiv1.TaskClaimResult{Outcome: outcome, Claim: claim})
		} else if r.URL.Path == "/v1/task-claims" {
			_ = json.NewEncoder(w).Encode(apiv1.TaskClaimList{Items: []apiv1.TaskClaim{claim}})
		} else {
			_ = json.NewEncoder(w).Encode(claim)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"AGENTD_API_URL": srv.URL, "AGENTD_API_CA_FILE": ca, "AGENTD_API_TOKEN_FILE": token}
	getenv := func(k string) string { return env[k] }
	intent := `{"identity":{"project":"sample","repository":"repo","taskKey":"task:pilot"}}`
	for _, st := range []int{201, 200, 409, 403, 503} {
		status = st
		var out, stderr bytes.Buffer
		before := calls
		code := run(context.Background(), []string{"task-claim-create"}, strings.NewReader(intent), &out, &stderr, getenv, agentd.ExecRunner{})
		want := exitOK
		if st >= 400 {
			want = exitFailure
		}
		if code != want || calls != before+1 || !json.Valid(bytes.TrimSpace(out.Bytes())) || (st == 409 && !strings.Contains(out.String(), "22222222-2222-4222-8222-222222222222")) {
			t.Fatal("ownership/failure lost", st, code, out.String(), stderr.String())
		}
	}
	status = 200
	for _, args := range [][]string{{"task-claim-list", "--project=sample", "--limit=1", "--continue=opaque&cursor"}, {"task-claim-get", "--project=sample", "--claim=" + claim.Name, "--uid=" + claim.UID}} {
		var out, stderr bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &out, &stderr, getenv, agentd.ExecRunner{}); code != 0 {
			t.Fatal(code, stderr.String())
		}
	}
	before := calls
	for _, bad := range []string{intent + `{}`, `{"identity":{"project":"sample","repository":"repo","taskKey":"issue:0"}}`, `{"identity":{"project":"sample","repository":"repo","taskKey":"task:pilot"},"owner":{"uid":"other"}}`, `{"identity":{"project":"sample","repository":"repo","taskKey":"task:pilot","actor":{}}}`, strings.Repeat(" ", 4097), "\xff"} {
		var out, stderr bytes.Buffer
		if code := run(context.Background(), []string{"task-claim-create"}, strings.NewReader(bad), &out, &stderr, getenv, agentd.ExecRunner{}); code != exitUsage {
			t.Fatal("invalid intent not refused", code)
		}
	}
	for _, args := range [][]string{{"task-claim-get", "--project=sample", "--claim=../../inbox"}, {"task-claim-list", "--project=sample", "--limit=101"}, {"task-claim-list", "--project=other?x=y"}, {"task-claim-create", "--uid=spoofed"}, {"task-claim-create", "--owner=other"}} {
		var out, stderr bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(intent), &out, &stderr, getenv, agentd.ExecRunner{}); code != exitUsage {
			t.Fatal("invalid flags not refused", code)
		}
	}
	if calls != before {
		t.Fatal("invalid intent reached API")
	}
}
