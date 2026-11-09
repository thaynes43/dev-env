package delegatedresult

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// These proposal fields are not claims that a production admission API exists.
// The admission adapter must use repository UID, task and catalog validators.
// Local shape checks below do not authenticate any of these identifiers.
type Key struct {
	Scope            string `json:"scope"`
	HostAdmissionUID string `json:"hostAdmissionUID"`
	SessionUID       string `json:"sessionUID"`
	PodUID           string `json:"podUID"`
	TaskUID          string `json:"taskUID"`
	InvocationUID    string `json:"invocationUID"`
	ResultKind       string `json:"resultKind"`
	SchemaVersion    int    `json:"schemaVersion"`
}

type Attribution struct {
	Provider          string  `json:"provider"`
	RequestedModel    string  `json:"requestedModel"`
	ServedModel       *string `json:"servedModel"`
	ServedModelStatus string  `json:"servedModelStatus"`
}

type Provenance struct {
	Tenant                 string      `json:"tenant"`
	Project                string      `json:"project"`
	Repository             string      `json:"repository"`
	CatalogIdentity        string      `json:"catalogIdentity"`
	ReaderPolicyVersion    string      `json:"readerPolicyVersion"`
	RetentionPolicyVersion string      `json:"retentionPolicyVersion"`
	AdmissionEpoch         uint64      `json:"admissionEpoch"`
	CancellationEpoch      uint64      `json:"cancellationEpoch"`
	Host                   Attribution `json:"host"`
	Delegate               Attribution `json:"delegate"`
}

// Attempt freezes values by copy. No setters or pointer/slice getters exist.
// Construction is NOT admission, privacy approval, execution or durable storage.
type Attempt struct {
	key        Key
	provenance Provenance
	answer     string
	digest     string
}

func NewAttempt(key Key, provenance Provenance, req Request) (Attempt, error) {
	if !validKey(key) || !validProvenance(provenance) || req.SchemaVersion != 1 || !validAnswer(req.Answer) {
		return Attempt{}, ErrInvalid
	}
	provenance.Host = copyAttribution(provenance.Host)
	provenance.Delegate = copyAttribution(provenance.Delegate)
	digest := sha256.Sum256([]byte("delegated-result-v1\x00" + req.Answer))
	return Attempt{key: key, provenance: provenance, answer: req.Answer, digest: hex.EncodeToString(digest[:])}, nil
}

func (a Attempt) SameImmutable(b Attempt) bool {
	return a.valid() && b.valid() && a.key == b.key && sameProvenance(a.provenance, b.provenance) && a.answer == b.answer && a.digest == b.digest
}

func (a Attempt) valid() bool {
	return validKey(a.key) && validProvenance(a.provenance) && validAnswer(a.answer) && len(a.digest) == 64
}

func copyAttribution(a Attribution) Attribution {
	if a.ServedModel != nil {
		value := *a.ServedModel
		a.ServedModel = &value
	}
	return a
}

func sameAttribution(a, b Attribution) bool {
	return a.Provider == b.Provider && a.RequestedModel == b.RequestedModel && a.ServedModelStatus == b.ServedModelStatus && ((a.ServedModel == nil && b.ServedModel == nil) || (a.ServedModel != nil && b.ServedModel != nil && *a.ServedModel == *b.ServedModel))
}

func sameProvenance(a, b Provenance) bool {
	ah, ad, bh, bd := a.Host, a.Delegate, b.Host, b.Delegate
	a.Host, a.Delegate, b.Host, b.Delegate = Attribution{}, Attribution{}, Attribution{}, Attribution{}
	return a == b && sameAttribution(ah, bh) && sameAttribution(ad, bd)
}

func validLabel(s string) bool {
	if s == "" || len(s) > 512 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return strings.TrimSpace(s) == s
}

// Matches the existing protocol taskOutcomeIdentifier / UID schema pattern.
// Kept local because that source validator is unexported; not a new grant.
var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func validKey(k Key) bool {
	if !validLabel(k.Scope) {
		return false
	}
	for _, s := range []string{k.HostAdmissionUID, k.SessionUID, k.PodUID, k.TaskUID, k.InvocationUID} {
		if !identifier.MatchString(s) {
			return false
		}
	}
	return k.SchemaVersion == 1 && k.ResultKind == "final"
}

