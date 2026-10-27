package session

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

const (
	openProjectPortalOrigin = "https://openproject-web.frontend.svc.cluster.local:8443"
	openProjectPortalHost   = "openproject-web.frontend.svc.cluster.local"
	openProjectProjectPath  = "/api/v3/projects/3/work_packages"
	operatorPortalOrigin    = "https://dev-env-operator.dev-env-system.svc.cluster.local:8443"
	operatorPortalHost      = "dev-env-operator.dev-env-system.svc.cluster.local"
	productPageSize         = 50
	productPageCap          = 4
	// sessionPageCap bounds the Sessions read at 500 sessions; the operator
	// pages every repository's sessions in the namespace.
	sessionPageCap        = 10
	productHALLimit int64 = 1 << 20
)

// SessionSource reads portal session summaries. The operator token is read on
// every request so a one-hour projected token can rotate.
type SessionSource struct {
	path   string
	client *http.Client
	now    func() time.Time
}

// TaskSource reads project 3's work-package collection. It does not follow an
// arbitrary HAL origin or path.
type TaskSource struct {
	path   string
	client *http.Client
	now    func() time.Time
}

func newSessionSource(tokenPath string, ca []byte, now func() time.Time, dial func(context.Context, string, string) (net.Conn, error)) (*SessionSource, error) {
	client, err := pinnedClient(ca, operatorPortalHost, 5*time.Second, dial)
	if err != nil || now == nil || tokenPath == "" {
		return nil, portal.ErrUnavailable
	}
	return &SessionSource{path: tokenPath, client: client, now: now}, nil
}

func newTaskSource(tokenPath string, ca []byte, now func() time.Time, dial func(context.Context, string, string) (net.Conn, error)) (*TaskSource, error) {
	client, err := pinnedClient(ca, openProjectPortalHost, 5*time.Second, dial)
	if err != nil || now == nil || tokenPath == "" {
		return nil, portal.ErrUnavailable
	}
	return &TaskSource{path: tokenPath, client: client, now: now}, nil
}

func (s *SessionSource) Read(ctx context.Context, filter portal.ProductFilter) (portal.ProductPage, error) {
	page := portal.ProductPage{Title: "Sessions", Connected: true}
	if s == nil || s.client == nil || s.now == nil {
		return page, portal.ErrUnavailable
	}
	token, err := operatorCredential(s.path, s.now())
	if err != nil {
		return page, portal.ErrUnavailable
	}
	cursor := ""
	complete := false
	freshness := "observed"
	var sessions []apiv1.PortalSessionSummary
	for n := 0; n < sessionPageCap; n++ {
		raw := operatorPortalOrigin + "/v1/sessions?limit=" + strconv.Itoa(productPageSize) + "&view=" + apiv1.PortalViewActivity
		if cursor != "" {
			raw += "&cursor=" + url.QueryEscape(cursor)
		}
		body, err := productGet(ctx, s.client, raw, token, 256<<10, mediaJSON)
		if err != nil {
			return page, err
		}
		decoded, err := decodeSessionPage(body, s.now())
		if err != nil {
			return page, err
		}
		freshness = worstFreshness(freshness, decoded.Freshness)
		sessions = append(sessions, decoded.Sessions...)
		complete = decoded.Complete
		cursor = decoded.NextCursor
		if complete {
			break
		}
	}
	return sessionsPage(sessions, complete, freshness, s.now(), filter), nil
}

func (s *TaskSource) Read(ctx context.Context, filter portal.ProductFilter) (portal.ProductPage, error) {
	page := portal.ProductPage{Title: "Tasks", Connected: true}
	if s == nil || s.client == nil || s.now == nil {
		return page, portal.ErrUnavailable
	}
	token, err := readBearerFile(s.path)
	if err != nil {
		return page, portal.ErrUnavailable
	}
	seen := map[int]struct{}{}
	total := 0
	haveTotal := false
	complete := false
	var all []portal.ProductRow
	for pageNo := 1; pageNo <= productPageCap; pageNo++ {
		body, err := productGet(ctx, s.client, taskCollectionURL(pageNo), token, productHALLimit, mediaHAL)
		if err != nil {
			return page, err
		}
		rows, pageTotal, done, err := decodeTaskPage(body, pageNo, seen, s.now())
		if err != nil || (haveTotal && pageTotal != total) {
			return page, portal.ErrUnavailable
		}
		total = pageTotal
		haveTotal = true
		all = append(all, rows...)
		if done {
			complete = true
			break
		}
	}
	return tasksPage(all, complete, filter), nil
}

