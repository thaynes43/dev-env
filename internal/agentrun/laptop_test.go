package agentrun

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

type laptopHelpers struct {
	calls    [][]string
	starts   [][]string
	stops    int
	mints    int
	contexts int
	ca       string
	fail     string
}

// laptopHarness serves the API with a private CA and the actual Service DNS
// name on a loopback address, as kubectl's tunnel will.
func laptopHarness(t *testing.T, dns string) (*harness, *laptopHelpers) {
	t.Helper()
	h := newHarness(t)
	cert, ca := serviceCertificate(t, dns)
	srv := httptest.NewUnstartedServer(h.api.srv.Config.Handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	h.api.srv = srv
	h.vars = map[string]string{}
	h.kubectl = func([]string) int { return 0 }
	l := &laptopHelpers{ca: ca}
	h.env.Output = func(ctx context.Context, argv []string) ([]byte, error) {
		l.calls = append(l.calls, append([]string(nil), argv...))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if l.fail != "" && strings.Contains(strings.Join(argv, " "), l.fail) {
			return []byte("private-token-output"), errors.New("private-token-error")
		}
		switch {
		case hasWord(argv, "current-context"):
			l.contexts++
			return []byte("captured-context\n"), nil
		case hasWord(argv, "configmap"):
			return json.Marshal(map[string]any{"data": map[string]string{"ca.crt": l.ca}})
		case hasWord(argv, "token"):
			l.mints++
			return []byte("human-token\n"), nil
		default:
			t.Fatalf("unexpected kubectl output command: %q", argv)
			return nil, nil
		}
	}
	h.env.PortForward = func(_ context.Context, argv []string) (string, func(), error) {
		l.starts = append(l.starts, append([]string(nil), argv...))
		stop := func() { l.stops++ }
		if l.fail == "port-forward" {
			return "", stop, errors.New("private-forward-error")
		}
		return srv.URL, stop, nil
	}
	return h, l
}

func hasWord(argv []string, word string) bool {
	for _, arg := range argv {
		if arg == word {
			return true
		}
	}
	return false
}

func serviceCertificate(t *testing.T, dns string) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test API CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{dns},
		NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: key},
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
}

const operatorDNS = "dev-env-operator.dev-env-system.svc.cluster.local"

func TestLaptopReadAndExecUseOneContext(t *testing.T) {
	for _, verb := range []string{"list", "fleet", "show", "attach", "detach"} {
		t.Run(verb, func(t *testing.T) {
			h, l := laptopHarness(t, operatorDNS)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				switch r.URL.Path {
				case apiv1.SessionsPath:
					writeTestJSON(w, http.StatusOK, apiv1.SessionList{Sessions: []apiv1.Session{}})
				case apiv1.FleetPath:
					writeTestJSON(w, http.StatusOK, apiv1.Fleet{})
				default:
					writeTestJSON(w, http.StatusOK, runningOn(testSession(name, ""), "talosw02"))
				}
			}
			args := []string{verb, "--kubeconfig", "/laptop/config"}
			if verb != "list" && verb != "fleet" {
				args = append(args, name)
			}
			h.mustRun(ExitOK, args...)
			if l.contexts != 1 || l.mints != 1 || len(l.starts) != 1 || l.stops != 1 {
				t.Fatalf("contexts=%d mints=%d starts=%d stops=%d", l.contexts, l.mints, len(l.starts), l.stops)
			}
			prefix := []string{"/usr/bin/kubectl", "--kubeconfig", "/laptop/config", "--context", "captured-context"}
			for _, argv := range append(l.calls[1:], l.starts...) {
				if !reflect.DeepEqual(argv[:len(prefix)], prefix) {
					t.Errorf("operation uses another cluster: %q", argv)
				}
			}
			wantForward := append(append([]string(nil), prefix...), "port-forward", "-n", "dev-env-system", "service/dev-env-operator", ":8443", "--address=127.0.0.1")
			if !reflect.DeepEqual(l.starts[0], wantForward) {
				t.Errorf("forward = %q, want %q", l.starts[0], wantForward)
			}
			for _, command := range h.ran {
				if !strings.HasPrefix(command, strings.Join(prefix, " ")+" exec -n dev-agents ") {
					t.Errorf("exec context differs: %s", command)
				}
			}
			if got := h.lastAuth(); got != "Bearer human-token" {
				t.Errorf("Authorization = %q", got)
			}
		})
	}
}

