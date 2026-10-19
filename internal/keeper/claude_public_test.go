package keeper

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/claudeauth"
)

type claudePublicationRaceClient struct {
	client.Client
	Key         types.NamespacedName
	Replacement []byte
	Patches     int
}

func (c *claudePublicationRaceClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if key == c.Key && c.Replacement != nil {
		var newer corev1.Secret
		if err := c.Client.Get(ctx, key, &newer); err != nil {
			return err
		}
		newer.Data = map[string][]byte{claudeauth.LiveKey: c.Replacement}
		c.Replacement = nil
		return c.Update(ctx, &newer)
	}
	return nil
}

func (c *claudePublicationRaceClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.Patches++
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func claudePublicFixture(t *testing.T, raw []byte) (*claudePublicSecret, *claudePublicationRaceClient) {
	t.Helper()
	key := types.NamespacedName{Namespace: "agents", Name: DefaultClaudeLiveSecret}
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, UID: "synthetic-secret"}}
	if raw != nil {
		s.Data = map[string][]byte{claudeauth.LiveKey: raw}
	}
	rc := &claudePublicationRaceClient{Client: claudeClient(t, s), Key: key}
	return &claudePublicSecret{Client: rc, Secret: key}, rc
}

func encodeClaude(t *testing.T, a claudeauth.Access, at time.Time) []byte {
	t.Helper()
	raw, err := claudeauth.Encode(a, at)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func liveClaude(t *testing.T, p *claudePublicSecret) corev1.Secret {
	t.Helper()
	var s corev1.Secret
	if err := p.Client.Get(context.Background(), p.Secret, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestClaudePublicationGenerationCAS(t *testing.T) {
	old := syntheticClaudeAccess(2, claudeNow, 8*time.Hour)
	oldRaw := encodeClaude(t, old, claudeNow)
	// An outage: the stored generation expired long ago but must stay comparable.
	later := claudeNow.Add(40 * time.Hour)
	for _, kind := range []string{"new", "same", "higher", "lower", "expired-old-higher", "expired-old-lower", "changed-same", "account", "malformed", "refresh-member", "race"} {
		t.Run(kind, func(t *testing.T) {
			var seed []byte
			now := claudeNow
			next := old
			wantOK := true
			switch kind {
			case "new":
			case "same":
				seed = oldRaw
			case "higher":
				seed = oldRaw
				next.Generation = 3
			case "lower":
				seed, wantOK = oldRaw, false
				next.Generation = 1
			case "expired-old-higher":
				seed, now = oldRaw, later
				next = syntheticClaudeAccess(3, later, 8*time.Hour)
			case "expired-old-lower":
				seed, now, wantOK = encodeClaude(t, syntheticClaudeAccess(5, claudeNow, 8*time.Hour), claudeNow), later, false
				next = syntheticClaudeAccess(4, later, 8*time.Hour)
			case "changed-same":
				seed, wantOK = oldRaw, false
				next.AccessToken = "different-access"
			case "account":
				seed, wantOK = oldRaw, false
				next.Generation, next.AccountUUID = 3, "acct-other"
			case "malformed":
				seed, wantOK = []byte(`{"generation":2}`), false
				next.Generation = 3
			case "refresh-member":
				seed, wantOK = []byte(strings.Replace(string(oldRaw), `"version":1`, `"version":1,"refreshToken":"`+claudeOldRefreshCanary+`"`, 1)), false
				next.Generation = 3
			case "race":
				seed, wantOK = oldRaw, false
				next.Generation = 3
			}
			p, rc := claudePublicFixture(t, seed)
			if kind == "race" {
				rc.Replacement = encodeClaude(t, syntheticClaudeAccess(9, claudeNow, 8*time.Hour), claudeNow)
			}
			err := p.publish(context.Background(), next, time.Time{}, now)
			if (err == nil) != wantOK {
				t.Fatalf("publish error = %v, want ok=%v", err, wantOK)
			}
			if err != nil && !errors.Is(err, errClaudePublication) {
				t.Fatalf("error is not the fixed one: %v", err)
			}
			got := liveClaude(t, p).Data[claudeauth.LiveKey]
			switch {
			case kind == "same":
				if rc.Patches != 0 {
					t.Fatal("an identical publication patched")
				}
			case wantOK && kind != "new" && kind != "same":
				if a, derr := claudeauth.Decode(got, claudeEpoch); derr != nil || a.Generation != next.Generation {
					t.Fatalf("published generation: %v", derr)
				}
			case !wantOK && kind != "race" && !bytes.Equal(got, seed):
				t.Fatal("a refused publication changed the Secret")
			case kind == "race":
				if a, derr := claudeauth.Decode(got, claudeEpoch); derr != nil || a.Generation != 9 {
					t.Fatal("the newer concurrent publication was overwritten")
				}
			}
		})
	}
}

func TestClaudePublicationHasNoRefreshMaterial(t *testing.T) {
	p, _ := claudePublicFixture(t, nil)
	rec := syntheticClaudeRecord(5)
	refreshExp := claudeNow.Add(30 * 24 * time.Hour)
	if err := p.publish(context.Background(), rec.Access, refreshExp, claudeNow); err != nil {
		t.Fatal(err)
	}
	s := liveClaude(t, p)
	var all strings.Builder
	for k, v := range s.Data {
		all.WriteString(k + string(v))
	}
	for k, v := range s.Annotations {
		all.WriteString(k + v)
	}
	text := strings.ToLower(all.String())
	if strings.Contains(text, strings.ToLower(claudeOldRefreshCanary)) || strings.Contains(text, "refreshtoken") || strings.Contains(strings.ToLower(string(s.Data[claudeauth.LiveKey])), "refresh") {
		t.Fatal("published Secret carries refresh material")
	}
	if s.Annotations[AnnotationRefreshExpiresAt] != refreshExp.Format(time.RFC3339) || s.Annotations[AnnotationExpiresAt] != rec.Access.ExpiresAt.Format(time.RFC3339) || s.Annotations[AnnotationWrittenAt] != claudeNow.Format(time.RFC3339) {
		t.Fatal("annotations are not the non-secret expiries")
	}
	a, err := claudeauth.Decode(s.Data[claudeauth.LiveKey], claudeNow)
	if err != nil || a.AccessToken != claudeAccessCanary || a.Generation != 5 {
		t.Fatal("published document does not decode to the projection")
	}
	// Without a refresh expiry the annotation is absent; an invalid generation publishes nothing.
	p, _ = claudePublicFixture(t, nil)
	if err := p.publish(context.Background(), rec.Access, time.Time{}, claudeNow); err != nil {
		t.Fatal(err)
	}
	if _, ok := liveClaude(t, p).Annotations[AnnotationRefreshExpiresAt]; ok {
		t.Fatal("empty refresh expiry was annotated")
	}
	p, rc := claudePublicFixture(t, nil)
	bad := rec.Access
	bad.Generation = 0
	if err := p.publish(context.Background(), bad, time.Time{}, claudeNow); !errors.Is(err, errClaudePublication) || rc.Patches != 0 {
		t.Fatal("an invalid access published")
	}
	if err := p.publish(context.Background(), rec.Access, time.Time{}, rec.Access.ExpiresAt.Add(time.Second)); !errors.Is(err, errClaudePublication) || rc.Patches != 0 {
		t.Fatal("an expired access published")
	}
}

func TestClaudePublicationClearsStaleRefreshExpiry(t *testing.T) {
	p, _ := claudePublicFixture(t, nil)
	exp := claudeNow.Add(30 * 24 * time.Hour)
	if err := p.publish(context.Background(), syntheticClaudeAccess(1, claudeNow, 8*time.Hour), exp, claudeNow); err != nil {
		t.Fatal(err)
	}
	if liveClaude(t, p).Annotations[AnnotationRefreshExpiresAt] != exp.Format(time.RFC3339) {
		t.Fatal("refresh expiry was not annotated")
	}
	// A rotation without a stated lifetime: the old token's expiry must go.
	later := claudeNow.Add(7 * time.Hour)
	if err := p.publish(context.Background(), syntheticClaudeAccess(2, later, 8*time.Hour), time.Time{}, later); err != nil {
		t.Fatal(err)
	}
	s := liveClaude(t, p)
	if _, ok := s.Annotations[AnnotationRefreshExpiresAt]; ok {
		t.Fatal("a stale refresh expiry survived the publication")
	}
	if s.Annotations[AnnotationWrittenAt] != later.Format(time.RFC3339) {
		t.Fatal("the other annotations were not updated")
	}
	// A new expiry is written again.
	exp2 := later.Add(30 * 24 * time.Hour)
	if err := p.publish(context.Background(), syntheticClaudeAccess(3, later, 8*time.Hour), exp2, later); err != nil || liveClaude(t, p).Annotations[AnnotationRefreshExpiresAt] != exp2.Format(time.RFC3339) {
		t.Fatal("refresh expiry was not re-annotated")
	}
}

func TestClaudePublishedGeneration(t *testing.T) {
	p, _ := claudePublicFixture(t, nil)
	if g, err := p.generation(context.Background()); err != nil || g != 0 {
		t.Fatalf("empty = %d, %v", g, err)
	}
	p, _ = claudePublicFixture(t, encodeClaude(t, syntheticClaudeAccess(7, claudeNow, 8*time.Hour), claudeNow))
	if g, err := p.generation(context.Background()); err != nil || g != 7 {
		t.Fatalf("populated = %d, %v", g, err)
	}
	// Even long expired, the generation is still the floor.
	if g, err := (&claudePublicSecret{Client: p.Client, Secret: p.Secret}).generation(context.Background()); err != nil || g != 7 {
		t.Fatal("expired generation was not readable")
	}
	p, _ = claudePublicFixture(t, []byte("not json"))
	if _, err := p.generation(context.Background()); !errors.Is(err, errClaudePublication) {
		t.Fatalf("malformed = %v", err)
	}
	p, _ = claudePublicFixture(t, nil)
	p.Secret.Name = "missing"
	if _, err := p.generation(context.Background()); !errors.Is(err, errClaudePublication) {
		t.Fatalf("missing = %v", err)
	}
	// The floor makes a re-created journal still publish above the live generation.
	p, _ = claudePublicFixture(t, encodeClaude(t, syntheticClaudeAccess(7, claudeNow, 8*time.Hour), claudeNow))
	pub, _ := p.generation(context.Background())
	next, err := nextClaudeGeneration(0, pub)
	if err != nil || next != 8 {
		t.Fatalf("floor = %d, %v", next, err)
	}
	if err := p.publish(context.Background(), syntheticClaudeAccess(next, claudeNow, 8*time.Hour), time.Time{}, claudeNow); err != nil {
		t.Fatal(err)
	}
}
