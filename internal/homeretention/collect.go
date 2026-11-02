package homeretention

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxCollectRepositories = 32
	maxCollectWorktrees    = 32
	maxCollectRefs         = 512
	maxCollectStashes      = 128
	maxCollectStatus       = 4096
	maxCollectArtifacts    = 64
	maxCollectDepth        = 3
	maxGitCalls            = 2000
	maxGitOutputBytes      = 1 << 20
	maxGitCommand          = 30 * time.Second

	forEachRefFormat = "%(objectname)%00%(objecttype)%00%(*objectname)%00%(refname)"
)

var (
	localConfigArgs    = []string{"config", "--list", "--local", "--show-origin", "--null"}
	worktreeConfigArgs = []string{"config", "--list", "--worktree", "--show-origin", "--null"}
	indexFlagArgs      = []string{"ls-files", "-v", "-z"}
	gitlinkIndexArgs   = []string{"ls-files", "-s", "-z"}
	headTreeArgs       = []string{"ls-tree", "-r", "-z", "HEAD"}
	// statusArgs is the only allowlisted status. --ignore-submodules=all stops
	// Git from starting a descendant status for an index gitlink. Checked
	// descendant status is a separate command after that descendant's config check.
	statusArgs = []string{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=all"}
)

// RepositorySpec is one caller-selected repository. Identity is an
// authenticated repository identifier supplied by the caller. The collector
// does not invent one from a remote URL or a display name.
type RepositorySpec struct {
	Identity string
	Path     string
}

// ArtifactInput is an explicit required artifact. The collector does not scan
// HOME or provider history to discover artifacts or their bytes.
type ArtifactInput struct {
	ID     string
	Kind   string
	SHA256 string
}

// PublicationQuery is one local ref just read from the repository.
type PublicationQuery struct {
	Binding    Binding
	Repository string
	Ref        Ref
}

// PublicationSource is an existing authenticated push or remote-query boundary.
// A nil receipt means that boundary finished and did not prove publication. An
// error, or a receipt that does not verify for this exact object, fails closed.
// Implementations must not consult Git remote-tracking refs.
type PublicationSource interface {
	Query(ctx context.Context, query PublicationQuery) (*Signed[PublicationPayload], error)
}

// DurableCopySource is an existing authenticated durable-copy boundary. A nil
// receipt means no verified copy was returned.
type DurableCopySource interface {
	Query(ctx context.Context, binding Binding, artifact Artifact) (*Signed[DurableCopyPayload], error)
}

// HomeRevisionSource re-reads the exact HOME revision. Any change between
// reads fails the collection.
type HomeRevisionSource interface {
	Current(ctx context.Context) (string, error)
}

// GitRunner executes one allowlisted read-only Git command.
type GitRunner interface {
	Run(ctx context.Context, dir string, args []string) (stdout string, exitCode int, err error)
}

// CollectRequest is the whole local collection. Requirements and artifacts are
// explicit. SubmoduleIdentities maps SubmoduleIdentityKey to a repository
// identity. Nil publication or durable-copy sources attach no proof.
type CollectRequest struct {
	Binding             Binding
	Repositories        []RepositorySpec
	Requirements        Requirements
	Artifacts           []ArtifactInput
	SubmoduleIdentities map[string]string
	Verifier            EvidenceVerifier
	Publications        PublicationSource
	DurableCopies       DurableCopySource
}

// SubmoduleIdentityKey binds an initialized submodule path to its parent
// repository identity. The path uses forward slashes and is relative to the
// parent worktree.
func SubmoduleIdentityKey(parentIdentity, relativePath string) string {
	return parentIdentity + "\n" + relativePath
}

// ExecGit runs read-only Git with a private empty HOME, hooks and network
// protocols disabled, and no caller environment. Close removes only that
// private directory.
type ExecGit struct{ home string }

// NewExecGit creates the private HOME used to avoid the process owner's HOME.
func NewExecGit() (*ExecGit, error) {
	home, err := os.MkdirTemp("", "homeretention-git-home-")
	if err != nil {
		return nil, fmt.Errorf("preservation incomplete: %w", err)
	}
	return &ExecGit{home: home}, nil
}

// Close removes the private HOME created for Git. It does not remove a
// repository, cache, or any other directory.
func (g *ExecGit) Close() error {
	if g == nil || g.home == "" {
		return nil
	}
	if !strings.HasPrefix(filepath.Base(g.home), "homeretention-git-home-") {
		return errors.New("refusing to remove an unexpected git home")
	}
	return os.RemoveAll(g.home)
}

// Run executes an allowlisted Git command. Every command is bounded by the
// shorter of the caller's remaining time and 30 seconds, including when the
// caller already set a longer deadline. Output beyond the byte bound fails
// closed. Safety settings are passed on the command line and through
// GIT_CONFIG_COUNT so a nested Git process inherits them.
func (g *ExecGit) Run(ctx context.Context, dir string, args []string) (string, int, error) {
	if g == nil || g.home == "" {
		return "", -1, errors.New("preservation incomplete: git runner is not initialized")
	}
	if err := allowedReadOnlyGit(args); err != nil {
		return "", -1, err
	}
	bounded, cancel, err := boundGitContext(ctx)
	if err != nil {
		return "", -1, err
	}
	defer cancel()
	full := append([]string{"-C", dir, "--no-optional-locks"}, gitSafetyArgs()...)
	full = append(full, args...)
	cmd := exec.CommandContext(bounded, "git", full...)
	cmd.Env = gitCommandEnv(g.home)
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = maxGitOutputBytes, 64<<10
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if stdout.overflow || stderr.overflow {
		return "", -1, errors.New("preservation incomplete: git output exceeded bound")
	}
	if err == nil {
		return stdout.buf.String(), 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.buf.String(), exitErr.ExitCode(), nil
	}
	return "", -1, fmt.Errorf("preservation incomplete: git: %w", err)
}

// boundGitContext applies the per-command limit. A longer caller deadline does
// not extend the command. A shorter caller deadline is kept.
func boundGitContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, errors.New("context required")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	limit := maxGitCommand
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			return nil, nil, context.DeadlineExceeded
		}
		if remaining < limit {
			limit = remaining
		}
	}
	child, cancel := context.WithTimeout(ctx, limit)
	return child, cancel, nil
}

func gitSafetyConfig() [][2]string {
	return [][2]string{
		{"protocol.allow", "never"},
		{"gc.auto", "0"},
		{"maintenance.auto", "false"},
		{"core.hooksPath", "/dev/null"},
		{"core.fsmonitor", "false"},
		{"core.sshCommand", ""},
		{"core.askPass", ""},
		{"credential.helper", ""},
		{"submodule.recurse", "false"},
		{"status.submoduleSummary", "false"},
		{"fetch.recurseSubmodules", "false"},
		{"diff.external", ""},
		{"core.useReplaceRefs", "false"},
		{"advice.detachedHead", "false"},
	}
}

func gitSafetyArgs() []string {
	pairs := gitSafetyConfig()
	args := make([]string, 0, len(pairs)*2)
	for _, pair := range pairs {
		args = append(args, "-c", pair[0]+"="+pair[1])
	}
	return args
}

