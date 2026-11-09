package agentd

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
)

func fixtureExpect() answerExpectation {
	return answerExpectation{
		SessionID:      "fixture-session",
		PromptSHA256:   strings.Repeat("ab", 32),
		ModelRequested: "fixture-model",
	}
}

func quoteAnswer(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				const hexdigits = "0123456789abcdef"
				b.WriteString(`\u`)
				b.WriteByte(hexdigits[(r>>12)&0xF])
				b.WriteByte(hexdigits[(r>>8)&0xF])
				b.WriteByte(hexdigits[(r>>4)&0xF])
				b.WriteByte(hexdigits[r&0xF])
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func answerObject(fields [][2]string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, field := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(quoteAnswer(field[0]))
		b.WriteByte(':')
		b.WriteString(field[1])
	}
	b.WriteString("}\n")
	return b.String()
}

func producerFields(expect answerExpectation, textJSON string, utf8Bytes, chunks int) [][2]string {
	return [][2]string{
		{"receipt", quoteAnswer("assistant-answer-v1")},
		{"stage", quoteAnswer("complete")},
		{"stopReason", quoteAnswer("end_turn")},
		{"sessionID", quoteAnswer(expect.SessionID)},
		{"promptSHA256", quoteAnswer(expect.PromptSHA256)},
		{"modelRequested", quoteAnswer(expect.ModelRequested)},
		{"servedModel", quoteAnswer("Unknown")},
		{"utf8Bytes", strconv.Itoa(utf8Bytes)},
		{"chunkCount", strconv.Itoa(chunks)},
		{"text", textJSON},
	}
}

func producerDoc(expect answerExpectation, text string, chunks int) string {
	return answerObject(producerFields(expect, quoteAnswer(text), len(text), chunks))
}

func withoutField(fields [][2]string, name string) [][2]string {
	out := make([][2]string, 0, len(fields)-1)
	for _, field := range fields {
		if field[0] != name {
			out = append(out, field)
		}
	}
	return out
}

func replaceField(fields [][2]string, name, raw string) [][2]string {
	out := append([][2]string(nil), fields...)
	for i := range out {
		if out[i][0] == name {
			out[i][1] = raw
		}
	}
	return out
}

func decodeFixture(raw string, expect answerExpectation) (answerResult, error) {
	return decodeAssistantAnswer(strings.NewReader(raw), expect)
}

func TestDecodeAssistantAnswerSuccess(t *testing.T) {
	expect := fixtureExpect()
	longLabel := "s" + strings.Repeat("a", 127)
	cases := []struct {
		name   string
		raw    string
		expect answerExpectation
		text   string
		bytes  int
		chunks int
	}{
		{
			name:   "producer shape",
			raw:    producerDoc(expect, "hello", 1),
			expect: expect,
			text:   "hello",
			bytes:  5,
			chunks: 1,
		},
		{
			name:   "unicode snow",
			raw:    producerDoc(expect, "雪", 1),
			expect: expect,
			text:   "雪",
			bytes:  len("雪"),
			chunks: 1,
		},
		{
			name:   "controls and replacement",
			raw:    producerDoc(expect, "a\n\t\r\b\f\x01\uFFFD", 1),
			expect: expect,
			text:   "a\n\t\r\b\f\x01\uFFFD",
			bytes:  len("a\n\t\r\b\f\x01\uFFFD"),
			chunks: 1,
		},
		{
			name: "surrogate pair",
			raw: answerObject(producerFields(
				expect, `"\uD83D\uDE00"`, len("😀"), 1,
			)),
			expect: expect,
			text:   "😀",
			bytes:  len("😀"),
			chunks: 1,
		},
		{
			name: "raw emoji and slash escape",
			raw: answerObject(producerFields(
				expect, `"A\/😀"`, len("A/😀"), 1,
			)),
			expect: expect,
			text:   "A/😀",
			bytes:  len("A/😀"),
			chunks: 1,
		},
		{
			name: "escaped replacement character",
			raw: answerObject(producerFields(
				expect, `"\uFFFD"`, len("\uFFFD"), 1,
			)),
			expect: expect,
			text:   "\uFFFD",
			bytes:  len("\uFFFD"),
			chunks: 1,
		},
		{
			name:   "empty chunks still count",
			raw:    producerDoc(expect, "a", 65536),
			expect: expect,
			text:   "a",
			bytes:  1,
			chunks: 65536,
		},
		{
			name:   "exact utf8 bound",
			raw:    producerDoc(expect, strings.Repeat("a", 65536), 1),
			expect: expect,
			text:   strings.Repeat("a", 65536),
			bytes:  65536,
			chunks: 1,
		},
		{
			name:   "label length 128",
			raw:    producerDoc(answerExpectation{SessionID: longLabel, PromptSHA256: expect.PromptSHA256, ModelRequested: expect.ModelRequested}, "z", 1),
			expect: answerExpectation{SessionID: longLabel, PromptSHA256: expect.PromptSHA256, ModelRequested: expect.ModelRequested},
			text:   "z",
			bytes:  1,
			chunks: 1,
		},
		{
			name:   "leading space",
			raw:    " " + producerDoc(expect, "hello", 1),
			expect: expect,
			text:   "hello",
			bytes:  5,
			chunks: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeFixture(tc.raw, tc.expect)
			if err != nil {
				t.Fatalf("error %v", err)
			}
			if got.Text != tc.text || got.UTF8Bytes != tc.bytes || got.ChunkCount != tc.chunks {
				t.Fatalf("got text %q bytes %d chunks %d", got.Text, got.UTF8Bytes, got.ChunkCount)
			}
		})
	}
}

