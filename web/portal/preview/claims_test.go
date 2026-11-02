package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

func TestClaimsPreviewsAreSyntheticAndReadOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "claims")
	if err := writeClaims(dir); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, want string }{
		{"claims-empty.html", "No TaskClaims recorded for dev-env at"},
		{"claims-many.html", "More claims exist. Order is not sorted across pages."},
		{"claims-unknown-owner.html", "Unknown owner"},
		{"claims-refused.html", "The operator refused this read."},
		{"claims-unavailable.html", "Claims could not be read."},
		{"claims-not-connected.html", "Claims are not connected."},
	} {
		raw, err := os.ReadFile(filepath.Join(dir, test.name))
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		if !strings.Contains(body, test.want) || !strings.Contains(body, "Fixture preview · synthetic data") || strings.Contains(body, "<form") || strings.Contains(body, "<script") || strings.Contains(body, "<button") {
			t.Fatalf("unsafe/incomplete preview %s", test.name)
		}
		if strings.Count(body, `href="/portal/projects/dev-env/environments/internal/claims" aria-current="page"`) != 2 {
			t.Fatalf("%s lacks Claims in both navigations", test.name)
		}
		if strings.Contains(body, "thaynes43") {
			t.Fatalf("%s links a real repository", test.name)
		}
	}
	many, err := os.ReadFile(filepath.Join(dir, "claims-many.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(many), "Claim held") != apiv1.PortalTaskClaimsMaxLimit || !strings.Contains(string(many), ">Next page</a>") {
		t.Fatal("the many preview is not one truncated page of 100")
	}
	unknown, err := os.ReadFile(filepath.Join(dir, "claims-unknown-owner.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(unknown), ">Active") != 1 {
		t.Fatal("only the one observed owner may be Active")
	}
	if err := writeClaims(dir); err == nil {
		t.Fatal("existing preview overwritten")
	}
}

func TestClaimsPreviewRecordsFollowTheContract(t *testing.T) {
	for _, f := range claimsPreviewFixtures() {
		if f.page.State != portal.ClaimsEmpty && f.page.State != portal.ClaimsAvailable {
			continue
		}
		if err := apiv1.ValidatePortalTaskClaims(f.page.Read); err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if portal.ClaimsStateOf(f.page.Read, "dev-env", false) != f.page.State {
			t.Fatalf("%s state does not follow from its read", f.name)
		}
	}
}
