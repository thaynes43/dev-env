package main

import "testing"

// DESIGN-059: the session allowance store is off unless the flag is given.
func TestSessionAllowancesFlag(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.sessionAllowances {
		t.Fatal("--enable-session-allowances is on by default")
	}
	o, err = parseFlags([]string{"--enable-session-allowances"})
	if err != nil || !o.sessionAllowances {
		t.Fatalf("the flag did not turn it on: %v", err)
	}
}
