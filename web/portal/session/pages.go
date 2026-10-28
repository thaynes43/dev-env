package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/web/portal"
)

const pagesPath = "/portal/projects/dev-env/environments/internal/pages"
const pagesSourceURL = "http://alertmanager-operated.observability.svc.cluster.local:9093/api/v2/alerts"

type pagesSource struct {
	client *http.Client
	now    func() time.Time
}

func loadPagesEnabled(root string) (bool, error) {
	present, err := mountedPresent(filepath.Join(root, "config/pages-alertmanager.json"))
	if err != nil {
		return false, errProductionConfig
	}
	if !present {
		return false, nil
	}
	raw, err := readMountedBytes(root, "config/pages-alertmanager.json", 8192)
	if err != nil || !protocol.StrictPortalJSON(raw) {
		return false, errProductionConfig
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || !exactKeys(fields, []string{"source_url"}) {
		return false, errProductionConfig
	}
	url, ok := jsonString(fields["source_url"])
	if !ok || url != pagesSourceURL {
		return false, errProductionConfig
	}
	return true, nil
}
func newPagesSource(enabled bool, now func() time.Time, dial func(context.Context, string, string) (net.Conn, error)) *pagesSource {
	if !enabled || now == nil {
		return nil
	}
	transport := &http.Transport{Proxy: nil, DialContext: dial, DisableKeepAlives: true}
	return &pagesSource{now: now, client: &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (s *pagesSource) read(ctx context.Context) (pages.AlertmanagerSnapshot, error) {
	unavailable := pages.UnavailableAlertmanager()
	if s == nil || s.client == nil || s.now == nil {
		return unavailable, portal.ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pagesSourceURL, nil)
	if err != nil {
		return unavailable, portal.ErrUnavailable
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return unavailable, portal.ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return unavailable, portal.ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, pages.AlertmanagerMaxBytes+1))
	if err != nil || len(raw) > pages.AlertmanagerMaxBytes || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		return unavailable, portal.ErrUnavailable
	}
	snapshot, err := pages.ParseAlertmanager(raw)
	if err != nil {
		return unavailable, portal.ErrUnavailable
	}
	snapshot.ObservedAt = s.now().UTC().Format(time.RFC3339Nano)
	snapshot.Source = "Alertmanager /api/v2/alerts"
	snapshot.Coverage = "Current alerts only; notification and delivery history, triage disposition and recurrence are Unknown."
	return snapshot, nil
}
func servePages(w http.ResponseWriter, r *http.Request, c AssemblyConfig) {
	authHeaders(w)
	if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" || c.Session == nil || c.Authorizer == nil {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	principal, err := c.Session.Authenticate(r)
	if errors.Is(err, portal.ErrDenied) {
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
	snapshot := pages.UnavailableAlertmanager()
	snapshot.Coverage = "Current alerts only; notification and delivery history, triage disposition and recurrence are Unknown."
	if c.Pages != nil {
		if got, e := c.Pages.read(r.Context()); e == nil {
			snapshot = got
		}
	}
	body, err := portal.RenderPages(snapshot, c.Pages == nil, maximumResponse*7/8)
	if err != nil {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	current, err := c.Session.Authenticate(r)
	if err != nil || current != principal {
		authFailure(w, portal.ErrDenied)
		return
	}
	if err = c.Authorizer.Authorize(r.Context(), principal, resource); err != nil {
		authFailure(w, err)
		return
	}
	w.Header().Set("Content-Security-Policy", portal.PagesPolicy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}
