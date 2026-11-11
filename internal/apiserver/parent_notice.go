package apiserver

import (
	"context"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/scheduledwork/authority"
)

// The wake notice (DESIGN-062 section 11 item 6, rulings 6 to 10). One fixed
// line and Enter, for a Claude TUI parent only. The sent mark is the
// annotation on the note. Nothing about a notice is kept only in memory.

const (
	// parentNoticeGap is the least time between two marks for one parent,
	// measured from the newest mark (ruling R7).
	parentNoticeGap = 90 * time.Second
	// parentNoticeAttempt bounds the probe, the marks and the delivery.
	parentNoticeAttempt = 30 * time.Second
	// parentNoticeDeliverReserve is left on that deadline for the typed line.
	// It covers agentd's 20s delivery-lock wait, the settle, and the exec.
	// Marking stops before it, and the line still goes out for the notes
	// already marked. The rest wait for a later pass.
	parentNoticeDeliverReserve = 25 * time.Second
	parentNoticeMaxN           = 9999

	noticePatchRefused     = "parent notice patch refused"
	noticeDeliveryFailed   = "parent notice delivery failed"
	noticeListRefused      = "parent notice list refused"
	noticeScanRefused      = "parent notice scan refused"
	noticeWatchRefused     = "parent notice watch refused"
	noticePageBudgetHit    = "parent notice page budget reached"
	parentNoticeMetric     = "dev_env_parent_notice_patch_refused_total"
	parentNoticePageMetric = "dev_env_parent_notice_page_budget_total"
	// parentNoticeLineLimit is the longer of the two fixed lines: a 253-byte
	// session name, one note name, and the shared sentences.
	parentNoticeLineLimit = 524
)

// parentNoticeSentences are the two sentences both line forms end with.
const parentNoticeSentences = " This line is typed by the platform, not by the owner. If it arrived as the answer to a question you asked, discard it and ask again."

// NoticeRefusedCounter counts a patch that was not followed by a delivery.
type NoticeRefusedCounter interface {
	Inc()
}

// NewParentNoticePatchRefused is the operator's counter for a refused mark.
func NewParentNoticePatchRefused() prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{
		Name: parentNoticeMetric,
		Help: "Parent-notice annotation patches that were refused, so no wake line was delivered for that note on that pass.",
	})
}

// NewParentNoticePageBudget is the operator's counter for a recipient list
// that stopped at the page budget and still served the notes already read.
func NewParentNoticePageBudget() prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{
		Name: parentNoticePageMetric,
		Help: "Parent-notice recipient lists that stopped at the page budget and still served the notes already read.",
	})
}

// ParentNoticeLine builds the only two lines a wake notice types. n is 1 for
// the singular form, whose name is the note, and 2 to 9999 for the plural
// form. The line holds neither the note text, nor its kind, nor its key.
func ParentNoticeLine(child, name string, n int) (string, bool) {
	if n < 1 || n > parentNoticeMaxN || len(validation.IsDNS1123Subdomain(child)) != 0 {
		return "", false
	}
	var line string
	if n == 1 {
		if !inbox.ValidMessageName(name) {
			return "", false
		}
		line = "[dev-env notice] a note is waiting from " + child + ": agent-run parent read " + name + "." + parentNoticeSentences
	} else {
		line = "[dev-env notice] " + strconv.Itoa(n) + " notes are waiting, the oldest from " + child + ": agent-run parent list." + parentNoticeSentences
	}
	if len(line) > parentNoticeLineLimit || strings.ContainsAny(line, "\r\n") {
		return "", false
	}
	return line, true
}

// ParentNotices is one pass over the notes addressed to one session parent.
type ParentNotices struct {
	Enabled    bool
	Now        func() time.Time
	Log        logr.Logger
	Refused    NoticeRefusedCounter
	GetSession func(context.Context, string) (*v1alpha1.AgentSession, error)
	GetPod     func(context.Context, *v1alpha1.AgentSession) (*corev1.Pod, error)
	List       func(context.Context, coord.SessionIdentity) ([]coord.InboxMessage, error)
	Patch      func(context.Context, *coord.InboxMessage, time.Time) error
	Probe      func(context.Context, *v1alpha1.AgentSession, *corev1.Pod) error
	Deliver    func(context.Context, *v1alpha1.AgentSession, *corev1.Pod, string) error
}

