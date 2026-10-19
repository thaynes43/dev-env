package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/internal/claudeauth"
)

var claudeNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

const claudeUID = "0a1b2c3d-0000-4000-8000-0123456789ab"

func syntheticClaudeAccess(gen uint64, written time.Time, life time.Duration) claudeauth.Access {
	return claudeauth.Access{Generation: gen, AccessToken: claudeAccessCanary, ExpiresAt: written.Add(life), Scopes: []string{"user:profile", "user:inference"},
		SubscriptionType: "max", RateLimitTier: "default_claude_max_20x", AccountUUID: "acct-1", OrganizationUUID: "org-1"}
}

func syntheticClaudeRecord(gen uint64) claudeRecord {
	return claudeRecord{Access: syntheticClaudeAccess(gen, claudeNow, 8*time.Hour), Refresh: newSecretValue(claudeOldRefreshCanary), RefreshExpiresAt: claudeNow.Add(30 * 24 * time.Hour), LastRefresh: claudeNow, Stage: claudeReady}
}

func claudeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func claudeJournalFixture(t *testing.T) (*claudeJournal, client.Client) {
	t.Helper()
	name := types.NamespacedName{Namespace: "system", Name: DefaultClaudeJournalSecret}
	c := claudeClient(t, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: name.Namespace, Name: name.Name}})
	return &claudeJournal{Client: c, Secret: name}, c
}

