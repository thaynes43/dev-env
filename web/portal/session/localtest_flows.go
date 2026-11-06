//go:build portallocaltest

package session

// Write flows of the portal local test instance (DESIGN-055 addendum,
// OP479): send, launch, history and Asks answers run through the production
// handlers against fixture operator routes that record every call. Nothing
// here reaches a real operator, agent, shelf or Asks API.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// localTestCall is one write or history request a fixture upstream received.
type localTestCall struct {
	Method, Path string
	Body         []byte
}

// localTestCalls records fixture upstream calls so tests can assert that a
// flow reached the fixture exactly as often as expected.
type localTestCalls struct {
	mu   sync.Mutex
	list []localTestCall
}

func (c *localTestCalls) add(call localTestCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list = append(c.list, call)
}

func (c *localTestCalls) snapshot() []localTestCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]localTestCall(nil), c.list...)
}

// localTestLaunchRepos is the fixture launch scope.
var localTestLaunchRepos = []string{"dev-env", "fixture-repo"}

// localTestWrites is the fixture operator's write side. It records every
// non-GET request, then answers the wired routes; anything else is refused.
// Launched sessions join the Sessions listing as Pending rows.
type localTestWrites struct {
	calls    *localTestCalls
	now      func() time.Time
	sessions []byte
	repos    []string

	mu       sync.Mutex
	launches map[string]localTestLaunch
	launched []json.RawMessage
	asks     []apiv1.Ask
}

// asksSnapshot is the current fixture asks, answers included.
func (o *localTestWrites) asksSnapshot() []apiv1.Ask {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]apiv1.Ask(nil), o.asks...)
}

// answer answers like the operator's portal answer route: an open ask takes
// one valid answer and is then Answered; a closed ask is a conflict and an
// expired one is gone. It notifies no agent.
func (o *localTestWrites) answer(w http.ResponseWriter, id string, body []byte) {
	var request apiv1.AskAnswerRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.Validate() != nil {
		http.Error(w, "fixture operator refused", http.StatusBadRequest)
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now().UTC()
	for i, ask := range o.asks {
		if ask.ID != id {
			continue
		}
		switch {
		case ask.State != apiv1.AskStateOpen:
			http.Error(w, "fixture operator conflict", http.StatusConflict)
		case !now.Before(ask.ExpiresAt):
			http.Error(w, "fixture operator gone", http.StatusGone)
		case request.ValidateForAt(ask, now) != nil:
			http.Error(w, "fixture operator refused", http.StatusBadRequest)
		default:
			ask.State = apiv1.AskStateAnswered
			ask.Answer = &apiv1.AskAnswer{OptionID: request.OptionID, Text: request.Text, Approved: request.Approved, AnsweredBy: apiv1.AskActor{Issuer: request.Actor.Issuer, Subject: request.Actor.Subject, DisplayName: "Local test reader"}, AnsweredAt: now}
			o.asks[i] = ask
			raw, _ := json.Marshal(ask)
			localTestJSON(w, mediaJSON, raw)
		}
		return
	}
	http.NotFound(w, nil)
}

type localTestLaunch struct {
	body    []byte
	receipt apiv1.PortalLaunchReceipt
}

// sessionsBody is the fixture Sessions page plus any launched sessions.
func (o *localTestWrites) sessionsBody() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.launched) == 0 {
		return o.sessions
	}
	var page map[string]json.RawMessage
	var rows []json.RawMessage
	if json.Unmarshal(o.sessions, &page) != nil || json.Unmarshal(page["sessions"], &rows) != nil {
		return o.sessions
	}
	rows = append(rows, o.launched...)
	page["sessions"], _ = json.Marshal(rows)
	raw, err := json.Marshal(page)
	if err != nil {
		return o.sessions
	}
	return raw
}

func (o *localTestWrites) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10+1))
	if err != nil || len(body) > 64<<10 {
		http.Error(w, "fixture operator refused", http.StatusRequestEntityTooLarge)
		return
	}
	o.calls.add(localTestCall{Method: r.Method, Path: r.URL.Path, Body: body})
	switch {
	case r.Method == http.MethodPost && r.URL.Path == apiv1.PortalSessionMessagesPath:
		o.send(w, body)
	case r.Method == http.MethodPost && r.URL.Path == apiv1.PortalLaunchPath:
		o.launch(w, body)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, apiv1.PortalAsksPath+"/") && strings.HasSuffix(r.URL.Path, apiv1.AskAnswerSuffix):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, apiv1.PortalAsksPath+"/"), apiv1.AskAnswerSuffix)
		if apiv1.PortalAskAnswerPath(id) != r.URL.Path {
			http.NotFound(w, r)
			return
		}
		o.answer(w, id, body)
	default:
		http.NotFound(w, r)
	}
}

