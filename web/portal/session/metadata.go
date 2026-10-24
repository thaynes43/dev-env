package session

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/tokenfile"
	"github.com/thaynes43/dev-env/web/portal"
)

const (
	openProjectBase = "https://work.haynesnetwork.com"
	openProjectHost = "work.haynesnetwork.com"
	metadataLimit   = 256 << 10
	claimLimit      = 64 << 10
	maxSummaries    = 64
	mediaJSON       = "application/json"
	mediaHAL        = "application/hal+json"
	projectIdent    = "haynes-engineering"
	projectIdentURL = "/api/v3/projects/" + projectIdent
)

var approvedTasks = []string{"120", "121", "122", "123", "178"}

var bindingName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// canonicalTaskKey is the TaskClaim grammar from taskclaim.ValidIdentity and the
// coordination CRD. A bare work-package number does not match it.
var canonicalTaskKey = regexp.MustCompile(`^(issue:[1-9][0-9]{0,18}|task:[a-z0-9]([a-z0-9._-]{0,126}[a-z0-9])?)$`)

type sessionBinding struct {
	project, repository, taskKey         string
	session, sessionUID, claim, claimUID string
}

func (b sessionBinding) valid() bool {
	return b.project == "dev-env" && b.repository == "dev-env" && projectcatalog.ValidName(b.project) && projectcatalog.ValidName(b.repository) && canonicalTaskKey.MatchString(b.taskKey) && bindingName.MatchString(b.session) && bindingName.MatchString(b.claim) && boundedIdentifier(b.sessionUID) && boundedIdentifier(b.claimUID)
}

type metadataConfig struct {
	enabled                   bool
	openProjectCA, operatorCA []byte
	openProjectToken          string
	operatorToken             string
	operatorBase              string
	bindings                  map[string]sessionBinding
	timeout                   time.Duration
	now                       func() time.Time
}

type metadataReader struct {
	enabled          bool
	openProject      *http.Client
	operator         *http.Client
	openProjectToken string
	operatorToken    string
	operatorBase     string
	bindings         map[string]sessionBinding
	now              func() time.Time
}

func (metadataReader) String() string     { return "[redacted portal metadata]" }
func (m metadataReader) GoString() string { return m.String() }

func newMetadataReader(c metadataConfig, dial func(context.Context, string, string) (net.Conn, error)) (*metadataReader, error) {
	if !c.enabled {
		return &metadataReader{}, nil
	}
	if c.now == nil || c.timeout <= 0 || c.timeout > 10*time.Second || len(c.bindings) != len(approvedTasks) {
		return nil, portal.ErrUnavailable
	}
	seenKey := map[string]struct{}{}
	seenClaim := map[string]struct{}{}
	seenClaimUID := map[string]struct{}{}
	sessionName := map[string]string{}
	sessionUID := map[string]string{}
	for _, task := range approvedTasks {
		binding, ok := c.bindings[task]
		if !ok || !binding.valid() || binding.taskKey == task {
			return nil, portal.ErrUnavailable
		}
		if _, dup := seenKey[binding.taskKey]; dup {
			return nil, portal.ErrUnavailable
		}
		if _, dup := seenClaim[binding.claim]; dup {
			return nil, portal.ErrUnavailable
		}
		if _, dup := seenClaimUID[binding.claimUID]; dup {
			return nil, portal.ErrUnavailable
		}
		if previous, seen := sessionName[binding.session]; seen && previous != binding.sessionUID {
			return nil, portal.ErrUnavailable
		}
		if previous, seen := sessionUID[binding.sessionUID]; seen && previous != binding.session {
			return nil, portal.ErrUnavailable
		}
		seenKey[binding.taskKey] = struct{}{}
		seenClaim[binding.claim] = struct{}{}
		seenClaimUID[binding.claimUID] = struct{}{}
		sessionName[binding.session] = binding.sessionUID
		sessionUID[binding.sessionUID] = binding.session
	}
	base, err := serviceOrigin(c.operatorBase)
	if err != nil || c.openProjectToken == "" || c.operatorToken == "" {
		return nil, portal.ErrUnavailable
	}
	openProject, err := pinnedClient(c.openProjectCA, openProjectHost, c.timeout, dial)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	operator, err := pinnedClient(c.operatorCA, hostOf(base), c.timeout, dial)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	return &metadataReader{enabled: true, openProject: openProject, operator: operator, openProjectToken: c.openProjectToken, operatorToken: c.operatorToken, operatorBase: base, bindings: c.bindings, now: c.now}, nil
}

