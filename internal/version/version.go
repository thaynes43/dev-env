// Package version reports the build identity of every dev-env binary.
//
// The Makefile can set the release version and the commit at link time:
//
//	-X github.com/thaynes43/dev-env/internal/version.version=v2.0.0
//	-X github.com/thaynes43/dev-env/internal/version.commit=1a2b3c4d5e6f
//
// A `go build` from a git checkout needs neither: the Go toolchain stamps the
// module version and the VCS revision into the binary, and Get falls back to
// them. A build with neither reports "dev" and "unknown".
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Set by -ldflags -X at link time. Empty means "not set".
var (
	version = ""
	commit  = ""
)

// Info is the build identity of one binary.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

// Get returns the identity of the running binary.
func Get() Info {
	bi, _ := debug.ReadBuildInfo() // nil when the binary carries no build info
	return resolve(version, commit, bi)
}

// String renders the one-line form that every binary's `version` command prints,
// for example "agent-run v2.0.0 (commit 1a2b3c4d5e6f, go1.27.1, linux/amd64)".
func (i Info) String(binary string) string {
	return fmt.Sprintf("%s %s (commit %s, %s, %s)", binary, i.Version, i.Commit, i.GoVersion, i.Platform)
}

// shortRevision is the commit length the Makefile also uses.
const shortRevision = 12

// resolve prefers the link-time values and falls back to the build info that the
// Go toolchain embeds. bi may be nil.
func resolve(ldVersion, ldCommit string, bi *debug.BuildInfo) Info {
	info := Info{
		Version:   ldVersion,
		Commit:    ldCommit,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}

	if info.Version == "" && bi != nil && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		info.Version = bi.Main.Version
	}
	if info.Commit == "" && bi != nil {
		info.Commit = commitFromSettings(bi.Settings)
	}

	if info.Version == "" {
		info.Version = "dev"
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	return info
}

// commitFromSettings reads the VCS stamp: the revision, shortened, with "-dirty"
// when the working tree had uncommitted changes at build time.
func commitFromSettings(settings []debug.BuildSetting) string {
	var revision string
	var modified bool
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision == "" {
		return ""
	}
	if len(revision) > shortRevision {
		revision = revision[:shortRevision]
	}
	if modified {
		revision += "-dirty"
	}
	return revision
}
