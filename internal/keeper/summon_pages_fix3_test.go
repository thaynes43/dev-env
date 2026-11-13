package keeper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// Story 782, fix round 3: the proofs for the coordinator's rulings on the
// re-check of fix round 2 (rulings in PR 1102 comment 6841848115; not a ruling
// by Tom). No test here reaches a cluster: the servers are local httptest
// servers, the fake client or the local envtest control plane, no test reads
// the process's cluster environment, and no exec credential plugin runs.

// summonHookWords is each refused field's fixed words in the log line.
var summonHookWords = map[string]string{
	"Transport":                 "transport",
	"WrapTransport":             "transport wrapper",
	"Dial":                      "dialer",
	"Proxy":                     "proxy function",
	"AuthProvider":              "auth provider",
	"AuthConfigPersister":       "auth config persister",
	"ExecProvider":              "exec credential plugin",
	"WarningHandler":            "warning handler",
	"WarningHandlerWithContext": "context warning handler",
}

type summonPersister struct{ ran *atomic.Int32 }

func (p summonPersister) Persist(map[string]string) error { p.ran.Add(1); return nil }

type summonWarnings struct {
	ran  *atomic.Int32
	lock *sync.Mutex
}

func (w summonWarnings) HandleWarningHeader(int, string, string) {
	w.ran.Add(1)
	w.lock.Lock()
	defer w.lock.Unlock()
}

func (w summonWarnings) HandleWarningHeaderWithContext(context.Context, int, string, string) {
	w.ran.Add(1)
	w.lock.Lock()
	defer w.lock.Unlock()
}