func decodeSessionPage(body []byte, now time.Time) (apiv1.PortalSessionPage, error) {
	var page apiv1.PortalSessionPage
	obj, err := jsonObject(body)
	if err != nil {
		return page, err
	}
	for _, key := range []string{"sessions", "limit", "complete", "freshness", "observedAt"} {
		if _, ok := obj[key]; !ok {
			return page, portal.ErrUnavailable
		}
	}
	for key := range obj {
		switch key {
		case "sessions", "limit", "complete", "freshness", "observedAt", "nextCursor":
		default:
			return page, portal.ErrUnavailable
		}
	}
	limit, ok := jsonInt(obj["limit"])
	if !ok || limit < 1 || limit > 100 || limit != productPageSize {
		return page, portal.ErrUnavailable
	}
	complete, ok := jsonBool(obj["complete"])
	if !ok {
		return page, portal.ErrUnavailable
	}
	var freshness string
	if json.Unmarshal(obj["freshness"], &freshness) != nil || (freshness != "observed" && freshness != "stale" && freshness != "unknown") {
		return page, portal.ErrUnavailable
	}
	if _, err := observationTime(obj["observedAt"], now); err != nil {
		return page, err
	}
	rows, ok := jsonArray(obj["sessions"])
	if !ok || len(rows) > limit {
		return page, portal.ErrUnavailable
	}
	names, uids := map[string]struct{}{}, map[string]struct{}{}
	page.Sessions = make([]apiv1.PortalSessionSummary, 0, len(rows))
	for _, raw := range rows {
		row, err := decodeSessionRow(raw, now)
		if err != nil {
			return apiv1.PortalSessionPage{}, err
		}
		if _, dup := names[row.Name]; dup {
			return page, portal.ErrUnavailable
		}
		if _, dup := uids[row.UID]; dup {
			return page, portal.ErrUnavailable
		}
		names[row.Name] = struct{}{}
		uids[row.UID] = struct{}{}
		page.Sessions = append(page.Sessions, row)
	}
	next := ""
	if raw, ok := obj["nextCursor"]; ok {
		if json.Unmarshal(raw, &next) != nil || next == "" || len(next) > 2048 || strings.ContainsAny(next, " \t\r\n/?#") {
			return page, portal.ErrUnavailable
		}
	}
	if complete == (next != "") {
		return page, portal.ErrUnavailable
	}
	page.Limit = limit
	page.Complete = complete
	page.Freshness = freshness
	page.NextCursor = next
	observed, _ := observationTime(obj["observedAt"], now)
	page.ObservedAt = observed
	return page, nil
}

func decodeSessionRow(raw json.RawMessage, now time.Time) (apiv1.PortalSessionSummary, error) {
	var row apiv1.PortalSessionSummary
	obj, err := jsonObject(raw)
	if err != nil {
		return row, err
	}
	// A row is the six base keys, or those six and a valid activity object.
	// Without activity every activity field renders Unknown.
	if !exactKeys(obj, []string{"name", "uid", "repo", "phase", "observedAt", "freshness"}, "activity") {
		return row, portal.ErrUnavailable
	}
	var name, uid, repo, phase, freshness string
	if json.Unmarshal(obj["name"], &name) != nil || json.Unmarshal(obj["uid"], &uid) != nil || json.Unmarshal(obj["repo"], &repo) != nil || json.Unmarshal(obj["phase"], &phase) != nil || json.Unmarshal(obj["freshness"], &freshness) != nil {
		return row, portal.ErrUnavailable
	}
	if name == "" || uid == "" || !validProject(repo) || !sessionPhase(phase) || (freshness != "observed" && freshness != "stale" && freshness != "unknown") {
		return row, portal.ErrUnavailable
	}
	observed, err := observationTime(obj["observedAt"], now)
	if err != nil {
		return row, err
	}
	row = apiv1.PortalSessionSummary{Name: name, UID: uid, Repo: repo, Phase: phase, ObservedAt: observed, Freshness: freshness}
	if rawActivity, ok := obj["activity"]; ok {
		activity, err := decodeSessionActivity(rawActivity, now)
		if err != nil {
			return apiv1.PortalSessionSummary{}, err
		}
		row.Activity = &activity
	}
	return row, nil
}

