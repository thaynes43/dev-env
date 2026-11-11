package apiserver

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/scheduledwork/authority"
)

const (
	noticeText = "NOTE-TEXT-MUST-NOT-BE-TYPED"
	noticeKey  = "secret-key-must-not-be-typed"
)

func noticeName(digit string) string {
	return "message." + strings.Repeat(digit, 32) + "." + strings.Repeat("ab", 16)
}

type noticeCount struct{ n atomic.Int64 }

func (c *noticeCount) Inc() { c.n.Add(1) }

type noticeLog struct {
	mu   sync.Mutex
	msgs []string
}

func (s *noticeLog) Init(logr.RuntimeInfo)          {}
func (s *noticeLog) Enabled(int) bool               { return true }
func (s *noticeLog) Error(error, string, ...any)    {}
func (s *noticeLog) WithValues(...any) logr.LogSink { return s }
func (s *noticeLog) WithName(string) logr.LogSink   { return s }
func (s *noticeLog) Info(_ int, msg string, kv ...any) {
	s.mu.Lock()
	s.msgs = append(s.msgs, msg)
	for i := 0; i+1 < len(kv); i += 2 {
		if text, ok := kv[i+1].(string); ok {
			s.msgs = append(s.msgs, text)
		}
	}
	s.mu.Unlock()
}
func (s *noticeLog) joined() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.msgs, "\n")
}

type noticeFix struct {
	mu         sync.Mutex
	now        time.Time
	sess       *v1alpha1.AgentSession
	pod        *corev1.Pod
	notes      []coord.InboxMessage
	lines      []string
	patches    int
	patchErr   error
	deliverErr error
	refused    noticeCount
	log        noticeLog
}

func newNoticeFix() *noticeFix {
	now := time.Date(2026, 11, 11, 5, 0, 0, 0, time.UTC)
	f := &noticeFix{now: now}
	f.sess = &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: "parent-1", UID: "parent-uid"},
		Spec: v1alpha1.AgentSessionSpec{
			Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeRemote, OperatingMode: v1alpha1.OperatingModeRunning,
		},
		Status: v1alpha1.AgentSessionStatus{
			Phase: v1alpha1.PhaseRunning,
			Agent: &v1alpha1.AgentStatus{Status: protocol.AgentIdle, LastHeartbeat: &metav1.Time{Time: now}},
		},
	}
	yes := true
	f.pod = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: f.sess.Namespace, Name: f.sess.Name, UID: "pod-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentSession",
				Name: f.sess.Name, UID: f.sess.UID, Controller: &yes,
			}},
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	f.notes = []coord.InboxMessage{f.note(inbox.KindQuestion, noticeKey, noticeText, now.Add(-time.Hour), "1", noticeName("c"))}
	return f
}

func (f *noticeFix) note(kind, key, text string, at time.Time, rv, name string) coord.InboxMessage {
	return coord.InboxMessage{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "private-coordination", Name: name, UID: types.UID("uid-" + name[len(name)-2:]),
			ResourceVersion: rv, CreationTimestamp: metav1.NewTime(at),
		},
		Spec: coord.InboxMessageSpec{
			Kind: kind, Key: key, Text: text,
			Sender:    coord.SessionIdentity{Namespace: "dev-agents", Name: "child-1", UID: "child-uid"},
			Recipient: coord.SessionIdentity{Namespace: f.sess.Namespace, Name: f.sess.Name, UID: string(f.sess.UID)},
		},
	}
}

func (f *noticeFix) parent() coord.SessionIdentity {
	return coord.SessionIdentity{Namespace: f.sess.Namespace, Name: f.sess.Name, UID: string(f.sess.UID)}
}

func (f *noticeFix) notices() ParentNotices {
	return ParentNotices{
		Enabled: true,
		Now: func() time.Time {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.now
		},
		Log:     logr.New(&f.log),
		Refused: &f.refused,
		GetSession: func(context.Context, string) (*v1alpha1.AgentSession, error) {
			return f.sess, nil
		},
		GetPod: func(context.Context, *v1alpha1.AgentSession) (*corev1.Pod, error) {
			return f.pod, nil
		},
		List:    f.list,
		Patch:   f.patch,
		Deliver: f.deliver,
	}
}

