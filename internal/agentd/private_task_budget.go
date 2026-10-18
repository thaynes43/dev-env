package agentd

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/tokenfile"
)

const privateBudgetPoll = 5 * time.Second

// watchPrivateBudget observes no progress authority. The immutable hard timer
// stays independent; an uncertain API observation requests whole-container exit.
func watchPrivateBudget(ctx context.Context, ticks <-chan time.Time, observe func(context.Context) (bool, error), exit func(int)) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			if ctx.Err() != nil {
				return
			}
			latched, err := observe(ctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil || latched {
				exit(124)
				return
			}
		}
	}
}

func (d *Daemon) armPrivateTaskBudgetWatch(ctx context.Context) (func(), error) {
	raw := os.Getenv(protocol.TaskBudgetEnv)
	if raw == "" || d.S.WorkspaceID != "" {
		return func() {}, nil
	}
	b, err := protocol.ParseTaskBudgetDeadline(raw, d.Session.SessionUID, d.S.PodUID, d.now())
	if err != nil || b.CampaignDeadline.IsZero() {
		return nil, errors.New("private budget campaign binding is unavailable")
	}
	h, err := NewHeartbeatClient(d.S, d.Session.Name)
	if err != nil {
		return nil, err
	}
	tr := h.HTTP.Transport.(*http.Transport).Clone()
	tr.Proxy = nil
	hc := *h.HTTP
	hc.Transport = tr
	hc.Timeout = 2 * time.Second
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	gate := &apiv1.HTTPTaskBudgetGate{BaseURL: d.S.APIURL, Client: &hc, Token: func(context.Context) (string, error) {
		raw, err := tokenfile.Read(d.S.APITokenFile, tokenfile.MaxBytes)
		return strings.TrimSpace(string(raw)), err
	}}
	binding := apiv1.TaskBudgetBinding{TaskUID: b.TaskUID, Epoch: b.Epoch, Deadline: b.CampaignDeadline, HostID: b.HostID, PodUID: b.RootPodUID}
	observe := func(ctx context.Context) (bool, error) { return gate.Observe(ctx, binding) }
	latched, err := observe(ctx)
	if err != nil || latched {
		tr.CloseIdleConnections()
		return nil, apiv1.ErrTaskBudgetGate
	}
	bounded, cancel := context.WithCancel(ctx)
	ticker := time.NewTicker(privateBudgetPoll)
	done := make(chan struct{})
	go func() { defer close(done); watchPrivateBudget(bounded, ticker.C, observe, os.Exit) }()
	return func() { cancel(); ticker.Stop(); <-done; tr.CloseIdleConnections() }, nil
}
