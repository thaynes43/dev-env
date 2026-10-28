package session

import (
	"os"
	"strings"
	"testing"
)

func TestCommittedProductFixturesRender(t *testing.T) {
	for name, render := range map[string]func(*os.File) ([]byte, error){
		"sessions": func(f *os.File) ([]byte, error) { return RenderSessionsFixture(f) },
		"tasks":    func(f *os.File) ([]byte, error) { return RenderTasksFixture(f) },
	} {
		f, err := os.Open("../../../scripts/trials/portal_product/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		body, err := render(f)
		_ = f.Close()
		if err != nil || !strings.Contains(string(body), "Fixture preview") || !strings.Contains(string(body), "Synthetic data.") {
			t.Fatalf("%s fixture: err=%v", name, err)
		}
	}
	for name, raw := range map[string]string{
		"unknown key": `{"version":1,"now":"2026-10-26T15:00:00Z","body":{},"extra":1}`,
		"version":     `{"version":2,"now":"2026-10-26T15:00:00Z","body":{}}`,
		"no clock":    `{"version":1,"body":{}}`,
		"trailing":    `{"version":1,"now":"2026-10-26T15:00:00Z","body":{}} {}`,
	} {
		if _, err := RenderSessionsFixture(strings.NewReader(raw)); err == nil {
			t.Errorf("%s fixture accepted", name)
		}
	}
}

func TestLaunchFixturesAreLabelledAndContactNothing(t *testing.T) {
	f, err := os.Open("../../../scripts/trials/portal_product/sessions.json")
	if err != nil {
		t.Fatal(err)
	}
	body, err := RenderSessionsLaunchFixture(f)
	_ = f.Close()
	s := string(body)
	if err != nil || !strings.Contains(s, "Fixture preview") || !strings.Contains(s, `id="launch"`) || !strings.Contains(s, `<option value="fixture-repo-one">`) || strings.Contains(s, `<option value="dev-env">`) {
		t.Fatalf("launch fixture: %v", err)
	}
	results, err := RenderLaunchResultFixtures()
	if err != nil || len(results) != 5 {
		t.Fatalf("results: %v %d", err, len(results))
	}
	for outcome, page := range results {
		if !strings.Contains(string(page), "Fixture preview") || strings.Contains(string(page), "<form") {
			t.Errorf("%s: not a labelled, formless fixture", outcome)
		}
	}
}

// The committed Sessions fixture shows each conversation-usage state the page
// can take: Final and current, Partial with its reason, and stale, for Claude and
// Codex, with the conversation table first in the Usage view.
func TestCommittedSessionsFixtureShowsConversationUsage(t *testing.T) {
	f, err := os.Open("../../../scripts/trials/portal_product/sessions.json")
	if err != nil {
		t.Fatal(err)
	}
	body, err := RenderSessionsFixture(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{
		`<span class="badge tone-muted">Final</span>`, `<span class="badge tone-wait">Partial</span><span>Some records could not be read or counted. The counts shown are a lower bound.</span>`,
		`<span class="badge tone-wait">Stale</span><span>Older than 15 minutes, so it may be behind the conversation.</span>`, `<span class="badge tone-muted">Current</span>`,
		"Codex input already includes cache reads, so the two are never added.", `<td data-label="Cache read">0</td>`, `<td data-label="Cache create" class="unknown">Unknown</td>`,
		`<h3 id="usage-b0">Conversation usage by provider and served model</h3>`, "codex · gpt-6-astra", "Conversation: In 1.3M · Out 41.2k · stale",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the fixture page lacks %q", want)
		}
	}
}
