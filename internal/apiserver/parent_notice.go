package apiserver

import (
	"context"
	"errors"
	"io"
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
	parentNoticeGap = time.Minute

	noticePatchRefused    = "parent notice patch refused"
	noticeDeliveryFailed  = "parent notice delivery failed"
	noticeListRefused     = "parent notice list refused"
	noticeScanRefused     = "parent notice scan refused"
	noticeWatchRefused    = "parent notice watch refused"
	parentNoticeMetric    = "dev_env_parent_notice_patch_refused_total"
	parentNoticeLineLimit = 400
)

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

// ParentNoticeLine is the only line a wake notice types. child is the child
// session's name and name is the note's name. The line holds neither the
// note text, nor its kind, nor its key.
func ParentNoticeLine(child, name string) (string, bool) {
	if len(validation.IsDNS1123Subdomain(child)) != 0 || !inbox.ValidMessageName(name) {
		return "", false
	}
	line := "[dev-env notice] a note is waiting from " + child + ": agent-run parent read " + name
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
	Deliver    func(context.Context, *v1alpha1.AgentSession, *corev1.Pod, string) error
}

// Pass delivers at most one unmarked note to parentName, or nothing.
func (p *ParentNotices) Pass(ctx context.Context, parentName string) {
	if p == nil || !p.Enabled || parentName == "" || p.GetSession == nil {
		return
	}
	sess, err := p.GetSession(ctx, parentName)
	if err != nil || !claudeTUI(sess) {
		return
	}
	if p.GetPod == nil {
		return
	}
	pod, err := p.GetPod(ctx, sess)
	if err != nil || pod == nil || !parentReady(sess, pod, p.now()) {
		return
	}
	id := coord.SessionIdentity{Namespace: sess.Namespace, Name: sess.Name, UID: string(sess.UID)}
	if p.List == nil {
		return
	}
	notes, err := p.List(ctx, id)
	if err != nil {
		p.Log.Info(noticeListRefused, "parent", parentName)
		return
	}
	now := p.now()
	if noticeTooSoon(notes, now) {
		return
	}
	note, line, ok := oldestNotice(notes, id)
	if !ok {
		return
	}
	if p.Patch == nil || p.Deliver == nil {
		return
	}
	if err := p.Patch(ctx, note.DeepCopy(), now); err != nil {
		p.Log.Info(noticePatchRefused, "note", note.Name)
		if p.Refused != nil {
			p.Refused.Inc()
		}
		return
	}
	if err := p.Deliver(ctx, sess, pod, line); err != nil {
		p.Log.Info(noticeDeliveryFailed, "note", note.Name)
	}
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

// parentReady is the gate in rulings 6 and the Claude-TUI rule. A hold leaves
// the note unmarked. The heartbeat bound is the operator's existing one.
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
	if a == nil || a.LastHeartbeat == nil || a.Status == "" || a.Status == protocol.AgentWaiting {
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

func oldestNotice(notes []coord.InboxMessage, parent coord.SessionIdentity) (*coord.InboxMessage, string, bool) {
	var best *coord.InboxMessage
	var line string
	for i := range notes {
		n := &notes[i]
		if !unmarkedNotice(n, parent) {
			continue
		}
		candidate, ok := ParentNoticeLine(n.Spec.Sender.Name, n.Name)
		if !ok {
			continue
		}
		if best == nil || n.CreationTimestamp.Time.Before(best.CreationTimestamp.Time) ||
			(n.CreationTimestamp.Time.Equal(best.CreationTimestamp.Time) && n.Name < best.Name) {
			best = n
			line = candidate
		}
	}
	if best == nil {
		return nil, "", false
	}
	return best, line, true
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
// notes on start and again after a watch gap, and it looks on each session
// status change and each new note. The flag off means it is not added.
type ParentNoticeRunner struct {
	Enabled          bool
	SessionNamespace string
	Notes            *inbox.KubeStore
	Live             client.Reader
	Watcher          client.WithWatch
	Exec             PodExecutor
	Log              logr.Logger
	Refused          NoticeRefusedCounter
	Now              func() time.Time

	GetSession    func(context.Context, string) (*v1alpha1.AgentSession, error)
	GetPod        func(context.Context, *v1alpha1.AgentSession) (*corev1.Pod, error)
	List          func(context.Context, coord.SessionIdentity) ([]coord.InboxMessage, error)
	Patch         func(context.Context, *coord.InboxMessage, time.Time) error
	Deliver       func(context.Context, *v1alpha1.AgentSession, *corev1.Pod, string) error
	ScanParents   func(context.Context) ([]string, error)
	WatchSessions func(context.Context) (watch.Interface, error)
	WatchNotes    func(context.Context) (watch.Interface, error)

	mu sync.Mutex
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
		GetSession: r.getSession, GetPod: r.getPod, List: r.list, Patch: r.patch, Deliver: r.deliver,
	}
}

func (r *ParentNoticeRunner) passName(ctx context.Context, name string) {
	if name == "" || ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notices().Pass(ctx, name)
}

func (r *ParentNoticeRunner) scan(ctx context.Context) {
	names, err := r.parentNames(ctx)
	if err != nil {
		r.Log.Info(noticeScanRefused)
		return
	}
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
	return r.Notes.NoticeNotes(ctx, party)
}

func (r *ParentNoticeRunner) patch(ctx context.Context, note *coord.InboxMessage, at time.Time) error {
	if r.Patch != nil {
		return r.Patch(ctx, note, at)
	}
	return r.Notes.MarkParentNotice(ctx, note, at)
}

func (r *ParentNoticeRunner) deliver(ctx context.Context, sess *v1alpha1.AgentSession, pod *corev1.Pod, line string) error {
	if r.Deliver != nil {
		return r.Deliver(ctx, sess, pod, line)
	}
	if r.Exec == nil || pod == nil || line == "" {
		return errors.New("parent notice has no exec")
	}
	ctx, cancel := context.WithTimeout(ctx, podCmdTimeout)
	defer cancel()
	stderr := &limitedBuffer{max: 4 << 10}
	args := []string{"agentd", "ctl", "notice", "--expected-pod-uid", string(pod.UID), "--expected-session-uid", string(sess.UID)}
	return r.Exec.Run(ctx, pod.Namespace, pod.Name, controller.ContainerName, args, strings.NewReader(line+"\n"), io.Discard, stderr)
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
