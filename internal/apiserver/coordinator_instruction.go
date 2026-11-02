package apiserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

const instructionMaxText = 8 << 10
const instructionMaxRecords = 4

var instructionAttemptPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var instructionKeyPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,118}[a-z0-9])?$`)
var instructionNoncePattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func instructionUnknown() error {
	return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "instruction outcome Unknown; retry the same intent and key")
}

// bindCoordinatorInstructionParent copies the live parent Session UID onto a
// child created by that parent. A failed or replaced read leaves the field
// absent. The request body is not consulted, and usage-capture state is not.
func (s *Server) bindCoordinatorInstructionParent(ctx context.Context, c *caller, sess *v1alpha1.AgentSession) {
	if c == nil || c.kind != kindSession || c.session == nil || c.session.UID == "" || sess == nil || sess.Spec.Parent == "" || c.session.Name != sess.Spec.Parent {
		return
	}
	var parent v1alpha1.AgentSession
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: c.session.Namespace, Name: sess.Spec.Parent}, &parent); err != nil {
		return
	}
	if parent.UID != c.session.UID || !parent.DeletionTimestamp.IsZero() {
		return
	}
	sess.Spec.CoordinatorInstructionParentUID = string(parent.UID)
}

func decodeInstruction(r *http.Request, limit int64, out any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(data)) > limit || !utf8.Valid(data) {
		return badRequest("expected bounded UTF-8 instruction JSON")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || !errors.Is(d.Decode(new(any)), io.EOF) {
		return badRequest("expected one instruction JSON object with intent fields only")
	}
	return nil
}

func validInstructionText(text string) bool {
	if len(text) == 0 || len(text) > instructionMaxText || !utf8.ValidString(text) {
		return false
	}
	for _, r := range text {
		if r < 0x20 && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

func instructionDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func instructionInboxKey(key string) string { return "ci." + key }

func (s *Server) instructionSession(ctx context.Context, r *http.Request) (*v1alpha1.AgentSession, error) {
	key, err := s.sessionKey(r)
	if err != nil {
		return nil, err
	}
	var sess v1alpha1.AgentSession
	if s.Live.Get(ctx, key, &sess) != nil || sess.UID == "" {
		return nil, inboxHidden()
	}
	return &sess, nil
}

func (s *Server) instructionPod(ctx context.Context, sess *v1alpha1.AgentSession) (*corev1.Pod, error) {
	var pod corev1.Pod
	if s.Live.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}, &pod) != nil || pod.UID == "" || !pod.DeletionTimestamp.IsZero() || !metav1.IsControlledBy(&pod, sess) {
		return nil, inboxHidden()
	}
	owner := metav1.GetControllerOf(&pod)
	if owner == nil || owner.UID != sess.UID || owner.Name != sess.Name {
		return nil, inboxHidden()
	}
	return &pod, nil
}

func instructionCallerPod(c *caller, pod *corev1.Pod) bool {
	return c != nil && c.kind == kindSession && c.session != nil && c.pod != nil && pod != nil &&
		c.pod.UID == pod.UID && c.identity.PodUID == string(pod.UID) && c.session.UID != ""
}

func (s *Server) registerInstructionAttempt(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.inboxAvailable(); err != nil {
		return 0, nil, err
	}
	var req apiv1.CoordinatorInstructionAttemptRequest
	if err := decodeInstruction(r, 4096, &req); err != nil {
		return 0, nil, err
	}
	if !instructionAttemptPattern.MatchString(req.AttemptID) {
		return 0, nil, badRequest("instruction attempt id is invalid")
	}
	sess, err := s.instructionSession(ctx, r)
	if err != nil {
		return 0, nil, err
	}
	caller, err := s.inboxRecipientCaller(ctx, c)
	if err != nil || caller.Name != sess.Name || caller.UID != sess.UID {
		return 0, nil, inboxHidden()
	}
	pod, err := s.instructionPod(ctx, sess)
	if err != nil || !instructionCallerPod(c, pod) {
		return 0, nil, inboxHidden()
	}
	phase := apiv1.InstructionPhaseActive
	if req.Ended {
		phase = apiv1.InstructionPhaseEnded
	}
	stored, err := s.patchInstructionStatus(ctx, sess, func(fresh *v1alpha1.AgentSession) error {
		if fresh.UID != sess.UID {
			return inboxHidden()
		}
		cur := fresh.Status.CoordinatorInstructionAttempt
		if cur != nil && cur.SessionUID != string(fresh.UID) {
			return inboxHidden()
		}
		if cur != nil && cur.AttemptID == req.AttemptID {
			if cur.Phase == apiv1.InstructionPhaseEnded && phase == apiv1.InstructionPhaseActive {
				return inboxHidden()
			}
			if cur.PodUID != string(pod.UID) && phase == apiv1.InstructionPhaseActive {
				return inboxHidden()
			}
			if cur.Phase == phase && cur.PodUID == string(pod.UID) {
				return nil
			}
			if phase == apiv1.InstructionPhaseEnded && cur.Phase == apiv1.InstructionPhaseActive && cur.PodUID == string(pod.UID) {
				next := *cur
				next.Phase = phase
				fresh.Status.CoordinatorInstructionAttempt = &next
				return nil
			}
			return inboxHidden()
		}
		if req.Ended {
			return inboxHidden()
		}
		fresh.Status.CoordinatorInstructionAttempt = &v1alpha1.CoordinatorInstructionAttemptStatus{
			SessionUID: string(fresh.UID), PodUID: string(pod.UID), AttemptID: req.AttemptID, Phase: phase,
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	got := stored.Status.CoordinatorInstructionAttempt
	if got == nil || got.AttemptID != req.AttemptID || got.SessionUID != string(sess.UID) || got.PodUID != string(pod.UID) || got.Phase != phase {
		return 0, nil, instructionUnknown()
	}
	return http.StatusOK, apiv1.CoordinatorInstructionAttemptResult{AttemptID: got.AttemptID, Phase: got.Phase, SessionUID: got.SessionUID, PodUID: got.PodUID}, nil
}

func (s *Server) publishInstruction(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.inboxAvailable(); err != nil {
		return 0, nil, err
	}
	if s.TaskClaims == nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "instruction scope is not enabled")
	}
	var req apiv1.CoordinatorInstructionRequest
	if err := decodeInstruction(r, inbox.MaxRequestBytes, &req); err != nil {
		return 0, nil, err
	}
	if !instructionKeyPattern.MatchString(req.Key) || !inbox.ValidKey(instructionInboxKey(req.Key)) || !instructionAttemptPattern.MatchString(req.AttemptID) || !validInstructionText(req.Text) || !projectcatalog.ValidName(req.Project) || req.Repository == "" || req.TargetSessionUID == "" || req.TargetPodUID == "" || req.TaskKey == "" {
		return 0, nil, badRequest("instruction intent requires a bounded key, scope, target, attempt and text")
	}
	if !slices.Contains(s.InboxProjects, req.Project) {
		return 0, nil, inboxHidden()
	}
	target, err := s.instructionSession(ctx, r)
	if err != nil {
		return 0, nil, err
	}
	sender, err := s.inboxSenderCaller(ctx, c)
	if err != nil || sender.UID == "" || string(sender.UID) != target.Spec.CoordinatorInstructionParentUID || sender.Name != target.Spec.Parent {
		return 0, nil, inboxHidden()
	}
	if string(target.UID) != req.TargetSessionUID || target.DeletionTimestamp != nil && !target.DeletionTimestamp.IsZero() {
		return 0, nil, inboxHidden()
	}
	pod, err := s.instructionPod(ctx, target)
	if err != nil || string(pod.UID) != req.TargetPodUID {
		return 0, nil, inboxHidden()
	}
	scope, err := s.instructionScope(ctx, target, req.Project, req.Repository, req.TaskKey)
	if err != nil {
		return 0, nil, err
	}
	senderSnap, err := claimSnapshot(sender)
	if err != nil || senderSnap.Project() != scope.project || senderSnap.Selected().Name != scope.repository {
		return 0, nil, inboxHidden()
	}
	digest := instructionDigest(req.Text)
	existing := instructionByKey(target.Status.CoordinatorInstructions, req.Key)
	if existing != nil {
		if !instructionDeliveryMatch(existing, sender, req, digest) {
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "instruction key already holds a different intent")
		}
		return s.confirmInstruction(ctx, sender, target, req, digest)
	}
	if !instructionAttemptLive(target, pod, req.AttemptID) {
		return 0, nil, inboxHidden()
	}
	spec := coord.InboxMessageSpec{
		Project: req.Project, Key: instructionInboxKey(req.Key), Sender: sessionIdentity(sender), Recipient: sessionIdentity(target),
		Actor: inboxActor(c), Provenance: inboxProvenance(scope.snapshot), Repository: scope.repository,
		Provider: string(sender.Spec.Agent), RequestedModel: sender.Spec.Model, Text: req.Text,
	}
	if !inbox.ValidMessage(spec) {
		return 0, nil, badRequest("instruction intent or saved sender attribution is invalid")
	}
	msg, created, err := s.Inbox.Create(ctx, spec)
	if err != nil {
		if errors.Is(err, inbox.ErrLimit) {
			return 0, nil, tooManyRequests("instruction store is full")
		}
		return 0, nil, instructionUnknown()
	}
	if !inbox.SameIntent(msg.Spec, spec) || msg.UID == "" {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "instruction key already holds a different intent")
	}
	nonce, err := instructionNonce()
	if err != nil {
		return 0, nil, instructionUnknown()
	}
	record := v1alpha1.CoordinatorInstructionStatus{
		Key: req.Key, MessageName: msg.Name, MessageUID: string(msg.UID), SenderUID: string(sender.UID), SenderPodUID: c.identity.PodUID,
		TargetSessionUID: string(target.UID), TargetPodUID: string(pod.UID), AttemptID: req.AttemptID,
		Project: scope.project, Repository: scope.repository, TaskKey: scope.taskKey, TextSHA256: digest, Nonce: nonce,
		DeliveredAt: metav1.NewTime(s.now().UTC().Truncate(time.Second)),
	}
	stored, err := s.patchInstructionStatus(ctx, target, func(fresh *v1alpha1.AgentSession) error {
		if fresh.UID != target.UID || fresh.Spec.CoordinatorInstructionParentUID != string(sender.UID) {
			return inboxHidden()
		}
		if got := instructionByKey(fresh.Status.CoordinatorInstructions, req.Key); got != nil {
			if instructionDeliveryMatch(got, sender, req, digest) && got.MessageUID == record.MessageUID {
				record = *got
				return nil
			}
			return newError(http.StatusConflict, apiv1.CodeConflict, "instruction key already holds a different intent")
		}
		if len(fresh.Status.CoordinatorInstructions) >= instructionMaxRecords {
			return tooManyRequests("instruction record bound is full")
		}
		if !instructionAttemptLive(fresh, pod, req.AttemptID) {
			return inboxHidden()
		}
		fresh.Status.CoordinatorInstructions = append(fresh.Status.CoordinatorInstructions, record)
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	got := instructionByKey(stored.Status.CoordinatorInstructions, req.Key)
	if got == nil || !instructionDeliveryMatch(got, sender, req, digest) || got.MessageUID != string(msg.UID) {
		return 0, nil, instructionUnknown()
	}
	status := http.StatusOK
	outcome := apiv1.InstructionOutcomeConfirmed
	if created && got.Consumed == nil {
		status = http.StatusCreated
		outcome = apiv1.InstructionOutcomeCreated
	}
	return status, instructionReceipt(outcome, got), nil
}

func (s *Server) confirmInstruction(ctx context.Context, sender, target *v1alpha1.AgentSession, req apiv1.CoordinatorInstructionRequest, digest string) (int, any, error) {
	msg, err := s.Inbox.Get(ctx, inbox.MessageName(sessionIdentity(sender), instructionInboxKey(req.Key)))
	if err != nil || msg.Spec.Text == "" || instructionDigest(msg.Spec.Text) != digest || string(msg.UID) == "" {
		return 0, nil, instructionUnknown()
	}
	got := instructionByKey(target.Status.CoordinatorInstructions, req.Key)
	if got == nil || got.MessageUID != string(msg.UID) || !instructionDeliveryMatch(got, sender, req, digest) {
		return 0, nil, instructionUnknown()
	}
	return http.StatusOK, instructionReceipt(apiv1.InstructionOutcomeConfirmed, got), nil
}

func (s *Server) readInstruction(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.inboxAvailable(); err != nil {
		return 0, nil, err
	}
	key := r.PathValue("key")
	if !instructionKeyPattern.MatchString(key) {
		return 0, nil, inboxHidden()
	}
	target, err := s.instructionSession(ctx, r)
	if err != nil {
		return 0, nil, err
	}
	rec := instructionByKey(target.Status.CoordinatorInstructions, key)
	if rec == nil {
		return 0, nil, inboxHidden()
	}
	caller, err := s.inboxRecipientCaller(ctx, c)
	if err != nil {
		return 0, nil, inboxHidden()
	}
	parent := caller.UID != "" && string(caller.UID) == target.Spec.CoordinatorInstructionParentUID && caller.Name == target.Spec.Parent && string(caller.UID) == rec.SenderUID
	recipient := caller.Name == target.Name && caller.UID == target.UID && string(caller.UID) == rec.TargetSessionUID
	if !parent && !recipient {
		return 0, nil, inboxHidden()
	}
	msg, err := s.Inbox.Get(ctx, rec.MessageName)
	if err != nil || string(msg.UID) != rec.MessageUID || instructionDigest(msg.Spec.Text) != rec.TextSHA256 {
		return 0, nil, instructionUnknown()
	}
	if rec.SenderUID == "" || rec.SenderUID != target.Spec.CoordinatorInstructionParentUID {
		return 0, nil, instructionUnknown()
	}
	view := apiv1.CoordinatorInstructionView{CoordinatorInstructionReceipt: instructionReceipt(apiv1.InstructionOutcomeConfirmed, rec), SenderUID: rec.SenderUID}
	if ack, err := s.Inbox.ReadAck(ctx, msg); err == nil && ack != nil {
		view.ReadAck = true
	} else if err != nil && !errors.Is(err, inbox.ErrNotFound) {
		return 0, nil, instructionUnknown()
	}
	if recipient {
		view.Text = msg.Spec.Text
		view.Nonce = rec.Nonce
	}
	return http.StatusOK, view, nil
}

func (s *Server) listPendingInstructions(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.inboxAvailable(); err != nil {
		return 0, nil, err
	}
	target, err := s.instructionSession(ctx, r)
	if err != nil {
		return 0, nil, err
	}
	caller, err := s.inboxRecipientCaller(ctx, c)
	if err != nil || caller.Name != target.Name || caller.UID != target.UID {
		return 0, nil, inboxHidden()
	}
	pod, err := s.instructionPod(ctx, target)
	if err != nil || !instructionCallerPod(c, pod) {
		return 0, nil, inboxHidden()
	}
	cur := target.Status.CoordinatorInstructionAttempt
	if cur == nil || !instructionAttemptLive(target, pod, cur.AttemptID) {
		return 0, nil, inboxHidden()
	}
	pending := make([]apiv1.CoordinatorInstructionView, 0)
	for i := range target.Status.CoordinatorInstructions {
		rec := target.Status.CoordinatorInstructions[i]
		if rec.Consumed != nil || rec.AttemptID != cur.AttemptID || rec.TargetSessionUID != string(target.UID) || rec.TargetPodUID != string(pod.UID) {
			continue
		}
		if rec.SenderUID == "" || rec.SenderUID != target.Spec.CoordinatorInstructionParentUID || rec.Key == "" {
			return 0, nil, instructionUnknown()
		}
		msg, err := s.Inbox.Get(ctx, rec.MessageName)
		if err != nil || string(msg.UID) != rec.MessageUID || instructionDigest(msg.Spec.Text) != rec.TextSHA256 {
			return 0, nil, instructionUnknown()
		}
		view := apiv1.CoordinatorInstructionView{CoordinatorInstructionReceipt: instructionReceipt(apiv1.InstructionOutcomeConfirmed, &rec), SenderUID: rec.SenderUID, Text: msg.Spec.Text, Nonce: rec.Nonce}
		if ack, err := s.Inbox.ReadAck(ctx, msg); err == nil && ack != nil {
			view.ReadAck = true
		} else if err != nil && !errors.Is(err, inbox.ErrNotFound) {
			return 0, nil, instructionUnknown()
		}
		pending = append(pending, view)
		if len(pending) > instructionMaxRecords {
			return 0, nil, instructionUnknown()
		}
	}
	return http.StatusOK, apiv1.CoordinatorInstructionCheckpoint{
		Outcome: apiv1.InstructionOutcomeConfirmed, AttemptID: cur.AttemptID, SessionUID: string(target.UID), PodUID: string(pod.UID), Pending: pending,
	}, nil
}

func (s *Server) consumeInstruction(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.inboxAvailable(); err != nil {
		return 0, nil, err
	}
	key := r.PathValue("key")
	var req apiv1.CoordinatorInstructionConsumeRequest
	if err := decodeInstruction(r, 4096, &req); err != nil {
		return 0, nil, err
	}
	if !instructionKeyPattern.MatchString(key) || !instructionAttemptPattern.MatchString(req.AttemptID) || !instructionNoncePattern.MatchString(req.Nonce) || req.TargetSessionUID == "" || req.TargetPodUID == "" {
		return 0, nil, badRequest("instruction consumption requires the delivery nonce, attempt and target")
	}
	target, err := s.instructionSession(ctx, r)
	if err != nil {
		return 0, nil, err
	}
	caller, err := s.inboxRecipientCaller(ctx, c)
	if err != nil || caller.Name != target.Name || caller.UID != target.UID || string(caller.UID) != req.TargetSessionUID {
		return 0, nil, inboxHidden()
	}
	pod, err := s.instructionPod(ctx, target)
	if err != nil || !instructionCallerPod(c, pod) || string(pod.UID) != req.TargetPodUID {
		return 0, nil, inboxHidden()
	}
	prior := instructionByKey(target.Status.CoordinatorInstructions, key)
	if prior == nil || prior.Nonce != req.Nonce || prior.AttemptID != req.AttemptID || prior.TargetPodUID != req.TargetPodUID || prior.TargetSessionUID != req.TargetSessionUID {
		return 0, nil, inboxHidden()
	}
	if prior.Consumed != nil {
		if instructionConsumptionMatch(prior) {
			return http.StatusOK, instructionConsumeACK(apiv1.InstructionOutcomeConfirmed, prior), nil
		}
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "instruction consumption already records a different receipt")
	}
	if !instructionAttemptLive(target, pod, req.AttemptID) {
		return 0, nil, inboxHidden()
	}
	msg, err := s.Inbox.Get(ctx, prior.MessageName)
	if err != nil || string(msg.UID) != prior.MessageUID || instructionDigest(msg.Spec.Text) != prior.TextSHA256 {
		return 0, nil, instructionUnknown()
	}
	stamp := metav1.NewTime(s.now().UTC().Truncate(time.Second))
	stored, err := s.patchInstructionStatus(ctx, target, func(fresh *v1alpha1.AgentSession) error {
		if fresh.UID != target.UID {
			return inboxHidden()
		}
		idx := instructionIndex(fresh.Status.CoordinatorInstructions, key)
		if idx < 0 {
			return inboxHidden()
		}
		rec := fresh.Status.CoordinatorInstructions[idx]
		if rec.Nonce != req.Nonce || rec.AttemptID != req.AttemptID || rec.TargetPodUID != string(pod.UID) || rec.TargetSessionUID != string(fresh.UID) || rec.MessageUID != string(msg.UID) || rec.TextSHA256 != prior.TextSHA256 {
			return inboxHidden()
		}
		if rec.Consumed != nil {
			fresh.Status.CoordinatorInstructions[idx] = rec
			if instructionConsumptionMatch(&rec) {
				return nil
			}
			return newError(http.StatusConflict, apiv1.CodeConflict, "instruction consumption already records a different receipt")
		}
		if !instructionAttemptLive(fresh, pod, req.AttemptID) {
			return inboxHidden()
		}
		rec.Consumed = &v1alpha1.CoordinatorInstructionConsumptionStatus{
			AttemptID: rec.AttemptID, SessionUID: rec.TargetSessionUID, PodUID: rec.TargetPodUID,
			MessageUID: rec.MessageUID, Nonce: rec.Nonce, TextSHA256: rec.TextSHA256, At: stamp,
		}
		fresh.Status.CoordinatorInstructions[idx] = rec
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	got := instructionByKey(stored.Status.CoordinatorInstructions, key)
	if got == nil || !instructionConsumptionMatch(got) || got.Consumed.Nonce != req.Nonce || got.Consumed.AttemptID != req.AttemptID {
		return 0, nil, instructionUnknown()
	}
	outcome := apiv1.InstructionOutcomeConsumed
	status := http.StatusCreated
	if !got.Consumed.At.Equal(&stamp) {
		outcome = apiv1.InstructionOutcomeConfirmed
		status = http.StatusOK
	}
	return status, instructionConsumeACK(outcome, got), nil
}

func (s *Server) patchInstructionStatus(ctx context.Context, sess *v1alpha1.AgentSession, mutate func(*v1alpha1.AgentSession) error) (*v1alpha1.AgentSession, error) {
	key := types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		var fresh v1alpha1.AgentSession
		if s.Live.Get(ctx, key, &fresh) != nil || fresh.UID != sess.UID {
			return nil, instructionUnknown()
		}
		base := fresh.DeepCopy()
		if err := mutate(&fresh); err != nil {
			return nil, err
		}
		if apiequality.Semantic.DeepEqual(base.Status, fresh.Status) {
			return &fresh, nil
		}
		err := s.Client.Status().Patch(ctx, &fresh, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		if err == nil {
			var got v1alpha1.AgentSession
			if s.Live.Get(ctx, key, &got) != nil || got.UID != sess.UID {
				return nil, instructionUnknown()
			}
			return &got, nil
		}
		var apiErr *apiError
		if errors.As(err, &apiErr) {
			return nil, err
		}
		if !apierrors.IsConflict(err) {
			return nil, instructionUnknown()
		}
		last = err
	}
	if last != nil {
		return nil, instructionUnknown()
	}
	return nil, instructionUnknown()
}

type instructionScope struct {
	project, repository, taskKey string
	snapshot                     projectcatalog.Snapshot
}

func (s *Server) instructionScope(ctx context.Context, target *v1alpha1.AgentSession, project, repository, taskKey string) (instructionScope, error) {
	snapshot, err := claimSnapshot(target)
	if err != nil || snapshot.Project() != project || snapshot.Selected().Name != repository {
		return instructionScope{}, inboxHidden()
	}
	if !taskclaim.ValidIdentity(coord.TaskIdentity{Project: project, Repository: repository, TaskKey: taskKey}) {
		return instructionScope{}, badRequest("instruction task identity is invalid")
	}
	owned, err := s.TaskClaims.ListOwned(ctx, project, string(target.UID))
	if err != nil {
		return instructionScope{}, instructionUnknown()
	}
	if len(owned) != 1 || owned[0].Spec.Identity.Project != project || owned[0].Spec.Identity.Repository != repository || owned[0].Spec.Identity.TaskKey != taskKey || owned[0].Spec.Owner.UID != string(target.UID) {
		return instructionScope{}, inboxHidden()
	}
	return instructionScope{project: project, repository: repository, taskKey: taskKey, snapshot: snapshot}, nil
}

func instructionAttemptLive(sess *v1alpha1.AgentSession, pod *corev1.Pod, attempt string) bool {
	if sess == nil || pod == nil || sess.Status.Agent == nil || sess.Status.Agent.Status != protocol.AgentBusy {
		return false
	}
	cur := sess.Status.CoordinatorInstructionAttempt
	return cur != nil && cur.Phase == apiv1.InstructionPhaseActive && cur.AttemptID == attempt && cur.SessionUID == string(sess.UID) && cur.PodUID == string(pod.UID)
}

func instructionByKey(list []v1alpha1.CoordinatorInstructionStatus, key string) *v1alpha1.CoordinatorInstructionStatus {
	if i := instructionIndex(list, key); i >= 0 {
		return &list[i]
	}
	return nil
}

func instructionIndex(list []v1alpha1.CoordinatorInstructionStatus, key string) int {
	for i := range list {
		if list[i].Key == key {
			return i
		}
	}
	return -1
}

func instructionDeliveryMatch(rec *v1alpha1.CoordinatorInstructionStatus, sender *v1alpha1.AgentSession, req apiv1.CoordinatorInstructionRequest, digest string) bool {
	return rec != nil && sender != nil && rec.Key == req.Key && rec.SenderUID == string(sender.UID) && rec.TargetSessionUID == req.TargetSessionUID &&
		rec.TargetPodUID == req.TargetPodUID && rec.AttemptID == req.AttemptID && rec.Project == req.Project && rec.Repository == req.Repository &&
		rec.TaskKey == req.TaskKey && rec.TextSHA256 == digest && instructionNoncePattern.MatchString(rec.Nonce)
}

func instructionConsumptionMatch(rec *v1alpha1.CoordinatorInstructionStatus) bool {
	if rec == nil || rec.Consumed == nil {
		return false
	}
	c := rec.Consumed
	return c.AttemptID == rec.AttemptID && c.SessionUID == rec.TargetSessionUID && c.PodUID == rec.TargetPodUID && c.MessageUID == rec.MessageUID && c.Nonce == rec.Nonce && c.TextSHA256 == rec.TextSHA256 && !c.At.IsZero()
}

func instructionReceipt(outcome string, rec *v1alpha1.CoordinatorInstructionStatus) apiv1.CoordinatorInstructionReceipt {
	return apiv1.CoordinatorInstructionReceipt{
		Outcome: outcome, Key: rec.Key, MessageName: rec.MessageName, MessageUID: rec.MessageUID, TextSHA256: rec.TextSHA256,
		TargetSessionUID: rec.TargetSessionUID, TargetPodUID: rec.TargetPodUID, AttemptID: rec.AttemptID,
		Project: rec.Project, Repository: rec.Repository, TaskKey: rec.TaskKey, Consumed: rec.Consumed != nil,
	}
}

func instructionConsumeACK(outcome string, rec *v1alpha1.CoordinatorInstructionStatus) apiv1.CoordinatorInstructionConsumeACK {
	return apiv1.CoordinatorInstructionConsumeACK{
		Outcome: outcome, Key: rec.Key, MessageUID: rec.MessageUID, AttemptID: rec.AttemptID, Nonce: rec.Nonce,
		TextSHA256: rec.TextSHA256, TargetSessionUID: rec.TargetSessionUID, TargetPodUID: rec.TargetPodUID,
	}
}

func instructionNonce() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
