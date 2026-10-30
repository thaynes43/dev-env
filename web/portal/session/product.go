package session

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
	sessionReadTimeout       = 12 * time.Second
	sessionPageCap           = 10
	productHALLimit    int64 = 1 << 20
)

// SessionSource reads portal session summaries. The operator token is read on
// every request so a one-hour projected token can rotate.
type SessionSource struct {
	path          string
	client        *http.Client
	now           func() time.Time
	conversations ConversationSource
	lastSuccessMu sync.Mutex
	lastSuccess   time.Time
	logger        *slog.Logger
}

// TaskSource reads project 3's work-package collection. It does not follow an
// arbitrary HAL origin or path.
type TaskSource struct {
	path          string
	client        *http.Client
	now           func() time.Time
	lastSuccessMu sync.Mutex
	lastSuccess   time.Time
	logger        *slog.Logger
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

// sessionReadError is shared by Sessions and Tasks. It carries only fixed
// codes and integer positions, never source
// errors, response values, URLs, credentials or headers. RowIndex is zero-based;
// -1 means the failure belongs to the page envelope or transport.
type sessionReadError struct{ portal.ProductReadFailure }

func (e *sessionReadError) Error() string { return e.ReasonCode }
func (e *sessionReadError) Unwrap() error { return portal.ErrUnavailable }

func readFailure(code string) *sessionReadError {
	return &sessionReadError{portal.ProductReadFailure{ReasonCode: code, RowIndex: -1}}
}

func sessionFailure(err error) *sessionReadError {
	var failure *sessionReadError
	if errors.As(err, &failure) {
		copy := *failure
		return &copy
	}
	return readFailure("response_contract")
}

func sessionTransportFailure(ctx context.Context, err error) *sessionReadError {
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return readFailure("cancelled")
	}
	failure := readFailure("operator_unreachable")
	var netErr net.Error
	failure.TimedOut = errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout())
	return failure
}

