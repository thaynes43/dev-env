package apiserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	portalReaderNamespace = "dev-agents"
	portalReaderAccount   = "dev-env-v2-portal"
	portalReaderRef       = portalReaderNamespace + "/" + portalReaderAccount
	portalPageDefault     = 50
	portalPageMax         = 100
	portalFreshFor        = 15 * time.Minute
	portalCursorMax       = 2048
)

// resolvePortalReader accepts only a pod-bound token for the portal ServiceAccount.
// The pod does not have to be an AgentSession. A missing or replaced pod is refused.
func (s *Server) resolvePortalReader(ctx context.Context, id Identity) (*caller, error) {
	if id.Username != serviceAccountPrefix+portalReaderNamespace+":"+portalReaderAccount || id.Namespace != portalReaderNamespace || id.ServiceAccount != portalReaderAccount {
		return nil, forbidden("the portal reader identity is not the portal ServiceAccount")
	}
	if id.PodName == "" || id.PodUID == "" || len(validation.IsDNS1123Subdomain(id.PodName)) != 0 {
		return nil, forbidden("the portal reader calls with its pod's projected token")
	}
	if s.Live == nil {
		return nil, forbidden("the portal reader's pod evidence is unavailable")
	}
	var pod corev1.Pod
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: portalReaderNamespace, Name: id.PodName}, &pod); err != nil {
		return nil, forbidden("the portal reader's pod evidence is unavailable")
	}
	if pod.DeletionTimestamp != nil || string(pod.UID) != id.PodUID || pod.Namespace != portalReaderNamespace || pod.Spec.ServiceAccountName != portalReaderAccount {
		return nil, forbidden("the portal reader token is not bound to its current pod")
	}
	return &caller{kind: kindPortalReader, identity: id, parent: portalReaderRef, pod: &pod}, nil
}

func (s *Server) listPortalSessions(ctx context.Context, r *http.Request) (int, any, error) {
	if err := portalCanonicalRequest(r); err != nil {
		return 0, nil, err
	}
	if s.Policy.SessionNamespace != portalReaderNamespace || s.Live == nil {
		return 0, nil, forbidden("the portal reader is not serving the dev-agents session namespace")
	}
	limit, cursor, activity, err := portalPageQuery(r.URL.RawQuery)
	if err != nil {
		return 0, nil, err
	}
	if cursor != nil && cursor.Limit != limit {
		return 0, nil, badRequest("the portal session cursor does not match this limit")
	}
	opts := &client.ListOptions{Namespace: portalReaderNamespace, Limit: int64(limit)}
	if cursor != nil {
		opts.Continue = cursor.Continue
	}
	var list v1alpha1.AgentSessionList
	if err := s.Live.List(ctx, &list, opts); err != nil {
		if apierrors.IsResourceExpired(err) {
			return 0, nil, badRequest("the portal session snapshot expired")
		}
		return 0, nil, fromKubeError(err, "list sessions")
	}
	if int64(len(list.Items)) > int64(limit) {
		return 0, nil, internal("the portal session list was not bounded")
	}
	if cursor != nil && list.ResourceVersion != cursor.RV {
		return 0, nil, badRequest("the portal session snapshot changed")
	}
	now := s.now()
	page := apiv1.PortalSessionPage{Sessions: []apiv1.PortalSessionSummary{}, Limit: limit, ObservedAt: now.UTC(), Freshness: "observed"}
	for i := range list.Items {
		item := &list.Items[i]
		// Every repository's sessions are listed; the portal groups them into
		// projects by repo (Tom, 2026-10-27).
		if item.Namespace != portalReaderNamespace {
			continue
		}
		summary := portalSummary(item, now)
		if activity {
			summary.Activity = portalActivity(item, now)
			if s.PortalConversationUsage {
				summary.Activity.ConversationUsage = portalConversationUsage(item, now)
			}
		}
		if freshnessRank(summary.Freshness) > freshnessRank(page.Freshness) {
			page.Freshness = summary.Freshness
		}
		page.Sessions = append(page.Sessions, summary)
	}
	if list.Continue == "" {
		page.Complete = true
		return http.StatusOK, page, nil
	}
	if list.ResourceVersion == "" {
		return 0, nil, internal("the portal session list has no snapshot")
	}
	encoded, err := encodePortalCursor(portalCursor{RV: list.ResourceVersion, Continue: list.Continue, Limit: limit})
	if err != nil {
		return 0, nil, internal("the portal session cursor does not fit its bound")
	}
	page.NextCursor = encoded
	return http.StatusOK, page, nil
}

