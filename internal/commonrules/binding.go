package commonrules

import (
	"bytes"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func (s Snapshot) Binding() (*v1alpha1.CommonRuleBinding, error) {
	raw, err := s.Bytes()
	if err != nil {
		return nil, err
	}
	return &v1alpha1.CommonRuleBinding{Version: 1, CommonRevision: s.CommonRevision, ProjectDigest: s.ProjectDigest, SnapshotDigest: Digest(raw), Snapshot: string(raw)}, nil
}

// BoundSession validates retained authority without reading current sources.
// Terminal retries and resumes must not capture replacement source bytes.
func BoundSession(s *v1alpha1.AgentSession) (Snapshot, error) {
	if s == nil || s.Spec.CommonRules == nil {
		return Snapshot{}, ErrRefused
	}
	b := s.Spec.CommonRules
	if b.Version != 1 || len(b.Snapshot) > MaxSnapshotBytes || !ValidDigest(b.SnapshotDigest) || b.ProjectDigest != Digest([]byte(s.Annotations[v1alpha1.AnnotationProjectSnapshot])) {
		return Snapshot{}, ErrRefused
	}
	raw := []byte(b.Snapshot)
	snapshot, err := Parse(raw)
	if err != nil || b.CommonRevision != snapshot.CommonRevision || b.ProjectDigest != snapshot.ProjectDigest || b.SnapshotDigest != Digest(raw) {
		return Snapshot{}, ErrRefused
	}
	canonical, err := snapshot.Bytes()
	if err != nil || !bytes.Equal(raw, canonical) {
		return Snapshot{}, ErrRefused
	}
	return snapshot, nil
}
