package usagecollector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

type accountMetadata struct{ account, workspace, plan string }

func privateHash(prefix, value string) (string, error) {
	if len(value) == 0 || len(value) > 512 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return "", ErrCapture
	}
	sum := sha256.Sum256([]byte(value))
	return prefix + hex.EncodeToString(sum[:]), nil
}

// Normal supported metadata commands may expose additional fields. Only these
// positive identity fields survive; provider messages and raw identities do not.
func cursorMetadata(raw []byte) (accountMetadata, error) {
	if !providerallowance.ValidJSONDocument(raw, 16<<10) {
		return accountMetadata{}, refused("metadata_not_json", ErrCapture)
	}
	if !providerallowance.CanonicalFieldNames(raw, "cliVersion", "subscriptionTier", "userEmail") {
		return accountMetadata{}, refused("metadata_unqualified", ErrCapture)
	}
	var value struct {
		Version string `json:"cliVersion"`
		Plan    string `json:"subscriptionTier"`
		Email   string `json:"userEmail"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return accountMetadata{}, refused("metadata_unqualified", ErrCapture)
	}
	if value.Version != providerallowance.CursorCLIQualifiedVersion {
		return accountMetadata{}, unqualifiedVersion(value.Version)
	}
	if !token.MatchString(strings.ToLower(value.Plan)) || !strings.Contains(value.Email, "@") {
		return accountMetadata{}, refused("metadata_unqualified", ErrCapture)
	}
	ref, err := privateHash("cursor-email-sha256:", value.Email)
	if err != nil {
		return accountMetadata{}, ErrCapture
	}
	return accountMetadata{account: ref, plan: value.Plan}, nil
}

func claudeMetadata(raw []byte) (accountMetadata, error) {
	if !providerallowance.ValidJSONDocument(raw, 16<<10) {
		return accountMetadata{}, refused("metadata_not_json", ErrCapture)
	}
	if !providerallowance.CanonicalFieldNames(raw, "loggedIn", "authMethod", "apiProvider", "subscriptionType", "email", "orgId") {
		return accountMetadata{}, refused("metadata_unqualified", ErrCapture)
	}
	var value struct {
		LoggedIn     *bool  `json:"loggedIn"`
		AuthMethod   string `json:"authMethod"`
		APIProvider  string `json:"apiProvider"`
		Subscription string `json:"subscriptionType"`
		Email        string `json:"email"`
		OrgID        string `json:"orgId"`
	}
	if json.Unmarshal(raw, &value) != nil || value.LoggedIn == nil {
		return accountMetadata{}, refused("metadata_unqualified", ErrCapture)
	}
	if !*value.LoggedIn {
		return accountMetadata{}, refused("not_logged_in", ErrCapture)
	}
	if value.AuthMethod != "claude.ai" || value.APIProvider != "firstParty" || value.Subscription != "max" || !strings.Contains(value.Email, "@") {
		return accountMetadata{}, refused("metadata_unqualified", ErrCapture)
	}
	ref, err := privateHash("anthropic-email-sha256:", value.Email)
	if err != nil {
		return accountMetadata{}, ErrCapture
	}
	workspace, err := privateHash("anthropic-org-sha256:", value.OrgID)
	if err != nil {
		return accountMetadata{}, ErrCapture
	}
	return accountMetadata{account: ref, workspace: workspace, plan: value.Subscription}, nil
}

func (m accountMetadata) matches(binding ProviderBinding) bool {
	workspace := ""
	if binding.WorkspaceRef != nil {
		workspace = *binding.WorkspaceRef
	}
	return m.account == binding.AccountRef && m.workspace == workspace
}
