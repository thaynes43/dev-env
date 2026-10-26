package session

import (
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

// unknownActivity is a view=activity object with nothing measured.
const unknownActivity = `"activity":{"mode":"remote","agentState":"idle","requestedModel":"claude-opus-5-5","usage":{"scope":"Unknown","resultState":"Unknown","servedModels":[],"inputTokens":{"state":"Unknown"},"outputTokens":{"state":"Unknown"},"cacheReadInputTokens":{"state":"Unknown"},"cacheCreationInputTokens":{"state":"Unknown"},"cost":{"state":"Unknown","kind":"Unknown"}}}`

const reportedActivity = `"activity":{"mode":"task","agentState":"busy","lastActivity":"2026-10-25T11:58:00Z","requestedModel":"claude-opus-5-5","usage":{"scope":"InitialManagedInvocation","resultState":"Partial","servedModels":["claude-haiku-4-5","claude-opus-5-5"],"inputTokens":{"state":"Reported","amount":1234567},"outputTokens":{"state":"Reported","amount":0},"cacheReadInputTokens":{"state":"Reported","amount":5000},"cacheCreationInputTokens":{"state":"Unknown"},"cost":{"state":"Reported","amount":"0.75","unit":"USD","kind":"ProviderCLIEquivalentEstimate"}}}`

func activityPage(rows ...string) string {
	return `{"sessions":[` + strings.Join(rows, ",") + `],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-10-25T11:59:00Z"}`
}

func activityRow(name, activity string) string {
	return `{"name":"` + name + `","uid":"uid-` + name + `","repo":"dev-env","phase":"Running","observedAt":"2026-10-25T11:59:00Z","freshness":"observed",` + activity + `}`
}

func TestSessionActivityRendersSeparateUsageWithoutTotals(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	page, err := decodeSessionPage([]byte(activityPage(activityRow("worker", reportedActivity), activityRow("remote", unknownActivity))), now)
	if err != nil || len(page.Sessions) != 2 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	product := portal.ProductPage{Title: "Sessions", Notice: "Connected session summaries.", Note: sessionUsageNote, Connected: true, ObservedAt: "2026-10-25T12:00:00Z"}
	for _, row := range page.Sessions {
		product.Rows = append(product.Rows, sessionRow(row))
	}
	fields := func(row portal.ProductRow) map[string]string {
		out := map[string]string{}
		for _, f := range row.Fields {
			out[f.Label] = f.Value
		}
		return out
	}
	worker := fields(product.Rows[0])
	for label, want := range map[string]string{
		"Agent state":                      "busy",
		"Last activity":                    "2026-10-25T11:58:00Z",
		"Mode":                             "task",
		"Requested model":                  "claude-opus-5-5",
		"Served model (provider reported)": "claude-haiku-4-5, claude-opus-5-5",
		"Usage scope":                      "First managed invocation (partial result)",
		"Input tokens":                     "1,234,567",
		"Output tokens":                    "0",
		"Cache read tokens":                "5,000",
		"Cache create tokens":              "Unknown",
		"Cost estimate":                    "0.75 USD (CLI-equivalent estimate, not a charge)",
	} {
		if worker[label] != want {
			t.Errorf("worker %s = %q, want %q", label, worker[label], want)
		}
	}
	remote := fields(product.Rows[1])
	for _, label := range []string{"Last activity", "Served model (provider reported)", "Usage scope", "Input tokens", "Output tokens", "Cache read tokens", "Cache create tokens", "Cost estimate"} {
		if remote[label] != "Unknown" {
			t.Errorf("remote %s = %q, want Unknown", label, remote[label])
		}
	}
	if remote["Requested model"] != "claude-opus-5-5" || remote["Mode"] != "remote" {
		t.Fatalf("remote %+v", remote)
	}
	body, err := portal.RenderProduct(product)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if !strings.Contains(html, "first managed invocation") || !strings.Contains(html, "not a charge") || !strings.Contains(html, "Unknown, not zero") {
		t.Fatalf("coverage note missing: %s", html)
	}
	// No total of any two counters or of the served models appears.
	for _, total := range []string{"1,239,567", "1239567", "1,234,567 total", "Total"} {
		if strings.Contains(html, total) {
			t.Fatalf("rendered a total %q", total)
		}
	}
	disconnected, err := portal.RenderProduct(portal.ProductPage{Title: "Sessions", Notice: "Sessions unavailable", Note: sessionUsageNote})
	if err != nil || strings.Contains(string(disconnected), "first managed invocation") {
		t.Fatalf("coverage note shown without connected rows: %s", disconnected)
	}
}

func TestSessionActivityDecoderFailsClosed(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	good := activityRow("worker", reportedActivity)
	if _, err := decodeSessionPage([]byte(activityPage(good)), now); err != nil {
		t.Fatal(err)
	}
	legacy := `{"name":"worker","uid":"uid-worker","repo":"dev-env","phase":"Running","observedAt":"2026-10-25T11:59:00Z","freshness":"observed"}`
	cases := map[string]string{
		"legacy row without activity": legacy,
		"activity null":               strings.Replace(good, reportedActivity, `"activity":null`, 1),
		"extra row key":               strings.Replace(good, `"phase":"Running"`, `"phase":"Running","prompt":"SECRET"`, 1),
	}
	mutate := map[string][2]string{
		"activity extra key":         {`"mode":"task"`, `"mode":"task","prompt":"SECRET"`},
		"mode outside set":           {`"mode":"task"`, `"mode":"admin"`},
		"mode null":                  {`"mode":"task"`, `"mode":null`},
		"agent state case":           {`"agentState":"busy"`, `"agentState":"Busy"`},
		"model empty":                {`"requestedModel":"claude-opus-5-5"`, `"requestedModel":""`},
		"model alias with space":     {`"requestedModel":"claude-opus-5-5"`, `"requestedModel":"opus latest"`},
		"model missing":              {`"requestedModel":"claude-opus-5-5",`, ``},
		"last activity null":         {`"lastActivity":"2026-10-25T11:58:00Z"`, `"lastActivity":null`},
		"last activity future":       {`"lastActivity":"2026-10-25T11:58:00Z"`, `"lastActivity":"2026-10-25T13:00:00Z"`},
		"usage extra key":            {`"scope":"InitialManagedInvocation"`, `"scope":"InitialManagedInvocation","total":1239567`},
		"usage null":                 {`"usage":{"scope"`, `"usage":null,"x":{"scope"`},
		"scope outside set":          {`"scope":"InitialManagedInvocation"`, `"scope":"AllTurns"`},
		"result outside set":         {`"resultState":"Partial"`, `"resultState":"Done"`},
		"measured result Unknown":    {`"resultState":"Partial"`, `"resultState":"Unknown"`},
		"served models null":         {`"servedModels":["claude-haiku-4-5","claude-opus-5-5"]`, `"servedModels":null`},
		"served models duplicate":    {`"servedModels":["claude-haiku-4-5","claude-opus-5-5"]`, `"servedModels":["claude-opus-5-5","claude-opus-5-5"]`},
		"served model not string":    {`"servedModels":["claude-haiku-4-5","claude-opus-5-5"]`, `"servedModels":[5]`},
		"served models over bound":   {`"servedModels":["claude-haiku-4-5","claude-opus-5-5"]`, `"servedModels":["a","b","c","d","e","f","g","h","i"]`},
		"reported without amount":    {`"inputTokens":{"state":"Reported","amount":1234567}`, `"inputTokens":{"state":"Reported"}`},
		"reported amount null":       {`"inputTokens":{"state":"Reported","amount":1234567}`, `"inputTokens":{"state":"Reported","amount":null}`},
		"negative amount":            {`"inputTokens":{"state":"Reported","amount":1234567}`, `"inputTokens":{"state":"Reported","amount":-1}`},
		"amount over bound":          {`"inputTokens":{"state":"Reported","amount":1234567}`, `"inputTokens":{"state":"Reported","amount":1000000000000000001}`},
		"fractional amount":          {`"inputTokens":{"state":"Reported","amount":1234567}`, `"inputTokens":{"state":"Reported","amount":1.5}`},
		"unknown with amount":        {`"cacheCreationInputTokens":{"state":"Unknown"}`, `"cacheCreationInputTokens":{"state":"Unknown","amount":0}`},
		"token extra key":            {`"cacheCreationInputTokens":{"state":"Unknown"}`, `"cacheCreationInputTokens":{"state":"Unknown","note":"x"}`},
		"token state outside set":    {`"cacheCreationInputTokens":{"state":"Unknown"}`, `"cacheCreationInputTokens":{"state":"Zero"}`},
		"cost billed kind":           {`"kind":"ProviderCLIEquivalentEstimate"`, `"kind":"Billed"`},
		"cost other unit":            {`"unit":"USD"`, `"unit":"EUR"`},
		"cost amount null":           {`"amount":"0.75"`, `"amount":null`},
		"cost amount not decimal":    {`"amount":"0.75"`, `"amount":"$0.75"`},
		"cost unknown with amount":   {`"cost":{"state":"Reported"`, `"cost":{"state":"Unknown"`},
		"unknown scope with reports": {`"scope":"InitialManagedInvocation"`, `"scope":"Unknown"`},
	}
	for name, m := range mutate {
		if !strings.Contains(good, m[0]) {
			t.Fatalf("%s: fixture lacks %s", name, m[0])
		}
		cases[name] = strings.Replace(good, m[0], m[1], 1)
	}
	unknownRow := activityRow("remote", unknownActivity)
	for name, m := range map[string][2]string{
		"unknown scope with served model": {`"servedModels":[]`, `"servedModels":["claude-opus-5-5"]`},
		"unknown scope with zero":         {`"inputTokens":{"state":"Unknown"}`, `"inputTokens":{"state":"Reported","amount":0}`},
		"unknown scope with result":       {`"resultState":"Unknown"`, `"resultState":"Final"`},
		"unknown cost without kind":       {`"cost":{"state":"Unknown","kind":"Unknown"}`, `"cost":{"state":"Unknown"}`},
	} {
		cases[name] = strings.Replace(unknownRow, m[0], m[1], 1)
	}
	for name, row := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSessionPage([]byte(activityPage(row)), now); err == nil {
				t.Fatalf("accepted %s", row)
			}
		})
	}
}

func TestGroupDigits(t *testing.T) {
	for in, want := range map[string]string{"0": "0", "999": "999", "1000": "1,000", "1234567": "1,234,567", "1000000000000000000": "1,000,000,000,000,000,000"} {
		if got := groupDigits(in); got != want {
			t.Errorf("groupDigits(%s) = %s, want %s", in, got, want)
		}
	}
}
