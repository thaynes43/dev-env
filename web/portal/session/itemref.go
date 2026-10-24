package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// CanonicalItem is the existing HaynesKube item. Historical v2 item names are not selectors.
const (
	CanonicalVault = "HaynesKube"
	CanonicalItem  = "dev-env"
)

var errConsumerRejected = errors.New("portal consumer selection rejected")

// selectableProperties are individual remoteRef entries on HaynesKube/dev-env.
// The four runtime names are inert staged definitions: accepting them is not
// population, installation, SecretSynced, or an access grant.
// SSH_CA_* are unrelated keeper fields from DESIGN-001 D-72 and must not be projected.
var (
	selectableProperties = map[string]struct{}{
		"OIDC_CLIENT_ID":     {},
		"OIDC_CLIENT_SECRET": {},
		"DB_USERNAME":        {},
		"DB_PASSWORD":        {},
		"SESSION_AEAD_KEY":   {},
		"SESSION_GENERATION": {},
	}
	unrelatedProperties = map[string]struct{}{
		"SSH_CA_PRIVATE_KEY_B64": {},
		"SSH_CA_PUBLIC_KEY":      {},
	}
	historicalItems = map[string]struct{}{
		"dev-env-v2-portal":           {},
		"dev-env-v2-portal-runtime":   {},
		"dev-env-v2-portal-migration": {},
	}
)

type consumerDocument struct {
	Vault    string          `json:"vault"`
	Item     string          `json:"item"`
	Data     []consumerEntry `json:"data"`
	DataFrom json.RawMessage `json:"dataFrom,omitempty"`
}

type consumerEntry struct {
	SecretKey string    `json:"secretKey"`
	RemoteRef remoteRef `json:"remoteRef"`
}

type remoteRef struct {
	Key      string `json:"key"`
	Property string `json:"property"`
}

// ValidatePortalConsumer accepts individual remoteRef.property entries for the
// two OIDC fields and the four inert runtime projections on HaynesKube/dev-env.
// It rejects historical item keys, whole-item extract, unrelated properties, and renamed secret keys.
func ValidatePortalConsumer(document []byte) error {
	if len(document) == 0 || len(document) > 8192 {
		return errConsumerRejected
	}
	dec := json.NewDecoder(bytes.NewReader(document))
	dec.DisallowUnknownFields()
	var doc consumerDocument
	if err := dec.Decode(&doc); err != nil {
		return errConsumerRejected
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errConsumerRejected
	}
	if len(bytes.TrimSpace(doc.DataFrom)) > 0 && !bytes.Equal(bytes.TrimSpace(doc.DataFrom), []byte("null")) {
		return errConsumerRejected
	}
	if doc.Vault != CanonicalVault || doc.Item != CanonicalItem || len(doc.Data) == 0 || len(doc.Data) > len(selectableProperties) {
		return errConsumerRejected
	}
	seen := map[string]struct{}{}
	for _, entry := range doc.Data {
		if _, historical := historicalItems[entry.RemoteRef.Key]; historical || entry.RemoteRef.Key != CanonicalItem {
			return errConsumerRejected
		}
		property := entry.RemoteRef.Property
		if entry.SecretKey != property {
			return errConsumerRejected
		}
		if _, dup := seen[property]; dup {
			return errConsumerRejected
		}
		seen[property] = struct{}{}
		if _, unrelated := unrelatedProperties[property]; unrelated {
			return errConsumerRejected
		}
		if _, ok := selectableProperties[property]; !ok {
			return errConsumerRejected
		}
	}
	return nil
}
