// Command preview renders the existing finite synthetic Attention fixture
// through the Go handler. It opens no listener and contacts no service.
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
	s := portal.Snapshot{Resource: scope, State: f.ReadState}
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
	output := flag.String("output", "", "output HTML file (required); run from repository root")
	flag.Parse()
	if *output == "" {
		fmt.Fprintln(os.Stderr, "--output is required")
		os.Exit(1)
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
