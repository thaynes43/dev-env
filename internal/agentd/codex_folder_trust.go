package agentd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// errFolderNotQualified means the path is not the resolved repository the
// platform bootstrapped for this session. errCodexConfigInvalid means
// config.toml was left unchanged.
var (
	errFolderNotQualified = errors.New("folder not qualified")
	errCodexConfigInvalid = errors.New("codex config invalid")
)

const codexConfigMax = 256 << 10

// ensureCodexRepoTrust records the platform clone as trusted in Codex's own
// config.toml, under projects."<path>".trust_level, before a client starts.
// A folder that does not qualify, or a config that cannot be updated without
// touching other keys, returns before any write.
func ensureCodexRepoTrust(ctx context.Context, s Settings, cwd string) error {
	if err := qualifyCodexRepo(ctx, s, cwd); err != nil {
		return err
	}
	// A private host's composer rejects a trusted root outside ~/codex/<project>,
	// and that check runs on every status read. Writing ~/repos/<repo> would
	// make the host unreadable. Stop before opening the file.
	if s.PrivateCodexHost {
		return errCodexConfigInvalid
	}
	if err := prepareCodexHome(s); err != nil {
		return err
	}
	unlock, err := lockCodexConfig(s)
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(s.CodexHome, "config.toml")
	src, mode, err := readCodexConfig(s, path)
	if err != nil {
		return err
	}
	next, changed, err := setProjectTrust(src, cwd)
	if err != nil || !changed {
		return err
	}
	return writeCodexConfigIfUnchanged(path, src, next, mode)
}

// qualifyCodexRepo accepts only the resolved path of one repository the
// platform bootstrapped: ~/repos/<repo>, with no symlink and with that
// directory as the Git toplevel. A handoff path, a parent, a subdirectory
// and a symlink out of ~/repos all fail here, before config.toml is opened.
func qualifyCodexRepo(ctx context.Context, s Settings, cwd string) error {
	if !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd {
		return errFolderNotQualified
	}
	name := filepath.Base(cwd)
	if !apiv1.ValidRepoName(name) || s.ClonePath(name) != cwd {
		return errFolderNotQualified
	}
	rel, err := filepath.Rel(s.ReposDir(), cwd)
	if err != nil || rel != name {
		return errFolderNotQualified
	}
	// The written key is the path in double quotes. A character Go would
	// escape is not a path this writer can record without changing the key.
	if strconv.Quote(cwd) != `"`+cwd+`"` {
		return errFolderNotQualified
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() || noSymlinkComponents(cwd) != nil {
		return errFolderNotQualified
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil || filepath.Clean(resolved) != cwd {
		return errFolderNotQualified
	}
	return proveCodexRepoRoot(ctx, cwd)
}

// proveCodexRepoRoot asks Git, with this directory's own .git and no parent
// discovery, whether cwd is the work tree root. A file .git, a symlink .git
// and a subdirectory of a repository all fail.
func proveCodexRepoRoot(ctx context.Context, cwd string) error {
	gitDir := filepath.Join(cwd, ".git")
	info, err := os.Lstat(gitDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || noSymlinkComponents(gitDir) != nil {
		return errFolderNotQualified
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return errFolderNotQualified
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, git, "--git-dir", gitDir, "--work-tree", cwd, "rev-parse", "--show-toplevel")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "HOME=" + os.Getenv("HOME")}
	out, err := cmd.Output()
	if err != nil {
		return errFolderNotQualified
	}
	top := filepath.Clean(strings.TrimSpace(string(out)))
	if top != cwd {
		return errFolderNotQualified
	}
	return nil
}

func prepareCodexHome(s Settings) error {
	home := s.CodexHome
	if !filepath.IsAbs(home) || filepath.Clean(home) != home || noSymlinkComponents(filepath.Dir(home)) != nil {
		return errCodexConfigInvalid
	}
	parent, err := os.Lstat(filepath.Dir(home))
	if err != nil || !parent.IsDir() {
		return errCodexConfigInvalid
	}
	if info, err := os.Lstat(home); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errCodexConfigInvalid
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errCodexConfigInvalid
	} else if err := os.Mkdir(home, 0o700); err != nil {
		return errCodexConfigInvalid
	}
	return nil
}

func readCodexConfig(s Settings, path string) ([]byte, fs.FileMode, error) {
	if err := prepareCodexHome(s); err != nil {
		return nil, 0, err
	}
	mode := fs.FileMode(0o600)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, 0, errCodexConfigInvalid
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, 0, errCodexConfigInvalid
	}
	data, err := boundedHostFile(path, codexConfigMax, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, mode, nil
	}
	if err != nil {
		return nil, 0, errCodexConfigInvalid
	}
	return data, mode, nil
}

