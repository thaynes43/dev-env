package usagecollector

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const cursorRows, cursorCols = 50, 180

// One fixed public multi-member vocabulary for refusal diagnostics only.
// Membership never makes a rune eligible for rendering or input/capture.
const cursorDotSpinnerDiagnosticSet = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

// cursorTerminal is the bounded visible screen, not a transcript accumulator.
// Unsupported terminal operations refuse instead of leaving erased text eligible
// to authorize input. Partial UTF8/escape and synchronized redraws cannot settle.
type cursorTerminal struct {
	cells                      [cursorRows][cursorCols]rune
	row, col, saveRow, saveCol int
	pending                    []byte
	synchronized               bool
}

func (t *cursorTerminal) feed(raw []byte) error {
	t.pending = append(t.pending, raw...)
	if len(t.pending) > 8<<10 {
		return refused("builtin_terminal_bounds_unqualified", ErrCapture)
	}
	i := 0
	for i < len(t.pending) {
		b := t.pending[i]
		if b == 0x1b {
			if i+1 >= len(t.pending) {
				break
			}
			if t.pending[i+1] == ']' {
				end := i + 2
				for end < len(t.pending) && t.pending[end] != 7 && (t.pending[end] != 0x1b || end+1 >= len(t.pending) || t.pending[end+1] != '\\') {
					end++
				}
				if end >= len(t.pending) {
					break
				}
				if t.pending[end] == 0x1b {
					end++
				}
				i = end + 1
				continue
			}
			if t.pending[i+1] != '[' {
				return refused("builtin_terminal_escape_unqualified", ErrCapture)
			}
			end := i + 2
			for end < len(t.pending) && t.pending[end] >= 0x20 && t.pending[end] <= 0x3f {
				end++
			}
			if end >= len(t.pending) {
				break
			}
			if end-i > 64 || t.pending[end] < 0x40 || t.pending[end] > 0x7e {
				return refused("builtin_terminal_escape_unqualified", ErrCapture)
			}
			if err := t.control(string(t.pending[i+2:end]), t.pending[end]); err != nil {
				return err
			}
			i = end + 1
			continue
		}
		switch b {
		case '\r':
			t.col = 0
		case '\n':
			t.row++
			if t.row == cursorRows {
				copy(t.cells[:], t.cells[1:])
				t.cells[cursorRows-1] = [cursorCols]rune{}
				t.row--
			}
		case '\t':
			t.col = min(cursorCols-1, (t.col/8+1)*8)
		case '\b':
			t.col = max(0, t.col-1)
		default:
			if b < 0x20 || b == 0x7f {
				return refused("builtin_terminal_control_unqualified", ErrCapture)
			}
			if !utf8.FullRune(t.pending[i:]) {
				t.pending = append([]byte(nil), t.pending[i:]...)
				return nil
			}
			r, size := utf8.DecodeRune(t.pending[i:])
			qualified := r < 128 || r == '•' || r == '·' || r == '…' || r >= 0x2190 && r <= 0x21ff || r >= 0x2500 && r <= 0x259f || r == '✓'
			if r == utf8.RuneError || !qualified {
				var reason string
				switch {
				case r == utf8.RuneError:
					reason = "builtin_terminal_encoding_unqualified"
				case unicode.IsMark(r):
					reason = "builtin_terminal_combining_unqualified"
				case unicode.IsSpace(r):
					reason = "builtin_terminal_spacing_unqualified"
				case unicode.Is(unicode.Cf, r):
					reason = "builtin_terminal_format_unqualified"
				case strings.ContainsRune(cursorDotSpinnerDiagnosticSet, r):
					reason = "builtin_terminal_dotset_unqualified"
				default:
					reason = cursorRuneClassReason(r)
				}
				return refused(reason, ErrCapture)
			}
			if t.col >= cursorCols {
				return refused("builtin_terminal_bounds_unqualified", ErrCapture)
			}
			t.cells[t.row][t.col] = r
			t.col++
			i += size
			continue
		}
		i++
	}
	t.pending = append([]byte(nil), t.pending[i:]...)
	return nil
}

