package keeper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/pages"
)

// The summoned-session pager's ledger (DESIGN-064 6.8; story 782). It is the
// ConfigMap dev-env-keeper-summon-pages in the keeper's namespace, precreated
// by GitOps; the keeper gets and updates it by name only. Two keys:
//
//	state.json  the dedupe, the rate window and the members later slices use
//	pages.json  the retained page record (internal/pages), content-free
//
// Both are written in one Update with the read resourceVersion before every
// send, so a conflict or a failed write sends nothing.
const (
	DefaultSummonPagesConfigMap = "dev-env-keeper-summon-pages"
	summonPagesStateKey         = "state.json"
	summonPagesMaxBytes         = 640 << 10
	summonPagesCMBytes          = 960 << 10 // room below Kubernetes' 1 MiB ceiling

	// summonPagesMaxEvents bounds the dedupe. A summoned session lives at most
	// seven days after it ends (ADR-020), and each has a few events.
	summonPagesMaxEvents = 2048
	// summonPagesMaxDrills bounds the drill ids the ledger remembers. A used
	// id is never forgotten; past the bound a new one is refused (F8).
	summonPagesMaxDrills = 16
	summonPagesMaxLines  = 256
	summonPagesMaxListed = 4096
	summonPagesMaxCaller = 256

	// The states of an entry in state.json: the four of internal/pages, and
	// capped, an event past the cap that is never paged (ruling Q5).
	summonStateCapped = "capped"
)

var errSummonPages = errors.New("summon page state unavailable")

// summonPageState is the whole schema, the digest and overflow members
// included (ruling Q11 on item 742): a later slice fills members, never adds
// one, so a rolled-back keeper still reads the ledger. Times are UTC Unix
// seconds; no note text, credential or link is stored.
type summonPageState struct {
	Version int `json:"version"`
	// Events is the dedupe, one entry per page attempt or capped event, keyed
	// "<session UID>/<event>", "drill/<id>" or "more/<n>". A key present is
	// never sent again.
	Events map[string]summonPageEntry `json:"events"`
	// Window holds the reserved times of the page attempts in the last 60
	// minutes: at most six (op782-q-2, a rolling 60 minutes).
	Window []int64 `json:"window"`
	// Capped counts events capped since the last more-events page; More counts
	// the more-events pages reserved, and names the next one.
	Capped int `json:"capped"`
	More   int `json:"more"`
	// Digest is story 742b's (story 792, summon_digest.go). Story 782's keeper
	// writes it empty and keeps it as read.
	Digest summonDigestState `json:"digest"`
	// Overflow is story 742c's, per caller; written empty and kept as read.
	Overflow map[string]summonOverflowEntry `json:"overflow"`
}

type summonPageEntry struct {
	// At is when the entry was written.
	At int64 `json:"at"`
	// State is recorded, sent, failed, ambiguous or capped.
	State string `json:"state"`
	// Session is the session's name (empty for a drill or a summary).
	Session string `json:"session,omitempty"`
}

// summonDigestState is the daily digest's (story 742b, built by story 792):
// Since is the watermark, the pager's first run (only closes and capped
// events at or after it are in a digest, ruling Q6), LastAt when the last
// digest was reserved, Pending the content-free lines waiting for the next
// one, and Listed every Events key a digest attempt has carried, with that
// attempt's time, so each outcome gets one digest attempt, ever. An outcome's
// key is "<session UID>/digest"; a capped event's is its own entry's key.
type summonDigestState struct {
	Since   int64              `json:"since"`
	LastAt  int64              `json:"lastAt"`
	Pending []summonDigestLine `json:"pending"`
	Listed  map[string]int64   `json:"listed"`
}

// summonDigestLine is one digest line, "<session> <lane> <state> <reason>",
// with no note text (ruling Q6).
type summonDigestLine struct {
	Key     string `json:"key"`
	Session string `json:"session"`
	Lane    string `json:"lane"`
	State   string `json:"state"`
	Reason  string `json:"reason"`
	At      int64  `json:"at"`
}

// summonOverflowEntry is the overflow page's per-caller record (story 742c):
// the urgent refusal count it last paged, and when.
type summonOverflowEntry struct {
	LastPagedCount int64 `json:"lastPagedCount"`
	LastPagedAt    int64 `json:"lastPagedAt"`
}

