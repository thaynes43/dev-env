// Package apiserver is the operator's /v1 API (DESIGN-001 3.4, D-46): HTTPS
// with a cert-manager certificate, JSON, every call authenticated by a
// TokenReview for the audience dev-env-operator (D-05) and authorized by the
// caller's class (Policy). It serves:
//
//	POST   /v1/sessions                    create a task session
//	GET    /v1/sessions                    list, with filters
//	GET    /v1/sessions/{name}             one session
//	DELETE /v1/sessions/{name}             reap: rescue, suspend, archive (D-45)
//	POST   /v1/sessions/{name}/heartbeat   agentd's status (D-41), the session's own pod only
//	GET    /v1/fleet                       what runs and waits, and on which revision
//	POST   /v1/grants                      request an access grant, sessions only (D-56)
//	GET    /v1/grants                      list, with filters
//	GET    /v1/grants/{name}               one grant
//	DELETE /v1/grants/{name}               release: set spec.release for the broker
//
// The API writes AgentSession objects and their status, and AccessGrant specs
// (never their status: the broker decides a grant); it never touches a pod or a
// volume. Agents have no write on AgentSessions (DESIGN-001 6.11), so every
// session they start comes through here, where its parent is taken from the
// token. Every replica serves the API; only the leader reconciles.
package apiserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskbudget"
	"github.com/thaynes43/dev-env/internal/templates"
)