func (m *metadataReader) Read(ctx context.Context, resource portal.Resource) (portal.Snapshot, error) {
	if m == nil || !m.enabled || m.now == nil {
		return portal.Snapshot{}, portal.ErrUnavailable
	}
	if ctx.Err() != nil {
		return portal.Snapshot{}, portal.ErrUnavailable
	}
	if resource.Project != "dev-env" || resource.Environment != "internal" {
		return portal.Snapshot{}, portal.ErrDenied
	}
	tasks := approvedTasks
	if resource.Task != "" {
		if _, ok := m.bindings[resource.Task]; !ok {
			return portal.Snapshot{}, portal.ErrDenied
		}
		tasks = []string{resource.Task}
	}
	items, updated, err := m.collect(ctx, tasks)
	if err != nil {
		return portal.Snapshot{}, err
	}
	// #177 supplies Attention groups through the offline workflow handoff checker.
	// That checker is not a portal read contract, so these rows stay ungrouped.
	return portal.Snapshot{Resource: resource, State: portal.Unknown, UpdatedAt: updated, VerifiedAt: m.now(), Items: items}, nil
}

func (m *metadataReader) collect(ctx context.Context, tasks []string) ([]portal.Item, time.Time, error) {
	sessions, err := m.sessionSummaries(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	items := make([]portal.Item, 0, len(tasks))
	var updated time.Time
	for _, task := range tasks {
		item, err := m.one(ctx, task, sessions)
		if err != nil {
			return nil, time.Time{}, err
		}
		if item.UpdatedAt.After(updated) {
			updated = item.UpdatedAt
		}
		items = append(items, item)
	}
	return items, updated, nil
}

func (m *metadataReader) one(ctx context.Context, task string, sessions []operatorSession) (portal.Item, error) {
	binding := m.bindings[task]
	// opwork.Client.wp reads GET /api/v3/work_packages/{id}. The project-nested
	// single-id path is not that interface. haynes-engineering is the project
	// identifier used by that client, proven here from the representation.
	body, err := m.get(ctx, m.openProject, openProjectBase+"/api/v3/work_packages/"+task, m.openProjectToken, metadataLimit, mediaHAL)
	if err != nil {
		return portal.Item{}, portal.ErrUnavailable
	}
	var page struct {
		ID        json.RawMessage `json:"id"`
		Subject   string          `json:"subject"`
		UpdatedAt string          `json:"updatedAt"`
		Links     struct {
			Project struct {
				Href string `json:"href"`
			} `json:"project"`
		} `json:"_links"`
		Embedded struct {
			Project struct {
				ID         json.RawMessage `json:"id"`
				Identifier string          `json:"identifier"`
				Links      struct {
					Self struct {
						Href string `json:"href"`
					} `json:"self"`
				} `json:"_links"`
			} `json:"project"`
		} `json:"_embedded"`
	}
	if json.Unmarshal(body, &page) != nil || !bytes.Equal(bytes.TrimSpace(page.ID), []byte(task)) || !identityText(page.Subject) {
		return portal.Item{}, portal.ErrUnavailable
	}
	projectID, projectIDOK := jsonPositive(page.Embedded.Project.ID)
	if !haynesProject(page.Links.Project.Href, page.Embedded.Project.Identifier, page.Embedded.Project.Links.Self.Href, projectID, projectIDOK) {
		return portal.Item{}, portal.ErrUnavailable
	}
	updated, err := time.Parse(time.RFC3339, page.UpdatedAt)
	if err != nil {
		return portal.Item{}, portal.ErrUnavailable
	}
	matched := 0
	var summary operatorSession
	for _, candidate := range sessions {
		if candidate.Name == binding.session && candidate.UID == binding.sessionUID {
			matched++
			summary = candidate
		}
	}
	if matched != 1 || summary.Repo != "dev-env" || !identityText(summary.Phase) {
		return portal.Item{}, portal.ErrUnavailable
	}
	// getTaskClaim requires ?project= and returns not found when it does not match.
	claimBody, err := m.get(ctx, m.operator, m.operatorBase+"/v1/task-claims/"+binding.claim+"?project=dev-env", m.operatorToken, claimLimit, mediaJSON)
	if err != nil {
		return portal.Item{}, portal.ErrUnavailable
	}
	var claim apiv1.TaskClaim
	if json.Unmarshal(claimBody, &claim) != nil || claim.UID != binding.claimUID || claim.Identity.Project != binding.project || claim.Identity.Repository != binding.repository || claim.Identity.TaskKey != binding.taskKey || claim.Identity.TaskKey == task {
		return portal.Item{}, portal.ErrUnavailable
	}
	worker, err := portal.ProjectWorker(portal.SourceBinding{Resource: portal.Resource{Project: "dev-env", Environment: "internal", Task: task}, ClaimUID: binding.claimUID, SessionName: binding.session, SessionUID: binding.sessionUID, IdentityProject: binding.project, IdentityRepository: binding.repository, TaskKey: binding.taskKey}, claim, apiv1.Session{Name: summary.Name, UID: summary.UID, Phase: summary.Phase})
	if err != nil {
		return portal.Item{}, portal.ErrUnavailable
	}
	workPackage, err := strconv.Atoi(task)
	if err != nil {
		return portal.Item{}, portal.ErrUnavailable
	}
	return portal.Item{Resource: portal.Resource{Project: "dev-env", Environment: "internal", Task: task}, WorkPackage: workPackage, Title: page.Subject, Owner: worker.Owner, WorkerState: worker.WorkerPhase, UpdatedAt: updated, VerifiedAt: m.now()}, nil
}

// haynesProject accepts the identifier href used by opwork, or a numeric
// /api/v3/projects/{id} href only when the same representation's embedded
// project identifier is haynes-engineering and its id matches that href.
// A numeric href without that embedded identity fails closed. Links are not followed.
func haynesProject(href, identifier, self string, id int, idOK bool) bool {
	if identifier != "" && identifier != projectIdent {
		return false
	}
	numeric, numericOK := projectNumber(href)
	switch {
	case href == projectIdentURL:
		if self == "" || self == projectIdentURL {
			return true
		}
		selfID, selfOK := projectNumber(self)
		return idOK && selfOK && selfID == id && self == "/api/v3/projects/"+strconv.Itoa(id)
	case numericOK:
		if identifier != projectIdent || !idOK || id != numeric {
			return false
		}
		return self == "" || self == href
	default:
		return false
	}
}

func projectNumber(href string) (int, bool) {
	const prefix = "/api/v3/projects/"
	if !strings.HasPrefix(href, prefix) || strings.ContainsAny(href, "?#") {
		return 0, false
	}
	raw := strings.TrimPrefix(href, prefix)
	if raw == "" || raw == projectIdent {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	return n, err == nil && n > 0 && strconv.Itoa(n) == raw
}

func jsonPositive(raw json.RawMessage) (int, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] == '"' {
		return 0, false
	}
	n, err := strconv.Atoi(string(raw))
	return n, err == nil && n > 0 && strconv.Itoa(n) == string(raw)
}

type operatorSession struct {
	UID, Name, Repo, Phase string
}

func (m *metadataReader) sessionSummaries(ctx context.Context) ([]operatorSession, error) {
	body, err := m.get(ctx, m.operator, m.operatorBase+"/v1/sessions?repo=dev-env", m.operatorToken, metadataLimit, mediaJSON)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	var list struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	if json.Unmarshal(body, &list) != nil || len(list.Sessions) > maxSummaries {
		return nil, portal.ErrUnavailable
	}
	out := make([]operatorSession, 0, len(list.Sessions))
	for _, raw := range list.Sessions {
		if nonEmptyPrompt(raw) {
			return nil, portal.ErrUnavailable
		}
		var summary struct {
			UID   string `json:"uid"`
			Name  string `json:"name"`
			Repo  string `json:"repo"`
			Phase string `json:"phase"`
		}
		if json.Unmarshal(raw, &summary) != nil {
			return nil, portal.ErrUnavailable
		}
		out = append(out, operatorSession{UID: summary.UID, Name: summary.Name, Repo: summary.Repo, Phase: summary.Phase})
	}
	return out, nil
}

func nonEmptyPrompt(raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return true
	}
	prompt, ok := obj["prompt"]
	if !ok {
		return false
	}
	trimmed := bytes.TrimSpace(prompt)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) && !bytes.Equal(trimmed, []byte(`""`))
}

