package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRescueNotificationOwnerFileDefaultOffAndBounded(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.rescueIncidentOwners != nil {
		t.Fatal("ownership enabled by default", err)
	}
	p := filepath.Join(t.TempDir(), "owners.json")
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"schema":"dev-env.rescue-incident-owners.v1","owners":[]}`)
	o, err = parseFlags([]string{"--rescue-incident-owners-file=" + p})
	if err != nil || o.rescueIncidentOwners == nil || len(o.rescueIncidentOwners.Owners) != 0 {
		t.Fatal("explicit empty configuration lost", err)
	}
	for _, value := range []string{`{"schema":"unexpected"}`, strings.Repeat(" ", (32<<10)+1)} {
		write(value)
		if _, err := parseFlags([]string{"--rescue-incident-owners-file=" + p}); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	if _, err := parseFlags([]string{"--rescue-incident-owners-file=" + filepath.Dir(p)}); err == nil {
		t.Fatal("non-regular configuration accepted")
	}
}
