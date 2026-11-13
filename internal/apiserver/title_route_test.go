package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/sessiontitle"
	"github.com/thaynes43/dev-env/internal/templates"
)

func putTitle(f *fixture, token, name, title, expect string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.do(http.MethodPut, apiv1.SessionTitlePath(name), token, map[string]string{"title": title, "expect": expect})
}

func TestTitleAccess(t *testing.T) {
	t.Run("human", func(t *testing.T) {
		f := newFixture(t)
		f.sessionPod("title-human", "full", 0)
		w := putTitle(f, tokHuman, "title-human", "Cafe", "")
		if w.Code != http.StatusOK || f.session("title-human").Spec.Title != "Cafe" {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})
	t.Run("client", func(t *testing.T) {
		f := newFixture(t)
		f.sessionPod("title-client", "full", 0)
		w := putTitle(f, tokClient, "title-client", "Cafe", "")
		if w.Code != http.StatusOK || f.session("title-client").Spec.Title != "Cafe" {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})
	t.Run("parent session", func(t *testing.T) {
		f := newFixture(t)
		parent := f.sessionPod("title-parent", "full", 0)
		f.sessionPod("title-child", "full", 1)
		child := f.session("title-child")
		child.Spec.Parent = "title-parent"
		if err := f.c.Update(context.Background(), child); err != nil {
			t.Fatal(err)
		}
		w := putTitle(f, parent, "title-child", "Cafe", "")
		if w.Code != http.StatusOK || f.session("title-child").Spec.Title != "Cafe" {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})
	t.Run("workbench on another session", func(t *testing.T) {
		f := newFixture(t)
		f.srv.Policy.Clients = append(f.srv.Policy.Clients, sessionNS+"/"+templates.WorkbenchServiceAccount)
		f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
		tok := f.sessionPod("title-bench", "workbench", 0)
		id := saIdentity(sessionNS + "/" + templates.WorkbenchServiceAccount)
		id.PodName, id.PodUID = f.auth[tok].PodName, f.auth[tok].PodUID
		f.auth[tok] = id
		f.sessionPod("title-other", "full", 0)
		w := putTitle(f, tok, "title-other", "Cafe", "")
		if w.Code != http.StatusOK || f.session("title-other").Spec.Title != "Cafe" {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})
	t.Run("coordinator on its child", func(t *testing.T) {
		f := coordinatorFixture(t)
		f.sessionPod("title-coord", "full", 1)
		child := f.session("title-coord")
		child.Spec.Parent = coordinatorSA
		if err := f.c.Update(context.Background(), child); err != nil {
			t.Fatal(err)
		}
		w := putTitle(f, tokCoordinator, "title-coord", "Cafe", "")
		if w.Code != http.StatusOK || f.session("title-coord").Spec.Title != "Cafe" {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})
	t.Run("session itself", func(t *testing.T) {
		f := newFixture(t)
		tok := f.sessionPod("title-self", "full", 0)
		wantError(t, putTitle(f, tok, "title-self", "Cafe", ""), http.StatusForbidden, apiv1.CodeForbidden)
		if f.session("title-self").Spec.Title != "" {
			t.Fatal("a session set its own title")
		}
	})
	t.Run("workbench itself", func(t *testing.T) {
		f := newFixture(t)
		f.srv.Policy.Clients = append(f.srv.Policy.Clients, sessionNS+"/"+templates.WorkbenchServiceAccount)
		f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
		tok := f.sessionPod("title-bench-self", "workbench", 0)
		id := saIdentity(sessionNS + "/" + templates.WorkbenchServiceAccount)
		id.PodName, id.PodUID = f.auth[tok].PodName, f.auth[tok].PodUID
		f.auth[tok] = id
		wantError(t, putTitle(f, tok, "title-bench-self", "Cafe", ""), http.StatusForbidden, apiv1.CodeForbidden)
	})
	t.Run("sibling", func(t *testing.T) {
		f := newFixture(t)
		tok := f.sessionPod("title-sib-a", "full", 0)
		f.sessionPod("title-sib-b", "full", 0)
		wantError(t, putTitle(f, tok, "title-sib-b", "Cafe", ""), http.StatusForbidden, apiv1.CodeForbidden)
	})
	t.Run("child on its parent", func(t *testing.T) {
		f := newFixture(t)
		f.sessionPod("title-up", "full", 0)
		child := f.sessionPod("title-down", "full", 1)
		got := f.session("title-down")
		got.Spec.Parent = "title-up"
		if err := f.c.Update(context.Background(), got); err != nil {
			t.Fatal(err)
		}
		wantError(t, putTitle(f, child, "title-up", "Cafe", ""), http.StatusForbidden, apiv1.CodeForbidden)
	})
	t.Run("ordinary session on a session it did not create", func(t *testing.T) {
		f := newFixture(t)
		tok := f.sessionPod("title-stranger", "full", 0)
		f.sessionPod("title-unrelated", "full", 0)
		wantError(t, putTitle(f, tok, "title-unrelated", "Cafe", ""), http.StatusForbidden, apiv1.CodeForbidden)
	})
	t.Run("meatball", func(t *testing.T) {
		f := meatballFixture(t, meatballPolicy())
		f.sessionPod("title-meatball", "full", 0)
		wantError(t, putTitle(f, tokMeatball, "title-meatball", "Cafe", ""), http.StatusForbidden, apiv1.CodeForbidden)
		if f.session("title-meatball").Spec.Title != "" {
			t.Fatal("Meatball set a title")
		}
	})
	t.Run("summoning caller", func(t *testing.T) {
		f := newFixture(t)
		policy := &v1alpha1.CallerPolicy{
			ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: "ops.summoner", UID: "summon-policy"},
			Spec:       v1alpha1.CallerPolicySpec{Summon: &v1alpha1.CallerPolicySummon{ServiceAccount: "ops/summoner"}},
		}
		if err := f.c.Create(context.Background(), policy); err != nil {
			t.Fatal(err)
		}
		f.auth["tok-summon"] = saIdentity("ops/summoner")
		f.sessionPod("title-summon", "full", 0)
		wantError(t, putTitle(f, "tok-summon", "title-summon", "Cafe", ""), http.StatusForbidden, apiv1.CodeForbidden)
	})
	t.Run("portal reader", func(t *testing.T) {
		f := newFixture(t)
		f.srv.Policy.PortalReaderEnabled = true
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal", Namespace: portalReaderNamespace, UID: uuid.NewUUID()},
			Spec: corev1.PodSpec{ServiceAccountName: portalReaderAccount, Containers: []corev1.Container{{Name: "portal", Image: "x"}}}}
		if err := f.c.Create(context.Background(), pod); err != nil {
			t.Fatal(err)
		}
		if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, pod); err != nil {
			t.Fatal(err)
		}
		id := saIdentity(portalReaderNamespace + "/" + portalReaderAccount)
		id.PodName, id.PodUID = pod.Name, string(pod.UID)
		f.auth["tok-portal"] = id
		f.sessionPod("title-portal", "full", 0)
		wantError(t, putTitle(f, "tok-portal", "title-portal", "Cafe", ""), http.StatusForbidden, apiv1.CodeForbidden)
	})
	t.Run("portal launch", func(t *testing.T) {
		f := newFixture(t)
		f.sessionPod("title-launch", "full", 0)
		req := httptest.NewRequest(http.MethodPut, apiv1.SessionTitlePath("title-launch"), strings.NewReader(`{"title":"Cafe","expect":""}`))
		req.SetPathValue("name", "title-launch")
		req.Header.Set("Content-Type", "application/json")
		_, _, err := f.srv.setTitle(context.Background(), httptest.NewRecorder(), req, &caller{kind: kindPortalLaunch, parent: "portal/launch"})
		var ae *apiError
		if !errors.As(err, &ae) || ae.status != http.StatusForbidden {
			t.Fatalf("portal launch: %v", err)
		}
		if f.session("title-launch").Spec.Title != "" {
			t.Fatal("a portal launch set a title")
		}
	})
}

func TestTitleSummonedTarget(t *testing.T) {
	f := newFixture(t)
	f.sessionPod("title-summoned", "full", 0)
	s := f.session("title-summoned")
	s.Spec.Caller = "ops/summoner"
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	e := wantError(t, putTitle(f, tokHuman, "title-summoned", "Cafe", ""), http.StatusConflict, apiv1.CodeConflict)
	if !strings.Contains(e.Message, "summoned") || f.session("title-summoned").Spec.Title != "" {
		t.Fatalf("%s title %q", e.Message, f.session("title-summoned").Spec.Title)
	}
}

func TestTitleReaping(t *testing.T) {
	f := newFixture(t)
	f.sessionPod("title-reap", "full", 0)
	if err := f.c.Delete(context.Background(), f.session("title-reap")); err != nil {
		t.Fatal(err)
	}
	if f.session("title-reap").DeletionTimestamp.IsZero() {
		t.Fatal("delete did not mark the session")
	}
	e := wantError(t, putTitle(f, tokHuman, "title-reap", "Cafe", ""), http.StatusConflict, apiv1.CodeConflict)
	if !strings.Contains(e.Message, "reaped") || f.session("title-reap").Spec.Title != "" {
		t.Fatalf("%s", e.Message)
	}
}

func TestTitleStaleExpect(t *testing.T) {
	f := newFixture(t)
	f.sessionPod("title-expect", "full", 0)
	e := wantError(t, putTitle(f, tokHuman, "title-expect", "Cafe", "stale"), http.StatusConflict, apiv1.CodeConflict)
	if e.Reason != "title_changed" || strings.Contains(e.Message, "Cafe") || strings.Contains(strings.ToLower(e.Message), "try again") || strings.Contains(strings.ToLower(e.Message), "retry") {
		t.Fatalf("%+v", e)
	}
	if f.session("title-expect").Spec.Title != "" {
		t.Fatal("a stale expect wrote the title")
	}
	if w := putTitle(f, tokHuman, "title-expect", "Cafe", ""); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	e = wantError(t, putTitle(f, tokHuman, "title-expect", "Cafe", ""), http.StatusConflict, apiv1.CodeConflict)
	if e.Reason != "title_changed" || f.session("title-expect").Spec.Title != "Cafe" {
		t.Fatalf("matching title with a stale expect: %+v stored %q", e, f.session("title-expect").Spec.Title)
	}
}

func TestTitleSameNoWrite(t *testing.T) {
	f := newFixture(t)
	f.sessionPod("title-same", "full", 0)
	if w := putTitle(f, tokHuman, "title-same", "Cafe", ""); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	before := f.session("title-same")
	f.now = f.now.Add(59 * time.Second)
	w := putTitle(f, tokHuman, "title-same", "Cafe", "Cafe")
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	after := f.session("title-same")
	if after.ResourceVersion != before.ResourceVersion || after.Annotations[v1alpha1.AnnotationTitleSetAt] != before.Annotations[v1alpha1.AnnotationTitleSetAt] {
		t.Fatalf("same title wrote: rv %s/%s at %s/%s", before.ResourceVersion, after.ResourceVersion, before.Annotations[v1alpha1.AnnotationTitleSetAt], after.Annotations[v1alpha1.AnnotationTitleSetAt])
	}
}

func TestTitleRateLimit(t *testing.T) {
	f := newFixture(t)
	f.sessionPod("title-rate", "full", 0)
	start := f.now
	if w := putTitle(f, tokHuman, "title-rate", "One", ""); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	f.now = start.Add(59 * time.Second)
	wantError(t, putTitle(f, tokHuman, "title-rate", "Two", "One"), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	if f.session("title-rate").Spec.Title != "One" {
		t.Fatal("a limited change wrote")
	}
	f.now = start.Add(time.Minute)
	if w := putTitle(f, tokHuman, "title-rate", "Two", "One"); w.Code != http.StatusOK || f.session("title-rate").Spec.Title != "Two" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestTitleUnparseableSetAtDoesNotLimit(t *testing.T) {
	f := newFixture(t)
	f.sessionPod("title-clock", "full", 0)
	s := f.session("title-clock")
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Annotations[v1alpha1.AnnotationTitleSetAt] = "yesterday"
	s.Annotations["keep"] = "yes"
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if w := putTitle(f, tokHuman, "title-clock", "Cafe", ""); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestTitleMissingKeys(t *testing.T) {
	f := newFixture(t)
	f.sessionPod("title-keys", "full", 0)
	for _, body := range []string{`{"title":"Cafe"}`, `{"expect":""}`, `{}`} {
		wantError(t, f.do(http.MethodPut, apiv1.SessionTitlePath("title-keys"), tokHuman, body), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	}
	if f.session("title-keys").Spec.Title != "" {
		t.Fatal("a partial body wrote")
	}
}

func TestTitleBadTitle(t *testing.T) {
	f := newFixture(t)
	f.sessionPod("title-bad", "full", 0)
	e := wantError(t, putTitle(f, tokHuman, "title-bad", "a|b", ""), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if strings.Contains(e.Message, "a|b") || len(e.Fields) != 1 || e.Fields[0].Reason != sessiontitle.ReasonCharacters {
		t.Fatalf("%+v", e)
	}
}

func TestTitleClear(t *testing.T) {
	f := newFixture(t)
	f.sessionPod("title-clear", "full", 0)
	if w := putTitle(f, tokHuman, "title-clear", "Cafe", ""); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	f.now = f.now.Add(time.Minute)
	if w := putTitle(f, tokHuman, "title-clear", "", "Cafe"); w.Code != http.StatusOK || f.session("title-clear").Spec.Title != "" {
		t.Fatalf("clear: %d %s stored %q", w.Code, w.Body.String(), f.session("title-clear").Spec.Title)
	}
}

func TestTitleChangeLeavesPodAndAnnotations(t *testing.T) {
	f := newFixture(t)
	f.sessionPod("title-stable", "full", 0)
	s := f.session("title-stable")
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Annotations["keep"] = "yes"
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "title-stable"}, &pod); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(pod)
	uid := f.session("title-stable").UID
	if w := putTitle(f, tokHuman, "title-stable", "Cafe", ""); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var afterPod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "title-stable"}, &afterPod); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(afterPod)
	if string(got) != string(before) {
		t.Fatalf("pod changed\n%s", got)
	}
	stored := f.session("title-stable")
	if stored.UID != uid || stored.Annotations["keep"] != "yes" || stored.Annotations[v1alpha1.AnnotationTitleSetBy] == "" || stored.Annotations[v1alpha1.AnnotationTitleSetAt] == "" {
		t.Fatalf("annotations %+v", stored.Annotations)
	}
}

func TestTitleAuditHash(t *testing.T) {
	f := newFixture(t)
	sink := &titleLog{}
	f.srv.Log = logr.New(sink)
	f.sessionPod("title-audit", "full", 0)
	const text = "Café"
	if w := putTitle(f, tokHuman, "title-audit", text, ""); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	want := sessiontitle.HashPrefix(sessiontitle.Compose("claude-opus-5-5", text))
	joined := strings.Join(sink.lines, "\n")
	if !strings.Contains(joined, "session title set") || !strings.Contains(joined, want) || !strings.Contains(joined, "title-audit") {
		t.Fatalf("log %q want hash %s", joined, want)
	}
	if strings.Contains(joined, text) || strings.Contains(joined, "Opus") {
		t.Fatalf("log holds the title %q", joined)
	}
}

type titleLog struct{ lines []string }

func (l *titleLog) Init(logr.RuntimeInfo) {}
func (l *titleLog) Enabled(int) bool      { return true }
func (l *titleLog) Info(_ int, msg string, kv ...any) {
	l.lines = append(l.lines, fmt.Sprint(append([]any{msg}, kv...)))
}
func (l *titleLog) Error(error, string, ...any)    {}
func (l *titleLog) WithValues(...any) logr.LogSink { return l }
func (l *titleLog) WithName(string) logr.LogSink   { return l }

func TestHeartbeatAnswersRecordedTitle(t *testing.T) {
	f := newFixture(t)
	const name = "title-beat"
	tok := f.sessionPod(name, "full", 0)
	w := f.do(http.MethodPost, protocol.HeartbeatPath(name), tok, status(name))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var empty struct {
		Title struct {
			Text string `json:"text"`
		} `json:"title"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &empty); err != nil || empty.Title.Text != "" {
		t.Fatalf("empty title body %s %v", w.Body.String(), err)
	}
	s := f.session(name)
	s.Spec.Title = "Café"
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodPost, protocol.HeartbeatPath(name), tok, status(name))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var got struct {
		Title struct {
			Text string `json:"text"`
		} `json:"title"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Title.Text != "Café" {
		t.Fatalf("body %s %v", w.Body.String(), err)
	}
}

func TestRemoteControlStatusClipsAndDropsRename(t *testing.T) {
	f := newFixture(t)
	const name = "title-clip"
	tok := f.sessionPod(name, "full", 0)
	st := status(name)
	st.RemoteControl = &protocol.RemoteControl{
		Name: name, State: protocol.RemoteRegistered,
		ProviderTitle: strings.Repeat("a", 200),
		Rename:        &protocol.RemoteRename{State: "Nope", Reason: "quiet", Attempts: 1},
	}
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), tok, st); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	rc := f.session(name).Status.RemoteControl
	if rc == nil || rc.Rename != nil || len(rc.ProviderTitle) > 128 || !strings.HasSuffix(rc.ProviderTitle, "…") {
		t.Fatalf("dropped/clipped %+v", rc)
	}
	st.RemoteControl.ProviderTitle = "Someone"
	st.RemoteControl.Rename = &protocol.RemoteRename{State: "Held", Reason: "quiet", Attempts: 3}
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), tok, st); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if f.session(name).Status.RemoteControl.Rename != nil {
		t.Fatal("attempts outside 0 to 2 were stored")
	}
	at := time.Date(2026, 11, 13, 14, 2, 0, 0, time.UTC)
	st.RemoteControl.Rename = &protocol.RemoteRename{State: "Held", Reason: "quiet", Since: at, Attempts: 1}
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), tok, st); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	got := f.session(name).Status.RemoteControl
	if got.ProviderTitle != "Someone" || got.Rename == nil || got.Rename.State != "Held" || got.Rename.Reason != "quiet" || got.Rename.Attempts != 1 {
		t.Fatalf("stored %+v", got)
	}
}

func TestOlderOperatorDropsProviderTitleAndRename(t *testing.T) {
	raw := []byte(`{"name":"dev-env-1019-120000","state":"registered","title":"Opus | Cafe","providerTitle":"Someone","rename":{"state":"Held","reason":"quiet","attempts":1}}`)
	var old struct {
		Name  string `json:"name"`
		State string `json:"state"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(raw, &old); err != nil || old.Name != "dev-env-1019-120000" || old.Title != "Opus | Cafe" {
		t.Fatalf("%+v %v", old, err)
	}
}
