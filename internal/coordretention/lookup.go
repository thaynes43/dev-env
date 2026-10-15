package coordretention

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/shelf"
)

// ArchiveLookup is the bounded, typed shelf lookup used by this pass. It does
// not verify archived objects and confers no release or deletion authority.
type ArchiveLookup interface {
	Lookup(context.Context, []protocol.CoordArchiveIdentity) ([]protocol.CoordArchiveLookupResult, error)
}

// LookupResult retains the evaluation and only the lookup metadata needed by a
// later mark/grace phase. Message is populated only for a pending read ack.
type LookupResult struct {
	Result  Result
	Record  *protocol.CoordArchiveLookupResult
	Message *protocol.CoordArchiveLookupResult
}

var ErrLookupBusy = errors.New("coordination retention lookup: shelf exec slot unavailable")

// Lookup consumes one evaluated pass with sequential, grouped archive lookups.
// It returns no partial result when validation or any lookup fails.
func Lookup(ctx context.Context, operatorNamespace string, slots *shelf.ExecSlots, archive ArchiveLookup, results []Result) ([]LookupResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if slots == nil || archive == nil {
		return nil, errors.New("coordination retention lookup: shelf slots and archive are required")
	}
	out := make([]LookupResult, len(results))
	groups := map[string][]protocol.CoordArchiveIdentity{}
	seen := map[string]struct{}{}
	for i, result := range results {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s := result.Summary
		if !validSummary(s, operatorNamespace) {
			out[i].Result = Result{Summary: s, Decision: Unknown}
			continue
		}
		out[i].Result = result
		if err := addLookup(groups, seen, s.Resource, s.Project, s.Name, s.UID); err != nil {
			return nil, err
		}
		if result.RequiredMessage == nil {
			continue
		}
		if result.Decision != Unknown || s.Kind != InboxReadAck || *result.RequiredMessage != s.Message || s.Message.Namespace != operatorNamespace || s.Message.Project != s.Project {
			return nil, errors.New("coordination retention lookup: invalid pending message dependency")
		}
		m := s.Message
		if err := addLookup(groups, seen, "inboxmessages", m.Project, m.Name, m.UID); err != nil {
			return nil, err
		}
	}

	proof := map[string]protocol.CoordArchiveLookupResult{}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ids := groups[key]
		sort.Slice(ids, func(i, j int) bool {
			if ids[i].Name != ids[j].Name {
				return ids[i].Name < ids[j].Name
			}
			return ids[i].UID < ids[j].UID
		})
		for start := 0; start < len(ids); start += protocol.MaxCoordArchiveLookupItems {
			end := min(start+protocol.MaxCoordArchiveLookupItems, len(ids))
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			release, ok := slots.TryTake()
			if !ok {
				return nil, ErrLookupBusy
			}
			if err := ctx.Err(); err != nil {
				release()
				return nil, err
			}
			answer, err := archive.Lookup(ctx, ids[start:end])
			release()
			if err != nil {
				return nil, fmt.Errorf("coordination retention lookup: %w", err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if len(answer) != end-start {
				return nil, errors.New("coordination retention lookup: incomplete response")
			}
			for n := range answer {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if answer[n].CoordArchiveIdentity != ids[start+n] {
					return nil, errors.New("coordination retention lookup: response identity differs")
				}
				proof[lookupKey(answer[n].CoordArchiveIdentity)] = answer[n]
			}
		}
	}
	for i := range out {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s := out[i].Result.Summary
		if !validSummary(s, operatorNamespace) {
			continue
		}
		record, ok := proof[lookupKey(protocol.CoordArchiveIdentity{Resource: s.Resource, Project: s.Project, Name: s.Name, UID: s.UID})]
		if !ok {
			return nil, errors.New("coordination retention lookup: missing record proof")
		}
		out[i].Record = &record
		if out[i].Result.RequiredMessage == nil {
			continue
		}
		m := s.Message
		message, ok := proof[lookupKey(protocol.CoordArchiveIdentity{Resource: "inboxmessages", Project: m.Project, Name: m.Name, UID: m.UID})]
		if !ok {
			return nil, errors.New("coordination retention lookup: missing message proof")
		}
		out[i].Message = &message
		if message.Holds {
			out[i].Result.Decision = Released
			out[i].Result.Observations = []protocol.CoordArchiveObservation{{
				Role: "message", Namespace: m.Namespace, Name: m.Name, UID: m.UID, Observed: "message-released",
			}}
		} else {
			out[i].Result.Decision = Kept
			out[i].Result.Observations = nil
		}
		out[i].Result.RequiredMessage = nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func addLookup(groups map[string][]protocol.CoordArchiveIdentity, seen map[string]struct{}, resource, project, name, uid string) error {
	id := protocol.CoordArchiveIdentity{Resource: resource, Project: project, Name: name, UID: uid}
	if _, err := id.RecordDir(); err != nil {
		return fmt.Errorf("coordination retention lookup: invalid identity: %w", err)
	}
	key := lookupKey(id)
	if _, ok := seen[key]; ok {
		return nil
	}
	seen[key] = struct{}{}
	group := id.Resource + "\x00" + id.Project
	groups[group] = append(groups[group], id)
	return nil
}

func lookupKey(id protocol.CoordArchiveIdentity) string {
	return id.Resource + "\x00" + id.Project + "\x00" + id.Name + "\x00" + id.UID
}
