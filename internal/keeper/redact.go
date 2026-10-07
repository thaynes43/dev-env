package keeper

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// redacted stands in for credential material wherever it would be printed.
const redacted = "[redacted]"

// secretValue holds credential material: a minted token, a signed JWT. Every way
// a value can be printed, formatted, marshalled or logged shows a placeholder,
// so a credential cannot reach a log line or an error message by accident.
// Reveal is the one way to the material, and only the code that sends it calls
// it.
type secretValue struct{ v string }

func newSecretValue(v string) secretValue { return secretValue{v: v} }

// Reveal returns the material itself.
func (s secretValue) Reveal() string { return s.v }

// Empty reports whether there is no material.
func (s secretValue) Empty() bool { return s.v == "" }

// Format covers every fmt verb, %d and %x included.
func (secretValue) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// String is redacted.
func (secretValue) String() string { return redacted }

// GoString is redacted.
func (secretValue) GoString() string { return redacted }

// LogValue is redacted for slog handlers.
func (secretValue) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalLog is redacted for logr sinks.
func (secretValue) MarshalLog() any { return redacted }

// MarshalJSON is redacted.
func (secretValue) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// MarshalText is redacted.
func (secretValue) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// scrub replaces every occurrence of each secret in msg with the placeholder:
// the secret as is, without surrounding whitespace (a value stored with a
// trailing newline, quoted bare), and both in base64, the form Secret data takes
// on the wire. Errors that come back from a write carry text from the API
// server; scrubbing them keeps a token out of the log even if a message ever
// quoted the request.
func scrub(msg string, secrets ...secretValue) string {
	for _, s := range secrets {
		for _, form := range []string{s.v, strings.TrimSpace(s.v)} {
			if form == "" {
				continue
			}
			msg = strings.ReplaceAll(msg, form, redacted)
			msg = strings.ReplaceAll(msg, base64.StdEncoding.EncodeToString([]byte(form)), redacted)
		}
	}
	return msg
}
