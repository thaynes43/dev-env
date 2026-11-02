package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/apiserver"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

const (
	aggregateAllowanceSlot   = "provider-allowances"
	aggregateConfigMaxBytes  = 64 * 1024
	aggregatePortalNamespace = "dev-agents"
	aggregatePortalAccount   = "dev-env-v2-portal"
)

// errAggregateAllowanceConfig is the only refusal for this flag.
// It contains no submitted text.
var errAggregateAllowanceConfig = errors.New("aggregate allowance configuration was refused")

var aggregateConfigFields = []string{
	"slot", "namespace", "name",
	"publisherSessionUID", "publisherServiceAccount",
	"readerServiceAccount", "readerSessionUIDs",
	"portalNamespace", "portalServiceAccount",
	"maxAgeSeconds", "contexts", "registry",
}

// aggregateAllowanceSelection is an explicit, complete enable.
// Nil means the route stays off and no client is used.
type aggregateAllowanceSelection struct {
	publisherUID types.UID
	publisherSA  string
	readerSA     string
	readerUIDs   []types.UID
	trust        providerallowance.AggregateTrust
}

// parseAggregateAllowanceConfig accepts only an explicit enable plus the exact
// selection document. An empty flag is off. A document without the enable,
// an enable without a document, and any malformed or partial document refuse.
// Maximum age does not turn the route on by itself.
func parseAggregateAllowanceConfig(enabled bool, raw, apiAddr, sessionNamespace string) (*aggregateAllowanceSelection, error) {
	if !enabled {
		if raw != "" {
			return nil, errAggregateAllowanceConfig
		}
		return nil, nil
	}
	sel, err := parseAggregateAllowanceDocument(raw, sessionNamespace)
	if err != nil || apiAddr == "0" || sessionNamespace == "" {
		return nil, errAggregateAllowanceConfig
	}
	return sel, nil
}

func parseAggregateAllowanceDocument(raw, sessionNamespace string) (*aggregateAllowanceSelection, error) {
	if raw == "" || len(raw) > aggregateConfigMaxBytes || !utf8.ValidString(raw) || bytes.HasPrefix([]byte(raw), []byte{0xEF, 0xBB, 0xBF}) {
		return nil, errAggregateAllowanceConfig
	}
	if !providerallowance.ExactObjectKeys([]byte(raw), aggregateConfigFields) {
		return nil, errAggregateAllowanceConfig
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var doc struct {
		Slot                    string          `json:"slot"`
		Namespace               string          `json:"namespace"`
		Name                    string          `json:"name"`
		PublisherSessionUID     string          `json:"publisherSessionUID"`
		PublisherServiceAccount string          `json:"publisherServiceAccount"`
		ReaderServiceAccount    string          `json:"readerServiceAccount"`
		ReaderSessionUIDs       []string        `json:"readerSessionUIDs"`
		PortalNamespace         string          `json:"portalNamespace"`
		PortalServiceAccount    string          `json:"portalServiceAccount"`
		MaxAgeSeconds           int64           `json:"maxAgeSeconds"`
		Contexts                json.RawMessage `json:"contexts"`
		Registry                json.RawMessage `json:"registry"`
	}
	if err := dec.Decode(&doc); err != nil {
		return nil, errAggregateAllowanceConfig
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, errAggregateAllowanceConfig
	}
	if doc.Slot != aggregateAllowanceSlot || doc.Namespace != apiserver.AggregateAllowanceNamespace || doc.Name != apiserver.AggregateAllowanceName {
		return nil, errAggregateAllowanceConfig
	}
	if doc.PortalNamespace != aggregatePortalNamespace || doc.PortalServiceAccount != aggregatePortalAccount {
		return nil, errAggregateAllowanceConfig
	}
	if sessionNamespace != aggregatePortalNamespace && sessionNamespace != doc.PortalNamespace {
		return nil, errAggregateAllowanceConfig
	}
	if len(validation.IsDNS1123Label(doc.PublisherServiceAccount)) != 0 || len(validation.IsDNS1123Label(doc.ReaderServiceAccount)) != 0 || !aggregateUID(doc.PublisherSessionUID) {
		return nil, errAggregateAllowanceConfig
	}
	if len(doc.ReaderSessionUIDs) == 0 || len(doc.ReaderSessionUIDs) > 8 {
		return nil, errAggregateAllowanceConfig
	}
	readers := make([]types.UID, 0, len(doc.ReaderSessionUIDs))
	seen := map[string]struct{}{}
	for _, uid := range doc.ReaderSessionUIDs {
		if !aggregateUID(uid) {
			return nil, errAggregateAllowanceConfig
		}
		if _, dup := seen[uid]; dup {
			return nil, errAggregateAllowanceConfig
		}
		seen[uid] = struct{}{}
		readers = append(readers, types.UID(uid))
	}
	trustRaw, err := json.Marshal(struct {
		MaxAgeSeconds int64           `json:"maxAgeSeconds"`
		Contexts      json.RawMessage `json:"contexts"`
		Registry      json.RawMessage `json:"registry"`
	}{MaxAgeSeconds: doc.MaxAgeSeconds, Contexts: doc.Contexts, Registry: doc.Registry})
	if err != nil {
		return nil, errAggregateAllowanceConfig
	}
	trust, err := providerallowance.ParseAggregateTrust(trustRaw)
	if err != nil || trust.MaxAge != doc.MaxAgeSeconds || trust.Registry == nil {
		return nil, errAggregateAllowanceConfig
	}
	return &aggregateAllowanceSelection{
		publisherUID: types.UID(doc.PublisherSessionUID),
		publisherSA:  doc.PublisherServiceAccount,
		readerSA:     doc.ReaderServiceAccount,
		readerUIDs:   readers,
		trust:        trust,
	}, nil
}

func aggregateUID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		switch {
		case value[i] >= 'a' && value[i] <= 'z':
		case value[i] >= 'A' && value[i] <= 'Z':
		case value[i] >= '0' && value[i] <= '9':
		case value[i] == '-' || value[i] == '.':
		default:
			return false
		}
	}
	return true
}

// aggregateAllowanceReports builds the slot handler. A nil selection returns
// nil and does not use either client. Construction does not read the ConfigMap.
func aggregateAllowanceReports(sel *aggregateAllowanceSelection, writer client.Client, live client.Reader) (*apiserver.AggregateAllowanceReports, error) {
	if sel == nil {
		return nil, nil
	}
	if writer == nil || live == nil || sel.trust.Registry == nil || sel.trust.MaxAge <= 0 {
		return nil, errAggregateAllowanceConfig
	}
	store := &apiserver.AggregateAllowanceReportStore{
		Writer: writer, Live: live,
		Namespace: apiserver.AggregateAllowanceNamespace,
		Name:      apiserver.AggregateAllowanceName,
		DataKey:   apiserver.AggregateAllowanceDataKey,
		Contexts:  sel.trust.Contexts,
		Registry:  sel.trust.Registry,
		MaxAge:    time.Duration(sel.trust.MaxAge) * time.Second,
	}
	return &apiserver.AggregateAllowanceReports{
		Enabled: true, Store: store, Live: live,
		SessionNamespace: aggregatePortalNamespace, SessionServiceAccount: sel.readerSA, SessionUIDs: append([]types.UID(nil), sel.readerUIDs...),
		PublisherSessionUID: sel.publisherUID, PublisherServiceAccount: sel.publisherSA,
		PortalNamespace: aggregatePortalNamespace, PortalServiceAccount: aggregatePortalAccount,
	}, nil
}
