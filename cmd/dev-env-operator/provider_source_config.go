package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/apiserver"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// Fixed Codex current-source slot. These are the contract, not parameters.
// The route, ConfigMap, key, publisher, portal ServiceAccount and selection
// identity stay closed. Maximum age is the only numeric setting, and only
// inside an explicit enable. Nothing here reads Kubernetes.
const providerSourceSlot = "codex-current-source"
const providerSourceSlotNamespace = "dev-env-system"
const providerSourceSlotName = "dev-env-provider-source-codex-current"
const providerSourceSessionNamespace = "dev-agents"

// providerSourceSessionName is the session object the fixed UID identifies.
// The handler checks that UID and the current pod. It does not pin a pod UID.
const providerSourceSessionName = "dev-env-1027-010058"
const providerSourceSessionUID = types.UID("84ddc461-fd07-47ab-a2c1-72910898bac4")
const providerSourcePublisherSA = "dev-env-workbench"
const providerSourcePortalNamespace = "dev-agents"
const providerSourcePortalSA = "dev-env-v2-portal"
const providerSourceIncarnation = "codex-retained-tool-source-1"
const providerSourceGeneration = int64(1)
const providerSourceMaxAgeSeconds = int64(1800)
const providerSourceConfigMaxBytes = 4096

// errProviderSourceConfig is the only refusal for this flag. It is fixed and
// contains no submitted text.
var errProviderSourceConfig = errors.New("provider source configuration was refused")

var providerSourceConfigFields = []string{
	"slot",
	"publisherRef",
	"producerIncarnation",
	"sourceGeneration",
	"maxAgeSeconds",
}

// providerSourceSelection is the accepted age. Every other identity is a constant.
// Nil means the route stays off.
type providerSourceSelection struct {
	maxAge time.Duration
}

// parseProviderSourceConfig accepts only an explicit enable plus the exact
// selection document. An empty flag is off. Maximum age alone, a disabled
// flag with a document, and any malformed, unknown, incomplete, out-of-scope,
// non-positive or too-old setting refuse. The API and the portal reader are
// not turned on here.
func parseProviderSourceConfig(enabled bool, raw, apiAddr, sessionNamespace string) (*providerSourceSelection, error) {
	if !enabled {
		if raw != "" {
			return nil, errProviderSourceConfig
		}
		return nil, nil
	}
	age, err := parseProviderSourceDocument(raw)
	if err != nil {
		return nil, errProviderSourceConfig
	}
	if apiAddr == "0" || sessionNamespace != providerSourceSessionNamespace {
		return nil, errProviderSourceConfig
	}
	return &providerSourceSelection{maxAge: age}, nil
}

func parseProviderSourceDocument(raw string) (time.Duration, error) {
	if raw == "" || len(raw) > providerSourceConfigMaxBytes || !utf8.ValidString(raw) || bytes.HasPrefix([]byte(raw), []byte{0xEF, 0xBB, 0xBF}) {
		return 0, errProviderSourceConfig
	}
	body := []byte(raw)
	if !providerSourceConfigExactKeys(body) {
		return 0, errProviderSourceConfig
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var doc struct {
		Slot                string `json:"slot"`
		PublisherRef        string `json:"publisherRef"`
		ProducerIncarnation string `json:"producerIncarnation"`
		SourceGeneration    int64  `json:"sourceGeneration"`
		MaxAgeSeconds       int64  `json:"maxAgeSeconds"`
	}
	if err := dec.Decode(&doc); err != nil {
		return 0, errProviderSourceConfig
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return 0, errProviderSourceConfig
	}
	if doc.Slot != providerSourceSlot || doc.PublisherRef != providerallowance.CurrentSourcePublisher || doc.ProducerIncarnation != providerSourceIncarnation || doc.SourceGeneration != providerSourceGeneration {
		return 0, errProviderSourceConfig
	}
	if doc.MaxAgeSeconds <= 0 || doc.MaxAgeSeconds > providerSourceMaxAgeSeconds {
		return 0, errProviderSourceConfig
	}
	return time.Duration(doc.MaxAgeSeconds) * time.Second, nil
}

func providerSourceConfigExactKeys(raw []byte) bool {
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
	if len(seen) != len(providerSourceConfigFields) {
		return false
	}
	for _, key := range providerSourceConfigFields {
		if _, ok := seen[key]; !ok {
			return false
		}
	}
	return true
}

// providerSourceReports builds the existing handler from the manager client
// and the API reader. A nil selection returns nil and does not use either
// client. The store is the existing ProviderSourceReportStore. No list, watch,
// create or read runs here.
func providerSourceReports(sel *providerSourceSelection, writer client.Client, live client.Reader) (*apiserver.ProviderSourceReports, error) {
	if sel == nil {
		return nil, nil
	}
	if writer == nil || live == nil || sel.maxAge <= 0 || sel.maxAge > time.Duration(providerSourceMaxAgeSeconds)*time.Second {
		return nil, errProviderSourceConfig
	}
	store := &apiserver.ProviderSourceReportStore{
		Writer: writer, Live: live, Namespace: providerSourceSlotNamespace, Name: providerSourceSlotName, DataKey: apiserver.ProviderSourceDataKey,
	}
	store.Selection.PublisherRef = providerallowance.CurrentSourcePublisher
	store.Selection.ProducerIncarnation = providerSourceIncarnation
	store.Selection.SourceGeneration = providerSourceGeneration
	store.Selection.MaxAge = sel.maxAge
	return &apiserver.ProviderSourceReports{
		Enabled: true, Store: store, Live: live, SessionNamespace: providerSourceSessionNamespace,
		PublisherSessionUID: providerSourceSessionUID, PublisherServiceAccount: providerSourcePublisherSA,
		PortalNamespace: providerSourcePortalNamespace, PortalServiceAccount: providerSourcePortalSA,
	}, nil
}
