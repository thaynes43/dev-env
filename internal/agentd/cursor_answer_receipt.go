// Package agentd holds the in-pod supervisor. This file is a standalone,
// unexported decoder for assistant-answer-v1. Production code does not call it.

package agentd

import (
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	answerReceiptName = "assistant-answer-v1"
	answerStageDone   = "complete"
	answerStopEndTurn = "end_turn"
	answerServedNone  = "Unknown"
	maxAnswerUTF8     = 65536
	maxAnswerChunks   = 65536
	// Producer text can expand to \u00XX (6 bytes) per source byte, plus a
	// fixed envelope. This bounds memory. It is not a read deadline.
	maxAnswerEncoded = 6*maxAnswerUTF8 + 4096
)

// answerExpectation is caller context. A match is not proof of an admitted
// task, a Pod, an author, or an authorization.
type answerExpectation struct {
	SessionID      string
	PromptSHA256   string
	ModelRequested string
}

// answerResult is empty unless every check succeeded.
type answerResult struct {
	Text       string
	UTF8Bytes  int
	ChunkCount int
}

type answerError string

func (e answerError) Error() string { return string(e) }

const (
	errRead           answerError = "read-error"
	errOversized      answerError = "oversized"
	errInvalidUTF8    answerError = "invalid-utf8"
	errTruncated      answerError = "truncated"
	errMalformed      answerError = "malformed"
	errBadEscape      answerError = "bad-escape"
	errDuplicateField answerError = "duplicate-field"
	errWrongType      answerError = "wrong-type"
	errBadNumber      answerError = "bad-number"
	errMissingField   answerError = "missing-field"
	errExtraField     answerError = "extra-field"
	errBadReceipt     answerError = "bad-receipt"
	errBadStage       answerError = "bad-stage"
	errBadStopReason  answerError = "bad-stop-reason"
	errBadServedModel answerError = "bad-served-model"
	errBinding        answerError = "binding-mismatch"
	errBadText        answerError = "bad-text"
	errBadUTF8Bytes   answerError = "bad-utf8-bytes"
	errBadChunkCount  answerError = "bad-chunk-count"
	errTrailing       answerError = "trailing-data"
)

var answerFields = [...]string{
	"receipt",
	"stage",
	"stopReason",
	"sessionID",
	"promptSHA256",
	"modelRequested",
	"servedModel",
	"utf8Bytes",
	"chunkCount",
	"text",
}

// decodeAssistantAnswer reads one bounded document and returns answer text
// only after every check passes. Any failure returns a zero result and a
// fixed class. The class does not contain the document, the text, or the
// expected identifiers. Caller cancellation and blocking reads stay outside.
func decodeAssistantAnswer(r io.Reader, expect answerExpectation) (answerResult, error) {
	var zero answerResult
	if r == nil {
		return zero, errRead
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxAnswerEncoded+1))
	if err != nil {
		return zero, errRead
	}
	if len(raw) > maxAnswerEncoded {
		return zero, errOversized
	}
	if !utf8.Valid(raw) {
		return zero, errInvalidUTF8
	}
	got, err := parseAnswerDocument(raw, expect)
	if err != nil {
		return zero, err
	}
	return got, nil
}

type answerField struct {
	kind int
	str  string
	num  int64
}

const (
	fieldString = 1
	fieldNumber = 2
)

type answerParser struct {
	b []byte
	i int
}

func parseAnswerDocument(raw []byte, expect answerExpectation) (answerResult, error) {
	var zero answerResult
	p := answerParser{b: raw}
	fields, err := p.parseObject()
	if err != nil {
		return zero, err
	}
	p.skipSpace()
	if p.i != len(p.b) {
		return zero, errTrailing
	}
	return answerFieldsToResult(fields, expect)
}

func (p *answerParser) skipSpace() {
	for p.i < len(p.b) {
		switch p.b[p.i] {
		case ' ', '\n', '\r', '\t':
			p.i++
		default:
			return
		}
	}
}

func (p *answerParser) parseObject() (map[string]answerField, error) {
	p.skipSpace()
	if p.i >= len(p.b) {
		return nil, errTruncated
	}
	if p.b[p.i] != '{' {
		return nil, errMalformed
	}
	p.i++
	fields := make(map[string]answerField, len(answerFields))
	p.skipSpace()
	if p.i >= len(p.b) {
		return nil, errTruncated
	}
	if p.b[p.i] == '}' {
		p.i++
		return fields, nil
	}
	for {
		key, err := p.parseString()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.i >= len(p.b) {
			return nil, errTruncated
		}
		if p.b[p.i] != ':' {
			return nil, errMalformed
		}
		p.i++
		p.skipSpace()
		value, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		if _, exists := fields[key]; exists {
			return nil, errDuplicateField
		}
		fields[key] = value
		p.skipSpace()
		if p.i >= len(p.b) {
			return nil, errTruncated
		}
		switch p.b[p.i] {
		case ',':
			p.i++
			p.skipSpace()
			if p.i >= len(p.b) {
				return nil, errTruncated
			}
			if p.b[p.i] == '}' {
				return nil, errMalformed
			}
		case '}':
			p.i++
			return fields, nil
		default:
			return nil, errMalformed
		}
	}
}

func (p *answerParser) parseValue() (answerField, error) {
	if p.i >= len(p.b) {
		return answerField{}, errTruncated
	}
	switch p.b[p.i] {
	case '"':
		text, err := p.parseString()
		if err != nil {
			return answerField{}, err
		}
		return answerField{kind: fieldString, str: text}, nil
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		n, err := p.parseNumber()
		if err != nil {
			return answerField{}, err
		}
		return answerField{kind: fieldNumber, num: n}, nil
	case 't', 'f', 'n', '{', '[':
		return answerField{}, errWrongType
	default:
		return answerField{}, errMalformed
	}
}