func sessionPhase(phase string) bool {
	switch phase {
	case "Pending", "Running", "Idle", "Draining", "Suspended", "Archived", "Failed":
		return true
	default:
		return false
	}
}

func decodeTaskPage(body []byte, page int, seen map[int]struct{}, now time.Time) ([]portal.ProductRow, int, bool, error) {
	obj, err := jsonObject(body)
	if err != nil {
		return nil, 0, false, err
	}
	for _, key := range []string{"total", "count", "pageSize", "offset", "_embedded", "_links"} {
		if _, ok := obj[key]; !ok {
			return nil, 0, false, portal.ErrUnavailable
		}
	}
	total, okT := jsonInt(obj["total"])
	count, okC := jsonInt(obj["count"])
	size, okS := jsonInt(obj["pageSize"])
	offset, okO := jsonInt(obj["offset"])
	if !okT || !okC || !okS || !okO || total < 0 || count < 0 || count > productPageSize || size != productPageSize || offset != page {
		return nil, 0, false, portal.ErrUnavailable
	}
	embedded, err := jsonObject(obj["_embedded"])
	if err != nil {
		return nil, 0, false, err
	}
	rawElements, ok := embedded["elements"]
	if !ok {
		return nil, 0, false, portal.ErrUnavailable
	}
	elements, ok := jsonArray(rawElements)
	if !ok || len(elements) != count {
		return nil, 0, false, portal.ErrUnavailable
	}
	// The collection's own paging links are neither followed nor checked: the
	// reader builds every page URL itself, and OpenProject 17 points them at
	// /api/v3/workspaces/3 with extra query keys.
	if _, err := jsonObject(obj["_links"]); err != nil {
		return nil, 0, false, err
	}
	rows := make([]portal.ProductRow, 0, len(elements))
	for _, raw := range elements {
		var element halWorkPackage
		if json.Unmarshal(raw, &element) != nil {
			return nil, 0, false, portal.ErrUnavailable
		}
		if _, dup := seen[element.ID]; dup {
			return nil, 0, false, portal.ErrUnavailable
		}
		row, err := taskRow(element, now)
		if err != nil {
			return nil, 0, false, err
		}
		seen[element.ID] = struct{}{}
		rows = append(rows, row)
	}
	expected := total - (page-1)*productPageSize
	last := count < productPageSize || page*productPageSize >= total
	if last {
		if count != expected {
			return nil, 0, false, portal.ErrUnavailable
		}
		return rows, total, true, nil
	}
	return rows, total, false, nil
}

func jsonObject(data []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, portal.ErrUnavailable
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		keyTok, err := dec.Token()
		key, ok := keyTok.(string)
		if err != nil || !ok {
			return nil, portal.ErrUnavailable
		}
		if _, dup := out[key]; dup {
			return nil, portal.ErrUnavailable
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, portal.ErrUnavailable
		}
		out[key] = raw
	}
	tok, err = dec.Token()
	if err != nil || tok != json.Delim('}') {
		return nil, portal.ErrUnavailable
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, portal.ErrUnavailable
	}
	return out, nil
}

func jsonInt(raw json.RawMessage) (int, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if dec.Decode(&value) != nil {
		return 0, false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return 0, false
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(number.String())
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func jsonBool(raw json.RawMessage) (bool, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var value any
	if dec.Decode(&value) != nil {
		return false, false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return false, false
	}
	valueBool, ok := value.(bool)
	return valueBool, ok
}

func jsonArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, false
	}
	return values, true
}

func worstFreshness(current, next string) string {
	if sessionFreshnessRank(next) > sessionFreshnessRank(current) {
		return next
	}
	return current
}

func sessionFreshnessRank(value string) int {
	switch value {
	case "observed":
		return 0
	case "stale":
		return 1
	default:
		return 2
	}
}

func observationTime(raw json.RawMessage, now time.Time) (time.Time, error) {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return time.Time{}, portal.ErrUnavailable
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil || parsed.After(now) {
		return time.Time{}, portal.ErrUnavailable
	}
	return parsed.UTC(), nil
}

