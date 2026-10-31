package agentrun

import (
	"context"
	"encoding/base64"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

var reservedClientUID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var reservedClientName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
var reservedClientNamespace = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func parseStorageTarget(raw string) (apiv1.GrantStorageTarget, error) {
	kind, object, ok := strings.Cut(raw, "/")
	name, uid, hasUID := strings.Cut(object, "@")
	if !ok || !hasUID {
		return apiv1.GrantStorageTarget{}, usageError("--storage-target is Kind/name@UID")
	}
	target := apiv1.GrantStorageTarget{Kind: kind, Name: name, UID: uid}
	if err := validateStorageTargets([]apiv1.GrantStorageTarget{target}); err != nil {
		return apiv1.GrantStorageTarget{}, err
	}
	return target, nil
}

func validateStorageTargets(targets []apiv1.GrantStorageTarget) error {
	if len(targets) > 8 {
		return usageError("reserved storage names at most 8 exact kind/name/UID targets")
	}
	names, uids := map[string]bool{}, map[string]bool{}
	for _, target := range targets {
		if target.Kind != "PersistentVolumeClaim" && target.Kind != "VolumeSnapshot" && target.Kind != "StatefulSet" {
			return usageError("storage kind is PersistentVolumeClaim, VolumeSnapshot or StatefulSet")
		}
		key := target.Kind + "/" + target.Name
		if len(target.Name) > 253 || !reservedClientName.MatchString(target.Name) || !reservedClientUID.MatchString(target.UID) || names[key] || uids[target.UID] {
			return usageError("storage targets require distinct exact kind/name/UIDs; replacements need a fresh decision")
		}
		names[key] = true
		uids[target.UID] = true
	}
	return nil
}

func validateReservedRequest(req apiv1.CreateGrantRequest) error {
	if !req.Reserved && !req.AllNamespaces && len(req.StorageTargets) == 0 {
		return nil
	}
	if req.Type != "kube" && req.Type != "breakglass" {
		return usageError("reserved scope is kube or breakglass; hardware intent uses its separate audit operation")
	}
	if req.TTL != "" {
		ttl, err := time.ParseDuration(req.TTL)
		if err != nil || ttl < 10*time.Minute || ttl > time.Hour {
			return usageError("reserved TTL is 10m to 1h; this maximum does not change the 30m maintenance default")
		}
	}
	if len(req.Secrets) > 0 {
		return usageError("reserved Secret reads do not take named-Secret use scope")
	}
	switch {
	case req.Type == "breakglass":
		if (req.Role != "" && req.Role != apiv1.BreakglassRole) || len(req.Namespaces) > 0 || req.AllNamespaces || len(req.StorageTargets) > 0 {
			return usageError("RD2 is the cluster-wide breakglass catalog role with no namespace or target list")
		}
	case req.Role == "dev-env-grant-secrets-read":
		if len(req.StorageTargets) > 0 || (req.AllNamespaces && len(req.Namespaces) > 0) || (!req.AllNamespaces && (len(req.Namespaces) < 1 || len(req.Namespaces) > 16)) {
			return usageError("RD3 reads 1–16 exact namespaces or an approval-time allNamespaces UID snapshot, never both")
		}
	case req.Role == "dev-env-grant-storage":
		if req.AllNamespaces || len(req.Namespaces) != 1 || len(req.StorageTargets) < 1 {
			return usageError("RD4 storage requires one namespace and 1–8 exact kind/name/UID targets")
		}
		if err := validateStorageTargets(req.StorageTargets); err != nil {
			return err
		}
	default:
		return usageError("reserved scope is breakglass, secrets-read or exact-target storage")
	}
	seen := map[string]bool{}
	for _, ns := range req.Namespaces {
		if len(ns) > 63 || !reservedClientNamespace.MatchString(ns) || seen[ns] {
			return usageError("reserved namespace names must be exact, valid and unique")
		}
		seen[ns] = true
	}
	return nil
}

func validateReservedDecisionExtras(req apiv1.GrantDecisionRequest) error {
	if !req.Reserved {
		if req.TomRef != "" {
			return usageError("--tom-ref requires --reserved")
		}
		return nil
	}
	digest, err := base64.RawURLEncoding.DecodeString(req.Digest)
	if !validGrantDecisionRef(req.TomRef) || !reservedClientUID.MatchString(req.GrantUID) || err != nil || len(digest) != 32 {
		return usageError("reserved decisions require the exact grant UID/digest and --tom-ref <nonsecret decision reference, for example Tom's message or the configured delegated-authority reference>; agent assertion, not verified human consent")
	}
	if req.TTL != "" {
		ttl, err := time.ParseDuration(req.TTL)
		if err != nil || ttl < 10*time.Minute || ttl > time.Hour {
			return usageError("reserved decision TTL is 10m to 1h")
		}
	}
	return nil
}

func validateReservedCurrent(req apiv1.GrantDecisionRequest, g apiv1.Grant) error {
	if !req.Reserved {
		return nil
	}
	return validateReservedRequest(apiv1.CreateGrantRequest{Reserved: true, Type: g.Type, Role: g.Role, Namespaces: g.Namespaces, AllNamespaces: g.AllNamespaces, StorageTargets: g.StorageTargets, Secrets: g.Secrets, TTL: g.TTL})
}

// S7 uses the existing authenticated API, once. A lost or mismatched answer is
// uncertain and never authorizes a replacement decision or automatic retry.
func (a *app) postReservedDecision(ctx context.Context, c *conn, name string, req apiv1.GrantDecisionRequest, expected ...*apiv1.Grant) (apiv1.Grant, error) {
	if len(expected) != 1 || expected[0] == nil || expected[0].UID != req.GrantUID || expected[0].ScopeDigest != req.Digest {
		return apiv1.Grant{}, fail(ExitFailed, "reserved decision needs the exact reviewed grant UID and scope")
	}
	if err := validateReservedCurrent(req, *expected[0]); err != nil {
		return apiv1.Grant{}, err
	}
	expectedClass := reservedClientClass(*expected[0])
	var out apiv1.Grant
	status, err := a.callOnce(ctx, c, http.MethodPost, apiv1.GrantDecisionPath(name), req, &out)
	if err != nil {
		return out, grantDecisionError(status, err)
	}
	audit := out.ReservedDecision
	ttl := req.TTL
	if ttl == "" {
		ttl = expected[0].TTL
	}
	expectedTTL, ttlErr := time.ParseDuration(ttl)
	valid := status == http.StatusAccepted && out.Name == name && out.UID == req.GrantUID && out.ScopeDigest == req.Digest && audit != nil && ttlErr == nil
	if valid {
		actual, err := time.ParseDuration(audit.TTL)
		valid = err == nil && actual == expectedTTL && audit.Approve == req.Approve && audit.GrantUID == req.GrantUID && audit.ScopeDigest == req.Digest && audit.TomRef == req.TomRef && audit.Ref == req.Ref && audit.BoundsRevision == "1" && audit.Case == expectedClass && reservedClientClass(out) == expectedClass && audit.ActorUID != "" && audit.RequesterUID != "" && audit.RequesterPodUID != "" && !audit.DecidedAt.IsZero() && audit.EffectiveDigest != ""
	}
	if !valid {
		return out, unconfirmed(http.MethodPost, apiv1.GrantDecisionPath(name), "reserved decision reply lost its exact UID/scope/TTL/ref/bounds correlation; inspect the grant")
	}
	return out, nil
}

func reservedClientClass(g apiv1.Grant) string {
	if g.Type == "breakglass" && g.Role == apiv1.BreakglassRole {
		return "rd2"
	}
	if g.Type == "kube" && g.Role == "dev-env-grant-secrets-read" {
		if g.AllNamespaces {
			return "rd3b"
		}
		return "rd3a"
	}
	if g.Type == "kube" && g.Role == "dev-env-grant-storage" && len(g.StorageTargets) > 0 {
		return "rd4-storage"
	}
	return ""
}
