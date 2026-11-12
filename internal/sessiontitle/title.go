// Package sessiontitle is the one check and the one composer for a session's
// role title (DESIGN-065). Callers do not trim or rewrite a value: agent-run
// trims before it calls, and the API stores the text it accepted.
package sessiontitle

import (
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
	// ReasonCharacters is a bar, a control or format character, a code point
	// above U+FFFF, or anything that is not a letter, mark, digit, punctuation
	// character, symbol or ASCII space.
	ReasonCharacters = "title_characters"
	// ReasonSpacing is a leading space, a trailing space, or two spaces in a row.
	ReasonSpacing = "title_spacing"
)

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
	if utf8.RuneCountInString(title) > MaxRunes || len(title) > MaxBytes {
		return ReasonTooLong
	}
	return ""
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
		return "title_characters: letters, marks, digits, punctuation and symbols only, with no bar, no control or format character, and nothing above U+FFFF"
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
