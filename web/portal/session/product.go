package session

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
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

func (s *SessionSource) Read(ctx context.Context) (portal.ProductPage, error) {
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
	for n := 0; n < productPageCap; n++ {
		raw := operatorPortalOrigin + "/v1/sessions?limit=" + strconv.Itoa(productPageSize)
		if cursor != "" {
			raw += "&cursor=" + url.QueryEscape(cursor)
		}
		body, err := productGet(ctx, s.client, raw, token, 256<<10, mediaJSON)
		if err != nil {
			return page, err
		}
		var decoded apiv1.PortalSessionPage
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if dec.Decode(&decoded) != nil || decoded.Limit < 1 || decoded.Limit > 100 {
			return page, portal.ErrUnavailable
		}
		if decoded.Freshness == "unknown" || decoded.Freshness == "stale" {
			freshness = decoded.Freshness
		}
		for _, row := range decoded.Sessions {
			if row.Repo != "dev-env" || row.Name == "" || row.UID == "" || row.Phase == "" {
				return page, portal.ErrUnavailable
			}
			page.Rows = append(page.Rows, portal.ProductRow{Title: shown(row.Name), Fields: []portal.ProductField{{Label: "Phase", Value: shown(row.Phase)}, {Label: "Freshness", Value: shown(row.Freshness)}, {Label: "UID", Value: shown(row.UID)}}})
		}
		complete = decoded.Complete
		cursor = decoded.NextCursor
		if complete || cursor == "" {
			break
		}
	}
	page.Notice = "Connected session summaries."
	if !complete {
		page.Notice = "Sessions read is incomplete. This is not the full inventory."
	} else if freshness != "observed" {
		page.Notice = "Session freshness is " + freshness + "."
	} else if len(page.Rows) == 0 {
		page.Notice = "The completed session read returned no dev-env sessions."
	}
	return page, nil
}

func (s *TaskSource) Read(ctx context.Context) (portal.ProductPage, error) {
	page := portal.ProductPage{Title: "Tasks", Connected: true}
	if s == nil || s.client == nil || s.now == nil {
		return page, portal.ErrUnavailable
	}
	token, err := readBearerFile(s.path)
	if err != nil {
		return page, portal.ErrUnavailable
	}
	offset := 1
	complete := false
	for n := 0; n < productPageCap; n++ {
		raw := openProjectPortalOrigin + openProjectProjectPath + "?pageSize=" + strconv.Itoa(productPageSize) + "&offset=" + strconv.Itoa(offset)
		body, err := productGet(ctx, s.client, raw, token, 256<<10, mediaHAL)
		if err != nil {
			return page, err
		}
		var decoded halCollection
		if json.Unmarshal(body, &decoded) != nil {
			return page, portal.ErrUnavailable
		}
		for _, element := range decoded.Embedded.Elements {
			row, err := taskRow(element)
			if err != nil {
				return page, err
			}
			page.Rows = append(page.Rows, row)
		}
		next := offset + len(decoded.Embedded.Elements)
		if href := decoded.Links.Next.Href; href != "" && !fixedTaskPage(href, next) {
			return page, portal.ErrUnavailable
		}
		if decoded.Total > 0 && next > decoded.Total || len(decoded.Embedded.Elements) == 0 {
			complete = true
			break
		}
		if decoded.Total == 0 {
			break
		}
		offset = next
	}
	page.Notice = "Connected project 3 work packages."
	if !complete {
		page.Notice = "Tasks read is incomplete. This is not the full inventory."
	} else if len(page.Rows) == 0 {
		page.Notice = "The completed task read returned no work packages."
	}
	return page, nil
}

type halCollection struct {
	Total    int `json:"total"`
	Embedded struct {
		Elements []halWorkPackage `json:"elements"`
	} `json:"_embedded"`
	Links struct {
		Next struct {
			Href string `json:"href"`
		} `json:"nextByOffset"`
	} `json:"_links"`
}

type halWorkPackage struct {
	ID           int    `json:"id"`
	Subject      string `json:"subject"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
	CustomField1 string `json:"customField1"`
	CustomField3 string `json:"customField3"`
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

func taskRow(element halWorkPackage) (portal.ProductRow, error) {
	if element.ID < 1 || !projectThree(element) {
		return portal.ProductRow{}, portal.ErrUnavailable
	}
	id := strconv.Itoa(element.ID)
	return portal.ProductRow{
		Title: shown(element.Subject),
		Link:  "https://work.haynesnetwork.com/work_packages/" + id,
		Fields: []portal.ProductField{
			{Label: "Status", Value: shown(element.Links.Status.Title)},
			{Label: "Assignee", Value: shown(element.Links.Assignee.Title)},
			{Label: "Accountable", Value: shown(element.CustomField1)},
			{Label: "Responsible", Value: shown(element.Links.Responsible.Title)},
			{Label: "Repositories", Value: shown(element.CustomField3)},
			{Label: "Created", Value: shownTime(element.CreatedAt)},
			{Label: "Updated", Value: shownTime(element.UpdatedAt)},
		},
	}, nil
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

func fixedTaskPage(href string, offset int) bool {
	u, err := url.Parse(href)
	if err != nil || u.Scheme != "https" || u.Host != openProjectPortalHost+":8443" || u.Path != openProjectProjectPath || u.Fragment != "" {
		return false
	}
	q := u.Query()
	return len(q) == 2 && q.Get("pageSize") == strconv.Itoa(productPageSize) && q.Get("offset") == strconv.Itoa(offset)
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

func shownTime(value string) string {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "Unknown"
	}
	return parsed.UTC().Format(time.RFC3339)
}

func serveProduct(w http.ResponseWriter, r *http.Request, c AssemblyConfig) {
	authHeaders(w)
	if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.URL.RawPath != "" || c.Session == nil || c.Authorizer == nil || c.Now == nil {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	principal, err := c.Session.Authenticate(r)
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
			page, err = c.Sessions.Read(r.Context())
		}
	case TasksPath:
		page = portal.ProductPage{Title: "Tasks", Notice: "Tasks not connected"}
		if c.Tasks != nil {
			page, err = c.Tasks.Read(r.Context())
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}
