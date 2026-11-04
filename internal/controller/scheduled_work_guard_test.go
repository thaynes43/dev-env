package controller

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
)

// The scheduled-work releaser's guard, judged without an API server: the
// finalizer goes only from a session that carries it, has ended or is being
// deleted, and only for the reason the reads show.
func TestScheduledWorkReleaseGuardTable(t *testing.T) {
	now := time.Date(2026, 12, 3, 15, 0, 0, 0, time.UTC)
	at := time.Date(2026, 12, 3, 14, 0, 0, 0, time.UTC)
	const name = "sw-0123456789ab-202612031400"
	session := func(edit func(*v1alpha1.AgentSession)) *v1alpha1.AgentSession {
		// Suspended: ended by the session-end rule.
		s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name,
			Labels:      map[string]string{v1alpha1.LabelScheduledWork: "item-uid"},
			Annotations: map[string]string{v1alpha1.AnnotationScheduledWorkStartDeadline: now.Add(-time.Minute).Format(time.RFC3339)},
			Finalizers:  []string{Finalizer, v1alpha1.ScheduledWorkFinalizer}},
			Spec: v1alpha1.AgentSessionSpec{Mode: v1alpha1.ModeTask, OperatingMode: v1alpha1.OperatingModeSuspended}}
		if edit != nil {
			edit(s)
		}
		return s
	}
	work := func(uid string, occ ...v1alpha1.ScheduledWorkOccurrence) *v1alpha1.ScheduledWork {
		w := &v1alpha1.ScheduledWork{ObjectMeta: metav1.ObjectMeta{UID: "item-uid"}}
		if uid != "" {
			w.UID = types.UID(uid)
		}
		w.Status.Occurrences = occ
		return w
	}
	occ := func(state, session string) v1alpha1.ScheduledWorkOccurrence {
		return v1alpha1.ScheduledWorkOccurrence{At: metav1.NewTime(at), State: state, Session: session}
	}
	deleting := func(s *v1alpha1.AgentSession) {
		d := metav1.NewTime(now)
		s.DeletionTimestamp = &d
	}
	running := func(s *v1alpha1.AgentSession) {
		s.Spec.OperatingMode, s.Status.Phase = v1alpha1.OperatingModeRunning, v1alpha1.PhaseRunning
	}
	hb := metav1.NewTime(now.Add(-time.Minute))
	agentExited := func(s *v1alpha1.AgentSession) {
		s.Status.Agent = &v1alpha1.AgentStatus{Boot: "ready", Status: "exited", LastHeartbeat: &hb}
	}
	both := func(fs ...func(*v1alpha1.AgentSession)) func(*v1alpha1.AgentSession) {
		return func(s *v1alpha1.AgentSession) {
			for _, f := range fs {
				f(s)
			}
		}
	}
	oldPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}}
	for _, c := range []struct {
		name  string
		s     *v1alpha1.AgentSession
		ev    scheduledWorkRelease
		allow bool
	}{
		{"recorded, terminal", session(nil), scheduledWorkRelease{why: swReleaseRecorded, read: itemFound, work: work("", occ(v1alpha1.OccurrenceSucceeded, name))}, true},
		{"recorded while being deleted", session(both(running, deleting)), scheduledWorkRelease{why: swReleaseRecorded, read: itemFound, work: work("", occ(v1alpha1.OccurrenceFailed, name))}, true},
		{"recorded, still running", session(nil), scheduledWorkRelease{why: swReleaseRecorded, read: itemFound, work: work("", occ(v1alpha1.OccurrenceRunning, name))}, false},
		{"recorded, parked is not terminal", session(nil), scheduledWorkRelease{why: swReleaseRecorded, read: itemFound, work: work("", occ(v1alpha1.OccurrenceParked, name))}, false},
		{"recorded on another item", session(nil), scheduledWorkRelease{why: swReleaseRecorded, read: itemFound, work: work("other-uid", occ(v1alpha1.OccurrenceSucceeded, name))}, false},
		{"recorded with no item read", session(nil), scheduledWorkRelease{why: swReleaseRecorded, read: itemMissing}, false},
		{"session has not ended", session(running), scheduledWorkRelease{why: swReleaseRecorded, read: itemFound, work: work("", occ(v1alpha1.OccurrenceSucceeded, name))}, false},
		{"an agent end the pod proves", session(both(running, agentExited)), scheduledWorkRelease{why: swReleaseRecorded, read: itemFound, work: work("", occ(v1alpha1.OccurrenceSucceeded, name)), pod: oldPod}, true},
		{"an agent end with no pod to prove it", session(both(running, agentExited)), scheduledWorkRelease{why: swReleaseRecorded, read: itemFound, work: work("", occ(v1alpha1.OccurrenceSucceeded, name))}, false},
		{"session without the finalizer", session(func(s *v1alpha1.AgentSession) { s.Finalizers = []string{Finalizer} }),
			scheduledWorkRelease{why: swReleaseRecorded, read: itemFound, work: work("", occ(v1alpha1.OccurrenceSucceeded, name))}, false},
		{"session without the label", session(func(s *v1alpha1.AgentSession) { s.Labels = nil }),
			scheduledWorkRelease{why: swReleaseRecorded, read: itemFound, work: work("", occ(v1alpha1.OccurrenceSucceeded, name))}, false},
		{"not tracked: no occurrence", session(nil), scheduledWorkRelease{why: swReleaseNotTracked, read: itemFound, work: work("")}, true},
		{"not tracked: another session", session(nil), scheduledWorkRelease{why: swReleaseNotTracked, read: itemFound, work: work("", occ(v1alpha1.OccurrenceRunning, "sw-ffffffffffff-202612031400"))}, true},
		{"not tracked, but it is this session's", session(nil), scheduledWorkRelease{why: swReleaseNotTracked, read: itemFound, work: work("", occ(v1alpha1.OccurrenceRunning, name))}, false},
		{"not tracked, but unnamed and open", session(nil), scheduledWorkRelease{why: swReleaseNotTracked, read: itemFound, work: work("", occ(v1alpha1.OccurrenceClaimed, ""))}, false},
		{"replaced", session(nil), scheduledWorkRelease{why: swReleaseReplaced, read: itemFound, work: work("other-uid")}, true},
		{"replaced, but the same UID", session(nil), scheduledWorkRelease{why: swReleaseReplaced, read: itemFound, work: work("")}, false},
		{"item gone", session(nil), scheduledWorkRelease{why: swReleaseItemGone, read: itemMissing}, true},
		{"item gone, but read", session(nil), scheduledWorkRelease{why: swReleaseItemGone, read: itemFound, work: work("")}, false},
		{"item gone, kind unserved instead", session(nil), scheduledWorkRelease{why: swReleaseItemGone, read: itemKindGone}, false},
		{"kind gone after the deadline", session(nil), scheduledWorkRelease{why: swReleaseKindGone, read: itemKindGone, now: now}, true},
		{"kind gone before the deadline", session(func(s *v1alpha1.AgentSession) {
			s.Annotations[v1alpha1.AnnotationScheduledWorkStartDeadline] = now.Add(time.Minute).Format(time.RFC3339)
		}), scheduledWorkRelease{why: swReleaseKindGone, read: itemKindGone, now: now}, false},
		{"kind gone, no deadline", session(func(s *v1alpha1.AgentSession) { s.Annotations = nil }),
			scheduledWorkRelease{why: swReleaseKindGone, read: itemKindGone, now: now}, false},
		{"kind gone, but the item is missing only", session(nil), scheduledWorkRelease{why: swReleaseKindGone, read: itemMissing, now: now}, false},
		{"no reason", session(nil), scheduledWorkRelease{read: itemFound, work: work("", occ(v1alpha1.OccurrenceSucceeded, name))}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.ev.at, c.ev.now = at, now
			err := scheduledWorkReleaseAllowed(c.s, c.ev)
			if (err == nil) != c.allow {
				t.Fatalf("allowed %v (%v), want %v", err == nil, err, c.allow)
			}
		})
	}
}

