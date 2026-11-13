package keeper

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// The summoned-session pager (DESIGN-064 6.8; story 782, slice a of 742). A
// leader-only runnable that copies the Ask notifier: every 60 seconds it lists
// the AgentSessions the API's summoning create labelled, still requires
// spec.caller, and sends each session's terminal page (derived from its close
// record by summonoutcome.TerminalPage, ruling Q1) and each lifecycle event in
// status.summon.pages[], once each, at priority 0, any hour (ruling Q7). Its
// ledger is written before every send. It is off by default.
//
// It never stops the keeper: Start returns no error, a bad flag set disables
// only the pager (DisableInvalid), and an unavailable ledger, list or
// credential sends nothing and waits for the next tick. It shares no state
// with the Ask notifier or the login refresher: it has its own Kubernetes
// client, REST mapper and transport, and its own Pushover transport (story
// 782 fix round 1, F1); it reads the same Pushover credential files on each
// send, as every keeper notice does.
const (
	summonPagesEvery = 60 * time.Second
	// At most six page attempts in any 60 minutes, the drill and the
	// more-events summary included (ruling Q5; op782-q-2, a rolling window).
	summonPagesWindow = time.Hour
	summonPagesCap    = 6
	// The list is bounded as the Ask store's is (ruling Q2).
	summonPagesListPage  = 500
	summonPagesListPages = 20
	// summonPagesProofReads bounds the reads, a tick, that prove a session
	// gone before its entries are pruned (story 782 fix round 1, F3).
	summonPagesProofReads = 20
	// summonPagesRequestTimeout bounds every request of the pager's own
	// client, its REST mapper's discovery included (F1).
	summonPagesRequestTimeout = noticeStoreTimeout
	summonNoteMax             = 200
	// summonConsolePath is the console's sessions list: the link of a page
	// whose session has no verified link (6.8; the console has no per-session
	// page whose pattern this slice could verify).
	summonConsolePath = "/portal/projects/dev-env/environments/internal/sessions"
	summonPageSender  = "dev-env-keeper"
	summonPageSource  = "summon-pages"
	summonEventDrill  = "drill"
	summonEventMore   = "more-events"
)

// SummonPageOptions configure the pager; every field is off or empty by default.
type SummonPageOptions struct {
	Enabled bool
	// PortalOrigin is the console's HTTPS origin, for the sessions list link.
	PortalOrigin string
	// Drill, when set, sends one page starting DRILL for this id, once ever
	// (ruling Q8). 1 to 63 lower-case letters, digits and hyphens. The ledger
	// remembers every id it sent; past summonPagesMaxDrills ids a new one is
	// refused and logged (F8).
	Drill string
	// StateConfigMap is the precreated ledger in the keeper's namespace.
	StateConfigMap string
	// SessionNamespace is where the AgentSessions live. Empty disables the
	// pager; no other option's namespace stands in for it (F7).
	SessionNamespace string

	// requestTimeout replaces summonPagesRequestTimeout, and wrapTransport
	// wraps the pager's own transport: test seams only.
	requestTimeout time.Duration
	wrapTransport  func(http.RoundTripper) http.RoundTripper
}

func (o SummonPageOptions) withDefaults() SummonPageOptions {
	if o.StateConfigMap == "" {
		o.StateConfigMap = DefaultSummonPagesConfigMap
	}
	return o
}

// problem names, in fixed words and never a value, what is wrong with an
// enabled option set; "" when it is valid.
func (o SummonPageOptions) problem(notices NoticeOptions, ask AskNotificationOptions, leaderElect bool) string {
	o = o.withDefaults()
	notices = notices.withDefaults()
	askLedger := ask.StateConfigMap
	if askLedger == "" {
		askLedger = DefaultAskNotificationConfigMap
	}
	switch {
	case !leaderElect:
		return "the pager needs keeper leader election"
	case !httpsOrigin(o.PortalOrigin):
		return "the portal origin must be an HTTPS origin without credentials, path, query or fragment"
	case o.Drill != "" && len(validation.IsDNS1123Label(o.Drill)) > 0:
		return "the drill id must be 1 to 63 lower-case letters, digits or hyphens"
	case !strings.HasPrefix(notices.URL, "https://"):
		return "the Pushover URL must be HTTPS"
	case len(validation.IsDNS1123Subdomain(o.StateConfigMap)) > 0 || o.StateConfigMap == notices.StateConfigMap || o.StateConfigMap == askLedger:
		return "the ledger must be a ConfigMap name of its own, not the notice or Ask ledger"
	case o.SessionNamespace == "":
		return "the session namespace is empty"
	}
	return ""
}

