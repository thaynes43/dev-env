package apiserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// The grant routes (D-56). A session asks for a grant for its own pod; the
// broker decides it, makes it and ends it. The API writes AccessGrant specs
// only: it has no status write, so it can neither approve nor end a grant. It
// reads grants from the API server, not the manager's cache: a session polls its
// grant right after asking, and the operator keeps no AccessGrant informer.

// maxGrantBody bounds a grant request: a reason of at most 1000 characters and
// a few short lists.
const maxGrantBody = 64 << 10

// maxPendingGrants is how many grants a session may have waiting at a time
// (DESIGN-001 6.12).
const maxPendingGrants = 3

// createGrant serves POST /v1/grants.
func (s *Server) createGrant(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if c.kind != kindSession {
		return 0, nil, forbidden("grants are requested by sessions, for their own pod, and approved by Tom on the broker's page; %s is not a session", c)
	}
	if !c.session.DeletionTimestamp.IsZero() {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "session %s is being reaped; its grants end with it", c.parent)
	}
	var req apiv1.CreateGrantRequest
	if err := decodeJSON(w, r, maxGrantBody, &req, true); err != nil {
		return 0, nil, err
	}
	g, err := s.newGrant(req, c)
	if err != nil {
		return 0, nil, err
	}

	s.createMu.Lock()
	defer s.createMu.Unlock()

	mine, err := s.sessionGrants(ctx, c.parent)
	if err != nil {
		return 0, nil, err
	}
	// An identical request comes first: asking again for what the session
	// already waits for or holds returns that grant, even at the limit.
	key := scopeKey(&g.Spec)
	now := s.now()
	for i := range mine {
		if mergeable(&mine[i], now) && scopeKey(&mine[i].Spec) == key {
			s.Log.Info("grant request merged", "grant", mine[i].Name, "caller", c.String())
			w.Header().Set("Location", apiv1.GrantPath(mine[i].Name))
			return http.StatusOK, s.grantView(&mine[i]), nil
		}
	}
	n := 0
	for i := range mine {
		if pending(&mine[i]) {
			n++
		}
	}
	if n >= maxPendingGrants {
		return 0, nil, newError(http.StatusTooManyRequests, apiv1.CodeLimitExceeded,
			"session %s already has %d grants waiting, the most at a time (DESIGN-001 6.12); wait for an answer or release one", c.parent, n)
	}

	for i := 0; i < nameAttempts; i++ {
		name, err := grantName(now)
		if err != nil {
			return 0, nil, err
		}
		g.Name = name
		err = s.Client.Create(ctx, g)
		if apierrors.IsAlreadyExists(err) {
			g.ResourceVersion = ""
			continue
		}
		if err != nil {
			return 0, nil, fromGrantKubeError(err, "create grant")
		}
		role, cred := "", ""
		if g.Spec.Kube != nil {
			role = g.Spec.Kube.Role
		}
		if g.Spec.Credential != nil {
			cred = string(g.Spec.Credential.Name)
		}
		s.Log.Info("grant requested", "grant", g.Name, "caller", c.String(), "type", g.Spec.Type,
			"role", role, "credential", cred, "ttl", g.Spec.TTL.Duration.String())
		w.Header().Set("Location", apiv1.GrantPath(g.Name))
		return http.StatusCreated, s.grantView(g), nil
	}
	return 0, nil, internal("no free grant name after %d tries", nameAttempts)
}

