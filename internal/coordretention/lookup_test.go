package coordretention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/shelf"
)

type lookupFake struct {
	calls [][]protocol.CoordArchiveIdentity
	holds map[string]bool
	err   error
	after func()
	reply func([]protocol.CoordArchiveIdentity) []protocol.CoordArchiveLookupResult
}

func (f *lookupFake) Lookup(_ context.Context, ids []protocol.CoordArchiveIdentity) ([]protocol.CoordArchiveLookupResult, error) {
	f.calls = append(f.calls, append([]protocol.CoordArchiveIdentity(nil), ids...))
	if f.after != nil {
		f.after()
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.reply != nil {
		return f.reply(ids), nil
	}
	out := make([]protocol.CoordArchiveLookupResult, len(ids))
	for i, id := range ids {
		out[i] = protocol.CoordArchiveLookupResult{CoordArchiveIdentity: id, Holds: f.holds[lookupKey(id)], Revisions: []protocol.CoordArchiveRevision{}}
		if out[i].Holds {
			out[i].Revisions = []protocol.CoordArchiveRevision{{ResourceVersion: "1", SHA256: strings.Repeat("a", 64)}}
		}
	}
	return out, nil
}

func TestLookupChunksGroupsAndDeduplicates(t *testing.T) {
	base := summaryOf(t, TaskClaim)
	results := make([]Result, 1025)
	for i := range results {
		s := base
		s.UID = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		results[i] = Result{Summary: s, Decision: Released}
	}
	other := summaryOf(t, PublishedContext)
	other.Project = "other"
	results = append(results, Result{Summary: other, Decision: Kept}, results[0])
	fake := &lookupFake{}
	out, err := Lookup(context.Background(), retentionNS, shelf.NewExecSlots(1), fake, results)
	if err != nil || len(out) != len(results) || len(fake.calls) != 3 {
		t.Fatalf("lookup = %d calls=%d err=%v", len(out), len(fake.calls), err)
	}
	if len(fake.calls[0]) != 1 || len(fake.calls[1]) != 1024 || len(fake.calls[2]) != 1 {
		t.Fatalf("chunk lengths = %d,%d,%d", len(fake.calls[0]), len(fake.calls[1]), len(fake.calls[2]))
	}
	for _, call := range fake.calls {
		for _, id := range call {
			if id.Resource != call[0].Resource || id.Project != call[0].Project {
				t.Fatalf("mixed group: %#v", call)
			}
		}
	}
}

func TestLookupResolvesOnlyPendingReadAckMessage(t *testing.T) {
	ack := summaryOf(t, InboxReadAck)
	claim := summaryOf(t, TaskClaim)
	pending := Result{Summary: ack, Decision: Unknown, RequiredMessage: &ack.Message}
	ordinary := Result{Summary: claim, Decision: Unknown}
	fake := &lookupFake{holds: map[string]bool{lookupKey(protocol.CoordArchiveIdentity{Resource: "inboxmessages", Project: ack.Message.Project, Name: ack.Message.Name, UID: ack.Message.UID}): true}}
	out, err := Lookup(context.Background(), retentionNS, shelf.NewExecSlots(1), fake, []Result{pending, ordinary, pending})
	if err != nil || len(out) != 3 || out[0].Result.Decision != Released || out[0].Result.RequiredMessage != nil || out[1].Result.Decision != Unknown || out[2].Message == nil {
		t.Fatalf("out = %#v, err=%v", out, err)
	}
	messageCalls := 0
	for _, call := range fake.calls {
		for _, id := range call {
			if id.Resource == "inboxmessages" {
				messageCalls++
			}
		}
	}
	if messageCalls != 1 {
		t.Fatalf("message queries = %d", messageCalls)
	}
}

func TestLookupRefusesInvalidPendingAndPartialProofWithoutExecResult(t *testing.T) {
	ack := summaryOf(t, InboxReadAck)
	bad := ack.Message
	bad.Project = "foreign"
	fake := &lookupFake{}
	if got, err := Lookup(context.Background(), retentionNS, shelf.NewExecSlots(1), fake, []Result{{Summary: ack, Decision: Unknown, RequiredMessage: &bad}}); err == nil || got != nil || len(fake.calls) != 0 {
		t.Fatalf("foreign pending = %#v, %v, calls=%d", got, err, len(fake.calls))
	}
	invalid := summaryOf(t, TaskClaim)
	invalid.Valid = false
	out, err := Lookup(context.Background(), retentionNS, shelf.NewExecSlots(1), fake, []Result{{Summary: invalid, Decision: Released}})
	if err != nil || len(out) != 1 || out[0].Result.Decision != Unknown || len(fake.calls) != 0 {
		t.Fatalf("invalid summary = %#v, %v, calls=%d", out, err, len(fake.calls))
	}
	for name, reply := range map[string]func([]protocol.CoordArchiveIdentity) []protocol.CoordArchiveLookupResult{
		"partial": func([]protocol.CoordArchiveIdentity) []protocol.CoordArchiveLookupResult { return nil },
		"wrong identity": func(ids []protocol.CoordArchiveIdentity) []protocol.CoordArchiveLookupResult {
			wrong := ids[0]
			wrong.UID = "88888888-8888-4888-8888-888888888888"
			return []protocol.CoordArchiveLookupResult{{CoordArchiveIdentity: wrong}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := &lookupFake{reply: reply}
			if got, err := Lookup(context.Background(), retentionNS, shelf.NewExecSlots(1), f, []Result{{Summary: summaryOf(t, TaskClaim), Decision: Released}}); err == nil || got != nil || len(f.calls) != 1 {
				t.Fatalf("bad proof = %#v, %v, calls=%d", got, err, len(f.calls))
			}
		})
	}
}

func TestLookupBusyErrorsAndReleasesSlotsOnErrorAndCancel(t *testing.T) {
	claim := summaryOf(t, TaskClaim)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &lookupFake{}
	if got, err := Lookup(ctx, retentionNS, shelf.NewExecSlots(1), fake, []Result{{Summary: claim, Decision: Released}}); !errors.Is(err, context.Canceled) || got != nil || len(fake.calls) != 0 {
		t.Fatalf("pre-cancel = %#v, %v, calls=%d", got, err, len(fake.calls))
	}
	busy := shelf.NewExecSlots(1)
	release, ok := busy.TryTake()
	if !ok {
		t.Fatal("fixture slot unavailable")
	}
	fake = &lookupFake{}
	if got, err := Lookup(context.Background(), retentionNS, busy, fake, []Result{{Summary: claim, Decision: Released}}); !errors.Is(err, ErrLookupBusy) || got != nil || len(fake.calls) != 0 {
		t.Fatalf("busy = %#v, %v, calls=%d", got, err, len(fake.calls))
	}
	release()

	slots := shelf.NewExecSlots(1)
	fake = &lookupFake{err: errors.New("lookup failed")}
	if got, err := Lookup(context.Background(), retentionNS, slots, fake, []Result{{Summary: claim, Decision: Released}}); err == nil || got != nil {
		t.Fatalf("error result = %#v, %v", got, err)
	}
	if release, ok := slots.TryTake(); !ok {
		t.Fatal("error leaked slot")
	} else {
		release()
	}
	ctx, cancel = context.WithCancel(context.Background())
	fake = &lookupFake{after: cancel}
	if got, err := Lookup(ctx, retentionNS, slots, fake, []Result{{Summary: claim, Decision: Released}}); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("cancel result = %#v, %v", got, err)
	}
	if release, ok := slots.TryTake(); !ok {
		t.Fatal("cancel leaked slot")
	} else {
		release()
	}
}

