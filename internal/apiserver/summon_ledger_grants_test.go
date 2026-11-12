package apiserver

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// The API treats this key as opaque. It is deliberately not a caller-entry
// document, and includes an unknown member and whitespace to detect rewrites.
func TestSummonLedgerAPIKeepsAdmissionKey(t *testing.T) {
	const key = "_admission"
	const grants = `{ "schema": "dev-env.summon-grants.v1", "grants": [], "future": 7 }`
	cm := summonLedgerObject()
	cm.Data = map[string]string{key: grants}
	f := summonFixture(t, cm)
	c := &caller{policy: summonTestPolicy()}
	hash := strings.Repeat("a", 64)
	ctx := context.Background()
	check := func(step string, err error, got, want summonChargeResult) {
		t.Helper()
		if err != nil || got != want {
			t.Fatalf("%s: result=%v want=%v err=%v", step, got, want, err)
		}
		_, stored, err := readLedgerConfigMap(ctx, f.c, ledgerTestKey)
		if err != nil || stored.Data[key] != grants {
			t.Fatalf("%s changed or decoded the admission key: %v", step, err)
		}
	}
	b, err := f.srv.bindSummonCharge(ctx, c, "d-one", hash, "rem-responder-1", nil, false, 1)
	check("charge", err, b.result, summonChargedNow)
	b, err = f.srv.markSummonAttempt(ctx, c, "d-one", hash, "rem-responder-1", false, 1)
	check("mark", err, b.result, summonMarked)
	b, err = f.srv.rebindSummonCharge(ctx, c, "d-one", hash, "rem-responder-1", []string{"rem-responder-1", "rem-responder-2"}, false, 1)
	check("rebind", err, b.result, summonRebound)
	b, err = f.srv.bindSummonCharge(ctx, c, "d-refused", hash, "rem-responder-3", nil, true, 1)
	check("urgent refusal", err, b.result, summonHourFull)
	if f.ledger().UrgentRefusals != 1 || len(f.ledger().Charges) != 1 {
		t.Fatal("reserved key affected caller counts")
	}
	f.now = f.now.Add(81 * time.Minute)
	b, err = f.srv.bindSummonCharge(ctx, c, "d-two", hash, "rem-responder-4", nil, false, 1)
	check("prune", err, b.result, summonChargedNow)
	if e := f.ledger(); len(e.Charges) != 1 || e.Charges[0].ID != "d-two" || e.UrgentRefusals != 1 {
		t.Fatal("prune/count changed caller semantics")
	}
}

// The leader can add a grant after the API read and before its Update.
// The API must conflict, reread, and carry the new key through unchanged.
func TestSummonLedgerAPIRetryKeepsConcurrentGrant(t *testing.T) {
	f := summonFixture(t)
	const grants = `{"schema":"dev-env.summon-grants.v1","grants":[],"future":9}`
	fired := false
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == ledgerTestName && !fired {
			fired = true
			var live corev1.ConfigMap
			if err := c.Get(ctx, client.ObjectKeyFromObject(cm), &live); err != nil {
				return err
			}
			if live.Data == nil {
				live.Data = map[string]string{}
			}
			live.Data["_admission"] = grants
			if err := c.Update(ctx, &live); err != nil {
				return err
			}
		}
		return c.Update(ctx, obj, opts...)
	}})
	b, err := f.srv.bindSummonCharge(context.Background(), &caller{policy: summonTestPolicy()}, "d-one", strings.Repeat("a", 64), "rem-responder-1", nil, false, 1)
	if err != nil || b.result != summonChargedNow || !fired {
		t.Fatalf("CAS schedule: fired=%v result=%v err=%v", fired, b.result, err)
	}
	_, cm, err := readLedgerConfigMap(context.Background(), f.c, ledgerTestKey)
	if err != nil || cm.Data["_admission"] != grants || len(f.ledger().Charges) != 1 {
		t.Fatalf("API lost the leader's concurrent key: %v", err)
	}
}
