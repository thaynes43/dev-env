package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

const privateRootBranch = "dev-env/project"
const privateAnchorMetadataLimit = 4096

// The snapshot stores rules once; wrappers are bound by a digest. Allow sixfold
// JSON escaping of four bounded metadata strings per anchor, source metadata,
// and the root's paths/header. Existing workspace receipts retain their 64KiB
// limit; only this owned project record needs the derived larger bound.
const privateRootReceiptLimit = projectcatalog.MaxSnapshotBytes + projectcatalog.MaxProjectRepositories*(4*privateAnchorMetadataLimit*6+(4<<10)) + (64 << 10)

var rootObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var errPrivateRoot = errors.New("private project root ownership or clean state is unconfirmed; preserved")

type PrivateProjectRootReport struct {
	Project         string                 `json:"project"`
	State           string                 `json:"state"`
	CatalogRevision string                 `json:"catalogRevision"`
	RulesRevision   string                 `json:"rulesRevision"`
	Head            string                 `json:"head,omitempty"`
	Repositories    []PrivateProjectSource `json:"repositories,omitempty"`
	ObservedAt      time.Time              `json:"observedAt"`
	Detail          string                 `json:"detail,omitempty"`
}

// Ownership and operation outcomes are separate: an interrupted mutation
// retains Started and the preceding exact ownership, never an apparent Ready.
type privateRootReceipt struct {
	Version       int                              `json:"version"`
	ID            string                           `json:"id"`
	Root          string                           `json:"root"`
	GitDir        string                           `json:"gitDir"`
	Branch        string                           `json:"branch"`
	Head          string                           `json:"head"`
	Tree          string                           `json:"tree"`
	WrapperDigest string                           `json:"wrapperDigest"`
	ConfigDigest  string                           `json:"configDigest"`
	Exclusion     string                           `json:"exclusion"`
	Snapshot      json.RawMessage                  `json:"snapshot"`
	Repositories  []PrivateProjectSource           `json:"repositories"`
	Anchors       map[string]privateAnchorIdentity `json:"anchors"`
}

type privateAnchorIdentity struct {
	GitDir      string `json:"gitDir"`
	GitFile     string `json:"gitFile"`
	Backpointer string `json:"backpointer"`
	CommonFile  string `json:"commonFile"`
}

