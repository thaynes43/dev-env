package pages

import (
	"context"
	"testing"
	"time"
)

func TestResponseTaskClaimOwnerBoundToJoinedSnapshot(t *testing.T) {
	owner := ClaimOwner{Namespace: "dev-agents", Name: "original-session", UID: "original-uid"}
	for _, mode := range []string{"joined", "missing owner", "invalid namespace", "invalid name", "invalid name labels", "invalid uid", "unknown uid", "redacted name", "OpenProject", "stale", "refire", "wrong attempt", "mismatched", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			id, sources, at, _, claims := responseFixture()
			claims.observation.Claim.Reference = CanonicalClaimReference{Kind: "TaskClaim", ID: "dev-env-system/synthetic-claim/claim-uid"}
			claims.observation.Claim.ClaimOwner = owner
			switch mode {
			case "missing owner":
				claims.observation.Claim.ClaimOwner = ClaimOwner{}
			case "invalid namespace":
				claims.observation.Claim.ClaimOwner.Namespace = "../other"
			case "invalid name":
				claims.observation.Claim.ClaimOwner.Name = "unsafe/name"
			case "invalid name labels":
				claims.observation.Claim.ClaimOwner.Name = "bad..name"
			case "invalid uid":
				claims.observation.Claim.ClaimOwner.UID = "unsafe/uid"
			case "unknown uid":
				claims.observation.Claim.ClaimOwner.UID = "Unknown"
			case "redacted name":
				claims.observation.Claim.ClaimOwner.Name = "token=secret"
			case "OpenProject":
				claims.observation.Claim.Reference = CanonicalClaimReference{Kind: "OpenProject", ID: "322"}
			case "stale":
				claims.observation.ObservedAt = at.Add(-time.Hour)
			case "refire":
				claims.observation.Claim.Identity.StartsAt = "2026-10-30T11:01:00Z"
			case "wrong attempt":
				claims.observation.Claim.Order.Created = "1"
			case "mismatched":
				claims.observation.Claim.State = "resolved"
			case "unreadable":
				claims.observation.Available = false
			}
			got := ProjectResponses(context.Background(), []AlertIdentity{id, id}, sources, at)[id]
			want := ClaimOwner{Namespace: "Unknown", Name: "Unknown", UID: "Unknown"}
			if mode == "joined" {
				want = owner
			}
			if got.ClaimOwner != want {
				t.Fatalf("claim owner is not bound to the verified snapshot: %+v", got)
			}
			if len(claims.lookups) != 1 {
				t.Fatal("added per-claim/owner fan-out", claims.lookups)
			}
		})
	}
}