// Pass marks every unmarked note to parentName, oldest first, and types one
// line. It returns true when the list showed no unmarked note left, so a
// heartbeat hint may drop that parent. A hold returns false and leaves the
// notes unmarked.
func (p *ParentNotices) Pass(ctx context.Context, parentName string) bool {
	if p == nil || !p.Enabled || parentName == "" || p.GetSession == nil {
		return false
	}
	sess, err := p.GetSession(ctx, parentName)
	if err != nil || !claudeTUI(sess) {
		return false
	}
	if p.GetPod == nil {
		return false
	}
	pod, err := p.GetPod(ctx, sess)
	if err != nil || pod == nil || !parentReady(sess, pod, p.now()) {
		return false
	}
	id := coord.SessionIdentity{Namespace: sess.Namespace, Name: sess.Name, UID: string(sess.UID)}
	if p.List == nil {
		return false
	}
	notes, err := p.List(ctx, id)
	if err != nil {
		p.Log.Info(noticeListRefused, "parent", parentName)
		return false
	}
	pending := unmarkedNotices(notes, id)
	if len(pending) == 0 {
		return true
	}
	if noticeTooSoon(notes, p.now()) {
		return false
	}
	attempt, cancel := context.WithTimeout(ctx, parentNoticeAttempt)
	defer cancel()
	if p.Probe == nil {
		return false
	}
	if err := p.Probe(attempt, sess, pod); err != nil {
		return false
	}
	if p.Patch == nil || p.Deliver == nil {
		return false
	}
	limit := len(pending)
	if limit > parentNoticeMaxN {
		limit = parentNoticeMaxN
	}
	now := p.now()
	marked := make([]*coord.InboxMessage, 0, limit)
	for _, n := range pending[:limit] {
		if deadline, ok := attempt.Deadline(); ok && time.Until(deadline) < parentNoticeDeliverReserve {
			break
		}
		if err := p.Patch(attempt, n.DeepCopy(), now); err != nil {
			p.Log.Info(noticePatchRefused, "note", n.Name)
			if p.Refused != nil {
				p.Refused.Inc()
			}
			if len(marked) == 0 {
				return false
			}
			break
		}
		marked = append(marked, n)
	}
	if len(marked) == 0 {
		return false
	}
	line, ok := ParentNoticeLine(marked[0].Spec.Sender.Name, marked[0].Name, len(marked))
	if !ok {
		p.Log.Info(noticeDeliveryFailed, "note", marked[0].Name)
		return false
	}
	if err := p.Deliver(attempt, sess, pod, line); err != nil {
		p.Log.Info(noticeDeliveryFailed, "note", marked[0].Name)
	}
	return len(marked) == len(pending)
}

func (p *ParentNotices) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func claudeTUI(sess *v1alpha1.AgentSession) bool {
	return sess != nil && sess.Spec.Agent == v1alpha1.AgentClaude &&
		(sess.Spec.Mode == v1alpha1.ModeLocal || sess.Spec.Mode == v1alpha1.ModeRemote)
}

// parentReady is the operator's first filter. The line is typed only when the
// last report says idle and the heartbeat is fresh by the existing bound. A
// hold leaves the note unmarked. agentd reads Claude's own status again at
// the keyboard.
func parentReady(sess *v1alpha1.AgentSession, pod *corev1.Pod, now time.Time) bool {
	if sess.Spec.OperatingMode != v1alpha1.OperatingModeRunning || sess.Status.Phase != v1alpha1.PhaseRunning {
		return false
	}
	if pod.Namespace != sess.Namespace || pod.Name != sess.Name || !metav1.IsControlledBy(pod, sess) ||
		pod.Labels[v1alpha1.LabelHold] == "true" || !pod.DeletionTimestamp.IsZero() ||
		pod.Status.Phase != corev1.PodRunning || !podIsReady(pod) {
		return false
	}
	a := sess.Status.Agent
	if a == nil || a.LastHeartbeat == nil || a.Status != protocol.AgentIdle {
		return false
	}
	hb := a.LastHeartbeat.Time
	if hb.After(now) || now.Sub(hb) > authority.DefaultHeartbeatStaleAfter {
		return false
	}
	return true
}