func gitCommandEnv(home string) []string {
	pairs := gitSafetyConfig()
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=" + strconv.Itoa(len(pairs)),
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"PAGER=cat",
		"LC_ALL=C",
	}
	for index, pair := range pairs {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", index, pair[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", index, pair[1]))
	}
	return env
}

type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len() >= b.limit {
		b.overflow = true
		return len(p), nil
	}
	if b.buf.Len()+len(p) > b.limit {
		b.overflow = true
		_, _ = b.buf.Write(p[:b.limit-b.buf.Len()])
		return len(p), nil
	}
	return b.buf.Write(p)
}

// CollectPreservation reads a complete local inventory for the exact binding.
// It lists worktrees, branches, tags, detached HEADs, stashes, and dirty,
// untracked, nested, and submodule state. It does not fetch, checkout, reset,
// stash, or otherwise modify a repository. Publication proof comes only from
// the injected boundary. FetchSucceeded is left false and is not publication
// evidence. Unsupported repository config is refused before a command that can
// run a hook, filter, diff driver, or helper. Descendant submodule config is
// scanned before status or index inspection. Every status command passes
// --ignore-submodules=all, so Git does not start a descendant status from a
// gitlink. An index gitlink whose path is missing from .gitmodules fails
// closed before that status, including when .gitmodules is absent. A later
// submodule error cannot undo a filter that already ran. Checked child status
// is collected separately after that child's config check. core.worktree is
// unsupported, so status and the nested walk cannot observe different trees.
// Submodule directories are taken from Git's file-scoped config parse of
// .gitmodules before submodule status. That status lists immediate children;
// a nested checkout is queued from the child repository and still stops at
// maxCollectDepth. Index entries whose ls-files -v tag is not ordinary
// cached H fail closed. Those reads are not an atomic snapshot and take no lock.
func CollectPreservation(ctx context.Context, req CollectRequest, git GitRunner, revision HomeRevisionSource) (Inventory, error) {
	if ctx == nil {
		return Inventory{}, errors.New("context required")
	}
	if err := ctx.Err(); err != nil {
		return Inventory{}, err
	}
	if revision == nil {
		return Inventory{}, errors.New("home revision source required")
	}
	if err := validateCollectRequest(req); err != nil {
		return Inventory{}, err
	}
	if len(req.Repositories) > 0 && git == nil {
		return Inventory{}, errors.New("git runner required")
	}
	start, err := readRevision(ctx, revision, req.Binding.HomeRevision)
	if err != nil {
		return Inventory{}, err
	}
	c := &collector{run: git}
	found, err := c.discover(ctx, req)
	if err != nil {
		return Inventory{}, err
	}
	if _, err := readRevision(ctx, revision, start); err != nil {
		return Inventory{}, err
	}
	inventory, err := c.assemble(ctx, req, found)
	if err != nil {
		return Inventory{}, err
	}
	if err := c.confirm(ctx, found); err != nil {
		return Inventory{}, err
	}
	if _, err := readRevision(ctx, revision, start); err != nil {
		return Inventory{}, err
	}
	return inventory, nil
}

func readRevision(ctx context.Context, source HomeRevisionSource, want string) (string, error) {
	got, err := source.Current(ctx)
	if err != nil {
		return "", fmt.Errorf("preservation incomplete: %w", err)
	}
	if got != want || !validText(got) {
		return "", errors.New("preservation race: home revision changed")
	}
	return got, nil
}

func validateCollectRequest(req CollectRequest) error {
	if !validBinding(req.Binding) {
		return errors.New("binding is not exact")
	}
	if req.Repositories == nil {
		return errors.New("repositories must be explicit")
	}
	if len(req.Repositories) > maxCollectRepositories {
		return errors.New("preservation incomplete: repository bound exceeded")
	}
	if req.Artifacts == nil {
		return errors.New("artifacts must be explicit")
	}
	if len(req.Artifacts) > maxCollectArtifacts {
		return errors.New("preservation incomplete: artifact bound exceeded")
	}
	values := []string{req.Requirements.Handoff, req.Requirements.Result, req.Requirements.History, req.Requirements.Recovery}
	if !validText(req.Requirements.Source) || !validText(req.Requirements.Revision) {
		return errors.New("requirements are not explicit")
	}
	for _, value := range values {
		if value != "none" && value != "required" && value != "unknown" {
			return errors.New("requirements are not explicit")
		}
	}
	requirement := map[string]string{"handoff": req.Requirements.Handoff, "result": req.Requirements.Result, "history": req.Requirements.History, "recovery": req.Requirements.Recovery}
	seenRepo, seenPath, seenArtifact := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, repo := range req.Repositories {
		if !validText(repo.Identity) || seenRepo[repo.Identity] {
			return errors.New("preservation incomplete: repository identity invalid or duplicate")
		}
		seenRepo[repo.Identity] = true
		if err := usableDir(repo.Path); err != nil {
			return err
		}
		if seenPath[repo.Path] {
			return errors.New("preservation incomplete: repository path duplicated")
		}
		seenPath[repo.Path] = true
	}
	for _, artifact := range req.Artifacts {
		if !validText(artifact.ID) || seenArtifact[artifact.ID] || !validDigest(artifact.SHA256) {
			return errors.New("preservation incomplete: artifact identity or digest invalid")
		}
		seenArtifact[artifact.ID] = true
		if requirement[artifact.Kind] != "required" {
			return errors.New("preservation incomplete: artifact requirement inconsistent")
		}
	}
	return nil
}

type collector struct {
	run   GitRunner
	calls int
}

type queuedRepo struct {
	Identity string
	Path     string
	Depth    int
}

type discoveredRepo struct {
	Identity string
	Path     string
	Snap     repoSnapshot
}

type repoSnapshot struct {
	CommonDir string
	Porcelain string
	RefsRaw   string
	StashRaw  string
	Worktrees []worktreeSnapshot
}

type worktreeSnapshot struct {
	Path         string
	Head         string
	Branch       string
	Detached     bool
	GitDir       string
	Status       string
	Untracked    string
	SubmoduleRaw string
	Operations   []string
	Submodules   []submoduleSnapshot
}

type submoduleSnapshot struct {
	Path       string
	State      string
	Status     string
	StashRaw   string
	Operations []string
}

func (c *collector) discover(ctx context.Context, req CollectRequest) ([]discoveredRepo, error) {
	queue := make([]queuedRepo, 0, len(req.Repositories))
	seenIdentity := map[string]bool{}
	for _, spec := range req.Repositories {
		queue = append(queue, queuedRepo{Identity: spec.Identity, Path: spec.Path})
		seenIdentity[spec.Identity] = true
	}
	seenCommon := map[string]string{}
	found := make([]discoveredRepo, 0, len(queue))
	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		if item.Depth >= maxCollectDepth {
			return nil, errors.New("preservation incomplete: submodule depth exceeded")
		}
		if len(found) >= maxCollectRepositories {
			return nil, errors.New("preservation incomplete: repository bound exceeded")
		}
		snap, err := c.stableSnapshot(ctx, item.Path)
		if err != nil {
			return nil, err
		}
		if previous, ok := seenCommon[snap.CommonDir]; ok {
			if previous != item.Identity {
				return nil, errors.New("preservation incomplete: repository identity does not match its git directory")
			}
			continue
		}
		seenCommon[snap.CommonDir] = item.Identity
		found = append(found, discoveredRepo{Identity: item.Identity, Path: item.Path, Snap: snap})
		children, err := submoduleChildren(req, item, snap)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if seenIdentity[child.Identity] {
				return nil, errors.New("preservation incomplete: repository identity invalid or duplicate")
			}
			seenIdentity[child.Identity] = true
			queue = append(queue, child)
		}
	}
	return found, nil
}

