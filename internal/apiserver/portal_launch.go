package apiserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
)

// portalLaunchParentPrefix starts the parent of every session this route
// creates: "portal/<actorRef>". It is a valid launchedBy value
// (apiv1.PortalLauncherPattern) and cannot be a session name, which has no "/".
const portalLaunchParentPrefix = "portal/"

// portalLaunch creates one constrained session for the portal backend. It is a
// default-off route: Handler registers it only when PortalLaunchRepos is set.
// It builds a fixed CreateSessionRequest and sends it through createSessionMode,
// the one create path, so model, effort, size, prompt and idempotency rules are
// the existing ones. The caller it passes there is a derived kindPortalLaunch
// value that exists only inside this function: it is not an authenticated
// class, and no other handler ever receives it.
func (s *Server) portalLaunch(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if c.kind != kindPortalReader || len(s.PortalLaunchRepos) == 0 || s.PortalLaunchProfile == "" {
		return 0, nil, forbidden("portal launch action is unavailable")
	}
	var req apiv1.PortalLaunchRequest
	if err := decodePortalLaunch(w, r, &req); err != nil {
		return 0, nil, badRequest("invalid portal launch request")
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || !portalOpaqueID(req.RequestID) || !portalOpaqueID(req.ActorRef) {
		return 0, nil, forbidden("portal launch action is unavailable")
	}
	if _, ok := s.PortalLaunchRepos[req.Repo]; !ok {
		return 0, nil, forbidden("portal launch action is unavailable")
	}
	// The model is a full id, never an alias: the rule agent-run applies.
	if apiv1.CheckClaudeModel(req.Model) != apiv1.ClaudeModelOK {
		return 0, nil, invalid(fieldError("model", "must be a full Claude model id such as claude-opus-5-5, not an alias"))
	}
	switch v1alpha1.SessionMode(req.Mode) {
	case v1alpha1.ModeTask:
		if strings.TrimSpace(req.Prompt) == "" {
			return 0, nil, invalid(fieldError("prompt", "required for a task"))
		}
	case v1alpha1.ModeLocal, v1alpha1.ModeRemote:
		if req.Prompt != "" {
			return 0, nil, invalid(fieldError("prompt", "only a task takes a prompt"))
		}
	default:
		return 0, nil, invalid(fieldError("mode", "is not task, local or remote"))
	}
	if req.Size != "" && req.Size != string(v1alpha1.SizeS) && req.Size != string(v1alpha1.SizeM) {
		return 0, nil, invalid(fieldError("size", "is not S or M"))
	}
	// The profile is resolved on every request, so a templates edit cannot
	// hand a portal launch a ServiceAccount: a profile that names one (the
	// workbench's, with pods/exec) or that no longer exists is refused.
	if s.Templates == nil {
		return 0, nil, forbidden("portal launch action is unavailable")
	}
	t, err := s.Templates(ctx)
	if err != nil {
		return 0, nil, internal("the templates are not available; its log has the cause")
	}
	if _, profile, err := t.Profile(s.PortalLaunchProfile); err != nil || profile.ServiceAccount != "" {
		return 0, nil, forbidden("portal launch action is unavailable")
	}
	parent := portalLaunchParentPrefix + req.ActorRef
	if s.portalLaunchParentTaken(parent) {
		return 0, nil, forbidden("portal launch action is unavailable")
	}
	hash := sha256.Sum256([]byte(req.RequestID))
	create := apiv1.CreateSessionRequest{
		Repo:           req.Repo,
		Agent:          string(v1alpha1.AgentClaude),
		Mode:           req.Mode,
		Model:          req.Model,
		Effort:         req.Effort,
		Size:           req.Size,
		Prompt:         req.Prompt,
		Profile:        s.PortalLaunchProfile,
		IdempotencyKey: "portal-" + hex.EncodeToString(hash[:])[:40],
	}
	// HTML escaping would turn each "<", ">" and "&" of a prompt into six bytes
	// and could push a prompt inside its own limit past the create body cap.
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(create); err != nil {
		return 0, nil, internal("the operator failed this request; its log has the cause")
	}
	inner, err := http.NewRequestWithContext(ctx, http.MethodPost, apiv1.SessionsPath, &body)
	if err != nil {
		return 0, nil, internal("the operator failed this request; its log has the cause")
	}
	inner.Header.Set("Content-Type", "application/json")
	launcher := &caller{kind: kindPortalLaunch, identity: c.identity, parent: parent, depth: 1}
	status, out, err := s.createSessionMode(ctx, w, inner, launcher, false)
	// The portal has no use for the session route; the receipt names the session.
	w.Header().Del("Location")
	if err != nil {
		return 0, nil, err
	}
	sess, ok := out.(apiv1.Session)
	if !ok {
		return 0, nil, internal("the operator failed this request; its log has the cause")
	}
	return status, apiv1.PortalLaunchReceipt{
		RequestID: req.RequestID, SessionName: sess.Name, SessionUID: sess.UID, Phase: sess.Phase, ObservedAt: s.now().UTC(),
	}, nil
}

// portalLaunchCap is the most unfinished portal launches allowed at once.
func (s *Server) portalLaunchCap() int {
	if s.PortalLaunchMaxUnfinished > 0 {
		return s.PortalLaunchMaxUnfinished
	}
	return apiv1.DefaultPortalLaunchMaxUnfinished
}

// unfinishedPortalLaunches counts the sessions whose parent starts with
// "portal/" that are still at work, by the predicate the idempotency dedupe
// uses. It reads from the API server so a launch a moment ago counts.
func (s *Server) unfinishedPortalLaunches(ctx context.Context) (int, error) {
	var list v1alpha1.AgentSessionList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace)); err != nil {
		return 0, fromKubeError(err, "list sessions")
	}
	n := 0
	for i := range list.Items {
		if strings.HasPrefix(list.Items[i].Spec.Parent, portalLaunchParentPrefix) && unfinished(&list.Items[i]) {
			n++
		}
	}
	return n, nil
}

