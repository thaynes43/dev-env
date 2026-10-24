package protocol

import (
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Rescue-tree read codes. They name a refusal. They are not paths, commits,
// or file bytes.
const (
	RescueTreeNotFound   = "not-found"
	RescueTreeIncomplete = "incomplete"
	RescueTreeMismatch   = "mismatch"
	RescueTreeRefused = "refused"
	// RescueTreeMissingBase is a byte read whose recorded base commit is not
	// in the recipient clone. The thin bundle stays where it is.
	RescueTreeMissingBase = "missing-base"
	// TreeNotRegular is RepoBundle.TreeRefused when the rescue tree contains
	// a symlink, a gitlink, or a path that is not a regular file.
	TreeNotRegular = "not-regular"
	// TreeCaptureFailed is RepoBundle.TreeRefused when reading the tree's
	// metadata failed. The thin bundle stays valid.
	TreeCaptureFailed = "capture-failed"
	// TreeTooLarge is RepoBundle.TreeRefused when the tree's blobs could not
	// fit the existing 8 MiB response cap. The thin bundle stays valid.
	TreeTooLarge = "too-large"
	// TreeWithheld is RepoBundle.TreeRefused when the tree was not stored.
	// File bytes are not written to the shared volume and are not served.
	TreeWithheld = "withheld"
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
		if unsafeTreeComponent(part) {
			return false
		}
	}
	return true
}

func unsafeTreeComponent(part string) bool {
	if part == "" || part == "." || part == ".." || strings.Contains(part, ":") {
		return true
	}
	folded := foldGitComponent(part)
	return folded == ".git" || folded == ".git~1" || folded == "git~1"
}

func foldGitComponent(part string) string {
	var b strings.Builder
	for _, r := range strings.TrimRight(part, " .") {
		if unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// RegularTreeMode reports whether mode is a regular-file mode the rescue
// tree stores: 100644 or 100755.
func RegularTreeMode(mode string) bool {
	return mode == "100644" || mode == "100755"
}
