package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"

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

// errSupplementalAllowanceReader is the only refusal for the public binding.
// It contains no submitted text.
var errSupplementalAllowanceReader = errors.New("supplemental allowance reader was refused")

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
	supplemental *apiserver.SupplementalAllowanceReader
}

var supplementalReaderFields = []string{
	"namespace", "name", "sessionUID", "serviceAccount", "serviceAccountUID",
}

// supplementalReaderFlag retains the first public binding without failing Set.
// A flag.Value error makes the flag parser write the raw argument.
type supplementalReaderFlag struct {
	raw   string
	count int
}

func (f *supplementalReaderFlag) String() string { return "" }

func (f *supplementalReaderFlag) Set(raw string) error {
	f.count++
	if f.count == 1 {
		f.raw = raw
		return nil
	}
	f.raw = ""
	return nil
}

// parseAggregateAllowanceSelection requires one complete private selection,
// inline or file, before accepting the separate public binding. A repeated,
// malformed, overlapping, or ninth binding refuses without reading a Session,
// Pod, or ServiceAccount. The refusal does not contain the submitted text.
func parseAggregateAllowanceSelection(enabled bool, inline, path, apiAddr, sessionNamespace string, supplemental *supplementalReaderFlag) (*aggregateAllowanceSelection, error) {
	if supplemental != nil && supplemental.count > 1 {
		return nil, errSupplementalAllowanceReader
	}
	var binding *apiserver.SupplementalAllowanceReader
	if supplemental != nil && supplemental.count == 1 {
		var err error
		binding, err = parseSupplementalAllowanceReader(supplemental.raw, sessionNamespace)
		if err != nil {
			return nil, err
		}
	}
	sel, err := parseAggregateAllowanceInput(enabled, inline, path, apiAddr, sessionNamespace)
	if err != nil {
		return nil, err
	}
	if binding == nil {
		return sel, nil
	}
	if sel == nil || !supplementalFits(sel, binding) {
		return nil, errSupplementalAllowanceReader
	}
	sel.supplemental = binding
	return sel, nil
}

func parseSupplementalAllowanceReader(raw, sessionNamespace string) (*apiserver.SupplementalAllowanceReader, error) {
	if raw == "" || len(raw) > aggregateConfigMaxBytes || !utf8.ValidString(raw) || bytes.HasPrefix([]byte(raw), []byte{0xEF, 0xBB, 0xBF}) {
		return nil, errSupplementalAllowanceReader
	}
	if !providerallowance.ExactObjectKeys([]byte(raw), supplementalReaderFields) {
		return nil, errSupplementalAllowanceReader
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var doc struct {
		Namespace         string `json:"namespace"`
		Name              string `json:"name"`
		SessionUID        string `json:"sessionUID"`
		ServiceAccount    string `json:"serviceAccount"`
		ServiceAccountUID string `json:"serviceAccountUID"`
	}
	if err := dec.Decode(&doc); err != nil {
		return nil, errSupplementalAllowanceReader
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, errSupplementalAllowanceReader
	}
	if doc.Namespace != sessionNamespace || doc.Namespace != aggregatePortalNamespace {
		return nil, errSupplementalAllowanceReader
	}
	if len(validation.IsDNS1123Subdomain(doc.Name)) != 0 || len(validation.IsDNS1123Subdomain(doc.ServiceAccount)) != 0 {
		return nil, errSupplementalAllowanceReader
	}
	if !aggregateUID(doc.SessionUID) || !aggregateUID(doc.ServiceAccountUID) {
		return nil, errSupplementalAllowanceReader
	}
	return &apiserver.SupplementalAllowanceReader{
		Namespace: doc.Namespace, Name: doc.Name, SessionUID: types.UID(doc.SessionUID),
		ServiceAccount: doc.ServiceAccount, ServiceAccountUID: doc.ServiceAccountUID,
	}, nil
}

func supplementalFits(sel *aggregateAllowanceSelection, binding *apiserver.SupplementalAllowanceReader) bool {
	if sel == nil || binding == nil || len(sel.readerUIDs) >= 8 {
		return false
	}
	for _, uid := range sel.readerUIDs {
		if uid == binding.SessionUID {
			return false
		}
	}
	return true
}

// parseAggregateAllowanceInput selects inline or private file configuration.
// Disabled/conflicting inputs refuse before opening a file. The final file may
// use a normal Kubernetes projected-volume symlink, but must be a bounded
// regular file without world access. Only the existing selected config parser
// grants route eligibility; supplying a file never creates access or a mount.
func parseAggregateAllowanceInput(enabled bool, inline, path, apiAddr, sessionNamespace string) (*aggregateAllowanceSelection, error) {
	if path == "" {
		return parseAggregateAllowanceConfig(enabled, inline, apiAddr, sessionNamespace)
	}
	if !enabled || inline != "" || !filepath.IsAbs(path) || apiAddr == "0" {
		return nil, errAggregateAllowanceConfig
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errAggregateAllowanceConfig
	}
	file := os.NewFile(uintptr(fd), "aggregate-private-config")
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0007 != 0 || info.Size() > aggregateConfigMaxBytes {
		return nil, errAggregateAllowanceConfig
	}
	raw, err := io.ReadAll(io.LimitReader(file, aggregateConfigMaxBytes+1))
	if err != nil || len(raw) > aggregateConfigMaxBytes {
		return nil, errAggregateAllowanceConfig
	}
	return parseAggregateAllowanceConfig(true, string(raw), apiAddr, sessionNamespace)
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
		Supplemental: sel.supplemental,
	}, nil
}