// summonExecMarker writes a plugin that would leave a file behind if anything
// ran it, and returns the plugin's path and the marker's.
func summonExecMarker(t *testing.T) (plugin, marker string) {
	t.Helper()
	dir := t.TempDir()
	plugin, marker = filepath.Join(dir, "summon-exec-plugin"), filepath.Join(dir, "ran")
	if err := os.WriteFile(plugin, []byte("#!/bin/sh\n: > '"+marker+"'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return plugin, marker
}

// Ruling 1 (F1, HIGH, for a supplied config): besides a transport, a
// transport wrapper and a dialer, a keeper REST config that carries a proxy
// function, an auth provider, an auth config persister, an exec credential
// plugin or either warning handler is refused. The pager is off, one log line
// names which in fixed words, never a value; nothing of the config is dropped
// to make it fit, and none of its hooks runs. The production-shaped config is
// still accepted with its authentication intact.
func TestSummonPagesRefusesEveryCallbackField(t *testing.T) {
	var lock sync.Mutex
	var ran atomic.Int32
	proxy := func(*http.Request) (*url.URL, error) {
		ran.Add(1)
		lock.Lock() // the lock the keeper's own requests take
		defer lock.Unlock()
		return nil, nil
	}
	plugin, marker := summonExecMarker(t)
	exec := func() *clientcmdapi.ExecConfig {
		return &clientcmdapi.ExecConfig{APIVersion: "client.authentication.k8s.io/v1", Command: plugin,
			InteractiveMode: clientcmdapi.NeverExecInteractiveMode}
	}
	const providerValue = "SUMMONPROVIDERVALUE"
	set := map[string]func(*rest.Config){
		"Proxy":        func(c *rest.Config) { c.Proxy = proxy },
		"ExecProvider": func(c *rest.Config) { c.ExecProvider = exec() },
		"AuthProvider": func(c *rest.Config) {
			c.AuthProvider = &clientcmdapi.AuthProviderConfig{Name: "summon-test-provider", Config: map[string]string{"token": providerValue}}
		},
		"AuthConfigPersister":       func(c *rest.Config) { c.AuthConfigPersister = summonPersister{&ran} },
		"WarningHandler":            func(c *rest.Config) { c.WarningHandler = summonWarnings{&ran, &lock} },
		"WarningHandlerWithContext": func(c *rest.Config) { c.WarningHandlerWithContext = summonWarnings{&ran, &lock} },
	}
	o := Options{Namespace: "dev-env-system", LeaderElect: true,
		SummonPages: SummonPageOptions{Enabled: true, PortalOrigin: "https://portal.example", SessionNamespace: "dev-agents"}}
	cases := []struct {
		name   string
		fields []string
		words  string
	}{
		{"a proxy function that shares a lock", []string{"Proxy"}, "proxy function"},
		{"an exec credential plugin", []string{"ExecProvider"}, "exec credential plugin"},
		{"an auth provider", []string{"AuthProvider"}, "auth provider"},
		{"an auth config persister", []string{"AuthConfigPersister"}, "auth config persister"},
		{"a warning handler", []string{"WarningHandler"}, "warning handler"},
		{"a context warning handler", []string{"WarningHandlerWithContext"}, "context warning handler"},
		{"all six", []string{"Proxy", "AuthProvider", "AuthConfigPersister", "ExecProvider", "WarningHandler", "WarningHandlerWithContext"},
			"proxy function, auth provider, auth config persister, exec credential plugin, warning handler and context warning handler"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := summonProductionShapedConfig(t)
			for _, field := range tc.fields {
				set[field](cfg)
			}
			before := rest.CopyConfig(cfg)
			var logs bytes.Buffer
			if p := summonPagerFor(o, cfg, logr.FromSlogHandler(slog.NewJSONHandler(&logs, nil))); p != nil {
				t.Fatal("the pager was built on a config with a hook of its own")
			}
			want := fmt.Sprintf(`"msg":"summon pages disabled: the pager's own client could not be built","reason":"the keeper's REST config has its own %s"`, tc.words)
			if strings.Count(logs.String(), "summon pages disabled") != 1 || strings.Count(logs.String(), "\n") != 1 || !strings.Contains(logs.String(), want) {
				t.Fatalf("logs %s", logs.String())
			}
			for _, value := range []string{summonBearerCanary, cfg.Host, cfg.CAFile, plugin, "summon-test-provider", providerValue} {
				if strings.Contains(logs.String(), value) {
					t.Fatalf("the log line carries a config value: %s", logs.String())
				}
			}
			if _, err := summonPagesRESTConfig(cfg, SummonPageOptions{}); err == nil {
				t.Fatal("summonPagesRESTConfig accepted the config")
			}
			if ran.Load() != 0 {
				t.Fatalf("the keeper's own hook ran %d times", ran.Load())
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the exec credential plugin ran: %v", err)
			}
			if cfg.BearerToken != summonBearerCanary || cfg.BearerTokenFile != before.BearerTokenFile || cfg.CAFile != before.CAFile ||
				(cfg.Proxy == nil) != (before.Proxy == nil) || cfg.ExecProvider != before.ExecProvider || cfg.AuthProvider != before.AuthProvider ||
				cfg.AuthConfigPersister != before.AuthConfigPersister || cfg.WarningHandler != before.WarningHandler ||
				cfg.WarningHandlerWithContext != before.WarningHandlerWithContext {
				t.Fatal("the keeper's REST config was changed")
			}
		})
	}
	t.Run("the production-shaped config", func(t *testing.T) {
		cfg := summonProductionShapedConfig(t)
		var logs bytes.Buffer
		if p := summonPagerFor(o, cfg, logr.FromSlogHandler(slog.NewJSONHandler(&logs, nil))); p == nil {
			t.Fatalf("refused: %s", logs.String())
		}
		pc, err := summonPagesRESTConfig(cfg, SummonPageOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if pc.BearerToken != summonBearerCanary || pc.BearerTokenFile != cfg.BearerTokenFile || pc.CAFile != cfg.CAFile || pc.Host != cfg.Host {
			t.Fatal("the pager's copy dropped the config's authentication")
		}
		if pc.Proxy != nil || pc.ExecProvider != nil || pc.AuthProvider != nil || pc.AuthConfigPersister != nil ||
			pc.WarningHandler != nil || pc.WarningHandlerWithContext != nil || ran.Load() != 0 {
			t.Fatal("the pager's copy gained a hook")
		}
	})
}

type summonFieldRule string

const (
	summonPlain    summonFieldRule = "plain"
	summonRefused  summonFieldRule = "refused"
	summonReplaced summonFieldRule = "replaced"
)

// summonConfigFields is every field of rest.Config, by its path through the
// struct fields it holds (ContentConfig, ImpersonationConfig and
// TLSClientConfig), with what the pager does with a supplied value: keeps it
// (plain), refuses the config (refused), or puts its own in the pager's
// client (replaced). A new client-go field must be added here before it can
// pass.
var summonConfigFields = map[string]summonFieldRule{
	"Host":                               summonPlain,
	"APIPath":                            summonPlain,
	"ContentConfig.AcceptContentTypes":   summonPlain,
	"ContentConfig.ContentType":          summonPlain,
	"ContentConfig.GroupVersion":         summonPlain,
	"ContentConfig.NegotiatedSerializer": summonReplaced,
	"Username":                           summonPlain,
	"Password":                           summonPlain,
	"BearerToken":                        summonPlain,
	"BearerTokenFile":                    summonPlain,
	"Impersonate.UserName":               summonPlain,
	"Impersonate.UID":                    summonPlain,
	"Impersonate.Groups":                 summonPlain,
	"Impersonate.Extra":                  summonPlain,
	"AuthProvider":                       summonRefused,
	"AuthConfigPersister":                summonRefused,
	"ExecProvider":                       summonRefused,
	"TLSClientConfig.Insecure":           summonPlain,
	"TLSClientConfig.ServerName":         summonPlain,
	"TLSClientConfig.CertFile":           summonPlain,
	"TLSClientConfig.KeyFile":            summonPlain,
	"TLSClientConfig.CAFile":             summonPlain,
	"TLSClientConfig.CertData":           summonPlain,
	"TLSClientConfig.KeyData":            summonPlain,
	"TLSClientConfig.CAData":             summonPlain,
	"TLSClientConfig.NextProtos":         summonPlain,
	"UserAgent":                          summonPlain,
	"DisableCompression":                 summonPlain,
	"Transport":                          summonRefused,
	"WrapTransport":                      summonRefused,
	"QPS":                                summonReplaced,
	"Burst":                              summonPlain,
	"RateLimiter":                        summonReplaced,
	"WarningHandler":                     summonRefused,
	"WarningHandlerWithContext":          summonRefused,
	"Timeout":                            summonReplaced,
	"Dial":                               summonRefused,
	"Proxy":                              summonRefused,
}

// summonConfigLeaves walks t's fields, going into each field of struct kind,
// and calls leaf with every other field's path and index.
func summonConfigLeaves(t reflect.Type, prefix string, index []int, leaf func(path string, index []int, f reflect.StructField)) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		path := prefix + f.Name
		at := append(append([]int(nil), index...), i)
		if f.Type.Kind() == reflect.Struct {
			summonConfigLeaves(f.Type, path+".", at, leaf)
			continue
		}
		leaf(path, at, f)
	}
}

