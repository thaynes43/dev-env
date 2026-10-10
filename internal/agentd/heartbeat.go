package agentd

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// HeartbeatClient posts agentd's status to the operator (D-08, D-41).
type HeartbeatClient struct {
	URL       string
	TokenFile string
	Session   string
	HTTP      *http.Client
}

// heartbeatTimeout bounds one heartbeat request.
const heartbeatTimeout = 10 * time.Second

// NewHeartbeatClient builds the client from the settings. It trusts the
// system roots plus APICAFile when that is set.
func NewHeartbeatClient(s Settings, session string) (*HeartbeatClient, error) {
	if s.APIURL == "" {
		return nil, errors.New("AGENTD_API_URL is not set")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if s.APICAFile != "" {
		pem, err := os.ReadFile(s.APICAFile)
		if err != nil {
			return nil, fmt.Errorf("AGENTD_API_CA_FILE: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("AGENTD_API_CA_FILE %s holds no PEM certificate", s.APICAFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &HeartbeatClient{
		URL:       s.APIURL,
		TokenFile: s.APITokenFile,
		Session:   session,
		HTTP:      &http.Client{Transport: tr, Timeout: heartbeatTimeout},
	}, nil
}

// Send posts one status. It reads the token file each time, because the
// kubelet rotates the projected token. Errors name the status code, never the
// token.
func (h *HeartbeatClient) Send(ctx context.Context, st protocol.Status) error {
	tok, err := os.ReadFile(h.TokenFile)
	if err != nil {
		return fmt.Errorf("API token: %w", err)
	}
	body, err := json.Marshal(st)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, heartbeatTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL+protocol.HeartbeatPath(h.Session), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 == 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	return fmt.Errorf("heartbeat refused: %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
}

// ReadDecisionAuthority uses the pod's rotating authenticated API token and an
// uncached own-UID-bound route. Local files never supply answer authority.
func (h *HeartbeatClient) ReadDecisionAuthority(ctx context.Context) (protocol.DecisionAuthorityResult, error) {
	var result protocol.DecisionAuthorityResult
	tok, err := os.ReadFile(h.TokenFile)
	if err != nil {
		return result, errors.New("decision authority token unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, heartbeatTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.URL+protocol.DecisionAuthorityPath(h.Session), nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	httpClient := *h.HTTP
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := httpClient.Do(req)
	if err != nil {
		return result, errors.New("decision authority observation unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return result, errors.New("decision authority observation refused")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2049))
	if err != nil || len(data) > 2048 {
		return result, errors.New("decision authority response exceeds bound")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&result) != nil || dec.Decode(new(any)) != io.EOF {
		return result, errors.New("decision authority response invalid")
	}
	return result, nil
}

func confirmDecisionAuthority(ctx context.Context, s Settings, r protocol.DecisionRecord) error {
	h, err := NewHeartbeatClient(s, r.Session)
	if err != nil {
		return errors.New("decision answer has no authenticated operator authority")
	}
	result, err := h.ReadDecisionAuthority(ctx)
	if err != nil || result.Authority == nil || !result.Authority.Confirms(r) {
		return errors.New("decision answer lacks exact confirmed coordinator authority")
	}
	return nil
}
