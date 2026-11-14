package usagecollector

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func cursorReady(workspace string) string {
	return "Cursor Agent v" + providerallowance.CursorCLIQualifiedVersion + "\r\nPlan, search, build anything\r\n" + workspace + "\r\n"
}

const cursorMenu = "→ /usage\r\nShow plan and on-demand usage\r\n"
const cursorScreen = "Loading usage data...\r\nUsage • fixture-tier\r\nResets Nov 6\r\nMonthly plan and on-demand usage\r\nCategory Current Usage\r\nIncluded 61% used\r\nAuto 61% used\r\nAPI 0% used\r\nEsc to close\r\n"

const cursorSingleRowMenu = "→ /usage    Show plan and on-demand usage\r\n"

func TestCursorProtocolSingleInactiveAutocompleteRow(t *testing.T) {
	p := cursorProtocol{workspace: "/private/empty"}
	if input, _, err := feedCursorFrame(&p, cursorReady(p.workspace)); err != nil || input != "/usage" {
		t.Fatal("qualified ready state failed", err)
	}
	input, report, err := feedCursorFrame(&p, cursorSingleRowMenu)
	if err != nil || input != "\r" || report != nil || p.state != 3 {
		t.Fatal("one selected inactive command/description row could not advance", err, p.state)
	}
	input, report, err = feedCursorFrame(&p, cursorScreen)
	if err != nil || input != "" || report == nil || *report.IncludedPercentDisplay != 61 {
		t.Fatal("single-row command lost the separate usage display", err)
	}
}

func TestCursorSingleRowMenuKeepsInputAndModalFences(t *testing.T) {
	for _, frame := range []string{
		strings.Replace(cursorSingleRowMenu, "/usage ", "/usage-other ", 1),
		strings.Replace(cursorSingleRowMenu, "Show plan and on-demand usage", "Run another command", 1),
		strings.TrimSuffix(cursorSingleRowMenu, "\r\n"), // Active row is not a menu.
		cursorSingleRowMenu + "→ /usage-other",
		cursorSingleRowMenu + cursorSingleRowMenu,
		cursorSingleRowMenu + cursorMenu,
		cursorSingleRowMenu + "Continue? [y/N]\r\n",
		strings.Replace(cursorSingleRowMenu, "usage\r\n", "usage PRIVATE-SENTINEL\r\n", 1),
	} {
		p := cursorProtocol{state: 2}
		input, report, _ := feedCursorFrame(&p, frame)
		if input != "" || report != nil || p.state != 2 {
			t.Fatal("foreign, active, ambiguous or modal menu authorized input")
		}
	}
}

func TestCursorInlineMenuRejectsInactiveCompetingSelections(t *testing.T) {
	for _, test := range []struct{ name, row string }{
		{"foreign-selected", "→ /other    Another command\r\n"},
		{"partial-legacy-selector", "→ /usage\r\n"},
		{"partial-legacy-description", "Show plan and on-demand usage\r\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := cursorProtocol{state: 2}
			// Both candidate rows end before the active blank row. An active
			// foreign-input guard cannot mask selection ambiguity here.
			input, report, _ := feedCursorFrame(&p, cursorSingleRowMenu+test.row)
			if input != "" || report != nil || p.state != 2 {
				t.Fatal("competing inactive selection authorized Enter")
			}
		})
	}
}

func TestCursorProtocolOnlySendsAfterDistinctQualifiedStates(t *testing.T) {
	p := cursorProtocol{workspace: "/private/empty", trust: true}
	trust := "Workspace Trust Required\n/private/empty\n[a] Trust this workspace\n[q] Quit\n"
	for _, step := range []struct{ screen, input string }{{trust, "a"}, {cursorReady(p.workspace), "/usage"}, {cursorMenu, "\r"}} {
		input, report, err := feedCursorFrame(&p, step.screen)
		if err != nil || input != step.input || report != nil {
			t.Fatal("state did not gate exact input", input, err)
		}
	}
	input, report, err := feedCursorFrame(&p, cursorScreen)
	if err != nil || input != "" || report == nil || *report.IncludedPercentDisplay != 61 || report.Plan != "fixture-tier" || *report.ResetMonthDay != "Nov 6" || report.AccountRef != nil || report.Period != "not_exposed" || report.ProviderObservedAt != nil {
		t.Fatal("display fabricated account/precision or lost normal usage", err)
	}
}