// sessionGet and productGet preserve classified failure categories for Sessions
// and Tasks. Both use pinned, non-redirecting clients.
func sessionGet(ctx context.Context, client *http.Client, rawURL, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, readFailure("response_contract")
	}
	req.Header.Set("Accept", mediaJSON)
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return nil, sessionTransportFailure(ctx, err)
	}
	if response == nil {
		return nil, readFailure("operator_unreachable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		failure := readFailure("operator_refused")
		failure.HTTPStatus = response.StatusCode
		return nil, failure
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != mediaJSON {
		return nil, readFailure("response_contract")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if err != nil {
		return nil, sessionTransportFailure(ctx, err)
	}
	if len(body) > 256<<10 {
		return nil, readFailure("response_contract")
	}
	return body, nil
}

type sessionReadStats struct {
	started                                                 time.Time
	preRead, credential, fetch, decode, projection, history time.Duration
	page                                                    int
}

// finishRead keeps skipped and attempted reads under the same safe diagnostics
// contract, including this process's last successful read.
func (s *SessionSource) finishRead(ctx context.Context, filter portal.ProductFilter, page portal.ProductPage, err error, stats sessionReadStats) (portal.ProductPage, error) {
	observed := time.Now().UTC()
	logger := slog.Default()
	lastSuccess := time.Time{}
	if s != nil {
		if s.now != nil {
			observed = s.now().UTC()
		}
		if s.logger != nil {
			logger = s.logger
		}
		s.lastSuccessMu.Lock()
		if err == nil && observed.After(s.lastSuccess) {
			s.lastSuccess = observed
		}
		lastSuccess = s.lastSuccess
		s.lastSuccessMu.Unlock()
	}
	if err == nil {
		if !observed.IsZero() {
			page.ReadCompletedAt = observed.Format(time.RFC3339Nano)
		}
		return page, nil
	}
	failure := sessionFailure(err)
	failure.Page = stats.page
	failure.ObservedAt = observed.Format(time.RFC3339Nano)
	if !lastSuccess.IsZero() {
		failure.LastSuccessAt = lastSuccess.Format(time.RFC3339Nano)
	}
	err = failure
	notice := "Sessions unavailable"
	if failure.ReasonCode == "not_connected" {
		notice = "Sessions not connected"
	}
	page = portal.ProductPage{Title: "Sessions", Notice: notice, Filter: filter, Allowance: disconnectedAllowances(), ReadFailure: &failure.ProductReadFailure}
	level := slog.LevelWarn
	if failure.ReasonCode == "not_connected" || failure.ReasonCode == "cancelled" {
		level = slog.LevelInfo
	}
	logger.LogAttrs(ctx, level, "portal_session_read_failed",
		slog.String("reason_code", failure.ReasonCode), slog.Int("page", failure.Page), slog.Int("row_index", failure.RowIndex), slog.Int("http_status", failure.HTTPStatus), slog.Bool("timed_out", failure.TimedOut), slog.Int64("elapsed_ms", time.Since(stats.started).Milliseconds()),
		slog.Int64("pre_read_ms", stats.preRead.Milliseconds()), slog.Int64("credential_ms", stats.credential.Milliseconds()), slog.Int64("fetch_ms", stats.fetch.Milliseconds()), slog.Int64("decode_ms", stats.decode.Milliseconds()), slog.Int64("projection_ms", stats.projection.Milliseconds()), slog.Int64("history_ms", stats.history.Milliseconds()))
	return page, err
}

// skippedRead records an exhausted pre-read budget without reading credentials
// or contacting the operator. Cancellation remains distinct from budget expiry.
func (s *SessionSource) skippedRead(ctx context.Context, filter portal.ProductFilter, started time.Time) (portal.ProductPage, error) {
	failure := readFailure("read_not_started")
	if errors.Is(ctx.Err(), context.Canceled) {
		failure = readFailure("cancelled")
	}
	return s.finishRead(ctx, filter, portal.ProductPage{}, failure, sessionReadStats{started: started, preRead: time.Since(started)})
}

func (s *SessionSource) Read(ctx context.Context, filter portal.ProductFilter) (portal.ProductPage, error) {
	return s.read(ctx, filter, time.Now())
}

func (s *SessionSource) read(ctx context.Context, filter portal.ProductFilter, requestStarted time.Time) (page portal.ProductPage, err error) {
	stats := sessionReadStats{started: requestStarted, preRead: time.Since(requestStarted)}
	ctx, cancel := context.WithTimeout(ctx, sessionReadTimeout)
	defer cancel()
	defer func() { page, err = s.finishRead(ctx, filter, page, err, stats) }()

	if ctx.Err() != nil {
		return page, sessionTransportFailure(ctx, ctx.Err())
	}
	if s == nil || s.client == nil || s.now == nil {
		return page, readFailure("not_connected")
	}
	step := time.Now()
	token, err := operatorCredential(s.path, s.now())
	stats.credential = time.Since(step)
	if err != nil {
		return page, readFailure("credential_unavailable")
	}
	cursor, complete, freshness := "", false, "observed"
	var sessions []apiv1.PortalSessionSummary
	for n := 0; n < sessionPageCap; n++ {
		stats.page = 0
		if ctx.Err() != nil {
			return page, sessionTransportFailure(ctx, ctx.Err())
		}
		raw := operatorPortalOrigin + "/v1/sessions?limit=" + strconv.Itoa(productPageSize) + "&view=" + apiv1.PortalViewActivity
		if cursor != "" {
			raw += "&cursor=" + url.QueryEscape(cursor)
		}
		stats.page = n + 1
		step = time.Now()
		body, getErr := sessionGet(ctx, s.client, raw, token)
		stats.fetch += time.Since(step)
		if getErr != nil {
			return page, getErr
		}
		step = time.Now()
		decoded, decodeErr := decodeSessionPage(body, s.now())
		stats.decode += time.Since(step)
		if decodeErr != nil {
			return page, decodeErr
		}
		freshness = worstFreshness(freshness, decoded.Freshness)
		sessions = append(sessions, decoded.Sessions...)
		complete, cursor = decoded.Complete, decoded.NextCursor
		if complete {
			break
		}
	}
	// There is no inventory page in flight during projection or history.
	stats.page = 0
	step = time.Now()
	page = sessionsPage(sessions, complete, freshness, s.now(), filter)
	stats.projection = time.Since(step)
	step = time.Now()
	page = withHistory(ctx, page, filter.Session, s.conversations, s.now())
	stats.history = time.Since(step)
	if ctx.Err() != nil {
		return page, sessionTransportFailure(ctx, ctx.Err())
	}
	return page, nil
}

// finishRead uses the shared safe failure model, but keeps Tasks success state
// independent of Sessions. Partial rows are never retained after a failed read.
func (s *TaskSource) finishRead(ctx context.Context, filter portal.ProductFilter, page portal.ProductPage, err error, stats sessionReadStats) (portal.ProductPage, error) {
	observed := time.Now().UTC()
	logger := slog.Default()
	lastSuccess := time.Time{}
	if s != nil {
		if s.now != nil {
			observed = s.now().UTC()
		}
		if s.logger != nil {
			logger = s.logger
		}
		s.lastSuccessMu.Lock()
		if err == nil && observed.After(s.lastSuccess) {
			s.lastSuccess = observed
		}
		lastSuccess = s.lastSuccess
		s.lastSuccessMu.Unlock()
	}
	if err == nil {
		if !observed.IsZero() {
			page.ReadCompletedAt = observed.Format(time.RFC3339Nano)
		}
		return page, nil
	}
	// Copy only allowlisted codes and bounded numeric diagnostics into the log.
	failure := readFailure("unknown")
	var classified *sessionReadError
	if errors.As(err, &classified) {
		switch classified.ReasonCode {
		case "not_connected", "credential_unavailable", "operator_unreachable", "operator_refused", "response_contract", "clock_ahead", "cancelled":
			failure.ReasonCode = classified.ReasonCode
		}
		if classified.RowIndex >= 0 && classified.RowIndex < productPageSize {
			failure.RowIndex = classified.RowIndex
		}
		if classified.HTTPStatus >= 100 && classified.HTTPStatus <= 599 {
			failure.HTTPStatus = classified.HTTPStatus
		}
		failure.TimedOut = classified.TimedOut
	}
	if stats.page > 0 && stats.page <= productPageCap {
		failure.Page = stats.page
	}
	failure.ObservedAt = observed.Format(time.RFC3339Nano)
	if !lastSuccess.IsZero() {
		failure.LastSuccessAt = lastSuccess.Format(time.RFC3339Nano)
	}
	notice := "Tasks unavailable"
	level := slog.LevelWarn
	if failure.ReasonCode == "not_connected" {
		notice = "Tasks not connected"
	}
	if failure.ReasonCode == "not_connected" || failure.ReasonCode == "cancelled" {
		level = slog.LevelInfo
	}
	logger.LogAttrs(ctx, level, "portal_task_read_failed",
		slog.String("reason_code", failure.ReasonCode), slog.Int("page", failure.Page), slog.Int("row_index", failure.RowIndex), slog.Int("http_status", failure.HTTPStatus), slog.Bool("timed_out", failure.TimedOut), slog.Int64("elapsed_ms", time.Since(stats.started).Milliseconds()),
		slog.Int64("credential_ms", stats.credential.Milliseconds()), slog.Int64("fetch_ms", stats.fetch.Milliseconds()), slog.Int64("decode_ms", stats.decode.Milliseconds()), slog.Int64("projection_ms", stats.projection.Milliseconds()))
	return portal.ProductPage{Title: "Tasks", Notice: notice, Filter: filter, ReadFailure: &failure.ProductReadFailure}, failure
}

func (s *TaskSource) Read(ctx context.Context, filter portal.ProductFilter) (page portal.ProductPage, err error) {
	stats := sessionReadStats{started: time.Now()}
	defer func() { page, err = s.finishRead(ctx, filter, page, err, stats) }()
	if s == nil || s.client == nil || s.now == nil {
		return page, readFailure("not_connected")
	}
	step := time.Now()
	token, err := readBearerFile(s.path)
	stats.credential = time.Since(step)
	if err != nil {
		return page, readFailure("credential_unavailable")
	}
	seen := map[int]struct{}{}
	total := 0
	haveTotal := false
	complete := false
	var all []portal.ProductRow
	for pageNo := 1; pageNo <= productPageCap; pageNo++ {
		stats.page = pageNo
		step = time.Now()
		body, err := productGet(ctx, s.client, taskCollectionURL(pageNo), token, productHALLimit, mediaHAL)
		stats.fetch += time.Since(step)
		if err != nil {
			return page, err
		}
		step = time.Now()
		rows, pageTotal, done, err := decodeTaskPage(body, pageNo, seen, s.now())
		stats.decode += time.Since(step)
		if err != nil {
			return page, err
		}
		if haveTotal && pageTotal != total {
			return page, readFailure("response_contract")
		}
		total = pageTotal
		haveTotal = true
		all = append(all, rows...)
		if done {
			complete = true
			break
		}
	}
	stats.page = 0
	step = time.Now()
	page = tasksPage(all, complete, filter)
	stats.projection = time.Since(step)
	return page, nil
}

func decodeSessionPage(body []byte, now time.Time) (page apiv1.PortalSessionPage, err error) {
	defer func() {
		if err != nil {
			err = sessionFailure(err)
		}
	}()
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
	for i, raw := range rows {
		row, err := decodeSessionRow(raw, now)
		if err != nil {
			failure := sessionFailure(err)
			failure.RowIndex = i
			return apiv1.PortalSessionPage{}, failure
		}
		if _, dup := names[row.Name]; dup {
			failure := readFailure("response_contract")
			failure.RowIndex = i
			return page, failure
		}
		if _, dup := uids[row.UID]; dup {
			failure := readFailure("response_contract")
			failure.RowIndex = i
			return page, failure
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

func decodeSessionRow(raw json.RawMessage, now time.Time) (row apiv1.PortalSessionSummary, err error) {
	defer func() {
		if err != nil {
			err = sessionFailure(err)
		}
	}()
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

func decodeTaskPage(body []byte, page int, seen map[int]struct{}, now time.Time) (rows []portal.ProductRow, total int, complete bool, err error) {
	defer func() {
		if err != nil {
			err = sessionFailure(err)
		}
	}()
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
	rows = make([]portal.ProductRow, 0, len(elements))
	for i, raw := range elements {
		var element halWorkPackage
		if json.Unmarshal(raw, &element) != nil {
			failure := readFailure("response_contract")
			failure.RowIndex = i
			return nil, 0, false, failure
		}
		if _, dup := seen[element.ID]; dup {
			failure := readFailure("response_contract")
			failure.RowIndex = i
			return nil, 0, false, failure
		}
		row, err := taskRow(element, now)
		if err != nil {
			failure := sessionFailure(err)
			failure.RowIndex = i
			return nil, 0, false, failure
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
	if err != nil {
		return time.Time{}, portal.ErrUnavailable
	}
	if parsed.After(now.Add(2 * time.Second)) {
		return time.Time{}, readFailure("clock_ahead")
	}
	if parsed.After(now) {
		parsed = now
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
	listOwner := taskListOwner(element.CustomField2, element.CustomField1)
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
	if listOwner != "Owner not recorded" {
		badges = append(badges, listOwner)
	}
	fixture := taskFixture(element.Description.Raw)
	if fixture {
		badges = append([]string{"Fixture"}, badges...)
	}
	summary := portal.ProductSection{Title: "Summary", Note: taskSummary(element.Description.Raw)}
	if summary.Note == "" {
		summary.Note = "No description."
	}
	if fixture {
		summary.States = []portal.ProductState{{
			Status: portal.ProductStatus{Text: "Fixture", Tone: "muted"},
			Text:   "Synthetic fixture",
		}}
	}
	people := []portal.ProductField{
		{Label: "Execution owner", Value: recordedOwner(element.CustomField2)},
		{Label: "Accountable owner", Value: recordedOwner(element.CustomField1)},
	}
	for _, f := range []portal.ProductField{
		{Label: "Assignee", Value: element.Links.Assignee.Title},
		{Label: "Responsible", Value: element.Links.Responsible.Title},
	} {
		if strings.TrimSpace(f.Value) != "" {
			f.Value = shown(f.Value)
			people = append(people, f)
		}
	}
	sections := []portal.ProductSection{summary, {Title: "Ownership", Fields: people}}
	next := taskNextAction(status)
	var attention *portal.ProductAttention
	if !fixture && taskGroup(status) == 0 {
		// The Needs attention panel names the status once, as the reason,
		// then the next action. The header badge already shows the status.
		attention = &portal.ProductAttention{Reason: status, NextAction: next}
	} else if next != "" {
		sections = append(sections, portal.ProductSection{Title: "Next action", Note: next})
	}
	sections = append(sections, portal.ProductSection{Title: "Dates", Fields: []portal.ProductField{
		{Label: "Created", Value: shownDate(element.CreatedAt)},
		{Label: "Updated", Value: shownDate(element.UpdatedAt)},
	}, Note: taskUpdatedLine(element.UpdatedAt, now)})
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
		Status: portal.ProductStatus{Text: status, Tone: taskTone(status)}, Description: description, Meta: listOwner, Age: age,
		Badges: badges, Sections: sections, ContextTitle: "Work package", Context: context,
		Attention: attention, Fixture: fixture,
	}, nil
}

// taskFixturePrefixes are the explicit synthetic markers: the exact opening
// words a self-declaring synthetic work package writes into its description.
// Matching is case-sensitive and the description is not trimmed.
var taskFixturePrefixes = []string{"Synthetic fixture", "Synthetic acceptance fixture", "Negative authorization probe"}

// taskFixture reports whether the raw description starts with one of
// taskFixturePrefixes. The subject is not read and nothing is inferred.
func taskFixture(raw string) bool {
	for _, prefix := range taskFixturePrefixes {
		if strings.HasPrefix(raw, prefix) {
			return true
		}
	}
	return false
}

// taskNextAction is one line taken only from the status. In progress and
// every unlisted status have none. Age is not an input.
func taskNextAction(status string) string {
	switch strings.ToLower(status) {
	case "needs approval":
		return "Waiting for approval"
	case "needs answer":
		return "Waiting for an answer"
	case "paused":
		return "Paused: resume needs its owner"
	case "on hold":
		return "On hold"
	default:
		return ""
	}
}

// taskUpdatedLine is the detail's update line. An unreadable time stays Unknown.
func taskUpdatedLine(raw string, now time.Time) string {
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "Last updated Unknown"
	}
	return "Last updated " + portal.Ago(parsed, now) + " · " + parsed.UTC().Format("2 Jan 2006, 15:04 UTC")
}

// recordedOwner shows one owner field, or "Not recorded" when it is empty.
func recordedOwner(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "Not recorded"
	}
	return shown(raw)
}

// taskListOwner is the list's short owner: today's execution-worktree rule,
// then the accountable owner, then the execution text when it has no short
// name. Both empty fields read "Owner not recorded".
func taskListOwner(execution, accountable string) string {
	owner := taskOwner(execution)
	if owner == "" && strings.TrimSpace(accountable) != "" {
		owner = shown(accountable)
	}
	if owner == "" && strings.TrimSpace(execution) != "" {
		owner = shown(execution)
	}
	if owner == "" {
		return "Owner not recorded"
	}
	return owner
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

var taskGroups = []portal.ProductGroup{{Title: "Needs attention", Attention: true}, {Title: "In progress"}, {Title: "Planned"}, {Title: "Closed"}, {Title: "Other status"}, {Title: "Fixtures (synthetic)"}}

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
// inside each group. A synthetic fixture is the last group and is left out of
// Needs attention. Counts appear only for a complete read.
func tasksPage(rows []portal.ProductRow, complete bool, filter portal.ProductFilter) portal.ProductPage {
	page := portal.ProductPage{Title: "Tasks", ListTitle: "All tasks", Connected: true, Complete: complete, Filter: portal.ProductFilter{Attention: filter.Attention}}
	ordered := make([][]portal.ProductRow, len(taskGroups))
	attention := 0
	for _, row := range rows {
		if row.Fixture {
			ordered[len(ordered)-1] = append(ordered[len(ordered)-1], row)
			continue
		}
		g := taskGroup(row.Status.Text)
		ordered[g] = append(ordered[g], row)
		if g == 0 {
			attention++
		}
	}
	page.Rail.All, page.Rail.Attention = strconv.Itoa(len(rows)), strconv.Itoa(attention)
	listed := len(rows)
	if filter.Attention {
		page.ListTitle, listed = "Needs attention", attention
		blank := make([][]portal.ProductRow, len(taskGroups))
		blank[0] = ordered[0]
		ordered = blank
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
		return nil, readFailure("response_contract")
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return nil, sessionTransportFailure(ctx, err)
	}
	if response == nil {
		return nil, readFailure("operator_unreachable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
		failure := readFailure("operator_refused")
		failure.HTTPStatus = response.StatusCode
		return nil, failure
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != accept {
		return nil, readFailure("response_contract")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, sessionTransportFailure(ctx, err)
	}
	if int64(len(body)) > limit {
		return nil, readFailure("response_contract")
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
// either page, project=<repository> on Sessions, or session=<uid> on Sessions
// to select one session's conversation. Any other query, a second key or
// value, or a non-canonical spelling is refused. view=usage selects Usage on Sessions.
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
	case len(q["view"]) == 1 && u.Path == SessionsPath && q.Get("view") == "usage":
		return portal.ProductFilter{View: "usage"}, true
	case len(q["session"]) == 1 && u.Path == SessionsPath && sessionUIDQuery.MatchString(q.Get("session")):
		return portal.ProductFilter{Session: q.Get("session")}, true
	}
	return portal.ProductFilter{}, false
}

func serveProduct(w http.ResponseWriter, r *http.Request, c AssemblyConfig) {
	started := time.Now()
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
		// Leave room within the 20s write timeout for final auth checks and rendering.
		readCtx, cancel := context.WithDeadline(r.Context(), started.Add(sessionReadTimeout))
		if readCtx.Err() != nil {
			page, _ = c.Sessions.skippedRead(readCtx, filter, started)
		} else {
			page, _ = c.Sessions.read(readCtx, filter, started)
		}
		cancel()
		if page.ReadFailure != nil && c.Sessions == nil {
			page.ReadFailure.ObservedAt = c.Now().UTC().Format(time.RFC3339Nano)
		}
	case TasksPath:
		readCtx, cancel := context.WithDeadline(r.Context(), started.Add(sessionReadTimeout))
		page, _ = c.Tasks.Read(readCtx, filter)
		cancel()
		if page.ReadFailure != nil && c.Tasks == nil {
			page.ReadFailure.ObservedAt = c.Now().UTC().Format(time.RFC3339Nano)
		}
	default:
		authFailure(w, portal.ErrDenied)
		return
	}
	// Final session, authorization and form-scope checks share one deadline,
	// inherited from the request, leaving headroom for rendering and writing.
	checkCtx, checkCancel := context.WithDeadline(r.Context(), started.Add(19*time.Second))
	defer checkCancel()
	r = r.WithContext(checkCtx)
	if current, currentErr := c.Session.Authenticate(r); currentErr != nil || current != principal {
		authFailure(w, portal.ErrDenied)
		return
	}
	if err = c.Authorizer.Authorize(r.Context(), principal, resource); err != nil {
		authFailure(w, err)
		return
	}
	page.ObservedAt = c.Now().UTC().Format(time.RFC3339)
	// A configured send route turns reachable sessions' boxes into forms bound
	// to this caller's portal session. Without it, Send stays disabled.
	if r.URL.Path == SessionsPath && filter.View != "usage" && c.Send != nil {
		if _, record, currentErr := c.Session.current(r); currentErr == nil && record.Principal == principal {
			page = withSendForms(page, record.CSRF, c.Send)
		}
	}
	// A configured launch route adds the New agent form for any principal the
	// group check admits; an unconfigured launch route, and the Tasks page, get none.
	if r.URL.Path == SessionsPath && filter.View != "usage" && c.Launch != nil {
		if _, record, currentErr := c.Session.current(r); currentErr == nil && record.Principal == principal {
			page = withLaunchForm(page, principal, record.CSRF, c.Launch)
		}
	}
	// Leave headroom under the assembly's response bound for the page frame.
	body, policy, err := portal.RenderProductPolicyWithin(page, maximumResponse*7/8)
	if err != nil {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	// The rendered page allows exactly its embedded stylesheet, and posts only
	// to this origin when it holds a send form; the header matches the page's
	// meta policy. Refusals keep authHeaders' policy.
	w.Header().Set("Content-Security-Policy", policy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}