// Server holds the API's handlers and what they read and write.
type Server struct {
	// Nil keeps the first durable ledger unit inactive. Runtime activation also
	// needs protected storage, independently validated receipts and owned stop.
	TaskBudgets         *taskbudget.Service
	AssignedTaskBudgets map[string]string // configured HostID -> logical TaskUID
	// Projects is a concrete uncached named ConfigMap binding, never a caller-supplied resolver.
	Projects *CatalogBinding
	// PrivateProjectTasks admits new catalog tasks into private repositories.
	// It is independent of the provider and shared-workspace feature gates.
	PrivateProjectTasks   bool
	ManagedCodexTasks     bool
	ManagedChildDecisions bool
	// Client reads from the manager's cache and writes to the API server.
	Client client.Client
	// Live reads straight from the API server (the manager's APIReader). A
	// create checks idempotency keys and running children with it, because the
	// cache can lag a create made a moment before.
	Live client.Reader
	// Auth authenticates bearer tokens.
	Auth Authenticator
	// Exec runs agentd's commands in a session's pod, for the log and message
	// routes (D-08, D-65). Nil answers those routes with 503.
	Exec PodExecutor
	// Policy names the callers the API serves.
	Policy Policy
	// Shelf lists the rescues on the shared volume, for GET /v1/rescues and a
	// restore (D-67). Nil answers both with 503.
	Shelf RescueShelf
	// Templates returns the current templates, or why they are unusable. Nil
	// skips the profile check and leaves the fleet's revision unknown.
	Templates func(context.Context) (*templates.Templates, error)
	// Log gets one line per request: never a token or a body.
	Log logr.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// GrantApprovalURL is the base URL of the broker's approval page; a pending
	// grant's view links to it plus the grant's name. Empty links nothing.
	GrantApprovalURL string

	// createMu serialises this replica's creates, so its idempotency,
	// child-count, grant-merge and pending-grant checks see each other's writes.
	createMu sync.Mutex
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// handler serves one route for one method. It returns the status and the body
// to send, or an error, which is a refusal when it is an *apiError and a 500
// otherwise.
type handler func(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error)

// route is a path's handlers by method.
type route struct {
	methods map[string]handler
	// quiet logs the route at debug level: heartbeats come every minute.
	quiet       bool
	coordinator bool
}

// Handler returns the API's HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(apiv1.TaskBudgetsPath, s.serve(route{coordinator: true, methods: map[string]handler{http.MethodPost: s.createTaskBudget}}))
	mux.Handle(apiv1.TaskBudgetsPath+"/{uid}", s.serve(route{coordinator: true, methods: map[string]handler{http.MethodGet: s.getTaskBudget}}))
	mux.Handle(apiv1.TaskBudgetsPath+"/{uid}/admit", s.serve(route{coordinator: true, methods: map[string]handler{http.MethodPost: s.admitTaskBudget}}))
	mux.Handle(apiv1.TaskBudgetsPath+"/{uid}/observe", s.serve(route{coordinator: true, methods: map[string]handler{http.MethodPost: s.observeTaskBudget}}))
	mux.Handle(apiv1.TaskBudgetsPath+"/{uid}/events", s.serve(route{coordinator: true, methods: map[string]handler{http.MethodPost: s.recordTaskBudgetEvent}}))
	mux.Handle(apiv1.TaskBudgetsPath+"/{uid}/extend", s.serve(route{coordinator: true, methods: map[string]handler{http.MethodPost: s.extendTaskBudget}}))
	mux.Handle(apiv1.SessionsPath, s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodGet:  s.listSessions,
		http.MethodPost: s.createSession,
	}}))
	mux.Handle(apiv1.SessionsPath+"/{name}", s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodGet:    s.getSession,
		http.MethodDelete: s.reapSession,
	}}))
	mux.Handle(apiv1.SessionsPath+"/{name}/log", s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodGet: s.sessionLog,
	}}))
	mux.Handle(apiv1.SessionsPath+"/{name}/messages", s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodPost: s.sendMessage,
	}}))
	mux.Handle(apiv1.SessionsPath+"/{name}/decision", s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodGet:  s.readChildDecision,
		http.MethodPost: s.answerChildDecision,
	}}))
	mux.Handle(apiv1.SessionsPath+"/{name}/suspend", s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodPost: s.suspendSession,
	}}))
	mux.Handle(apiv1.SessionsPath+"/{name}/resume", s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodPost: s.resumeSession,
	}}))
	mux.Handle(apiv1.SessionsPath+"/{name}/decision-authority", s.serve(route{methods: map[string]handler{http.MethodGet: s.ownDecisionAuthority}}))
	mux.Handle(apiv1.SessionsPath+"/{name}/heartbeat", s.serve(route{quiet: true, methods: map[string]handler{
		http.MethodPost: s.heartbeat,
	}}))
	mux.Handle(apiv1.GrantsPath, s.serve(route{methods: map[string]handler{
		http.MethodGet:  s.listGrants,
		http.MethodPost: s.createGrant,
	}}))
	mux.Handle(apiv1.GrantsPath+"/{name}", s.serve(route{methods: map[string]handler{
		http.MethodGet:    s.getGrant,
		http.MethodDelete: s.releaseGrant,
	}}))
	mux.Handle(apiv1.ActivitiesPath, s.serve(route{methods: map[string]handler{
		http.MethodGet:  s.listActivities,
		http.MethodPost: s.declareActivity,
	}}))
	mux.Handle(apiv1.ActivitiesPath+"/{name}", s.serve(route{methods: map[string]handler{
		http.MethodDelete: s.endActivity,
	}}))
	mux.Handle(apiv1.RescuesPath, s.serve(route{methods: map[string]handler{
		http.MethodGet: s.listRescues,
	}}))
	mux.Handle(apiv1.FleetPath, s.serve(route{methods: map[string]handler{
		http.MethodGet: s.fleet,
	}}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, notFound("no route %s %s", r.Method, r.URL.Path))
	})
	return mux
}

// serve authenticates, classifies and dispatches a request, writes the answer
// and logs one line.
func (s *Server) serve(rt route) http.Handler {
	allowed := make([]string, 0, len(rt.methods))
	for m := range rt.methods {
		allowed = append(allowed, m)
	}
	slices.Sort(allowed)
	allow := strings.Join(allowed, ", ")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		var c *caller
		status, err := func() (int, error) {
			h, ok := rt.methods[r.Method]
			if !ok {
				w.Header().Set("Allow", allow)
				return http.StatusMethodNotAllowed, newError(http.StatusMethodNotAllowed, apiv1.CodeMethodNotAllowed, "%s takes %s", r.URL.Path, allow)
			}
			tok, err := bearerToken(r)
			if err != nil {
				return 0, err
			}
			id, err := s.Auth.Authenticate(r.Context(), tok)
			if err != nil {
				return 0, err
			}
			if c, err = s.resolveCaller(r.Context(), id); err != nil {
				return 0, err
			}
			if err := coordinatorAllowed(c, rt.coordinator); err != nil {
				return 0, err
			}
			status, body, err := h(r.Context(), w, r, c)
			if err != nil {
				return 0, err
			}
			if body == nil {
				w.WriteHeader(status)
			} else {
				writeJSON(w, status, body)
			}
			return status, nil
		}()
		if err != nil {
			var ae *apiError
			if !errors.As(err, &ae) {
				s.Log.Error(err, "request failed", "method", r.Method, "path", coordinatorLogPath(r, c))
				ae = internal("the operator failed this request; its log has the cause")
			}
			status = ae.status
			writeError(w, ae)
		}
		who := "unauthenticated"
		if c != nil {
			who = c.String()
		}
		lg := s.Log
		if rt.quiet && err == nil {
			lg = lg.V(1)
		}
		lg.Info("request", "method", r.Method, "path", coordinatorLogPath(r, c), "status", status, "caller", who,
			"durationMs", s.now().Sub(start).Milliseconds())
	})
}

