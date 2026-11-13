// Package sessiontitle is the one check and the one composer for a session's
// role title (DESIGN-065), and the classifier for a Claude Code capture-pane
// screen. Callers do not trim or rewrite a value: agent-run trims before it
// calls, and the API stores the text it accepted.
package sessiontitle

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxRunes is the most code points a title may hold.
	MaxRunes = 48
	// MaxBytes is the most UTF-8 bytes a title may hold.
	MaxBytes = 96

	// ReasonTooLong is more than 48 code points or more than 96 bytes.
	ReasonTooLong = "title_too_long"
	// ReasonCharacters is a bar, a backslash, a trailing semicolon, a control
	// or format character, a code point above U+FFFF, a first character that
	// is not a letter or a digit, or anything that is not a letter, mark,
	// digit, punctuation character, symbol or ASCII space.
	ReasonCharacters = "title_characters"
	// ReasonSpacing is a leading space, a trailing space, or two spaces in a row.
	ReasonSpacing = "title_spacing"
)

// Effective is the title a session shows. present is whether the title
// annotation exists. An absent annotation leaves specTitle. A present value
// that Validate accepts, including empty, is the title. A present value
// Validate refuses is ignored: the title is specTitle and ignored is true.
func Effective(annotation string, present bool, specTitle string) (string, bool) {
	if !present {
		return specTitle, false
	}
	if annotation != "" && Validate(annotation) != "" {
		return specTitle, true
	}
	return annotation, false
}

// Validate reports why title cannot be stored. An empty title is absent and
// valid to omit. The checks run spacing, then characters, then length, so a
// value that is only too long is title_too_long.
func Validate(title string) string {
	if title == "" {
		return ""
	}
	if strings.HasPrefix(title, " ") || strings.HasSuffix(title, " ") || strings.Contains(title, "  ") {
		return ReasonSpacing
	}
	for _, r := range title {
		if !runeAllowed(r) {
			return ReasonCharacters
		}
	}
	// tmux's argument parser strips a trailing semicolon, and a backslash is
	// an escape in that parser. A semicolon in the middle is a title character.
	if strings.HasSuffix(title, ";") || strings.Contains(title, `\`) {
		return ReasonCharacters
	}
	// The CLI's --remote-control takes an optional value, so a value that
	// begins with a hyphen would be read as an option. The first character
	// is therefore a letter or a digit.
	if !StartsWithLetterOrDigit(title) {
		return ReasonCharacters
	}
	if utf8.RuneCountInString(title) > MaxRunes || len(title) > MaxBytes {
		return ReasonTooLong
	}
	return ""
}

// StartsWithLetterOrDigit reports whether the first rune is a letter or a
// decimal digit. An empty string does not.
func StartsWithLetterOrDigit(s string) bool {
	r, size := utf8.DecodeRuneInString(s)
	return size > 0 && (unicode.IsLetter(r) || unicode.IsDigit(r))
}

func runeAllowed(r rune) bool {
	if r > 0xFFFF || r == '|' {
		return false
	}
	if r == ' ' {
		return true
	}
	if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
		return false
	}
	return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
}

// Explain is the fixed sentence for a reason code. It does not include the title.
func Explain(reason string) string {
	switch reason {
	case ReasonTooLong:
		return "title_too_long: a title is 1 to 48 characters and at most 96 bytes"
	case ReasonCharacters:
		return "title_characters: letters, marks, digits, punctuation and symbols only; the first character is a letter or a digit; no bar, no backslash, no trailing semicolon, no control or format character, and nothing above U+FFFF"
	case ReasonSpacing:
		return "title_spacing: no leading or trailing space, and no two spaces in a row"
	default:
		return reason
	}
}

// Family is the phone prefix for a full model id. The match is a case-sensitive
// prefix, so claude-opus-5-5 is Opus and an alias is not. Anything else has no
// prefix and the display title is the role text alone.
func Family(model string) string {
	switch {
	case strings.HasPrefix(model, "claude-fable-"):
		return "Fable"
	case strings.HasPrefix(model, "claude-opus-"):
		return "Opus"
	case strings.HasPrefix(model, "claude-sonnet-"):
		return "Sonnet"
	case strings.HasPrefix(model, "claude-haiku-"):
		return "Haiku"
	case strings.HasPrefix(model, "gpt-"):
		return "GPT"
	default:
		return ""
	}
}

// HashPrefix is the first 12 hex characters of the SHA-256 of s. Audit logs
// use it so a line can be matched to a title without holding the title.
func HashPrefix(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// recordedMinors are the major.minor pairs of Claude Code for which this
// build has capture-pane fixtures (DESIGN-065 rule 8.5 condition 8).
var recordedMinors = []string{"2.1"}

// RecordedCLIMinors is the major.minor list a rename may type into.
func RecordedCLIMinors() []string {
	out := make([]string, len(recordedMinors))
	copy(out, recordedMinors)
	return out
}

var cliVersionRE = regexp.MustCompile(`(\d+)\.(\d+)\.\d+`)

// CLIMinor returns the major.minor in a `claude --version` line such as
// "2.1.321 (Claude Code)". A patch difference is the same minor.
func CLIMinor(output string) (string, bool) {
	m := cliVersionRE.FindStringSubmatch(output)
	if m == nil {
		return "", false
	}
	return m[1] + "." + m[2], true
}

// MinorRecorded reports whether minor is one RecordedCLIMinors names.
func MinorRecorded(minor string) bool {
	for _, m := range recordedMinors {
		if m == minor {
			return true
		}
	}
	return false
}

// Compose is the string passed to the provider. It is empty when there is no
// title, the title alone when the model has no family, and "<Family> | <title>"
// otherwise. The bar is added here; role text may not contain one.
func Compose(model, title string) string {
	if title == "" {
		return ""
	}
	if family := Family(model); family != "" {
		return family + " | " + title
	}
	return title
}
