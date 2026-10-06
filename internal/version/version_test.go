package version

import (
	"runtime"
	"runtime/debug"
	"testing"
)

func TestResolve(t *testing.T) {
	stamped := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.0.0-20261006173400-0123456789ab"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
			{Key: "vcs.modified", Value: "false"},
		},
	}
	dirty := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "fedcba9876543210fedcba9876543210fedcba98"},
			{Key: "vcs.modified", Value: "true"},
		},
	}

	tests := []struct {
		name        string
		ldVersion   string
		ldCommit    string
		bi          *debug.BuildInfo
		wantVersion string
		wantCommit  string
	}{
		{
			name:      "link-time values win over the build info",
			ldVersion: "v2.0.0", ldCommit: "1a2b3c4d5e6f", bi: stamped,
			wantVersion: "v2.0.0", wantCommit: "1a2b3c4d5e6f",
		},
		{
			name:        "falls back to the toolchain's VCS stamp",
			bi:          stamped,
			wantVersion: "v0.0.0-20261006173400-0123456789ab", wantCommit: "0123456789ab",
		},
		{
			name:        "a (devel) build with local changes",
			bi:          dirty,
			wantVersion: "dev", wantCommit: "fedcba987654-dirty",
		},
		{
			name:        "no build info at all",
			wantVersion: "dev", wantCommit: "unknown",
		},
		{
			name:      "link-time version, commit from the stamp",
			ldVersion: "v2.0.1", bi: dirty,
			wantVersion: "v2.0.1", wantCommit: "fedcba987654-dirty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolve(tt.ldVersion, tt.ldCommit, tt.bi)
			if got.Version != tt.wantVersion {
				t.Errorf("Version = %q, want %q", got.Version, tt.wantVersion)
			}
			if got.Commit != tt.wantCommit {
				t.Errorf("Commit = %q, want %q", got.Commit, tt.wantCommit)
			}
			if got.GoVersion != runtime.Version() {
				t.Errorf("GoVersion = %q, want %q", got.GoVersion, runtime.Version())
			}
			if want := runtime.GOOS + "/" + runtime.GOARCH; got.Platform != want {
				t.Errorf("Platform = %q, want %q", got.Platform, want)
			}
		})
	}
}

func TestInfoString(t *testing.T) {
	info := Info{Version: "v2.0.0", Commit: "1a2b3c4d5e6f", GoVersion: "go1.27.1", Platform: "linux/amd64"}
	want := "agent-run v2.0.0 (commit 1a2b3c4d5e6f, go1.27.1, linux/amd64)"
	if got := info.String("agent-run"); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