// summonCanCall reports whether a value of t can carry a callback or a shared
// object: a function, an interface, a channel, or a pointer, slice, array or
// map that holds one.
func summonCanCall(t reflect.Type, seen map[reflect.Type]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Func, reflect.Interface, reflect.Chan, reflect.UnsafePointer:
		return true
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return summonCanCall(t.Elem(), seen)
	case reflect.Map:
		return summonCanCall(t.Key(), seen) || summonCanCall(t.Elem(), seen)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if summonCanCall(t.Field(i).Type, seen) {
				return true
			}
		}
	}
	return false
}

// summonNonZero is a value of t that is not its zero value, for a plain
// field: a string, number, bool, or a slice, map or pointer of them.
func summonNonZero(t *testing.T, typ reflect.Type) reflect.Value {
	t.Helper()
	v := reflect.New(typ).Elem()
	switch typ.Kind() {
	case reflect.String:
		v.SetString("summon-field-value")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(7)
	case reflect.Slice:
		v = reflect.MakeSlice(typ, 1, 1)
		v.Index(0).Set(summonNonZero(t, typ.Elem()))
	case reflect.Map:
		v = reflect.MakeMap(typ)
		v.SetMapIndex(summonNonZero(t, typ.Key()), summonNonZero(t, typ.Elem()))
	case reflect.Pointer:
		v = reflect.New(typ.Elem())
		v.Elem().Set(summonNonZero(t, typ.Elem()))
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			v.Field(i).Set(summonNonZero(t, typ.Field(i).Type))
		}
	default:
		t.Fatalf("no plain value of kind %s", typ.Kind())
	}
	return v
}

