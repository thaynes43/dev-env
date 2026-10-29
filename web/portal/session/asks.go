package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

const AsksPath = "/portal/projects/dev-env/environments/internal/asks"
const asksPageSize = 20

var errAsksNotConnected = errors.New("asks not connected")

// AsksSource uses the same pinned operator transport and rotating credential
// as Sessions. A nil source disables both the board's reads and its answers.
type AsksSource struct {
	path   string
	client *http.Client
	now    func() time.Time
}

// config/asks.json is opt-in: {"enabled":true}. Missing or false leaves
// credentials untouched and the board truthfully disconnected.
func optionalAsksSource(root string, ca []byte) (*AsksSource, error) {
	present, err := mountedPresent(filepath.Join(root, "config/asks.json"))
	if err != nil || !present {
		return nil, err
	}
	raw, err := readMountedBytes(root, "config/asks.json", 8192)
	if err != nil || !protocol.StrictPortalJSON(raw) {
		return nil, errProductionConfig
	}
	fields, err := jsonObject(raw)
	if err != nil || !exactKeys(fields, []string{"enabled"}) {
		return nil, errProductionConfig
	}
	enabled, ok := jsonBool(fields["enabled"])
	if !ok {
		return nil, errProductionConfig
	}
	if !enabled {
		return nil, nil
	}
	path := filepath.Join(root, "secrets/OPERATOR_TOKEN")
	if _, err := operatorCredential(path, time.Now()); err != nil {
		return nil, errProductionConfig
	}
	return NewAsksSource(path, ca, time.Now)
}

func NewAsksSource(tokenPath string, ca []byte, now func() time.Time) (*AsksSource, error) {
	source, err := newSessionSource(tokenPath, ca, now, nil)
	if err != nil {
		return nil, err
	}
	return &AsksSource{path: source.path, client: source.client, now: source.now}, nil
}

func (s *AsksSource) read(ctx context.Context) ([]apiv1.Ask, bool, error) {
	if s == nil || s.client == nil || s.now == nil {
		return nil, false, portal.ErrUnavailable
	}
	token, err := operatorCredential(s.path, s.now())
	if err != nil {
		return nil, false, portal.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, sessionReadTimeout)
	defer cancel()
	// Read open asks independently so a busy answered history cannot hide
	// an older question. Both collections share the same bounded read budget.
	open, openComplete, err := s.readState(ctx, "open", token)
	if err != nil {
		return nil, false, err
	}
	all, allComplete, err := s.readState(ctx, "all", token)
	if err != nil {
		return nil, false, err
	}
	positions := map[string]int{}
	asks := append([]apiv1.Ask(nil), open...)
	for i, ask := range asks {
		positions[ask.ID] = i
	}
	for _, ask := range all {
		if i, found := positions[ask.ID]; found {
			asks[i] = ask
		} else {
			positions[ask.ID] = len(asks)
			asks = append(asks, ask)
		}
	}
	return asks, openComplete && allComplete, nil
}

func (s *AsksSource) readState(ctx context.Context, state, token string) ([]apiv1.Ask, bool, error) {
	var asks []apiv1.Ask
	seen, cursors := map[string]bool{}, map[string]bool{}
	cursor := ""
	for n := 0; n < productPageCap; n++ {
		query := url.Values{"state": {state}, "limit": {strconv.Itoa(asksPageSize)}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		body, err := s.get(ctx, query, token)
		if err != nil {
			return nil, false, err
		}
		page, err := decodeAsksPage(body)
		if err != nil {
			return nil, false, err
		}
		for _, ask := range page.Asks {
			if seen[ask.ID] || (state == "open" && ask.State != apiv1.AskStateOpen) {
				return nil, false, portal.ErrUnavailable
			}
			seen[ask.ID] = true
			asks = append(asks, ask)
		}
		if page.Complete {
			return asks, true, nil
		}
		if cursors[page.NextCursor] {
			return nil, false, portal.ErrUnavailable
		}
		cursor, cursors[page.NextCursor] = page.NextCursor, true
	}
	return asks, false, nil
}

func (s *AsksSource) get(ctx context.Context, query url.Values, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, operatorPortalOrigin+apiv1.PortalAsksPath+"?"+query.Encode(), nil)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", mediaJSON)
	response, err := s.client.Do(req)
	if err != nil || response == nil || response.Body == nil {
		return nil, portal.ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return nil, errAsksNotConnected
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != mediaJSON || response.StatusCode != http.StatusOK {
		return nil, portal.ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, productHALLimit+1))
	if err != nil || int64(len(raw)) > productHALLimit {
		return nil, portal.ErrUnavailable
	}
	return raw, nil
}

