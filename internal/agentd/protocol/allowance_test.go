package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The readings here are synthetic, built from the shapes in DESIGN-059 4.1
// and 4.2. None comes from a provider.

func allowanceAt() time.Time { return time.Date(2026, 11, 9, 23, 42, 10, 500_000_000, time.UTC) }

func validReading() AllowanceReading {
	at := allowanceAt()
	return AllowanceReading{
		Provider: AgentCodex, Source: AllowanceSourceCodexRollout, ReadAt: at,
		Windows: []AllowanceWindow{
			{Minutes: WeeklyAllowanceMinutes, UsedPercent: "65", ResetsAt: at.Add(36 * time.Hour).Truncate(time.Second)},
			{Minutes: 300, UsedPercent: "12.5", ResetsAt: at.Add(2 * time.Hour).Truncate(time.Second)},
		},
	}
}

func TestAllowanceReadingValid(t *testing.T) {
	if !validReading().Valid() {
		t.Fatal("the valid reading was refused")
	}
	claude := validReading()
	claude.Provider, claude.Source, claude.Windows = AgentClaude, AllowanceSourceClaudeStatusline, claude.Windows[:1]
	if !claude.Valid() {
		t.Fatal("a one-window Claude reading was refused")
	}
	at := allowanceAt()
	bad := map[string]func(*AllowanceReading){
		"cursor provider":       func(a *AllowanceReading) { a.Provider, a.Source = AgentCursor, AllowanceSourceCodexRollout },
		"source of other agent": func(a *AllowanceReading) { a.Source = AllowanceSourceClaudeStatusline },
		"no source":             func(a *AllowanceReading) { a.Source = "" },
		"zero time":             func(a *AllowanceReading) { a.ReadAt = time.Time{} },
		"no windows":            func(a *AllowanceReading) { a.Windows = nil },
		"three windows": func(a *AllowanceReading) {
			a.Windows = append(a.Windows, AllowanceWindow{Minutes: 60, UsedPercent: "1", ResetsAt: at.Add(time.Hour).Truncate(time.Second)})
		},
		"same length twice":     func(a *AllowanceReading) { a.Windows[1].Minutes = WeeklyAllowanceMinutes },
		"zero minutes":          func(a *AllowanceReading) { a.Windows[1].Minutes = 0 },
		"window too long":       func(a *AllowanceReading) { a.Windows[0].Minutes = MaxAllowanceWindowMinutes + 1 },
		"percent over 100":      func(a *AllowanceReading) { a.Windows[0].UsedPercent = "100.5" },
		"negative percent":      func(a *AllowanceReading) { a.Windows[0].UsedPercent = "-1" },
		"trailing zero":         func(a *AllowanceReading) { a.Windows[0].UsedPercent = "65.0" },
		"leading zero":          func(a *AllowanceReading) { a.Windows[0].UsedPercent = "065" },
		"seven fraction digits": func(a *AllowanceReading) { a.Windows[0].UsedPercent = "1.0000001" },
		"empty percent":         func(a *AllowanceReading) { a.Windows[0].UsedPercent = "" },
		"reset before reading":  func(a *AllowanceReading) { a.Windows[0].ResetsAt = at.Add(-time.Second).Truncate(time.Second) },
		"reset equals reading":  func(a *AllowanceReading) { a.ReadAt = a.Windows[1].ResetsAt },
		"reset past one window": func(a *AllowanceReading) {
			a.Windows[1].ResetsAt = at.Add(5*time.Hour + AllowanceResetSlack + time.Minute).Truncate(time.Second)
		},
		"reset not whole second": func(a *AllowanceReading) { a.Windows[0].ResetsAt = at.Add(time.Hour) },
		"zero reset":             func(a *AllowanceReading) { a.Windows[0].ResetsAt = time.Time{} },
	}
	for name, mutate := range bad {
		a := validReading()
		a.Windows = append([]AllowanceWindow(nil), a.Windows...)
		mutate(&a)
		if a.Valid() {
			t.Errorf("%s: accepted", name)
		}
	}
	// A reset rounded up to the hour, a little more than one window away, is kept.
	a := validReading()
	a.Windows[1].ResetsAt = at.Add(5*time.Hour + 30*time.Minute).Truncate(time.Second)
	if !a.Valid() {
		t.Error("a reset within the slack was refused")
	}
}

func TestCanonicalAllowancePercent(t *testing.T) {
	for in, want := range map[string]string{
		"0": "0", "0.0": "0", "65": "65", "65.0": "65", "12.50": "12.5", "100": "100", "100.000": "100",
		"6.5e1": "65", "1.23456789": "1.234568", "99.9999996": "100", "0.0000004": "0",
	} {
		got, ok := CanonicalAllowancePercent(in)
		if !ok || got != want {
			t.Errorf("%q: got %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "-1", "100.1", "1e400", "0x10", "1/2", " 5", "+5", "5.", ".5", "NaN", "Inf", strings.Repeat("1", 65)} {
		if got, ok := CanonicalAllowancePercent(in); ok {
			t.Errorf("%q: accepted as %q", in, got)
		}
	}
}

func TestAllowanceResetsAt(t *testing.T) {
	if at, ok := AllowanceResetsAt("1762732130"); !ok || at.Unix() != 1762732130 || at.Location() != time.UTC {
		t.Errorf("got %v %v", at, ok)
	}
	for _, in := range []string{"", "0", "-5", "1.5", "1e9", "+5", "01", "253402300800"} {
		if _, ok := AllowanceResetsAt(in); ok {
			t.Errorf("%q: accepted", in)
		}
	}
}

func TestAllowanceSameValues(t *testing.T) {
	a, b := validReading(), validReading()
	b.Windows = []AllowanceWindow{a.Windows[1], a.Windows[0]}
	b.ReadAt = b.ReadAt.Add(time.Hour)
	if !a.SameValues(b) {
		t.Error("order or time made equal windows differ")
	}
	b.Windows[0].UsedPercent = "13"
	if a.SameValues(b) {
		t.Error("a changed percent kept the values the same")
	}
	if a.SameValues(AllowanceReading{Windows: a.Windows[:1]}) {
		t.Error("fewer windows kept the values the same")
	}
}

// An operator that does not know the field ignores it, and a status without
// it has no allowance key at all.
func TestStatusAllowanceField(t *testing.T) {
	st := Status{Session: "s", Agentd: "agentd", Boot: BootReady, Agent: AgentState{State: AgentBusy}}
	raw, err := json.Marshal(st)
	if err != nil || strings.Contains(string(raw), `"allowance"`) {
		t.Fatalf("status without a reading: %s %v", raw, err)
	}
	r := validReading()
	st.Allowance = &r
	raw, err = json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var old struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(raw, &old); err != nil || old.Session != "s" {
		t.Fatalf("an older decoder: %v", err)
	}
	var back Status
	if err := json.Unmarshal(raw, &back); err != nil || back.Allowance == nil || !back.Allowance.Valid() || !back.Allowance.SameValues(r) {
		t.Fatalf("round trip: %+v %v", back.Allowance, err)
	}
	for _, field := range []string{"plan", "credits", "account", "email"} {
		if strings.Contains(strings.ToLower(string(raw)), field) {
			t.Errorf("the wire carries %q", field)
		}
	}
}
