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

	"github.com/thaynes43/dev-env/internal/codexauth"
)

const (
	DefaultCodexJournalSecret = "dev-env-keeper-codex-auth"
	DefaultCodexLiveSecret    = "dev-env-codex-live"
	DefaultCodexLoginDir      = "/run/dev-env-codex-login/private"
	codexJournalKey           = "state.json"
	codexReady                = "Ready"
	codexIntent               = "RefreshIntent"
	codexNeedsLogin           = "NeedsLogin"
	codexSaveTimeout          = 2 * time.Second
)

var errCodexJournal = errors.New("Codex private journal is unavailable")

// codexRecord is never a generic Keeper.Job: replaying a possibly spent rotating
// token is forbidden. Only explicit private-journal serialization reveals it.
type codexRecord struct {
	Access     codexauth.Access
	Refresh    secretValue
	Stage      string
	AttemptUID string
	LoginUntil time.Time
	LoginOwner string
}

func (codexRecord) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, redacted) }
func (codexRecord) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }
func (codexRecord) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (codexRecord) MarshalLog() any              { return redacted }

type codexRecordWire struct {
	Version      int       `json:"version"`
	Generation   uint64    `json:"generation"`
	AccountID    string    `json:"account_id"`
	IDToken      string    `json:"id_token"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"exp"`
	LastRefresh  time.Time `json:"last_refresh"`
	Stage        string    `json:"stage"`
	AttemptUID   string    `json:"attemptUID"`
	LoginUntil   time.Time `json:"loginUntil"`
	LoginOwner   string    `json:"loginOwner"`
}

func recordWire(r codexRecord) codexRecordWire {
	a := r.Access
	return codexRecordWire{1, a.Generation, a.AccountID, a.IDToken, a.AccessToken, r.Refresh.Reveal(), a.ExpiresAt, a.LastRefresh, r.Stage, r.AttemptUID, r.LoginUntil, r.LoginOwner}
}

func validCodexRecord(r codexRecord) bool {
	if r.Stage != codexReady && r.Stage != codexIntent && r.Stage != codexNeedsLogin {
		return false
	}
	if r.AttemptUID != "" && !validUID(r.AttemptUID) {
		return false
	}
	if r.Stage == codexIntent && r.AttemptUID == "" {
		return false
	}
	if !r.LoginUntil.IsZero() && (r.Stage != codexNeedsLogin || r.AttemptUID == "" || r.LoginOwner == "") {
		return false
	}
	if len(r.LoginOwner) > 128 || (r.LoginUntil.IsZero() && r.LoginOwner != "") {
		return false
	}
	if len(r.Refresh.Reveal()) > codexauth.MaxTokenBytes {
		return false
	}
	if r.Access.Generation == 0 {
		return r.Stage == codexNeedsLogin && r.Refresh.Empty() && r.Access.AccountID == "" && r.Access.IDToken == "" && r.Access.AccessToken == ""
	}
	if r.Access.Validate(r.Access.LastRefresh) != nil {
		return false
	}
	return r.Stage == codexNeedsLogin || !r.Refresh.Empty()
}

func sameCodexMaterial(a, b codexRecord) bool {
	x, y := a.Access, b.Access
	return x.Generation == y.Generation && x.AccountID == y.AccountID && x.IDToken == y.IDToken && x.AccessToken == y.AccessToken && x.ExpiresAt.Equal(y.ExpiresAt) && x.LastRefresh.Equal(y.LastRefresh) && a.Refresh.Reveal() == b.Refresh.Reveal()
}

type codexJournal struct {
	Client client.Client // uncached get/patch of one GitOps-created Secret
	Secret types.NamespacedName
}

func (j *codexJournal) load(ctx context.Context) (*corev1.Secret, codexRecord, error) {
	if ctx.Err() != nil {
		return nil, codexRecord{}, errCodexJournal
	}
	var s corev1.Secret
	if j.Client.Get(ctx, j.Secret, &s) != nil || s.ResourceVersion == "" {
		return nil, codexRecord{}, errCodexJournal
	}
	raw := s.Data[codexJournalKey]
	if len(raw) == 0 {
		return &s, codexRecord{Stage: codexNeedsLogin}, nil
	}
	var w codexRecordWire
	if codexauth.DecodeStrict(raw, &w) != nil || w.Version != 1 {
		return nil, codexRecord{}, errCodexJournal
	}
	r := codexRecord{Access: codexauth.Access{Generation: w.Generation, AccountID: w.AccountID, IDToken: w.IDToken, AccessToken: w.AccessToken, ExpiresAt: w.ExpiresAt, LastRefresh: w.LastRefresh}, Refresh: newSecretValue(w.RefreshToken), Stage: w.Stage, AttemptUID: w.AttemptUID, LoginUntil: w.LoginUntil, LoginOwner: w.LoginOwner}
	if !validCodexRecord(r) {
		return nil, codexRecord{}, errCodexJournal
	}
	return &s, r, nil
}

// save performs one compare-and-swap, with no provider or patch retry. A response
// can be lost after a committed write, so every error is treated as ambiguous.
func (j *codexJournal) save(ctx context.Context, original *corev1.Secret, r codexRecord) error {
	if ctx.Err() != nil || original == nil || original.ResourceVersion == "" || !validCodexRecord(r) {
		return errCodexJournal
	}
	raw, err := json.Marshal(recordWire(r))
	if err != nil || len(raw) > codexauth.MaxBytes {
		return errCodexJournal
	}
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]string{"resourceVersion": original.ResourceVersion}, "data": map[string][]byte{codexJournalKey: raw}})
	target := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: j.Secret.Namespace, Name: j.Secret.Name}}
	c, cancel := context.WithTimeout(ctx, codexSaveTimeout)
	defer cancel()
	if j.Client.Patch(c, target, client.RawPatch(types.MergePatchType, patch), client.FieldOwner(FieldOwner)) != nil {
		return errCodexJournal
	}
	return nil
}