// DisableInvalid keeps a bad pager flag from stopping the credential keeper:
// it turns the pager off and logs why, once, without any value.
func (o SummonPageOptions) DisableInvalid(notices NoticeOptions, ask AskNotificationOptions, leaderElect bool, log logr.Logger) SummonPageOptions {
	if !o.Enabled {
		return o
	}
	if why := o.problem(notices, ask, leaderElect); why != "" {
		o.Enabled = false
		if log.GetSink() != nil {
			log.Error(nil, "summon pages disabled: invalid configuration", "reason", why)
		}
	}
	return o
}

func httpsOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && len(origin) <= 300 &&
		(origin == "https://"+u.Host || origin == "https://"+u.Host+"/")
}

type summonSessionReader interface {
	list(context.Context) ([]v1alpha1.AgentSession, error)
	// uid reads one session by name: its UID, errSummonSessionGone when no
	// session has that name, or another error when the read failed.
	uid(ctx context.Context, name string) (types.UID, error)
}

var errSummonSessionGone = errors.New("summoned session not found")

// kubeSummonSessions lists the labelled AgentSessions by an uncached read:
// 500 a page and at most 20 pages; a list that does not finish is an error.
type kubeSummonSessions struct {
	reader    client.Reader
	namespace string
}

func (k *kubeSummonSessions) list(ctx context.Context) ([]v1alpha1.AgentSession, error) {
	var out []v1alpha1.AgentSession
	cursor := ""
	for page := 0; page < summonPagesListPages; page++ {
		var list v1alpha1.AgentSessionList
		opts := []client.ListOption{client.InNamespace(k.namespace), client.Limit(summonPagesListPage),
			client.MatchingLabels{summonoutcome.LabelSummoned: summonoutcome.LabelSummonedValue}}
		if cursor != "" {
			opts = append(opts, client.Continue(cursor))
		}
		lctx, cancel := context.WithTimeout(ctx, noticeStoreTimeout)
		err := k.reader.List(lctx, &list, opts...)
		cancel()
		if err != nil {
			return nil, errSummonPages
		}
		out = append(out, list.Items...)
		if cursor = list.Continue; cursor == "" {
			return out, nil
		}
	}
	return nil, errSummonPages
}

func (k *kubeSummonSessions) uid(ctx context.Context, name string) (types.UID, error) {
	gctx, cancel := context.WithTimeout(ctx, noticeStoreTimeout)
	defer cancel()
	var s v1alpha1.AgentSession
	err := k.reader.Get(gctx, types.NamespacedName{Namespace: k.namespace, Name: name}, &s)
	switch {
	case apierrors.IsNotFound(err):
		return "", errSummonSessionGone
	case err != nil:
		return "", errSummonPages
	}
	return s.UID, nil
}

// summonPager is the leader-only runnable. Its sender is the Pushover sender
// and nothing else: there is no email or other path (R-01).
type summonPager struct {
	reader    summonSessionReader
	store     summonPageStore
	sender    *PushoverSender
	clock     clock.Clock
	log       logr.Logger
	origin    string
	drill     string
	namespace string
	// drillRefused keeps the refusal of a drill id past the bound to one log
	// line a process.
	drillRefused bool
	// proofAfter is the last prune candidate a proof read answered: the next
	// tick's reads start after it (story 782 fix round 2, F3). It lives in
	// memory only; a new process starts from the oldest again.
	proofAfter summonProofKey
}

// summonProofKey orders prune candidates: oldest entry first, then UID, then
// stored name. The zero key sorts before every candidate.
type summonProofKey struct {
	at        int64
	uid, name string
}

func (a summonProofKey) less(b summonProofKey) bool {
	if a.at != b.at {
		return a.at < b.at
	}
	if a.uid != b.uid {
		return a.uid < b.uid
	}
	return a.name < b.name
}

