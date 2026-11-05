//go:build portallocaltest

package session

// Write flows of the portal local test instance (DESIGN-055 addendum,
// OP479): send, launch, history and Asks answers run through the production
// handlers against fixture operator routes that record every call. Nothing
// here reaches a real operator, agent, shelf or Asks API.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

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