func refreshPrivateProjectRoot(ctx context.Context, r Runner, s Settings, snapshot projectcatalog.Snapshot, authority *projectCatalogAuthority) (report PrivateProjectRootReport) {
	report = PrivateProjectRootReport{Project: snapshot.Project(), State: "Failed", CatalogRevision: snapshot.CatalogRevision(), RulesRevision: snapshot.RulesRevision(), ObservedAt: time.Now().UTC(), Detail: errPrivateRoot.Error()}
	parent := filepath.Join(s.StateDir, "project-roots")
	if ensureWorkspaceDirectory(parent) != nil {
		return report
	}
	operation := filepath.Join(parent, snapshot.Project()+".operation.json")
	started := report
	started.State = "Started"
	if writeWorkspaceJSON(operation, started) != nil {
		return report
	}
	defer func() {
		report.ObservedAt = time.Now().UTC()
		if writeWorkspaceJSON(operation, report) != nil {
			report.State, report.Detail = "Failed", "private project root outcome retention is unconfirmed"
		}
	}()
	if authority.confirm(ctx) != nil || ensureWorkspaceDirectory(filepath.Join(s.Home, "codex")) != nil {
		return report
	}
	root := filepath.Join(s.Home, "codex", snapshot.Project())
	receiptPath := filepath.Join(parent, snapshot.Project()+".json")
	var old privateRootReceipt
	_, rootErr := os.Lstat(root)
	newRoot := errors.Is(rootErr, os.ErrNotExist)
	if newRoot {
		if _, err := os.Lstat(receiptPath); !errors.Is(err, os.ErrNotExist) {
			return report
		}
	} else {
		if rootErr != nil || readWorkspaceJSONBounded(receiptPath, &old, privateRootReceiptLimit) != nil || provePrivateRoot(ctx, r, s, old, old.Repositories, old.Anchors) != nil {
			return report
		}
		if old.Root != root {
			return report
		}
		// Removed anchors are never deleted or silently reclassified as owned
		// by a newer map. A later explicit migration can decide their future.
		for _, prior := range old.Repositories {
			if !slices.ContainsFunc(snapshot.Repositories(), func(repo projectcatalog.Repository) bool {
				return repo.Name == prior.Repository.Name && repo.GitHub == prior.Repository.GitHub
			}) {
				return report
			}
		}
	}
	var sources []PrivateProjectSource
	for _, repo := range snapshot.Repositories() {
		if authority.confirm(ctx) != nil || validateProjectReference(ctx, r, s, repo) != nil || projectGitOperationClear(ctx, r, s, s.ClonePath(repo.Name)) != nil {
			return report
		}
		target, fetched, err := fetchProjectTarget(ctx, r, s, repo)
		observed := PrivateProjectSource{Repository: repo, Target: target, FetchedAt: fetched, State: "Preserved", Detail: "project source fetch is unconfirmed"}
		report.Repositories = append(report.Repositories, observed)
		if err != nil || !rootObjectID.MatchString(target) {
			return report
		}
		observed.State, observed.Detail = "Ready", "fresh project anchor target"
		sources = append(sources, observed)
	}
	next := privateRootReceipt{Version: 1, ID: old.ID, Root: root, GitDir: filepath.Join(root, ".git"), Branch: privateRootBranch, WrapperDigest: projectcatalog.Digest([]byte(snapshot.ProjectRules())), ConfigDigest: old.ConfigDigest, Repositories: sources, Anchors: map[string]privateAnchorIdentity{}}
	next.Snapshot, _ = json.Marshal(snapshot)
	for _, repo := range snapshot.Repositories() {
		next.Exclusion += "/" + repo.Name + "/\n"
	}
	if newRoot {
		id, err := newUUID()
		if err != nil {
			return report
		}
		next.ID = id
		if preparePrivateRoot(ctx, r, s, &next, snapshot.ProjectRules(), authority) != nil {
			return report
		}
	}
	for _, source := range sources {
		anchor := filepath.Join(root, source.Repository.Name)
		var prior *privateAnchorIdentity
		if identity, exists := old.Anchors[source.Repository.Name]; exists {
			prior = &identity
		}
		if authority.confirm(ctx) != nil {
			return report
		}
		identity, err := advancePrivateAnchor(ctx, r, s, source.Repository, anchor, source.Target, prior, authority)
		if err != nil {
			return report
		}
		next.Anchors[source.Repository.Name] = identity
	}
	if !newRoot {
		if provePrivateRoot(ctx, r, s, old, sources, next.Anchors) != nil || authority.confirm(ctx) != nil {
			return report
		}
		if old.WrapperDigest == next.WrapperDigest {
			next.Head, next.Tree = old.Head, old.Tree
		} else {
			var err error
			next.Head, next.Tree, err = generatedRootCommit(ctx, r, s, root, snapshot.ProjectRules(), old.Head)
			if err != nil || authority.confirm(ctx) != nil || provePrivateRoot(ctx, r, s, old, sources, next.Anchors) != nil {
				return report
			}
			if _, err := rootGit(ctx, r, s, root, nil, "-c", "merge.autostash=false", "merge", "--ff-only", "--no-edit", "--no-autostash", next.Head); err != nil {
				return report
			}
			if setGeneratedRootWrapperModes(ctx, root, snapshot.ProjectRules(), authority) != nil {
				return report
			}
		}
		if old.Exclusion != next.Exclusion && writeFileAtomic(filepath.Join(next.GitDir, "info", "exclude"), []byte(next.Exclusion), 0o644) != nil {
			return report
		}
	}
	encoded, err := json.Marshal(next)
	if err != nil || len(encoded)+1 > privateRootReceiptLimit || authority.confirm(ctx) != nil || provePrivateRoot(ctx, r, s, next, sources, next.Anchors) != nil || writeWorkspaceJSON(receiptPath, next) != nil {
		return report
	}
	report.State, report.Head, report.Repositories, report.Detail = "Ready", next.Head, sources, "managed Git root and detached repository anchors verified"
	return report
}

