package session

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

func TestIssuerTransportRefusesDestinationMethodRedirectAndOversize(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-host", "metadata", "loopback", "wrong-path", "wrong-method", "credential-get", "redirect", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			body := &trackedBody{Reader: strings.NewReader(`{"keys":[]}`)}
			client, err := NewIssuerHTTPClient("https://issuer.example/portal/", map[string]string{"https://issuer.example/keys": "GET"}, tripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				code := 200
				header := make(http.Header)
				if mode == "redirect" {
					code = 302
					header.Set("Location", "http://169.254.169.254/latest/meta-data/")
				}
				if mode == "oversize" {
					body.Reader = strings.NewReader(strings.Repeat("x", 65))
				}
				return &http.Response{StatusCode: code, Header: header, Body: body}, nil
			}), time.Second, 64)
			if err != nil {
				t.Fatal(err)
			}
			target := "https://issuer.example/keys"
			method := "GET"
			switch mode {
			case "wrong-host":
				target = "https://evil.example/keys"
			case "metadata":
				target = "http://169.254.169.254/latest/meta-data/"
			case "loopback":
				target = "https://127.0.0.1/keys"
			case "wrong-path":
				target = "https://issuer.example/other"
			case "wrong-method":
				method = "POST"
			}
			r, err := http.NewRequest(method, target, nil)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "credential-get" {
				r.Header.Set("Authorization", "Bearer SYNTHETIC-CANARY")
			}
			response, err := client.Do(r)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				data, readErr := io.ReadAll(response.Body)
				closeErr := response.Body.Close()
				if readErr != nil || closeErr != nil || string(data) != `{"keys":[]}` || !body.closed {
					t.Fatal("bounded response body not preserved/closed")
				}
			} else {
				if !errors.Is(err, portal.ErrUnavailable) {
					t.Fatal("unsafe transport response accepted")
				}
			}
			want := 0
			if mode == "valid" || mode == "redirect" || mode == "oversize" {
				want = 1
			}
			if calls != want {
				t.Fatal("rejected destination made a request or redirect followed")
			}
			if calls == 1 && !body.closed {
				t.Fatal("upstream body leaked")
			}
		})
	}
}

func TestTransportConfigurationCannotIntroduceOtherDestinations(t *testing.T) {
	base := tripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("constructor made network request")
		return nil, portal.ErrUnavailable
	})
	for _, target := range []string{"http://issuer.example/keys", "https://evil.example/keys", "https://127.0.0.1/keys", "https://localhost/keys", "https://issuer.example/keys?target=elsewhere", "https://user:password@issuer.example/keys"} {
		if client, err := NewIssuerHTTPClient("https://issuer.example/portal/", map[string]string{target: "GET"}, base, time.Second, 64); !errors.Is(err, portal.ErrUnavailable) || client != nil {
			t.Fatal("unsafe configured destination accepted")
		}
	}
}

func TestIssuerItselfCannotBeIPOrLocalhost(t *testing.T) {
	base := tripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("rejected issuer made a request")
		return nil, portal.ErrUnavailable
	})
	for _, issuer := range []string{"https://127.0.0.1", "https://169.254.169.254", "https://[::1]", "https://[fe80::1%25eth0]", "https://localhost", "https://LOCALHOST", "https://auth.localhost.", "https://localhost."} {
		t.Run(issuer, func(t *testing.T) {
			// Endpoint and issuer hosts match; only the literal/local-host rule
			// can reject these otherwise complete configurations.
			client, err := NewIssuerHTTPClient(issuer, map[string]string{issuer + "/keys": "GET"}, base, time.Second, 64)
			if !errors.Is(err, portal.ErrUnavailable) || client != nil {
				t.Fatal("unsafe issuer accepted")
			}
		})
	}
}