func (p *answerParser) parseNumber() (int64, error) {
	if p.i >= len(p.b) {
		return 0, errTruncated
	}
	if p.b[p.i] == '-' {
		return 0, errBadNumber
	}
	start := p.i
	if p.b[p.i] == '0' {
		p.i++
		if p.i < len(p.b) {
			c := p.b[p.i]
			if c == '.' || c == 'e' || c == 'E' || (c >= '0' && c <= '9') {
				return 0, errBadNumber
			}
		}
		return 0, nil
	}
	if p.b[p.i] < '0' || p.b[p.i] > '9' {
		return 0, errMalformed
	}
	for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
		p.i++
	}
	if p.i < len(p.b) {
		c := p.b[p.i]
		if c == '.' || c == 'e' || c == 'E' {
			return 0, errBadNumber
		}
	}
	n, err := strconv.ParseInt(string(p.b[start:p.i]), 10, 64)
	if err != nil {
		return 0, errBadNumber
	}
	return n, nil
}

func (p *answerParser) parseString() (string, error) {
	if p.i >= len(p.b) {
		return "", errTruncated
	}
	if p.b[p.i] != '"' {
		return "", errMalformed
	}
	p.i++
	var out strings.Builder
	for {
		if p.i >= len(p.b) {
			return "", errTruncated
		}
		c := p.b[p.i]
		if c == '"' {
			p.i++
			return out.String(), nil
		}
		if c == '\\' {
			p.i++
			if p.i >= len(p.b) {
				return "", errTruncated
			}
			esc := p.b[p.i]
			p.i++
			switch esc {
			case '"', '\\', '/':
				out.WriteByte(esc)
			case 'b':
				out.WriteByte('\b')
			case 'f':
				out.WriteByte('\f')
			case 'n':
				out.WriteByte('\n')
			case 'r':
				out.WriteByte('\r')
			case 't':
				out.WriteByte('\t')
			case 'u':
				r, err := p.parseUnicode()
				if err != nil {
					return "", err
				}
				out.WriteRune(r)
			default:
				return "", errBadEscape
			}
			continue
		}
		if c < 0x20 {
			return "", errMalformed
		}
		r, size := utf8.DecodeRune(p.b[p.i:])
		if r == utf8.RuneError && size == 1 {
			return "", errInvalidUTF8
		}
		out.WriteRune(r)
		p.i += size
	}
}

func (p *answerParser) parseUnicode() (rune, error) {
	high, err := p.readHex4()
	if err != nil {
		return 0, err
	}
	if high >= 0xDC00 && high <= 0xDFFF {
		return 0, errBadEscape
	}
	if high < 0xD800 || high > 0xDBFF {
		return rune(high), nil
	}
	if p.i+1 >= len(p.b) || p.b[p.i] != '\\' || p.b[p.i+1] != 'u' {
		return 0, errBadEscape
	}
	p.i += 2
	low, err := p.readHex4()
	if err != nil {
		return 0, err
	}
	if low < 0xDC00 || low > 0xDFFF {
		return 0, errBadEscape
	}
	cp := 0x10000 + (high-0xD800)<<10 + (low - 0xDC00)
	return rune(cp), nil
}

func (p *answerParser) readHex4() (int, error) {
	if p.i+4 > len(p.b) {
		return 0, errTruncated
	}
	n := 0
	for k := 0; k < 4; k++ {
		c := p.b[p.i]
		p.i++
		var d int
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		default:
			return 0, errBadEscape
		}
		n = n<<4 | d
	}
	return n, nil
}

func answerFieldsToResult(fields map[string]answerField, expect answerExpectation) (answerResult, error) {
	var zero answerResult
	for _, name := range answerFields {
		if _, ok := fields[name]; !ok {
			return zero, errMissingField
		}
	}
	if len(fields) != len(answerFields) {
		return zero, errExtraField
	}
	for _, name := range []string{
		"receipt", "stage", "stopReason", "sessionID", "promptSHA256",
		"modelRequested", "servedModel", "text",
	} {
		if fields[name].kind != fieldString {
			return zero, errWrongType
		}
	}
	for _, name := range []string{"utf8Bytes", "chunkCount"} {
		if fields[name].kind != fieldNumber {
			return zero, errWrongType
		}
	}
	if fields["receipt"].str != answerReceiptName {
		return zero, errBadReceipt
	}
	if fields["stage"].str != answerStageDone {
		return zero, errBadStage
	}
	if fields["stopReason"].str != answerStopEndTurn {
		return zero, errBadStopReason
	}
	if fields["servedModel"].str != answerServedNone {
		return zero, errBadServedModel
	}
	session := fields["sessionID"].str
	sum := fields["promptSHA256"].str
	model := fields["modelRequested"].str
	if !answerLabel(expect.SessionID) || session != expect.SessionID || !answerLabel(session) {
		return zero, errBinding
	}
	if !answerHash(expect.PromptSHA256) || sum != expect.PromptSHA256 || !answerHash(sum) {
		return zero, errBinding
	}
	if !answerLabel(expect.ModelRequested) || model != expect.ModelRequested || !answerLabel(model) {
		return zero, errBinding
	}
	text := fields["text"].str
	if text == "" {
		return zero, errBadText
	}
	size := len(text)
	declared := fields["utf8Bytes"].num
	if declared < 1 || declared > maxAnswerUTF8 || declared != int64(size) {
		return zero, errBadUTF8Bytes
	}
	chunks := fields["chunkCount"].num
	if chunks < 1 || chunks > maxAnswerChunks {
		return zero, errBadChunkCount
	}
	return answerResult{Text: text, UTF8Bytes: size, ChunkCount: int(chunks)}, nil
}

func answerLabel(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

func answerHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