// TestOnlyTheReleaserRemovesTheScheduledWorkFinalizer reads every non-test Go
// file of the module. v1alpha1.ScheduledWorkFinalizer may appear only where a
// finalizer is read or added (ContainsFinalizer, AddFinalizer, an element of a
// `Finalizers:` list literal, as the executor's Create builds it) and in one
// RemoveFinalizer call, in releaseScheduledWorkFinalizer; its value may be
// spelled only in labels.go. Any other use (a comparison, a filter, a patch)
// could remove it, so it fails here and review looks. Every other
// RemoveFinalizer in this package lifts the rescue finalizer, and every one in
// the module names a finalizer constant. The scan reads syntax, so these gaps
// remain for review: a finalizer list edited by index or replaced by a patch
// without naming the constant (TestOnlyTheGuardDeletes covers that for this
// package), and the value assembled from split strings.
func TestOnlyTheReleaserRemovesTheScheduledWorkFinalizer(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	var violations []string
	removals, files := 0, 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "testdata", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		files++
		var stack []ast.Node
		ast.Inspect(file, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)
			fn := enclosingFunc(stack)
			switch x := n.(type) {
			case *ast.BasicLit:
				if x.Kind == token.STRING && strings.Contains(x.Value, "scheduled-work-retained") && rel != "api/v1alpha1/labels.go" {
					violations = append(violations, fset.Position(x.Pos()).String()+": spells the scheduled-work finalizer's value")
				}
			case *ast.Ident:
				if x.Name != "ScheduledWorkFinalizer" {
					return true
				}
				ref, at := ast.Node(x), len(stack)-2
				if sel, ok := stack[at].(*ast.SelectorExpr); ok && sel.Sel == x {
					ref, at = sel, at-1
				}
				switch p := stack[at].(type) {
				case *ast.ValueSpec:
					if rel == "api/v1alpha1/labels.go" && len(p.Names) > 0 && p.Names[0] == x {
						return true
					}
				case *ast.CompositeLit:
					// Only as an element of a Finalizers list being built, as the
					// executor's Create does; a literal used as a removal filter is not.
					if kv, ok := stack[at-1].(*ast.KeyValueExpr); ok && kv.Value == p && isIdent(kv.Key, "Finalizers") {
						return true
					}
				case *ast.CallExpr:
					name := calleeName(p)
					isArg := false
					for _, a := range p.Args {
						isArg = isArg || a == ref
					}
					switch {
					case isArg && (name == "ContainsFinalizer" || name == "AddFinalizer"):
						return true
					case isArg && name == "RemoveFinalizer" && rel == "internal/controller/scheduled_work_release.go" && fn == "releaseScheduledWorkFinalizer" && len(p.Args) == 2 && p.Args[1] == ref:
						removals++
						return true
					}
				}
				violations = append(violations, fset.Position(x.Pos()).String()+" in "+fn+": uses ScheduledWorkFinalizer other than to read it, add it, or remove it in releaseScheduledWorkFinalizer")
			case *ast.CallExpr:
				if calleeName(x) != "RemoveFinalizer" || fn == "releaseScheduledWorkFinalizer" {
					return true
				}
				// Elsewhere in this package only the rescue finalizer is lifted,
				// and anywhere in the module only a named finalizer constant, so
				// no variable can carry this one.
				switch {
				case strings.HasPrefix(rel, "internal/controller/") && (len(x.Args) != 2 || !isIdent(x.Args[1], "Finalizer")):
					violations = append(violations, fset.Position(x.Pos()).String()+" in "+fn+": a RemoveFinalizer here lifts something other than the rescue finalizer")
				case len(x.Args) != 2 || !strings.HasSuffix(finalizerName(x.Args[1]), "Finalizer"):
					violations = append(violations, fset.Position(x.Pos()).String()+" in "+fn+": RemoveFinalizer takes something other than a named finalizer constant")
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error(v)
	}
	if files < 100 {
		t.Fatalf("scanned %d files; the walk did not reach the module", files)
	}
	if removals != 1 {
		t.Errorf("releaseScheduledWorkFinalizer removes the scheduled-work finalizer %d times, want once", removals)
	}
}

// finalizerName is the name an identifier or a selector ends in, else "".
func finalizerName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	}
	return ""
}