func portalCanonicalRequest(r *http.Request) error {
	if r.URL == nil || r.URL.Path != apiv1.SessionsPath || (r.URL.RawPath != "" && r.URL.RawPath != apiv1.SessionsPath) || strings.Contains(r.URL.RawQuery, ";") {
		return badRequest("the portal session path is not canonical")
	}
	if _, err := url.ParseQuery(r.URL.RawQuery); err != nil {
		return badRequest("the portal session query is not canonical")
	}
	return nil
}

type portalCursor struct {
	RV       string `json:"rv"`
	Continue string `json:"continue"`
	Limit    int    `json:"limit"`
}

// portalPageQuery reads limit, cursor and view. view=activity is the only view;
// without it each summary keeps its original six keys.
func portalPageQuery(raw string) (int, *portalCursor, bool, error) {
	q, err := url.ParseQuery(raw)
	if err != nil || strings.Contains(raw, ";") {
		return 0, nil, false, badRequest("the portal session query is not canonical")
	}
	limit := portalPageDefault
	var cursor *portalCursor
	activity := false
	for key, values := range q {
		if len(values) != 1 {
			return 0, nil, false, badRequest("query parameter %s is repeated", key)
		}
		switch key {
		case "limit":
			n, err := strconv.Atoi(values[0])
			if err != nil || n < 1 || n > portalPageMax || strconv.Itoa(n) != values[0] {
				return 0, nil, false, badRequest("limit must be from 1 to %d", portalPageMax)
			}
			limit = n
		case "cursor":
			decoded, err := decodePortalCursor(values[0])
			if err != nil {
				return 0, nil, false, badRequest("the portal session cursor is malformed")
			}
			cursor = &decoded
		case "view":
			if values[0] != apiv1.PortalViewActivity {
				return 0, nil, false, badRequest("view must be %s", apiv1.PortalViewActivity)
			}
			activity = true
		default:
			return 0, nil, false, badRequest("the portal reader does not accept %q", key)
		}
	}
	return limit, cursor, activity, nil
}