func rootGit(ctx context.Context, r Runner, s Settings, dir string, input *strings.Reader, args ...string) (string, error) {
	full := append([]string{"-C", dir}, sharedGitPolicyArgs...)
	full = append(full, projectGitPolicyArgs...)
	full = append(full, "-c", "init.defaultRefFormat=files", "-c", "core.attributesFile=/dev/null", "-c", "user.name="+s.GitUserName, "-c", "user.email="+s.GitUserEmail)
	full = append(full, args...)
	cmd := Cmd{Name: "git", Args: full, Env: append(slices.Clone(gitEnv), "GIT_OPTIONAL_LOCKS=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"), UnsetEnv: privateGitUnsetEnv}
	if input != nil {
		cmd.Stdin = input
	}
	result, err := r.Run(ctx, cmd)
	return strings.TrimSpace(string(result.Stdout)), err
}

func generatedRootCommit(ctx context.Context, r Runner, s Settings, dir, rules, parent string) (string, string, error) {
	blob, err := rootGit(ctx, r, s, dir, strings.NewReader(rules), "hash-object", "-w", "--stdin")
	if err != nil || !rootObjectID.MatchString(blob) {
		return "", "", errPrivateRoot
	}
	tree, err := rootGit(ctx, r, s, dir, strings.NewReader("100644 blob "+blob+"\tAGENTS.md\n100644 blob "+blob+"\tCLAUDE.md\n"), "mktree")
	if err != nil || !rootObjectID.MatchString(tree) {
		return "", "", errPrivateRoot
	}
	args := []string{"commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	head, err := rootGit(ctx, r, s, dir, strings.NewReader("dev-env generated project rules\n"), args...)
	if err != nil || !rootObjectID.MatchString(head) {
		return "", "", errPrivateRoot
	}
	return head, tree, nil
}

func preparePrivateRoot(ctx context.Context, r Runner, s Settings, next *privateRootReceipt, rules string, authority *projectCatalogAuthority) error {
	staging := filepath.Join(filepath.Dir(next.Root), "."+filepath.Base(next.Root)+".agentd-root")
	if _, err := os.Lstat(staging); !errors.Is(err, os.ErrNotExist) {
		return errPrivateRoot
	}
	if err := os.Mkdir(staging, 0o755); err != nil {
		return err
	}
	if _, err := rootGit(ctx, r, s, staging, nil, "init", "--quiet", "--template=", "--initial-branch="+privateRootBranch); err != nil {
		return err
	}
	config, err := os.ReadFile(filepath.Join(staging, ".git", "config"))
	if err != nil {
		return errPrivateRoot
	}
	next.ConfigDigest = projectcatalog.Digest(config)
	head, tree, err := generatedRootCommit(ctx, r, s, staging, rules, "")
	if err != nil {
		return err
	}
	if _, err := rootGit(ctx, r, s, staging, nil, "checkout", "--quiet", "-b", privateRootBranch, head); err != nil {
		return err
	}
	if err := setGeneratedRootWrapperModes(ctx, staging, rules, authority); err != nil {
		return err
	}
	if writeFileAtomic(filepath.Join(staging, ".git", "dev-env-project-id"), []byte(next.ID), 0o600) != nil || writeFileAtomic(filepath.Join(staging, ".git", "info", "exclude"), []byte(next.Exclusion), 0o644) != nil {
		return errPrivateRoot
	}
	if authority.confirm(ctx) != nil {
		return errPrivateRoot
	}
	if _, err := os.Lstat(next.Root); !errors.Is(err, os.ErrNotExist) {
		return errPrivateRoot
	}
	if err := os.Rename(staging, next.Root); err != nil {
		return err
	}
	next.Head, next.Tree = head, tree
	return nil
}

// Git creates these two generated files through the process umask. Only a
// successful owned checkout or proved-clean fast-forward calls this helper;
// existing roots must already prove their exact modes before either write.
func setGeneratedRootWrapperModes(ctx context.Context, dir, rules string, authority *projectCatalogAuthority) error {
	var files []*os.File
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		path := filepath.Join(dir, name)
		if noSymlinkComponents(path) != nil {
			return errPrivateRoot
		}
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return errPrivateRoot
		}
		files = append(files, f)
		if proveGeneratedRootWrapper(f, rules) != nil {
			return errPrivateRoot
		}
	}
	if authority.confirm(ctx) != nil {
		return errPrivateRoot
	}
	for _, f := range files {
		if proveGeneratedRootWrapper(f, rules) != nil || f.Chmod(0o644) != nil || f.Sync() != nil {
			return errPrivateRoot
		}
		if fi, err := f.Stat(); err != nil || fi.Mode().Perm() != 0o644 {
			return errPrivateRoot
		}
	}
	return nil
}

