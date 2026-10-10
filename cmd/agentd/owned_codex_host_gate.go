package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

type ownedBudgetGate struct {
	remote   *apiv1.HTTPTaskBudgetGate
	settings agentd.Settings
}

func boundedProjection(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, apiv1.ErrTaskBudgetGate
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > limit {
		return nil, apiv1.ErrTaskBudgetGate
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, apiv1.ErrTaskBudgetGate
	}
	return data, nil
}

func newOwnedBudgetGate(baseURL, tokenFile, caFile string, s agentd.Settings) (*ownedBudgetGate, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, apiv1.ErrTaskBudgetGate
	}
	pem, err := boundedProjection(caFile, 128<<10)
	if err != nil {
		return nil, apiv1.ErrTaskBudgetGate
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, apiv1.ErrTaskBudgetGate
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: time.Second, MaxIdleConns: 1, IdleConnTimeout: time.Second}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	remote := &apiv1.HTTPTaskBudgetGate{BaseURL: baseURL, Client: client, Token: func(ctx context.Context) (string, error) {
		if ctx.Err() != nil {
			return "", apiv1.ErrTaskBudgetGate
		}
		data, err := boundedProjection(tokenFile, 16<<10)
		if err != nil {
			return "", apiv1.ErrTaskBudgetGate
		}
		token := strings.TrimSpace(string(data))
		if token == "" || strings.ContainsAny(token, "\r\n\t ") {
			return "", apiv1.ErrTaskBudgetGate
		}
		return token, nil
	}}
	return &ownedBudgetGate{remote: remote, settings: s}, nil
}

func wireBinding(b hostexecutor.Binding) apiv1.TaskBudgetBinding {
	return apiv1.TaskBudgetBinding{TaskUID: b.TaskUID, Epoch: b.Epoch, Deadline: b.Deadline, HostID: b.HostID, PodUID: b.PodUID}
}

// The declared immutable binding is only an expectation. Read the operator's
// assigned binding without creating a ledger, then demand exact equality.
func (g *ownedBudgetGate) binding(ctx context.Context, expected hostexecutor.Binding) (hostexecutor.Binding, error) {
	if g == nil || g.remote == nil || expected.TaskUID == "" || strings.ContainsAny(expected.TaskUID, "/\\?#") || expected.Epoch == 0 || expected.HostID == "" || expected.PodUID == "" || !expected.Deadline.After(time.Now()) {
		return hostexecutor.Binding{}, apiv1.ErrTaskBudgetGate
	}
	token, err := g.remote.Token(ctx)
	if err != nil {
		return hostexecutor.Binding{}, apiv1.ErrTaskBudgetGate
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.remote.BaseURL+apiv1.TaskBudgetPath(expected.TaskUID), nil)
	if err != nil {
		return hostexecutor.Binding{}, apiv1.ErrTaskBudgetGate
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.remote.Client.Do(req)
	if err != nil {
		return hostexecutor.Binding{}, apiv1.ErrTaskBudgetGate
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return hostexecutor.Binding{}, apiv1.ErrTaskBudgetGate
	}
	var status apiv1.TaskBudgetStatus
	d := json.NewDecoder(io.LimitReader(resp.Body, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(&status) != nil || !errors.Is(d.Decode(new(any)), io.EOF) || !status.Observed || status.Latched || !sameOwnedBinding(status.Binding, wireBinding(expected)) {
		return hostexecutor.Binding{}, apiv1.ErrTaskBudgetGate
	}
	return hostexecutor.Binding{TaskUID: status.Binding.TaskUID, Epoch: status.Binding.Epoch, Deadline: status.Binding.Deadline, HostID: status.Binding.HostID, PodUID: status.Binding.PodUID}, nil
}

func (g *ownedBudgetGate) Admit(ctx context.Context, b hostexecutor.Binding) error {
	if g == nil || g.remote == nil || g.remote.Admit(ctx, wireBinding(b)) != nil {
		return apiv1.ErrTaskBudgetGate
	}
	if g.settings.CodexAccessFile == "" || agentd.SyncCodexAccess(g.settings, time.Now()) != nil {
		return apiv1.ErrTaskBudgetGate
	}
	return nil
}
func (g *ownedBudgetGate) Observe(ctx context.Context, b hostexecutor.Binding) (bool, error) {
	if g == nil || g.remote == nil {
		return true, apiv1.ErrTaskBudgetGate
	}
	latched, err := g.remote.Observe(ctx, wireBinding(b))
	if err != nil || latched {
		return true, err
	}
	if agentd.SyncCodexAccess(g.settings, time.Now()) != nil {
		return true, apiv1.ErrTaskBudgetGate
	}
	return false, nil
}

func sameOwnedBinding(a, b apiv1.TaskBudgetBinding) bool {
	return a.TaskUID == b.TaskUID && a.Epoch == b.Epoch && a.HostID == b.HostID && a.PodUID == b.PodUID && a.Deadline.Equal(b.Deadline)
}
