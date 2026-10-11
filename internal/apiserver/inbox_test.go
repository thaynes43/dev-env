package apiserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

func enableInbox(f *fixture) {
	writer := interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		obj.SetUID(uuid.NewUUID())
		return c.Create(ctx, obj, opts...)
	}})
	f.srv.Inbox = &inbox.KubeStore{Client: writer, Live: f.c, Namespace: "dev-env-system"}
	f.srv.InboxProjects = []string{"sample"}
}
func inboxRequest(f *fixture, recipient, key string) apiv1.PublishInboxRequest {
	s := f.session(recipient)
	return apiv1.PublishInboxRequest{Project: "sample", Recipient: apiv1.TaskClaimOwner{Namespace: s.Namespace, Name: s.Name, UID: string(s.UID)}, Key: key, Text: "  PRIVATE_INBOX_EXACT_TEXT\n"}
}
func inboxPath(m apiv1.InboxMetadata) string {
	return apiv1.InboxPath + "/" + m.Name + "?project=sample"
}
func ackPath(m apiv1.InboxMetadata) string {
	return apiv1.InboxPath + "/" + m.Name + "/ack?project=sample"
}
func removeInboxSession(t *testing.T, f *fixture, name string) {
	t.Helper()
	s := f.session(name)
	if err := f.c.Delete(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	s = f.session(name)
	s.Finalizers = nil
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func TestInboxTwoAPIsAtomicRetryAndReconstruction(t *testing.T) {
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	sender := claimSession(t, f, "sender", humanSA)
	recipient := claimSession(t, f, "recipient", clientSA)
	req := inboxRequest(f, "recipient", "logical-1")
	second := &Server{Client: f.c, Live: f.c, Auth: f.auth, Policy: f.srv.Policy, Projects: f.srv.Projects, Inbox: &inbox.KubeStore{Client: f.srv.Inbox.Client, Live: f.c, Namespace: "dev-env-system"}, InboxProjects: []string{"sample"}, Now: f.srv.Now, Log: f.srv.Log}
	hs := []http.Handler{f.h, second.Handler()}
	result := make([]*httptest.ResponseRecorder, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range hs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r := httptest.NewRequest(http.MethodPost, apiv1.InboxPath, strings.NewReader(`{"project":"sample","recipient":{"namespace":"dev-agents","name":"recipient","uid":"`+req.Recipient.UID+`"},"key":"logical-1","text":"  PRIVATE_INBOX_EXACT_TEXT\n"}`))
			r.Header.Set("Authorization", "Bearer "+sender)
			result[i] = httptest.NewRecorder()
			hs[i].ServeHTTP(result[i], r)
		}(i)
	}
	close(start)
	wg.Wait()
	codes := []int{result[0].Code, result[1].Code}
	slices.Sort(codes)
	if !slices.Equal(codes, []int{http.StatusOK, http.StatusCreated}) {
		t.Fatalf("atomic results %d %s %d %s", result[0].Code, result[0].Body.String(), result[1].Code, result[1].Body.String())
	}
	m := decode[apiv1.InboxResult](t, result[0]).Message
	other := decode[apiv1.InboxResult](t, result[1]).Message
	if m.UID == "" || m.UID != other.UID {
		t.Fatal("duplicate messages")
	}
	if strings.Contains(result[0].Body.String(), req.Text) || strings.Contains(result[0].Body.String(), "PRIVATE_PROMPT") || m.Provenance.InitialPinState != "Unknown" {
		t.Fatal("publication metadata leaked text or inflated source")
	}
	f.h = second.Handler()
	w := f.do(http.MethodPost, apiv1.InboxPath, sender, req)
	if w.Code != 200 || !reflect.DeepEqual(decode[apiv1.InboxResult](t, w).Message, m) {
		t.Fatal("reconstructed retry changed identity", w.Body.String())
	}
	changed := req
	changed.Text += "changed"
	w = f.do(http.MethodPost, apiv1.InboxPath, sender, changed)
	if w.Code != 409 {
		t.Fatal("changed immutable intent accepted", w.Code, w.Body.String())
	}
	w = f.do(http.MethodGet, inboxPath(m), recipient, nil)
	if w.Code != 200 || decode[apiv1.InboxMessage](t, w).Text != req.Text {
		t.Fatal("original recipient read", w.Code, w.Body.String())
	}
	w = f.do(http.MethodGet, ackPath(m), recipient, nil)
	wantError(t, w, 404, apiv1.CodeNotFound)
	w = f.do(http.MethodGet, apiv1.InboxPath+"?project=sample", recipient, nil)
	list := decode[apiv1.InboxList](t, w)
	if w.Code != 200 || len(list.Items) != 1 || strings.Contains(w.Body.String(), "PRIVATE_INBOX_EXACT_TEXT") {
		t.Fatal("list leaked text", w.Body.String())
	}
	w = f.do(http.MethodPost, ackPath(m), recipient, struct{}{})
	if w.Code != 201 {
		t.Fatal("ack", w.Code, w.Body.String())
	}
	a := decode[apiv1.InboxAckResult](t, w).Ack
	w = f.do(http.MethodPost, ackPath(m), recipient, struct{}{})
	if w.Code != 200 || !reflect.DeepEqual(decode[apiv1.InboxAckResult](t, w).Ack, a) {
		t.Fatal("ack duplicate changed attribution", w.Body.String())
	}
	removeInboxSession(t, f, "sender")
	second.InboxCoordinatorReadProjects = []string{"sample"}
	w = f.do(http.MethodGet, inboxPath(m), tokCoordinator, nil)
	if w.Code != 200 {
		t.Fatal("source deletion hid durable content", w.Code, w.Body.String())
	}
}

func TestInboxOfflineDeletingMissingAndReplacementRecipient(t *testing.T) {
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	sender := claimSession(t, f, "sender", humanSA)
	recipient := claimSession(t, f, "recipient", clientSA)
	s := f.session("recipient")
	s.Status.Phase = v1alpha1.PhaseSuspended
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	req := inboxRequest(f, "recipient", "stopped")
	w := f.do(http.MethodPost, apiv1.InboxPath, sender, req)
	if w.Code != 201 {
		t.Fatal("stopped publication", w.Code, w.Body.String())
	}
	m := decode[apiv1.InboxResult](t, w).Message
	s = f.session("recipient")
	if err := f.c.Delete(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	req.Key = "deleting"
	w = f.do(http.MethodPost, apiv1.InboxPath, sender, req)
	if w.Code != 201 {
		t.Fatal("deleting publication", w.Code, w.Body.String())
	}
	w = f.do(http.MethodGet, inboxPath(m), recipient, nil)
	if w.Code != 200 {
		t.Fatal("deleting original recipient could not read", w.Code, w.Body.String())
	}
	w = f.do(http.MethodPost, ackPath(m), recipient, struct{}{})
	if w.Code != 201 {
		t.Fatal("deleting recipient ack", w.Code, w.Body.String())
	}
	removeInboxSession(t, f, "recipient")
	req.Key = "stopped"
	w = f.do(http.MethodPost, apiv1.InboxPath, sender, req)
	if w.Code != 200 || decode[apiv1.InboxResult](t, w).Message.UID != m.UID {
		t.Fatal("accepted retry required absent recipient", w.Code, w.Body.String())
	}
	req.Key = "missing-new"
	w = f.do(http.MethodPost, apiv1.InboxPath, sender, req)
	wantError(t, w, 404, apiv1.CodeNotFound)
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "recipient"}, &pod); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	replacement := claimSession(t, f, "recipient", clientSA)
	for _, path := range []string{inboxPath(m), ackPath(m)} {
		w = f.do(http.MethodGet, path, replacement, nil)
		wantError(t, w, 404, apiv1.CodeNotFound)
	}
	w = f.do(http.MethodPost, ackPath(m), replacement, struct{}{})
	wantError(t, w, 404, apiv1.CodeNotFound)
	w = f.do(http.MethodGet, apiv1.InboxPath+"?project=sample", replacement, nil)
	if w.Code != 200 || len(decode[apiv1.InboxList](t, w).Items) != 0 {
		t.Fatal("replacement adopted old inbox", w.Body.String())
	}
	f.srv.InboxCoordinatorReadProjects = []string{"sample"}
	w = f.do(http.MethodGet, inboxPath(m), tokCoordinator, nil)
	if w.Code != 200 || decode[apiv1.InboxMessage](t, w).Recipient.UID != m.Recipient.UID {
		t.Fatal("coordinator original offline read failed", w.Body.String())
	}
}

