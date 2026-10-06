package agentd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// The keys agentd seeds in the CLI's state file (.claude.json). A cold home
// otherwise stops the TUI on the theme, security-notes and folder-trust prompts
// (S-1). Checked on CLI 2.1.292 (2026-10-06): hasCompletedOnboarding and a
// project's hasTrustDialogAccepted take a cold TUI straight to its prompt.
const (
	keyOnboarded    = "hasCompletedOnboarding"
	keyProjects     = "projects"
	keyTrusted      = "hasTrustDialogAccepted"
	keyProjOnboard  = "hasCompletedProjectOnboarding"
	keyOAuthAccount = "oauthAccount"
)

// oauthAccount is the seed for .claude.json's oauthAccount: the account and
// organization uuids only (R-02 P-3). Remote Control's eligibility check reads
// it before the CLI's own profile fetch can land (S-6), so remote sessions
// need it. Plan 03 has the keeper put it in a Secret; agentd reads the file.
type oauthAccount struct {
	AccountUUID      string `json:"accountUuid"`
	OrganizationUUID string `json:"organizationUuid"`
}

// seedClaudeState merges the onboarding flag, folder trust for each of dirs
// and, when accountFile exists, oauthAccount into the state file. It keeps every
// other key the CLI wrote, and it never copies machineID,
// replBridgePlaceholders or session records between pods: it only ever adds
// the keys above. An unparseable file is left as it is.
func seedClaudeState(statePath, accountFile string, dirs []string) ([]string, error) {
	top := map[string]json.RawMessage{}
	data, err := os.ReadFile(statePath)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &top); err != nil {
			return nil, fmt.Errorf("%s is not a JSON object, left as it is: %w", statePath, err)
		}
	case isNotExist(err):
	default:
		return nil, err
	}

	var notes []string
	top[keyOnboarded] = json.RawMessage("true")

	projects := map[string]map[string]json.RawMessage{}
	if raw, ok := top[keyProjects]; ok {
		if err := json.Unmarshal(raw, &projects); err != nil {
			return nil, fmt.Errorf("%s: projects is not an object, left as it is: %w", statePath, err)
		}
	}
	for _, d := range dirs {
		p := projects[d]
		if p == nil {
			p = map[string]json.RawMessage{}
		}
		p[keyTrusted] = json.RawMessage("true")
		p[keyProjOnboard] = json.RawMessage("true")
		projects[d] = p
	}
	raw, err := json.Marshal(projects)
	if err != nil {
		return nil, err
	}
	top[keyProjects] = raw
	notes = append(notes, fmt.Sprintf("onboarding done, %d folders trusted", len(dirs)))

	if accountFile != "" {
		acct, err := readOAuthAccount(accountFile)
		switch {
		case err == nil:
			merged, err := mergeOAuthAccount(top[keyOAuthAccount], acct)
			if err != nil {
				return nil, err
			}
			top[keyOAuthAccount] = merged
			notes = append(notes, "oauthAccount seeded (account and organization ids)")
		case isNotExist(err):
			notes = append(notes, "no oauthAccount file yet; Remote Control needs it (plan 03)")
		default:
			notes = append(notes, fmt.Sprintf("WARN oauthAccount not seeded: %v", err))
		}
	}

	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return nil, err
	}
	// 0600: the CLI writes the account's email and profile here.
	if err := writeFileAtomic(statePath, append(out, '\n'), 0o600); err != nil {
		return nil, err
	}
	return notes, nil
}

func readOAuthAccount(path string) (oauthAccount, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return oauthAccount{}, err
	}
	var a oauthAccount
	if err := json.Unmarshal(data, &a); err != nil {
		return oauthAccount{}, fmt.Errorf("%s: %w", path, err)
	}
	if a.AccountUUID == "" || a.OrganizationUUID == "" {
		return oauthAccount{}, fmt.Errorf("%s: accountUuid and organizationUuid are both required", path)
	}
	return a, nil
}

// mergeOAuthAccount sets the two uuids in an existing oauthAccount object and
// keeps the profile fields the CLI fetched itself.
func mergeOAuthAccount(cur json.RawMessage, a oauthAccount) (json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	if len(cur) > 0 && string(cur) != "null" {
		if err := json.Unmarshal(cur, &obj); err != nil {
			return nil, fmt.Errorf("oauthAccount is not an object: %w", err)
		}
	}
	acc, _ := json.Marshal(a.AccountUUID)
	org, _ := json.Marshal(a.OrganizationUUID)
	obj["accountUuid"] = acc
	obj["organizationUuid"] = org
	return json.Marshal(obj)
}

// assertDefaultModel sets settings.json's "model": the default for every bare
// `claude` in the pod. An in-session `/model` rewrites that key, so v1
// re-asserts it on every boot (2026-08-29: the default silently became Opus
// 4.8). Every other key stays.
func assertDefaultModel(settingsPath, model string) (string, error) {
	top := map[string]json.RawMessage{}
	data, err := os.ReadFile(settingsPath)
	switch {
	case err == nil && len(strings.TrimSpace(string(data))) > 0:
		if err := json.Unmarshal(data, &top); err != nil {
			return "", fmt.Errorf("%s is not a JSON object, left as it is: %w", settingsPath, err)
		}
	case err == nil, isNotExist(err):
	default:
		return "", err
	}
	want, _ := json.Marshal(model)
	if cur, ok := top["model"]; ok && string(cur) == string(want) {
		return "claude default model already " + model, nil
	}
	top["model"] = want
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(settingsPath, append(out, '\n'), 0o644); err != nil {
		return "", err
	}
	return "claude default model set to " + model, nil
}
