package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// PrepareProjectSyncStorage is for the explicitly enabled management Job,
// after its live identity and operation receipt have been confirmed. Task and
// host lifecycles never call it and never initialize or repair a marker.
func PrepareProjectSyncStorage(s Settings, initialize bool) error {
	if err := CheckProjectSyncMounts(s); err != nil {
		return err
	}
	paths := []string{s.Home, s.workspaceDir(), s.ReposDir(), filepath.Join(s.Home, "codex"), s.WorkDir()}
	marker := filepath.Join(s.workspaceDir(), "marker.json")
	if _, err := os.Lstat(marker); err == nil {
		return workspaceStoragePreflight(s)
	} else if !errors.Is(err, os.ErrNotExist) || !initialize {
		return errors.New("sync requires a confirmed workspace marker")
	}
	for _, path := range paths[1:] {
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			return errors.New("initializer refuses nonempty or unconfirmed unmarked shared storage")
		}
	}
	if err := createProjectWorkspaceMarker(marker, workspaceMarker{workspaceVersion, s.WorkspaceID}); err != nil {
		return fmt.Errorf("workspace marker creation is unconfirmed: %w", err)
	}
	return workspaceStoragePreflight(s)
}

// CheckProjectSyncMounts checks the actual literal mounts without admitting or
// creating a marker. The fixed actor uses it before writing its private receipt.
func CheckProjectSyncMounts(s Settings) error {
	if err := workspaceMountPreflight(s); err != nil {
		return err
	}
	data, err := readWorkspaceMountInfo()
	if err != nil {
		return errors.New("sync mount table is unavailable")
	}
	paths := []string{s.Home, s.workspaceDir(), s.ReposDir(), filepath.Join(s.Home, "codex"), s.WorkDir()}
	for _, path := range paths {
		writable := false
		for _, line := range strings.Split(string(data), "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 6 && parts[4] == path {
				writable = slices.Contains(strings.Split(parts[5], ","), "rw")
			}
		}
		if !writable {
			return errors.New("sync requires writable private and literal shared mounts")
		}
	}
	return nil
}

var createProjectWorkspaceMarker = func(path string, marker workspaceMarker) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return projectSyncExclusiveFile(path, append(data, '\n'))
}

func projectSyncExclusiveFile(path string, data []byte) error {
	if err := noSymlinkComponents(filepath.Dir(path)); err != nil {
		return err
	}
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

// PrepareProjectSyncGit creates an operation-private Git config. The helper
// reads only the keeper's named, verified read-only token projection at use;
// provider state and the ordinary renderer are not involved. The caller has
// already validated the live Job's mounts before invoking this helper.
func PrepareProjectSyncGit(r Runner, s Settings, operationDir, tokenFile string) (Runner, error) {
	if tokenFile != "/creds/gh_token" || !filepath.IsAbs(operationDir) || filepath.Dir(operationDir) != filepath.Join(s.StateDir, "project-sync") {
		return nil, errors.New("sync Git configuration must be operation-private and use the keeper token projection")
	}
	if err := noSymlinkComponents(operationDir); err != nil {
		return nil, err
	}
	path := filepath.Join(operationDir, "gitconfig")
	helper := projectSyncGitHelper
	data := fmt.Sprintf("[user]\n\tname = %s\n\temail = %s\n[credential \"https://github.com\"]\n\thelper =\n\thelper = %s\n[safe]\n\tdirectory = %s\n\tdirectory = %s\n", strconv.Quote(s.GitUserName), strconv.Quote(s.GitUserEmail), strconv.Quote(helper), strconv.Quote(filepath.Join(s.ReposDir(), "*")), strconv.Quote(filepath.Join(s.WorkDir(), "*")))
	if err := projectSyncExclusiveFile(path, []byte(data)); err != nil {
		return nil, errors.New("private Git configuration write is unconfirmed")
	}
	confirmed, err := os.ReadFile(path)
	fi, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || string(confirmed) != data {
		return nil, errors.New("private Git configuration confirmation failed")
	}
	return projectSyncGitRunner{Runner: r, home: s.Home, config: path}, nil
}

type projectSyncGitRunner struct {
	Runner
	home, config string
}

func (r projectSyncGitRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	if c.Name != "git" {
		return Result{}, errors.New("project synchronization permits Git commands only")
	}
	// Command-level resets override any repository-local helpers. Shared Git
	// configuration must not add a second credential owner or launch askpass.
	c.Args = append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.askPass=", "-c", "credential.helper=", "-c", "credential.https://github.com.helper=", "-c", "credential.https://github.com.helper=" + projectSyncGitHelper}, c.Args...)
	c.Env = append(slices.Clone(c.Env), "HOME="+r.home, "GIT_CONFIG_GLOBAL="+r.config, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=0", "GIT_TERMINAL_PROMPT=0")
	return r.Runner.Run(ctx, c)
}

const projectSyncGitHelper = `!f() { echo username=x-access-token; echo "password=$(cat -- '/creds/gh_token')"; }; f`
