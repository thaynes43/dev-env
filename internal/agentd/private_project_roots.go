package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
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
	// Reason is why a Failed refresh stopped (protocol.RootReasons). When one
	// member repository is why, Repository is its catalog name and Cause its
	// clean-checkout reason (protocol.RepositoryReasons).
	Reason     string `json:"reason,omitempty"`
	Repository string `json:"repository,omitempty"`
	Cause      string `json:"cause,omitempty"`
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

// The pending record holds two root states, each with up to the maximum
// anchors; the bound is twice the receipt's.
const privateRootPendingLimit = 2 * privateRootReceiptLimit

// Generated wrappers hold the bounded rules plus a header that names at most
// MaxProjectRepositories repositories.
const privateRootWrapperLimit = projectcatalog.MaxRulesBytes + (64 << 10)

// privateRootState is one exact, component-wise state of an owned root. An
// empty Head means the root is not published yet.
type privateRootState struct {
	Head          string                        `json:"head"`
	Tree          string                        `json:"tree"`
	WrapperDigest string                        `json:"wrapperDigest"`
	Exclusion     string                        `json:"exclusion"`
	Anchors       map[string]privateAnchorState `json:"anchors"`
}

// A nil Identity names an anchor that this advance creates and has not yet
// recorded.
type privateAnchorState struct {
	Repository projectcatalog.Repository `json:"repository"`
	Target     string                    `json:"target"`
	Identity   *privateAnchorIdentity    `json:"identity,omitempty"`
}

// privateRootPending is written before agentd's first change to a root and
// updated as it records new anchors. Each component of the live root is then
// at From or To; a later refresh proves that and resumes from the state it
// finds (DESIGN-007, recoverable advance).
type privateRootPending struct {
	Version      int              `json:"version"`
	ID           string           `json:"id"`
	Root         string           `json:"root"`
	ConfigDigest string           `json:"configDigest"`
	From         privateRootState `json:"from"`
	To           privateRootState `json:"to"`
}

func (receipt privateRootReceipt) state(anchors []PrivateProjectSource, identities map[string]privateAnchorIdentity) privateRootState {
	state := privateRootState{Head: receipt.Head, Tree: receipt.Tree, WrapperDigest: receipt.WrapperDigest, Exclusion: receipt.Exclusion, Anchors: map[string]privateAnchorState{}}
	for _, anchor := range anchors {
		var owned *privateAnchorIdentity
		if identity, ok := identities[anchor.Repository.Name]; ok {
			owned = &identity
		}
		state.Anchors[anchor.Repository.Name] = privateAnchorState{Repository: anchor.Repository, Target: anchor.Target, Identity: owned}
	}
	return state
}