// send accepts one well-formed message request and answers Delivered with a
// fixture pod UID. It delivers nothing.
func (o *localTestWrites) send(w http.ResponseWriter, body []byte) {
	var request apiv1.PortalMessageRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.Project != "dev-env" || !validCommandID(request.RequestID) || request.SessionName == "" || request.SessionUID == "" || request.Text == "" || request.ActorRef == "" {
		http.Error(w, "fixture operator refused", http.StatusBadRequest)
		return
	}
	receipt := apiv1.PortalMessageReceipt{RequestID: request.RequestID, SessionName: request.SessionName, SessionUID: request.SessionUID, ResolvedPodUID: "fixture-pod-" + request.SessionUID, ObservedAt: o.now().UTC(), TransportStatus: apiv1.PortalTransportDelivered, Reason: "delivered"}
	raw, _ := json.Marshal(receipt)
	localTestJSON(w, "application/json", raw)
}

// launch answers like the operator's portal launch route: a new request ID
// creates one Pending fixture session; the same request again returns the same
// receipt; other content under that ID is a conflict. It starts nothing.
func (o *localTestWrites) launch(w http.ResponseWriter, body []byte) {
	var request apiv1.PortalLaunchRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || !launchRequestID.MatchString(request.RequestID) || !slices.Contains(o.repos, request.Repo) || !slices.Contains([]string{"task", "local", "remote"}, request.Mode) || !slices.Contains(apiv1.PortalLaunchModels(), request.Model) || request.ActorRef == "" {
		http.Error(w, "fixture operator refused", http.StatusBadRequest)
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if previous, found := o.launches[request.RequestID]; found {
		if !bytes.Equal(previous.body, body) {
			http.Error(w, "fixture operator conflict", http.StatusConflict)
			return
		}
		raw, _ := json.Marshal(previous.receipt)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
		return
	}
	n := strconv.Itoa(len(o.launched) + 1)
	now := o.now().UTC()
	receipt := apiv1.PortalLaunchReceipt{RequestID: request.RequestID, SessionName: "fixture-launch-" + n, SessionUID: "fixture-uid-launch-" + n, Phase: "Pending", ObservedAt: now}
	row, _ := json.Marshal(map[string]string{"name": receipt.SessionName, "uid": receipt.SessionUID, "repo": request.Repo, "phase": "Pending", "observedAt": now.Format(time.RFC3339), "freshness": "observed"})
	if o.launches == nil {
		o.launches = map[string]localTestLaunch{}
	}
	o.launches[request.RequestID] = localTestLaunch{body: append([]byte(nil), body...), receipt: receipt}
	o.launched = append(o.launched, row)
	raw, _ := json.Marshal(receipt)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(raw)
}

// localTestCurrentAuthorizer is the production group authorizer, asking the
// fixture identity provider's UserInfo route over the fixture net.
func localTestCurrentAuthorizer(store *localTestStore, ca []byte, now func() time.Time, upstreams *localTestNet) (*CurrentAuthorizer, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, portal.ErrUnavailable
	}
	return newCurrentAuthorizer(CurrentAuthorizationConfig{Issuer: localTestIssuer, UserInfoURL: "https://" + localTestIssuerHost + "/application/o/userinfo/", Store: store, Now: now, Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}, upstreams.dial)
}

// localTestUserInfo answers the group check for the fixed principal's
// fixture access token only.
func localTestUserInfo() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/application/o/userinfo/" || r.Header.Get("Authorization") != "Bearer "+localTestAccessToken {
			http.Error(w, "fixture identity provider refused", http.StatusUnauthorized)
			return
		}
		localTestJSON(w, "application/json", []byte(`{"sub":"`+localTestSubject+`","portal_is_active":true,"portal_is_admin":true}`))
	})
}

// localTestHistoryTargets is the fixture history scope: the interactive
// fixture sessions only.
func localTestHistoryTargets() map[string]string {
	return map[string]string{"fixture-uid-coordinator": "fixture-coordinator", "fixture-uid-terminal": "fixture-terminal"}
}

// conversation answers the operator's conversation route with a short fixture
// projection for a history target, and records the read.
func (o *localTestWrites) conversation(w http.ResponseWriter, r *http.Request) {
	o.calls.add(localTestCall{Method: r.Method, Path: r.URL.Path})
	uid := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, apiv1.PortalConversationPrefix), "/conversation")
	name, ok := localTestHistoryTargets()[uid]
	if !ok {
		http.NotFound(w, r)
		return
	}
	at := o.now().UTC()
	projection := protocol.PortalConversation{State: "truncated", ReasonCode: "source_limit", ConversationID: "00000000-0000-4000-8000-0000000000c1", OldestSeq: 1, NewestSeq: 3, ObservedAt: at, Turns: []protocol.PortalConversationTurn{
		{Seq: 1, At: at.Add(-26 * time.Minute), Role: "user", Text: "Fixture conversation: review the portal composer."},
		{Seq: 2, At: at.Add(-24 * time.Minute), Role: "assistant", Text: "Fixture reply: the composer renders."},
		{Seq: 3, At: at.Add(-9 * time.Minute), Role: "delivered-message", From: "fixture-coordinator-parent", Text: "Fixture relay for " + name + "."},
	}}
	raw, _ := json.Marshal(apiv1.PortalConversationResponse{SessionName: name, SessionUID: uid, PodUID: "fixture-pod-" + uid, Projection: projection})
	localTestJSON(w, "application/json", raw)
}

