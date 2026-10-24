package session

import (
	"errors"
	"strings"
	"testing"
)

func TestPortalConsumerSelectsOnlyCanonicalOIDCProperties(t *testing.T) {
	valid := []byte(`{"vault":"HaynesKube","item":"dev-env","data":[{"secretKey":"OIDC_CLIENT_ID","remoteRef":{"key":"dev-env","property":"OIDC_CLIENT_ID"}},{"secretKey":"OIDC_CLIENT_SECRET","remoteRef":{"key":"dev-env","property":"OIDC_CLIENT_SECRET"}}]}`)
	if err := ValidatePortalConsumer(valid); err != nil {
		t.Fatal("canonical OIDC selection rejected")
	}
	staged := []byte(`{"vault":"HaynesKube","item":"dev-env","data":[{"secretKey":"DB_USERNAME","remoteRef":{"key":"dev-env","property":"DB_USERNAME"}},{"secretKey":"DB_PASSWORD","remoteRef":{"key":"dev-env","property":"DB_PASSWORD"}},{"secretKey":"SESSION_AEAD_KEY","remoteRef":{"key":"dev-env","property":"SESSION_AEAD_KEY"}},{"secretKey":"SESSION_GENERATION","remoteRef":{"key":"dev-env","property":"SESSION_GENERATION"}}]}`)
	if err := ValidatePortalConsumer(staged); err != nil {
		t.Fatal("inert runtime projection rejected")
	}
	cases := []string{
		strings.ReplaceAll(string(valid), `"item":"dev-env"`, `"item":"dev-env-v2-portal"`),
		strings.ReplaceAll(string(valid), `"key":"dev-env"`, `"key":"dev-env-v2-portal-runtime"`),
		strings.ReplaceAll(string(valid), `"key":"dev-env"`, `"key":"dev-env-v2-portal-migration"`),
		strings.TrimSuffix(string(valid), "}") + `,"dataFrom":[{"extract":{"key":"dev-env"}}]}`,
		strings.Replace(string(valid), `"property":"OIDC_CLIENT_ID"`, `"property":"SSH_CA_PRIVATE_KEY_B64"`, 1),
		strings.Replace(string(valid), `"secretKey":"OIDC_CLIENT_ID"`, `"secretKey":"SSH_CA_PRIVATE_KEY_B64"`, 1),
		strings.Replace(string(valid), `"property":"OIDC_CLIENT_SECRET"`, `"property":"SSH_CA_PUBLIC_KEY"`, 1),
		strings.Replace(string(valid), `"property":"OIDC_CLIENT_ID"`, `"property":"unrelated-field"`, 1),
		strings.Replace(string(valid), `"secretKey":"OIDC_CLIENT_ID"`, `"secretKey":"client-id"`, 1),
		strings.Replace(string(valid), `"property":"OIDC_CLIENT_ID"`, `"property":"OIDC_CLIENT_ID","decodingStrategy":"Base64"`, 1),
		`{"vault":"HaynesKube","item":"dev-env","data":[{"secretKey":"SSH_CA_PRIVATE_KEY_B64","remoteRef":{"key":"dev-env","property":"SSH_CA_PRIVATE_KEY_B64"}}]}`,
		`{"vault":"HaynesKube","item":"dev-env","data":[{"secretKey":"SSH_CA_PUBLIC_KEY","remoteRef":{"key":"dev-env","property":"SSH_CA_PUBLIC_KEY"}}]}`,
		`{"vault":"HaynesKube","item":"dev-env","data":[{"secretKey":"PORTAL_SUBJECT","remoteRef":{"key":"dev-env","property":"PORTAL_SUBJECT"}}]}`,
	}
	for _, document := range cases {
		if !errors.Is(ValidatePortalConsumer([]byte(document)), errConsumerRejected) {
			t.Fatalf("historical or broad selection accepted: %s", document)
		}
	}
}