func TestDecodeAssistantAnswerWorstCaseEscapeFits(t *testing.T) {
	expect := fixtureExpect()
	var textJSON strings.Builder
	textJSON.Grow(2 + 65536*6)
	textJSON.WriteByte('"')
	for i := 0; i < 65536; i++ {
		textJSON.WriteString(`\u0001`)
	}
	textJSON.WriteByte('"')
	raw := answerObject(producerFields(expect, textJSON.String(), 65536, 1))
	if len(raw) > maxAnswerEncoded {
		t.Fatalf("fixture exceeds cap: %d", len(raw))
	}
	got, err := decodeFixture(raw, expect)
	if err != nil {
		t.Fatalf("error %v", err)
	}
	if got.UTF8Bytes != 65536 || got.ChunkCount != 1 || got.Text != strings.Repeat("\x01", 65536) {
		t.Fatalf("bytes %d chunks %d", got.UTF8Bytes, got.ChunkCount)
	}
}

func TestDecodeAssistantAnswerOneByteReader(t *testing.T) {
	expect := fixtureExpect()
	raw := producerDoc(expect, "hello", 1)
	got, err := decodeAssistantAnswer(&oneByteReader{b: []byte(raw)}, expect)
	if err != nil {
		t.Fatalf("error %v", err)
	}
	if got.Text != "hello" {
		t.Fatalf("text %q", got.Text)
	}
}

type oneByteReader struct {
	b []byte
	i int
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	p[0] = r.b[r.i]
	r.i++
	return 1, nil
}

