package session

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

type AssemblyConfig struct {
	Session    *Engine
	Authorizer portal.Authorizer
	Reader     portal.MetadataReader
	FreshFor   time.Duration
	Now        func() time.Time
}

// NewAssembly composes source handlers; it opens no listener. Missing dependencies
// always yield an unavailable handler, never a default mux or fixture success.
// Live wiring must use this containment boundary, current shared storage and
// independently reviewed per-user authorization/summary adapters.
func NewAssembly(c AssemblyConfig) http.Handler {
	if c.Session == nil || c.Authorizer == nil || c.Reader == nil || c.FreshFor <= 0 || c.Now == nil {
		return contained(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { authHeaders(w); authFailure(w, portal.ErrUnavailable) }))
	}
	reads := portal.NewHandler(portal.Dependencies{Authenticator: c.Session, Authorizer: c.Authorizer, Reader: c.Reader, FreshFor: c.FreshFor, Now: c.Now})
	return contained(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/auth/") {
			c.Session.ServeHTTP(w, r)
			return
		}
		reads.ServeHTTP(w, c.Session.bind(r))
	}))
}

// NewRuntimeAssembly is the unconfigured process. It stays unavailable.
// The executable uses LoadProduction. A static generation or restored database is no witness.
func NewRuntimeAssembly() http.Handler { return NewAssembly(AssemblyConfig{}) }

// The body cap covers the read boundary's 24 bounded text rows. No data or
// cookies escape before successful completion; panic values are never logged.
const maximumResponse = 1024 * 1024

var errResponseBound = errors.New("portal response unavailable")

type responseBuffer struct {
	header http.Header
	code   int
	body   bytes.Buffer
	failed bool
}

func (b *responseBuffer) Header() http.Header { return b.header }
func (b *responseBuffer) WriteHeader(code int) {
	if code < 200 || code > 599 {
		b.failed = true
		return
	}
	if b.code == 0 {
		b.code = code
	}
}
func (b *responseBuffer) Write(data []byte) (int, error) {
	if b.failed || len(data) > maximumResponse-b.body.Len() {
		b.failed = true
		return 0, errResponseBound
	}
	if b.code == 0 {
		b.code = http.StatusOK
	}
	return b.body.Write(data)
}

func contained(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buffer := &responseBuffer{header: make(http.Header)}
		completed := false
		func() {
			defer func() {
				if recover() != nil {
					buffer.failed = true
				}
			}()
			next.ServeHTTP(buffer, r)
			completed = true
		}()
		if !completed || buffer.failed || r.Context().Err() != nil {
			authHeaders(w)
			authFailure(w, portal.ErrUnavailable)
			return
		}
		for name, values := range buffer.header {
			w.Header()[name] = append([]string(nil), values...)
		}
		code := buffer.code
		if code == 0 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
		_, _ = w.Write(buffer.body.Bytes())
	})
}
