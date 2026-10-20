package keeper

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/claudeauth"
)

// The lapse date is published as its own key (claudeauth.LoginKey) so access.json
// stays exactly what an older agentd's strict decoder accepts.
func TestClaudePublicationCarriesTheLapseDateInItsOwnKey(t *testing.T) {
	p, rc := claudePublicFixture(t, nil)
	acc := syntheticClaudeAccess(5, claudeNow, 8*time.Hour)
	lapse := claudeNow.Add(30 * 24 * time.Hour)
	if err := p.publish(context.Background(), acc, lapse, claudeNow); err != nil {
		t.Fatal(err)
	}
	s := liveClaude(t, p)
	got, err := claudeauth.DecodeLogin(s.Data[claudeauth.LoginKey])
	if err != nil || !got.Equal(lapse) {
		t.Fatalf("lapse document = %v, %v", got, err)
	}
	if _, err := claudeauth.Decode(s.Data[claudeauth.LiveKey], claudeNow); err != nil || bytes.Contains(s.Data[claudeauth.LiveKey], []byte("loginLapsesAt")) {
		t.Fatal("access.json changed shape")
	}

	// The same generation and the same date: nothing is written.
	patches := rc.Patches
	if err := p.publish(context.Background(), acc, lapse, claudeNow.Add(time.Minute)); err != nil || rc.Patches != patches {
		t.Fatalf("an unchanged publication patched (%d -> %d): %v", patches, rc.Patches, err)
	}

	// The same generation and a new date (a keeper that learned it after a
	// restart): only the lapse key and its annotation change.
	before := s.Data[claudeauth.LiveKey]
	later := lapse.Add(48 * time.Hour)
	if err := p.publish(context.Background(), acc, later, claudeNow.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	s = liveClaude(t, p)
	if got, _ := claudeauth.DecodeLogin(s.Data[claudeauth.LoginKey]); !got.Equal(later) || !bytes.Equal(s.Data[claudeauth.LiveKey], before) {
		t.Fatal("same-generation lapse update changed the access document or missed the date")
	}
	if s.Annotations[AnnotationRefreshExpiresAt] != later.Format(time.RFC3339) || s.Annotations[AnnotationWrittenAt] != claudeNow.Format(time.RFC3339) {
		t.Fatalf("annotations after a lapse-only update: %v", s.Annotations)
	}

	// A provider that gives no date removes the key: never a stale or zero date.
	if err := p.publish(context.Background(), acc, time.Time{}, claudeNow.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	s = liveClaude(t, p)
	if _, ok := s.Data[claudeauth.LoginKey]; ok {
		t.Fatal("the lapse key outlived an unknown date")
	}
	if _, ok := s.Annotations[AnnotationRefreshExpiresAt]; ok {
		t.Fatal("the lapse annotation outlived an unknown date")
	}
}

func TestClaudeWorkerPublishesTheLapseWithTheFirstGeneration(t *testing.T) {
	f := newClaudeFix(t)
	rec := syntheticClaudeRecord(1)
	f.seed(t, rec)
	f.tick()
	s := f.liveSecret(t)
	if got, err := claudeauth.DecodeLogin(s.Data[claudeauth.LoginKey]); err != nil || !got.Equal(rec.RefreshExpiresAt) {
		t.Fatalf("lapse key after the first tick: %v %v", got, err)
	}
}
