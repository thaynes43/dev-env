package apiv1

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func portalClaimsFixture() PortalTaskClaims {
	observed := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	beat := observed.Add(-2 * time.Minute)
	return PortalTaskClaims{Version: PortalTaskClaimsVersion, Project: "dev-env", State: PortalTaskClaimsStateAvailable, Reason: PortalTaskClaimsReasonComplete,
		Items: []PortalTaskClaimItem{{Name: "task-claim." + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32), UID: "00000000-0000-4000-8000-000000000001",
			Repository: "dev-env", TaskKey: "issue:42", GitHub: "thaynes43/dev-env", CreatedAt: observed.Add(-time.Hour),
			Owner: PortalTaskClaimOwner{Name: "dev-env-1102", UID: "11111111-0000-4000-8000-000000000001", State: PortalClaimOwnerRunning, Freshness: PortalClaimFreshnessObserved, HeartbeatAt: &beat}}},
		Limit: 100, Complete: true, ObservedAt: observed}
}

func TestPortalTaskClaimsJSONKeysAreTheAllowlist(t *testing.T) {
	page := portalClaimsFixture()
	page.State, page.Reason, page.Complete, page.NextCursor = PortalTaskClaimsStateAvailable, PortalTaskClaimsReasonTruncated, false, "Y3Vyc29y"
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		t.Fatal("decode")
	}
	keys := func(m map[string]json.RawMessage) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	sorted := func(a ...[]string) []string {
		var out []string
		for _, s := range a {
			out = append(out, s...)
		}
		sort.Strings(out)
		return out
	}
	if !reflect.DeepEqual(keys(top), sorted(PortalTaskClaimsKeys, PortalTaskClaimsOptional)) {
		t.Fatalf("page keys %v", keys(top))
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(top["items"], &items) != nil || len(items) != 1 || !reflect.DeepEqual(keys(items[0]), sorted(PortalTaskClaimItemKeys)) {
		t.Fatalf("item keys %v", items)
	}
	var owner map[string]json.RawMessage
	if json.Unmarshal(items[0]["owner"], &owner) != nil || !reflect.DeepEqual(keys(owner), sorted(PortalTaskClaimOwnerKeys, PortalTaskClaimOwnerOptional)) {
		t.Fatalf("owner keys %v", owner)
	}
	for _, withheld := range []string{"actor", "serviceAccount", "podName", "podUID", "catalogRevision", "rulesRevision", "defaultBranch", "initialPinState", "reportedHead", "namespace", "stopState"} {
		if strings.Contains(string(raw), `"`+withheld+`"`) {
			t.Fatalf("withheld field %s on the wire", withheld)
		}
	}
}