func (m *metadataReader) get(ctx context.Context, client *http.Client, rawURL, token string, limit int64, accept string) ([]byte, error) {
	if client == nil || !bearerText(token) || (accept != mediaJSON && accept != mediaHAL) {
		return nil, portal.ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil || req.URL.Scheme != "https" {
		return nil, portal.ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", accept)
	req.Header.Set("Cache-Control", "no-store")
	response, err := client.Do(req)
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil || response == nil || response.StatusCode != http.StatusOK || response.Body == nil {
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

func pinnedClient(caPEM []byte, serverName string, timeout time.Duration, dial func(context.Context, string, string) (net.Conn, error)) (*http.Client, error) {
	roots := x509.NewCertPool()
	if serverName == "" || !roots.AppendCertsFromPEM(caPEM) {
		return nil, portal.ErrUnavailable
	}
	dialer := (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext
	if dial != nil {
		dialer = dial
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer,
		TLSClientConfig:        &tls.Config{RootCAs: roots, ServerName: serverName, MinVersion: tls.VersionTLS12},
		DisableCompression:     true,
		MaxIdleConns:           2,
		MaxConnsPerHost:        2,
		IdleConnTimeout:        30 * time.Second,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		MaxResponseHeaderBytes: 16 * 1024,
		TLSNextProto:           map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func serviceOrigin(raw string) (string, error) {
	u, err := configuredURL(raw)
	if err != nil || (u.Path != "" && u.Path != "/") {
		return "", portal.ErrUnavailable
	}
	return u.Scheme + "://" + u.Host, nil
}

func hostOf(origin string) string {
	u, err := configuredURL(origin)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// readBearerFile reads one mounted token. The returned error never includes the bytes.
func readBearerFile(path string) (string, error) {
	data, err := tokenfile.Read(path, 4096)
	if err != nil {
		return "", err
	}
	if len(data) > 0 && data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}
	if !bearerText(string(data)) {
		return "", errors.New("portal token unavailable")
	}
	return string(data), nil
}
