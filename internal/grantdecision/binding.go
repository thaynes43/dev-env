// Package grantdecision provides the immutable request binding used when an
// authorized decision is carried to the access broker. It grants no authority.
package grantdecision

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// Binding identifies one request and the exact session pod for which it was
// presented. The digest covers the entire spec, including the requester, scope,
// requested TTL, reason and release flag. It contains no credential values.
type Binding struct {
	GrantUID   types.UID `json:"grantUID"`
	SessionUID types.UID `json:"sessionUID"`
	PodUID     types.UID `json:"podUID"`
	Generation int64     `json:"generation"`
	ScopeHash  string    `json:"scopeHash"`
}

// Bind uses deterministic JSON encoding for this build. A schema or encoding
// change in a later build may change the digest and must fail closed. The
// pod UID is supplied from an independently authenticated, live pod lookup.
func Bind(g *v1alpha1.AccessGrant, podUID types.UID) (Binding, error) {
	if g == nil || g.UID == "" || g.Spec.Requester.SessionUID == "" || podUID == "" {
		return Binding{}, errors.New("grant, session and pod identities are required")
	}
	hash, err := ScopeHash(g)
	if err != nil {
		return Binding{}, err
	}
	return Binding{GrantUID: g.UID, SessionUID: g.Spec.Requester.SessionUID, PodUID: podUID, Generation: g.Generation, ScopeHash: hash}, nil
}

// Matches is a binding check, not an authorization check. The caller must
// independently validate the decision principal and live session and pod.
func (b Binding) Matches(g *v1alpha1.AccessGrant, podUID types.UID) bool {
	actual, err := Bind(g, podUID)
	return err == nil && b == actual
}

// ScopeHash binds the complete request, but does not authenticate it.
func ScopeHash(g *v1alpha1.AccessGrant) (string, error) {
	if g == nil {
		return "", errors.New("grant is required")
	}
	b, err := json.Marshal(g.Spec)
	if err != nil {
		return "", errors.New("could not encode grant request")
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
