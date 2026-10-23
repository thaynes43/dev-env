package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func (a *app) list(ctx context.Context, args []string) error {
	cmd := newCommand("list", outputName, outputJSON)
	var repo, state string
	var mine bool
	cmd.fs.StringVar(&repo, "repo", "", "")
	cmd.fs.StringVar(&state, "state", "", "")
	cmd.fs.BoolVar(&mine, "mine", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("list takes no arguments, got %q; agent-run show <name> shows one session", pos)
	}
	q := url.Values{}
	if repo != "" {
		q.Set(apiv1.FilterRepo, repo)
	}
	if state != "" {
		q.Set(apiv1.FilterState, state)
	}
	if mine {
		q.Set(apiv1.FilterMine, "true")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var out apiv1.SessionList
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.SessionsPath, q, nil, &out); err != nil {
		return err
	}
	switch cmd.c.output {
	case outputJSON:
		return a.printJSON(out)
	case outputName:
		for _, s := range out.Sessions {
			a.outf("%s\n", s.Name)
		}
		return nil
	}
	if len(out.Sessions) == 0 {
		a.outf("No sessions.\n")
		return nil
	}
	tw := a.table()
	_, _ = fmt.Fprintln(tw, "NAME\tPHASE\tAGENT\tNODE\tAGE\tPARENT\tNOTE")
	now := a.env.Now()
	for _, s := range out.Sessions {
		agent := "-"
		if s.AgentStatus != nil && s.AgentStatus.State != "" {
			agent = s.AgentStatus.State
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.Phase, agent, dash(s.Node),
			age(now, s.CreatedAt), dash(s.Parent), note(s))
	}
	return tw.Flush()
}

// note is a list row's last column: what is unusual about the session.
func note(s apiv1.Session) string {
	var parts []string
	if s.Reaping {
		parts = append(parts, "reaping")
	}
	if s.Outdated {
		parts = append(parts, "outdated")
	}
	// An ended pod's session is Failed whatever its task did (D-44); the task's
	// own result tells a success from a failure (DESIGN-021).
	if v := s.TaskVerdict(); v != nil {
		parts = append(parts, "task "+v.State)
	}
	switch s.Phase {
	case "Pending":
		if s.Pending != "" {
			parts = append(parts, s.Pending)
		}
	case "Failed":
		if m := podReadyMessage(s); m != "" {
			parts = append(parts, m)
		}
	}
	return strings.Join(parts, "; ")
}

func (a *app) show(ctx context.Context, args []string) error {
	cmd := newCommand("show", outputJSON)
	var once, asJSON, captured bool
	cmd.fs.BoolVar(&once, "once", false, "")
	cmd.fs.BoolVar(&asJSON, "json", false, "")
	cmd.fs.BoolVar(&captured, "captured-usage", false, "")
	onceFlags, jsonFlags := 0, 0
	for _, arg := range args {
		if arg == "--once" || arg == "-once" || strings.HasPrefix(arg, "--once=") || strings.HasPrefix(arg, "-once=") {
			onceFlags++
		}
		if arg == "--json" || arg == "-json" || strings.HasPrefix(arg, "--json=") || strings.HasPrefix(arg, "-json=") {
			jsonFlags++
		}
	}
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("show takes one session name; agent-run list shows them")
	}
	if onceFlags != 0 && (onceFlags != 1 || jsonFlags != 1 || !once || !asJSON || !canaryName.MatchString(pos[0])) {
		return usageError("show --once requires one valid DNS-label session name and explicit --json --once")
	}
	if asJSON {
		cmd.c.output = outputJSON
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	if captured {
		if once {
			return usageError("show --captured-usage cannot be combined with --once")
		}
		if cmd.c.output != outputJSON {
			return usageError("show --captured-usage requires -o json")
		}
	}
	if once {
		return a.showOnce(ctx, c, pos[0])
	}
	callCtx := ctx
	if captured {
		callCtx = withUsageCapture(ctx)
	}
	var s apiv1.Session
	if _, err := a.call(callCtx, c, http.MethodGet, apiv1.SessionPath(url.PathEscape(pos[0])), nil, nil, &s); err != nil {
		return err
	}
	if captured && s.CapturedUsage == nil {
		s.CapturedUsage = &apiv1.CapturedUsage{Available: false, Reason: "unavailable"}
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(s)
	}
	a.printSession(s)
	return nil
}

