package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
	patched    []string
	patches    int
	patchErr   error
	failPatch  string
	probeErr   error
	probes     int
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
		Probe:   f.probe,
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

func (f *noticeFix) probe(ctx context.Context, _ *v1alpha1.AgentSession, _ *corev1.Pod) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes++
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("probe has no deadline")
	}
	return f.probeErr
}

func (f *noticeFix) patch(_ context.Context, note *coord.InboxMessage, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patches++
	f.patched = append(f.patched, note.Name)
	if f.patchErr != nil {
		return f.patchErr
	}
	if f.failPatch != "" && f.failPatch == note.Name {
		return errors.New("refused")
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
	want, ok := ParentNoticeLine("child-1", f.notes[0].Name, 1)
	if !ok || len(f.lines) != 1 || f.lines[0] != want || !f.marked(f.notes[0].Name) {
		t.Fatalf("idle: lines %q marked %v want %q", f.lines, f.marked(f.notes[0].Name), want)
	}
	if strings.Contains(f.lines[0], noticeText) || strings.Contains(f.lines[0], noticeKey) || strings.Contains(f.lines[0], "[Message from") {
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
	f.sess.Status.Phase = v1alpha1.PhaseRunning
	f.pass()
	if len(f.lines) != 0 || f.patches != 0 || f.marked(f.notes[0].Name) {
		t.Fatal("a suspended operating mode with a running phase got a notice")
	}
	f = newNoticeFix()
	f.sess.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	f.sess.Status.Phase = v1alpha1.PhaseSuspended
	f.pass()
	if len(f.lines) != 0 || f.patches != 0 || f.marked(f.notes[0].Name) {
		t.Fatal("a suspended phase with a running operating mode got a notice")
	}
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

func TestParentNoticeOneLineForTheBacklog(t *testing.T) {
	f := newNoticeFix()
	older := f.note(inbox.KindBlocker, "old-key", "old text", f.now.Add(-2*time.Hour), "1", noticeName("e"))
	newer := f.note(inbox.KindResult, "new-key", "new text", f.now.Add(-time.Hour), "1", noticeName("f"))
	f.notes = []coord.InboxMessage{newer, older}
	f.pass()
	want, ok := ParentNoticeLine("child-1", older.Name, 2)
	if !ok || len(f.lines) != 1 || f.lines[0] != want || !f.marked(noticeName("e")) || !f.marked(noticeName("f")) {
		t.Fatalf("one line: %q marked older %v newer %v", f.lines, f.marked(noticeName("e")), f.marked(noticeName("f")))
	}
	if strings.Contains(f.lines[0], "old text") || strings.Contains(f.lines[0], "new text") || strings.Contains(f.lines[0], noticeName("e")) {
		t.Fatalf("plural line names a note or its text: %q", f.lines[0])
	}
	if len(f.patched) != 2 || f.patched[0] != noticeName("e") || f.patched[1] != noticeName("f") {
		t.Fatalf("mark order %v", f.patched)
	}
	f.pass()
	if len(f.lines) != 1 || f.patches != 2 {
		t.Fatalf("sent again: lines %q patches %d", f.lines, f.patches)
	}

	f = newNoticeFix()
	older = f.note(inbox.KindBlocker, "old-key", "old text", f.now.Add(-2*time.Hour), "1", noticeName("e"))
	newer = f.note(inbox.KindResult, "new-key", "new text", f.now.Add(-time.Hour), "1", noticeName("f"))
	f.notes = []coord.InboxMessage{newer, older}
	f.failPatch = noticeName("e")
	f.pass()
	if len(f.lines) != 0 || f.marked(noticeName("e")) || f.marked(noticeName("f")) || f.refused.n.Load() != 1 {
		t.Fatalf("first refusal typed or marked: lines %q refused %d", f.lines, f.refused.n.Load())
	}

	f = newNoticeFix()
	older = f.note(inbox.KindBlocker, "old-key", "old text", f.now.Add(-2*time.Hour), "1", noticeName("e"))
	newer = f.note(inbox.KindResult, "new-key", "new text", f.now.Add(-time.Hour), "1", noticeName("f"))
	f.notes = []coord.InboxMessage{newer, older}
	f.failPatch = noticeName("f")
	f.pass()
	one, ok := ParentNoticeLine("child-1", noticeName("e"), 1)
	if !ok || len(f.lines) != 1 || f.lines[0] != one || !f.marked(noticeName("e")) || f.marked(noticeName("f")) {
		t.Fatalf("later refusal: lines %q older %v newer %v", f.lines, f.marked(noticeName("e")), f.marked(noticeName("f")))
	}
}

func TestParentNoticeRaceDeliversOnce(t *testing.T) {
	f := newNoticeFix()
	f.notes[0].ResourceVersion = "1"
	store := noticeStore(t)
	var mu sync.Mutex
	rv := "1"
	var sawResourceVersion bool
	store.Client = interceptor.NewClient(store.Client.(client.WithWatch), interceptor.Funcs{
		Patch: func(_ context.Context, _ client.WithWatch, obj client.Object, patch client.Patch, _ ...client.PatchOption) error {
			data, err := patch.Data(obj)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), `"spec"`) || strings.Contains(string(data), noticeText) {
				return errors.New("patch carried more than the mark")
			}
			var body struct {
				Metadata struct {
					ResourceVersion string            `json:"resourceVersion"`
					Annotations     map[string]string `json:"annotations"`
				} `json:"metadata"`
			}
			if json.Unmarshal(data, &body) != nil {
				return errors.New("patch body")
			}
			mu.Lock()
			defer mu.Unlock()
			if body.Metadata.ResourceVersion == "" {
				// A patch with no precondition is applied. The test then sees
				// two deliveries, which is the failure this case exists to catch.
				rv = rv + "x"
				return nil
			}
			sawResourceVersion = true
			if body.Metadata.ResourceVersion != rv || body.Metadata.Annotations[inbox.ParentNoticeAnnotation] == "" {
				return apierrors.NewConflict(schema.GroupResource{Resource: "inboxmessages"}, obj.GetName(), errors.New("resourceVersion"))
			}
			rv = rv + "b"
			return nil
		},
	})
	var deliveries atomic.Int32
	p := f.notices()
	p.List = func(context.Context, coord.SessionIdentity) ([]coord.InboxMessage, error) {
		n := *f.notes[0].DeepCopy()
		n.ResourceVersion = "1"
		n.Annotations = nil
		return []coord.InboxMessage{n}, nil
	}
	p.Patch = func(ctx context.Context, note *coord.InboxMessage, at time.Time) error {
		return store.MarkParentNotice(ctx, note, at)
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
	if !sawResourceVersion || deliveries.Load() != 1 || f.refused.n.Load() != 1 {
		t.Fatalf("rv %v deliveries %d refused %d", sawResourceVersion, deliveries.Load(), f.refused.n.Load())
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

func noticeStore(t *testing.T) inbox.KubeStore {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := coord.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			obj.SetUID(types.UID("note-uid"))
			obj.SetCreationTimestamp(metav1.Now())
			return c.Create(ctx, obj, opts...)
		},
	}).Build()
	return inbox.KubeStore{Client: c, Live: c, Namespace: "private-coordination"}
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
	sessions := make(chan watch.Event)
	notes := make(chan watch.Event)
	opened := make(chan struct{})
	var once sync.Once
	var lists atomic.Int32
	r := &ParentNoticeRunner{
		Enabled: true, Now: p.Now, Log: p.Log, Refused: p.Refused,
		GetSession: p.GetSession, GetPod: p.GetPod, Patch: p.Patch, Probe: p.Probe, Deliver: p.Deliver,
		List: func(ctx context.Context, id coord.SessionIdentity) ([]coord.InboxMessage, error) {
			lists.Add(1)
			return p.List(ctx, id)
		},
		ScanParents: func(context.Context) ([]string, error) { return []string{f.sess.Name}, nil },
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
	select {
	case line := <-delivered:
		if strings.Contains(line, noticeText) || !strings.Contains(line, "child-1") {
			t.Fatalf("start scan typed %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("start scan did not deliver")
	}
	afterScan := lists.Load()
	sessions <- watch.Event{Type: watch.Modified, Object: f.sess.DeepCopy()}
	if lists.Load() != afterScan {
		t.Fatal("a heartbeat with nothing pending listed notes")
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

type noticeCall struct {
	cmd                []string
	stdin              string
	ns, pod, container string
	deadline           time.Time
	hasDeadline        bool
}

type noticeExec struct {
	mu    sync.Mutex
	calls []noticeCall
	err   error
}

func (e *noticeExec) Run(ctx context.Context, ns, pod, container string, cmd []string, stdin io.Reader, _, _ io.Writer) error {
	call := noticeCall{cmd: append([]string(nil), cmd...), ns: ns, pod: pod, container: container}
	if stdin != nil {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		call.stdin = string(b)
	}
	if d, ok := ctx.Deadline(); ok {
		call.deadline, call.hasDeadline = d, true
	}
	e.mu.Lock()
	e.calls = append(e.calls, call)
	err := e.err
	e.mu.Unlock()
	return err
}

func TestParentNoticeExecTypesOnlyTheFixedLine(t *testing.T) {
	f := newNoticeFix()
	line, ok := ParentNoticeLine("child-1", f.notes[0].Name, 1)
	if !ok {
		t.Fatal("line")
	}
	ex := &noticeExec{}
	p := f.notices()
	r := &ParentNoticeRunner{
		Enabled: true, Exec: ex, Now: p.Now, Log: p.Log, Refused: p.Refused,
		GetSession: p.GetSession, GetPod: p.GetPod, List: p.List, Patch: p.Patch,
	}
	if !r.notices().Pass(context.Background(), f.sess.Name) {
		t.Fatal("pass did not clear")
	}
	if len(ex.calls) != 2 {
		t.Fatalf("execs %d", len(ex.calls))
	}
	probe, typed := ex.calls[0], ex.calls[1]
	if probe.ns != f.pod.Namespace || probe.pod != f.pod.Name || probe.container != controller.ContainerName || typed.container != controller.ContainerName {
		t.Fatalf("exec target %+v %+v", probe, typed)
	}
	if strings.Join(probe.cmd, " ") != "agentd ctl notice --probe --expected-pod-uid pod-uid --expected-session-uid parent-uid" || probe.stdin != "" {
		t.Fatalf("probe %q stdin %q", probe.cmd, probe.stdin)
	}
	if strings.Join(typed.cmd, " ") != "agentd ctl notice --expected-pod-uid pod-uid --expected-session-uid parent-uid" {
		t.Fatalf("cmd %q", typed.cmd)
	}
	if typed.stdin != line+"\n" || strings.Contains(typed.stdin, noticeText) || strings.Contains(typed.stdin, noticeKey) || strings.Contains(typed.stdin, "[Message from") {
		t.Fatalf("stdin %q", typed.stdin)
	}
	if !probe.hasDeadline || !typed.hasDeadline || typed.deadline.Sub(probe.deadline) > time.Second {
		t.Fatal("probe and delivery do not share one deadline", probe.deadline, typed.deadline)
	}
}

func TestParentNoticeBusyHolds(t *testing.T) {
	for _, status := range []string{protocol.AgentBusy, protocol.AgentWaiting, protocol.AgentPending, "pondering"} {
		f := newNoticeFix()
		f.sess.Status.Agent.Status = status
		f.pass()
		if len(f.lines) != 0 || f.patches != 0 || f.probes != 0 || f.marked(f.notes[0].Name) {
			t.Fatalf("%s delivered probes %d lines %q", status, f.probes, f.lines)
		}
	}
}

func TestParentNoticeProbeHoldWritesNoMark(t *testing.T) {
	f := newNoticeFix()
	f.probeErr = errors.New("busy")
	f.pass()
	if f.probes != 1 || f.patches != 0 || len(f.lines) != 0 || f.marked(f.notes[0].Name) {
		t.Fatalf("probes %d patches %d lines %q", f.probes, f.patches, f.lines)
	}
}

func TestParentNoticeSlowExecThenNewNote(t *testing.T) {
	f := newNoticeFix()
	started := time.Now()
	p := f.notices()
	p.Deliver = func(ctx context.Context, sess *v1alpha1.AgentSession, pod *corev1.Pod, line string) error {
		deadline, ok := ctx.Deadline()
		if !ok || deadline.Sub(started) > 31*time.Second || deadline.Sub(started) < 29*time.Second {
			t.Errorf("attempt deadline %s", deadline.Sub(started))
		}
		f.mu.Lock()
		f.now = f.now.Add(20 * time.Second)
		f.mu.Unlock()
		return f.deliver(ctx, sess, pod, line)
	}
	if !p.Pass(context.Background(), f.sess.Name) || len(f.lines) != 1 {
		t.Fatal(f.lines)
	}
	markedAt := f.now.Add(-20 * time.Second)
	next := f.note(inbox.KindResult, "later-key", "later text", f.now, "2", noticeName("d"))
	f.mu.Lock()
	f.notes = append(f.notes, next)
	f.mu.Unlock()
	p.Pass(context.Background(), f.sess.Name)
	if len(f.lines) != 1 || f.marked(noticeName("d")) {
		t.Fatalf("a note during the gap was typed: %q", f.lines)
	}
	f.mu.Lock()
	f.now = markedAt.Add(parentNoticeGap - time.Second)
	f.mu.Unlock()
	p.Pass(context.Background(), f.sess.Name)
	if len(f.lines) != 1 || f.marked(noticeName("d")) {
		t.Fatal("89s was enough", f.lines)
	}
	f.mu.Lock()
	f.now = markedAt.Add(parentNoticeGap)
	f.mu.Unlock()
	if !p.Pass(context.Background(), f.sess.Name) || len(f.lines) != 2 || !f.marked(noticeName("d")) || !strings.Contains(f.lines[1], noticeName("d")) || strings.Contains(f.lines[1], "later text") {
		t.Fatalf("after 90s: %q marked %v", f.lines, f.marked(noticeName("d")))
	}
}

func TestParentNoticeStopsMarkingToLeaveTimeToDeliver(t *testing.T) {
	f := newNoticeFix()
	older := f.note(inbox.KindBlocker, "old-key", "old text", f.now.Add(-2*time.Hour), "1", noticeName("e"))
	newer := f.note(inbox.KindResult, "new-key", "new text", f.now.Add(-time.Hour), "1", noticeName("f"))
	f.notes = []coord.InboxMessage{newer, older}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	p := f.notices()
	p.Patch = func(ctx context.Context, note *coord.InboxMessage, at time.Time) error {
		if note.Name == older.Name {
			time.Sleep(2 * time.Second)
		}
		return f.patch(ctx, note, at)
	}
	p.Pass(ctx, f.sess.Name)
	one, ok := ParentNoticeLine("child-1", older.Name, 1)
	if !ok || len(f.lines) != 1 || f.lines[0] != one || !f.marked(older.Name) || f.marked(newer.Name) {
		t.Fatalf("lines %q older %v newer %v", f.lines, f.marked(older.Name), f.marked(newer.Name))
	}
}

func TestParentNoticeServesALongRecipientList(t *testing.T) {
	f := newNoticeFix()
	notes := make([]coord.InboxMessage, 513)
	old := f.now.Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	for i := 0; i < 512; i++ {
		notes[i] = f.note(inbox.KindQuestion, fmt.Sprintf("k-%d", i), "text", f.now.Add(-time.Duration(i+2)*time.Hour), "1", fmt.Sprintf("message.%032x.%032x", i+1, i+1))
		notes[i].Annotations = map[string]string{inbox.ParentNoticeAnnotation: old}
	}
	notes[512] = f.note(inbox.KindResult, "open-key", noticeText, f.now.Add(-time.Hour), "1", noticeName("a"))
	f.notes = notes
	f.pass()
	want, ok := ParentNoticeLine("child-1", noticeName("a"), 1)
	if !ok || len(f.lines) != 1 || f.lines[0] != want || !f.marked(noticeName("a")) || f.patches != 1 {
		t.Fatalf("lines %q patches %d", f.lines, f.patches)
	}
}

func TestParentNoticePageBudgetIsLoggedAndServed(t *testing.T) {
	ctx := context.Background()
	s := noticeStore(t)
	spec := coord.InboxMessageSpec{
		Project: "sample", Key: "budget-note", Text: "  exact UTF-8 text\n",
		Sender:    coord.SessionIdentity{Namespace: "dev-agents", Name: "sender", UID: "sender-uid"},
		Recipient: coord.SessionIdentity{Namespace: "dev-agents", Name: "recipient", UID: "recipient-uid"},
		Actor:     coord.ClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env-agent", PodName: "sender", PodUID: "sender-pod-uid"},
		Provenance: coord.ClaimProvenance{
			CatalogRevision: strings.Repeat("a", 64), RulesRevision: strings.Repeat("b", 64),
			GitHub: "thaynes43/dev-env", DefaultBranch: "main", InitialPinState: "Unknown",
		},
		Repository: "repo", Provider: "claude", RequestedModel: "claude-opus-5-5", Kind: inbox.KindQuestion,
	}
	m, created, err := s.CreateParentNote(ctx, spec)
	if err != nil || !created {
		t.Fatal(err, created)
	}
	calls := 0
	s.Live = interceptor.NewClient(s.Live.(client.WithWatch), interceptor.Funcs{List: func(_ context.Context, _ client.WithWatch, list client.ObjectList, _ ...client.ListOption) error {
		calls++
		if calls > inbox.NoticePageBudget+1 {
			t.Fatal("read past the page budget")
		}
		page := list.(*coord.InboxMessageList)
		page.Items = []coord.InboxMessage{*m.DeepCopy()}
		page.Continue = "more"
		return nil
	}})
	var log noticeLog
	var pages noticeCount
	r := &ParentNoticeRunner{Notes: &s, Log: logr.New(&log), Pages: &pages}
	notes, err := r.list(ctx, spec.Recipient)
	if err != nil || len(notes) != inbox.NoticePageBudget || calls != inbox.NoticePageBudget || pages.n.Load() != 1 {
		t.Fatalf("notes %d calls %d counted %d err %v", len(notes), calls, pages.n.Load(), err)
	}
	if !strings.Contains(log.joined(), noticePageBudgetHit) || strings.Contains(log.joined(), spec.Text) || strings.Contains(log.joined(), spec.Key) {
		t.Fatalf("log %q", log.joined())
	}
}

type chanWatch struct{ ch chan watch.Event }

func (c *chanWatch) Stop()                          {}
func (c *chanWatch) ResultChan() <-chan watch.Event { return c.ch }