func decodePortalCursor(raw string) (portalCursor, error) {
	var cursor portalCursor
	if len(raw) == 0 || len(raw) > portalCursorMax || strings.ContainsAny(raw, "/?#") {
		return cursor, errPortalCursor
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) == 0 || len(data) > portalCursorMax {
		return cursor, errPortalCursor
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if dec.Decode(&cursor) != nil || !portalCursorText(cursor.RV, 128) || !portalCursorText(cursor.Continue, 1500) || cursor.Limit < 1 || cursor.Limit > portalPageMax {
		return portalCursor{}, errPortalCursor
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return portalCursor{}, errPortalCursor
	}
	return cursor, nil
}

func portalCursorText(value string, max int) bool {
	if value == "" || len(value) > max || strings.ContainsAny(value, " \t\r\n/?#") {
		return false
	}
	return true
}

var errPortalCursor = badRequest("the portal session cursor is malformed")

func encodePortalCursor(cursor portalCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > portalCursorMax {
		return "", errPortalCursor
	}
	return encoded, nil
}

func freshnessRank(value string) int {
	switch value {
	case "observed":
		return 0
	case "stale":
		return 1
	default:
		return 2
	}
}

func portalSummary(s *v1alpha1.AgentSession, now time.Time) apiv1.PortalSessionSummary {
	observed := now.UTC()
	freshness := "unknown"
	if s.Status.Agent != nil && s.Status.Agent.LastHeartbeat != nil {
		beat := s.Status.Agent.LastHeartbeat.Time
		if !beat.After(now) {
			observed = beat.UTC()
			if now.Sub(beat) <= portalFreshFor {
				freshness = "observed"
			} else {
				freshness = "stale"
			}
		}
	}
	return apiv1.PortalSessionSummary{Name: s.Name, UID: string(s.UID), Repo: s.Spec.Repo, Phase: phaseOf(s), ObservedAt: observed, Freshness: freshness}
}

// portalLauncher is apiv1.PortalLauncherPattern; any other spec.parent is omitted.
var portalLauncher = regexp.MustCompile(apiv1.PortalLauncherPattern)

var (
	portalRemoteControlURL   = regexp.MustCompile(apiv1.PortalRemoteControlURLPattern)
	portalRemoteControlState = regexp.MustCompile(apiv1.PortalRemoteControlStatePattern)
)

// portalRemoteControl is status.remoteControl reduced to its state word and a
// link of the exact claude.ai shape. A state outside its shape drops the whole
// entry; a link outside its shape is dropped alone. The entry's name and
// session id are never passed.
func portalRemoteControl(rc *v1alpha1.RemoteControlStatus) *apiv1.PortalRemoteControl {
	if rc == nil || !portalRemoteControlState.MatchString(rc.State) {
		return nil
	}
	out := &apiv1.PortalRemoteControl{State: rc.State}
	if portalRemoteControlURL.MatchString(rc.URL) {
		out.URL = rc.URL
	}
	return out
}

// portalActivity is the DESIGN-029 allowlist for one session. Words outside
// their fixed sets, an unbounded model id and a future activity time are
// Unknown. Usage comes only from sessionUsage, the session's own validated
// task observation; it is not summed with anything.
func portalActivity(s *v1alpha1.AgentSession, now time.Time) *apiv1.PortalSessionActivity {
	a := &apiv1.PortalSessionActivity{Mode: protocol.UsageUnknown, Provider: protocol.UsageUnknown, AgentState: protocol.UsageUnknown, RequestedModel: protocol.UsageUnknown, Usage: portalUsage(sessionUsage(s))}
	if portalLauncher.MatchString(s.Spec.Parent) {
		a.LaunchedBy = s.Spec.Parent
	}
	switch s.Spec.Mode {
	case v1alpha1.ModeTask, v1alpha1.ModeLocal, v1alpha1.ModeRemote:
		a.Mode = string(s.Spec.Mode)
	}
	switch s.Spec.Agent {
	case v1alpha1.AgentClaude, v1alpha1.AgentCodex, v1alpha1.AgentOpencode:
		a.Provider = string(s.Spec.Agent)
	}
	if s.Spec.Mode == v1alpha1.ModeTask {
		a.TaskResult = string(v1alpha1.TaskResultUnknown)
		if r := s.Status.TaskResult; r != nil {
			switch r.State {
			case v1alpha1.TaskResultSucceeded, v1alpha1.TaskResultFailed, v1alpha1.TaskResultTimedOut:
				a.TaskResult = string(r.State)
			}
		}
	}
	a.RemoteControl = portalRemoteControl(s.Status.RemoteControl)
	if protocol.ValidUsageModelID(s.Spec.Model) {
		a.RequestedModel = s.Spec.Model
	}
	if st := s.Status.Agent; st != nil {
		switch st.Status {
		case protocol.AgentPending, protocol.AgentBusy, protocol.AgentIdle, protocol.AgentWaiting, protocol.AgentExited, protocol.AgentFailed, protocol.AgentInterrupted:
			a.AgentState = st.Status
		}
		if st.LastActivity != nil && !st.LastActivity.IsZero() && !st.LastActivity.After(now) {
			at := st.LastActivity.UTC()
			a.LastActivity = &at
		}
	}
	return a
}

// portalConversationUsage is status.conversationUsage reduced to DESIGN-029's
// allowlist, or nil. nil means no key is sent, which the portal reads as not
// measured: it is also what an Unknown, an invalid or a task session's figure
// becomes, never a zero. The stored status is checked again here, as a heartbeat
// checked it, because the portal trusts what the reader sends.
func portalConversationUsage(s *v1alpha1.AgentSession, now time.Time) *apiv1.PortalConversationUsage {
	st := s.Status.ConversationUsage
	if st == nil || (s.Spec.Mode != v1alpha1.ModeLocal && s.Spec.Mode != v1alpha1.ModeRemote) || st.Provider != string(s.Spec.Agent) {
		return nil
	}
	in := protocol.ConversationUsage{Version: st.Version, Provider: st.Provider, ObservedAt: st.ObservedAt.UTC(), Coverage: st.Coverage,
		CounterSemantics: st.CounterSemantics, ResultState: st.ResultState, ReasonCode: st.ReasonCode, Turns: st.Turns, Skipped: st.Skipped}
	for _, m := range st.Models {
		in.Models = append(in.Models, protocol.ConversationModelUsage{Model: m.Model,
			InputTokens: statusTokens(m.InputTokens), OutputTokens: statusTokens(m.OutputTokens),
			CacheReadInputTokens: statusTokens(m.CacheReadInputTokens), CacheCreationInputTokens: statusTokens(m.CacheCreationInputTokens)})
	}
	if in.ResultState == protocol.ConversationUnknown || !in.Valid() {
		return nil
	}
	observed := in.ObservedAt
	if observed.After(now) {
		observed = now.UTC()
	}
	out := &apiv1.PortalConversationUsage{ResultState: in.ResultState, ReasonCode: in.ReasonCode, Turns: in.Turns, Skipped: in.Skipped,
		ObservedAt: observed, Models: make([]apiv1.PortalConversationModelUsage, 0, len(in.Models))}
	for _, m := range in.Models {
		out.Models = append(out.Models, apiv1.PortalConversationModelUsage{Model: m.Model,
			InputTokens: portalTokens(m.InputTokens), OutputTokens: portalTokens(m.OutputTokens),
			CacheReadInputTokens: portalTokens(m.CacheReadInputTokens), CacheCreationInputTokens: portalTokens(m.CacheCreationInputTokens)})
	}
	return out
}

func statusTokens(m v1alpha1.TokenObservationStatus) protocol.TokenObservation {
	t := protocol.TokenObservation{State: m.State}
	if m.Amount != nil {
		amount := *m.Amount
		t.Amount = &amount
	}
	return t
}

func portalUsage(u *protocol.UsageObservation) apiv1.PortalSessionUsage {
	unknown := protocol.TokenObservation{State: protocol.UsageUnknown}
	out := apiv1.PortalSessionUsage{Scope: protocol.UsageUnknown, ResultState: protocol.UsageUnknown, ServedModels: []string{},
		InputTokens: unknown, OutputTokens: unknown, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown,
		Cost: protocol.CostObservation{State: protocol.UsageUnknown, Kind: protocol.UsageUnknown}}
	if u == nil || u.Validate() != nil {
		return out
	}
	out.Scope, out.ResultState, out.Cost = protocol.UsageInitial, u.ResultState, u.Cost
	out.InputTokens, out.OutputTokens = portalTokens(u.InputTokens), portalTokens(u.OutputTokens)
	out.CacheReadInputTokens, out.CacheCreationInputTokens = portalTokens(u.CacheReadInputTokens), portalTokens(u.CacheCreationInputTokens)
	if u.ActualModelState == protocol.UsageProviderReported {
		for _, m := range u.ServedModels {
			out.ServedModels = append(out.ServedModels, m.Model)
		}
	}
	return out
}

// portalTokens copies the amount so the response never aliases session status.
func portalTokens(m protocol.TokenObservation) protocol.TokenObservation {
	if m.Amount == nil {
		return protocol.TokenObservation{State: m.State}
	}
	amount := *m.Amount
	return protocol.TokenObservation{State: m.State, Amount: &amount}
}