func (c *collector) stableSnapshot(ctx context.Context, path string) (repoSnapshot, error) {
	first, err := c.readRepoSnapshot(ctx, path)
	if err != nil {
		return repoSnapshot{}, err
	}
	second, err := c.readRepoSnapshot(ctx, path)
	if err != nil {
		return repoSnapshot{}, err
	}
	if !reflect.DeepEqual(first, second) {
		return repoSnapshot{}, errors.New("preservation race: git state changed")
	}
	return second, nil
}

func (c *collector) confirm(ctx context.Context, found []discoveredRepo) error {
	for _, item := range found {
		again, err := c.readRepoSnapshot(ctx, item.Path)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(item.Snap, again) {
			return errors.New("preservation race: git state changed")
		}
	}
	return nil
}

func (c *collector) assemble(ctx context.Context, req CollectRequest, found []discoveredRepo) (Inventory, error) {
	repos := make([]Repository, 0, len(found))
	for _, item := range found {
		repo, err := c.repositoryFromSnapshot(ctx, req, item)
		if err != nil {
			return Inventory{}, err
		}
		repos = append(repos, repo)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Identity < repos[j].Identity })
	artifacts, err := c.artifactReceipts(ctx, req)
	if err != nil {
		return Inventory{}, err
	}
	return Inventory{Complete: true, Repositories: repos, Requirements: req.Requirements, Artifacts: artifacts}, nil
}