// newGrant builds the AccessGrant a request asks for, or refuses it. It checks
// only what the schema cannot (D-46's split): the type, the TTL's form, the
// fields each type needs, and no field of another type, which the API would
// otherwise drop. The schema's rules (D-54: the dev-env namespaces, TTL bounds,
// catalog roles, CIDR form) run at create and come back as 422 fields.
func (s *Server) newGrant(req apiv1.CreateGrantRequest, c *caller) (*v1alpha1.AccessGrant, error) {
	var fields []apiv1.FieldError
	add := func(field, format string, args ...any) { fields = append(fields, fieldError(field, format, args...)) }
	notFor := func(kind string, set map[string]bool) {
		for _, f := range []string{"role", "namespaces", "fqdns", "cidrs", "endpoints", "ports", "credential"} {
			if set[f] {
				add(f, "not for type %s", kind)
			}
		}
	}
	set := map[string]bool{
		"role":       req.Role != "",
		"namespaces": len(req.Namespaces) > 0,
		"fqdns":      len(req.FQDNs) > 0,
		"cidrs":      len(req.CIDRs) > 0,
		"endpoints":  len(req.Endpoints) > 0,
		"ports":      len(req.Ports) > 0,
		"credential": req.Credential != "",
	}
	others := func(keep ...string) map[string]bool {
		out := map[string]bool{}
		for k, v := range set {
			out[k] = v && !slices.Contains(keep, k)
		}
		return out
	}

	typ := v1alpha1.GrantType(req.Type)
	spec := v1alpha1.AccessGrantSpec{
		Requester: v1alpha1.GrantRequester{
			Session: c.parent,
			Repo:    c.session.Spec.Repo,
			Profile: c.profile,
			Agent:   c.session.Spec.Agent,
			Parent:  c.session.Spec.Parent,
		},
		Type:   typ,
		Reason: req.Reason,
	}
	ttl := apiv1.DefaultGrantTTL
	switch typ {
	case v1alpha1.GrantKube:
		if req.Role == "" {
			add("role", "required for type kube: a role from the grant catalog, such as %s", v1alpha1.RoleWorkloads)
		}
		spec.Kube = &v1alpha1.KubeGrant{Role: req.Role, Namespaces: req.Namespaces}
		notFor(req.Type, others("role", "namespaces"))
	case v1alpha1.GrantBreakglass:
		if req.Role != "" && req.Role != v1alpha1.RoleBreakglass {
			add("role", "break-glass is always %s (D-27); leave role empty", v1alpha1.RoleBreakglass)
		}
		ttl = apiv1.DefaultBreakglassTTL
		spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleBreakglass, Namespaces: req.Namespaces}
		notFor(req.Type, others("role", "namespaces"))
	case v1alpha1.GrantEgress:
		if len(req.FQDNs)+len(req.CIDRs)+len(req.Endpoints) == 0 {
			add("", "name at least one destination: fqdns, cidrs or endpoints")
		}
		if len(req.Ports) == 0 {
			add("ports", "required for type egress: at least one port")
		}
		e := &v1alpha1.EgressGrant{FQDNs: req.FQDNs, CIDRs: req.CIDRs}
		for _, ep := range req.Endpoints {
			out := v1alpha1.EgressEndpoint{Namespace: ep.Namespace}
			if len(ep.MatchLabels) > 0 {
				out.MatchLabels = make(map[string]v1alpha1.LabelValue, len(ep.MatchLabels))
				for k, v := range ep.MatchLabels {
					out.MatchLabels[k] = v1alpha1.LabelValue(v)
				}
			}
			e.Endpoints = append(e.Endpoints, out)
		}
		for _, p := range req.Ports {
			// The schema defaults the protocol to TCP; the API sets it too, so
			// the request it stores is the one it compares (scopeKey).
			proto := v1alpha1.GrantProtocol(p.Protocol)
			if proto == "" {
				proto = v1alpha1.ProtocolTCP
			}
			e.Ports = append(e.Ports, v1alpha1.GrantPort{Port: p.Port, Protocol: proto})
		}
		spec.Egress = e
		notFor(req.Type, others("fqdns", "cidrs", "endpoints", "ports"))
	case v1alpha1.GrantCredential:
		if req.Credential == "" {
			add("credential", "required for type credential: %s or %s", v1alpha1.CredentialProxmox, v1alpha1.CredentialHWSSH)
		}
		spec.Credential = &v1alpha1.CredentialGrant{Name: v1alpha1.CredentialName(req.Credential)}
		notFor(req.Type, others("credential"))
	case "":
		add("type", "required: kube, egress, credential or breakglass")
	default:
		add("type", "%q is not kube, egress, credential or breakglass", req.Type)
	}
	if req.TTL != "" {
		d, err := time.ParseDuration(req.TTL)
		if err != nil {
			add("ttl", "%q is not a Go duration such as 45m", req.TTL)
		} else {
			ttl = d
		}
	}
	spec.TTL = metav1.Duration{Duration: ttl}
	if strings.TrimSpace(req.Reason) == "" {
		add("reason", "required: why the session asks, in a sentence; Tom reads it on the approval page")
	}
	if len(fields) > 0 {
		return nil, invalid(fields...)
	}
	return &v1alpha1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: s.Policy.SessionNamespace,
			Labels:    map[string]string{v1alpha1.LabelSession: c.parent},
		},
		Spec: spec,
	}, nil
}

