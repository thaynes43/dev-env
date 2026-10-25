package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestPortalReaderSessionSummaries(t *testing.T) {
	f := newFixture(t)
	now := f.now
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal-pod", Namespace: sessionNS, UID: types.UID("pod-uid")}, Spec: corev1.PodSpec{ServiceAccountName: "dev-env-v2-portal", Containers: []corev1.Container{{Name: "portal", Image: "x"}}}}
	beat := func(at time.Time) *v1alpha1.AgentStatus {
		stamp := metav1.NewTime(at)
		return &v1alpha1.AgentStatus{LastHeartbeat: &stamp}
	}
	sess := func(name, repo, phase string, hb *v1alpha1.AgentStatus) v1alpha1.AgentSession {
		return v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: types.UID("uid-" + name)}, Spec: v1alpha1.AgentSessionSpec{Repo: repo, Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "SECRET-PROMPT", Parent: humanSA}, Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.SessionPhase(phase), Agent: hb}}
	}
	live := &portalLive{pod: pod, rv: "7", sessions: []v1alpha1.AgentSession{
		sess("newer", "dev-env", "Running", beat(now.Add(-time.Minute))),
		sess("older", "dev-env", "Running", beat(now.Add(-2*time.Hour))),
		sess("unseen", "dev-env", "", nil),
		sess("future", "dev-env", "Running", beat(now.Add(time.Hour))),
		sess("zzz-foreign", "haynes-ops", "Running", beat(now)),
		{ObjectMeta: metav1.ObjectMeta{Name: "outside", Namespace: "other", UID: "uid-outside"}, Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Prompt: "SECRET-PROMPT"}},
	}}
	f.srv.Live = live
	id := saIdentity(portalReaderRef)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	f.auth["tok-portal"] = id
	f.srv.Policy.Clients = append(f.srv.Policy.Clients, portalReaderRef)
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, "tok-portal", nil), http.StatusForbidden, apiv1.CodeForbidden)
	if strings.Contains(f.do(http.MethodGet, apiv1.SessionsPath, "tok-portal", nil).Body.String(), "SECRET-PROMPT") {
		t.Fatal("disabled portal account received a generic session list")
	}
	f.srv.Policy.Clients = []string{clientSA}
	f.srv.Policy.PortalReaderEnabled = true
	first := f.do(http.MethodGet, apiv1.SessionsPath+"?limit=2", "tok-portal", nil)
	page := decode[apiv1.PortalSessionPage](t, first)
	body := first.Body.String()
	if page.Complete || page.Limit != 2 || len(page.Sessions) != 2 || page.Sessions[0].Name != "future" || page.Sessions[1].Name != "newer" || strings.Contains(body, "SECRET-PROMPT") || strings.Contains(body, "haynes-ops") || strings.Contains(body, "prompt") || strings.Contains(body, "outside") {
		t.Fatalf("page leaked scope or scanned without a bound: %+v %s", page, body)
	}
	if page.Sessions[0].Freshness != "unknown" || page.Sessions[1].Freshness != "observed" || page.Sessions[1].Phase != "Running" {
		t.Fatalf("freshness %+v", page.Sessions)
	}
	if live.lists[0].Limit != 2 || live.lists[0].Namespace != sessionNS || live.lists[0].Continue != "" {
		t.Fatalf("first list was not a bounded namespace page: %+v", live.lists[0])
	}
	rest := decode[apiv1.PortalSessionPage](t, f.do(http.MethodGet, apiv1.SessionsPath+"?limit=2&cursor="+page.NextCursor, "tok-portal", nil))
	if rest.Complete || len(rest.Sessions) != 1 || rest.Sessions[0].Name != "older" || rest.Sessions[0].Freshness != "stale" {
		t.Fatalf("second page %+v", rest)
	}
	continued := live.lists[len(live.lists)-1]
	if continued.Continue == "" || continued.Raw != nil || continued.Limit != 2 {
		t.Fatalf("continuation did not use the opaque snapshot token alone: %+v", continued)
	}
	live.changed = true
	if f.do(http.MethodGet, apiv1.SessionsPath+"?limit=2&cursor="+rest.NextCursor, "tok-portal", nil).Code == http.StatusOK {
		t.Fatal("changed snapshot was reported complete")
	}
	live.changed = false
	final := decode[apiv1.PortalSessionPage](t, f.do(http.MethodGet, apiv1.SessionsPath+"?limit=2&cursor="+rest.NextCursor, "tok-portal", nil))
	if !final.Complete || len(final.Sessions) != 1 || final.Sessions[0].Name != "unseen" || final.Sessions[0].Freshness != "unknown" || final.Sessions[0].Phase != "Pending" {
		t.Fatalf("final page %+v", final)
	}
	for _, path := range []string{
		apiv1.SessionsPath + "?repo=dev-env",
		apiv1.SessionsPath + "?repo=haynes-ops",
		apiv1.SessionsPath + "?limit=101",
		apiv1.SessionsPath + "?cursor=" + page.NextCursor + "/extra",
		apiv1.SessionsPath + "?mine=true",
		apiv1.SessionsPath + "?limit=1&bad=%zz",
		apiv1.SessionsPath + "?repo=foreign;limit=1",
		"/v1/%73essions",
		apiv1.SessionsPath + "?limit=1&cursor=" + page.NextCursor,
	} {
		if f.do(http.MethodGet, path, "tok-portal", nil).Code == http.StatusOK {
			t.Fatalf("unsafe query accepted %s", path)
		}
	}
	if f.do(http.MethodPost, apiv1.SessionsPath, "tok-portal", task()).Code != http.StatusForbidden {
		t.Fatal("portal reader created a session")
	}
	if f.do(http.MethodGet, apiv1.SessionsPath+"/newer", "tok-portal", nil).Code != http.StatusForbidden {
		t.Fatal("portal reader read a session detail")
	}
	if f.do(http.MethodGet, apiv1.TaskClaimsPath, "tok-portal", nil).Code != http.StatusForbidden {
		t.Fatal("portal reader listed task claims")
	}
	wrong := id
	wrong.PodUID = "other-pod"
	f.auth["tok-wrong-pod"] = wrong
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, "tok-wrong-pod", nil), http.StatusForbidden, apiv1.CodeForbidden)
	stranger := saIdentity("dev-agents/dev-env-v2-portal-other")
	stranger.PodName, stranger.PodUID = pod.Name, string(pod.UID)
	f.auth["tok-wrong-sa"] = stranger
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, "tok-wrong-sa", nil), http.StatusForbidden, apiv1.CodeForbidden)
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, "tok-not-audience", nil), http.StatusUnauthorized, apiv1.CodeUnauthenticated)
	if _, err := json.Marshal(page); err != nil {
		t.Fatal(err)
	}
	deleting := pod.DeepCopy()
	stamp := metav1.NewTime(now)
	deleting.DeletionTimestamp = &stamp
	f.srv.Live = &portalLive{pod: deleting, rv: "7"}
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, "tok-portal", nil), http.StatusForbidden, apiv1.CodeForbidden)
	f.srv.Live = &portalLive{pod: pod, getErr: errors.New("unavailable"), rv: "7"}
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, "tok-portal", nil), http.StatusForbidden, apiv1.CodeForbidden)
	if err := ValidatePortalCaller(Policy{Human: portalReaderRef}); err == nil {
		t.Fatal("portal account was also accepted as the human caller")
	}
	if err := ValidatePortalCaller(Policy{Coordinators: []CoordinatorHost{{ServiceAccount: portalReaderRef}}}); err == nil {
		t.Fatal("portal account was also accepted as a coordinator")
	}
}