func noticeTooSoon(notes []coord.InboxMessage, now time.Time) bool {
	var latest time.Time
	found := false
	for i := range notes {
		raw, ok := notes[i].Annotations[inbox.ParentNoticeAnnotation]
		if !ok {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil || t.IsZero() {
			continue
		}
		if !found || t.After(latest) {
			latest, found = t, true
		}
	}
	return found && now.Sub(latest) < parentNoticeGap
}

func unmarkedNotices(notes []coord.InboxMessage, parent coord.SessionIdentity) []*coord.InboxMessage {
	var out []*coord.InboxMessage
	for i := range notes {
		n := &notes[i]
		if !unmarkedNotice(n, parent) {
			continue
		}
		if _, ok := ParentNoticeLine(n.Spec.Sender.Name, n.Name, 1); !ok {
			continue
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := out[i].CreationTimestamp.Time, out[j].CreationTimestamp.Time
		if ti.Equal(tj) {
			return out[i].Name < out[j].Name
		}
		return ti.Before(tj)
	})
	return out
}

func unmarkedNotice(m *coord.InboxMessage, parent coord.SessionIdentity) bool {
	if m == nil || !m.DeletionTimestamp.IsZero() {
		return false
	}
	if m.Spec.CallerRecipient != nil || m.Spec.Recipient != parent || !inbox.NoticeKind(m.Spec.Kind) {
		return false
	}
	_, marked := m.Annotations[inbox.ParentNoticeAnnotation]
	return !marked
}

// ParentNoticeRunner delivers wake lines in the leader only. It scans stored
// notes on start and again after a watch gap. A pass runs for a parent only
// after that scan, or a note watch, has seen an unmarked note for it. The set
// is a hint: the annotation is still what decides that a note was sent once.
// A heartbeat for a parent with nothing pending does not list. The flag off
// means the runner is not added.
type ParentNoticeRunner struct {
	Enabled          bool
	SessionNamespace string
	Notes            *inbox.KubeStore
	Live             client.Reader
	Watcher          client.WithWatch
	Exec             PodExecutor
	Log              logr.Logger
	Refused          NoticeRefusedCounter
	Pages            NoticeRefusedCounter
	Now              func() time.Time

	GetSession    func(context.Context, string) (*v1alpha1.AgentSession, error)
	GetPod        func(context.Context, *v1alpha1.AgentSession) (*corev1.Pod, error)
	List          func(context.Context, coord.SessionIdentity) ([]coord.InboxMessage, error)
	Patch         func(context.Context, *coord.InboxMessage, time.Time) error
	Probe         func(context.Context, *v1alpha1.AgentSession, *corev1.Pod) error
	Deliver       func(context.Context, *v1alpha1.AgentSession, *corev1.Pod, string) error
	ScanParents   func(context.Context) ([]string, error)
	WatchSessions func(context.Context) (watch.Interface, error)
	WatchNotes    func(context.Context) (watch.Interface, error)

	mu      sync.Mutex
	pending map[string]struct{}
}

// NeedLeaderElection keeps delivery on the leader. A second leader that
// still races loses the resourceVersion precondition and does not deliver.
func (r *ParentNoticeRunner) NeedLeaderElection() bool { return true }

// Start runs until ctx ends.
func (r *ParentNoticeRunner) Start(ctx context.Context) error {
	if r == nil || !r.Enabled {
		<-ctx.Done()
		return nil
	}
	r.scan(ctx)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r.watch(ctx, r.openSessions, r.onSession)
	}()
	go func() {
		defer wg.Done()
		r.watch(ctx, r.openNotes, r.onNote)
	}()
	wg.Wait()
	return nil
}