func TestValidatePortalTaskClaims(t *testing.T) {
	if err := ValidatePortalTaskClaims(portalClaimsFixture()); err != nil {
		t.Fatal(err)
	}
	empty := portalClaimsFixture()
	empty.State, empty.Items = PortalTaskClaimsStateEmpty, []PortalTaskClaimItem{}
	if err := ValidatePortalTaskClaims(empty); err != nil {
		t.Fatal(err)
	}
	truncatedEmpty := portalClaimsFixture()
	truncatedEmpty.Reason, truncatedEmpty.Complete, truncatedEmpty.NextCursor, truncatedEmpty.Items = PortalTaskClaimsReasonTruncated, false, "abc_-123", []PortalTaskClaimItem{}
	if err := ValidatePortalTaskClaims(truncatedEmpty); err != nil {
		t.Fatal("a truncated page may hold zero items")
	}
	for _, reason := range []string{PortalTaskClaimsReasonUnconfigured, PortalTaskClaimsReasonScopeUnconfirmed, PortalTaskClaimsReasonUnavailable} {
		unknown := portalClaimsFixture()
		unknown.State, unknown.Reason, unknown.Complete, unknown.Items = PortalTaskClaimsStateUnknown, reason, false, []PortalTaskClaimItem{}
		if err := ValidatePortalTaskClaims(unknown); err != nil {
			t.Fatalf("%s: %v", reason, err)
		}
	}
	stale := portalClaimsFixture()
	old := stale.ObservedAt.Add(-16 * time.Minute)
	stale.Items[0].Owner.Freshness, stale.Items[0].Owner.HeartbeatAt = PortalClaimFreshnessStale, &old
	if err := ValidatePortalTaskClaims(stale); err != nil {
		t.Fatal(err)
	}
	future := portalClaimsFixture().ObservedAt.Add(time.Second)
	refusals := map[string]func(*PortalTaskClaims){
		"version":    func(p *PortalTaskClaims) { p.Version = "portaltaskclaims.v2" },
		"project":    func(p *PortalTaskClaims) { p.Project = "Dev_Env" },
		"limit zero": func(p *PortalTaskClaims) { p.Limit = 0 },
		"limit 101":  func(p *PortalTaskClaims) { p.Limit = 101 },
		"items over limit": func(p *PortalTaskClaims) {
			second := p.Items[0]
			second.Name, second.UID = "task-claim."+strings.Repeat("c", 32)+"."+strings.Repeat("d", 32), "00000000-0000-4000-8000-000000000002"
			p.Limit, p.Items = 1, append(p.Items, second)
		},
		"nil items":        func(p *PortalTaskClaims) { p.Items = nil },
		"observed zero":    func(p *PortalTaskClaims) { p.ObservedAt = time.Time{} },
		"unknown state":    func(p *PortalTaskClaims) { p.State = "Partial" },
		"empty with items": func(p *PortalTaskClaims) { p.State = PortalTaskClaimsStateEmpty },
		"empty truncated": func(p *PortalTaskClaims) {
			p.State, p.Items, p.Reason = PortalTaskClaimsStateEmpty, []PortalTaskClaimItem{}, PortalTaskClaimsReasonTruncated
		},
		"unknown with items": func(p *PortalTaskClaims) {
			p.State, p.Reason, p.Complete = PortalTaskClaimsStateUnknown, PortalTaskClaimsReasonUnavailable, false
		},
		"unknown complete": func(p *PortalTaskClaims) {
			p.State, p.Reason, p.Items, p.Complete = PortalTaskClaimsStateUnknown, PortalTaskClaimsReasonUnavailable, []PortalTaskClaimItem{}, true
		},
		"stale but recent": func(p *PortalTaskClaims) {
			recent := p.ObservedAt.Add(-time.Minute)
			p.Items[0].Owner.Freshness, p.Items[0].Owner.HeartbeatAt = PortalClaimFreshnessStale, &recent
		},
		"complete with cursor":      func(p *PortalTaskClaims) { p.NextCursor = "abc" },
		"truncated without cursor":  func(p *PortalTaskClaims) { p.Reason, p.Complete = PortalTaskClaimsReasonTruncated, false },
		"truncated marked complete": func(p *PortalTaskClaims) { p.Reason, p.NextCursor = PortalTaskClaimsReasonTruncated, "abc" },
		"cursor grammar": func(p *PortalTaskClaims) {
			p.Reason, p.Complete, p.NextCursor = PortalTaskClaimsReasonTruncated, false, "a/b="
		},
		"cursor size": func(p *PortalTaskClaims) {
			p.Reason, p.Complete, p.NextCursor = PortalTaskClaimsReasonTruncated, false, strings.Repeat("a", 2049)
		},
		"duplicate":             func(p *PortalTaskClaims) { p.Items = append(p.Items, p.Items[0]) },
		"claim name":            func(p *PortalTaskClaims) { p.Items[0].Name = "task-claim.short" },
		"task key":              func(p *PortalTaskClaims) { p.Items[0].TaskKey = "issue:042" },
		"github":                func(p *PortalTaskClaims) { p.Items[0].GitHub = "thaynes43/dev-env.git" },
		"created zero":          func(p *PortalTaskClaims) { p.Items[0].CreatedAt = time.Time{} },
		"owner state word":      func(p *PortalTaskClaims) { p.Items[0].Owner.State = "Active" },
		"freshness word":        func(p *PortalTaskClaims) { p.Items[0].Owner.Freshness = "fresh" },
		"observed without beat": func(p *PortalTaskClaims) { p.Items[0].Owner.HeartbeatAt = nil },
		"observed but old":      func(p *PortalTaskClaims) { p.Items[0].Owner.HeartbeatAt = &old },
		"future heartbeat":      func(p *PortalTaskClaims) { p.Items[0].Owner.HeartbeatAt = &future },
		"unknown with beat":     func(p *PortalTaskClaims) { p.Items[0].Owner.Freshness = PortalClaimFreshnessUnknown },
		"not found with beat":   func(p *PortalTaskClaims) { p.Items[0].Owner.State = PortalClaimOwnerNotFound },
		"replaced observed": func(p *PortalTaskClaims) {
			p.Items[0].Owner.State, p.Items[0].Owner.HeartbeatAt = PortalClaimOwnerReplaced, nil
		},
	}
	for name, mutate := range refusals {
		page := portalClaimsFixture()
		mutate(&page)
		if ValidatePortalTaskClaims(page) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestPortalClaimIssueNumberIsExact(t *testing.T) {
	if n, ok := PortalClaimIssueNumber("issue:42"); !ok || n != "42" {
		t.Fatal("issue key refused")
	}
	for _, key := range []string{"issue:0", "issue:042", "task:op359", "issue:42 ", "Issue:42", "task:upgrade.pr12." + strings.Repeat("a", 40)} {
		if _, ok := PortalClaimIssueNumber(key); ok {
			t.Fatalf("%q linked", key)
		}
	}
}
