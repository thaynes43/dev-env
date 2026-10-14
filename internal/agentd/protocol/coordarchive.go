package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"unicode/utf8"
)

// DESIGN-024 section 4. This first R2 slice validates ingestion only; it has
// no filesystem, Kubernetes, retention, release or deletion authority.
const (
	CoordArchiveFormat          = "dev-env.coordination-archive.v1"
	CoordArchiveAPIVersion      = "coordination.dev-env.haynesops.com/v1alpha1"
	MaxCoordArchiveObjectBytes  = 1 << 20
	MaxCoordArchiveRequestBytes = MaxCoordArchiveObjectBytes + 64<<10
)

// CoordArchiveObjectDigest binds the full canonical object, not typed CRD data.
type CoordArchiveObjectDigest struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// CoordArchiveManifest is the identity/digest portion of the stored manifest.
// The shelf's later put command supplies archivedAt when publishing the entry.
type CoordArchiveManifest struct {
	Format          string                   `json:"format"`
	Resource        string                   `json:"resource"`
	Kind            string                   `json:"kind"`
	APIVersion      string                   `json:"apiVersion"`
	Namespace       string                   `json:"namespace"`
	Name            string                   `json:"name"`
	UID             string                   `json:"uid"`
	ResourceVersion string                   `json:"resourceVersion"`
	Project         string                   `json:"project"`
	RecipientUID    string                   `json:"recipientUID,omitempty"`
	MessageUID      string                   `json:"messageUID,omitempty"`
	Object          CoordArchiveObjectDigest `json:"object"`
	ArchivedBy      string                   `json:"archivedBy"`
}

var (
	coordArchiveLabel    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	coordArchiveRevision = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	coordArchiveDigest   = regexp.MustCompile(`^[a-f0-9]{64}$`)
	coordArchiveName     = regexp.MustCompile(`^(task-claim|published-context|message|read-ack)\.[a-f0-9]{32}\.[a-f0-9]{32}$`)
)

// VerifyCoordArchiveObject reads at most the finite put-request budget plus
// one overflow byte. It returns only a full, identity- and digest-verified
// canonical object. Unknown fields and json.Number encodings are retained.
// It is not proof of Session absence, caller authority or permission to delete.
func VerifyCoordArchiveObject(m CoordArchiveManifest, r io.Reader) ([]byte, error) {
	invalid := func(why string) ([]byte, error) {
		return nil, HistoryErrorf(HistoryErrInvalid, "", "coordination archive: %s", why)
	}
	if !validCoordArchiveManifest(m) {
		return invalid("invalid manifest identity, resource or digest")
	}
	if r == nil {
		return invalid("missing object")
	}
	raw, err := io.ReadAll(io.LimitReader(r, MaxCoordArchiveRequestBytes+1))
	if err != nil || len(raw) > MaxCoordArchiveRequestBytes || !utf8.Valid(raw) {
		return invalid("object read, UTF-8 or incoming byte bound refused")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return invalid("object is not a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return invalid("object has trailing or malformed JSON")
	}
	if !coordArchiveObjectMatches(m, object) {
		return invalid("object identity, project or message binding differs")
	}
	canonical, err := CanonicalJSON(object)
	if err != nil || len(canonical) > MaxCoordArchiveObjectBytes || int64(len(canonical)) != m.Object.Bytes {
		return invalid("canonical object size differs or exceeds its bound")
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != m.Object.SHA256 {
		return invalid("canonical object SHA256 differs")
	}
	return canonical, nil
}

func validCoordArchiveManifest(m CoordArchiveManifest) bool {
	prefix, kind := "", ""
	switch m.Resource {
	case "taskclaims":
		prefix, kind = "task-claim", "TaskClaim"
	case "publishedcontexts":
		prefix, kind = "published-context", "PublishedContext"
	case "inboxmessages":
		prefix, kind = "message", "InboxMessage"
	case "inboxreadacks":
		prefix, kind = "read-ack", "InboxReadAck"
	default:
		return false
	}
	if m.Format != CoordArchiveFormat || m.APIVersion != CoordArchiveAPIVersion || m.Kind != kind ||
		!coordArchiveLabel.MatchString(m.Namespace) || !coordArchiveLabel.MatchString(m.Project) ||
		!coordArchiveName.MatchString(m.Name) || len(m.Name) < len(prefix)+1 || m.Name[:len(prefix)+1] != prefix+"." ||
		!ValidKubeUID(m.UID) || !coordArchiveRevision.MatchString(m.ResourceVersion) ||
		!coordArchiveDigest.MatchString(m.Object.SHA256) || m.Object.Bytes < 1 || m.Object.Bytes > MaxCoordArchiveObjectBytes ||
		m.ArchivedBy == "" || len(m.ArchivedBy) > 256 || !utf8.ValidString(m.ArchivedBy) {
		return false
	}
	if m.Resource == "inboxmessages" || m.Resource == "inboxreadacks" {
		if !ValidKubeUID(m.RecipientUID) {
			return false
		}
	} else if m.RecipientUID != "" {
		return false
	}
	if m.Resource == "inboxreadacks" {
		return ValidKubeUID(m.MessageUID)
	}
	return m.MessageUID == ""
}

func coordArchiveObjectMatches(m CoordArchiveManifest, object map[string]any) bool {
	metadata, metadataOK := object["metadata"].(map[string]any)
	spec, specOK := object["spec"].(map[string]any)
	if !metadataOK || !specOK || object["apiVersion"] != m.APIVersion || object["kind"] != m.Kind ||
		metadata["namespace"] != m.Namespace || metadata["name"] != m.Name || metadata["uid"] != m.UID ||
		metadata["resourceVersion"] != m.ResourceVersion {
		return false
	}
	if m.Resource == "taskclaims" {
		identity, ok := spec["identity"].(map[string]any)
		return ok && identity["project"] == m.Project
	}
	if spec["project"] != m.Project {
		return false
	}
	if m.Resource == "inboxmessages" || m.Resource == "inboxreadacks" {
		recipient, ok := spec["recipient"].(map[string]any)
		if !ok || recipient["uid"] != m.RecipientUID {
			return false
		}
	}
	return m.Resource != "inboxreadacks" || spec["messageUID"] == m.MessageUID
}
