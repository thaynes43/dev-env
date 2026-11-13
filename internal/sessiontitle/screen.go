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

//go:embed testdata/busy-thinking-empty-input.2.1.321.ansi
var BusyThinkingEmptyScreen string

//go:embed testdata/busy-thinking-typed-rename-zz.2.1.321.ansi
var BusyThinkingTypedScreen string

//go:embed testdata/busy-streaming-no-spinner-empty-input.2.1.321.ansi
var BusyStreamingScreen string

//go:embed testdata/busy-spinner-U273B-empty-input.2.1.321.ansi
var BusySpinner273BScreen string

//go:embed testdata/busy-spinner-U2722-empty-input.2.1.321.ansi
var BusySpinner2722Screen string

//go:embed testdata/busy-spinner-U273D-empty-input.2.1.321.ansi
var BusySpinner273DScreen string

//go:embed testdata/busy-spinner-U2736-empty-input.2.1.321.ansi
var BusySpinner2736Screen string

//go:embed testdata/busy-spinner-U00B7-empty-input.2.1.321.ansi
var BusySpinner00B7Screen string

//go:embed testdata/busy-spinner-U002A-empty-input.2.1.321.ansi
var BusySpinner002AScreen string

//go:embed testdata/cursor-and-format-facts.2.1.321.txt
var cursorFacts string

//go:embed testdata/tmux-facts.txt
var tmuxFacts string

// listPanesFormat is the list-panes format recorded in
// cursor-and-format-facts.2.1.321.txt. It is a constant so a test can compare
// it with that recording; it is not read back out of the file it is checked
// against.
const listPanesFormat = "#{window_index} #{pane_index} #{pane_id} #{pane_dead} #{pane_in_mode} #{pane_synchronized} #{pane_pid} #{pane_current_command}"

// ListPanesFormat is the tmux 3.3a list-panes format. The recorded line was
// produced with pane_synchronized.
func ListPanesFormat() string {
	return listPanesFormat
}

// CursorFactsText is the coordinator's cursor and format recording.
func CursorFactsText() string {
	return cursorFacts
}

// BusySpinnerScreens are the six recorded spinner frames, each an empty input.
func BusySpinnerScreens() map[string]string {
	return map[string]string{
		"U+273B": BusySpinner273BScreen,
		"U+2722": BusySpinner2722Screen,
		"U+273D": BusySpinner273DScreen,
		"U+2736": BusySpinner2736Screen,
		"U+00B7": BusySpinner00B7Screen,
		"U+002A": BusySpinner002AScreen,
	}
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
// and an exact match both require the cursor. A spinner row makes the screen
// unknown. Anything else is unknown, except other non-blank text in the one
// well-formed input row.
func ClassifyInput(screen, exact string, cur Cursor) InputKind {
	return classifyInput(screen, exact, cur, true)
}

// ClassifyBox answers the same question as ClassifyInput and does not treat a
// spinner row as disqualifying. A question, a selection or a permission
// screen has no input box and stays unknown. Cleanup and delivery use this.
func ClassifyBox(screen, exact string, cur Cursor) InputKind {
	return classifyInput(screen, exact, cur, false)
}

func classifyInput(screen, exact string, cur Cursor, spinnersHold bool) InputKind {
	lines := parseScreen(screen)
	row, ok := findInputRow(lines, spinnersHold)
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

// RecordedCursor is the cursor the coordinator measured for a recorded
// screen. Derived screens are not in the set. Empty-input frames from the
// same session use column 2 on row 46. The typed "/rename zz" frames use
// column 12 on row 46.
func RecordedCursor(screen string) (Cursor, bool) {
	switch screen {
	case IdleEmptyScreen, BusyThinkingEmptyScreen, BusyStreamingScreen,
		BusySpinner273BScreen, BusySpinner2722Screen, BusySpinner273DScreen,
		BusySpinner2736Screen, BusySpinner00B7Screen, BusySpinner002AScreen:
		return Cursor{X: 2, Y: 46, Known: true}, true
	case TypedRenameScreen, BusyThinkingTypedScreen:
		return Cursor{X: 12, Y: 46, Known: true}, true
	default:
		return Cursor{}, false
	}
}

// ObservedCursor is the cursor a derived box would have: on the input row,
// just after the prompt mark when the row is empty or a dim placeholder, and
// just after any other text. A screen with no such box is not known.
func ObservedCursor(screen string) Cursor {
	lines := parseScreen(screen)
	row, ok := findInputRow(lines, true)
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
func findInputRow(lines [][]cell, spinnersHold bool) (int, bool) {
	for _, line := range lines {
		if disqualifyingRow(line, spinnersHold) {
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

func disqualifyingRow(line []cell, spinnersHold bool) bool {
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
	return spinnersHold && spinnerRow(text)
}

// spinnerRow matches the recorded shape: a mark, one space, and a word that
// ends in "…". A finished row such as "✻ Crunched for 15s · done 8:31 AM"
// does not match, because its first word does not end in "…".
func spinnerRow(text string) bool {
	t := strings.TrimLeft(text, " \t")
	r, size := utf8.DecodeRuneInString(t)
	if size == 0 || !spinnerMark(r) {
		return false
	}
	rest := t[size:]
	if !strings.HasPrefix(rest, " ") {
		return false
	}
	rest = rest[1:]
	end := len(rest)
	for i, rr := range rest {
		if rr == ' ' || rr == '\t' {
			end = i
			break
		}
	}
	word := rest[:end]
	return word != "" && word != "…" && strings.HasSuffix(word, "…")
}

func spinnerMark(r rune) bool {
	switch r {
	case '\u273B', '\u2722', '\u273D', '\u2736', '\u00B7', '\u002A', '\u2733', '\u273A':
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
