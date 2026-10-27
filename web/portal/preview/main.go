// Command preview renders the existing finite synthetic Attention fixture
// through the Go handler, and the synthetic Sessions and Tasks fixtures through
// the live product decoders and page builders. It opens no listener and
// contacts no service.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
	"github.com/thaynes43/dev-env/web/portal/session"
)

type fixture struct {
	Version   int              `json:"version"`
	AsOf      time.Time        `json:"asOf"`
	ReadState portal.ReadState `json:"readState"`
	Items     []fixtureItem    `json:"items"`
}
type fixtureItem struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Group       portal.Group `json:"group"`
	WorkPackage int          `json:"canonicalWorkPackage"`
	Owner       *string      `json:"owner"`
	NextActor   *string      `json:"nextActor"`
	NextAction  *string      `json:"nextAction"`
	UpdatedAt   *time.Time   `json:"sourceUpdatedAt"`
	VerifiedAt  *time.Time   `json:"verifiedAt"`
	WorkerState *string      `json:"workerState"`
	TaskOutcome *string      `json:"taskOutcome"`
	ReviewHead  *string      `json:"reviewHead"`
}

type offline struct{ snapshot portal.Snapshot }

var scope = portal.Resource{Project: "synthetic", Environment: "offline"}
var principal = portal.Principal{Issuer: "https://identity.example/fixture-only/", Subject: "synthetic-reader"}

func (o offline) Authenticate(*http.Request) (portal.Principal, error) { return principal, nil }
func (o offline) Authorize(_ context.Context, p portal.Principal, r portal.Resource) error {
	if p != principal || r.Project != scope.Project || r.Environment != scope.Environment {
		return portal.ErrDenied
	}
	if r.Task == "" {
		return nil
	}
	for _, item := range o.snapshot.Items {
		if item.Resource == r {
			return nil
		}
	}
	return portal.ErrDenied
}
func (o offline) Read(_ context.Context, r portal.Resource) (portal.Snapshot, error) {
	if r != scope {
		return portal.Snapshot{}, portal.ErrDenied
	}
	return o.snapshot, nil
}

func value(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func instant(v *time.Time) time.Time {
	if v == nil {
		return time.Time{}
	}
	return *v
}

func render(source io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(source, 64*1024+1))
	if err != nil || len(raw) > 64*1024 {
		return nil, portal.ErrUnavailable
	}
	var f fixture
	// Reject malformed/oversized fixture envelopes before any output is opened.
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&f); err != nil {
		return nil, portal.ErrUnavailable
	}
	if err = d.Decode(new(any)); !errors.Is(err, io.EOF) || f.Version != 1 || f.AsOf.IsZero() {
		return nil, portal.ErrUnavailable
	}
	// The fixture's readState/asOf explicitly witness this synthetic listing;
	// individual rows retain their own missing or older verification evidence.
	s := portal.Snapshot{Resource: scope, State: f.ReadState, UpdatedAt: f.AsOf, VerifiedAt: f.AsOf}
	for _, item := range f.Items {
		r := scope
		r.Task = item.ID
		s.Items = append(s.Items, portal.Item{Resource: r, WorkPackage: item.WorkPackage, Group: item.Group, Title: item.Title, Owner: value(item.Owner), NextActor: value(item.NextActor), NextAction: value(item.NextAction), UpdatedAt: instant(item.UpdatedAt), VerifiedAt: instant(item.VerifiedAt), WorkerState: value(item.WorkerState), TaskOutcome: value(item.TaskOutcome), ReviewHead: value(item.ReviewHead)})
	}
	o := offline{s}
	h := portal.NewHandler(portal.Dependencies{Authenticator: o, Authorizer: o, Reader: o, FreshFor: 5 * time.Minute, Now: func() time.Time { return f.AsOf }})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/portal/projects/synthetic/environments/offline/attention", nil))
	if w.Code != 200 {
		return nil, portal.ErrUnavailable
	}
	return w.Body.Bytes(), nil
}

func main() {
	output := flag.String("output", "", "output HTML file; run from repository root")
	scenarios := flag.String("scenarios", "", "directory for offline presentation evidence pages")
	product := flag.String("product", "", "directory for the Sessions and Tasks fixture previews; run from repository root")
	flag.Parse()
	if *output == "" && *scenarios == "" && *product == "" {
		fmt.Fprintln(os.Stderr, "--output, --scenarios or --product is required")
		os.Exit(1)
	}
	if *product != "" {
		if err := writeProduct(*product); err != nil {
			fmt.Fprintf(os.Stderr, "Synthetic product previews unavailable: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Wrote synthetic Sessions and Tasks fixture previews.")
	}
	if *scenarios != "" {
		if err := writeScenarios(*scenarios); err != nil {
			fmt.Fprintf(os.Stderr, "Synthetic scenarios unavailable; no complete evidence set written: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Wrote offline synthetic Attention scenarios.")
	}
	if *output == "" {
		return
	}
	f, err := os.Open("scripts/trials/portal_attention/fixtures.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Synthetic fixture unavailable; no preview written.")
		os.Exit(1)
	}
	data, renderErr := render(f)
	closeErr := f.Close()
	if renderErr != nil || closeErr != nil {
		fmt.Fprintln(os.Stderr, "Synthetic fixture invalid or unavailable; no preview written.")
		os.Exit(1)
	}
	if err = os.WriteFile(*output, data, 0600); err != nil {
		fmt.Fprintln(os.Stderr, "Preview output unavailable.")
		os.Exit(1)
	}
	fmt.Println("Wrote offline synthetic Attention preview.")
}

// writeProduct renders sessions.html, tasks.html, sessions-send.html (send
// route configured) and sessions-history.html (one session selected, with a
// fixture conversation) from the committed synthetic fixtures, and one receipt
// page per transport status. Every page carries the Fixture preview label.
func writeProduct(dir string) error {
	for _, page := range []struct {
		fixture, out string
		render       func(io.Reader) ([]byte, error)
	}{
		{"scripts/trials/portal_product/sessions.json", "sessions.html", session.RenderSessionsFixture},
		{"scripts/trials/portal_product/tasks.json", "tasks.html", session.RenderTasksFixture},
		{"scripts/trials/portal_product/sessions.json", "sessions-send.html", session.RenderSessionsSendFixture},
		{"scripts/trials/portal_product/sessions.json", "sessions-history.html", session.RenderSessionsHistoryFixture},
		{"scripts/trials/portal_product/sessions.json", "sessions-launch.html", session.RenderSessionsLaunchFixture},
	} {
		f, err := os.Open(page.fixture)
		if err != nil {
			return err
		}
		data, renderErr := page.render(f)
		closeErr := f.Close()
		if renderErr != nil || closeErr != nil {
			return errors.Join(renderErr, closeErr)
		}
		if err := os.WriteFile(dir+"/"+page.out, data, 0o600); err != nil {
			return err
		}
	}
	receipts, err := session.RenderReceiptFixtures(time.Date(2026, 10, 26, 15, 0, 5, 0, time.UTC))
	if err != nil {
		return err
	}
	for status, data := range receipts {
		if err := os.WriteFile(dir+"/receipt-"+status+".html", data, 0o600); err != nil {
			return err
		}
	}
	results, err := session.RenderLaunchResultFixtures()
	if err != nil {
		return err
	}
	for outcome, data := range results {
		if err := os.WriteFile(dir+"/launch-"+outcome+".html", data, 0o600); err != nil {
			return err
		}
	}
	return nil
}
