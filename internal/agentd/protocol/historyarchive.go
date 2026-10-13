package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// What `agentd ctl history-export` (in the hold pod, over a private HOME) and
// `agentd ctl history-archive` (in the shelf) exchange with the operator
// (DESIGN-025 sections 6.3, 6.4 and 7). The operator pipes the export's tar
// stream into the archive's put, behind one line of content manifest.

// Formats.
const (
	// HistoryExportPlanFormat is the plan `history-export plan` prints.
	HistoryExportPlanFormat = "dev-env.history-export-plan.v1"
	// HistoryArchiveFormat is content.json's format.
	HistoryArchiveFormat = "dev-env.history-archive.v1"
	// HistoryArchiveEntryFormat is entry.json's format.
	HistoryArchiveEntryFormat = "dev-env.history-archive.entry.v1"
	// HistoryArchiveIndexFormat is an index entry's format.
	HistoryArchiveIndexFormat = "dev-env.history-archive.index.v1"
)

// The archive's layout under its mount, /home/dev/.history-archive.
const (
	// HistoryArchiveDirName is the archive's mount point, relative to HOME.
	HistoryArchiveDirName = ".history-archive"
	// HistoryArchiveIncoming holds writes in progress.
	HistoryArchiveIncoming = ".incoming"
	// HistoryArchiveEntries is v1/<project or _none>/<session UID>/<16 hex>/.
	HistoryArchiveEntries = "v1"
	// HistoryArchiveIndex is index/<project or _none>/<YYYY-MM>/<name>.json.
	HistoryArchiveIndex = "index"
	// HistoryNoProject stands for a Session that saved no project.
	HistoryNoProject = "_none"
	// HistoryContentFile, HistoryEntryFile, HistoryBlocksDir, HistoryFilesDir and
	// HistoryGitDir are an entry's parts.
	HistoryContentFile = "content.json"
	HistoryEntryFile   = "entry.json"
	HistoryBlocksDir   = "blocks"
	HistoryFilesDir    = "files"
	HistoryGitDir      = "git"
)

// History roots: the four places an export reads from, and the first segment
// of every exported file's archive path.
const (
	HistoryRootClaude = "claude"
	HistoryRootCodex  = "codex"
	HistoryRootAgentd = "agentd"
	HistoryRootLog    = "log"
)

// Providers a content manifest may name.
const (
	HistoryProviderClaude = "claude"
	HistoryProviderCodex  = "codex"
)

// Bounds (DESIGN-025 6.3 and 7).
const (
	HistoryBlockSize      = 1 << 20
	MaxHistoryExportBytes = 2 << 30
	MaxHistoryExportFiles = 100_000
	MaxHistoryFileBytes   = 512 << 20
	MaxHistoryPathDepth   = 64
	MaxHistoryPathBytes   = 1024
	MaxHistoryExcluded    = 100_000
	// MaxHistoryExcludedEntries is how many names under excluded entries a
	// plan stats to size them; past it, sizes are lower bounds.
	MaxHistoryExcludedEntries = 1_000_000
	MaxHistoryGitFiles        = 64
	MaxHistoryReadBytes       = 64 << 10
	MaxHistoryListLimit       = 100
	// MaxHistoryManifestPage bounds one page of `history-archive manifest`:
	// the files, then the excluded names, at most this many items together.
	MaxHistoryManifestPage = 1000
	MaxHistoryContentBytes = 64 << 20
	// HistoryIncomingMaxAge is how old debris in .incoming must be before a
	// put removes it: older than any put could still be running.
	HistoryIncomingMaxAge = time.Hour
)

// Export refusals: the operator counts each as dev_env_home_expiry_refused_total
// with the same reason (DESIGN-025 section 10).
const (
	HistoryErrLayoutUnknown    = "layout_unknown"
	HistoryErrUnknownEntry     = "unknown_entry"
	HistoryErrUnexpectedEntry  = "unexpected_entry"
	HistoryErrCredentialInTree = "credential_in_tree"
	HistoryErrTooLarge         = "export_too_large"
	HistoryErrChanged          = "export_changed"
	// HistoryErrIO is a read that failed: Unknown for the operator, which asks
	// again later.
	HistoryErrIO = "io_error"
)

