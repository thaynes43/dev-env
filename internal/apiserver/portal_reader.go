package apiserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	var pod corev1.Pod
	if err := s.get(ctx, types.NamespacedName{Namespace: portalReaderNamespace, Name: id.PodName}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, forbidden("the portal reader's pod is gone")
		}
		return nil, fromKubeError(err, "the portal reader's pod")
	}
	if string(pod.UID) != id.PodUID || pod.Namespace != portalReaderNamespace || pod.Spec.ServiceAccountName != portalReaderAccount {
		return nil, forbidden("the portal reader token is not bound to its current pod")
	}
	return &caller{kind: kindPortalReader, identity: id, parent: portalReaderRef, pod: &pod}, nil
}

func (s *Server) listPortalSessions(ctx context.Context, r *http.Request) (int, any, error) {
	if s.Policy.SessionNamespace != portalReaderNamespace {
		return 0, nil, forbidden("the portal reader is not serving the dev-agents session namespace")
	}
	limit, cursor, err := portalPageQuery(r.URL.Query())
	if err != nil {
		return 0, nil, err
	}
	var list v1alpha1.AgentSessionList
	if err := s.Live.List(ctx, &list, client.InNamespace(portalReaderNamespace)); err != nil {
		return 0, nil, fromKubeError(err, "list sessions")
	}
	items := make([]v1alpha1.AgentSession, 0, len(list.Items))
	for _, item := range list.Items {
		if item.Namespace == portalReaderNamespace && item.Spec.Repo == portalSessionRepo {
			items = append(items, item)
		}
	}
	sortPortalSessions(items)
	now := s.now()
	page := apiv1.PortalSessionPage{Sessions: []apiv1.PortalSessionSummary{}, Limit: limit, ObservedAt: now, Freshness: "observed"}
	started := cursor == nil
	for i := range items {
		if !started {
			if samePortalCursor(portalPosition(&items[i]), *cursor) {
				started = true
			}
			continue
		}
		if len(page.Sessions) == limit {
			page.Complete = false
			page.NextCursor = encodePortalCursor(portalPosition(&items[i-1]))
			return http.StatusOK, page, nil
		}
		summary := portalSummary(&items[i], now)
		if summary.Freshness == "unknown" || (page.Freshness == "observed" && summary.Freshness != "observed") {
			page.Freshness = summary.Freshness
		}
		page.Sessions = append(page.Sessions, summary)
	}
	if !started {
		return 0, nil, badRequest("the portal session cursor is not in this repository")
	}
	page.Complete = true
	return http.StatusOK, page, nil
}

type portalCursor struct {
	Created time.Time `json:"created"`
	Name    string    `json:"name"`
}

func portalPageQuery(q map[string][]string) (int, *portalCursor, error) {
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
	if len(raw) == 0 || len(raw) > 256 || strings.ContainsAny(raw, "/?#") {
		return cursor, errPortalCursor
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) > 256 {
		return cursor, errPortalCursor
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if dec.Decode(&cursor) != nil || cursor.Created.IsZero() || len(validation.IsDNS1123Label(cursor.Name)) != 0 {
		return portalCursor{}, errPortalCursor
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return portalCursor{}, errPortalCursor
	}
	cursor.Created = cursor.Created.UTC()
	return cursor, nil
}

func samePortalCursor(a, b portalCursor) bool {
	return a.Name == b.Name && a.Created.UTC().Equal(b.Created.UTC())
}

var errPortalCursor = badRequest("the portal session cursor is malformed")

func encodePortalCursor(cursor portalCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func portalPosition(s *v1alpha1.AgentSession) portalCursor {
	return portalCursor{Created: s.CreationTimestamp.Time.UTC(), Name: s.Name}
}

func sortPortalSessions(items []v1alpha1.AgentSession) {
	for i := 1; i < len(items); i++ {
		item := items[i]
		j := i
		for j > 0 && portalBefore(&item, &items[j-1]) {
			items[j] = items[j-1]
			j--
		}
		items[j] = item
	}
}

func portalBefore(a, b *v1alpha1.AgentSession) bool {
	if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
		return c > 0
	}
	return a.Name < b.Name
}

func portalSummary(s *v1alpha1.AgentSession, now time.Time) apiv1.PortalSessionSummary {
	observed := s.CreationTimestamp.Time
	for _, condition := range s.Status.Conditions {
		if condition.LastTransitionTime.After(observed) {
			observed = condition.LastTransitionTime.Time
		}
	}
	freshness := "unknown"
	phase := phaseOf(s)
	if s.Status.Phase != "" {
		if now.Sub(observed) <= portalFreshFor {
			freshness = "observed"
		} else {
			freshness = "stale"
		}
	}
	return apiv1.PortalSessionSummary{Name: s.Name, UID: string(s.UID), Repo: portalSessionRepo, Phase: phase, ObservedAt: observed.UTC(), Freshness: freshness}
}