func TestDecodeAssistantAnswerFailures(t *testing.T) {
	expect := fixtureExpect()
	base := producerFields(expect, quoteAnswer("hello"), 5, 1)
	canary := "CANARY-ANSWER-TEXT"
	canaryDoc := answerObject(replaceField(
		producerFields(expect, quoteAnswer(canary), len(canary), 1),
		"stage", quoteAnswer("partial"),
	))
	cases := []struct {
		name string
		raw  string
		want error
	}{
		{name: "missing receipt", raw: answerObject(withoutField(base, "receipt")), want: errMissingField},
		{name: "missing text", raw: answerObject(withoutField(base, "text")), want: errMissingField},
		{name: "missing utf8", raw: answerObject(withoutField(base, "utf8Bytes")), want: errMissingField},
		{name: "extra field", raw: answerObject(append(append([][2]string{}, base...), [2]string{"note", "1"})), want: errExtraField},
		{name: "duplicate field", raw: answerObject(append(append([][2]string{}, base...), [2]string{"receipt", quoteAnswer("assistant-answer-v1")})), want: errDuplicateField},
		{name: "bad version", raw: answerObject(replaceField(base, "receipt", quoteAnswer("assistant-answer-v0"))), want: errBadReceipt},
		{name: "bad stage", raw: answerObject(replaceField(base, "stage", quoteAnswer("partial"))), want: errBadStage},
		{name: "max tokens", raw: answerObject(replaceField(base, "stopReason", quoteAnswer("max_tokens"))), want: errBadStopReason},
		{name: "cancelled", raw: answerObject(replaceField(base, "stopReason", quoteAnswer("cancelled"))), want: errBadStopReason},
		{name: "refusal", raw: answerObject(replaceField(base, "stopReason", quoteAnswer("refusal"))), want: errBadStopReason},
		{name: "unknown stop", raw: answerObject(replaceField(base, "stopReason", quoteAnswer("unknown"))), want: errBadStopReason},
		{name: "served model claim", raw: answerObject(replaceField(base, "servedModel", quoteAnswer("other-model"))), want: errBadServedModel},
		{name: "empty served model", raw: answerObject(replaceField(base, "servedModel", quoteAnswer(""))), want: errBadServedModel},
		{name: "quoted utf8", raw: answerObject(replaceField(base, "utf8Bytes", quoteAnswer("5"))), want: errWrongType},
		{name: "bool utf8", raw: answerObject(replaceField(base, "utf8Bytes", "true")), want: errWrongType},
		{name: "null utf8", raw: answerObject(replaceField(base, "utf8Bytes", "null")), want: errWrongType},
		{name: "fraction", raw: answerObject(replaceField(base, "utf8Bytes", "5.0")), want: errBadNumber},
		{name: "exponent", raw: answerObject(replaceField(base, "chunkCount", "1e2")), want: errBadNumber},
		{name: "leading zero", raw: answerObject(replaceField(base, "chunkCount", "01")), want: errBadNumber},
		{name: "negative", raw: answerObject(replaceField(base, "chunkCount", "-1")), want: errBadNumber},
		{name: "overflow", raw: answerObject(replaceField(base, "chunkCount", "99999999999999999999")), want: errBadNumber},
		{name: "zero bytes", raw: answerObject(replaceField(replaceField(base, "text", quoteAnswer("")), "utf8Bytes", "0")), want: errBadText},
		{name: "byte mismatch", raw: answerObject(replaceField(base, "utf8Bytes", "4")), want: errBadUTF8Bytes},
		{name: "bytes above cap", raw: answerObject(replaceField(base, "utf8Bytes", "65537")), want: errBadUTF8Bytes},
		{name: "zero chunks", raw: answerObject(replaceField(base, "chunkCount", "0")), want: errBadChunkCount},
		{name: "chunks above cap", raw: answerObject(replaceField(base, "chunkCount", "65537")), want: errBadChunkCount},
		{name: "null text", raw: answerObject(replaceField(base, "text", "null")), want: errWrongType},
		{name: "number text", raw: answerObject(replaceField(base, "text", "1")), want: errWrongType},
		{name: "lone high surrogate", raw: answerObject(replaceField(base, "text", `"\uD800"`)), want: errBadEscape},
		{name: "lone low surrogate", raw: answerObject(replaceField(base, "text", `"\uDE00"`)), want: errBadEscape},
		{name: "high then non-surrogate", raw: answerObject(replaceField(base, "text", `"\uD800\u0050"`)), want: errBadEscape},
		{name: "two high surrogates", raw: answerObject(replaceField(base, "text", `"\uD800\uD800"`)), want: errBadEscape},
		{name: "bad hex", raw: answerObject(replaceField(base, "text", `"\u12GG"`)), want: errBadEscape},
		{name: "unknown escape", raw: answerObject(replaceField(base, "text", `"\q"`)), want: errBadEscape},
		{name: "raw control", raw: strings.Replace(producerDoc(expect, "hello", 1), "hello", "hel\x01lo", 1), want: errMalformed},
		{name: "truncated object", raw: `{"receipt":`, want: errTruncated},
		{name: "truncated string", raw: `{"receipt":"assistant`, want: errTruncated},
		{name: "truncated escape", raw: `{"text":"\u12`, want: errTruncated},
		{name: "second document", raw: producerDoc(expect, "hello", 1) + "{}", want: errTrailing},
		{name: "trailing junk", raw: strings.TrimRight(producerDoc(expect, "hello", 1), "\n") + "x", want: errTrailing},
		{name: "array", raw: "[]\n", want: errMalformed},
		{name: "bool document", raw: "true\n", want: errMalformed},
		{name: "nested value", raw: answerObject(replaceField(base, "text", "{}")), want: errWrongType},
		{name: "wrong session", raw: producerDoc(expect, "hello", 1), want: errBinding},
		{name: "uppercase hash", raw: answerObject(replaceField(base, "promptSHA256", quoteAnswer(strings.ToUpper(expect.PromptSHA256)))), want: errBinding},
		{name: "wrong model", raw: producerDoc(expect, "hello", 1), want: errBinding},
		{name: "session too long", raw: producerDoc(answerExpectation{SessionID: "s" + strings.Repeat("a", 128), PromptSHA256: expect.PromptSHA256, ModelRequested: expect.ModelRequested}, "z", 1), want: errBinding},
		{name: "canary stays out of the error", raw: canaryDoc, want: errBadStage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			use := expect
			switch tc.name {
			case "wrong session":
				use.SessionID = "other-session"
			case "wrong model":
				use.ModelRequested = "other-model"
			case "session too long":
				use = answerExpectation{
					SessionID:      "s" + strings.Repeat("a", 128),
					PromptSHA256:   expect.PromptSHA256,
					ModelRequested: expect.ModelRequested,
				}
			}
			got, err := decodeFixture(tc.raw, use)
			if err != tc.want {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if got != (answerResult{}) {
				t.Fatalf("nonzero result")
			}
			msg := err.Error()
			if strings.Contains(msg, canary) || strings.Contains(msg, expect.SessionID) || strings.Contains(msg, expect.PromptSHA256) || strings.Contains(msg, "hello") {
				t.Fatalf("error echoes input %q", msg)
			}
		})
	}
}