// grantName is grant-<MMDD>-<HHMMSS>-<4 hex> in UTC (D-54): the grant, its
// ServiceAccount, bindings and network policy share it.
func grantName(now time.Time) (string, error) {
	b := make([]byte, 2)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("a grant name's random part: %w", err)
	}
	return "grant-" + now.UTC().Format("0102-150405") + "-" + hex.EncodeToString(b), nil
}

// sessionGrants lists a session's grants from the API server, so a request a
// moment ago counts.
func (s *Server) sessionGrants(ctx context.Context, session string) ([]v1alpha1.AccessGrant, error) {
	var list v1alpha1.AccessGrantList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace),
		client.MatchingLabels{v1alpha1.LabelSession: session}); err != nil {
		return nil, fromGrantKubeError(err, "list grants")
	}
	out := list.Items[:0]
	for _, g := range list.Items {
		if g.Spec.Requester.Session == session && g.DeletionTimestamp.IsZero() {
			out = append(out, g)
		}
	}
	return out, nil
}

// pending is a grant that waits for an answer: no phase yet or Pending, and not
// released. A session may have three (DESIGN-001 6.12).
func pending(g *v1alpha1.AccessGrant) bool {
	return !g.Spec.Release && (g.Status.Phase == "" || g.Status.Phase == v1alpha1.GrantPending)
}

// mergeable is a grant an identical request merges into: pending, or active and
// not yet past its expiry, and not released.
func mergeable(g *v1alpha1.AccessGrant, now time.Time) bool {
	if pending(g) {
		return true
	}
	if g.Spec.Release || g.Status.Phase != v1alpha1.GrantActive {
		return false
	}
	return g.Status.ExpiresAt == nil || g.Status.ExpiresAt.After(now)
}

