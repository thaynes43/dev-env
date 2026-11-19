package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/claudeauth"
	"github.com/thaynes43/dev-env/internal/codexauth"
)

const historyBootID = "0123456789ab"

type claudeHistoryRig struct {
	*continueRig
	now     time.Time
	created time.Time
}

func newClaudeHistoryRig(t *testing.T) *claudeHistoryRig {
	t.Helper()
	r := &claudeHistoryRig{continueRig: newContinueRig(t, protocol.ModeRemote, true, false),
		now: loginNow.Add(time.Hour), created: loginNow.Add(time.Hour + time.Second)}
	writeJSON(t, r.s.statePath(bootFile), bootRecord{BootID: historyBootID})
	writeJSON(t, r.s.statePath(launchFile), Launch{
		BootID: historyBootID, CreatedAt: r.created, ConversationID: continueConv, TUI: true,
	})
	pid := os.Getpid()
	writeJSON(t, r.s.statePath(pidFile), agentPid{Pid: pid, Start: procStartTime(pid)})
	r.restartDaemon()
	return r
}

func (r *claudeHistoryRig) restartDaemon() {
	r.restart()
	r.d.Now = func() time.Time { return r.now }
}

func (r *claudeHistoryRig) apply(generation uint64) {
	r.t.Helper()
	publish(r.t, r.live, projAccess(generation, 9*time.Hour))
	r.d.pollClaude(context.Background())
}

func (r *claudeHistoryRig) observeAfter(at time.Time) {
	r.t.Helper()
	r.now = at.Add(time.Second)
	r.d.pollClaude(context.Background())
}

func (r *claudeHistoryRig) history() (claudeAuthHistory, []byte) {
	r.t.Helper()
	h, raw, err := readClaudeAuthHistory(r.s)
	if err != nil {
		r.t.Fatal(err)
	}
	return h, raw
}

