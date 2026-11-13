package sessiontitle

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestClassifyRecordedFixtures(t *testing.T) {
	if ClassifyInput(IdleEmptyScreen, "") != InputEmpty {
		t.Fatal("empty input")
	}
	if ClassifyInput(IdleEmptyScreen, "/rename zz") != InputEmpty {
		t.Fatal("empty input is not the typed text")
	}
	if ClassifyInput(IdlePlaceholderScreen, "") != InputEmpty {
		t.Fatal("dim placeholder")
	}
	if ClassifyInput(TypedRenameScreen, "/rename zz") != InputExact {
		t.Fatal("typed /rename zz")
	}
	if ClassifyInput(TypedRenameScreen, "/rename zz ") != InputOther {
		t.Fatal("typed text that is not exact")
	}
	if ClassifyInput(TypedRenameScreen, "") != InputOther {
		t.Fatal("typed text is not an empty input")
	}
}

func TestClassifyInputOtherAndUnknown(t *testing.T) {
	with := screenWithInput(IdleEmptyScreen, "hello")
	if ClassifyInput(with, "") != InputOther {
		t.Fatal("text in the input row")
	}
	if ClassifyInput(with, "hello") != InputExact {
		t.Fatal("exact text in the input row")
	}
	undim := strings.Replace(IdlePlaceholderScreen, "\x1b[2m", "", 1)
	if ClassifyInput(undim, "") != InputOther {
		t.Fatal("placeholder text without the dim attribute")
	}
	for _, screen := range []string{
		"Allow this command?\n1. Yes\n2. No",
		"Permission prompt\nDo you want to proceed?\n1. Yes\n2. No",
		"$ ",
		"",
		IdleEmptyScreen[:len(IdleEmptyScreen)/2],
	} {
		if got := ClassifyInput(screen, ""); got != InputUnknown {
			t.Fatalf("%q classified %d", screen, got)
		}
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
