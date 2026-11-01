package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
)

const (
	// currentSourceReportPath is the only slot this consumer reads. It is the
	// same path as apiserver.ProviderSourceReportPath. This package does not
	// import the operator server and does not register the route.
	currentSourceReportPath = "/v1/provider-source-reports/codex-current-source"
	// currentSourceSlotName is the only slot name config/current-source.json may name.
	currentSourceSlotName = "codex-current-source"
	// currentSourceReportMaxBytes matches DecodeCurrentSource's 16 KiB ceiling.
	currentSourceReportMaxBytes int64 = 16 << 10
	// currentSourceMaxAgeSeconds is the validator's 24 hour ceiling, in seconds.
	currentSourceMaxAgeSeconds int64 = 24 * 60 * 60
	currentSourceConfigRel           = "config/current-source.json"
)

// errCurrentSourceRead is the fixed transport failure. Its text is not a
// response body, a token, or a selection id.
var errCurrentSourceRead = errors.New("current source unavailable")

// currentSourceConnection is one immutable fixed-slot fetch.
// A nil connection performs no read. The selection is copied at configuration.
// A request never writes SessionSource.allowance and never retains another
// request's body.
type currentSourceConnection struct {
	client    *http.Client
	tokenPath string
	selection providerallowance.CurrentSourceSelection
}

// currentSourceUnavailableReader is the fixed Unknown panel for a failed
// selected-source read. It has no windows and no source bytes.
type currentSourceUnavailableReader struct{}

func (currentSourceUnavailableReader) Allowances(time.Time) []portal.ProductAllowance {
	return currentSourceUnavailable()
}

// projectAuthorizedCurrentSource fetches the fixed slot at most once and
// projects that request's bytes. It does not assign SessionSource.allowance.
// The projection replaces the page allowance, so a bound reader cannot merge
// into or remain beside the selected report. A nil connection leaves the page
// unchanged. Call it only after Authentik active and admin authorization, and
// only for a Sessions read that produced an inventory.
func (s *SessionSource) projectAuthorizedCurrentSource(ctx context.Context, page portal.ProductPage) portal.ProductPage {
	if s == nil || s.currentSource == nil || s.now == nil || page.ReadFailure != nil {
		return page
	}
	return applyCachedAllowance(page, s.currentSource.reader(ctx, s), s.now)
}

func (c *currentSourceConnection) reader(ctx context.Context, src *SessionSource) AllowanceReader {
	raw, err := c.fetch(ctx, src)
	if err != nil {
		return currentSourceUnavailableReader{}
	}
	report, err := providerallowance.DecodeCurrentSource(raw)
	if err != nil || report.ProviderUpdatedAt != nil {
		return currentSourceUnavailableReader{}
	}
	return NewCurrentSourceReader(raw, c.selection)
}

func (c *currentSourceConnection) fetch(ctx context.Context, src *SessionSource) ([]byte, error) {
	if c == nil || src == nil || c.client == nil || src.now == nil || ctx == nil || ctx.Err() != nil {
		return nil, errCurrentSourceRead
	}
	token, err := operatorCredential(c.tokenPath, src.now())
	if err != nil {
		return nil, errCurrentSourceRead
	}
	body, err := productGet(ctx, c.client, operatorPortalOrigin+currentSourceReportPath, token, currentSourceReportMaxBytes, mediaJSON)
	if err != nil || int64(len(body)) == 0 || int64(len(body)) > currentSourceReportMaxBytes {
		return nil, errCurrentSourceRead
	}
	return append([]byte(nil), body...), nil
}

// attachCurrentSourceConfig reads the optional consumer file. An absent file
// leaves the session disconnected and returns nil. A present file copies one
// selection onto sessions and does not set allowance, install a flag, or dial.
// The mounted file is not a deployed manifest.
func attachCurrentSourceConfig(root string, sessions *SessionSource) error {
	if root == "" {
		return errProductionConfig
	}
	path := filepath.Join(root, currentSourceConfigRel)
	present, err := mountedPresent(path)
	if err != nil {
		return errProductionConfig
	}
	if !present {
		return nil
	}
	if sessions == nil || sessions.client == nil || sessions.now == nil || sessions.path == "" {
		return errProductionConfig
	}
	text, err := readMountedText(root, currentSourceConfigRel, 4096)
	if err != nil {
		return errProductionConfig
	}
	selection, err := decodeCurrentSourceConfig([]byte(text))
	if err != nil {
		return errProductionConfig
	}
	sessions.currentSource = &currentSourceConnection{
		client:    sessions.client,
		tokenPath: sessions.path,
		selection: copyCurrentSourceSelection(selection),
	}
	return nil
}

func decodeCurrentSourceConfig(raw []byte) (providerallowance.CurrentSourceSelection, error) {
	if !currentSourceConfigKeysOnce(raw) {
		return providerallowance.CurrentSourceSelection{}, errProductionConfig
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var doc struct {
		Slot                string `json:"slot"`
		PublisherRef        string `json:"publisherRef"`
		ProducerIncarnation string `json:"producerIncarnation"`
		SourceGeneration    int64  `json:"sourceGeneration"`
		MaxAgeSeconds       int64  `json:"maxAgeSeconds"`
	}
	if err := dec.Decode(&doc); err != nil {
		return providerallowance.CurrentSourceSelection{}, errProductionConfig
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return providerallowance.CurrentSourceSelection{}, errProductionConfig
	}
	if doc.Slot != currentSourceSlotName || doc.PublisherRef != providerallowance.CurrentSourcePublisher || !currentSourceConfigID(doc.ProducerIncarnation) {
		return providerallowance.CurrentSourceSelection{}, errProductionConfig
	}
	if doc.SourceGeneration <= 0 || doc.MaxAgeSeconds <= 0 || doc.MaxAgeSeconds > currentSourceMaxAgeSeconds {
		return providerallowance.CurrentSourceSelection{}, errProductionConfig
	}
	return providerallowance.CurrentSourceSelection{
		PublisherRef:        doc.PublisherRef,
		ProducerIncarnation: doc.ProducerIncarnation,
		SourceGeneration:    doc.SourceGeneration,
		MaxAge:              time.Duration(doc.MaxAgeSeconds) * time.Second,
	}, nil
}

func currentSourceConfigKeysOnce(raw []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return false
	}
	seen := map[string]struct{}{}
	for dec.More() {
		keyTok, keyErr := dec.Token()
		key, ok := keyTok.(string)
		if keyErr != nil || !ok {
			return false
		}
		if _, dup := seen[key]; dup {
			return false
		}
		seen[key] = struct{}{}
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return false
		}
	}
	end, endErr := dec.Token()
	if endErr != nil || end != json.Delim('}') {
		return false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return false
	}
	for _, key := range []string{"slot", "publisherRef", "producerIncarnation", "sourceGeneration", "maxAgeSeconds"} {
		if _, ok := seen[key]; !ok {
			return false
		}
	}
	return len(seen) == 5
}

func currentSourceConfigID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7E {
			return false
		}
	}
	return true
}
