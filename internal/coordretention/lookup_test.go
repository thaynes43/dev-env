package coordretention

import (
	"context"
	"errors"
	"fmt"
	"testing"

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
		out[i] = protocol.CoordArchiveLookupResult{CoordArchiveIdentity: id, Holds: f.holds[lookupKey(id)]}
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
			if got, err := Lookup(context.Background(), retentionNS, shelf.NewExecSlots(1), f, []Result{{Summary: summaryOf(t, TaskClaim)}}); err == nil || got != nil || len(f.calls) != 1 {
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
	if got, err := Lookup(ctx, retentionNS, shelf.NewExecSlots(1), fake, []Result{{Summary: claim}}); !errors.Is(err, context.Canceled) || got != nil || len(fake.calls) != 0 {
		t.Fatalf("pre-cancel = %#v, %v, calls=%d", got, err, len(fake.calls))
	}
	busy := shelf.NewExecSlots(1)
	release, ok := busy.TryTake()
	if !ok {
		t.Fatal("fixture slot unavailable")
	}
	fake = &lookupFake{}
	if got, err := Lookup(context.Background(), retentionNS, busy, fake, []Result{{Summary: claim}}); !errors.Is(err, ErrLookupBusy) || got != nil || len(fake.calls) != 0 {
		t.Fatalf("busy = %#v, %v, calls=%d", got, err, len(fake.calls))
	}
	release()

	slots := shelf.NewExecSlots(1)
	fake = &lookupFake{err: errors.New("lookup failed")}
	if got, err := Lookup(context.Background(), retentionNS, slots, fake, []Result{{Summary: claim}}); err == nil || got != nil {
		t.Fatalf("error result = %#v, %v", got, err)
	}
	if release, ok := slots.TryTake(); !ok {
		t.Fatal("error leaked slot")
	} else {
		release()
	}
	ctx, cancel = context.WithCancel(context.Background())
	fake = &lookupFake{after: cancel}
	if got, err := Lookup(ctx, retentionNS, slots, fake, []Result{{Summary: claim}}); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("cancel result = %#v, %v", got, err)
	}
	if release, ok := slots.TryTake(); !ok {
		t.Fatal("cancel leaked slot")
	} else {
		release()
	}
}
