package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the image may carry no zoneinfo; the notice window needs a zone

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The keeper's notices (plan 13, story 13.1). The keeper sends straight to the
// dedicated "dev-env" Pushover application, not through Alertmanager:
//
//   - a warning from 7 days before the Claude login lapses (priority 0): at most
//     one per calendar day, only inside the daytime window;
//   - a critical notice (priority 1) when a provider reaches NeedsLogin, any
//     hour, once per incident, and one "resolved" notice (priority 0) when it
//     leaves NeedsLogin.
//
// The Pushover token and user key are read from files on every tick (the
// ExternalSecret's mount), held only for the request, and appear in no log, error,
// metric or notice. Deduplication state is a small ConfigMap of times and incident
// ids; a restart reads it back and repeats nothing.
const (
	// DefaultPushoverURL is Pushover's message endpoint. The token travels in
	// the form body, never the URL.
	DefaultPushoverURL = "https://api.pushover.net/1/messages.json"
	// DefaultPushoverTokenFile and DefaultPushoverUserFile sit in one mount
	// directory, so GitOps mounts the Secret without new arguments.
	DefaultPushoverTokenFile = "/etc/dev-env-keeper/pushover/token"
	DefaultPushoverUserFile  = "/etc/dev-env-keeper/pushover/user"
	// DefaultNoticeStateConfigMap is pre-created by GitOps in the keeper's namespace.
	DefaultNoticeStateConfigMap = "dev-env-keeper-notices"
	noticeStateKey              = "state.json"

	DefaultNoticeZone        = "America/New_York"
	DefaultNoticeWindowStart = "09:00"
	DefaultNoticeWindowEnd   = "21:00"

	// noticeWarnWithin: the warning starts this long before the lapse.
	noticeWarnWithin = 7 * 24 * time.Hour
	noticeTick       = time.Minute
	// noticeBackoff: after a failed send nothing is sent for this long.
	noticeBackoff     = 5 * time.Minute
	noticeHTTPTimeout = 10 * time.Second
	// noticeLapseSlack: a later lapse date resets the warning only when it is
	// later than the stored one by more than this, so a provider that moves the
	// date by seconds on a refresh cannot cause a second warning in a day.
	noticeLapseSlack = time.Hour

	// NoticeLinkURL is the session list where the standby appears on the phone.
	// The keeper's RBAC reads one AgentSession by name and cannot find the
	// standby's own link, so every notice links the list.
	NoticeLinkURL   = "https://claude.ai/code"
	NoticeLinkTitle = "Open the dev-env standby"
)

var (
	errPushoverCredentials = errors.New("pushover credentials are not available")
	errPushoverSend        = errors.New("pushover request failed")
	errNoticeState         = errors.New("notice state is unavailable")
)

// Notice is one Pushover message.
type Notice struct {
	Title, Message string
	Priority       int
	URL, URLTitle  string
}

// NoticeSender sends one notice. Errors never carry the request or the credentials.
type NoticeSender interface {
	Send(ctx context.Context, n Notice) error
}

// PushoverSender posts to Pushover, reading the credential files on each send.
type PushoverSender struct {
	URL                 string
	TokenFile, UserFile string
	Client              *http.Client
}

// PushoverCredentialsPresent reports whether both files exist and hold a
// non-empty value. It reads and discards them.
func PushoverCredentialsPresent(tokenFile, userFile string) bool {
	_, _, ok := readPushoverCredentials(tokenFile, userFile)
	return ok
}

func readPushoverCredentials(tokenFile, userFile string) (token, user string, ok bool) {
	read := func(path string) string {
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > 4<<10 {
			return ""
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(raw))
	}
	token, user = read(tokenFile), read(userFile)
	return token, user, token != "" && user != ""
}