func submoduleChildren(req CollectRequest, item queuedRepo, snap repoSnapshot) ([]queuedRepo, error) {
	seen := map[string]bool{}
	var out []queuedRepo
	for _, worktree := range snap.Worktrees {
		for _, sub := range worktree.Submodules {
			if sub.State == "-" {
				continue
			}
			if !validRelative(sub.Path) {
				return nil, errors.New("preservation incomplete: submodule path unreadable")
			}
			identity, ok := req.SubmoduleIdentities[SubmoduleIdentityKey(item.Identity, sub.Path)]
			if !ok || !validText(identity) {
				return nil, errors.New("preservation incomplete: submodule identity unbound")
			}
			child := filepath.Clean(filepath.Join(worktree.Path, filepath.FromSlash(sub.Path)))
			if err := withinDir(worktree.Path, child); err != nil {
				return nil, err
			}
			if seen[child] {
				continue
			}
			seen[child] = true
			if item.Depth+1 >= maxCollectDepth {
				return nil, errors.New("preservation incomplete: submodule depth exceeded")
			}
			out = append(out, queuedRepo{Identity: identity, Path: child, Depth: item.Depth + 1})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (c *collector) readRepoSnapshot(ctx context.Context, repoPath string) (repoSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return repoSnapshot{}, err
	}
	if err := usableDir(repoPath); err != nil {
		return repoSnapshot{}, err
	}
	commonOut, err := c.gitOK(ctx, repoPath, "rev-parse", "--git-common-dir")
	if err != nil {
		return repoSnapshot{}, err
	}
	commonDir, err := absoluteExistingDir(repoPath, commonOut)
	if err != nil {
		return repoSnapshot{}, err
	}
	porcelain, err := c.gitOK(ctx, repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return repoSnapshot{}, err
	}
	refs, err := c.gitOK(ctx, repoPath, "for-each-ref", "--format="+forEachRefFormat, "refs/heads", "refs/tags")
	if err != nil {
		return repoSnapshot{}, err
	}
	stashes, err := c.gitOK(ctx, repoPath, "stash", "list", "--format=%H")
	if err != nil {
		return repoSnapshot{}, err
	}
	parsed, err := parsePorcelain(porcelain)
	if err != nil {
		return repoSnapshot{}, err
	}
	worktrees := make([]worktreeSnapshot, 0, len(parsed))
	for _, item := range parsed {
		worktree, err := c.readWorktree(ctx, item)
		if err != nil {
			return repoSnapshot{}, err
		}
		worktrees = append(worktrees, worktree)
	}
	sort.Slice(worktrees, func(i, j int) bool { return worktrees[i].Path < worktrees[j].Path })
	return repoSnapshot{CommonDir: commonDir, Porcelain: porcelain, RefsRaw: refs, StashRaw: stashes, Worktrees: worktrees}, nil
}

func (c *collector) readWorktree(ctx context.Context, item porcelainWorktree) (worktreeSnapshot, error) {
	if err := usableDir(item.Path); err != nil {
		return worktreeSnapshot{}, err
	}
	head, err := c.gitOK(ctx, item.Path, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return worktreeSnapshot{}, err
	}
	if strings.TrimSpace(head) != item.Head {
		return worktreeSnapshot{}, errors.New("preservation race: git state changed")
	}
	gitDirOut, err := c.gitOK(ctx, item.Path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return worktreeSnapshot{}, err
	}
	gitDir, err := absoluteExistingDir(item.Path, gitDirOut)
	if err != nil {
		return worktreeSnapshot{}, err
	}
	// Mapped descendant config and any index gitlink that mapping omits are
	// refused before status. Status itself does not enter a descendant.
	if err := c.scanSubmoduleTree(ctx, item.Path, 0); err != nil {
		return worktreeSnapshot{}, err
	}
	if err := c.refuseHiddenIndexFlags(ctx, item.Path); err != nil {
		return worktreeSnapshot{}, err
	}
	status, err := c.gitOK(ctx, item.Path, statusArgs...)
	if err != nil {
		return worktreeSnapshot{}, err
	}
	untracked, err := c.gitOK(ctx, item.Path, "ls-files", "-o", "--exclude-standard", "-z")
	if err != nil {
		return worktreeSnapshot{}, err
	}
	operations, err := operationNames(gitDir)
	if err != nil {
		return worktreeSnapshot{}, err
	}
	subRaw, subs, err := c.readSubmodules(ctx, item.Path)
	if err != nil {
		return worktreeSnapshot{}, err
	}
	return worktreeSnapshot{Path: item.Path, Head: item.Head, Branch: item.Branch, Detached: item.Detached, GitDir: gitDir, Status: status, Untracked: untracked, SubmoduleRaw: subRaw, Operations: operations, Submodules: subs}, nil
}

func (c *collector) readSubmodules(ctx context.Context, worktree string) (string, []submoduleSnapshot, error) {
	if err := c.scanSubmoduleTree(ctx, worktree, 0); err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(filepath.Join(worktree, ".gitmodules"))
	if os.IsNotExist(err) {
		// scanSubmoduleTree already refused an index gitlink with no mapping.
		// An absent file is an empty submodule list only after that check.
		return "", []submoduleSnapshot{}, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", nil, errors.New("preservation incomplete: submodule metadata unreadable")
	}
	mapped, err := c.gitmodulesPaths(ctx, worktree)
	if err != nil {
		return "", nil, err
	}
	known := make(map[string]bool, len(mapped))
	for _, path := range mapped {
		known[path] = true
	}
	// Immediate children only. Recursive status repeats a nested checkout under
	// this identity and again under the child, which mismatches one git
	// directory. The child read discovers that nested checkout within
	// maxCollectDepth. core.worktree stays unsupported on every level.
	// Child status ignores its own descendants; those are checked separately.
	raw, err := c.gitOK(ctx, worktree, "submodule", "status")
	if err != nil {
		return "", nil, err
	}
	parsed, err := parseSubmodules(raw)
	if err != nil {
		return "", nil, err
	}
	subs := make([]submoduleSnapshot, 0, len(parsed))
	for _, sub := range parsed {
		if !known[sub.Path] {
			return "", nil, errors.New("preservation incomplete: submodule mapping missing")
		}
		if sub.State == "-" {
			subs = append(subs, submoduleSnapshot{Path: sub.Path, State: "-", Operations: []string{}})
			continue
		}
		if !validRelative(sub.Path) {
			return "", nil, errors.New("preservation incomplete: submodule path unreadable")
		}
		child := filepath.Clean(filepath.Join(worktree, filepath.FromSlash(sub.Path)))
		if err := withinDir(worktree, child); err != nil {
			return "", nil, err
		}
		if err := c.refuseHiddenIndexFlags(ctx, child); err != nil {
			return "", nil, err
		}
		status, err := c.gitOK(ctx, child, statusArgs...)
		if err != nil {
			return "", nil, err
		}
		stashes, err := c.gitOK(ctx, child, "stash", "list", "--format=%H")
		if err != nil {
			return "", nil, err
		}
		gitDirOut, err := c.gitOK(ctx, child, "rev-parse", "--absolute-git-dir")
		if err != nil {
			return "", nil, err
		}
		gitDir, err := absoluteExistingDir(child, gitDirOut)
		if err != nil {
			return "", nil, err
		}
		operations, err := operationNames(gitDir)
		if err != nil {
			return "", nil, err
		}
		subs = append(subs, submoduleSnapshot{Path: sub.Path, State: sub.State, Status: status, StashRaw: stashes, Operations: operations})
	}
	return raw, subs, nil
}

func (c *collector) repositoryFromSnapshot(ctx context.Context, req CollectRequest, item discoveredRepo) (Repository, error) {
	refs, err := parseRefs(item.Snap.RefsRaw)
	if err != nil {
		return Repository{}, err
	}
	stashes, err := parseStashes(item.Snap.StashRaw)
	if err != nil {
		return Repository{}, err
	}
	byName := map[string]Ref{}
	for _, ref := range refs {
		byName[ref.Name] = ref
	}
	worktrees := make([]Worktree, 0, len(item.Snap.Worktrees))
	for _, snap := range item.Snap.Worktrees {
		if snap.Branch != "" {
			ref, ok := byName[snap.Branch]
			if !ok || ref.Commit != snap.Head {
				return Repository{}, errors.New("preservation race: git state changed")
			}
		}
		worktree, err := worktreeFromSnapshot(snap)
		if err != nil {
			return Repository{}, err
		}
		head := Ref{Name: "HEAD:" + snap.Path, Kind: "head", Commit: snap.Head, Object: snap.Head, Receipts: []Signed[PublicationPayload]{}}
		if _, exists := byName[head.Name]; exists {
			return Repository{}, errors.New("preservation incomplete: worktree HEAD duplicated")
		}
		byName[head.Name] = head
		refs = append(refs, head)
		worktrees = append(worktrees, worktree)
	}
	if len(refs) > maxCollectRefs {
		return Repository{}, errors.New("preservation incomplete: ref bound exceeded")
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	unpushed := make([]string, 0)
	for i := range refs {
		receipt, err := c.publication(ctx, req, item.Identity, refs[i])
		if err != nil {
			return Repository{}, err
		}
		if receipt == nil {
			unpushed = append(unpushed, refs[i].Name)
			refs[i].Receipts = []Signed[PublicationPayload]{}
			continue
		}
		refs[i].Receipts = []Signed[PublicationPayload]{*receipt}
	}
	for _, stash := range stashes {
		name, _, _ := strings.Cut(stash, " ")
		unpushed = append(unpushed, name)
	}
	sort.Strings(unpushed)
	return Repository{Identity: item.Identity, WorktreesComplete: true, RefsComplete: true, StashesComplete: true, FetchSucceeded: false, Worktrees: worktrees, Refs: refs, Stashes: stashes, UnpushedRefs: unpushed}, nil
}

func worktreeFromSnapshot(snap worktreeSnapshot) (Worktree, error) {
	staged, unstaged, untracked, err := parseStatus(snap.Status)
	if err != nil {
		return Worktree{}, err
	}
	nested, err := nestedUntracked(snap.Untracked)
	if err != nil {
		return Worktree{}, err
	}
	ignored, err := discoverNestedRepositories(snap.Path, snap.GitDir, initializedSubmodulePaths(snap.Submodules))
	if err != nil {
		return Worktree{}, err
	}
	nested = nested || ignored
	clean := true
	for _, sub := range snap.Submodules {
		if sub.State == "+" || sub.State == "U" || strings.TrimSpace(sub.Status) != "" || strings.TrimSpace(sub.StashRaw) != "" || len(sub.Operations) > 0 {
			clean = false
		}
	}
	return Worktree{Path: snap.Path, HeadRef: "HEAD:" + snap.Path, Staged: staged, Unstaged: unstaged, Untracked: untracked, OperationInProgress: len(snap.Operations) > 0, NestedRepository: nested, SubmodulesComplete: true, SubmodulesClean: clean}, nil
}

func (c *collector) publication(ctx context.Context, req CollectRequest, repository string, ref Ref) (*Signed[PublicationPayload], error) {
	if req.Publications == nil {
		return nil, nil
	}
	queryRef := ref
	queryRef.Receipts = []Signed[PublicationPayload]{}
	receipt, err := req.Publications.Query(ctx, PublicationQuery{Binding: req.Binding, Repository: repository, Ref: queryRef})
	if err != nil {
		return nil, fmt.Errorf("preservation incomplete: publication query: %w", err)
	}
	if receipt == nil {
		return nil, nil
	}
	if err := acceptPublication(req.Verifier, req.Binding, repository, ref, *receipt); err != nil {
		return nil, err
	}
	return receipt, nil
}

func acceptPublication(verifier EvidenceVerifier, binding Binding, repository string, ref Ref, receipt Signed[PublicationPayload]) error {
	if verifier == nil {
		return errors.New("publication proof rejected: verifier absent")
	}
	payload := receipt.Payload
	authority, ok := verifier.Verify("publication", payload, receipt.Signature)
	if !ok || payload.Version != Version || payload.Binding != binding || payload.Repository != repository || authority.Repository != repository || authority.Remote != payload.Remote {
		return errors.New("publication proof rejected")
	}
	if payload.LocalRef != ref.Name || payload.Commit != ref.Commit || payload.Object != ref.Object || payload.RemoteCommit != ref.Commit || payload.RemoteObject != ref.Object || !validText(payload.RemoteRef) || !strings.HasPrefix(payload.RemoteRef, "refs/") {
		return errors.New("publication proof rejected")
	}
	if payload.Method != "authenticated_push" && payload.Method != "authenticated_remote_query" {
		return errors.New("publication proof rejected")
	}
	if _, parsed := parseTime(payload.VerifiedAt); !parsed {
		return errors.New("publication proof rejected")
	}
	return nil
}

func (c *collector) artifactReceipts(ctx context.Context, req CollectRequest) ([]Artifact, error) {
	artifacts := make([]Artifact, 0, len(req.Artifacts))
	for _, input := range req.Artifacts {
		artifact := Artifact{ID: input.ID, Kind: input.Kind, SHA256: input.SHA256, Receipts: []Signed[DurableCopyPayload]{}}
		if req.DurableCopies == nil {
			artifacts = append(artifacts, artifact)
			continue
		}
		receipt, err := req.DurableCopies.Query(ctx, req.Binding, artifact)
		if err != nil {
			return nil, fmt.Errorf("preservation incomplete: durable copy query: %w", err)
		}
		if receipt == nil {
			artifacts = append(artifacts, artifact)
			continue
		}
		if err := acceptDurableCopy(req.Verifier, req.Binding, artifact, *receipt); err != nil {
			return nil, err
		}
		artifact.Receipts = []Signed[DurableCopyPayload]{*receipt}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}

func acceptDurableCopy(verifier EvidenceVerifier, binding Binding, artifact Artifact, receipt Signed[DurableCopyPayload]) error {
	if verifier == nil {
		return errors.New("durable copy rejected: verifier absent")
	}
	payload := receipt.Payload
	authority, ok := verifier.Verify("durable-copy", payload, receipt.Signature)
	if !ok || payload.Version != Version || payload.Binding != binding || payload.ArtifactID != artifact.ID || payload.Kind != artifact.Kind || payload.SHA256 != artifact.SHA256 || payload.Destination != authority.Destination || payload.Method != "durable_copy_verified" {
		return errors.New("durable copy rejected")
	}
	if _, parsed := parseTime(payload.VerifiedAt); !parsed {
		return errors.New("durable copy rejected")
	}
	return nil
}

// refuseUnsafeConfig reads local and, when enabled, worktree config before any
// command that can execute a hook, filter, diff driver, or helper. Git ignores
// an alias that reuses a builtin name, so this scan is not defending against
// alias.status replacing git status. Keys this collector does not recognize as
// non-executing repository data fail closed instead of being invoked.
func (c *collector) refuseUnsafeConfig(ctx context.Context, dir string) error {
	local, err := c.gitRaw(ctx, dir, localConfigArgs...)
	if err != nil {
		return err
	}
	entries, worktreeConfig, err := parseListedConfig(local)
	if err != nil {
		return err
	}
	if err := rejectUnsupportedConfig(entries); err != nil {
		return err
	}
	if !worktreeConfig {
		return nil
	}
	extra, err := c.gitRaw(ctx, dir, worktreeConfigArgs...)
	if err != nil {
		return err
	}
	entries, _, err = parseListedConfig(extra)
	if err != nil {
		return err
	}
	return rejectUnsupportedConfig(entries)
}

func (c *collector) gitOK(ctx context.Context, dir string, args ...string) (string, error) {
	if !isConfigList(args) {
		if err := c.refuseUnsafeConfig(ctx, dir); err != nil {
			return "", err
		}
	}
	return c.gitRaw(ctx, dir, args...)
}

func (c *collector) gitRaw(ctx context.Context, dir string, args ...string) (string, error) {
	if err := allowedReadOnlyGit(args); err != nil {
		return "", err
	}
	c.calls++
	if c.calls > maxGitCalls {
		return "", errors.New("preservation incomplete: git call bound exceeded")
	}
	out, code, err := c.run.Run(ctx, dir, args)
	if err != nil {
		return "", fmt.Errorf("preservation incomplete: %w", err)
	}
	if len(out) > maxGitOutputBytes {
		return "", errors.New("preservation incomplete: git output exceeded bound")
	}
	if code != 0 {
		return "", fmt.Errorf("preservation incomplete: git %s exited %d", args[0], code)
	}
	return out, nil
}

func isConfigList(args []string) bool {
	return slices.Equal(args, localConfigArgs) || slices.Equal(args, worktreeConfigArgs) || allowedFileConfig(args)
}

func allowedFileConfig(args []string) bool {
	if len(args) != 6 || args[0] != "config" || args[1] != "--file" || args[3] != "--list" || args[4] != "--null" || args[5] != "--no-includes" {
		return false
	}
	file := args[2]
	if file == "" || !filepath.IsAbs(file) || filepath.Clean(file) != file || filepath.Base(file) != ".gitmodules" {
		return false
	}
	return !strings.ContainsAny(file, "\x00\n")
}

func allowedReadOnlyGit(args []string) error {
	refArgs := []string{"for-each-ref", "--format=" + forEachRefFormat, "refs/heads", "refs/tags"}
	switch {
	case slices.Equal(args, localConfigArgs):
	case slices.Equal(args, worktreeConfigArgs):
	case slices.Equal(args, []string{"rev-parse", "--git-common-dir"}):
	case slices.Equal(args, []string{"rev-parse", "--absolute-git-dir"}):
	case slices.Equal(args, []string{"rev-parse", "--verify", "--quiet", "HEAD"}):
	case slices.Equal(args, []string{"worktree", "list", "--porcelain"}):
	case slices.Equal(args, refArgs):
	case slices.Equal(args, []string{"stash", "list", "--format=%H"}):
	case slices.Equal(args, statusArgs):
	case slices.Equal(args, []string{"ls-files", "-o", "--exclude-standard", "-z"}):
	case slices.Equal(args, indexFlagArgs):
	case slices.Equal(args, gitlinkIndexArgs):
	case slices.Equal(args, headTreeArgs):
	case slices.Equal(args, []string{"submodule", "status"}):
	case allowedFileConfig(args):
	default:
		return errors.New("read-only git command refused: " + strings.Join(args, " "))
	}
	return nil
}

type porcelainWorktree struct {
	Path     string
	Head     string
	Branch   string
	Detached bool
}

func parsePorcelain(out string) ([]porcelainWorktree, error) {
	if strings.TrimSpace(out) == "" {
		return nil, errors.New("preservation incomplete: worktree list empty")
	}
	var kept []porcelainWorktree
	for _, block := range strings.Split(out, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var item porcelainWorktree
		bare, prunable := false, false
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				item.Path = strings.TrimPrefix(line, "worktree ")
			case strings.HasPrefix(line, "HEAD "):
				item.Head = strings.TrimPrefix(line, "HEAD ")
			case strings.HasPrefix(line, "branch "):
				item.Branch = strings.TrimPrefix(line, "branch ")
			case line == "detached":
				item.Detached = true
			case line == "bare":
				bare = true
			case strings.HasPrefix(line, "prunable"):
				prunable = true
			case line == "locked" || strings.HasPrefix(line, "locked "):
			default:
				return nil, errors.New("preservation incomplete: unrecognized worktree record")
			}
		}
		if bare || prunable {
			return nil, errors.New("preservation incomplete: prunable or bare worktree")
		}
		if item.Path == "" || !validObject(item.Head) || item.Detached == (item.Branch != "") {
			return nil, errors.New("preservation incomplete: worktree identity unreadable")
		}
		if !item.Detached && !strings.HasPrefix(item.Branch, "refs/heads/") {
			return nil, errors.New("preservation incomplete: worktree identity unreadable")
		}
		kept = append(kept, item)
	}
	if len(kept) == 0 {
		return nil, errors.New("preservation incomplete: no worktree to inventory")
	}
	if len(kept) > maxCollectWorktrees {
		return nil, errors.New("preservation incomplete: worktree bound exceeded")
	}
	return kept, nil
}

func parseRefs(out string) ([]Ref, error) {
	refs := []Ref{}
	seen := map[string]bool{}
	if strings.TrimSpace(out) == "" {
		return refs, nil
	}
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) != 4 {
			return nil, errors.New("preservation incomplete: ref record unreadable")
		}
		objectName, objectType, peeled, name := parts[0], parts[1], parts[2], parts[3]
		ref := Ref{Name: name, Object: objectName, Receipts: []Signed[PublicationPayload]{}}
		switch objectType {
		case "commit":
			if peeled != "" || !validObject(objectName) {
				return nil, errors.New("preservation incomplete: ref record unreadable")
			}
			ref.Commit = objectName
			if strings.HasPrefix(name, "refs/heads/") {
				ref.Kind = "branch"
			} else if strings.HasPrefix(name, "refs/tags/") {
				ref.Kind = "tag"
			} else {
				return nil, errors.New("preservation incomplete: ref record unreadable")
			}
		case "tag":
			if !strings.HasPrefix(name, "refs/tags/") || !validObject(objectName) || !validObject(peeled) {
				return nil, errors.New("preservation incomplete: ref record unreadable")
			}
			ref.Kind, ref.Commit = "tag", peeled
		default:
			return nil, errors.New("preservation incomplete: ref record unreadable")
		}
		if !validText(ref.Name) || seen[ref.Name] {
			return nil, errors.New("preservation incomplete: ref record unreadable")
		}
		seen[ref.Name] = true
		refs = append(refs, ref)
		if len(refs) > maxCollectRefs {
			return nil, errors.New("preservation incomplete: ref bound exceeded")
		}
	}
	return refs, nil
}

