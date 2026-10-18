package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/pelletier/go-toml/v2"

	"github.com/thaynes43/dev-env/internal/commonrules"
)

const OwnedHomeInputLimit = commonrules.MaxSnapshotBytes + (128 << 10)

// OwnedHomeInput contains only reviewed nonsecret startup inputs. Provider
// history and access credentials are never preparation inputs or rewritten.
type OwnedHomeInput struct {
	Version     int             `json:"version"`
	CommonRules json.RawMessage `json:"commonRules"`
	CodexConfig string          `json:"codexConfig"`
}

// PrepareOwnedHome runs before the one-use intent in the original PID1. The
// policy digest covers both JSON and the separately mounted admin floor.
func PrepareOwnedHome(s Settings, data map[string]string, expected string) error {
	return ownedHome(s, data, expected, true)
}

// CheckOwnedHome validates frozen startup inputs without creating or repairing files.
func CheckOwnedHome(s Settings, data map[string]string, expected string) error {
	return ownedHome(s, data, expected, false)
}
func ownedHome(s Settings, data map[string]string, expected string, prepare bool) error {
	if len(data) != 2 || len(data["input.json"]) > OwnedHomeInputLimit || len(data["requirements.toml"]) > 64<<10 {
		return commonrules.ErrRefused
	}
	raw, err := json.Marshal(data)
	if err != nil || !commonrules.ValidDigest(expected) || commonrules.Digest(raw) != expected {
		return commonrules.ErrRefused
	}
	var input OwnedHomeInput
	dec := json.NewDecoder(bytes.NewBufferString(data["input.json"]))
	dec.DisallowUnknownFields()
	if dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF || input.Version != 1 || len(input.CodexConfig) == 0 || len(input.CodexConfig) > 64<<10 || len(data["requirements.toml"]) == 0 {
		return commonrules.ErrRefused
	}
	snapshot, err := commonrules.Parse(input.CommonRules)
	if err != nil {
		return err
	}
	var cfg, requirements map[string]any
	if toml.Unmarshal([]byte(input.CodexConfig), &cfg) != nil || toml.Unmarshal([]byte(data["requirements.toml"]), &requirements) != nil {
		return commonrules.ErrRefused
	}
	_, fallbacks, docMax, err := codexConfiguredInputsBytes([]byte(input.CodexConfig))
	if err != nil || docMax < 131072 || !slices.Contains(fallbacks, "CLAUDE.md") {
		return commonrules.ErrRefused
	}
	if s.CodexHome != filepath.Join(s.Home, ".codex") || s.StateDir != filepath.Join(s.Home, ".agentd") {
		return commonrules.ErrRefused
	}
	for _, dir := range []string{s.Home, s.CodexHome, s.StateDir, filepath.Join(s.CodexHome, "app-server-daemon")} {
		if prepare {
			if err = privateCommonDir(dir); err != nil {
				return err
			}
		} else {
			st, e := os.Lstat(dir)
			if e != nil || noSymlinkComponents(dir) != nil || !st.IsDir() || st.Mode().Perm() != 0700 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
				return commonrules.ErrRefused
			}
		}
	}
	files := map[string][]byte{
		filepath.Join(s.CodexHome, "config.toml"):                        []byte(input.CodexConfig),
		filepath.Join(s.CodexHome, "AGENTS.md"):                          []byte(snapshot.CodexGlobal()),
		filepath.Join(s.CodexHome, "app-server-daemon", "settings.json"): []byte("{\"updater\":{\"autoUpdateEnabled\":false}}\n"),
	}
	// Validate ALL retained destinations before any write; never silently replace
	// an owner's edited config or recovery input with new defaults.
	for path, want := range files {
		got, readErr := readOwnedCommon(path, OwnedHomeInputLimit)
		if readErr != nil && (!prepare || !errors.Is(readErr, os.ErrNotExist)) {
			return readErr
		}
		if readErr == nil && !bytes.Equal(got, want) {
			return commonrules.ErrRefused
		}
	}
	if !prepare {
		return nil
	}
	for path, want := range files {
		if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			// O_EXCL/no-follow prevents a raced existing destination from being replaced.
			fd, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return e
			}
			_, e = fd.Write(want)
			if e == nil {
				e = fd.Sync()
			}
			closeErr := fd.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
			parent, e := os.Open(filepath.Dir(path))
			if e != nil {
				return e
			}
			e = parent.Sync()
			_ = parent.Close()
			if e != nil {
				return e
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}
