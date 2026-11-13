package sessiontitle

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestClassifyRecordedFixtures(t *testing.T) {
	empty, ok := RecordedCursor(IdleEmptyScreen)
	if !ok || ClassifyInput(IdleEmptyScreen, "", empty) != InputEmpty {
		t.Fatal("empty input")
	}
	if ClassifyInput(IdleEmptyScreen, "/rename zz", empty) != InputEmpty {
		t.Fatal("empty input is not the typed text")
	}
	// IdlePlaceholderScreen has no recorded cursor. ObservedCursor is used
	// because the placeholder row is a derived reading of that fixture.
	if ClassifyInput(IdlePlaceholderScreen, "", ObservedCursor(IdlePlaceholderScreen)) != InputEmpty {
		t.Fatal("dim placeholder")
	}
	typed, ok := RecordedCursor(TypedRenameScreen)
	if !ok || ClassifyInput(TypedRenameScreen, "/rename zz", typed) != InputExact {
		t.Fatal("typed /rename zz")
	}
	if ClassifyInput(TypedRenameScreen, "/rename zz ", typed) != InputOther {
		t.Fatal("typed text that is not the exact string")
	}
	if ClassifyInput(TypedRenameScreen, "", typed) != InputOther {
		t.Fatal("typed text is not an empty input")
	}
	if !empty.Known || empty.X != 2 {
		t.Fatalf("empty cursor %+v", empty)
	}
}

func TestClassifyQuestionOpenIsUnknown(t *testing.T) {
	// question-open.2.1.321.ansi is the recorded AskUserQuestion screen.
	// It has no recorded cursor. Column 2 on row 46 is the empty-input cursor,
	// so the unknown result is the screen.
	if got := ClassifyInput(QuestionOpenScreen, "", Cursor{X: 2, Y: 46, Known: true}); got != InputUnknown {
		t.Fatalf("recorded question classified %d", got)
	}
	if got := ClassifyInput(QuestionOpenScreen, "", ObservedCursor(QuestionOpenScreen)); got != InputUnknown {
		t.Fatalf("recorded question with its own cursor classified %d", got)
	}
	if ObservedCursor(QuestionOpenScreen).Known {
		t.Fatal("the recorded question has no idle input box")
	}
	if got := ClassifyBox(QuestionOpenScreen, "", Cursor{X: 2, Y: 46, Known: true}); got != InputUnknown {
		t.Fatalf("question box classified %d", got)
	}
}

func TestClassifyInputOtherAndUnknown(t *testing.T) {
	// Derived from the recorded idle fixture by putting text in its input row.
	with := screenWithInput(IdleEmptyScreen, "hello")
	cur := ObservedCursor(with)
	if ClassifyInput(with, "", cur) != InputOther {
		t.Fatal("text in the input row")
	}
	if ClassifyInput(with, "hello", cur) != InputExact {
		t.Fatal("exact text in the input row")
	}
	undim := strings.Replace(IdlePlaceholderScreen, "\x1b[2m", "", 1)
	if ClassifyInput(undim, "", ObservedCursor(undim)) != InputOther {
		t.Fatal("placeholder text without the dim attribute")
	}
	// Spaces-only is derived from the idle fixture. It is not an empty input.
	spaces := screenWithInput(IdleEmptyScreen, "   ")
	if ClassifyInput(spaces, "", ObservedCursor(spaces)) != InputUnknown {
		t.Fatal("spaces in the input row")
	}
	// A blank row inside the box is derived from the idle fixture. Multi-row
	// input is not empty.
	if ClassifyInput(extraInputRow(IdleEmptyScreen), "", ObservedCursor(IdleEmptyScreen)) != InputUnknown {
		t.Fatal("multi-row input")
	}
	// Derived from question-open.2.1.321.ansi: the same recorded screen with
	// the question words changed. It is still a selection list.
	derived := strings.ReplaceAll(QuestionOpenScreen, "Fixture: pick one", "Permission: pick one")
	if derived == QuestionOpenScreen {
		t.Fatal("the derived permission screen is the recorded question unchanged")
	}
	if ClassifyInput(derived, "", Cursor{X: 2, Y: 46, Known: true}) != InputUnknown {
		t.Fatal("permission screen derived from the recorded question")
	}
	for _, screen := range []string{
		"$ ",
		"",
		IdleEmptyScreen[:len(IdleEmptyScreen)/2],
	} {
		if got := ClassifyInput(screen, "", Cursor{X: 2, Y: 46, Known: true}); got != InputUnknown {
			t.Fatalf("%q classified %d", screen, got)
		}
	}
	// The cursor is not just after the prompt mark, so the empty-looking row
	// is unknown.
	wrong := ObservedCursor(IdleEmptyScreen)
	wrong.X = 6
	if ClassifyInput(IdleEmptyScreen, "", wrong) != InputUnknown {
		t.Fatal("cursor not after the prompt")
	}
	// Derived from the recorded idle fixture by a numbered line above the box.
	// The cursor is that box's row. ObservedCursor is not used: it follows
	// the typing rule, and this screen is unknown to that rule.
	numbered, row, placed := screenWithConversationLine(IdleEmptyScreen, "1. Fix the bug")
	if !placed {
		t.Fatal("could not place a line above the box")
	}
	box := Cursor{X: 2, Y: row, Known: true}
	if ClassifyInput(numbered, "", box) != InputUnknown {
		t.Fatal("numbered list in the conversation")
	}
	if ObservedCursor(numbered).Known {
		t.Fatal("ObservedCursor follows the typing rule")
	}
	if ClassifyBox(numbered, "", box) != InputEmpty {
		t.Fatal("a numbered line above the box blocked the empty input")
	}
}