// portalLaunchParentTaken reports whether parent equals the identity of another
// caller class, so a launched session could never be mistaken for that
// caller's child. It cannot happen with the real namespaces; the check makes it
// impossible by construction.
func (s *Server) portalLaunchParentTaken(parent string) bool {
	if parent == s.Policy.Human || slices.Contains(s.Policy.Clients, parent) ||
		parent == s.Policy.SessionNamespace+"/"+s.Policy.SessionServiceAccount ||
		parent == s.Policy.SessionNamespace+"/"+s.Policy.WorkbenchServiceAccount ||
		parent == StandbyParent || parent == projectcanary.PilotCaller || parent == portalReaderRef {
		return true
	}
	for _, host := range s.Policy.Coordinators {
		if host.ServiceAccount == parent {
			return true
		}
	}
	return false
}

// decodePortalLaunch is decodePortalMessage's strict decode for the launch
// request: duplicate and unknown keys are refused without echoing them. The
// five identity-and-shape keys are required; effort, size and prompt are optional.
func decodePortalLaunch(w http.ResponseWriter, r *http.Request, out *apiv1.PortalLaunchRequest) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return badRequest("invalid portal launch request")
	}
	// JSON may escape each prompt byte as six bytes; 4096 further bytes cover the
	// bounded metadata and fixed field syntax.
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 6*protocol.MaxPromptBytes+4096))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return badRequest("invalid portal launch request")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return badRequest("invalid portal launch request")
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return badRequest("invalid portal launch request")
		}
		seen[key] = true
		var target *string
		switch key {
		case "requestID":
			target = &out.RequestID
		case "actorRef":
			target = &out.ActorRef
		case "repo":
			target = &out.Repo
		case "mode":
			target = &out.Mode
		case "model":
			target = &out.Model
		case "effort":
			target = &out.Effort
		case "size":
			target = &out.Size
		case "prompt":
			target = &out.Prompt
		default:
			return badRequest("invalid portal launch request")
		}
		// A null leaves a string untouched without an error, so refuse it here.
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil || string(raw) == "null" || json.Unmarshal(raw, target) != nil {
			return badRequest("invalid portal launch request")
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') {
		return badRequest("invalid portal launch request")
	}
	for _, key := range []string{"requestID", "actorRef", "repo", "mode", "model"} {
		if !seen[key] {
			return badRequest("invalid portal launch request")
		}
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return badRequest("invalid portal launch request")
	}
	return nil
}
