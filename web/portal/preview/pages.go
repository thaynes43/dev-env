package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/web/portal"
)

// The input is deliberately fixed and synthetic; this preview never reads a
// snapshot, creates a session, contacts a source, or opens a listener.
const syntheticPages = `[{"fingerprint":"synthetic-alert-01","labels":{"alertname":"Synthetic sample alert","severity":"warning","service":"fixture-only"},"annotations":{"summary":"Example alert for a local preview","description":"This is synthetic fixture content. No live alert data is shown."},"status":{"state":"active"},"startsAt":"2026-10-28T12:00:00Z","updatedAt":"2026-10-28T12:01:00Z"}]`

func writePages(dir string) error {
	available, err := pages.ParseAlertmanager([]byte(syntheticPages))
	if err != nil {
		return err
	}
	empty, err := pages.ParseAlertmanager([]byte(`[]`))
	if err != nil {
		return err
	}
	unavailable := pages.UnavailableAlertmanager()
	for _, s := range []*pages.AlertmanagerSnapshot{&available, &empty, &unavailable} {
		s.Coverage = "Fixture preview · synthetic data. Current alerts only; notification and delivery history, triage disposition and recurrence are Unknown."
		if s.Availability == "Available" {
			s.ObservedAt = "2026-10-28T12:02:00Z"
			s.Source = "Synthetic fixture"
		}
	}
	fixtures := []struct {
		name     string
		snapshot pages.AlertmanagerSnapshot
		disabled bool
	}{{"pages-available.html", available, false}, {"pages-empty.html", empty, false}, {"pages-unavailable.html", unavailable, false}, {"pages-disabled.html", unavailable, true}}
	rendered := make([][]byte, 0, len(fixtures))
	for _, f := range fixtures {
		body, err := portal.RenderPages(f.snapshot, f.disabled, 896<<10)
		if err != nil {
			return err
		}
		rendered = append(rendered, body)
	}
	// Refuse an existing directory, file or symlink instead of replacing data.
	if err := os.Mkdir(dir, 0700); err != nil {
		return fmt.Errorf("create new preview directory: %w", err)
	}
	for i, f := range fixtures {
		file, err := os.OpenFile(filepath.Join(dir, f.name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(rendered[i])
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