func refreshPrivateProjectRoot(ctx context.Context, r Runner, s Settings, snapshot projectcatalog.Snapshot, authority *projectCatalogAuthority) (report PrivateProjectRootReport) {
	report = PrivateProjectRootReport{Project: snapshot.Project(), State: "Failed", CatalogRevision: snapshot.CatalogRevision(), RulesRevision: snapshot.RulesRevision(), ObservedAt: time.Now().UTC(), Detail: errPrivateRoot.Error()}
	// stop records why the refresh ended here: err's own root refusal when it
	// carries one, otherwise reason, with the member repository and its
	// clean-checkout cause when one repository is why (DESIGN-006).
	stop := func(reason, repository string, err error) PrivateProjectRootReport {
		refusal := &rootError{reason: reason, repository: repository, cause: reasonOf(err, "")}
		var known *rootError
		if errors.As(err, &known) {
			refusal = known
		}
		report.Reason, report.Repository, report.Cause = refusal.reason, refusal.repository, refusal.cause
		if ended := endedReason(ctx); ended != "" && report.Reason != protocol.ReasonAnchorRemoved {
			report.Reason = ended
		}
		return report
	}
	parent := filepath.Join(s.StateDir, "project-roots")
	if ensureWorkspaceDirectory(parent) != nil {
		return stop(protocol.ReasonStateUncertain, "", nil)
	}
	operation := filepath.Join(parent, snapshot.Project()+".operation.json")
	started := report
	started.State = "Started"
	if writeWorkspaceJSON(operation, started) != nil {
		return stop(protocol.ReasonReceiptUnconfirmed, "", nil)
	}
	defer func() {
		report.ObservedAt = time.Now().UTC()
		if writeWorkspaceJSON(operation, report) != nil {
			report.State, report.Detail = "Failed", "private project root outcome retention is unconfirmed"
			report.Reason, report.Repository, report.Cause = protocol.ReasonReceiptUnconfirmed, "", ""
		}
	}()
	if authority.confirm(ctx) != nil {
		return stop(protocol.ReasonCatalogUnconfirmed, "", nil)
	}
	if ensureWorkspaceDirectory(filepath.Join(s.Home, "codex")) != nil {
		return stop(protocol.ReasonStateUncertain, "", nil)
	}
	root := filepath.Join(s.Home, "codex", snapshot.Project())
	receiptPath := filepath.Join(parent, snapshot.Project()+".json")
	pendingPath := filepath.Join(parent, snapshot.Project()+".pending.json")
	current, id, configDigest, err := privateRootStart(ctx, r, s, root, receiptPath, pendingPath, authority)
	if err != nil {
		return stop(protocol.ReasonRootUnproven, "", err)
	}
	published := current.Head != ""
	// Removed anchors are never deleted or silently reclassified as owned
	// by a newer map. A later explicit migration can decide their future.
	// Sorted, so the same state always names the same anchor.
	for _, name := range slices.Sorted(maps.Keys(current.Anchors)) {
		prior := current.Anchors[name]
		if !slices.ContainsFunc(snapshot.Repositories(), func(repo projectcatalog.Repository) bool {
			return repo.Name == name && repo.GitHub == prior.Repository.GitHub
		}) {
			return stop(protocol.ReasonAnchorRemoved, name, nil)
		}
	}
	var sources []PrivateProjectSource
	for _, repo := range snapshot.Repositories() {
		if authority.confirm(ctx) != nil {
			return stop(protocol.ReasonCatalogUnconfirmed, "", nil)
		}
		if err := validateProjectReference(ctx, r, s, repo); err != nil {
			return stop(protocol.ReasonRepositoryNotReady, repo.Name, withReason(protocol.ReasonReferenceUncertain, err))
		}
		if err := projectGitOperationClear(ctx, r, s, s.ClonePath(repo.Name)); err != nil {
			return stop(protocol.ReasonRepositoryNotReady, repo.Name, withReason(protocol.ReasonGitOperationPresent, err))
		}
		target, fetched, err := fetchProjectTarget(ctx, r, s, repo)
		observed := PrivateProjectSource{Repository: repo, Target: target, FetchedAt: fetched, State: "Preserved", Detail: "project source fetch is unconfirmed"}
		report.Repositories = append(report.Repositories, observed)
		if err != nil {
			return stop(protocol.ReasonFetchFailed, repo.Name, withReason(protocol.ReasonFetchFailed, err))
		}
		if !rootObjectID.MatchString(target) {
			return stop(protocol.ReasonFetchFailed, repo.Name, reasonErr(protocol.ReasonUncertain, "fetched target is not an object id"))
		}
		observed.State, observed.Detail = "Ready", "fresh project anchor target"
		sources = append(sources, observed)
	}
	next := privateRootReceipt{Version: 1, ID: id, Root: root, GitDir: filepath.Join(root, ".git"), Branch: privateRootBranch, WrapperDigest: projectcatalog.Digest([]byte(snapshot.ProjectRules())), ConfigDigest: configDigest, Repositories: sources, Anchors: map[string]privateAnchorIdentity{}}
	next.Snapshot, _ = json.Marshal(snapshot)
	for _, repo := range snapshot.Repositories() {
		next.Exclusion += "/" + repo.Name + "/\n"
	}
	// Prove every anchor fast-forward before the first change, so a
	// deterministic refusal leaves the root exactly where it was, and fetch
	// what each checkout needs, so the fast-forwards below are local work.
	to := privateRootState{Head: current.Head, Tree: current.Tree, WrapperDigest: next.WrapperDigest, Exclusion: next.Exclusion, Anchors: map[string]privateAnchorState{}}
	for _, source := range sources {
		name := source.Repository.Name
		state := privateAnchorState{Repository: source.Repository, Target: source.Target}
		if prior, exists := current.Anchors[name]; exists {
			state.Identity = prior.Identity
			if prior.Target != source.Target {
				anchor := filepath.Join(root, name)
				if _, err := projectGit(ctx, r, s, anchor, "merge-base", "--is-ancestor", prior.Target, source.Target); err != nil {
					if ExitCodeOf(err) == 1 {
						return stop(protocol.ReasonAnchorNotFastForward, name, nil)
					}
					return stop(protocol.ReasonAnchorUnproven, name, reasonErr(protocol.ReasonUncertain, "anchor ancestry is uncertain"))
				}
				if err := prefetchPrivateAnchor(ctx, r, s, anchor, prior.Target, source.Target); err != nil {
					return stop(protocol.ReasonFetchFailed, name, withReason(protocol.ReasonFetchFailed, err))
				}
			}
		}
		to.Anchors[name] = state
	}
	if published && current.WrapperDigest != next.WrapperDigest {
		// Generated objects only; the live index and HEAD are unchanged.
		to.Head, to.Tree, err = generatedRootCommit(ctx, r, s, root, snapshot.ProjectRules(), current.Head)
		if err != nil {
			return stop(protocol.ReasonRootPrepareFailed, "", nil)
		}
	}
	if id == "" {
		if id, err = newUUID(); err != nil {
			return stop(protocol.ReasonRootPrepareFailed, "", nil)
		}
		next.ID = id
	}
	// From here every change is recorded first: the live root stays, component
	// by component, at From or To, and a later refresh resumes from it.
	pending := privateRootPending{Version: 1, ID: id, Root: root, ConfigDigest: configDigest, From: current, To: to}
	if authority.confirm(ctx) != nil {
		return stop(protocol.ReasonCatalogUnconfirmed, "", nil)
	}
	if writePrivateRootPending(pendingPath, pending) != nil {
		return stop(protocol.ReasonReceiptUnconfirmed, "", nil)
	}
	if !published {
		staging := privateRootStaging(root, id)
		head, tree, config, err := preparePrivateRoot(ctx, r, s, staging, id, snapshot.ProjectRules(), next.Exclusion, authority)
		if err != nil {
			return stop(protocol.ReasonRootPrepareFailed, "", nil)
		}
		pending.ConfigDigest, pending.To.Head, pending.To.Tree = config, head, tree
		if writePrivateRootPending(pendingPath, pending) != nil {
			return stop(protocol.ReasonReceiptUnconfirmed, "", nil)
		}
		if authority.confirm(ctx) != nil {
			return stop(protocol.ReasonCatalogUnconfirmed, "", nil)
		}
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			return stop(protocol.ReasonRootPrepareFailed, "", nil)
		}
		if os.Rename(staging, root) != nil {
			return stop(protocol.ReasonRootPrepareFailed, "", nil)
		}
		configDigest, next.ConfigDigest = config, config
		to.Head, to.Tree = head, tree
		current = privateRootState{Head: head, Tree: tree, WrapperDigest: next.WrapperDigest, Exclusion: next.Exclusion, Anchors: map[string]privateAnchorState{}}
	}
	next.Head, next.Tree = to.Head, to.Tree
	for _, source := range sources {
		name := source.Repository.Name
		var prior *privateAnchorIdentity
		if state, exists := current.Anchors[name]; exists {
			prior = state.Identity
		}
		if authority.confirm(ctx) != nil {
			return stop(protocol.ReasonCatalogUnconfirmed, "", nil)
		}
		identity, err := advancePrivateAnchor(ctx, r, s, source.Repository, filepath.Join(root, name), source.Target, prior, authority)
		if err != nil {
			return stop(protocol.ReasonAnchorUnproven, name, err)
		}
		next.Anchors[name] = identity
		if prior == nil {
			state := pending.To.Anchors[name]
			state.Identity = &identity
			pending.To.Anchors[name] = state
			if writePrivateRootPending(pendingPath, pending) != nil {
				return stop(protocol.ReasonReceiptUnconfirmed, "", nil)
			}
		}
	}
	if current.Head != to.Head || current.Exclusion != next.Exclusion {
		// Re-prove the exact reached state before writing into the root.
		reached := current
		reached.Anchors = map[string]privateAnchorState{}
		for _, source := range sources {
			identity := next.Anchors[source.Repository.Name]
			reached.Anchors[source.Repository.Name] = privateAnchorState{Repository: source.Repository, Target: source.Target, Identity: &identity}
		}
		if authority.confirm(ctx) != nil {
			return stop(protocol.ReasonCatalogUnconfirmed, "", nil)
		}
		if proof, err := provePrivateRootReached(ctx, r, s, id, root, configDigest, reached, reached); err != nil || proof.modes {
			return stop(protocol.ReasonRootUnproven, "", err)
		}
		if current.Head != to.Head {
			if err := privateRootStepTime(ctx); err != nil {
				return stop(protocol.ReasonBudgetReserve, "", nil)
			}
			if _, err := rootGit(ctx, r, s, root, nil, "-c", "merge.autostash=false", "merge", "--ff-only", "--no-edit", "--no-autostash", to.Head); err != nil {
				return stop(protocol.ReasonRootAdvanceFailed, "", nil)
			}
			if setGeneratedRootWrapperModes(ctx, root, snapshot.ProjectRules(), authority) != nil {
				return stop(protocol.ReasonRootAdvanceFailed, "", nil)
			}
		}
		if current.Exclusion != next.Exclusion && writeFileAtomic(filepath.Join(next.GitDir, "info", "exclude"), []byte(next.Exclusion), 0o644) != nil {
			return stop(protocol.ReasonRootAdvanceFailed, "", nil)
		}
	}
	encoded, err := json.Marshal(next)
	if err != nil || len(encoded)+1 > privateRootReceiptLimit {
		return stop(protocol.ReasonReceiptUnconfirmed, "", nil)
	}
	if authority.confirm(ctx) != nil {
		return stop(protocol.ReasonCatalogUnconfirmed, "", nil)
	}
	if err := provePrivateRoot(ctx, r, s, next, sources, next.Anchors); err != nil {
		return stop(protocol.ReasonRootUnproven, "", err)
	}
	if writeWorkspaceJSON(receiptPath, next) != nil {
		return stop(protocol.ReasonReceiptUnconfirmed, "", nil)
	}
	// The receipt now names the reached state. A pending record that cannot be
	// removed is still accepted by the next refresh, which finishes this one.
	if os.Remove(pendingPath) != nil {
		return stop(protocol.ReasonReceiptUnconfirmed, "", nil)
	}
	report.State, report.Head, report.Repositories, report.Detail = "Ready", next.Head, sources, "managed Git root and detached repository anchors verified"
	return report
}