func (f *noticeFix) list(_ context.Context, id coord.SessionIdentity) ([]coord.InboxMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != f.parent() {
		return nil, errors.New("wrong parent")
	}
	out := make([]coord.InboxMessage, len(f.notes))
	for i := range f.notes {
		out[i] = *f.notes[i].DeepCopy()
	}
	return out, nil
}

func (f *noticeFix) patch(_ context.Context, note *coord.InboxMessage, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patches++
	if f.patchErr != nil {
		return f.patchErr
	}
	for i := range f.notes {
		if f.notes[i].Name != note.Name {
			continue
		}
		if f.notes[i].ResourceVersion != note.ResourceVersion {
			return apierrors.NewConflict(schema.GroupResource{Resource: "inboxmessages"}, note.Name, errors.New("resourceVersion"))
		}
		if f.notes[i].Annotations == nil {
			f.notes[i].Annotations = map[string]string{}
		}
		f.notes[i].Annotations[inbox.ParentNoticeAnnotation] = at.UTC().Format(time.RFC3339Nano)
		f.notes[i].ResourceVersion = f.notes[i].ResourceVersion + "b"
		return nil
	}
	return errors.New("missing note")
}

func (f *noticeFix) deliver(_ context.Context, _ *v1alpha1.AgentSession, _ *corev1.Pod, line string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lines = append(f.lines, line)
	return f.deliverErr
}

func (f *noticeFix) pass() {
	p := f.notices()
	p.Pass(context.Background(), f.sess.Name)
}

func (f *noticeFix) marked(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.notes {
		if f.notes[i].Name == name {
			_, ok := f.notes[i].Annotations[inbox.ParentNoticeAnnotation]
			return ok
		}
	}
	return false
}

func TestParentNoticeHeldWhileWaitingThenSentOnce(t *testing.T) {
	f := newNoticeFix()
	f.sess.Status.Agent.Status = protocol.AgentWaiting
	f.pass()
	if len(f.lines) != 0 || f.patches != 0 || f.marked(f.notes[0].Name) {
		t.Fatalf("waiting delivered or marked: lines %q patches %d", f.lines, f.patches)
	}
	f.sess.Status.Agent.Status = protocol.AgentIdle
	f.pass()
	want, ok := ParentNoticeLine("child-1", f.notes[0].Name)
	if !ok || len(f.lines) != 1 || f.lines[0] != want || !f.marked(f.notes[0].Name) {
		t.Fatalf("idle: lines %q marked %v want %q", f.lines, f.marked(f.notes[0].Name), want)
	}
	if strings.Contains(f.lines[0], noticeText) || strings.Contains(f.lines[0], noticeKey) || strings.Contains(f.lines[0], "question") || strings.Contains(f.lines[0], "[Message from") {
		t.Fatalf("line holds more than the notice: %q", f.lines[0])
	}
	f.pass()
	if len(f.lines) != 1 || f.patches != 1 {
		t.Fatalf("sent again: lines %q patches %d", f.lines, f.patches)
	}
}

func TestParentNoticeFreshOperatorKeepsMarkAndUnmarked(t *testing.T) {
	f := newNoticeFix()
	marked := f.note(inbox.KindResult, "older-key", "older text", f.now.Add(-2*time.Hour), "9", noticeName("d"))
	marked.Annotations = map[string]string{inbox.ParentNoticeAnnotation: f.now.Add(-2 * time.Minute).Format(time.RFC3339Nano)}
	f.notes = append(f.notes, marked)
	f.pass()
	if len(f.lines) != 1 || strings.Contains(f.lines[0], noticeName("d")) || !strings.Contains(f.lines[0], f.notes[0].Name) {
		t.Fatalf("fresh pass: %q", f.lines)
	}
	if !f.marked(noticeName("d")) || !f.marked(f.notes[0].Name) {
		t.Fatal("mark was dropped or the unmarked note was not sent")
	}
	again := f.notices()
	again.Pass(context.Background(), f.sess.Name)
	if len(f.lines) != 1 {
		t.Fatalf("a fresh operator resent: %q", f.lines)
	}
}

