package agentd

import (
	"bytes"
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	claudeAuthChildBatch   = 64
	claudeAuthChildEntries = 4096
	claudeAuthChildFiles   = 32
	claudeAuthChildTail    = 16 << 10
)

// Precision is the width of the timestamp's final recorded digit.
type claudeAuthTime struct {
	At             time.Time `json:"at"`
	PrecisionNanos int64     `json:"precisionNanos"`
}

func (t claudeAuthTime) IsZero() bool { return t.At.IsZero() }

func (t claudeAuthTime) end() time.Time {
	return t.At.Add(time.Duration(t.PrecisionNanos))
}

type claudeAuthObservation struct {
	Stamp     claudeAuthTime
	Sidechain bool
	Rejected  bool
	Count     uint8
}

// Native child transcripts live under the launch's conversation directory.
// Cap the scan and read only bounded tails from the newest children it finds.
// A separately launched process has no binding here.
func readClaudeAuthObservations(s Settings, l Launch, now time.Time) []claudeAuthObservation {
	if !conversationIDPattern.MatchString(l.ConversationID) || !filepath.IsAbs(l.Dir) {
		return nil
	}
	project := filepath.Join(s.ClaudeConfigDir, "projects", claudeProjectKey(l.Dir))
	dir, err := portalOpenDirectory(project)
	if err != nil {
		return nil
	}
	defer func() { _ = dir.Close() }()
	parent := readClaudeAuthTail(dir, l.ConversationID+".jsonl", transcriptTailBytes)
	observations := appendClaudeAuthObservations(nil, parent, l.CreatedAt, now, false)

	children, err := portalOpenDirectory(filepath.Join(project, l.ConversationID, "subagents"))
	if err != nil {
		return observations
	}
	defer func() { _ = children.Close() }()
	type childTranscript struct {
		name     string
		modified time.Time
	}
	var recent []childTranscript
	for remaining := claudeAuthChildEntries; remaining > 0; {
		entries, readErr := children.ReadDir(min(remaining, claudeAuthChildBatch))
		remaining -= len(entries)
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") || !entry.Type().IsRegular() {
				continue
			}
			var st unix.Stat_t
			if unix.Fstatat(int(children.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT == unix.S_IFREG {
				recent = append(recent, childTranscript{name, time.Unix(st.Mtim.Sec, st.Mtim.Nsec)})
			}
		}
		slices.SortFunc(recent, func(a, b childTranscript) int {
			if byTime := b.modified.Compare(a.modified); byTime != 0 {
				return byTime
			}
			return cmp.Compare(a.name, b.name)
		})
		recent = recent[:min(len(recent), claudeAuthChildFiles)]
		if readErr != nil {
			break
		}
	}
	// Two native files can report rejections at the same instant. Count
	// each source once at that timestamp and retain no source identifiers.
	byTimestamp := make(map[time.Time]claudeAuthObservation)
	seen := make(map[time.Time]bool)
	for _, child := range recent {
		tail := readClaudeAuthTail(children, child.name, claudeAuthChildTail)
		clear(seen)
		for _, o := range appendClaudeAuthObservations(nil, tail, l.CreatedAt, now, true) {
			at := o.Stamp.At
			if seen[at] {
				continue
			}
			seen[at] = true
			merged := byTimestamp[at]
			if o.Stamp.PrecisionNanos > merged.Stamp.PrecisionNanos {
				o.Count = merged.Count
				merged = o
			}
			merged.Count++
			byTimestamp[at] = merged
		}
	}
	sidechains := make([]claudeAuthObservation, 0, len(byTimestamp))
	for _, o := range byTimestamp {
		sidechains = append(sidechains, o)
	}
	slices.SortFunc(sidechains, func(a, b claudeAuthObservation) int { return a.Stamp.At.Compare(b.Stamp.At) })
	observations = append(observations, sidechains...)
	return observations
}

func readClaudeAuthTail(dir *os.File, name string, limit int64) []byte {
	fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Sys().(*syscall.Stat_t).Nlink != 1 {
		return nil
	}
	raw, _ := boundedTranscriptTail(f, fi.Size(), limit)
	return raw
}

func appendClaudeAuthObservations(observations []claudeAuthObservation, tail []byte, launchAt, now time.Time, nativeChild bool) []claudeAuthObservation {
	for line := range bytes.SplitSeq(tail, []byte("\n")) {
		var e transcriptEntry
		if json.Unmarshal(line, &e) != nil || e.Type != "assistant" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, e.Timestamp)
		if err != nil || at.Before(launchAt) {
			continue
		}
		stamp := claudeAuthTime{At: at.UTC(), PrecisionNanos: claudeTimestampPrecision(e.Timestamp)}
		// A future-stamped response could move across a credential write on
		// the next poll. Wait until the full recorded interval has elapsed.
		if stamp.end().After(now) {
			continue
		}
		sidechain := nativeChild || e.IsSidechain
		rejected := e.IsAPIError && (string(e.APIErrorStatus) == "401" || string(e.Error) == `"`+authErrorCode+`"`)
		// Only structured rejection evidence is needed from a sidechain.
		// Acceptance needs a parent response with model and usage evidence.
		if !rejected && (sidechain || e.IsAPIError || len(e.APIErrorStatus) != 0 || len(e.Error) != 0 || !acceptedResponse(e.Message)) {
			continue
		}
		observations = append(observations, claudeAuthObservation{Stamp: stamp, Sidechain: sidechain, Rejected: rejected, Count: 1})
	}
	return observations
}

func claudeTimestampPrecision(timestamp string) int64 {
	precision := int64(time.Second)
	if i := strings.IndexAny(timestamp, ".,"); i >= 0 {
		for _, digit := range timestamp[i+1:] {
			if digit < '0' || digit > '9' || precision == 1 {
				break
			}
			precision /= 10
		}
	}
	return precision
}