// setProjectTrust returns the config with projects."<folder>".trust_level set
// to "trusted". changed is false when that entry was already trusted. Any
// shape that would require rewriting other keys returns errCodexConfigInvalid
// and the caller keeps the original bytes.
func setProjectTrust(src []byte, folder string) ([]byte, bool, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(src)) > 0 {
		if err := toml.Unmarshal(src, &doc); err != nil {
			return nil, false, errCodexConfigInvalid
		}
	}
	var projects map[string]any
	if raw, ok := doc["projects"]; ok {
		table, ok := raw.(map[string]any)
		if !ok {
			return nil, false, errCodexConfigInvalid
		}
		projects = table
	}
	if projects != nil {
		if raw, ok := projects[folder]; ok {
			table, ok := raw.(map[string]any)
			if !ok {
				return nil, false, errCodexConfigInvalid
			}
			if table["trust_level"] == "trusted" {
				return src, false, nil
			}
		}
	}
	next, err := editProjectTrust(src, folder)
	if err != nil {
		return nil, false, err
	}
	if len(next) > codexConfigMax || !bytes.Equal(projectTrustLevel(next, folder), []byte("trusted")) {
		return nil, false, errCodexConfigInvalid
	}
	return next, true, nil
}

func projectTrustLevel(src []byte, folder string) []byte {
	doc := map[string]any{}
	if toml.Unmarshal(src, &doc) != nil {
		return nil
	}
	projects, _ := doc["projects"].(map[string]any)
	table, _ := projects[folder].(map[string]any)
	value, _ := table["trust_level"].(string)
	return []byte(value)
}

func editProjectTrust(src []byte, folder string) ([]byte, error) {
	if len(src) == 0 {
		return appendProjectTrust(nil, folder), nil
	}
	// A multiline string can hide a header or swallow an appended table.
	// Leave the file alone rather than rewrite it.
	if bytes.Contains(src, []byte(`"""`)) || bytes.Contains(src, []byte(`'''`)) {
		return nil, errCodexConfigInvalid
	}
	header, dotted, err := findProjectTrust(src, folder)
	if err != nil {
		return nil, err
	}
	if header < 0 && dotted < 0 {
		// The parser saw this folder but the line scan did not. Appending a
		// second table would leave two entries. Stop instead.
		if projectHasFolder(src, folder) {
			return nil, errCodexConfigInvalid
		}
		return appendProjectTrust(src, folder), nil
	}
	if header >= 0 && dotted >= 0 {
		return nil, errCodexConfigInvalid
	}
	lines := splitKeep(src)
	if dotted >= 0 {
		lines[dotted] = replaceTrustAssignment(lines[dotted], `projects.`+strconv.Quote(folder)+`.trust_level`)
		return joinLines(lines), nil
	}
	return editTrustTable(lines, header)
}

func projectHasFolder(src []byte, folder string) bool {
	doc := map[string]any{}
	if toml.Unmarshal(src, &doc) != nil {
		return true
	}
	projects, _ := doc["projects"].(map[string]any)
	_, ok := projects[folder]
	return ok
}

func appendProjectTrust(src []byte, folder string) []byte {
	var b bytes.Buffer
	b.Write(src)
	if len(src) > 0 && src[len(src)-1] != '\n' {
		b.WriteByte('\n')
	}
	if len(src) > 0 {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "[projects.%s]\ntrust_level = %q\n", strconv.Quote(folder), "trusted")
	return b.Bytes()
}

func findProjectTrust(src []byte, folder string) (header, dotted int, err error) {
	header, dotted = -1, -1
	lines := splitKeep(src)
	for i, line := range lines {
		body := strings.TrimRight(line, "\r\n")
		if d, ok := dottedTrustFolder(body); ok {
			if d == folder {
				if dotted >= 0 {
					return -1, -1, errCodexConfigInvalid
				}
				dotted = i
			}
			continue
		}
		name, ok := projectsHeader(body)
		if !ok {
			continue
		}
		if name == folder {
			if header >= 0 {
				return -1, -1, errCodexConfigInvalid
			}
			header = i
		}
	}
	return header, dotted, nil
}

func editTrustTable(lines []string, header int) ([]byte, error) {
	end := len(lines)
	for i := header + 1; i < len(lines); i++ {
		body := strings.TrimRight(lines[i], "\r\n")
		if _, ok := anyHeader(body); ok {
			end = i
			break
		}
	}
	at := -1
	for i := header + 1; i < end; i++ {
		if isTrustLevelAssignment(strings.TrimRight(lines[i], "\r\n")) {
			if at >= 0 {
				return nil, errCodexConfigInvalid
			}
			at = i
		}
	}
	if at < 0 {
		nl := "\n"
		if strings.HasSuffix(lines[header], "\r\n") {
			nl = "\r\n"
		}
		insert := "trust_level = \"trusted\"" + nl
		out := append([]string{}, lines[:header+1]...)
		out = append(out, insert)
		out = append(out, lines[header+1:]...)
		return joinLines(out), nil
	}
	lines[at] = replaceTrustAssignment(lines[at], "trust_level")
	return joinLines(lines), nil
}