func TestLookupSameResourceProjectsStaySeparate(t *testing.T) {
	one := summaryOf(t, PublishedContext)
	two := one
	two.Project = "other"
	f := &lookupFake{}
	got, err := Lookup(context.Background(), retentionNS, shelf.NewExecSlots(2), f, []Result{{Summary: one, Decision: Released}, {Summary: two, Decision: Kept}})
	if err != nil || len(got) != 2 || len(f.calls) != 2 || f.calls[0][0].Resource != f.calls[1][0].Resource || f.calls[0][0].Project == f.calls[1][0].Project || got[0].Record.Project != one.Project || got[1].Record.Project != two.Project {
		t.Fatalf("project separation: %#v calls=%#v err=%v", got, f.calls, err)
	}
}

func TestLookupComposesEvaluatedAckAndLiveMessageDedup(t *testing.T) {
	ack := summaryOf(t, InboxReadAck)
	message := summaryOf(t, InboxMessage)
	if message.Name != ack.Message.Name || message.UID != ack.Message.UID {
		t.Fatal("fixture message binding differs")
	}
	for _, tc := range []struct {
		name             string
		readError, holds bool
		want             Decision
	}{
		{"absence with holds", false, true, Released},
		{"absence without holds", false, false, Kept},
		{"read error despite holds", true, true, Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &hookReader{get: func(_ context.Context, key client.ObjectKey, _ client.Object) error {
				if tc.readError {
					return errors.New("message unavailable")
				}
				return absentObject(key)
			}}
			evaluated := Evaluate(context.Background(), config(r), []Summary{ack})
			if (evaluated[0].RequiredMessage != nil) == tc.readError {
				t.Fatal("unexpected evaluated dependency")
			}
			// A separately listed message shares the exact pending dependency.
			inputs := append(evaluated, Result{Summary: message, Decision: Unknown})
			f := &lookupFake{holds: map[string]bool{lookupKey(protocol.CoordArchiveIdentity{Resource: message.Resource, Project: message.Project, Name: message.Name, UID: message.UID}): tc.holds}}
			got, err := Lookup(context.Background(), retentionNS, shelf.NewExecSlots(2), f, inputs)
			if err != nil || len(got) != 2 || got[0].Result.Decision != tc.want || got[0].Result.RequiredMessage != nil || got[1].Result.Decision != Unknown {
				t.Fatalf("composition: %#v err=%v", got, err)
			}
			count := 0
			for _, batch := range f.calls {
				for _, id := range batch {
					if id.Resource == "inboxmessages" {
						count++
					}
				}
			}
			if count != 1 {
				t.Fatalf("listed/dependency message queried %d times", count)
			}
		})
	}
}