// Ruling 2: a guard for the next client-go. Every field of rest.Config, and
// of the TLSClientConfig, ContentConfig and ImpersonationConfig it holds, is
// in the table above; a field missing from it, or a table entry for no field,
// fails, and so does a field that can carry a callback or a shared object
// (function or interface kind, or a pointer, slice or map holding one) listed
// as plain. Each entry is then held to what it says: a plain value set alone
// is kept in the pager's copy and accepted, a refused one set alone refuses
// the config with its fixed words, and a replaced one is the pager's own.
func TestSummonPagesRESTConfigFieldTable(t *testing.T) {
	var lock sync.Mutex
	var ran atomic.Int32
	plugin, marker := summonExecMarker(t)
	refuse := map[string]func(*rest.Config){
		"Transport": func(c *rest.Config) { c.Transport = &http.Transport{} },
		"WrapTransport": func(c *rest.Config) {
			c.WrapTransport = func(rt http.RoundTripper) http.RoundTripper { ran.Add(1); return rt }
		},
		"Dial":         func(c *rest.Config) { c.Dial = (&summonNoDial{&ran}).dial },
		"Proxy":        func(c *rest.Config) { c.Proxy = func(*http.Request) (*url.URL, error) { ran.Add(1); return nil, nil } },
		"AuthProvider": func(c *rest.Config) { c.AuthProvider = &clientcmdapi.AuthProviderConfig{Name: "summon-test-provider"} },
		"AuthConfigPersister": func(c *rest.Config) {
			c.AuthConfigPersister = summonPersister{&ran}
		},
		"ExecProvider": func(c *rest.Config) {
			c.ExecProvider = &clientcmdapi.ExecConfig{APIVersion: "client.authentication.k8s.io/v1", Command: plugin,
				InteractiveMode: clientcmdapi.NeverExecInteractiveMode}
		},
		"WarningHandler":            func(c *rest.Config) { c.WarningHandler = summonWarnings{&ran, &lock} },
		"WarningHandlerWithContext": func(c *rest.Config) { c.WarningHandlerWithContext = summonWarnings{&ran, &lock} },
	}
	replace := map[string]struct {
		set   func(*rest.Config)
		check func(pc *rest.Config) bool
	}{
		"Timeout": {func(c *rest.Config) { c.Timeout = time.Hour }, func(pc *rest.Config) bool { return pc.Timeout == summonPagesRequestTimeout }},
		"QPS":     {func(c *rest.Config) { c.QPS = 7 }, func(pc *rest.Config) bool { return pc.QPS < 0 }},
		"RateLimiter": {func(c *rest.Config) { c.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(1, 1) },
			func(pc *rest.Config) bool { return pc.RateLimiter == nil }},
		// The pager's discovery client and its per-kind REST clients each put
		// their own serializer in place of a supplied one: proved below
		// against the local envtest control plane.
		"ContentConfig.NegotiatedSerializer": {nil, nil},
	}

	seen := map[string]bool{}
	var plain []string
	summonConfigLeaves(reflect.TypeOf(rest.Config{}), "", nil, func(path string, index []int, f reflect.StructField) {
		seen[path] = true
		rule, ok := summonConfigFields[path]
		switch {
		case !ok:
			t.Errorf("rest.Config field %s (%s) is not in the pager's table: decide whether the pager keeps, refuses or replaces it", path, f.Type)
		case rule == summonPlain && summonCanCall(f.Type, map[reflect.Type]bool{}):
			t.Errorf("rest.Config field %s (%s) can carry a callback or a shared object and is listed as plain", path, f.Type)
		case rule == summonPlain:
			plain = append(plain, path)
		case rule == summonRefused && refuse[path] == nil:
			t.Errorf("refused field %s has no refusal case", path)
		case rule == summonReplaced:
			if _, ok := replace[path]; !ok {
				t.Errorf("replaced field %s has no check", path)
			}
		}
	})
	for path := range summonConfigFields {
		if !seen[path] {
			t.Errorf("the table names %s, which rest.Config does not have", path)
		}
	}
	for path := range summonHookWords {
		if summonConfigFields[path] != summonRefused {
			t.Errorf("hook words for %s, which the table does not refuse", path)
		}
	}

	field := func(cfg *rest.Config, path string) reflect.Value {
		v := reflect.ValueOf(cfg).Elem()
		for _, name := range strings.Split(path, ".") {
			v = v.FieldByName(name)
		}
		return v
	}
	for _, path := range plain {
		cfg := &rest.Config{}
		want := summonNonZero(t, field(cfg, path).Type())
		field(cfg, path).Set(want)
		pc, err := summonPagesRESTConfig(cfg, SummonPageOptions{})
		if err != nil {
			t.Errorf("plain field %s refused: %v", path, err)
			continue
		}
		if got := field(pc, path); !reflect.DeepEqual(got.Interface(), want.Interface()) {
			t.Errorf("plain field %s not kept in the pager's copy", path)
		}
	}
	for path, set := range refuse {
		if summonConfigFields[path] != summonRefused {
			t.Errorf("refusal case for %s, which the table does not refuse", path)
			continue
		}
		cfg := &rest.Config{Host: "https://127.0.0.1:1"}
		set(cfg)
		_, err := summonPagesRESTConfig(cfg, SummonPageOptions{})
		var hooks summonConfigHooks
		if !errors.As(err, &hooks) || hooks.Error() != "the keeper's REST config has its own "+summonHookWords[path] {
			t.Errorf("refused field %s: %v", path, err)
		}
	}
	for path, r := range replace {
		if r.set == nil {
			continue
		}
		cfg := &rest.Config{Host: "https://127.0.0.1:1"}
		r.set(cfg)
		pc, err := summonPagesRESTConfig(cfg, SummonPageOptions{})
		if err != nil || !r.check(pc) {
			t.Errorf("replaced field %s is not the pager's own: %v", path, err)
		}
	}
	if ran.Load() != 0 {
		t.Fatalf("a supplied hook ran %d times", ran.Load())
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the exec credential plugin ran: %v", err)
	}

	t.Run("a supplied serializer is replaced", func(t *testing.T) {
		var calls atomic.Int32
		counting := summonCountingSerializer{NegotiatedSerializer: clientgoscheme.Codecs.WithoutConversion(), calls: &calls}
		// The control: a REST client built on the supplied serializer uses it.
		plainCfg := rest.CopyConfig(envConfig(t))
		plainCfg.NegotiatedSerializer, plainCfg.GroupVersion, plainCfg.APIPath = counting, &corev1.SchemeGroupVersion, "/api"
		rc, err := rest.RESTClientFor(plainCfg)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var ns corev1.Namespace
		if err := rc.Get().Resource("namespaces").Name("default").Do(ctx).Into(&ns); err != nil {
			t.Fatal(err)
		}
		if calls.Load() == 0 {
			t.Fatal("the control did not use the supplied serializer: the check below proves nothing")
		}
		calls.Store(0)
		cfg := rest.CopyConfig(envConfig(t))
		cfg.NegotiatedSerializer = counting
		c, err := newSummonPagesClient(cfg, SummonPageOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var sessions v1alpha1.AgentSessionList
		if err := c.List(ctx, &sessions, client.InNamespace("default"), client.Limit(1)); err != nil {
			t.Fatal(err)
		}
		var cm corev1.ConfigMap
		if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "summon-no-such-ledger"}, &cm); err == nil {
			t.Fatal("the ledger read found a ConfigMap the test never made")
		}
		if calls.Load() != 0 {
			t.Fatalf("the pager's client used the supplied serializer %d times", calls.Load())
		}
	})
}

