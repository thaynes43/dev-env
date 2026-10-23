package main

import (
	"os"
	"strings"
	"testing"
)

func TestExistingFoundationFixtureRendersOffline(t *testing.T) {
	f, err := os.Open("../../../scripts/trials/portal_attention/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	body, err := render(f)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, title := range []string{"Needs you · 1", "Needs worker · 1", "Review returned · 1", "Handoff missing · 1", "Source preview", "Stale evidence", "Freshness unknown", "2026-10-23T14:18:10Z"} {
		if !strings.Contains(s, title) {
			t.Errorf("missing fixture presentation %s", title)
		}
	}
	if strings.Contains(s, "Listing counts") {
		t.Fatal("unexpected listing warning in verified synthetic snapshot")
	}
	if strings.Count(s, "<article ") != 4 || strings.Count(s, "href=") != 4 {
		t.Fatal("fixture rows or fixed source links missing")
	}
	for _, active := range []string{"<script", "<iframe", "<form", "<img"} {
		if strings.Contains(s, active) {
			t.Fatalf("unexpected active content %s", active)
		}
	}
	// A second independent render from the same accepted fixture must match.
	if _, err = f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	again, err := render(f)
	if err != nil || string(again) != s {
		t.Fatal("preview is not repeatable")
	}
}

func TestInvalidFixtureWritesNoPartialPage(t *testing.T) {
	for _, raw := range []string{"", "{", `{"version":1,"asOf":"2026-10-23T16:00:00Z","readState":"verified","items":[],"prompt":"SECRET"}`, `{"version":2}`, strings.Repeat("x", 65537)} {
		body, err := render(strings.NewReader(raw))
		if err == nil || len(body) != 0 {
			t.Fatal("invalid fixture returned partial HTML")
		}
	}
}

func TestOfflineScenariosKeepHonestStates(t *testing.T) {
	dir := t.TempDir()
	if err := writeScenarios(dir); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(dir + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	empty := read("verified-empty.html")
	if !strings.Contains(empty, "Verified empty") || !strings.Contains(empty, ">Needs you · 0</h2>") || !strings.Contains(empty, "No items in this group.") {
		t.Fatal("verified empty groups")
	}
	unknown := read("unknown-listing.html")
	if !strings.Contains(unknown, "Attention unknown") || strings.Contains(unknown, "· 0") || strings.Contains(unknown, "Needs you ·") {
		t.Fatal("unknown listing invented a count")
	}
	stale := read("stale-with-fresh-row.html")
	if !strings.Contains(stale, "Stale evidence. Listing counts are not verified as current;") || !strings.Contains(stale, `<p class="freshness">Verified within snapshot window</p>`) || strings.Contains(stale, "· 0") {
		t.Fatal("fresh row inside stale listing")
	}
	missing := read("fresh-row-unknown-listing.html")
	if !strings.Contains(missing, "Freshness unknown. Listing counts are not verified as current;") || !strings.Contains(missing, `<p class="freshness">Verified within snapshot window</p>`) || strings.Contains(missing, "· 0") {
		t.Fatal("fresh row inside unknown listing")
	}
	future := read("future-listing.html")
	if !strings.Contains(future, "Unknown (untrusted future timestamp:") || strings.Contains(future, "· 0") {
		t.Fatal("future listing")
	}
	hostile := read("hostile.html")
	if strings.Contains(hostile, "<script>") || strings.Contains(hostile, "<img ") || !strings.Contains(hostile, "&lt;script&gt;") || !strings.Contains(hostile, "<dt>Owner</dt><dd>Unknown</dd>") {
		t.Fatal("hostile text")
	}
	absent := read("no-owner.html")
	if !strings.Contains(absent, "<dt>Owner</dt><dd>Unknown</dd>") || !strings.Contains(absent, `<dt>Next action</dt><dd class="next">Unknown</dd>`) || !strings.Contains(absent, "<dt>Next actor</dt><dd>Unknown</dd>") {
		t.Fatal("missing actor")
	}
	const refused = "Attention unavailable. Current task state is Unknown.\n"
	for _, name := range []string{"denied.txt", "unavailable.txt"} {
		if read(name) != refused {
			t.Fatalf("%s body", name)
		}
	}
	populated := read("populated.html")
	if strings.Count(populated, "<article ") != 4 || strings.Contains(populated, "<script") || strings.Contains(populated, "Listing counts") {
		t.Fatal("populated preview")
	}
}