func enclosingFunc(stack []ast.Node) string {
	for i := len(stack) - 1; i >= 0; i-- {
		if f, ok := stack[i].(*ast.FuncDecl); ok {
			return f.Name.Name
		}
	}
	return "package scope"
}

// The sweep queues exactly the labelled sessions in its namespace that carry
// the finalizer, read through the reader it is given.
func TestScheduledWorkSweepQueuesLabelledSessions(t *testing.T) {
	mk := func(ns, name string, labelled, fin bool) *v1alpha1.AgentSession {
		s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
		if labelled {
			s.Labels = map[string]string{v1alpha1.LabelScheduledWork: "item-uid"}
		}
		if fin {
			s.Finalizers = []string{v1alpha1.ScheduledWorkFinalizer}
		}
		return s
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		mk(sessionNS, "held", true, true),
		mk(sessionNS, "released", true, false),
		mk(sessionNS, "unlabelled", false, true),
		mk("elsewhere", "other-namespace", true, true),
	).Build()
	events := make(chan event.GenericEvent, 8)
	w := &scheduledWorkSweep{reader: reader, namespace: sessionNS, every: time.Hour, events: events}
	if err := w.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(events)
	var got []string
	for e := range events {
		got = append(got, e.Object.GetName())
	}
	if len(got) != 1 || got[0] != "held" {
		t.Fatalf("queued %v, want [held]", got)
	}
}

