package agentd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// A command or skill named rename in the session's project or user directories
// would swallow the typed /rename. The directories are <project>/.claude/commands,
// <project>/.claude/skills, <claude-config>/commands, <claude-config>/skills,
// and the same two directories under each enabled plugin. Settings files are
// <claude-config>/settings.json, <project>/.claude/settings.json and
// <project>/.claude/settings.local.json. Enabled plugins are read from
// <claude-config>/plugins/installed_plugins.json.
const renameShadowLimit = 256 << 10

// commandShadowed reports whether a rename command or skill is installed, or
// the check cannot be made. Either one means the typed rename must not run.
func commandShadowed(project, claudeConfig string) bool {
	if project == "" || claudeConfig == "" {
		return true
	}
	dirs := []string{
		filepath.Join(project, ".claude", "commands"),
		filepath.Join(project, ".claude", "skills"),
		filepath.Join(claudeConfig, "commands"),
		filepath.Join(claudeConfig, "skills"),
	}
	for _, dir := range dirs {
		if hit, bad := renameNamed(dir); hit || bad {
			return true
		}
	}
	var enabled, paths []string
	for _, path := range []string{
		filepath.Join(claudeConfig, "settings.json"),
		filepath.Join(project, ".claude", "settings.json"),
		filepath.Join(project, ".claude", "settings.local.json"),
	} {
		doc, missing, bad := readRenameSettings(path)
		if missing {
			continue
		}
		if bad {
			return true
		}
		enabled = append(enabled, doc.enabled...)
		paths = append(paths, doc.paths...)
	}
	for _, path := range paths {
		if path == "" || shadowedTree(path) {
			return true
		}
	}
	if len(enabled) == 0 {
		return false
	}
	installs, missing, bad := readPluginInstalls(filepath.Join(claudeConfig, "plugins", "installed_plugins.json"))
	if missing || bad {
		return true
	}
	for _, name := range enabled {
		path, ok := installs[name]
		if !ok || path == "" || shadowedTree(path) {
			return true
		}
	}
	return false
}

func shadowedTree(root string) bool {
	for _, dir := range []string{filepath.Join(root, "commands"), filepath.Join(root, "skills")} {
		if hit, bad := renameNamed(dir); hit || bad {
			return true
		}
	}
	return false
}

func renameNamed(dir string) (bool, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, false
		}
		return false, true
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "rename", "rename.md", "rename.markdown":
			return true, false
		}
	}
	return false, false
}

type renameSettings struct {
	enabled []string
	paths   []string
}

func readRenameSettings(path string) (renameSettings, bool, bool) {
	data, missing, bad := readShadowFile(path)
	if missing || bad {
		return renameSettings{}, missing, bad
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return renameSettings{}, false, true
	}
	var out renameSettings
	if body, ok := raw["enabledPlugins"]; ok {
		var plugins map[string]json.RawMessage
		if json.Unmarshal(body, &plugins) != nil {
			return renameSettings{}, false, true
		}
		for name, value := range plugins {
			if strings.TrimSpace(string(value)) == "true" {
				out.enabled = append(out.enabled, name)
			}
		}
	}
	if body, ok := raw["plugins"]; ok {
		var plugins []map[string]json.RawMessage
		if json.Unmarshal(body, &plugins) != nil {
			return renameSettings{}, false, true
		}
		for _, item := range plugins {
			path := settingsString(item, "installPath")
			if path == "" {
				path = settingsString(item, "path")
			}
			if path == "" {
				return renameSettings{}, false, true
			}
			out.paths = append(out.paths, path)
		}
	}
	return out, false, false
}

func readPluginInstalls(path string) (map[string]string, bool, bool) {
	data, missing, bad := readShadowFile(path)
	if missing || bad {
		return nil, missing, bad
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil {
		return nil, false, true
	}
	body, ok := root["plugins"]
	if !ok {
		return nil, false, true
	}
	out := map[string]string{}
	var asMap map[string]json.RawMessage
	if json.Unmarshal(body, &asMap) == nil {
		for name, entry := range asMap {
			var listed []map[string]json.RawMessage
			if json.Unmarshal(entry, &listed) != nil || len(listed) == 0 {
				return nil, false, true
			}
			install := settingsString(listed[0], "installPath")
			if install == "" {
				return nil, false, true
			}
			out[name] = install
		}
		return out, false, false
	}
	var asList []map[string]json.RawMessage
	if json.Unmarshal(body, &asList) != nil {
		return nil, false, true
	}
	for _, item := range asList {
		name := settingsString(item, "name")
		install := settingsString(item, "installPath")
		if name == "" || install == "" {
			return nil, false, true
		}
		out[name] = install
	}
	return out, false, false
}

func settingsString(item map[string]json.RawMessage, key string) string {
	raw, ok := item[key]
	if !ok {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	return text
}

func readShadowFile(path string) ([]byte, bool, bool) {
	data, err := readBounded(path, renameShadowLimit)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, true, false
		}
		return nil, false, true
	}
	return data, false, false
}
