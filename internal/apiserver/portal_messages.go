package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
)

// portalMessage is a distinct, default-off command delegation. The scope is a
// fixed project and an explicitly configured name/UID pair, not read authority.
func (s *Server) portalMessage(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if c.kind != kindPortalReader || len(s.PortalMessageTargets) == 0 {
		return 0, nil, forbidden("portal message action is unavailable")
	}
	var req apiv1.PortalMessageRequest
	if err := decodePortalMessage(w, r, &req); err != nil {
		return 0, nil, badRequest("invalid portal message request")
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || req.Project != "dev-env" || !portalOpaqueID(req.RequestID) || !portalOpaqueID(req.ActorRef) || len(validation.IsDNS1123Label(req.SessionName)) != 0 || req.SessionUID == "" || len(req.SessionUID) > 128 || s.PortalMessageTargets[req.SessionName] != req.SessionUID {
		return 0, nil, forbidden("portal message action is unavailable")
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || len(text) > apiv1.MaxMessageBytes || stripControl(text) != text || portalMessageURL.MatchString(text) {
		return 0, nil, invalid(fieldError("text", "message is invalid"))
	}
	result := apiv1.PortalMessageReceipt{RequestID: req.RequestID, SessionName: req.SessionName, SessionUID: req.SessionUID, ObservedAt: s.now().UTC(), TransportStatus: apiv1.PortalTransportRefused, Reason: "refused"}
	if s.Live == nil {
		return http.StatusOK, result, nil
	}
	var sess v1alpha1.AgentSession
	if s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: req.SessionName}, &sess) != nil || string(sess.UID) != req.SessionUID || sess.Spec.Repo != "dev-env" || (sess.Spec.Mode != v1alpha1.ModeLocal && sess.Spec.Mode != v1alpha1.ModeRemote) || sess.Status.Phase != v1alpha1.PhaseRunning || sess.Spec.OperatingMode == v1alpha1.OperatingModeSuspended || !sess.DeletionTimestamp.IsZero() {
		return http.StatusOK, result, nil
	}
	if s.checkSessionTaskBudget(ctx, &sess) != nil {
		return http.StatusOK, result, nil
	}
	pod, err := s.runningPod(ctx, &sess)
	if err != nil || pod.UID == "" || s.Exec == nil {
		return http.StatusOK, result, nil
	}
	result.ResolvedPodUID = string(pod.UID)
	callCtx, cancel := context.WithTimeout(ctx, podCmdTimeout)
	defer cancel()
	// The agentd process checks both UIDs before accepting stdin. Never include
	// caller-supplied identity, text or executor stderr in logs or errors.
	args := []string{"agentd", "ctl", "deliver", "--from", "portal/" + req.ActorRef, "--expected-pod-uid", string(pod.UID), "--expected-session-uid", string(sess.UID)}
	err = s.Exec.Run(callCtx, pod.Namespace, pod.Name, controller.ContainerName, args, strings.NewReader(text), io.Discard, io.Discard)
	result.ObservedAt = s.now().UTC()
	var exit utilexec.ExitError
	switch {
	case err == nil:
		result.TransportStatus = apiv1.PortalTransportDelivered
		result.Reason = "delivered"
	case errors.As(err, &exit) && exit.ExitStatus() == exitNotAddressable:
		result.Reason = "refused"
	default:
		result.TransportStatus = apiv1.PortalTransportUnknown
		result.Reason = "unknown"
	}
	return http.StatusOK, result, nil
}

var portalMessageURL = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://|\bwww\.)`)

func portalOpaqueID(s string) bool {
	if len(s) < 16 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// decodePortalMessage rejects duplicate and unknown keys without returning
// attacker-controlled field names in an error response.
func decodePortalMessage(w http.ResponseWriter, r *http.Request, out *apiv1.PortalMessageRequest) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return badRequest("invalid portal message request")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*apiv1.MaxMessageBytes+2048))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return badRequest("invalid portal message request")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return badRequest("invalid portal message request")
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return badRequest("invalid portal message request")
		}
		seen[key] = true
		var target *string
		switch key {
		case "requestID":
			target = &out.RequestID
		case "project":
			target = &out.Project
		case "sessionName":
			target = &out.SessionName
		case "sessionUID":
			target = &out.SessionUID
		case "text":
			target = &out.Text
		case "actorRef":
			target = &out.ActorRef
		default:
			return badRequest("invalid portal message request")
		}
		if decoder.Decode(target) != nil {
			return badRequest("invalid portal message request")
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') || len(seen) != 6 {
		return badRequest("invalid portal message request")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return badRequest("invalid portal message request")
	}
	return nil
}
