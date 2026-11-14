package apiserver

import (
	"strings"

	"github.com/thaynes43/dev-env/internal/summonbrief"
	"github.com/thaynes43/dev-env/internal/summonledger"
)

// Bounds from DESIGN-064 5.6. The rendered brief also stays within
// protocol.MaxPromptBytes, checked by the create path.
const (
	summonValueMaxBytes = 4096
	summonDataMaxBytes  = 16384
	summonNameMaxLen    = 60
	// summonReservedPrefix is the platform's filing prefix. No caller-driven
	// create may use it, including a name generated from the repository. The
	// platform's own filing path is a later story and is not this check.
	summonReservedPrefix = "esc-rem-"
	// summonCurationPrefix is curation's prefix. An upgrade entry cannot declare
	// it, and a requested upgrade name must not use it either (OP757).
	summonCurationPrefix = "wo-cigar-curate-"
	// summonUpgradeStaleDefault is the design default when a remote upgrade
	// entry omits staleAfter.
	summonUpgradeStaleDefault = "180m"
	// summonLedgerNamespace and summonLedgerName name the create ledger
	// (DESIGN-064 5.9, summon_ledger.go): one ConfigMap in the operator's own
	// namespace. GitOps precreates it; the operator needs get and update on it.
	// Without it a summoning create is refused with 503.
	summonLedgerNamespace = summonledger.Namespace
	summonLedgerName      = summonledger.Name
	// summonMaxSuffix is the last name suffix a re-fired key may take (6.4).
	summonMaxSuffix = summonledger.MaxSuffix
)

// renderSummonBrief renders a summon brief (DESIGN-064 5.6) with
// internal/summonbrief.Render, a leaf package the operator leader's filing
// can import too (story 800 moved it there): the template verbatim, the
// platform line, then the data in a block fenced by boundary, every value
// sanitized.
func renderSummonBrief(name, policy, lane, template string, fields []string, data map[string]string, boundary string) string {
	return summonbrief.Render(name, policy, lane, template, fields, data, boundary)
}

// refuseReservedSessionName refuses a final session name in the platform's
// filing prefix. Caller-driven creates call it after the name is chosen and
// before the write. The platform's own filing path is not built here.
func refuseReservedSessionName(name string) error {
	if strings.HasPrefix(name, summonReservedPrefix) {
		return forbidden("esc-rem- is reserved for the platform's filing (DESIGN-064 6.4)")
	}
	return nil
}
