package grantattest

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func stamp() (created, at, now time.Time) {
	created = time.Date(2026, 10, 31, 19, 0, 0, 0, time.UTC)
	at = created.Add(5 * time.Minute)
	now = at
	return created, at, now
}

func digestA() string { return strings.Repeat("a", 43) }

func rd2Input() Input {
	created, at, now := stamp()
	return Input{
		Classes: []string{ClassRD2}, Now: now,
		Actor: Actor{UID: "actor-uid", Session: "actor-1"},
		Grant: GrantView{
			Name: "grant-s7rd2", UID: "grant-uid", Type: string(v1alpha1.GrantBreakglass), Role: v1alpha1.RoleBreakglass,
			TTL: time.Hour, RequesterSession: "requester-1", RequesterUID: "requester-uid",
			Phase: string(v1alpha1.GrantPending), ScopeDigest: digestA(), CreatedAt: created,
		},
		Request: Request{
			Reserved: true, Approve: true, Class: ClassRD2, BoundsRevision: BoundsRevision,
			GrantUID: "grant-uid", RequesterUID: "requester-uid", ActorUID: "actor-uid",
			Digest: digestA(), TomRef: "tom/msg-1", At: at, TTL: time.Hour,
		},
	}
}

func assertBound(t *testing.T, rec Record) {
	t.Helper()
	if rec.Authorizes || rec.RiskAccepted || rec.LiveGrant || rec.Deletes || rec.ServerNamespaceQueryPerformed || rec.EnforcesCommand || rec.EnforcesTarget || rec.ReadsKey || rec.CallsHardware || rec.PodExecCopyAddressed || rec.StandingApproval || rec.AttestedApproval || rec.ExtendsLease || rec.ChangesCredentialPolicy || rec.NeverContained || rec.DerivedImpersonationRecalled || rec.DerivedTokensRecalled || rec.SecretReadsBeyondNeverRecalled || rec.Wording != ReservedWording || rec.BoundsRevision != BoundsRevision {
		t.Fatalf("record claims an effect: %+v", rec)
	}
	raw, err := EncodeReserved(rec)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseReserved(raw)
	if err != nil || parsed.Digest != rec.Digest || parsed.Class != rec.Class {
		t.Fatalf("round trip: %v", err)
	}
}

func TestRD2Positive(t *testing.T) {
	rec := mustGrant(t, rd2Input())
	if rec.Class != ClassRD2 || rec.Binding != BindingPerGrantClusterRole || !rec.RootEquivalent || !rec.EveryResourceVerb || !rec.ClusterRoleBinding || !rec.ServiceAccountPerGrant || rec.NamespaceRoleBindings || rec.ExcludesNever || rec.TTL != time.Hour {
		t.Fatalf("rd2 record %+v", rec)
	}
}

func TestRD3aPositiveAndBounds(t *testing.T) {
	in := rd2Input()
	in.Classes = []string{ClassRD3a}
	in.Request.Class = ClassRD3a
	in.Grant.Type = string(v1alpha1.GrantKube)
	in.Grant.Role = v1alpha1.RoleSecretsRead
	in.Grant.Namespaces = []string{"ns-b", "ns-a"}
	in.Request.Namespaces = []string{"ns-b", "ns-a"}
	rec := mustGrant(t, in)
	if rec.Class != ClassRD3a || rec.Binding != BindingNamespaceRole || rec.ClusterRoleBinding || !rec.NamespaceRoleBindings || !rec.ExcludesNever || !slices.Equal(rec.Verbs, SecretVerbs()) || !slices.Equal(rec.Namespaces, []string{"ns-a", "ns-b"}) || !slices.Equal(in.Grant.Namespaces, []string{"ns-b", "ns-a"}) {
		t.Fatalf("rd3a record %+v input %v", rec, in.Grant.Namespaces)
	}
	wide := in
	wide.Grant.Namespaces = manyNS(17)
	wide.Request.Namespaces = slices.Clone(wide.Grant.Namespaces)
	if _, err := EvaluateReserved(wide); !errors.Is(err, ErrRefused) {
		t.Fatal("17 namespaces accepted")
	}
	wide.Grant.Namespaces = manyNS(16)
	wide.Request.Namespaces = slices.Clone(wide.Grant.Namespaces)
	if _, err := EvaluateReserved(wide); err != nil {
		t.Fatal(err)
	}
	for _, ns := range ReservedDefaultExclusions() {
		blocked := in
		blocked.Grant.Namespaces = []string{ns}
		blocked.Request.Namespaces = []string{ns}
		if _, err := EvaluateReserved(blocked); !errors.Is(err, ErrRefused) {
			t.Fatalf("never namespace %s accepted", ns)
		}
	}
}

