package apiv1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

var ErrTaskBudgetGate = errors.New("task budget gate refused or unavailable")

// HTTPTaskBudgetGate confirms exact task/epoch/deadline/host/Pod identity on
// every admission and observation. The executor must treat an Observe error as
// a stop request. A nil token source, invalid response or redirect fails closed.
type HTTPTaskBudgetGate struct {
	BaseURL string
	Client  *http.Client
	Token   func(context.Context) (string, error)
}

func (g *HTTPTaskBudgetGate) check(ctx context.Context, b TaskBudgetBinding, action string) (TaskBudgetStatus, error) {
	var result TaskBudgetStatus
	if g == nil || g.Client == nil || g.Token == nil || b.TaskUID == "" || b.Epoch == 0 || b.Deadline.IsZero() || b.HostID == "" || b.PodUID == "" {
		return result, ErrTaskBudgetGate
	}
	u, err := url.Parse(g.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return result, ErrTaskBudgetGate
	}
	token, err := g.Token(ctx)
	if err != nil || strings.TrimSpace(token) == "" {
		return result, ErrTaskBudgetGate
	}
	u.Path = strings.TrimRight(u.Path, "/") + TaskBudgetPath(b.TaskUID) + "/" + action
	body, err := json.Marshal(TaskBudgetRequest{Binding: b})
	if err != nil {
		return result, ErrTaskBudgetGate
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return result, ErrTaskBudgetGate
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	hc := *g.Client
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return result, ErrTaskBudgetGate
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return result, ErrTaskBudgetGate
	}
	d := json.NewDecoder(io.LimitReader(resp.Body, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || d.Decode(new(any)) != io.EOF {
		return TaskBudgetStatus{}, ErrTaskBudgetGate
	}
	a := result.Binding
	if !result.Observed || a.TaskUID != b.TaskUID || a.Epoch != b.Epoch || a.HostID != b.HostID || a.PodUID != b.PodUID || !a.Deadline.Equal(b.Deadline) {
		return TaskBudgetStatus{}, ErrTaskBudgetGate
	}
	return result, nil
}

func (g *HTTPTaskBudgetGate) Admit(ctx context.Context, b TaskBudgetBinding) error {
	st, err := g.check(ctx, b, "admit")
	if err != nil || st.Latched || !st.Admitted {
		return ErrTaskBudgetGate
	}
	return nil
}

func (g *HTTPTaskBudgetGate) Observe(ctx context.Context, b TaskBudgetBinding) (bool, error) {
	st, err := g.check(ctx, b, "observe")
	if err != nil {
		return true, err
	}
	return st.Latched, nil
}