func history401(at time.Time, sidechain bool) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"isSidechain":%t,"isApiErrorMessage":true,"apiErrorStatus":401,"message":{"content":"synthetic private text"}}`,
		at.Format(time.RFC3339Nano), sidechain)
}

func TestClaudeAuthHistoryMainChain401(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	at := r.created.Add(5*time.Second + 123*time.Millisecond)
	r.transcript(history401(at, false))
	r.observeAfter(at)
	h, _ := r.history()
	g := h.Generations[0]
	wantLaunch := claudeAuthLaunch{BootID: historyBootID, CreatedAt: r.created, Source: claudeSourceProjection}
	if h.Launch == nil || *h.Launch != wantLaunch || g.FirstRejectionLaunch == nil || *g.FirstRejectionLaunch != wantLaunch {
		t.Fatalf("launch = %+v, first rejection launch = %+v", h.Launch, g.FirstRejectionLaunch)
	}
	if !g.MainChain || g.Sidechain || g.RejectionCount != 1 || g.ErrorClass != "authentication" ||
		!g.FirstRejectedAt.Equal(at) || !g.LastRejectedAt.Equal(at) || !g.LastAcceptedBeforeFirstRejectionAt.IsZero() ||
		h.ObservationTiming != "assistant-transcript-proxy" || !h.ClockSkewUnknown {
		t.Fatalf("history = %+v, generation = %+v", h, g)
	}
}

func TestClaudeAuthHistorySidechain401SurvivesParentSuccess(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	refused := r.created.Add(5 * time.Second)
	accepted := refused.Add(time.Second)
	r.transcript(history401(refused, true), acceptedTurn(accepted))
	r.observeAfter(accepted)
	h, _ := r.history()
	g := h.Generations[0]
	if g.MainChain || !g.Sidechain || g.RejectionCount != 1 ||
		!g.FirstRejectedAt.Equal(refused) || !g.LastAcceptedAt.Equal(accepted) || !g.LastAcceptedBeforeFirstRejectionAt.IsZero() {
		t.Fatalf("sidechain history = %+v", g)
	}
	if _, err := os.Lstat(r.s.statePath(rejectionFile)); !errors.Is(err, os.ErrNotExist) || r.continues() != 0 {
		t.Fatal("diagnostic evidence changed the live rejection marker or continue behavior")
	}
}

func TestClaudeAuthHistoryAcceptedBeforeFirstRejection(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	accepted := r.created.Add(5*time.Second + 123*time.Microsecond)
	r.transcript(acceptedTurn(accepted.Add(-time.Second)), acceptedTurn(accepted))
	r.observeAfter(accepted)
	refused := r.now.Add(5 * time.Second)
	parentSuccess := refused.Add(time.Second)
	// The accepted response before the first 401 has already left the tail.
	// Later success and unproven assistant/user entries cannot replace it.
	r.transcript(healthyTurn(refused.Add(-2*time.Second)), userTurn(refused.Add(-time.Second)),
		history401(refused, false), acceptedTurn(parentSuccess), history401(refused.Add(2*time.Second), false))
	r.observeAfter(refused.Add(2 * time.Second))
	h, _ := r.history()
	g := h.Generations[0]
	if !g.LastAcceptedBeforeFirstRejectionAt.Equal(accepted) || !g.LastAcceptedAt.Equal(parentSuccess) ||
		!g.FirstRejectedAt.Equal(refused) || !g.LastRejectedAt.Equal(refused.Add(2*time.Second)) || g.RejectionCount != 2 {
		t.Fatalf("wrong rejection bracket: %+v", g)
	}
}

func TestClaudeAuthHistoryReceiptBeforeFailedCredentialWrite(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	_, before := r.history()
	r.now = r.created.Add(time.Minute)
	publish(t, r.live, projAccess(2, 9*time.Hour))
	receiptPath := r.s.statePath("claude-access-generation.json")
	err := syncClaudeAccess(r.s, r.now, r.d.now, func(path string, _ []byte) error {
		if path != filepath.Join(r.s.ClaudeConfigDir, credentialsFileName) {
			t.Fatalf("unexpected write path: %q", path)
		}
		var receipt claudeReceipt
		if readJSONFile(receiptPath, &receipt) != nil || receipt.Generation != 2 {
			t.Fatal("legacy receipt must be committed before the attempted credential write")
		}
		return ErrClaudeAccess
	})
	if !errors.Is(err, ErrClaudeAccess) {
		t.Fatalf("write failure = %v", err)
	}
	_, after := r.history()
	if !bytes.Equal(before, after) {
		t.Fatal("a receipt for a failed write changed applied history")
	}
	start := r.now
	err = syncClaudeAccess(r.s, start, r.d.now, func(path string, raw []byte) error {
		if err := writeClaudePrivateAtomic(path, raw); err != nil {
			return err
		}
		r.now = r.now.Add(3 * time.Second)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	h, _ := r.history()
	g := h.Generations[1]
	if g.AppliedGeneration != 2 || !g.CredentialWrittenAt.Equal(r.now) || !g.ConfirmedAt.Equal(r.now) || g.CredentialWrittenAt.Equal(start) {
		t.Fatalf("successful write was not dated at completion: %+v", g)
	}
}

func TestClaudeAuthHistoryNoWriteTimeFromLegacyReceipt(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	if err := os.Remove(r.s.statePath(claudeAuthHistoryFile)); err != nil {
		t.Fatal(err)
	}
	r.now = r.created.Add(time.Minute)
	r.restartDaemon()
	r.apply(1)
	h, _ := r.history()
	g := h.Generations[0]
	if g.AppliedGeneration != 1 || !g.CredentialWrittenAt.IsZero() || !g.ConfirmedAt.Equal(r.now) {
		t.Fatalf("a no-op sync invented a write time: %+v", g)
	}
}

func TestClaudeAuthHistoryRetainsResolvedGeneration(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	firstWrite := r.now
	if err := os.Chtimes(r.s.statePath("claude-access-generation.json"), firstWrite, firstWrite); err != nil {
		t.Fatal(err)
	}
	at := r.created.Add(5 * time.Second)
	r.transcript(history401(at, false))
	r.observeAfter(at)
	if _, err := os.Stat(r.s.statePath(rejectionFile)); err != nil {
		t.Fatalf("legacy rejection was not set: %v", err)
	}
	r.now = at.Add(time.Minute)
	r.apply(4)
	if _, err := os.Stat(r.s.statePath(rejectionFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy rejection did not resolve after the new generation")
	}
	h, _ := r.history()
	old, current := h.Generations[0], h.Generations[1]
	if old.AppliedGeneration != 1 || old.RejectionCount != 1 || !old.FirstRejectedAt.Equal(at) || !old.CredentialWrittenAt.Equal(firstWrite) ||
		current.AppliedGeneration != 4 || !current.CredentialWrittenAt.Equal(r.now) || current.RejectionCount != 0 {
		t.Fatalf("new generation did not preserve prior evidence: %+v", h.Generations)
	}
}

func TestClaudeAuthHistoryDeduplicatesReplayAcrossPollsAndRestart(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	at := r.created.Add(5 * time.Second)
	r.transcript(history401(at, false), history401(at, true), history401(at, false))
	r.observeAfter(at)
	h, raw := r.history()
	if g := h.Generations[0]; g.RejectionCount != 2 || !g.Sidechain || !g.MainChain {
		t.Fatalf("same timestamp on the two chains = %+v", g)
	}
	r.observeAfter(at)
	r.restartDaemon()
	r.appendTurn(history401(at, true))
	r.observeAfter(at)
	_, replayed := r.history()
	if !bytes.Equal(raw, replayed) {
		t.Fatal("reading the same events after restart changed the recorded observations")
	}
	older := at.Add(-time.Second)
	r.appendTurn(history401(older, true))
	r.observeAfter(at)
	h, _ = r.history()
	if g := h.Generations[0]; g.RejectionCount != 3 || !g.FirstRejectedAt.Equal(older) || !g.LastRejectedAt.Equal(at) {
		t.Fatalf("delayed unique event = %+v", g)
	}
}

func TestClaudeAuthHistoryGenerationAndCountBounds(t *testing.T) {
	r := newClaudeHistoryRig(t)
	for generation := uint64(1); generation <= 4; generation++ {
		r.apply(generation)
		var lines []string
		at := r.now.Add(time.Second)
		for range claudeAuthCountLimit - 1 {
			at = at.Add(time.Second)
			lines = append(lines, history401(at, true))
		}
		r.transcript(lines...)
		r.observeAfter(at)
	}
	h, raw := r.history()
	got := make([]uint64, 0, len(h.Generations))
	for _, g := range h.Generations {
		got = append(got, g.AppliedGeneration)
		if g.RejectionCount != claudeAuthCountLimit-1 {
			t.Fatalf("generation %d count = %d", g.AppliedGeneration, g.RejectionCount)
		}
	}
	if !slices.Equal(got, []uint64{2, 3, 4}) || len(raw) > claudeAuthHistorySize {
		t.Fatalf("generations = %v, history bytes = %d", got, len(raw))
	}
	at := r.now.Add(time.Second)
	r.appendTurn(history401(at, false), history401(at.Add(time.Second), true))
	r.observeAfter(at.Add(time.Second))
	h, _ = r.history()
	if g := h.Generations[2]; g.RejectionCount != claudeAuthCountLimit ||
		len(g.SeenRejections) != 0 || !g.LastRejectedAt.Equal(at.Add(time.Second)) {
		t.Fatalf("saturated generation = %+v", g)
	}
}

func TestClaudeAuthHistoryOnlyWritesSanitizedFields(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	accepted := r.created.Add(5 * time.Second)
	refused := accepted.Add(time.Second)
	r.transcript(acceptedTurn(accepted), history401(refused, true), history401(refused, false))
	r.observeAfter(refused)
	_, raw := r.history()
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	assertHistoryKeys(t, record, "clockSkewUnknown", "generations", "launch", "observationTiming", "version")
	assertHistoryKeys(t, record["launch"], "bootId", "createdAt", "source")
	g := record["generations"].([]any)[0].(map[string]any)
	assertHistoryKeys(t, g, "appliedGeneration", "confirmedAt", "credentialWrittenAt", "errorClass",
		"firstRejectedAt", "firstRejectionLaunch", "lastAcceptedAt", "lastAcceptedBeforeFirstRejectionAt",
		"lastRejectedAt", "mainChain", "rejectionCount", "seenRejections", "sidechain")
	assertHistoryKeys(t, g["firstRejectionLaunch"], "bootId", "createdAt", "source")
	keys := g["seenRejections"].([]any)
	assertHistoryKeys(t, keys[0], "at", "sidechain")
	assertHistoryKeys(t, keys[1], "at")
	var receipt claudeReceipt
	if err := readJSONFile(r.s.statePath("claude-access-generation.json"), &receipt); err != nil || receipt.Fingerprint == "" {
		t.Fatal("missing legacy receipt fingerprint")
	}
	for _, private := range []string{receipt.Fingerprint, projToken(1), projAccount, projOrg, projMCP, continueConv, "synthetic private text"} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatal("the history contains fixture credential, identity or transcript data")
		}
	}
	fi, err := os.Stat(r.s.statePath(claudeAuthHistoryFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatal("history must be private")
	}
}

func assertHistoryKeys(t *testing.T, v any, want ...string) {
	t.Helper()
	obj, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected an object, got %T", v)
	}
	got := make([]string, 0, len(obj))
	for k := range obj {
		got = append(got, k)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("history fields = %v, want %v", got, want)
	}
}

func TestClaudeAuthHistorySourceKinds(t *testing.T) {
	for _, tc := range []struct {
		mode string
		live string
		want claudeAuthSource
	}{
		{protocol.ModeRemote, "/projection", claudeSourceProjection},
		{protocol.ModeRemote, "", claudeSourcePod},
		{protocol.ModeLocal, "/projection", claudeSourceStatic},
		{protocol.ModeTask, "", claudeSourceStatic},
		{"", "", claudeSourceUnknown},
	} {
		if got := claudeHistoryLaunch(Settings{ClaudeLiveFile: tc.live}, tc.mode, Launch{}).Source; got != tc.want {
			t.Errorf("mode %q, projected %t: source %q, want %q", tc.mode, tc.live != "", got, tc.want)
		}
	}

	r := newClaudeHistoryRig(t)
	r.session.Mode = protocol.ModeLocal
	r.restartDaemon()
	r.apply(1)
	at := r.created.Add(5 * time.Second)
	r.transcript(history401(at, false))
	r.observeAfter(at)
	h, _ := r.history()
	if h.Launch == nil || h.Launch.Source != claudeSourceStatic || h.Generations[0].RejectionCount != 0 {
		t.Fatal("a local static launch was attributed to the projection")
	}
}

func TestClaudeAuthLegacyWireGoldens(t *testing.T) {
	golden := func(name string) []byte {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("testdata", "claude_auth_wire", name))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	access, login, receipt := golden("access.json"), golden("login.json"), golden("claude-access-generation.json")
	a, err := claudeauth.Decode(access, loginNow)
	if err != nil || a.Generation != 7 || a.AccessToken != "fixture-projected-access" {
		t.Fatal("legacy projection fixture did not decode")
	}
	if encoded, err := claudeauth.Encode(a, loginNow); err != nil || !bytes.Equal(access, encoded) {
		t.Fatal("legacy access projection bytes changed")
	}
	lapse, err := claudeauth.DecodeLogin(login)
	if err != nil || lapse.Format(time.RFC3339) != "2030-01-02T03:04:05Z" {
		t.Fatal("legacy login fixture did not decode")
	}
	if encoded, err := claudeauth.EncodeLogin(lapse); err != nil || !bytes.Equal(login, encoded) {
		t.Fatal("legacy login projection bytes changed")
	}
	s := projHome(t, filepath.Join(t.TempDir(), "projection", claudeauth.LiveKey))
	writeRaw(t, s.ClaudeLiveFile, access)
	if err := SyncClaudeAccess(s, loginNow); err != nil {
		t.Fatal(err)
	}
	path := s.statePath("claude-access-generation.json")
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, receipt) {
		t.Fatal("legacy receipt bytes changed")
	}
	var decoded claudeReceipt
	if codexauth.DecodeStrict(receipt, &decoded) != nil || decoded.Generation != 7 {
		t.Fatal("legacy receipt fixture did not decode")
	}
	writeRaw(t, path, receipt)
	if err := SyncClaudeAccess(s, loginNow); err != nil {
		t.Fatal("legacy receipt was not accepted by the sync path")
	}

	unknownMember := func(raw []byte) []byte {
		return append(bytes.Clone(raw[:len(raw)-1]), []byte(`,"diagnostic":true}`)...)
	}
	if _, err := claudeauth.Decode(unknownMember(access), loginNow); !errors.Is(err, claudeauth.ErrInvalid) {
		t.Fatal("legacy access projection stopped refusing unknown members")
	}
	if _, err := claudeauth.DecodeLogin(unknownMember(login)); !errors.Is(err, claudeauth.ErrInvalid) {
		t.Fatal("legacy login projection stopped refusing unknown members")
	}
	writeRaw(t, path, unknownMember(receipt))
	if err := SyncClaudeAccess(s, loginNow); !errors.Is(err, ErrClaudeAccess) {
		t.Fatal("legacy receipt stopped refusing unknown members")
	}
}