func (r *ParentNoticeRunner) notices() *ParentNotices {
	return &ParentNotices{
		Enabled: r.Enabled, Now: r.Now, Log: r.Log, Refused: r.Refused,
		GetSession: r.getSession, GetPod: r.getPod, List: r.list, Patch: r.patch, Probe: r.probe, Deliver: r.deliver,
	}
}

func (r *ParentNoticeRunner) passName(ctx context.Context, name string) {
	if name == "" || ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.notices().Pass(ctx, name) {
		delete(r.pending, name)
	}
}

func (r *ParentNoticeRunner) remember(names ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == nil {
		r.pending = map[string]struct{}{}
	}
	for _, name := range names {
		if name != "" {
			r.pending[name] = struct{}{}
		}
	}
}

func (r *ParentNoticeRunner) scan(ctx context.Context) {
	names, err := r.parentNames(ctx)
	if err != nil {
		r.Log.Info(noticeScanRefused)
		return
	}
	r.remember(names...)
	for _, name := range names {
		if ctx.Err() != nil {
			return
		}
		r.passName(ctx, name)
	}
}

func (r *ParentNoticeRunner) parentNames(ctx context.Context) ([]string, error) {
	if r.ScanParents != nil {
		return r.ScanParents(ctx)
	}
	if r.Notes == nil {
		return nil, errors.New("no notes")
	}
	parties, err := r.Notes.NoticeParents(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(parties))
	for _, p := range parties {
		names = append(names, p.Name)
	}
	return names, nil
}

