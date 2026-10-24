package main

import "testing"

func TestPortalCommandVersionAndUsage(t *testing.T) {
	if run([]string{"version"}) != 0 || run([]string{"serve"}) != 2 || run([]string{"version", "extra"}) != 2 {
		t.Fatal("portal command usage changed")
	}
}
