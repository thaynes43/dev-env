// Package allowancecollector owns the default-off operator allowance schedule.
// It uses fixed model-free readers in one selected existing remote owner. It
// does not create sessions, grant access, copy credentials or resume native work.
package allowancecollector

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

var ErrCollector = errors.New("allowance collector unavailable")

const Namespace = "dev-agents"
const StateKey = "collector.json"
const RemoteLimit = 120 * time.Second
const intentMargin = 30 * time.Second

type Host struct {
	Name           string `json:"name"`
	SessionUID     string `json:"sessionUID"`
	Generation     int64  `json:"generation"`
	PodUID         string `json:"podUID"`
	ServiceAccount string `json:"serviceAccount"`
	Image          string `json:"image"`
}

type Config struct {
	Version        int    `json:"version"`
	Host           Host   `json:"host"`
	SlotUID        string `json:"slotUID"`
	ValidUntil     string `json:"validUntil"`
	CadenceSeconds int64  `json:"cadenceSeconds"`
	BackoffSeconds int64  `json:"backoffSeconds"`
	trust          providerallowance.AggregateTrust
	fingerprint    string
	until          time.Time
}

var uuid = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var name = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
var image = regexp.MustCompile(`^ghcr\.io/thaynes43/dev-env:[a-zA-Z0-9._-]+@sha256:[a-f0-9]{64}$`)
var account = regexp.MustCompile(`^(openai|anthropic|cursor)-email-sha256:[a-f0-9]{64}$`)
var claudeWorkspace = regexp.MustCompile(`^anthropic-org-sha256:[a-f0-9]{64}$`)