func TestLaptopTokenRefreshAndExplicitContext(t *testing.T) {
	h, l := laptopHarness(t, operatorDNS)
	a := &app{env: h.env}
	defer a.close()
	c, err := a.connect(t.Context(), connOpts{context: "chosen-context"})
	if err != nil {
		t.Fatal(err)
	}
	for _, elapsed := range []time.Duration{4 * time.Minute, 2 * time.Minute} {
		h.now = h.now.Add(elapsed)
		if _, err := c.token.Token(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if l.contexts != 0 || l.mints != 2 {
		t.Errorf("contexts=%d mints=%d, want 0 and 2", l.contexts, l.mints)
	}
	for _, argv := range l.calls {
		if len(argv) < 3 || argv[1] != "--context" || argv[2] != "chosen-context" {
			t.Errorf("context = %q", argv)
		}
		if hasWord(argv, "token") && (!hasWord(argv, "dev-env-human") || !hasWord(argv, "dev-env-operator") || !hasWord(argv, "--duration=600s")) {
			t.Errorf("token scope = %q", argv)
		}
	}
}

func TestLaptopVerifiesCAAndServiceName(t *testing.T) {
	for _, tc := range []struct {
		name, dns string
		wrongCA   bool
	}{
		{"wrong service name", "other-service.dev-env-system.svc.cluster.local", false},
		{"wrong CA", operatorDNS, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, l := laptopHarness(t, tc.dns)
			if tc.wrongCA {
				_, l.ca = serviceCertificate(t, operatorDNS)
			}
			serveList(h)
			h.mustRun(ExitAuth, "list")
			contains(t, "stderr", h.stderr.String(), "certificate agent-run does not trust")
			if l.stops != 1 || len(h.api.requests()) != 0 || len(h.sleeps) != 0 {
				t.Errorf("stops=%d API requests=%d retries=%d", l.stops, len(h.api.requests()), len(h.sleeps))
			}
		})
	}
}

func TestLaptopFailuresDoNotLeakOrLeaveForward(t *testing.T) {
	for _, operation := range []string{"current-context", "configmap", "token", "port-forward", "bad-ca", "api-refusal"} {
		t.Run(operation, func(t *testing.T) {
			h, l := laptopHarness(t, operatorDNS)
			l.fail = operation
			want := ExitAuth
			wantStops := 0
			switch operation {
			case "port-forward":
				want, wantStops = ExitRetry, 1
			case "bad-ca":
				l.ca = "not a certificate"
			case "api-refusal":
				wantStops = 1
				h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
					apiError(w, http.StatusForbidden, apiv1.CodeForbidden, "forbidden")
				}
			}
			h.mustRun(want, "list")
			if l.stops != wantStops {
				t.Errorf("stops=%d, want %d", l.stops, wantStops)
			}
			if strings.Contains(h.stdout.String()+h.stderr.String(), "private-") {
				t.Error("helper credential output leaked")
			}
		})
	}
}

func TestLaptopExplicitInputs(t *testing.T) {
	t.Run("an explicit URL keeps the direct path and can mint", func(t *testing.T) {
		// The normal harness's certificate is valid for its loopback URL.
		h := newHarness(t)
		serveList(h)
		delete(h.vars, envTokenFile)
		h.kubectl = func([]string) int { return 0 }
		h.env.Output = func(_ context.Context, argv []string) ([]byte, error) {
			if hasWord(argv, "current-context") {
				return []byte("one-context"), nil
			}
			if !hasWord(argv, "token") || argv[2] != "one-context" {
				t.Fatalf("unexpected direct helper: %q", argv)
			}
			return []byte("direct-human-token"), nil
		}
		h.env.PortForward = func(context.Context, []string) (string, func(), error) {
			t.Fatal("explicit URL started a forward")
			return "", nil, nil
		}
		h.mustRun(ExitOK, "list")
		if h.lastAuth() != "Bearer direct-human-token" {
			t.Error("did not mint for the direct URL")
		}
	})
	t.Run("supplied CA and token override auto-fetch and mint", func(t *testing.T) {
		h, l := laptopHarness(t, operatorDNS)
		serveList(h)
		token := h.write("chosen-token", "chosen-token")
		ca := h.write("chosen-ca", l.ca)
		h.mustRun(ExitOK, "list", "--context", "selected", "--token-file", token, "--ca-file", ca)
		if len(l.calls) != 0 || l.mints != 0 || len(l.starts) != 1 || l.stops != 1 || h.lastAuth() != "Bearer chosen-token" {
			t.Fatalf("calls=%q mints=%d forwards=%d stops=%d", l.calls, l.mints, len(l.starts), l.stops)
		}
	})
}

func TestLaptopCancelClosesForward(t *testing.T) {
	h, l := laptopHarness(t, operatorDNS)
	ctx, cancel := context.WithCancel(t.Context())
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		cancel()
		<-r.Context().Done()
	}
	if code := Run(ctx, []string{"list"}, h.env); code != ExitFailed {
		t.Errorf("exit=%d, want %d", code, ExitFailed)
	}
	if l.stops != 1 {
		t.Errorf("stops=%d, want 1", l.stops)
	}
}

func TestPodKubeSelectorsCannotSplitAPIAndExec(t *testing.T) {
	for _, flag := range []string{"--context", "--kubeconfig"} {
		for _, verb := range []string{"list", "attach", "detach"} {
			t.Run(verb+flag, func(t *testing.T) {
				h := newHarness(t)
				k := inPod(h, http.StatusCreated)
				h.kubectl = func([]string) int { return 0 }
				args := []string{verb, flag, "other-cluster"}
				if verb != "list" {
					args = append(args, name)
				}
				h.mustRun(ExitUsage, args...)
				contains(t, "stderr", h.stderr.String(), "cannot select another cluster while using this pod's API or identity")
				if len(h.api.requests()) != 0 || len(h.ran) != 0 || k.mints.Load() != 0 {
					t.Errorf("split-cluster operation attempted: API=%d exec=%d mints=%d", len(h.api.requests()), len(h.ran), k.mints.Load())
				}
			})
		}
	}
}

func TestLaptopForwardBypassesHTTPProxy(t *testing.T) {
	h, _ := laptopHarness(t, operatorDNS)
	serveList(h)
	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	var calls atomic.Int32
	transport.Proxy = func(*http.Request) (*url.URL, error) {
		calls.Add(1)
		return nil, errors.New("the forwarding request reached a proxy")
	}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original })
	h.mustRun(ExitOK, "list")
	if calls.Load() != 0 {
		t.Errorf("proxy calls=%d, want 0", calls.Load())
	}
}