func TestParentNoticeSkipsCodexHeadlessAndCaller(t *testing.T) {
	f := newNoticeFix()
	f.sess.Spec.Agent = v1alpha1.AgentCodex
	f.sess.Spec.Mode = v1alpha1.ModeRemote
	f.pass()
	if len(f.lines) != 0 || f.patches != 0 || f.marked(f.notes[0].Name) {
		t.Fatal("codex got a notice")
	}
	f = newNoticeFix()
	f.sess.Spec.Mode = v1alpha1.ModeTask
	f.pass()
	if len(f.lines) != 0 || f.patches != 0 || f.marked(f.notes[0].Name) {
		t.Fatal("headless got a notice")
	}
	f = newNoticeFix()
	f.notes[0].Spec.Recipient = coord.SessionIdentity{}
	f.notes[0].Spec.CallerRecipient = &coord.InboxCaller{ServiceAccount: coord.MeatballServiceAccount, PolicyUID: "policy-uid"}
	f.pass()
	if len(f.lines) != 0 || f.patches != 0 {
		t.Fatal("caller parent got a notice")
	}
}

func TestParentNoticeReplyAndPlainTriggerNothing(t *testing.T) {
	for _, kind := range []string{inbox.KindReply, ""} {
		f := newNoticeFix()
		f.notes[0].Spec.Kind = kind
		f.pass()
		if len(f.lines) != 0 || f.patches != 0 || f.marked(f.notes[0].Name) {
			t.Fatalf("kind %q triggered a notice", kind)
		}
	}
}

func TestParentNoticeStaleHeartbeatHolds(t *testing.T) {
	f := newNoticeFix()
	f.sess.Status.Agent.LastHeartbeat = &metav1.Time{Time: f.now.Add(-authority.DefaultHeartbeatStaleAfter - time.Second)}
	f.pass()
	if len(f.lines) != 0 || f.patches != 0 || f.marked(f.notes[0].Name) {
		t.Fatal("stale heartbeat delivered")
	}
	f.sess.Status.Agent.LastHeartbeat = nil
	f.pass()
	if len(f.lines) != 0 || f.marked(f.notes[0].Name) {
		t.Fatal("missing heartbeat delivered")
	}
	f.sess.Status.Agent.LastHeartbeat = &metav1.Time{Time: f.now.Add(time.Second)}
	f.pass()
	if len(f.lines) != 0 || f.marked(f.notes[0].Name) {
		t.Fatal("future heartbeat delivered")
	}
	f.sess.Status.Agent.Status = ""
	f.sess.Status.Agent.LastHeartbeat = &metav1.Time{Time: f.now}
	f.pass()
	if len(f.lines) != 0 || f.marked(f.notes[0].Name) {
		t.Fatal("missing state delivered")
	}
	f.sess.Status.Agent.Status = protocol.AgentIdle
	f.sess.Status.Agent.LastHeartbeat = &metav1.Time{Time: f.now.Add(-authority.DefaultHeartbeatStaleAfter)}
	f.pass()
	if len(f.lines) != 1 || !f.marked(f.notes[0].Name) {
		t.Fatal("a report at the staleness bound was held")
	}
}

func TestParentNoticeSuspendedHoldsUntilRunning(t *testing.T) {
	f := newNoticeFix()
	f.sess.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	f.sess.Status.Phase = v1alpha1.PhaseSuspended
	f.pass()
	if len(f.lines) != 0 || f.patches != 0 || f.marked(f.notes[0].Name) {
		t.Fatal("suspended parent got a notice")
	}
	f.sess.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	f.sess.Status.Phase = v1alpha1.PhaseRunning
	f.pass()
	if len(f.lines) != 1 || !f.marked(f.notes[0].Name) {
		t.Fatal("parent was not served after it ran again")
	}
	f = newNoticeFix()
	f.pod.Status.Conditions[0].Status = corev1.ConditionFalse
	f.pass()
	if len(f.lines) != 0 || f.marked(f.notes[0].Name) {
		t.Fatal("a pod that is not Ready got a notice")
	}
	f.pod.Status.Conditions[0].Status = corev1.ConditionTrue
	f.pod.Labels = map[string]string{v1alpha1.LabelHold: "true"}
	f.pass()
	if len(f.lines) != 0 || f.marked(f.notes[0].Name) {
		t.Fatal("a hold pod got a notice")
	}
}

