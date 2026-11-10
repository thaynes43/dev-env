package portal

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

var claimsObserved = time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)

func claimItem(n byte, state, freshness string, beat *time.Time, created time.Time) apiv1.PortalTaskClaimItem {
	hex := strings.Repeat(string("0123456789abcdef"[n%16]), 32)
	return apiv1.PortalTaskClaimItem{Name: "task-claim." + hex + "." + hex, UID: "00000000-0000-4000-8000-0000000000" + string("0123456789abcdef"[n%16]) + "0",
		Repository: "dev-env", TaskKey: "task:story-" + string('a'+n), GitHub: "thaynes43/dev-env", CreatedAt: created,
		Owner: apiv1.PortalTaskClaimOwner{Name: "owner-" + string('a'+n), UID: "11111111-0000-4000-8000-00000000000" + string('0'+n%10), State: state, Freshness: freshness, HeartbeatAt: beat}}
}

func claimsRead(items ...apiv1.PortalTaskClaimItem) apiv1.PortalTaskClaims {
	if items == nil {
		items = []apiv1.PortalTaskClaimItem{}
	}
	return apiv1.PortalTaskClaims{Version: apiv1.PortalTaskClaimsVersion, Project: "dev-env", State: apiv1.PortalTaskClaimsStateAvailable, Reason: apiv1.PortalTaskClaimsReasonComplete, Items: items, Limit: 100, Complete: true, ObservedAt: claimsObserved}
}

func ago(d time.Duration) *time.Time {
	at := claimsObserved.Add(-d)
	return &at
}

func renderClaimsBody(t *testing.T, page ClaimsPage) string {
	t.Helper()
	if page.Project == "" {
		page.Project = "dev-env"
	}
	body, policy, err := RenderClaims(page, 1<<20)
	if err != nil {
		t.Fatalf("%s: %v", page.State, err)
	}
	if policy != ClaimsPolicy {
		t.Fatal("policy changed")
	}
	return string(body)
}

func TestClaimsEveryPageStateRenders(t *testing.T) {
	empty := claimsRead()
	empty.State = apiv1.PortalTaskClaimsStateEmpty
	for _, test := range []struct {
		page ClaimsPage
		want []string
	}{
		{ClaimsPage{State: ClaimsNotConnected}, []string{"Claims are not connected."}},
		{ClaimsPage{State: ClaimsRefused}, []string{"The operator refused this read.", "This is not an empty list."}},
		{ClaimsPage{State: ClaimsNotServed}, []string{"This operator does not serve claims to the portal."}},
		{ClaimsPage{State: ClaimsUnavailable}, []string{"Claims could not be read.", "Unknown, not empty.", "Refresh claims"}},
		{ClaimsPage{State: ClaimsUnconfigured}, []string{"Claims are not enabled on the operator."}},
		{ClaimsPage{State: ClaimsOutOfScope}, []string{"dev-env is outside the portal&#39;s claim scope."}},
		{ClaimsPage{State: ClaimsEmpty, Read: empty}, []string{`No TaskClaims recorded for dev-env at <time datetime="2026-11-02T15:00:00Z">2 Nov 2026, 15:00 UTC</time>.`, "Work without a claim is not shown."}},
		{ClaimsPage{State: ClaimsAvailable, Read: claimsRead(claimItem(1, "Running", "observed", ago(time.Minute), claimsObserved.Add(-time.Hour)))}, []string{"<table", "Claim held", "Active", "On this page: 1 claim · 1 Active"}},
	} {
		body := renderClaimsBody(t, test.page)
		for _, want := range append(test.want, "It is not a grant, an approval or permission to act.") {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %q", test.page.State, want)
			}
		}
		if test.page.State != ClaimsAvailable && (strings.Contains(body, "<table") || strings.Contains(body, "Active")) {
			t.Errorf("%s shows claims", test.page.State)
		}
		if strings.Contains(body, "<form") || strings.Contains(body, "<button") || strings.Contains(body, "method=") || strings.Contains(body, "<script") {
			t.Errorf("%s has a control", test.page.State)
		}
		for _, banned := range []string{"released", "Released", "free to take", "available to take", "Approved", "granted"} {
			if strings.Contains(body, banned) {
				t.Errorf("%s says %q", test.page.State, banned)
			}
		}
	}
}

