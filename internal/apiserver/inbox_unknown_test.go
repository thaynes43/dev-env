package apiserver

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

func TestInboxUnknownAfterAcceptedCreateRetriesWithoutRecipient(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	sender := claimSession(t, f, "sender", humanSA)
	claimSession(t, f, "recipient", clientSA)
	accepted := false
	f.srv.Inbox.Client = interceptor.NewClient(f.srv.Inbox.Client.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if err := c.Create(ctx, obj, opts...); err != nil {
			return err
		}
		accepted = true
		return errors.New("synthetic lost response")
	}})
	f.srv.Inbox.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if accepted {
			return errors.New("synthetic confirmation unavailable")
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	req := inboxRequest(f, "recipient", "unknown-recorded")
	w := f.do(http.MethodPost, apiv1.InboxPath, sender, req)
	wantError(t, w, http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	if !accepted {
		t.Fatal("test did not reach accepted create")
	}
	f.srv.Inbox.Live = f.c
	name := inbox.MessageName(sessionIdentity(f.session("sender")), req.Key)
	var original coord.InboxMessage
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: "dev-env-system", Name: name}, &original); err != nil {
		t.Fatal(err)
	}
	removeInboxSession(t, f, "recipient")
	w = f.do(http.MethodPost, apiv1.InboxPath, sender, req)
	if w.Code != http.StatusOK || decode[apiv1.InboxResult](t, w).Message.UID != string(original.UID) {
		t.Fatal("Unknown retry created or lost message", w.Code, w.Body.String())
	}
	req.Recipient.UID = "replacement-uid"
	w = f.do(http.MethodPost, apiv1.InboxPath, sender, req)
	if w.Code != http.StatusConflict || decode[apiv1.InboxResult](t, w).Message.UID != string(original.UID) {
		t.Fatal("retargeted absent receiver adopted key", w.Code, w.Body.String())
	}
}
