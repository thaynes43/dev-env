package main

import (
	"strings"
	"testing"
)

func TestSessionRaiseFlagsDefaultOff(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.sessionRaiseDispatch || strings.TrimSpace(o.sessionRaiseProfiles) != "" {
		t.Fatalf("raise flags are on by default: %+v", o.sessionRaiseDispatch)
	}
	o, err = parseFlags([]string{"--enable-session-raise-dispatch"})
	if err != nil || !o.sessionRaiseDispatch {
		t.Fatalf("dispatch flag: %v %+v", err, o.sessionRaiseDispatch)
	}
}

func TestProfileFlagRefusesStartupWithoutSliceB(t *testing.T) {
	_, err := parseFlags([]string{"--session-raise-profiles", "workbench"})
	if err == nil || err.Error() != "Profile raises need the pod change of DESIGN-066 slice (b)." {
		t.Fatalf("got %v", err)
	}
}
