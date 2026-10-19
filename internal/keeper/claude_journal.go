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

	"github.com/thaynes43/dev-env/internal/claudeauth"
	"github.com/thaynes43/dev-env/internal/codexauth"
)

const (
	DefaultClaudeJournalSecret = "dev-env-keeper-claude-auth"
	DefaultClaudeLiveSecret    = "dev-env-claude-live"
	claudeJournalKey           = "state.json"
	claudeReady                = "Ready"
	claudeIntent               = "RefreshIntent"
	claudeNeedsLogin           = "NeedsLogin"
)

var (
	errClaudeJournal = errors.New("claude private journal is unavailable")
	// errClaudeGeneration: a generation would not be strictly above what the
	// journal or the published document already holds.
	errClaudeGeneration = errors.New("claude generation would not increase")
)

// claudeRecord is never a generic Keeper.Job: replaying a possibly spent rotating
// token is forbidden. Only explicit private-journal serialization reveals it.
// Access carries the generation; the refresh credential never leaves this
// record and the journal Secret.
type claudeRecord struct {
	Access           claudeauth.Access
	Refresh          secretValue
	RefreshExpiresAt time.Time // zero when the provider did not say
	LastRefresh      time.Time
	Stage            string
	AttemptUID       string
}

func (claudeRecord) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, redacted) }
func (claudeRecord) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }
func (claudeRecord) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (claudeRecord) MarshalLog() any              { return redacted }

type claudeRecordWire struct {
	Version          int       `json:"version"`
	Generation       uint64    `json:"generation"`
	AccessToken      string    `json:"accessToken"`
	ExpiresAt        time.Time `json:"expiresAt"`
	RefreshToken     string    `json:"refreshToken"`
	RefreshExpiresAt time.Time `json:"refreshTokenExpiresAt"`
	Scopes           []string  `json:"scopes"`
	SubscriptionType string    `json:"subscriptionType"`
	RateLimitTier    string    `json:"rateLimitTier"`
	AccountUUID      string    `json:"accountUuid"`
	OrganizationUUID string    `json:"organizationUuid"`
	LastRefresh      time.Time `json:"lastRefresh"`
	Stage            string    `json:"stage"`
	AttemptUID       string    `json:"attemptUID"`
}

func claudeWire(r claudeRecord) claudeRecordWire {
	a := r.Access
	return claudeRecordWire{1, a.Generation, a.AccessToken, a.ExpiresAt, r.Refresh.Reveal(), r.RefreshExpiresAt, a.Scopes,
		a.SubscriptionType, a.RateLimitTier, a.AccountUUID, a.OrganizationUUID, r.LastRefresh, r.Stage, r.AttemptUID}
}

func validClaudeRecord(r claudeRecord) bool {
	if r.Stage != claudeReady && r.Stage != claudeIntent && r.Stage != claudeNeedsLogin {
		return false
	}
	if r.AttemptUID != "" && !validUID(r.AttemptUID) {
		return false
	}
	if r.Stage == claudeIntent && r.AttemptUID == "" {
		return false
	}
	if len(r.Refresh.Reveal()) > claudeauth.MaxTokenBytes {
		return false
	}
	if r.Access.Generation == 0 {
		a := r.Access
		return r.Stage == claudeNeedsLogin && r.Refresh.Empty() && a.AccessToken == "" && a.AccountUUID == "" && a.OrganizationUUID == "" && len(a.Scopes) == 0
	}
	// The access token must have been valid when it was written.
	if r.Access.Validate(r.LastRefresh) != nil {
		return false
	}
	return r.Stage == claudeNeedsLogin || !r.Refresh.Empty()
}

type claudeJournal struct {
	Client client.Client // uncached get/patch of one GitOps-created Secret
	Secret types.NamespacedName
}

// load reads and validates the record. An empty Secret is a NeedsLogin record at
// generation 0.
func (j *claudeJournal) load(ctx context.Context) (*corev1.Secret, claudeRecord, error) {
	if ctx.Err() != nil {
		return nil, claudeRecord{}, errClaudeJournal
	}
	var s corev1.Secret
	if j.Client.Get(ctx, j.Secret, &s) != nil || s.ResourceVersion == "" {
		return nil, claudeRecord{}, errClaudeJournal
	}
	r, err := decodeClaudeRecord(s.Data[claudeJournalKey])
	if err != nil {
		return nil, claudeRecord{}, err
	}
	return &s, r, nil
}

func decodeClaudeRecord(raw []byte) (claudeRecord, error) {
	if len(raw) == 0 {
		return claudeRecord{Stage: claudeNeedsLogin}, nil
	}
	var w claudeRecordWire
	if codexauth.DecodeStrict(raw, &w) != nil || w.Version != 1 {
		return claudeRecord{}, errClaudeJournal
	}
	r := claudeRecord{
		Access: claudeauth.Access{Generation: w.Generation, AccessToken: w.AccessToken, ExpiresAt: w.ExpiresAt, Scopes: w.Scopes,
			SubscriptionType: w.SubscriptionType, RateLimitTier: w.RateLimitTier, AccountUUID: w.AccountUUID, OrganizationUUID: w.OrganizationUUID},
		Refresh: newSecretValue(w.RefreshToken), RefreshExpiresAt: w.RefreshExpiresAt, LastRefresh: w.LastRefresh, Stage: w.Stage, AttemptUID: w.AttemptUID,
	}
	if !validClaudeRecord(r) {
		return claudeRecord{}, errClaudeJournal
	}
	return r, nil
}