func TestLookupOwnsOneSharedSlotDuringExecAndRestoresBoth(t *testing.T) {
	slots := shelf.NewExecSlots(2)
	f := &lookupFake{after: func() {
		release, ok := slots.TryTake()
		if !ok {
			t.Fatal("lookup holds more than one slot")
		}
		defer release()
		if extra, ok := slots.TryTake(); ok {
			extra()
			t.Fatal("lookup does not hold its slot during exec")
		}
	}}
	got, err := Lookup(context.Background(), retentionNS, slots, f, []Result{{Summary: summaryOf(t, TaskClaim), Decision: Released}})
	if err != nil || len(got) != 1 {
		t.Fatalf("lookup: %#v %v", got, err)
	}
	assertLookupSlots(t, slots, 2)
}

func assertLookupSlots(t *testing.T, slots *shelf.ExecSlots, count int) {
	t.Helper()
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for range count {
		release, ok := slots.TryTake()
		if !ok {
			t.Fatal("lookup leaked slot")
		}
		releases = append(releases, release)
	}
	if release, ok := slots.TryTake(); ok {
		release()
		t.Fatal("slot pool capacity changed")
	}
}

func TestLookupLaterBatchFailureDiscardsEarlierProof(t *testing.T) {
	s := summaryOf(t, TaskClaim)
	inputs := make([]Result, protocol.MaxCoordArchiveLookupItems+1)
	for i := range inputs {
		copy := s
		copy.UID = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		inputs[i] = Result{Summary: copy, Decision: Released}
	}
	for _, mode := range []string{"error", "partial", "identity", "busy", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			slots := shelf.NewExecSlots(2)
			var held []func()
			defer func() {
				for _, release := range held {
					release()
				}
			}()
			f := &lookupFake{}
			f.after = func() {
				if len(f.calls) == 2 {
					switch mode {
					case "error":
						f.err = errors.New("later error")
					case "partial":
						f.reply = func([]protocol.CoordArchiveIdentity) []protocol.CoordArchiveLookupResult { return nil }
					case "identity":
						f.reply = func(ids []protocol.CoordArchiveIdentity) []protocol.CoordArchiveLookupResult {
							x := ids[0]
							x.Project = "wrong"
							return []protocol.CoordArchiveLookupResult{{CoordArchiveIdentity: x}}
						}
					case "cancel":
						cancel()
					}
				}
			}
			if mode == "busy" {
				ctx = &lookupBetweenBatchesContext{Context: ctx, slots: slots, calls: &f.calls, held: &held}
			}
			got, err := Lookup(ctx, retentionNS, slots, f, inputs)
			if err == nil || got != nil {
				t.Fatalf("later %s exposed proof: %#v %v", mode, got, err)
			}
			if mode == "busy" && !errors.Is(err, ErrLookupBusy) {
				t.Fatalf("busy err: %v", err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel err: %v", err)
			}
			wantCalls := 2
			if mode == "busy" {
				wantCalls = 1
			}
			if len(f.calls) != wantCalls {
				t.Fatalf("calls=%d want=%d", len(f.calls), wantCalls)
			}
			for _, release := range held {
				release()
			}
			held = nil
			assertLookupSlots(t, slots, 2)
		})
	}
}

