package apiserver

import (
	"context"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func resumeInboxPod(t *testing.T, f *fixture, token string) {
	t.Helper()
	id := f.auth[token]
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: id.PodName}, &pod); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	pod.UID = uuid.NewUUID()
	pod.ResourceVersion = ""
	pod.CreationTimestamp = metav1.Time{}
	if err := f.c.Create(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	id.PodUID = string(pod.UID)
	f.auth[token] = id
}
func TestInboxResumedExecutorPreservesMessageAndFirstAckActor(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	sender := claimSession(t, f, "sender", humanSA)
	recipient := claimSession(t, f, "recipient", clientSA)
	req := inboxRequest(f, "recipient", "resume")
	w := f.do(http.MethodPost, apiv1.InboxPath, sender, req)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	m := decode[apiv1.InboxResult](t, w).Message
	w = f.do(http.MethodPost, ackPath(m), recipient, struct{}{})
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	first := decode[apiv1.InboxAckResult](t, w).Ack
	resumeInboxPod(t, f, sender)
	resumeInboxPod(t, f, recipient)
	w = f.do(http.MethodPost, apiv1.InboxPath, sender, req)
	if w.Code != 200 || decode[apiv1.InboxResult](t, w).Message.Actor != m.Actor {
		t.Fatal("resumed sender replaced original actor", w.Code, w.Body.String())
	}
	w = f.do(http.MethodGet, inboxPath(m), recipient, nil)
	if w.Code != 200 {
		t.Fatal("original Session resume lost inbox", w.Code, w.Body.String())
	}
	w = f.do(http.MethodPost, ackPath(m), recipient, struct{}{})
	if w.Code != 200 || decode[apiv1.InboxAckResult](t, w).Ack.Actor != first.Actor || decode[apiv1.InboxAckResult](t, w).Ack.CreatedAt != first.CreatedAt {
		t.Fatal("resumed recipient replaced first acknowledgement", w.Code, w.Body.String())
	}
	retained := f.session("recipient")
	delete(retained.Annotations, v1alpha1.AnnotationProjectSnapshot)
	if err := f.c.Update(context.Background(), retained); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodGet, inboxPath(m), recipient, nil)
	if w.Code != http.StatusOK {
		t.Fatal("stored original recipient scope was replaced by current annotation", w.Code, w.Body.String())
	}
	removeInboxSession(t, f, "recipient")
	var cm corev1.ConfigMap
	if err := f.c.Get(context.Background(), f.srv.Projects.Key, &cm); err != nil {
		t.Fatal(err)
	}
	cm.Data["catalog.json"] = "unavailable-current-authority"
	if err := f.c.Update(context.Background(), &cm); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodPost, apiv1.InboxPath, sender, req)
	if w.Code != 200 || decode[apiv1.InboxResult](t, w).Message.UID != m.UID {
		t.Fatal("recorded retry required fresh recipient/catalog", w.Code, w.Body.String())
	}
}

func TestInboxReadRechecksIdentityAfterStorageObservation(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	sender := claimSession(t, f, "sender", humanSA)
	recipient := claimSession(t, f, "recipient", clientSA)
	req := inboxRequest(f, "recipient", "fresh-read")
	w := f.do(http.MethodPost, apiv1.InboxPath, sender, req)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	m := decode[apiv1.InboxResult](t, w).Message
	// Keep the legacy classification valid, then replace its Pod during the
	// uncached message read. No text can leave on the earlier identity proof.
	f.srv.Inbox.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if key.Name == m.Name {
			resumeInboxPod(t, f, recipient)
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	w = f.do(http.MethodGet, inboxPath(m), recipient, nil)
	wantError(t, w, 404, apiv1.CodeNotFound)
}

func TestInboxLiveProofRejectsDeletingPodAndWrongControllerKind(t *testing.T) {
	for _, kind := range []string{"deleting-pod", "controller-kind", "deleting-sender"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			bindCatalogFixture(t, f)
			enableInbox(f)
			sender := claimSession(t, f, "sender", humanSA)
			claimSession(t, f, "recipient", clientSA)
			var pod corev1.Pod
			key := types.NamespacedName{Namespace: sessionNS, Name: "sender"}
			if err := f.c.Get(context.Background(), key, &pod); err != nil {
				t.Fatal(err)
			}
			if kind == "deleting-sender" {
				if err := f.c.Delete(context.Background(), f.session("sender")); err != nil {
					t.Fatal(err)
				}
			} else {
				if kind == "deleting-pod" {
					pod.Finalizers = []string{"test.invalid/retain"}
					if err := f.c.Update(context.Background(), &pod); err != nil {
						t.Fatal(err)
					}
					if err := f.c.Delete(context.Background(), &pod); err != nil {
						t.Fatal(err)
					}
				} else {
					pod.OwnerReferences[0].Kind = "Foreign"
					if err := f.c.Update(context.Background(), &pod); err != nil {
						t.Fatal(err)
					}
				}
			}
			w := f.do(http.MethodPost, apiv1.InboxPath, sender, inboxRequest(f, "recipient", "proof"))
			if w.Code != 404 {
				t.Fatal("stale/foreign caller accepted", w.Code, w.Body.String())
			}
		})
	}
}