func newSummonPager(o SummonPageOptions, reader summonSessionReader, store summonPageStore, sender *PushoverSender, clk clock.Clock, log logr.Logger) *summonPager {
	if !o.Enabled || reader == nil || store == nil || sender == nil {
		return nil
	}
	if clk == nil {
		clk = clock.RealClock{}
	}
	if log.GetSink() == nil {
		log = logr.Discard()
	}
	return &summonPager{reader: reader, store: store, sender: sender, clock: clk, log: log,
		origin: strings.TrimSuffix(o.PortalOrigin, "/"), drill: o.Drill, namespace: o.SessionNamespace}
}

// summonPagerFor builds the pager Run adds, or nil when it is off, its
// options are invalid or its own client cannot be built. It is given the
// keeper's REST config, never the keeper's client. Nothing is read here.
func summonPagerFor(o Options, cfg *rest.Config, log logr.Logger) *summonPager {
	sp := o.SummonPages.withDefaults().DisableInvalid(o.Notices, o.AskNotifications, o.LeaderElect, log)
	if !sp.Enabled {
		return nil
	}
	c, err := newSummonPagesClient(cfg, sp)
	if err != nil {
		var hooks summonConfigHooks
		if errors.As(err, &hooks) {
			log.Error(nil, "summon pages disabled: the pager's own client could not be built", "reason", hooks.Error())
		} else {
			log.Error(nil, "summon pages disabled: the pager's own client could not be built")
		}
		return nil
	}
	notices := o.Notices.withDefaults()
	return newSummonPager(sp, &kubeSummonSessions{reader: c, namespace: sp.SessionNamespace},
		&configMapSummonPageStore{client: c, name: types.NamespacedName{Namespace: o.Namespace, Name: sp.StateConfigMap}},
		&PushoverSender{URL: notices.URL, TokenFile: notices.TokenFile, UserFile: notices.UserFile, Client: newSummonPushClient()}, o.Clock, log)
}

// newSummonPagesClient builds the pager's own uncached client (story 782 fix
// round 1, F1). It shares no client, REST mapper, lock, rate limiter or
// transport with the login refresher, the credential fence or the Ask
// notifier, so a stalled pager request holds nothing they need:
//   - a copy of the keeper's REST config whose request timeout bounds every
//     request, the REST mapper's discovery included (discovery runs on a
//     background context, so only the HTTP client's timeout bounds it);
//   - a dialer of its own, which keeps client-go's transport cache from
//     handing it the keeper's transport and connections;
//   - no client-side rate limiter, so no request waits for a token outside
//     that timeout (fix round 2);
//   - its own HTTP client and REST mapper.
func newSummonPagesClient(cfg *rest.Config, o SummonPageOptions) (client.Client, error) {
	pc, err := summonPagesRESTConfig(cfg, o)
	if err != nil {
		return nil, err
	}
	scheme := runtime.NewScheme()
	if err := errors.Join(corev1.AddToScheme(scheme), v1alpha1.AddToScheme(scheme)); err != nil {
		return nil, err
	}
	hc, err := rest.HTTPClientFor(pc)
	if err != nil {
		return nil, err
	}
	mapper, err := apiutil.NewDynamicRESTMapper(pc, hc)
	if err != nil {
		return nil, err
	}
	return client.New(pc, client.Options{Scheme: scheme, HTTPClient: hc, Mapper: mapper})
}

// summonConfigHooks names, in fixed words, the hooks of its own a keeper REST
// config carries that the pager refuses; never a value.
type summonConfigHooks []string

func (h summonConfigHooks) Error() string {
	words := strings.Join(h, ", ")
	if i := strings.LastIndex(words, ", "); i >= 0 {
		words = words[:i] + " and " + words[i+2:]
	}
	return "the keeper's REST config has its own " + words
}