func TestDecodeAssistantAnswerBoundsAndPrivacy(t *testing.T) {
	expect := fixtureExpect()
	canary := "CANARY-ANSWER-TEXT"
	oversized := bytes.Repeat([]byte("x"), maxAnswerEncoded+1)
	got, err := decodeAssistantAnswer(bytes.NewReader(oversized), expect)
	if err != errOversized || got != (answerResult{}) {
		t.Fatalf("oversized got %v %q", err, got.Text)
	}
	if strings.Contains(err.Error(), "x") && len(err.Error()) > len("oversized") {
		t.Fatalf("oversized error echoes payload")
	}
	invalid := []byte(producerDoc(expect, "hello", 1))
	invalid = append(invalid, 0xff)
	got, err = decodeAssistantAnswer(bytes.NewReader(invalid), expect)
	if err != errInvalidUTF8 || got.Text != "" {
		t.Fatalf("utf8 got %v %q", err, got.Text)
	}
	raw := producerDoc(expect, canary, 1)
	got, err = decodeAssistantAnswer(strings.NewReader(raw), answerExpectation{
		SessionID:      "CANARYSESSION",
		PromptSHA256:   expect.PromptSHA256,
		ModelRequested: expect.ModelRequested,
	})
	if err != errBinding || got != (answerResult{}) {
		t.Fatalf("binding got %v %#v", err, got)
	}
	if strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), "CANARYSESSION") {
		t.Fatalf("binding error echoes identifiers")
	}
	if _, err = decodeAssistantAnswer(nil, expect); err != errRead {
		t.Fatalf("nil reader %v", err)
	}
}

// answerReadFail returns payload bytes, then a non-EOF error.
// withError reports that error on the same Read that delivers the last bytes.
type answerReadFail struct {
	data []byte
	err  error
	with bool
	used bool
}

