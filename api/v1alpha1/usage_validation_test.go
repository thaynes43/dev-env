package v1alpha1_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
)

func schemaUsage() *v1alpha1.UsageObservationStatus {
	zero := int64(0)
	unknown := v1alpha1.TokenObservationStatus{State: "Unknown"}
	return &v1alpha1.UsageObservationStatus{Version: 1, ID: strings.Repeat("a", 64), Source: v1alpha1.UsageSourceStatus{SessionUID: "original-session", PodUID: "original-pod", Provider: "claude", RequestedModel: "claude-opus-5-5", AttemptID: strings.Repeat("b", 64)}, ObservedAt: metav1.NewTime(time.Now().UTC().Truncate(time.Second)), Coverage: "InitialManagedInvocation", CounterSemantics: "InvocationFinalTotal", ResultState: "Final", InputTokens: v1alpha1.TokenObservationStatus{State: "Reported", Amount: &zero}, OutputTokens: unknown, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown, Cost: v1alpha1.CostObservationStatus{State: "Reported", Amount: "0", Unit: "USD", Kind: "ProviderCLIEquivalentEstimate"}, AggregationScope: "Unknown", ActualModelState: "Unknown", SubscriptionQuotaState: "Unknown", APIBillingState: "Unknown", LocalResourceState: "Unknown"}
}

func TestUsageSchemaWithDirectoryRetainsBothOriginals(t *testing.T) {
	c := ctx(t)
	s := testutil.New(t, "claude", "dev/client").Session
	s.Name, s.Namespace, s.UID, s.ResourceVersion = name(), ns, "", ""
	s.Spec.SessionDirectory = &v1alpha1.SessionDirectoryEnrollment{Version: 1, Project: "sample"}
	if err := k8s.Create(c, s); err != nil {
		t.Fatal("enrolled bounded managed task rejected", err)
	}
	s.Status.SessionDirectory = &v1alpha1.SessionDirectoryReceipt{State: "Preparing", Name: "session-directory." + strings.Repeat("a", 64), SpecDigest: strings.Repeat("b", 64)}
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("initial Directory receipt rejected", err)
	}
	s.Status.UsageObservation = schemaUsage()
	s.Status.UsageObservation.Source.SessionUID = string(s.UID)
	s.Status.SessionDirectory.State, s.Status.SessionDirectory.UID = "Confirmed", "original-directory"
	s.Status.Agent = &v1alpha1.AgentStatus{Status: "busy", LastHeartbeat: &metav1.Time{Time: time.Now().UTC().Truncate(time.Second)}}
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("typed explicit zero and Directory confirmation cannot coexist", err)
	}
	before, err := json.Marshal(s.Status)
	if err != nil {
		t.Fatal(err)
	}
	for _, boundary := range []string{"receipt-remove", "receipt-name", "receipt-digest", "receipt-uid", "receipt-regress", "usage-remove", "usage-change", "status-null", "status-remove", "enrollment-remove"} {
		t.Run(boundary, func(t *testing.T) {
			bad := s.DeepCopy()
			var err error
			switch boundary {
			case "receipt-remove":
				bad.Status.SessionDirectory = nil
			case "receipt-name":
				bad.Status.SessionDirectory.Name = "replacement"
			case "receipt-digest":
				bad.Status.SessionDirectory.SpecDigest = strings.Repeat("c", 64)
			case "receipt-uid":
				bad.Status.SessionDirectory.UID = "replacement"
			case "receipt-regress":
				bad.Status.SessionDirectory.State, bad.Status.SessionDirectory.UID = "Preparing", ""
			case "usage-remove":
				bad.Status.UsageObservation = nil
			case "usage-change":
				bad.Status.UsageObservation.Cost.Amount = "7"
			case "status-null":
				err = k8s.Status().Patch(c, bad, client.RawPatch(types.MergePatchType, []byte(`{"status":null}`)))
			case "status-remove":
				err = k8s.Status().Patch(c, bad, client.RawPatch(types.JSONPatchType, []byte(`[{"op":"remove","path":"/status"}]`)))
			case "enrollment-remove":
				bad.Spec.SessionDirectory = nil
				err = k8s.Update(c, bad)
			}
			if boundary != "status-null" && boundary != "status-remove" && boundary != "enrollment-remove" {
				err = k8s.Status().Update(c, bad)
			}
			if !apierrors.IsInvalid(err) {
				t.Fatal("combined schema accepted loss/replacement of original evidence", boundary, err)
			}
			if (boundary == "status-null" || boundary == "status-remove") && (!strings.Contains(err.Error(), "directory evidence cannot be erased with status") || !strings.Contains(err.Error(), "original usage observation cannot be erased")) {
				t.Fatal("combined root retention guards did not both reject whole-status removal", err)
			}
			var retained v1alpha1.AgentSession
			if err := k8s.Get(c, client.ObjectKeyFromObject(s), &retained); err != nil {
				t.Fatal(err)
			}
			after, err := json.Marshal(retained.Status)
			if err != nil || string(before) != string(after) || retained.UID != s.UID || retained.ResourceVersion != s.ResourceVersion || retained.Spec.SessionDirectory == nil {
				t.Fatal("refused update changed original evidence, identity or enrollment", err, retained)
			}
		})
	}
	s.Status.Agent.Status = "idle"
	s.Status.UsageConflict = &v1alpha1.UsageConflictStatus{State: "Unknown", Reason: "ConflictingSnapshot", IncomingSHA256: strings.Repeat("d", 64), ObservedAt: metav1.Now()}
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("combined retention guards blocked liveness/conflict update", err)
	}
	var retained v1alpha1.AgentSession
	if err := k8s.Get(c, client.ObjectKeyFromObject(s), &retained); err != nil {
		t.Fatal(err)
	}
	if retained.Status.SessionDirectory.UID != "original-directory" || retained.Status.UsageObservation.Cost.Amount != "0" || retained.Status.UsageObservation.InputTokens.Amount == nil || *retained.Status.UsageObservation.InputTokens.Amount != 0 || retained.Status.Agent.Status != "idle" || retained.Status.UsageConflict == nil {
		t.Fatal("combined schema pruned evidence or liveness", retained.Status)
	}
}