// printSession is show's text form: one fact per line.
func (a *app) printSession(s apiv1.Session) {
	now := a.env.Now()
	tw := tabwriter.NewWriter(a.env.Stdout, 0, 0, 1, ' ', 0)
	row := func(k, format string, args ...any) {
		_, _ = fmt.Fprintf(tw, "%s:\t%s\n", k, fmt.Sprintf(format, args...))
	}
	row("name", "%s", s.Name)
	phase := s.Phase
	if s.Node != "" {
		phase += " on " + s.Node
	}
	if s.Reaping {
		phase += ", reaping: rescue, then suspend and archive"
	}
	row("phase", "%s", phase)
	if s.SuspendedBy != "" && s.OperatingMode == "Suspended" {
		row("suspended by", "%s", s.SuspendedBy)
	}
	if s.Pending != "" && s.Phase == "Pending" {
		row("pending", "%s", s.Pending)
	}
	if v := s.TaskVerdict(); v != nil {
		row("task result", "%s (%s): %s", v.State, v.Reason, v.Message)
	}
	row("repo", "%s from %s", s.Repo, firstOf(s.Base, "origin/main"))
	row("agent", "%s %s, %s, %s mode", s.Agent, s.Model, effortText(s.Effort), s.Mode)
	row("size", "%s", firstOf(s.Size, "M"))
	if s.Profile != "" {
		row("profile", "%s", s.Profile)
	}
	if l := s.Limits; l != nil {
		row("limits", "timeout %s, max turns %s", dash(l.Timeout), countOrNone(l.MaxTurns))
	}
	if l := s.Lifecycle; l != nil {
		row("timers", "idle suspend after %s, archive after %s", firstOf(l.IdleSuspendAfter, "the templates'"), firstOf(l.ArchiveAfter, "the templates'"))
	}
	row("parent", "%s", dash(s.Parent))
	row("created", "%s (%s ago)", s.CreatedAt.UTC().Format(time.RFC3339), age(now, s.CreatedAt))
	if s.Revision != "" {
		rev := s.Revision
		if s.Outdated {
			rev += " (outdated: the current templates differ)"
		}
		row("revision", "%s", rev)
	}
	if st := s.AgentStatus; st != nil {
		row("agent state", "%s", firstOf(st.State, "-"))
		if st.Branch != "" {
			row("branch", "%s at %s", st.Branch, dash(st.Head))
		}
		if st.LastActivity != nil {
			row("last activity", "%s ago", age(now, *st.LastActivity))
		}
		if st.LastHeartbeat != nil {
			row("last heartbeat", "%s ago", age(now, *st.LastHeartbeat))
		}
		if t := st.Task; t != nil {
			res := fmt.Sprintf("exit %d at %s, %d turns", t.ExitCode, t.FinishedAt.UTC().Format(time.RFC3339), t.NumTurns)
			if t.TimedOut {
				res += ", timed out"
			}
			if t.IsError {
				res += ", error " + firstOf(t.Subtype, "reported")
			}
			row("task", "%s", res)
		}
		if st.Message != "" {
			row("message", "%s", st.Message)
		}
		for _, p := range st.Problems {
			row("problem", "%s", p)
		}
		if pb := st.ProjectBootstrap; pb != nil {
			line := pb.State
			if pb.Reason != "" {
				line += ", " + pb.Reason
			}
			ready := 0
			for _, repo := range pb.Repositories {
				if repo.State == "Ready" {
					ready++
				}
			}
			line += fmt.Sprintf(" (%d of %d repositories ready", ready, len(pb.Repositories))
			ready = 0
			for _, project := range pb.Projects {
				if project.State == "Ready" {
					ready++
				}
			}
			if len(pb.Projects) > 0 {
				line += fmt.Sprintf(", %d of %d projects ready", ready, len(pb.Projects))
			}
			row("project bootstrap", "%s)", line)
			for _, repo := range pb.Repositories {
				if repo.State != "Ready" {
					row("  repository", "%s %s at %s: %s", repo.Name, repo.State, dash(repo.Step), dash(repo.Reason))
				}
			}
			for _, project := range pb.Projects {
				if project.State == "Ready" {
					continue
				}
				why := dash(project.Reason)
				if project.Repository != "" {
					why += " in " + project.Repository
				}
				if project.Cause != "" {
					why += " (" + project.Cause + ")"
				}
				row("  project", "%s %s: %s", project.Name, project.State, why)
			}
		}
	}
	u := s.UsageObservation
	if u != nil && u.Validate() != nil {
		u = nil
	}
	row("Actual model", "%s", servedModelsText(u))
	if u != nil {
		cost := "Unknown"
		if u.Cost.State == protocol.UsageReported {
			cost = u.Cost.Amount + " USD (CLI equivalent estimate)"
		}
		row("Initial task cost", "%s", cost)
		row("Usage scope", "Initial managed invocation")
		row("Usage result", "%s; this Session's own CLI process, child Sessions counted on their own", u.ResultState)
		if u.Version == protocol.UsageVersion {
			row("Usage provenance", "Provider reported")
			row("Billing basis", "%s", billingBasisText(u.BillingBasis))
		}
		row("Requested model", "%s", u.Source.RequestedModel)
		row("Input tokens", "%s", usageTokenText(u.InputTokens))
		row("Output tokens", "%s", usageTokenText(u.OutputTokens))
		row("Cache read tokens", "%s", usageTokenText(u.CacheReadInputTokens))
		row("Cache create tokens", "%s", usageTokenText(u.CacheCreationInputTokens))
		for _, m := range u.ServedModels {
			row("  served model", "%s: input %s, output %s, cache read %s, cache create %s, cost %s", m.Model, usageTokenText(m.InputTokens), usageTokenText(m.OutputTokens),
				usageTokenText(m.CacheReadInputTokens), usageTokenText(m.CacheCreationInputTokens), usageCostText(m.Cost))
		}
		row("Subscription quota", "Unknown")
	} else {
		row("Requested model", "%s", firstOf(s.Model, "Unknown"))
		row("Initial task cost", "Unknown")
		row("Input tokens", "Unknown")
		row("Output tokens", "Unknown")
		row("Cache read tokens", "Unknown")
		row("Cache create tokens", "Unknown")
	}
	if r := s.UsageRollup; r != nil {
		coverage := fmt.Sprintf("%s over %d retained descendant Sessions, %d invocations counted once each", r.Coverage, r.Sessions, r.Counted)
		if r.Truncated {
			coverage += "; walk stopped at the bound"
		}
		coverage += "; reaped descendants are not counted"
		row("Descendant usage", "%s", coverage)
		row("  input tokens", "%s", usageTokenText(r.InputTokens))
		row("  output tokens", "%s", usageTokenText(r.OutputTokens))
		row("  cache read tokens", "%s", usageTokenText(r.CacheReadInputTokens))
		row("  cache create tokens", "%s", usageTokenText(r.CacheCreationInputTokens))
		row("  cost", "%s", usageCostText(r.Cost))
		row("  billing basis", "%s", billingBasisText(r.BillingBasis))
		models := "Unknown"
		if r.ObservedModelState == protocol.UsageProviderReported && len(r.ObservedModels) > 0 {
			models = strings.Join(r.ObservedModels, ", ") + " (provider reported)"
		}
		row("  models", "%s", models)
	}
	if issue := s.UsageConflict; issue != nil {
		row("usage conflict", "Unknown (%s); original observation retained", issue.Reason)
	}
	for _, c := range s.Conditions {
		row("condition", "%s=%s %s: %s", c.Type, c.Status, c.Reason, c.Message)
	}
	_ = tw.Flush()
	if s.Prompt != "" {
		a.outf("prompt:\n")
		for _, line := range strings.Split(strings.TrimRight(s.Prompt, "\n"), "\n") {
			a.outf("  %s\n", line)
		}
	}
}