// summonPagesRESTConfig is the pager's copy of the keeper's REST config; cfg
// is not changed. A config that carries a transport, a transport wrapper or a
// dialer of its own is refused (story 782 fix round 2, F1): the pager cannot
// show that one shares no lock, client or connection with the keeper's other
// workers, and nothing of the config, its authentication included, is dropped
// to make it fit. The keeper's production config, ctrl.GetConfig's in-cluster
// config, carries none of the three.
//
// The copy does no client-side rate limiting (QPS below zero, no rate
// limiter; ctrl.GetConfig already sets the keeper's QPS so): client-go waits
// for a token before it starts the request timeout, and discovery runs on a
// background context, so a limiter's wait would be outside every bound. The
// pager makes one list, at most 20 proof reads and a few ledger calls a
// minute, and the API server's priority and fairness still applies.
func summonPagesRESTConfig(cfg *rest.Config, o SummonPageOptions) (*rest.Config, error) {
	if cfg == nil {
		return nil, errSummonPages
	}
	var hooks summonConfigHooks
	if cfg.Transport != nil {
		hooks = append(hooks, "transport")
	}
	if cfg.WrapTransport != nil {
		hooks = append(hooks, "transport wrapper")
	}
	if cfg.Dial != nil {
		hooks = append(hooks, "dialer")
	}
	if len(hooks) > 0 {
		return nil, hooks
	}
	pc := rest.CopyConfig(cfg)
	pc.Timeout = summonPagesRequestTimeout
	if o.requestTimeout > 0 {
		pc.Timeout = o.requestTimeout
	}
	pc.RateLimiter, pc.QPS = nil, -1
	pc.Dial = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	if o.wrapTransport != nil {
		// A test seam only: no option or flag sets it.
		pc.Wrap(o.wrapTransport)
	}
	return pc, nil
}

// newSummonPushClient is the pager's Pushover client: the sender's timeout
// and a transport of its own, not the process's default transport the other
// notices use (F1).
func newSummonPushClient() *http.Client {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{Timeout: noticeHTTPTimeout, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	}
	return &http.Client{Timeout: noticeHTTPTimeout, Transport: t.Clone()}
}

func (*summonPager) NeedLeaderElection() bool { return true }

// Start never returns an error: a runnable's error would stop the manager and
// with it the login refresher. It logs the namespace it lists once (F7).
func (p *summonPager) Start(ctx context.Context) error {
	p.log.Info("summon pages started", "sessionNamespace", p.namespace)
	for {
		p.tick(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-p.clock.After(summonPagesEvery):
		}
	}
}

// summonJob is one page the tick may send.
type summonJob struct {
	key     string // the ledger key
	event   string
	at      time.Time
	session *v1alpha1.AgentSession // nil for the drill
	named   bool                   // a terminal page whose close a row names
	count   int                    // a more-events page's count
}

func (p *summonPager) tick(ctx context.Context) {
	defer func() {
		if recover() != nil {
			p.log.Error(nil, "summon page tick failed") // never the panic value
		}
	}()
	if !PushoverCredentialsPresent(p.sender.TokenFile, p.sender.UserFile) {
		p.log.Info("summon pages: no Pushover credentials")
		return
	}
	ledger, err := p.store.load(ctx)
	if err != nil {
		p.log.Error(nil, "summon page state unavailable")
		return
	}
	sessions, err := p.reader.list(ctx)
	if err != nil {
		p.log.Error(nil, "summon page scan unavailable")
		return
	}
	now := p.clock.Now()
	live := map[string]bool{}
	var jobs []summonJob
	if p.drill != "" {
		jobs = append(jobs, summonJob{key: "drill/" + p.drill, event: summonEventDrill})
	}
	for i := range sessions {
		s := &sessions[i]
		if s.Spec.Caller == "" || s.Labels[summonoutcome.LabelSummoned] != summonoutcome.LabelSummonedValue || s.UID == "" {
			continue // a session without spec.caller is never paged
		}
		live[string(s.UID)] = true
		jobs = append(jobs, summonJobs(s)...)
	}
	// A prune is saved only with a write that adds an entry, as the Ask
	// notifier's is, so a saved ledger never has an empty dedupe beside its
	// page records (which load refuses as a reset). Its proof reads are made
	// only in a tick that may add one.
	if p.fresh(ledger, jobs, now) {
		p.prune(ctx, ledger, live, now)
	}
	dirty := false
	sort.SliceStable(jobs, func(i, j int) bool {
		a, b := jobs[i], jobs[j]
		if (a.session == nil) != (b.session == nil) {
			return a.session == nil // the drill first
		}
		if !a.at.Equal(b.at) {
			return a.at.Before(b.at)
		}
		if a.session != nil && a.session.Name != b.session.Name {
			return a.session.Name < b.session.Name
		}
		return a.key < b.key
	})
	// The more-events page goes first whenever the window has room for it.
	jobs = append([]summonJob{{event: summonEventMore}}, jobs...)
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		now = p.clock.Now()
		if j.event == summonEventMore {
			if ledger.State.Capped == 0 || !ledger.State.room(now) {
				continue
			}
			j.count = ledger.State.Capped
			j.key = fmt.Sprintf("more/%d", ledger.State.More+1)
		}
		if _, seen := ledger.State.Events[j.key]; seen {
			continue
		}
		if j.event == summonEventDrill && ledger.State.drills() >= summonPagesMaxDrills {
			// A used drill id is never forgotten, so the ledger has no room
			// for another (F8): nothing is sent or recorded.
			if !p.drillRefused {
				p.drillRefused = true
				p.log.Error(nil, "summon page drill refused: the ledger holds the most drill ids it keeps")
			}
			continue
		}
		if len(ledger.State.Events) >= summonPagesMaxEvents {
			p.log.Error(nil, "summon page dedupe capacity reached")
			break
		}
		if !ledger.State.room(now) {
			ledger.State.Events[j.key] = summonPageEntry{At: now.Unix(), State: summonStateCapped, Session: sessionName(j.session)}
			ledger.State.Capped++
			dirty = true
			continue
		}
		if !p.attempt(ctx, ledger, j, now) {
			return
		}
		dirty = false
	}
	if dirty && ctx.Err() == nil {
		if err := p.store.save(ctx, ledger); err != nil {
			p.log.Error(nil, "summon page ledger update unconfirmed")
		}
	}
}