func TestClaimsOwnerLabels(t *testing.T) {
	for _, test := range []struct {
		state, freshness string
		beat             *time.Time
		want             string
	}{
		{"Running", "observed", ago(time.Minute), "Active"},
		{"Idle", "observed", ago(15 * time.Minute), "Active"},
		{"Draining", "observed", ago(time.Minute), "Draining"},
		{"Pending", "unknown", nil, "Starting"},
		{"Pending", "observed", ago(time.Minute), "Starting"},
		{"Running", "stale", ago(time.Hour), "Stale since"},
		{"Draining", "stale", ago(time.Hour), "Stale since"},
		{"Idle", "unknown", nil, "Unknown (no heartbeat)"},
		{"Suspended", "stale", ago(time.Hour), "Suspended"},
		{"Archived", "unknown", nil, "Ended (Archived)"},
		{"Failed", "stale", ago(time.Hour), "Ended (Failed)"},
		{"Deleting", "unknown", nil, "Ended (Deleting)"},
		{"NotFound", "unknown", nil, "Owner not found"},
		{"Replaced", "unknown", nil, "Owner not found"},
		{"Unknown", "unknown", nil, "Unknown owner"},
		// Words or evidence outside the table never become Active.
		{"Active", "observed", ago(time.Minute), "Unknown owner"},
		{"Running", "observed", nil, "Unknown owner"},
		{"Running", "observed", ago(16 * time.Minute), "Unknown owner"},
		{"Running", "observed", ago(-time.Minute), "Unknown owner"},
		{"Running", "fresh", ago(time.Minute), "Unknown owner"},
		{"Running", "stale", ago(time.Minute), "Unknown owner"},
		{"", "observed", ago(time.Minute), "Unknown owner"},
	} {
		got := ownerLabel(apiv1.PortalTaskClaimOwner{State: test.state, Freshness: test.freshness, HeartbeatAt: test.beat}, claimsObserved)
		if got.Label != test.want {
			t.Errorf("%s/%s: %q, want %q", test.state, test.freshness, got.Label, test.want)
		}
	}
}

func TestClaimsUnknownOwnersNeverRenderActive(t *testing.T) {
	read := claimsRead(
		claimItem(1, "Unknown", "unknown", nil, claimsObserved.Add(-time.Hour)),
		claimItem(2, "NotFound", "unknown", nil, claimsObserved.Add(-2*time.Hour)),
		claimItem(3, "Replaced", "unknown", nil, claimsObserved.Add(-3*time.Hour)),
		claimItem(4, "Running", "unknown", nil, claimsObserved.Add(-4*time.Hour)),
		claimItem(5, "Running", "stale", ago(2*time.Hour), claimsObserved.Add(-5*time.Hour)),
	)
	body := renderClaimsBody(t, ClaimsPage{State: ClaimsAvailable, Read: read})
	if strings.Contains(body, "Active") {
		t.Fatal("an unknown or stale owner rendered Active")
	}
	if strings.Count(body, "Claim held") != 5 || !strings.Contains(body, `Stale since <time datetime="2026-11-02T13:00:00Z">2 Nov 2026, 13:00 UTC</time>`) {
		t.Fatal("rows lost the held claim or stale time")
	}
	if !strings.Contains(body, "On this page: 5 claims · 1 Stale · 1 Unknown (no heartbeat) · 2 Owner not found · 1 Unknown owner") {
		t.Fatal("label counts wrong")
	}
}

