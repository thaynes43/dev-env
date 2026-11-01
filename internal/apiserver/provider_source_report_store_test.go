package apiserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

const (
	sourceRetrieved   = "2026-01-08T12:00:00Z"
	sourceOlder       = "2026-01-08T11:50:00Z"
	sourceNewer       = "2026-01-08T12:05:00Z"
	sourceAttempt     = "2026-01-08T12:06:00Z"
	sourceExpiredAt   = "2026-01-08T11:00:00Z"
	sourceResets      = "2026-01-15T12:00:00Z"
	sourceEarlyReset  = "2026-01-08T12:05:00Z"
	sourceIncarnation = "synthetic-incarnation-1"
	sourceSlotNS      = "synthetic-system"
	sourceSlotName    = "synthetic-slot"
	sourceEnvNS       = "synthetic-op323-source"
	sourceGitOps      = "synthetic-gitops"
)

var (
	sourceNow      = time.Date(2026, 1, 8, 12, 10, 0, 0, time.UTC)
	sourceEarlyNow = time.Date(2026, 1, 8, 11, 10, 0, 0, time.UTC)
)

func TestProviderSourceStoreDecisions(t *testing.T) {
	t.Run("preserves metadata and replays without a write", func(t *testing.T) {
		mem, store, raw := seededStore(t)
		rv := mem.cm.ResourceVersion
		sum := sha256.Sum256(raw)
		if err := store.Put(context.Background(), raw, sourceNow); err != nil {
			t.Fatal(err)
		}
		if mem.updates != 1 || mem.cm.ResourceVersion != rv || sha256.Sum256([]byte(mem.cm.Data[ProviderSourceDataKey])) != sum {
			t.Fatalf("replay wrote rv %s updates %d", mem.cm.ResourceVersion, mem.updates)
		}
		if !strings.Contains(mem.cm.Data[ProviderSourceDataKey], `"providerUpdatedAt":null`) || strings.Contains(mem.cm.Data[ProviderSourceDataKey], "Measured") {
			t.Fatalf("stored %s", mem.cm.Data[ProviderSourceDataKey])
		}
		view, err := store.Read(context.Background(), sourceNow)
		if err != nil || !view.Fresh || !bytes.Equal(view.Raw, raw) || view.ResourceVersion != rv || mem.updates != 1 {
			t.Fatalf("read %+v updates %d err %v", view, mem.updates, err)
		}
		restarted := *store
		again, err := restarted.Read(context.Background(), sourceNow)
		if err != nil || !bytes.Equal(again.Raw, raw) || again.ResourceVersion != rv {
			t.Fatalf("restart %+v %v", again, err)
		}
	})

	t.Run("refusals leave the original bytes", func(t *testing.T) {
		cases := []struct {
			name string
			body func(t *testing.T) []byte
			want error
			code error
		}{
			{name: "same retrieval different bytes", body: func(t *testing.T) []byte {
				return syntheticSourceRaw(t, "41", "59", sourceRetrieved, sourceResets, 1)
			}, want: ErrProviderSourceRetrieval},
			{name: "older retrieval", body: func(t *testing.T) []byte {
				return syntheticSourceRaw(t, "40", "60", sourceOlder, sourceResets, 1)
			}, want: ErrProviderSourceRetrieval},
			{name: "generation mismatch", body: func(t *testing.T) []byte {
				return syntheticSourceRaw(t, "42", "58", sourceNewer, sourceResets, 2)
			}, want: ErrProviderSourceRejected, code: providerallowance.ErrCurrentSourceSelection},
			{name: "generation reset", body: func(t *testing.T) []byte {
				return syntheticSourceRaw(t, "40", "60", sourceNewer, sourceResets, 0)
			}, want: ErrProviderSourceRejected, code: providerallowance.ErrCurrentSourceGeneration},
			{name: "reset is not after now", body: func(t *testing.T) []byte {
				return syntheticSourceRaw(t, "40", "60", sourceRetrieved, sourceEarlyReset, 1)
			}, want: ErrProviderSourceRejected, code: providerallowance.ErrCurrentSourceReset},
			{name: "digest mismatch", body: func(t *testing.T) []byte {
				raw := syntheticSourceRaw(t, "40", "60", sourceRetrieved, sourceResets, 1)
				return append([]byte{raw[0], ' '}, raw[1:]...)
			}, want: ErrProviderSourceDigest},
			{name: "body over 16KiB", body: func(t *testing.T) []byte {
				return bytes.Repeat([]byte("x"), providerSourceMaxBody+1)
			}, want: ErrProviderSourceRejected, code: providerallowance.ErrCurrentSourceSize},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				mem, store, original := seededStore(t)
				err := store.Put(context.Background(), tc.body(t), sourceNow)
				if !errors.Is(err, tc.want) || (tc.code != nil && !errors.Is(err, tc.code)) {
					t.Fatalf("got %v", err)
				}
				if strings.Contains(err.Error(), "usedPercent") || strings.Contains(err.Error(), sourceIncarnation) {
					t.Fatalf("error contains report: %v", err)
				}
				assertUntouched(t, mem, original)
			})
		}
	})

	t.Run("stored bytes that do not decode are not repaired", func(t *testing.T) {
		mem, store, _ := seededStore(t)
		mem.cm.Data[ProviderSourceDataKey] = "{"
		newer := syntheticSourceRaw(t, "42", "58", sourceNewer, sourceResets, 1)
		if err := store.Put(context.Background(), newer, sourceNow); !errors.Is(err, ErrProviderSourceRejected) {
			t.Fatal(err)
		}
		if mem.updates != 1 || mem.cm.Data[ProviderSourceDataKey] != "{" {
			t.Fatalf("repaired %#v", mem.cm.Data)
		}
		view, err := store.Read(context.Background(), sourceNow)
		if err != nil || !view.Suppressed || view.Fresh || view.Raw != nil || mem.cm.Data[ProviderSourceDataKey] != "{" {
			t.Fatalf("read %+v %v", view, err)
		}
	})

	t.Run("immutable and deleting objects are not replaced", func(t *testing.T) {
		newer := syntheticSourceRaw(t, "42", "58", sourceNewer, sourceResets, 1)
		mem, store, original := seededStore(t)
		immutable := true
		mem.cm.Immutable = &immutable
		if err := store.Put(context.Background(), newer, sourceNow); !errors.Is(err, ErrProviderSourceRejected) {
			t.Fatal(err)
		}
		assertUntouched(t, mem, original)
		mem, store, original = seededStore(t)
		ts := metav1.NewTime(sourceNow)
		mem.cm.DeletionTimestamp = &ts
		if err := store.Put(context.Background(), newer, sourceNow); !errors.Is(err, ErrProviderSourceRejected) {
			t.Fatal(err)
		}
		assertUntouched(t, mem, original)
	})

	t.Run("one stale conflict does not mix", func(t *testing.T) {
		mem, store, original := seededStore(t)
		winner := syntheticSourceRaw(t, "42", "58", sourceNewer, sourceResets, 1)
		attempted := syntheticSourceRaw(t, "41", "59", sourceAttempt, sourceResets, 1)
		store.Writer = &plantStub{mem: mem, winner: string(winner)}
		err := store.Put(context.Background(), attempted, sourceNow)
		if !errors.Is(err, ErrProviderSourceConflict) {
			t.Fatal(err)
		}
		got := mem.cm.Data[ProviderSourceDataKey]
		if got != string(winner) || strings.Contains(got, sourceAttempt) || strings.Contains(got, sourceRetrieved) || mem.cm.Data["keep"] != "yes" {
			t.Fatalf("mixed %q keep %q", got, mem.cm.Data["keep"])
		}
		if strings.Contains(got, sourceNow.Format(time.RFC3339)) || !strings.Contains(got, `"providerUpdatedAt":null`) {
			t.Fatalf("retrieval changed to %s", got)
		}
		assertSlotMeta(t, mem.cm)
		if mem.updates != 1 || strings.Contains(got, string(original)) && strings.Contains(got, string(attempted)) {
			t.Fatalf("updates %d", mem.updates)
		}
	})

	t.Run("lost response confirms the same bytes only", func(t *testing.T) {
		mem, store, _ := seededStore(t)
		newer := syntheticSourceRaw(t, "42", "58", sourceNewer, sourceResets, 1)
		store.Writer = loseResponse{next: mem}
		if err := store.Put(context.Background(), newer, sourceNow); err != nil {
			t.Fatal(err)
		}
		if mem.cm.Data[ProviderSourceDataKey] != string(newer) || mem.updates != 2 {
			t.Fatalf("updates %d data %s", mem.updates, mem.cm.Data[ProviderSourceDataKey])
		}
		mem, store, original := seededStore(t)
		store.Writer = failUpdate{}
		err := store.Put(context.Background(), newer, sourceNow)
		if !errors.Is(err, ErrProviderSourceUnavailable) {
			t.Fatal(err)
		}
		assertUntouched(t, mem, original)
	})

	t.Run("expiry suppresses and retains one report", func(t *testing.T) {
		mem := newSlotMem("")
		store := decisionStore(mem)
		raw := syntheticSourceRaw(t, "40", "60", sourceExpiredAt, sourceResets, 1)
		if err := store.Put(context.Background(), raw, sourceEarlyNow); err != nil {
			t.Fatal(err)
		}
		rv := mem.cm.ResourceVersion
		view, err := store.Read(context.Background(), sourceNow)
		if err != nil || !view.Found || !view.Suppressed || view.Fresh || view.Raw != nil || view.ResourceVersion != rv {
			t.Fatalf("view %+v %v", view, err)
		}
		if mem.updates != 1 || mem.cm.Data[ProviderSourceDataKey] != string(raw) || len(mem.cm.Data) != 2 {
			t.Fatalf("data %#v", mem.cm.Data)
		}
		if err := store.Put(context.Background(), raw, sourceNow); !errors.Is(err, ErrProviderSourceRejected) || mem.cm.ResourceVersion != rv {
			t.Fatalf("expired replay %v rv %s", err, mem.cm.ResourceVersion)
		}
	})

	t.Run("generation change replaces one slot", func(t *testing.T) {
		mem, store, original := seededStore(t)
		store.Selection = sourceSelection(2)
		view, err := store.Read(context.Background(), sourceNow)
		if err != nil || view.Fresh || view.Raw != nil || mem.cm.Data[ProviderSourceDataKey] != string(original) {
			t.Fatalf("old report still readable %+v %v", view, err)
		}
		next := syntheticSourceRaw(t, "55", "45", sourceNewer, sourceResets, 2)
		if err := store.Put(context.Background(), next, sourceNow); err != nil {
			t.Fatal(err)
		}
		if mem.cm.Data[ProviderSourceDataKey] != string(next) || strings.Contains(mem.cm.Data[ProviderSourceDataKey], `"usedPercent":"40"`) || mem.cm.Data["keep"] != "yes" {
			t.Fatalf("slot %#v", mem.cm.Data)
		}
		view, err = store.Read(context.Background(), sourceNow)
		if err != nil || !bytes.Equal(view.Raw, next) {
			t.Fatalf("read %+v %v", view, err)
		}
	})

	t.Run("absent object is not created", func(t *testing.T) {
		mem := &slotMem{}
		store := decisionStore(mem)
		raw := syntheticSourceRaw(t, "40", "60", sourceRetrieved, sourceResets, 1)
		err := store.Put(context.Background(), raw, sourceNow)
		if !errors.Is(err, ErrProviderSourceAbsent) || mem.cm != nil || mem.updates != 0 {
			t.Fatalf("err %v cm %v updates %d", err, mem.cm, mem.updates)
		}
		view, readErr := store.Read(context.Background(), sourceNow)
		if readErr != nil || view.Found || view.Fresh || view.Raw != nil {
			t.Fatalf("read %+v %v", view, readErr)
		}
	})

	t.Run("non-null providerUpdatedAt is refused before a write", func(t *testing.T) {
		mem, store, original := seededStore(t)
		body := syntheticSourceWithProvider(t, sourceRetrieved)
		report := mustReport(t, body)
		providerAt, providerErr := time.Parse(time.RFC3339Nano, *report.ProviderUpdatedAt)
		retrievedAt, retrievedErr := time.Parse(time.RFC3339Nano, report.RetrievedAt)
		if providerErr != nil || retrievedErr != nil || providerAt.After(retrievedAt) {
			t.Fatalf("fixture provider %q retrieved %q", *report.ProviderUpdatedAt, report.RetrievedAt)
		}
		if err := providerallowance.ValidateCurrentSource(report, store.Selection, sourceNow); err != nil {
			t.Fatal(err)
		}
		rv := mem.cm.ResourceVersion
		updates := mem.updates
		err := store.Put(context.Background(), body, sourceNow)
		if !errors.Is(err, ErrProviderSourceRejected) || errors.Is(err, ErrProviderSourceDigest) || errors.Is(err, ErrProviderSourceRetrieval) {
			t.Fatal(err)
		}
		if strings.Contains(err.Error(), "usedPercent") || strings.Contains(err.Error(), "providerUpdatedAt") || strings.Contains(err.Error(), sourceRetrieved) {
			t.Fatalf("error contains report: %v", err)
		}
		assertProviderSlotHeld(t, mem, original, rv, updates)
		live, caller := sourceBase()
		handler := sourceReports(store, live)
		status, resp, callErr := callProviderSource(t, handler, "PUT", ProviderSourceReportPath, "", body, caller)
		requireRefusal(t, status, resp, callErr, 422)
		assertProviderSlotHeld(t, mem, original, rv, updates)
	})

	t.Run("stored non-null providerUpdatedAt is suppressed", func(t *testing.T) {
		body := syntheticSourceWithProvider(t, sourceRetrieved)
		mem := newSlotMem(string(body))
		store := decisionStore(mem)
		rv := mem.cm.ResourceVersion
		updates := mem.updates
		view, err := store.Read(context.Background(), sourceNow)
		if err != nil || !view.Found || !view.Suppressed || view.Fresh || view.Raw != nil || view.ResourceVersion != rv {
			t.Fatalf("view %+v %v", view, err)
		}
		assertProviderSlotHeld(t, mem, body, rv, updates)
		if !strings.Contains(mem.cm.Data[ProviderSourceDataKey], `"retrievedAt":"`+sourceRetrieved+`"`) || !strings.Contains(mem.cm.Data[ProviderSourceDataKey], `"providerUpdatedAt":"`+sourceRetrieved+`"`) {
			t.Fatalf("retrieval rewritten %#v", mem.cm.Data)
		}
		live, caller := sourceBase()
		handler := sourceReports(store, live)
		status, resp, callErr := callProviderSource(t, handler, "GET", ProviderSourceReportPath, "", nil, caller)
		if callErr != nil || status != 200 || jsonString(t, resp) != `{"availability":"Unknown"}` {
			t.Fatalf("status %d body %#v err %v", status, resp, callErr)
		}
		encoded := jsonString(t, resp)
		if strings.Contains(encoded, "usedPercent") || strings.Contains(encoded, sourceRetrieved) || strings.Contains(encoded, "providerUpdatedAt") {
			t.Fatal(encoded)
		}
		assertProviderSlotHeld(t, mem, body, rv, updates)
	})
}