func (t *cursorTerminal) control(raw string, final byte) error {
	if final == 'h' || final == 'l' {
		switch raw {
		case "?25", "?2004": // Cursor visibility and bracketed-paste mode.
			return nil
		case "?2026":
			t.synchronized = final == 'h'
			return nil
		default:
			return refused("builtin_terminal_mode_unqualified", ErrCapture)
		}
	}
	var params []int
	if raw != "" {
		for _, value := range strings.Split(raw, ";") {
			if value == "" {
				params = append(params, 0)
				continue
			}
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || n > 10000 {
				return refused("builtin_terminal_params_unqualified", ErrCapture)
			}
			params = append(params, n)
		}
	}
	if final == 'm' { // Graphic rendition changes no visible characters.
		return nil
	}
	first := 1
	if len(params) > 0 && params[0] != 0 {
		first = params[0]
	}
	if len(params) > 2 || len(params) > 1 && final != 'H' && final != 'f' {
		return refused("builtin_terminal_params_unqualified", ErrCapture)
	}
	switch final {
	case 'A':
		t.row = max(0, t.row-first)
	case 'B':
		t.row = min(cursorRows-1, t.row+first)
	case 'C':
		t.col = min(cursorCols-1, t.col+first)
	case 'D':
		t.col = max(0, t.col-first)
	case 'E':
		t.row, t.col = min(cursorRows-1, t.row+first), 0
	case 'F':
		t.row, t.col = max(0, t.row-first), 0
	case 'G':
		t.col = min(cursorCols-1, first-1)
	case 'H', 'f':
		t.row, t.col = min(cursorRows-1, first-1), 0
		if len(params) == 2 && params[1] > 0 {
			t.col = min(cursorCols-1, params[1]-1)
		}
	case 'J', 'K':
		mode := 0
		if len(params) > 0 {
			mode = params[0]
		}
		if mode > 2 {
			return refused("builtin_terminal_params_unqualified", ErrCapture)
		}
		for row := range t.cells {
			if final == 'K' && row != t.row {
				continue
			}
			for col := range t.cells[row] {
				if mode == 2 || mode == 0 && (row > t.row || row == t.row && col >= t.col) || mode == 1 && (row < t.row || row == t.row && col <= t.col) {
					t.cells[row][col] = 0
				}
			}
		}
	case 's':
		t.saveRow, t.saveCol = t.row, t.col
	case 'u':
		t.row, t.col = t.saveRow, t.saveCol
	default:
		return refused("builtin_terminal_operation_unqualified", ErrCapture)
	}
	return nil
}

func (t *cursorTerminal) visible() (string, bool) {
	if len(t.pending) != 0 || t.synchronized || t.col >= cursorCols {
		return "", false
	}
	var lines []string
	for _, row := range t.cells {
		var line strings.Builder
		for _, r := range row {
			if r == 0 {
				r = ' '
			}
			line.WriteRune(r)
		}
		lines = append(lines, strings.TrimRight(line.String(), " "))
	}
	return strings.Join(lines, "\n"), true
}

// cursorRuneClassReason names the Unicode class of a refused character with one
// closed label per class. Many characters share each label, so the label cannot
// identify which character was seen; the character, its code point and its
// count are never emitted. Private-use, unassigned and noncharacter code points
// keep the generic reason.
func cursorRuneClassReason(r rune) string {
	switch {
	case unicode.IsLetter(r):
		return "builtin_terminal_letter_unqualified"
	case unicode.IsNumber(r):
		return "builtin_terminal_number_unqualified"
	case unicode.Is(unicode.Pd, r):
		return "builtin_terminal_dash_unqualified"
	case unicode.Is(unicode.Pi, r) || unicode.Is(unicode.Pf, r):
		return "builtin_terminal_quote_unqualified"
	case unicode.IsPunct(r):
		return "builtin_terminal_punct_unqualified"
	case unicode.Is(unicode.Cc, r):
		// C1 controls arrive as UTF-8 runes; C0 and DEL are refused earlier as control bytes.
		return "builtin_terminal_c1_unqualified"
	case unicode.IsSymbol(r) && r >= 0x25a0 && r <= 0x25ff:
		return "builtin_terminal_shape_unqualified"
	case unicode.Is(unicode.Sm, r):
		return "builtin_terminal_math_unqualified"
	case unicode.Is(unicode.Sc, r):
		return "builtin_terminal_currency_unqualified"
	case unicode.IsSymbol(r) && (r >= 0x2714 && r <= 0x2718 || r == 0x2705 || r == 0x274c || r == 0x274e):
		return "builtin_terminal_checkmark_unqualified"
	case unicode.IsSymbol(r) && (r == 0x2605 || r == 0x2606 || r >= 0x2721 && r <= 0x274b):
		return "builtin_terminal_star_unqualified"
	case unicode.IsSymbol(r) && r >= 0x2600 && r <= 0x26ff:
		// Miscellaneous Symbols, in eight aligned windows of 32 code points. A label names
		// the window, never the character; the table in the verification note maps them.
		return cursorMiscWindows[(r-0x2600)>>5]
	case unicode.IsSymbol(r) && r >= 0x2700 && r <= 0x27bf:
		return "builtin_terminal_dingbat_unqualified"
	case unicode.IsSymbol(r) && r >= 0x1f000:
		return "builtin_terminal_pictograph_unqualified"
	case unicode.IsSymbol(r):
		return "builtin_terminal_symbol_unqualified"
	}
	return "builtin_terminal_rune_unqualified"
}

// cursorMiscWindows labels U+2600 to U+26FF in eight windows of 32 code points each.
var cursorMiscWindows = [8]string{
	"builtin_terminal_miscwin1_unqualified", "builtin_terminal_miscwin2_unqualified",
	"builtin_terminal_miscwin3_unqualified", "builtin_terminal_miscwin4_unqualified",
	"builtin_terminal_miscwin5_unqualified", "builtin_terminal_miscwin6_unqualified",
	"builtin_terminal_miscwin7_unqualified", "builtin_terminal_miscwin8_unqualified",
}
