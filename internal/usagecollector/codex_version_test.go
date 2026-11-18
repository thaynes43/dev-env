package usagecollector

import "testing"

func TestQualifiedCodexVersions(t *testing.T) {
	for _, version := range []string{"codex-cli 0.160.1", "codex-cli 0.169.1", "codex-cli 0.169.1\n", "codex-cli 0.169.0", "codex-cli 0.170.0", "0.169.1", "unknown"} {
		want := version == "codex-cli 0.160.1" || version == "codex-cli 0.169.1" || version == "codex-cli 0.169.1\n"
		if got := qualifiedCodexVersion(version); got != want {
			t.Fatalf("%q accepted=%v, want %v", version, got, want)
		}
	}
}
