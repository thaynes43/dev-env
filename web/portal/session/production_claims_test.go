package session

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// config/claims.json absent serves no Claims route; present and enabled with
// an operator credential serves it; a present but invalid file fails startup.
func TestProductionClaimsWiring(t *testing.T) {
	for _, name := range []string{"PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGSSLCRL", "PGSSLCRLDIR"} {
		t.Setenv(name, "")
	}
	serve := func(root string) *httptest.ResponseRecorder {
		t.Helper()
		rt, err := LoadProduction(root)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		defer rt.Close()
		w := httptest.NewRecorder()
		rt.Handler.ServeHTTP(w, portalRequest(http.MethodGet, ClaimsPath, ""))
		return w
	}
	absent := serve(writeRuntimeDir(t, "SECRET-CANARY"))
	disabledRoot := writeRuntimeDir(t, "SECRET-CANARY")
	if err := os.WriteFile(filepath.Join(disabledRoot, "config/claims.json"), []byte(`{"enabled":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	disabled := serve(disabledRoot)
	enabled := writeRuntimeDir(t, "SECRET-CANARY")
	if err := os.WriteFile(filepath.Join(enabled, "config/claims.json"), []byte(`{"enabled":true,"project":"dev-env"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProduction(enabled); err == nil {
		t.Fatal("claims without an operator credential connected")
	}
	token := fixtureOperatorToken(apiv1.TokenAudience, time.Now().Add(time.Hour))
	if err := os.WriteFile(filepath.Join(enabled, "secrets/OPERATOR_TOKEN"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	present := serve(enabled)
	if present.Code != http.StatusSeeOther || present.Header().Get("Location") != "/auth/login" {
		t.Fatalf("configured Claims route answered %d %q", present.Code, present.Header().Get("Location"))
	}
	for name, w := range map[string]*httptest.ResponseRecorder{"absent": absent, "disabled": disabled} {
		if w.Code != http.StatusNotFound || w.Header().Get("Location") != "" {
			t.Fatalf("%s config served a Claims route: %d %q", name, w.Code, w.Header().Get("Location"))
		}
	}
	for _, body := range []string{`{"enabled":true}`, `{"enabled":true,"project":"other"}`, `{"enabled":true,"project":"dev-env","extra":1}`, `{"Enabled":true}`, `[]`} {
		root := writeRuntimeDir(t, "SECRET-CANARY")
		if err := os.WriteFile(filepath.Join(root, "secrets/OPERATOR_TOKEN"), []byte(token), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "config/claims.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadProduction(root); err == nil {
			t.Fatalf("accepted claims config %s", body)
		}
	}
}
