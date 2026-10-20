package apiserver

import (
	"context"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// workbenchClaimSession is claimSession for a session whose pod runs as the
// workbench ServiceAccount, with the token that pod's projected token carries.
func workbenchClaimSession(t *testing.T, f *fixture, name, parent string) string {
	t.Helper()
	claimSession(t, f, name, parent)
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Spec.ServiceAccountName = templates.WorkbenchServiceAccount
	if err := f.c.Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	tok := "tok-workbench-" + name
	id := saIdentity(sessionNS + "/" + templates.WorkbenchServiceAccount)
	id.PodName, id.PodUID = name, string(pod.UID)
	f.auth[tok] = id
	return tok
}

// A workbench session is the session it is for the coordination routes (task
// claims, inbox, published contexts), as it already is for the heartbeat: its live
// Pod runs as dev-env-workbench, which those routes' fence must accept. Only the
// policy field turns that on, and a workbench token bound to no session pod is
// still the client's and gains nothing.
func TestWorkbenchSessionUsesCoordinationRoutes(t *testing.T) {
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableClaims(f)
	enableInbox(f)
	f.srv.Policy.Clients = append(f.srv.Policy.Clients, sessionNS+"/"+templates.WorkbenchServiceAccount)
	f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
	wb := workbenchClaimSession(t, f, "wbsender", humanSA)
	recipient := claimSession(t, f, "wbrecipient", clientSA)

	// Task claims: create and list.
	if w := f.do(http.MethodPost, apiv1.TaskClaimsPath, wb, claimRequest("issue:21")); w.Code != http.StatusCreated {
		t.Fatal("workbench session could not claim", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample", wb, nil); w.Code != http.StatusOK {
		t.Fatal("workbench session could not list claims", w.Code, w.Body.String())
	}

	// Inbox: a workbench session sends to an ordinary one and the recipient reads it.
	req := inboxRequest(f, "wbrecipient", "wb-1")
	w := f.do(http.MethodPost, apiv1.InboxPath, wb, req)
	if w.Code != http.StatusCreated {
		t.Fatal("workbench session could not send", w.Code, w.Body.String())
	}
	sent := decode[apiv1.InboxResult](t, w)
	if w := f.do(http.MethodGet, inboxPath(sent.Message), recipient, nil); w.Code != http.StatusOK {
		t.Fatal("recipient could not read the workbench message", w.Code)
	}
	// A workbench session is also a recipient, and reads only its own inbox.
	back := inboxRequest(f, "wbsender", "wb-2")
	w = f.do(http.MethodPost, apiv1.InboxPath, recipient, back)
	if w.Code != http.StatusCreated {
		t.Fatal("ordinary session could not send to the workbench", w.Code, w.Body.String())
	}
	got := decode[apiv1.InboxResult](t, w)
	if w := f.do(http.MethodGet, inboxPath(got.Message), wb, nil); w.Code != http.StatusOK {
		t.Fatal("workbench recipient could not read", w.Code)
	}
	if w := f.do(http.MethodGet, inboxPath(sent.Message), wb, nil); w.Code != http.StatusNotFound {
		t.Fatal("workbench sender read a message that was not addressed to it", w.Code)
	}

	// The account alone grants nothing: a workbench token bound to no session pod
	// is the client's, which these routes refuse.
	unbound := saIdentity(sessionNS + "/" + templates.WorkbenchServiceAccount)
	f.auth["tok-wb-unbound"] = unbound
	if w := f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample", "tok-wb-unbound", nil); w.Code != http.StatusForbidden {
		t.Fatal("unbound workbench token read claims", w.Code)
	}

	// Without the policy field the pod's account is not a session account again.
	f.srv.Policy.WorkbenchServiceAccount = ""
	if w := f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample", wb, nil); w.Code != http.StatusForbidden {
		t.Fatal("workbench pod accepted with the policy off", w.Code)
	}
}