// The releaser reads a task result the way the reconciler writes one:
// scheduledwork.TaskResult derives from agent.task exactly what observe puts
// into status.taskResult (taskResult), so the two never disagree.
func TestScheduledWorkTaskResultMatchesTheReconcilers(t *testing.T) {
	finished := metav1.NewTime(time.Date(2026, 12, 3, 14, 5, 0, 0, time.UTC))
	tasks := []*v1alpha1.TaskStatus{nil,
		{Subtype: "success", NumTurns: 3, FinishedAt: finished},
		{ExitCode: 1, FinishedAt: finished},
		{TimedOut: true, Subtype: "success", FinishedAt: finished},
		{IsError: true, Subtype: "success", FinishedAt: finished},
		{Subtype: "error_max_turns", NumTurns: 1, FinishedAt: finished},
		{FinishedAt: finished},
	}
	for i, task := range tasks {
		for _, mode := range []v1alpha1.SessionMode{v1alpha1.ModeTask, v1alpha1.ModeRemote, v1alpha1.ModeLocal} {
			for _, private := range []bool{false, true} {
				s := &v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{Mode: mode},
					Status: v1alpha1.AgentSessionStatus{Agent: &v1alpha1.AgentStatus{Status: "exited", Task: task}}}
				if private && task != nil {
					s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{TaskTerminal: &v1alpha1.PrivateTaskTerminalStatus{Task: *task}}
					s.Status.Agent.Task = nil
				}
				want, got := taskResult(s, s.Status.Agent), scheduledwork.TaskResult(s)
				if (want == nil) != (got == nil) || (want != nil && *want != *got) {
					t.Errorf("task %d, %s, private %v: the releaser reads %+v, the reconciler writes %+v", i, mode, private, got, want)
				}
			}
		}
	}
}

// The blocked gauge counts, from the cache, the labelled sessions that still
// carry the finalizer although they are being deleted or have ended; a
// running one, a released one, and one whose end only a pod could prove do
// not count.
func TestScheduledWorkReleaseCollectorCountsHeldSessions(t *testing.T) {
	now := time.Date(2026, 12, 3, 15, 0, 0, 0, time.UTC)
	deleted := metav1.NewTime(now)
	mk := func(name string, fin bool, edit func(*v1alpha1.AgentSession)) *v1alpha1.AgentSession {
		s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: name, Labels: map[string]string{v1alpha1.LabelScheduledWork: "item-uid"}},
			Spec: v1alpha1.AgentSessionSpec{Mode: v1alpha1.ModeTask, OperatingMode: v1alpha1.OperatingModeRunning}, Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseRunning}}
		if fin {
			s.Finalizers = []string{v1alpha1.ScheduledWorkFinalizer}
		}
		if edit != nil {
			edit(s)
		}
		return s
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		mk("suspended", true, func(s *v1alpha1.AgentSession) { s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended }),
		mk("deleting", true, func(s *v1alpha1.AgentSession) { s.DeletionTimestamp = &deleted }),
		mk("pod-ended", true, func(s *v1alpha1.AgentSession) { s.Status.Phase = v1alpha1.PhaseFailed }),
		mk("running", true, nil),
		mk("agent-ended-unproven", true, func(s *v1alpha1.AgentSession) {
			s.Status.Agent = &v1alpha1.AgentStatus{Boot: "ready", Status: "exited"}
		}),
		mk("released", false, func(s *v1alpha1.AgentSession) { s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended }),
	).Build()
	c := &ScheduledWorkReleaseCollector{Reader: reader, Namespace: sessionNS, Now: func() time.Time { return now }}
	ch := make(chan prometheus.Metric, 4)
	c.Collect(ch)
	close(ch)
	var got []float64
	for m := range ch {
		var d dto.Metric
		if err := m.Write(&d); err != nil {
			t.Fatal(err)
		}
		got = append(got, d.GetGauge().GetValue())
	}
	if len(got) != 1 || got[0] != 3 {
		t.Fatalf("blocked %v, want [3]", got)
	}
}
