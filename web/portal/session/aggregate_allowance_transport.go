package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/internal/tokenfile"
	"github.com/thaynes43/dev-env/web/portal"
)

const (
	aggregateAllowancePath      = "/v1/provider-allowances"
	aggregateAllowanceConfigRel = "config/provider-allowances.json"
)

var aggregateAllowanceConfigFields = []string{"route", "method", "maxAgeSeconds", "contexts", "registry"}

// aggregateAllowanceConnection is one immutable common-route fetch.
// A nil connection performs no read and keeps no response body.
type aggregateAllowanceConnection struct {
	client    *http.Client
	tokenPath string
	contexts  []providerallowance.TrustedContext
	registry  providerallowance.Registry
	policy    AllowanceReadPolicy
}

type aggregateUnavailableReader struct{}

func (aggregateUnavailableReader) Allowances(time.Time) []portal.ProductAllowance {
	return MapProductAllowance(providerallowance.ReadResponse{})
}

// projectAuthorizedAggregateAllowance fetches the common route at most once.
// It does not assign SessionSource.allowance and does not run when the Codex
// fixed-slot consumer is selected. A nil connection leaves the page unchanged.
func (s *SessionSource) projectAuthorizedAggregateAllowance(ctx context.Context, page portal.ProductPage) portal.ProductPage {
	if s == nil || s.aggregateAllowance == nil || s.currentSource != nil || s.now == nil || page.ReadFailure != nil {
		return page
	}
	return applyCachedAllowance(page, s.aggregateAllowance.reader(ctx, s), s.now)
}

func (c *aggregateAllowanceConnection) reader(ctx context.Context, src *SessionSource) AllowanceReader {
	raw, err := c.fetch(ctx, src)
	if err != nil {
		return aggregateUnavailableReader{}
	}
	return NewCachedAllowanceReader(raw, providerallowance.FixtureParseLimits, c.contexts, c.registry, c.policy)
}

func (c *aggregateAllowanceConnection) fetch(ctx context.Context, src *SessionSource) ([]byte, error) {
	if c == nil || src == nil || c.client == nil || src.now == nil || ctx == nil || ctx.Err() != nil {
		return nil, errCurrentSourceRead
	}
	token, err := operatorCredential(c.tokenPath, src.now())
	if err != nil {
		return nil, errCurrentSourceRead
	}
	hc := *c.client
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	body, err := productGet(ctx, &hc, operatorPortalOrigin+aggregateAllowancePath, token, int64(providerallowance.FixtureParseLimits.MaxBytes), mediaJSON)
	if err != nil || len(body) == 0 || len(body) > providerallowance.FixtureParseLimits.MaxBytes {
		return nil, errCurrentSourceRead
	}
	return append([]byte(nil), body...), nil
}

// attachAggregateAllowanceConfig reads the optional consumer file.
// An absent file performs no fetch. A present file that conflicts with the
// Codex current-source consumer, or that is not the exact GET document, fails
// closed. The mounted file is not a deployed manifest.
func attachAggregateAllowanceConfig(root string, sessions *SessionSource) error {
	if root == "" {
		return errProductionConfig
	}
	path := filepath.Join(root, aggregateAllowanceConfigRel)
	data, err := tokenfile.Read(path, tokenfile.MaxBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || len(data) == 0 {
		return errProductionConfig
	}
	if sessions == nil || sessions.currentSource != nil || sessions.client == nil || sessions.now == nil || sessions.path == "" {
		return errProductionConfig
	}
	trust, err := decodeAggregateAllowanceConfig(data)
	if err != nil || trust.Registry == nil {
		return errProductionConfig
	}
	copied := make([]providerallowance.TrustedContext, len(trust.Contexts))
	copy(copied, trust.Contexts)
	sessions.aggregateAllowance = &aggregateAllowanceConnection{
		client: sessions.client, tokenPath: sessions.path, contexts: copied, registry: trust.Registry,
		policy: AllowanceReadPolicy{MaxAge: time.Duration(trust.MaxAge) * time.Second, Set: true},
	}
	return nil
}

func decodeAggregateAllowanceConfig(raw []byte) (providerallowance.AggregateTrust, error) {
	if len(raw) > 0 && raw[len(raw)-1] == '\n' {
		raw = raw[:len(raw)-1]
	}
	if !providerallowance.ExactObjectKeys(raw, aggregateAllowanceConfigFields) {
		return providerallowance.AggregateTrust{}, errProductionConfig
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var doc struct {
		Route         string          `json:"route"`
		Method        string          `json:"method"`
		MaxAgeSeconds int64           `json:"maxAgeSeconds"`
		Contexts      json.RawMessage `json:"contexts"`
		Registry      json.RawMessage `json:"registry"`
	}
	if err := dec.Decode(&doc); err != nil {
		return providerallowance.AggregateTrust{}, errProductionConfig
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return providerallowance.AggregateTrust{}, errProductionConfig
	}
	if doc.Route != aggregateAllowancePath || doc.Method != http.MethodGet {
		return providerallowance.AggregateTrust{}, errProductionConfig
	}
	trustRaw, err := json.Marshal(struct {
		MaxAgeSeconds int64           `json:"maxAgeSeconds"`
		Contexts      json.RawMessage `json:"contexts"`
		Registry      json.RawMessage `json:"registry"`
	}{MaxAgeSeconds: doc.MaxAgeSeconds, Contexts: doc.Contexts, Registry: doc.Registry})
	if err != nil {
		return providerallowance.AggregateTrust{}, errProductionConfig
	}
	trust, err := providerallowance.ParseAggregateTrust(trustRaw)
	if err != nil {
		return providerallowance.AggregateTrust{}, errProductionConfig
	}
	return trust, nil
}