type portalLive struct {
	pod      *corev1.Pod
	getErr   error
	sessions []v1alpha1.AgentSession
	rv       string
	changed  bool
	lists    []client.ListOptions
}

func (p *portalLive) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	pod, ok := obj.(*corev1.Pod)
	if !ok || p.pod == nil || key.Namespace != sessionNS || key.Name != p.pod.Name {
		return errors.New("unavailable")
	}
	if p.getErr != nil {
		return p.getErr
	}
	*pod = *p.pod
	return nil
}

func (p *portalLive) List(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
	lo := &client.ListOptions{}
	lo.ApplyOptions(opts)
	copied := *lo
	if lo.Raw != nil {
		raw := *lo.Raw
		copied.Raw = &raw
	}
	p.lists = append(p.lists, copied)
	if lo.Continue != "" && lo.Raw != nil && lo.Raw.ResourceVersion != "" {
		return errors.New("continue must not set resourceVersion")
	}
	if p.changed {
		return apierrors.NewResourceExpired("expired")
	}
	items := append([]v1alpha1.AgentSession{}, p.sessions...)
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].Name < items[j-1].Name; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	start := 0
	if lo.Continue != "" {
		name := strings.TrimPrefix(lo.Continue, "after:")
		for start < len(items) && items[start].Name <= name {
			start++
		}
	}
	limit := int(lo.Limit)
	if limit < 1 {
		limit = len(items)
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	out := list.(*v1alpha1.AgentSessionList)
	out.Items = append([]v1alpha1.AgentSession{}, items[start:end]...)
	out.ResourceVersion = p.rv
	if end < len(items) && len(out.Items) > 0 {
		out.Continue = "after:" + out.Items[len(out.Items)-1].Name
	}
	return nil
}