// fresh reports whether this tick may add a ledger entry: a summary the
// window has room for, or a page whose key the dedupe lacks (a drill past the
// bound excepted).
func (p *summonPager) fresh(l *summonPageLedger, jobs []summonJob, now time.Time) bool {
	if l.State.Capped > 0 && l.State.room(now) {
		return true
	}
	for _, j := range jobs {
		if _, seen := l.State.Events[j.key]; seen {
			continue
		}
		if j.event == summonEventDrill && l.State.drills() >= summonPagesMaxDrills {
			continue
		}
		return true
	}
	return false
}

// prune drops, in memory, what can never be needed again (story 782 fix
// round 1, F3 and F8). Absence from the labelled list proves nothing: a label
// can be removed and restored. A session's entry goes only once it is an hour
// old and a read of its stored session name proves the session gone, not
// found or found with another UID (UIDs are never reused, so a gone session
// cannot page again). At most summonPagesProofReads reads a tick, in the order
// oldest entry first, starting after the candidate the last answered read was
// for and going round to the oldest at the end (fix round 2, F3): sessions
// still there cannot use every read of every tick. A failed read ends this
// tick's reads, and the next tick starts with it. A summary's entry goes after
// a day. A drill id is never forgotten.
func (p *summonPager) prune(ctx context.Context, l *summonPageLedger, live map[string]bool, now time.Time) {
	type owner struct{ uid, name string }
	oldest := map[owner]int64{}
	old := func(e summonPageEntry) bool { return !now.Before(time.Unix(e.At, 0).Add(time.Hour)) }
	for key, e := range l.State.Events {
		uid, _, _ := cutSummonKey(key)
		switch uid {
		case "drill":
		case "more":
			if !now.Before(time.Unix(e.At, 0).Add(24 * time.Hour)) {
				delete(l.State.Events, key)
			}
		default:
			if live[uid] || e.Session == "" || !old(e) {
				continue
			}
			o := owner{uid, e.Session}
			if at, ok := oldest[o]; !ok || e.At < at {
				oldest[o] = e.At
			}
		}
	}
	candidates := make([]summonProofKey, 0, len(oldest))
	for o, at := range oldest {
		candidates = append(candidates, summonProofKey{at: at, uid: o.uid, name: o.name})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].less(candidates[j]) })
	start := sort.Search(len(candidates), func(i int) bool { return p.proofAfter.less(candidates[i]) })
	gone := map[owner]bool{}
	for n := 0; n < min(summonPagesProofReads, len(candidates)) && ctx.Err() == nil; n++ {
		c := candidates[(start+n)%len(candidates)]
		uid, err := p.reader.uid(ctx, c.name)
		if err != nil && !errors.Is(err, errSummonSessionGone) {
			break
		}
		p.proofAfter = c
		if err != nil || string(uid) != c.uid {
			gone[owner{c.uid, c.name}] = true
		}
	}
	for key, e := range l.State.Events {
		uid, _, _ := cutSummonKey(key)
		if gone[owner{uid, e.Session}] && old(e) {
			delete(l.State.Events, key)
		}
	}
}