func parseStashes(out string) ([]string, error) {
	if strings.TrimSpace(out) == "" {
		return []string{}, nil
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) > maxCollectStashes {
		return nil, errors.New("preservation incomplete: stash bound exceeded")
	}
	stashes := make([]string, 0, len(lines))
	for index, line := range lines {
		line = strings.TrimSpace(line)
		if !validObject(line) {
			return nil, errors.New("preservation incomplete: stash object unreadable")
		}
		stashes = append(stashes, fmt.Sprintf("stash@{%d} %s", index, line))
	}
	return stashes, nil
}

func parseStatus(out string) (staged, unstaged, untracked bool, err error) {
	rest := []byte(out)
	records := 0
	for len(rest) > 0 {
		if records == maxCollectStatus {
			return false, false, false, errors.New("preservation incomplete: status bound exceeded")
		}
		end := bytes.IndexByte(rest, 0)
		if end < 3 || rest[2] != ' ' {
			return false, false, false, errors.New("preservation incomplete: status record unreadable")
		}
		x, y := rest[0], rest[1]
		rest = rest[end+1:]
		records++
		switch {
		case x == '?' && y == '?':
			untracked = true
		case x == '!' && y == '!':
		default:
			if x != ' ' {
				staged = true
			}
			if y != ' ' {
				unstaged = true
			}
		}
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			next := bytes.IndexByte(rest, 0)
			if next < 0 {
				return false, false, false, errors.New("preservation incomplete: status record unreadable")
			}
			rest = rest[next+1:]
		}
	}
	return staged, unstaged, untracked, nil
}