func TestUsageSchemaPresenceLegacyAndImmutableOriginal(t *testing.T) {
	c := ctx(t)
	s := taskSession()
	if err := k8s.Create(c, s); err != nil {
		t.Fatal(err)
	}
	s.Status.Usage = &v1alpha1.UsageStatus{CostUSD: "0", InputTokens: 9}
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("legacy zero rejected", err)
	}
	if s.Status.UsageObservation != nil {
		t.Fatal("legacy zero promoted")
	}
	s.Status.UsageObservation = schemaUsage()
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("explicit zero rejected", err)
	}
	var got v1alpha1.AgentSession
	if err := k8s.Get(c, client.ObjectKeyFromObject(s), &got); err != nil {
		t.Fatal(err)
	}
	u := got.Status.UsageObservation
	if u == nil || u.Cost.Amount != "0" || u.InputTokens.Amount == nil || *u.InputTokens.Amount != 0 || u.OutputTokens.Amount != nil || got.Status.Usage.CostUSD != "0" {
		t.Fatal("presence or legacy evidence pruned", got.Status)
	}
	before, _ := json.Marshal(u)
	for _, mutate := range []func(*v1alpha1.AgentSession){func(x *v1alpha1.AgentSession) { x.Status.UsageObservation.Cost.Amount = "7" }, func(x *v1alpha1.AgentSession) { x.Status.UsageObservation.Source.AttemptID = strings.Repeat("c", 64) }, func(x *v1alpha1.AgentSession) { x.Status.UsageObservation = nil }} {
		bad := got.DeepCopy()
		mutate(bad)
		if err := k8s.Status().Update(c, bad); !apierrors.IsInvalid(err) {
			t.Fatal("original snapshot changed/erased", err)
		}
	}
	if err := k8s.Get(c, client.ObjectKeyFromObject(s), &got); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(got.Status.UsageObservation)
	if string(before) != string(after) {
		t.Fatal("invalid update lost original")
	}
	got.Status.Agent = &v1alpha1.AgentStatus{Status: "busy"}
	got.Status.UsageConflict = &v1alpha1.UsageConflictStatus{State: "Unknown", Reason: "ConflictingSnapshot", IncomingSHA256: strings.Repeat("d", 64), ObservedAt: metav1.Now()}
	if err := k8s.Status().Update(c, &got); err != nil {
		t.Fatal("liveness/conflict update blocked by original latch", err)
	}
}

func TestUsageSchemaRejectsUnqualifiedOrInventedMetrics(t *testing.T) {
	for _, boundary := range []string{"unknown-amount", "reported-missing", "unit", "billing", "actual-model", "tokens-missing", "tokens-negative", "tokens-bound", "cost-bound", "quota"} {
		t.Run(boundary, func(t *testing.T) {
			c := ctx(t)
			s := taskSession()
			if err := k8s.Create(c, s); err != nil {
				t.Fatal(err)
			}
			u := schemaUsage()
			switch boundary {
			case "unknown-amount":
				u.Cost.State = "Unknown"
			case "reported-missing":
				u.Cost.Amount = ""
			case "unit":
				u.Cost.Unit = ""
			case "billing":
				u.Cost.Kind = "ProviderAPIBilling"
			case "actual-model":
				u.ActualModelState = "Reported"
			case "tokens-missing":
				u.InputTokens.Amount = nil
			case "tokens-negative":
				n := int64(-1)
				u.InputTokens.Amount = &n
			case "tokens-bound":
				n := int64(1000000000000001)
				u.InputTokens.Amount = &n
			case "cost-bound":
				u.Cost.Amount = strings.Repeat("9", 65)
			case "quota":
				u.SubscriptionQuotaState = "Reported"
			}
			s.Status.UsageObservation = u
			if err := k8s.Status().Update(c, s); !apierrors.IsInvalid(err) {
				t.Fatal("invalid provenance accepted", boundary, err)
			}
		})
	}
}