func replaceTrustAssignment(line, key string) string {
	nl := ""
	body := line
	switch {
	case strings.HasSuffix(line, "\r\n"):
		nl = "\r\n"
		body = strings.TrimSuffix(line, "\r\n")
	case strings.HasSuffix(line, "\n"):
		nl = "\n"
		body = strings.TrimSuffix(line, "\n")
	}
	indent := body[:len(body)-len(strings.TrimLeft(body, " \t"))]
	return indent + key + " = \"trusted\"" + nl
}

func isTrustLevelAssignment(body string) bool {
	s := strings.TrimSpace(stripTOMLComment(body))
	eq := strings.IndexByte(s, '=')
	if eq < 0 {
		return false
	}
	return strings.TrimSpace(s[:eq]) == "trust_level"
}

func projectsHeader(body string) (string, bool) {
	name, ok := anyHeader(body)
	if !ok || !strings.HasPrefix(name, "projects.") {
		return "", false
	}
	parts := splitTOMLKey(name)
	if len(parts) != 2 || parts[0] != "projects" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func anyHeader(body string) (string, bool) {
	s := strings.TrimSpace(stripTOMLComment(body))
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' || strings.HasPrefix(s, "[[") {
		return "", false
	}
	return strings.TrimSpace(s[1 : len(s)-1]), true
}

func dottedTrustFolder(body string) (string, bool) {
	s := strings.TrimSpace(stripTOMLComment(body))
	eq := strings.IndexByte(s, '=')
	if eq < 0 {
		return "", false
	}
	parts := splitTOMLKey(strings.TrimSpace(s[:eq]))
	if len(parts) != 3 || parts[0] != "projects" || parts[2] != "trust_level" {
		return "", false
	}
	return parts[1], true
}

// splitTOMLKey splits a dotted key, keeping quoted segments whole and
// dropping spaces that sit outside quotes.
func splitTOMLKey(s string) []string {
	var parts []string
	var cur strings.Builder
	inSingle, inDouble := false, false
	flush := func() {
		parts = append(parts, cur.String())
		cur.Reset()
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
				continue
			}
			cur.WriteByte(c)
		case inDouble:
			if c == '\\' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
				continue
			}
			if c == '"' {
				inDouble = false
				continue
			}
			cur.WriteByte(c)
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '.':
			flush()
		case c == ' ' || c == '\t':
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return parts
}

func stripTOMLComment(s string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case inDouble:
			if c == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if c == '"' {
				inDouble = false
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '#':
			return s[:i]
		}
	}
	return s
}

func splitKeep(src []byte) []string {
	text := string(src)
	if text == "" {
		return nil
	}
	var lines []string
	for text != "" {
		n := strings.IndexByte(text, '\n')
		if n < 0 {
			lines = append(lines, text)
			break
		}
		lines = append(lines, text[:n+1])
		text = text[n+1:]
	}
	return lines
}

func joinLines(lines []string) []byte {
	var b bytes.Buffer
	for _, line := range lines {
		b.WriteString(line)
	}
	return b.Bytes()
}

// lockCodexConfig is an exclusive flock on config.toml.lock. A busy lock
// stops the launch. The caller leaves the file unchanged.
func lockCodexConfig(s Settings) (func(), error) {
	path := filepath.Join(s.CodexHome, "config.toml.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, errCodexConfigInvalid
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, errCodexConfigInvalid
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errCodexConfigInvalid
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

// writeCodexConfigIfUnchanged writes next only when the file is still src.
// A concurrent Codex save or another launch is left as it was.
func writeCodexConfigIfUnchanged(path string, src, next []byte, mode fs.FileMode) error {
	current, err := boundedHostFile(path, codexConfigMax, false)
	if errors.Is(err, os.ErrNotExist) {
		if len(src) != 0 {
			return errCodexConfigInvalid
		}
	} else if err != nil || !bytes.Equal(current, src) {
		return errCodexConfigInvalid
	}
	return writeCodexConfigAtomic(path, next, mode)
}

func writeCodexConfigAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errCodexConfigInvalid
	}
	f, err := os.CreateTemp(dir, ".config.toml.agentd-*")
	if err != nil {
		return errCodexConfigInvalid
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return errCodexConfigInvalid
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return errCodexConfigInvalid
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return errCodexConfigInvalid
	}
	if err := f.Close(); err != nil {
		return errCodexConfigInvalid
	}
	if st, err := os.Lstat(path); err == nil && (st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular()) {
		return errCodexConfigInvalid
	}
	if err := os.Rename(tmp, path); err != nil {
		return errCodexConfigInvalid
	}
	d, err := os.Open(dir)
	if err != nil {
		return errCodexConfigInvalid
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return errCodexConfigInvalid
	}
	return nil
}