// Archive errors (DESIGN-024's codes).
const (
	HistoryErrNotFound  = "not_found"
	HistoryErrConflict  = "conflict"
	HistoryErrCorrupt   = "corrupt"
	HistoryErrInvalid   = "invalid"
	HistoryErrNoArchive = "no_archive"
)

// HistoryError is the one JSON object every history command prints on stdout
// when it fails, with exit status 1. Path names the entry that refused, never
// its content.
type HistoryError struct {
	Code    string `json:"error"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

func (e *HistoryError) Error() string {
	if e.Path != "" {
		return e.Code + ": " + e.Path + ": " + e.Message
	}
	return e.Code + ": " + e.Message
}

// HistoryErrorf makes a HistoryError.
func HistoryErrorf(code, p, format string, args ...any) *HistoryError {
	return &HistoryError{Code: code, Path: p, Message: fmt.Sprintf(format, args...)}
}

// Exclusion classes: why a name is listed and not exported.
const (
	// HistoryClassNotHistory is configuration, cache or runtime state.
	HistoryClassNotHistory = "not_history"
	// HistoryClassUserCopy is a copy of user or workspace files under names
	// that say nothing about them (ADR-006): never exported.
	HistoryClassUserCopy = "user_copy"
	// HistoryClassCredential is a login; listed by name only, without a size.
	HistoryClassCredential = "credential"
	// HistoryClassLink is a symbolic link, never followed.
	HistoryClassLink = "link"
)

// HistoryRoot is one of the export's roots, relative to HOME.
type HistoryRoot struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
	// Absent: the directory does not exist, so nothing comes from it.
	Absent bool `json:"absent,omitempty"`
}

// HistoryFile is one exported file.
type HistoryFile struct {
	Root string `json:"root"`
	// Path is slash-separated, relative to the root.
	Path    string    `json:"path"`
	Bytes   int64     `json:"bytes"`
	Mode    uint32    `json:"mode"`
	ModTime time.Time `json:"modTime"`
	SHA256  string    `json:"sha256"`
	// Blocks are the SHA-256 of each HistoryBlockSize block, in the plan only;
	// the archive keeps them in blocks/<n>.json.
	Blocks []string `json:"blocks,omitempty"`
	// BlocksSHA256 is HistoryBlocksSHA256(Blocks).
	BlocksSHA256 string `json:"blocksSHA256"`
}

// Name is the file's archive name, <root>/<path>, as the tar stream carries it.
func (f HistoryFile) Name() string { return f.Root + "/" + f.Path }

// HistoryExcluded is one name the export listed and did not export.
type HistoryExcluded struct {
	Root  string `json:"root"`
	Path  string `json:"path"`
	Class string `json:"class"`
	// Bytes is the size of the entry's regular files; absent for a credential.
	Bytes int64 `json:"bytes,omitempty"`
	// Partial says the count stopped early: at its bound, or at a name it
	// could not list or stat. Bytes is then a lower bound.
	Partial bool `json:"partial,omitempty"`
}

// HistoryGit is the Git rescue the archive copies beside the history: the
// files of rescue/<session>/<stamp>/ on the shared volume.
type HistoryGit struct {
	// Rescue is <session>/<stamp>.
	Rescue string           `json:"rescue"`
	Files  []HistoryGitFile `json:"files"`
}

// HistoryGitFile is one file of a rescue directory.
type HistoryGitFile struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// HistoryExportPlan is what `history-export plan` prints: the files it would
// export, in the order the stream writes them, and every name it excluded.
type HistoryExportPlan struct {
	Format   string            `json:"format"`
	Session  string            `json:"session"`
	Roots    []HistoryRoot     `json:"roots"`
	Files    []HistoryFile     `json:"files"`
	Excluded []HistoryExcluded `json:"excluded"`
	Git      *HistoryGit       `json:"git,omitempty"`
	Bytes    int64             `json:"bytes"`
}

// HistorySession identifies the archived Session.
type HistorySession struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

// HistoryTerminal is the part of the Session's terminal record
// (status.privateHistory.executorTerminal) the archive keeps.
type HistoryTerminal struct {
	PodUID     string    `json:"podUID"`
	Phase      string    `json:"phase"`
	Reason     string    `json:"reason,omitempty"`
	NodeName   string    `json:"nodeName,omitempty"`
	ObservedAt time.Time `json:"observedAt"`
}

// The rescue verdicts that let a HOME go (DESIGN-025 6.2), as status.rescue
// spells them (api/v1alpha1 RescueResult). put refuses any other.
const (
	HistoryRescueVerified       = "Verified"
	HistoryRescueCleanAndPushed = "CleanAndPushed"
)

// HistoryRescue is the Git rescue the expiry relied on (status.rescue).
type HistoryRescue struct {
	Stamp  string `json:"stamp"`
	Result string `json:"result"`
	PodUID string `json:"podUID,omitempty"`
}

// HistoryContent is content.json, the first line of put's stdin: what the
// operator knows joined with the hold pod's plan. Its canonical bytes' SHA-256
// is the entry's contentSHA256. Nothing in it changes on a retry.
type HistoryContent struct {
	Format   string            `json:"format"`
	Session  HistorySession    `json:"session"`
	Project  string            `json:"project"`
	Provider string            `json:"provider"`
	HomeUID  string            `json:"homeUID"`
	Terminal HistoryTerminal   `json:"terminal"`
	Rescue   HistoryRescue     `json:"rescue"`
	Roots    []HistoryRoot     `json:"roots"`
	Files    []HistoryFile     `json:"files"`
	Excluded []HistoryExcluded `json:"excluded"`
	Git      *HistoryGit       `json:"git,omitempty"`
	Bytes    int64             `json:"bytes"`
	// PlanSHA256 is the digest of the plan this content was made from.
	PlanSHA256 string `json:"planSHA256"`
}

// HistoryEntry is entry.json: what changes on a retry, kept out of the digest.
type HistoryEntry struct {
	Format        string    `json:"format"`
	ContentSHA256 string    `json:"contentSHA256"`
	ArchivedAt    time.Time `json:"archivedAt"`
	ArchivedBy    string    `json:"archivedBy"`
}

// HistoryIndexEntry is one file under index/.
type HistoryIndexEntry struct {
	Format        string    `json:"format"`
	Project       string    `json:"project"`
	Session       string    `json:"session"`
	SessionUID    string    `json:"sessionUID"`
	ContentSHA256 string    `json:"contentSHA256"`
	ArchivedAt    time.Time `json:"archivedAt"`
	// Entry is the entry's directory, relative to the archive's root.
	Entry string `json:"entry"`
}

// HistoryPutResult is what `history-archive put` prints.
type HistoryPutResult struct {
	// Status is "stored" or "exists".
	Status        string `json:"status"`
	ContentSHA256 string `json:"contentSHA256"`
	Entry         string `json:"entry"`
	Bytes         int64  `json:"bytes"`
	Files         int    `json:"files"`
	GitFiles      int    `json:"gitFiles"`
}

// Put statuses.
const (
	HistoryPutStored = "stored"
	HistoryPutExists = "exists"
)

// HistoryVerifyResult is what `history-archive verify` prints for an entry
// whose every byte matched.
type HistoryVerifyResult struct {
	ContentSHA256 string `json:"contentSHA256"`
	Entry         string `json:"entry"`
	Files         int    `json:"files"`
	GitFiles      int    `json:"gitFiles"`
	Bytes         int64  `json:"bytes"`
}

// HistoryGetResult is what `history-archive get` prints.
type HistoryGetResult struct {
	ContentSHA256 string         `json:"contentSHA256"`
	Entry         HistoryEntry   `json:"entry"`
	Content       HistoryContent `json:"content"`
}

// HistorySummary is one item of `history-archive list`.
type HistorySummary struct {
	SessionUID    string           `json:"sessionUID"`
	Session       string           `json:"session,omitempty"`
	Provider      string           `json:"provider,omitempty"`
	ArchivedAt    time.Time        `json:"archivedAt"`
	ContentSHA256 string           `json:"contentSHA256"`
	Bytes         int64            `json:"bytes,omitempty"`
	Files         int              `json:"files,omitempty"`
	Terminal      *HistoryTerminal `json:"terminal,omitempty"`
	// Error is "corrupt" for an entry that failed its check; nothing else of
	// it is printed then.
	Error string `json:"error,omitempty"`
}

// HistoryList is one page of `history-archive list`, newest first.
type HistoryList struct {
	Project string           `json:"project"`
	Items   []HistorySummary `json:"items"`
	// Next is the cursor for the next page, empty on the last.
	Next string `json:"next,omitempty"`
}

// HistoryManifestPage is one page of an entry's verified manifest
// (`history-archive manifest`, DESIGN-025 8): everything content.json holds
// but the file and excluded-name lists, which come at most
// MaxHistoryManifestPage items a page. The items are one sequence, the files
// in the manifest's order and then the excluded names; Offset and Next count
// in it. So no reader holds a whole content.json to show part of it.
type HistoryManifestPage struct {
	ContentSHA256 string          `json:"contentSHA256"`
	Entry         HistoryEntry    `json:"entry"`
	Session       HistorySession  `json:"session"`
	Project       string          `json:"project"`
	Provider      string          `json:"provider"`
	HomeUID       string          `json:"homeUID"`
	Terminal      HistoryTerminal `json:"terminal"`
	Rescue        HistoryRescue   `json:"rescue"`
	Roots         []HistoryRoot   `json:"roots"`
	Git           *HistoryGit     `json:"git,omitempty"`
	Bytes         int64           `json:"bytes"`
	PlanSHA256    string          `json:"planSHA256"`
	FileCount     int             `json:"fileCount"`
	ExcludedCount int             `json:"excludedCount"`
	// Offset is this page's first item.
	Offset int `json:"offset"`
	// Next is the next page's offset; 0 when no item follows this page.
	Next     int               `json:"next,omitempty"`
	Files    []HistoryFile     `json:"files"`
	Excluded []HistoryExcluded `json:"excluded"`
}

// HistoryReadResult is one page of one archived file.
type HistoryReadResult struct {
	ContentSHA256 string `json:"contentSHA256"`
	File          string `json:"file"`
	FileSHA256    string `json:"fileSHA256"`
	Bytes         int64  `json:"bytes"`
	Offset        int64  `json:"offset"`
	Next          int64  `json:"next"`
	EOF           bool   `json:"eof"`
	// Data is base64, at most MaxHistoryReadBytes before encoding.
	Data []byte `json:"data"`
}

var (
	hexSHA256   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	rescueStamp = regexp.MustCompile(`^[0-9]{8}-[0-9]{4}$`)
	kubeUID     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	gitFileName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,255}$`)
)

