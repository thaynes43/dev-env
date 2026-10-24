package apiserver

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
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
// them out. A name match without a UID is refused. The handler still does
// not return file bytes. The thin bundle stays the rescue artifact.
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
	// The canonical commit is known. File bytes are not served: a shelf tar
	// is not that commit, and sending the bytes to a human or client would
	// leave the cluster. The shelf is not called and nothing is allocated.
	return 0, nil, invalid(fieldError("rescue", "rescued file bytes are not served"))
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
	} else {
		snap, err := projectcatalog.ParseSnapshot([]byte(raw))
		if err != nil || snap.Project() != q.project || snap.Selected().Name != q.repo {
			return invalid(fieldError("project", "the project does not match the session"))
		}
	}
	return rescueTreeCanonical(rec, q)
}

func rescueTreeCanonical(rec *v1alpha1.RescueStatus, q rescueTreeQueryValues) error {
	if rec == nil || rec.OmittedRefs > 0 {
		return invalid(fieldError("commit", "the rescue's canonical ref list is incomplete"))
	}
	dir := path.Dir(rec.LastBundle)
	for _, ref := range rec.UnpushedRefs {
		if ref.Commit != q.commit || !protocol.RescueWorkRef(ref.Name) {
			continue
		}
		if path.Base(ref.Repo) != q.repo || path.Clean(ref.Repo) != ref.Repo || strings.Contains(ref.Repo, "..") {
			continue
		}
		if ref.Bundle == "" || path.Clean(ref.Bundle) != ref.Bundle || path.Dir(ref.Bundle) != dir {
			continue
		}
		return nil
	}
	return invalid(fieldError("commit", "the commit is not a rescue ref in this rescue's canonical list"))
}
