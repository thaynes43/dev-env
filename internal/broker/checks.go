package broker

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The TTL bounds of D-54. The shortest is the shortest token TokenRequest
// issues.
const (
	MinTTL           = 10 * time.Minute
	MaxTTL           = 8 * time.Hour
	MaxCredentialTTL = 4 * time.Hour
	MaxBreakglassTTL = time.Hour
)

// DevEnvNamespaces are the three namespaces no grant reaches (DESIGN-001 6.12).
var DevEnvNamespaces = []string{"dev-env-system", "dev-agents", "dev-tools"}

// catalog is the grant role catalog: the only roles the broker binds.
var catalog = []string{
	v1alpha1.RoleWorkloads, v1alpha1.RoleStorage, v1alpha1.RoleSecretsRead,
	v1alpha1.RoleNodes, v1alpha1.RoleBreakglass,
}

// errNotBuilt marks a grant type this broker cannot make yet.
var errNotBuilt = errors.New("not built yet")

// check repeats the schema's rules in Go before the broker decides or makes
// anything (DESIGN-001 6.12): the broker does not trust that the CRD it runs
// against is the one this code was built with. reserved adds namespaces to the
// three dev-env ones (the broker's own, when they are named otherwise). It
// returns why a grant fails, or nil.
func check(g *v1alpha1.AccessGrant, reserved ...string) error {
	if len(g.Name) > validation.DNS1123LabelMaxLength || !strings.HasPrefix(g.Name, "grant-") || len(validation.IsDNS1123Label(g.Name)) > 0 {
		return fmt.Errorf("the name %q is not grant-<id>, a DNS label (D-54)", g.Name)
	}
	s := &g.Spec
	if s.Requester.Session == "" {
		return errors.New("the grant names no requesting session")
	}
	if len(validation.IsDNS1123Label(s.Requester.Session)) > 0 {
		return errors.New("the requesting session is not a session name")
	}
	kube := s.Type == v1alpha1.GrantKube || s.Type == v1alpha1.GrantBreakglass
	switch {
	case s.Type != v1alpha1.GrantKube && s.Type != v1alpha1.GrantBreakglass &&
		s.Type != v1alpha1.GrantEgress && s.Type != v1alpha1.GrantCredential:
		return fmt.Errorf("unknown grant type %q", s.Type)
	case kube != (s.Kube != nil):
		return errors.New("kube is set for types kube and breakglass, and only there")
	case (s.Type == v1alpha1.GrantEgress) != (s.Egress != nil):
		return errors.New("egress is set for type egress, and only there")
	case (s.Type == v1alpha1.GrantCredential) != (s.Credential != nil):
		return errors.New("credential is set for type credential, and only there")
	}

	ttl := s.TTL.Duration
	longest := MaxTTL
	switch s.Type {
	case v1alpha1.GrantCredential:
		longest = MaxCredentialTTL
	case v1alpha1.GrantBreakglass:
		longest = MaxBreakglassTTL
	}
	if ttl < MinTTL || ttl > longest {
		return fmt.Errorf("ttl %s is outside %s to %s for a %s grant", ttl, MinTTL, longest, s.Type)
	}

	forbidden := append(slices.Clone(DevEnvNamespaces), reserved...)
	if k := s.Kube; k != nil {
		if !slices.Contains(catalog, k.Role) {
			return fmt.Errorf("role %q is not in the grant role catalog", k.Role)
		}
		if (s.Type == v1alpha1.GrantBreakglass) != (k.Role == v1alpha1.RoleBreakglass) {
			return errors.New("type breakglass and role dev-env-grant-breakglass go together (D-27)")
		}
		if v1alpha1.ClusterWideRole(k.Role) != (len(k.Namespaces) == 0) {
			return fmt.Errorf("role %s is cluster-wide and takes no namespaces, or is namespaced and names at least one", k.Role)
		}
		for _, ns := range k.Namespaces {
			if len(validation.IsDNS1123Label(ns)) > 0 {
				return fmt.Errorf("namespace %q is not a namespace name", ns)
			}
			if slices.Contains(forbidden, ns) {
				return fmt.Errorf("no grant reaches namespace %s (DESIGN-001 6.12)", ns)
			}
		}
	}
	if e := s.Egress; e != nil {
		if err := checkEgress(e, forbidden); err != nil {
			return err
		}
	}
	// Credential grants (step 8) are refused until they are built,
	// rather than approved with nothing made.
	if s.Type == v1alpha1.GrantCredential {
		return fmt.Errorf("credential grants are %w", errNotBuilt)
	}
	return nil
}