func TestCursorProtocolChunkBoundariesDoNotAuthorizeEarlyInput(t *testing.T) {
	p := cursorProtocol{workspace: "/private/empty"}
	for _, step := range []struct{ screen, input string }{{"\x1b[32m" + cursorReady(p.workspace) + "\x1b[0m", "/usage"}, {cursorMenu, "\r"}, {cursorScreen, ""}} {
		for _, b := range []byte(step.screen) {
			input, report, err := p.accept([]byte{b})
			if err != nil || input != "" || report != nil {
				t.Fatal("fragment authorized input/report", err)
			}
		}
		input, report, err := p.settle()
		if err != nil || input != step.input || (step.input == "" && report == nil) {
			t.Fatal("settled full frame failed", err)
		}
	}
}

func TestCursorProtocolRefusesAuthTrustAndAmbiguousDisplay(t *testing.T) {
	for _, text := range []string{"Sign in to Cursor", "API key required", "Permission required", "Accept the terms", "Approve tool", "Allow command"} {
		p := cursorProtocol{workspace: "/private/empty"}
		input, _, err := feedCursorFrame(&p, cursorReady(p.workspace)+text)
		if err == nil || input != "" {
			t.Fatal("authentication/modal got input")
		}
	}
	for _, p := range []cursorProtocol{{workspace: "/private/empty"}, {workspace: "/another", trust: true}} {
		input, _, err := feedCursorFrame(&p, "Workspace Trust Required\n/private/empty\n[a] Trust this workspace\n[q] Quit\n")
		if err == nil || input != "" {
			t.Fatal("unapproved/mismatched workspace trusted")
		}
	}
	for _, body := range []string{strings.Replace(cursorScreen, "61% used", "101% used", 1), cursorScreen + "Included 12% used\n", strings.Replace(cursorScreen, "Resets Nov 6", "Resets 2026-11-06", 1), strings.Replace(cursorScreen, "API 0% used", "API unavailable", 1)} {
		p := cursorProtocol{state: 3}
		input, report, err := feedCursorFrame(&p, body)
		if err == nil || input != "" || report != nil {
			t.Fatal("ambiguous display accepted")
		}
	}
	p := cursorProtocol{state: 2}
	if input, report, err := feedCursorFrame(&p, cursorScreen); err != nil || input != "" || report != nil {
		t.Fatal("screen bypassed separately observed command menu")
	}
}

func TestCursorProtocolBoundsAndVersion(t *testing.T) {
	p := cursorProtocol{workspace: "/private/empty"}
	if input, _, err := feedCursorFrame(&p, strings.Replace(cursorReady(p.workspace), providerallowance.CursorCLIQualifiedVersion, "unqualified", 1)); err != nil || input != "" {
		t.Fatal("unqualified version got input")
	}
	p = cursorProtocol{}
	if input, _, err := p.accept([]byte(strings.Repeat("x", (256<<10)+1))); err == nil || input != "" {
		t.Fatal("unbounded terminal accepted")
	}
	p = cursorProtocol{}
	if input, _, err := p.accept([]byte("\x1b?")); err == nil || input != "" {
		t.Fatal("unknown terminal escape accepted")
	}
}

func feedCursorFrame(p *cursorProtocol, text string) (string, *providerallowance.CursorCLIUsageCapture, error) {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	if _, _, err := p.accept([]byte("\x1b[2J\x1b[H" + text)); err != nil {
		return "", nil, err
	}
	return p.settle()
}

func TestCursorProtocolErasedMenuAndExactIdentityCannotAuthorizeInput(t *testing.T) {
	for _, body := range []string{cursorMenu + "\x1b[2J\x1b[HContinue? [y/N]\n", strings.Replace(cursorMenu, "/usage", "/usage-other", 1)} {
		p := cursorProtocol{state: 2}
		input, _, _ := feedCursorFrame(&p, body)
		if input != "" {
			t.Fatal("erased or foreign command authorized Enter")
		}
	}
	for _, body := range []string{strings.Replace(cursorReady("/private/empty"), providerallowance.CursorCLIQualifiedVersion, providerallowance.CursorCLIQualifiedVersion+"-different", 1), cursorReady("/private/empty-other")} {
		p := cursorProtocol{workspace: "/private/empty"}
		input, _, _ := feedCursorFrame(&p, body)
		if input != "" {
			t.Fatal("prefix identity authorized input")
		}
	}
	for _, percent := range []string{"1.5", "-1", "101"} {
		p := cursorProtocol{state: 3}
		_, report, err := feedCursorFrame(&p, strings.Replace(cursorScreen, "Included 61%", "Included "+percent+"%", 1))
		if err == nil || report != nil {
			t.Fatal("percent suffix accepted", percent)
		}
	}
	p := cursorProtocol{workspace: "/private/empty"}
	_, _, _ = p.accept([]byte(cursorReady(p.workspace) + "Sign "))
	_, _, _ = p.accept([]byte("in to Cursor\n"))
	if input, _, err := p.settle(); err == nil || input != "" {
		t.Fatal("fragmented auth prompt got input")
	}
}