type summonNoDial struct{ ran *atomic.Int32 }

func (d *summonNoDial) dial(context.Context, string, string) (net.Conn, error) {
	d.ran.Add(1)
	return nil, errors.New("no dial in this test")
}

type summonCountingSerializer struct {
	runtime.NegotiatedSerializer
	calls *atomic.Int32
}

func (s summonCountingSerializer) SupportedMediaTypes() []runtime.SerializerInfo {
	s.calls.Add(1)
	return s.NegotiatedSerializer.SupportedMediaTypes()
}

func (s summonCountingSerializer) EncoderForVersion(e runtime.Encoder, gv runtime.GroupVersioner) runtime.Encoder {
	s.calls.Add(1)
	return s.NegotiatedSerializer.EncoderForVersion(e, gv)
}

func (s summonCountingSerializer) DecoderToVersion(d runtime.Decoder, gv runtime.GroupVersioner) runtime.Decoder {
	s.calls.Add(1)
	return s.NegotiatedSerializer.DecoderToVersion(d, gv)
}

// Ruling 4 (F6, MEDIUM): a dotted run cannot hide a scheme. No scheme link
// survives, in whole or in part, whatever stands before it: the re-check's
// four inputs, and a scheme after an underscore, a digit, a dotted number or
// another letter. The cases that passed before still pass.
//
// Story 782 fix round 4 (coordinator rulings in PR 1102 comment 6842728336):
// scheme links are found in the original text before any host is replaced,
// so no host match takes a scheme word and leaves its target (ruling 1); a
// scheme written with a colon only is a link unless a letter or a combining
// mark stands directly before its word (ruling 2). é.example-http://... is
// now é.[link removed]: example-http is the scheme word, found first.
func TestSummonPagesNoteSchemeAfterADottedRun(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"é.example-http://192.0.2.1", "é.[link removed]"},
		{"é.example-http://localhost", "é.[link removed]"},
		{"x.é-http://192.0.2.1", "x.é-[link removed]"},
		{"_x.y-http://192.0.2.1", "_[link removed]"},
		{"see x.é-https://localhost/a?b now", "see x.é-[link removed] now"},
		{"_http://192.0.2.1", "_[link removed]"},
		{"1http://192.0.2.1", "1[link removed]"},
		{"2.105.0-http://192.0.2.1", "2.105.0-[link removed]"},
		{"é.example-mailto:a@localhost", "[link removed]-[link removed]"},
		{"x_javascript:alert(1)", "x_[link removed]"},
		// Fix round 4, ruling 1: a host match takes no scheme word.
		{"é.http://localhost", "é.[link removed]"},
		{"é.http://192.0.2.1", "é.[link removed]"},
		{"foo。http://localhost", "foo。[link removed]"},
		{"1。http://192.0.2.1", "1。[link removed]"},
		{"é.mailto:a@localhost", "é.[link removed]"},
		{"see é.example.http://localhost now", "see é.[link removed] now"},
		{"see пример.рф.http://localhost now", "see [link removed].[link removed] now"},
		{"see é.example/http://localhost now", "see [link removed][link removed] now"},
		// Fix round 4, ruling 2: what stands before a colon-only scheme word
		// and is not a letter or a mark leaves it a link.
		{"1tel:555", "1[link removed]"},
		{"mailto:a@localhost", "[link removed]"},
		{"call tel:555 now", "call [link removed] now"},
		{"(tel:555)", "([link removed])"},
		{"profile:javascript:alert(1)", "profile:[link removed]"},
	} {
		got := summonNote(tc.in)
		if got != tc.want {
			t.Errorf("summonNote(%q) = %q, want %q", tc.in, got, tc.want)
		}
		for _, left := range []string{"://", "http", "192.0.2.1", "localhost", "mailto", "javascript", "tel:", "555", "alert"} {
			if strings.Contains(strings.ToLower(got), left) {
				t.Errorf("summonNote(%q) = %q keeps %q", tc.in, got, left)
			}
		}
	}
	for _, tc := range []struct{ in, want string }{
		{"release 2.105.0 is out", "release 2.105.0 is out"},
		{"see a.example-b now", "see [link removed]-b now"},
		{"see a_b.example now", "see a_[link removed] now"},
		{"see a.example.123 now", "see [link removed].123 now"},
		{"v2.105.0-rc.1 and go1.27.3", "v2.105.0-rc.1 and go1.27.3"},
		{"e.g. this", "e.g. this"},
		// Fix round 4, ruling 2: a scheme word glued to a letter or a mark of
		// any script is no link, and the note keeps it whole.
		{"profile:ops must approve", "profile:ops must approve"},
		{"retry metadata:x", "retry metadata:x"},
		{"hotel:5", "hotel:5"},
		{"éfile:x", "éfile:x"},
		{"Ωmailto:x пtel:5 日sms:1 ñjavascript:x", "Ωmailto:x пtel:5 日sms:1 ñjavascript:x"},
		{"e\u0301data:x", "e\u0301data:x"},
		{"xMAILTO:x", "xMAILTO:x"},
		// A word kept whole hides no scheme link after it.
		{"hotel:http://localhost", "hotel:[link removed]"},
		{"éfile:mailto:a@localhost", "éfile:[link removed]"},
	} {
		if got := summonNote(tc.in); got != tc.want {
			t.Errorf("summonNote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Ruling 5 (F6, MEDIUM): U+3002, U+FF0E and U+FF61 count as the dot between
// labels wherever the ASCII dot does, as the IDNA lookup maps them to it. A
// dotted number written with them stays.
func TestSummonPagesNoteOtherDots(t *testing.T) {
	for _, dot := range []string{"。", "．", "｡"} {
		for _, tc := range []struct{ in, want string }{
			{"see evil" + dot + "example now", "see [link removed] now"},
			{"see evil" + dot + "example:443 now", "see [link removed] now"},
			{"see evil" + dot + "example:443/x now", "see [link removed] now"},
			{"see EVIL" + dot + "Example" + dot, "see [link removed]" + dot},
			{"see evil" + dot + "xn--p1ai now", "see [link removed] now"},
			{"see a" + dot + "example-b now", "see [link removed]-b now"},
			{"see a" + dot + "example" + dot + "123 now", "see [link removed]" + dot + "123 now"},
			{"see www" + dot + "x1 now", "see [link removed] now"},
			{"host 10" + dot + "0" + dot + "0" + dot + "1:8080, then", "host [link removed], then"},
			{"release 2" + dot + "105" + dot + "0 is out", "release 2" + dot + "105" + dot + "0 is out"},
			{"mixed evil" + dot + "example.org", "mixed [link removed]"},
		} {
			if got := summonNote(tc.in); got != tc.want {
				t.Errorf("summonNote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	}
}

// Rulings 4 and 5 in the fake endpoint's final message, and fix round 4's
// rulings 1 and 2: no scheme link's delimiter or target is left, and a scheme
// word glued to a letter stays whole.
func TestSummonPagesNoteSchemesAndDotsInTheMessage(t *testing.T) {
	f := newSummonFix(t)
	const schemes = "runs é.example-http://192.0.2.1 é.example-http://localhost x.é-http://192.0.2.1 _x.y-http://192.0.2.1 _http://192.0.2.1; 2.105.0 stays"
	const schemesShown = "runs é.[link removed] é.[link removed] x.é-[link removed] _[link removed] _[link removed]; 2.105.0 stays"
	const dots = "dots evil。example evil．example evil｡example evil。example:443 evil．example:443 evil｡example:443; 2。105．0 stays"
	const dotsShown = "dots [link removed] [link removed] [link removed] [link removed] [link removed] [link removed]; 2。105．0 stays"
	const first = "first é.http://localhost é.http://192.0.2.1 foo。http://localhost 1。http://192.0.2.1 é.mailto:a@localhost x_javascript:alert(1) 1tel:555"
	const firstShown = "first é.[link removed] é.[link removed] foo。[link removed] 1。[link removed] é.[link removed] x_[link removed] 1[link removed]"
	const kept = "kept profile:ops must approve, metadata:x, hotel:5, éfile:x"
	for _, s := range []string{schemesShown, dotsShown, firstShown, kept} {
		if n := len([]rune(s)); n > summonNoteMax {
			t.Fatalf("the expected note is %d characters: the cut would hide a case", n)
		}
	}
	f.add(f.close(f.session("wo-upgrade-1", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", schemes),
		f.close(f.session("wo-upgrade-2", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", dots),
		f.close(f.session("wo-upgrade-3", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", first),
		f.close(f.session("wo-upgrade-4", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", kept))
	f.tick()
	if f.push.count() != 4 {
		t.Fatalf("sent %d", f.push.count())
	}
	shown := map[string]string{}
	for i := 0; i < 4; i++ {
		msg := f.push.form(i).Get("message")
		for _, left := range []string{"://", "http", "192.0.2.1", "localhost", "evil", "example"} {
			if strings.Contains(msg, left) {
				t.Fatalf("the message keeps %q: %q", left, msg)
			}
		}
		_, note, ok := strings.Cut(msg, "\nsession note: ")
		if !ok {
			t.Fatalf("no note in %q", msg)
		}
		for _, name := range []string{"wo-upgrade-1", "wo-upgrade-2", "wo-upgrade-3", "wo-upgrade-4"} {
			if strings.Contains(msg, "session "+name+" ") {
				shown[name] = note
			}
		}
	}
	if shown["wo-upgrade-1"] != schemesShown {
		t.Fatalf("note %q, want %q", shown["wo-upgrade-1"], schemesShown)
	}
	if shown["wo-upgrade-2"] != dotsShown {
		t.Fatalf("note %q, want %q", shown["wo-upgrade-2"], dotsShown)
	}
	for _, left := range []string{"mailto", "javascript", "tel:", "555", "alert", "a@"} {
		if strings.Contains(shown["wo-upgrade-3"], left) {
			t.Fatalf("the note keeps %q: %q", left, shown["wo-upgrade-3"])
		}
	}
	if shown["wo-upgrade-3"] != firstShown {
		t.Fatalf("note %q, want %q", shown["wo-upgrade-3"], firstShown)
	}
	if shown["wo-upgrade-4"] != kept {
		t.Fatalf("note %q, want %q", shown["wo-upgrade-4"], kept)
	}
}

// Ruling 6: while a read by name fails, proof pruning stops. Each tick whose
// proof read fails logs one line in fixed words, never the session's name;
// nothing is pruned without proof; once reads answer, pruning goes on and the
// line stops.
func TestSummonPagesFailedProofReadIsLogged(t *testing.T) {
	f := newSummonFix(t)
	l := f.ledger()
	old := f.clock.Now().Add(-3 * time.Hour).Unix()
	goneKey := func(i int) string { return fmt.Sprintf("00000000-0000-4000-9000-%012d/failed", i) }
	for i := 0; i < 3; i++ {
		l.State.Events[goneKey(i)] = summonPageEntry{At: old + int64(i), State: "sent", Session: fmt.Sprintf("wo-gone-%04d", i)}
	}
	if err := (&configMapSummonPageStore{client: f.c, name: summonLedgerName}).save(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	const line = `"msg":"summon page proof read failed: pruning waits for the next tick"`
	f.sessionGet = func(string) error { return errors.New("read refused by the test") }
	for n := 1; n <= 2; n++ {
		f.clock.Step(time.Minute)
		f.add(f.failedSession(fmt.Sprintf("wo-new-%d", n), time.Minute))
		f.tick()
		if got := strings.Count(f.logs.String(), line); got != n {
			t.Fatalf("tick %d: the line is logged %d times: %s", n, got, f.logs.String())
		}
		if f.sentFor(fmt.Sprintf("wo-new-%d", n)) != 1 {
			t.Fatalf("tick %d: pages %v", n, f.push.titles())
		}
		for i := 0; i < 3; i++ {
			if _, ok := f.ledger().State.Events[goneKey(i)]; !ok {
				t.Fatalf("tick %d: an entry was pruned without proof", n)
			}
		}
	}
	if strings.Contains(f.logs.String(), "wo-gone-") {
		t.Fatalf("the log names a session: %s", f.logs.String())
	}
	f.sessionGet = nil
	f.clock.Step(time.Minute)
	f.add(f.failedSession("wo-new-3", time.Minute))
	f.tick()
	if got := strings.Count(f.logs.String(), line); got != 2 {
		t.Fatalf("the line is logged %d times after reads answer", got)
	}
	for i := 0; i < 3; i++ {
		if _, ok := f.ledger().State.Events[goneKey(i)]; ok {
			t.Fatal("an entry proved gone was kept")
		}
	}
}