func TestClaimsOrderAndPaging(t *testing.T) {
	older := claimItem(1, "Running", "observed", ago(time.Minute), claimsObserved.Add(-5*time.Hour))
	newer := claimItem(2, "Running", "observed", ago(time.Minute), claimsObserved.Add(-time.Hour))
	complete := renderClaimsBody(t, ClaimsPage{State: ClaimsAvailable, Read: claimsRead(older, newer)})
	if strings.Index(complete, newer.Name) > strings.Index(complete, older.Name) || strings.Contains(complete, "More claims exist") || strings.Contains(complete, "Next page") {
		t.Fatal("a complete page is newest first with no next page")
	}
	truncated := claimsRead(older, newer)
	truncated.Reason, truncated.Complete, truncated.NextCursor = apiv1.PortalTaskClaimsReasonTruncated, false, "c3ludGhldGlj"
	body := renderClaimsBody(t, ClaimsPage{State: ClaimsAvailable, Read: truncated})
	if strings.Index(body, older.Name) > strings.Index(body, newer.Name) || !strings.Contains(body, "More claims exist. Order is not sorted across pages.") {
		t.Fatal("a truncated page keeps store order and says so")
	}
	if !strings.Contains(body, `href="/portal/projects/dev-env/environments/internal/claims?cursor=c3ludGhldGlj">Next page</a>`) || strings.Contains(body, "First page") {
		t.Fatal("next page link wrong")
	}
	later := renderClaimsBody(t, ClaimsPage{State: ClaimsAvailable, Read: claimsRead(older), Cursor: true})
	if !strings.Contains(later, ">First page</a>") || !strings.Contains(later, "This is a later page.") {
		t.Fatal("a later page lacks its way back")
	}
	zero := claimsRead()
	zero.Reason, zero.Complete, zero.NextCursor = apiv1.PortalTaskClaimsReasonTruncated, false, "abc"
	if body := renderClaimsBody(t, ClaimsPage{State: ClaimsAvailable, Read: zero}); !strings.Contains(body, "No claims on this page.") || strings.Contains(body, "No TaskClaims recorded") {
		t.Fatal("a truncated page with no items is not Empty")
	}
}

func TestClaimsRefusesStatesTheReadDoesNotSupport(t *testing.T) {
	empty := claimsRead()
	empty.State = apiv1.PortalTaskClaimsStateEmpty
	unknown := claimsRead()
	unknown.State, unknown.Reason, unknown.Complete = apiv1.PortalTaskClaimsStateUnknown, apiv1.PortalTaskClaimsReasonUnavailable, false
	other := claimsRead(claimItem(1, "Running", "observed", ago(time.Minute), claimsObserved))
	other.Project = "other"
	for name, page := range map[string]ClaimsPage{
		"empty after a cursor":          {State: ClaimsEmpty, Read: empty, Cursor: true},
		"available from unknown":        {State: ClaimsAvailable, Read: unknown},
		"empty from unknown":            {State: ClaimsEmpty, Read: unknown},
		"first complete page not Empty": {State: ClaimsAvailable, Read: claimsRead()},
		"empty from available":          {State: ClaimsEmpty, Read: claimsRead(claimItem(1, "Running", "observed", ago(time.Minute), claimsObserved))},
		"other project":                 {State: ClaimsAvailable, Read: other},
		"invalid read":                  {State: ClaimsAvailable, Read: apiv1.PortalTaskClaims{}},
		"unnamed state":                 {State: "partial"},
	} {
		page.Project = "dev-env"
		if body, _, err := RenderClaims(page, 1<<20); !errors.Is(err, ErrUnavailable) || body != nil {
			t.Errorf("%s rendered", name)
		}
	}
	if _, _, err := RenderClaims(ClaimsPage{State: ClaimsRefused}, 1<<20); err == nil {
		t.Fatal("a page without its project rendered")
	}
	if _, _, err := RenderClaims(ClaimsPage{State: ClaimsRefused, Project: "dev-env"}, 0); err == nil {
		t.Fatal("no limit rendered")
	}
}