func TestParentNoticeOnePerMinuteOldestFirst(t *testing.T) {
	f := newNoticeFix()
	older := f.note(inbox.KindBlocker, "old-key", "old text", f.now.Add(-2*time.Hour), "1", noticeName("e"))
	newer := f.note(inbox.KindResult, "new-key", "new text", f.now.Add(-time.Hour), "1", noticeName("f"))
	f.notes = []coord.InboxMessage{newer, older}
	f.pass()
	if len(f.lines) != 1 || !strings.Contains(f.lines[0], noticeName("e")) || strings.Contains(f.lines[0], "old text") || strings.Contains(f.lines[0], "new text") {
		t.Fatalf("first pass: %q", f.lines)
	}
	f.pass()
	if len(f.lines) != 1 || f.marked(noticeName("f")) {
		t.Fatalf("burst: %q marked newer %v", f.lines, f.marked(noticeName("f")))
	}
	f.mu.Lock()
	f.now = f.now.Add(parentNoticeGap)
	f.mu.Unlock()
	f.pass()
	if len(f.lines) != 2 || !strings.Contains(f.lines[1], noticeName("f")) {
		t.Fatalf("after a minute: %q", f.lines)
	}
}

func TestParentNoticeRaceDeliversOnce(t *testing.T) {
	f := newNoticeFix()
	var mu sync.Mutex
	rv := "1"
	f.notes[0].ResourceVersion = "1"
	var deliveries atomic.Int32
	p := f.notices()
	p.List = func(context.Context, coord.SessionIdentity) ([]coord.InboxMessage, error) {
		n := *f.notes[0].DeepCopy()
		n.ResourceVersion = "1"
		n.Annotations = nil
		return []coord.InboxMessage{n}, nil
	}
	p.Patch = func(_ context.Context, note *coord.InboxMessage, _ time.Time) error {
		mu.Lock()
		defer mu.Unlock()
		if note.ResourceVersion != rv {
			return apierrors.NewConflict(schema.GroupResource{Resource: "inboxmessages"}, note.Name, errors.New("resourceVersion"))
		}
		rv = "2"
		return nil
	}
	p.Deliver = func(context.Context, *v1alpha1.AgentSession, *corev1.Pod, string) error {
		deliveries.Add(1)
		return nil
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); p.Pass(context.Background(), f.sess.Name) }()
	go func() { defer wg.Done(); p.Pass(context.Background(), f.sess.Name) }()
	wg.Wait()
	if deliveries.Load() != 1 || f.refused.n.Load() != 1 {
		t.Fatalf("deliveries %d refused %d", deliveries.Load(), f.refused.n.Load())
	}
}

func TestParentNoticeDeliveryErrorIsNotRetried(t *testing.T) {
	f := newNoticeFix()
	f.deliverErr = errors.New("tmux paste failed")
	f.pass()
	if len(f.lines) != 1 || !f.marked(f.notes[0].Name) || f.refused.n.Load() != 0 {
		t.Fatalf("lines %q marked %v refused %d", f.lines, f.marked(f.notes[0].Name), f.refused.n.Load())
	}
	f.deliverErr = nil
	f.pass()
	if len(f.lines) != 1 || f.patches != 1 {
		t.Fatalf("retried after the mark: lines %d patches %d", len(f.lines), f.patches)
	}
}

func TestParentNoticeRefusedPatchDeliversNothing(t *testing.T) {
	for _, err := range []error{
		apierrors.NewForbidden(schema.GroupResource{Group: coord.GroupVersion.Group, Resource: "inboxmessages"}, noticeName("c"), errors.New("forbidden")),
		errors.New("patch failed"),
	} {
		f := newNoticeFix()
		f.patchErr = err
		f.pass()
		if len(f.lines) != 0 || f.marked(f.notes[0].Name) || f.refused.n.Load() != 1 {
			t.Fatalf("%v: lines %q marked %v refused %d", err, f.lines, f.marked(f.notes[0].Name), f.refused.n.Load())
		}
		if !strings.Contains(f.log.joined(), noticePatchRefused) || strings.Contains(f.log.joined(), noticeText) || strings.Contains(f.log.joined(), noticeKey) {
			t.Fatalf("log %q", f.log.joined())
		}
	}
}

func TestParentNoticeFlagOffDoesNothing(t *testing.T) {
	f := newNoticeFix()
	p := f.notices()
	p.Enabled = false
	p.List = func(context.Context, coord.SessionIdentity) ([]coord.InboxMessage, error) {
		t.Error("flag off listed notes")
		return nil, nil
	}
	p.Pass(context.Background(), f.sess.Name)
	if len(f.lines) != 0 || f.patches != 0 {
		t.Fatal("flag off delivered")
	}
}

