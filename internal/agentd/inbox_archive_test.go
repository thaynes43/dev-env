package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

func TestInboxClientPreservesArchivedListReadAndAckEnvelopes(t *testing.T) {
	archivedAt := time.Date(2026, 10, 15, 16, 0, 0, 0, time.UTC)
	createdAt := time.Date(2026, 10, 14, 16, 0, 0, 0, time.UTC)
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
	listRaw := mustArchiveInboxJSON(t, apiv1.InboxList{Items: []apiv1.InboxMetadata{metadata}, Continue: "archive-next"})
	messageRaw := mustArchiveInboxJSON(t, apiv1.InboxMessage{InboxMetadata: metadata, Text: message.Text})
	getAckRaw := mustArchiveInboxJSON(t, ackView)
	postAckRaw := mustArchiveInboxJSON(t, postAck)
	if bytes.Equal(getAckRaw, postAckRaw) {
		t.Fatal("GET and POST acknowledgement shapes collapsed")
	}
	var decodedGetAck apiv1.InboxReadAck
	if json.Unmarshal(getAckRaw, &decodedGetAck) != nil || !reflect.DeepEqual(decodedGetAck, ackView) {
		t.Fatal("GET acknowledgement bytes do not carry the independent complete view")
	}
	responses := map[string][]byte{
		http.MethodGet + " /v1/inbox?continue=archive-next&limit=1&project=sample": listRaw,
		http.MethodGet + " /v1/inbox/" + messageName + "?project=sample":           messageRaw,
		http.MethodGet + " /v1/inbox/" + messageName + "/ack?project=sample":       getAckRaw,
		http.MethodPost + " /v1/inbox/" + messageName + "/ack?project=sample":      postAckRaw,
	}
	type call struct{ method, uri, body string }
	var calls []call
	c, _ := inboxTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	for _, tc := range []struct {
		method string
		path   string
		body   []byte
	}{
		{http.MethodGet, "/v1/inbox?continue=archive-next&limit=1&project=sample", nil},
		{http.MethodGet, "/v1/inbox/" + messageName + "?project=sample", nil},
		{http.MethodGet, "/v1/inbox/" + messageName + "/ack?project=sample", nil},
		{http.MethodPost, "/v1/inbox/" + messageName + "/ack?project=sample", []byte("{}")},
	} {
		got, status, err := c.Do(context.Background(), tc.method, tc.path, tc.body)
		if err != nil || status != http.StatusOK || string(got) != string(responses[tc.method+" "+tc.path]) {
			t.Fatalf("%s %s = %q, %d, %v", tc.method, tc.path, got, status, err)
		}
	}
	if len(calls) != 4 {
		t.Fatalf("requests = %#v", calls)
	}
	for i, tc := range []struct{ body string }{{""}, {""}, {""}, {"{}"}} {
		if calls[i].body != tc.body {
			t.Fatalf("request %d body = %q want %q", i, calls[i].body, tc.body)
		}
	}
	before := len(calls)
	if _, _, err := c.Do(context.Background(), http.MethodGet, "/v1/not-inbox", nil); err == nil || len(calls) != before {
		t.Fatal("invalid inbox path reached the API")
	}
}

func TestInboxClientRejectsMalformedArchivedEnvelope(t *testing.T) {
	requests := 0
	c, _ := inboxTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = io.WriteString(w, `{"items":[`) // Invalid JSON is a transport boundary, not semantic validation.
	}))
	if _, _, err := c.Do(context.Background(), http.MethodGet, "/v1/inbox?project=sample", nil); err == nil || requests != 1 {
		t.Fatalf("malformed archived envelope = %v, %d requests", err, requests)
	}
}

func mustArchiveInboxJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
