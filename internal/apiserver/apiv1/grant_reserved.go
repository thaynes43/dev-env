package apiv1

import (
	"encoding/base64"
	"errors"
	"regexp"
	"time"
)

var (
	reservedRefPattern   = regexp.MustCompile(`^[A-Za-z0-9_.:/#-]{1,128}$`)
	reservedExactPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\-]{0,127}$`)
	reservedUIDPattern   = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
)

// GrantStorageTarget is the exact immutable target of an RD4 storage request.
type GrantStorageTarget struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// ReservedDecisionBinding is the retained S7 audit, never human provenance.
type ReservedDecisionBinding struct {
	Approve            bool                `json:"approve"`
	Case               string              `json:"case"`
	BoundsRevision     string              `json:"boundsRevision"`
	GrantUID           string              `json:"grantUID"`
	RequesterUID       string              `json:"requesterUID"`
	RequesterPodUID    string              `json:"requesterPodUID"`
	ActorUID           string              `json:"actorUID"`
	ActorSessionUID    string              `json:"actorSessionUID,omitempty"`
	ScopeDigest        string              `json:"scopeDigest"`
	EffectiveDigest    string              `json:"effectiveDigest"`
	TomRef             string              `json:"tomRef"`
	Ref                string              `json:"ref"`
	TTL                string              `json:"ttl"`
	DecidedAt          time.Time           `json:"decidedAt"`
	Namespaces         []ReservedNamespace `json:"namespaces,omitempty"`
	ExcludedNamespaces []string            `json:"excludedNamespaces"`
}

type ReservedNamespace struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// GrantHardwareIntentPath is a separate audit operation on a caller's own
// Active C4 credential lease. It never decides or creates a credential grant.
func GrantHardwareIntentPath(name string) string { return GrantPath(name) + "/hardware-intent" }

// HardwareIntentRequest contains no actor fields: TokenReview supplies them.
// TomRef is a required nonsecret decision reference (for example, Tom's message
// or a configured delegated-authority reference); agent assertion, not verified human consent.
type HardwareIntentRequest struct {
	GrantUID  string `json:"grantUID"`
	Digest    string `json:"digest"`
	Operation string `json:"operation"`
	Host      string `json:"host"`
	Device    string `json:"device"`
	Firmware  string `json:"firmware"`
	TomRef    string `json:"tomRef"`
}

// ValidateHardwareIntentRequest is shared by the standard-library client and
// the API. It validates a bounded exact description, never hardware authority.
func ValidateHardwareIntentRequest(r HardwareIntentRequest) error {
	digest, err := base64.RawURLEncoding.DecodeString(r.Digest)
	if !reservedUIDPattern.MatchString(r.GrantUID) || err != nil || len(digest) != 32 || len(r.Digest) != 43 {
		return errors.New("hardware intent requires the reviewed grant UID and scope digest")
	}
	if !reservedRefPattern.MatchString(r.TomRef) {
		return errors.New("hardware intent requires a nonsecret Tom message reference (1–128 letters, digits or . _ : / # -); this is an agent assertion")
	}
	for _, v := range []string{r.Operation, r.Host, r.Device, r.Firmware} {
		if !reservedExactPattern.MatchString(v) {
			return errors.New("hardware intent requires exact operation, host, device and firmware identifiers (1–128 characters, no whitespace or wildcards)")
		}
	}
	return nil
}

// HardwareIntentReceipt records intent only. Existing credential authority is
// unchanged. A reply does not prove any hardware operation was performed.
type HardwareIntentReceipt struct {
	HardwareIntentRequest
	SessionUID string    `json:"sessionUID"`
	PodUID     string    `json:"podUID"`
	RecordedAt time.Time `json:"recordedAt"`
	Notice     string    `json:"notice"`
}
