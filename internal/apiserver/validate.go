package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
)

// Body limits. A create carries a prompt of at most 64 KiB
// (protocol.MaxPromptBytes) in JSON, where escaping can double it; a heartbeat is
// a few KiB.
const (
	maxCreateBody    = 256 << 10
	maxHeartbeatBody = 1 << 20
)

// decodeJSON reads a JSON body of at most limit bytes into v. strict refuses
// unknown fields: a create's request is the API's to define. A heartbeat is
// decoded leniently, because a newer agentd may report more than this operator
// reads (D-40).
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, v any, strict bool) error {
	ct := r.Header.Get("Content-Type")
	if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != "application/json" {
		return newError(http.StatusUnsupportedMediaType, apiv1.CodeUnsupportedMediaType, "the body must be application/json, not %q", ct)
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		return bodyError(err, limit)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err != nil {
			return bodyError(err, limit)
		}
		return badRequest("the body holds more than one JSON value")
	}
	return nil
}

func bodyError(err error, limit int64) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return newError(http.StatusRequestEntityTooLarge, apiv1.CodeTooLarge, "the body is over %d bytes", limit)
	}
	if errors.Is(err, io.EOF) {
		return badRequest("the body is empty")
	}
	// encoding/json's messages name the field and the type, never the value.
	return badRequest("the body is not a valid request: %v", err)
}

