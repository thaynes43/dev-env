// Package commonrules retains the nonsecret common instruction inputs for an
// explicitly enabled managed task. Provider-owned configuration is not a source.
package commonrules

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"unicode/utf8"

	kjson "sigs.k8s.io/json"
)

const (
	Namespace      = "dev-agents"
	CommonName     = "dev-env-config-claude"
	HeaderName     = "dev-env-config-codex"
	CommonKey      = "CLAUDE.md"
	HeaderKey      = "AGENTS.header.md"
	MaxCommonBytes = 64 << 10
	MaxHeaderBytes = 16 << 10
	// Sixfold JSON escaping of the accepted text plus bounded provenance.
	MaxSnapshotBytes = 6*(MaxCommonBytes+MaxHeaderBytes) + (8 << 10)
)

var ErrRefused = errors.New("accepted common instruction inputs are unconfirmed; preserved")
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var uidPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,127}$`)
var resourceVersionPattern = regexp.MustCompile(`^[0-9]{1,64}$`)

// Source stores only one reviewed instruction key. Other ConfigMap data never
// enters the accepted snapshot.
type Source struct {
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	Key             string `json:"key"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
	SHA256          string `json:"sha256"`
	Text            string `json:"text"`
}

type Snapshot struct {
	Version        int    `json:"version"`
	CommonRevision string `json:"commonRevision"`
	ProjectDigest  string `json:"projectDigest"`
	Common         Source `json:"common"`
	Header         Source `json:"header"`
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func ValidDigest(value string) bool { return digestPattern.MatchString(value) }

// New validates byte bounds before hashing or serializing the source strings.
func New(common, header Source, projectDigest string) (Snapshot, error) {
	if !validSource(common, CommonName, CommonKey, MaxCommonBytes, false) || !validSource(header, HeaderName, HeaderKey, MaxHeaderBytes, false) || !ValidDigest(projectDigest) {
		return Snapshot{}, ErrRefused
	}
	common.SHA256, header.SHA256 = Digest([]byte(common.Text)), Digest([]byte(header.Text))
	s := Snapshot{Version: 1, ProjectDigest: projectDigest, Common: common, Header: header}
	s.CommonRevision = revision(common, header)
	if _, err := s.Bytes(); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

func validSource(s Source, name, key string, max int, hash bool) bool {
	return s.Namespace == Namespace && s.Name == name && s.Key == key && uidPattern.MatchString(s.UID) && resourceVersionPattern.MatchString(s.ResourceVersion) && len(s.Text) <= max && utf8.ValidString(s.Text) && (!hash || s.SHA256 == Digest([]byte(s.Text)))
}

func revision(common, header Source) string {
	type identity struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Key       string `json:"key"`
		SHA256    string `json:"sha256"`
	}
	// Observation resourceVersions and UIDs stay in the snapshot, not in the
	// content revision a reviewed GitOps record can declare before installation.
	data, _ := json.Marshal([2]identity{{common.Namespace, common.Name, common.Key, common.SHA256}, {header.Namespace, header.Name, header.Key, header.SHA256}})
	return Digest(data)
}

func (s Snapshot) Validate() error {
	if s.Version != 1 || !ValidDigest(s.ProjectDigest) || !validSource(s.Common, CommonName, CommonKey, MaxCommonBytes, true) || !validSource(s.Header, HeaderName, HeaderKey, MaxHeaderBytes, true) || s.CommonRevision != revision(s.Common, s.Header) {
		return ErrRefused
	}
	return nil
}

func (s Snapshot) Bytes() ([]byte, error) {
	if s.Validate() != nil {
		return nil, ErrRefused
	}
	data, err := json.Marshal(s)
	if err != nil || len(data) > MaxSnapshotBytes {
		return nil, ErrRefused
	}
	return data, nil
}

func Parse(data []byte) (Snapshot, error) {
	if len(data) == 0 || len(data) > MaxSnapshotBytes || !utf8.Valid(data) {
		return Snapshot{}, ErrRefused
	}
	var s Snapshot
	strict, err := kjson.UnmarshalStrict(data, &s)
	if err != nil || len(strict) != 0 || s.Validate() != nil {
		return Snapshot{}, ErrRefused
	}
	return s, nil
}

func (s Snapshot) CodexGlobal() string { return s.Header.Text + "\n" + s.Common.Text }
