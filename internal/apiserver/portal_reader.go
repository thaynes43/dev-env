package apiserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	portalReaderNamespace = "dev-agents"
	portalReaderAccount   = "dev-env-v2-portal"
	portalReaderRef       = portalReaderNamespace + "/" + portalReaderAccount
	portalSessionRepo     = "dev-env"
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
	limit, cursor, err := portalPageQuery(r.URL.RawQuery)
	if err != nil {
		return 0, nil, err
	}
	if cursor != nil && cursor.Limit != limit {
		return 0, nil, badRequest("the portal session cursor does not match this limit")
	}
	opts := &client.ListOptions{Namespace: portalReaderNamespace, Limit: int64(limit)}
	if cursor != nil {
		opts.Continue = cursor.Continue
		opts.Raw = &metav1.ListOptions{ResourceVersion: cursor.RV}
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
		if item.Namespace != portalReaderNamespace || item.Spec.Repo != portalSessionRepo {
			continue
		}
		summary := portalSummary(item, now)
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

func portalPageQuery(raw string) (int, *portalCursor, error) {
	q, err := url.ParseQuery(raw)
	if err != nil || strings.Contains(raw, ";") {
		return 0, nil, badRequest("the portal session query is not canonical")
	}
	limit := portalPageDefault
	var cursor *portalCursor
	for key, values := range q {
		if len(values) != 1 {
			return 0, nil, badRequest("query parameter %s is repeated", key)
		}
		switch key {
		case "limit":
			n, err := strconv.Atoi(values[0])
			if err != nil || n < 1 || n > portalPageMax || strconv.Itoa(n) != values[0] {
				return 0, nil, badRequest("limit must be from 1 to %d", portalPageMax)
			}
			limit = n
		case "cursor":
			decoded, err := decodePortalCursor(values[0])
			if err != nil {
				return 0, nil, badRequest("the portal session cursor is malformed")
			}
			cursor = &decoded
		default:
			return 0, nil, badRequest("the portal reader does not accept %q", key)
		}
	}
	return limit, cursor, nil
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
	return apiv1.PortalSessionSummary{Name: s.Name, UID: string(s.UID), Repo: portalSessionRepo, Phase: phaseOf(s), ObservedAt: observed, Freshness: freshness}
}