// grantScope is what a grant asks for, each list sorted and without repeats:
// two grants ask for the same thing when their keys are equal. TTL and reason
// take no part.
type grantScope struct {
	Type       string   `json:"type"`
	Role       string   `json:"role,omitempty"`
	Namespaces []string `json:"namespaces,omitempty"`
	FQDNs      []string `json:"fqdns,omitempty"`
	CIDRs      []string `json:"cidrs,omitempty"`
	Endpoints  []string `json:"endpoints,omitempty"`
	Ports      []string `json:"ports,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// scopeKey is a grant's scope as a string that compares lists as sets.
func scopeKey(spec *v1alpha1.AccessGrantSpec) string {
	k := grantScope{Type: string(spec.Type)}
	if kg := spec.Kube; kg != nil {
		k.Role, k.Namespaces = kg.Role, sortedSet(kg.Namespaces)
	}
	if e := spec.Egress; e != nil {
		k.FQDNs, k.CIDRs = sortedSet(e.FQDNs), sortedSet(e.CIDRs)
		eps := make([]string, 0, len(e.Endpoints))
		for _, ep := range e.Endpoints {
			// Map keys marshal sorted, and nil and empty labels both omit.
			b, _ := json.Marshal(ep)
			eps = append(eps, string(b))
		}
		k.Endpoints = sortedSet(eps)
		ports := make([]string, 0, len(e.Ports))
		for _, p := range e.Ports {
			proto := p.Protocol
			if proto == "" {
				proto = v1alpha1.ProtocolTCP
			}
			ports = append(ports, fmt.Sprintf("%d/%s", p.Port, proto))
		}
		k.Ports = sortedSet(ports)
	}
	if cg := spec.Credential; cg != nil {
		k.Credential = string(cg.Name)
	}
	b, _ := json.Marshal(k)
	return string(b)
}

func sortedSet(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

// listGrants serves GET /v1/grants: every grant, newest first, unless a filter
// narrows it. Agents can read AccessGrants anyway, and `agent-run grant list
// --all` shows each requester, approver and time.
func (s *Server) listGrants(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	f, err := parseGrantFilters(r.URL.Query())
	if err != nil {
		return 0, nil, err
	}
	var list v1alpha1.AccessGrantList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace)); err != nil {
		return 0, nil, fromGrantKubeError(err, "list grants")
	}
	items := list.Items
	slices.SortFunc(items, func(a, b v1alpha1.AccessGrant) int {
		if c := b.CreationTimestamp.Compare(a.CreationTimestamp.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	out := apiv1.GrantList{Items: []apiv1.Grant{}}
	for i := range items {
		if f.match(&items[i], c) {
			out.Items = append(out.Items, s.grantView(&items[i]))
		}
	}
	return http.StatusOK, out, nil
}

// grantFilters are a grant list's query parameters.
type grantFilters struct {
	session, phase string
	mine           bool
}

var grantPhases = []string{apiv1.GrantPending, apiv1.GrantActive, apiv1.GrantDenied, apiv1.GrantExpired, apiv1.GrantReleased, apiv1.GrantFailed}

func parseGrantFilters(q url.Values) (grantFilters, error) {
	var f grantFilters
	for k, v := range q {
		if len(v) != 1 {
			return f, badRequest("query parameter %s is given %d times", k, len(v))
		}
		switch k {
		case apiv1.FilterSession:
			f.session = v[0]
		case apiv1.FilterPhase:
			if !slices.Contains(grantPhases, v[0]) {
				return f, badRequest("phase is one of %s, not %q", strings.Join(grantPhases, ", "), v[0])
			}
			f.phase = v[0]
		case apiv1.FilterMine:
			switch v[0] {
			case "true":
				f.mine = true
			case "false":
			default:
				return f, badRequest("mine is true or false, not %q", v[0])
			}
		default:
			return f, badRequest("unknown query parameter %q: the filters are session, phase and mine", k)
		}
	}
	return f, nil
}

// match keeps a grant. Mine is the calling session's grants; a caller that is
// not a session has none.
func (f grantFilters) match(g *v1alpha1.AccessGrant, c *caller) bool {
	switch {
	case f.session != "" && g.Spec.Requester.Session != f.session,
		f.phase != "" && grantPhase(g) != f.phase,
		f.mine && (c.kind != kindSession || g.Spec.Requester.Session != c.parent):
		return false
	}
	return true
}

// grantPhase is the grant's phase; one the broker has not seen yet is Pending.
func grantPhase(g *v1alpha1.AccessGrant) string {
	if g.Status.Phase == "" {
		return apiv1.GrantPending
	}
	return string(g.Status.Phase)
}

// grantKey reads and checks the {name} path value.
func (s *Server) grantKey(r *http.Request) (types.NamespacedName, error) {
	name := r.PathValue("name")
	if !strings.HasPrefix(name, "grant-") || len(validation.IsDNS1123Label(name)) > 0 {
		return types.NamespacedName{}, notFound("no grant %q: a grant's name is grant-<id>", name)
	}
	return types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: name}, nil
}

// getGrant serves GET /v1/grants/{name}.
func (s *Server) getGrant(ctx context.Context, _ http.ResponseWriter, r *http.Request, _ *caller) (int, any, error) {
	key, err := s.grantKey(r)
	if err != nil {
		return 0, nil, err
	}
	var g v1alpha1.AccessGrant
	if err := s.Live.Get(ctx, key, &g); err != nil {
		return 0, nil, fromGrantKubeError(err, "grant "+key.Name)
	}
	return http.StatusOK, s.grantView(&g), nil
}

// releaseGrant serves DELETE /v1/grants/{name}: it sets spec.release, and the
// broker ends the grant and revokes what it made. The requesting session, Tom
// and clients may release a grant; another session may not. The grant object
// stays: it is the audit record (DESIGN-001 6.12).
func (s *Server) releaseGrant(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	key, err := s.grantKey(r)
	if err != nil {
		return 0, nil, err
	}
	var g v1alpha1.AccessGrant
	if err := s.Live.Get(ctx, key, &g); err != nil {
		return 0, nil, fromGrantKubeError(err, "grant "+key.Name)
	}
	if c.kind == kindSession && g.Spec.Requester.Session != c.parent {
		return 0, nil, forbidden("grant %s is session %s's; a session releases only its own grants", g.Name, g.Spec.Requester.Session)
	}
	if g.Spec.Release || g.Status.Phase.Ended() {
		return http.StatusOK, s.grantView(&g), nil
	}
	patch := client.RawPatch(types.MergePatchType, []byte(`{"spec":{"release":true}}`))
	if err := s.Client.Patch(ctx, &g, patch); err != nil {
		return 0, nil, fromGrantKubeError(err, "grant "+key.Name)
	}
	s.Log.Info("grant release requested", "grant", g.Name, "caller", c.String())
	return http.StatusAccepted, s.grantView(&g), nil
}

// grantView is a grant as the API shows it.
func (s *Server) grantView(g *v1alpha1.AccessGrant) apiv1.Grant {
	rq := g.Spec.Requester
	st := g.Status
	v := apiv1.Grant{
		Name:            g.Name,
		Session:         rq.Session,
		Repo:            rq.Repo,
		Profile:         rq.Profile,
		Agent:           string(rq.Agent),
		Parent:          rq.Parent,
		Type:            string(g.Spec.Type),
		TTL:             g.Spec.TTL.Duration.String(),
		Reason:          g.Spec.Reason,
		Release:         g.Spec.Release,
		CreatedAt:       g.CreationTimestamp.UTC(),
		Phase:           grantPhase(g),
		Message:         st.Message,
		ApprovedBy:      st.ApprovedBy,
		ApprovedAt:      timeOf(st.ApprovedAt),
		DeniedBy:        st.DeniedBy,
		ExpiresAt:       timeOf(st.ExpiresAt),
		EndedAt:         timeOf(st.EndedAt),
		NotifiedAt:      timeOf(st.NotifiedAt),
		ServiceAccount:  st.ServiceAccount,
		InstalledPodUID: st.InstalledPodUID,
		InstalledAt:     timeOf(st.InstalledAt),
	}
	if kg := g.Spec.Kube; kg != nil {
		v.Role, v.Namespaces = kg.Role, kg.Namespaces
	}
	if e := g.Spec.Egress; e != nil {
		v.FQDNs, v.CIDRs = e.FQDNs, e.CIDRs
		for _, ep := range e.Endpoints {
			out := apiv1.GrantEndpoint{Namespace: ep.Namespace}
			if len(ep.MatchLabels) > 0 {
				out.MatchLabels = make(map[string]string, len(ep.MatchLabels))
				for k, lv := range ep.MatchLabels {
					out.MatchLabels[k] = string(lv)
				}
			}
			v.Endpoints = append(v.Endpoints, out)
		}
		for _, p := range e.Ports {
			v.Ports = append(v.Ports, apiv1.GrantPort{Port: p.Port, Protocol: string(p.Protocol)})
		}
	}
	if cg := g.Spec.Credential; cg != nil {
		v.Credential = string(cg.Name)
	}
	if st.ApprovedTTL != nil {
		v.ApprovedTTL = st.ApprovedTTL.Duration.String()
	}
	if v.Phase == apiv1.GrantPending && !g.Spec.Release && s.GrantApprovalURL != "" {
		v.ApprovalURL = strings.TrimRight(s.GrantApprovalURL, "/") + "/" + g.Name
	}
	return v
}
