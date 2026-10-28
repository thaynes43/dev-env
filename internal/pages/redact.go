package pages

import (
	"net/url"
	"regexp"
	"strings"
)

// Redacted replaces any value or link that could carry a credential. Nothing
// that holds a secret is stored, parsed back, or shown.
const Redacted = "REDACTED"

var (
	// secretName matches a query or fragment key that names a credential.
	secretName = regexp.MustCompile(`(?i)token|key|secret|auth|sig|password`)
	// urlInText finds a link inside free text.
	urlInText = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s<>"')\]]+`)
	// pairInText finds name=value where the name is a credential name.
	pairInText = regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(?:token|key|secret|auth|sig|password)[a-z0-9_.-]*)=[^\s&<>"')]+`)
	// bearerInText finds an Authorization-style value.
	bearerInText = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
)

// RedactURL returns raw when it is a plain https link, and Redacted when it
// has credentials (user:pass@), a query or fragment key that names one
// (token, key, secret, auth, sig, password), a scheme other than https, or any
// other shape it cannot vouch for. The empty string stays empty.
func RedactURL(raw string) string {
	if raw == "" {
		return ""
	}
	if len(raw) > MaxURL {
		return Redacted
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.User != nil {
		return Redacted
	}
	for _, part := range []string{u.RawQuery, u.Fragment} {
		if part == "" {
			continue
		}
		for _, pair := range strings.FieldsFunc(part, func(r rune) bool { return r == '&' || r == ';' }) {
			name := pair
			if i := strings.IndexByte(pair, '='); i >= 0 {
				name = pair[:i]
			}
			if dn, err := url.QueryUnescape(name); err == nil {
				name = dn
			}
			if secretName.MatchString(name) {
				return Redacted
			}
		}
	}
	return raw
}

// RedactText replaces, inside free text, every unsafe link, every
// credential-named name=value pair, and every bearer or basic value.
func RedactText(s string) string {
	s = urlInText.ReplaceAllStringFunc(s, func(m string) string {
		// Trailing punctuation belongs to the sentence, not the link.
		trimmed := strings.TrimRight(m, ".,;:!?")
		tail := m[len(trimmed):]
		if RedactURL(trimmed) == Redacted {
			return Redacted + tail
		}
		return m
	})
	s = pairInText.ReplaceAllString(s, "${1}="+Redacted)
	s = bearerInText.ReplaceAllString(s, "${1} "+Redacted)
	return s
}
