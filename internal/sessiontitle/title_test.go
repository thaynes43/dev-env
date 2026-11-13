package sessiontitle

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestValidate(t *testing.T) {
	title96 := strings.Repeat("é", 48)
	title97 := strings.Repeat("é", 47) + "€"
	if len(title96) != 96 || utf8.RuneCountInString(title96) != 48 {
		t.Fatalf("96-byte fixture is %d bytes and %d runes", len(title96), utf8.RuneCountInString(title96))
	}
	if len(title97) != 97 || utf8.RuneCountInString(title97) != 48 {
		t.Fatalf("97-byte fixture is %d bytes and %d runes", len(title97), utf8.RuneCountInString(title97))
	}
	if n := strings.Repeat("\u4e00", 48); len(n) != 144 || utf8.RuneCountInString(n) != 48 {
		t.Fatalf("144-byte fixture is %d bytes and %d runes", len(n), utf8.RuneCountInString(n))
	}
	cases := []struct {
		title, reason string
	}{
		{"", ""},
		{"Main coordinator", ""},
		{"Café", ""},
		{"1a", ""},
		{"é", ""},
		{"e\u0301", ReasonCharacters},
		{"Main-coordinator", ""},
		{"a;b", ""},
		{strings.Repeat("a", 48), ""},
		{title96, ""},
		{strings.Repeat("a", 49), ReasonTooLong},
		{title97, ReasonCharacters},
		{strings.Repeat("\u4e00", 48), ReasonCharacters},
		{"测", ReasonCharacters},
		{"|", ReasonCharacters},
		{"a|b", ReasonCharacters},
		{"ab;", ReasonCharacters},
		{"a\\b", ReasonCharacters},
		{"a\u0007b", ReasonCharacters},
		{"a\tb", ReasonCharacters},
		{"a\nb", ReasonCharacters},
		{"a\u200bb", ReasonCharacters},
		{"a\u00adb", ReasonCharacters},
		{"a\u00a0b", ReasonCharacters},
		{"a\U00010000", ReasonCharacters},
		{"-Main", ReasonCharacters},
		{".Main", ReasonCharacters},
		{"!Main", ReasonCharacters},
		{"€", ReasonCharacters},
		{" Main", ReasonSpacing},
		{"Main ", ReasonSpacing},
		{"Main  coordinator", ReasonSpacing},
	}
	for _, c := range cases {
		if got := Validate(c.title); got != c.reason {
			t.Errorf("Validate(%q) = %q, want %q", c.title, got, c.reason)
		}
	}
}

func TestFamilyAndCompose(t *testing.T) {
	families := []struct{ model, family string }{
		{"claude-fable-5-2", "Fable"},
		{"claude-opus-5-5", "Opus"},
		{"claude-opus-5-5[1m]", "Opus"},
		{"claude-sonnet-5-5", "Sonnet"},
		{"claude-haiku-4-5", "Haiku"},
		{"gpt-6-astra", "GPT"},
		{"grok-code-fast-1", ""},
		{"claude-fable", ""},
		{"claude-opus", ""},
		{"gpt", ""},
		{"GPT-6", ""},
		{"", ""},
	}
	for _, c := range families {
		if got := Family(c.model); got != c.family {
			t.Errorf("Family(%q) = %q, want %q", c.model, got, c.family)
		}
	}
	if got := Compose("claude-opus-5-5", "Main coordinator"); got != "Opus | Main coordinator" {
		t.Fatalf("compose %q", got)
	}
	if got := Compose("claude-haiku-4-5", "Main coordinator"); got != "Haiku | Main coordinator" {
		t.Fatalf("compose %q", got)
	}
	if got := Compose("grok-code-fast-1", "Main coordinator"); got != "Main coordinator" {
		t.Fatalf("no family %q", got)
	}
	if got := Compose("claude-opus-5-5", ""); got != "" {
		t.Fatalf("no title %q", got)
	}
	if got := Compose("", "Main"); got != "Main" {
		t.Fatalf("no model %q", got)
	}
	longest := Compose("claude-sonnet-5-5", strings.Repeat("é", 48))
	if utf8.RuneCountInString(longest) != 57 || len(longest) != 105 || len(longest) > 128 {
		t.Fatalf("longest compose is %d runes and %d bytes", utf8.RuneCountInString(longest), len(longest))
	}
	if strings.Contains(Explain(ReasonTooLong), "Main") {
		t.Fatal("Explain included a title")
	}
}

func TestEffective(t *testing.T) {
	if got, ignored := Effective("", false, "Old"); got != "Old" || ignored {
		t.Fatalf("absent %q %v", got, ignored)
	}
	if got, ignored := Effective("New", true, "Old"); got != "New" || ignored {
		t.Fatalf("annotation %q %v", got, ignored)
	}
	if got, ignored := Effective("", true, "Old"); got != "" || ignored {
		t.Fatalf("clear %q %v", got, ignored)
	}
	if got, ignored := Effective("ab;", true, "Old"); got != "Old" || !ignored {
		t.Fatalf("ignored %q %v", got, ignored)
	}
}
