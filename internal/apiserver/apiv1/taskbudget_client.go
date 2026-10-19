package apiv1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
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
	if action == "observe" {
		// Only observation is idempotent. Allow net/http to replace a lost
		// reused connection within the SAME context/client timeout. A new
		// complete, exact-binding response is still mandatory below. Empty
		// slice marks replayability without sending a header on the wire.
		// Admission and every mutation retain their no-replay behavior.
		req.Header["Idempotency-Key"] = []string{}
	}
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 || !unambiguousBudgetStatus(raw) {
		return TaskBudgetStatus{}, ErrTaskBudgetGate
	}
	d := json.NewDecoder(bytes.NewReader(raw))
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
	return st.Latched || st.NativeStopRequested, nil
}

// Required safety fields cannot be absent/null, case aliases or duplicate keys.
// Typed decoding below still validates every DTO field and exact binding.
func unambiguousJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > 16 {
			return false
		}
		tok, err := d.Token()
		if err != nil {
			return false
		}
		switch tok {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				folded := strings.ToLower(name)
				if err != nil || !ok || seen[folded] {
					return false
				}
				seen[folded] = true
				if !walk(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case json.Delim('['):
			for d.More() {
				if !walk(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		default:
			return tok != json.Delim('}') && tok != json.Delim(']')
		}
	}
	if !walk(0) {
		return false
	}
	if _, err := d.Token(); err != io.EOF {
		return false
	}
	return true
}

func unambiguousBudgetStatus(raw []byte) bool {
	if !unambiguousJSON(raw) {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for _, key := range []string{"observed", "admitted", "latched", "stopConfirmed"} {
		v := string(fields[key])
		if v != "true" && v != "false" {
			return false
		}
	}
	var binding map[string]json.RawMessage
	if json.Unmarshal(fields["binding"], &binding) != nil || len(binding) != 5 {
		return false
	}
	for _, key := range []string{"taskUID", "epoch", "deadline", "hostID", "podUID"} {
		if len(binding[key]) == 0 || string(binding[key]) == "null" {
			return false
		}
	}
	return true
}

func (g *HTTPTaskBudgetGate) ConsumeNativeControl(ctx context.Context, r NativeControlRequest) error {
	if !r.Valid() {
		return ErrTaskBudgetGate
	}
	raw, _ := json.Marshal(r)
	status, body, err := g.ownedRequest(ctx, http.MethodPost, TaskBudgetPath(r.Binding.TaskUID)+"/native-consume", raw)
	if err != nil || status != http.StatusOK || !unambiguousJSON(body) {
		return ErrTaskBudgetGate
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || len(fields) != 4 || string(fields["consumed"]) != "true" || len(fields["id"]) == 0 || len(fields["binding"]) == 0 || len(fields["requestSHA256"]) == 0 {
		return ErrTaskBudgetGate
	}
	var permit NativeControlPermit
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(&permit) != nil || dec.Decode(new(any)) != io.EOF || !permit.Consumed || !sameOwnedWireBinding(permit.Binding, r.Binding) || permit.ID != r.ID || permit.RequestSHA256 != NativeControlDigest(r) {
		return ErrTaskBudgetGate
	}
	return nil
}

// BootstrapOwned creates exactly once. Conflicts and unknown replies refuse
// without a recovery read or mutation replay. The caller persists its
// one-use bootstrap intent before invoking this method. The server deadline
// remains immutable; a fixed five-second clock sanity tolerance avoids burning
// the one-use bootstrap for ordinary bounded operator/node clock differences.
func (g *HTTPTaskBudgetGate) BootstrapOwned(ctx context.Context, req CreateTaskBudgetRequest, host, pod string) (TaskBudgetBinding, error) {
	raw, _ := json.Marshal(req)
	status, body, err := g.ownedRequest(ctx, http.MethodPost, TaskBudgetsPath, raw)
	if err != nil {
		return TaskBudgetBinding{}, err
	}
	if status != http.StatusCreated {
		return TaskBudgetBinding{}, ErrTaskBudgetGate
	}

	st, err := decodeOwnedStatus(body)
	observed := time.Now()
	if err != nil || st.Admitted || st.Latched || st.Binding.TaskUID != req.TaskUID || st.Binding.Epoch != 1 || st.Binding.HostID != host || st.Binding.PodUID != pod || !st.Binding.Deadline.After(observed) || st.Binding.Deadline.After(observed.Add(time.Duration(req.Spec.OverallSeconds)*time.Second+5*time.Second)) {
		return TaskBudgetBinding{}, ErrTaskBudgetGate
	}
	return st.Binding, nil
}

// StopOwned submits one independently validated owned-root stop, without retry.
func (g *HTTPTaskBudgetGate) StopOwned(ctx context.Context, b TaskBudgetBinding) error {
	id := "host:" + b.HostID + ":" + strconv.FormatUint(b.Epoch, 10)
	event := TaskBudgetEvent{Binding: b, ID: OwnedStopEventID(b), Kind: "worker-stop", WorkerID: id, Evidence: TaskBudgetEvidence{ID: OwnedStopEventID(b), Kind: "owned-root-stop", Reference: b.PodUID}}
	raw, _ := json.Marshal(event)
	status, body, err := g.ownedRequest(ctx, http.MethodPost, TaskBudgetPath(b.TaskUID)+"/events", raw)
	if err != nil || status != http.StatusOK {
		return ErrTaskBudgetGate
	}
	st, err := decodeOwnedStatus(body)
	if err != nil || !sameOwnedWireBinding(st.Binding, b) || st.LastEvidence == nil || *st.LastEvidence != event.Evidence {
		return ErrTaskBudgetGate
	}
	return nil
}

func sameOwnedWireBinding(a, b TaskBudgetBinding) bool {
	return a.TaskUID == b.TaskUID && a.Epoch == b.Epoch && a.HostID == b.HostID && a.PodUID == b.PodUID && a.Deadline.Equal(b.Deadline)
}
func decodeOwnedStatus(raw []byte) (TaskBudgetStatus, error) {
	var st TaskBudgetStatus
	if !unambiguousBudgetStatus(raw) {
		return st, ErrTaskBudgetGate
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&st) != nil || d.Decode(new(any)) != io.EOF || !st.Observed {
		return TaskBudgetStatus{}, ErrTaskBudgetGate
	}
	return st, nil
}
func (g *HTTPTaskBudgetGate) ownedRequest(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	if g == nil || g.Client == nil || g.Token == nil {
		return 0, nil, ErrTaskBudgetGate
	}
	u, err := url.Parse(g.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return 0, nil, ErrTaskBudgetGate
	}
	token, err := g.Token(ctx)
	if err != nil || strings.TrimSpace(token) == "" {
		return 0, nil, ErrTaskBudgetGate
	}
	req, err := http.NewRequestWithContext(ctx, method, g.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, ErrTaskBudgetGate
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	hc := *g.Client
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, ErrTaskBudgetGate
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return 0, nil, ErrTaskBudgetGate
	}
	return resp.StatusCode, raw, nil
}

// ReadOwnedStatus performs one bounded read. It never infers continuation from
// a local receipt or retries an unknown mutation acknowledgement.
func (g *HTTPTaskBudgetGate) ReadOwnedStatus(ctx context.Context, uid string) (TaskBudgetStatus, error) {
	status, raw, err := g.ownedRequest(ctx, http.MethodGet, TaskBudgetPath(uid), nil)
	if err != nil || status != http.StatusOK {
		return TaskBudgetStatus{}, ErrTaskBudgetGate
	}
	st, err := decodeOwnedStatus(raw)
	if err != nil || st.Binding.TaskUID != uid {
		return TaskBudgetStatus{}, ErrTaskBudgetGate
	}
	return st, nil
}
