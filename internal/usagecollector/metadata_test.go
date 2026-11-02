package usagecollector

import (
	"strings"
	"testing"
)

func TestSupportedMetadataPositiveFieldsAndBinding(t *testing.T) {
	cursor := `{"cliVersion":"2026.10.22-27dcf5f","subscriptionTier":"Super","userEmail":"person@example.invalid","model":"not evidence"}`
	got, err := cursorMetadata([]byte(cursor))
	if err != nil || got.plan != "Super" || got.account == "" || got.workspace != "" {
		t.Fatal(err)
	}
	for _, raw := range []string{strings.Replace(cursor, `"Super"`, `null`, 1), strings.Replace(cursor, `"userEmail"`, `"UserEmail"`, 1), strings.Replace(cursor, `2026.10.22-27dcf5f`, `future-version`, 1), strings.Replace(cursor, `person@example.invalid`, ``, 1)} {
		if _, err = cursorMetadata([]byte(raw)); err == nil {
			t.Fatal("incomplete/foreign metadata accepted")
		}
	}
	claude := `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","subscriptionType":"max","email":"person@example.invalid","orgId":"fixture-org"}`
	m, err := claudeMetadata([]byte(claude))
	if err != nil || m.workspace == "" {
		t.Fatal(err)
	}
	for _, raw := range []string{strings.Replace(claude, `"max"`, `"api"`, 1), strings.Replace(claude, `"fixture-org"`, `null`, 1), strings.Replace(claude, `true`, `false`, 1)} {
		if _, err = claudeMetadata([]byte(raw)); err == nil {
			t.Fatal("unqualified auth metadata accepted")
		}
	}
}
