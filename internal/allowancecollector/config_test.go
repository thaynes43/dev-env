package allowancecollector

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	pa "github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestCollectorConfigRefusesUnqualifiedTrustAndPartialOwnership(t *testing.T) {
	now := time.Date(2026, 11, 2, 21, 0, 0, 0, time.UTC)
	cfg := collectorConfig(t, now)
	raw, _ := json.Marshal(cfg)
	for _, mode := range []string{"extra-key", "alias-key", "future-expiry", "wrong-sa", "wrong-publisher", "wrong-version", "missing-workspace", "wrong-capability", "unverified", "current-metric", "duplicate-account"} {
		t.Run(mode, func(t *testing.T) {
			body := append([]byte(nil), raw...)
			trust := cfg.trust
			trust.Contexts = append([]pa.TrustedContext(nil), cfg.trust.Contexts...)
			parts := pa.RegistryParts{Sources: trust.Registry.Sources(), Metrics: trust.Registry.Metrics(), Accounts: trust.Registry.Accounts()}
			publisher := hostUID
			switch mode {
			case "extra-key":
				body = []byte(strings.TrimSuffix(string(body), "}") + `,"canary":"do-not-echo"}`)
			case "alias-key":
				body = []byte(strings.Replace(string(body), `"slotUID"`, `"SlotUID"`, 1))
			case "future-expiry":
				body = []byte(strings.Replace(string(body), cfg.ValidUntil, now.Add(8*24*time.Hour).Format(time.RFC3339Nano), 1))
			case "wrong-sa":
				body = []byte(strings.Replace(string(body), `dev-env-workbench`, `other`, 1))
			case "wrong-publisher":
				publisher = "44444444-4444-4444-4444-444444444444"
			case "wrong-version":
				trust.Contexts[0].SourceVersion = "unqualified"
			case "missing-workspace":
				trust.Contexts[1].WorkspaceRef = nil
			case "wrong-capability":
				parts.Sources[0].CapabilityRef = "unqualified"
			case "unverified":
				trust.Contexts[0].BindingState = pa.BindingUnknown
			case "current-metric":
				parts.Metrics[0].HistoricalOnly = false
			case "duplicate-account":
				parts.Accounts = append(parts.Accounts, parts.Accounts[0])
			}
			trust.Registry = pa.NewStaticRegistry(parts)
			if _, err := ParseConfig(body, trust, publisher, "dev-env-workbench", now); err == nil || strings.Contains(err.Error(), "do-not-echo") {
				t.Fatal("unsafe config or echoed input", err)
			}
		})
	}
}
