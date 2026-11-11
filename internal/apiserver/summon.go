package apiserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// summonCreateKeys are the only JSON keys a summoning create may carry
// (DESIGN-064 6.4). idempotencyKey is accepted and stored. Replay, the name
// suffix and the create ledger are a later slice.
var summonCreateKeys = []string{
	"name", "mode", "lane", "idempotencyKey", "data", "agent", "model", "profile",
}

// readSummonPolicy reads the one CallerPolicy named from the token's
// ServiceAccount, uncached. The name is <namespace>.<name>. A missing object,
// a failed read, a deleting object, an object without spec.summon, or one
// whose serviceAccount is not the token's account is (nil, nil): the caller
// is refused with the existing 403. The account is not a known class until
// that object is read, so a read error does not become a 503. Meatball is
// the exception: its account is recognized before its read, and a failed
// read of that object stays 503.
// A second object that names the same account under another name is never read.
func (s *Server) readSummonPolicy(ctx context.Context, account string) (*v1alpha1.CallerPolicy, error) {
	if s.Live == nil || account == "" || !strings.Contains(account, "/") {
		return nil, nil
	}
	name := strings.ReplaceAll(account, "/", ".")
	var p v1alpha1.CallerPolicy
	err := s.Live.Get(ctx, types.NamespacedName{Namespace: callerPolicyNamespace, Name: name}, &p)
	if err != nil {
		if !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			s.Log.Error(err, "summon CallerPolicy read", "account", account)
		}
		return nil, nil
	}
	if p.UID == "" || !p.DeletionTimestamp.IsZero() || p.Spec.Summon == nil || p.Spec.Summon.ServiceAccount != account {
		return nil, nil
	}
	return &p, nil
}

func summonRouteDenied() error {
	return forbidden("a summoning caller may create a session and read its own sessions (DESIGN-064 6.4)")
}

func summonReadDenied() error {
	return forbidden("a summoning caller reads only its own sessions (DESIGN-064 6.4)")
}

// summonCallerAllowed is the positive route list for a summoning caller.
// Meatball's list is policyCallerAllowed and is not used here.
func summonCallerAllowed(c *caller, r *http.Request) error {
	if c == nil || c.kind != kindSummon {
		return nil
	}
	if r.URL.Path == apiv1.SessionsPath && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
		return nil
	}
	if name := r.PathValue("name"); name != "" && r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath(name) {
		return nil
	}
	return summonRouteDenied()
}

// refuseSummonedChild refuses POST /v1/sessions from a session whose
// spec.caller is set (DESIGN-064 6.4). The calling session is read from the
// API server, not only the cache resolveCaller used.
func (s *Server) refuseSummonedChild(ctx context.Context, c *caller) error {
	if c == nil || c.kind != kindSession || c.session == nil {
		return nil
	}
	sess := c.session
	if s.Live != nil {
		ns := sess.Namespace
		if ns == "" {
			ns = s.Policy.SessionNamespace
		}
		var fresh v1alpha1.AgentSession
		if err := s.Live.Get(ctx, types.NamespacedName{Namespace: ns, Name: sess.Name}, &fresh); err != nil {
			return fromKubeError(err, "the caller's session")
		}
		sess = &fresh
	}
	if sess.Spec.Caller != "" {
		return forbidden("a summoned session may not create sessions (DESIGN-064 6.4)")
	}
	return nil
}

// refuseSummonedParent refuses a create whose parent is a session with
// spec.caller set (DESIGN-064 6.4). The parent is read from the API server,
// not from the cache resolveCaller used. A parent string that is not a session
// name, or that names no session, is not this rule. Any other failed live read
// refuses the create. createSessionMode also calls refuseSummonedChild first,
// so a direct create, a canary and a private pilot still hear it before those
// branches. admitCreate is the step every ordinary create shares, including a
// scheduled dispatch of a record that already exists.
func (s *Server) refuseSummonedParent(ctx context.Context, sess *v1alpha1.AgentSession) error {
	if s == nil || sess == nil || s.Live == nil {
		return nil
	}
	name := sess.Spec.Parent
	if name == "" || strings.Contains(name, "/") || len(validation.IsDNS1123Label(name)) > 0 {
		return nil
	}
	var parent v1alpha1.AgentSession
	err := s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: name}, &parent)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fromKubeError(err, "the parent session")
	}
	if parent.Spec.Caller != "" {
		return forbidden("a summoned session may not create sessions (DESIGN-064 6.4)")
	}
	return nil
}