// servedModelsText names the provider-reported served models, or Unknown.
func servedModelsText(u *protocol.UsageObservation) string {
	if u == nil || u.ActualModelState != protocol.UsageProviderReported || len(u.ServedModels) == 0 {
		return "Unknown"
	}
	names := make([]string, 0, len(u.ServedModels))
	for _, m := range u.ServedModels {
		names = append(names, m.Model)
	}
	return strings.Join(names, ", ") + " (provider reported)"
}

func billingBasisText(basis string) string {
	if basis == protocol.UsageSubscription {
		return "Subscription (plan-served; the cost is a list-price equivalent, not a charge)"
	}
	return "Unknown"
}

func usageCostText(cost protocol.CostObservation) string {
	if cost.State == protocol.UsageReported && cost.Valid() {
		return cost.Amount + " USD (CLI equivalent estimate)"
	}
	return "Unknown"
}

func usageTokenText(metric protocol.TokenObservation) string {
	if metric.State == protocol.UsageReported && metric.Valid() {
		return fmt.Sprintf("%d", *metric.Amount)
	}
	return "Unknown"
}

func (a *app) reap(ctx context.Context, args []string) error {
	cmd := newCommand("reap", outputJSON)
	var force bool
	cmd.fs.BoolVar(&force, "force", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageError("reap takes the names of the sessions to reap; agent-run list shows them")
	}
	if force {
		a.errf("--force changes nothing in v2: every reap rescues the session's work first, and nothing skips that (D-45)")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var (
		reaped []apiv1.Session
		first  error
	)
	for _, name := range pos {
		var s apiv1.Session
		if _, err := a.call(ctx, c, http.MethodDelete, apiv1.SessionPath(url.PathEscape(name)), nil, nil, &s); err != nil {
			if first == nil {
				first = err
			}
			if len(pos) > 1 {
				a.errf("%s: %v", name, err)
			}
			continue
		}
		reaped = append(reaped, s)
		if cmd.c.output == outputText {
			a.outf("reaping %s: the operator rescues its work to a bundle, then stops its pod and archives its volume\n", s.Name)
		}
	}
	if cmd.c.output == outputJSON {
		if err := a.printJSON(apiv1.SessionList{Sessions: append([]apiv1.Session{}, reaped...)}); err != nil {
			return err
		}
	}
	if first != nil && len(pos) > 1 {
		// Each failure is already printed; keep the first one's exit code.
		var ee *exitError
		if errors.As(first, &ee) {
			return &exitError{code: ee.code}
		}
		return &exitError{code: ExitFailed}
	}
	return first
}

func (a *app) fleet(ctx context.Context, args []string) error {
	cmd := newCommand("fleet", outputJSON)
	var resources bool
	cmd.fs.BoolVar(&resources, "resources", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("fleet takes no arguments, got %q", pos)
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	callCtx := ctx
	if resources {
		callCtx = withFleetResources(ctx)
	}
	var f apiv1.Fleet
	if _, err := a.call(callCtx, c, http.MethodGet, apiv1.FleetPath, nil, nil, &f); err != nil {
		return err
	}
	if resources {
		fillAbsentFleetResources(&f)
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(f)
	}
	rev := f.Revision
	if rev == "" {
		rev = "unknown: " + firstOf(f.RevisionError, "the API gave no reason")
	}
	a.outf("revision: %s\n", rev)
	a.outf("phases:   %s\n", phaseCounts(f.Phases))
	if len(f.Nodes) > 0 {
		nodes := make([]string, 0, len(f.Nodes))
		for _, n := range f.Nodes {
			nodes = append(nodes, fmt.Sprintf("%s %d", n.Name, n.Sessions))
		}
		a.outf("nodes:    %s\n", strings.Join(nodes, ", "))
	}
	if len(f.Outdated) > 0 {
		a.outf("outdated: %s\n", strings.Join(f.Outdated, ", "))
	}
	if len(f.Sessions) == 0 {
		a.outf("\nNo session holds or waits for a pod.\n")
		return nil
	}
	a.outf("\n")
	now := a.env.Now()
	tw := a.table()
	if resources {
		_, _ = fmt.Fprintln(tw, "NAME\tPHASE\tAGENT\tNODE\tHEARTBEAT\tAGE\tNOTE\tPOD\tSPEC NODE\tRESOURCES")
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tPHASE\tAGENT\tNODE\tHEARTBEAT\tAGE\tNOTE")
	}
	for _, s := range f.Sessions {
		beat := "-"
		if s.LastHeartbeat != nil {
			beat = age(now, *s.LastHeartbeat) + " ago"
		}
		var parts []string
		if s.Reaping {
			parts = append(parts, "reaping")
		}
		if s.Outdated {
			parts = append(parts, "outdated")
		}
		if s.TaskResult != nil {
			parts = append(parts, "task "+s.TaskResult.State)
		}
		if s.Phase == "Pending" && s.Pending != "" {
			parts = append(parts, s.Pending)
		}
		if resources {
			podUID, specNode, observed := formatFleetResources(s.Resources)
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.Phase, dash(s.AgentState), dash(s.Node),
				beat, age(now, s.CreatedAt), strings.Join(parts, "; "), podUID, specNode, observed)
			continue
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.Phase, dash(s.AgentState), dash(s.Node),
			beat, age(now, s.CreatedAt), strings.Join(parts, "; "))
	}
	return tw.Flush()
}

func fillAbsentFleetResources(f *apiv1.Fleet) {
	for i := range f.Sessions {
		f.Sessions[i].Resources = normalizeFleetResources(f.Sessions[i].Resources)
	}
}

func normalizeFleetResources(obs *apiv1.FleetResources) *apiv1.FleetResources {
	if obs == nil {
		return unknownFleetObservation(apiv1.FleetResourcesUnavailable)
	}
	switch obs.State {
	case apiv1.FleetResourcesUnknown:
		return unknownFleetObservation(knownFleetReason(obs.Reason))
	case apiv1.FleetResourcesObserved:
		if !validObservedFleetResources(obs) {
			return unknownFleetObservation(apiv1.FleetResourcesUnavailable)
		}
		return obs
	default:
		return unknownFleetObservation(apiv1.FleetResourcesUnavailable)
	}
}

func unknownFleetObservation(reason string) *apiv1.FleetResources {
	return &apiv1.FleetResources{State: apiv1.FleetResourcesUnknown, Reason: knownFleetReason(reason)}
}

func knownFleetReason(reason string) string {
	switch reason {
	case apiv1.FleetResourcesMissing, apiv1.FleetResourcesForeign, apiv1.FleetResourcesAmbiguous, apiv1.FleetResourcesReplaced, apiv1.FleetResourcesUnreadable, apiv1.FleetResourcesOversized, apiv1.FleetResourcesUnavailable:
		return reason
	default:
		return apiv1.FleetResourcesUnavailable
	}
}

func validObservedFleetResources(obs *apiv1.FleetResources) bool {
	if obs.Reason != "" || obs.ObservedAt == nil || obs.ObservedAt.IsZero() || !safeFleetToken(obs.PodUID, 128) {
		return false
	}
	if obs.Node == nil {
		return false
	}
	switch obs.Node.State {
	case apiv1.FleetNodeSet:
		if !safeFleetToken(obs.Node.Name, 253) {
			return false
		}
	case apiv1.FleetNodeUnscheduled:
		if obs.Node.Name != "" {
			return false
		}
	default:
		return false
	}
	for i := range obs.Containers {
		if !validFleetContainer(obs.Containers[i], apiv1.FleetContainerOrdinary) {
			return false
		}
	}
	for i := range obs.InitContainers {
		if !validFleetContainer(obs.InitContainers[i], apiv1.FleetContainerInit) {
			return false
		}
	}
	return true
}

func validFleetContainer(container apiv1.FleetContainerResources, kind string) bool {
	return container.Kind == kind && safeFleetToken(container.Name, 253) && validFleetQuantity(container.CPURequest) && validFleetQuantity(container.CPULimit) && validFleetQuantity(container.MemoryRequest) && validFleetQuantity(container.MemoryLimit)
}

func validFleetQuantity(quantity apiv1.FleetQuantity) bool {
	switch quantity.State {
	case apiv1.FleetQuantityAbsent:
		return quantity.Value == ""
	case apiv1.FleetQuantitySet:
		return canonicalFleetQuantity(quantity.Value)
	default:
		return false
	}
}

func safeFleetToken(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func formatFleetResources(obs *apiv1.FleetResources) (podUID, specNode, text string) {
	obs = normalizeFleetResources(obs)
	if obs.State != apiv1.FleetResourcesObserved {
		return "Unknown", "Unknown", "Unknown (" + obs.Reason + ")"
	}
	specNode = "Unscheduled"
	if obs.Node.State == apiv1.FleetNodeSet {
		specNode = obs.Node.Name
	}
	parts := make([]string, 0, len(obs.Containers)+len(obs.InitContainers))
	for _, container := range obs.Containers {
		parts = append(parts, formatFleetContainer(container))
	}
	for _, container := range obs.InitContainers {
		parts = append(parts, formatFleetContainer(container))
	}
	if len(parts) == 0 {
		parts = append(parts, "no containers")
	}
	return obs.PodUID, specNode, strings.Join(parts, "; ")
}

func formatFleetContainer(container apiv1.FleetContainerResources) string {
	name := container.Name
	if container.Kind == apiv1.FleetContainerInit {
		name = "init/" + name
	}
	return fmt.Sprintf("%s cpu %s/%s memory %s/%s", name, formatFleetQuantity(container.CPURequest), formatFleetQuantity(container.CPULimit), formatFleetQuantity(container.MemoryRequest), formatFleetQuantity(container.MemoryLimit))
}

func formatFleetQuantity(quantity apiv1.FleetQuantity) string {
	switch quantity.State {
	case apiv1.FleetQuantitySet:
		if !canonicalFleetQuantity(quantity.Value) {
			return "Unknown"
		}
		return quantity.Value
	case apiv1.FleetQuantityAbsent:
		return "absent"
	default:
		return "Unknown"
	}
}

// canonicalFleetQuantity accepts the server's canonical quantity spelling.
// The mantissa is an integer without a leading zero, the sign is present only
// when negative, and the suffix is one decimal unit, one binary unit, or a
// decimal exponent. It does not parse Kubernetes quantities.
func canonicalFleetQuantity(value string) bool {
	if value == "0" {
		return true
	}
	if value == "" || len(value) > 32 || strings.ContainsFunc(value, unicode.IsControl) {
		return false
	}
	rest := value
	if rest[0] == '-' {
		rest = rest[1:]
		if rest == "" || rest[0] == '0' {
			return false
		}
	}
	if rest[0] < '1' || rest[0] > '9' {
		return false
	}
	i := 1
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	suffix := rest[i:]
	switch suffix {
	case "", "n", "u", "m", "k", "M", "G", "T", "P", "E", "Ki", "Mi", "Gi", "Ti", "Pi", "Ei":
		return true
	}
	if len(suffix) < 2 || suffix[0] != 'e' {
		return false
	}
	exp := suffix[1:]
	if exp[0] == '-' {
		exp = exp[1:]
		if exp == "" {
			return false
		}
	}
	if exp == "0" {
		return true
	}
	if exp[0] < '1' || exp[0] > '9' {
		return false
	}
	for _, c := range exp[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// phaseCounts is "Running 3, Pending 1", in the lifecycle's order.
func phaseCounts(m map[string]int) string {
	if len(m) == 0 {
		return "no sessions"
	}
	order := map[string]int{"Pending": 0, "Running": 1, "Idle": 2, "Draining": 3, "Suspended": 4, "Archived": 5, "Failed": 6}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		oi, iok := order[keys[i]]
		oj, jok := order[keys[j]]
		switch {
		case iok && jok:
			return oi < oj
		case iok != jok:
			return iok
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// table is a column writer whose Flush prints to stdout with no spaces at the
// ends of lines, which an empty last column would leave.
func (a *app) table() *tableWriter {
	t := &tableWriter{out: a.env.Stdout}
	t.Writer = tabwriter.NewWriter(&t.buf, 0, 0, 2, ' ', 0)
	return t
}

type tableWriter struct {
	*tabwriter.Writer
	buf bytes.Buffer
	out io.Writer
}

func (t *tableWriter) Flush() error {
	if err := t.Writer.Flush(); err != nil {
		return err
	}
	for _, line := range strings.SplitAfter(t.buf.String(), "\n") {
		if line == "" {
			continue
		}
		trimmed := strings.TrimRight(line, " \n")
		if strings.HasSuffix(line, "\n") {
			trimmed += "\n"
		}
		if _, err := io.WriteString(t.out, trimmed); err != nil {
			return err
		}
	}
	t.buf.Reset()
	return nil
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.env.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// age is a duration since t, short: 42s, 5m, 3h, 2d.
func age(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func countOrNone(n int32) string {
	if n == 0 {
		return "none"
	}
	return fmt.Sprint(n)
}