func (p *PushoverSender) Send(ctx context.Context, n Notice) error {
	token, user, ok := readPushoverCredentials(p.TokenFile, p.UserFile)
	if !ok {
		return errPushoverCredentials
	}
	form := url.Values{
		"token": {token}, "user": {user}, "title": {n.Title}, "message": {n.Message},
		"priority": {strconv.Itoa(n.Priority)},
	}
	if n.URL != "" {
		form.Set("url", n.URL)
		form.Set("url_title", n.URLTitle)
	}
	target := p.URL
	if target == "" {
		target = DefaultPushoverURL
	}
	c := p.Client
	if c == nil {
		c = &http.Client{Timeout: noticeHTTPTimeout}
	}
	// Never follow a redirect: the body carries the credentials.
	noRedirect := *c
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx, cancel := context.WithTimeout(ctx, noticeHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return errPushoverSend
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirect.Do(req)
	if err != nil {
		// A transport error names the URL, which holds no secret, but nothing
		// else about the request; a fixed text keeps it that way.
		return errPushoverSend
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Pushover's error body can repeat the request fields; it is not read.
		return fmt.Errorf("%w: status %d", errPushoverSend, resp.StatusCode)
	}
	return nil
}

// NoticeState is the deduplication record: times and incident ids, no secret.
type NoticeState struct {
	Version   int                       `json:"version"`
	Warnings  map[string]warnRecord     `json:"warnings,omitempty"`
	Incidents map[string]incidentRecord `json:"incidents,omitempty"`
}

type warnRecord struct {
	// Day is the local calendar day of the last warning ("2006-01-02").
	Day string `json:"day"`
	// Lapse is the lapse date that warning was about (Unix seconds).
	Lapse int64 `json:"lapse"`
}

type incidentRecord struct {
	ID       string `json:"id"`
	Opened   int64  `json:"opened"`
	Notified bool   `json:"notified"`
}

// NoticeStore keeps the NoticeState across restarts.
type NoticeStore interface {
	Load(ctx context.Context) (NoticeState, error)
	Save(ctx context.Context, s NoticeState) error
}

// ConfigMapNoticeStore keeps the state in the one ConfigMap GitOps created.
// Its Role grants get, update and patch on that name only: no create, no list.
type ConfigMapNoticeStore struct {
	Client client.Client
	Name   types.NamespacedName
}

func marshalState(s NoticeState) ([]byte, error) { return json.Marshal(s) }

func unmarshalState(raw []byte) (NoticeState, error) {
	st := NoticeState{Version: 1}
	if err := json.Unmarshal(raw, &st); err != nil || st.Version != 1 {
		return NoticeState{}, errNoticeState
	}
	return st, nil
}

func (c *ConfigMapNoticeStore) Load(ctx context.Context) (NoticeState, error) {
	var cm corev1.ConfigMap
	if err := c.Client.Get(ctx, c.Name, &cm); err != nil {
		return NoticeState{}, errNoticeState
	}
	if raw := cm.Data[noticeStateKey]; raw != "" {
		return unmarshalState([]byte(raw))
	}
	return NoticeState{Version: 1}, nil
}

func (c *ConfigMapNoticeStore) Save(ctx context.Context, s NoticeState) error {
	raw, err := marshalState(s)
	if err != nil {
		return errNoticeState
	}
	var cm corev1.ConfigMap
	if err := c.Client.Get(ctx, c.Name, &cm); err != nil {
		return errNoticeState
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[noticeStateKey] = string(raw)
	// Update carries the resourceVersion read above, so it is a compare-and-swap.
	if c.Client.Update(ctx, &cm) != nil {
		return errNoticeState
	}
	return nil
}

// NoticeOptions configure the notifier.
type NoticeOptions struct {
	TokenFile, UserFile string
	URL                 string // the Pushover endpoint; DefaultPushoverURL when empty
	Zone                string
	WindowStart         string // "09:00"
	WindowEnd           string // "21:00"
	StateConfigMap      string
}

func (o NoticeOptions) withDefaults() NoticeOptions {
	if o.TokenFile == "" {
		o.TokenFile = DefaultPushoverTokenFile
	}
	if o.UserFile == "" {
		o.UserFile = DefaultPushoverUserFile
	}
	if o.URL == "" {
		o.URL = DefaultPushoverURL
	}
	if o.Zone == "" {
		o.Zone = DefaultNoticeZone
	}
	if o.WindowStart == "" {
		o.WindowStart = DefaultNoticeWindowStart
	}
	if o.WindowEnd == "" {
		o.WindowEnd = DefaultNoticeWindowEnd
	}
	if o.StateConfigMap == "" {
		o.StateConfigMap = DefaultNoticeStateConfigMap
	}
	return o
}

// ParseClockOfDay reads "HH:MM" as the time since midnight.
func ParseClockOfDay(s string) (time.Duration, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

// Validate checks the window and zone flags.
func (o NoticeOptions) Validate() error {
	o = o.withDefaults()
	if _, err := time.LoadLocation(o.Zone); err != nil {
		return fmt.Errorf("notice timezone %q is unknown", o.Zone)
	}
	start, err := ParseClockOfDay(o.WindowStart)
	if err != nil {
		return fmt.Errorf("notice window start: %w", err)
	}
	end, err := ParseClockOfDay(o.WindowEnd)
	if err != nil {
		return fmt.Errorf("notice window end: %w", err)
	}
	if start >= end {
		return errors.New("notice window start must be before its end")
	}
	if !strings.HasPrefix(o.URL, "https://") {
		return errors.New("the Pushover URL must be HTTPS")
	}
	return nil
}

// Notifier turns the hub's observations into notices. It is a leader-only
// runnable; a restart rereads the state and repeats nothing.
type Notifier struct {
	Hub    *LoginHub
	Store  NoticeStore
	Sender NoticeSender
	// Enabled reports whether credentials exist right now. It is checked every
	// tick, so credentials that arrive later switch the notifier on.
	Enabled     func() bool
	Clock       clock.Clock
	Log         logr.Logger
	Zone        *time.Location
	WindowStart time.Duration // since local midnight
	WindowEnd   time.Duration

	mu           sync.Mutex
	state        *NoticeState // authoritative after the first load
	dirty        bool         // a change not yet stored
	backoffUntil time.Time
	logged       string // last enabled/disabled line logged
}

// NewNotifier builds a Notifier from validated options.
func NewNotifier(o NoticeOptions, hub *LoginHub, store NoticeStore, sender NoticeSender, clk clock.Clock, log logr.Logger) (*Notifier, error) {
	o = o.withDefaults()
	if err := o.Validate(); err != nil {
		return nil, err
	}
	zone, _ := time.LoadLocation(o.Zone)
	start, _ := ParseClockOfDay(o.WindowStart)
	end, _ := ParseClockOfDay(o.WindowEnd)
	if clk == nil {
		clk = clock.RealClock{}
	}
	return &Notifier{
		Hub: hub, Store: store, Sender: sender, Clock: clk, Log: log, Zone: zone, WindowStart: start, WindowEnd: end,
		Enabled: func() bool { return PushoverCredentialsPresent(o.TokenFile, o.UserFile) },
	}, nil
}

func (*Notifier) NeedLeaderElection() bool { return true }

// Start ticks until ctx ends.
func (n *Notifier) Start(ctx context.Context) error {
	for ctx.Err() == nil {
		n.Tick(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-n.Clock.After(noticeTick):
		}
	}
	return nil
}

func (n *Notifier) logOnce(line string) {
	if n.logged != line {
		n.Log.Info(line)
		n.logged = line
	}
}

// inWindow reports whether local time is in [WindowStart, WindowEnd).
func (n *Notifier) inWindow(local time.Time) bool {
	y, m, d := local.Date()
	since := local.Sub(time.Date(y, m, d, 0, 0, 0, 0, local.Location()))
	return since >= n.WindowStart && since < n.WindowEnd
}

// Tick runs one pass. Exposed for tests with a fake clock.
func (n *Notifier) Tick(ctx context.Context) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.Enabled == nil || !n.Enabled() {
		n.logOnce("notifier disabled: no Pushover credentials")
		return
	}
	n.logOnce("notifier enabled")
	if n.state == nil {
		st, err := n.Store.Load(ctx)
		if err != nil {
			// Without the stored state a send could repeat a notice: do nothing.
			n.logOnce("notifier waiting: notice state is unavailable")
			return
		}
		if st.Warnings == nil {
			st.Warnings = map[string]warnRecord{}
		}
		if st.Incidents == nil {
			st.Incidents = map[string]incidentRecord{}
		}
		st.Version = 1
		n.state = &st
	}
	now := n.Clock.Now()
	snap := n.Hub.Snapshot()
	providers := make([]string, 0, len(snap))
	for p := range snap {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	for _, p := range providers {
		n.incidents(ctx, p, snap[p], now)
		n.warning(ctx, p, snap[p], now)
	}
	n.persist(ctx)
}

// persist stores a pending change. A failure leaves it pending: the next tick
// stores it again, and nothing is sent twice meanwhile because the in-memory
// state already records what was sent.
func (n *Notifier) persist(ctx context.Context) {
	if !n.dirty {
		return
	}
	if err := n.Store.Save(ctx, *n.state); err != nil {
		n.Log.Info("notice state not stored yet; it is retried on the next pass")
		return
	}
	n.dirty = false
}

func (n *Notifier) send(ctx context.Context, notice Notice, now time.Time) bool {
	if now.Before(n.backoffUntil) {
		return false
	}
	notice.URL, notice.URLTitle = NoticeLinkURL, NoticeLinkTitle
	if err := n.Sender.Send(ctx, notice); err != nil {
		n.backoffUntil = now.Add(noticeBackoff)
		n.Log.Info("notice not sent; it is retried later", "reason", err.Error())
		return false
	}
	return true
}

func capitalize(provider string) string {
	if provider == "" {
		return provider
	}
	return strings.ToUpper(provider[:1]) + provider[1:]
}

func (n *Notifier) incidents(ctx context.Context, provider string, obs LoginStatus, now time.Time) {
	inc, open := n.state.Incidents[provider]
	switch obs.State {
	case LoginNeedsLogin:
		if !open {
			inc = incidentRecord{ID: provider + "-" + strconv.FormatInt(now.Unix(), 10), Opened: now.Unix()}
			n.state.Incidents[provider] = inc
			n.dirty = true
		}
		if inc.Notified {
			return
		}
		if n.send(ctx, Notice{
			Title:    "Keeper needs login (" + provider + ")",
			Message:  capitalize(provider) + " sign-in is needed. Until it is renewed the phone sessions cannot start. Renew from the standby session: agent-run keeper-login " + provider + " url (quickstart section 6).",
			Priority: 1,
		}, now) {
			inc.Notified = true
			n.state.Incidents[provider] = inc
			n.dirty = true
		}
	case LoginReady:
		if !open {
			return
		}
		if !inc.Notified {
			// The incident was never announced; there is nothing to resolve.
			delete(n.state.Incidents, provider)
			n.dirty = true
			return
		}
		if n.send(ctx, Notice{
			Title:    "Keeper login restored (" + provider + ")",
			Message:  capitalize(provider) + " sign-in was renewed; the keeper holds a working login again.",
			Priority: 0,
		}, now) {
			delete(n.state.Incidents, provider)
			n.dirty = true
		}
	}
	// LoginInProgress and Unknown change nothing: a ceremony in flight neither
	// opens nor closes an incident.
}

func (n *Notifier) warning(ctx context.Context, provider string, obs LoginStatus, now time.Time) {
	if obs.State != LoginReady || obs.LapsesAt.IsZero() {
		return
	}
	left := obs.LapsesAt.Sub(now)
	if left > noticeWarnWithin || left <= 0 {
		// Not yet within a week; or already lapsed, which NeedsLogin announces.
		return
	}
	local := now.In(n.Zone)
	if !n.inWindow(local) {
		return
	}
	day := local.Format("2006-01-02")
	rec := n.state.Warnings[provider]
	if obs.LapsesAt.Unix() > rec.Lapse+int64(noticeLapseSlack/time.Second) {
		// A new, later lapse date means the login was renewed: start again.
		rec = warnRecord{Lapse: obs.LapsesAt.Unix()}
	}
	if rec.Day == day {
		return
	}
	var when string
	switch days := int((left + 24*time.Hour - 1) / (24 * time.Hour)); {
	case left < 24*time.Hour:
		when = "in under a day"
	case days == 1:
		when = "in 1 day"
	default:
		when = fmt.Sprintf("in %d days", days)
	}
	if n.send(ctx, Notice{
		Title:    capitalize(provider) + " login lapses " + when,
		Message:  capitalize(provider) + " login lapses " + when + " (" + obs.LapsesAt.UTC().Format("2006-01-02 15:04 UTC") + ") — renew from the standby: agent-run keeper-login " + provider + " url (quickstart section 6).",
		Priority: 0,
	}, now) {
		if obs.LapsesAt.Unix() > rec.Lapse {
			rec.Lapse = obs.LapsesAt.Unix()
		}
		rec.Day = day
		n.state.Warnings[provider] = rec
		n.dirty = true
	}
}

// SendTestNotice sends the fixed test notice and nothing else: the one-shot
// trigger `dev-env-keeper notify-test` uses it. The error names no credential.
func SendTestNotice(ctx context.Context, s NoticeSender) error {
	return s.Send(ctx, Notice{
		Title: "dev-env test notice", Message: "dev-env test notice",
		Priority: 0, URL: NoticeLinkURL, URLTitle: NoticeLinkTitle,
	})
}
