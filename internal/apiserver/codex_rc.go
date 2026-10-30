package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// One default-false gate; this route uses the existing authenticated listener.
func (s *Server) registerCodexRC(mux *http.ServeMux) {
	if s.CodexRCEnabled {
		mux.Handle(apiv1.SessionsPath+"/{name}/codex-thread", s.serve(route{methods: map[string]handler{http.MethodPost: s.codexRCThread}}))
	}
}
func (s *Server) codexRCThread(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	// Exact remote-host creation predicate. Coordinator and ordinary workers get
	// no access, irrespective of model, claimed role or configured target.
	if c.kind != kindHuman && c.kind != kindClient && !s.isWorkbenchSession(c) {
		return 0, nil, forbidden("codex RC requires Tom or his workbench")
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		return 0, nil, badRequest("invalid codex RC request")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, apiv1.CodexRCMaxRequest))
	var req apiv1.CodexRCRequest
	if err != nil || apiv1.DecodeCodexRC(raw, &req, apiv1.CodexRCMaxRequest) != nil {
		return 0, nil, badRequest("invalid codex RC request")
	}
	if s.CodexRCTarget == nil || s.Projects == nil || s.Live == nil || s.Client == nil || s.Exec == nil {
		return 0, nil, newError(503, apiv1.CodeUnavailable, "codex RC qualification unavailable")
	}
	target := *s.CodexRCTarget
	if target.SessionName != r.PathValue("name") || !target.Matches(req) {
		return 0, nil, forbidden("codex RC target is outside configured scope")
	}
	// The single retained Session annotation is the durable one-use reservation.
	// Never evict it, even for a new request ID. Replay reads only the old receipt,
	// before readiness/catalog checks, and never re-execs an unresolved request.
	key := types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: target.SessionName}
	var sess v1alpha1.AgentSession
	if err := s.Live.Get(ctx, key, &sess); err != nil {
		return 0, nil, fromKubeError(err, "codex RC target")
	}
	if string(sess.UID) != req.SessionUID {
		return 0, nil, newError(409, apiv1.CodeConflict, "codex RC session UID changed")
	}
	digest := apiv1.CodexRCClientHash(c.identity.Username, req)
	if saved, ok := sess.Annotations[apiv1.CodexRCIntentAnnotation]; ok {
		var receipt apiv1.CodexRCReceipt
		if apiv1.DecodeCodexRC([]byte(saved), &receipt, apiv1.CodexRCMaxPacket) != nil || receipt.ClientSHA256 != digest || receipt.RequestID != req.RequestID {
			return 0, nil, newError(409, apiv1.CodeConflict, "codex RC intent already reserved; do not replay")
		}
		if receipt.Acknowledged() {
			return http.StatusOK, receipt, nil
		}
		return http.StatusConflict, receipt, nil
	}
	if !req.Valid(s.now()) || !target.Valid(s.now()) {
		return 0, nil, badRequest("invalid or expired codex RC qualification")
	}
	ctx, cancel := context.WithDeadline(ctx, req.Deadline)
	defer cancel()
	qualify := func() error {
		var current v1alpha1.AgentSession
		if s.Live.Get(ctx, key, &current) != nil || current.UID != sess.UID || !current.DeletionTimestamp.IsZero() || current.Spec.Agent != v1alpha1.AgentCodex || current.Spec.Mode != v1alpha1.ModeRemote || current.Status.Phase != v1alpha1.PhaseRunning || current.Spec.OperatingMode == v1alpha1.OperatingModeSuspended {
			return newError(409, apiv1.CodeConflict, "codex RC target changed")
		}
		pod, err := s.runningPod(ctx, &current)
		if err != nil {
			return err
		}
		if string(pod.UID) != target.PodUID {
			return newError(409, apiv1.CodeConflict, "codex RC pod UID changed")
		}
		snapshot, _, err := s.Projects.snapshot(ctx, s.Live, req.Project, req.Repository)
		if err != nil {
			return newError(503, apiv1.CodeUnavailable, "codex RC catalog unavailable")
		}
		accepted, err := projectcatalog.ParseSnapshot(snapshot)
		if err != nil || apiv1.CodexRCHash(accepted) != target.SnapshotSHA256 {
			return newError(409, apiv1.CodeConflict, "codex RC accepted repository changed")
		}
		return nil
	}
	if err := qualify(); err != nil {
		return 0, nil, err
	}
	dispatch := apiv1.CodexRCDispatch{Caller: c.identity.Username, Request: req, Target: target, ClientSHA256: digest}
	receipt := apiv1.CodexRCReceipt{Caller: c.identity.Username, RequestID: req.RequestID, ClientSHA256: digest, DispatchSHA256: apiv1.CodexRCHash(dispatch), Target: target, Model: req.Model, Effort: req.Effort, State: "reserved", Reason: "create_unconfirmed", StartedAt: s.now().UTC()}
	exec := func(operation string) (apiv1.CodexRCReceipt, error) {
		var result apiv1.CodexRCReceipt
		input, _ := json.Marshal(dispatch)
		stdout := &limitedBuffer{max: apiv1.CodexRCMaxPacket + 1}
		// No shell, dynamic method, new listener, process launch or caller path.
		err := s.Exec.Run(ctx, key.Namespace, key.Name, controller.ContainerName, []string{"agentd", "ctl", "codex-thread", operation}, bytes.NewReader(input), stdout, io.Discard)
		if err != nil || apiv1.DecodeCodexRC([]byte(stdout.String()), &result, apiv1.CodexRCMaxPacket) != nil || result.Caller != c.identity.Username || result.ClientSHA256 != digest || result.DispatchSHA256 != receipt.DispatchSHA256 || apiv1.CodexRCHash(result.Target) != apiv1.CodexRCHash(target) || result.RequestID != req.RequestID || result.Model != req.Model || result.Effort != req.Effort {
			return result, newError(503, apiv1.CodeUnavailable, "codex RC operation unconfirmed")
		}
		return result, nil
	}
	preflight, err := exec("preflight")
	if err != nil || preflight.State != "qualified" {
		return 0, nil, newError(409, apiv1.CodeConflict, "codex RC preflight refused")
	}
	if err := qualify(); err != nil {
		return 0, nil, err
	}
	if sess.Annotations == nil {
		sess.Annotations = map[string]string{}
	}
	frozen, _ := json.Marshal(receipt)
	sess.Annotations[apiv1.CodexRCIntentAnnotation] = string(frozen)
	// ResourceVersion CAS across replicas. No retry on conflict or unknown write.
	if err := s.Client.Update(ctx, &sess); err != nil {
		if apierrors.IsConflict(err) {
			return 0, nil, newError(409, apiv1.CodeConflict, "codex RC reservation conflict; do not replay")
		}
		return 0, nil, fromKubeError(err, "codex RC reserve")
	}
	s.Log.Info("codex RC reserved", "caller", c.identity.Username, "sessionUID", sess.UID, "digest", digest)
	if err := qualify(); err != nil {
		return http.StatusServiceUnavailable, receipt, nil
	}
	result, err := exec("start")
	receipt.State, receipt.Reason = "unconfirmed", "create_unconfirmed"
	receipt.FinishedAt = s.now().UTC()
	status := http.StatusServiceUnavailable
	if err == nil {
		receipt.ThreadID, receipt.TurnID = result.ThreadID, result.TurnID
		if result.Acknowledged() {
			receipt.State, receipt.Reason = "confirmed", "thread_and_turn_acknowledged"
			status = http.StatusCreated
		}
	}
	// Do not clear a reservation on refusal or lost ACK. Its state remains
	// one-use forever for this UID. Receipt persistence failure is uncertainty.
	var latest v1alpha1.AgentSession
	if s.Live.Get(ctx, key, &latest) != nil || latest.UID != sess.UID || latest.Annotations[apiv1.CodexRCIntentAnnotation] != string(frozen) {
		receipt.State, receipt.Reason = "unconfirmed", "receipt_unconfirmed"
		return 503, receipt, nil
	}
	data, _ := json.Marshal(receipt)
	latest.Annotations[apiv1.CodexRCIntentAnnotation] = string(data)
	if s.Client.Update(ctx, &latest) != nil {
		receipt.State, receipt.Reason = "unconfirmed", "receipt_unconfirmed"
		return 503, receipt, nil
	}
	s.Log.Info("codex RC result", "caller", c.identity.Username, "sessionUID", sess.UID, "digest", digest, "state", receipt.State, "elapsed", receipt.FinishedAt.Sub(receipt.StartedAt))
	return status, receipt, nil
}

// ParseCodexRCTarget accepts only a bounded deployment-owned qualification file.
func ParseCodexRCTarget(raw []byte, now time.Time) (*apiv1.CodexRCTarget, error) {
	var target apiv1.CodexRCTarget
	if apiv1.DecodeCodexRC(raw, &target, apiv1.CodexRCMaxPacket) != nil || !target.Valid(now) {
		return nil, badRequest("invalid codex RC target packet")
	}
	return &target, nil
}
