package shelf

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

const ns = "dev-agents"

type fakeExec struct {
	pods, cmds, stdin []string
	out, errOut       string
	err               error
}

func (e *fakeExec) Run(_ context.Context, namespace, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	e.pods = append(e.pods, namespace+"/"+pod+"/"+container)
	e.cmds = append(e.cmds, strings.Join(cmd, " "))
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		e.stdin = append(e.stdin, string(b))
	}
	_, _ = io.WriteString(stdout, e.out)
	_, _ = io.WriteString(stderr, e.errOut)
	return e.err
}

func shelfPod(name string, created time.Time, phase corev1.PodPhase, ready bool) *corev1.Pod {
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(created),
			Labels: map[string]string{"app.kubernetes.io/name": AppName}},
		Status: corev1.PodStatus{Phase: phase, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: cond}}},
	}
}

func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func TestPodPicksTheNewestReadyShelf(t *testing.T) {
	t0 := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	other := shelfPod("not-a-shelf", t0.Add(3*time.Hour), corev1.PodRunning, true)
	other.Labels["app.kubernetes.io/name"] = "something-else"
	c := newClient(t,
		shelfPod("shelf-old", t0, corev1.PodRunning, true),
		shelfPod("shelf-new", t0.Add(time.Hour), corev1.PodRunning, true),
		shelfPod("shelf-starting", t0.Add(2*time.Hour), corev1.PodRunning, false),
		shelfPod("shelf-pending", t0.Add(2*time.Hour), corev1.PodPending, false),
		other,
	)
	s := &Shelf{Reader: c, Namespace: ns}
	if got, err := s.Pod(context.Background()); err != nil || got != "shelf-new" {
		t.Fatalf("pod = %q, %v", got, err)
	}
	empty := &Shelf{Reader: newClient(t, shelfPod("shelf-starting", t0, corev1.PodRunning, false)), Namespace: ns, Exec: &fakeExec{}}
	if _, err := empty.List(context.Background(), ""); !errors.Is(err, ErrNoShelf) {
		t.Errorf("no ready shelf: err = %v", err)
	}
}

func TestListAndPruneRunAgentdInTheShelf(t *testing.T) {
	c := newClient(t, shelfPod("shelf-1", time.Now(), corev1.PodRunning, true))
	ex := &fakeExec{out: `{"rescues":[{"id":"a/20261008-0024","session":"a","name":"20261008-0024"}]}`}
	s := &Shelf{Reader: c, Namespace: ns, Exec: ex}
	list, err := s.List(context.Background(), "a")
	if err != nil || len(list.Rescues) != 1 || list.Rescues[0].ID != "a/20261008-0024" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if ex.pods[0] != ns+"/shelf-1/"+Container || ex.cmds[0] != "agentd ctl rescues --session a" {
		t.Errorf("ran %q in %q", ex.cmds[0], ex.pods[0])
	}

	ex.out = `{"olderThan":"720h0m0s","removed":[{"path":"rescue/b/20260801-0000","session":"b","age":"1000h0m0s","bytes":3}],"kept":2}`
	rep, err := s.Prune(context.Background(), protocol.PruneRequest{OlderThan: "720h0m0s", Keep: []string{"a"}})
	if err != nil || len(rep.Removed) != 1 || rep.Kept != 2 {
		t.Fatalf("prune = %+v, %v", rep, err)
	}
	var sent protocol.PruneRequest
	if err := json.Unmarshal([]byte(ex.stdin[0]), &sent); err != nil || sent.OlderThan != "720h0m0s" || len(sent.Keep) != 1 {
		t.Errorf("prune sent %q (%v)", ex.stdin[0], err)
	}
	if ex.cmds[1] != "agentd ctl prune" {
		t.Errorf("prune ran %q", ex.cmds[1])
	}

	ex.out = `{"found":true,"rescue":{"id":"a/20261008-0024","session":"a","name":"20261008-0024"}}`
	held, err := s.Hold(context.Background(), "a/20261008-0024")
	if err != nil || !held.Found || held.Rescue.ID != "a/20261008-0024" || ex.cmds[2] != "agentd ctl hold-rescue a/20261008-0024" {
		t.Errorf("hold = %+v, %v, ran %q", held, err, ex.cmds[2])
	}

	ex.out, ex.errOut, ex.err = "", "agentd: prune: olderThan 1h0m0s is below the floor", errors.New("exit 1")
	if _, err := s.Prune(context.Background(), protocol.PruneRequest{OlderThan: "1h"}); err == nil || !strings.Contains(err.Error(), "below the floor") {
		t.Errorf("a failed exec: err = %v, want agentd's stderr in it", err)
	}
	ex.errOut, ex.err = "", nil
	ex.out = "not json"
	if _, err := s.List(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Errorf("garbage output: err = %v", err)
	}
	ex.out = strings.Repeat("x", maxOutput+1)
	if _, err := s.List(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("too much output: err = %v", err)
	}
}

func TestPrunerKeepsEverySessionAndWhatTheyRestoreFrom(t *testing.T) {
	sess := func(name, restore string) *v1alpha1.AgentSession {
		return &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: v1alpha1.AgentSessionSpec{Repo: "demo", Restore: restore}}
	}
	c := newClient(t, shelfPod("shelf-1", time.Now(), corev1.PodRunning, true),
		sess("b-1008-000000", ""), sess("a-1008-000000", "gone-1001-000000/20261001-1200"))
	ex := &fakeExec{out: `{"olderThan":"240h0m0s","removed":[],"kept":0}`}
	doc, err := os.ReadFile("../templates/testdata/templates.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := templates.Parse(map[string]string{templates.Key: string(doc)})
	if err != nil {
		t.Fatal(err)
	}
	p := &Pruner{Shelf: &Shelf{Reader: c, Namespace: ns, Exec: ex}, Reader: c, Namespace: ns, Log: logr.Discard(),
		Templates: func(context.Context) (*templates.Templates, error) { return tpl, nil }}
	if _, err := p.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	var sent protocol.PruneRequest
	if err := json.Unmarshal([]byte(ex.stdin[0]), &sent); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sent.Keep, " "); got != "a-1008-000000 b-1008-000000 gone-1001-000000" {
		t.Errorf("keep = %q", got)
	}
	if sent.OlderThan != tpl.BundleRetention().String() {
		t.Errorf("olderThan = %q, want the templates' %s", sent.OlderThan, tpl.BundleRetention())
	}

	broken := &Pruner{Shelf: p.Shelf, Reader: c, Namespace: ns, Log: logr.Discard(),
		Templates: func(context.Context) (*templates.Templates, error) { return nil, errors.New("no templates") }}
	if _, err := broken.Prune(context.Background()); err == nil {
		t.Error("pruned without the templates' retention")
	}
	if len(ex.stdin) != 1 {
		t.Errorf("a prune without templates still ran agentd: %q", ex.stdin)
	}
}

// io.Copy, which the exec stream uses, must not get past the cap through an
// io.ReaderFrom or a WriteString.
func TestCappedHoldsUnderIOCopy(t *testing.T) {
	var w io.Writer = &capped{max: 8}
	if _, ok := w.(io.ReaderFrom); ok {
		t.Fatal("capped is an io.ReaderFrom; io.Copy would bypass its Write")
	}
	if _, err := io.Copy(w, strings.NewReader(strings.Repeat("x", 100))); err != nil {
		t.Fatal(err)
	}
	if c := w.(*capped); c.buf.Len() != 8 || !c.over {
		t.Errorf("kept %d bytes, over=%v", c.buf.Len(), c.over)
	}
}