// privateRootStart proves where a refresh begins: the exact saved receipt, or
// agentd's own recorded partial advance. Anything else is preserved. An empty
// Head in the returned state means the root is not published yet.
func privateRootStart(ctx context.Context, r Runner, s Settings, root, receiptPath, pendingPath string, authority *projectCatalogAuthority) (privateRootState, string, string, error) {
	unpublished := privateRootState{Anchors: map[string]privateAnchorState{}}
	_, rootErr := os.Lstat(root)
	if rootErr != nil && !errors.Is(rootErr, os.ErrNotExist) {
		return unpublished, "", "", errPrivateRoot
	}
	published := rootErr == nil
	var receipt privateRootReceipt
	_, receiptErr := os.Lstat(receiptPath)
	if receiptErr != nil && !errors.Is(receiptErr, os.ErrNotExist) {
		return unpublished, "", "", errPrivateRoot
	}
	saved := receiptErr == nil
	if saved && (readWorkspaceJSONBounded(receiptPath, &receipt, privateRootReceiptLimit) != nil || receipt.Root != root) {
		return unpublished, "", "", errPrivateRoot
	}
	var pending privateRootPending
	if _, err := os.Lstat(pendingPath); errors.Is(err, os.ErrNotExist) {
		switch {
		case !published && !saved:
			return unpublished, "", "", nil
		case !published || !saved:
			// A vanished owned root, or a root agentd never recorded.
			return unpublished, "", "", errPrivateRoot
		}
		if err := provePrivateRoot(ctx, r, s, receipt, receipt.Repositories, receipt.Anchors); err != nil {
			return unpublished, "", "", err
		}
		return receipt.state(receipt.Repositories, receipt.Anchors), receipt.ID, receipt.ConfigDigest, nil
	} else if err != nil || readWorkspaceJSONBounded(pendingPath, &pending, privateRootPendingLimit) != nil || pending.Version != 1 || pending.ID == "" || pending.Root != root || saved && receipt.ID != pending.ID {
		return unpublished, "", "", errPrivateRoot
	}
	if !published {
		// agentd never removes a published root, so only an advance that had
		// not yet published can resume here, from its own staging.
		if pending.From.Head != "" || saved || discardPrivateRootStaging(privateRootStaging(root, pending.ID), pending.ID, pending.To.WrapperDigest) != nil {
			return unpublished, "", "", errPrivateRoot
		}
		return unpublished, pending.ID, "", nil
	}
	proof, err := provePrivateRootReached(ctx, r, s, pending.ID, root, pending.ConfigDigest, pending.From, pending.To)
	if err != nil {
		return unpublished, "", "", err
	}
	if proof.modes && setGeneratedRootWrapperModes(ctx, root, proof.rules, authority) != nil {
		return unpublished, "", "", errPrivateRoot
	}
	return proof.state, pending.ID, pending.ConfigDigest, nil
}

