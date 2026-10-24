package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// readRescueTree is GET /v1/rescues/{session}/{stamp}/tree.
//
// Listing a rescue is not permission to read its bytes. The only caller that
// reaches the binding checks is an in-cluster session reading itself, by
// session UID, or its direct child when that child's stored parent UID is
// this session. A human, a client, and a coordinator are refused: D-10 and
// D-48 keep rescue bytes inside the cluster, and this route does not carry
// them out to those callers. A name match without a UID is refused. The
// handler execs the caller's pod, which already has the clone, and returns
// the files only after that pod verifies the thin bundle against the
// operator-recorded base commit. It does not exec the shelf.
func (s *Server) readRescueTree(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := rescueByteCaller(c); err != nil {
		return 0, nil, err
	}
	id := r.PathValue("session") + "/" + r.PathValue("stamp")
	session, _, err := protocol.ParseRescueID(id)
	if err != nil {
		return 0, nil, invalid(fieldError("rescue", "%v", err))
	}
	q, err := rescueTreeQuery(r.URL.Query())
	if err != nil {
		return 0, nil, err
	}
	sess, err := s.rescueSession(ctx, session)
	if err != nil {
		return 0, nil, err
	}
	if !rescueByteOwner(c, sess) {
		return 0, nil, forbidden("this caller cannot read that session's rescued bytes")
	}
	ref, err := rescueTreeBound(sess, id, q)
	if err != nil {
		return 0, nil, err
	}
	if c.pod == nil || c.pod.Name == "" || c.pod.Namespace == "" || s.Exec == nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the rescue tree cannot be read now")
	}
	// Two slots per replica. A near-cap read was measured at about 28 MiB of
	// live heap (113 MiB for four concurrent reads) against an operator that
	// already uses about 67 MiB of its 256 MiB limit. Two concurrent reads stay
	// near 123 MiB. A busy replica answers 429, which agent-run does not retry.
	release, ok := s.rescueTreeSlot()
	if !ok {
		return 0, nil, newError(http.StatusTooManyRequests, apiv1.CodeLimitExceeded, "this replica is already reading a rescue tree")
	}
	defer release()
	// The recipient is the caller's pod, which already has the clone. The
	// shelf has no base, and this does not write a tree onto the shared volume.
	ctx, cancel := context.WithTimeout(ctx, podCmdTimeout)
	defer cancel()
	stdout, stderr := &limitedBuffer{max: 8 << 20}, &limitedBuffer{max: 4 << 10}
	cmd := []string{"agentd", "ctl", "rescue-tree", "--repo", q.repo, "--commit", q.commit, "--base", ref.BaseCommit, "--clone", ref.Repo, "--bundle", ref.Bundle, "--ref", ref.Name, id}
	if err := s.Exec.Run(ctx, c.pod.Namespace, c.pod.Name, controller.ContainerName, cmd, nil, stdout, stderr); err != nil {
		s.Log.Info("rescue tree read failed", "rescue", id, "repo", q.repo)
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the rescue tree cannot be read now")
	}
	if stdout.over {
		return 0, nil, invalid(fieldError("rescue", "the rescue tree is %s", protocol.TreeTooLarge))
	}
	var tree protocol.RescueTree
	if err := json.Unmarshal(stdout.Bytes(), &tree); err != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the rescue tree cannot be read now")
	}
	view, err := rescueTreeView(id, q, ref.Name, tree)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, view, nil
}

type rescueTreeQueryValues struct {
	repo, commit, sessionUID, podUID, project string
}

func rescueTreeQuery(q url.Values) (rescueTreeQueryValues, error) {
	var out rescueTreeQueryValues
	for key, values := range q {
		if len(values) != 1 {
			return out, badRequest("query parameter %s is given more than once", key)
		}
		switch key {
		case "repo":
			out.repo = values[0]
		case "commit":
			out.commit = values[0]
		case "sessionUID":
			out.sessionUID = values[0]
		case "podUID":
			out.podUID = values[0]
		case "project":
			out.project = values[0]
		default:
			return out, badRequest("unknown query parameter %q", key)
		}
	}
	switch {
	case !projectcatalog.ValidName(out.repo):
		return out, invalid(fieldError("repo", "a rescue tree names one repository"))
	case !protocol.ValidGitObject(out.commit):
		return out, invalid(fieldError("commit", "a rescue tree names the rescue-branch commit"))
	case !protocol.ValidKubeUID(out.sessionUID):
		return out, invalid(fieldError("sessionUID", "a rescue tree names the session UID"))
	case !protocol.ValidKubeUID(out.podUID):
		return out, invalid(fieldError("podUID", "a rescue tree names the pod UID recorded on the rescue"))
	case out.project != "" && !projectcatalog.ValidName(out.project):
		return out, invalid(fieldError("project", "a rescue tree names the session's project"))
	}
	return out, nil
}

