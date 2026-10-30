package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

// productFixture is a synthetic upstream response and the clock to read it at.
type productFixture struct {
	Version int             `json:"version"`
	Now     time.Time       `json:"now"`
	Body    json.RawMessage `json:"body"`
}

// RenderSessionsFixture renders a synthetic operator session page through the
// same decoder and page builder the live Sessions read uses, labelled as a
// fixture preview. It opens no connection and reads no credential.
func RenderSessionsFixture(source io.Reader) ([]byte, error) {
	f, err := readProductFixture(source)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeSessionPage(f.Body, f.Now)
	if err != nil {
		return nil, err
	}
	page := sessionsPage(decoded.Sessions, decoded.Complete, decoded.Freshness, f.Now, portal.ProductFilter{})
	return renderFixture(page, f.Now)
}

// RenderTasksFixture renders one synthetic OpenProject collection page through
// the live Tasks decoder and page builder, labelled as a fixture preview.
func RenderTasksFixture(source io.Reader) ([]byte, error) {
	f, err := readProductFixture(source)
	if err != nil {
		return nil, err
	}
	rows, _, complete, err := decodeTaskPage(f.Body, 1, map[int]struct{}{}, f.Now)
	if err != nil {
		return nil, err
	}
	return renderFixture(tasksPage(rows, complete, portal.ProductFilter{}), f.Now)
}

func readProductFixture(source io.Reader) (productFixture, error) {
	var f productFixture
	raw, err := io.ReadAll(io.LimitReader(source, 256<<10+1))
	if err != nil || len(raw) > 256<<10 {
		return f, portal.ErrUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&f) != nil || !errors.Is(d.Decode(new(any)), io.EOF) || f.Version != 1 || f.Now.IsZero() || len(f.Body) == 0 {
		return productFixture{}, portal.ErrUnavailable
	}
	return f, nil
}

func renderFixture(page portal.ProductPage, now time.Time) ([]byte, error) {
	page.Preview = true
	page.ObservedAt = now.UTC().Format(time.RFC3339)
	if page.Connected && page.ReadFailure == nil {
		page.ReadCompletedAt = page.ObservedAt
	}
	return portal.RenderProduct(page)
}

// previewForms numbers fixture request IDs. Its forms post nowhere real.
type previewForms struct{ n int }

func (p *previewForms) NewRequestID() (string, error) {
	p.n++
	return fmt.Sprintf("fixture-request-%04d", p.n), nil
}

// LaunchRepos gives the fixture caller two fixture repositories.
func (p *previewForms) LaunchRepos(portal.Principal) []string {
	return []string{"fixture-repo-one", "fixture-repo-two"}
}

// RenderSessionsLaunchFixture renders the Sessions fixture as a page whose
// launch route is configured for the caller: it carries the New agent form with
// a fixture CSRF value, labelled a fixture preview. It contacts nothing.
func RenderSessionsLaunchFixture(source io.Reader) ([]byte, error) {
	f, err := readProductFixture(source)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeSessionPage(f.Body, f.Now)
	if err != nil {
		return nil, err
	}
	page := sessionsPage(decoded.Sessions, decoded.Complete, decoded.Freshness, f.Now, portal.ProductFilter{})
	page = withLaunchForm(page, portal.Principal{}, strings.Repeat("F", 43), &previewForms{})
	return renderFixture(page, f.Now)
}

// RenderLaunchResultFixtures renders one fixture result page per outcome the
// launch route can answer with a page.
func RenderLaunchResultFixtures() (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, outcome := range []string{"limit", "unavailable", "invalid", "conflict", "unknown"} {
		r := portal.ProductLaunchResult{Outcome: outcome, Back: SessionsPath + "#launch", Preview: true}
		if outcome == "invalid" {
			r.Fields = []portal.ProductField{{Label: "model", Value: "Fixture field message."}}
		}
		body, err := portal.RenderLaunchResult(r)
		if err != nil {
			return nil, err
		}
		out[outcome] = body
	}
	return out, nil
}

// RenderSessionsSendFixture renders the Sessions fixture as a page whose send
// route is configured: reachable sessions get forms with a fixture CSRF value
// and request IDs. It is labelled a fixture preview and contacts nothing.
func RenderSessionsSendFixture(source io.Reader) ([]byte, error) {
	f, err := readProductFixture(source)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeSessionPage(f.Body, f.Now)
	if err != nil {
		return nil, err
	}
	page := sessionsPage(decoded.Sessions, decoded.Complete, decoded.Freshness, f.Now, portal.ProductFilter{})
	return renderFixture(withSendForms(page, strings.Repeat("F", 43), &previewForms{}), f.Now)
}

// RenderReceiptFixtures renders one fixture receipt per transport status.
func RenderReceiptFixtures(now time.Time) (map[string][]byte, error) {
	reasons := map[string]string{"Delivered": "delivered", "Refused": "refused", "Pending": "", "Unknown": "unknown"}
	out := map[string][]byte{}
	for status, reason := range reasons {
		body, err := portal.RenderReceipt(portal.ProductReceipt{RequestID: "fixture-request-0001", Session: "fixture-coordinator", SessionUID: "fixture-uid-coordinator", ResolvedPodUID: "fixture-pod-uid-coordinator", TransportStatus: status, ReasonCode: reason, ObservedAt: now, Preview: true})
		if err != nil {
			return nil, err
		}
		out[strings.ToLower(status)] = body
	}
	return out, nil
}

// previewConversation is a fixture conversation for the selected-session
// preview. It is sample text, not any session's history.
type previewConversation struct{ now time.Time }

func (p previewConversation) ReadConversation(_ context.Context, name, _ string) (Conversation, error) {
	return Conversation{State: "truncated", Turns: []ConversationTurn{
		{At: p.now.Add(-26 * time.Minute), Role: "user", Text: "Sample: review the portal message box and report what is still missing."},
		{At: p.now.Add(-24 * time.Minute), Role: "assistant", Text: "Sample: the composer renders; history needs a supported read route.\nNext: define the contract with the backend lane."},
		{At: p.now.Add(-9 * time.Minute), Role: "delivered-message", From: "fixture-coordinator-parent", Text: "Sample: relay received for " + name + "."},
		{At: p.now.Add(-8 * time.Minute), Role: "assistant", Text: "Sample: acknowledged.", Truncated: true},
	}}, nil
}

// RenderSessionsHistoryFixture renders the Sessions fixture with its
// coordinator selected: a fixture conversation and send forms, labelled a
// fixture preview. It contacts nothing.
func RenderSessionsHistoryFixture(source io.Reader) ([]byte, error) {
	f, err := readProductFixture(source)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeSessionPage(f.Body, f.Now)
	if err != nil {
		return nil, err
	}
	filter := portal.ProductFilter{Session: "fixture-uid-coordinator"}
	page := sessionsPage(decoded.Sessions, decoded.Complete, decoded.Freshness, f.Now, filter)
	page = withHistory(context.Background(), page, filter.Session, previewConversation{now: f.Now}, f.Now)
	return renderFixture(withSendForms(page, strings.Repeat("F", 43), &previewForms{}), f.Now)
}