func writePrivateRootPending(path string, pending privateRootPending) error {
	encoded, err := json.Marshal(pending)
	if err != nil || len(encoded)+1 > privateRootPendingLimit {
		return errPrivateRoot
	}
	return writeWorkspaceJSON(path, pending)
}

func privateRootStaging(root, id string) string {
	return filepath.Join(filepath.Dir(root), "."+filepath.Base(root)+".agentd-root-"+id)
}

// discardPrivateRootStaging removes agentd's own unpublished staging, named by
// the pending root ID recorded before it was created, when it holds nothing
// but what preparePrivateRoot writes: wrappers at the recorded digest and a
// .git that privateRootStagingGitClear accepts. Changed staging is preserved.
func discardPrivateRootStaging(staging, id, wrapperDigest string) error {
	fi, err := os.Lstat(staging)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !fi.IsDir() || noSymlinkComponents(staging) != nil || !ownedByProcess(fi) {
		return errPrivateRoot
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		return errPrivateRoot
	}
	for _, entry := range entries {
		path := filepath.Join(staging, entry.Name())
		fi, err := os.Lstat(path)
		if err != nil {
			return errPrivateRoot
		}
		switch entry.Name() {
		case ".git":
			if !fi.IsDir() || privateRootStagingGitClear(path) != nil {
				return errPrivateRoot
			}
			marker := filepath.Join(path, "dev-env-project-id")
			if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
				if data, err := readPrivateRootFile(marker, int64(len(id))); err != nil || data != id {
					return errPrivateRoot
				}
			}
		case "AGENTS.md", "CLAUDE.md":
			st, ok := fi.Sys().(*syscall.Stat_t)
			if !ok || st.Nlink != 1 || !ownedByProcess(fi) {
				return errPrivateRoot
			}
			if data, err := readPrivateRootFile(path, privateRootWrapperLimit); err != nil || projectcatalog.Digest([]byte(data)) != wrapperDigest {
				return errPrivateRoot
			}
		default:
			return errPrivateRoot
		}
	}
	return os.RemoveAll(staging)
}

