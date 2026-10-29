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
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// AsksPath implements Epic #254 contracts 2579 (v1.1) and 2595 (v1.2).
// Design and operator routes are separate lanes; this opt-in board issues no
// grants and performs no action described by an approval.
const AsksPath = "/portal/projects/dev-env/environments/internal/asks"

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

// Open coverage is independent of the single page of history. A failed or
// truncated history never suppresses the open asks or their coverage.
type asksRead struct {
	Asks                                              []apiv1.Ask
	OpenComplete, HistoryComplete, HistoryUnavailable bool
	NextCursor                                        string
}

func (s *AsksSource) read(ctx context.Context, historyCursor string) (asksRead, error) {
	var result asksRead
	token, err := s.credential()
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, sessionReadTimeout)
	defer cancel()
	result.Asks, result.OpenComplete, err = s.readOpen(ctx, token)
	if err != nil {
		return asksRead{}, err
	}
	page, err := s.readPage(ctx, apiv1.AskStateFilterAll, historyCursor, token)
	if err != nil {
		result.HistoryUnavailable = true
		return result, nil
	}
	result.HistoryComplete, result.NextCursor = page.Complete, page.NextCursor
	positions := map[string]int{}
	for i, ask := range result.Asks {
		positions[ask.ID] = i
	}
	for _, ask := range page.Asks {
		if i, found := positions[ask.ID]; found {
			result.Asks[i] = ask
		} else {
			result.Asks = append(result.Asks, ask)
		}
	}
	return result, nil
}

func (s *AsksSource) credential() (string, error) {
	if s == nil || s.client == nil || s.now == nil {
		return "", portal.ErrUnavailable
	}
	token, err := operatorCredential(s.path, s.now())
	if err != nil {
		return "", readFailure("credential_unavailable")
	}
	return token, nil
}