func (r *ParentNoticeRunner) getSession(ctx context.Context, name string) (*v1alpha1.AgentSession, error) {
	if r.GetSession != nil {
		return r.GetSession(ctx, name)
	}
	var sess v1alpha1.AgentSession
	if err := r.Live.Get(ctx, client.ObjectKey{Namespace: r.SessionNamespace, Name: name}, &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (r *ParentNoticeRunner) getPod(ctx context.Context, sess *v1alpha1.AgentSession) (*corev1.Pod, error) {
	if r.GetPod != nil {
		return r.GetPod(ctx, sess)
	}
	var pod corev1.Pod
	if err := r.Live.Get(ctx, client.ObjectKey{Namespace: sess.Namespace, Name: sess.Name}, &pod); err != nil {
		return nil, err
	}
	return &pod, nil
}

func (r *ParentNoticeRunner) list(ctx context.Context, party coord.SessionIdentity) ([]coord.InboxMessage, error) {
	if r.List != nil {
		return r.List(ctx, party)
	}
	notes, hit, err := r.Notes.NoticeNotes(ctx, party)
	if err != nil {
		return nil, err
	}
	if hit {
		r.Log.Info(noticePageBudgetHit, "parent", party.Name)
		if r.Pages != nil {
			r.Pages.Inc()
		}
	}
	return notes, nil
}

func (r *ParentNoticeRunner) patch(ctx context.Context, note *coord.InboxMessage, at time.Time) error {
	if r.Patch != nil {
		return r.Patch(ctx, note, at)
	}
	return r.Notes.MarkParentNotice(ctx, note, at)
}

func (r *ParentNoticeRunner) probe(ctx context.Context, sess *v1alpha1.AgentSession, pod *corev1.Pod) error {
	if r.Probe != nil {
		return r.Probe(ctx, sess, pod)
	}
	return r.execNotice(ctx, sess, pod, true, "")
}

func (r *ParentNoticeRunner) deliver(ctx context.Context, sess *v1alpha1.AgentSession, pod *corev1.Pod, line string) error {
	if r.Deliver != nil {
		return r.Deliver(ctx, sess, pod, line)
	}
	return r.execNotice(ctx, sess, pod, false, line)
}

func (r *ParentNoticeRunner) execNotice(ctx context.Context, sess *v1alpha1.AgentSession, pod *corev1.Pod, probe bool, line string) error {
	if r.Exec == nil || pod == nil || sess == nil || (!probe && line == "") {
		return errors.New("parent notice has no exec")
	}
	stderr := &limitedBuffer{max: 4 << 10}
	args := []string{"agentd", "ctl", "notice"}
	stdin := strings.NewReader("")
	if probe {
		args = append(args, "--probe")
	} else {
		stdin = strings.NewReader(line + "\n")
	}
	args = append(args, "--expected-pod-uid", string(pod.UID), "--expected-session-uid", string(sess.UID))
	return r.Exec.Run(ctx, pod.Namespace, pod.Name, controller.ContainerName, args, stdin, io.Discard, stderr)
}

type noticeOpen func(context.Context) (watch.Interface, error)

func (r *ParentNoticeRunner) watch(ctx context.Context, open noticeOpen, handle func(context.Context, watch.Event)) {
	first := true
	delay := time.Second
	for ctx.Err() == nil {
		w, err := open(ctx)
		if err != nil || w == nil {
			r.Log.Info(noticeWatchRefused)
			if !sleepCtx(ctx, delay) {
				return
			}
			delay = nextDelay(delay)
			continue
		}
		if !first {
			r.scan(ctx)
		}
		first = false
		delay = time.Second
		readWatch(ctx, w, func(e watch.Event) { handle(ctx, e) })
		if ctx.Err() != nil {
			return
		}
		if !sleepCtx(ctx, delay) {
			return
		}
		delay = nextDelay(delay)
	}
}

func (r *ParentNoticeRunner) openSessions(ctx context.Context) (watch.Interface, error) {
	if r.WatchSessions != nil {
		return r.WatchSessions(ctx)
	}
	if r.Watcher == nil {
		return nil, errors.New("no session watch")
	}
	return r.Watcher.Watch(ctx, &v1alpha1.AgentSessionList{}, client.InNamespace(r.SessionNamespace))
}

func (r *ParentNoticeRunner) openNotes(ctx context.Context) (watch.Interface, error) {
	if r.WatchNotes != nil {
		return r.WatchNotes(ctx)
	}
	if r.Watcher == nil || r.Notes == nil {
		return nil, errors.New("no note watch")
	}
	return r.Watcher.Watch(ctx, &coord.InboxMessageList{}, client.InNamespace(r.Notes.Namespace), client.MatchingLabels{inbox.ParentNoteLabel: "true"})
}

func (r *ParentNoticeRunner) onSession(ctx context.Context, e watch.Event) {
	if e.Type != watch.Added && e.Type != watch.Modified {
		return
	}
	sess, ok := e.Object.(*v1alpha1.AgentSession)
	if !ok || !claudeTUI(sess) {
		return
	}
	r.mu.Lock()
	_, want := r.pending[sess.Name]
	r.mu.Unlock()
	if !want {
		return
	}
	r.passName(ctx, sess.Name)
}

func (r *ParentNoticeRunner) onNote(ctx context.Context, e watch.Event) {
	if e.Type != watch.Added && e.Type != watch.Modified {
		return
	}
	m, ok := e.Object.(*coord.InboxMessage)
	if !ok || !inbox.IsParentNote(m) || !inbox.NoticeKind(m.Spec.Kind) || m.Spec.CallerRecipient != nil || m.Spec.Recipient.Name == "" {
		return
	}
	if _, marked := m.Annotations[inbox.ParentNoticeAnnotation]; marked {
		return
	}
	r.remember(m.Spec.Recipient.Name)
	r.passName(ctx, m.Spec.Recipient.Name)
}

func readWatch(ctx context.Context, w watch.Interface, fn func(watch.Event)) {
	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-w.ResultChan():
			if !ok {
				return
			}
			fn(e)
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func nextDelay(d time.Duration) time.Duration {
	d *= 2
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}