// localTestSend wires the production send handler: every fixture session is a
// send target, receipts live in the instance's in-memory store, and the real
// command client dials the fixture operator.
func localTestSend(engine *Engine, authorizer portal.Authorizer, store *localTestStore, sessionsBody []byte, at time.Time, tokenPath string, ca []byte, now func() time.Time, upstreams *localTestNet) (*commandHandler, error) {
	page, err := decodeSessionPage(sessionsBody, at)
	if err != nil {
		return nil, err
	}
	targets := map[string]string{}
	for _, row := range page.Sessions {
		targets[row.UID] = row.Name
	}
	client, err := newCommandClient(tokenPath, ca, now, upstreams.dial)
	if err != nil {
		return nil, err
	}
	return &commandHandler{scope: &actionScope{Targets: targets}, engine: engine, authorizer: authorizer, receipts: store, transport: client, render: renderCommandReceipt, now: now}, nil
}

// localTestCommand is one reservation and its terminal receipt, if any.
type localTestCommand struct {
	reservation commandReservation
	terminal    *apiv1.PortalMessageReceipt
}

func localTestCommandKey(h Handle, id string) string { return string(h[:]) + "\x00" + id }

// The command receipt methods follow PostgresStore: a reservation belongs to a
// current portal session and expires with it, a replay with other content is
// refused, and a terminal receipt is written once.
func (s *localTestStore) reserveCommand(_ context.Context, h Handle, wanted commandReservation) (bool, error) {
	if !validCommandID(wanted.RequestID) || wanted.SessionName == "" || len(wanted.SessionName) > 253 || wanted.SessionUID == "" || len(wanted.SessionUID) > 128 || wanted.Digest == ([32]byte{}) {
		return false, portal.ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[h]
	if !ok {
		return false, portal.ErrDenied
	}
	wanted.ExpiresAt = session.ExpiresAt
	key := localTestCommandKey(h, wanted.RequestID)
	if existing, found := s.commands[key]; found {
		if !sameCommandReservation(existing.reservation, wanted) {
			return false, portal.ErrDenied
		}
		return false, nil
	}
	if s.commands == nil {
		s.commands = map[string]*localTestCommand{}
	}
	s.commands[key] = &localTestCommand{reservation: wanted}
	return true, nil
}

func (s *localTestStore) readCommand(_ context.Context, h Handle, id string) (commandReservation, *apiv1.PortalMessageReceipt, error) {
	if !validCommandID(id) {
		return commandReservation{}, nil, portal.ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[h]
	command, found := s.commands[localTestCommandKey(h, id)]
	if !ok || !found || !command.reservation.ExpiresAt.Equal(session.ExpiresAt) {
		return commandReservation{}, nil, portal.ErrDenied
	}
	if command.terminal == nil {
		return command.reservation, nil, nil
	}
	receipt := *command.terminal
	return command.reservation, &receipt, nil
}

func (s *localTestStore) finishCommand(_ context.Context, h Handle, reservation commandReservation, receipt apiv1.PortalMessageReceipt) error {
	if !validCommandID(reservation.RequestID) || receipt.RequestID != reservation.RequestID || receipt.SessionName != reservation.SessionName || receipt.SessionUID != reservation.SessionUID || receipt.ObservedAt.IsZero() || (receipt.TransportStatus != apiv1.PortalTransportDelivered && receipt.TransportStatus != apiv1.PortalTransportRefused && receipt.TransportStatus != apiv1.PortalTransportUnknown) || !validCommandReason(receipt.TransportStatus, receipt.Reason) || len(receipt.ResolvedPodUID) > 128 {
		return portal.ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[h]
	command, found := s.commands[localTestCommandKey(h, reservation.RequestID)]
	if !ok || !found || !sameCommandReservation(command.reservation, reservation) || !command.reservation.ExpiresAt.Equal(session.ExpiresAt) {
		return portal.ErrDenied
	}
	if command.terminal != nil {
		if !sameCommandReceipt(*command.terminal, receipt) {
			return portal.ErrDenied
		}
		return nil
	}
	stored := receipt
	command.terminal = &stored
	return nil
}

var _ commandReceipts = (*localTestStore)(nil)
