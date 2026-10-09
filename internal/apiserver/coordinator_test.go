package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const coordinatorSA = "dev-env-system/codex-host-a"
const tokCoordinator = "synthetic-coordinator-token"

func coordinatorFixture(t *testing.T) *fixture {
	f := newFixture(t)
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "codex-host-a-0", Namespace: "dev-env-system", UID: "host-pod-uid",
		Annotations: map[string]string{CoordinatorHostAnnotation: "host-a"}},
		Spec: corev1.PodSpec{ServiceAccountName: "codex-host-a", Containers: []corev1.Container{{Name: "host", Image: "synthetic"}}}}
	if err := f.c.Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(coordinatorSA)
	id.PodName, id.PodUID = p.Name, string(p.UID)
	f.auth[tokCoordinator] = id
	f.srv.Policy.CoordinatorEnabled = true
	f.srv.Policy.Coordinators = []CoordinatorHost{{ServiceAccount: coordinatorSA, PodName: p.Name, HostID: "host-a"}}
	return f
}

func TestCoordinatorIdentityIsLiveAndBound(t *testing.T) {
	for _, kind := range []string{"ready", "disabled", "unbound", "uid", "pod-name", "service-account", "host-id", "deleted", "no-live-reader"} {
		t.Run(kind, func(t *testing.T) {
			f := coordinatorFixture(t)
			id := f.auth[tokCoordinator]
			var pod corev1.Pod
			key := types.NamespacedName{Namespace: "dev-env-system", Name: id.PodName}
			if err := f.c.Get(context.Background(), key, &pod); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "disabled":
				f.srv.Policy.CoordinatorEnabled = false
			case "unbound":
				id.PodUID = ""
			case "uid":
				id.PodUID = "earlier-pod-uid"
			case "pod-name":
				id.PodName = "unconfigured-host-0"
			case "service-account":
				pod.Spec.ServiceAccountName = "different-sa"
			case "host-id":
				pod.Annotations[CoordinatorHostAnnotation] = "host-b"
			case "deleted":
				if err := f.c.Delete(context.Background(), &pod); err != nil {
					t.Fatal(err)
				}
			case "no-live-reader":
				f.srv.Live = nil
			}
			if kind == "service-account" || kind == "host-id" {
				if err := f.c.Update(context.Background(), &pod); err != nil {
					t.Fatal(err)
				}
			}
			f.auth[tokCoordinator] = id
			w := f.do(http.MethodGet, apiv1.SessionsPath, tokCoordinator, nil)
			if kind == "ready" {
				if w.Code != http.StatusOK {
					t.Fatal(w.Code, w.Body.String())
				}
			} else {
				wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
			}
		})
	}
}

func TestCoordinatorAlwaysFiltersDirectChildrenAndRefusesForeignRoutes(t *testing.T) {
	f := coordinatorFixture(t)
	f.sessionPod("own-task", "dev", 0)
	own := f.session("own-task")
	own.Spec.Parent = coordinatorSA
	if err := f.c.Update(context.Background(), own); err != nil {
		t.Fatal(err)
	}
	f.sessionPod("other-task", "dev", 0)
	for _, query := range []string{"", "?mine=false", "?mine=true"} {
		w := f.do(http.MethodGet, apiv1.SessionsPath+query, tokCoordinator, nil)
		if w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		list := decode[apiv1.SessionList](t, w)
		if len(list.Sessions) != 1 || list.Sessions[0].Name != "own-task" {
			t.Fatalf("cross-parent list: %+v", list)
		}
	}
	for _, name := range []string{"other-task", "missing-task"} {
		for _, route := range []struct {
			method, suffix string
			body           any
		}{
			{http.MethodGet, "", nil}, {http.MethodDelete, "", nil},
			{http.MethodGet, "/log", nil}, {http.MethodPost, "/messages", apiv1.MessageRequest{Text: "synthetic"}},
			{http.MethodPost, "/suspend", nil}, {http.MethodPost, "/resume", nil},
		} {
			w := f.do(route.method, apiv1.SessionPath(name)+route.suffix, tokCoordinator, route.body)
			e := wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
			if strings.Contains(e.Message, name) {
				t.Fatalf("target details leaked: %s", e.Message)
			}
		}
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, apiv1.FleetPath}, {http.MethodGet, apiv1.RescuesPath},
		{http.MethodGet, apiv1.GrantsPath}, {http.MethodPost, apiv1.GrantsPath},
		{http.MethodGet, apiv1.GrantPath("unrelated")}, {http.MethodDelete, apiv1.GrantPath("unrelated")},
		{http.MethodGet, apiv1.ActivitiesPath}, {http.MethodPost, apiv1.ActivitiesPath},
		{http.MethodDelete, apiv1.ActivityPath("unrelated")},
		{http.MethodPost, apiv1.SessionPath("own-task") + "/heartbeat"},
	} {
		wantError(t, f.do(route.method, route.path, tokCoordinator, nil), http.StatusForbidden, apiv1.CodeForbidden)
	}
}

func TestCoordinatorConfigurationRejectsTrustOverlap(t *testing.T) {
	policy := Policy{Human: humanSA, Clients: []string{clientSA}, SessionNamespace: sessionNS, SessionServiceAccount: "dev-env-session"}
	for _, raw := range []string{
		`[{"serviceAccount":"dev/dev-env","podName":"host-0","hostID":"host"}]`,
		`[{"serviceAccount":"dev-env-system/dev-env-human","podName":"host-0","hostID":"host"}]`,
		`[{"serviceAccount":"dev-agents/dev-env-session","podName":"host-0","hostID":"host"}]`,
		`[{"serviceAccount":"bad","podName":"host-0","hostID":"host"}]`,
		`[{"serviceAccount":"dev-env-system/host","podName":"host-0","hostID":"host","unknown":"claim"}]`,
		`[{"serviceAccount":"dev-env-system/host","podName":"host-0","hostID":"host"},{"serviceAccount":"dev-env-system/host","podName":"host-1","hostID":"second"}]`,
	} {
		if _, err := ParseCoordinatorHosts(raw, policy); err == nil {
			t.Fatal("accepted overlapping/invalid config", raw)
		}
	}
	if hosts, err := ParseCoordinatorHosts("", policy); err != nil || len(hosts) != 0 {
		t.Fatal(hosts, err)
	}
}