func rescueByteCaller(c *caller) error {
	if c == nil {
		return forbidden("this caller cannot read rescued bytes")
	}
	switch c.kind {
	case kindHuman, kindClient, kindCoordinator:
		return forbidden("this caller cannot read rescued bytes")
	case kindSession:
		if c.session == nil || c.session.UID == "" || c.session.Spec.ProjectCanary != nil || projectcanary.IsPilot(c.session) || c.depth > maxSessionDepth {
			return forbidden("this caller cannot read rescued bytes")
		}
		return nil
	default:
		return forbidden("this caller cannot read rescued bytes")
	}
}

func rescueByteOwner(c *caller, sess *v1alpha1.AgentSession) bool {
	if c == nil || c.kind != kindSession || c.session == nil || c.session.UID == "" || sess == nil || sess.UID == "" {
		return false
	}
	if sess.Name == c.session.Name && sess.UID == c.session.UID {
		return true
	}
	// A parent name recurs. The child is this session's only when the
	// platform stored this session's UID as the child's parent UID.
	if sess.Spec.Parent == "" || sess.Spec.Parent != c.session.Name || sess.Spec.Parent != c.parent {
		return false
	}
	return sess.Spec.UsageCaptureParentUID != "" && sess.Spec.UsageCaptureParentUID == string(c.session.UID)
}

func (s *Server) rescueSession(ctx context.Context, name string) (*v1alpha1.AgentSession, error) {
	if s.Live == nil {
		return nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the rescue tree cannot be read now")
	}
	var sess v1alpha1.AgentSession
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: name}, &sess); err != nil {
		return nil, fromKubeError(err, "session "+name)
	}
	if sess.UID == "" {
		return nil, notFound("no session %q", name)
	}
	return &sess, nil
}

func rescueTreeBound(sess *v1alpha1.AgentSession, id string, q rescueTreeQueryValues) (v1alpha1.RescuedRef, error) {
	var none v1alpha1.RescuedRef
	_, stamp, err := protocol.ParseRescueID(id)
	if err != nil {
		return none, invalid(fieldError("rescue", "%v", err))
	}
	rec := sess.Status.Rescue
	switch {
	case string(sess.UID) != q.sessionUID:
		return none, invalid(fieldError("sessionUID", "the session UID does not match"))
	case sess.Spec.Repo != q.repo:
		return none, invalid(fieldError("repo", "the repository does not match the session"))
	case rec == nil || rec.Stamp != stamp || rec.Result != v1alpha1.RescueVerified || rec.Superseded || rec.PodUID == "" || rec.PodUID != q.podUID:
		return none, invalid(fieldError("rescue", "the rescue is missing, incomplete, or superseded"))
	case rec.LastBundle != "rescue/"+sess.Name+"/"+stamp+"/manifest.json":
		return none, invalid(fieldError("rescue", "the rescue manifest does not match this stamp"))
	}
	raw := sess.Annotations[v1alpha1.AnnotationProjectSnapshot]
	if raw == "" {
		if q.project != "" {
			return none, invalid(fieldError("project", "this session has no project"))
		}
	} else {
		snap, err := projectcatalog.ParseSnapshot([]byte(raw))
		if err != nil || snap.Project() != q.project || snap.Selected().Name != q.repo {
			return none, invalid(fieldError("project", "the project does not match the session"))
		}
	}
	return rescueTreeCanonical(rec, q)
}

