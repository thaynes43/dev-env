package usagecollector

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// cursorProtocol accepts only the qualified normal interactive builtin. Each
// input follows a separately observed state. It never submits a model prompt,
// accepts an authentication dialog, or services a private RPC.
type cursorProtocol struct {
	workspace string
	trust     bool
	state     int
	terminal  cursorTerminal
	dirty     bool
	total     int
}

const cursorUsageDashboardLabel = "View in dashboard: cursor.com/dashboard?tab=usage"

var cursorUsageTitle = regexp.MustCompile(`(?m)Usage\s*[•·]\s*([A-Za-z][A-Za-z0-9_-]{0,63})\b`)
var cursorUsageReset = regexp.MustCompile(`\bResets ((?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) [1-9][0-9]?)\b`)
var cursorInlineUsageMenu = regexp.MustCompile(`^→ /usage[ \t]+Show plan and on-demand usage$`)

func (p *cursorProtocol) accept(raw []byte) (string, *providerallowance.CursorCLIUsageCapture, error) {
	p.total += len(raw)
	if p.total > 256<<10 {
		return "", nil, refused("command_output_oversized", ErrCapture)
	}
	if err := p.terminal.feed(raw); err != nil {
		return "", nil, err
	}
	p.dirty = true
	return "", nil, nil
}

// settle may authorize input only after the process has produced no output for
// the runner's bounded quiet interval. A partial/synchronized frame cannot act.
func (p *cursorProtocol) settle() (input string, report *providerallowance.CursorCLIUsageCapture, err error) {
	if !p.dirty {
		return "", nil, nil
	}
	text, complete := p.terminal.visible()
	if !complete {
		return "", nil, nil
	}
	p.dirty = false
	lines := map[string]bool{}
	inlineUsageRows := 0
	selectedRows := 0
	dashboardFooterRows := 0
	visibleLines := strings.Split(text, "\n")
	active := strings.Trim(visibleLines[p.terminal.row], " \t│┃")
	for row, line := range visibleLines {
		if row != p.terminal.row {
			line = strings.Trim(line, " \t│┃")
			lines[line] = true
			if line == cursorUsageDashboardLabel {
				dashboardFooterRows++
			}
			if strings.HasPrefix(line, "→") {
				selectedRows++
			}
			if cursorInlineUsageMenu.MatchString(line) {
				inlineUsageRows++
			}
		}
	}
	lower := strings.ToLower(text)
	for _, marker := range []string{"sign in", "log in", "login", "authenticate", "credential", "api key", "permission", "terms", "continue", "confirm", "approve tool", "allow command"} {
		if strings.Contains(lower, marker) {
			return "", nil, refused("builtin_prompt_refused", ErrCapture)
		}
	}
	// The qualified final display contains one literal query marker in its
	// inactive dashboard footer. Exempt only that exact row and only after the
	// selected builtin has produced a complete usage display. Count every other
	// question mark, including active, adjacent and duplicate footer text.
	if questions := strings.Count(text, "?"); questions != 0 {
		knownFooter := questions == 1 && dashboardFooterRows == 1 && (p.state == 3 || p.state == 4) && lines["Esc to close"] && cursorUsageTitle.MatchString(text)
		if !knownFooter {
			return "", nil, refused("builtin_prompt_refused", ErrCapture)
		}
	}
	advance := func(state int, value string) (string, *providerallowance.CursorCLIUsageCapture, error) {
		knownInput := active == "" || active == ">" || active == "→" || p.state == 2 && (active == "/usage" || active == "> /usage" || active == "→ /usage")
		if !knownInput {
			return "", nil, refused("builtin_input_unqualified", ErrCapture)
		}
		p.state = state
		return value, nil, nil
	}
	switch p.state {
	case 0:
		if lines["Workspace Trust Required"] {
			if !p.trust || !lines[p.workspace] || !lines["[a] Trust this workspace"] || !lines["[q] Quit"] {
				return "", nil, refused("builtin_workspace_unqualified", ErrCapture)
			}
			return advance(1, "a")
		}
		fallthrough
	case 1:
		if (lines["Cursor Agent"] && lines["v"+providerallowance.CursorCLIQualifiedVersion] || lines["Cursor Agent v"+providerallowance.CursorCLIQualifiedVersion]) && lines["Plan, search, build anything"] && lines[p.workspace] {
			return advance(2, "/usage")
		}
	case 2:
		twoRows := lines["→ /usage"] && lines["Show plan and on-demand usage"]
		// Autocomplete can put the exact selected command and description
		// on one inactive row. Never accept an active row, another command,
		// extra text, or multiple selected rows. The input fence still runs.
		if inlineUsageRows > 0 && (inlineUsageRows != 1 || selectedRows != 1 || lines["Show plan and on-demand usage"]) {
			return "", nil, refused("builtin_input_unqualified", ErrCapture)
		}
		if twoRows || inlineUsageRows == 1 {
			return advance(3, "\r")
		}
	case 3:
		// Loading may be transient or absent for a fast/cached builtin. A new,
		// complete screen after the selected command is display evidence only;
		// neither Loading nor a new screen establishes provider refresh/time.
		if lines["Loading usage data..."] || cursorUsageTitle.MatchString(text) {
			p.state = 4
		}
		fallthrough
	case 4:
		if p.state != 4 || !lines["Esc to close"] {
			return "", nil, nil
		}
		titles, resets := cursorUsageTitle.FindAllStringSubmatch(text, -1), cursorUsageReset.FindAllStringSubmatch(text, -1)
		if len(titles) != 1 || len(resets) != 1 || !strings.Contains(text, "Category") || !strings.Contains(text, "Current") {
			return "", nil, refused("builtin_display_unqualified", ErrCapture)
		}
		var included *int
		rows := map[string]int{}
		for _, line := range strings.Split(text, "\n") {
			for _, name := range []string{"Included", "Auto", "API"} {
				line = strings.Trim(line, " \t│┃")
				if !strings.HasPrefix(line, name+" ") && !strings.HasPrefix(line, name+"\t") {
					continue
				}
				rows[name]++
				matches := regexp.MustCompile(`^`+name+`[ \t]+(100|[1-9]?[0-9])% used(?:[ \t]|$)`).FindAllStringSubmatch(line, -1)
				if len(matches) != 1 {
					return "", nil, refused("builtin_display_unqualified", ErrCapture)
				}
				if name == "Included" {
					value, _ := strconv.Atoi(matches[0][1])
					included = &value
				}
			}
		}
		if rows["Included"] != 1 || rows["Auto"] != 1 || rows["API"] != 1 || included == nil {
			return "", nil, refused("builtin_display_unqualified", ErrCapture)
		}
		p.state = 5
		reset := resets[0][1]
		return "", &providerallowance.CursorCLIUsageCapture{Schema: providerallowance.CursorCLICaptureSchema, RendererVersion: providerallowance.CursorCLIQualifiedVersion, Plan: titles[0][1], IncludedPercentDisplay: included, PercentPrecision: "rounded_integer_display", ResetMonthDay: &reset, Period: "not_exposed"}, nil
	default:
		return "", nil, refused("builtin_state_unqualified", ErrCapture)
	}
	return "", nil, nil
}
