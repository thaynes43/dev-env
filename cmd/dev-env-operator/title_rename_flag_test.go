package main

import (
	"strings"
	"testing"
)

func TestTitleRenameFlagRefusesMalformed(t *testing.T) {
	for _, raw := range []string{"a,,b", "a, b", "dev-env-1019-120000,*", "Bad_Name", "* ,dev-env-1019-120000"} {
		_, err := parseFlags([]string{"--title-rename-sessions=" + raw})
		if err == nil || !strings.Contains(err.Error(), "title-rename-sessions") {
			t.Fatalf("%q: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "*", "dev-env-1019-120000", "dev-env-1019-120000,dev-env-1019-130000"} {
		args := []string{}
		if raw != "" {
			args = []string{"--title-rename-sessions=" + raw}
		}
		_, err := parseFlags(args)
		if err != nil && strings.Contains(err.Error(), "title-rename-sessions") {
			t.Fatalf("%q: %v", raw, err)
		}
	}
}
