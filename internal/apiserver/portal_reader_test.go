package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestPortalReaderSessionSummaries(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal-pod", Namespace: sessionNS, UID: types.UID("pod-uid")}, Spec: corev1.PodSpec{ServiceAccountName: "dev-env-v2-portal", Containers: []corev1.Container{{Name: "portal", Image: "x"}}}}
	if err := f.c.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	now := f.now
	add := func(name, repo, phase string, created time.Time) {
		t.Helper()
		s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: types.UID("uid-" + name), CreationTimestamp: metav1.NewTime(created)}, Spec: v1alpha1.AgentSessionSpec{Repo: repo, Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "SECRET-PROMPT", Parent: humanSA}}
		if err := f.c.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		s.CreationTimestamp = metav1.NewTime(created)
		if err := f.c.Update(ctx, s); err != nil {
			t.Fatal(err)
		}
		if phase != "" {
			s.Status.Phase = v1alpha1.SessionPhase(phase)
			if err := f.c.Status().Update(ctx, s); err != nil {
				t.Fatal(err)
			}
		}
	}
	add("newer", "dev-env", "Running", now.Add(-time.Minute))
	add("older", "dev-env", "Running", now.Add(-2*time.Hour))
	add("unseen", "dev-env", "", now.Add(-time.Minute))
	add("foreign", "haynes-ops", "Running", now)
	other := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "outside", Namespace: "other", CreationTimestamp: metav1.NewTime(now)}, Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "SECRET-PROMPT", Parent: humanSA}}
	if err := f.c.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(portalReaderRef)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	f.auth["tok-portal"] = id
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, "tok-portal", nil), http.StatusForbidden, apiv1.CodeForbidden)
	f.srv.Policy.PortalReaderEnabled = true
	page := decode[apiv1.PortalSessionPage](t, f.do(http.MethodGet, apiv1.SessionsPath+"?limit=2", "tok-portal", nil))
	body := f.do(http.MethodGet, apiv1.SessionsPath+"?limit=2", "tok-portal", nil).Body.String()
	if page.Complete || page.Limit != 2 || len(page.Sessions) != 2 || strings.Contains(body, "SECRET-PROMPT") || strings.Contains(body, "haynes-ops") || strings.Contains(body, "prompt") || strings.Contains(body, "outside") {
		t.Fatalf("page leaked scope or truncated silently: %+v %s", page, body)
	}
	if page.Sessions[0].Name != "unseen" && page.Sessions[0].Name != "newer" {
		t.Fatalf("unexpected first page order %+v", page.Sessions)
	}
	names := map[string]apiv1.PortalSessionSummary{}
	for _, row := range page.Sessions {
		names[row.Name] = row
		if row.Repo != "dev-env" || row.UID == "" {
			t.Fatalf("summary missing canonical fields %+v", row)
		}
	}
	if row, ok := names["unseen"]; ok && (row.Phase != "Pending" || row.Freshness != "unknown") {
		t.Fatalf("unobserved phase %+v", row)
	}
	rest := decode[apiv1.PortalSessionPage](t, f.do(http.MethodGet, apiv1.SessionsPath+"?limit=2&cursor="+page.NextCursor, "tok-portal", nil))
	if !rest.Complete || len(rest.Sessions) != 1 || rest.Sessions[0].Name == "foreign" || strings.Contains(rest.Sessions[0].Name, "outside") {
		t.Fatalf("second page %+v", rest)
	}
	if rest.Sessions[0].Freshness != "stale" || rest.Sessions[0].Phase != "Running" {
		t.Fatalf("stale session %+v", rest.Sessions[0])
	}
	for _, path := range []string{
		apiv1.SessionsPath + "?repo=dev-env",
		apiv1.SessionsPath + "?repo=haynes-ops",
		apiv1.SessionsPath + "?limit=101",
		apiv1.SessionsPath + "?cursor=" + page.NextCursor + "/extra",
		apiv1.SessionsPath + "?mine=true",
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
}