func TestCapturedUsageSchemaAcceptsAndRefuses(t *testing.T) {
	c := ctx(t)
	s := testutil.New(t, "claude", "dev/client").Session
	s.Name, s.Namespace = name(), ns
	s.Spec.UsageCaptureParentUID = "parent-uid"
	if err := k8s.Create(c, s); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Get(c, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	obs := schemaUsage()
	obs.Source.SessionUID = string(s.UID)
	row := func(key, child string) v1alpha1.CapturedUsageReceipt {
		return v1alpha1.CapturedUsageReceipt{Key: key, ParentUID: string(s.UID), ChildName: child, CapturedAt: metav1.NewTime(time.Now().UTC().Truncate(time.Second)), Observation: *obs}
	}
	s.Status.CapturedUsage = &v1alpha1.CapturedUsageStatus{Version: 1, Scope: v1alpha1.CapturedUsageScope, Coverage: v1alpha1.CapturedUsageCoverage, Retention: v1alpha1.CapturedUsageRetention, Receipts: []v1alpha1.CapturedUsageReceipt{row(strings.Repeat("a", 64), "child")}}
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("valid ledger rejected", err)
	}
	if err := k8s.Get(c, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	s.Status.CapturedUsage.Receipts = append(s.Status.CapturedUsage.Receipts, row(strings.Repeat("b", 64), "child-2"))
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("second receipt rejected", err)
	}
	if err := k8s.Get(c, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	if len(s.Status.CapturedUsage.Receipts) != 2 || s.Status.CapturedUsage.Overflow {
		t.Fatalf("second row %+v", s.Status.CapturedUsage)
	}
	hb := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	s.Status.Agent = &v1alpha1.AgentStatus{Status: "busy", LastHeartbeat: &hb}
	s.Status.NodeName = "worker-a"
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("ledger-bearing parent rejected a liveness update", err)
	}
	if err := k8s.Get(c, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	if s.Status.NodeName != "worker-a" || len(s.Status.CapturedUsage.Receipts) != 2 {
		t.Fatalf("liveness disturbed the ledger %+v", s.Status.CapturedUsage)
	}
	s.Status.CapturedUsage.Overflow = true
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("absent overflow to true rejected", err)
	}
	if err := k8s.Get(c, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	cleared := s.DeepCopy()
	cleared.Status.CapturedUsage.Overflow = false
	wantInvalid(t, k8s.Status().Update(c, cleared), "captured usage overflow is sticky")
	if err := k8s.Get(c, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	mutated := s.DeepCopy()
	mutated.Status.CapturedUsage.Receipts[0].ChildName = "renamed"
	wantInvalid(t, k8s.Status().Update(c, mutated), "captured usage receipts do not change")
	if err := k8s.Get(c, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	removed := s.DeepCopy()
	removed.Status.CapturedUsage = nil
	wantInvalid(t, k8s.Status().Update(c, removed), "captured usage ledger cannot be removed")

	if err := k8s.Get(c, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	changed := s.DeepCopy()
	changed.Spec.UsageCaptureParentUID = "other-uid"
	wantInvalid(t, k8s.Update(c, changed), "usage capture parent UID is immutable")
	if err := k8s.Get(c, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	dropped := s.DeepCopy()
	dropped.Spec.UsageCaptureParentUID = ""
	wantInvalid(t, k8s.Update(c, dropped), "no spec field may be added or removed")

	wide := testutil.New(t, "claude", "dev/client").Session
	wide.Name, wide.Namespace = name(), ns
	if err := k8s.Create(c, wide); err != nil {
		t.Fatal(err)
	}
	wide.Spec.UsageCaptureParentUID = "later-uid"
	wantInvalid(t, k8s.Update(c, wide), "no spec field may be added or removed")

	many := testutil.New(t, "claude", "dev/client").Session
	many.Name, many.Namespace = name(), ns
	if err := k8s.Create(c, many); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Get(c, client.ObjectKeyFromObject(many), many); err != nil {
		t.Fatal(err)
	}
	rows := make([]v1alpha1.CapturedUsageReceipt, v1alpha1.CapturedUsageMaxReceipts)
	for i := range rows {
		rows[i] = v1alpha1.CapturedUsageReceipt{Key: fmt.Sprintf("%064x", i+1), ParentUID: string(many.UID), ChildName: fmt.Sprintf("c-%d", i), CapturedAt: metav1.NewTime(time.Now().UTC().Truncate(time.Second)), Observation: *obs}
	}
	many.Status.CapturedUsage = &v1alpha1.CapturedUsageStatus{Version: 1, Scope: v1alpha1.CapturedUsageScope, Coverage: v1alpha1.CapturedUsageCoverage, Retention: v1alpha1.CapturedUsageRetention, Receipts: rows}
	if err := k8s.Status().Update(c, many); err != nil {
		t.Fatal("64 receipts rejected", err)
	}
	if err := k8s.Get(c, client.ObjectKeyFromObject(many), many); err != nil {
		t.Fatal(err)
	}
	many.Status.CapturedUsage.Receipts = append(many.Status.CapturedUsage.Receipts, v1alpha1.CapturedUsageReceipt{Key: strings.Repeat("f", 64), ParentUID: string(many.UID), ChildName: "extra", CapturedAt: metav1.Now(), Observation: *obs})
	wantInvalid(t, k8s.Status().Update(c, many), "at most 64")

	child := taskSession()
	child.Spec.Parent = s.Name
	if err := k8s.Create(c, child); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Delete(c, child); err != nil {
		t.Fatal(err)
	}
	again, err := client.New(restCfg, client.Options{Scheme: k8s.Scheme()})
	if err != nil {
		t.Fatal(err)
	}
	var kept v1alpha1.AgentSession
	if err := again.Get(c, client.ObjectKeyFromObject(s), &kept); err != nil || kept.Status.CapturedUsage == nil || len(kept.Status.CapturedUsage.Receipts) != 2 || !kept.Status.CapturedUsage.Overflow {
		t.Fatal("reconstructed reader lost the ledger after child removal", err)
	}
	if err := k8s.Get(c, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	s.Finalizers = nil
	if err := k8s.Update(c, s); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Delete(c, s); err != nil {
		t.Fatal(err)
	}
	if err := again.Get(c, client.ObjectKeyFromObject(s), &kept); !apierrors.IsNotFound(err) {
		t.Fatal("parent removal kept a receipt", err)
	}
}

func TestCapturedUsageStatusRemovalRefused(t *testing.T) {
	c := ctx(t)
	s := taskSession()
	if err := k8s.Create(c, s); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Get(c, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	obs := schemaUsage()
	obs.Source.SessionUID = string(s.UID)
	s.Status.CapturedUsage = &v1alpha1.CapturedUsageStatus{
		Version: 1, Scope: v1alpha1.CapturedUsageScope, Coverage: v1alpha1.CapturedUsageCoverage, Retention: v1alpha1.CapturedUsageRetention,
		Receipts: []v1alpha1.CapturedUsageReceipt{{
			Key: strings.Repeat("c", 64), ParentUID: string(s.UID), ChildName: "child",
			CapturedAt: metav1.NewTime(time.Now().UTC().Truncate(time.Second)), Observation: *obs,
		}},
	}
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("ledger-only status rejected", err)
	}
	if err := k8s.Get(c, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	if s.Status.CapturedUsage == nil || s.Status.UsageObservation != nil || s.Status.PrivateHistory != nil || s.Status.SessionDirectory != nil || s.Status.SharedAdmission != nil {
		t.Fatal("status removal fixture is not ledger-only")
	}
	rv := s.ResourceVersion
	key := s.Status.CapturedUsage.Receipts[0].Key
	for _, tc := range []struct {
		name  string
		patch string
		kind  types.PatchType
	}{
		{"status-null", `{"status":null}`, types.MergePatchType},
		{"status-remove", `[{"op":"remove","path":"/status"}]`, types.JSONPatchType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := s.DeepCopy()
			err := k8s.Status().Patch(c, bad, client.RawPatch(tc.kind, []byte(tc.patch)))
			wantInvalid(t, err, "captured usage ledger cannot be erased with status")
			got := &v1alpha1.AgentSession{}
			if err := k8s.Get(c, client.ObjectKeyFromObject(s), got); err != nil {
				t.Fatal(err)
			}
			if got.ResourceVersion != rv || got.Status.CapturedUsage == nil || len(got.Status.CapturedUsage.Receipts) != 1 || got.Status.CapturedUsage.Receipts[0].Key != key {
				t.Fatalf("refused status removal changed stored ledger rv=%s/%s", got.ResourceVersion, rv)
			}
		})
	}
}
