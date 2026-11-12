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

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// summonCreateKeys are the only JSON keys a summoning create may carry
// (DESIGN-064 6.4). idempotencyKey selects the replay (summon_create.go).
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

// summonedChildRefusal is the refusal for a session that is summoned, or
// that was summoned when it was authenticated. spec.caller is immutable.
const summonedChildRefusal = "a summoned session may not create sessions (DESIGN-064 6.4)"

// refuseSummonedChild refuses a create from a session principal that is
// summoned, or whose session cannot be read (DESIGN-064 6.4). The
// authenticated snapshot is not replaced by whatever object now has its
// name. One case still creates: an ordinary snapshot whose live object has
// another UID and no spec.caller (confirmSessionCaller says why).
// createSessionMode calls this before the canary and pilot branches.
// admitCreate calls refuseSummonedParent under the create lock, which is the
// same check for a scheduled dispatch.
func (s *Server) refuseSummonedChild(ctx context.Context, c *caller) error {
	if c == nil || c.kind != kindSession || c.session == nil {
		return nil
	}
	return s.confirmSessionCaller(ctx, c.session)
}

// refuseSummonedParent refuses a create whose parent is a session principal
// (DESIGN-064 6.4): the session's own request, or a scheduled dispatch on the
// session basis. An account parent, a slash in the name, is not a session and
// is not this rule. The snapshot is the session already in hand. Its UID is
// the authenticated UID. On a scheduled dispatch that UID is d.Owner.UID:
// scheduledCaller has refused any other, and d.Owner.UID is the authorizer's
// SessionRef. A session created later under the same name has a new UID, so
// it cannot be that ref. The owner pod's controller reference is not used,
// because a later session can be written onto the pod.
func (s *Server) refuseSummonedParent(ctx context.Context, c *caller, sess *v1alpha1.AgentSession) error {
	if s == nil || sess == nil {
		return nil
	}
	name := sess.Spec.Parent
	if name == "" || strings.Contains(name, "/") || len(validation.IsDNS1123Label(name)) > 0 {
		return nil
	}
	if c != nil && c.kind == kindSession && c.session != nil && c.session.Name == name {
		return s.confirmSessionCaller(ctx, c.session)
	}
	return s.liveSummonedObject(ctx, s.Policy.SessionNamespace, name)
}

// confirmSessionCaller applies the session-principal rule to snapshot, the
// authenticated or cached session. A spec.caller already set is refused
// before the live read: that field is immutable, so a later disappearance or
// a different object of the same name does not erase it. A missing live
// object or a failed read refuses. A live spec.caller refuses, whichever UID
// the live object has; with the same UID that is the stale cache that
// omitted it. A live object with another UID and no spec.caller does not
// refuse: an ordinary session replaced by another ordinary session still
// creates, which the usage-capture binding relies on. This rule trusts the
// pod's controller reference as resolveSession does; it is no defence against
// a writer that can rewrite that reference. Live nil does not invent an
// object; with no caller on the snapshot there is nothing further to refuse.
// The snapshot is not modified.
func (s *Server) confirmSessionCaller(ctx context.Context, snapshot *v1alpha1.AgentSession) error {
	if snapshot == nil {
		return nil
	}
	if snapshot.Spec.Caller != "" {
		return forbidden("%s", summonedChildRefusal)
	}
	if s == nil || s.Live == nil {
		return nil
	}
	ns := s.Policy.SessionNamespace
	if ns == "" {
		ns = snapshot.Namespace
	}
	var fresh v1alpha1.AgentSession
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: ns, Name: snapshot.Name}, &fresh); err != nil {
		return fromKubeError(err, "the caller's session")
	}
	if fresh.UID != snapshot.UID {
		// A summoned snapshot was refused above. A live object that is
		// summoned is refused here, whichever UID it has. An ordinary
		// session replaced by another ordinary session still creates:
		// bindUsageCaptureParent copies a parent UID only when the live
		// UID matches, and TestUsageCaptureCreateBinding requires that
		// create. The pod controller reference is not consulted.
		if fresh.Spec.Caller != "" {
			return forbidden("%s", summonedChildRefusal)
		}
		return nil
	}
	if fresh.Spec.Caller != "" {
		return forbidden("%s", summonedChildRefusal)
	}
	return nil
}

