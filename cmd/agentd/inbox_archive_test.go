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
	"time"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

func TestInboxArchiveCommandsPreserveArchivedResponses(t *testing.T) {
	archivedAt := time.Date(2026, 10, 15, 16, 30, 0, 0, time.UTC)
	createdAt := time.Date(2026, 10, 14, 16, 30, 0, 0, time.UTC)
	sender := coord.SessionIdentity{Namespace: "dev-agents", Name: "sender", UID: "11111111-1111-4111-8111-111111111111"}
	recipient := coord.SessionIdentity{Namespace: "dev-agents", Name: "recipient", UID: "22222222-2222-4222-8222-222222222222"}
	senderActor := coord.ClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env-agent", PodName: "sender", PodUID: "33333333-3333-4333-8333-333333333333"}
	message := coord.InboxMessageSpec{Project: "sample", Key: "same-work", Sender: sender, Recipient: recipient, Actor: senderActor, Provenance: coord.ClaimProvenance{CatalogRevision: strings.Repeat("a", 64), RulesRevision: strings.Repeat("b", 64), GitHub: "thaynes43/dev-env", DefaultBranch: "main", InitialPinState: "Unknown"}, Repository: "repo", Provider: "codex", RequestedModel: "gpt-5.6-terra", Text: "exact archived text\n"}
	messageName, messageUID := inbox.MessageName(message.Sender, message.Key), "44444444-4444-4444-8444-444444444444"
	ackActor := coord.ClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env-agent", PodName: "recipient", PodUID: "55555555-5555-4555-8555-555555555555"}
	ackSpec := coord.InboxReadAckSpec{Project: message.Project, MessageName: messageName, MessageUID: messageUID, Recipient: recipient, Actor: ackActor}
	if !inbox.ValidMessage(message) || !inbox.ValidAck(ackSpec) {
		t.Fatal("fixture does not satisfy immutable inbox contracts")
	}
	owner := apiv1.TaskClaimOwner{Namespace: recipient.Namespace, Name: recipient.Name, UID: recipient.UID}
	metadata := apiv1.InboxMetadata{
		Name: messageName, UID: messageUID, Project: message.Project, Key: message.Key, Sender: apiv1.TaskClaimOwner{Namespace: sender.Namespace, Name: sender.Name, UID: sender.UID}, Recipient: owner,
		Actor: apiv1.TaskClaimActor{Kind: senderActor.Kind, ServiceAccount: senderActor.ServiceAccount, PodName: senderActor.PodName, PodUID: senderActor.PodUID}, Provenance: apiv1.TaskClaimProvenance{CatalogRevision: message.Provenance.CatalogRevision, RulesRevision: message.Provenance.RulesRevision, GitHub: message.Provenance.GitHub, DefaultBranch: message.Provenance.DefaultBranch, InitialPinState: message.Provenance.InitialPinState},
		Repository: message.Repository, Provider: message.Provider, RequestedModel: message.RequestedModel, CreatedAt: createdAt, Archived: true, ArchivedAt: &archivedAt,
	}
	ackView := apiv1.InboxReadAck{Name: inbox.AckName(ackSpec.MessageUID, ackSpec.Recipient.UID), UID: "66666666-6666-4666-8666-666666666666", Project: ackSpec.Project, MessageName: ackSpec.MessageName, MessageUID: ackSpec.MessageUID, Recipient: owner, Actor: apiv1.TaskClaimActor{Kind: ackActor.Kind, ServiceAccount: ackActor.ServiceAccount, PodName: ackActor.PodName, PodUID: ackActor.PodUID}, CreatedAt: createdAt, Archived: true, ArchivedAt: &archivedAt}
	postAck := apiv1.InboxAckResult{Outcome: "Confirmed", Ack: ackView}
	if ackView.Actor == metadata.Actor {
		t.Fatal("ack actor reused the saved sender")
	}
	listRaw := mustArchiveCommandJSON(t, apiv1.InboxList{Items: []apiv1.InboxMetadata{metadata}, Continue: "archive-next"})
	messageRaw := mustArchiveCommandJSON(t, apiv1.InboxMessage{InboxMetadata: metadata, Text: message.Text})
	postAckRaw := mustArchiveCommandJSON(t, postAck)
	responses := map[string][]byte{
		http.MethodGet + " /v1/inbox?continue=archive-next&limit=1&project=sample": listRaw,
		http.MethodGet + " /v1/inbox/" + messageName + "?project=sample":           messageRaw,
		http.MethodPost + " /v1/inbox/" + messageName + "/ack?project=sample":      postAckRaw,
	}
	type call struct{ method, uri, body string }
	var calls []call
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.RequestURI()
		calls = append(calls, call{r.Method, r.URL.RequestURI(), string(body)})
		response, ok := responses[key]
		if !ok {
			t.Errorf("unexpected request %q", key)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(response)
	}))
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("synthetic-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"AGENTD_API_URL": srv.URL, "AGENTD_API_TOKEN_FILE": token, "AGENTD_API_CA_FILE": ca}
	getenv := func(key string) string { return env[key] }
	for _, tc := range []struct {
		args     []string
		response []byte
		body     string
	}{
		{[]string{"inbox-list", "--project=sample", "--limit=1", "--continue=archive-next"}, responses[http.MethodGet+" /v1/inbox?continue=archive-next&limit=1&project=sample"], ""},
		{[]string{"inbox-read", "--project=sample", "--message=" + messageName}, responses[http.MethodGet+" /v1/inbox/"+messageName+"?project=sample"], ""},
		{[]string{"inbox-ack", "--project=sample", "--message=" + messageName}, responses[http.MethodPost+" /v1/inbox/"+messageName+"/ack?project=sample"], "{}"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), tc.args, strings.NewReader(""), &stdout, &stderr, getenv, agentd.ExecRunner{}); code != exitOK || stdout.String() != string(tc.response)+"\n" || stderr.Len() != 0 {
			t.Fatalf("%v = code %d stdout %q stderr %q", tc.args, code, stdout.String(), stderr.String())
		}
		if calls[len(calls)-1].body != tc.body {
			t.Fatalf("%v request body = %q want %q", tc.args, calls[len(calls)-1].body, tc.body)
		}
	}
	if len(calls) != 3 || calls[2].method != http.MethodPost || !strings.Contains(calls[2].uri, "/ack?") {
		t.Fatalf("commands made unexpected requests: %#v", calls)
	}
}

func mustArchiveCommandJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
