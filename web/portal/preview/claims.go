package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// Claims previews (DESIGN-045) are fixed synthetic presentation data. No
// session, token, network reader or operator participates in their rendering.

var claimsPreviewNow = time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)

func claimsPreviewItem(i int, state, freshness string, beat *time.Time) apiv1.PortalTaskClaimItem {
	digest := sha256.Sum256([]byte("synthetic-claim-" + strconv.Itoa(i)))
	name := hex.EncodeToString(digest[:])
	key := "task:synthetic-story-" + strconv.Itoa(i)
	if i%3 == 0 {
		key = "issue:" + strconv.Itoa(900+i)
	}
	return apiv1.PortalTaskClaimItem{
		Name: "task-claim." + name[:32] + "." + name[32:], UID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i),
		Repository: "dev-env", TaskKey: key, GitHub: "synthetic-owner/synthetic-repo",
		CreatedAt: claimsPreviewNow.Add(-time.Duration(i+1) * 37 * time.Minute),
		Owner: apiv1.PortalTaskClaimOwner{Name: "synthetic-agent-" + strconv.Itoa(i%17), UID: fmt.Sprintf("11111111-0000-4000-8000-%012d", i%17),
			State: state, Freshness: freshness, HeartbeatAt: beat},
	}
}

func claimsPreviewBeat(ago time.Duration) *time.Time {
	at := claimsPreviewNow.Add(-ago)
	return &at
}

// claimsPreviewOwner cycles through every owner state the label table names.
func claimsPreviewOwner(i int) (string, string, *time.Time) {
	switch i % 12 {
	case 0, 1, 2, 3:
		return apiv1.PortalClaimOwnerRunning, apiv1.PortalClaimFreshnessObserved, claimsPreviewBeat(2 * time.Minute)
	case 4:
		return apiv1.PortalClaimOwnerIdle, apiv1.PortalClaimFreshnessObserved, claimsPreviewBeat(9 * time.Minute)
	case 5:
		return apiv1.PortalClaimOwnerDraining, apiv1.PortalClaimFreshnessObserved, claimsPreviewBeat(4 * time.Minute)
	case 6:
		return apiv1.PortalClaimOwnerRunning, apiv1.PortalClaimFreshnessStale, claimsPreviewBeat(3 * time.Hour)
	case 7:
		return apiv1.PortalClaimOwnerPending, apiv1.PortalClaimFreshnessUnknown, nil
	case 8:
		return apiv1.PortalClaimOwnerSuspended, apiv1.PortalClaimFreshnessStale, claimsPreviewBeat(26 * time.Hour)
	case 9:
		return apiv1.PortalClaimOwnerArchived, apiv1.PortalClaimFreshnessStale, claimsPreviewBeat(80 * time.Hour)
	case 10:
		return apiv1.PortalClaimOwnerNotFound, apiv1.PortalClaimFreshnessUnknown, nil
	default:
		return apiv1.PortalClaimOwnerUnknown, apiv1.PortalClaimFreshnessUnknown, nil
	}
}

func claimsPreviewPage(state, reason string, items []apiv1.PortalTaskClaimItem, cursor string) apiv1.PortalTaskClaims {
	if items == nil {
		items = []apiv1.PortalTaskClaimItem{}
	}
	return apiv1.PortalTaskClaims{Version: apiv1.PortalTaskClaimsVersion, Project: "dev-env", State: state, Reason: reason, Items: items,
		Limit: apiv1.PortalTaskClaimsMaxLimit, NextCursor: cursor, Complete: cursor == "" && state != apiv1.PortalTaskClaimsStateUnknown, ObservedAt: claimsPreviewNow}
}

// claimsPreviewMany is the first page of 120 synthetic claims: 100 items in
// store order and a continuation.
func claimsPreviewMany() apiv1.PortalTaskClaims {
	items := make([]apiv1.PortalTaskClaimItem, 0, apiv1.PortalTaskClaimsMaxLimit)
	for i := 0; i < 120 && len(items) < apiv1.PortalTaskClaimsMaxLimit; i++ {
		state, freshness, beat := claimsPreviewOwner(i)
		items = append(items, claimsPreviewItem(i, state, freshness, beat))
	}
	return claimsPreviewPage(apiv1.PortalTaskClaimsStateAvailable, apiv1.PortalTaskClaimsReasonTruncated, items, "c3ludGhldGljLXByZXZpZXctY3Vyc29y")
}

func claimsPreviewUnknownOwners() apiv1.PortalTaskClaims {
	items := []apiv1.PortalTaskClaimItem{
		claimsPreviewItem(0, apiv1.PortalClaimOwnerUnknown, apiv1.PortalClaimFreshnessUnknown, nil),
		claimsPreviewItem(1, apiv1.PortalClaimOwnerReplaced, apiv1.PortalClaimFreshnessUnknown, nil),
		claimsPreviewItem(2, apiv1.PortalClaimOwnerNotFound, apiv1.PortalClaimFreshnessUnknown, nil),
		claimsPreviewItem(3, apiv1.PortalClaimOwnerRunning, apiv1.PortalClaimFreshnessUnknown, nil),
		claimsPreviewItem(4, apiv1.PortalClaimOwnerIdle, apiv1.PortalClaimFreshnessStale, claimsPreviewBeat(47*time.Minute)),
		claimsPreviewItem(5, apiv1.PortalClaimOwnerFailed, apiv1.PortalClaimFreshnessStale, claimsPreviewBeat(5*time.Hour)),
		claimsPreviewItem(6, apiv1.PortalClaimOwnerDeleting, apiv1.PortalClaimFreshnessUnknown, nil),
		claimsPreviewItem(7, apiv1.PortalClaimOwnerRunning, apiv1.PortalClaimFreshnessObserved, claimsPreviewBeat(time.Minute)),
	}
	return claimsPreviewPage(apiv1.PortalTaskClaimsStateAvailable, apiv1.PortalTaskClaimsReasonComplete, items, "")
}

func claimsPreviewFixtures() []struct {
	name string
	page portal.ClaimsPage
} {
	return []struct {
		name string
		page portal.ClaimsPage
	}{
		{"claims-empty.html", portal.ClaimsPage{State: portal.ClaimsEmpty, Read: claimsPreviewPage(apiv1.PortalTaskClaimsStateEmpty, apiv1.PortalTaskClaimsReasonComplete, nil, "")}},
		{"claims-many.html", portal.ClaimsPage{State: portal.ClaimsAvailable, Read: claimsPreviewMany()}},
		{"claims-unknown-owner.html", portal.ClaimsPage{State: portal.ClaimsAvailable, Read: claimsPreviewUnknownOwners()}},
		{"claims-refused.html", portal.ClaimsPage{State: portal.ClaimsRefused}},
		{"claims-unavailable.html", portal.ClaimsPage{State: portal.ClaimsUnavailable}},
		{"claims-not-connected.html", portal.ClaimsPage{State: portal.ClaimsNotConnected}},
	}
}

// writeClaims renders every fixture before it creates the new directory, so a
// failure leaves no partial evidence set and an existing directory is refused.
func writeClaims(dir string) error {
	fixtures := claimsPreviewFixtures()
	outputs := make([][]byte, len(fixtures))
	for i, f := range fixtures {
		f.page.Preview, f.page.Project = true, "dev-env"
		body, _, err := portal.RenderClaims(f.page, 896<<10)
		if err != nil {
			return err
		}
		outputs[i] = body
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return fmt.Errorf("create new preview directory: %w", err)
	}
	for i, f := range fixtures {
		file, err := os.OpenFile(filepath.Join(dir, f.name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(outputs[i])
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
