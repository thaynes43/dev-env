package main

import (
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver"
	"github.com/thaynes43/dev-env/internal/shelf"
)

var _ apiserver.CoordinationArchiveReader = (*shelf.CoordinationArchive)(nil)

func TestCoordinationArchiveReadFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
		err  bool
	}{
		{name: "default"},
		{name: "explicit false", args: []string{"--enable-coordination-archive-reads=false"}},
		{name: "true", args: []string{"--enable-coordination-archive-reads=true"}, want: true},
		{name: "invalid boolean", args: []string{"--enable-coordination-archive-reads=maybe"}, err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := parseFlags(tc.args)
			if (err != nil) != tc.err {
				t.Fatalf("parseFlags(%q) error = %v, want error=%t", tc.args, err, tc.err)
			}
			if err == nil && o.coordinationArchiveReads != tc.want {
				t.Fatalf("coordinationArchiveReads = %t, want %t", o.coordinationArchiveReads, tc.want)
			}
		})
	}
}