func TestRD3bSnapshotNeverFutureAndReplacedUID(t *testing.T) {
	in := rd2Input()
	in.Classes = []string{ClassRD3b}
	in.Request.Class = ClassRD3b
	in.Grant.Type = string(v1alpha1.GrantKube)
	in.Grant.Role = v1alpha1.RoleSecretsRead
	snap := []NamespaceBinding{{Name: "zeta", UID: "uid-z"}, {Name: "alpha", UID: "uid-a"}}
	in.Request.AllNamespaces = true
	in.Request.Snapshot = snap
	rec := mustGrant(t, in)
	if !rec.AllNamespaces || rec.ClusterRoleBinding || rec.ServerNamespaceQueryPerformed || rec.Binding != BindingSnapshotRole || !slices.Equal(snapNames(rec.Snapshot), []string{"alpha", "zeta"}) || !slices.Equal(snapNames(snap), []string{"zeta", "alpha"}) {
		t.Fatalf("rd3b record %+v", rec.Snapshot)
	}
	never := in
	never.Request.Snapshot = append(slices.Clone(snap), NamespaceBinding{Name: "dev-env-system", UID: "uid-never"})
	if _, err := EvaluateReserved(never); !errors.Is(err, ErrRefused) {
		t.Fatal("never namespace entered the snapshot")
	}
	fallback := in
	fallback.Request.ClusterBindingFallback = true
	if _, err := EvaluateReserved(fallback); !errors.Is(err, ErrRefused) {
		t.Fatal("cluster binding fallback accepted")
	}
	live := []NamespaceBinding{{Name: "zeta", UID: "uid-z"}, {Name: "future", UID: "uid-f"}, {Name: "dev-agents", UID: "uid-never"}, {Name: "alpha", UID: "uid-a"}}
	before := slices.Clone(live)
	got, err := RecheckSnapshot(rec, live)
	if err != nil || got.ServerNamespaceQueryPerformed || !slices.Equal(snapNames(got.Bound), []string{"alpha", "zeta"}) || !slices.Equal(got.FutureExcluded, []string{"future"}) || !slices.Equal(got.NeverExcluded, []string{"dev-agents"}) || !slices.Equal(live, before) {
		t.Fatalf("recheck %+v %v", got, err)
	}
	replaced := slices.Clone(live)
	replaced[3].UID = "uid-replaced"
	if _, err := RecheckSnapshot(rec, replaced); !errors.Is(err, ErrRefused) {
		t.Fatal("replaced UID accepted")
	}
	if _, err := RecheckSnapshot(rec, live[:1]); !errors.Is(err, ErrRefused) {
		t.Fatal("absent snapshot namespace accepted")
	}
}

