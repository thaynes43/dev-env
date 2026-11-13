package sessionraise

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// MaxWords is the most the notice shows, and the most a raise accepts.
const MaxWords = 300

// secretShape is one shape from haynes-ops SECRET_SHAPES (night-merge.mjs,
// 3ad4b39f) plus the OpenProject token the front door already scrubs.
// The name is the phrase the refusal quotes.
type secretShape struct {
	re   *regexp.Regexp
	name string
}

// The expressions are the JavaScript ones, written for Go's RE2. They only
// keep out the shapes they name.
var secretShapes = []secretShape{
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`), "a GitHub token"},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`), "a GitHub token"},
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), "a private key"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.`), "a JSON web token"},
	{regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s/:@]+:[^\s/@]+@`), "a URL with credentials"},
	{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{12,}`), "a bearer credential"},
	{regexp.MustCompile(`(?i)\b(?:password|passwd|secret|api[_-]?key|client[_-]?secret)\s*["']?\s*[:=]\s*\S`), "a credential assignment"},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), "a Slack token"},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "an AWS key"},
	{regexp.MustCompile(`\b(?:10(?:\.\d{1,3}){3}|192\.168(?:\.\d{1,3}){2}|172\.(?:1[6-9]|2\d|3[01])(?:\.\d{1,3}){2}|127(?:\.\d{1,3}){3}|169\.254(?:\.\d{1,3}){2}|100\.(?:6[4-9]|[7-9]\d|1[01]\d|12[0-7])(?:\.\d{1,3}){2})\b`), "a private network address"},
	{regexp.MustCompile(`(?i)\b(?:f[cd][0-9a-f]{2}|fe80):[0-9a-f]{0,4}:`), "a private network address"},
	{regexp.MustCompile(`(?i)[A-Za-z0-9-]\.svc\b|\.cluster\.local\b`), "an in-cluster name"},
	{regexp.MustCompile(`opapi-[0-9A-Za-z]{16,}`), "an OpenProject token"},
}

// SecretShape returns the phrase for the first shape words matches, or "".
func SecretShape(words string) string {
	for _, s := range secretShapes {
		if s.re.MatchString(words) {
			return s.name
		}
	}
	return ""
}

// SecretShapeNames is every distinct phrase, in refusal order, for the tests.
func SecretShapeNames() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range secretShapes {
		if seen[s.name] {
			continue
		}
		seen[s.name] = true
		out = append(out, s.name)
	}
	return out
}

// Normalize folds words the way the used-word check compares them: NFKC,
// punctuation removed, lower case, ends trimmed, and each run of white space
// made one space.
func Normalize(words string) string {
	s := norm.NFKC.String(words)
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.P, r) {
			return -1
		}
		return r
	}, s)
	s = strings.ToLower(s)
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// WordsHash is the hex SHA-256 of the normalised words.
func WordsHash(words string) string {
	sum := sha256.Sum256([]byte(Normalize(words)))
	return hex.EncodeToString(sum[:])
}

// WordsProblem is why words cannot be recorded, or "".
// "empty", "long", "control", or "secret:<shape>".
func WordsProblem(words string) string {
	if words == "" || Normalize(words) == "" {
		return "empty"
	}
	if !utf8.ValidString(words) || utf8.RuneCountInString(words) > MaxWords {
		return "long"
	}
	for _, r := range words {
		if r == '\u2028' || r == '\u2029' || unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			return "control"
		}
	}
	if shape := SecretShape(words); shape != "" {
		return "secret:" + shape
	}
	return ""
}
