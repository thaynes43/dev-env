package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// A command or skill named rename in the session's project or user directories
// would swallow the typed /rename. The directories are <project>/.claude/commands,
// <project>/.claude/skills, <claude-config>/commands, <claude-config>/skills,
// and the same two directories under each enabled plugin. Settings files are
// <claude-config>/settings.json, <project>/.claude/settings.json and
// <project>/.claude/settings.local.json. Enabled plugins are read from
// <claude-config>/plugins/installed_plugins.json.
const (
	renameShadowLimit = 256 << 10
	// shadowEntryCap is the most directory entries one scan reads. Past that
	// the check cannot be finished, so the rename holds.
	shadowEntryCap = 256
	// shadowCommandDepth is how far under a commands directory a rename file
	// is still seen. The commands directory's own entries are depth 1.
	shadowCommandDepth = 3
)

// commandShadowed reports whether a rename command or skill is installed, or
// the check cannot be made. Either one means the typed rename must not run.
// The scan stops when ctx ends, which holds the rename and lets the caller
// release the delivery lock. A link to a directory is followed. A FIFO, or a
// link to one, fails at open.
func commandShadowed(ctx context.Context, project, claudeConfig string) bool {
	if ctx.Err() != nil || project == "" || claudeConfig == "" {
		return true
	}
	n := 0
	if shadowedCommands(ctx, filepath.Join(project, ".claude", "commands"), &n) ||
		shadowedSkills(ctx, filepath.Join(project, ".claude", "skills"), &n) ||
		shadowedCommands(ctx, filepath.Join(claudeConfig, "commands"), &n) ||
		shadowedSkills(ctx, filepath.Join(claudeConfig, "skills"), &n) {
		return true
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
		if ctx.Err() != nil || path == "" || shadowedTree(ctx, path, &n) {
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
		if ctx.Err() != nil || !ok || path == "" || shadowedTree(ctx, path, &n) {
			return true
		}
	}
	return false
}

func shadowedTree(ctx context.Context, root string, n *int) bool {
	return shadowedCommands(ctx, filepath.Join(root, "commands"), n) || shadowedSkills(ctx, filepath.Join(root, "skills"), n)
}

func shadowedCommands(ctx context.Context, dir string, n *int) bool {
	hit, bad := walkCommands(ctx, dir, 1, n)
	return hit || bad
}

func shadowedSkills(ctx context.Context, dir string, n *int) bool {
	hit, bad := walkSkills(ctx, dir, n)
	return hit || bad
}

func walkCommands(ctx context.Context, dir string, depth int, n *int) (bool, bool) {
	if ctx.Err() != nil {
		return false, true
	}
	if depth > shadowCommandDepth {
		return false, false
	}
	entries, err := readShadowDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, false
		}
		return false, true
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return false, true
		}
		*n++
		if *n > shadowEntryCap {
			return false, true
		}
		if renameFileName(entry.Name()) {
			return true, false
		}
		path := filepath.Join(dir, entry.Name())
		if shadowIsDir(entry, path) {
			hit, bad := walkCommands(ctx, path, depth+1, n)
			if hit || bad {
				return hit, bad
			}
		}
	}
	return false, false
}

func walkSkills(ctx context.Context, dir string, n *int) (bool, bool) {
	if ctx.Err() != nil {
		return false, true
	}
	entries, err := readShadowDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, false
		}
		return false, true
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return false, true
		}
		*n++
		if *n > shadowEntryCap {
			return false, true
		}
		if renameFileName(entry.Name()) {
			return true, false
		}
		path := filepath.Join(dir, entry.Name())
		if !shadowIsDir(entry, path) {
			continue
		}
		data, err := readBounded(filepath.Join(path, "SKILL.md"), renameShadowLimit)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return false, true
		}
		if skillFrontmatterName(data) == "rename" {
			return true, false
		}
	}
	return false, false
}

func shadowIsDir(entry os.DirEntry, path string) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func renameFileName(name string) bool {
	switch name {
	case "rename", "rename.md", "rename.markdown":
		return true
	default:
		return false
	}
}

func skillFrontmatterName(data []byte) string {
	text := string(data)
	if !strings.HasPrefix(text, "---") {
		return ""
	}
	rest := strings.TrimPrefix(text, "---")
	rest = strings.TrimPrefix(rest, "\r\n")
	rest = strings.TrimPrefix(rest, "\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		name, ok := strings.CutPrefix(line, "name:")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		name = strings.Trim(name, `"'`)
		return name
	}
	return ""
}

// readShadowDir opens a directory with O_DIRECTORY and O_NONBLOCK. A symlink
// to a directory is followed. A FIFO, or a symlink to one, fails at once.
func readShadowDir(path string) ([]os.DirEntry, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.ReadDir(-1)
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
