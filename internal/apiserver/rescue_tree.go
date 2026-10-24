package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"

	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/shelf"
)

// readRescueTree is GET /v1/rescues/{session}/{stamp}/tree.
//
// Listing a rescue is not permission to read its bytes. The caller must be
// one who can restore (human, client, or a session that can still create),
// and the session's spec.parent must be that caller, or the caller must be
// that session. A coordinator cannot restore, so a coordinator cannot read
// bytes. A canary cannot create, so it cannot read bytes. Restore itself
// stays fleet-wide for those caller classes; byte egress is limited to the
// existing parent identity because the bytes leave the cluster.
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
	if err := rescueTreeBound(sess, id, q); err != nil {
		return 0, nil, err
	}
	if s.Shelf == nil {
		return 0, nil, noShelf()
	}
	tree, err := s.Shelf.Tree(ctx, id, q.repo, q.commit)
	if err != nil {
		if errors.Is(err, shelf.ErrNoShelf) {
			return 0, nil, shelfError(err)
		}
		s.Log.Info("rescue tree read failed", "rescue", id, "repo", q.repo)
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the rescue tree cannot be read now")
	}
	view, err := rescueTreeView(id, q, tree)
	if err != nil {
		s.Log.Info("rescue tree answer refused", "rescue", id, "repo", q.repo, "code", tree.Code)
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
	case kindHuman, kindClient:
		return nil
	case kindCoordinator:
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
	if c == nil || sess == nil || sess.UID == "" || c.parent == "" || sess.Spec.Parent == "" {
		return false
	}
	switch c.kind {
	case kindHuman, kindClient:
		return sess.Spec.Parent == c.parent
	case kindSession:
		if c.session == nil {
			return false
		}
		return sess.Name == c.session.Name || sess.Spec.Parent == c.parent
	default:
		return false
	}
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

func rescueTreeBound(sess *v1alpha1.AgentSession, id string, q rescueTreeQueryValues) error {
	_, stamp, err := protocol.ParseRescueID(id)
	if err != nil {
		return invalid(fieldError("rescue", "%v", err))
	}
	rec := sess.Status.Rescue
	switch {
	case string(sess.UID) != q.sessionUID:
		return invalid(fieldError("sessionUID", "the session UID does not match"))
	case sess.Spec.Repo != q.repo:
		return invalid(fieldError("repo", "the repository does not match the session"))
	case rec == nil || rec.Stamp != stamp || rec.Result != v1alpha1.RescueVerified || rec.Superseded || rec.PodUID == "" || rec.PodUID != q.podUID:
		return invalid(fieldError("rescue", "the rescue is missing, incomplete, or superseded"))
	case rec.LastBundle != "rescue/"+sess.Name+"/"+stamp+"/manifest.json":
		return invalid(fieldError("rescue", "the rescue manifest does not match this stamp"))
	}
	raw := sess.Annotations[v1alpha1.AnnotationProjectSnapshot]
	if raw == "" {
		if q.project != "" {
			return invalid(fieldError("project", "this session has no project"))
		}
		return nil
	}
	snap, err := projectcatalog.ParseSnapshot([]byte(raw))
	if err != nil || snap.Project() != q.project || snap.Selected().Name != q.repo {
		return invalid(fieldError("project", "the project does not match the session"))
	}
	return nil
}

func rescueTreeView(id string, q rescueTreeQueryValues, tree protocol.RescueTree) (apiv1.RescueTree, error) {
	var none apiv1.RescueTree
	switch {
	case !tree.OK && tree.Code == protocol.RescueTreeNotFound:
		return none, notFound("no rescue tree %s", id)
	case !tree.OK && (tree.Code == protocol.RescueTreeIncomplete || tree.Code == protocol.RescueTreeMismatch || tree.Code == protocol.RescueTreeRefused):
		return none, invalid(fieldError("rescue", "the rescue tree is %s", tree.Code))
	case !tree.OK || tree.ID != id || tree.Repo != q.repo || tree.Commit != q.commit || !protocol.RescueWorkRef(tree.Ref):
		return none, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the rescue tree cannot be read now")
	}
	files := make([]apiv1.RescueTreeFile, 0, len(tree.Files))
	seen := map[string]bool{}
	for _, f := range tree.Files {
		sum := sha256.Sum256(f.Bytes)
		got := hex.EncodeToString(sum[:])
		if !protocol.AcceptableRescueTreePath(f.Path) || !protocol.RegularTreeMode(f.Mode) || !protocol.ValidSHA256(f.SHA256) || got != f.SHA256 || seen[f.Path] {
			return none, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the rescue tree cannot be read now")
		}
		seen[f.Path] = true
		files = append(files, apiv1.RescueTreeFile{Path: f.Path, Mode: f.Mode, SHA256: f.SHA256, Bytes: append([]byte(nil), f.Bytes...)})
	}
	return apiv1.RescueTree{
		ID: id, SessionUID: q.sessionUID, PodUID: q.podUID, Repo: q.repo, Project: q.project,
		Commit: q.commit, Ref: tree.Ref, Files: files,
	}, nil
}