func TestClaimsLinksGoOnlyToGitHubAndSessions(t *testing.T) {
	issue := claimItem(1, "Running", "observed", ago(time.Minute), claimsObserved.Add(-time.Hour))
	issue.TaskKey = "issue:42"
	task := claimItem(2, "Running", "observed", ago(time.Minute), claimsObserved.Add(-2*time.Hour))
	task.TaskKey = "task:op359"
	upgrade := claimItem(3, "Running", "observed", ago(time.Minute), claimsObserved.Add(-3*time.Hour))
	upgrade.TaskKey = "task:upgrade.pr12." + strings.Repeat("a", 40)
	body := renderClaimsBody(t, ClaimsPage{State: ClaimsAvailable, Read: claimsRead(issue, task, upgrade)})
	if !strings.Contains(body, `href="https://github.com/thaynes43/dev-env/issues/42" target="_blank" rel="noopener noreferrer">GitHub issue #42</a>`) || strings.Count(body, "No tracker link") != 2 {
		t.Fatal("tracker links wrong")
	}
	if !strings.Contains(body, `href="/portal/projects/dev-env/environments/internal/sessions?session=11111111-0000-4000-8000-000000000001"`) {
		t.Fatal("owner link missing")
	}
	for _, href := range regexp.MustCompile(`href="([^"]*)"`).FindAllStringSubmatch(body, -1) {
		link := href[1]
		if !strings.HasPrefix(link, "/portal/projects/dev-env/environments/internal/") && !strings.HasPrefix(link, "https://github.com/thaynes43/dev-env/issues/") && link != "#claims" && !strings.HasPrefix(link, "#i-") {
			t.Fatalf("unexpected link %q", link)
		}
	}
	bad := claimItem(4, "Running", "observed", ago(time.Minute), claimsObserved)
	bad.Owner.UID = strings.Repeat("a", 65)
	if body := renderClaimsBody(t, ClaimsPage{State: ClaimsAvailable, Read: claimsRead(bad)}); strings.Contains(body, "?session=") {
		t.Fatal("an owner UID outside the Sessions grammar was linked")
	}
}

func TestClaimsSinceUsesOnlySourceTimes(t *testing.T) {
	future := claimItem(1, "Running", "observed", ago(time.Minute), claimsObserved.Add(time.Hour))
	body := renderClaimsBody(t, ClaimsPage{State: ClaimsAvailable, Read: claimsRead(future, claimItem(2, "Running", "observed", ago(time.Minute), claimsObserved.Add(-26*time.Hour-5*time.Minute)))})
	if !strings.Contains(body, "Held for an unknown time") || !strings.Contains(body, "Held 1d 2h when observed") || !strings.Contains(body, `<time datetime="2026-11-01T12:55:00Z">1 Nov 2026, 12:55 UTC</time>`) {
		t.Fatal("since column wrong")
	}
	if strings.Contains(body, `datetime="2026-11-02T16:00:00Z"`) || strings.Contains(body, `datetime=""`) || !strings.Contains(body, "<span>Unknown</span>") {
		t.Fatal("a creation time after the read is shown as a time")
	}
	if claimAge(claimsObserved.Add(-30*time.Second), claimsObserved) != "Held under a minute when observed" || claimAge(claimsObserved.Add(-75*time.Minute), claimsObserved) != "Held 1h 15m when observed" {
		t.Fatal("age format")
	}
}

func TestClaimsPolicyNavigationAndStyle(t *testing.T) {
	digest := sha256.Sum256([]byte(viewsStyle + claimsStyle))
	if !strings.Contains(ClaimsPolicy, "style-src 'sha256-"+base64.StdEncoding.EncodeToString(digest[:])+"'") || !strings.Contains(ClaimsPolicy, "form-action 'none'") || !strings.Contains(ClaimsPolicy, "default-src 'none'") {
		t.Fatal("claims policy")
	}
	if strings.Contains(claimsStyle, "/*") || strings.Contains(claimsStyle, "*/") {
		t.Fatal("a CSS comment breaks the CSP hash")
	}
	body := renderClaimsBody(t, ClaimsPage{State: ClaimsRefused})
	meta := strings.Split(strings.Split(body, `http-equiv="Content-Security-Policy" content="`)[1], `"`)[0]
	if html.UnescapeString(meta) != strings.TrimSuffix(ClaimsPolicy, "; frame-ancestors 'none'") || strings.Contains(meta, "frame-ancestors") {
		t.Fatal("meta policy differs from the header")
	}
	if strings.Count(body, `href="/portal/projects/dev-env/environments/internal/claims" aria-current="page"`) != 2 {
		t.Fatal("Claims is not current in both navigations")
	}
	for _, other := range []string{"sessions", "tasks", "asks", "pages"} {
		if strings.Contains(body, `href="/portal/projects/dev-env/environments/internal/`+other+`" aria-current`) {
			t.Fatalf("%s marked current", other)
		}
	}
	if !strings.Contains(body, `<symbol id="i-claims"`) {
		t.Fatal("Claims icon missing")
	}
}