func TestProviderSourceEnvtest(t *testing.T) {
	k8s, ns := startSourceEnv(t)
	ctx := context.Background()

	t.Run("ssa leaves the report and metadata", func(t *testing.T) {
		const name = "synthetic-slot-ssa"
		applySourceSlot(t, ctx, k8s, ns, name)
		store := sourceKubeStore(k8s, k8s, ns, name, 1)
		raw := syntheticSourceRaw(t, "40", "60", sourceRetrieved, sourceResets, 1)
		if err := store.Put(ctx, raw, sourceNow); err != nil {
			t.Fatal(err)
		}
		got := loadSlot(t, ctx, k8s, ns, name)
		assertEnvMeta(t, got)
		if got.Data[ProviderSourceDataKey] != string(raw) || len(got.OwnerReferences) != 0 {
			t.Fatalf("after put %#v", got.Data)
		}
		if !managerPresent(got, ProviderSourceFieldManager) {
			t.Fatalf("writer managedFields %#v", got.ManagedFields)
		}
		rv := got.ResourceVersion
		if err := store.Put(ctx, raw, sourceNow); err != nil {
			t.Fatal(err)
		}
		replayed := loadSlot(t, ctx, k8s, ns, name)
		if replayed.ResourceVersion != rv || replayed.Data[ProviderSourceDataKey] != string(raw) {
			t.Fatal("replay wrote")
		}
		other := *store
		view, err := other.Read(ctx, sourceNow)
		if err != nil || !bytes.Equal(view.Raw, raw) || view.ResourceVersion != rv {
			t.Fatalf("second replica %+v %v", view, err)
		}
		applySourceSlot(t, ctx, k8s, ns, name)
		after := loadSlot(t, ctx, k8s, ns, name)
		assertEnvMeta(t, after)
		if after.Data[ProviderSourceDataKey] != string(raw) || !strings.Contains(after.Data[ProviderSourceDataKey], `"sourceGeneration":1`) || !strings.Contains(after.Data[ProviderSourceDataKey], `"providerUpdatedAt":null`) {
			t.Fatalf("ssa reset the report %#v", after.Data)
		}
		if !managerPresent(after, sourceGitOps) {
			t.Fatalf("gitops managedFields %#v", after.ManagedFields)
		}
	})

	t.Run("replicas cannot mix one resourceVersion", func(t *testing.T) {
		const name = "synthetic-slot-conflict"
		applySourceSlot(t, ctx, k8s, ns, name)
		store := sourceKubeStore(k8s, k8s, ns, name, 1)
		original := syntheticSourceRaw(t, "40", "60", sourceRetrieved, sourceResets, 1)
		if err := store.Put(ctx, original, sourceNow); err != nil {
			t.Fatal(err)
		}
		winner := syntheticSourceRaw(t, "42", "58", sourceNewer, sourceResets, 1)
		attempted := syntheticSourceRaw(t, "41", "59", sourceAttempt, sourceResets, 1)
		key := client.ObjectKey{Namespace: ns, Name: name}
		racer := sourceKubeStore(k8s, &plantConflict{live: k8s, next: k8s, key: key, dataKey: ProviderSourceDataKey, winner: string(winner)}, ns, name, 1)
		err := racer.Put(ctx, attempted, sourceNow)
		if !errors.Is(err, ErrProviderSourceConflict) {
			t.Fatal(err)
		}
		got := loadSlot(t, ctx, k8s, ns, name)
		assertEnvMeta(t, got)
		if got.Data[ProviderSourceDataKey] != string(winner) || strings.Contains(got.Data[ProviderSourceDataKey], sourceAttempt) || strings.Contains(got.Data[ProviderSourceDataKey], sourceRetrieved) {
			t.Fatalf("mixed %s", got.Data[ProviderSourceDataKey])
		}
	})

	t.Run("absent and deleted slots are not created", func(t *testing.T) {
		raw := syntheticSourceRaw(t, "40", "60", sourceRetrieved, sourceResets, 1)
		missing := sourceKubeStore(k8s, k8s, ns, "synthetic-slot-missing", 1)
		if err := missing.Put(ctx, raw, sourceNow); !errors.Is(err, ErrProviderSourceAbsent) {
			t.Fatal(err)
		}
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: "synthetic-slot-missing"}, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
		const name = "synthetic-slot-deleted"
		applySourceSlot(t, ctx, k8s, ns, name)
		if err := k8s.Delete(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}); err != nil {
			t.Fatal(err)
		}
		deleted := sourceKubeStore(k8s, k8s, ns, name, 1)
		if err := deleted.Put(ctx, raw, sourceNow); !errors.Is(err, ErrProviderSourceAbsent) {
			t.Fatal(err)
		}
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
	})

	t.Run("expiry and generation keep one object", func(t *testing.T) {
		const expiredName = "synthetic-slot-expiry"
		applySourceSlot(t, ctx, k8s, ns, expiredName)
		expiredStore := sourceKubeStore(k8s, k8s, ns, expiredName, 1)
		expired := syntheticSourceRaw(t, "40", "60", sourceExpiredAt, sourceResets, 1)
		if err := expiredStore.Put(ctx, expired, sourceEarlyNow); err != nil {
			t.Fatal(err)
		}
		before := loadSlot(t, ctx, k8s, ns, expiredName)
		view, err := expiredStore.Read(ctx, sourceNow)
		if err != nil || view.Fresh || view.Raw != nil || !view.Suppressed {
			t.Fatalf("view %+v %v", view, err)
		}
		after := loadSlot(t, ctx, k8s, ns, expiredName)
		if after.ResourceVersion != before.ResourceVersion || after.Data[ProviderSourceDataKey] != string(expired) || len(after.Data) != 1 {
			t.Fatalf("expiry wrote %#v", after.Data)
		}
		live, caller := sourceBase()
		handler := sourceReports(expiredStore, live)
		handler.Now = func() time.Time { return sourceNow }
		status, body, callErr := callProviderSource(t, handler, "GET", ProviderSourceReportPath, "", nil, caller)
		if callErr != nil || status != 200 || jsonString(t, body) != `{"availability":"Unknown"}` {
			t.Fatalf("status %d body %#v err %v", status, body, callErr)
		}
		if strings.Contains(jsonString(t, body), "usedPercent") || strings.Contains(jsonString(t, body), "Measured") {
			t.Fatal(jsonString(t, body))
		}

		const genName = "synthetic-slot-generation"
		applySourceSlot(t, ctx, k8s, ns, genName)
		genStore := sourceKubeStore(k8s, k8s, ns, genName, 1)
		first := syntheticSourceRaw(t, "40", "60", sourceRetrieved, sourceResets, 1)
		if err := genStore.Put(ctx, first, sourceNow); err != nil {
			t.Fatal(err)
		}
		genStore.Selection = sourceSelection(2)
		hidden, err := genStore.Read(ctx, sourceNow)
		if err != nil || hidden.Fresh || hidden.Raw != nil {
			t.Fatal(err)
		}
		second := syntheticSourceRaw(t, "55", "45", sourceNewer, sourceResets, 2)
		if err := genStore.Put(ctx, second, sourceNow); err != nil {
			t.Fatal(err)
		}
		got := loadSlot(t, ctx, k8s, ns, genName)
		if got.Data[ProviderSourceDataKey] != string(second) || strings.Contains(got.Data[ProviderSourceDataKey], sourceRetrieved) || len(got.Data) != 1 {
			t.Fatalf("generation %#v", got.Data)
		}
	})

	t.Run("handler get and disable do not change retrieval", func(t *testing.T) {
		const name = "synthetic-slot-handler"
		applySourceSlot(t, ctx, k8s, ns, name)
		store := sourceKubeStore(k8s, k8s, ns, name, 1)
		raw := syntheticSourceRaw(t, "40", "60", sourceRetrieved, sourceResets, 1)
		if err := store.Put(ctx, raw, sourceNow); err != nil {
			t.Fatal(err)
		}
		before := loadSlot(t, ctx, k8s, ns, name)
		live, caller := sourceBase()
		handler := sourceReports(store, live)
		status, body, err := callProviderSource(t, handler, "GET", ProviderSourceReportPath, "", nil, caller)
		if err != nil || status != 200 || !bytes.Equal(body.(json.RawMessage), raw) {
			t.Fatalf("get status %d body %#v err %v", status, body, err)
		}
		status, body, err = callProviderSource(t, handler, "PUT", ProviderSourceReportPath, "", raw, caller)
		if err != nil || status != 204 || body != nil {
			t.Fatalf("replay status %d err %v", status, err)
		}
		after := loadSlot(t, ctx, k8s, ns, name)
		if after.ResourceVersion != before.ResourceVersion || after.Data[ProviderSourceDataKey] != before.Data[ProviderSourceDataKey] {
			t.Fatal("get or replay changed the slot")
		}
		handler.Enabled = false
		status, body, err = callProviderSource(t, handler, "GET", ProviderSourceReportPath, "", nil, caller)
		requireRefusal(t, status, body, err, 404)
		status, body, err = callProviderSource(t, handler, "PUT", ProviderSourceReportPath, "", raw, caller)
		requireRefusal(t, status, body, err, 404)
		disabled := loadSlot(t, ctx, k8s, ns, name)
		if disabled.ResourceVersion != before.ResourceVersion || disabled.Data[ProviderSourceDataKey] != string(raw) {
			t.Fatal("disabled route wrote")
		}
	})

	t.Run("prune of the three names reports a failed delete", func(t *testing.T) {
		const name = "synthetic-slot-prune"
		const keep = "synthetic-slot-keep"
		if err := k8s.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Data: map[string]string{"keep": "report"}}); err != nil {
			t.Fatal(err)
		}
		if err := k8s.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: keep}, Data: map[string]string{"keep": "yes"}}); err != nil {
			t.Fatal(err)
		}
		if err := k8s.Create(ctx, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{name}, Verbs: []string{"get", "update"},
		}}}); err != nil {
			t.Fatal(err)
		}
		if err := k8s.Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: name},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: "synthetic-system", Name: "synthetic-operator"}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := pruneSourceNames(ctx, refuseDelete{err: errors.New("synthetic delete refused")}, ns, name); err == nil {
			t.Fatal("failed delete reported as erased")
		}
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &corev1.ConfigMap{}); err != nil {
			t.Fatal(err)
		}
		if err := pruneSourceNames(ctx, k8s, ns, name); err != nil {
			t.Fatal(err)
		}
		for _, obj := range []client.Object{
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}},
			&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}},
			&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}},
		} {
			if err := k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, obj); !apierrors.IsNotFound(err) {
				t.Fatalf("%T: %v", obj, err)
			}
		}
		var kept corev1.ConfigMap
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: keep}, &kept); err != nil || kept.Data["keep"] != "yes" {
			t.Fatalf("keep %v %#v", err, kept.Data)
		}
	})
}

