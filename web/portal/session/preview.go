package session

import (
	"bytes"
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
	return portal.RenderProduct(page)
}

// previewForms numbers fixture request IDs. Its forms post nowhere real.
type previewForms struct{ n int }

func (p *previewForms) NewRequestID() (string, error) {
	p.n++
	return fmt.Sprintf("fixture-request-%04d", p.n), nil
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
	reasons := map[string]string{"Delivered": "", "Refused": "The agent is not running.", "Pending": "", "Unknown": "The delivery call timed out."}
	out := map[string][]byte{}
	for status, reason := range reasons {
		body, err := portal.RenderReceipt(portal.ProductReceipt{RequestID: "fixture-request-0001", Session: "fixture-coordinator", SessionUID: "fixture-uid-coordinator", ResolvedPodUID: "fixture-pod-uid-coordinator", TransportStatus: status, Reason: reason, ObservedAt: now, Preview: true})
		if err != nil {
			return nil, err
		}
		out[strings.ToLower(status)] = body
	}
	return out, nil
}
