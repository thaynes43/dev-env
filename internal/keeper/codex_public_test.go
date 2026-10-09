package keeper

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/codexauth"
)

type codexPublicationRaceClient struct {
	client.Client
	Key         types.NamespacedName
	Replacement []byte
	Patches     int
}

func (c *codexPublicationRaceClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if key == c.Key && c.Replacement != nil {
		var newer corev1.Secret
		if err := c.Client.Get(ctx, key, &newer); err != nil {
			return err
		}
		newer.Data = map[string][]byte{codexauth.LiveKey: c.Replacement}
		c.Replacement = nil
		return c.Client.Update(ctx, &newer)
	}
	return nil
}

func (c *codexPublicationRaceClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.Patches++
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func TestCodexPublicationGenerationCAS(t *testing.T) {
	for _, kind := range []string{"new", "same", "higher", "lower", "expired-lower", "changed-same", "account", "malformed", "race"} {
		t.Run(kind, func(t *testing.T) {
			w, _, _ := codexFixture(t)
			key := types.NamespacedName{Namespace: "agents", Name: DefaultCodexLiveSecret}
			now := w.Clock.Now()
			old := syntheticCodexAccess(t, now, 2, "synthetic-account", 10*24*time.Hour)
			if kind == "expired-lower" {
				old = syntheticCodexAccess(t, now.Add(-11*24*time.Hour), 2, "synthetic-account", 10*24*time.Hour)
			}
			raw, err := codexauth.Encode(old, old.LastRefresh)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "malformed" {
				raw = []byte(`{"generation":2}`)
			}
			if kind == "new" {
				raw = nil
			}
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, UID: "synthetic-secret"}, Data: map[string][]byte{codexauth.LiveKey: raw}}
			if err := w.Journal.Client.Create(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			next := old
			switch kind {
			case "higher":
				next = syntheticCodexAccess(t, now, 3, "synthetic-account", 10*24*time.Hour)
			case "lower", "expired-lower":
				next = syntheticCodexAccess(t, now, 1, "synthetic-account", 10*24*time.Hour)
			case "changed-same":
				next = syntheticCodexAccess(t, now, 2, "synthetic-account", 9*24*time.Hour)
			case "account":
				next = syntheticCodexAccess(t, now, 3, "other-synthetic-account", 10*24*time.Hour)
			case "race":
				next = syntheticCodexAccess(t, now, 3, "synthetic-account", 10*24*time.Hour)
			}
			c := &codexPublicationRaceClient{Client: w.Journal.Client, Key: key}
			if kind == "race" {
				newer := syntheticCodexAccess(t, now, 4, "synthetic-account", 10*24*time.Hour)
				c.Replacement, _ = codexauth.Encode(newer, now)
			}
			p := &codexPublicSecret{Client: c, Secret: key}
			err = p.publish(context.Background(), next, now)
			wantOK := kind == "new" || kind == "same" || kind == "higher"
			if (err == nil) != wantOK {
				t.Fatal("incorrect publication admission")
			}
			var after corev1.Secret
			if err := c.Client.Get(context.Background(), key, &after); err != nil {
				t.Fatal(err)
			}
			if kind == "race" {
				a, err := codexauth.DecodeStored(after.Data[codexauth.LiveKey])
				if err != nil || a.Generation != 4 || c.Patches != 1 {
					t.Fatal("stale CAS overwrote newer generation or retried")
				}
			} else if !wantOK && string(after.Data[codexauth.LiveKey]) != string(raw) {
				t.Fatal("refused material was modified")
			}
			if kind == "same" && c.Patches != 0 {
				t.Fatal("identical generation was not idempotent")
			}
		})
	}
}
