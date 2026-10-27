package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
)

// portalConversation is an independently scoped private read. It never uses
// logs or archives when an older agentd lacks the fixed projection command.
func (s *Server) portalConversation(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	uid := r.PathValue("uid")
	name := s.PortalConversationTargets[uid]
	if c.kind != kindPortalReader || uid == "" || len(uid) > 64 || name == "" || len(validation.IsDNS1123Label(name)) != 0 || r.URL.RawPath != "" {
		return 0, nil, forbidden("portal conversation unavailable")
	}
	// ParseQuery rejects malformed escapes, duplicate/unknown keys and bounds.
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || r.URL.ForceQuery || len(values) > 2 {
		return 0, nil, badRequest("invalid conversation request")
	}
	limit := 60
	before := ""
	for key, value := range values {
		if len(value) != 1 {
			return 0, nil, badRequest("invalid conversation request")
		}
		switch key {
		case "limit":
			limit, err = strconv.Atoi(value[0])
			if err != nil || limit < 1 || limit > 60 {
				return 0, nil, badRequest("invalid conversation request")
			}
		case "before":
			before = value[0]
			if !validPortalConversationCursor(before) {
				return 0, nil, badRequest("invalid conversation request")
			}
		default:
			return 0, nil, badRequest("invalid conversation request")
		}
	}
	unavailable := apiv1.PortalConversationResponse{SessionName: name, SessionUID: uid, Projection: protocol.PortalConversation{State: "unavailable", ReasonCode: "unsupported", ObservedAt: s.now().UTC()}}
	if s.Live == nil || s.Exec == nil {
		return http.StatusOK, unavailable, nil
	}
	var sess v1alpha1.AgentSession
	if s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: name}, &sess) != nil || string(sess.UID) != uid || sess.Spec.Repo != "dev-env" || sess.Status.Phase != v1alpha1.PhaseRunning || sess.Spec.OperatingMode == v1alpha1.OperatingModeSuspended || !sess.DeletionTimestamp.IsZero() {
		return http.StatusOK, unavailable, nil
	}
	pod, err := s.runningPod(ctx, &sess)
	if err != nil || pod.UID == "" {
		return http.StatusOK, unavailable, nil
	}
	unavailable.PodUID = string(pod.UID)
	args := []string{"agentd", "ctl", "portal-conversation", "--expected-session-uid", uid, "--expected-pod-uid", string(pod.UID), "--limit", strconv.Itoa(limit)}
	if before != "" {
		args = append(args, "--before", before)
	}
	bounded := &portalConversationBuffer{}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if s.Exec.Run(callCtx, pod.Namespace, pod.Name, controller.ContainerName, args, nil, bounded, io.Discard) != nil {
		return http.StatusOK, unavailable, nil
	}
	// An old Pod may answer after a Session or Pod replacement. Revalidate
	// both live objects before releasing any private projection.
	var current v1alpha1.AgentSession
	if s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: name}, &current) != nil || string(current.UID) != uid || current.Spec.Repo != "dev-env" || current.Status.Phase != v1alpha1.PhaseRunning || current.Spec.OperatingMode == v1alpha1.OperatingModeSuspended || !current.DeletionTimestamp.IsZero() {
		return http.StatusOK, unavailable, nil
	}
	currentPod, err := s.runningPod(ctx, &current)
	if err != nil || currentPod.UID != pod.UID {
		return http.StatusOK, unavailable, nil
	}
	var projection protocol.PortalConversation
	if bounded.overflow || !protocol.StrictPortalConversationJSON(bounded.Bytes()) {
		return http.StatusOK, unavailable, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(bounded.Bytes()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&projection) != nil || decoder.Decode(&struct{}{}) != io.EOF || !projection.Valid() {
		return http.StatusOK, unavailable, nil
	}
	return http.StatusOK, apiv1.PortalConversationResponse{SessionName: name, SessionUID: uid, PodUID: string(pod.UID), Projection: projection}, nil
}

func validPortalConversationCursor(s string) bool {
	if len(s) < 1 || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

type portalConversationBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *portalConversationBuffer) Write(p []byte) (int, error) {
	if len(p) > protocol.PortalConversationEncodedLimit-b.Len() {
		b.overflow = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}