// save performs one compare-and-swap on the Secret's resourceVersion, with no
// provider or patch retry. A response can be lost after a committed write, so
// every error is treated as ambiguous.
//
// The generation guard: against the stored record in original, a save may not
// lower the generation, and may not change the access material under an
// unchanged generation. The stored record survives a keeper restart, so the
// generation stays strictly increasing across restarts (increment 1 review:
// a reset makes every HOME that holds a receipt refuse forever).
func (j *claudeJournal) save(ctx context.Context, original *corev1.Secret, r claudeRecord) error {
	if ctx.Err() != nil || original == nil || original.ResourceVersion == "" || !validClaudeRecord(r) {
		return errClaudeJournal
	}
	stored, err := decodeClaudeRecord(original.Data[claudeJournalKey])
	if err != nil || r.Access.Generation < stored.Access.Generation {
		return errClaudeJournal
	}
	if r.Access.Generation == stored.Access.Generation && stored.Access.Generation != 0 && !sameClaudeAccess(stored.Access, r.Access) {
		return errClaudeJournal
	}
	raw, err := json.Marshal(claudeWire(r))
	if err != nil || len(raw) > claudeauth.MaxBytes {
		return errClaudeJournal
	}
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]string{"resourceVersion": original.ResourceVersion}, "data": map[string][]byte{claudeJournalKey: raw}})
	target := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: j.Secret.Namespace, Name: j.Secret.Name}}
	c, cancel := context.WithTimeout(ctx, claudeSaveTimeout)
	defer cancel()
	if j.Client.Patch(c, target, client.RawPatch(types.MergePatchType, patch), client.FieldOwner(FieldOwner)) != nil {
		return errClaudeJournal
	}
	return nil
}

func sameClaudeAccess(a, b claudeauth.Access) bool {
	if a.Generation != b.Generation || a.AccessToken != b.AccessToken || !a.ExpiresAt.Equal(b.ExpiresAt) ||
		a.SubscriptionType != b.SubscriptionType || a.RateLimitTier != b.RateLimitTier ||
		a.AccountUUID != b.AccountUUID || a.OrganizationUUID != b.OrganizationUUID || len(a.Scopes) != len(b.Scopes) {
		return false
	}
	for i := range a.Scopes {
		if a.Scopes[i] != b.Scopes[i] {
			return false
		}
	}
	return true
}

// nextClaudeGeneration is the one way a new generation is chosen. It is one
// above the larger of the journal's and the published document's generation, so
// it stays strictly increasing even when the journal Secret was re-created
// (generation 0) while dev-env-claude-live still holds a higher one.
func nextClaudeGeneration(journal, published uint64) (uint64, error) {
	top := max(journal, published)
	if top == ^uint64(0) {
		return 0, errClaudeGeneration
	}
	return top + 1, nil
}

// applyClaudeRefresh builds the Ready record that follows a refresh response:
// the new access token and expiry at generation gen, the account metadata kept
// unless the response restates it, and the refresh credential kept when the
// provider did not rotate it. The caller persists the result before it
// publishes anything.
func applyClaudeRefresh(prev claudeRecord, resp claudeTokens, gen uint64, now time.Time) (claudeRecord, error) {
	if gen == 0 || gen <= prev.Access.Generation || resp.Access.Empty() || resp.ExpiresIn <= 0 {
		return claudeRecord{}, errClaudeGeneration
	}
	next := prev
	next.Access.Generation = gen
	next.Access.AccessToken = resp.Access.Reveal()
	next.Access.ExpiresAt = now.Add(resp.ExpiresIn)
	next.LastRefresh = now
	next.Stage, next.AttemptUID = claudeReady, ""
	if len(resp.Scopes) > 0 {
		next.Access.Scopes = resp.Scopes
	}
	if resp.AccountUUID != "" {
		next.Access.AccountUUID = resp.AccountUUID
	}
	if resp.OrganizationUUID != "" {
		next.Access.OrganizationUUID = resp.OrganizationUUID
	}
	if !resp.Refresh.Empty() {
		next.Refresh = resp.Refresh
		// A rotated token with no stated lifetime has an unknown expiry; the old
		// token's expiry says nothing about it.
		next.RefreshExpiresAt = time.Time{}
	}
	if resp.RefreshExpiresIn > 0 {
		next.RefreshExpiresAt = now.Add(resp.RefreshExpiresIn)
	}
	if !validClaudeRecord(next) {
		return claudeRecord{}, errClaudeJournal
	}
	return next, nil
}