// TemplatesFrom returns a Templates function that reads the templates ConfigMap
// through c (the manager's cache) and parses it.
func TemplatesFrom(c client.Reader, key types.NamespacedName) func(context.Context) (*templates.Templates, error) {
	return func(ctx context.Context) (*templates.Templates, error) {
		var cm corev1.ConfigMap
		if err := c.Get(ctx, key, &cm); err != nil {
			return nil, fmt.Errorf("templates %s: %w", key, err)
		}
		return templates.Parse(cm.Data)
	}
}

// Runner serves the API over HTTPS as a manager runnable on every replica
// (NeedLeaderElection is false). The certificate is cert-manager's, mounted from
// its Secret as tls.crt and tls.key; a renewal is picked up without a restart.
type Runner struct {
	// Addr is the listen address, for example :8443 (DESIGN-001 8.4).
	Addr string
	// CertDir holds tls.crt and tls.key.
	CertDir string
	// Handler is the Server's Handler.
	Handler http.Handler
	// ErrorLog gets the HTTP server's own errors, such as a failed handshake.
	ErrorLog *log.Logger

	ready atomic.Bool
	bound atomic.Value // string: the bound address, once listening
}

// NeedLeaderElection is false: every replica serves the API.
func (r *Runner) NeedLeaderElection() bool { return false }

// ReadyCheck is a readyz check: ready once the API listens.
func (r *Runner) ReadyCheck(_ *http.Request) error {
	if !r.ready.Load() {
		return errors.New("the /v1 API is not listening yet")
	}
	return nil
}

// boundAddr is the address the API listens on, once it does.
func (r *Runner) boundAddr() string {
	if v, ok := r.bound.Load().(string); ok {
		return v
	}
	return ""
}

// Start serves until ctx ends, then drains for up to ten seconds. A certificate
// watcher that fails stops the API with its error, so the pod restarts rather
// than serve a certificate that no longer renews.
func (r *Runner) Start(ctx context.Context) error {
	cw, err := certwatcher.New(filepath.Join(r.CertDir, "tls.crt"), filepath.Join(r.CertDir, "tls.key"))
	if err != nil {
		return fmt.Errorf("the /v1 API's certificate: %w", err)
	}
	ln, err := net.Listen("tcp", r.Addr)
	if err != nil {
		return fmt.Errorf("the /v1 API: %w", err)
	}
	srv := &http.Server{
		Handler: r.Handler,
		TLSConfig: &tls.Config{
			MinVersion:     tls.VersionTLS13,
			GetCertificate: cw.GetCertificate,
		},
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          r.ErrorLog,
		// Requests do not inherit ctx: at shutdown, Shutdown lets the ones in
		// flight finish rather than cancel them halfway through a write.
	}

	wctx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	watchErr := make(chan error, 1)
	go func() { watchErr <- cw.Start(wctx) }()

	var failure error
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
		case err := <-watchErr:
			if err == nil {
				err = errors.New("stopped")
			}
			failure = fmt.Errorf("the /v1 API's certificate watcher: %w", err)
		}
		r.ready.Store(false)
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	r.bound.Store(ln.Addr().String())
	r.ready.Store(true)
	err = srv.ServeTLS(ln, "", "")
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-done
	return failure
}