func validAttribution(a Attribution) bool {
	if !validLabel(a.Provider) || !validLabel(a.RequestedModel) {
		return false
	}
	switch a.ServedModelStatus {
	case "Unknown":
		return a.ServedModel == nil
	case "Observed":
		return a.ServedModel != nil && validLabel(*a.ServedModel)
	default:
		return false
	}
}

func validProvenance(p Provenance) bool {
	for _, s := range []string{p.Tenant, p.Project, p.Repository, p.CatalogIdentity, p.ReaderPolicyVersion, p.RetentionPolicyVersion} {
		if !validLabel(s) {
			return false
		}
	}
	return p.AdmissionEpoch > 0 && validAttribution(p.Host) && validAttribution(p.Delegate)
}

// Receipt is a proposed acknowledgment from an independently authenticated store.
// A syntactically valid receipt does not prove that store exists or is durable.
type Receipt struct {
	SchemaVersion int        `json:"schemaVersion"`
	Key           Key        `json:"key"`
	Provenance    Provenance `json:"provenance"`
	RecordUID     string     `json:"recordUID"`
	CommitID      string     `json:"commitID"`
	Digest        string     `json:"digest"`
	ByteCount     int        `json:"byteCount"`
	CommittedAt   time.Time  `json:"committedAt"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	Outcome       string     `json:"outcome"`
}

// DecodeReceipt requires all fields, including explicit null servedModel when
// Unknown. Prior, when available, additionally binds immutable commit metadata.
// now must come from a trusted caller clock. Current authorization is separate.
func DecodeReceipt(r io.Reader, expected Attempt, prior *Receipt, now time.Time) (Receipt, error) {
	var out Receipt
	if r == nil || !expected.valid() || now.IsZero() {
		return out, ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxRawBytes+1))
	if err != nil || !strictJSON(data) {
		return out, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || !receiptFieldsPresent(data) || out.Validate(expected, prior, now) != nil {
		return Receipt{}, ErrInvalid
	}
	return out, nil
}

func (r Receipt) Validate(a Attempt, prior *Receipt, now time.Time) error {
	if !a.valid() || now.IsZero() || r.SchemaVersion != 1 || r.Key != a.key || !validProvenance(r.Provenance) || !sameProvenance(r.Provenance, a.provenance) || r.Digest != a.digest || r.ByteCount != len(a.answer) || !validLabel(r.RecordUID) || !validLabel(r.CommitID) || r.CommittedAt.IsZero() || r.CommittedAt.After(now) || !r.ExpiresAt.After(now) || !r.ExpiresAt.After(r.CommittedAt) || (r.Outcome != "Created" && r.Outcome != "Confirmed") {
		return ErrInvalid
	}
	if prior != nil {
		if prior.Validate(a, nil, now) != nil || prior.RecordUID != r.RecordUID || prior.CommitID != r.CommitID || !prior.CommittedAt.Equal(r.CommittedAt) || !prior.ExpiresAt.Equal(r.ExpiresAt) || r.Outcome != "Confirmed" {
			return ErrInvalid
		}
	}
	return nil
}

// Null integer fields would otherwise silently decode as zero in encoding/json.
func receiptFieldsPresent(data []byte) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return false
	}
	return requiredObject(obj, []string{"schemaVersion", "key", "provenance", "recordUID", "commitID", "digest", "byteCount", "committedAt", "expiresAt", "outcome"}, "") && nestedRequired(obj["key"], []string{"scope", "hostAdmissionUID", "sessionUID", "podUID", "taskUID", "invocationUID", "resultKind", "schemaVersion"}, false) && nestedRequired(obj["provenance"], []string{"tenant", "project", "repository", "catalogIdentity", "readerPolicyVersion", "retentionPolicyVersion", "admissionEpoch", "cancellationEpoch", "host", "delegate"}, true)
}

func requiredObject(obj map[string]json.RawMessage, names []string, nullable string) bool {
	if len(obj) != len(names) {
		return false
	}
	for _, name := range names {
		v, ok := obj[name]
		if !ok || (name != nullable && bytes.Equal(bytes.TrimSpace(v), []byte("null"))) {
			return false
		}
	}
	return true
}

func nestedRequired(data []byte, names []string, provenance bool) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil || !requiredObject(obj, names, "") {
		return false
	}
	if provenance {
		for _, name := range []string{"host", "delegate"} {
			var attr map[string]json.RawMessage
			if json.Unmarshal(obj[name], &attr) != nil || !requiredObject(attr, []string{"provider", "requestedModel", "servedModel", "servedModelStatus"}, "servedModel") {
				return false
			}
		}
	}
	return true
}