// liveSummonedObject is the caller check for a session-shaped parent that is
// not the authenticated session. A missing object is not that principal.
// Any other failed read refuses. A spec.caller on the object refuses.
func (s *Server) liveSummonedObject(ctx context.Context, ns, name string) error {
	if s == nil || s.Live == nil {
		return nil
	}
	var parent v1alpha1.AgentSession
	err := s.Live.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &parent)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fromKubeError(err, "the parent session")
	}
	if parent.Spec.Caller != "" {
		return forbidden("%s", summonedChildRefusal)
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

// newSummonDispatchID is a keyless create's dispatch id (DESIGN-064 5.9):
// 32 random hex characters, which match the field's pattern. A keyless
// create is never a repeat, so its id is never computed again.
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
// Every refusal is logged with the caller, the lane and the reason, and no
// data value.
func (s *Server) createSummonSession(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (status int, body any, err error) {
	ref := ""
	if c != nil {
		ref = c.parent
	}
	if c == nil || c.policy == nil || c.policy.Spec.Summon == nil {
		return 0, nil, forbidden("%s may not call this API: it is not Tom's, a client's or a session's ServiceAccount, nor a summoning caller with a CallerPolicy", ref)
	}
	lane := ""
	defer func() {
		if err != nil {
			s.logSummonRefusal(c, lane, err)
		}
	}()
	var raw json.RawMessage
	if err := decodeJSON(w, r, maxCreateBody, &raw, true); err != nil {
		return 0, nil, err
	}
	// A member name outside printable ASCII, and two members of one field, are
	// refused before a value map is decoded. encoding/json keeps one value
	// and drops the other, including a key that differs only by an escape or
	// by case.
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
	lane = summonLogValue(req.Lane)
	data, err := parseSummonData(dataRaw(keys))
	if err != nil {
		return 0, nil, err
	}
	sess, entry, brief, err := s.buildSummonSession(ctx, c, req, data)
	if err != nil {
		return 0, nil, err
	}
	lane = string(entry.Lane)

	s.createMu.Lock()
	defer s.createMu.Unlock()
	if err := s.admitCreate(ctx, c, sess); err != nil {
		return 0, nil, err
	}
	return s.dispatchSummon(ctx, w, c, req, entry, sess, brief)
}

// summonMemberASCII is the refusal for a member name that is not printable
// ASCII. Printable ASCII is the bytes 0x20 through 0x7e. With only those
// names left, strings.ToLower and strings.EqualFold agree.
const summonMemberASCII = "a summoning create's member names are printable ASCII"

// summonDuplicateFields reports a member name that is not printable ASCII,
// and members that name one field twice, at the body and inside data. A
// non-ASCII name is refused before a value map is decoded. Two `data`
// members are refused as duplicates first, without scanning inside either.
// Duplicate names are
// compared after JSON unescaping and without regard to case. The scan
// decodes values as raw JSON, so it does not build an object map that would
// keep one of the two.
func summonDuplicateFields(raw []byte) ([]apiv1.FieldError, error) {
	return duplicateMemberFields(raw, "")
}

type summonMember struct {
	key string
	val json.RawMessage
}

func duplicateMemberFields(raw []byte, prefix string) ([]apiv1.FieldError, error) {
	members, refused, err := decodeSummonMembers(raw, prefix)
	if err != nil || refused != nil {
		return refused, err
	}
	var inner []apiv1.FieldError
	if prefix == "" {
		if dataRaw, ok := oneSummonDataObject(members); ok {
			inner, err = duplicateMemberFields(dataRaw, "data.")
			if err != nil || summonASCIIRefusal(inner) {
				return inner, err
			}
		}
	}
	fields := foldSummonDuplicates(members, prefix)
	fields = append(fields, inner...)
	slices.SortFunc(fields, func(a, b apiv1.FieldError) int { return strings.Compare(a.Field, b.Field) })
	return fields, nil
}

// decodeSummonMembers reads one JSON object into a slice. A member name with
// a byte outside printable ASCII is returned as a refusal and no map is built.
// A non-object at prefix "" is a bad body. A non-object inside data is ignored
// here; parseSummonData refuses it.
func decodeSummonMembers(raw []byte, prefix string) ([]summonMember, []apiv1.FieldError, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		if prefix != "" {
			return nil, nil, nil
		}
		return nil, nil, badRequest("the body must be a JSON object")
	}
	var members []summonMember
	for dec.More() {
		tok, err = dec.Token()
		if err != nil {
			return nil, nil, badRequest("the body must be a JSON object")
		}
		key, ok := tok.(string)
		if !ok {
			return nil, nil, badRequest("the body must be a JSON object")
		}
		var val json.RawMessage
		if err = dec.Decode(&val); err != nil {
			return nil, nil, badRequest("the body must be a JSON object")
		}
		if !summonPrintableASCII(key) {
			field := key
			if prefix != "" {
				field = prefix + key
			}
			return nil, []apiv1.FieldError{fieldError(field, "%s", summonMemberASCII)}, nil
		}
		members = append(members, summonMember{key: key, val: append(json.RawMessage(nil), val...)})
	}
	tok, err = dec.Token()
	if err != nil || tok != json.Delim('}') || dec.More() {
		return nil, nil, badRequest("the body must be a JSON object")
	}
	return members, nil, nil
}

func summonPrintableASCII(name string) bool {
	for i := 0; i < len(name); i++ {
		if name[i] < 0x20 || name[i] > 0x7e {
			return false
		}
	}
	return true
}

func summonASCIIRefusal(fields []apiv1.FieldError) bool {
	return len(fields) == 1 && fields[0].Message == summonMemberASCII
}

// oneSummonDataObject is the raw value of the one data member, when that
// value is a JSON object. Two data members are duplicates, handled by the
// fold, and are not scanned as an object.
func oneSummonDataObject(members []summonMember) (json.RawMessage, bool) {
	var raw json.RawMessage
	n := 0
	for _, m := range members {
		if strings.ToLower(m.key) != "data" {
			continue
		}
		n++
		if n == 1 {
			raw = m.val
		}
	}
	trimmed := bytes.TrimSpace(raw)
	if n != 1 || len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	return raw, true
}

func foldSummonDuplicates(members []summonMember, prefix string) []apiv1.FieldError {
	type spellings struct{ names []string }
	seen := map[string]*spellings{}
	for _, m := range members {
		fold := strings.ToLower(m.key)
		sk := seen[fold]
		if sk == nil {
			sk = &spellings{}
			seen[fold] = sk
		}
		sk.names = append(sk.names, m.key)
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
	return fields
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

// buildSummonSession checks the request against the caller's policy and
// builds the session it asks for, with every 422 and 403 decided here. The
// dispatch id is left empty: dispatchSummon chooses it, after the repeat and
// the name checks and before anything is written. brief renders the brief
// for the session's final name, which its platform line carries (DESIGN-064
// 5.6; the correction of 5.9, 2026-11-12): the requested name, or a suffix
// of it. Its size is checked here with the longest suffix, so no final name
// can make it too large after the charge.
func (s *Server) buildSummonSession(ctx context.Context, c *caller, req summonWire, data map[string]string) (*v1alpha1.AgentSession, *v1alpha1.CallerPolicySummonSession, func(name string) string, error) {
	if req.Name == "" || len(req.Name) > summonNameMaxLen || len(validation.IsDNS1123Label(req.Name)) > 0 {
		return nil, nil, nil, invalid(fieldError("name", "a summoning session name is a DNS label of at most %d characters", summonNameMaxLen))
	}
	if err := refuseReservedSessionName(req.Name); err != nil {
		return nil, nil, nil, err
	}
	entry := summonEntry(c.policy.Spec.Summon.Sessions, req.Mode, req.Lane)
	if entry == nil {
		return nil, nil, nil, invalid(fieldError("lane", "no policy entry for mode %q and lane %q", req.Mode, req.Lane))
	}
	if entry.Lane == v1alpha1.LaneUpgrade && strings.HasPrefix(req.Name, summonCurationPrefix) {
		return nil, nil, nil, invalid(fieldError("name", "an upgrade session cannot be named under wo-cigar-curate-"))
	}
	if !summonHasPrefix(req.Name, entry.NamePrefixes) {
		return nil, nil, nil, invalid(fieldError("name", "must start with one of the entry's prefixes (%s)", strings.Join(entry.NamePrefixes, ", ")))
	}
	if req.Agent != "" && req.Agent != string(v1alpha1.AgentClaude) {
		return nil, nil, nil, invalid(fieldError("agent", "a summoned session runs agent claude"))
	}
	if req.Model != "" && req.Model != entry.Model.Default {
		return nil, nil, nil, invalid(fieldError("model", "the model must be empty or the entry's default"))
	}
	if req.Profile != "" && req.Profile != entry.Profile {
		return nil, nil, nil, invalid(fieldError("profile", "the profile must be empty or the entry's profile"))
	}
	if req.IdempotencyKey != "" {
		var fields []apiv1.FieldError
		for _, msg := range validation.IsValidLabelValue(req.IdempotencyKey) {
			fields = append(fields, fieldError("idempotencyKey", "%s", msg))
		}
		if len(fields) > 0 {
			return nil, nil, nil, invalid(fields...)
		}
	}
	if err := checkSummonData(entry.Brief.Fields, data); err != nil {
		return nil, nil, nil, err
	}
	if !apiv1.ClaudeEffortAccepted(entry.Model.Default, entry.Effort) {
		return nil, nil, nil, invalid(fieldError("effort", "the policy entry's effort is not one %s takes", entry.Model.Default))
	}
	if s.Templates != nil {
		t, err := s.Templates(ctx)
		if err != nil {
			return nil, nil, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the templates cannot be read")
		}
		if _, _, err := t.Profile(entry.Profile); err != nil {
			return nil, nil, nil, invalid(fieldError("profile", "%v", err))
		}
	}
	boundary, err := s.summonBoundary()
	if err != nil {
		s.Log.Error(err, "summon boundary")
		return nil, nil, nil, internal("the brief boundary could not be drawn")
	}
	brief := func(name string) string {
		return renderSummonBrief(name, c.policy.Name, string(entry.Lane), entry.Brief.Template, entry.Brief.Fields, data, boundary)
	}
	names := summonNameSequence(req.Name)
	if longest := brief(names[len(names)-1]); len(longest) > protocol.MaxPromptBytes {
		return nil, nil, nil, invalid(fieldError("data", "the rendered brief is %d bytes, more than %d", len(longest), protocol.MaxPromptBytes))
	}
	snap, err := summonSnapshot(c.policy, entry, "")
	if err != nil {
		return nil, nil, nil, err
	}
	spec := v1alpha1.AgentSessionSpec{
		Repo:    entry.Repo,
		Agent:   v1alpha1.AgentClaude,
		Mode:    v1alpha1.SessionMode(entry.Kind),
		Model:   entry.Model.Default,
		Effort:  entry.Effort,
		Prompt:  brief(req.Name),
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
			return nil, nil, nil, forbidden("CallerPolicy entry is not a valid summon entry")
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
	sess.Annotations = map[string]string{v1alpha1.AnnotationRequestHash: summonRequestHash(req)}
	return sess, entry, brief, nil
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
