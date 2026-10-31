package broker

// Delegate decisions (DESIGN-042 6.2, mode (b)). An allowlisted delegate
// identity decides another agent's grant through the operator's authenticated
// route, POST /v1/grants/{name}/decision. The operator writes the set-once
// decision annotation (MaintenanceDecisionAnnotation, now on kube and egress
// grants too); the broker checks its binding, finds a GrantPolicy whose
// spec.delegated covers the scope and the requester, and calls Decide with
// "delegate/<namespace>/<serviceaccount>". It never writes a human approver. A
// delegate decision is not a human decision.
//
// Default off: with an empty --grant-delegates the operator serves no decision
// route and the broker reads no delegate decision, so behaviour is unchanged.

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// DelegateApprover prefixes status.approvedBy or status.deniedBy for a
	// delegate decision: "delegate/<namespace>/<serviceaccount>".
	DelegateApprover = "delegate/"
	// MaxGrantDelegates bounds --grant-delegates.
	MaxGrantDelegates = 16
	// DelegateWording is the phrase every message of a delegate decision
	// carries.
	DelegateWording = "not a human decision"
)

// ParseGrantDelegates parses --grant-delegates: comma-separated ServiceAccounts,
// "<namespace>/<name>", at most MaxGrantDelegates, none repeated and none of
// refused. The operator and the broker take the same list (as they take the
// same human ServiceAccount); the result is sorted, so the same entries in any
// order parse alike. Empty is nil: no delegates.
func ParseGrantDelegates(raw string, refused ...string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []string
	for _, ref := range strings.Split(raw, ",") {
		ref = strings.TrimSpace(ref)
		ns, name, ok := strings.Cut(ref, "/")
		switch {
		case !ok || len(validation.IsDNS1123Label(ns)) != 0 || len(validation.IsDNS1123Subdomain(name)) != 0:
			return nil, fmt.Errorf("--grant-delegates: %q is not <namespace>/<serviceaccount>", ref)
		case slices.Contains(refused, ref):
			return nil, fmt.Errorf("--grant-delegates: %s may not be a delegate: it is the human, a client, the shared session or the workbench identity", ref)
		case slices.Contains(out, ref):
			return nil, fmt.Errorf("--grant-delegates: %s is named twice", ref)
		}
		out = append(out, ref)
	}
	if len(out) > MaxGrantDelegates {
		return nil, errors.New("--grant-delegates: more than 16 delegates")
	}
	slices.Sort(out)
	return out, nil
}

// DelegateRef is the approver string of a delegate ServiceAccount.
func DelegateRef(serviceAccount string) string { return DelegateApprover + serviceAccount }

// delegateListed reports whether by is "delegate/<ref>" for a ref on the list.
func delegateListed(by string, delegates []string) bool {
	ref, ok := strings.CutPrefix(by, DelegateApprover)
	return ok && ref != "" && slices.Contains(delegates, ref)
}
