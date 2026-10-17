package agentrun

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// grant list and show are the first slice of plan 07 step 7. They read the
// existing /v1/grants routes. Request, use, release, break-glass and the
// in-pod MCP server are not in this slice.

var grantPhases = []string{
	apiv1.GrantPending, apiv1.GrantActive, apiv1.GrantDenied,
	apiv1.GrantExpired, apiv1.GrantReleased, apiv1.GrantFailed,
}

func (a *app) grant(ctx context.Context, args []string) error {
	if len(args) == 0 {
		_, _ = io.WriteString(a.env.Stderr, commandHelp["grant"])
		return &exitError{code: ExitUsage}
	}
	switch args[0] {
	case "list":
		return a.grantList(ctx, args[1:])
	case "show":
		return a.grantShow(ctx, args[1:])
	case "request":
		return a.grantRequest(ctx, args[1:])
	case "release":
		return a.grantRelease(ctx, args[1:])
	case "help", "-h", "--help":
		_, _ = io.WriteString(a.env.Stdout, commandHelp["grant"])
		return nil
	}
	_, _ = io.WriteString(a.env.Stderr, commandHelp["grant"])
	return usageError("unknown grant command %q: it is list, show, request or release", args[0])
}

func (a *app) grantList(ctx context.Context, args []string) error {
	cmd := newCommand("grant", outputJSON)
	var session, phase string
	var mine bool
	cmd.fs.StringVar(&session, "session", "", "")
	cmd.fs.StringVar(&phase, "phase", "", "")
	cmd.fs.BoolVar(&mine, "mine", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("grant list takes --session, --phase and --mine, and no arguments; agent-run help grant")
	}
	if phase != "" && !grantPhaseKnown(phase) {
		return usageError("grant list: phase is one of %s, not %q", strings.Join(grantPhases, ", "), phase)
	}
	q := url.Values{}
	if session != "" {
		q.Set(apiv1.FilterSession, session)
	}
	if phase != "" {
		q.Set(apiv1.FilterPhase, phase)
	}
	if mine {
		q.Set(apiv1.FilterMine, "true")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var out apiv1.GrantList
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.GrantsPath, q, nil, &out); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(out)
	}
	if len(out.Items) == 0 {
		a.outf("no grants\n")
		return nil
	}
	tw := a.table()
	_, _ = fmt.Fprintln(tw, "NAME\tPHASE\tTYPE\tSESSION\tTTL\tEXPIRES")
	for _, g := range out.Items {
		expires := "-"
		if g.ExpiresAt != nil && !g.ExpiresAt.IsZero() {
			expires = g.ExpiresAt.UTC().Format("2006-01-02 15:04")
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", g.Name, g.Phase, g.Type, firstOf(g.Session, "-"), g.TTL, expires)
	}
	return tw.Flush()
}

func (a *app) grantRequest(ctx context.Context, args []string) error {
	cmd := newCommand("grant", outputJSON)
	var typ, role, credential, ttl, reason string
	var namespaces, fqdns, cidrs, endpoints, ports stringList
	cmd.fs.StringVar(&typ, "type", "", "")
	cmd.fs.StringVar(&role, "role", "", "")
	cmd.fs.StringVar(&credential, "credential", "", "")
	cmd.fs.StringVar(&ttl, "ttl", "", "")
	cmd.fs.StringVar(&reason, "reason", "", "")
	cmd.fs.Var(&namespaces, "namespace", "")
	cmd.fs.Var(&fqdns, "fqdn", "")
	cmd.fs.Var(&cidrs, "cidr", "")
	cmd.fs.Var(&endpoints, "endpoint", "")
	cmd.fs.Var(&ports, "port", "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("grant request takes flags, not arguments; agent-run help grant")
	}
	req, err := grantRequestBody(typ, role, credential, ttl, reason, namespaces, fqdns, cidrs, endpoints, ports)
	if err != nil {
		return err
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	// An identical request merges into the grant the session already holds, so
	// repeating this post does not open a second one.
	var out apiv1.Grant
	status, err := a.call(ctx, c, http.MethodPost, apiv1.GrantsPath, nil, req, &out)
	if err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(out)
	}
	if status == http.StatusOK {
		a.outf("existing grant\n")
	} else {
		a.outf("requested\n")
	}
	return a.printGrant(out)
}

