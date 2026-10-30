package main

import "testing"

func TestAsksSweeperUsesDefaultOffAPIFlagAndRequiresLeader(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.asksEnabled {
		t.Fatal("asks and sweeper enabled by default", err)
	}
	if _, err := parseFlags([]string{"--enable-asks", "--leader-elect=false"}); err == nil {
		t.Fatal("sweeper allowed without leader election")
	}
	if _, err := parseFlags([]string{"--enable-asks"}); err != nil {
		t.Fatal("asks flag refused", err)
	}
}
