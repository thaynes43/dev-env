// This file is never compiled. TestHistoryLayoutScanCatchesNewState parses it
// to prove that TestHistoryLayoutCoversAgentdState's scan finds every way
// below of writing a new name under agentd's state directory. Each name is
// unclassified, or cannot be resolved, and the scan must report it.
package agentdstate

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const constName = "unclassified-b.json"

func statePathLiteral(s Settings) { _ = os.WriteFile(s.statePath("unclassified-a.json"), nil, 0o600) }

func statePathConst(s Settings) { _ = os.WriteFile(s.statePath(constName), nil, 0o600) }

func joinStateDir(s Settings) {
	_ = os.MkdirAll(filepath.Join(s.StateDir, "unclassified-c", "x"), 0o700)
}

func joinStateDirParam(stateDir string) {
	_ = os.WriteFile(filepath.Join(stateDir, "unclassified-d"), nil, 0o600)
}

func createTemp(s Settings) { _, _ = os.CreateTemp(s.StateDir, "unclassified-e-*") }

func lockHelper(s Settings) { _, _ = workspaceFileLock(s.StateDir, "unclassified-f") }

func rangeList(s Settings) {
	for _, f := range []string{"unclassified-g.json"} {
		_ = os.WriteFile(s.statePath(f), nil, 0o600)
	}
}

func paramHelper(s Settings, name string) { _ = os.WriteFile(s.statePath(name), nil, 0o600) }

func paramPassed(s Settings) { paramHelper(s, "unclassified-h.json") }

func dynamicName(s Settings) { _ = os.WriteFile(s.statePath(someCall()+".json"), nil, 0o600) }

func rebasedReceiver(s Settings) {
	archive := s
	archive.StateDir = filepath.Join(s.StateDir, "unclassified-i")
	_ = os.WriteFile(archive.statePath("x.json"), nil, 0o600)
}

func aliasStateDir(s Settings) {
	dir := s.StateDir
	_ = os.WriteFile(filepath.Join(dir, "unclassified-j"), nil, 0o600)
}

func tempBeside(path string) {
	dir := filepath.Dir(path)
	_, _ = os.CreateTemp(dir, "."+filepath.Base(path)+".unclassified-k-*")
}

func tempForward(path string, data []byte) { tempBeside(path) }

func tempForwardCaller(s Settings) { tempForward(s.statePath("launch.json"), nil) }

func tempLiteral(path string) { _, _ = os.CreateTemp(filepath.Dir(path), ".unclassified-l-*") }

func tempLiteralCaller(s Settings) {
	receipt := s.statePath("boot.json")
	tempLiteral(receipt)
}