func decodeAsksPage(body []byte) (apiv1.AskList, error) {
	var page apiv1.AskList
	if !protocol.StrictPortalJSON(body) {
		return page, portal.ErrUnavailable
	}
	fields, ok := askObject(body, []string{"asks", "complete"}, "nextCursor")
	if !ok {
		return page, portal.ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&page) != nil || decoder.Decode(new(any)) != io.EOF || len(page.Asks) > asksPageSize || page.Asks == nil || (page.Complete && page.NextCursor != "") || (!page.Complete && (page.NextCursor == "" || len(page.NextCursor) > 1024)) {
		return apiv1.AskList{}, portal.ErrUnavailable
	}
	rows, ok := jsonArray(fields["asks"])
	if !ok {
		return apiv1.AskList{}, portal.ErrUnavailable
	}
	for _, row := range rows {
		if !strictAskJSON(row) {
			return apiv1.AskList{}, portal.ErrUnavailable
		}
	}
	for _, ask := range page.Asks {
		if err := apiv1.ValidateAsk(ask); err != nil {
			return apiv1.AskList{}, portal.ErrUnavailable
		}
	}
	return page, nil
}

func (s *AsksSource) answer(ctx context.Context, id string, request apiv1.AskAnswerRequest) error {
	if s == nil || s.client == nil || s.now == nil || !apiv1.ValidAskID(id) || request.Validate() != nil {
		return portal.ErrUnavailable
	}
	token, err := operatorCredential(s.path, s.now())
	if err != nil {
		return portal.ErrUnavailable
	}
	body, err := json.Marshal(request)
	if err != nil {
		return portal.ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, operatorPortalOrigin+apiv1.PortalAsksPath+"/"+id+"/answer", bytes.NewReader(body))
	if err != nil {
		return portal.ErrUnavailable
	}
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mediaJSON)
	req.Header.Set("Accept", mediaJSON)
	response, err := s.client.Do(req)
	if err != nil || response == nil || response.Body == nil {
		return portal.ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != mediaJSON || response.StatusCode != http.StatusOK {
		return portal.ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 || !protocol.StrictPortalJSON(raw) {
		return portal.ErrUnavailable
	}
	var ask apiv1.Ask
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if !strictAskJSON(raw) || decoder.Decode(&ask) != nil || decoder.Decode(new(any)) != io.EOF || apiv1.ValidateAsk(ask) != nil || ask.ID != id || ask.State != apiv1.AskStateAnswered || ask.Answer == nil || ask.Answer.OptionID != request.OptionID || ask.Answer.Text != request.Text || ask.Answer.Approved != request.Approved || ask.Answer.AnsweredBy != request.Actor {
		return portal.ErrUnavailable
	}
	return nil
}

func serveAsks(w http.ResponseWriter, r *http.Request, c AssemblyConfig) {
	authHeaders(w)
	// Leave a second under the server's write deadline for the bounded render.
	ctx, cancel := context.WithTimeout(r.Context(), 19*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	if r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || c.Now == nil || (r.Method != http.MethodGet && r.Method != http.MethodPost) || (r.Method == http.MethodGet && r.URL.Path != AsksPath) || (r.Method == http.MethodPost && r.URL.Path != AsksPath+"/answer") {
		authFailure(w, portal.ErrDenied)
		return
	}
	r, key, record, err := authorizedAction(c.Session, c.Authorizer, r)
	if err != nil {
		if r.Method == http.MethodGet && errors.Is(err, portal.ErrDenied) {
			// Only missing authentication starts sign-in; denied group members stay denied.
			if _, authErr := c.Session.Authenticate(r); errors.Is(authErr, portal.ErrDenied) {
				w.Header().Set("Location", "/auth/login")
				w.WriteHeader(http.StatusSeeOther)
				return
			}
		}
		authFailure(w, err)
		return
	}
	page := portal.AsksPage{Notice: "Asks are not connected"}
	if r.Method == http.MethodPost {
		if c.Asks == nil {
			authFailure(w, portal.ErrUnavailable)
			return
		}
		if err := parseAskForm(w, r, c.Session, record); err != nil {
			authFailure(w, err)
			return
		}
	}
	var asks []apiv1.Ask
	if c.Asks != nil {
		var complete bool
		asks, complete, err = c.Asks.read(r.Context())
		if err != nil {
			page.Notice, page.Retry = "Asks are unavailable. Open asks could not be read.", true
			if errors.Is(err, errAsksNotConnected) {
				page.Notice, page.Retry = "Asks are not connected", false
			}
		} else {
			page.Connected, page.Complete = true, complete
			page.Notice = ""
			if !complete {
				page.Notice = "This is a partial read. More asks may need attention."
			}
		}
	}
	if r.Method == http.MethodPost {
		if c.Asks == nil || err != nil {
			authFailure(w, portal.ErrUnavailable)
			return
		}
		err = postAskAnswer(w, r, c, key, record, asks)
		if err != nil {
			authFailure(w, err)
			return
		}
		w.Header().Set("Location", AsksPath)
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	_, freshKey, fresh, err := authorizedAction(c.Session, c.Authorizer, r)
	if err != nil || !sameAskSession(key, record, freshKey, fresh) {
		authFailure(w, portal.ErrDenied)
		return
	}
	page.Asks, page.Now, page.CSRF = asks, c.Now(), record.CSRF
	_, page.CanAnswer = c.Session.config.Store.(commandReceipts)
	body, policy, err := portal.RenderAsks(page, maximumResponse*7/8)
	if err != nil {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	w.Header().Set("Content-Security-Policy", policy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

func sameAskSession(key Handle, record Record, freshKey Handle, fresh Record) bool {
	return freshKey == key && fresh.Principal == record.Principal && sha256.Sum256([]byte(fresh.accessToken)) == sha256.Sum256([]byte(record.accessToken)) && subtle.ConstantTimeCompare([]byte(fresh.CSRF), []byte(record.CSRF)) == 1
}

func postAskAnswer(w http.ResponseWriter, r *http.Request, c AssemblyConfig, key Handle, record Record, asks []apiv1.Ask) error {
	store, ok := c.Session.config.Store.(commandReceipts)
	if !ok {
		return portal.ErrDenied
	}
	id, requestID := r.PostForm.Get("askID"), r.PostForm.Get("requestID")
	if !launchRequestID.MatchString(requestID) {
		return portal.ErrDenied
	}
	var selected *apiv1.Ask
	for i := range asks {
		if asks[i].ID == id {
			selected = &asks[i]
			break
		}
	}
	if selected == nil {
		return portal.ErrDenied
	}
	ask := *selected
	wantKeys := 5
	request := apiv1.AskAnswerRequest{OptionID: r.PostForm.Get("optionId"), Text: strings.TrimSpace(r.PostForm.Get("text")), Actor: apiv1.AskActor{Issuer: record.Principal.Issuer, Subject: record.Principal.Subject}}
	if ask.Type == apiv1.AskTypeApproval {
		wantKeys = 7
		if len(r.PostForm["decision"]) != 1 || len(r.PostForm["confirm"]) != 1 || r.PostForm.Get("confirm") != "yes" || (r.PostForm.Get("decision") != "approve" && r.PostForm.Get("decision") != "decline") {
			return portal.ErrDenied
		}
		request.Approved = r.PostForm.Get("decision") == "approve"
	}
	if len(r.PostForm) != wantKeys {
		return portal.ErrDenied
	}
	validationAsk := ask
	validationAsk.State = apiv1.AskStateOpen
	if request.ValidateFor(validationAsk) != nil {
		return portal.ErrDenied
	}
	// Bind replay protection to the immutable scope and answer. Namespacing
	// prevents collision with a send form's request ID in the shared reservation store.
	encoded, err := json.Marshal(struct {
		ID     string
		Scope  *apiv1.AskScope
		Answer apiv1.AskAnswerRequest
	}{ask.ID, ask.Scope, request})
	if err != nil {
		return portal.ErrDenied
	}
	wanted := commandReservation{RequestID: "asks-" + requestID, SessionName: ask.Context.SessionName, SessionUID: ask.Context.SessionUID, Digest: sha256.Sum256(encoded)}
	created, err := store.reserveCommand(r.Context(), key, wanted)
	if err != nil {
		return err
	}
	reservation, _, err := store.readCommand(r.Context(), key, wanted.RequestID)
	if err != nil || reservation.RequestID != wanted.RequestID || reservation.SessionName != wanted.SessionName || reservation.SessionUID != wanted.SessionUID || reservation.Digest != wanted.Digest {
		return portal.ErrDenied
	}
	freshRequest, freshKey, freshRecord, err := authorizedAction(c.Session, c.Authorizer, r)
	if err != nil || !sameAskSession(key, record, freshKey, freshRecord) {
		return portal.ErrDenied
	}
	if !created {
		return nil
	}
	if ask.State != apiv1.AskStateOpen || !c.Now().Before(ask.ExpiresAt) || (ask.Scope != nil && !c.Now().Before(ask.Scope.ExpiresAt)) || request.ValidateFor(ask) != nil {
		return portal.ErrDenied
	}
	return c.Asks.answer(freshRequest.Context(), id, request)
}

func parseAskForm(w http.ResponseWriter, r *http.Request, engine *Engine, record Record) error {
	if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != engine.config.Origin {
		return portal.ErrDenied
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" {
		return portal.ErrDenied
	}
	// Up to four UTF-8 bytes per rune, each URL-encoded as three bytes.
	r.Body = http.MaxBytesReader(w, r.Body, 12*apiv1.AskAnswerTextMaxRunes+4096)
	if r.ParseForm() != nil {
		return portal.ErrDenied
	}
	for _, name := range []string{"csrf", "requestID", "askID", "optionId", "text"} {
		if len(r.PostForm[name]) != 1 {
			return portal.ErrDenied
		}
	}
	for name, values := range r.PostForm {
		if len(values) != 1 {
			return portal.ErrDenied
		}
		switch name {
		case "csrf", "requestID", "askID", "optionId", "text", "decision", "confirm":
		default:
			return portal.ErrDenied
		}
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(record.CSRF)) != 1 {
		return portal.ErrDenied
	}
	return nil
}

// Match exact JSON field names, reject nulls and duplicate keys at every level,
// then apply the shared typed validators. encoding/json alone accepts null and
// case-folded names for scalar struct fields.
func askObject(raw []byte, required []string, optional ...string) (map[string]json.RawMessage, bool) {
	fields, err := jsonObject(raw)
	if err != nil || !exactKeys(fields, required, optional...) {
		return nil, false
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, false
		}
	}
	return fields, true
}

func strictAskJSON(raw []byte) bool {
	fields, ok := askObject(raw, []string{"id", "type", "question", "recommended", "allowFreeText", "context", "createdAt", "expiresAt", "state"}, "options", "scope", "answer", "delivery")
	if !ok {
		return false
	}
	if _, ok := askObject(fields["context"], []string{"sessionName", "sessionUID"}, "workPackage", "links"); !ok {
		return false
	}
	if options, present := fields["options"]; present {
		rows, ok := jsonArray(options)
		if !ok {
			return false
		}
		for _, row := range rows {
			if _, ok := askObject(row, []string{"id", "label"}, "description"); !ok {
				return false
			}
		}
	}
	if scope, present := fields["scope"]; present {
		if _, ok := askObject(scope, []string{"action", "target", "expiresAt"}); !ok {
			return false
		}
	}
	if answer, present := fields["answer"]; present {
		row, ok := askObject(answer, []string{"optionId", "approved", "answeredBy", "answeredAt"}, "text")
		if !ok {
			return false
		}
		if _, ok := askObject(row["answeredBy"], []string{"issuer", "subject"}, "displayName"); !ok {
			return false
		}
	}
	if delivery, present := fields["delivery"]; present {
		if _, ok := askObject(delivery, []string{"inboxMessage", "deliveredAt"}); !ok {
			return false
		}
	}
	return true
}
