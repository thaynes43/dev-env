package keeper

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/pages"
)

const (
	askNotificationStateKey = "state.json"
	askNotificationMaxBytes = 896 << 10
	askNotificationCMBytes  = 960 << 10 // room below Kubernetes' 1 MiB ceiling
)

// IDs and UTC Unix-second timestamps only. The requester ID is a SHA-256 of
// the original Session UID, never a name, principal, credential or Ask prose.
// Seen maps each coalesced Ask ID to its representative group/Ask ID.
type askNotificationState struct {
	Version int                               `json:"version"`
	Seen    map[string]string                 `json:"seen"`
	Groups  map[string]askNotificationAttempt `json:"groups"`
}

type askNotificationAttempt struct {
	Requester string  `json:"r"`
	Times     []int64 `json:"t"` // one reservation, at most one pre-send retry
	RetryAt   int64   `json:"retry,omitempty"`
}

type askNotificationLedger struct {
	State askNotificationState
	Pages *pages.Document
	cm    *corev1.ConfigMap // resourceVersion from the authoritative read
}

type askNotificationStore interface {
	load(context.Context) (*askNotificationLedger, error)
	save(context.Context, *askNotificationLedger) error
}

// The ConfigMap must be precreated by GitOps. The keeper gets get/update on
// this exact name, no create/list; state and page write-ahead share one CAS.
type configMapAskNotificationStore struct {
	client client.Client // uncached
	name   types.NamespacedName
}

func (s *configMapAskNotificationStore) load(ctx context.Context) (*askNotificationLedger, error) {
	ctx, cancel := context.WithTimeout(ctx, noticeStoreTimeout)
	defer cancel()
	var cm corev1.ConfigMap
	if err := s.client.Get(ctx, s.name, &cm); err != nil {
		return nil, errAskNotifications
	}
	st := askNotificationState{Version: 1, Seen: map[string]string{}, Groups: map[string]askNotificationAttempt{}}
	raw := cm.Data[askNotificationStateKey]
	if len(raw) > askNotificationMaxBytes {
		return nil, errAskNotifications
	}
	if raw != "" {
		st = askNotificationState{} // missing fields/null must not inherit a valid empty ledger
		dec := json.NewDecoder(bytes.NewBufferString(raw))
		dec.DisallowUnknownFields()
		if dec.Decode(&st) != nil {
			return nil, errAskNotifications
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			return nil, errAskNotifications
		}
	}
	if !st.valid() {
		return nil, errAskNotifications
	}
	doc := pages.New()
	pageRaw := cm.Data[pages.ConfigMapKey]
	if pageRaw != "" {
		var err error
		doc, err = pages.Parse([]byte(pageRaw))
		if err != nil {
			return nil, errAskNotifications
		}
	}
	// Never adopt a partial/reset dedupe record as a clean slate.
	if len(st.Seen) == 0 && len(doc.Records) > 0 {
		return nil, errAskNotifications
	}
	return &askNotificationLedger{State: st, Pages: doc, cm: &cm}, nil
}

func (s *configMapAskNotificationStore) save(ctx context.Context, ledger *askNotificationLedger) error {
	if !ledger.State.valid() || ledger.cm == nil {
		return errAskNotifications
	}
	raw, err := json.Marshal(ledger.State)
	if err != nil || len(raw) > askNotificationMaxBytes {
		return errAskNotifications
	}
	pageRaw, err := ledger.Pages.Marshal()
	if err != nil || len(raw)+len(pageRaw) > askNotificationCMBytes {
		return errAskNotifications
	}
	cm := ledger.cm.DeepCopy()
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[askNotificationStateKey], cm.Data[pages.ConfigMapKey] = string(raw), string(pageRaw)
	ctx, cancel := context.WithTimeout(ctx, noticeStoreTimeout)
	defer cancel()
	if err := s.client.Update(ctx, cm); err != nil {
		// Conflict and unconfirmed transport errors both prevent a send. A
		// committed write-ahead reservation remains Unknown after restart.
		return errAskNotifications
	}
	ledger.cm = cm
	return nil
}

func (s askNotificationState) valid() bool {
	if s.Version != 1 || s.Seen == nil || s.Groups == nil || len(s.Seen) > askNotifyMaxIDs || len(s.Groups) > askNotifyMaxIDs {
		return false
	}
	refs := make(map[string]bool, len(s.Groups))
	for id, group := range s.Seen {
		if !apiv1.ValidAskID(id) {
			return false
		}
		if _, ok := s.Groups[group]; !ok {
			return false
		}
		refs[group] = true
	}
	for id, g := range s.Groups {
		if !refs[id] || !apiv1.ValidAskID(id) || len(g.Requester) != 64 || len(g.Times) < 1 || len(g.Times) > 2 || g.RetryAt < 0 {
			return false
		}
		if _, err := hex.DecodeString(g.Requester); err != nil {
			return false
		}
		for i, t := range g.Times {
			if t <= 0 || (i > 0 && t <= g.Times[i-1]) {
				return false
			}
		}
		if g.RetryAt != 0 && (len(g.Times) != 1 || g.RetryAt < g.Times[0]+int64(askRequesterInterval/time.Second)) {
			return false
		}
	}
	return true
}

func (s askNotificationState) room(requester string, now time.Time) bool {
	count := 0
	for _, g := range s.Groups {
		for _, at := range g.Times {
			t := time.Unix(at, 0)
			if g.Requester == requester && now.Before(t.Add(askRequesterInterval)) {
				return false
			}
			if now.Before(t.Add(askRecipientInterval)) {
				count++
			}
		}
	}
	return count < askRecipientCap
}

func (l *askNotificationLedger) prune(live map[string]bool, now time.Time) {
	for id, group := range l.State.Seen {
		g := l.State.Groups[group]
		if !live[id] && !now.Before(time.Unix(g.Times[len(g.Times)-1], 0).Add(askRecipientInterval)) {
			delete(l.State.Seen, id)
		}
	}
	retained := map[string]bool{}
	for _, group := range l.State.Seen {
		retained[group] = true
	}
	for group := range l.State.Groups {
		if !retained[group] {
			delete(l.State.Groups, group)
		}
	}
}