type halWorkPackage struct {
	ID           int    `json:"id"`
	Subject      string `json:"subject"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
	CustomField1 string `json:"customField1"` // Accountable owner
	CustomField2 string `json:"customField2"` // Execution owner
	CustomField3 string `json:"customField3"` // Repositories
	Description  struct {
		Raw string `json:"raw"`
	} `json:"description"`
	Links struct {
		Project struct {
			Href string `json:"href"`
		} `json:"project"`
		Status struct {
			Title string `json:"title"`
		} `json:"status"`
		Type struct {
			Title string `json:"title"`
		} `json:"type"`
		Parent struct {
			Title string `json:"title"`
		} `json:"parent"`
		Assignee struct {
			Title string `json:"title"`
		} `json:"assignee"`
		Responsible struct {
			Title string `json:"title"`
		} `json:"responsible"`
	} `json:"_links"`
	Embedded struct {
		Project struct {
			ID         int    `json:"id"`
			Identifier string `json:"identifier"`
		} `json:"project"`
	} `json:"_embedded"`
}

// taskSummaryRunes bounds the description excerpt; the full text stays in
// OpenProject behind the row's link.
const taskSummaryRunes = 600

func taskRow(element halWorkPackage, now time.Time) (portal.ProductRow, error) {
	if element.ID < 1 || !projectThree(element) {
		return portal.ProductRow{}, portal.ErrUnavailable
	}
	id := strconv.Itoa(element.ID)
	status := shown(element.Links.Status.Title)
	kind := shown(element.Links.Type.Title)
	repos := shown(element.CustomField3)
	owner := taskOwner(element.CustomField2)
	if owner == "" && strings.TrimSpace(element.CustomField1) != "" {
		owner = shown(element.CustomField1)
	}
	updatedAgo, age := "updated Unknown", "Unknown"
	if updated, err := time.Parse(time.RFC3339, element.UpdatedAt); err == nil {
		updatedAgo, age = "updated "+portal.Ago(updated, now), compactAgo(&updated, now)
	}
	kicker, badges := "#"+id, []string{updatedAgo}
	if kind != "Unknown" {
		kicker += " · " + kind
		badges = []string{kind, updatedAgo}
	}
	description := ""
	if parent := element.Links.Parent.Title; strings.TrimSpace(parent) != "" {
		description = "in " + shown(parent)
	} else if repos != "Unknown" {
		description = repos
	}
	if owner != "" {
		badges = append(badges, owner)
	}
	summary := portal.ProductSection{Title: "Summary", Note: taskSummary(element.Description.Raw)}
	if summary.Note == "" {
		summary.Note = "No description."
	}
	var people []portal.ProductField
	for _, f := range []portal.ProductField{
		{Label: "Execution owner", Value: element.CustomField2},
		{Label: "Accountable owner", Value: element.CustomField1},
		{Label: "Assignee", Value: element.Links.Assignee.Title},
		{Label: "Responsible", Value: element.Links.Responsible.Title},
	} {
		if strings.TrimSpace(f.Value) != "" {
			f.Value = shown(f.Value)
			people = append(people, f)
		}
	}
	sections := []portal.ProductSection{summary}
	if len(people) > 0 {
		sections = append(sections, portal.ProductSection{Title: "Ownership", Fields: people})
	}
	sections = append(sections, portal.ProductSection{Title: "Dates", Fields: []portal.ProductField{
		{Label: "Created", Value: shownDate(element.CreatedAt)},
		{Label: "Updated", Value: shownDate(element.UpdatedAt)},
	}})
	context := []portal.ProductField{
		{Label: "Work package", Value: "#" + id, Mono: true},
		{Label: "Type", Value: kind},
		{Label: "Status", Value: status},
	}
	if parent := element.Links.Parent.Title; strings.TrimSpace(parent) != "" {
		context = append(context, portal.ProductField{Label: "Parent", Value: shown(parent)})
	}
	context = append(context, portal.ProductField{Label: "Repositories", Value: repos})
	return portal.ProductRow{
		Title: shown(element.Subject), Kicker: kicker, Crumb: "#" + id,
		Link: "https://work.haynesnetwork.com/work_packages/" + id, LinkLabel: "Open in OpenProject",
		Status: portal.ProductStatus{Text: status, Tone: taskTone(status)}, Description: description, Meta: owner, Age: age,
		Badges: badges, Sections: sections, ContextTitle: "Work package", Context: context,
	}, nil
}

// taskOwner is the list's short owner: the worktree name from opwork's
// "agent | worktree <name> | branch <branch>", or else the first part of a
// free-form Execution owner, at most taskOwnerRunes runes. The detail shows
// the full value.
func taskOwner(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var parts []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == '|' || r == '/' }) {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	owner := shown(parts[0])
	for _, part := range parts {
		if name, ok := strings.CutPrefix(part, "worktree "); ok && strings.TrimSpace(name) != "" {
			owner = shown(name)
			break
		}
	}
	if runes := []rune(owner); len(runes) > taskOwnerRunes {
		owner = strings.TrimSpace(string(runes[:taskOwnerRunes])) + "…"
	}
	return owner
}

const taskOwnerRunes = 40

var (
	markdownLink = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	// Underscores are kept: snake_case names are common in these descriptions.
	markdownMarks = regexp.MustCompile("(?m)^\\s*(#{1,6}|[-*>]|[0-9]+\\.)\\s+|[*`]{1,3}")
)

