package sessiontitle

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestClassifyRecordedFixtures(t *testing.T) {
	empty := ObservedCursor(IdleEmptyScreen)
	if ClassifyInput(IdleEmptyScreen, "", empty) != InputEmpty {
		t.Fatal("empty input")
	}
	if ClassifyInput(IdleEmptyScreen, "/rename zz", empty) != InputEmpty {
		t.Fatal("empty input is not the typed text")
	}
	if ClassifyInput(IdlePlaceholderScreen, "", ObservedCursor(IdlePlaceholderScreen)) != InputEmpty {
		t.Fatal("dim placeholder")
	}
	typed := ObservedCursor(TypedRenameScreen)
	if ClassifyInput(TypedRenameScreen, "/rename zz", typed) != InputExact {
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
	if got := ClassifyInput(QuestionOpenScreen, "", Cursor{}); got != InputUnknown {
		t.Fatalf("recorded question classified %d", got)
	}
	if got := ClassifyInput(QuestionOpenScreen, "", ObservedCursor(QuestionOpenScreen)); got != InputUnknown {
		t.Fatalf("recorded question with its own cursor classified %d", got)
	}
	if ObservedCursor(QuestionOpenScreen).Known {
		t.Fatal("the recorded question has no idle input box")
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
		if got := ClassifyInput(screen, "", Cursor{}); got != InputUnknown {
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
	// Derived from the recorded idle fixture by inserting a numbered list in
	// the conversation. The whole-screen rule makes that unknown.
	mark := strings.LastIndex(IdleEmptyScreen, "❯\u00a0")
	if mark < 0 {
		t.Fatal("idle fixture has no input mark")
	}
	numbered := IdleEmptyScreen[:mark] + "1. Fix the bug\n" + IdleEmptyScreen[mark:]
	if ClassifyInput(numbered, "", ObservedCursor(numbered)) != InputUnknown {
		t.Fatal("numbered list in the conversation")
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
	raw, err := os.ReadFile("testdata/tmux-facts.txt")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("facts %q", lines)
	}
	if ListPanesFormat() != lines[3] || RecordedPaneLine() != lines[1] {
		t.Fatalf("format %q line %q", ListPanesFormat(), RecordedPaneLine())
	}
	if !strings.Contains(lines[3], "#{pane_synchronized}") || strings.Contains(lines[3], "window_synchronized") {
		t.Fatal(lines[3])
	}
	if len(strings.Fields(lines[1])) != 8 {
		t.Fatal(lines[1])
	}
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