// privateRootStagingGitPaths names everything an empty-template files-backend
// init, the generated blob/tree/commit, the checkout of privateRootBranch
// (dev-env/project), an interrupted Git lock, and agentd's own marker and
// exclusion writes (with their atomic-write temporaries) create under .git.
var privateRootStagingGitPaths = regexp.MustCompile(`^(?:(?:HEAD|config|index)(?:\.lock)?|dev-env-project-id|\.dev-env-project-id\.agentd-[^/]+|info|info/exclude|info/\.exclude\.agentd-[^/]+|logs|logs/HEAD|logs/refs|logs/refs/heads|logs/refs/heads/dev-env|logs/refs/heads/dev-env/project|refs|refs/heads|refs/tags|refs/heads/dev-env|refs/heads/dev-env/project(?:\.lock)?|objects|objects/info|objects/pack|objects/[0-9a-f]{2}|objects/[0-9a-f]{2}/(?:[0-9a-f]{38}|[0-9a-f]{62}|tmp_obj_[A-Za-z0-9]+))$`)

// privateRootStagingGitClear proves a staging .git holds only those paths, each
// a directory or a single-link regular file owned by this process, within a
// bounded walk that never follows a symlink.
func privateRootStagingGitClear(gitDir string) error {
	count := 0
	return filepath.WalkDir(gitDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return errPrivateRoot
		}
		if count++; count > 1024 {
			return errPrivateRoot
		}
		fi, err := entry.Info()
		if err != nil || !ownedByProcess(fi) {
			return errPrivateRoot
		}
		if path != gitDir {
			rel, err := filepath.Rel(gitDir, path)
			if err != nil || !privateRootStagingGitPaths.MatchString(filepath.ToSlash(rel)) {
				return errPrivateRoot
			}
		}
		if fi.IsDir() {
			return nil
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !fi.Mode().IsRegular() || path == gitDir || !ok || st.Nlink != 1 {
			return errPrivateRoot
		}
		return nil
	})
}

func ownedByProcess(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid())
}

