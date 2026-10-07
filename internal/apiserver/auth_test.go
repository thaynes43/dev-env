package apiserver

import (
	"context"
	"errors"
	"net/http"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// reviewer is a TokenReviewer whose API server answers with status, or fails
// with err, and records what it was asked.
func reviewer(t *testing.T, status authenticationv1.TokenReviewStatus, err error, asked *authenticationv1.TokenReviewSpec) TokenReviewer {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
			tr, ok := obj.(*authenticationv1.TokenReview)
			if !ok {
				t.Fatalf("created a %T", obj)
			}
			*asked = tr.Spec
			if err != nil {
				return err
			}
			tr.Status = status
			return nil
		},
	}).Build()
	return TokenReviewer{Client: c}
}

func TestTokenReviewer(t *testing.T) {
	ctx := context.Background()
	var asked authenticationv1.TokenReviewSpec
	r := reviewer(t, authenticationv1.TokenReviewStatus{
		Authenticated: true,
		Audiences:     []string{apiv1.TokenAudience},
		User: authenticationv1.UserInfo{
			Username: "system:serviceaccount:dev-agents:dev-env-agent",
			Extra: map[string]authenticationv1.ExtraValue{
				extraPodName: {"haynes-ops-1006-100000"},
				extraPodUID:  {"uid-1"},
			},
		},
	}, nil, &asked)
	id, err := r.Authenticate(ctx, "the-token")
	if err != nil {
		t.Fatal(err)
	}
	if asked.Token != "the-token" || len(asked.Audiences) != 1 || asked.Audiences[0] != apiv1.TokenAudience {
		t.Errorf("asked %+v", asked)
	}
	want := Identity{Username: "system:serviceaccount:dev-agents:dev-env-agent", Namespace: "dev-agents",
		ServiceAccount: "dev-env-agent", PodName: "haynes-ops-1006-100000", PodUID: "uid-1"}
	if id != want {
		t.Errorf("identity %+v", id)
	}

	for name, st := range map[string]authenticationv1.TokenReviewStatus{
		"not authenticated": {Authenticated: false, Error: "token has expired"},
		"another audience":  {Authenticated: true, Audiences: []string{"https://kubernetes.default.svc"}, User: authenticationv1.UserInfo{Username: "x"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := reviewer(t, st, nil, &asked).Authenticate(ctx, "t")
			var ae *apiError
			if !errors.As(err, &ae) || ae.status != http.StatusUnauthorized {
				t.Fatalf("got %v", err)
			}
		})
	}

	_, err = reviewer(t, authenticationv1.TokenReviewStatus{}, errors.New("connection refused"), &asked).Authenticate(ctx, "t")
	var ae *apiError
	if !errors.As(err, &ae) || ae.status != http.StatusServiceUnavailable {
		t.Fatalf("an unreachable API server: %v", err)
	}
}

func TestIdentityFrom(t *testing.T) {
	for user, want := range map[string]Identity{
		"system:serviceaccount:dev:dev-env": {Namespace: "dev", ServiceAccount: "dev-env"},
		"system:serviceaccount:dev":         {},
		"system:serviceaccount::x":          {},
		"system:serviceaccount:a:b:c":       {},
		"tom@example.com":                   {},
	} {
		got := identityFrom(authenticationv1.UserInfo{Username: user})
		want.Username = user
		if got != want {
			t.Errorf("%q: %+v, want %+v", user, got, want)
		}
	}
}

func TestBearerToken(t *testing.T) {
	for h, ok := range map[string]bool{"Bearer abc": true, "bearer abc": true, "Basic abc": false, "Bearer ": false, "": false, "abc": false} {
		r, _ := http.NewRequest(http.MethodGet, "/", nil)
		if h != "" {
			r.Header.Set("Authorization", h)
		}
		tok, err := bearerToken(r)
		if (err == nil) != ok || (ok && tok != "abc") {
			t.Errorf("%q: %q %v", h, tok, err)
		}
	}
}

func TestParseServiceAccountRefs(t *testing.T) {
	got, err := ParseServiceAccountRefs([]string{"dev/dev-env", " dev-agents/dev-env-workbench ", ""})
	if err != nil || len(got) != 2 || got[1] != "dev-agents/dev-env-workbench" {
		t.Errorf("%v %v", got, err)
	}
	for _, bad := range []string{"dev-env", "/x", "x/", "a/b/c"} {
		if _, err := ParseServiceAccountRefs([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