// ParseConfig requires independently selected aggregate trust and the exact
// existing host identity. Supplying bytes does not activate a runnable or IO.
func ParseConfig(raw []byte, trust providerallowance.AggregateTrust, publisherUID, publisherSA string, now time.Time) (*Config, error) {
	if now.IsZero() || len(raw) == 0 || len(raw) > 8<<10 || !providerallowance.ValidJSONDocument(raw, 8<<10) || !providerallowance.ExactObjectKeys(raw, []string{"version", "host", "slotUID", "validUntil", "cadenceSeconds", "backoffSeconds"}) {
		return nil, ErrCollector
	}
	var host struct {
		Host json.RawMessage `json:"host"`
	}
	if json.Unmarshal(raw, &host) != nil || !providerallowance.ExactObjectKeys(host.Host, []string{"name", "sessionUID", "generation", "podUID", "serviceAccount", "image"}) {
		return nil, ErrCollector
	}
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&cfg) != nil || dec.Decode(new(any)) != io.EOF {
		return nil, ErrCollector
	}
	until, err := time.Parse(time.RFC3339Nano, cfg.ValidUntil)
	if err != nil || !until.After(now) || until.After(now.Add(7*24*time.Hour)) || cfg.Version != 1 || !name.MatchString(cfg.Host.Name) || !uuid.MatchString(cfg.Host.SessionUID) || !uuid.MatchString(cfg.Host.PodUID) || cfg.Host.Generation < 1 || cfg.Host.ServiceAccount != "dev-env-workbench" || !image.MatchString(cfg.Host.Image) || !uuid.MatchString(cfg.SlotUID) || cfg.CadenceSeconds < 300 || cfg.CadenceSeconds > 1800 || cfg.BackoffSeconds < cfg.CadenceSeconds || cfg.BackoffSeconds > 3600 || publisherUID != cfg.Host.SessionUID || publisherSA != cfg.Host.ServiceAccount {
		return nil, ErrCollector
	}
	if trust.Registry == nil || len(trust.Contexts) != 3 || trust.MaxAge < 1 || trust.MaxAge > 1800 || len(trust.Registry.Sources()) != 3 || len(trust.Registry.Accounts()) != 3 || len(trust.Registry.Entitlements()) != 0 || len(trust.Registry.Projections()) != 0 {
		return nil, ErrCollector
	}
	expected := map[string]struct {
		kind       providerallowance.SourceKind
		version    string
		capability string
	}{"openai": {providerallowance.SourceNativeRPC, providerallowance.CodexRPCSourceVersion, providerallowance.CodexRPCCapability}, "anthropic": {providerallowance.SourceNativeRPC, providerallowance.ClaudeSDKSourceVersion, "claude-sdk:usage-report"}, "cursor": {providerallowance.SourceNativeDisplay, providerallowance.CursorCLISourceVersion, providerallowance.CursorCLICapability}}
	for _, ctx := range trust.Contexts {
		want, ok := expected[ctx.Provider]
		if !ok || ctx.SourceKind != want.kind || ctx.SourceVersion != want.version || ctx.BindingState != providerallowance.BindingVerified || ctx.AccountRef == nil || !account.MatchString(*ctx.AccountRef) || len(*ctx.AccountRef) < len(ctx.Provider)+1 || (*ctx.AccountRef)[:len(ctx.Provider)+1] != ctx.Provider+"-" || ctx.BindingGeneration < 1 {
			return nil, ErrCollector
		}
		if ctx.Provider == "anthropic" {
			if ctx.WorkspaceRef == nil || !claudeWorkspace.MatchString(*ctx.WorkspaceRef) {
				return nil, ErrCollector
			}
		} else if ctx.WorkspaceRef != nil {
			return nil, ErrCollector
		}
		for _, rule := range trust.Registry.Sources() {
			if rule.Provider == ctx.Provider && rule.CapabilityRef != want.capability {
				return nil, ErrCollector
			}
		}
		delete(expected, ctx.Provider)
	}

	// An incomplete trust registry must refuse before any provider operation.
	for _, ctx := range trust.Contexts {
		sources, accounts, metrics := 0, 0, 0
		for _, rule := range trust.Registry.Sources() {
			if rule.Provider == ctx.Provider && rule.Kind == ctx.SourceKind && rule.Version == ctx.SourceVersion && rule.AllowNormalizedMeasures {
				sources++
			}
		}
		workspace := ""
		if ctx.WorkspaceRef != nil {
			workspace = *ctx.WorkspaceRef
		}
		for _, rule := range trust.Registry.Accounts() {
			if rule.AccountRef == *ctx.AccountRef && rule.WorkspaceRef == workspace && rule.Generation == ctx.BindingGeneration && rule.ProducerRef == ctx.ProducerRef && rule.ProducerIncarnation == ctx.ProducerIncarnation && rule.AccountScopeOnly == (ctx.WorkspaceRef == nil) {
				accounts++
			}
		}
		for _, rule := range trust.Registry.Metrics() {
			if rule.Provider == ctx.Provider && rule.SourceKind == ctx.SourceKind && rule.SourceVersion == ctx.SourceVersion {
				metrics++
			}
		}
		if sources != 1 || accounts != 1 || metrics == 0 {
			return nil, ErrCollector
		}
	}
	// Own the selected values so later caller mutation cannot drift the durable
	// fingerprint, account fences or registry underneath a running collector.
	trust.Contexts = append([]providerallowance.TrustedContext(nil), trust.Contexts...)
	for i := range trust.Contexts {
		if trust.Contexts[i].AccountRef != nil {
			v := *trust.Contexts[i].AccountRef
			trust.Contexts[i].AccountRef = &v
		}
		if trust.Contexts[i].WorkspaceRef != nil {
			v := *trust.Contexts[i].WorkspaceRef
			trust.Contexts[i].WorkspaceRef = &v
		}
	}
	trust.Registry = providerallowance.NewStaticRegistry(providerallowance.RegistryParts{Sources: trust.Registry.Sources(), Metrics: trust.Registry.Metrics(), Accounts: trust.Registry.Accounts(), Entitlements: trust.Registry.Entitlements(), Projections: trust.Registry.Projections()})
	for _, rule := range trust.Registry.Metrics() {
		if !rule.HistoricalOnly {
			return nil, ErrCollector
		}
	}
	// The fingerprint is private change detection, not anonymization. It covers
	// caller trust too, so a configuration/account rebind cannot reuse an intent.
	canonical, err := json.Marshal(struct {
		Config   Config
		Contexts []providerallowance.TrustedContext
		Registry providerallowance.RegistryParts
		MaxAge   int64
	}{cfg, trust.Contexts, providerallowance.RegistryParts{Sources: trust.Registry.Sources(), Metrics: trust.Registry.Metrics(), Accounts: trust.Registry.Accounts(), Entitlements: trust.Registry.Entitlements(), Projections: trust.Registry.Projections()}, trust.MaxAge})
	if err != nil {
		return nil, ErrCollector
	}
	sum := sha256.Sum256(canonical)
	cfg.fingerprint, cfg.until, cfg.trust = hex.EncodeToString(sum[:]), until, trust
	return &cfg, nil
}