func TestParentNoticeRunnerStatusChangeAndStoredNote(t *testing.T) {
	f := newNoticeFix()
	delivered := make(chan string, 2)
	p := f.notices()
	p.Deliver = func(_ context.Context, _ *v1alpha1.AgentSession, _ *corev1.Pod, line string) error {
		if err := f.deliver(context.Background(), nil, nil, line); err != nil {
			return err
		}
		delivered <- line
		return nil
	}
	sessions := make(chan watch.Event, 1)
	notes := make(chan watch.Event, 1)
	opened := make(chan struct{})
	var once sync.Once
	r := &ParentNoticeRunner{
		Enabled: true, Now: p.Now, Log: p.Log, Refused: p.Refused,
		GetSession: p.GetSession, GetPod: p.GetPod, List: p.List, Patch: p.Patch, Deliver: p.Deliver,
		ScanParents: func(context.Context) ([]string, error) { return nil, nil },
		WatchSessions: func(context.Context) (watch.Interface, error) {
			once.Do(func() { close(opened) })
			return &chanWatch{ch: sessions}, nil
		},
		WatchNotes: func(context.Context) (watch.Interface, error) {
			return &chanWatch{ch: notes}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = r.Start(ctx); close(done) }()
	select {
	case <-opened:
	case <-time.After(2 * time.Second):
		t.Fatal("session watch did not open")
	}
	sessions <- watch.Event{Type: watch.Modified, Object: f.sess.DeepCopy()}
	select {
	case line := <-delivered:
		if strings.Contains(line, noticeText) || !strings.Contains(line, "child-1") {
			t.Fatalf("status change typed %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("status change did not deliver")
	}
	second := f.note(inbox.KindResult, "second-key", "second text", f.now.Add(-time.Minute), "1", noticeName("d"))
	second.Labels = map[string]string{inbox.ParentNoteLabel: "true"}
	f.mu.Lock()
	f.notes = append(f.notes, second)
	f.now = f.now.Add(parentNoticeGap)
	f.mu.Unlock()
	notes <- watch.Event{Type: watch.Added, Object: second.DeepCopy()}
	select {
	case line := <-delivered:
		if !strings.Contains(line, noticeName("d")) || strings.Contains(line, "second text") {
			t.Fatalf("stored note typed %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stored note did not deliver")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop")
	}
}

func TestParentNoticeRunnerFlagOffDoesNotScan(t *testing.T) {
	scanned := false
	r := &ParentNoticeRunner{Enabled: false, ScanParents: func(context.Context) ([]string, error) {
		scanned = true
		return nil, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Start(ctx); err != nil || scanned {
		t.Fatal(err, scanned)
	}
}

type noticeExec struct {
	cmd                []string
	stdin              string
	ns, pod, container string
}

func (e *noticeExec) Run(_ context.Context, ns, pod, container string, cmd []string, stdin io.Reader, _, _ io.Writer) error {
	e.ns, e.pod, e.container = ns, pod, container
	e.cmd = append([]string(nil), cmd...)
	b, err := io.ReadAll(stdin)
	e.stdin = string(b)
	return err
}

func TestParentNoticeExecTypesOnlyTheFixedLine(t *testing.T) {
	f := newNoticeFix()
	line, ok := ParentNoticeLine("child-1", f.notes[0].Name)
	if !ok {
		t.Fatal("line")
	}
	ex := &noticeExec{}
	r := &ParentNoticeRunner{Exec: ex}
	if err := r.deliver(context.Background(), f.sess, f.pod, line); err != nil {
		t.Fatal(err)
	}
	if ex.ns != f.pod.Namespace || ex.pod != f.pod.Name || ex.container != controller.ContainerName {
		t.Fatalf("exec target %s/%s %s", ex.ns, ex.pod, ex.container)
	}
	if strings.Join(ex.cmd, " ") != "agentd ctl notice --expected-pod-uid pod-uid --expected-session-uid parent-uid" {
		t.Fatalf("cmd %q", ex.cmd)
	}
	if ex.stdin != line+"\n" || strings.Contains(ex.stdin, noticeText) || strings.Contains(ex.stdin, noticeKey) || strings.Contains(ex.stdin, "[Message from") {
		t.Fatalf("stdin %q", ex.stdin)
	}
}

type chanWatch struct{ ch chan watch.Event }

func (c *chanWatch) Stop()                          {}
func (c *chanWatch) ResultChan() <-chan watch.Event { return c.ch }