func syntheticSourceRaw(t *testing.T, used, remaining, retrieved, resets string, generation int64) []byte {
	t.Helper()
	raw, err := json.Marshal(providerallowance.CurrentSourceReport{
		Schema:                providerallowance.CurrentSourceSchema,
		PublisherRef:          providerallowance.CurrentSourcePublisher,
		ProducerIncarnation:   sourceIncarnation,
		SourceGeneration:      generation,
		RetrievedAt:           retrieved,
		UsedPercent:           used,
		RemainingPercent:      remaining,
		WindowDurationMinutes: 10080,
		ResetsAt:              resets,
		RemainingPercentBasis: providerallowance.CurrentSourceRemainingBasis,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := providerallowance.DecodeCurrentSource(raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func syntheticSourceWithProvider(t *testing.T, providerAt string) []byte {
	t.Helper()
	updated := providerAt
	raw, err := json.Marshal(providerallowance.CurrentSourceReport{
		Schema:                providerallowance.CurrentSourceSchema,
		PublisherRef:          providerallowance.CurrentSourcePublisher,
		ProducerIncarnation:   sourceIncarnation,
		SourceGeneration:      1,
		RetrievedAt:           sourceRetrieved,
		ProviderUpdatedAt:     &updated,
		UsedPercent:           "40",
		RemainingPercent:      "60",
		WindowDurationMinutes: 10080,
		ResetsAt:              sourceResets,
		RemainingPercentBasis: providerallowance.CurrentSourceRemainingBasis,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := providerallowance.DecodeCurrentSource(raw)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(report)
	if err != nil || !bytes.Equal(raw, canonical) || report.ProviderUpdatedAt == nil || *report.ProviderUpdatedAt != providerAt {
		t.Fatalf("not canonical: %s", raw)
	}
	return raw
}

func assertProviderSlotHeld(t *testing.T, mem *slotMem, original []byte, rv string, updates int) {
	t.Helper()
	if mem.updates != updates || mem.cm.ResourceVersion != rv || mem.cm.Data[ProviderSourceDataKey] != string(original) || mem.cm.Data["keep"] != "yes" {
		t.Fatalf("changed updates %d rv %s data %#v", mem.updates, mem.cm.ResourceVersion, mem.cm.Data)
	}
	assertSlotMeta(t, mem.cm)
}

func sourceSelection(generation int64) providerallowance.CurrentSourceSelection {
	return providerallowance.CurrentSourceSelection{
		PublisherRef:        providerallowance.CurrentSourcePublisher,
		ProducerIncarnation: sourceIncarnation,
		SourceGeneration:    generation,
		MaxAge:              1800 * time.Second,
	}
}

func seededStore(t *testing.T) (*slotMem, *ProviderSourceReportStore, []byte) {
	t.Helper()
	raw := syntheticSourceRaw(t, "40", "60", sourceRetrieved, sourceResets, 1)
	mem := newSlotMem("")
	store := decisionStore(mem)
	if err := store.Put(context.Background(), raw, sourceNow); err != nil {
		t.Fatal(err)
	}
	if mem.updates != 1 || mem.cm.Data[ProviderSourceDataKey] != string(raw) || mem.cm.Data["keep"] != "yes" {
		t.Fatalf("seed %#v updates %d", mem.cm.Data, mem.updates)
	}
	assertSlotMeta(t, mem.cm)
	if err := providerallowance.ValidateCurrentSource(mustReport(t, raw), store.Selection, sourceNow); err != nil {
		t.Fatal(err)
	}
	return mem, store, raw
}

func mustReport(t *testing.T, raw []byte) providerallowance.CurrentSourceReport {
	t.Helper()
	report, err := providerallowance.DecodeCurrentSource(raw)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func decisionStore(mem *slotMem) *ProviderSourceReportStore {
	return &ProviderSourceReportStore{
		Live: mem, Writer: mem, Namespace: sourceSlotNS, Name: sourceSlotName,
		DataKey: ProviderSourceDataKey, Selection: sourceSelection(1),
	}
}

func newSlotMem(report string) *slotMem {
	data := map[string]string{"keep": "yes"}
	if report != "" {
		data[ProviderSourceDataKey] = report
	}
	return &slotMem{cm: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace: sourceSlotNS, Name: sourceSlotName, ResourceVersion: "1",
		Labels:        map[string]string{"synthetic.example/slot": "fixed"},
		Annotations:   map[string]string{"synthetic.example/manager": "gitops"},
		Finalizers:    []string{"synthetic.example/keep"},
		ManagedFields: []metav1.ManagedFieldsEntry{{Manager: sourceGitOps, Operation: metav1.ManagedFieldsOperationApply, APIVersion: "v1", FieldsType: "FieldsV1"}},
	}, Data: data}}
}

func assertSlotMeta(t *testing.T, cm *corev1.ConfigMap) {
	t.Helper()
	if cm.Labels["synthetic.example/slot"] != "fixed" || cm.Annotations["synthetic.example/manager"] != "gitops" {
		t.Fatalf("metadata %#v", cm.ObjectMeta)
	}
	if len(cm.Finalizers) != 1 || cm.Finalizers[0] != "synthetic.example/keep" || !managerPresent(cm, sourceGitOps) {
		t.Fatalf("ownership finalizers %#v fields %#v", cm.Finalizers, cm.ManagedFields)
	}
}

func assertUntouched(t *testing.T, mem *slotMem, original []byte) {
	t.Helper()
	if mem.updates != 1 || mem.cm.Data[ProviderSourceDataKey] != string(original) || mem.cm.Data["keep"] != "yes" {
		t.Fatalf("changed updates %d %#v", mem.updates, mem.cm.Data)
	}
	assertSlotMeta(t, mem.cm)
}

func assertEnvMeta(t *testing.T, cm *corev1.ConfigMap) {
	t.Helper()
	if cm.Labels["synthetic.example/slot"] != "fixed" || cm.Annotations["synthetic.example/manager"] != "gitops" || len(cm.ManagedFields) == 0 {
		t.Fatalf("labels %#v annotations %#v fields %#v", cm.Labels, cm.Annotations, cm.ManagedFields)
	}
}

func managerPresent(cm *corev1.ConfigMap, name string) bool {
	for _, entry := range cm.ManagedFields {
		if entry.Manager == name {
			return true
		}
	}
	return false
}

type slotMem struct {
	cm      *corev1.ConfigMap
	updates int
}

func (m *slotMem) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	dest, ok := obj.(*corev1.ConfigMap)
	if !ok || m.cm == nil || key.Namespace != m.cm.Namespace || key.Name != m.cm.Name {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, key.Name)
	}
	*dest = *m.cm.DeepCopy()
	return nil
}

func (m *slotMem) Update(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
	next, ok := obj.(*corev1.ConfigMap)
	if !ok || m.cm == nil || next.ResourceVersion != m.cm.ResourceVersion {
		name := ""
		if next != nil {
			name = next.Name
		}
		return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, name, errors.New("conflict"))
	}
	copied := next.DeepCopy()
	m.updates++
	copied.ResourceVersion = strconv.Itoa(m.updates + 1)
	m.cm = copied
	return nil
}

type plantStub struct {
	mem    *slotMem
	winner string
}

func (p *plantStub) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if p.mem.cm.Data == nil {
		p.mem.cm.Data = map[string]string{}
	}
	p.mem.cm.Data[ProviderSourceDataKey] = p.winner
	p.mem.cm.ResourceVersion = "50"
	return p.mem.Update(ctx, obj, opts...)
}

type loseResponse struct{ next objectUpdater }

func (l loseResponse) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if err := l.next.Update(ctx, obj, opts...); err != nil {
		return err
	}
	return errors.New("lost response")
}

