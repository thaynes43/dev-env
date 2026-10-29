// CEL comparison only. No network, credentials, provider calls or source text output.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"slices"
	"time"
	"unicode/utf8"

	"cel.dev/cel-go/common"
	"cel.dev/cel-go/parser"
	"cel.dev/cel-go/parser/gen"
	"github.com/antlr4-go/antlr/v4"
)

const (
	profile            = "cel-go0.30.0"
	maxExpressionBytes = 64 * 1024
	maxInputLine       = 1024 * 1024
	maxInputBytes      = 32 * 1024 * 1024
	maxRequests        = 256
)

type request struct {
	Op       string `json:"op"`
	Actual   string `json:"actual"`
	Expected string `json:"expected"`
}

type reply struct {
	OK      bool   `json:"ok"`
	Equal   *bool  `json:"equal,omitempty"`
	Code    string `json:"code,omitempty"`
	Profile string `json:"profile"`
}

type token struct {
	kind int
	text string
}

type lexerErrors struct {
	*antlr.DefaultErrorListener
	failed bool
}

func (e *lexerErrors) SyntaxError(antlr.Recognizer, interface{}, int, int, string, antlr.RecognitionException) {
	e.failed = true
}

func significantTokens(expression string) ([]token, bool) {
	if len(expression) > maxExpressionBytes || !utf8.ValidString(expression) {
		return nil, false
	}
	lexer := gen.NewCELLexer(antlr.NewInputStream(expression))
	errors := &lexerErrors{DefaultErrorListener: antlr.NewDefaultErrorListener()}
	lexer.RemoveErrorListeners()
	lexer.AddErrorListener(errors)
	var tokens []token
	for {
		t := lexer.NextToken()
		if t.GetTokenType() == antlr.TokenEOF {
			break
		}
		// COMMENT is hidden-channel too, but its complete original bytes stay.
		// Full grammar lexer errors make skipped/unrecognized input invalid.
		if t.GetTokenType() != gen.CELLexerWHITESPACE {
			tokens = append(tokens, token{t.GetTokenType(), t.GetText()})
		}
		if len(tokens) > 8192 {
			return nil, false
		}
	}
	return tokens, !errors.failed
}

func compare(actual, expected string) (out reply) {
	out = reply{Profile: profile, Code: "InvalidCEL"}
	defer func() {
		if recover() != nil {
			out = reply{Profile: profile, Code: "CELFailure"}
		}
	}()
	a, validA := significantTokens(actual)
	e, validE := significantTokens(expected)
	if !validA || !validE {
		return out
	}
	p, err := parser.NewParser(parser.EnableOptionalSyntax(true), parser.MaxRecursionDepth(100),
		parser.MaxExpressionNodeCount(8192), parser.ExpressionSizeCodePointLimit(maxExpressionBytes),
		parser.ErrorReportingLimit(1), parser.ErrorRecoveryLimit(1), parser.ErrorRecoveryLookaheadTokenLimit(16))
	if err != nil {
		out.Code = "CELFailure"
		return out
	}
	_, actualErrors := p.Parse(common.NewTextSource(actual))
	_, expectedErrors := p.Parse(common.NewTextSource(expected))
	if len(actualErrors.GetErrors()) != 0 || len(expectedErrors.GetErrors()) != 0 {
		return out
	}
	equal := slices.Equal(a, e)
	return reply{OK: true, Equal: &equal, Profile: profile}
}

func serve(input io.Reader, output io.Writer) bool {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), maxInputLine+1)
	encoder := json.NewEncoder(output)
	started := time.Now()
	total := 0
	count := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		count++
		total += len(line)
		if count > maxRequests || total > maxInputBytes || len(line) > maxInputLine || time.Since(started) > 120*time.Second {
			_ = encoder.Encode(reply{Profile: profile, Code: "CELBudgetExceeded"})
			return false
		}
		var req request
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&req)
		var extra any
		if !utf8.Valid(line) || decodeErr != nil || decoder.Decode(&extra) != io.EOF {
			_ = encoder.Encode(reply{Profile: profile, Code: "MalformedCELRequest"})
			return false
		}
		var out reply
		switch req.Op {
		case "hello":
			if req.Actual != "" || req.Expected != "" {
				out = reply{Profile: profile, Code: "MalformedCELRequest"}
			} else {
				out = reply{OK: true, Profile: profile}
			}
		case "compare":
			out = compare(req.Actual, req.Expected)
		default:
			out = reply{Profile: profile, Code: "MalformedCELRequest"}
		}
		if encoder.Encode(out) != nil || !out.OK {
			return false
		}
	}
	if scanner.Err() != nil {
		_ = encoder.Encode(reply{Profile: profile, Code: "CELInputRefused"})
		return false
	}
	return true
}

func main() {
	if !serve(os.Stdin, os.Stdout) {
		os.Exit(1)
	}
}