// newSession builds the AgentSession a create asks for, or refuses it. It checks
// what only the API knows: what the built plans serve, the effort levels per model,
// the caller's rights and the profile. The schema's own rules (D-39) are left to
// the API server, which refuses a bad object at create with the schema's
// messages, and agentd's rules are checked by agentd's own code (D-40). So the
// API restates neither.
func (s *Server) newSession(ctx context.Context, req apiv1.CreateSessionRequest, c *caller) (*v1alpha1.AgentSession, error) {
	if req.Name != "" || req.Lane != "" {
		return nil, forbidden("name and lane are for summoning callers, by their CallerPolicy (DESIGN-001 3.7), which arrive in plan 10")
	}

	var fields []apiv1.FieldError
	add := func(field, format string, args ...any) { fields = append(fields, fieldError(field, format, args...)) }

	// What the built plans serve: Claude task sessions (plan 01) and local
	// sessions (plan 02, D-58). Each later plan lifts its own line.
	switch v1alpha1.AgentKind(req.Agent) {
	case v1alpha1.AgentClaude:
	case v1alpha1.AgentCodex:
		add("agent", "codex sessions arrive in plan 04; plan 01 runs claude")
	case v1alpha1.AgentOpencode:
		add("agent", "opencode sessions arrive in plan 09; plan 01 runs claude")
	case "":
		add("agent", "required: claude")
	default:
		add("agent", "%q is not claude, codex or opencode", req.Agent)
	}
	switch v1alpha1.SessionMode(req.Mode) {
	case v1alpha1.ModeTask, v1alpha1.ModeLocal:
		// Local sessions run the TUI from plan 02 (D-58).
	case v1alpha1.ModeRemote:
		add("mode", "remote sessions arrive in plan 03; task and local sessions run today")
	case "":
		add("mode", "required: task or local")
	default:
		add("mode", "%q is not task, local or remote", req.Mode)
	}
	if len(req.Tools) > 0 {
		add("tools", "tool pools arrive in plan 08")
	}
	if req.Model == "" {
		add("model", "required: a full model id such as claude-opus-5-5")
	}
	if req.Effort != "" && v1alpha1.AgentKind(req.Agent) == v1alpha1.AgentClaude && req.Model != "" && !apiv1.ClaudeEffortAccepted(req.Model, req.Effort) {
		if levels := apiv1.ClaudeEffortLevels(req.Model); levels == nil {
			add("effort", "model %s has no effort control; leave effort empty", req.Model)
		} else {
			add("effort", "model %s takes %s (or ultracode where it takes xhigh), not %q", req.Model, strings.Join(levels, ", "), req.Effort)
		}
	}
	if len(req.Prompt) > protocol.MaxPromptBytes {
		add("prompt", "%d bytes, more than %d: a session's environment carries it (D-40)", len(req.Prompt), protocol.MaxPromptBytes)
	}
	if req.Restore != "" {
		if _, _, err := protocol.ParseRescueID(req.Restore); err != nil {
			add("restore", "%v", err)
		}
	}
	if req.IdempotencyKey != "" {
		for _, msg := range validation.IsValidLabelValue(req.IdempotencyKey) {
			add("idempotencyKey", "%s", msg)
		}
	}

	spec := v1alpha1.AgentSessionSpec{
		Repo:    req.Repo,
		Base:    req.Base,
		Agent:   v1alpha1.AgentKind(req.Agent),
		Mode:    v1alpha1.SessionMode(req.Mode),
		Model:   req.Model,
		Effort:  req.Effort,
		Prompt:  req.Prompt,
		Size:    v1alpha1.SizeClass(req.Size),
		Profile: req.Profile,
		Parent:  c.parent,
		Restore: req.Restore,
	}
	if l := req.Limits; l != nil {
		spec.Limits = &v1alpha1.SessionLimits{MaxTurns: l.MaxTurns}
		if l.Timeout != "" {
			d, err := time.ParseDuration(l.Timeout)
			if err != nil {
				add("limits.timeout", "%q is not a Go duration such as 40m", l.Timeout)
			} else {
				spec.Limits.Timeout = &metav1.Duration{Duration: d}
			}
		}
	}

	if l := req.Lifecycle; l != nil {
		spec.Lifecycle = &v1alpha1.Lifecycle{}
		for field, v := range map[string]struct {
			text string
			dst  **metav1.Duration
		}{
			"lifecycle.idleSuspendAfter": {l.IdleSuspendAfter, &spec.Lifecycle.IdleSuspendAfter},
			"lifecycle.archiveAfter":     {l.ArchiveAfter, &spec.Lifecycle.ArchiveAfter},
		} {
			if v.text == "" {
				continue
			}
			d, err := time.ParseDuration(v.text)
			if err != nil || d <= 0 {
				add(field, "%q is not a positive Go duration such as 72h", v.text)
				continue
			}
			*v.dst = &metav1.Duration{Duration: d}
		}
	}

	// A session's child runs on its parent's profile: a session cannot widen
	// what it may read or reach by starting another (D-46).
	if c.kind == kindSession && c.profile != "" {
		switch spec.Profile {
		case "":
			spec.Profile = c.profile
		case c.profile:
		default:
			return nil, forbidden("a session's child runs on its parent's profile %s, not %s", c.profile, spec.Profile)
		}
	}
	if spec.Profile != "" && s.Templates != nil {
		if t, err := s.Templates(ctx); err == nil {
			if _, _, err := t.Profile(spec.Profile); err != nil {
				add("profile", "%v", err)
			}
		}
	}
	if len(fields) > 0 {
		return nil, invalid(fields...)
	}

	sess := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: s.Policy.SessionNamespace,
			Labels:    map[string]string{v1alpha1.LabelDepth: fmt.Sprint(c.depth)},
		},
		Spec: spec,
	}
	if req.IdempotencyKey != "" {
		sess.Labels[v1alpha1.LabelIdempotencyKey] = req.IdempotencyKey
		sess.Annotations = map[string]string{v1alpha1.AnnotationRequestHash: requestHash(req)}
	}
	return sess, nil
}

// checkAgentd runs agentd's own check on the session, with its final name.
func checkAgentd(sess *v1alpha1.AgentSession) error {
	if err := controller.CheckAgentdSession(sess); err != nil {
		return invalid(fieldError("", "%v", err))
	}
	return nil
}

// requestHash is the SHA-256 of the request as decoded: two requests that ask
// for the same session hash the same, whatever their JSON's spacing or order.
func requestHash(req apiv1.CreateSessionRequest) string {
	b, _ := json.Marshal(req)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