func TestRD4StorageTargetUIDBinding(t *testing.T) {
	in := rd2Input()
	in.Classes = []string{ClassRD4Storage}
	in.Request.Class = ClassRD4Storage
	in.Grant.Name = "grant-s7store"
	in.Grant.Type = string(v1alpha1.GrantKube)
	in.Grant.Role = v1alpha1.RoleStorage
	in.Grant.Namespaces = []string{"data"}
	in.Request.Namespaces = []string{"data"}
	targets := []StorageTarget{
		{Namespace: "data", Kind: KindStatefulSet, Name: "db", UID: "uid-db"},
		{Namespace: "data", Kind: KindPersistentVolumeClaim, Name: "vol", UID: "uid-vol"},
	}
	in.Request.Targets = targets
	rec := mustGrant(t, in)
	if rec.Binding != BindingDeletionGuard || rec.Deletes || rec.ServiceAccountPerGrant || rec.ClusterRoleBinding || !slices.Equal(targets, in.Request.Targets) {
		t.Fatalf("storage record %+v", rec)
	}
	active := in.Grant
	active.Phase = string(v1alpha1.GrantActive)
	guard, err := DeletionGuard(GuardInput{Record: rec, Grant: active, Target: targets[1]})
	if err != nil || !guard.Matches || guard.Deletes || guard.Authorizes {
		t.Fatalf("guard %+v %v", guard, err)
	}
	other := targets[1]
	other.UID = "uid-other"
	if _, err := DeletionGuard(GuardInput{Record: rec, Grant: active, Target: targets[1], Other: &other}); !errors.Is(err, ErrRefused) {
		t.Fatal("other UID with the same name matched")
	}
	ended := active
	ended.Phase = string(v1alpha1.GrantExpired)
	if _, err := DeletionGuard(GuardInput{Record: rec, Grant: ended, Target: targets[1]}); !errors.Is(err, ErrRefused) {
		t.Fatal("ended grant matched")
	}
	if _, err := DeletionGuard(GuardInput{Record: rec, Grant: active, Target: targets[1], OtherGrantUID: "other-grant"}); !errors.Is(err, ErrRefused) {
		t.Fatal("other grant matched")
	}
	if _, err := DeletionGuard(GuardInput{Record: rec, Grant: active, Target: targets[1], OtherEnded: true}); !errors.Is(err, ErrRefused) {
		t.Fatal("ended other grant matched")
	}
	if _, err := DeletionGuard(GuardInput{Record: rec, Grant: active, Target: targets[1], OtherTomRef: "other/ref"}); !errors.Is(err, ErrRefused) {
		t.Fatal("other ref matched")
	}
	dup := in
	dup.Request.Targets = []StorageTarget{targets[0], targets[0]}
	if _, err := EvaluateReserved(dup); !errors.Is(err, ErrRefused) || !slices.Equal(dup.Request.Targets, []StorageTarget{targets[0], targets[0]}) {
		t.Fatal("duplicate target mutated or accepted")
	}
}

