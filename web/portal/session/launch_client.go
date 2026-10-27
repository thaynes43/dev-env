package session

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// launchOutcome is what the portal knows about one launch after it asked the
// operator. Only launchCreated means a session exists. launchUnknown means one
// may or may not: the request was dispatched and the answer is missing or
// unreadable, so the caller must never send it again on its own.
type launchOutcome string

const (
	launchCreated     launchOutcome = "created"
	launchLimit       launchOutcome = "limit"
	launchUnavailable launchOutcome = "unavailable"
	launchInvalid     launchOutcome = "invalid"
	launchConflict    launchOutcome = "conflict"
	launchUnknown     launchOutcome = "unknown"
)

// launchResult is the typed outcome. Receipt is set for launchCreated; Fields
// are the operator's field errors for launchInvalid.
type launchResult struct {
	Outcome launchOutcome
	Receipt apiv1.PortalLaunchReceipt
	Fields  []apiv1.FieldError
}

type launchTransport interface {
	launch(context.Context, apiv1.PortalLaunchRequest) launchResult
}

// launchClient has one fixed destination and uses the existing rotating
// operator credential, like commandClient. It never retries or follows redirects.
type launchClient struct {
	path   string
	client *http.Client
	now    func() time.Time
}

func newLaunchClient(tokenPath string, ca []byte, now func() time.Time, dial func(context.Context, string, string) (net.Conn, error)) (*launchClient, error) {
	client, err := pinnedClient(ca, operatorPortalHost, 65*time.Second, dial)
	if err != nil || tokenPath == "" || now == nil {
		return nil, portal.ErrUnavailable
	}
	return &launchClient{path: tokenPath, client: client, now: now}, nil
}

var (
	launchSessionName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	launchSessionUID  = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	launchPhase       = regexp.MustCompile(`^[A-Za-z]{1,32}$`)
)

// launch posts the request once. Before dispatch, a failure is unavailable: the
// operator never saw it. After dispatch, anything but a clear refusal or a
// valid receipt is unknown.
func (c *launchClient) launch(ctx context.Context, request apiv1.PortalLaunchRequest) launchResult {
	if c == nil || c.client == nil || c.now == nil {
		return launchResult{Outcome: launchUnavailable}
	}
	token, err := operatorCredential(c.path, c.now())
	if err != nil {
		return launchResult{Outcome: launchUnavailable}
	}
	// A prompt's "<" and "&" stay single bytes; the operator checks the size.
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	if enc.Encode(request) != nil {
		return launchResult{Outcome: launchUnavailable}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, operatorPortalOrigin+apiv1.PortalLaunchPath, &body)
	if err != nil {
		return launchResult{Outcome: launchUnavailable}
	}
	// No recreated body: even a transport-level retry cannot replay a launch whose
	// first attempt may have reached the operator.
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := c.client.Do(req)
	if err != nil || response == nil || response.Body == nil {
		return launchResult{Outcome: launchUnknown}
	}
	defer func() { _ = response.Body.Close() }()
	// These answers need no body: the operator refused the caller, has no such
	// route (its router answers text/plain), hit the cap, or already has a
	// different launch for this request. Nothing new was created in any of them.
	switch response.StatusCode {
	case http.StatusForbidden, http.StatusUnauthorized, http.StatusNotFound:
		return launchResult{Outcome: launchUnavailable}
	case http.StatusTooManyRequests:
		return launchResult{Outcome: launchLimit}
	case http.StatusConflict:
		return launchResult{Outcome: launchConflict}
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return launchResult{Outcome: launchUnknown}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 16*1024+1))
	if err != nil || len(data) > 16*1024 {
		return launchResult{Outcome: launchUnknown}
	}
	switch response.StatusCode {
	case http.StatusOK, http.StatusCreated:
		receipt, ok := decodeLaunchReceipt(data, request.RequestID)
		if !ok {
			return launchResult{Outcome: launchUnknown}
		}
		return launchResult{Outcome: launchCreated, Receipt: receipt}
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return launchResult{Outcome: launchInvalid, Fields: decodeLaunchFields(data)}
	}
	return launchResult{Outcome: launchUnknown}
}

// decodeLaunchReceipt accepts exactly the operator's receipt for this request.
func decodeLaunchReceipt(data []byte, requestID string) (apiv1.PortalLaunchReceipt, bool) {
	var receipt apiv1.PortalLaunchReceipt
	fields, err := jsonObject(data)
	if err != nil || !exactKeys(fields, []string{"requestID", "sessionName", "sessionUID", "phase", "observedAt"}) {
		return receipt, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || decoder.Decode(&struct{}{}) != io.EOF || receipt.RequestID != requestID ||
		!launchSessionName.MatchString(receipt.SessionName) || !launchSessionUID.MatchString(receipt.SessionUID) ||
		!launchPhase.MatchString(receipt.Phase) || receipt.ObservedAt.IsZero() {
		return apiv1.PortalLaunchReceipt{}, false
	}
	return receipt, true
}

// decodeLaunchFields reads the operator's field errors out of its error body,
// bounded. A body it cannot read yields none; the result page then shows only
// its fixed wording.
func decodeLaunchFields(data []byte) []apiv1.FieldError {
	var body struct {
		Error struct {
			Fields []apiv1.FieldError `json:"fields"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &body) != nil {
		return nil
	}
	if len(body.Error.Fields) > 8 {
		body.Error.Fields = body.Error.Fields[:8]
	}
	return body.Error.Fields
}