func TestInboxSeparateContentACLAndNoProviderSideEffects(t *testing.T) {
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	enableClaims(f)
	executor := &fakeExec{}
	f.srv.Exec = executor
	sender := claimSession(t, f, "sender", humanSA)
	recipient := claimSession(t, f, "recipient", clientSA)
	sibling := claimSession(t, f, "sibling", coordinatorSA)
	req := inboxRequest(f, "recipient", "content-acl")
	w := f.do(http.MethodPost, apiv1.InboxPath, sender, req)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	m := decode[apiv1.InboxResult](t, w).Message
	for _, token := range []string{tokHuman, tokClient, tokStranger, sender, sibling, tokCoordinator} {
		w = f.do(http.MethodGet, inboxPath(m), token, nil)
		wantError(t, w, 404, apiv1.CodeNotFound)
		if strings.Contains(w.Body.String(), "PRIVATE_INBOX") || strings.Contains(w.Body.String(), m.Name) {
			t.Fatal("foreign read disclosed content")
		}
	}
	f.srv.InboxCoordinatorReadProjects = []string{"sample"}
	w = f.do(http.MethodGet, inboxPath(m), tokCoordinator, nil)
	if w.Code != 200 {
		t.Fatal("explicit scoped coordinator denied", w.Code, w.Body.String())
	}
	for _, token := range []string{tokCoordinator, tokHuman, tokClient} {
		w = f.do(http.MethodPost, ackPath(m), token, struct{}{})
		wantError(t, w, 404, apiv1.CodeNotFound)
		w = f.do(http.MethodPost, apiv1.InboxPath, token, req)
		wantError(t, w, 404, apiv1.CodeNotFound)
	}
	f.srv.InboxProjects = []string{"other"}
	w = f.do(http.MethodGet, inboxPath(m), recipient, nil)
	wantError(t, w, 404, apiv1.CodeNotFound)
	// Any legacy exec path would fail: durable routes must not invoke it.
	if len(executor.cmds) != 0 {
		t.Fatal("durable inbox invoked a provider executor", executor.cmds)
	}
}