type failUpdate struct{}

func (failUpdate) Update(context.Context, client.Object, ...client.UpdateOption) error {
	return errors.New("lost before write")
}

type plantConflict struct {
	live    objectGetter
	next    objectUpdater
	key     client.ObjectKey
	dataKey string
	winner  string
	planted bool
}

func (p *plantConflict) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if !p.planted {
		p.planted = true
		var current corev1.ConfigMap
		if err := p.live.Get(ctx, p.key, &current); err != nil {
			return err
		}
		if current.Data == nil {
			current.Data = map[string]string{}
		}
		current.Data[p.dataKey] = p.winner
		if err := p.next.Update(ctx, &current, opts...); err != nil {
			return err
		}
	}
	return p.next.Update(ctx, obj, opts...)
}

func startSourceEnv(t *testing.T) (client.Client, string) {
	t.Helper()
	k8s, err := client.New(envConfig(t), client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: sourceEnvNS}}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	return k8s, sourceEnvNS
}

func sourceKubeStore(live objectGetter, writer objectUpdater, namespace, name string, generation int64) *ProviderSourceReportStore {
	return &ProviderSourceReportStore{
		Live: live, Writer: writer, Namespace: namespace, Name: name,
		DataKey: ProviderSourceDataKey, Selection: sourceSelection(generation),
	}
}

func applySourceSlot(t *testing.T, ctx context.Context, c client.Client, namespace, name string) {
	t.Helper()
	obj := corev1ac.ConfigMap(name, namespace).
		WithLabels(map[string]string{"synthetic.example/slot": "fixed"}).
		WithAnnotations(map[string]string{"synthetic.example/manager": "gitops"})
	if err := c.Apply(ctx, obj, client.FieldOwner(sourceGitOps)); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

func loadSlot(t *testing.T, ctx context.Context, c client.Client, namespace, name string) *corev1.ConfigMap {
	t.Helper()
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &cm); err != nil {
		t.Fatal(err)
	}
	return &cm
}

type nameDeleter interface {
	Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error
}

func pruneSourceNames(ctx context.Context, c nameDeleter, namespace, name string) error {
	objs := []client.Object{
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}},
	}
	var errs []error
	for _, obj := range objs {
		if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type refuseDelete struct{ err error }

func (r refuseDelete) Delete(context.Context, client.Object, ...client.DeleteOption) error {
	return r.err
}
