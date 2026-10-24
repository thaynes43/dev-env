package protocol

import (
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Rescue-tree read codes. They name a refusal. They are not paths, commits,
// or file bytes.
const (
	RescueTreeNotFound   = "not-found"
	RescueTreeIncomplete = "incomplete"
	RescueTreeMismatch   = "mismatch"
	RescueTreeRefused    = "refused"
	// TreeNotRegular is RepoBundle.TreeRefused when the rescue tree contains
	// a symlink, a gitlink, or a path that is not a regular file.
	TreeNotRegular = "not-regular"
)

// RescueTree is what `agentd ctl rescue-tree` prints. OK is false for a
// refusal; the files are present only when OK is true.
type RescueTree struct {
	OK     bool             `json:"ok"`
	Code   string           `json:"code,omitempty"`
	ID     string           `json:"id,omitempty"`
	Repo   string           `json:"repo,omitempty"`
	Commit string           `json:"commit,omitempty"`
	Ref    string           `json:"ref,omitempty"`
	Files  []RescueTreeFile `json:"files,omitempty"`
}

// RescueTreeFile is one regular file in the rescue-branch tree.
type RescueTreeFile struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	SHA256 string `json:"sha256"`
	Bytes  []byte `json:"bytes"`
}

var gitObject = regexp.MustCompile(`^[0-9a-f]{40}$|^[0-9a-f]{64}$`)

// ValidGitObject reports whether s is a Git object name this rescue path
// accepts: 40 or 64 lowercase hex digits.
func ValidGitObject(s string) bool { return gitObject.MatchString(s) }

// RescueWorkRef reports whether ref is a rescue-branch tip. The agent branch
// and a stash anchor are not this tree.
func RescueWorkRef(ref string) bool {
	rest, ok := strings.CutPrefix(ref, "refs/heads/rescue/")
	if !ok || rest == "" || rest == "." || strings.Contains(rest, "..") {
		return false
	}
	if strings.ContainsAny(rest, " \t\n\r\\") || strings.HasPrefix(rest, "/") {
		return false
	}
	return utf8.ValidString(rest)
}

// AcceptableRescueTreePath reports whether p can be stored or returned as a
// file in a rescue tree. It rejects absolute paths, traversal, empty
// components, and a .git component.
func AcceptableRescueTreePath(p string) bool {
	if p == "" || len(p) > 4096 || !utf8.ValidString(p) {
		return false
	}
	if strings.ContainsAny(p, "\\\n\r\x00") || path.IsAbs(p) {
		return false
	}
	if path.Clean(p) != p {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || part == ".git" {
			return false
		}
	}
	return true
}

// RegularTreeMode reports whether mode is a regular-file mode the rescue
// tree stores: 100644 or 100755.
func RegularTreeMode(mode string) bool {
	return mode == "100644" || mode == "100755"
}