func TestCursorTerminalPartialIdentityAndSynchronizedRedrawCannotAct(t *testing.T) {
	p := cursorProtocol{workspace: "/private/empty"}
	partial := strings.TrimSuffix(cursorReady(p.workspace), "\n")
	if _, _, err := p.accept([]byte(partial)); err != nil {
		t.Fatal(err)
	}
	if input, _, err := p.settle(); err != nil || input != "" {
		t.Fatal("unfinished workspace line authorized input")
	}
	_, _, _ = p.accept([]byte("-other\n"))
	if input, _, _ := p.settle(); input != "" {
		t.Fatal("workspaceprefix authorized input")
	}
	p = cursorProtocol{workspace: "/private/empty"}
	_, _, _ = p.accept([]byte("\x1b[?2026h" + cursorReady(p.workspace)))
	if input, _, err := p.settle(); err != nil || input != "" {
		t.Fatal("partial redraw authorized input")
	}
	_, _, _ = p.accept([]byte("\x1b[?2026l"))
	if input, _, err := p.settle(); err != nil || input != "/usage" {
		t.Fatal("completed synchronized frame refused", err)
	}
	p = cursorProtocol{state: 2}
	_, _, _ = p.accept([]byte("Show plan and on-demand usage\n→ /usage"))
	if input, _, _ := p.settle(); input != "" {
		t.Fatal("unfinished selectedcommand authorizedEnter")
	}
	_, _, _ = p.accept([]byte("-other\n"))
	if input, _, _ := p.settle(); input != "" {
		t.Fatal("commandprefix authorizedEnter")
	}
}

func TestCursorTerminalRefusesOverflowThatCouldHideModalText(t *testing.T) {
	p := cursorProtocol{state: 2}
	if input, _, err := p.accept([]byte(cursorMenu + strings.Repeat(" ", 179) + "Confirm >")); err == nil || input != "" {
		t.Fatal("terminaloverflow hidconfirmationmodal")
	}
}

func TestCursorTerminalBareLineFeedPreservesColumnAndVisiblePrefix(t *testing.T) {
	var terminal cursorTerminal
	if err := terminal.feed([]byte("\r\nBAD\r\n\x1b[1;4H\nCursor Agent v" + providerallowance.CursorCLIQualifiedVersion + "\r\n")); err != nil {
		t.Fatal(err)
	}
	text, ok := terminal.visible()
	if !ok || !strings.Contains(text, "BADCursor Agent v") {
		t.Fatal("bareLFerasedexistingprefix")
	}
}

func TestCursorCompleteScreenMaySkipTransientLoadingButNeedsNewOutput(t *testing.T) {
	p := cursorProtocol{state: 3}
	body := strings.TrimPrefix(cursorScreen, "Loading usage data...\r\n")
	if _, report, err := feedCursorFrame(&p, body); err != nil || report == nil {
		t.Fatal("fastcompletebuiltinrefused", err)
	}
	if input, report, err := p.settle(); err != nil || input != "" || report != nil {
		t.Fatal("oldframecreatedanothercapture")
	}
}

func TestCursorTerminalRetainedAutocompleteEllipsis(t *testing.T) {
	const description = "Create custom subagents for specialized AI…"
	var terminal cursorTerminal
	if err := terminal.feed([]byte(description + "\r\n")); err != nil {
		t.Fatal("retained printable ellipsis refused", err)
	}
	visible, ok := terminal.visible()
	if !ok || !strings.Contains(visible, description) {
		t.Fatal("ellipsis text was hidden or changed")
	}
	p := cursorProtocol{state: 2}
	input, report, err := feedCursorFrame(&p, cursorSingleRowMenu+description+"\r\n")
	if err != nil || input != "\r" || report != nil {
		t.Fatal("unselected ellipsis description prevented exact usage selection", err)
	}
}

func TestCursorEllipsisKeepsTerminalAndPromptFences(t *testing.T) {
	for _, text := range []string{"界", "😀", "\u202e", "e\u0301", "☃", "\x00", "\xff"} {
		p := cursorProtocol{state: 2}
		input, report, err := feedCursorFrame(&p, cursorSingleRowMenu+text+"\r\n")
		if err == nil || input != "" || report != nil {
			t.Fatal("unqualified wide, bidi, combining, unknown or control text authorized input")
		}
	}
	p := cursorProtocol{state: 2}
	if input, report, err := feedCursorFrame(&p, cursorSingleRowMenu+"Continue? … [y/N]\r\n"); err == nil || input != "" || report != nil {
		t.Fatal("ellipsis hid a confirmation prompt")
	}
	p = cursorProtocol{state: 2}
	if input, report, err := p.accept([]byte(cursorSingleRowMenu + strings.Repeat("x", 179) + "…Confirm")); err == nil || input != "" || report != nil {
		t.Fatal("ellipsis bypassed terminal bounds")
	}
}

