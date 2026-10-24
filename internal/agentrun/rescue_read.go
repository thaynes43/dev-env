package agentrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// rescueRead is `agent-run rescue read`. It copies one rescue-branch tree into
// a new directory. It does not create a session.
func (a *app) rescueRead(ctx context.Context, args []string) error {
	cmd := newCommand("rescue", outputJSON)
	var repo, commit, sessionUID, podUID, project, out string
	cmd.fs.StringVar(&repo, "repo", "", "")
	cmd.fs.StringVar(&commit, "commit", "", "")
	cmd.fs.StringVar(&sessionUID, "session-uid", "", "")
	cmd.fs.StringVar(&podUID, "pod-uid", "", "")
	cmd.fs.StringVar(&project, "project", "", "")
	cmd.fs.StringVar(&out, "out", "", "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("rescue read takes one rescue id, as <session>/<stamp>")
	}
	id := pos[0]
	session, stamp, err := protocol.ParseRescueID(id)
	if err != nil {
		return usageError("%v", err)
	}
	switch {
	case !projectcatalog.ValidName(repo):
		return usageError("--repo names the rescued clone")
	case !protocol.ValidGitObject(commit):
		return usageError("--commit is the rescue-branch commit")
	case !protocol.ValidKubeUID(sessionUID):
		return usageError("--session-uid is the rescued session's UID")
	case !protocol.ValidKubeUID(podUID):
		return usageError("--pod-uid is the pod UID recorded on the rescue")
	case project != "" && !projectcatalog.ValidName(project):
		return usageError("--project names the session's project")
	case out == "":
		return usageError("--out is the new directory for the tree")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("repo", repo)
	q.Set("commit", commit)
	q.Set("sessionUID", sessionUID)
	q.Set("podUID", podUID)
	if project != "" {
		q.Set("project", project)
	}
	var tree apiv1.RescueTree
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.RescueTreePath(session, stamp), q, nil, &tree); err != nil {
		return err
	}
	if err := checkRescueTree(tree, id, repo, commit, sessionUID, podUID, project); err != nil {
		return err
	}
	written, err := writeRescueTree(out, tree.Files)
	if err != nil {
		return err
	}
	return a.printRescueReceipt(cmd.c.output, written)
}

type rescueReceiptFile struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

func checkRescueTree(tree apiv1.RescueTree, id, repo, commit, sessionUID, podUID, project string) error {
	if tree.ID != id || tree.Repo != repo || tree.Commit != commit || tree.SessionUID != sessionUID || tree.PodUID != podUID || tree.Project != project || !protocol.RescueWorkRef(tree.Ref) {
		return errors.New("the rescue tree does not match the request")
	}
	seen := map[string]bool{}
	for _, f := range tree.Files {
		sum := sha256.Sum256(f.Bytes)
		got := hex.EncodeToString(sum[:])
		if !protocol.AcceptableRescueTreePath(f.Path) || !protocol.RegularTreeMode(f.Mode) || !protocol.ValidSHA256(f.SHA256) || got != f.SHA256 || seen[f.Path] {
			return errors.New("the rescue tree does not match its hashes")
		}
		seen[f.Path] = true
	}
	return nil
}

func writeRescueTree(out string, files []apiv1.RescueTreeFile) ([]rescueReceiptFile, error) {
	abs, err := filepath.Abs(out)
	if err != nil {
		return nil, err
	}
	if err := reserveOutputDir(abs); err != nil {
		return nil, err
	}
	if err := os.Mkdir(abs, 0o700); err != nil {
		return nil, errors.New("--out already exists")
	}
	ok := false
	defer func() {
		if ok {
			return
		}
		fi, statErr := os.Lstat(abs)
		if statErr != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return
		}
		_ = os.RemoveAll(abs)
	}()
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	receipt := make([]rescueReceiptFile, 0, len(files))
	for _, f := range files {
		if dir := filepath.ToSlash(filepath.Dir(f.Path)); dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
		}
		mode := os.FileMode(0o644)
		if f.Mode == "100755" {
			mode = 0o755
		}
		w, err := root.OpenFile(filepath.ToSlash(f.Path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return nil, err
		}
		n, werr := w.Write(f.Bytes)
		if cerr := w.Chmod(mode); werr == nil && cerr != nil {
			werr = cerr
		}
		cerr := w.Close()
		if werr != nil || cerr != nil || n != len(f.Bytes) {
			return nil, errors.New("writing the rescue tree failed")
		}
		receipt = append(receipt, rescueReceiptFile{Path: f.Path, Mode: f.Mode, SHA256: f.SHA256, Bytes: len(f.Bytes)})
	}
	ok = true
	return receipt, nil
}

func reserveOutputDir(abs string) error {
	if abs == "" || abs == string(filepath.Separator) {
		return errors.New("--out is not a new directory")
	}
	cur := string(filepath.Separator)
	for _, part := range strings.Split(abs, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			if cur != abs {
				return errors.New("--out's parent does not exist")
			}
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return errors.New("--out must not pass through a symlink")
		}
		if cur == abs {
			return errors.New("--out already exists")
		}
		if !fi.IsDir() {
			return errors.New("--out's parent is not a directory")
		}
	}
	return errors.New("--out already exists")
}

func (a *app) printRescueReceipt(format string, files []rescueReceiptFile) error {
	if format == outputJSON {
		return a.printJSON(struct {
			Files []rescueReceiptFile `json:"files"`
		}{Files: files})
	}
	for _, f := range files {
		a.outf("%s  %s  %s  %d\n", f.SHA256, f.Mode, f.Path, f.Bytes)
	}
	if len(files) == 0 {
		_, err := io.WriteString(a.env.Stdout, "rescue tree: no files\n")
		return err
	}
	return nil
}