// Simulates other shelf consumers taking the shared pool after one completed
// client call; no goroutine, timer, sleeps or real exec are involved.
type lookupBetweenBatchesContext struct {
	context.Context
	slots *shelf.ExecSlots
	calls *[][]protocol.CoordArchiveIdentity
	held  *[]func()
}

func (c *lookupBetweenBatchesContext) Err() error {
	if len(*c.calls) == 1 && len(*c.held) == 0 {
		for range 2 {
			if release, ok := c.slots.TryTake(); ok {
				*c.held = append(*c.held, release)
			}
		}
	}
	return c.Context.Err()
}

// Cancels on the second context observation after the fake client completed:
// after the immediate post-client check, before proof/output can be exposed.
type lookupAfterClientContext struct {
	context.Context
	completed *bool
	cancel    context.CancelFunc
	checks    int
}

func (c *lookupAfterClientContext) Err() error {
	if *c.completed {
		c.checks++
		if c.checks == 2 {
			c.cancel()
		}
	}
	return c.Context.Err()
}
func TestLookupCancellationWithoutExecAndBeforeOutput(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	cancel()
	invalid := summaryOf(t, TaskClaim)
	invalid.Valid = false
	for name, inputs := range map[string][]Result{"empty": nil, "all invalid": {{Summary: invalid, Decision: Unknown}}} {
		t.Run(name, func(t *testing.T) {
			f := &lookupFake{}
			if got, err := Lookup(base, retentionNS, shelf.NewExecSlots(2), f, inputs); !errors.Is(err, context.Canceled) || got != nil || len(f.calls) != 0 {
				t.Fatalf("cancelled empty/invalid: %#v %v", got, err)
			}
		})
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	completed := false
	late := &lookupAfterClientContext{Context: ctx, completed: &completed, cancel: stop}
	f := &lookupFake{after: func() { completed = true }}
	if got, err := Lookup(late, retentionNS, shelf.NewExecSlots(2), f, []Result{{Summary: summaryOf(t, TaskClaim), Decision: Released}}); !errors.Is(err, context.Canceled) || got != nil || len(f.calls) != 1 {
		t.Fatalf("late cancellation: %#v %v", got, err)
	}
}
