package apiv1

import "time"

// HistoryPath serves the archived provider history of private Sessions whose
// HOME expired (DESIGN-025 8): GET HistoryPath?project=P lists one project's
// entries, GET HistoryEntryPath(uid) is one entry's manifest in pages, and GET
// HistoryFilePath(uid) one page of one archived file. Every item and answer
// says archived: true. The routes are new, so no older client decodes them;
// fields may be added, and a client ignores what it does not know.
const HistoryPath = "/v1/history"

// HistoryEntryPath is one Session's archived history.
func HistoryEntryPath(sessionUID string) string { return HistoryPath + "/" + sessionUID }

// HistoryFilePath is one page of one file of a Session's archived history.
func HistoryFilePath(sessionUID string) string { return HistoryEntryPath(sessionUID) + "/file" }

// Page bounds of the history routes.
const (
	// DefaultHistoryListLimit and MaxHistoryListLimit bound a list page.
	DefaultHistoryListLimit = 20
	MaxHistoryListLimit     = 100
	// DefaultHistoryManifestLimit and MaxHistoryManifestLimit bound the files
	// and excluded names of one manifest page, together.
	DefaultHistoryManifestLimit = 100
	MaxHistoryManifestLimit     = 1000
	// MaxHistoryFilePage is the most bytes of a file one page carries, before
	// base64.
	MaxHistoryFilePage = 64 << 10
)

// HistoryCorrupt is a list item's Error when the entry failed its check; the
// item then carries its UID, time and digest only.
const HistoryCorrupt = "corrupt"

// HistoryTerminal is the terminal record the archived HOME's expiry rested
// on: the executor Pod, its terminal phase and reason, and when the operator
// observed it.
type HistoryTerminal struct {
	PodUID     string    `json:"podUID"`
	Phase      string    `json:"phase"`
	Reason     string    `json:"reason,omitempty"`
	NodeName   string    `json:"nodeName,omitempty"`
	ObservedAt time.Time `json:"observedAt"`
}

// HistorySummary is one archived history in a list.
type HistorySummary struct {
	// Archived is always true: the Session's HOME is gone or going, and this
	// is the archive's copy.
	Archived      bool             `json:"archived"`
	ArchivedAt    time.Time        `json:"archivedAt"`
	Project       string           `json:"project"`
	Session       string           `json:"session,omitempty"`
	SessionUID    string           `json:"sessionUID"`
	Provider      string           `json:"provider,omitempty"`
	ContentSHA256 string           `json:"contentSHA256"`
	Bytes         int64            `json:"bytes,omitempty"`
	Files         int              `json:"files,omitempty"`
	Terminal      *HistoryTerminal `json:"terminal,omitempty"`
	// Error is HistoryCorrupt for an entry that failed its check.
	Error string `json:"error,omitempty"`
}

// HistoryList is one page of a project's archived histories, newest first.
type HistoryList struct {
	Archived bool             `json:"archived"`
	Project  string           `json:"project"`
	Items    []HistorySummary `json:"items"`
	// Continue is the next page's cursor; empty on the last page.
	Continue string `json:"continue,omitempty"`
}

// HistorySession names the archived Session.
type HistorySession struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

// HistoryRescue is the Git rescue the expiry relied on.
type HistoryRescue struct {
	Stamp  string `json:"stamp"`
	Result string `json:"result"`
	PodUID string `json:"podUID,omitempty"`
}

// HistoryRoot is one exported root (claude, codex, agentd or log) and its
// directory relative to HOME; Absent when the directory did not exist.
type HistoryRoot struct {
	Name   string `json:"name"`
	Dir    string `json:"dir"`
	Absent bool   `json:"absent,omitempty"`
}

// HistoryFile is one archived file. Its path in the file route is
// Root + "/" + Path.
type HistoryFile struct {
	Root    string    `json:"root"`
	Path    string    `json:"path"`
	Bytes   int64     `json:"bytes"`
	SHA256  string    `json:"sha256"`
	ModTime time.Time `json:"modTime"`
}

// HistoryExcluded is a name the export listed and did not archive: not
// history, a copy of user files, a credential (its name only) or a link.
type HistoryExcluded struct {
	Root    string `json:"root"`
	Path    string `json:"path"`
	Class   string `json:"class"`
	Bytes   int64  `json:"bytes,omitempty"`
	Partial bool   `json:"partial,omitempty"`
}

// HistoryGit names the Git rescue's files archived beside the history. Their
// content is not served: code comes back through a rescue restore.
type HistoryGit struct {
	Rescue string           `json:"rescue"`
	Files  []HistoryGitFile `json:"files"`
}

// HistoryGitFile is one file of the archived Git rescue.
type HistoryGitFile struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// HistoryManifest is one page of an archived history's manifest. Every page
// carries the identity, the terminal record, the rescue and the counts; Files
// and Excluded hold at most the page's limit together, the files first.
type HistoryManifest struct {
	Archived      bool            `json:"archived"`
	ArchivedAt    time.Time       `json:"archivedAt"`
	ContentSHA256 string          `json:"contentSHA256"`
	Session       HistorySession  `json:"session"`
	Project       string          `json:"project"`
	Provider      string          `json:"provider"`
	HomeUID       string          `json:"homeUID"`
	Terminal      HistoryTerminal `json:"terminal"`
	Rescue        HistoryRescue   `json:"rescue"`
	Roots         []HistoryRoot   `json:"roots"`
	Git           *HistoryGit     `json:"git,omitempty"`
	// Bytes is the archived files' total size.
	Bytes         int64             `json:"bytes"`
	FileCount     int               `json:"fileCount"`
	ExcludedCount int               `json:"excludedCount"`
	Offset        int               `json:"offset"`
	Files         []HistoryFile     `json:"files"`
	Excluded      []HistoryExcluded `json:"excluded"`
	// Continue is the next page's cursor; empty on the last page.
	Continue string `json:"continue,omitempty"`
}

// HistoryFilePage is one page of one archived file. Every byte in it was
// checked against the archive's block hashes before it was sent.
type HistoryFilePage struct {
	Archived      bool      `json:"archived"`
	ArchivedAt    time.Time `json:"archivedAt"`
	SessionUID    string    `json:"sessionUID"`
	ContentSHA256 string    `json:"contentSHA256"`
	Path          string    `json:"path"`
	FileSHA256    string    `json:"fileSHA256"`
	Bytes         int64     `json:"bytes"`
	Offset        int64     `json:"offset"`
	// Next is the offset of the next page; EOF says there is none.
	Next int64 `json:"next"`
	EOF  bool  `json:"eof"`
	// Data is base64 in JSON, at most MaxHistoryFilePage bytes.
	Data []byte `json:"data"`
}