func rescueTreeCanonical(rec *v1alpha1.RescueStatus, q rescueTreeQueryValues) (v1alpha1.RescuedRef, error) {
	var none v1alpha1.RescuedRef
	if rec == nil || rec.OmittedRefs > 0 {
		return none, invalid(fieldError("commit", "the rescue's canonical ref list is incomplete"))
	}
	dir := path.Dir(rec.LastBundle)
	for _, ref := range rec.UnpushedRefs {
		if ref.Commit != q.commit || !protocol.RescueWorkRef(ref.Name) {
			continue
		}
		if path.Base(ref.Repo) != q.repo || !filepath.IsAbs(ref.Repo) || filepath.Clean(ref.Repo) != ref.Repo || strings.Contains(ref.Repo, "..") {
			continue
		}
		if ref.Bundle == "" || path.Clean(ref.Bundle) != ref.Bundle || path.Dir(ref.Bundle) != dir || strings.Contains(ref.Bundle, "..") {
			continue
		}
		if !protocol.ValidGitObject(ref.BaseCommit) {
			return none, invalid(fieldError("commit", "the rescue has no recorded base commit"))
		}
		return ref, nil
	}
	return none, invalid(fieldError("commit", "the commit is not a rescue ref in this rescue's canonical list"))
}

func (s *Server) rescueTreeSlot() (func(), bool) {
	if s.RescueTreeSlots == nil {
		return func() {}, true
	}
	return s.RescueTreeSlots.TryTake()
}

func rescueTreeView(id string, q rescueTreeQueryValues, ref string, tree protocol.RescueTree) (apiv1.RescueTree, error) {
	var none apiv1.RescueTree
	switch {
	case !tree.OK && tree.Code == protocol.RescueTreeNotFound:
		return none, notFound("no rescue tree %s", id)
	case !tree.OK && (tree.Code == protocol.RescueTreeMissingBase || tree.Code == protocol.RescueTreeIncomplete || tree.Code == protocol.TreeTooLarge || tree.Code == protocol.TreeNotRegular || tree.Code == protocol.RescueTreeMismatch || tree.Code == protocol.RescueTreeRefused || tree.Code == protocol.TreeCaptureFailed || tree.Code == protocol.TreeWithheld):
		return none, invalid(fieldError("rescue", "the rescue tree is %s", tree.Code))
	case !tree.OK || tree.ID != id || tree.Repo != q.repo || tree.Commit != q.commit || tree.Ref != ref || !protocol.RescueWorkRef(tree.Ref) || !protocol.ValidGitObject(tree.Prerequisite):
		return none, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the rescue tree cannot be read now")
	}
	var raw int64
	files := make([]apiv1.RescueTreeFile, 0, len(tree.Files))
	seen := map[string]bool{}
	for _, f := range tree.Files {
		sum := sha256.Sum256(f.Bytes)
		got := hex.EncodeToString(sum[:])
		raw += int64(len(f.Bytes))
		if !protocol.AcceptableRescueTreePath(f.Path) || !protocol.RegularTreeMode(f.Mode) || !protocol.ValidSHA256(f.SHA256) || got != f.SHA256 || seen[f.Path] || raw < 0 {
			return none, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the rescue tree cannot be read now")
		}
		seen[f.Path] = true
		files = append(files, apiv1.RescueTreeFile{Path: f.Path, Mode: f.Mode, SHA256: f.SHA256, Bytes: f.Bytes})
	}
	delta := make([]apiv1.RescueTreeDelta, 0, len(tree.Delta))
	for _, d := range tree.Delta {
		if !protocol.AcceptableRescueTreePath(d.Path) || (d.Change != "added" && d.Change != "modified" && d.Change != "deleted") {
			return none, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the rescue tree cannot be read now")
		}
		if d.Change != "deleted" && (!protocol.RegularTreeMode(d.Mode) || !protocol.ValidGitObject(d.OID)) {
			return none, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the rescue tree cannot be read now")
		}
		if d.Carried && !seen[d.Path] {
			return none, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the rescue tree cannot be read now")
		}
		delta = append(delta, apiv1.RescueTreeDelta{Path: d.Path, Change: d.Change, Mode: d.Mode, OID: d.OID, Carried: d.Carried})
	}
	if raw > 8<<20 {
		return none, invalid(fieldError("rescue", "the rescue tree is %s", protocol.TreeTooLarge))
	}
	return apiv1.RescueTree{
		ID: id, SessionUID: q.sessionUID, PodUID: q.podUID, Repo: q.repo, Project: q.project,
		Commit: q.commit, Ref: tree.Ref, Prerequisite: tree.Prerequisite, Delta: delta, Files: files,
	}, nil
}
