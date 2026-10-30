package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// MaintenanceDecision is written only by the operator's authenticated human route.
// The held admission packet protects this annotation. It is not agent attestation.
type MaintenanceDecision struct {
	GrantUID string        `json:"grantUID"`
	Digest   string        `json:"digest"`
	By       string        `json:"by"`
	Approve  bool          `json:"approve"`
	TTL      time.Duration `json:"ttl"`
	At       time.Time     `json:"at"`
}

func (b *Broker) maintenanceDecision(ctx context.Context, g *v1alpha1.AccessGrant) (bool, error) {
	raw := g.Annotations[MaintenanceDecisionAnnotation]
	if raw == "" {
		return false, nil
	}
	var d MaintenanceDecision
	if len(raw) > 2048 || json.Unmarshal([]byte(raw), &d) != nil || d.GrantUID != string(g.UID) || d.Digest != MaintenanceDigest(g) || d.By != "human/dev-env-system/dev-env-human" || d.At.Before(g.CreationTimestamp.Time) || d.At.After(b.now().Time) || !b.now().Time.Before(d.At.Add(PendingTimeout)) {
		return true, fmt.Errorf("%w: invalid maintenance decision binding", ErrRefused)
	}
	err := b.Decide(ctx, g.Name, Decision{Approve: d.Approve, By: d.By, TTL: d.TTL, MaintenanceUID: d.GrantUID, MaintenanceDigest: d.Digest})
	return true, err
}
