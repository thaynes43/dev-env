package sessiontitle

import (
	_ "embed"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Screens recorded from Claude Code 2.1.321 (DESIGN-065 rule 8.11). They are
// capture-pane -e output. QuestionOpenScreen is the recorded AskUserQuestion
// screen: it has no input box, and it must stay unknown.
//
//go:embed testdata/idle-empty-input.2.1.321.ansi
var IdleEmptyScreen string

//go:embed testdata/idle-placeholder.2.1.321.ansi
var IdlePlaceholderScreen string

//go:embed testdata/typed-rename-zz.2.1.321.ansi
var TypedRenameScreen string

//go:embed testdata/question-open.2.1.321.ansi
var QuestionOpenScreen string

//go:embed testdata/tmux-facts.txt
var tmuxFacts string

// ListPanesFormat is the tmux 3.3a list-panes format in testdata/tmux-facts.txt.
// The recorded line was produced with pane_synchronized.
func ListPanesFormat() string {
	lines := tmuxFactLines()
	if len(lines) < 4 {
		return ""
	}
	return lines[3]
}

// RecordedPaneLine is the list-panes line recorded beside that format.
func RecordedPaneLine() string {
	lines := tmuxFactLines()
	if len(lines) < 2 {
		return ""
	}
	return lines[1]
}

func tmuxFactLines() []string {
	return strings.Split(strings.TrimRight(tmuxFacts, "\n"), "\n")
}

// InputKind is what ClassifyInput can say about one capture-pane -e screen.
type InputKind int

const (
	// InputUnknown is a screen that is not one idle input box: a question, a
	// permission prompt, a selection list, a spinner, more than one input row,
	// or a cursor that is not where that box's text ends.
	InputUnknown InputKind = iota
	// InputEmpty is the one idle box with nothing in the input row, or with
	// the recorded dim placeholder and nothing else, and the cursor just after
	// the prompt mark.
	InputEmpty
	// InputExact is that box whose input row holds exactly the text asked
	// about, with the cursor directly after it.
	InputExact
	// InputOther is that box with other non-blank text in the input row.
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

// Cursor is a pane cursor from tmux display-message. Known is false when that
// read failed or was not supplied; ClassifyInput then answers unknown.
type Cursor struct {
	X, Y  int
	Known bool
}

// ClassifyInput reads the whole visible screen. exact is the input text a
// second check wants; an empty exact never returns InputExact. Empty input
// and an exact match both require the cursor. Anything else is unknown,
// except other non-blank text in the one well-formed input row.
func ClassifyInput(screen, exact string, cur Cursor) InputKind {
	lines := parseScreen(screen)
	row, ok := findInputRow(lines)
	if !ok || !cur.Known {
		return InputUnknown
	}
	text, dim, good := inputText(lines[row])
	if !good {
		return InputUnknown
	}
	const afterPrompt = 2
	if text == "" || (text == placeholderText && dim) {
		if cur.Y == row && cur.X == afterPrompt {
			return InputEmpty
		}
		return InputUnknown
	}
	if strings.TrimSpace(text) == "" {
		return InputUnknown
	}
	end := afterPrompt + utf8.RuneCountInString(text)
	if exact != "" && text == exact {
		if cur.Y == row && cur.X == end {
			return InputExact
		}
		return InputUnknown
	}
	if cur.Y == row && cur.X >= afterPrompt {
		return InputOther
	}
	return InputUnknown
}

// ObservedCursor is the cursor a recorded box would have: on the input row,
// just after the prompt mark when the row is empty or a dim placeholder, and
// just after any other text. A screen with no such box is not known.
func ObservedCursor(screen string) Cursor {
	lines := parseScreen(screen)
	row, ok := findInputRow(lines)
	if !ok {
		return Cursor{}
	}
	text, dim, good := inputText(lines[row])
	if !good {
		return Cursor{}
	}
	x := 2
	if text != "" && (text != placeholderText || !dim) && strings.TrimSpace(text) != "" {
		x += utf8.RuneCountInString(text)
	}
	return Cursor{X: x, Y: row, Known: true}
}

// findInputRow reports the input row when the screen is exactly one input box
// and that box is the last element before the status row. A selection hint, a
// numbered option or a spinner anywhere on the screen is not that box.
func findInputRow(lines [][]cell) (int, bool) {
	for _, line := range lines {
		if disqualifyingRow(line) {
			return 0, false
		}
	}
	var content []int
	for i, line := range lines {
		if !blankCells(line) {
			content = append(content, i)
		}
	}
	footers := 0
	for i := len(content) - 1; i >= 0; i-- {
		if !footerOK(lines[content[i]]) {
			break
		}
		footers++
	}
	if footers < 1 {
		return 0, false
	}
	rest := content[:len(content)-footers]
	if len(rest) < 3 {
		return 0, false
	}
	bottom := rest[len(rest)-1]
	input := rest[len(rest)-2]
	top := rest[len(rest)-3]
	if bottom != input+1 || input != top+1 {
		return 0, false
	}
	if !dashRule(lines[bottom]) || !isPromptRow(lines[input]) || !topRule(lines[top]) {
		return 0, false
	}
	n := 0
	for _, line := range lines {
		if isPromptRow(line) {
			n++
		}
	}
	if n != 1 {
		return 0, false
	}
	return input, true
}

func isPromptRow(line []cell) bool {
	return len(line) >= 2 && line[0].r == '❯' && line[1].r == '\u00a0'
}

var numberedOption = regexp.MustCompile(`^(?:❯[ \t]+)?\d+\.\s+\S`)

func disqualifyingRow(line []cell) bool {
	text := strings.TrimSpace(rowText(line))
	if text == "" {
		return false
	}
	low := strings.ToLower(text)
	if strings.Contains(low, "enter to select") || strings.Contains(low, "esc to cancel") || strings.Contains(low, "esc to interrupt") {
		return true
	}
	if numberedOption.MatchString(text) {
		return true
	}
	return spinnerRow(text)
}

func spinnerRow(text string) bool {
	low := strings.ToLower(text)
	if strings.Contains(low, "done") || strings.Contains(low, "crunched") {
		return false
	}
	t := strings.TrimLeft(text, " \t")
	if t == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(t)
	switch r {
	case '✻', '✽', '✶', '✢', '✳', '✺':
		return true
	default:
		return false
	}
}

func rowText(line []cell) string {
	var b strings.Builder
	for _, c := range line {
		b.WriteRune(c.r)
	}
	return b.String()
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
	if !isPromptRow(line) {
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