const cursorDashboardFooter = " View in dashboard: cursor.com/dashboard?tab=usage\r\n"

func TestCursorRetainedDashboardFooterAfterDistinctStates(t *testing.T) {
	p := cursorProtocol{workspace: "/private/empty"}
	for _, step := range []struct{ frame, input string }{{cursorReady(p.workspace), "/usage"}, {cursorSingleRowMenu, "\r"}} {
		input, report, err := feedCursorFrame(&p, step.frame)
		if err != nil || input != step.input || report != nil {
			t.Fatal("prior distinct state refused", err)
		}
	}
	input, report, err := feedCursorFrame(&p, cursorScreen+cursorDashboardFooter)
	if err != nil || input != "" || report == nil || *report.IncludedPercentDisplay != 61 {
		t.Fatal("exact retained inactive dashboard footer refused", err)
	}
	visible, complete := p.terminal.visible()
	if !complete || !strings.Contains(visible, "cursor.com/dashboard?tab=usage") {
		t.Fatal("footer text was hidden or rewritten")
	}
}

func TestCursorDashboardFooterDoesNotHideQuestionsOrAuthorizeInput(t *testing.T) {
	for _, test := range []struct {
		name, frame string
		state       int
	}{
		{"ready", cursorReady("/private/empty") + cursorDashboardFooter, 0},
		{"trust", "Workspace Trust Required\r\n/private/empty\r\n[a] Trust this workspace\r\n[q] Quit\r\n" + cursorDashboardFooter, 0},
		{"selection", cursorSingleRowMenu + cursorDashboardFooter, 2},
		{"active", cursorScreen + strings.TrimSuffix(cursorDashboardFooter, "\r\n"), 3},
		{"duplicate", cursorScreen + cursorDashboardFooter + cursorDashboardFooter, 3},
		{"adjacent", cursorScreen + cursorDashboardFooter + "Another question?\r\n", 3},
		{"active-question", cursorScreen + cursorDashboardFooter + "> Another question?", 3},
		{"prefix", cursorScreen + "Approve?" + cursorDashboardFooter, 3},
		{"suffix", cursorScreen + strings.TrimSuffix(cursorDashboardFooter, "\r\n") + " Another question?\r\n", 3},
		{"foreign-host", cursorScreen + strings.Replace(cursorDashboardFooter, "cursor.com", "other.example", 1), 3},
		{"foreign-query", cursorScreen + strings.Replace(cursorDashboardFooter, "tab=usage", "tab=another", 1), 3},
		{"foreign-prefix", cursorScreen + "PRIVATE-SENTINEL" + cursorDashboardFooter, 3},
		{"incomplete-display", strings.Replace(cursorScreen, "Esc to close", "Other footer", 1) + cursorDashboardFooter, 3},
		{"confirm", cursorScreen + cursorDashboardFooter + "Confirm action\r\n", 3},
		{"auth", cursorScreen + cursorDashboardFooter + "Sign in\r\n", 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := cursorProtocol{workspace: "/private/empty", trust: true, state: test.state}
			input, report, err := feedCursorFrame(&p, test.frame)
			if err == nil || input != "" || report != nil {
				t.Fatal("footer hid a question or authorized input/capture")
			}
		})
	}
}

// Rows other than Included, Auto and API are ignored today. This pins that
// behaviour so a later capture with an extra row (for example a Sand line) can
// be read against it. It qualifies no such row.
func TestCursorProtocolIgnoresRowsOtherThanIncludedAutoAndAPI(t *testing.T) {
	for _, extra := range []string{"Grok Bot 12% used\r\n", "Sand 12% used\r\n", "On-demand $0.00 used\r\n", "Autopilot 9% used\r\n"} {
		p := cursorProtocol{state: 3}
		body := strings.Replace(cursorScreen, "Esc to close", extra+"Esc to close", 1)
		input, report, err := feedCursorFrame(&p, body)
		if err != nil || input != "" || report == nil {
			t.Fatal("extra row changed acceptance", extra)
		}
		if report.IncludedPercentDisplay == nil || *report.IncludedPercentDisplay != 61 || report.Plan != "fixture-tier" {
			t.Fatal("extra row changed the captured values", extra)
		}
		raw, err := json.Marshal(report)
		if err != nil || strings.Contains(string(raw), "Grok") || strings.Contains(string(raw), "Sand") || strings.Contains(string(raw), "12") {
			t.Fatal("extra row leaked into the capture", extra)
		}
	}
}
