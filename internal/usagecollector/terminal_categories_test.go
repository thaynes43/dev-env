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
		{"builtin_terminal_letter_unqualified", []string{"é", "Ж", "日"}},
		{"builtin_terminal_number_unqualified", []string{"²", "½", "٣"}},
		{"builtin_terminal_dash_unqualified", []string{"—", "–", "‐"}},
		{"builtin_terminal_quote_unqualified", []string{"‘", "’", "“", "”"}},
		{"builtin_terminal_punct_unqualified", []string{"¿", "¡", "§", "、"}},
		{"builtin_terminal_math_unqualified", []string{"×", "±", "≠"}},
		{"builtin_terminal_currency_unqualified", []string{"€", "£", "¥"}},
		{"builtin_terminal_shape_unqualified", []string{"■", "▲", "●", "\u25a0", "\u25b7", "\u25ff"}},
		{"builtin_terminal_pictograph_unqualified", []string{"🔥", "\U0001f000", "\U0001f600", "\U0001f9ff"}},
		{"builtin_terminal_checkmark_unqualified", []string{"\u2714", "\u2715", "\u2717", "\u2718", "\u2705", "\u274c", "\u274e"}},
		{"builtin_terminal_star_unqualified", []string{"★", "☆", "\u2721", "\u2722", "\u2733", "\u273b", "\u273d", "\u274b"}},
		{"builtin_terminal_miscsymbol_unqualified", []string{"⚠", "\u2600", "\u2603", "\u26a1", "\u26ff"}},
		{"builtin_terminal_dingbat_unqualified", []string{"\u2700", "\u2719", "\u271d", "\u274f", "\u2794", "\u27a4", "\u27bf"}},
		{"builtin_terminal_c1_unqualified", []string{"\u0080", "\u009b", "\u009f"}},
		{"builtin_terminal_symbol_unqualified", []string{"©", "°", "™"}},
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

// Private-use and unassigned code points belong to no named class and keep the
// generic reason. Like every refusal it acts on nothing and discloses nothing.
func TestCursorGenericRuneReasonKeepsOnlyUnclassifiedRunes(t *testing.T) {
	for _, sample := range []string{"\ue000", "\U000f0000", "\u0378", "\ufffe", "\uffff", "\U0010ffff"} {
		p := cursorProtocol{}
		input, capture, err := p.accept([]byte(sample + "PRIVATE-SENTINEL"))
		if input != "" || capture != nil {
			t.Fatal("refusal acted")
		}
		requireBuiltinReason(t, "cursor", err, "builtin_terminal_rune_unqualified")
		var out bytes.Buffer
		emitAcquisitionDiagnostic(&out, "cursor", atAcquisitionStage("builtin", err))
		if bytes.Contains(out.Bytes(), []byte(sample)) || bytes.Contains(out.Bytes(), []byte("PRIVATE-SENTINEL")) {
			t.Fatal("generic reason disclosed input")
		}
	}
}
