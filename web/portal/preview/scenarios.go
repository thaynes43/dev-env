package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

func openFixture() (*os.File, error) {
	for _, path := range []string{
		"scripts/trials/portal_attention/fixtures.json",
		"../../../scripts/trials/portal_attention/fixtures.json",
	} {
		f, err := os.Open(path)
		if err == nil {
			return f, nil
		}
	}
	return nil, portal.ErrUnavailable
}

func serve(deps portal.Dependencies, now time.Time) (int, []byte) {
	deps.FreshFor = 5 * time.Minute
	deps.Now = func() time.Time { return now }
	w := httptest.NewRecorder()
	portal.NewHandler(deps).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/portal/projects/synthetic/environments/offline/attention", nil))
	return w.Code, w.Body.Bytes()
}

func serveOffline(snapshot portal.Snapshot, now time.Time) (int, []byte) {
	o := offline{snapshot}
	return serve(portal.Dependencies{Authenticator: o, Authorizer: o, Reader: o}, now)
}

type refusingAuth struct{}

func (refusingAuth) Authenticate(*http.Request) (portal.Principal, error) { return principal, nil }
func (refusingAuth) Authorize(context.Context, portal.Principal, portal.Resource) error {
	return portal.ErrDenied
}
func (refusingAuth) Read(context.Context, portal.Resource) (portal.Snapshot, error) {
	return portal.Snapshot{}, portal.ErrDenied
}

func evidenceItem(id string, group portal.Group, title string, updated, verified time.Time) portal.Item {
	resource := scope
	resource.Task = id
	return portal.Item{
		Resource: resource, WorkPackage: 178, Group: group, Title: title,
		Owner: "Example owner", NextActor: "Example next actor", NextAction: "Open the canonical record and do only the recorded step",
		TaskOutcome: "Pending", WorkerState: "Waiting", ReviewState: "Not returned",
		AuthorityState: "Unchanged", ReleaseState: "Not released",
		UpdatedAt: updated, VerifiedAt: verified,
	}
}

func writeNamed(dir, name string, body []byte) error {
	return os.WriteFile(filepath.Join(dir, name), body, 0o600)
}

func writeHTML(dir, name string, snapshot portal.Snapshot, now time.Time) error {
	code, body := serveOffline(snapshot, now)
	if code != http.StatusOK {
		return fmt.Errorf("%s status %d", name, code)
	}
	return writeNamed(dir, name, body)
}

// writeScenarios renders the offline presentation matrix through the existing handler.
// The pages are synthetic. They do not contact a tracker, provider, or cluster.
func writeScenarios(dir string) error {
	now := time.Date(2026, 10, 23, 16, 0, 0, 0, time.UTC)
	freshUpdated, freshVerified := now.Add(-2*time.Minute), now.Add(-time.Minute)
	base := portal.Snapshot{Resource: scope, State: portal.Verified, UpdatedAt: freshUpdated, VerifiedAt: freshVerified}

	fixture, err := openFixture()
	if err != nil {
		return err
	}
	populated, err := render(fixture)
	closeErr := fixture.Close()
	if err != nil || closeErr != nil {
		return portal.ErrUnavailable
	}
	if err = writeNamed(dir, "populated.html", populated); err != nil {
		return err
	}

	empty := base
	if err = writeHTML(dir, "verified-empty.html", empty, now); err != nil {
		return err
	}
	unknown := base
	unknown.State = portal.Unknown
	unknown.Items = nil
	if err = writeHTML(dir, "unknown-listing.html", unknown, now); err != nil {
		return err
	}

	stale := base
	stale.UpdatedAt, stale.VerifiedAt = now.Add(-7*time.Minute), now.Add(-6*time.Minute)
	stale.Items = []portal.Item{evidenceItem("fresh-inside-stale", portal.NeedsYou, "A fresh row inside a stale listing", freshUpdated, freshVerified)}
	if err = writeHTML(dir, "stale-with-fresh-row.html", stale, now); err != nil {
		return err
	}

	missing := base
	missing.VerifiedAt = time.Time{}
	missing.Items = []portal.Item{evidenceItem("fresh-inside-unknown", portal.NeedsWorker, "A fresh row inside an unknown listing", freshUpdated, freshVerified)}
	if err = writeHTML(dir, "fresh-row-unknown-listing.html", missing, now); err != nil {
		return err
	}

	future := base
	future.VerifiedAt = now.Add(time.Hour)
	futureItem := evidenceItem("future-row", portal.ReviewReturned, "Future evidence stays untrusted", freshUpdated, now.Add(time.Hour))
	future.Items = []portal.Item{futureItem}
	if err = writeHTML(dir, "future-listing.html", future, now); err != nil {
		return err
	}

	hostileItem := evidenceItem("hostile-text", portal.NeedsYou, `<script>alert("x")</script> `+strings.Repeat("é", 80), freshUpdated, freshVerified)
	hostileItem.Owner = "\u202e"
	hostileItem.NextActor = "\u0301\u3164"
	hostileItem.NextAction = `<img src="https://evil.example/" onerror="steal()">` + strings.Repeat("A", 180)
	hostileItem.WorkerState = "Exited"
	hostileItem.TaskOutcome = "Unverified"
	hostileItem.ReviewState = "Findings returned"
	hostileItem.ReviewHead = strings.Repeat("a", 40)
	hostile := base
	hostile.Items = []portal.Item{hostileItem}
	if err = writeHTML(dir, "hostile.html", hostile, now); err != nil {
		return err
	}

	absent := evidenceItem("no-owner", portal.HandoffMissing, "No owner or next action was supplied", freshUpdated, freshVerified)
	absent.Owner, absent.NextActor, absent.NextAction = "", "", ""
	noOwner := base
	noOwner.Items = []portal.Item{absent}
	if err = writeHTML(dir, "no-owner.html", noOwner, now); err != nil {
		return err
	}

	refused := "Attention unavailable. Current task state is Unknown.\n"
	auth := refusingAuth{}
	code, body := serve(portal.Dependencies{Authenticator: auth, Authorizer: auth, Reader: auth}, now)
	if code != http.StatusNotFound || string(body) != refused {
		return fmt.Errorf("denied scenario status %d", code)
	}
	if err = writeNamed(dir, "denied.txt", body); err != nil {
		return err
	}
	reader := portal.UnavailableReader{}
	holder := offline{base}
	code, body = serve(portal.Dependencies{Authenticator: holder, Authorizer: holder, Reader: reader}, now)
	if code != http.StatusServiceUnavailable || string(body) != refused {
		return fmt.Errorf("unavailable scenario status %d", code)
	}
	return writeNamed(dir, "unavailable.txt", body)
}
