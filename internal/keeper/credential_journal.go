package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const (
	DefaultCredentialJournalSecret = "dev-env-keeper-credential-journal"
	journalKey                     = "journal.json"
	journalMaxEntries              = 128
	journalMaxBytes                = 512 << 10
)

var errJournalFull = errors.New("credential journal capacity reached")

type journalStage string

const (
	journalIntent  journalStage = "Intent"
	journalMinted  journalStage = "Minted"
	journalCleanup journalStage = "Cleanup"
	journalRevoked journalStage = "Revoked"
)

// credentialEntry is private. All generic formatting and marshaling is redacted;
// journal serialization and the install wire payload are explicit boundaries.
type credentialEntry struct {
	JobUID          types.UID
	Spec            v1alpha1.CredentialJobSpec
	Stage           journalStage
	Uncertain       bool
	TokenID         string
	TokenSecret     secretValue
	Failure         v1alpha1.CredentialFailureCode
	InstallFailures int
	InstallPodUID   string
}

func (credentialEntry) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, redacted) }
func (credentialEntry) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }
func (credentialEntry) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (credentialEntry) MarshalLog() any              { return redacted }

type entryWire struct {
	JobUID          types.UID                      `json:"jobUID"`
	Spec            v1alpha1.CredentialJobSpec     `json:"spec"`
	Stage           journalStage                   `json:"stage"`
	Uncertain       bool                           `json:"uncertain,omitempty"`
	TokenID         string                         `json:"tokenID,omitempty"`
	TokenSecret     string                         `json:"tokenSecret,omitempty"`
	Failure         v1alpha1.CredentialFailureCode `json:"failureCode,omitempty"`
	InstallFailures int                            `json:"installFailures,omitempty"`
	InstallPodUID   string                         `json:"installPodUID,omitempty"`
}
type journalWire struct {
	Version int                  `json:"version"`
	Entries map[string]entryWire `json:"entries"`
}

type credentialJournal struct {
	Client client.Client // uncached; get/patch only this named Secret
	Secret types.NamespacedName
}

func (j *credentialJournal) load(ctx context.Context) (*corev1.Secret, map[string]credentialEntry, error) {
	var s corev1.Secret
	if err := j.Client.Get(ctx, j.Secret, &s); err != nil {
		return nil, nil, errors.New("could not read the named credential journal")
	}
	entries := map[string]credentialEntry{}
	raw := s.Data[journalKey]
	if len(raw) == 0 {
		return &s, entries, nil
	}
	if len(raw) > journalMaxBytes {
		return nil, nil, errors.New("credential journal exceeds its byte limit")
	}
	var wire journalWire
	if json.Unmarshal(raw, &wire) != nil || wire.Version != 1 || len(wire.Entries) > journalMaxEntries {
		return nil, nil, errors.New("credential journal is invalid")
	}
	for key, w := range wire.Entries {
		e := credentialEntry{JobUID: w.JobUID, Spec: w.Spec, Stage: w.Stage, Uncertain: w.Uncertain, TokenID: w.TokenID, TokenSecret: newSecretValue(w.TokenSecret), Failure: w.Failure, InstallFailures: w.InstallFailures, InstallPodUID: w.InstallPodUID}
		if key != string(e.JobUID) || !validEntry(e) {
			return nil, nil, errors.New("credential journal entry is invalid")
		}
		entries[key] = e
	}
	return &s, entries, nil
}

func validEntry(e credentialEntry) bool {
	if !validUID(string(e.JobUID)) || checkCredentialSpec(e.Spec) != nil || e.InstallFailures < 0 {
		return false
	}
	switch e.Stage {
	case journalIntent, journalMinted, journalCleanup, journalRevoked:
	default:
		return false
	}
	if e.Stage == journalMinted && (e.TokenID != providerID(e.Spec.Grant.UID) || e.TokenSecret.Empty()) {
		return false
	}
	return true
}

func encodeJournal(entries map[string]credentialEntry) ([]byte, error) {
	if len(entries) > journalMaxEntries {
		return nil, errJournalFull
	}
	wire := journalWire{Version: 1, Entries: map[string]entryWire{}}
	for key, e := range entries {
		if !validEntry(e) || key != string(e.JobUID) {
			return nil, errors.New("cannot persist an invalid credential entry")
		}
		wire.Entries[key] = entryWire{JobUID: e.JobUID, Spec: e.Spec, Stage: e.Stage, Uncertain: e.Uncertain, TokenID: e.TokenID, TokenSecret: e.TokenSecret.Reveal(), Failure: e.Failure, InstallFailures: e.InstallFailures, InstallPodUID: e.InstallPodUID}
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, errors.New("could not encode credential journal")
	}
	if len(raw) > journalMaxBytes {
		return nil, errJournalFull
	}
	return raw, nil
}

// put/delete retry only optimistic Secret conflicts, never provider operations.
func (j *credentialJournal) update(ctx context.Context, uid types.UID, next *credentialEntry) error {
	for range 3 {
		s, entries, err := j.load(ctx)
		if err != nil {
			return err
		}
		if next == nil {
			delete(entries, string(uid))
		} else {
			entries[string(uid)] = *next
		}
		raw, err := encodeJournal(entries)
		if err != nil {
			return err
		}
		// Raw merge patch includes resourceVersion. Avoid logging server errors: a
		// webhook may echo the entire private request in its response.
		patch, _ := json.Marshal(map[string]any{"metadata": map[string]string{"resourceVersion": s.ResourceVersion}, "data": map[string][]byte{journalKey: raw}})
		target := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: j.Secret.Namespace, Name: j.Secret.Name}}
		err = j.Client.Patch(ctx, target, client.RawPatch(types.MergePatchType, patch), client.FieldOwner(FieldOwner))
		if err == nil {
			return nil
		}
		if !isConflict(err) {
			return errors.New("could not persist credential journal")
		}
	}
	return errors.New("credential journal kept changing")
}

func providerName(uid types.UID) string { return "grant-" + string(uid) }
func providerID(uid types.UID) string   { return "dev-env@pve!" + providerName(uid) }
func providerComment(e credentialEntry) string {
	return "dev-env:" + string(e.Spec.Grant.UID) + ":" + string(e.JobUID)
}
func entryExpired(e credentialEntry, now time.Time) bool { return !now.Before(e.Spec.ExpiresAt.Time) }
