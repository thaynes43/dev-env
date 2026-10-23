package apiv1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCapturedUsageLegacyJSONOmitsTheField(t *testing.T) {
	var session Session
	if err := json.Unmarshal([]byte(`{"name":"parent","phase":"Running","createdAt":"2026-10-23T01:00:00Z"}`), &session); err != nil {
		t.Fatal(err)
	}
	if session.CapturedUsage != nil {
		t.Fatal("legacy session decoded a receipt view")
	}
	raw, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "capturedUsage") || strings.Contains(string(raw), "usageCaptureParentUID") {
		t.Fatalf("legacy marshal added capture fields: %s", raw)
	}
}