// summonJobs lists a session's pages: its terminal page, unless its row says
// digest, and each recorded lifecycle event this keeper has a template for.
func summonJobs(s *v1alpha1.AgentSession) []summonJob {
	var out []summonJob
	if s.Status.Summon != nil {
		for _, pg := range s.Status.Summon.Pages {
			if _, ok := summonLifecycle[pg.Event]; ok {
				out = append(out, summonJob{key: string(s.UID) + "/" + string(pg.Event), event: string(pg.Event), at: pg.At.Time, session: s})
			}
		}
	}
	if ev, ok := summonoutcome.ClosedTerminalPage(s); ok && ev != summonoutcome.TerminalDigest {
		c := s.Status.Summon.Closed
		_, named := summonoutcome.TerminalRow(s.Spec.Lane, s.Status.Outcome.State, c.Source, c.Reason)
		out = append(out, summonJob{key: string(s.UID) + "/" + string(ev), event: string(ev), at: c.At.Time, session: s, named: named})
	}
	return out
}

// attempt reserves one page in the ledger, sends it only once that write is
// confirmed, and stores the result. It reports whether the tick may go on.
func (p *summonPager) attempt(ctx context.Context, ledger *summonPageLedger, j summonJob, now time.Time) bool {
	notice, record := p.render(j)
	dispatchBy := now.Add(noticeStoreTimeout)
	ledger.State.reserve(now, dispatchBy)
	ledger.State.Events[j.key] = summonPageEntry{At: now.Unix(), State: string(pages.StateRecorded), Session: sessionName(j.session)}
	if j.event == summonEventMore {
		ledger.State.Capped -= j.count
		ledger.State.More++
	}
	if _, err := ledger.Pages.Begin(record, now); err != nil {
		p.log.Error(nil, "summon page record refused")
		return false
	}
	if err := p.store.save(ctx, ledger); err != nil {
		p.log.Error(nil, "summon page reservation unconfirmed")
		return false
	}
	// A late save cannot send: the window counted the reserved instant.
	outcome, status, request := pages.OutcomeFailed, 0, ""
	if !p.clock.Now().After(dispatchBy) {
		outcome, status, request = summonSendOutcome(p.sender.SendReceipt(ctx, notice))
	}
	ledger.Pages.Finish(record.Key, outcome, status, request, p.clock.Now())
	state := map[pages.Outcome]pages.State{pages.OutcomeSent: pages.StateSent, pages.OutcomeFailed: pages.StateFailed, pages.OutcomeAmbiguous: pages.StateAmbiguous}[outcome]
	e := ledger.State.Events[j.key]
	e.State = string(state)
	ledger.State.Events[j.key] = e
	if err := p.store.save(ctx, ledger); err != nil {
		// The reservation stays recorded: the page is never sent again.
		p.log.Error(nil, "summon page outcome unconfirmed")
		return false
	}
	return true
}

// summonSendOutcome classifies one send. Nothing is retried: a definite
// refusal is failed, anything that may have reached Pushover is ambiguous.
func summonSendOutcome(rc Receipt, err error) (pages.Outcome, int, string) {
	var refused *SendError
	switch {
	case err == nil && rc.Confirmed:
		return pages.OutcomeSent, rc.Status, rc.Request
	case errors.Is(err, errPushoverCredentials):
		return pages.OutcomeFailed, 0, "" // no request was made
	case errors.As(err, &refused) && refused.Status >= 400 && refused.Status < 500:
		return pages.OutcomeFailed, refused.Status, ""
	case errors.As(err, &refused):
		return pages.OutcomeAmbiguous, refused.Status, ""
	default:
		return pages.OutcomeAmbiguous, rc.Status, ""
	}
}

func sessionName(s *v1alpha1.AgentSession) string {
	if s == nil {
		return ""
	}
	return summonStoredName(s.Name)
}

func summonRecordKey(ledgerKey string) string { return "summon/" + ledgerKey }

// summonLifecycle is the template row of each lifecycle event this keeper
// sends. Stories 768 and 745 add theirs here when they add the event.
var summonLifecycle = map[v1alpha1.SummonPageEvent]string{
	v1alpha1.PageEscalationHeld:     "escalation %s has waited more than 15 minutes for a slot.",
	v1alpha1.PageEscalationReminder: "escalation %s has been idle for 24 hours.",
}