func (r *answerReadFail) Read(p []byte) (int, error) {
	if r.with {
		if r.used {
			return 0, r.err
		}
		r.used = true
		n := copy(p, r.data)
		return n, r.err
	}
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestDecodeAssistantAnswerReadError(t *testing.T) {
	expect := fixtureExpect()
	canary := errors.New("CANARY-READ")
	valid := producerDoc(expect, "hello", 1)
	cases := []struct {
		name string
		r    io.Reader
	}{
		{
			name: "partial then error",
			r:    &answerReadFail{data: []byte(`{"receipt":"assistant-answer-v1"`), err: canary},
		},
		{
			name: "valid bytes with error",
			r:    &answerReadFail{data: []byte(valid), err: canary, with: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeAssistantAnswer(tc.r, expect)
			if err != errRead || got != (answerResult{}) {
				t.Fatalf("got %v %#v", err, got)
			}
			if err.Error() != "read-error" || strings.Contains(err.Error(), "CANARY-READ") || strings.Contains(err.Error(), "hello") {
				t.Fatalf("error class %q", err.Error())
			}
		})
	}
}

func TestDecodeAssistantAnswerMismatchedLowercaseHash(t *testing.T) {
	expect := fixtureExpect()
	use := expect
	use.PromptSHA256 = strings.Repeat("cd", 32)
	got, err := decodeFixture(producerDoc(expect, "hello", 1), use)
	if err != errBinding || got != (answerResult{}) {
		t.Fatalf("got %v %#v", err, got)
	}
	if strings.Contains(err.Error(), expect.PromptSHA256) || strings.Contains(err.Error(), use.PromptSHA256) {
		t.Fatalf("error echoes hash %q", err.Error())
	}
}

func TestDecodeAssistantAnswerEscapedDuplicateKey(t *testing.T) {
	expect := fixtureExpect()
	doc := producerDoc(expect, "hello", 1)
	raw := `{"\u0072eceipt":"assistant-answer-v1",` + doc[1:]
	got, err := decodeFixture(raw, expect)
	if err != errDuplicateField || got != (answerResult{}) {
		t.Fatalf("got %v %#v", err, got)
	}
	if strings.Contains(err.Error(), "receipt") || strings.Contains(err.Error(), "hello") {
		t.Fatalf("error echoes key %q", err.Error())
	}
}

func TestDecodeAssistantAnswerEveryOmission(t *testing.T) {
	expect := fixtureExpect()
	base := producerFields(expect, quoteAnswer("hello"), len("hello"), 1)
	for _, name := range answerFields {
		t.Run(name, func(t *testing.T) {
			got, err := decodeFixture(answerObject(withoutField(base, name)), expect)
			if err != errMissingField || got != (answerResult{}) {
				t.Fatalf("got %v %#v", err, got)
			}
			if strings.Contains(err.Error(), name) || strings.Contains(err.Error(), "hello") {
				t.Fatalf("error echoes field %q", err.Error())
			}
		})
	}
}

func TestDecodeAssistantAnswerReorderedFields(t *testing.T) {
	expect := fixtureExpect()
	fields := producerFields(expect, quoteAnswer("hello"), len("hello"), 1)
	for i, j := 0, len(fields)-1; i < j; i, j = i+1, j-1 {
		fields[i], fields[j] = fields[j], fields[i]
	}
	got, err := decodeFixture(answerObject(fields), expect)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "hello" || got.UTF8Bytes != 5 || got.ChunkCount != 1 {
		t.Fatalf("%#v", got)
	}
}

func TestDecodeAssistantAnswerInvalidUTF8InsideText(t *testing.T) {
	expect := fixtureExpect()
	raw := []byte(producerDoc(expect, "hello", 1))
	idx := bytes.Index(raw, []byte("hello"))
	if idx < 0 {
		t.Fatal("text missing")
	}
	raw[idx] = 0xff
	got, err := decodeAssistantAnswer(bytes.NewReader(raw), expect)
	if err != errInvalidUTF8 || got != (answerResult{}) {
		t.Fatalf("got %v %#v", err, got)
	}
	if strings.Contains(got.Text, "\uFFFD") || strings.Contains(err.Error(), "hello") {
		t.Fatalf("repaired or echoed %#v %q", got, err.Error())
	}
}

const pythonProducerFixture = `{"receipt":"assistant-answer-v1","stage":"complete","stopReason":"end_turn","sessionID":"fixture-session","promptSHA256":"ce5116e8585551448d3c9bf54f5c6ecb9a070371167393959f8f1ea21e122aad","modelRequested":"fixture-model","servedModel":"Unknown","utf8Bytes":19,"chunkCount":2,"text":"literal 雪😀\n�"}
`

func TestDecodeAssistantAnswerPythonProducerFixture(t *testing.T) {
	expect := answerExpectation{
		SessionID:      "fixture-session",
		PromptSHA256:   "ce5116e8585551448d3c9bf54f5c6ecb9a070371167393959f8f1ea21e122aad",
		ModelRequested: "fixture-model",
	}
	want := "literal 雪😀\n\uFFFD"
	got, err := decodeAssistantAnswer(strings.NewReader(pythonProducerFixture), expect)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != want || got.UTF8Bytes != 19 || got.ChunkCount != 2 || len(got.Text) != 19 {
		t.Fatalf("%#v len %d", got, len(got.Text))
	}
	if strings.Contains(got.Text, "Unknown") || got.Text == "" {
		t.Fatalf("served model or empty text %#v", got)
	}
}