func saveClaude(t *testing.T, j *claudeJournal, r claudeRecord) {
	t.Helper()
	s, _, err := j.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := j.save(context.Background(), s, r); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeJournalEmptyIsNeedsLogin(t *testing.T) {
	j, _ := claudeJournalFixture(t)
	_, r, err := j.load(context.Background())
	if err != nil || r.Stage != claudeNeedsLogin || r.Access.Generation != 0 || !r.Refresh.Empty() {
		t.Fatalf("empty journal: %v %s", err, r.Stage)
	}
}

func TestClaudeJournalRoundTripAndRedaction(t *testing.T) {
	j, c := claudeJournalFixture(t)
	want := syntheticClaudeRecord(4)
	saveClaude(t, j, want)
	_, got, err := j.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Refresh.Reveal() != claudeOldRefreshCanary || !sameClaudeAccess(got.Access, want.Access) || !got.RefreshExpiresAt.Equal(want.RefreshExpiresAt) || got.Stage != claudeReady {
		t.Fatal("round trip changed the record")
	}
	// The private Secret does hold the refresh credential; nothing else may print it.
	var s corev1.Secret
	if err := c.Get(context.Background(), j.Secret, &s); err != nil || !strings.Contains(string(s.Data[claudeJournalKey]), claudeOldRefreshCanary) {
		t.Fatal("journal Secret does not hold the credential")
	}
	text := fmt.Sprintf("%v|%+v|%#v|%s|%q", got, got, got, got, got)
	if b, err := got.MarshalJSON(); err == nil {
		text += string(b)
	}
	text += slog.AnyValue(got).String() + fmt.Sprint(got.LogValue(), got.MarshalLog(), got.Access, got.Refresh)
	assertNoClaudeCanary(t, text)
}

func TestClaudeJournalCAS(t *testing.T) {
	j, _ := claudeJournalFixture(t)
	first, _, err := j.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stale, _, err := j.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := j.save(context.Background(), first, syntheticClaudeRecord(1)); err != nil {
		t.Fatal(err)
	}
	err = j.save(context.Background(), stale, syntheticClaudeRecord(2))
	if !errors.Is(err, errClaudeJournal) {
		t.Fatalf("a stale resourceVersion must lose the CAS, got %v", err)
	}
	_, r, _ := j.load(context.Background())
	if r.Access.Generation != 1 {
		t.Fatal("the losing writer changed the journal")
	}
	assertNoClaudeCanary(t, err.Error())
}

func TestClaudeJournalGenerationStrictlyIncreasesAcrossRestart(t *testing.T) {
	j, c := claudeJournalFixture(t)
	saveClaude(t, j, syntheticClaudeRecord(3))
	// A restarted keeper has only the Secret.
	restarted := &claudeJournal{Client: c, Secret: j.Secret}
	s, r, err := restarted.load(context.Background())
	if err != nil || r.Access.Generation != 3 {
		t.Fatalf("restart lost the generation: %v", err)
	}
	gen, err := nextClaudeGeneration(r.Access.Generation, 0)
	if err != nil || gen != 4 {
		t.Fatalf("next generation = %d, %v", gen, err)
	}
	next, err := applyClaudeRefresh(r, claudeTokens{Access: newSecretValue("access-two"), ExpiresIn: 8 * time.Hour}, gen, claudeNow.Add(7*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.save(context.Background(), s, next); err != nil {
		t.Fatal(err)
	}
	// A lower generation, or a replayed older record, is refused even with a fresh resourceVersion.
	s, _, _ = restarted.load(context.Background())
	for _, g := range []uint64{1, 2, 3} {
		if restarted.save(context.Background(), s, syntheticClaudeRecord(g)) == nil {
			t.Fatalf("generation %d was saved over 4", g)
		}
	}
	// The same generation with different material is refused; a stage change alone is allowed.
	other := next
	other.Access.AccessToken = "different-access"
	if restarted.save(context.Background(), s, other) == nil {
		t.Fatal("changed material saved under an unchanged generation")
	}
	intent := next
	intent.Stage, intent.AttemptUID = claudeIntent, claudeUID
	if err := restarted.save(context.Background(), s, intent); err != nil {
		t.Fatalf("stage change under the same generation: %v", err)
	}
}

func TestClaudeNextGenerationUsesPublishedFloor(t *testing.T) {
	// The journal Secret was re-created (generation 0) while the live Secret kept 7.
	for _, c := range []struct{ journal, published, want uint64 }{{0, 0, 1}, {3, 0, 4}, {0, 7, 8}, {9, 7, 10}, {7, 7, 8}} {
		got, err := nextClaudeGeneration(c.journal, c.published)
		if err != nil || got != c.want {
			t.Fatalf("next(%d,%d) = %d, %v", c.journal, c.published, got, err)
		}
	}
	if _, err := nextClaudeGeneration(^uint64(0), 0); !errors.Is(err, errClaudeGeneration) {
		t.Fatal("overflow was not refused")
	}
}

func TestClaudeRecordValidation(t *testing.T) {
	mut := func(f func(*claudeRecord)) claudeRecord { r := syntheticClaudeRecord(2); f(&r); return r }
	bad := map[string]claudeRecord{
		"unknown stage":         mut(func(r *claudeRecord) { r.Stage = "Other" }),
		"ready without refresh": mut(func(r *claudeRecord) { r.Refresh = secretValue{} }),
		"intent without uid":    mut(func(r *claudeRecord) { r.Stage = claudeIntent }),
		"bad uid":               mut(func(r *claudeRecord) { r.Stage, r.AttemptUID = claudeIntent, "not-a-uid" }),
		"zero generation":       mut(func(r *claudeRecord) { r.Access.Generation = 0 }),
		"empty access":          mut(func(r *claudeRecord) { r.Access.AccessToken = "" }),
		"expired when written":  mut(func(r *claudeRecord) { r.LastRefresh = r.Access.ExpiresAt.Add(time.Second) }),
		"no scopes":             mut(func(r *claudeRecord) { r.Access.Scopes = nil }),
		"no uuid":               mut(func(r *claudeRecord) { r.Access.AccountUUID = "" }),
		"huge refresh":          mut(func(r *claudeRecord) { r.Refresh = newSecretValue(strings.Repeat("r", claudeauth.MaxTokenBytes+1)) }),
		"gen zero with token":   {Stage: claudeNeedsLogin, Refresh: newSecretValue("x")},
		"gen zero ready":        {Stage: claudeReady},
	}
	for name, r := range bad {
		if validClaudeRecord(r) {
			t.Errorf("%s was accepted", name)
		}
	}
	for name, r := range map[string]claudeRecord{
		"ready":                  syntheticClaudeRecord(2),
		"intent":                 mut(func(r *claudeRecord) { r.Stage, r.AttemptUID = claudeIntent, claudeUID }),
		"needs login, refresh":   mut(func(r *claudeRecord) { r.Stage = claudeNeedsLogin }),
		"needs login, tombstone": mut(func(r *claudeRecord) { r.Stage, r.Refresh = claudeNeedsLogin, secretValue{} }),
		"empty":                  {Stage: claudeNeedsLogin},
	} {
		if !validClaudeRecord(r) {
			t.Errorf("%s was refused", name)
		}
	}
}

func TestClaudeJournalRefusesCorruptStoredState(t *testing.T) {
	good := syntheticClaudeRecord(2)
	raw, _ := jsonForClaude(good)
	for name, data := range map[string]string{
		"not json":      "refresh=" + claudeOldRefreshCanary,
		"unknown field": strings.Replace(raw, `"version":1`, `"version":1,"extra":"`+claudeOldRefreshCanary+`"`, 1),
		"wrong version": strings.Replace(raw, `"version":1`, `"version":2`, 1),
		"duplicate":     strings.Replace(raw, `"version":1`, `"version":1,"version":1`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			j, c := claudeJournalFixture(t)
			var s corev1.Secret
			if err := c.Get(context.Background(), j.Secret, &s); err != nil {
				t.Fatal(err)
			}
			s.Data = map[string][]byte{claudeJournalKey: []byte(data)}
			if err := c.Update(context.Background(), &s); err != nil {
				t.Fatal(err)
			}
			_, _, err := j.load(context.Background())
			if !errors.Is(err, errClaudeJournal) {
				t.Fatalf("want the fixed error, got %v", err)
			}
			assertNoClaudeCanary(t, err.Error())
			// A corrupt stored record also blocks a save: the guard cannot be bypassed.
			if err := j.save(context.Background(), &s, syntheticClaudeRecord(9)); !errors.Is(err, errClaudeJournal) {
				t.Fatalf("save over corrupt state: %v", err)
			}
		})
	}
}

func jsonForClaude(r claudeRecord) (string, error) {
	b, err := json.Marshal(claudeWire(r))
	return string(b), err
}

func TestApplyClaudeRefresh(t *testing.T) {
	prev := syntheticClaudeRecord(2)
	now := claudeNow.Add(7 * time.Hour)
	t.Run("absent refresh_token keeps the old one and its expiry", func(t *testing.T) {
		next, err := applyClaudeRefresh(prev, claudeTokens{Access: newSecretValue("access-two"), ExpiresIn: 8 * time.Hour}, 3, now)
		if err != nil {
			t.Fatal(err)
		}
		if next.Refresh.Reveal() != claudeOldRefreshCanary || !next.RefreshExpiresAt.Equal(prev.RefreshExpiresAt) || next.Access.AccessToken != "access-two" ||
			!next.Access.ExpiresAt.Equal(now.Add(8*time.Hour)) || next.Access.Generation != 3 || next.Stage != claudeReady || !next.LastRefresh.Equal(now) {
			t.Fatal("absent refresh_token changed the credential")
		}
		if strings.Join(next.Access.Scopes, " ") != "user:profile user:inference" || next.Access.AccountUUID != "acct-1" || next.Access.SubscriptionType != "max" {
			t.Fatal("metadata was not carried over")
		}
	})
	t.Run("rotated refresh_token replaces it", func(t *testing.T) {
		next, err := applyClaudeRefresh(prev, claudeTokens{Access: newSecretValue("access-two"), Refresh: newSecretValue(claudeNewRefreshCanary), ExpiresIn: 8 * time.Hour, RefreshExpiresIn: 30 * 24 * time.Hour,
			Scopes: []string{"user:profile"}, AccountUUID: "acct-2", OrganizationUUID: "org-2"}, 3, now)
		if err != nil {
			t.Fatal(err)
		}
		if next.Refresh.Reveal() != claudeNewRefreshCanary || !next.RefreshExpiresAt.Equal(now.Add(30*24*time.Hour)) ||
			strings.Join(next.Access.Scopes, " ") != "user:profile" || next.Access.AccountUUID != "acct-2" || next.Access.OrganizationUUID != "org-2" {
			t.Fatal("rotation was not applied")
		}
	})
	t.Run("rotated without a lifetime does not inherit the old expiry", func(t *testing.T) {
		next, err := applyClaudeRefresh(prev, claudeTokens{Access: newSecretValue("access-two"), Refresh: newSecretValue(claudeNewRefreshCanary), ExpiresIn: 8 * time.Hour}, 3, now)
		if err != nil || !next.RefreshExpiresAt.IsZero() {
			t.Fatal("old refresh expiry was attached to a new token")
		}
	})
	t.Run("a generation that does not increase is refused", func(t *testing.T) {
		for _, g := range []uint64{0, 1, 2} {
			if _, err := applyClaudeRefresh(prev, claudeTokens{Access: newSecretValue("a"), ExpiresIn: time.Hour}, g, now); err == nil {
				t.Fatalf("generation %d accepted over 2", g)
			}
		}
	})
	t.Run("an empty response is refused", func(t *testing.T) {
		if _, err := applyClaudeRefresh(prev, claudeTokens{ExpiresIn: time.Hour}, 3, now); err == nil {
			t.Fatal("empty access accepted")
		}
		if _, err := applyClaudeRefresh(prev, claudeTokens{Access: newSecretValue("a")}, 3, now); err == nil {
			t.Fatal("zero lifetime accepted")
		}
	})
}