// Every member of state.json but an entry's session is required, present and
// not null (story 782 fix round 1, F5): a missing or null member would decode
// as zero and, for capped or more, lose a count or reuse a summary key. Each
// type decodes its own object, refusing a missing, null or unknown member.
var (
	summonStateMembers    = []string{"version", "events", "window", "capped", "more", "digest", "overflow"}
	summonEntryMembers    = []string{"at", "state"}
	summonDigestMembers   = []string{"since", "lastAt", "pending", "listed"}
	summonLineMembers     = []string{"key", "session", "lane", "state", "reason", "at"}
	summonOverflowMembers = []string{"lastPagedCount", "lastPagedAt"}
)

// decodeSummonObject decodes one JSON object into v (a pointer to a type
// without its own UnmarshalJSON) once every required member is present and
// not null; an unknown member is refused.
func decodeSummonObject(raw []byte, v any, required []string) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return errSummonPages
	}
	for _, name := range required {
		m, ok := members[name]
		if !ok || string(bytes.TrimSpace(m)) == "null" {
			return errSummonPages
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil {
		return errSummonPages
	}
	return nil
}

func (s *summonPageState) UnmarshalJSON(raw []byte) error {
	type plain summonPageState
	return decodeSummonObject(raw, (*plain)(s), summonStateMembers)
}

func (e *summonPageEntry) UnmarshalJSON(raw []byte) error {
	type plain summonPageEntry
	return decodeSummonObject(raw, (*plain)(e), summonEntryMembers)
}

func (d *summonDigestState) UnmarshalJSON(raw []byte) error {
	type plain summonDigestState
	return decodeSummonObject(raw, (*plain)(d), summonDigestMembers)
}

func (l *summonDigestLine) UnmarshalJSON(raw []byte) error {
	type plain summonDigestLine
	return decodeSummonObject(raw, (*plain)(l), summonLineMembers)
}

func (o *summonOverflowEntry) UnmarshalJSON(raw []byte) error {
	type plain summonOverflowEntry
	return decodeSummonObject(raw, (*plain)(o), summonOverflowMembers)
}

func newSummonPageState() summonPageState {
	return summonPageState{Version: 1, Events: map[string]summonPageEntry{}, Window: []int64{},
		Digest: summonDigestState{Pending: []summonDigestLine{}, Listed: map[string]int64{}}, Overflow: map[string]summonOverflowEntry{}}
}

type summonPageLedger struct {
	State summonPageState
	Pages *pages.Document
	cm    *corev1.ConfigMap // resourceVersion from the authoritative read
}

type summonPageStore interface {
	load(context.Context) (*summonPageLedger, error)
	save(context.Context, *summonPageLedger) error
}

type configMapSummonPageStore struct {
	client client.Client // uncached
	name   types.NamespacedName
}

func (s *configMapSummonPageStore) load(ctx context.Context) (*summonPageLedger, error) {
	ctx, cancel := context.WithTimeout(ctx, noticeStoreTimeout)
	defer cancel()
	var cm corev1.ConfigMap
	if err := s.client.Get(ctx, s.name, &cm); err != nil {
		return nil, errSummonPages
	}
	st := newSummonPageState()
	raw := cm.Data[summonPagesStateKey]
	if len(raw) > summonPagesMaxBytes {
		return nil, errSummonPages
	}
	if raw != "" {
		st = summonPageState{} // a missing member or null must not inherit a valid empty ledger
		dec := json.NewDecoder(bytes.NewBufferString(raw))
		dec.DisallowUnknownFields()
		if dec.Decode(&st) != nil {
			return nil, errSummonPages
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			return nil, errSummonPages
		}
	}
	if !st.valid() {
		return nil, errSummonPages
	}
	doc := pages.New()
	if pageRaw := cm.Data[pages.ConfigMapKey]; pageRaw != "" {
		var err error
		if doc, err = pages.Parse([]byte(pageRaw)); err != nil {
			return nil, errSummonPages
		}
	}
	// Never adopt a reset dedupe as a clean slate.
	if len(st.Events) == 0 && len(doc.Records) > 0 {
		return nil, errSummonPages
	}
	return &summonPageLedger{State: st, Pages: doc, cm: &cm}, nil
}

func (s *configMapSummonPageStore) save(ctx context.Context, l *summonPageLedger) error {
	if l == nil || l.cm == nil || !l.State.valid() {
		return errSummonPages
	}
	// Never write what load refuses as a reset dedupe (story 792): an empty
	// dedupe beside page or digest records.
	if len(l.State.Events) == 0 && l.Pages != nil && len(l.Pages.Records) > 0 {
		return errSummonPages
	}
	raw, err := json.Marshal(l.State)
	if err != nil || len(raw) > summonPagesMaxBytes {
		return errSummonPages
	}
	pageRaw, err := l.Pages.Marshal()
	if err != nil || len(raw)+len(pageRaw) > summonPagesCMBytes {
		return errSummonPages
	}
	cm := l.cm.DeepCopy()
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[summonPagesStateKey], cm.Data[pages.ConfigMapKey] = string(raw), string(pageRaw)
	ctx, cancel := context.WithTimeout(ctx, noticeStoreTimeout)
	defer cancel()
	if err := s.client.Update(ctx, cm); err != nil {
		// A conflict and an unconfirmed write both stop the send. A committed
		// reservation stays recorded, and is never sent again.
		return errSummonPages
	}
	l.cm = cm
	return nil
}

var (
	summonEventKey = regexp.MustCompile(`^(?:[0-9A-Za-z][0-9A-Za-z-]{0,63}/[a-z][a-z-]{0,31}|drill/[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?|more/[1-9][0-9]{0,9})$`)
	summonStored   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
)

func summonEntryState(s string) bool {
	switch s {
	case string(pages.StateRecorded), string(pages.StateSent), string(pages.StateFailed), string(pages.StateAmbiguous), summonStateCapped:
		return true
	}
	return false
}

func (s summonPageState) valid() bool {
	if s.Version != 1 || s.Events == nil || s.Window == nil || s.Overflow == nil || s.Digest.Pending == nil || s.Digest.Listed == nil {
		return false
	}
	if len(s.Events) > summonPagesMaxEvents || len(s.Window) > 4*summonPagesCap || s.Capped < 0 || s.Capped > 1<<30 || s.More < 0 || s.More > 1<<30 {
		return false
	}
	for k, e := range s.Events {
		if !summonEventKey.MatchString(k) || e.At <= 0 || !summonEntryState(e.State) || (e.Session != "" && !summonStored.MatchString(e.Session)) {
			return false
		}
	}
	for i, t := range s.Window {
		if t <= 0 || (i > 0 && t < s.Window[i-1]) {
			return false
		}
	}
	d := s.Digest
	if d.Since < 0 || d.LastAt < 0 || len(d.Pending) > summonPagesMaxLines || len(d.Listed) > summonPagesMaxListed {
		return false
	}
	for _, line := range d.Pending {
		if !summonEventKey.MatchString(line.Key) || !summonStored.MatchString(line.Session) || line.At <= 0 ||
			len(line.Lane) > 64 || len(line.State) > 64 || len(line.Reason) > 64 {
			return false
		}
	}
	for k, at := range d.Listed {
		if !summonEventKey.MatchString(k) || at <= 0 {
			return false
		}
	}
	if len(s.Overflow) > summonPagesMaxCaller {
		return false
	}
	for caller, o := range s.Overflow {
		if !summonStored.MatchString(caller) || o.LastPagedCount < 0 || o.LastPagedAt < 0 {
			return false
		}
	}
	return true
}

// room reports whether the rolling 60 minutes have a slot left, with the Ask
// notifier's arithmetic: an attempt counts until an hour after its reserved
// second.
func (s summonPageState) room(now time.Time) bool {
	n := 0
	for _, at := range s.Window {
		if now.Before(time.Unix(at, 0).Add(summonPagesWindow)) {
			n++
		}
	}
	return n < summonPagesCap
}

// reserve records one page attempt at the latest instant it may be sent, as
// the Ask notifier does: a slow save cannot shorten the window. The window is
// pruned against now, never against that later instant (F4): an attempt
// counts until an hour after its reserved second.
func (s *summonPageState) reserve(now, dispatchBy time.Time) {
	kept := s.Window[:0]
	for _, at := range s.Window {
		if now.Before(time.Unix(at, 0).Add(summonPagesWindow)) {
			kept = append(kept, at)
		}
	}
	s.Window = append(kept, askNotificationSecond(dispatchBy))
}

// drills counts the drill ids the ledger remembers.
func (s summonPageState) drills() int {
	n := 0
	for key := range s.Events {
		if strings.HasPrefix(key, "drill/") {
			n++
		}
	}
	return n
}

func cutSummonKey(key string) (owner, event string, ok bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			return key[:i], key[i+1:], true
		}
	}
	return key, "", false
}
