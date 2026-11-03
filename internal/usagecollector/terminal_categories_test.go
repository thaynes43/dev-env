//go:build linux

package usagecollector

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCursorRefusedStandardCategoriesArePrivateAndNeverAct(t *testing.T) {
	for _, group := range []struct {
		reason  string
		samples []string
	}{
		{"builtin_terminal_encoding_unqualified", []string{"\xff", "\uFFFD", "\xc0\xaf"}},
		{"builtin_terminal_combining_unqualified", []string{"\u0301", "\u20dd"}},
		{"builtin_terminal_spacing_unqualified", []string{"\u00a0", "\u202f", "\u2003"}},
		{"builtin_terminal_format_unqualified", []string{"\u202e", "\u200d"}},
	} {
		var first []byte
		for _, sample := range group.samples {
			for state := 0; state <= 4; state++ {
				p := cursorProtocol{state: state}
				input, capture, err := p.accept([]byte(sample + "PRIVATE-SENTINEL"))
				if input != "" || capture != nil || p.state != state {
					t.Fatal("refusal acted")
				}
				requireBuiltinReason(t, "cursor", err, group.reason)
				var out bytes.Buffer
				emitAcquisitionDiagnostic(&out, "cursor", atAcquisitionStage("builtin", err))
				var fields map[string]json.RawMessage
				if json.Unmarshal(out.Bytes(), &fields) != nil || len(fields) != 5 || bytes.Contains(out.Bytes(), []byte(sample)) || bytes.Contains(out.Bytes(), []byte("PRIVATE-SENTINEL")) {
					t.Fatal("category disclosed input or changed fields")
				}
				if first == nil {
					first = append([]byte(nil), out.Bytes()...)
				} else if !bytes.Equal(first, out.Bytes()) {
					t.Fatal("category distinguished its members")
				}
			}
		}
		d := AcquisitionDiagnostic{diagnosticSchema, "cursor", "builtin", group.reason, ""}
		if !d.valid() {
			t.Fatal("closed category refused")
		}
		for _, provider := range []string{"anthropic", "openai"} {
			d.Provider = provider
			if d.valid() {
				t.Fatal("category escaped provider boundary")
			}
		}
		d.Provider = "cursor"
		d.Stage = "identity_before"
		if d.valid() {
			t.Fatal("category escaped stage")
		}
		d.Stage = "builtin"
		d.ClientVersion = "2.1.296"
		if d.valid() {
			t.Fatal("category gained payload")
		}
	}
}

func TestCursorOwnedProcessEncodingRefusalIsPrivate(t *testing.T) {
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	if os.Mkdir(workspace, 0700) != nil {
		t.Fatal("workspace")
	}
	executable := filepath.Join(dir, "inert-cursor")
	if os.WriteFile(executable, []byte("#!/bin/sh\nprintf '\\377PRIVATE-SENTINEL'\n/bin/sleep 60\n"), 0700) != nil {
		t.Fatal("fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := CollectCursor(ctx, CursorOptions{Executable: executable, Workspace: workspace}, time.Now)
	requireBuiltinReason(t, "cursor", err, "builtin_terminal_encoding_unqualified")
}
func TestCursorCategoryUTF8FragmentsNeverSettleEarly(t *testing.T) {
	for _, sample := range []string{"\uFFFD", "\u0301", "\u202f", "\u202e"} {
		p := cursorProtocol{}
		raw := []byte(sample)
		for i, b := range raw {
			input, capture, err := p.accept([]byte{b})
			if input != "" || capture != nil {
				t.Fatal("fragment acted")
			}
			if i < len(raw)-1 {
				if err != nil {
					t.Fatal("classified partial encoding")
				}
				if input, capture, _ = p.settle(); input != "" || capture != nil {
					t.Fatal("partial rune settled")
				}
			} else if err == nil {
				t.Fatal("complete unsupported rune accepted")
			}
		}
	}
}