func nestedUntracked(out string) (bool, error) {
	rest := []byte(out)
	nested := false
	records := 0
	for len(rest) > 0 {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return false, errors.New("preservation incomplete: untracked record unreadable")
		}
		name := rest[:end]
		rest = rest[end+1:]
		if len(name) == 0 {
			continue
		}
		records++
		if records > maxCollectStatus {
			return false, errors.New("preservation incomplete: untracked bound exceeded")
		}
		if bytes.HasSuffix(name, []byte("/")) {
			nested = true
		}
	}
	return nested, nil
}

type parsedSubmodule struct {
	State string
	Path  string
}

func parseSubmodules(out string) ([]parsedSubmodule, error) {
	if strings.TrimSpace(out) == "" {
		return []parsedSubmodule{}, nil
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) > maxCollectRepositories {
		return nil, errors.New("preservation incomplete: submodule bound exceeded")
	}
	subs := make([]parsedSubmodule, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		state := line[:1]
		if state != " " && state != "+" && state != "-" && state != "U" {
			return nil, errors.New("preservation incomplete: submodule status unreadable")
		}
		fields := strings.Fields(line[1:])
		if len(fields) < 2 || !validObject(fields[0]) || !validRelative(fields[1]) {
			return nil, errors.New("preservation incomplete: submodule status unreadable")
		}
		subs = append(subs, parsedSubmodule{State: state, Path: fields[1]})
	}
	return subs, nil
}

func operationNames(gitDir string) ([]string, error) {
	found := []string{}
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "BISECT_START"} {
		target := filepath.Join(gitDir, name)
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("preservation incomplete: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("preservation incomplete: git operation symlink refused")
		}
		found = append(found, name)
	}
	return found, nil
}

func absoluteExistingDir(base, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "\n") {
		return "", errors.New("preservation incomplete: git directory unreadable")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	value = filepath.Clean(value)
	if err := usableDir(value); err != nil {
		return "", err
	}
	return value, nil
}

func usableDir(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("preservation incomplete: repository path is not absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("preservation incomplete: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("preservation incomplete: repository path is not a directory")
	}
	return nil
}

func withinDir(root, target string) error {
	if err := usableDir(root); err != nil {
		return err
	}
	if err := usableDir(target); err != nil {
		return err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("preservation incomplete: submodule path escapes its worktree")
	}
	return nil
}

