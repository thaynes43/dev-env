package apiserver

import (
	"context"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

// A workbench session's pod runs as dev-env-workbench, a trusted client. Its
// pod-bound token must still speak for its session (the heartbeat needs that),
// while a token of the same account that no session pod carries stays the
// client's.
func TestWorkbenchSessionPodTokenIsTheSessions(t *testing.T) {
	f := newFixture(t)
	f.srv.Policy.Clients = append(f.srv.Policy.Clients, sessionNS+"/"+templates.WorkbenchServiceAccount)
	f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
	const name = "haynes-ops-1019-100000"
	f.sessionPod(name, "workbench", 0)
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &pod); err != nil {
		t.Fatal(err)
	}
	wb := saIdentity(sessionNS + "/" + templates.WorkbenchServiceAccount)

	// Bound to the session's pod: a session.
	bound := wb
	bound.PodName, bound.PodUID = name, string(pod.UID)
	c, err := f.srv.resolveCaller(context.Background(), bound)
	if err != nil || c.kind != kindSession || c.parent != name {
		t.Fatalf("pod-bound: %+v %v", c, err)
	}

	// Bound to no pod (the human's tooling minting the account's token): a client.
	c, err = f.srv.resolveCaller(context.Background(), wb)
	if err != nil || c.kind != kindClient || c.parent != sessionNS+"/"+templates.WorkbenchServiceAccount {
		t.Fatalf("unbound: %+v %v", c, err)
	}

	// Bound to a pod no session owns: a client, not a session.
	other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "tool", Namespace: sessionNS, UID: uuid.NewUUID()}}
	if err := f.c.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	foreign := wb
	foreign.PodName, foreign.PodUID = "tool", string(other.UID)
	if c, err = f.srv.resolveCaller(context.Background(), foreign); err != nil || c.kind != kindClient {
		t.Fatalf("foreign pod: %+v %v", c, err)
	}

	// A pod-bound token whose pod is gone is refused, never a client.
	gone := wb
	gone.PodName, gone.PodUID = "vanished", "x"
	if c, err = f.srv.resolveCaller(context.Background(), gone); err == nil {
		t.Fatalf("a vanished pod resolved to %+v", c)
	}

	// Without the policy field the account is only a client, as before.
	f.srv.Policy.WorkbenchServiceAccount = ""
	if c, err = f.srv.resolveCaller(context.Background(), bound); err != nil || c.kind != kindClient {
		t.Fatalf("policy off: %+v %v", c, err)
	}
}

// The heartbeat route accepts the workbench session's own token.
func TestWorkbenchSessionHeartbeats(t *testing.T) {
	f := newFixture(t)
	f.srv.Policy.Clients = append(f.srv.Policy.Clients, sessionNS+"/"+templates.WorkbenchServiceAccount)
	f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
	const name = "haynes-ops-1019-100001"
	tok := f.sessionPod(name, "workbench", 0)
	id := saIdentity(sessionNS + "/" + templates.WorkbenchServiceAccount)
	id.PodName, id.PodUID = f.auth[tok].PodName, f.auth[tok].PodUID
	f.auth[tok] = id
	w := f.do(http.MethodPost, protocol.HeartbeatPath(name), tok, status(name))
	if w.Code != http.StatusNoContent {
		t.Fatalf("heartbeat: %d %s", w.Code, w.Body.String())
	}
}
