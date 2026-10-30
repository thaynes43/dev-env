package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/web/portal"
	"github.com/thaynes43/dev-env/web/portal/session"
)

type parityOrders struct{ observation pages.OrderObservation }

func (f parityOrders) FindOrder(_ context.Context, _ pages.AlertIdentity) (pages.OrderObservation, error) {
	return f.observation, nil
}

type parityClaims struct{ observation pages.ClaimObservation }

func (f parityClaims) FindClaim(_ context.Context, _ pages.AlertIdentity, _ pages.OrderReference) (pages.ClaimObservation, error) {
	return f.observation, nil
}

const responseAlertFixture = `[{"fingerprint":"synthetic-fingerprint","labels":{"alertname":"Synthetic alert","severity":"warning"},"status":{"state":"active"},"startsAt":"2026-10-30T11:00:00Z"}]`

func TestPagesMCPPortalResponseParity(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ state, status string }{
		{"claimed", "claimed"}, {"responding", "claimed"}, {"needs-user", "escalated"}, {"resolved", "done"}, {"Unknown", "failed"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			snapshot, err := pages.ParseAlertmanager([]byte(responseAlertFixture))
			if err != nil {
				t.Fatal(err)
			}
			identity := pages.AlertIdentity{Fingerprint: snapshot.Alerts[0].Fingerprint, StartsAt: snapshot.Alerts[0].StartsAt}
			order := pages.OrderReference{Key: "rem-responder-0123abcd", Created: strconv.FormatInt(at.Add(-time.Hour).Unix(), 10)}
			sources := pages.ResponseSources{
				Orders: parityOrders{pages.OrderObservation{
					SourceObservation: pages.SourceObservation{Available: true, Source: "Synthetic V1 order", ObservedAt: at},
					Order:             &pages.ResponderOrder{Identity: identity, Order: order, Status: tc.status, Updated: strconv.FormatInt(at.Add(-time.Minute).Unix(), 10)},
				}},
				Claims: parityClaims{pages.ClaimObservation{
					SourceObservation: pages.SourceObservation{Available: true, Source: "Synthetic canonical claim", ObservedAt: at},
					Claim:             &pages.ClaimEvidence{Identity: identity, Order: order, Reference: pages.CanonicalClaimReference{Kind: "OpenProject", ID: "313"}, Owner: "synthetic-owner", State: tc.state, StateAt: at.Add(-time.Minute), EvidenceURL: "https://example.test/work_packages/313"},
				}},
				MaxAge: 5 * time.Minute,
			}
			backend := &fakePagesBackend{raw: []byte(responseAlertFixture)}
			mcp := &pagesMCPSession{ctx: context.Background(), config: pagesMCPConfig{Alertmanager: true}, backend: backend, responses: sources, now: func() time.Time { return at }}
			view := session.PagesWorkspaceWithResponses(context.Background(), snapshot, false, sources, at)
			ui := view.Groups[0].Alerts[0].Response
			var list pagesMCPResult
			if err := json.Unmarshal([]byte(responseText(t, callPages(mcp, "pages_list", `{"source":"alertmanager"}`))), &list); err != nil || len(list.Items) != 1 {
				t.Fatal("list", list, err)
			}
			var get pagesMCPResult
			args := `{"source":"alertmanager","key":"synthetic-fingerprint","expectedRevision":"` + list.Revision + `"}`
			if err := json.Unmarshal([]byte(responseText(t, callPages(mcp, "pages_get", args))), &get); err != nil || len(get.Items) != 1 || get.ChangedSinceList || get.Revision != list.Revision {
				t.Fatal("get", get, err)
			}
			if ui == nil || !reflect.DeepEqual(ui, list.Items[0].Response) || !reflect.DeepEqual(ui, get.Items[0].Response) || ui.Identity != identity || ui.State != tc.state {
				t.Fatalf("portal/MCP identity or evidence drift: ui=%+v list=%+v get=%+v", ui, list.Items[0].Response, get.Items[0].Response)
			}
			body, err := portal.RenderPages(view, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{identity.Fingerprint, `datetime="` + identity.StartsAt + `"`, ui.Order.Key, ui.Order.Created, ui.Owner, ui.Claim.Kind, ui.Claim.ID, ui.State, ui.OrderSource, ui.ClaimSource, `<fieldset disabled>`, portal.PagesControlNotice} {
				if !strings.Contains(string(body), want) {
					t.Fatalf("portal omitted shared evidence %q", want)
				}
			}
			if tc.state != "Unknown" && (!strings.Contains(string(body), `href="`+ui.EvidenceURL+`"`) || !strings.Contains(string(body), `datetime="`+ui.StateAt+`"`)) {
				t.Fatal("portal omitted the safe evidence link or state timestamp")
			}
			if view.Controls.Store != portal.PagesStoreDisconnected || view.Controls.DocumentLoaded || strings.Contains(string(body), "<form") {
				t.Fatal("response projection enabled controls")
			}
			// A refire reuses the legacy snapshot selection key but changes the
			// exact incident identity and revision. Old evidence cannot follow it.
			backend.raw = []byte(strings.ReplaceAll(responseAlertFixture, "11:00:00Z", "11:30:00Z"))
			var refire pagesMCPResult
			if err := json.Unmarshal([]byte(responseText(t, callPages(mcp, "pages_get", args))), &refire); err != nil || len(refire.Items) != 1 || !refire.ChangedSinceList || refire.Items[0].Response.Identity == identity || refire.Items[0].Response.State != "Unknown" {
				t.Fatal("refire inherited the previous start's evidence", refire, err)
			}
		})
	}
}

func TestPagesProductionResponseSourcesStayUnconfigured(t *testing.T) {
	snapshot, err := pages.ParseAlertmanager([]byte(responseAlertFixture))
	if err != nil {
		t.Fatal(err)
	}
	view := session.PagesWorkspace(snapshot, false)
	ui := view.Groups[0].Alerts[0].Response
	// This has the same zero-value response wiring as runPagesMCP: no config
	// option installs an order or canonical-claim reader.
	mcp := &pagesMCPSession{ctx: context.Background(), config: pagesMCPConfig{Alertmanager: true}, backend: &fakePagesBackend{raw: []byte(responseAlertFixture)}, now: func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }}
	var got pagesMCPResult
	if err := json.Unmarshal([]byte(responseText(t, callPages(mcp, "pages_list", `{"source":"alertmanager"}`))), &got); err != nil || len(got.Items) != 1 {
		t.Fatal(got, err)
	}
	if ui == nil || !reflect.DeepEqual(ui, got.Items[0].Response) || ui.State != "Unknown" || ui.Owner != "Unknown" || ui.Order.Key != "Unknown" || ui.Claim.ID != "Unknown" || ui.OrderCondition != "Unconfigured" || ui.ClaimCondition != "Unconfigured" || ui.EvidenceURL != "Unknown" {
		t.Fatal("production fabricated response evidence", ui, got)
	}
	if view.Controls.Store != portal.PagesStoreDisconnected || view.Controls.Applied != portal.PagesAppliedState || view.Controls.DocumentLoaded {
		t.Fatal("production controls connected", view.Controls)
	}
	if _, _, err := (pages.UnconfiguredStore{}).Load(context.Background(), pages.Principal{}); !errors.Is(err, pages.ErrNotConfigured) {
		t.Fatal("production preference store configured", err)
	}
}
