package agentrun

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// The rejected-login marker. When the provider refuses the generation a pod
// applied while its local expiry is still ahead, the pod's agentd records that
// in ~/.agentd/claude-rejected.json (version, generation, observedAt: no token,
// no hash, no error text; internal/agentd/claude_rejection.go owns the file and
// its rules). auth status reads it, read-only and local. agent-run imports no
// agentd package: this is the reader's copy of the shape.
//
// Rules, which match agentd's readers (design choices, not proved prevention):
//   - a marker for the generation shown is Rejected, however old; age changes
//     only the sentence. The expired-token display takes over only when the
//     access token itself has expired (the row is then NeedsLogin already);
//   - a marker for an older generation is ignored: the newer one says nothing
//     has been observed yet, which is not "proved accepted";
//   - a marker that exists but cannot be read or believed (oversize, unknown
//     field, future-dated, not a regular file, a generation above the one shown)
//     makes the row Unknown, "cannot tell", never Ready; so does a marker with no
//     readable receipt of the generation agentd applied. The reference for "this
//     file cannot describe the generation shown" is that receipt (its generation
//     and mtime), never the Secret volume's mtime. agent-run never deletes or
//     writes the marker.

const (
	authRejected     = "Rejected"
	authRejectedFile = "claude-rejected.json"
	authReceiptFile  = "claude-access-generation.json"
	// These equal agentd's rejectionPendingWindow, rejectionSkew and rejectionMaxBytes.
	authRejectPending  = 10 * time.Minute
	authRejectSkew     = time.Minute
	authRejectMaxBytes = 4 << 10
)

type rejectionVerdict int

const (
	rejectNone rejectionVerdict = iota
	rejectRefused
	rejectCannotTell
)

// applyRejection merges the marker into the claude row. Precedence with the
// expiry states (a design choice, tested at the boundaries):
//   - access unexpired (row Ready): a marker for the generation shown makes the
//     row Rejected (exit 1); a marker that cannot be believed makes it Unknown;
//   - access expired (row Refreshing for the pending window, then NeedsLogin):
//     the expiry state and exit code stand, because the token cannot be used
//     whatever the provider said; the Reason still carries the rejection (or the
//     cannot-tell note), so a recorded rejection is never hidden by Refreshing;
//   - no marker, or one about another generation: the row is as without this file.
//
// A row is never Ready while a rejection or a cannot-tell marker applies.
func applyRejection(row *authRow, markerPath string, now time.Time) {
	if markerPath == "" || row.Generation == 0 {
		return
	}
	switch row.State {
	case authReady, authRefreshing, authNeedsLogin:
	default:
		return
	}
	verdict, m := readRejectionVerdict(row.Generation, markerPath, now)
	if verdict == rejectNone {
		return
	}
	age := max(0, now.Sub(m.ObservedAt)).Round(time.Second)
	if row.State != authReady {
		note := fmt.Sprintf("the provider had refused generation %d %s ago", m.Generation, age)
		if verdict == rejectCannotTell {
			note = "this pod's rejection record is unreadable or inconsistent, so it cannot tell whether the provider refused the keeper's login"
		}
		row.Reason += "; " + note
		return
	}
	if verdict == rejectCannotTell {
		cannotTellRejection(row)
		return
	}
	note := fmt.Sprintf("the provider refused generation %d %s ago; the keeper's next generation is pending; no sign-in is needed unless it stays unpublished", m.Generation, age)
	if age >= authRejectPending {
		note = fmt.Sprintf("the provider refused generation %d %s ago and no newer generation has arrived; check the keeper (a keeper delay, not a lapsed login)", m.Generation, age)
	}
	if row.Reason != "" {
		note += "; " + row.Reason
	}
	row.State, row.Reason, row.code = authRejected, note, 1
}

type rejectionMarker struct {
	Version    int       `json:"version"`
	Generation uint64    `json:"generation"`
	ObservedAt time.Time `json:"observedAt"`
}

// readRejectionVerdict reads the marker for the generation the row shows.
func readRejectionVerdict(shown uint64, markerPath string, now time.Time) (rejectionVerdict, rejectionMarker) {
	var m rejectionMarker
	fi, err := os.Lstat(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		return rejectNone, m
	}
	if err != nil {
		return rejectCannotTell, m
	}
	// The stable reference is agentd's receipt beside the marker (the generation
	// it applied and when). A Secret volume's mtimes are not stable: a pod restart
	// resets them.
	rg, rt, ok := readAppliedReceipt(filepath.Join(filepath.Dir(markerPath), authReceiptFile))
	if !ok {
		return rejectCannotTell, m
	}
	valid := fi.Mode().IsRegular() && fi.Size() <= authRejectMaxBytes
	if valid {
		raw, err := readAuthFile(markerPath)
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		valid = err == nil && uniqueMembers(raw) && dec.Decode(&m) == nil && !dec.More() && m.Version == 1 && m.Generation != 0 &&
			!m.ObservedAt.IsZero() && !m.ObservedAt.After(now.Add(authRejectSkew))
	}
	if !valid {
		// agentd has not applied the generation shown, so it cannot have written a
		// marker for it; or the file was last written before agentd applied the
		// generation it would describe.
		if shown > rg || fi.ModTime().Before(rt) {
			return rejectNone, m
		}
		return rejectCannotTell, m
	}
	switch {
	case m.Generation < shown:
		return rejectNone, m
	case m.Generation > shown:
		return rejectCannotTell, m
	}
	return rejectRefused, m
}

// readAppliedReceipt reads agentd's generation receipt: the generation applied
// and when. Only the generation member is decoded; nothing else is read or shown.
func readAppliedReceipt(path string) (gen uint64, at time.Time, ok bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return 0, time.Time{}, false
	}
	raw, err := readAuthFile(path)
	if err != nil {
		return 0, time.Time{}, false
	}
	var r struct {
		Generation uint64 `json:"generation"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Generation == 0 {
		return 0, time.Time{}, false
	}
	return r.Generation, fi.ModTime(), true
}

// uniqueMembers reports whether raw is one flat JSON object with no repeated
// member name, as agentd's strict decoder requires. The marker holds scalars only.
func uniqueMembers(raw []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for dec.More() {
		k, err := dec.Token()
		name, isStr := k.(string)
		if err != nil || !isStr || seen[name] {
			return false
		}
		seen[name] = true
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return false
		}
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return false
	}
	_, err := dec.Token()
	return errors.Is(err, io.EOF)
}

// cannotTellRejection: the pod's rejection record exists but cannot be believed.
func cannotTellRejection(row *authRow) {
	note := "this pod's rejection record is unreadable or inconsistent, so it cannot tell whether the provider refused the keeper's login"
	if row.Reason != "" {
		note += "; " + row.Reason
	}
	row.State, row.Reason = authUnknown, note
	if row.code != 1 {
		row.code = 2
	}
}
