package asks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Requester is the session that created an ask. The namespace is dev-agents.
type Requester struct {
	Namespace string
	Name      string
	UID       string
}

func whole(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }

func wholeInstant(t time.Time) bool { return t.Equal(t.Truncate(time.Second)) }

// Name is the ask's object name and id: ask- plus 16 hex digits derived from
// the requester and the request id, so a retry finds the same object.
func Name(req Requester, requestID string) string {
	sum := sha256.Sum256([]byte(req.Namespace + "\x00" + req.Name + "\x00" + req.UID + "\x00" + requestID))
	return "ask-" + hex.EncodeToString(sum[:8])
}

func requesterValue(req Requester) string {
	sum := sha256.Sum256([]byte(req.Namespace + "\x00" + req.Name + "\x00" + req.UID))
	return hex.EncodeToString(sum[:])[:63]
}

type digestBody struct {
	RequestID      string            `json:"requestId"`
	Type           string            `json:"type"`
	Question       string            `json:"question"`
	Options        []apiv1.AskOption `json:"options"`
	Recommended    string            `json:"recommended"`
	AllowFreeText  bool              `json:"allowFreeText"`
	WorkPackage    int               `json:"workPackage"`
	Links          []string          `json:"links"`
	ScopeAction    string            `json:"scopeAction"`
	ScopeTarget    string            `json:"scopeTarget"`
	ScopeExpiresAt string            `json:"scopeExpiresAt"`
	ExpiresIn      string            `json:"expiresIn"`
}

func requestDigest(in apiv1.AskCreateRequest) (string, error) {
	body := digestBody{
		RequestID: in.RequestID, Type: string(in.Type), Question: in.Question,
		Options: in.Options, Recommended: in.Recommended, AllowFreeText: in.AllowFreeText,
		WorkPackage: in.Context.WorkPackage, Links: in.Context.Links, ExpiresIn: in.ExpiresIn,
	}
	if body.Options == nil {
		body.Options = []apiv1.AskOption{}
	}
	if body.Links == nil {
		body.Links = []string{}
	}
	if in.Scope != nil {
		body.ScopeAction = in.Scope.Action
		body.ScopeTarget = in.Scope.Target
		body.ScopeExpiresAt = in.Scope.ExpiresAt.UTC().Format(time.RFC3339)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("%w: request digest", ErrUnavailable)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// normalizeCreate copies the request, normalizes multi-line text, and refuses
// a sub-second scope.expiresAt. metav1.Time stores whole seconds; truncating
// a fractional scope on the way in would make a value the validator accepted
// fail ValidateAsk after a round trip, and truncating before validation would
// pull a scope that was just over 24h back under the cap. A fractional scope
// is a 400 instead (lead note 2598).
func normalizeCreate(in apiv1.AskCreateRequest) (apiv1.AskCreateRequest, error) {
	in.Question = apiv1.NormalizeAskText(in.Question)
	if len(in.Options) > 0 {
		opts := slices.Clone(in.Options)
		for i := range opts {
			opts[i].Description = apiv1.NormalizeAskText(opts[i].Description)
		}
		in.Options = opts
	}
	if in.Context.Links != nil {
		in.Context.Links = slices.Clone(in.Context.Links)
	}
	if in.Scope != nil {
		scope := *in.Scope
		if !wholeInstant(scope.ExpiresAt) {
			return apiv1.AskCreateRequest{}, fmt.Errorf("%w: scope.expiresAt: must be a whole number of seconds", apiv1.ErrAskInvalid)
		}
		scope.ExpiresAt = scope.ExpiresAt.UTC()
		in.Scope = &scope
	}
	return in, nil
}

type prepared struct {
	Name   string
	Spec   coord.AskSpec
	Labels map[string]string
}

func prepare(req Requester, in apiv1.AskCreateRequest, now time.Time) (prepared, error) {
	if req.Namespace != Namespace || req.Name == "" || req.UID == "" {
		return prepared{}, ErrUnavailable
	}
	now = whole(now)
	in, err := normalizeCreate(in)
	if err != nil {
		return prepared{}, err
	}
	if err := in.ValidateAt(now); err != nil {
		return prepared{}, err
	}
	exp, err := in.ExpiresAtFrom(now)
	if err != nil {
		return prepared{}, err
	}
	exp = exp.UTC()
	if !wholeInstant(exp) {
		return prepared{}, fmt.Errorf("%w: expiresAt: must be a whole number of seconds", apiv1.ErrAskInvalid)
	}
	digest, err := requestDigest(in)
	if err != nil {
		return prepared{}, err
	}
	spec := coord.AskSpec{
		ID: Name(req, in.RequestID), Type: string(in.Type), Question: in.Question,
		Recommended: in.Recommended, AllowFreeText: in.AllowFreeText,
		Context: coord.AskContext{
			SessionName: req.Name, SessionUID: req.UID,
			WorkPackage: in.Context.WorkPackage, Links: in.Context.Links,
		},
		CreatedAt: metav1.NewTime(now), ExpiresAt: metav1.NewTime(exp),
		RequestID: in.RequestID, RequestDigest: digest,
	}
	if !apiv1.ValidAskID(spec.ID) {
		return prepared{}, fmt.Errorf("%w: ask id", ErrUnavailable)
	}
	if len(in.Options) > 0 {
		spec.Options = make([]coord.AskOption, len(in.Options))
		for i, o := range in.Options {
			spec.Options[i] = coord.AskOption{ID: o.ID, Label: o.Label, Description: o.Description}
		}
	}
	if in.Scope != nil {
		spec.Scope = &coord.AskScope{Action: in.Scope.Action, Target: in.Scope.Target, ExpiresAt: metav1.NewTime(in.Scope.ExpiresAt.UTC())}
	}
	wire, err := toWire(&coord.Ask{ObjectMeta: metav1.ObjectMeta{Name: spec.ID, Namespace: Namespace}, Spec: spec, Status: coord.AskStatus{State: string(apiv1.AskStateOpen)}})
	if err != nil {
		return prepared{}, err
	}
	if err := apiv1.ValidateAsk(wire); err != nil {
		return prepared{}, fmt.Errorf("%w: prepared ask failed validation", ErrUnavailable)
	}
	return prepared{
		Name: spec.ID, Spec: spec,
		Labels: map[string]string{
			RequesterLabel: requesterValue(req),
			StateLabel:     string(apiv1.AskStateOpen),
		},
	}, nil
}
