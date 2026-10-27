package session

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// commandClient has one fixed destination and uses the existing rotating
// operator credential. It never retries a request or follows redirects.
type commandClient struct {
	path   string
	client *http.Client
	now    func() time.Time
}

func newCommandClient(tokenPath string, ca []byte, now func() time.Time, dial func(context.Context, string, string) (net.Conn, error)) (*commandClient, error) {
	client, err := pinnedClient(ca, operatorPortalHost, 65*time.Second, dial)
	if err != nil || tokenPath == "" || now == nil {
		return nil, portal.ErrUnavailable
	}
	return &commandClient{path: tokenPath, client: client, now: now}, nil
}

// Every error after dispatch is ambiguous to the caller. It must retain the
// reservation and show Unknown, never submit the same request again.
func (c *commandClient) send(ctx context.Context, request apiv1.PortalMessageRequest) (apiv1.PortalMessageReceipt, error) {
	var receipt apiv1.PortalMessageReceipt
	if c == nil || c.client == nil || c.now == nil {
		return receipt, portal.ErrUnavailable
	}
	token, err := operatorCredential(c.path, c.now())
	if err != nil {
		return receipt, portal.ErrUnavailable
	}
	body, err := json.Marshal(request)
	if err != nil {
		return receipt, portal.ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, operatorPortalOrigin+apiv1.PortalSessionMessagesPath, bytes.NewReader(body))
	if err != nil {
		return receipt, portal.ErrUnavailable
	}
	// Disable recreation of the body, so even transport-level retries cannot
	// replay a command whose first attempt may have reached the operator.
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return receipt, portal.ErrUnavailable
	}
	if response == nil || response.Body == nil {
		return receipt, portal.ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || response.StatusCode != http.StatusOK {
		return receipt, portal.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 16*1024+1))
	if err != nil || len(data) > 16*1024 {
		return receipt, portal.ErrUnavailable
	}
	fields, err := jsonObject(data)
	if err != nil || !exactKeys(fields, []string{"requestID", "sessionName", "sessionUID", "observedAt", "transportStatus", "reason"}, "resolvedPodUID") {
		return receipt, portal.ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || decoder.Decode(&struct{}{}) != io.EOF || receipt.RequestID != request.RequestID || receipt.SessionName != request.SessionName || receipt.SessionUID != request.SessionUID || receipt.ObservedAt.IsZero() || len(receipt.ResolvedPodUID) > 128 || !validCommandReason(receipt.TransportStatus, receipt.Reason) || (receipt.TransportStatus == apiv1.PortalTransportDelivered && receipt.ResolvedPodUID == "") {
		return apiv1.PortalMessageReceipt{}, portal.ErrUnavailable
	}
	return receipt, nil
}