var (
	// summonVerifiedLink is status.remoteControl.url as agentd writes it for a
	// registered session (internal/agentd/remote.go): nothing else is a link.
	summonVerifiedLink = regexp.MustCompile(`^https://claude\.ai/code/session_[A-Za-z0-9_-]{1,120}$`)
	// summonURLLike finds anything in a note that may be a link: a scheme
	// with //, a scheme Pushover or a phone could open, a www. host, or a
	// dotted run of labels (groups 1 and 2: the run and any port, path, query
	// or fragment after it). A label is letters with their combining marks,
	// decimal digits and hyphens, of any script; any other character, an
	// underscore included, ends it (story 782 fix round 2, F6, and the
	// coordinator's answer op782-fix2-a-1). summonRedact decides which runs
	// are hosts.
	summonURLLike = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://\S*|\b(?:mailto|tel|sms|javascript|data|file|vbscript):\S+|\bwww\.\S+|` +
		`([\p{L}\p{M}\p{Nd}-]+(?:\.[\p{L}\p{M}\p{Nd}-]+)+)((?::[0-9]+)?(?:[/?#]\S*)?)`)
	summonWordShape = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
)

// summonWord shows an operator-written word (a lane, state, source or
// reason) only when it is at most 64 lower-case letters, digits and hyphens
// (op782-q-1): a keeper older than its operator cannot know the word.
func summonWord(s string) string {
	if summonWordShape.MatchString(s) {
		return s
	}
	return "unknown"
}

// summonStoredName is a session or caller name as the API validated it, or
// "unknown".
func summonStoredName(s string) string {
	if summonStored.MatchString(s) {
		return s
	}
	return "unknown"
}

// summonNote is a session's note as a page shows it: every control or format
// character and every line or paragraph separator (U+2028 and U+2029
// included) a space, every link replaced by [link removed], then cut to 200
// characters. A cut can end a word as a host ("evil.co1x" cut to "evil.co"),
// so the cut text is redacted once more and cut again; that second cut can
// only shorten the last [link removed].
func summonNote(note string) string {
	note = strings.ToValidUTF8(note, "\ufffd")
	note = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return ' '
		}
		return r
	}, note)
	note = summonRedact(note)
	if utf8.RuneCountInString(note) > summonNoteMax {
		note = summonRedact(string([]rune(note)[:summonNoteMax]))
		if utf8.RuneCountInString(note) > summonNoteMax {
			note = string([]rune(note)[:summonNoteMax])
		}
	}
	return note
}

// summonRedact replaces every link in note with [link removed]: each scheme
// or www. match; a dotted run followed by a port, path, query or fragment,
// whatever its labels (an address such as 10.0.0.1:8080 or 10.0.0.1/admin);
// and the dotted host that starts a bare run, if any (F6, and the
// coordinator's answers op782-fix1-a-1 and op782-fix2-a-1: evil.example,
// é.example, пример.xn--p1ai, उदाहरण.भारत and a file name such as main.go; a
// dotted number alone, such as 2.105.0, stays).
func summonRedact(note string) string {
	var b strings.Builder
	last := 0
	for _, m := range summonURLLike.FindAllStringSubmatchIndex(note, -1) {
		start, end := m[0], m[1]
		if m[2] >= 0 && m[4] == m[5] {
			// A bare run: only its host, if it starts with one, is a link.
			host := summonHostEnd(note[m[2]:m[3]])
			if host == 0 {
				continue
			}
			end = m[2] + host
		}
		b.WriteString(note[last:start])
		b.WriteString(summonLinkRemoved(note[start:end]))
		last = end
	}
	b.WriteString(note[last:])
	return b.String()
}

// summonLinkRemoved is the replacement of one link. Trailing punctuation
// belongs to the sentence, and so does a closing parenthesis the link did not
// open.
func summonLinkRemoved(m string) string {
	trimmed := strings.TrimRight(m, ".,;:!?")
	if strings.HasSuffix(trimmed, ")") && strings.Count(trimmed, "(") < strings.Count(trimmed, ")") {
		trimmed = strings.TrimSuffix(trimmed, ")")
	}
	return "[link removed]" + m[len(trimmed):]
}