// historyCursor is a list cursor: an index file's name below its project,
// <YYYY-MM>/<archivedAt>-<session UID>-<16 hex>.json.
var historyCursor = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}/[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[0-9a-f-]{36}-[0-9a-f]{16}\.json$`)

// ValidHistoryCursor reports whether s is a cursor `history-archive list`
// prints as next.
func ValidHistoryCursor(s string) bool { return historyCursor.MatchString(s) }

// ValidSHA256 reports whether s is 64 lowercase hex digits.
func ValidSHA256(s string) bool { return hexSHA256.MatchString(s) }

// ValidRescueStamp reports whether s is a rescue report's stamp, UTC
// YYYYMMDD-HHMM, without the -N its directory may add.
func ValidRescueStamp(s string) bool { return rescueStamp.MatchString(s) }

// ValidKubeUID reports whether s is a Kubernetes UID.
func ValidKubeUID(s string) bool { return kubeUID.MatchString(s) }

// HistoryProjectDir is the directory name a project's entries and index use.
func HistoryProjectDir(project string) string {
	if project == "" {
		return HistoryNoProject
	}
	return project
}

// ValidHistoryProject reports whether a project can name an archive
// directory: empty (no project) or a DNS label.
func ValidHistoryProject(project string) bool {
	return project == "" || ValidSessionName(project)
}

// HistoryEntryDir is an entry's directory relative to the archive's root.
func HistoryEntryDir(project, sessionUID, contentSHA256 string) string {
	return path.Join(HistoryArchiveEntries, HistoryProjectDir(project), sessionUID, contentSHA256[:16])
}

// HistoryBlocksSHA256 is the digest of a block list: the SHA-256 of its
// canonical JSON, ["<hex>",...], or of [] for an empty file.
func HistoryBlocksSHA256(blocks []string) string {
	if blocks == nil {
		blocks = []string{}
	}
	b, _ := CanonicalJSON(blocks)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// CanonicalSHA256 is the hex SHA-256 of v's canonical JSON, and those bytes.
func CanonicalSHA256(v any) (string, []byte, error) {
	b, err := CanonicalJSON(v)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), b, nil
}

// Credential names (DESIGN-025 6.3). A name is compared without case.
var (
	credentialNames = map[string]bool{
		"auth.json": true, ".credentials.json": true, "credentials.json": true,
		"access.json": true, ".git-credentials": true, ".netrc": true,
		"kubeconfig": true, "gh_token": true, "hosts.yml": true,
		"dev-env-hw": true, ".env": true,
	}
	credentialPrefixes = []string{
		"auth.json.", ".credentials.json.", "id_rsa", "id_dsa", "id_ecdsa",
		"id_ed25519", ".env.",
	}
	credentialSuffixes = []string{".pem", ".key", ".p12", ".pfx"}
)

// IsCredentialName reports whether a base name is a credential's by DESIGN-025
// 6.3's name rules: it refuses a whole export, and a put.
func IsCredentialName(base string) bool {
	n := strings.ToLower(base)
	if credentialNames[n] {
		return true
	}
	for _, p := range credentialPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	for _, s := range credentialSuffixes {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return false
}

// CheckHistoryPath checks one slash path relative to a root: valid UTF-8, not
// empty, not absolute, no empty, "." or ".." segment, within the depth and
// length bounds, and no segment with a credential name. It returns the
// HistoryError code.
func CheckHistoryPath(p string) (code, why string) {
	if !utf8.ValidString(p) {
		return HistoryErrInvalid, "not UTF-8"
	}
	if p == "" || strings.HasPrefix(p, "/") || len(p) > MaxHistoryPathBytes || strings.ContainsRune(p, 0) || strings.Contains(p, "\\") {
		return HistoryErrInvalid, "not a relative slash path within the length bound"
	}
	segs := strings.Split(p, "/")
	if len(segs) > MaxHistoryPathDepth {
		return HistoryErrInvalid, "deeper than the bound"
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return HistoryErrInvalid, "has an empty, . or .. segment"
		}
		if IsCredentialName(s) {
			return HistoryErrCredentialInTree, "a segment is a credential's name"
		}
	}
	return "", ""
}

// ValidHistoryRoot reports whether name is one of the four roots.
func ValidHistoryRoot(name string) bool {
	switch name {
	case HistoryRootClaude, HistoryRootCodex, HistoryRootAgentd, HistoryRootLog:
		return true
	}
	return false
}

// ValidHistoryGitFileName reports whether a rescue file name can be copied:
// a plain base name.
func ValidHistoryGitFileName(name string) bool {
	return gitFileName.MatchString(name) && name != "." && name != ".."
}

// SortHistoryFiles orders files as the plan and the stream do: by root, then
// by path, bytewise.
func SortHistoryFiles(files []HistoryFile) {
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
}

// HistoryContentFromPlan copies the plan's part of a content manifest: the
// roots, files (without their block lists), exclusions, Git files and size,
// and the plan's digest.
func HistoryContentFromPlan(c *HistoryContent, plan HistoryExportPlan, planSHA256 string) {
	c.Roots = append([]HistoryRoot(nil), plan.Roots...)
	c.Files = make([]HistoryFile, len(plan.Files))
	for i, f := range plan.Files {
		f.Blocks = nil
		c.Files[i] = f
	}
	c.Excluded = append([]HistoryExcluded(nil), plan.Excluded...)
	if plan.Git != nil {
		g := *plan.Git
		g.Files = append([]HistoryGitFile(nil), plan.Git.Files...)
		c.Git = &g
	}
	c.Bytes = plan.Bytes
	c.PlanSHA256 = planSHA256
}