func validRelative(path string) bool {
	if path == "" || filepath.IsAbs(path) || strings.ContainsAny(path, "\\\x00\n") || filepath.Clean(path) != path {
		return false
	}
	if filepath.ToSlash(path) != path {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func (c *collector) scanSubmoduleTree(ctx context.Context, worktree string, depth int) error {
	if depth >= maxCollectDepth {
		return errors.New("preservation incomplete: submodule depth exceeded")
	}
	paths, err := c.gitmodulesPaths(ctx, worktree)
	if err != nil {
		return err
	}
	for _, rel := range paths {
		child := filepath.Clean(filepath.Join(worktree, filepath.FromSlash(rel)))
		info, statErr := os.Lstat(child)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return fmt.Errorf("preservation incomplete: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("preservation incomplete: submodule path is not a directory")
		}
		if err := withinDir(worktree, child); err != nil {
			return err
		}
		if err := c.refuseUnsafeConfig(ctx, child); err != nil {
			return err
		}
		if err := c.scanSubmoduleTree(ctx, child, depth+1); err != nil {
			return err
		}
	}
	// After mapped descendants are config-checked, so a bad child config still
	// fails before any index read. A gitlink omitted from .gitmodules, or
	// present when that file is absent, fails here, before the caller status.
	return c.refuseUnmappedGitlinks(ctx, worktree, paths)
}

// gitmodulesPaths asks Git to parse one regular .gitmodules file. The allowlist
// keeps ordinary path, url, and active entries, and refuses every other key.
// A directory Git reports as absent is skipped. A comment left in a value is
// not treated as a different path.
func (c *collector) gitmodulesPaths(ctx context.Context, worktree string) ([]string, error) {
	path := filepath.Join(worktree, ".gitmodules")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxGitOutputBytes {
		return nil, errors.New("preservation incomplete: submodule metadata unreadable")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("preservation incomplete: submodule metadata unreadable")
	}
	out, err := c.gitRaw(ctx, worktree, "config", "--file", path, "--list", "--null", "--no-includes")
	if err != nil {
		return nil, err
	}
	entries, err := parseFileConfig(out)
	if err != nil {
		return nil, err
	}
	paths := []string{}
	seenKey := map[string]bool{}
	seenPath := map[string]bool{}
	names := map[string]bool{}
	withPath := map[string]bool{}
	for _, entry := range entries {
		if seenKey[entry.key] || !supportedGitmodulesKey(entry.key, entry.value) {
			return nil, errors.New("preservation incomplete: unsupported git config")
		}
		seenKey[entry.key] = true
		name, variable, ok := submoduleVariable(entry.key)
		if !ok {
			return nil, errors.New("preservation incomplete: unsupported git config")
		}
		names[name] = true
		if !strings.EqualFold(variable, "path") {
			continue
		}
		if seenPath[entry.value] || withPath[name] {
			return nil, errors.New("preservation incomplete: submodule path unreadable")
		}
		seenPath[entry.value] = true
		withPath[name] = true
		paths = append(paths, entry.value)
		if len(paths) > maxCollectRepositories {
			return nil, errors.New("preservation incomplete: submodule bound exceeded")
		}
	}
	for name := range names {
		if !withPath[name] {
			return nil, errors.New("preservation incomplete: submodule path unreadable")
		}
	}
	return paths, nil
}

func parseFileConfig(out string) ([]listedConfig, error) {
	if out == "" {
		return nil, nil
	}
	parts := strings.Split(out, "\x00")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 || len(parts) > maxCollectStatus {
		return nil, errors.New("preservation incomplete: git config unreadable")
	}
	entries := make([]listedConfig, 0, len(parts))
	for _, part := range parts {
		key, value, ok := strings.Cut(part, "\n")
		if !ok || key == "" || strings.Contains(key, "\n") || strings.Contains(value, "\n") {
			return nil, errors.New("preservation incomplete: git config unreadable")
		}
		entries = append(entries, listedConfig{origin: "file:", key: key, value: value})
	}
	return entries, nil
}

func submoduleVariable(key string) (string, string, bool) {
	parts := strings.Split(key, ".")
	if len(parts) < 3 || !strings.EqualFold(parts[0], "submodule") {
		return "", "", false
	}
	name := strings.Join(parts[1:len(parts)-1], ".")
	variable := parts[len(parts)-1]
	if name == "" || variable == "" || strings.ContainsAny(name, " \t\"'#\\") {
		return "", "", false
	}
	return name, variable, true
}

func supportedGitmodulesKey(key, value string) bool {
	if strings.ContainsAny(value, "\x00\n\r") || strings.Contains(value, "::") || strings.HasPrefix(value, "!") || strings.HasPrefix(value, "-") {
		return false
	}
	_, variable, ok := submoduleVariable(key)
	if !ok {
		return false
	}
	switch strings.ToLower(variable) {
	case "path":
		return validRelative(value) && !strings.ContainsAny(value, "#\"")
	case "url":
		return value != "" && !strings.ContainsAny(value, " \t")
	case "active":
		return value == "true" || value == "false"
	default:
		return false
	}
}

func initializedSubmodulePaths(subs []submoduleSnapshot) map[string]bool {
	paths := map[string]bool{}
	for _, sub := range subs {
		if sub.State != "-" && validRelative(sub.Path) {
			paths[sub.Path] = true
		}
	}
	return paths
}

type listedConfig struct {
	origin string
	key    string
	value  string
}

func parseListedConfig(out string) ([]listedConfig, bool, error) {
	if out == "" {
		return nil, false, nil
	}
	parts := strings.Split(out, "\x00")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 || len(parts)%2 != 0 || len(parts)/2 > maxCollectStatus {
		return nil, false, errors.New("preservation incomplete: git config unreadable")
	}
	entries := make([]listedConfig, 0, len(parts)/2)
	worktree := false
	for i := 0; i < len(parts); i += 2 {
		key, value, ok := strings.Cut(parts[i+1], "\n")
		if !ok || key == "" || strings.Contains(key, "\n") || parts[i] == "" {
			return nil, false, errors.New("preservation incomplete: git config unreadable")
		}
		entry := listedConfig{origin: parts[i], key: key, value: value}
		entries = append(entries, entry)
		if strings.EqualFold(entry.key, "extensions.worktreeconfig") && entry.value == "true" {
			worktree = true
		}
	}
	return entries, worktree, nil
}

func rejectUnsupportedConfig(entries []listedConfig) error {
	for _, entry := range entries {
		switch {
		case strings.HasPrefix(entry.origin, "file:"), strings.HasPrefix(entry.origin, "blob:"):
			if !supportedRepositoryKey(entry.key, entry.value) {
				return errors.New("preservation incomplete: unsupported git config")
			}
		case entry.origin == "command line" || strings.HasPrefix(entry.origin, "command line:"):
			// A local listing should not include process overrides. If it does,
			// only the safety pairs this process sets are ignored. Git does not
			// let those pairs, or an alias, replace the config builtin.
			if !safetyOverride(entry.key, entry.value) {
				return errors.New("preservation incomplete: unsupported git config")
			}
		default:
			return errors.New("preservation incomplete: unsupported git config")
		}
	}
	return nil
}

// refuseHiddenIndexFlags rejects skip-worktree and assume-unchanged index
// bits, and any other tag that is not ordinary cached H. Status porcelain is
// not read until this check passes. The two reads are not atomic.
func (c *collector) refuseHiddenIndexFlags(ctx context.Context, dir string) error {
	out, err := c.gitOK(ctx, dir, indexFlagArgs...)
	if err != nil {
		return err
	}
	return rejectHiddenIndexFlags(out)
}

// refuseUnmappedGitlinks reads index modes only. ls-files --stage does not
// start Git in a gitlink. A mode 160000 path must be one of the paths already
// parsed from .gitmodules; an empty mapping refuses every gitlink.
func (c *collector) refuseUnmappedGitlinks(ctx context.Context, worktree string, mapped []string) error {
	out, err := c.gitOK(ctx, worktree, gitlinkIndexArgs...)
	if err != nil {
		return err
	}
	if err := rejectUnmappedGitlinks(out, mapped); err != nil {
		return err
	}
	head, err := c.gitOK(ctx, worktree, headTreeArgs...)
	if err != nil {
		return err
	}
	return rejectStagedGitlinks(out, head)
}

func rejectHiddenIndexFlags(out string) error {
	if out == "" {
		return nil
	}
	parts := strings.Split(out, "\x00")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 || len(parts) > maxCollectStatus {
		return errors.New("preservation incomplete: index flags unreadable")
	}
	for _, rec := range parts {
		if len(rec) < 3 || rec[0] != 'H' || rec[1] != ' ' {
			return errors.New("preservation incomplete: index flags hide edits")
		}
	}
	return nil
}

func rejectUnmappedGitlinks(out string, mapped []string) error {
	if out == "" {
		return nil
	}
	known := make(map[string]bool, len(mapped))
	for _, path := range mapped {
		known[path] = true
	}
	parts := strings.Split(out, "\x00")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 || len(parts) > maxCollectStatus {
		return errors.New("preservation incomplete: index metadata unreadable")
	}
	for _, rec := range parts {
		mode, rest, ok := strings.Cut(rec, " ")
		if !ok || mode == "" || rest == "" {
			return errors.New("preservation incomplete: index metadata unreadable")
		}
		if mode != "160000" {
			continue
		}
		_, path, ok := strings.Cut(rec, "\t")
		if !ok || !validRelative(path) {
			return errors.New("preservation incomplete: index metadata unreadable")
		}
		if !known[path] {
			return errors.New("preservation incomplete: submodule mapping missing")
		}
	}
	return nil
}

func safetyOverride(key, value string) bool {
	for _, pair := range gitSafetyConfig() {
		if strings.EqualFold(pair[0], key) && pair[1] == value {
			return true
		}
	}
	return false
}

func supportedRepositoryKey(key, value string) bool {
	if strings.ContainsAny(value, "\x00") {
		return false
	}
	parts := strings.Split(key, ".")
	if len(parts) < 2 {
		return false
	}
	section := strings.ToLower(parts[0])
	last := strings.ToLower(parts[len(parts)-1])
	switch section {
	case "core":
		// core.worktree is not allowed. Status would follow it while the nested
		// walk stays on the porcelain worktree path.
		if len(parts) != 2 {
			return false
		}
		switch last {
		case "bare", "repositoryformatversion", "filemode", "logallrefupdates", "ignorecase", "precomposeunicode", "symlinks":
			return true
		}
	case "user":
		if len(parts) != 2 {
			return false
		}
		return last == "name" || last == "email"
	case "remote":
		if len(parts) != 3 || parts[1] == "" {
			return false
		}
		if last != "url" && last != "fetch" {
			return false
		}
		return !strings.Contains(value, "::")
	case "branch":
		if len(parts) != 3 || parts[1] == "" {
			return false
		}
		return last == "remote" || last == "merge"
	case "submodule":
		if len(parts) != 3 || parts[1] == "" {
			return false
		}
		if last != "path" && last != "url" && last != "active" {
			return false
		}
		return !strings.Contains(value, "::")
	case "extensions":
		if len(parts) != 2 {
			return false
		}
		return last == "worktreeconfig" && (value == "true" || value == "false")
	}
	return false
}

var (
	errNestedSymlink = errors.New("preservation incomplete: nested walk symlink refused")
	errNestedBound   = errors.New("preservation incomplete: nested walk bound exceeded")
)

func discoverNestedRepositories(worktree, gitDir string, submodules map[string]bool) (bool, error) {
	if err := usableDir(worktree); err != nil {
		return false, err
	}
	found := false
	count := 0
	err := filepath.WalkDir(worktree, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("preservation incomplete: %w", walkErr)
		}
		count++
		if count > maxCollectStatus {
			return errNestedBound
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errNestedSymlink
		}
		if path == worktree {
			return nil
		}
		if path == gitDir || path == filepath.Join(worktree, ".git") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(worktree, path)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return errors.New("preservation incomplete: nested walk escaped")
		}
		relSlash := filepath.ToSlash(rel)
		if entry.IsDir() && submodules[relSlash] {
			return filepath.SkipDir
		}
		if entry.Name() == ".git" {
			found = true
			if entry.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

// rejectStagedGitlinks keeps status suppression from hiding staged gitlinks.
// Both inputs describe this repository only; neither command enters a child.
// Any addition, deletion, object or mode change involving a gitlink refuses
// completeness. Unmerged index entries also fail closed.
func rejectStagedGitlinks(index, head string) error {
	type entry struct{ mode, object string }
	parse := func(raw string, tree bool) (map[string]entry, error) {
		entries := map[string]entry{}
		if raw == "" {
			return entries, nil
		}
		if !strings.HasSuffix(raw, "\x00") {
			return nil, errors.New("preservation incomplete: gitlink metadata unreadable")
		}
		records := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
		if len(records) > maxCollectStatus {
			return nil, errors.New("preservation incomplete: gitlink metadata bound exceeded")
		}
		for _, record := range records {
			metadata, path, ok := strings.Cut(record, "\t")
			fields := strings.Fields(metadata)
			if !ok || !validRelative(path) || len(fields) != 3 {
				return nil, errors.New("preservation incomplete: gitlink metadata unreadable")
			}
			object := fields[1]
			if tree {
				object = fields[2]
			} else if fields[2] != "0" {
				return nil, errors.New("preservation incomplete: unmerged index")
			}
			if !validObject(object) {
				return nil, errors.New("preservation incomplete: gitlink object unreadable")
			}
			if _, exists := entries[path]; exists {
				return nil, errors.New("preservation incomplete: duplicate gitlink metadata")
			}
			entries[path] = entry{fields[0], object}
		}
		return entries, nil
	}
	staged, err := parse(index, false)
	if err != nil {
		return err
	}
	committed, err := parse(head, true)
	if err != nil {
		return err
	}
	for path, item := range staged {
		previous, exists := committed[path]
		if (item.mode == "160000" || previous.mode == "160000") && (!exists || item != previous) {
			return errors.New("preservation incomplete: staged submodule changes")
		}
	}
	for path, item := range committed {
		if item.mode == "160000" {
			if _, exists := staged[path]; !exists {
				return errors.New("preservation incomplete: staged submodule changes")
			}
		}
	}
	return nil
}