// readPrivateRootFile reads a bounded regular file without following a final
// symlink or blocking on a FIFO.
func readPrivateRootFile(path string, limit int64) (string, error) {
	if noSymlinkComponents(path) != nil {
		return "", errPrivateRoot
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", errPrivateRoot
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() || fi.Size() > limit {
		return "", errPrivateRoot
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return "", errPrivateRoot
	}
	return string(data), nil
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

// preparePrivateRoot builds an unpublished root in staging named by its
// pending ID. The caller records the result before it publishes the root.
func preparePrivateRoot(ctx context.Context, r Runner, s Settings, staging, id, rules, exclusion string, authority *projectCatalogAuthority) (string, string, string, error) {
	if _, err := os.Lstat(staging); !errors.Is(err, os.ErrNotExist) {
		return "", "", "", errPrivateRoot
	}
	if err := os.Mkdir(staging, 0o755); err != nil {
		return "", "", "", err
	}
	if _, err := rootGit(ctx, r, s, staging, nil, "init", "--quiet", "--template=", "--initial-branch="+privateRootBranch); err != nil {
		return "", "", "", err
	}
	config, err := os.ReadFile(filepath.Join(staging, ".git", "config"))
	if err != nil {
		return "", "", "", errPrivateRoot
	}
	head, tree, err := generatedRootCommit(ctx, r, s, staging, rules, "")
	if err != nil {
		return "", "", "", err
	}
	if _, err := rootGit(ctx, r, s, staging, nil, "checkout", "--quiet", "-b", privateRootBranch, head); err != nil {
		return "", "", "", err
	}
	if err := setGeneratedRootWrapperModes(ctx, staging, rules, authority); err != nil {
		return "", "", "", err
	}
	if writeFileAtomic(filepath.Join(staging, ".git", "dev-env-project-id"), []byte(id), 0o600) != nil || writeFileAtomic(filepath.Join(staging, ".git", "info", "exclude"), []byte(exclusion), 0o644) != nil {
		return "", "", "", errPrivateRoot
	}
	return head, tree, projectcatalog.Digest(config), nil
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

// provePrivateRoot proves the live root is exactly a saved receipt's state.
func provePrivateRoot(ctx context.Context, r Runner, s Settings, receipt privateRootReceipt, anchors []PrivateProjectSource, identities map[string]privateAnchorIdentity) error {
	snapshot, err := projectcatalog.ParseSnapshot(receipt.Snapshot)
	if err != nil || receipt.Version != 1 || receipt.ID == "" || receipt.Root != filepath.Join(s.Home, "codex", snapshot.Project()) || receipt.GitDir != filepath.Join(receipt.Root, ".git") || receipt.Branch != privateRootBranch || !rootObjectID.MatchString(receipt.Head) || !rootObjectID.MatchString(receipt.Tree) || receipt.WrapperDigest != projectcatalog.Digest([]byte(snapshot.ProjectRules())) {
		return errPrivateRoot
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
	state := receipt.state(anchors, identities)
	if len(state.Anchors) != len(anchors) {
		return errPrivateRoot
	}
	for _, anchor := range state.Anchors {
		if anchor.Identity == nil {
			return errPrivateRoot
		}
	}
	proof, err := provePrivateRootReached(ctx, r, s, receipt.ID, receipt.Root, receipt.ConfigDigest, state, state)
	if err != nil {
		return err
	}
	if proof.modes || len(proof.state.Anchors) != len(state.Anchors) {
		return errPrivateRoot
	}
	return nil
}

type privateRootProof struct {
	state privateRootState
	// rules holds the wrapper bytes proven at state.WrapperDigest.
	rules string
	// modes reports wrappers at To that still carry the creating umask's mode.
	modes bool
}

// provePrivateRootReached proves the live root is, component by component, at
// from or at to, and returns the state it found. Passing one state twice is an
// exact proof. Only agentd's own recorded advance passes two different states.
func provePrivateRootReached(ctx context.Context, r Runner, s Settings, id, root, configDigest string, from, to privateRootState) (privateRootProof, error) {
	var proof privateRootProof
	gitDir := filepath.Join(root, ".git")
	if id == "" || configDigest == "" || privateGitStorageClear(gitDir, "refs/heads/"+privateRootBranch, "logs/HEAD", "logs/refs/heads/"+privateRootBranch) != nil || projectGitOperationClear(ctx, r, s, root) != nil {
		return proof, errPrivateRoot
	}
	for _, path := range []string{"config", "HEAD", "index", "dev-env-project-id", "info/exclude"} {
		full := filepath.Join(gitDir, path)
		fi, err := os.Lstat(full)
		if err != nil || !fi.Mode().IsRegular() || noSymlinkComponents(full) != nil {
			return proof, errPrivateRoot
		}
		if (path == "dev-env-project-id" && fi.Size() != int64(len(id))) || (path == "info/exclude" && fi.Size() != int64(len(from.Exclusion)) && fi.Size() != int64(len(to.Exclusion))) {
			return proof, errPrivateRoot
		}
		if path == "config" && fi.Size() > 64<<10 {
			return proof, errPrivateRoot
		}
	}
	config, err := os.ReadFile(filepath.Join(gitDir, "config"))
	if err != nil || configDigest != projectcatalog.Digest(config) {
		return proof, errPrivateRoot
	}
	format, err := rootGit(ctx, r, s, root, nil, "config", "--local", "--get", "extensions.refStorage")
	if err != nil && ExitCodeOf(err) != 1 || err == nil && format != "files" {
		return proof, errPrivateRoot
	}
	if data, err := os.ReadFile(filepath.Join(gitDir, "dev-env-project-id")); err != nil || string(data) != id {
		return proof, errPrivateRoot
	}
	exclusion, err := os.ReadFile(filepath.Join(gitDir, "info", "exclude"))
	if err != nil || string(exclusion) != from.Exclusion && string(exclusion) != to.Exclusion {
		return proof, errPrivateRoot
	}
	for _, path := range []string{"info/attributes", "objects/info/alternates", "objects/info/http-alternates", "config.worktree"} {
		if _, err := os.Lstat(filepath.Join(gitDir, path)); !errors.Is(err, os.ErrNotExist) {
			return proof, errPrivateRoot
		}
	}
	head, err := rootGit(ctx, r, s, root, nil, "rev-parse", "--verify", "HEAD^{commit}")
	reached, atTo := from, false
	switch {
	case err == nil && from.Head != "" && head == from.Head:
	case err == nil && to.Head != "" && head == to.Head:
		reached, atTo = to, true
	default:
		return proof, errPrivateRoot
	}
	if !rootObjectID.MatchString(reached.Head) || !rootObjectID.MatchString(reached.Tree) {
		return proof, errPrivateRoot
	}
	proof.state = privateRootState{Head: reached.Head, Tree: reached.Tree, WrapperDigest: reached.WrapperDigest, Exclusion: string(exclusion), Anchors: map[string]privateAnchorState{}}
	for _, check := range []struct {
		args []string
		want string
	}{
		{[]string{"rev-parse", "--show-toplevel"}, root},
		{[]string{"rev-parse", "--path-format=absolute", "--git-common-dir"}, gitDir},
		{[]string{"rev-parse", "--verify", "HEAD^{tree}"}, reached.Tree},
		{[]string{"symbolic-ref", "--quiet", "HEAD"}, "refs/heads/" + privateRootBranch},
		{[]string{"remote"}, ""},
		{[]string{"ls-files", "-v"}, "H AGENTS.md\nH CLAUDE.md"},
		{[]string{"diff", "--cached", "--quiet", "--no-ext-diff", "--no-textconv", "HEAD", "--"}, ""},
	} {
		actual, err := rootGit(ctx, r, s, root, nil, check.args...)
		if err != nil || actual != check.want {
			return proof, errPrivateRoot
		}
	}
	worktrees, err := rootGit(ctx, r, s, root, nil, "worktree", "list", "--porcelain")
	if err != nil || strings.Count(worktrees, "worktree ") != 1 || !strings.HasPrefix(worktrees, "worktree "+root+"\n") {
		return proof, errPrivateRoot
	}
	allowed := []string{".git", "AGENTS.md", "CLAUDE.md"}
	var names []string
	for name := range from.Anchors {
		names = append(names, name)
	}
	for name := range to.Anchors {
		if _, ok := from.Anchors[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		anchor, err := provePrivateAnchorReached(ctx, r, s, filepath.Join(root, name), name, from, to)
		if err != nil {
			return proof, anchorRefusal(name, err)
		}
		if anchor != nil {
			proof.state.Anchors[name] = *anchor
			allowed = append(allowed, name)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return proof, errPrivateRoot
	}
	for _, entry := range entries {
		if !slices.Contains(allowed, entry.Name()) {
			return proof, errPrivateRoot
		}
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		path := filepath.Join(root, name)
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() {
			return proof, errPrivateRoot
		}
		// Git writes a fast-forwarded wrapper through the process umask; only
		// a recorded advance to `to` may still be short of the declared mode.
		if fi.Mode().Perm() != 0o644 {
			if !atTo || fi.Mode() & ^os.FileMode(0o644) != 0 {
				return proof, errPrivateRoot
			}
			proof.modes = true
		}
		data, err := readPrivateRootFile(path, privateRootWrapperLimit)
		if err != nil || projectcatalog.Digest([]byte(data)) != reached.WrapperDigest || name == "CLAUDE.md" && data != proof.rules {
			return proof, errPrivateRoot
		}
		proof.rules = data
	}
	return proof, nil
}

// provePrivateAnchorReached proves one anchor at its from or to commit with
// its recorded identity. An anchor only to declares may be absent (nil), or
// the self-consistent detached worktree that agentd's post-create proof
// accepts when its identity was not yet recorded.
func provePrivateAnchorReached(ctx context.Context, r Runner, s Settings, path, name string, from, to privateRootState) (*privateAnchorState, error) {
	f, inFrom := from.Anchors[name]
	t, inTo := to.Anchors[name]
	if !projectcatalog.ValidName(name) || inFrom && (f.Repository.Name != name || f.Identity == nil) || inTo && t.Repository.Name != name {
		return nil, errPrivateRoot
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if inFrom {
			return nil, errPrivateRoot
		}
		return nil, nil
	} else if err != nil {
		return nil, errPrivateRoot
	}
	reached, targets := t, []string{t.Target}
	if inFrom {
		reached, targets = f, []string{f.Target}
		if inTo {
			if t.Repository.GitHub != f.Repository.GitHub || t.Identity == nil || *t.Identity != *f.Identity {
				return nil, errPrivateRoot
			}
			reached.Repository = t.Repository
			targets = append(targets, t.Target)
		}
	} else if t.Identity == nil {
		identity, err := privateAnchorMetadata(ctx, r, s, t.Repository, path)
		if err != nil {
			return nil, err
		}
		reached.Identity = &identity
	}
	head, err := provePrivateAnchor(ctx, r, s, reached.Repository, path, reached.Identity)
	if err != nil {
		return nil, err
	}
	if !rootObjectID.MatchString(head) || !slices.Contains(targets, head) {
		return nil, errPrivateRoot
	}
	reached.Target = head
	return &reached, nil
}

func privateAnchorMetadata(ctx context.Context, r Runner, s Settings, repo projectcatalog.Repository, anchor string) (privateAnchorIdentity, error) {
	var identity privateAnchorIdentity
	if err := validateProjectReference(ctx, r, s, repo); err != nil {
		return identity, &rootError{reason: protocol.ReasonRepositoryNotReady, repository: repo.Name, cause: reasonOf(err, protocol.ReasonReferenceUncertain)}
	}
	if noSymlinkComponents(filepath.Join(anchor, ".git")) != nil {
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
	head, err := provePrivateAnchor(ctx, r, s, repo, anchor, owned)
	if err != nil {
		return err
	}
	if expected != "" && head != expected {
		return anchorRefusal(repo.Name, nil)
	}
	return nil
}

// provePrivateAnchor proves a clean, detached anchor with the owned identity
// and returns its commit.
func provePrivateAnchor(ctx context.Context, r Runner, s Settings, repo projectcatalog.Repository, anchor string, owned *privateAnchorIdentity) (string, error) {
	identity, err := privateAnchorMetadata(ctx, r, s, repo, anchor)
	if err != nil {
		return "", anchorRefusal(repo.Name, err)
	}
	if owned == nil || identity != *owned {
		return "", anchorRefusal(repo.Name, nil)
	}
	common, err := projectGit(ctx, r, s, anchor, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || common != filepath.Join(s.ClonePath(repo.Name), ".git") {
		return "", anchorRefusal(repo.Name, nil)
	}
	top, err := projectGit(ctx, r, s, anchor, "rev-parse", "--show-toplevel")
	if err != nil || top != anchor {
		return "", anchorRefusal(repo.Name, nil)
	}
	if _, err := projectGit(ctx, r, s, anchor, "symbolic-ref", "--quiet", "HEAD"); ExitCodeOf(err) != 1 {
		return "", anchorRefusal(repo.Name, nil)
	}
	// The clean-checkout proof's own reason is the anchor's cause.
	if _, err := projectIndexProof(ctx, r, s, anchor, true); err != nil {
		return "", anchorRefusal(repo.Name, withReason(protocol.ReasonUncertain, err))
	}
	head, err := projectGit(ctx, r, s, anchor, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", anchorRefusal(repo.Name, nil)
	}
	return head, nil
}

// privateRootStepReserve is how much of the time budget must be left before a
// mutating Git step in a published root starts. The step's network work is
// already done, so it is local, and the budget ends between steps rather than
// inside one. A step it does stop gets SIGTERM, and Git removes its locks
// (ExecRunner); only a hard kill can still leave one (DESIGN-007).
var privateRootStepReserve = 30 * time.Second

func privateRootStepTime(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < privateRootStepReserve {
		return errPrivateRoot
	}
	return nil
}

// prefetchPrivateAnchor fetches, in one batch, every blob a fast-forward of
// anchor from head to target checks out. A blob:none reference otherwise
// fetches them from inside the checkout, after it has already removed the
// paths the target deletes, so a stop there would leave the anchor neither at
// head nor at target. A diffstat between two commits reads neither the index
// nor the files.
func prefetchPrivateAnchor(ctx context.Context, r Runner, s Settings, anchor, head, target string) error {
	_, err := projectGit(ctx, r, s, anchor, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--shortstat", head, target, "--")
	return err
}

func advancePrivateAnchor(ctx context.Context, r Runner, s Settings, repo projectcatalog.Repository, anchor, target string, owned *privateAnchorIdentity, authority *projectCatalogAuthority) (privateAnchorIdentity, error) {
	refuse := func(reason string) error { return &rootError{reason: reason, repository: repo.Name} }
	if _, err := os.Lstat(anchor); errors.Is(err, os.ErrNotExist) {
		if owned != nil {
			// A recorded anchor is gone; it is never recreated silently.
			return privateAnchorIdentity{}, refuse(protocol.ReasonAnchorUnproven)
		}
		if authority.confirm(ctx) != nil {
			return privateAnchorIdentity{}, &rootError{reason: protocol.ReasonCatalogUnconfirmed}
		}
		if err := validateProjectReference(ctx, r, s, repo); err != nil {
			return privateAnchorIdentity{}, &rootError{reason: protocol.ReasonRepositoryNotReady, repository: repo.Name, cause: reasonOf(err, protocol.ReasonReferenceUncertain)}
		}
		if privateRootStepTime(ctx) != nil {
			return privateAnchorIdentity{}, &rootError{reason: protocol.ReasonBudgetReserve}
		}
		// A stopped worktree add removes its own half-made worktree on
		// SIGTERM, so this step may fetch.
		_, err := projectGit(ctx, r, s, s.ClonePath(repo.Name), "worktree", "add", "--detach", anchor, target)
		if err != nil {
			return privateAnchorIdentity{}, refuse(protocol.ReasonAnchorAddFailed)
		}
		identity, err := privateAnchorMetadata(ctx, r, s, repo, anchor)
		if err != nil {
			return identity, anchorRefusal(repo.Name, err)
		}
		if err := validatePrivateAnchor(ctx, r, s, repo, anchor, target, &identity); err != nil {
			return identity, anchorRefusal(repo.Name, err)
		}
		return identity, nil
	} else if err != nil {
		return privateAnchorIdentity{}, refuse(protocol.ReasonAnchorUnproven)
	}
	if err := validatePrivateAnchor(ctx, r, s, repo, anchor, "", owned); err != nil {
		return privateAnchorIdentity{}, anchorRefusal(repo.Name, err)
	}
	head, err := projectGit(ctx, r, s, anchor, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return privateAnchorIdentity{}, refuse(protocol.ReasonAnchorUnproven)
	}
	if head == target {
		return *owned, nil
	}
	if _, err := projectGit(ctx, r, s, anchor, "merge-base", "--is-ancestor", head, target); err != nil {
		if ExitCodeOf(err) == 1 {
			return privateAnchorIdentity{}, refuse(protocol.ReasonAnchorNotFastForward)
		}
		return privateAnchorIdentity{}, &rootError{reason: protocol.ReasonAnchorUnproven, repository: repo.Name, cause: protocol.ReasonUncertain}
	}
	if authority.confirm(ctx) != nil {
		return privateAnchorIdentity{}, &rootError{reason: protocol.ReasonCatalogUnconfirmed}
	}
	if err := validatePrivateAnchor(ctx, r, s, repo, anchor, head, owned); err != nil {
		return privateAnchorIdentity{}, anchorRefusal(repo.Name, err)
	}
	if privateRootStepTime(ctx) != nil {
		return privateAnchorIdentity{}, &rootError{reason: protocol.ReasonBudgetReserve}
	}
	if _, err = projectGit(ctx, r, s, anchor, "-c", "merge.autostash=false", "merge", "--ff-only", "--no-edit", "--no-autostash", target); err != nil {
		return privateAnchorIdentity{}, refuse(protocol.ReasonAnchorAdvanceFailed)
	}
	if err := validatePrivateAnchor(ctx, r, s, repo, anchor, target, owned); err != nil {
		return privateAnchorIdentity{}, anchorRefusal(repo.Name, err)
	}
	return *owned, nil
}