func TestRecordedCLIMinorsMatchFixtures(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`\.(\d+\.\d+)\.\d+\.ansi$`)
	got := map[string]bool{}
	for _, e := range entries {
		if m := re.FindStringSubmatch(e.Name()); m != nil {
			got[m[1]] = true
		}
	}
	if len(got) != 1 || !got["2.1"] {
		t.Fatalf("fixtures %v", got)
	}
	if !MinorRecorded("2.1") || MinorRecorded("2.2") || MinorRecorded("2.0") {
		t.Fatal(RecordedCLIMinors())
	}
	for _, out := range []string{"2.1.321 (Claude Code)", "2.1.999 (Claude Code)"} {
		minor, ok := CLIMinor(out)
		if !ok || minor != "2.1" || !MinorRecorded(minor) {
			t.Fatalf("%q → %q %v", out, minor, ok)
		}
	}
	if _, ok := CLIMinor("claude"); ok {
		t.Fatal("accepted a line with no version")
	}
	if HashPrefix("Opus | Café") == HashPrefix("Opus | Cafe") || len(HashPrefix("x")) != 12 {
		t.Fatal("hash prefix")
	}
}

func TestListPanesFormatIsTheRecordedLine(t *testing.T) {
	var format string
	for _, line := range strings.Split(CursorFactsText(), "\n") {
		if after, ok := strings.CutPrefix(line, "list-panes format: "); ok {
			format = after
		}
	}
	if format == "" || ListPanesFormat() != format {
		t.Fatalf("format %q recorded %q", ListPanesFormat(), format)
	}
	if !strings.Contains(format, "#{pane_synchronized}") || strings.Contains(format, "window_synchronized") {
		t.Fatal(format)
	}
	pane := RecordedPaneLine()
	if len(strings.Fields(pane)) != 8 || !strings.Contains(pane, "2028") {
		t.Fatal(pane)
	}
}

func TestBusyScreensAndFinishedRows(t *testing.T) {
	for name, screen := range BusySpinnerScreens() {
		cur, ok := RecordedCursor(screen)
		if !ok || cur.X != 2 || cur.Y != 46 {
			t.Fatalf("%s cursor %+v %v", name, cur, ok)
		}
		if ClassifyInput(screen, "", cur) != InputUnknown {
			t.Fatalf("%s was known before typing", name)
		}
		if ClassifyBox(screen, "", cur) != InputEmpty {
			t.Fatalf("%s box was not an empty input", name)
		}
	}
	stream, ok := RecordedCursor(BusyStreamingScreen)
	if !ok || ClassifyInput(BusyStreamingScreen, "", stream) != InputEmpty {
		t.Fatal("streaming frame")
	}
	typed, ok := RecordedCursor(BusyThinkingTypedScreen)
	if !ok || typed.X != 12 || ClassifyBox(BusyThinkingTypedScreen, "/rename zz", typed) != InputExact {
		t.Fatal("busy typed box")
	}
	if ClassifyInput(BusyThinkingTypedScreen, "/rename zz", typed) != InputUnknown {
		t.Fatal("a spinner row did not hold ClassifyInput")
	}
	for _, row := range []string{
		"✻ Crunched for 15s · done 8:31 AM",
		"✻ Worked for 12s · done 8:29 AM",
	} {
		if spinnerRow(row) {
			t.Fatalf("finished row matched: %q", row)
		}
	}
	if !spinnerRow("✻ Hatching… (8s)") || !spinnerRow("* Thinking… (1s)") {
		t.Fatal("recorded spinner shape")
	}
	idle, ok := RecordedCursor(IdleEmptyScreen)
	if !ok || ClassifyInput(IdleEmptyScreen, "", idle) != InputEmpty {
		t.Fatal("a finished row held the idle screen")
	}
}

func screenWithConversationLine(screen, text string) (string, int, bool) {
	const mark = "❯\u00a0"
	at := strings.LastIndex(screen, mark)
	if at < 0 {
		return "", 0, false
	}
	promptLine := strings.LastIndex(screen[:at], "\n")
	if promptLine < 0 {
		return "", 0, false
	}
	topLine := strings.LastIndex(screen[:promptLine], "\n")
	if topLine < 0 {
		return "", 0, false
	}
	out := screen[:topLine+1] + text + "\n" + screen[topLine+1:]
	row := strings.Count(out[:strings.LastIndex(out, mark)], "\n")
	return out, row, true
}

func screenWithInput(screen, text string) string {
	const mark = "❯\u00a0"
	i := strings.LastIndex(screen, mark)
	if i < 0 {
		return screen
	}
	rest := screen[i+len(mark):]
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		return screen[:i+len(mark)] + text
	}
	return screen[:i+len(mark)] + text + rest[nl:]
}

func extraInputRow(screen string) string {
	const mark = "❯\u00a0"
	i := strings.LastIndex(screen, mark)
	if i < 0 {
		return screen
	}
	nl := strings.IndexByte(screen[i:], '\n')
	if nl < 0 {
		return screen
	}
	at := i + nl + 1
	return screen[:at] + "\n" + screen[at:]
}