func proveGeneratedRootWrapper(f *os.File, rules string) error {
	fi, err := f.Stat()
	current, pathErr := os.Lstat(f.Name())
	if err != nil || pathErr != nil || !fi.Mode().IsRegular() || fi.Mode() & ^os.FileMode(0o644) != 0 || fi.Size() != int64(len(rules)) || !os.SameFile(fi, current) || noSymlinkComponents(f.Name()) != nil {
		return errPrivateRoot
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return errPrivateRoot
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return errPrivateRoot
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(len(rules))+1))
	if err != nil || string(data) != rules {
		return errPrivateRoot
	}
	return nil
}

func provePrivateRoot(ctx context.Context, r Runner, s Settings, receipt privateRootReceipt, anchors []PrivateProjectSource, identities map[string]privateAnchorIdentity) error {
	snapshot, err := projectcatalog.ParseSnapshot(receipt.Snapshot)
	if err != nil || receipt.Version != 1 || receipt.ID == "" || receipt.Root != filepath.Join(s.Home, "codex", snapshot.Project()) || receipt.GitDir != filepath.Join(receipt.Root, ".git") || receipt.Branch != privateRootBranch || !rootObjectID.MatchString(receipt.Head) || !rootObjectID.MatchString(receipt.Tree) || receipt.WrapperDigest != projectcatalog.Digest([]byte(snapshot.ProjectRules())) {
		return errPrivateRoot
	}
	if privateGitStorageClear(receipt.GitDir, "refs/heads/"+privateRootBranch, "logs/HEAD", "logs/refs/heads/"+privateRootBranch) != nil || projectGitOperationClear(ctx, r, s, receipt.Root) != nil {
		return errPrivateRoot
	}
	for _, path := range []string{"config", "HEAD", "index", "dev-env-project-id", "info/exclude"} {
		full := filepath.Join(receipt.GitDir, path)
		fi, err := os.Lstat(full)
		if err != nil || !fi.Mode().IsRegular() || noSymlinkComponents(full) != nil {
			return errPrivateRoot
		}
		if (path == "dev-env-project-id" && fi.Size() != int64(len(receipt.ID))) || (path == "info/exclude" && fi.Size() != int64(len(receipt.Exclusion))) {
			return errPrivateRoot
		}
		if path == "config" && fi.Size() > 64<<10 {
			return errPrivateRoot
		}
	}
	config, err := os.ReadFile(filepath.Join(receipt.GitDir, "config"))
	if err != nil || receipt.ConfigDigest != projectcatalog.Digest(config) {
		return errPrivateRoot
	}
	format, err := rootGit(ctx, r, s, receipt.Root, nil, "config", "--local", "--get", "extensions.refStorage")
	if err != nil && ExitCodeOf(err) != 1 || err == nil && format != "files" {
		return errPrivateRoot
	}
	if data, err := os.ReadFile(filepath.Join(receipt.GitDir, "dev-env-project-id")); err != nil || string(data) != receipt.ID {
		return errPrivateRoot
	}
	if data, err := os.ReadFile(filepath.Join(receipt.GitDir, "info", "exclude")); err != nil || string(data) != receipt.Exclusion {
		return errPrivateRoot
	}
	for _, path := range []string{"info/attributes", "objects/info/alternates", "objects/info/http-alternates", "config.worktree"} {
		if _, err := os.Lstat(filepath.Join(receipt.GitDir, path)); !errors.Is(err, os.ErrNotExist) {
			return errPrivateRoot
		}
	}
	if len(receipt.Repositories) != len(snapshot.Repositories()) || len(identities) != len(anchors) {
		return errPrivateRoot
	}
	var exclusion string
	for i, repo := range snapshot.Repositories() {
		if receipt.Repositories[i].Repository != repo || !rootObjectID.MatchString(receipt.Repositories[i].Target) || receipt.Repositories[i].State != "Ready" {
			return errPrivateRoot
		}
		exclusion += "/" + repo.Name + "/\n"
	}
	if receipt.Exclusion != exclusion {
		return errPrivateRoot
	}
	for _, check := range []struct {
		args []string
		want string
	}{
		{[]string{"rev-parse", "--show-toplevel"}, receipt.Root},
		{[]string{"rev-parse", "--path-format=absolute", "--git-common-dir"}, receipt.GitDir},
		{[]string{"rev-parse", "--verify", "HEAD^{commit}"}, receipt.Head},
		{[]string{"rev-parse", "--verify", "HEAD^{tree}"}, receipt.Tree},
		{[]string{"symbolic-ref", "--quiet", "HEAD"}, "refs/heads/" + privateRootBranch},
		{[]string{"remote"}, ""},
		{[]string{"ls-files", "-v"}, "H AGENTS.md\nH CLAUDE.md"},
		{[]string{"diff", "--cached", "--quiet", "--no-ext-diff", "--no-textconv", "HEAD", "--"}, ""},
	} {
		actual, err := rootGit(ctx, r, s, receipt.Root, nil, check.args...)
		if err != nil || actual != check.want {
			return errPrivateRoot
		}
	}
	worktrees, err := rootGit(ctx, r, s, receipt.Root, nil, "worktree", "list", "--porcelain")
	if err != nil || strings.Count(worktrees, "worktree ") != 1 || !strings.HasPrefix(worktrees, "worktree "+receipt.Root+"\n") {
		return errPrivateRoot
	}
	allowed := []string{".git", "AGENTS.md", "CLAUDE.md"}
	for _, anchor := range anchors {
		identity, owned := identities[anchor.Repository.Name]
		if !owned || !projectcatalog.ValidName(anchor.Repository.Name) || validatePrivateAnchor(ctx, r, s, anchor.Repository, filepath.Join(receipt.Root, anchor.Repository.Name), anchor.Target, &identity) != nil {
			return errPrivateRoot
		}
		allowed = append(allowed, anchor.Repository.Name)
	}
	entries, err := os.ReadDir(receipt.Root)
	if err != nil {
		return errPrivateRoot
	}
	for _, entry := range entries {
		if !slices.Contains(allowed, entry.Name()) {
			return errPrivateRoot
		}
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		path := filepath.Join(receipt.Root, name)
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o644 || fi.Size() != int64(len(snapshot.ProjectRules())) {
			return errPrivateRoot
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != snapshot.ProjectRules() {
			return errPrivateRoot
		}
	}
	return nil
}

func privateAnchorMetadata(ctx context.Context, r Runner, s Settings, repo projectcatalog.Repository, anchor string) (privateAnchorIdentity, error) {
	var identity privateAnchorIdentity
	if validateProjectReference(ctx, r, s, repo) != nil || noSymlinkComponents(filepath.Join(anchor, ".git")) != nil {
		return identity, errPrivateRoot
	}
	gitDir, err := projectGit(ctx, r, s, anchor, "rev-parse", "--absolute-git-dir")
	if err != nil || filepath.Dir(gitDir) != filepath.Join(s.ClonePath(repo.Name), ".git", "worktrees") || noSymlinkComponents(gitDir) != nil {
		return identity, errPrivateRoot
	}
	read := func(path string) (string, error) {
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > privateAnchorMetadataLimit || noSymlinkComponents(path) != nil {
			return "", errPrivateRoot
		}
		data, err := os.ReadFile(path)
		return string(data), err
	}
	identity.GitDir = gitDir
	if identity.GitFile, err = read(filepath.Join(anchor, ".git")); err != nil || identity.GitFile != "gitdir: "+gitDir+"\n" {
		return identity, errPrivateRoot
	}
	if identity.Backpointer, err = read(filepath.Join(gitDir, "gitdir")); err != nil || identity.Backpointer != filepath.Join(anchor, ".git")+"\n" {
		return identity, errPrivateRoot
	}
	if identity.CommonFile, err = read(filepath.Join(gitDir, "commondir")); err != nil || filepath.Clean(filepath.Join(gitDir, strings.TrimSpace(identity.CommonFile))) != filepath.Join(s.ClonePath(repo.Name), ".git") {
		return identity, errPrivateRoot
	}
	if _, err := read(filepath.Join(gitDir, "HEAD")); err != nil {
		return identity, errPrivateRoot
	}
	index := filepath.Join(gitDir, "index")
	if fi, err := os.Lstat(index); err != nil || !fi.Mode().IsRegular() || noSymlinkComponents(index) != nil {
		return identity, errPrivateRoot
	}
	for _, name := range []string{"ORIG_HEAD", "logs/HEAD"} {
		path := filepath.Join(gitDir, name)
		if err := noSymlinkComponents(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return identity, errPrivateRoot
		}
		if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() || err != nil && !errors.Is(err, os.ErrNotExist) {
			return identity, errPrivateRoot
		}
	}
	if privateOrigHeadClear(gitDir) != nil {
		return identity, errPrivateRoot
	}
	if _, err := os.Lstat(filepath.Join(gitDir, "config.worktree")); !errors.Is(err, os.ErrNotExist) {
		return identity, errPrivateRoot
	}
	return identity, nil
}

func validatePrivateAnchor(ctx context.Context, r Runner, s Settings, repo projectcatalog.Repository, anchor, expected string, owned *privateAnchorIdentity) error {
	identity, err := privateAnchorMetadata(ctx, r, s, repo, anchor)
	if err != nil || owned == nil || identity != *owned {
		return errPrivateRoot
	}
	common, err := projectGit(ctx, r, s, anchor, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || common != filepath.Join(s.ClonePath(repo.Name), ".git") {
		return errPrivateRoot
	}
	top, err := projectGit(ctx, r, s, anchor, "rev-parse", "--show-toplevel")
	if err != nil || top != anchor {
		return errPrivateRoot
	}
	if _, err := projectGit(ctx, r, s, anchor, "symbolic-ref", "--quiet", "HEAD"); ExitCodeOf(err) != 1 {
		return errPrivateRoot
	}
	if _, err := projectIndexProof(ctx, r, s, anchor, true); err != nil {
		return errPrivateRoot
	}
	head, err := projectGit(ctx, r, s, anchor, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || (expected != "" && head != expected) {
		return errPrivateRoot
	}
	return nil
}

func advancePrivateAnchor(ctx context.Context, r Runner, s Settings, repo projectcatalog.Repository, anchor, target string, owned *privateAnchorIdentity, authority *projectCatalogAuthority) (privateAnchorIdentity, error) {
	if _, err := os.Lstat(anchor); errors.Is(err, os.ErrNotExist) {
		if owned != nil || authority.confirm(ctx) != nil || validateProjectReference(ctx, r, s, repo) != nil {
			return privateAnchorIdentity{}, errPrivateRoot
		}
		_, err := projectGit(ctx, r, s, s.ClonePath(repo.Name), "worktree", "add", "--detach", anchor, target)
		if err != nil {
			return privateAnchorIdentity{}, errPrivateRoot
		}
		identity, err := privateAnchorMetadata(ctx, r, s, repo, anchor)
		if err != nil || validatePrivateAnchor(ctx, r, s, repo, anchor, target, &identity) != nil {
			return identity, errPrivateRoot
		}
		return identity, nil
	} else if err != nil {
		return privateAnchorIdentity{}, errPrivateRoot
	}
	if validatePrivateAnchor(ctx, r, s, repo, anchor, "", owned) != nil {
		return privateAnchorIdentity{}, errPrivateRoot
	}
	head, err := projectGit(ctx, r, s, anchor, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return privateAnchorIdentity{}, errPrivateRoot
	}
	if head == target {
		return *owned, nil
	}
	if _, err := projectGit(ctx, r, s, anchor, "merge-base", "--is-ancestor", head, target); err != nil || authority.confirm(ctx) != nil || validatePrivateAnchor(ctx, r, s, repo, anchor, head, owned) != nil {
		return privateAnchorIdentity{}, errPrivateRoot
	}
	if _, err = projectGit(ctx, r, s, anchor, "-c", "merge.autostash=false", "merge", "--ff-only", "--no-edit", "--no-autostash", target); err != nil || validatePrivateAnchor(ctx, r, s, repo, anchor, target, owned) != nil {
		return privateAnchorIdentity{}, errPrivateRoot
	}
	return *owned, nil
}
