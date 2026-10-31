package broker

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// LegacyDelegateBinding authorizes only one existing baseline-account actor.
// It is configuration, never inferred from a role, profile or coordinator claim.
type LegacyDelegateBinding struct {
	ServiceAccount    string
	ServiceAccountUID string
	Session           string
	SessionUID        string
	PodName           string
	PodUID            string
}

var legacyUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ParseLegacyGrantDelegates parses at most four exact baseline actor bindings:
// ns/sa:saUID:session:sessionUID:pod:podUID. Every binding must also be designated
// in both delegate lists by the startup caller. Empty enables no legacy actor.
func ParseLegacyGrantDelegates(raw, namespace, account string) ([]LegacyDelegateBinding, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 4 {
		return nil, fmt.Errorf("--grant-legacy-delegates: at most four exact actors")
	}
	var out []LegacyDelegateBinding
	for _, part := range parts {
		fields := strings.Split(strings.TrimSpace(part), ":")
		if len(fields) != 6 || fields[0] != namespace+"/"+account || namespace == "" || account == "" {
			return nil, fmt.Errorf("--grant-legacy-delegates: expected baseline ns/sa:saUID:session:sessionUID:pod:podUID")
		}
		if !legacyUID.MatchString(fields[1]) || !legacyUID.MatchString(fields[3]) || !legacyUID.MatchString(fields[5]) || len(validation.IsDNS1123Subdomain(fields[2])) != 0 || len(validation.IsDNS1123Subdomain(fields[4])) != 0 || fields[2] != fields[4] {
			return nil, fmt.Errorf("--grant-legacy-delegates: exact UUIDs and same-name session/pod required")
		}
		b := LegacyDelegateBinding{fields[0], fields[1], fields[2], fields[3], fields[4], fields[5]}
		for _, o := range out {
			if b.Session == o.Session || b.PodUID == o.PodUID || b.SessionUID == o.SessionUID {
				return nil, fmt.Errorf("--grant-legacy-delegates: duplicate or ambiguous actor")
			}
		}
		out = append(out, b)
	}
	return out, nil
}

// ValidateLegacyDelegateLists keeps the shared account unusable unless both
// lists explicitly designate its exact configured legacy actors.
func ValidateLegacyDelegateLists(bindings []LegacyDelegateBinding, delegates, policyDelegates []string) error {
	for _, b := range bindings {
		if !slices.Contains(delegates, b.ServiceAccount) || !slices.Contains(policyDelegates, b.ServiceAccount) {
			return fmt.Errorf("--grant-legacy-delegates: %s must appear in both delegate lists", b.ServiceAccount)
		}
	}
	return nil
}

// CheckLegacyDelegate checks the exact configured actor and live SA UID. The
// existing caller/broker checks still verify token Pod UID, controller and state.
func CheckLegacyDelegate(ctx context.Context, r client.Reader, bindings []LegacyDelegateBinding, by string, d *DelegateIdentity, sess *v1alpha1.AgentSession, pod *corev1.Pod) error {
	if d == nil || sess == nil || pod == nil || d.Form != 3 || d.ServiceAccountUID == "" {
		return fmt.Errorf("%w: missing legacy actor binding", ErrRefused)
	}
	ref := strings.TrimPrefix(by, DelegateApprover)
	found := false
	for _, b := range bindings {
		if b.ServiceAccount == ref && b.ServiceAccountUID == d.ServiceAccountUID && b.Session == sess.Name && b.SessionUID == string(sess.UID) && b.PodName == pod.Name && b.PodUID == string(pod.UID) && d.Session == sess.Name && d.SessionUID == string(sess.UID) && d.PodName == pod.Name && d.PodUID == string(pod.UID) {
			found = true
		}
	}
	if !found || pod.Namespace+"/"+pod.Spec.ServiceAccountName != ref || sess.Spec.Agent != v1alpha1.AgentClaude || sess.Labels[v1alpha1.LabelDepth] != "1" || sess.Labels[templates.CoordinatorIdentityLabel] != "" || (pod.Labels[v1alpha1.LabelProfile] != "full" && !(pod.Labels[v1alpha1.LabelProfile] == "" && sess.Spec.Profile == "full")) {
		return fmt.Errorf("%w: legacy actor is not the configured full-profile depth1 Claude session", ErrRefused)
	}
	var sa corev1.ServiceAccount
	if err := reachRead(ctx, r, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Spec.ServiceAccountName}, &sa); err != nil {
		return err
	}
	if string(sa.UID) != d.ServiceAccountUID {
		return fmt.Errorf("%w: legacy actor ServiceAccount replaced", ErrRefused)
	}
	return nil
}