// taskSummary is a plain-text excerpt of a work package's Markdown
// description: link text without its URL, no heading or list marks, one
// line, at most taskSummaryRunes runes.
func taskSummary(raw string) string {
	text := markdownLink.ReplaceAllString(raw, "$1")
	text = markdownMarks.ReplaceAllString(text, "")
	text = strings.Join(strings.FieldsFunc(text, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if runes := []rune(text); len(runes) > taskSummaryRunes {
		text = strings.TrimSpace(string(runes[:taskSummaryRunes])) + "…"
	}
	return text
}

// taskTone colors a work-package status by what it asks of a reader. An
// unrecognized status is muted, never guessed.
func taskTone(status string) string {
	switch taskGroup(status) {
	case 0:
		return "wait"
	case 1:
		return "busy"
	case 2:
		return "info"
	default:
		return "muted"
	}
}

var taskGroups = []portal.ProductGroup{{Title: "Needs attention", Attention: true}, {Title: "In progress"}, {Title: "Planned"}, {Title: "Closed"}, {Title: "Other status"}}

func taskGroup(status string) int {
	switch strings.ToLower(status) {
	case "needs answer", "needs approval", "on hold", "paused", "blocked", "feedback":
		return 0
	case "in progress", "in review", "developed", "tested":
		return 1
	case "new", "draft", "ungroomed", "scheduled", "specified", "planned", "confirmed", "to be scheduled":
		return 2
	case "closed", "done", "accepted", "resolved", "rejected":
		return 3
	default:
		return 4
	}
}

// tasksPage groups work packages by status, keeping the collection's order
// inside each group. Counts appear only for a complete read.
func tasksPage(rows []portal.ProductRow, complete bool, filter portal.ProductFilter) portal.ProductPage {
	page := portal.ProductPage{Title: "Tasks", ListTitle: "All tasks", Connected: true, Complete: complete, Filter: portal.ProductFilter{Attention: filter.Attention}}
	ordered := make([][]portal.ProductRow, len(taskGroups))
	for _, row := range rows {
		g := taskGroup(row.Status.Text)
		ordered[g] = append(ordered[g], row)
	}
	page.Rail.All, page.Rail.Attention = strconv.Itoa(len(rows)), strconv.Itoa(len(ordered[0]))
	listed := len(rows)
	if filter.Attention {
		page.ListTitle, listed = "Needs attention", len(ordered[0])
		ordered = append([][]portal.ProductRow{ordered[0]}, make([][]portal.ProductRow, len(taskGroups)-1)...)
	}
	switch {
	case !complete:
		page.Notice, page.ListCount = "Tasks read is incomplete. This is not the full inventory.", "Partial list"
	case len(rows) == 0:
		page.Notice = "The completed task read returned no work packages."
	case listed == 0:
		page.Notice = "Nothing needs attention."
	default:
		page.Notice, page.Quiet = "Connected project 3 work packages.", true
		page.ListCount = strconv.Itoa(listed) + " work packages · haynes-engineering"
	}
	page.Groups = make([]portal.ProductGroup, len(taskGroups))
	copy(page.Groups, taskGroups)
	n := 0
	for g := range ordered {
		for _, row := range ordered[g] {
			n++
			row.ID = "t" + strconv.Itoa(n)
			page.Groups[g].Rows = append(page.Groups[g].Rows, row)
		}
	}
	return page
}

func projectThree(element halWorkPackage) bool {
	if element.Links.Project.Href != "/api/v3/projects/3" {
		return false
	}
	if element.Embedded.Project.ID == 0 && element.Embedded.Project.Identifier == "" {
		return true
	}
	return element.Embedded.Project.ID == 3 && element.Embedded.Project.Identifier == "haynes-engineering"
}

func taskCollectionURL(page int) string {
	return openProjectPortalOrigin + openProjectProjectPath + "?offset=" + strconv.Itoa(page) + "&pageSize=" + strconv.Itoa(productPageSize) + "&filters=%5B%5D"
}

func productGet(ctx context.Context, client *http.Client, rawURL, token string, limit int64, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil || client == nil || !bearerText(token) {
		return nil, portal.ErrUnavailable
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil || response == nil {
		return nil, portal.ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
		return nil, portal.ErrUnavailable
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != accept {
		return nil, portal.ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, portal.ErrUnavailable
	}
	return body, nil
}

func operatorCredential(path string, now time.Time) (string, error) {
	raw, err := readBearerFile(path)
	if err != nil || !operatorTokenCurrent(raw, now) {
		return "", portal.ErrUnavailable
	}
	return raw, nil
}

func operatorTokenCurrent(raw string, now time.Time) bool {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Aud json.RawMessage `json:"aud"`
		Exp int64           `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp <= now.Unix() {
		return false
	}
	var one string
	var many []string
	if json.Unmarshal(claims.Aud, &one) == nil {
		many = []string{one}
	} else if json.Unmarshal(claims.Aud, &many) != nil || len(many) != 1 {
		return false
	}
	return len(many) == 1 && many[0] == apiv1.TokenAudience
}

func shown(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	if value == "" {
		return "Unknown"
	}
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}

// shownDate is an RFC 3339 time for people, in UTC; anything else is Unknown.
func shownDate(value string) string {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "Unknown"
	}
	return parsed.UTC().Format("2 Jan 2006, 15:04 UTC")
}

// productFilter reads a product page's one optional filter: show=attention on
// either page, or project=<repository> on Sessions. Any other query, a second
// key or value, or a non-canonical spelling is refused.
func productFilter(u *url.URL) (portal.ProductFilter, bool) {
	if u.RawQuery == "" {
		return portal.ProductFilter{}, true
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 1 || q.Encode() != u.RawQuery {
		return portal.ProductFilter{}, false
	}
	switch {
	case len(q["show"]) == 1 && q.Get("show") == "attention":
		return portal.ProductFilter{Attention: true}, true
	case len(q["project"]) == 1 && u.Path == SessionsPath && validProject(q.Get("project")):
		return portal.ProductFilter{Project: q.Get("project")}, true
	}
	return portal.ProductFilter{}, false
}

func serveProduct(w http.ResponseWriter, r *http.Request, c AssemblyConfig) {
	authHeaders(w)
	filter, ok := productFilter(r.URL)
	if r.Method != http.MethodGet || !ok || r.URL.RawPath != "" || c.Session == nil || c.Authorizer == nil || c.Now == nil {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	principal, err := c.Session.Authenticate(r)
	if errors.Is(err, portal.ErrDenied) {
		// No current session: start sign-in. A signed-in user who lacks access
		// is refused below by Authorize and never loops back here.
		w.Header().Set("Location", "/auth/login")
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	if err != nil {
		authFailure(w, err)
		return
	}
	resource := portal.Resource{Project: "dev-env", Environment: "internal"}
	if err = c.Authorizer.Authorize(r.Context(), principal, resource); err != nil {
		authFailure(w, err)
		return
	}
	var page portal.ProductPage
	switch r.URL.Path {
	case SessionsPath:
		page = portal.ProductPage{Title: "Sessions", Notice: "Sessions not connected"}
		if c.Sessions != nil {
			page, err = c.Sessions.Read(r.Context(), filter)
		}
	case TasksPath:
		page = portal.ProductPage{Title: "Tasks", Notice: "Tasks not connected"}
		if c.Tasks != nil {
			page, err = c.Tasks.Read(r.Context(), filter)
		}
	default:
		authFailure(w, portal.ErrDenied)
		return
	}
	if err != nil {
		title := "Sessions"
		if r.URL.Path == TasksPath {
			title = "Tasks"
		}
		page = portal.ProductPage{Title: title, Notice: title + " unavailable"}
	}
	if current, currentErr := c.Session.Authenticate(r); currentErr != nil || current != principal {
		authFailure(w, portal.ErrDenied)
		return
	}
	if err = c.Authorizer.Authorize(r.Context(), principal, resource); err != nil {
		authFailure(w, err)
		return
	}
	page.ObservedAt = c.Now().UTC().Format(time.RFC3339)
	body, err := portal.RenderProduct(page)
	if err != nil {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	// The rendered page allows exactly its embedded stylesheet; refusals keep authHeaders' policy.
	w.Header().Set("Content-Security-Policy", portal.ProductPolicy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}
