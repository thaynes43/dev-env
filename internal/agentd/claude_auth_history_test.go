package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
		BootID: historyBootID, CreatedAt: r.created, ConversationID: continueConv, TUI: true, Dir: "/work/repo",
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

func (r *claudeHistoryRig) childTranscript(name string, line string) {
	r.t.Helper()
	path := filepath.Join(r.s.ClaudeConfigDir, "projects", "-work-repo", continueConv, "subagents", name)
	writeRaw(r.t, path, []byte(line+"\n"))
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
		!g.FirstRejection.At.Equal(at) || !g.LastRejection.At.Equal(at) || !g.LastAcceptedBeforeFirstRejection.IsZero() ||
		h.ObservationTiming != "assistant-transcript-proxy" || !h.ClockSkewUnknown {
		t.Fatalf("history = %+v, generation = %+v", h, g)
	}
}

func TestClaudeAuthHistorySidechain401SurvivesParentSuccess(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	refused := r.created.Add(5 * time.Second)
	accepted := refused.Add(time.Second)
	r.childTranscript("agent-a1.jsonl", history401(refused, false))
	r.transcript(acceptedTurn(accepted))
	r.observeAfter(accepted)
	h, _ := r.history()
	g := h.Generations[0]
	if g.MainChain || !g.Sidechain || g.RejectionCount != 1 ||
		!g.FirstRejection.At.Equal(refused) || !g.LastAccepted.At.Equal(accepted) || !g.LastAcceptedBeforeFirstRejection.IsZero() {
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
	if !g.LastAcceptedBeforeFirstRejection.At.Equal(accepted) || g.LastAcceptedBeforeFirstRejection.PrecisionNanos != 1000 ||
		!g.LastAccepted.At.Equal(parentSuccess) ||
		!g.FirstRejection.At.Equal(refused) || !g.LastRejection.At.Equal(refused.Add(2*time.Second)) || g.RejectionCount != 2 {
		t.Fatalf("wrong rejection bracket: %+v", g)
	}
}

func TestClaudeAuthHistoryFirstAcceptanceAfterLatestRejection(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	first := r.created.Add(5 * time.Second)
	refused := first.Add(time.Second)
	overlap := refused.Add(500 * time.Millisecond)
	r.transcript(acceptedTurn(first), history401(refused, false), acceptedTurn(overlap))
	r.observeAfter(overlap)
	h, _ := r.history()
	if g := h.Generations[0]; !g.FirstAccepted.At.Equal(first) || !g.LastAccepted.At.Equal(overlap) || !g.FirstAcceptedAfterLastRejection.IsZero() {
		t.Fatalf("overlapping acceptance established recovery: %+v", g)
	}

	recovered := refused.Add(time.Second)
	later := recovered.Add(2 * time.Second)
	r.transcript(acceptedTurn(recovered), acceptedTurn(later))
	r.observeAfter(later)
	h, _ = r.history()
	if g := h.Generations[0]; !g.FirstAccepted.At.Equal(first) || !g.FirstAcceptedAfterLastRejection.At.Equal(recovered) || !g.LastAccepted.At.Equal(later) {
		t.Fatalf("first proven recovery was lost: %+v", g)
	}

	// The later acceptance is still known after it leaves the tail. A late
	// rejection invalidates the first recovery and leaves the later one usable.
	lateRejection := recovered.Add(500 * time.Millisecond)
	r.transcript(history401(lateRejection, false))
	r.observeAfter(later)
	h, _ = r.history()
	if g := h.Generations[0]; !g.LastRejection.At.Equal(lateRejection) || !g.FirstAcceptedAfterLastRejection.At.Equal(later) {
		t.Fatalf("late rejection did not rebuild recovery from retained evidence: %+v", g)
	}

	refusedAgain := later.Add(time.Second)
	r.appendTurn(history401(refusedAgain, false))
	r.observeAfter(refusedAgain)
	h, _ = r.history()
	if g := h.Generations[0]; !g.LastRejection.At.Equal(refusedAgain) || !g.FirstAcceptedAfterLastRejection.IsZero() || !g.LastAccepted.At.Equal(later) {
		t.Fatalf("latest rejection did not clear recovery: %+v", g)
	}

	recoveredAgain := refusedAgain.Add(time.Second)
	r.appendTurn(acceptedTurn(recoveredAgain))
	r.observeAfter(recoveredAgain)
	h, _ = r.history()
	if g := h.Generations[0]; !g.FirstAcceptedAfterLastRejection.At.Equal(recoveredAgain) || !g.FirstAccepted.At.Equal(first) {
		t.Fatalf("same-generation recovery was not recorded: %+v", g)
	}
}

func TestClaudeAuthHistoryTimestampPrecisionAtCredentialWrite(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	before := r.created.Add(10*time.Second + 122*time.Millisecond)
	overlap := before.Add(time.Millisecond)
	after := overlap.Add(time.Millisecond)
	r.now = overlap.Add(500 * time.Microsecond)
	r.apply(2)
	r.transcript(history401(before, false), history401(overlap, false), history401(after, false))
	r.observeAfter(after)
	h, _ := r.history()
	old, current := h.Generations[0], h.Generations[1]
	if old.RejectionCount != 1 || !old.LastRejection.At.Equal(before) ||
		current.RejectionCount != 1 || !current.FirstRejection.At.Equal(after) ||
		old.LastRejection.PrecisionNanos != int64(time.Millisecond) {
		t.Fatalf("overlapping timestamp was attributed to a generation: %+v", h.Generations)
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

func TestClaudeAuthHistorySkipsGapBeforeNoOpConfirmation(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	lockPath := r.s.statePath(claudeAuthHistoryLock)
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	r.now = r.created.Add(5 * time.Second)
	r.apply(2) // The projection is applied while the history lock is unavailable.
	h, _ := r.history()
	if len(h.Generations) != 1 {
		t.Fatal("history unexpectedly recorded generation 2 during the lock failure")
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	inGap := r.now.Add(time.Second)
	r.transcript(history401(inGap, false))
	r.now = inGap.Add(3 * time.Second)
	confirmed := r.now
	r.apply(2)
	after := confirmed.Add(time.Second)
	r.appendTurn(history401(after, false))
	r.observeAfter(after)
	h, _ = r.history()
	old, current := h.Generations[0], h.Generations[1]
	if old.RejectionCount != 0 || current.RejectionCount != 1 || !current.FirstRejection.At.Equal(after) ||
		!current.CredentialWrittenAt.IsZero() || !current.ConfirmedAt.Equal(confirmed) {
		t.Fatalf("poll-gap rejection was attributed: %+v", h.Generations)
	}
}

func TestClaudeAuthHistorySameGenerationRewriteLeavesWriteTimeUnknown(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	if err := os.Remove(r.s.statePath(claudeAuthHistoryFile)); err != nil {
		t.Fatal(err)
	}
	r.now = r.created.Add(5 * time.Second)
	confirmed := r.now
	r.apply(1)
	writeRaw(t, filepath.Join(r.s.ClaudeConfigDir, credentialsFileName), []byte(`{}`))
	r.now = confirmed.Add(5 * time.Second)
	wrote := false
	err := syncClaudeAccess(r.s, r.now, r.d.now, func(path string, raw []byte) error {
		wrote = true
		return writeClaudePrivateAtomic(path, raw)
	})
	if err != nil || !wrote {
		t.Fatalf("same-generation credential merge: wrote = %t, err = %v", wrote, err)
	}
	h, _ := r.history()
	if g := h.Generations[0]; !g.CredentialWrittenAt.IsZero() || !g.ConfirmedAt.Equal(confirmed) {
		t.Fatalf("a rewrite changed the initial application times: %+v", g)
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
	if old.AppliedGeneration != 1 || old.RejectionCount != 1 || !old.FirstRejection.At.Equal(at) || !old.CredentialWrittenAt.Equal(firstWrite) ||
		current.AppliedGeneration != 4 || !current.CredentialWrittenAt.Equal(r.now) || current.RejectionCount != 0 {
		t.Fatalf("new generation did not preserve prior evidence: %+v", h.Generations)
	}
	firstAccepted := r.now.Add(time.Second)
	r.transcript(acceptedTurn(firstAccepted))
	r.observeAfter(firstAccepted)
	r.restartDaemon()
	lastAccepted := firstAccepted.Add(5 * time.Second)
	r.transcript(acceptedTurn(lastAccepted))
	r.observeAfter(lastAccepted)
	h, _ = r.history()
	current = h.Generations[1]
	if !current.FirstAccepted.At.Equal(firstAccepted) || !current.LastAccepted.At.Equal(lastAccepted) ||
		!current.FirstAcceptedAfterLastRejection.IsZero() || !h.Generations[0].FirstRejection.At.Equal(at) {
		t.Fatalf("the first acceptance after a generation change was lost: %+v", h.Generations)
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
	if g := h.Generations[0]; g.RejectionCount != 3 || !g.FirstRejection.At.Equal(older) || !g.LastRejection.At.Equal(at) {
		t.Fatalf("delayed unique event = %+v", g)
	}
}

func TestClaudeAuthHistorySeparatesNativeChildrenWithoutPersistingIDs(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	at := r.created.Add(5 * time.Second)
	r.childTranscript("agent-a1.jsonl", history401(at, false))
	// The same sidechain observation is also visible in the parent file.
	r.transcript(history401(at, true))
	r.observeAfter(at)
	h, _ := r.history()
	if count := h.Generations[0].RejectionCount; count != 1 {
		t.Fatalf("one child visible in two files counted %d times", count)
	}
	r.childTranscript("agent-b2.jsonl", history401(at, false))
	r.observeAfter(at)
	r.restartDaemon()
	r.observeAfter(at)
	h, raw := r.history()
	if count := h.Generations[0].RejectionCount; count != 2 {
		t.Fatalf("two children at the same timestamp counted %d times", count)
	}
	for _, private := range []string{"agent-a1", "agent-b2", continueConv} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatal("history included a transcript identifier")
		}
	}
}

func TestClaudeAuthHistoryFindsNewestChildWithinScanCap(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	dir := filepath.Join(r.s.ClaudeConfigDir, "projects", "-work-repo", continueConv, "subagents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range claudeAuthChildEntries + 1 {
		path := filepath.Join(dir, fmt.Sprintf("agent-%04d.jsonl", i))
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	names, err := f.Readdirnames(-1)
	_ = f.Close()
	if err != nil || len(names) != claudeAuthChildEntries+1 {
		t.Fatalf("directory fixture: %d names, %v", len(names), err)
	}
	at := r.created.Add(5 * time.Second)
	modified := time.Now().Add(time.Hour)
	for i := range 2 {
		path := filepath.Join(dir, names[claudeAuthChildEntries-1+i])
		if err := os.WriteFile(path, []byte(history401(at.Add(time.Duration(i)*time.Second), false)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	r.observeAfter(at.Add(time.Second))
	h, _ := r.history()
	if g := h.Generations[0]; g.RejectionCount != 1 || !g.Sidechain || !g.FirstRejection.At.Equal(at) {
		t.Fatalf("wrong child observed at the scan cap: %+v", g)
	}
}

func TestClaudeAuthHistoryRetainsLaunchAttributionAcrossResume(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	first := r.created.Add(5 * time.Second)
	r.transcript(history401(first, false))
	r.observeAfter(first)
	r.restartDaemon()
	laterBoot := "abcdef012345"
	resumedAt := r.now.Add(time.Second)
	writeJSON(t, r.s.statePath(bootFile), bootRecord{BootID: laterBoot})
	writeJSON(t, r.s.statePath(resumeFile), Launch{
		BootID: laterBoot, CreatedAt: resumedAt, ConversationID: continueConv, TUI: true, Dir: "/work/repo", Resume: true,
	})
	last := resumedAt.Add(time.Second)
	r.appendTurn(history401(last, true))
	r.observeAfter(last)
	h, _ := r.history()
	g := h.Generations[0]
	if g.RejectionCount != 2 || g.FirstRejectionLaunch == nil || g.FirstRejectionLaunch.BootID != historyBootID ||
		g.LastRejectionLaunch == nil || g.LastRejectionLaunch.BootID != laterBoot {
		t.Fatalf("resume rewrote earlier launch attribution: %+v", g)
	}
}

func TestClaudeAuthHistoryIgnoresPreLaunchRejectionAfterResume(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	r.restartDaemon()
	const laterBoot = "abcdef012345"
	resumedAt := r.created.Add(time.Minute)
	writeJSON(t, r.s.statePath(bootFile), bootRecord{BootID: laterBoot})
	writeJSON(t, r.s.statePath(resumeFile), Launch{
		BootID: laterBoot, CreatedAt: resumedAt, ConversationID: continueConv, TUI: true, Dir: "/work/repo", Resume: true,
	})
	after := resumedAt.Add(time.Second)
	r.transcript(history401(resumedAt.Add(-time.Second), false), history401(after, false))
	r.observeAfter(after)
	h, _ := r.history()
	if g := h.Generations[0]; g.RejectionCount != 1 || !g.FirstRejection.At.Equal(after) {
		t.Fatalf("pre-launch rejection was attributed after resume: %+v", g)
	}
}

func TestClaudeAuthHistoryDefersFutureIntervalAcrossGenerationWrite(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	at := r.created.Add(5*time.Second + 123*time.Millisecond)
	r.transcript(history401(at, false))
	r.now = at.Add(500 * time.Microsecond)
	r.d.pollClaude(context.Background())
	h, _ := r.history()
	if g := h.Generations[0]; g.RejectionCount != 0 {
		t.Fatalf("rejection was counted before its recorded interval ended: %+v", g)
	}
	r.now = at.Add(2 * time.Millisecond)
	r.apply(2)
	r.observeAfter(at)
	h, _ = r.history()
	old, current := h.Generations[0], h.Generations[1]
	if old.RejectionCount != 1 || !old.FirstRejection.At.Equal(at) || current.RejectionCount != 0 {
		t.Fatalf("deferred rejection was attributed incorrectly after the write: %+v", h.Generations)
	}
}

func TestClaudeAuthHistoryIgnoresSidechainAcceptances(t *testing.T) {
	for _, source := range []string{"parent-file", "child-file"} {
		t.Run(source, func(t *testing.T) {
			r := newClaudeHistoryRig(t)
			r.apply(1)
			accepted := r.created.Add(5 * time.Second)
			refused := accepted.Add(time.Second)
			r.transcript(history401(refused, false))
			if source == "parent-file" {
				r.appendTurn(acceptedTurnWith(accepted.Format(time.RFC3339Nano), "claude-sonnet-5-5", `{"input_tokens":3,"output_tokens":9}`, true))
			} else {
				r.childTranscript("agent-a1.jsonl", acceptedTurn(accepted))
			}
			r.observeAfter(refused)
			h, _ := r.history()
			if g := h.Generations[0]; g.RejectionCount != 1 || !g.FirstAccepted.IsZero() || !g.LastAccepted.IsZero() ||
				!g.LastAcceptedBeforeFirstRejection.IsZero() || !g.FirstAcceptedAfterLastRejection.IsZero() {
				t.Fatalf("sidechain success was recorded as parent acceptance: %+v", g)
			}
		})
	}
}

func TestClaudeAuthHistoryRejectsInconsistentRejectionCounts(t *testing.T) {
	for _, count := range []uint8{2, claudeAuthCountLimit} {
		t.Run(fmt.Sprintf("count-%d", count), func(t *testing.T) {
			r := newClaudeHistoryRig(t)
			r.apply(1)
			path := r.s.statePath(claudeAuthHistoryFile)
			raw := fmt.Appendf(nil, `{"version":1,"observationTiming":"assistant-transcript-proxy","clockSkewUnknown":true,"generations":[{"appliedGeneration":1,"confirmedAt":%q,"rejectionCount":%d,"seenRejections":[{"at":%q,"count":1}]}]}`,
				r.now.Format(time.RFC3339Nano), count, r.created.Format(time.RFC3339Nano))
			writeRaw(t, path, raw)
			if _, _, err := readClaudeAuthHistory(r.s); err == nil {
				t.Error("history decoder accepted inconsistent rejection counts")
			}
			publish(t, r.live, projAccess(2, 9*time.Hour))
			wrote := false
			err := syncClaudeAccess(r.s, r.now, r.d.now, func(path string, raw []byte) error {
				wrote = true
				return writeClaudePrivateAtomic(path, raw)
			})
			if err != nil || !wrote {
				t.Fatalf("corrupt history blocked the projection: wrote = %t, err = %v", wrote, err)
			}
			if after, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, after) {
				t.Fatal("projection changed the invalid history file")
			}
		})
	}
}

func TestClaudeAuthHistoryGenerationAndCountBounds(t *testing.T) {
	r := newClaudeHistoryRig(t)
	for generation := uint64(1); generation <= 4; generation++ {
		r.apply(generation)
		var lines []string
		at := r.now.Add(time.Second + 123456789*time.Nanosecond)
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
		len(g.SeenRejections) != 0 || !g.LastRejection.At.Equal(at.Add(time.Second)) {
		t.Fatalf("saturated generation = %+v", g)
	}
}

func TestClaudeAuthHistoryWorstCaseSize(t *testing.T) {
	s := Settings{StateDir: t.TempDir()}
	at := time.Date(2026, 10, 19, 12, 0, 0, 123456789, time.UTC)
	launch := &claudeAuthLaunch{BootID: historyBootID, CreatedAt: at, Source: claudeSourceStatic}
	updateClaudeAuthHistory(s, func(h *claudeAuthHistory) {
		h.Launch = launch
		for i := range claudeAuthGenerations {
			confirmed := at.Add(time.Duration(i) * time.Hour)
			// Use the widest UTC timestamp and precision encodings together
			// as an upper bound, even when these widths cannot occur together.
			stamp := func(offset int) claudeAuthTime {
				return claudeAuthTime{At: confirmed.Add(time.Duration(offset) * time.Second), PrecisionNanos: int64(time.Second)}
			}
			g := claudeAuthGeneration{
				AppliedGeneration:                math.MaxUint64 - uint64(claudeAuthGenerations-1-i),
				ConfirmedAt:                      confirmed,
				CredentialWrittenAt:              confirmed,
				FirstRejection:                   stamp(3),
				LastRejection:                    stamp(claudeAuthCountLimit + 1),
				LastAcceptedBeforeFirstRejection: stamp(2),
				FirstAccepted:                    stamp(1),
				LastAccepted:                     stamp(claudeAuthCountLimit + 3),
				FirstAcceptedAfterLastRejection:  stamp(claudeAuthCountLimit + 2),
				FirstRejectionLaunch:             launch,
				LastRejectionLaunch:              launch,
				MainChain:                        true,
				Sidechain:                        true,
				ErrorClass:                       claudeAuthRejected,
				RejectionCount:                   claudeAuthCountLimit - 1,
			}
			for k := range claudeAuthCountLimit - 1 {
				g.SeenRejections = append(g.SeenRejections, claudeAuthRejectionKey{At: stamp(k + 3).At, Sidechain: true, Count: 1})
			}
			h.Generations = append(h.Generations, g)
		}
	})
	_, raw, err := readClaudeAuthHistory(s)
	if err != nil || len(raw) >= claudeAuthHistorySize {
		t.Fatalf("worst-case history failed the %d-byte limit: size = %d, err = %v", claudeAuthHistorySize, len(raw), err)
	}
	t.Logf("worst-case history: %d of %d bytes", len(raw), claudeAuthHistorySize)
}

func TestClaudeAuthHistoryOnlyWritesSanitizedFields(t *testing.T) {
	r := newClaudeHistoryRig(t)
	r.apply(1)
	accepted := r.created.Add(5 * time.Second)
	refused := accepted.Add(time.Second)
	recovered := refused.Add(time.Second)
	r.transcript(acceptedTurn(accepted), history401(refused, true), history401(refused, false), acceptedTurn(recovered))
	r.observeAfter(recovered)
	_, raw := r.history()
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	assertHistoryKeys(t, record, "clockSkewUnknown", "generations", "launch", "observationTiming", "version")
	assertHistoryKeys(t, record["launch"], "bootId", "createdAt", "source")
	g := record["generations"].([]any)[0].(map[string]any)
	assertHistoryKeys(t, g, "appliedGeneration", "confirmedAt", "credentialWrittenAt", "errorClass",
		"firstAccepted", "firstAcceptedAfterLastRejection", "firstRejection", "firstRejectionLaunch", "lastAccepted", "lastAcceptedBeforeFirstRejection",
		"lastRejection", "lastRejectionLaunch", "mainChain", "rejectionCount", "seenRejections", "sidechain")
	assertHistoryKeys(t, g["firstRejectionLaunch"], "bootId", "createdAt", "source")
	assertHistoryKeys(t, g["lastRejectionLaunch"], "bootId", "createdAt", "source")
	for _, field := range []string{"firstAccepted", "firstAcceptedAfterLastRejection", "firstRejection", "lastRejection", "lastAccepted", "lastAcceptedBeforeFirstRejection"} {
		assertHistoryKeys(t, g[field], "at", "precisionNanos")
	}
	keys := g["seenRejections"].([]any)
	assertHistoryKeys(t, keys[0], "at", "count", "sidechain")
	assertHistoryKeys(t, keys[1], "at", "count")
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