func grantRequestBody(typ, role, credential, ttl, reason string, namespaces, fqdns, cidrs, endpoints, ports []string) (apiv1.CreateGrantRequest, error) {
	switch typ {
	case apiv1.GrantTypeKube, apiv1.GrantTypeEgress, apiv1.GrantTypeCredential, apiv1.GrantTypeBreakglass:
	default:
		return apiv1.CreateGrantRequest{}, usageError("grant request: --type is kube, egress, credential or breakglass, not %q", typ)
	}
	if strings.TrimSpace(reason) == "" {
		return apiv1.CreateGrantRequest{}, usageError("grant request: --reason is required; Tom reads it on the approval page")
	}
	eps := make([]apiv1.GrantEndpoint, 0, len(endpoints))
	for _, raw := range endpoints {
		ep, err := parseGrantEndpoint(raw)
		if err != nil {
			return apiv1.CreateGrantRequest{}, err
		}
		eps = append(eps, ep)
	}
	parsedPorts := make([]apiv1.GrantPort, 0, len(ports))
	for _, raw := range ports {
		p, err := parseGrantPort(raw)
		if err != nil {
			return apiv1.CreateGrantRequest{}, err
		}
		parsedPorts = append(parsedPorts, p)
	}
	return apiv1.CreateGrantRequest{
		Type: typ, Role: role, Namespaces: namespaces, FQDNs: fqdns, CIDRs: cidrs,
		Endpoints: eps, Ports: parsedPorts, Credential: credential, TTL: ttl, Reason: reason,
	}, nil
}

func parseGrantEndpoint(raw string) (apiv1.GrantEndpoint, error) {
	ns, labels, labeled := strings.Cut(raw, ":")
	if ns == "" || strings.Contains(ns, "/") || strings.Contains(ns, ",") {
		return apiv1.GrantEndpoint{}, usageError("grant request: --endpoint is NAMESPACE or NAMESPACE:key=value, not %q", raw)
	}
	ep := apiv1.GrantEndpoint{Namespace: ns}
	if !labeled {
		return ep, nil
	}
	ep.MatchLabels = map[string]string{}
	for _, pair := range strings.Split(labels, ",") {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || k == "" || v == "" || strings.Contains(v, "=") {
			return apiv1.GrantEndpoint{}, usageError("grant request: --endpoint labels are key=value pairs, not %q", raw)
		}
		ep.MatchLabels[k] = v
	}
	return ep, nil
}

func parseGrantPort(raw string) (apiv1.GrantPort, error) {
	num, proto, hasProto := strings.Cut(raw, "/")
	p, err := strconv.Atoi(num)
	if err != nil || p < 1 || p > 65535 {
		return apiv1.GrantPort{}, usageError("grant request: --port is 1-65535 or N/tcp or N/udp, not %q", raw)
	}
	out := apiv1.GrantPort{Port: int32(p)}
	if !hasProto {
		return out, nil
	}
	switch strings.ToUpper(proto) {
	case "TCP", "UDP":
		out.Protocol = strings.ToUpper(proto)
	default:
		return apiv1.GrantPort{}, usageError("grant request: --port protocol is tcp or udp, not %q", raw)
	}
	return out, nil
}

func (a *app) grantRelease(ctx context.Context, args []string) error {
	cmd := newCommand("grant", outputJSON)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || !validGrantCLIName(pos[0]) {
		return usageError("grant release takes one grant-<id> name; agent-run help grant")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	// A second release of the same grant returns the audit record. It does not
	// revoke anything itself: the broker ends the grant after spec.release.
	var out apiv1.Grant
	status, err := a.call(ctx, c, http.MethodDelete, apiv1.GrantPath(pos[0]), nil, nil, &out)
	if err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(out)
	}
	if status == http.StatusOK {
		a.outf("already released\n")
	} else {
		a.outf("release requested\n")
	}
	return a.printGrant(out)
}

func (a *app) grantShow(ctx context.Context, args []string) error {
	cmd := newCommand("grant", outputJSON)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || !validGrantCLIName(pos[0]) {
		return usageError("grant show takes one grant-<id> name; agent-run help grant")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var out apiv1.Grant
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.GrantPath(pos[0]), nil, nil, &out); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(out)
	}
	return a.printGrant(out)
}

func (a *app) printGrant(out apiv1.Grant) error {
	expires := "-"
	if out.ExpiresAt != nil && !out.ExpiresAt.IsZero() {
		expires = out.ExpiresAt.UTC().Format(timeRFC3339)
	}
	a.outf("name: %s\nphase: %s\ntype: %s\nsession: %s\nttl: %s\nexpires: %s\nreason: %s\n",
		out.Name, out.Phase, out.Type, firstOf(out.Session, "-"), out.TTL, expires, out.Reason)
	return nil
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func grantPhaseKnown(phase string) bool {
	for _, known := range grantPhases {
		if phase == known {
			return true
		}
	}
	return false
}

func validGrantCLIName(name string) bool {
	if !strings.HasPrefix(name, "grant-") || len(name) <= len("grant-") || len(name) > 63 || strings.HasSuffix(name, "-") || strings.Contains(name, "/") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}
		return false
	}
	return true
}

const timeRFC3339 = "2006-01-02T15:04:05Z"