func TestClaimsNavigationOnlyWhenConfigured(t *testing.T) {
	const href = `href="/portal/projects/dev-env/environments/internal/claims"`
	count := func(body []byte) int { return strings.Count(string(body), href) }
	for _, claims := range []bool{false, true} {
		want := 0
		if claims {
			want = 2
		}
		asks, _, err := RenderAsks(AsksPage{Notice: "Asks are not connected", ClaimsNav: claims}, 1<<20)
		if err != nil || count(asks) != want || strings.Count(string(asks), `internal/asks" aria-current="page"`) != 2 {
			t.Fatalf("asks claims=%v: %d links, %v", claims, count(asks), err)
		}
		product, err := RenderProduct(ProductPage{Title: "Sessions", Notice: "Sessions not connected", ClaimsNav: claims})
		if err != nil || count(product) != want {
			t.Fatalf("product claims=%v: %d links, %v", claims, count(product), err)
		}
		pages, err := RenderPages(PagesWorkspace{ClaimsNav: claims}, 1<<20)
		if err != nil || count(pages) != want {
			t.Fatalf("pages claims=%v: %d links, %v", claims, count(pages), err)
		}
	}
	for _, target := range []string{"product-nav", "product-mobile-nav", "product-icons"} {
		if viewsTemplate.Lookup(target).Tree == claimsTemplate.Lookup(target).Tree || viewsTemplate.Lookup(target).Tree == asksClaimsTemplate.Lookup(target).Tree ||
			viewsTemplate.Lookup(target).Tree == pagesClaimsTemplate.Lookup(target).Tree || viewsTemplate.Lookup(target).Tree == viewsClaimsTemplate.Lookup(target).Tree {
			t.Fatalf("%s shares a mutable parse tree", target)
		}
	}
}

// The Claims nav variant must stay the shared nav plus exactly the Claims link.
func TestClaimsNavVariantMatchesSharedNav(t *testing.T) {
	lines := strings.Split(viewsSource, "\n")
	find := func(prefix string) string {
		for _, line := range lines {
			if strings.HasPrefix(line, prefix) {
				return line
			}
		}
		t.Fatalf("%s missing", prefix)
		return ""
	}
	top := find(`{{define "product-nav"}}`)
	mobile := find(`{{define "product-mobile-nav"}}`)
	topLink := `<a href="/portal/projects/dev-env/environments/internal/claims"{{if eq . "claims"}} aria-current="page"{{end}}>Claims</a>`
	mobileLink := `<a href="/portal/projects/dev-env/environments/internal/claims"{{if eq . "claims"}} aria-current="page"{{end}}><svg class="i"><use href="#i-claims"/></svg>Claims</a>`
	wantTop := strings.Replace(strings.Replace(top, `"product-nav"`, `"product-nav-claims"`, 1), ">Asks</a>", ">Asks</a>"+topLink, 1)
	wantMobile := strings.Replace(strings.Replace(mobile, `"product-mobile-nav"`, `"product-mobile-nav-claims"`, 1), "Asks</a>", "Asks</a>"+mobileLink, 1)
	if find(`{{define "product-nav-claims"}}`) != wantTop || find(`{{define "product-mobile-nav-claims"}}`) != wantMobile {
		t.Fatal("the Claims nav variant drifted from the shared nav")
	}
	if strings.Contains(top, "/claims") || strings.Contains(mobile, "/claims") {
		t.Fatal("the shared nav links Claims unconditionally")
	}
}