func (s *AsksSource) readOpen(ctx context.Context, token string) ([]apiv1.Ask, bool, error) {
	var asks []apiv1.Ask
	seen, cursors := map[string]bool{}, map[string]bool{}
	cursor := ""
	for n := 0; n < productPageCap; n++ {
		page, err := s.readPage(ctx, apiv1.AskStateFilterOpen, cursor, token)
		if err != nil {
			return nil, false, err
		}
		for _, ask := range page.Asks {
			if seen[ask.ID] || ask.State != apiv1.AskStateOpen {
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

func (s *AsksSource) readPage(ctx context.Context, state, cursor, token string) (apiv1.AskList, error) {
	query := url.Values{apiv1.AskQueryState: {state}, apiv1.AskQueryLimit: {strconv.Itoa(apiv1.AskListDefaultLimit)}}
	if cursor != "" {
		query.Set(apiv1.AskQueryCursor, cursor)
	}
	raw, err := s.get(ctx, apiv1.PortalAsksPath+"?"+query.Encode(), token)
	if err != nil {
		return apiv1.AskList{}, err
	}
	return decodeAsksPage(raw)
}

func (s *AsksSource) readAsk(ctx context.Context, id string) (apiv1.Ask, error) {
	if !apiv1.ValidAskID(id) {
		return apiv1.Ask{}, portal.ErrDenied
	}
	token, err := s.credential()
	if err != nil {
		return apiv1.Ask{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, sessionReadTimeout)
	defer cancel()
	raw, err := s.get(ctx, apiv1.PortalAskPath(id), token)
	if err != nil {
		return apiv1.Ask{}, err
	}
	ask, err := decodeAsk(raw)
	if err != nil || ask.ID != id {
		return apiv1.Ask{}, portal.ErrUnavailable
	}
	return ask, nil
}

func (s *AsksSource) get(ctx context.Context, path, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, operatorPortalOrigin+path, nil)
	if err != nil {
		return nil, readFailure("response_contract")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", mediaJSON)
	response, err := s.client.Do(req)
	if err != nil {
		return nil, sessionTransportFailure(ctx, err)
	}
	if response == nil || response.Body == nil {
		return nil, readFailure("response_contract")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound && strings.HasPrefix(path, apiv1.PortalAsksPath+"?") {
		return nil, errAsksNotConnected
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, portal.ErrDenied
	}
	if response.StatusCode != http.StatusOK {
		failure := readFailure("operator_refused")
		failure.HTTPStatus = response.StatusCode
		return nil, failure
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != mediaJSON {
		return nil, readFailure("response_contract")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, productHALLimit+1))
	if err != nil {
		return nil, sessionTransportFailure(ctx, err)
	}
	if int64(len(raw)) > productHALLimit {
		return nil, readFailure("response_contract")
	}
	return raw, nil
}

func decodeAsk(raw []byte) (apiv1.Ask, error) {
	var ask apiv1.Ask
	if !utf8.Valid(raw) || !protocol.StrictPortalJSON(raw) || !strictAskJSON(raw) {
		return ask, portal.ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&ask) != nil || decoder.Decode(new(any)) != io.EOF || apiv1.ValidateAsk(ask) != nil {
		return apiv1.Ask{}, portal.ErrUnavailable
	}
	return ask, nil
}

func decodeAsksPage(body []byte) (apiv1.AskList, error) {
	var page apiv1.AskList
	if !utf8.Valid(body) || !protocol.StrictPortalJSON(body) {
		return page, portal.ErrUnavailable
	}
	fields, ok := askObject(body, []string{"asks", "complete"}, "nextCursor")
	if !ok {
		return page, portal.ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&page) != nil || decoder.Decode(new(any)) != io.EOF || len(page.Asks) > apiv1.AskListDefaultLimit || page.Asks == nil || (page.Complete && page.NextCursor != "") || (!page.Complete && (page.NextCursor == "" || len(page.NextCursor) > 1024)) {
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
	seen := map[string]bool{}
	for _, ask := range page.Asks {
		if err := apiv1.ValidateAsk(ask); err != nil || seen[ask.ID] {
			return apiv1.AskList{}, portal.ErrUnavailable
		}
		seen[ask.ID] = true
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, operatorPortalOrigin+apiv1.PortalAskAnswerPath(id), bytes.NewReader(body))
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
	switch response.StatusCode {
	case http.StatusBadRequest:
		return apiv1.ErrAskInvalid
	case http.StatusConflict:
		return apiv1.ErrAskNotOpen
	case http.StatusGone:
		return apiv1.ErrAskExpired
	case http.StatusUnauthorized, http.StatusForbidden:
		return portal.ErrDenied
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != mediaJSON || response.StatusCode != http.StatusOK {
		return portal.ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return portal.ErrUnavailable
	}
	ask, err := decodeAsk(raw)
	if err != nil || ask.ID != id || ask.State != apiv1.AskStateAnswered || !sameAskAnswer(ask.Answer, request) {
		return portal.ErrUnavailable
	}
	return nil
}

// Only these finite, validated GET queries reach the operator. POSTs carry
// their target in the CSRF-protected form and never accept a query.
func asksQuery(r *http.Request) (id, cursor string, ok bool) {
	if r.URL.ForceQuery {
		return "", "", false
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", "", false
	}
	if r.Method == http.MethodPost {
		return "", "", r.URL.RawQuery == ""
	}
	for key, values := range q {
		if len(values) != 1 {
			return "", "", false
		}
		switch key {
		case "ask":
			if !apiv1.ValidAskID(values[0]) {
				return "", "", false
			}
			id = values[0]
		case apiv1.AskQueryCursor:
			if values[0] == "" || len(values[0]) > 1024 || !utf8.ValidString(values[0]) || strings.ContainsAny(values[0], "\r\n\x00") {
				return "", "", false
			}
			cursor = values[0]
		default:
			return "", "", false
		}
	}
	return id, cursor, true
}

func serveAsks(w http.ResponseWriter, r *http.Request, c AssemblyConfig) {
	authHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 19*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	selectedID, cursor, queryOK := asksQuery(r)
	if r.URL.RawPath != "" || !queryOK || c.Now == nil || (r.Method != http.MethodGet && r.Method != http.MethodPost) || (r.Method == http.MethodGet && r.URL.Path != AsksPath) || (r.Method == http.MethodPost && r.URL.Path != AsksPath+"/answer") {
		authFailure(w, portal.ErrDenied)
		return
	}
	r, key, record, err := authorizedAction(c.Session, c.Authorizer, r)
	if err != nil {
		if r.Method == http.MethodGet && errors.Is(err, portal.ErrDenied) {
			if _, authErr := c.Session.Authenticate(r); errors.Is(authErr, portal.ErrDenied) {
				w.Header().Set("Location", "/auth/login")
				w.WriteHeader(http.StatusSeeOther)
				return
			}
		}
		authFailure(w, err)
		return
	}
	page := portal.AsksPage{Notice: "Asks are not connected", SelectedID: selectedID}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		if c.Asks == nil {
			authFailure(w, portal.ErrUnavailable)
			return
		}
		if err := parseAskForm(w, r, c.Session, record); err != nil {
			authFailure(w, err)
			return
		}
		id := r.PostForm.Get("askID")
		if !apiv1.ValidAskID(id) {
			authFailure(w, portal.ErrDenied)
			return
		}
		ask, err := c.Asks.readAsk(r.Context(), id)
		if err != nil {
			if errors.Is(err, portal.ErrUnavailable) {
				page.SelectedID, page.Retry = id, true
				page.Notice = "This ask could not be read, so your answer was not sent. Refresh to try again."
				renderAsks(w, r, c, key, record, page, http.StatusServiceUnavailable)
				return
			}
			authFailure(w, err)
			return
		}
		err = postAskAnswer(r, c, key, record, ask)
		if err == nil {
			w.Header().Set("Location", AsksPath+"?ask="+id)
			w.WriteHeader(http.StatusSeeOther)
			return
		}
		status, page.Notice = askAnswerNotice(err)
		if status == 0 {
			authFailure(w, err)
			return
		}
		page.Connected, page.SelectedID, page.Asks = true, id, []apiv1.Ask{ask}
		page.Retry = errors.Is(err, errAskUnconfirmed)
		// A lifecycle refusal must not offer the stale, pre-POST open form.
		page.ReadOnly = errors.Is(err, apiv1.ErrAskNotOpen) || errors.Is(err, apiv1.ErrAskExpired)
		page.AnswerErrorID, page.AnswerError = id, page.Notice
		page.AnswerText, page.AnswerOption = apiv1.NormalizeAskText(r.PostForm.Get("text")), r.PostForm.Get("optionId")
		page.Notice = ""
	} else if c.Asks != nil {
		result, readErr := c.Asks.read(r.Context(), cursor)
		if readErr != nil {
			page.Notice, page.Retry = "Asks are unavailable. Open asks could not be read. "+askReadNotice(readErr, c.Now()), true
			if errors.Is(readErr, errAsksNotConnected) {
				page.Notice, page.Retry = "Asks are not connected", false
			}
		} else {
			page.Asks, page.Connected, page.Complete = result.Asks, true, result.OpenComplete
			page.Notice = ""
			if !result.OpenComplete {
				page.Notice = "This is a partial read. More asks may need attention."
			}
			if result.HistoryUnavailable {
				page.HistoryNotice = "Older history is unavailable. Open asks are shown separately."
			} else if !result.HistoryComplete || cursor != "" {
				page.HistoryNotice = "Only one page of history is shown. Older history may be cut off."
			}
			if result.NextCursor != "" {
				page.OlderHref = AsksPath + "?" + url.Values{apiv1.AskQueryCursor: {result.NextCursor}}.Encode()
			}
		}
		if selectedID != "" && !errors.Is(readErr, errAsksNotConnected) {
			ask, detailErr := c.Asks.readAsk(r.Context(), selectedID)
			if detailErr != nil {
				// Keep any earlier list notice (unavailable or partial read).
				detail := "The selected ask could not be read. " + askReadNotice(detailErr, c.Now())
				if page.Notice != "" {
					detail = page.Notice + " " + detail
				}
				page.Notice, page.Retry = detail, true
			} else {
				found := false
				for i := range page.Asks {
					if page.Asks[i].ID == selectedID {
						page.Asks[i], found = ask, true
					}
				}
				if !found {
					page.Asks = append(page.Asks, ask)
				}
				if readErr != nil {
					// Only the selected ask was read: do not claim the history is empty.
					page.HistoryNotice = "Other asks could not be read."
				}
				page.Connected = true
			}
		}
	}
	renderAsks(w, r, c, key, record, page, status)
}

// Use only fixed reason codes and safe HTTP status values, never upstream text.
func askReadNotice(err error, observed time.Time) string {
	failure := sessionFailure(err)
	reason := "The operator returned an unreadable Asks response."
	switch {
	case errors.Is(err, portal.ErrDenied):
		reason = "The operator did not authorize the Asks read."
	case failure.ReasonCode == "credential_unavailable":
		reason = "The operator credential is unavailable."
	case failure.ReasonCode == "operator_unreachable" && failure.TimedOut:
		reason = "The operator did not answer in time."
	case failure.ReasonCode == "operator_unreachable":
		reason = "The operator could not be reached."
	case failure.ReasonCode == "operator_refused":
		reason = "The operator refused the Asks read with HTTP " + strconv.Itoa(failure.HTTPStatus) + "."
	case failure.ReasonCode == "cancelled":
		reason = "The Asks read was cancelled."
	}
	return reason + " Checked " + observed.UTC().Format("2 Jan 2006, 15:04:05 UTC") + "."
}

func renderAsks(w http.ResponseWriter, r *http.Request, c AssemblyConfig, key Handle, record Record, page portal.AsksPage, status int) {
	_, freshKey, fresh, err := authorizedAction(c.Session, c.Authorizer, r)
	if err != nil || !sameAskSession(key, record, freshKey, fresh) {
		authFailure(w, portal.ErrDenied)
		return
	}
	page.Now, page.CSRF = c.Now(), record.CSRF
	origin, err := url.Parse(c.Session.config.Origin)
	if err != nil {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	page.PortalHost = origin.Hostname()
	_, page.CanAnswer = c.Session.config.Store.(commandReceipts)
	body, policy, err := portal.RenderAsks(page, maximumResponse*7/8)
	if err != nil {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	w.Header().Set("Content-Security-Policy", policy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

var (
	errAskEmptyAnswer    = errors.New("empty ask answer")
	errAskConfirm        = errors.New("ask confirmation required")
	errAskDecision       = errors.New("ask decision required")
	errAskReadOnlyOption = errors.New("approval option is read only")
	errAskNotRecorded    = errors.New("earlier ask answer not recorded")
	errAskUnconfirmed    = errors.New("ask answer unconfirmed")
)

func askAnswerNotice(err error) (int, string) {
	switch {
	case errors.Is(err, errAskNotRecorded):
		return http.StatusConflict, "Your earlier attempt was not recorded. Check your answer and send it again."
	case errors.Is(err, errAskUnconfirmed):
		return http.StatusServiceUnavailable, "The operator did not confirm this answer. Refresh to see whether it was recorded, or send it again."
	case errors.Is(err, apiv1.ErrAskNotOpen):
		return http.StatusConflict, "This ask is already answered or withdrawn."
	case errors.Is(err, apiv1.ErrAskExpired):
		return http.StatusGone, "This ask has expired."
	case errors.Is(err, errAskEmptyAnswer):
		return http.StatusBadRequest, "Write an answer or choose an option."
	case errors.Is(err, errAskConfirm):
		return http.StatusBadRequest, "Confirm this decision for the scope and expiry shown."
	case errors.Is(err, errAskDecision):
		return http.StatusBadRequest, "Choose Approve or Deny."
	case errors.Is(err, errAskReadOnlyOption):
		return http.StatusBadRequest, "Approval options are read-only context. Use Approve or Deny."
	case errors.Is(err, apiv1.ErrAskInvalid):
		return http.StatusBadRequest, "Check your answer and choose a listed option."
	default:
		return 0, ""
	}
}

func sameAskSession(key Handle, record Record, freshKey Handle, fresh Record) bool {
	return freshKey == key && fresh.Principal == record.Principal && sha256.Sum256([]byte(fresh.accessToken)) == sha256.Sum256([]byte(record.accessToken)) && subtle.ConstantTimeCompare([]byte(fresh.CSRF), []byte(record.CSRF)) == 1
}

func postAskAnswer(r *http.Request, c AssemblyConfig, key Handle, record Record, ask apiv1.Ask) error {
	store, ok := c.Session.config.Store.(commandReceipts)
	if !ok {
		return portal.ErrDenied
	}
	requestID := r.PostForm.Get("requestID")
	if !launchRequestID.MatchString(requestID) {
		return portal.ErrDenied
	}
	wantKeys := 5
	request := apiv1.AskAnswerRequest{OptionID: r.PostForm.Get("optionId"), Text: apiv1.NormalizeAskText(r.PostForm.Get("text")), Actor: apiv1.AskActor{Issuer: record.Principal.Issuer, Subject: record.Principal.Subject}}
	if ask.Type == apiv1.AskTypeApproval {
		wantKeys = 7
		if r.PostForm.Get("decision") != "approve" && r.PostForm.Get("decision") != "deny" {
			return errAskDecision
		}
		if r.PostForm.Get("confirm") != "yes" {
			return errAskConfirm
		}
		if request.OptionID != "" {
			return errAskReadOnlyOption
		}
		approved := r.PostForm.Get("decision") == "approve"
		request.Approved = &approved
	}
	if len(r.PostForm) != wantKeys {
		return portal.ErrDenied
	}
	validationErr := request.ValidateForAt(ask, c.Now())
	// An identical recorded answer is a successful retry; it changes no record.
	alreadyAnswered := errors.Is(validationErr, apiv1.ErrAskNotOpen) && sameAskAnswer(ask.Answer, request)
	if validationErr != nil && !alreadyAnswered {
		if errors.Is(validationErr, apiv1.ErrAskInvalid) && ask.Type == apiv1.AskTypeInfo && request.OptionID == "" && strings.TrimSpace(request.Text) == "" {
			return errAskEmptyAnswer
		}
		return validationErr
	}
	// Bind replay protection to immutable scope, answer, issuer and subject.
	// Display names are presentation and never decide attribution or retries.
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
	if alreadyAnswered {
		return nil
	}
	if !created {
		return errAskNotRecorded // Reused request IDs never send again.
	}
	if err := request.ValidateForAt(ask, c.Now()); err != nil {
		return err
	}
	err = c.Asks.answer(freshRequest.Context(), ask.ID, request)
	if errors.Is(err, portal.ErrUnavailable) {
		return errAskUnconfirmed
	}
	return err
}

func sameAskAnswer(answer *apiv1.AskAnswer, request apiv1.AskAnswerRequest) bool {
	return answer != nil && answer.OptionID == request.OptionID && answer.Text == request.Text && sameAskApproval(answer.Approved, request.Approved) && answer.AnsweredBy.Issuer == request.Actor.Issuer && answer.AnsweredBy.Subject == request.Actor.Subject
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
		row, ok := askObject(answer, []string{"optionId", "answeredBy", "answeredAt"}, "text", "approved")
		if !ok {
			return false
		}
		if _, ok := askObject(row["answeredBy"], nil, "issuer", "subject", "displayName"); !ok {
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

func sameAskApproval(a, b *bool) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