// summonOwns is a summoning caller's session: spec.caller is its policy's
// name and spec.parent is its account. Meatball keeps policyOwns.
func summonOwns(c *caller, sess *v1alpha1.AgentSession) bool {
	return c != nil && c.policy != nil && sess != nil && sess.UID != "" &&
		sess.Spec.Caller == c.policy.Name && sess.Spec.Parent == c.parent
}

// summonBoundary draws the fence token: 16 hex characters, or the test hook.
func (s *Server) summonBoundary() (string, error) {
	if s != nil && s.SummonBoundary != nil {
		return s.SummonBoundary()
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// newSummonDispatchID is a schema-valid dispatch id. The create ledger that
// charges an id before the session exists is a later slice; this value is
// not recorded there. It is 32 hex characters, which matches the field's pattern.
func newSummonDispatchID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

type summonWire struct {
	Name           string          `json:"name"`
	Mode           string          `json:"mode"`
	Lane           string          `json:"lane"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Data           json.RawMessage `json:"data"`
	Agent          string          `json:"agent"`
	Model          string          `json:"model"`
	Profile        string          `json:"profile"`
}

// createSummonSession serves POST /v1/sessions for a summoning caller.
// The caller sends data. The API writes the session, including spec.prompt.
func (s *Server) createSummonSession(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	ref := ""
	if c != nil {
		ref = c.parent
	}
	if c == nil || c.policy == nil || c.policy.Spec.Summon == nil {
		return 0, nil, forbidden("%s may not call this API: it is not Tom's, a client's or a session's ServiceAccount, and summoning callers (CallerPolicy)", ref)
	}
	var raw json.RawMessage
	if err := decodeJSON(w, r, maxCreateBody, &raw, true); err != nil {
		return 0, nil, err
	}
	// Duplicate members are refused before any object map is built. encoding/json
	// keeps one value and drops the other, including a key that differs only by
	// an escape or by case.
	dups, err := summonDuplicateFields(raw)
	if err != nil {
		return 0, nil, err
	}
	if len(dups) > 0 {
		return 0, nil, invalid(dups...)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil || keys == nil {
		return 0, nil, badRequest("the body must be a JSON object")
	}
	if err := summonKeysAllowed(keys); err != nil {
		return 0, nil, err
	}
	var req summonWire
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return 0, nil, bodyError(err, maxCreateBody)
	}
	data, err := parseSummonData(dataRaw(keys))
	if err != nil {
		return 0, nil, err
	}
	sess, err := s.buildSummonSession(ctx, c, req, data)
	if err != nil {
		return 0, nil, err
	}

	s.createMu.Lock()
	defer s.createMu.Unlock()
	if err := s.admitCreate(ctx, c, sess); err != nil {
		return 0, nil, err
	}
	if err := checkAgentd(sess); err != nil {
		return 0, nil, err
	}
	if err := s.Client.Create(ctx, sess, client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "session %s already exists", sess.Name)
		}
		return 0, nil, fromKubeError(err, "create session")
	}
	s.Log.Info("session created", "session", sess.Name, "caller", c.String(), "repo", sess.Spec.Repo,
		"agent", sess.Spec.Agent, "mode", sess.Spec.Mode, "model", sess.Spec.Model, "lane", sess.Spec.Lane)
	w.Header().Set("Location", apiv1.SessionPath(sess.Name))
	return http.StatusCreated, view(sess, false), nil
}

// summonDuplicateFields reports members that name one field twice, at the
// body and inside data. Names are compared after JSON unescaping and without
// regard to case. The scan decodes values as raw JSON, so it does not build
// an object map that would keep one of the two.
func summonDuplicateFields(raw []byte) ([]apiv1.FieldError, error) {
	return duplicateMemberFields(raw, "")
}

func duplicateMemberFields(raw []byte, prefix string) ([]apiv1.FieldError, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		if prefix != "" {
			return nil, nil
		}
		return nil, badRequest("the body must be a JSON object")
	}
	type spellings struct{ names []string }
	seen := map[string]*spellings{}
	var dataRaw json.RawMessage
	dataCount := 0
	for dec.More() {
		tok, err = dec.Token()
		if err != nil {
			return nil, badRequest("the body must be a JSON object")
		}
		key, ok := tok.(string)
		if !ok {
			return nil, badRequest("the body must be a JSON object")
		}
		var val json.RawMessage
		if err = dec.Decode(&val); err != nil {
			return nil, badRequest("the body must be a JSON object")
		}
		fold := strings.ToLower(key)
		sk := seen[fold]
		if sk == nil {
			sk = &spellings{}
			seen[fold] = sk
		}
		sk.names = append(sk.names, key)
		if prefix == "" && fold == "data" {
			dataCount++
			if dataCount == 1 {
				dataRaw = append(json.RawMessage(nil), val...)
			}
		}
	}
	tok, err = dec.Token()
	if err != nil || tok != json.Delim('}') || dec.More() {
		return nil, badRequest("the body must be a JSON object")
	}
	folds := make([]string, 0, len(seen))
	for fold := range seen {
		folds = append(folds, fold)
	}
	slices.Sort(folds)
	var fields []apiv1.FieldError
	for _, fold := range folds {
		if len(seen[fold].names) < 2 {
			continue
		}
		names := slices.Clone(seen[fold].names)
		slices.Sort(names)
		names = slices.Compact(names)
		for _, name := range names {
			field := name
			if prefix != "" {
				field = prefix + name
			}
			fields = append(fields, fieldError(field, "a summoning create names this field more than once"))
		}
	}
	if prefix == "" && dataCount == 1 && len(bytes.TrimSpace(dataRaw)) > 0 && bytes.TrimSpace(dataRaw)[0] == '{' {
		inner, err := duplicateMemberFields(dataRaw, "data.")
		if err != nil {
			return nil, err
		}
		fields = append(fields, inner...)
	}
	slices.SortFunc(fields, func(a, b apiv1.FieldError) int { return strings.Compare(a.Field, b.Field) })
	return fields, nil
}

func dataRaw(keys map[string]json.RawMessage) json.RawMessage {
	for k, v := range keys {
		if strings.EqualFold(k, "data") {
			return v
		}
	}
	return nil
}

func summonKeysAllowed(keys map[string]json.RawMessage) error {
	// JSON object keys are case-sensitive, and encoding/json matches a struct
	// field case-insensitively without refusing a second spelling. Two keys
	// that differ only in case would make the stored data depend on map
	// iteration. Refuse both spellings.
	folded := make(map[string][]string, len(keys))
	for k := range keys {
		fold := strings.ToLower(k)
		folded[fold] = append(folded[fold], k)
	}
	folds := make([]string, 0, len(folded))
	for fold := range folded {
		folds = append(folds, fold)
	}
	slices.Sort(folds)
	var fields []apiv1.FieldError
	for _, fold := range folds {
		spellings := folded[fold]
		slices.Sort(spellings)
		if len(spellings) > 1 {
			for _, k := range spellings {
				fields = append(fields, fieldError(k, "a summoning create names this field more than once, differing only in case"))
			}
			continue
		}
		k := spellings[0]
		if !slices.ContainsFunc(summonCreateKeys, func(allow string) bool {
			return strings.EqualFold(k, allow)
		}) {
			fields = append(fields, fieldError(k, "a summoning create cannot set %s", k))
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return invalid(fields...)
}

func parseSummonData(raw json.RawMessage) (map[string]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, invalid(fieldError("data", "data must be an object of strings"))
	}
	out := make(map[string]string, len(obj))
	for k, v := range obj {
		s, ok := jsonStringValue(v)
		if !ok {
			return nil, invalid(fieldError("data."+k, "a data value must be a string"))
		}
		out[k] = s
	}
	return out, nil
}

func jsonStringValue(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func (s *Server) buildSummonSession(ctx context.Context, c *caller, req summonWire, data map[string]string) (*v1alpha1.AgentSession, error) {
	if req.Name == "" || len(req.Name) > summonNameMaxLen || len(validation.IsDNS1123Label(req.Name)) > 0 {
		return nil, invalid(fieldError("name", "a summoning session name is a DNS label of at most %d characters", summonNameMaxLen))
	}
	if err := refuseReservedSessionName(req.Name); err != nil {
		return nil, err
	}
	entry := summonEntry(c.policy.Spec.Summon.Sessions, req.Mode, req.Lane)
	if entry == nil {
		return nil, invalid(fieldError("lane", "no policy entry for mode %q and lane %q", req.Mode, req.Lane))
	}
	if entry.Lane == v1alpha1.LaneUpgrade && strings.HasPrefix(req.Name, summonCurationPrefix) {
		return nil, invalid(fieldError("name", "an upgrade session cannot be named under wo-cigar-curate-"))
	}
	if !summonHasPrefix(req.Name, entry.NamePrefixes) {
		return nil, invalid(fieldError("name", "must start with one of the entry's prefixes (%s)", strings.Join(entry.NamePrefixes, ", ")))
	}
	if req.Agent != "" && req.Agent != string(v1alpha1.AgentClaude) {
		return nil, invalid(fieldError("agent", "a summoned session runs agent claude"))
	}
	if req.Model != "" && req.Model != entry.Model.Default {
		return nil, invalid(fieldError("model", "the model must be empty or the entry's default"))
	}
	if req.Profile != "" && req.Profile != entry.Profile {
		return nil, invalid(fieldError("profile", "the profile must be empty or the entry's profile"))
	}
	if req.IdempotencyKey != "" {
		var fields []apiv1.FieldError
		for _, msg := range validation.IsValidLabelValue(req.IdempotencyKey) {
			fields = append(fields, fieldError("idempotencyKey", "%s", msg))
		}
		if len(fields) > 0 {
			return nil, invalid(fields...)
		}
	}
	if err := checkSummonData(entry.Brief.Fields, data); err != nil {
		return nil, err
	}
	if !apiv1.ClaudeEffortAccepted(entry.Model.Default, entry.Effort) {
		return nil, invalid(fieldError("effort", "the policy entry's effort is not one %s takes", entry.Model.Default))
	}
	if s.Templates != nil {
		t, err := s.Templates(ctx)
		if err != nil {
			return nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the templates cannot be read")
		}
		if _, _, err := t.Profile(entry.Profile); err != nil {
			return nil, invalid(fieldError("profile", "%v", err))
		}
	}
	boundary, err := s.summonBoundary()
	if err != nil {
		s.Log.Error(err, "summon boundary")
		return nil, internal("the brief boundary could not be drawn")
	}
	brief := renderSummonBrief(req.Name, c.policy.Name, string(entry.Lane), entry.Brief.Template, entry.Brief.Fields, data, boundary)
	if len(brief) > protocol.MaxPromptBytes {
		return nil, invalid(fieldError("data", "the rendered brief is %d bytes, more than %d", len(brief), protocol.MaxPromptBytes))
	}
	dispatchID, err := newSummonDispatchID()
	if err != nil {
		s.Log.Error(err, "summon dispatch id")
		return nil, internal("the dispatch id could not be drawn")
	}
	snap, err := summonSnapshot(c.policy, entry, dispatchID)
	if err != nil {
		return nil, err
	}
	spec := v1alpha1.AgentSessionSpec{
		Repo:    entry.Repo,
		Agent:   v1alpha1.AgentClaude,
		Mode:    v1alpha1.SessionMode(entry.Kind),
		Model:   entry.Model.Default,
		Effort:  entry.Effort,
		Prompt:  brief,
		Profile: entry.Profile,
		Parent:  c.parent,
		Caller:  c.policy.Name,
		Lane:    entry.Lane,
		Summon:  snap,
	}
	if req.IdempotencyKey != "" {
		spec.IdempotencyKey = req.IdempotencyKey
	}
	if entry.Kind == v1alpha1.SummonKindTask {
		d, err := time.ParseDuration(entry.Timeout)
		if err != nil || d <= 0 {
			return nil, forbidden("CallerPolicy entry is not a valid summon entry")
		}
		spec.Limits = &v1alpha1.SessionLimits{Timeout: &metav1.Duration{Duration: d}}
		if entry.MaxTurns != nil {
			spec.Limits.MaxTurns = *entry.MaxTurns
		}
	}
	sess := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: s.Policy.SessionNamespace,
			Name:      req.Name,
			Labels:    map[string]string{v1alpha1.LabelDepth: "0"},
		},
		Spec: spec,
	}
	if req.IdempotencyKey != "" {
		sess.Labels[v1alpha1.LabelIdempotencyKey] = req.IdempotencyKey
	}
	return sess, nil
}

func summonEntry(sessions []v1alpha1.CallerPolicySummonSession, mode, lane string) *v1alpha1.CallerPolicySummonSession {
	for i := range sessions {
		if string(sessions[i].Kind) == mode && string(sessions[i].Lane) == lane {
			return &sessions[i]
		}
	}
	return nil
}

func summonHasPrefix(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func checkSummonData(fields []string, data map[string]string) error {
	allowed := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		allowed[f] = struct{}{}
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var errs []apiv1.FieldError
	total := 0
	for _, k := range keys {
		if _, ok := allowed[k]; !ok {
			errs = append(errs, fieldError("data."+k, "not in the entry's brief.fields"))
			continue
		}
		n := len(data[k])
		if n > summonValueMaxBytes {
			errs = append(errs, fieldError("data."+k, "%d bytes, more than %d", n, summonValueMaxBytes))
		}
		total += n
	}
	if total > summonDataMaxBytes {
		errs = append(errs, fieldError("data", "%d bytes, more than %d", total, summonDataMaxBytes))
	}
	if len(errs) > 0 {
		return invalid(errs...)
	}
	return nil
}

func summonSnapshot(policy *v1alpha1.CallerPolicy, entry *v1alpha1.CallerPolicySummonSession, dispatchID string) (*v1alpha1.SessionSummon, error) {
	snap := &v1alpha1.SessionSummon{
		DispatchID:    dispatchID,
		Priority:      entry.Priority,
		FallbackModel: entry.Model.Fallback,
		OnUnreported:  entry.OnUnreported,
	}
	switch entry.Lane {
	case v1alpha1.LaneUpgrade:
		snap.StaleAfter = entry.StaleAfter
		if snap.StaleAfter == "" {
			snap.StaleAfter = summonUpgradeStaleDefault
		}
	case v1alpha1.LaneRemediation, v1alpha1.LaneCuration:
		if entry.MaxHold == "" {
			return nil, forbidden("CallerPolicy entry is not a valid summon entry")
		}
		snap.MaxHold = entry.MaxHold
	}
	if entry.Lane == v1alpha1.LaneRemediation {
		esc := summonEntry(policy.Spec.Summon.Sessions, string(v1alpha1.SummonKindRemote), string(v1alpha1.LaneEscalation))
		if esc == nil {
			return nil, forbidden("CallerPolicy entry is not a valid summon entry")
		}
		snap.Escalation = &v1alpha1.SessionSummonEscalation{
			Kind:         esc.Kind,
			Lane:         esc.Lane,
			Repo:         esc.Repo,
			Profile:      esc.Profile,
			Model:        esc.Model.Default,
			Fallback:     esc.Model.Fallback,
			Effort:       esc.Effort,
			OnUnreported: esc.OnUnreported,
		}
	}
	return snap, nil
}
