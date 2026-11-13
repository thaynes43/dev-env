package sessiontitle

import (
	_ "embed"
	"strings"
	"unicode/utf8"
)

// Screens recorded from Claude Code 2.1.321 (DESIGN-065 rule 8.11). They are
// the only captures ClassifyInput treats as known.
//
//go:embed testdata/idle-empty-input.2.1.321.ansi
var IdleEmptyScreen string

//go:embed testdata/idle-placeholder.2.1.321.ansi
var IdlePlaceholderScreen string

//go:embed testdata/typed-rename-zz.2.1.321.ansi
var TypedRenameScreen string

// InputKind is what ClassifyInput can say about one capture-pane -e screen.
type InputKind int

const (
	// InputUnknown is a screen this build has not recorded: a question, a
	// permission prompt, a capture it cannot parse, or chrome it does not know.
	InputUnknown InputKind = iota
	// InputEmpty is the recorded idle box with nothing in the input row, or
	// with the recorded dim placeholder and nothing else.
	InputEmpty
	// InputExact is that box whose input row holds exactly the text asked about.
	InputExact
	// InputOther is that box with any other text in the input row.
	InputOther
)

// placeholderText is the dim hint on the recorded idle screen. Any other dim
// text is text in the input, not an empty line.
const placeholderText = `Try "write a test for <filepath>"`

const (
	footerBypass = "⏵⏵ bypass permissions on"
	footerCycle  = "(shift+tab to cycle)"
	fgRule       = 244
	fgBypass     = 211
	fgCycle      = 246
)

// ClassifyInput reads a capture-pane -e screen. exact is the input text a
// second check wants; an empty exact never returns InputExact. Anything that
// is not the recorded idle box is InputUnknown.
func ClassifyInput(screen, exact string) InputKind {
	lines := parseScreen(screen)
	var idx []int
	for i, line := range lines {
		if !blankCells(line) {
			idx = append(idx, i)
		}
	}
	if len(idx) < 4 {
		return InputUnknown
	}
	top := lines[idx[len(idx)-4]]
	input := lines[idx[len(idx)-3]]
	bottom := lines[idx[len(idx)-2]]
	footer := lines[idx[len(idx)-1]]
	if !footerOK(footer) || !dashRule(bottom) || !topRule(top) {
		return InputUnknown
	}
	text, dim, ok := inputText(input)
	if !ok {
		return InputUnknown
	}
	if text == "" || (text == placeholderText && dim) {
		return InputEmpty
	}
	if exact != "" && text == exact {
		return InputExact
	}
	return InputOther
}

type cell struct {
	r   rune
	fg  int
	dim bool
}

func parseScreen(screen string) [][]cell {
	screen = strings.ReplaceAll(screen, "\r\n", "\n")
	screen = strings.TrimSuffix(screen, "\n")
	raw := strings.Split(screen, "\n")
	out := make([][]cell, len(raw))
	for i, line := range raw {
		out[i] = parseCells(line)
	}
	return out
}

func parseCells(line string) []cell {
	fg := -1
	dim := false
	var cells []cell
	for i := 0; i < len(line); {
		if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
			j := i + 2
			for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) {
				j++
			}
			if j >= len(line) {
				break
			}
			if line[j] == 'm' {
				applySGR(line[i+2:j], &fg, &dim)
			}
			i = j + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		cells = append(cells, cell{r: r, fg: fg, dim: dim})
		i += size
	}
	return cells
}

func applySGR(params string, fg *int, dim *bool) {
	if params == "" {
		*fg = -1
		*dim = false
		return
	}
	parts := strings.Split(params, ";")
	nums := make([]int, len(parts))
	for i, p := range parts {
		if p == "" {
			continue
		}
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				n = -1
				break
			}
			n = n*10 + int(c-'0')
		}
		nums[i] = n
	}
	for i := 0; i < len(nums); i++ {
		switch nums[i] {
		case 0:
			*fg = -1
			*dim = false
		case 2:
			*dim = true
		case 22:
			*dim = false
		case 39:
			*fg = -1
		case 49:
		case 38:
			if i+2 < len(nums) && nums[i+1] == 5 && nums[i+2] >= 0 {
				*fg = nums[i+2]
				i += 2
			} else {
				*fg = -2
			}
		}
	}
}

func blankCells(line []cell) bool {
	for _, c := range line {
		if c.r != ' ' {
			return false
		}
	}
	return true
}

func dashRule(line []cell) bool {
	if len(line) == 0 {
		return false
	}
	for _, c := range line {
		if c.r != '─' || c.fg != fgRule {
			return false
		}
	}
	return true
}

func topRule(line []cell) bool {
	if len(line) == 0 {
		return false
	}
	dash := false
	for _, c := range line {
		if c.fg != fgRule {
			return false
		}
		if c.r == '─' {
			dash = true
		}
	}
	return dash
}

func footerOK(line []cell) bool {
	return phraseFG(line, footerBypass, fgBypass) && phraseFG(line, footerCycle, fgCycle)
}

func phraseFG(line []cell, phrase string, fg int) bool {
	want := []rune(phrase)
	if len(want) == 0 || len(line) < len(want) {
		return false
	}
	for i := 0; i+len(want) <= len(line); i++ {
		match := true
		for j, r := range want {
			if line[i+j].r != r || line[i+j].fg != fg {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func inputText(line []cell) (string, bool, bool) {
	if len(line) < 2 || line[0].r != '❯' || line[1].r != '\u00a0' {
		return "", false, false
	}
	var b strings.Builder
	dim := true
	for _, c := range line[2:] {
		b.WriteRune(c.r)
		if !c.dim {
			dim = false
		}
	}
	if b.Len() == 0 {
		dim = false
	}
	return b.String(), dim, true
}
