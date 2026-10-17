package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

type coordJournalState struct {
	Sequence   int
	LastAt     time.Time
	GraceStart *time.Time
	Tail       string
}

// Caller holds the shelf lock. Only fixed-size names, one bounded event, and
// the folded state are retained; directory order is never sequence order.
func (a CoordArchive) readJournal(id protocol.CoordArchiveIdentity, rel string) (coordJournalState, error) {
	zero := coordJournalState{}
	dir := filepath.Join(a.Root, filepath.FromSlash(rel), "events")
	d, err := openBeneath(a.Root, dir, historyDirFlags)
	if errors.Is(err, fs.ErrNotExist) {
		return zero, nil
	}
	corrupt := func() (coordJournalState, error) {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, rel, "invalid event journal")
	}
	if err != nil {
		return corrupt()
	}
	defer func() { _ = d.Close() }()
	count, max := 0, 0
	for {
		names, readErr := d.Readdirnames(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return corrupt()
		}
		for _, name := range names {
			if len(name) < 17 || name[8] != '-' || !strings.HasSuffix(name, ".json") {
				return corrupt()
			}
			seq, parseErr := strconv.Atoi(name[:8])
			state := strings.TrimSuffix(name[9:], ".json")
			if parseErr != nil || seq < 1 || fmt.Sprintf("%08d", seq) != name[:8] || (state != "absent" && state != "present" && state != "reset" && state != "hold") {
				return corrupt()
			}
			count++
			if seq > max {
				max = seq
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if count != max {
		return corrupt()
	}
	result := zero
	for seq := 1; seq <= max; seq++ {
		found := 0
		var event protocol.CoordArchiveEvent
		tail := ""
		for _, state := range []string{"absent", "present", "reset", "hold"} {
			file := filepath.Join(dir, fmt.Sprintf("%08d-%s.json", seq, state))
			raw, readErr := readArchiveFileLinks(a.Root, file, protocol.MaxCoordArchiveEventBytes, 2)
			if errors.Is(readErr, fs.ErrNotExist) {
				continue
			}
			if readErr != nil {
				return corrupt()
			}
			found++
			var e protocol.CoordArchiveEvent
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&e); err != nil || e.Validate(id.Resource) != nil || e.State != state {
				return corrupt()
			}
			canonical, err := protocol.CanonicalJSON(e)
			if err != nil || !bytes.Equal(raw, canonical) {
				return corrupt()
			}
			event, tail = e, file
		}
		if found != 1 || event.At.Before(result.LastAt) {
			return corrupt()
		}
		result.Sequence = seq
		result.LastAt = event.At
		result.Tail = tail
		if event.State == "absent" {
			if result.GraceStart == nil {
				at := event.At
				result.GraceStart = &at
			}
		} else if event.State != "hold" {
			result.GraceStart = nil
		}
	}
	return result, nil
}

// journalTail is the last folded event. Its time equals GraceStart only for the
// absent event that opened the current grace.
func journalTail(state coordJournalState) (string, *time.Time) {
	if state.Tail == "" || state.LastAt.IsZero() {
		return "", nil
	}
	base := filepath.Base(state.Tail)
	if len(base) < len("00000001-a.json") || base[8] != '-' || !strings.HasSuffix(base, ".json") {
		return "", nil
	}
	name := strings.TrimSuffix(base[9:], ".json")
	switch name {
	case "absent", "present", "reset", "hold":
		at := state.LastAt.UTC()
		return name, &at
	default:
		return "", nil
	}
}

func (a CoordArchive) syncJournal(state coordJournalState) error {
	if state.Tail == "" {
		return nil
	}
	f, _, err := openHistoryFileLinks(a.Root, state.Tail, 2)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return a.syncPublicationDirectories(filepath.Dir(state.Tail))
}

// Caller holds the exclusive lock. No existing event can be replaced.
func (a CoordArchive) appendEvent(rel string, state coordJournalState, event protocol.CoordArchiveEvent) (coordJournalState, bool, error) {
	zero := coordJournalState{}
	if (event.State == "absent") == (state.GraceStart != nil) {
		// An earlier link may be visible after failed parent fsync. A retry must
		// repair durability even when the transition itself is already recorded.
		if err := a.syncJournal(state); err != nil {
			return zero, false, coordArchiveWriteError(rel, err)
		}
		return state, false, nil
	}
	if event.At.Before(state.LastAt) || state.Sequence >= 99999999 {
		return zero, false, protocol.HistoryErrorf(protocol.HistoryErrInvalid, rel, "event timestamp or sequence cannot advance")
	}
	dir := filepath.Join(a.Root, filepath.FromSlash(rel), "events")
	for _, p := range []string{dir, filepath.Join(a.Root, protocol.HistoryArchiveIncoming)} {
		if err := coordArchiveDirectories(a.Root, p); err != nil {
			return zero, false, coordArchiveWriteError(rel, err)
		}
	}
	files := HistoryArchive{Root: a.Root}
	tmp, err := files.newIncomingDir()
	if err != nil {
		return zero, false, coordArchiveWriteError(rel, err)
	}
	raw, err := protocol.CanonicalJSON(event)
	if err != nil {
		return zero, false, err
	}
	staged := filepath.Join(tmp, "event.json")
	if err := files.incomingFile(staged, raw); err != nil {
		return zero, false, coordArchiveWriteError(rel, err)
	}
	if err := files.incomingSyncTree(tmp); err != nil {
		return zero, false, coordArchiveWriteError(rel, err)
	}
	final := filepath.Join(dir, fmt.Sprintf("%08d-%s.json", state.Sequence+1, event.State))
	if err := os.Link(staged, final); err != nil {
		return zero, false, coordArchiveWriteError(rel, err)
	}
	state.Sequence++
	state.LastAt = event.At
	state.Tail = final
	if event.State == "absent" {
		at := event.At
		state.GraceStart = &at
	} else if event.State != "hold" {
		state.GraceStart = nil
	}
	if err := a.syncJournal(state); err != nil {
		return zero, false, coordArchiveWriteError(rel, err)
	}
	return state, true, nil
}

// Mark records present/reset for marked UIDs only. Validation and journal
// preflight cover the batch before writes; publication failures can leave a
// durable prefix, and retries safely skip/resync its already-cleared UIDs.
func (a CoordArchive) Mark(items []protocol.CoordArchiveMarkRequest) ([]protocol.CoordArchiveMarkResult, error) {
	if len(items) == 0 || len(items) > protocol.MaxCoordArchiveLookupItems {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "mark requires 1 to 1024 items")
	}
	paths := make([]string, len(items))
	for n, item := range items {
		rel, err := item.RecordDir()
		if err != nil {
			return nil, err
		}
		if err := item.Event.Validate(item.Resource); err != nil {
			return nil, err
		}
		if item.Event.State == "absent" {
			return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, rel, "only verified put can append absent")
		}
		paths[n] = rel
	}
	if err := a.CheckMounted(); err != nil {
		return nil, err
	}
	unlock, err := lockHistoryArchive(a.Root, true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	states := map[string]coordJournalState{}
	for n, item := range items {
		rel := paths[n]
		state, ok := states[rel]
		if !ok {
			state, err = a.readJournal(item.CoordArchiveIdentity, rel)
			if err != nil {
				return nil, err
			}
		}
		if state.GraceStart != nil && item.Event.At.Before(state.LastAt) {
			return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, rel, "backward event timestamp")
		}
		states[rel] = state
	}
	out := make([]protocol.CoordArchiveMarkResult, 0, len(items))
	for n, item := range items {
		rel := paths[n]
		state := states[rel]
		updated, appended, err := a.appendEvent(rel, state, item.Event)
		if err != nil {
			return nil, err
		}
		states[rel] = updated
		out = append(out, protocol.CoordArchiveMarkResult{CoordArchiveIdentity: item.CoordArchiveIdentity, Appended: appended, GraceStart: updated.GraceStart})
	}
	return out, nil
}
