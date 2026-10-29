package keeper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/utils/clock"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/pages"
)

const (
	DefaultAskNotificationConfigMap = "dev-env-keeper-ask-notifications"
	askNoticeText                   = "An agent needs your answer"
	askNoticeKind                   = "ask-new"
	askNoticePath                   = "/portal/projects/dev-env/environments/internal/asks"
	askNotifyEvery                  = 30 * time.Second
	askRequesterInterval            = 5 * time.Minute
	askRecipientInterval            = time.Hour
	askRecipientCap                 = 6
	askNotifyMaxIDs                 = 4096
)

var errAskNotifications = errors.New("ask notifications unavailable")

// AskNotificationOptions is independent of login notice windows. Empty
// QuietHours permits all hours; a configured window is the ALLOWED local
// delivery window. Q-255-02 still holds activation, even with source present.
type AskNotificationOptions struct {
	Enabled        bool
	PortalOrigin   string
	QuietHours     string // e.g. 09:00-21:00, empty by default
	Timezone       string // America/New_York by default
	StateConfigMap string
}

func (o AskNotificationOptions) Validate() error {
	if !o.Enabled {
		return nil
	}
	u, err := url.Parse(o.PortalOrigin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(o.PortalOrigin != "https://"+u.Host && o.PortalOrigin != "https://"+u.Host+"/") || len(o.PortalOrigin) > 300 {
		return errors.New("ask notifications require a configured HTTPS portal origin without credentials, path, query or fragment")
	}
	if _, err := o.window(); err != nil {
		return err
	}
	return nil
}

type askNoticeWindow struct {
	zone       *time.Location
	start, end time.Duration
	allHours   bool
}

func (o AskNotificationOptions) window() (askNoticeWindow, error) {
	zone := o.Timezone
	if zone == "" {
		zone = DefaultNoticeZone
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return askNoticeWindow{}, errors.New("ask notification timezone is unknown")
	}
	w := askNoticeWindow{zone: loc, allHours: o.QuietHours == ""}
	if w.allHours {
		return w, nil
	}
	parts := strings.Split(o.QuietHours, "-")
	if len(parts) != 2 {
		return w, errors.New("ask notification window must be HH:MM-HH:MM")
	}
	w.start, err = ParseClockOfDay(parts[0])
	if err != nil {
		return w, errors.New("ask notification window start must be HH:MM")
	}
	w.end, err = ParseClockOfDay(parts[1])
	if err != nil || w.start == w.end {
		return w, errors.New("ask notification window end must be HH:MM and differ from its start")
	}
	return w, nil
}

func (w askNoticeWindow) allows(now time.Time) bool {
	if w.allHours {
		return true
	}
	t := now.In(w.zone)
	d := time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
	if w.start < w.end {
		return d >= w.start && d < w.end
	}
	return d >= w.start || d < w.end // a window crossing midnight
}

// next returns the first eligible instant, checking real minutes across DST.
// A skipped local start defers to the first real minute in the window; a
// repeated local minute is eligible in either occurrence. Rate caps use UTC.
func (w askNoticeWindow) next(now time.Time) time.Time {
	if w.allows(now) {
		return now
	}
	t := now.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 72*60; i++ { // bounded calendar arithmetic, at most three days
		if w.allows(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return t
}

type askNotificationReader interface {
	ListNamespace(context.Context) ([]apiv1.Ask, bool, error)
	Get(context.Context, string) (apiv1.Ask, error)
}

// askNotifier is a leader-only runnable. Each tick rereads durable state.
// No send occurs without a confirmed write-ahead CAS of dedupe, rate and page
// records. A recorded attempt with no outcome is Unknown and never replayed.
type askNotifier struct {
	reader askNotificationReader
	store  askNotificationStore
	sender NoticeSender
	clock  clock.Clock
	log    logr.Logger
	origin string
	window askNoticeWindow
}

func newAskNotifier(o AskNotificationOptions, reader askNotificationReader, store askNotificationStore, sender NoticeSender, clk clock.Clock, log logr.Logger) (*askNotifier, error) {
	if !o.Enabled {
		return nil, nil
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if reader == nil || store == nil || sender == nil {
		return nil, errAskNotifications
	}
	w, _ := o.window()
	if clk == nil {
		clk = clock.RealClock{}
	}
	if log.GetSink() == nil {
		log = logr.Discard()
	}
	return &askNotifier{reader: reader, store: store, sender: sender, clock: clk, log: log, origin: strings.TrimSuffix(o.PortalOrigin, "/"), window: w}, nil
}

func (*askNotifier) NeedLeaderElection() bool { return true }

func (n *askNotifier) Start(ctx context.Context) error {
	for {
		n.tick(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-n.clock.After(askNotifyEvery):
		}
	}
}

func askRequesterID(a apiv1.Ask) string {
	d := sha256.Sum256([]byte(a.Context.SessionUID))
	return hex.EncodeToString(d[:])
}

func (n *askNotifier) tick(ctx context.Context) {
	now := n.clock.Now()
	if n.window.next(now).After(now) {
		return // defer; no persisted intent or cap consumed outside the window
	}
	ledger, err := n.store.load(ctx)
	if err != nil {
		n.log.Error(nil, "ask notification state unavailable")
		return
	}
	asks, incomplete, err := n.reader.ListNamespace(ctx)
	if err != nil || incomplete || len(asks) > askNotifyMaxIDs {
		n.log.Error(nil, "ask notification scan unavailable")
		return
	}
	sort.Slice(asks, func(i, j int) bool {
		if !asks[i].CreatedAt.Equal(asks[j].CreatedAt) {
			return asks[i].CreatedAt.Before(asks[j].CreatedAt)
		}
		return asks[i].ID < asks[j].ID
	})
	live := make(map[string]bool, len(asks))
	for _, a := range asks {
		live[a.ID] = true
	}
	ledger.prune(live, now)
	for _, a := range asks {
		now = n.clock.Now()
		if ctx.Err() != nil || !n.window.allows(now) || a.EffectiveState(now) != apiv1.AskStateOpen {
			continue
		}
		groupID, seen := ledger.State.Seen[a.ID]
		if seen {
			g := ledger.State.Groups[groupID]
			if groupID != a.ID || len(g.Times) != 1 || g.RetryAt == 0 || now.Unix() < g.RetryAt {
				continue
			}
		} else {
			groupID = a.ID
		}
		requester := askRequesterID(a)
		if !ledger.State.room(requester, now) {
			continue // pending observations coalesce into the next eligible push
		}
		// Recheck selected Ask immediately before reserving the send. Terminal
		// or expired records never cause a retry. The keeper never writes Asks.
		fresh, err := n.reader.Get(ctx, a.ID)
		now = n.clock.Now()
		if err != nil || fresh.EffectiveState(now) != apiv1.AskStateOpen || !n.window.allows(now) || !ledger.State.room(requester, now) {
			continue
		}
		if !seen {
			// One push links the oldest pending Ask and coalesces all pending
			// open Asks from the same original requester in this snapshot.
			var batch []string
			for _, pending := range asks {
				if _, exists := ledger.State.Seen[pending.ID]; !exists && askRequesterID(pending) == requester && pending.EffectiveState(now) == apiv1.AskStateOpen {
					batch = append(batch, pending.ID)
				}
			}
			if len(ledger.State.Seen)+len(batch) > askNotifyMaxIDs {
				n.log.Error(nil, "ask notification dedupe capacity reached")
				return
			}
			for _, id := range batch {
				ledger.State.Seen[id] = groupID
			}
		}
		g := ledger.State.Groups[groupID]
		g.Requester = requester
		// Reserve the latest permitted dispatch instant, not an earlier read
		// time. Saving is bounded to five seconds; a late save cannot send.
		// Counting this conservative horizon keeps actual dispatches at least
		// five minutes apart even when the ConfigMap write is slow.
		dispatchBy := now.Add(noticeStoreTimeout)
		g.Times = append(g.Times, askNotificationSecond(dispatchBy))
		g.RetryAt = 0 // write ahead: an interrupted attempt is never replayed
		ledger.State.Groups[groupID] = g
		key := askNoticeKind + ":" + groupID
		if _, err := ledger.Pages.Begin(pages.Page{Key: key, Correlation: a.ID, Sender: "dev-env-keeper", Source: askNoticeKind}, now); err != nil {
			return
		}
		if err := n.store.save(ctx, ledger); err != nil {
			n.log.Error(nil, "ask notification reservation unconfirmed", "id", a.ID)
			return
		}
		outcome, status, retry := pages.OutcomeFailed, 0, true
		if at := n.clock.Now(); !at.After(dispatchBy) && n.window.allows(at) && fresh.EffectiveState(at) == apiv1.AskStateOpen {
			outcome, status, retry = n.send(ctx, a.ID)
		}
		ledger.Pages.Finish(key, outcome, status, "", n.clock.Now())
		if retry && len(g.Times) == 1 {
			g.RetryAt = max(askNotificationSecond(n.clock.Now().Add(askRequesterInterval)), g.Times[0]+int64(askRequesterInterval/time.Second))
			ledger.State.Groups[groupID] = g
		}
		if err := n.store.save(ctx, ledger); err != nil {
			n.log.Error(nil, "ask notification outcome unconfirmed", "id", a.ID)
			return
		}
	}
}

// Round reservations up so subsecond send times cannot shorten a rate window.
func askNotificationSecond(t time.Time) int64 {
	second := t.Unix()
	if t.Nanosecond() != 0 {
		second++
	}
	return second
}

func (n *askNotifier) send(ctx context.Context, id string) (pages.Outcome, int, bool) {
	notice := Notice{Title: askNoticeText, Message: askNoticeText, Priority: 0,
		URL: n.origin + askNoticePath + "?ask=" + id, URLTitle: "Open Asks"}
	var rc Receipt
	var err error
	if sender, ok := n.sender.(ReceiptSender); ok {
		rc, err = sender.SendReceipt(ctx, notice)
	} else {
		err = n.sender.Send(ctx, notice)
		rc.Confirmed = err == nil
	}
	var refused *SendError
	switch {
	case err == nil && rc.Confirmed:
		return pages.OutcomeSent, rc.Status, false
	case errors.Is(err, errPushoverCredentials):
		return pages.OutcomeFailed, 0, true // definitely no HTTP request; one retry
	case errors.As(err, &refused) && refused.Status >= 400 && refused.Status < 500:
		return pages.OutcomeFailed, refused.Status, false
	case errors.As(err, &refused):
		return pages.OutcomeAmbiguous, refused.Status, false
	default:
		return pages.OutcomeAmbiguous, rc.Status, false
	}
}