func TestInboxLostAcknowledgementUnknownAndRecipientFence(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	sender := claimSession(t, f, "sender", humanSA)
	claimSession(t, f, "recipient", clientSA)
	base := f.srv.Inbox.Client.(client.WithWatch)
	f.srv.Inbox.Client = interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if err := c.Create(ctx, obj, opts...); err != nil {
			return err
		}
		return errors.New("synthetic lost ACK")
	}})
	req := inboxRequest(f, "recipient", "lost")
	w := f.do(http.MethodPost, apiv1.InboxPath, sender, req)
	if w.Code != 200 {
		t.Fatal("lost ACK failed confirmation", w.Code, w.Body.String())
	}
	f.srv.Inbox.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		return errors.New("synthetic unavailable")
	}})
	w = f.do(http.MethodPost, apiv1.InboxPath, sender, req)
	wantError(t, w, 503, apiv1.CodeUnavailable)
	f.srv.Inbox.Live = f.c
	// Change the recipient while the named catalog is being rechecked.
	f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*corev1.ConfigMap); ok {
			s := f.session("recipient")
			s.Annotations["changed"] = "before-create"
			if err := f.c.Update(ctx, s); err != nil {
				t.Fatal(err)
			}
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	req.Key = "fenced"
	w = f.do(http.MethodPost, apiv1.InboxPath, sender, req)
	wantError(t, w, 404, apiv1.CodeNotFound)
	name := inbox.MessageName(sessionIdentity(f.session("sender")), req.Key)
	if _, err := f.srv.Inbox.Get(context.Background(), name); !errors.Is(err, inbox.ErrNotFound) {
		t.Fatal("recipient drift created message", err)
	}
}

func TestInboxRequestUTF8BoundsAndDefaultOff(t *testing.T) {
	f := newFixture(t)
	if w := f.do(http.MethodPost, apiv1.InboxPath, tokHuman, struct{}{}); w.Code != 503 {
		t.Fatal("inbox enabled by default", w.Code)
	}
	bindCatalogFixture(t, f)
	enableInbox(f)
	sender := claimSession(t, f, "sender", humanSA)
	claimSession(t, f, "recipient", clientSA)
	for n, text := range []string{strings.Repeat("a", 16<<10), strings.Repeat("é", 8<<10), strings.Repeat("\x00", 16<<10)} {
		req := inboxRequest(f, "recipient", []string{"ascii", "unicode", "escaped"}[n])
		req.Text = text
		w := f.do(http.MethodPost, apiv1.InboxPath, sender, req)
		if w.Code != 201 {
			t.Fatal("exact UTF-8 bound refused", n, w.Code, w.Body.String())
		}
	}
	req := inboxRequest(f, "recipient", "overflow")
	req.Text = strings.Repeat("é", (8<<10)+1)
	wantError(t, f.do(http.MethodPost, apiv1.InboxPath, sender, req), 400, apiv1.CodeBadRequest)
	for _, body := range []string{`{"sender":{},"project":"sample"}`, "{\"text\":\"" + string([]byte{0xff}) + "\"}", `{} {}`, strings.Repeat(" ", inbox.MaxRequestBytes+1)} {
		wantError(t, f.do(http.MethodPost, apiv1.InboxPath, sender, body), 400, apiv1.CodeBadRequest)
	}
}
