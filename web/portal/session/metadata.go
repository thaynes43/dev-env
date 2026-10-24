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
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/tokenfile"
	"github.com/thaynes43/dev-env/web/portal"
)

const (
	openProjectBase = "https://work.haynesnetwork.com"
	openProjectHost = "work.haynesnetwork.com"
	metadataLimit   = 256 << 10
	claimLimit      = 64 << 10
	maxSummaries    = 64
)

var approvedTasks = []string{"120", "121", "122", "123", "178"}

var bindingName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type sessionBinding struct {
	session, sessionUID, claim, claimUID string
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
	for _, task := range approvedTasks {
		binding, ok := c.bindings[task]
		if !ok || !bindingName.MatchString(binding.session) || !bindingName.MatchString(binding.claim) || !boundedIdentifier(binding.sessionUID) || !boundedIdentifier(binding.claimUID) {
			return nil, portal.ErrUnavailable
		}
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
	if err := m.validate(ctx, tasks); err != nil {
		return portal.Snapshot{}, err
	}
	// Sources matched. Attention group and outcome are not invented from them.
	return portal.Snapshot{Resource: resource, State: portal.Unknown}, nil
}

func (m *metadataReader) validate(ctx context.Context, tasks []string) error {
	sessions, err := m.sessionSummaries(ctx)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if err := m.one(ctx, task, sessions); err != nil {
			return err
		}
	}
	return nil
}

func (m *metadataReader) one(ctx context.Context, task string, sessions []operatorSession) error {
	binding := m.bindings[task]
	// opwork.Client.wp reads GET /api/v3/work_packages/{id}. The project-nested
	// single-id path is not that interface. haynes-engineering is the project
	// identifier used by that client, proven here from the representation.
	body, err := m.get(ctx, m.openProject, openProjectBase+"/api/v3/work_packages/"+task, m.openProjectToken, metadataLimit)
	if err != nil {
		return portal.ErrUnavailable
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
				Identifier string `json:"identifier"`
			} `json:"project"`
		} `json:"_embedded"`
	}
	if json.Unmarshal(body, &page) != nil || !bytes.Equal(bytes.TrimSpace(page.ID), []byte(task)) || !identityText(page.Subject) || !haynesEngineering(page.Links.Project.Href, page.Embedded.Project.Identifier) {
		return portal.ErrUnavailable
	}
	if _, err := time.Parse(time.RFC3339, page.UpdatedAt); err != nil {
		return portal.ErrUnavailable
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
		return portal.ErrUnavailable
	}
	// getTaskClaim requires ?project= and returns not found when it does not match.
	claimBody, err := m.get(ctx, m.operator, m.operatorBase+"/v1/task-claims/"+binding.claim+"?project=dev-env", m.operatorToken, claimLimit)
	if err != nil {
		return portal.ErrUnavailable
	}
	var claim apiv1.TaskClaim
	if json.Unmarshal(claimBody, &claim) != nil || claim.UID != binding.claimUID || claim.Identity.Project != "dev-env" || claim.Identity.Repository != "dev-env" || claim.Identity.TaskKey != task {
		return portal.ErrUnavailable
	}
	_, err = portal.ProjectWorker(portal.SourceBinding{Resource: portal.Resource{Project: "dev-env", Environment: "internal", Task: task}, ClaimUID: binding.claimUID, SessionName: binding.session, SessionUID: binding.sessionUID}, claim, apiv1.Session{Name: summary.Name, UID: summary.UID, Phase: summary.Phase})
	if err != nil {
		return portal.ErrUnavailable
	}
	return nil
}

func haynesEngineering(href, identifier string) bool {
	const project = "/api/v3/projects/haynes-engineering"
	if identifier != "" && identifier != "haynes-engineering" {
		return false
	}
	if href != "" && href != project {
		return false
	}
	return href == project || identifier == "haynes-engineering"
}

type operatorSession struct {
	UID, Name, Repo, Phase string
}

func (m *metadataReader) sessionSummaries(ctx context.Context) ([]operatorSession, error) {
	body, err := m.get(ctx, m.operator, m.operatorBase+"/v1/sessions?repo=dev-env", m.operatorToken, metadataLimit)
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

func (m *metadataReader) get(ctx context.Context, client *http.Client, rawURL, token string, limit int64) ([]byte, error) {
	if client == nil || !bearerText(token) {
		return nil, portal.ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil || req.URL.Scheme != "https" {
		return nil, portal.ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-store")
	response, err := client.Do(req)
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil || response == nil || response.StatusCode != http.StatusOK || response.Body == nil {
		return nil, portal.ErrUnavailable
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
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
