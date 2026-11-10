package main

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/testenv"
)

func TestParentNoteInboxRejectsNilConfig(t *testing.T) {
	if _, err := parentNoteInbox(nil, nil, nil, runtime.NewScheme(), "dev-env-system"); err == nil {
		t.Fatal("nil config produced a watch client")
	}
}

func TestParentNoteInboxUsesManagerConfigAndScheme(t *testing.T) {
	env, err := testenv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme, coord.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c, err := client.New(env.Config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	store, err := parentNoteInbox(c, c, env.Config, scheme, "dev-env-system")
	if err != nil {
		t.Fatal(err)
	}
	if store == nil || store.Client != c || store.Live != c || store.Watcher == nil || store.Namespace != "dev-env-system" {
		t.Fatal("inbox store did not keep the manager client, reader, namespace and a watch client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "parent-watch-"}}
	if err := c.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	stream, err := store.Watcher.Watch(ctx, &coord.InboxMessageList{}, client.InNamespace(ns.Name))
	if err != nil {
		t.Fatal(err)
	}
	stream.Stop()

	bare, err := parentNoteInbox(c, c, env.Config, runtime.NewScheme(), ns.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bare.Watcher.Watch(ctx, &coord.InboxMessageList{}, client.InNamespace(ns.Name)); err == nil {
		t.Fatal("a watch client built without the manager scheme watched inboxmessages")
	}
}
