package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// missingCloneNotificationKey describes only a closed, single-cause failure.
// Diagnostic strings, timestamps, retry stamps and bundle paths are excluded.
// A legacy producer's absent bootstrap proof remains explicitly unknown.
func missingCloneNotificationKey(s *v1alpha1.AgentSession, rep protocol.RescueReport, reason string) string {
	if s.Spec.Workspace != nil || (reason != "NotProven" && reason != "BootstrapInterrupted") ||
		rep.Session != s.Name || len(rep.Repos) != 0 || rep.Bundle != nil || rep.VolumeEmpty ||
		!rep.OK || !rep.CleanAndPushed || rep.Agent == nil || rep.Agent.Running || s.Spec.Repo == "" {
		return ""
	}
	if rep.NoClone != nil {
		if _, ok := interruptedBootstrap(s, rep); !ok || reason != "BootstrapInterrupted" {
			return ""
		}
	} else if reason != "NotProven" {
		return ""
	}
	facts := struct {
		Version int                    `json:"version"`
		Repo    string                 `json:"repo"`
		Reason  string                 `json:"reason"`
		Agent   protocol.AgentStop     `json:"agent"`
		Proof   *protocol.NoCloneProof `json:"proof"`
	}{1, s.Spec.Repo, reason, *rep.Agent, rep.NoClone}
	b, _ := json.Marshal(facts)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// RescueIncidentOwners is deployment-owned acknowledgment configuration. It
// changes notification urgency only, never the rescue result or lifecycle.
type RescueIncidentOwners struct {
	Schema string                `json:"schema"`
	Owners []RescueIncidentOwner `json:"owners"`
}

type RescueIncidentOwner struct {
	SessionUID          string    `json:"sessionUID"`
	Generation          int64     `json:"generation"`
	Reason              string    `json:"reason"`
	NotificationKey     string    `json:"notificationKey"`
	PendingDelete       bool      `json:"pendingDelete"`
	OwnerSessionUID     string    `json:"ownerSessionUID"`
	OwnerConversationID string    `json:"ownerConversationId"`
	AcknowledgedAt      time.Time `json:"acknowledgedAt"`
	ExpiresAt           time.Time `json:"expiresAt"`
	AckRef              string    `json:"ackRef"`
	AckSHA256           string    `json:"ackSHA256"`
}

var (
	rescueUIDPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
	rescueKeyPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	rescueRefPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,255}$`)
)

// ParseRescueIncidentOwners accepts no unknown fields, duplicate Session UIDs or
// unbounded/permanent ownership. The deployment reviewer must independently
// verify each referenced native acknowledgment; this parser cannot do that.
func ParseRescueIncidentOwners(b []byte) (*RescueIncidentOwners, error) {
	bad := errors.New("invalid rescue incident ownership configuration")
	if len(b) == 0 || len(b) > 32<<10 {
		return nil, bad
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var out RescueIncidentOwners
	if d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF || out.Schema != "dev-env.rescue-incident-owners.v1" || len(out.Owners) > 64 {
		return nil, bad
	}
	seen := map[string]bool{}
	for _, o := range out.Owners {
		if !rescueUIDPattern.MatchString(o.SessionUID) || !rescueUIDPattern.MatchString(o.OwnerSessionUID) || !rescueUIDPattern.MatchString(o.OwnerConversationID) ||
			(o.Reason != "NotProven" && o.Reason != "BootstrapInterrupted") ||
			!rescueKeyPattern.MatchString(o.NotificationKey) || !rescueKeyPattern.MatchString(o.AckSHA256) || !rescueRefPattern.MatchString(o.AckRef) ||
			o.Generation < 1 || o.AcknowledgedAt.IsZero() || !o.ExpiresAt.After(o.AcknowledgedAt) || o.ExpiresAt.Sub(o.AcknowledgedAt) > 24*time.Hour || seen[o.SessionUID] {
			return nil, bad
		}
		seen[o.SessionUID] = true
	}
	return &out, nil
}

// ownerFor never writes status or consults work claims, annotations or Pod phase.
// Unknown risk, stale generation, future acknowledgment and expiry fail unowned.
func (c *RescueIncidentOwners) ownerFor(s *v1alpha1.AgentSession, cond *metav1.Condition, sessions []v1alpha1.AgentSession, now time.Time) string {
	if c == nil || s.Status.Rescue == nil || cond == nil || cond.Status != metav1.ConditionTrue {
		return ""
	}
	r := s.Status.Rescue
	if r.Result != v1alpha1.RescueFailed || r.Superseded || r.Generation != s.Generation || !rescueKeyPattern.MatchString(r.NotificationKey) ||
		(cond.Reason != "NotProven" && cond.Reason != "BootstrapInterrupted") {
		return ""
	}
	for _, o := range c.Owners {
		if o.SessionUID == string(s.UID) && o.Generation == s.Generation && o.Reason == cond.Reason && o.NotificationKey == r.NotificationKey &&
			o.PendingDelete == !s.DeletionTimestamp.IsZero() && !now.Before(o.AcknowledgedAt) && now.Before(o.ExpiresAt) {
			for i := range sessions {
				owner := &sessions[i]
				a := owner.Status.Agent
				if string(owner.UID) != o.OwnerSessionUID || !owner.DeletionTimestamp.IsZero() || owner.Status.TaskOutcome != nil ||
					a == nil || a.Task != nil || a.Boot != "ready" || a.ConversationID != o.OwnerConversationID ||
					a.LastHeartbeat == nil || a.LastHeartbeat.After(now) || now.Sub(a.LastHeartbeat.Time) > 3*time.Minute {
					continue
				}
				if a.Status == "busy" || a.Status == "idle" || a.Status == "waiting" {
					return o.OwnerSessionUID
				}
			}
		}
	}
	return ""
}