// summonHostEnd is the length of the longest dotted host that starts a run
// of labels: two or more labels, the last of them a top label, ending at the
// run's end, a dot or a hyphen (so the host in a.example-b and a.example.123
// is a.example); 0 when there is none.
func summonHostEnd(run string) int {
	for end := len(run); ; {
		dot := strings.LastIndexByte(run[:end], '.')
		if dot < 0 {
			return 0
		}
		label := run[dot+1 : end]
		if summonTopLabel(label) {
			return end
		}
		if i := strings.IndexByte(label, '-'); i > 0 && summonTopLabel(label[:i]) {
			return dot + 1 + i
		}
		end = dot
	}
}

// summonTopLabel reports whether a label can end a host: it starts xn--, or it
// is two or more letters of any script, with any combining marks.
func summonTopLabel(label string) bool {
	if len(label) > 4 && strings.EqualFold(label[:4], "xn--") {
		return true
	}
	letters := 0
	for _, r := range label {
		switch {
		case unicode.IsLetter(r):
			letters++
		case unicode.IsMark(r):
		default:
			return false
		}
	}
	return letters >= 2
}

// render builds the fixed-template notice and its content-free record: the
// fixed title, the session name and the note's byte count (ruling Q9).
func (p *summonPager) render(j summonJob) (Notice, pages.Page) {
	console := p.origin + summonConsolePath
	n := Notice{Priority: 0, URL: console, URLTitle: "Open dev-env sessions"}
	rec := pages.Page{Key: summonRecordKey(j.key), Sender: summonPageSender, Source: summonPageSource, Priority: 0}
	switch j.event {
	case summonEventDrill:
		n.Title = "DRILL [dev-env] drill"
		n.Message = fmt.Sprintf("DRILL [dev-env] drill %s: a test page from the keeper's summoned-session pager. Nothing needs you.", p.drill)
		rec.Title, rec.Body, rec.Correlation = n.Title, "drill "+p.drill, p.drill
		return n, rec
	case summonEventMore:
		n.Title = "[dev-env] " + summonEventMore
		events := "events were"
		if j.count == 1 {
			events = "event was"
		}
		n.Message = fmt.Sprintf("[dev-env] %s: %d more %s not paged. At most %d pages go in any 60 minutes; the keeper's ledger records each one as capped.", summonEventMore, j.count, events, summonPagesCap)
		rec.Title, rec.Body = n.Title, fmt.Sprintf("%d more events", j.count)
		return n, rec
	}
	s := j.session
	name, caller, lane := summonStoredName(s.Name), summonStoredName(s.Spec.Caller), summonWord(string(s.Spec.Lane))
	var head string
	switch {
	case j.event == string(summonoutcome.TerminalEscalated):
		head = fmt.Sprintf("summoned session %s escalated its work to you.", name)
	case j.event == string(summonoutcome.TerminalLost):
		head = fmt.Sprintf("summoned session %s was lost (%s).", name, summonWord(s.Status.Summon.Closed.Reason))
	case j.event == string(summonoutcome.TerminalFailed) && !j.named:
		c := s.Status.Summon.Closed
		head = fmt.Sprintf("summoned session %s closed in a way the keeper's terminal table does not name: lane %s, state %s, source %s, reason %s.",
			name, lane, summonWord(string(s.Status.Outcome.State)), summonWord(c.Source), summonWord(c.Reason))
	case j.event == string(summonoutcome.TerminalFailed) && s.Status.Summon.Closed.Source == summonoutcome.Report:
		head = fmt.Sprintf("summoned session %s reported failed.", name)
	case j.event == string(summonoutcome.TerminalFailed):
		head = fmt.Sprintf("summoned session %s closed failed without a report (%s).", name, summonWord(s.Status.Summon.Closed.Reason))
	default:
		head = fmt.Sprintf(summonLifecycle[v1alpha1.SummonPageEvent(j.event)], name)
	}
	n.Title = "[dev-env] " + j.event
	n.Message = fmt.Sprintf("[dev-env] %s: %s\nCaller %s, lane %s.", j.event, head, caller, lane)
	note := ""
	if s.Status.Outcome != nil {
		note = s.Status.Outcome.Note
	}
	if shown := summonNote(note); shown != "" {
		n.Message += "\nsession note: " + shown
	}
	if rc := s.Status.RemoteControl; rc != nil && summonVerifiedLink.MatchString(rc.URL) {
		n.URL, n.URLTitle = rc.URL, "Open the session"
	}
	rec.Title, rec.Correlation = n.Title, name
	rec.Body = fmt.Sprintf("session %s, note %d bytes", name, len(note))
	return n, rec
}
