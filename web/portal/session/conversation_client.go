package session

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// conversationSource requires an explicit target scope and checks the request's
// own portal session against live group membership before and after fetching.
type conversationSource struct {
	authorizer *CurrentAuthorizer
	targets    map[string]string // Session UID -> exact name
	tokenPath  string
	client     *http.Client
	now        func() time.Time
}

// NewConversationSource configures a private source. Production calls it only
// when the private action file has a history scope.
func NewConversationSource(authorizer *CurrentAuthorizer, store *PostgresStore, targets map[string]string, tokenPath string, ca []byte, now func() time.Time) (ConversationSource, error) {
	if authorizer == nil || store == nil || store.pool == nil || authorizer.store != store || authorizer.client == nil || authorizer.now == nil || len(targets) == 0 || len(targets) > 256 || tokenPath == "" || now == nil {
		return nil, portal.ErrUnavailable
	}
	copied := make(map[string]string, len(targets))
	for uid, name := range targets {
		if !sessionUIDQuery.MatchString(uid) || name == "" || len(name) > 63 {
			return nil, portal.ErrUnavailable
		}
		copied[uid] = name
	}
	client, err := pinnedClient(ca, operatorPortalHost, 5*time.Second, nil)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	return &conversationSource{authorizer: authorizer, targets: copied, tokenPath: tokenPath, client: client, now: now}, nil
}

func (c *conversationSource) ReadConversation(ctx context.Context, name, uid string) (Conversation, error) {
	var empty Conversation
	if c == nil || c.authorizer == nil || c.client == nil || c.now == nil || !sessionUIDQuery.MatchString(uid) || c.targets[uid] != name {
		return empty, portal.ErrDenied
	}
	ref, ok := ctx.Value(sessionContextKey{}).(sessionReference)
	if !ok {
		return empty, portal.ErrDenied
	}
	resource := portal.Resource{Project: "dev-env", Environment: "internal"}
	if err := c.authorizer.Authorize(ctx, ref.principal, resource); err != nil {
		return empty, normalize(err)
	}
	token, err := operatorCredential(c.tokenPath, c.now())
	if err != nil {
		return empty, portal.ErrUnavailable
	}
	endpoint := operatorPortalOrigin + apiv1.PortalConversationPrefix + url.PathEscape(uid) + "/conversation"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return empty, portal.ErrUnavailable
	}
	req.Close = true
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	response, err := c.client.Do(req)
	if err != nil || response == nil || response.Body == nil {
		return empty, portal.ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || response.StatusCode != http.StatusOK {
		return empty, portal.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, protocol.PortalConversationEncodedLimit+1))
	if err != nil || len(data) > protocol.PortalConversationEncodedLimit {
		return empty, portal.ErrUnavailable
	}
	if !protocol.StrictPortalJSON(data) {
		return empty, portal.ErrUnavailable
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || !exactKeys(fields, []string{"sessionName", "sessionUID", "projection"}, "podUID") || !protocol.StrictPortalConversationJSON(fields["projection"]) {
		return empty, portal.ErrUnavailable
	}
	var result apiv1.PortalConversationResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF || result.SessionName != name || result.SessionUID != uid || len(result.PodUID) > 128 || (result.Projection.State != "unavailable" && result.PodUID == "") || !result.Projection.Valid() {
		return empty, portal.ErrUnavailable
	}
	if err = c.authorizer.Authorize(ctx, ref.principal, resource); err != nil {
		return empty, normalize(err)
	}
	out := Conversation{State: result.Projection.State, ReasonCode: result.Projection.ReasonCode}
	for _, turn := range result.Projection.Turns {
		out.Turns = append(out.Turns, ConversationTurn{At: turn.At, Role: turn.Role, From: turn.From, Text: turn.Text, Truncated: turn.Truncated})
	}
	return out, nil
}