func TestRD4HardwareOwnLease(t *testing.T) {
	rec := mustHardware(t, hardwareInput())
	if rec.Class != ClassRD4Hardware || rec.Binding != BindingAuditIntent || rec.LeaseClass != LeaseClassC4 || rec.TTL != 0 || rec.GrantUID != "" || rec.EnforcesCommand || rec.EnforcesTarget || rec.ReadsKey || rec.CallsHardware || rec.ChangesCredentialPolicy || rec.Operation != "audit power state" {
		t.Fatalf("hardware record %+v", rec)
	}
	for name, edit := range map[string]func(*HardwareInput){
		"other owner":      func(in *HardwareInput) { in.Lease.OwnerUID = "other-owner" },
		"other requester":  func(in *HardwareInput) { in.RequesterUID = "requester-uid" },
		"wrong class":      func(in *HardwareInput) { in.Lease.Class = "hw-ssh" },
		"ended":            func(in *HardwareInput) { in.Lease.Ended = true },
		"released":         func(in *HardwareInput) { in.Lease.Release = true },
		"expired":          func(in *HardwareInput) { in.Lease.ExpiresAt = in.Now },
		"through s2":       func(in *HardwareInput) { in.ThroughS2 = true },
		"grant uid":        func(in *HardwareInput) { in.GrantUID = "grant-uid" },
		"read key":         func(in *HardwareInput) { in.ReadKey = true },
		"call hardware":    func(in *HardwareInput) { in.CallHardware = true },
		"enforce command":  func(in *HardwareInput) { in.EnforceCommand = true },
		"ttl":              func(in *HardwareInput) { in.TTL = 30 * time.Minute },
		"missing firmware": func(in *HardwareInput) { in.Intent.Firmware = "" },
		"wildcard host":    func(in *HardwareInput) { in.Intent.Host = "host*" },
		"bad tomRef":       func(in *HardwareInput) { in.Intent.TomRef = "has space" },
	} {
		in := hardwareInput()
		edit(&in)
		if _, err := EvaluateReservedHardware(in); !errors.Is(err, ErrRefused) {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestDisabledUnlistedAndWrongCase(t *testing.T) {
	off := rd2Input()
	off.Classes = nil
	off.Existing = &Record{}
	if _, err := EvaluateReserved(off); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if _, err := EvaluateReservedHardware(HardwareInput{}); !errors.Is(err, ErrDisabled) {
		t.Fatal("hardware default is on")
	}
	unlisted := rd2Input()
	unlisted.Classes = []string{ClassRD3a}
	if _, err := EvaluateReserved(unlisted); !errors.Is(err, ErrUnlisted) {
		t.Fatalf("unlisted: %v", err)
	}
	unknown := rd2Input()
	unknown.Request.Class = "rd9"
	if _, err := EvaluateReserved(unknown); !errors.Is(err, ErrRefused) || errors.Is(err, ErrUnlisted) {
		t.Fatalf("unknown class: %v", err)
	}
	wrong := rd2Input()
	wrong.Grant.Type = string(v1alpha1.GrantKube)
	wrong.Grant.Role = v1alpha1.RoleSecretsRead
	wrong.Grant.Namespaces = []string{"ns-a"}
	wrong.Request.Namespaces = []string{"ns-a"}
	if _, err := EvaluateReserved(wrong); !errors.Is(err, ErrRefused) {
		t.Fatal("rd2 accepted a secrets grant")
	}
	hw := hardwareInput()
	hw.Classes = []string{ClassRD2}
	if _, err := EvaluateReservedHardware(hw); !errors.Is(err, ErrUnlisted) {
		t.Fatal("unlisted hardware accepted")
	}
}

func TestInvalidRefStandingAndSelfGrant(t *testing.T) {
	for _, ref := range []string{"", "has space", strings.Repeat("a", 129), "http://example.test?q=1"} {
		in := rd2Input()
		in.Request.TomRef = ref
		if _, err := EvaluateReserved(in); !errors.Is(err, ErrRefused) {
			t.Fatalf("ref %q accepted", ref)
		}
	}
	standing := rd2Input()
	standing.Request.Standing = true
	attested := rd2Input()
	attested.Request.Attested = true
	if _, err := EvaluateReserved(standing); !errors.Is(err, ErrRefused) {
		t.Fatal("standing approval accepted")
	}
	if _, err := EvaluateReserved(attested); !errors.Is(err, ErrRefused) {
		t.Fatal("attested approval accepted")
	}
	self := rd2Input()
	self.Actor.UID = self.Grant.RequesterUID
	self.Request.ActorUID = self.Grant.RequesterUID
	if _, err := EvaluateReserved(self); !errors.Is(err, ErrRefused) {
		t.Fatal("self grant accepted")
	}
	named := rd2Input()
	named.Actor.Session = named.Grant.RequesterSession
	if _, err := EvaluateReserved(named); !errors.Is(err, ErrRefused) {
		t.Fatal("self session accepted")
	}
	other := rd2Input()
	other.Request.ActorUID = "other-actor"
	if _, err := EvaluateReserved(other); !errors.Is(err, ErrRefused) {
		t.Fatal("request posted as another actor")
	}
	risk := rd2Input()
	risk.Request.RiskAccepted = true
	if _, err := EvaluateReserved(risk); !errors.Is(err, ErrRefused) {
		t.Fatal("risk accepted")
	}
}

func TestTTLMaxExpiryFutureAndMaintenanceDefault(t *testing.T) {
	if DecisionWindow != 30*time.Minute || ReservedMaxTTL != time.Hour || DecisionWindow == ReservedMaxTTL {
		t.Fatal("maintenance window and reserved maximum are blurred")
	}
	zero := rd2Input()
	zero.Request.TTL = 0
	if _, err := EvaluateReserved(zero); !errors.Is(err, ErrRefused) {
		t.Fatal("empty TTL became a default")
	}
	over := rd2Input()
	over.Request.TTL = time.Hour + time.Second
	if _, err := EvaluateReserved(over); !errors.Is(err, ErrRefused) {
		t.Fatal("TTL above the grant was accepted")
	}
	grantOver := rd2Input()
	grantOver.Grant.TTL = 2 * time.Hour
	if _, err := EvaluateReserved(grantOver); !errors.Is(err, ErrRefused) {
		t.Fatal("grant TTL above 1h was accepted")
	}
	explicit := rd2Input()
	explicit.Grant.TTL = time.Hour
	explicit.Request.TTL = 30 * time.Minute
	rec := mustGrant(t, explicit)
	if rec.TTL != 30*time.Minute {
		t.Fatalf("explicit 30m stored as %s", rec.TTL)
	}
	extend := rd2Input()
	extend.Request.Extend = true
	if _, err := EvaluateReserved(extend); !errors.Is(err, ErrRefused) {
		t.Fatal("extension accepted")
	}
	maintenance := rd2Input()
	maintenance.Grant.Type = string(v1alpha1.GrantMaintenance)
	if _, err := EvaluateReserved(maintenance); !errors.Is(err, ErrRefused) {
		t.Fatal("maintenance grant accepted as reserved")
	}
	credential := rd2Input()
	credential.Grant.Type = string(v1alpha1.GrantCredential)
	if _, err := EvaluateReserved(credential); !errors.Is(err, ErrRefused) {
		t.Fatal("credential grant accepted on the reserved grant path")
	}
	created, at, now := stamp()
	future := rd2Input()
	future.Request.At = now.Add(2 * time.Minute)
	if _, err := EvaluateReserved(future); !errors.Is(err, ErrRefused) {
		t.Fatal("future decision accepted")
	}
	edge := rd2Input()
	edge.Request.At = now.Add(ReservedMaxClockSkew)
	if _, err := EvaluateReserved(edge); err != nil {
		t.Fatal(err)
	}
	early := rd2Input()
	early.Request.At = created.Add(-time.Second)
	if _, err := EvaluateReserved(early); !errors.Is(err, ErrRefused) {
		t.Fatal("decision before the grant accepted")
	}
	expired := rd2Input()
	expired.Now = created.Add(DecisionWindow)
	expired.Request.At = at
	if _, err := EvaluateReserved(expired); !errors.Is(err, ErrRefused) {
		t.Fatal("expired pending window accepted")
	}
}

func TestStaleUIDDigestBoundsAndReplay(t *testing.T) {
	for name, edit := range map[string]func(*Input){
		"grant uid":     func(in *Input) { in.Request.GrantUID = "other-grant" },
		"requester uid": func(in *Input) { in.Request.RequesterUID = "other-requester" },
		"actor uid":     func(in *Input) { in.Request.ActorUID = "other-actor" },
		"digest":        func(in *Input) { in.Request.Digest = strings.Repeat("b", 43) },
		"bounds":        func(in *Input) { in.Request.BoundsRevision = 2 },
		"zero bounds":   func(in *Input) { in.Request.BoundsRevision = 0 },
	} {
		in := rd2Input()
		edit(&in)
		if _, err := EvaluateReserved(in); !errors.Is(err, ErrRefused) {
			t.Fatalf("%s accepted", name)
		}
	}
	replay := rd2Input()
	replay.Existing = &Record{Class: ClassRD2}
	if _, err := EvaluateReserved(replay); !errors.Is(err, ErrReplay) {
		t.Fatalf("replay: %v", err)
	}
	hw := hardwareInput()
	hw.Existing = &Record{}
	if _, err := EvaluateReservedHardware(hw); !errors.Is(err, ErrReplay) {
		t.Fatal("hardware replay accepted")
	}
}

func TestDuplicateListsDoNotMutate(t *testing.T) {
	in := rd2Input()
	in.Classes = []string{ClassRD3a}
	in.Request.Class = ClassRD3a
	in.Grant.Type = string(v1alpha1.GrantKube)
	in.Grant.Role = v1alpha1.RoleSecretsRead
	ns := []string{"ns-a", "ns-a"}
	in.Grant.Namespaces = ns
	in.Request.Namespaces = ns
	if _, err := EvaluateReserved(in); !errors.Is(err, ErrRefused) || !slices.Equal(ns, []string{"ns-a", "ns-a"}) {
		t.Fatal("duplicate namespaces mutated or accepted")
	}
	snapIn := rd2Input()
	snapIn.Classes = []string{ClassRD3b}
	snapIn.Request.Class = ClassRD3b
	snapIn.Grant.Type = string(v1alpha1.GrantKube)
	snapIn.Grant.Role = v1alpha1.RoleSecretsRead
	snapIn.Request.AllNamespaces = true
	ambiguous := []NamespaceBinding{{Name: "ns-a", UID: "uid-1"}, {Name: "ns-a", UID: "uid-2"}}
	snapIn.Request.Snapshot = ambiguous
	if _, err := EvaluateReserved(snapIn); !errors.Is(err, ErrRefused) || ambiguous[0].UID != "uid-1" || ambiguous[1].UID != "uid-2" {
		t.Fatal("ambiguous snapshot mutated or accepted")
	}
	sameUID := []NamespaceBinding{{Name: "ns-a", UID: "uid-1"}, {Name: "ns-b", UID: "uid-1"}}
	snapIn.Request.Snapshot = sameUID
	if _, err := EvaluateReserved(snapIn); !errors.Is(err, ErrRefused) {
		t.Fatal("duplicate snapshot UID accepted")
	}
	verbs := rd2Input()
	verbs.Request.Verbs = []string{"get", "list", "watch"}
	if _, err := EvaluateReserved(verbs); !errors.Is(err, ErrRefused) || !slices.Equal(verbs.Request.Verbs, []string{"get", "list", "watch"}) {
		t.Fatal("verb list mutated or accepted")
	}
}

func TestScopeDigestFollowsGrantattest(t *testing.T) {
	grant := func(ttl time.Duration) *v1alpha1.AccessGrant {
		return &v1alpha1.AccessGrant{Spec: v1alpha1.AccessGrantSpec{
			Requester: v1alpha1.GrantRequester{Session: "requester-1", SessionUID: "11111111-1111-1111-1111-111111111111"},
			Type:      v1alpha1.GrantBreakglass,
			Kube:      &v1alpha1.KubeGrant{Role: v1alpha1.RoleBreakglass},
			TTL:       metav1.Duration{Duration: ttl},
			Reason:    "reserved",
		}}
	}
	d := ScopeDigest(grant(time.Hour))
	if !scopeDigestPattern.MatchString(d) {
		t.Fatalf("digest %q", d)
	}
	in := rd2Input()
	in.Grant.RequesterUID = "11111111-1111-1111-1111-111111111111"
	in.Request.RequesterUID = in.Grant.RequesterUID
	in.Grant.ScopeDigest = d
	in.Request.Digest = d
	if _, err := EvaluateReserved(in); err != nil {
		t.Fatal(err)
	}
	in.Request.Digest = ScopeDigest(grant(30 * time.Minute))
	if _, err := EvaluateReserved(in); !errors.Is(err, ErrRefused) {
		t.Fatal("a digest from a different TTL was accepted")
	}
}

func TestParseRejectsAuthorizationClaim(t *testing.T) {
	raw, err := EncodeReserved(mustGrant(t, rd2Input()))
	if err != nil {
		t.Fatal(err)
	}
	claimed := strings.Replace(raw, `"authorizes":false`, `"authorizes":true`, 1)
	if _, err := ParseReserved(claimed); !errors.Is(err, ErrRefused) {
		t.Fatal("authorization claim parsed")
	}
	unknown := strings.Replace(raw, `"v":1`, `"v":1,"extra":true`, 1)
	if _, err := ParseReserved(unknown); !errors.Is(err, ErrRefused) {
		t.Fatal("unknown field parsed")
	}
	if _, err := ParseReserved(""); !errors.Is(err, ErrRefused) {
		t.Fatal("empty record parsed")
	}
}

func TestParseClasses(t *testing.T) {
	got, err := ParseReservedClasses("")
	if err != nil || got != nil {
		t.Fatalf("default %#v %v", got, err)
	}
	got, err = ParseReservedClasses(" rd3a, rd2 ")
	if err != nil || !slices.Equal(got, []string{ClassRD2, ClassRD3a}) {
		t.Fatalf("parsed %#v %v", got, err)
	}
	if _, err := ParseReservedClasses("rd2,rd2"); !errors.Is(err, ErrRefused) {
		t.Fatal("duplicate class accepted")
	}
	if _, err := ParseReservedClasses("nope"); !errors.Is(err, ErrRefused) {
		t.Fatal("unknown class accepted")
	}
	if _, err := ParseReservedClasses("rd2,"); !errors.Is(err, ErrRefused) {
		t.Fatal("empty entry accepted")
	}
}

func TestSnapshotCap(t *testing.T) {
	in := rd2Input()
	in.Classes = []string{ClassRD3b}
	in.Request.Class = ClassRD3b
	in.Grant.Type = string(v1alpha1.GrantKube)
	in.Grant.Role = v1alpha1.RoleSecretsRead
	in.Request.AllNamespaces = true
	in.Request.Snapshot = manySnap(MaxNamespaceSnapshot)
	if _, err := EvaluateReserved(in); err != nil {
		t.Fatal(err)
	}
	in.Request.Snapshot = manySnap(MaxNamespaceSnapshot + 1)
	if _, err := EvaluateReserved(in); !errors.Is(err, ErrRefused) {
		t.Fatal("snapshot above the cap was truncated or accepted")
	}
}

func mustGrant(t *testing.T, in Input) Record {
	t.Helper()
	rec, err := EvaluateReserved(in)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	assertBound(t, rec)
	return rec
}

func mustHardware(t *testing.T, in HardwareInput) Record {
	t.Helper()
	rec, err := EvaluateReservedHardware(in)
	if err != nil {
		t.Fatalf("hardware: %v", err)
	}
	assertBound(t, rec)
	return rec
}

func hardwareInput() HardwareInput {
	_, at, now := stamp()
	return HardwareInput{
		Classes: []string{ClassRD4Hardware}, Now: now, At: at, BoundsRevision: BoundsRevision,
		Actor: Actor{UID: "actor-uid", Session: "actor-1"}, RequesterUID: "actor-uid",
		Lease:  LeaseView{Class: LeaseClassC4, UID: "lease-uid", OwnerUID: "actor-uid", Phase: string(v1alpha1.GrantActive), ExpiresAt: now.Add(time.Hour)},
		Intent: HardwareIntent{Operation: "audit power state", Host: "talosw01", Device: "disk-1", Firmware: "1.2.3", TomRef: "tom/hw-1"},
	}
}

func manyNS(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("ns%02d", i+1)
	}
	return out
}

func manySnap(n int) []NamespaceBinding {
	out := make([]NamespaceBinding, n)
	for i := range out {
		out[i] = NamespaceBinding{Name: fmt.Sprintf("ns%02d", i+1), UID: fmt.Sprintf("uid-%02d", i+1)}
	}
	return out
}

func snapNames(in []NamespaceBinding) []string {
	out := make([]string, len(in))
	for i := range in {
		out[i] = in[i].Name
	}
	return out
}
