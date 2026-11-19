package keeper

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Catch a new trigger from the control socket, consumer status, or another
// keeper runnable. The positive provider fixture separately verifies that the
// cadence path sends the exact current journal bearer to the profile endpoint.
func TestClaudeEarlyDecisionHasOnlyTheKeeperCadenceEntryPoint(t *testing.T) {
	want := map[string]map[string]int{
		"runClaudeProbes":       {"Start": 1},
		"probeClaude":           {"runClaudeProbes": 1},
		"considerEarlyRefresh":  {"probeClaude": 1},
		"dispatchClaudeRefresh": {"probeClaude": 1, "refresh": 1},
	}
	got := make(map[string]map[string]int)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(files, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(name, "claude_") {
			for _, imp := range file.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(path, "/api/v1alpha1") {
					t.Errorf("%s imports the AgentSession API", name)
				}
			}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			checkClaudeDecisionSelectors(t, files, name, fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				s, ok := n.(*ast.SelectorExpr)
				if !ok || want[s.Sel.Name] == nil {
					return true
				}
				if got[s.Sel.Name] == nil {
					got[s.Sel.Name] = make(map[string]int)
				}
				got[s.Sel.Name][fn.Name.Name]++
				return true
			})
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("early decision paths changed: got %v, want %v", got, want)
	}
}

// Pin the inputs as well as the callers. In particular, neither the worker's
// login observations nor other reads from the publisher or Kubernetes client
// should enter the decision. Method calls that emit metrics, persist state or
// dispatch are allowed effects of the decision.
func checkClaudeDecisionSelectors(t *testing.T, files *token.FileSet, file string, fn *ast.FuncDecl) {
	t.Helper()
	allowed := map[string]string{
		"runClaudeProbes": `
			w.EarlyRecovery w.EarlyRecovery.ProbeInterval w.Clock w.Clock.Now w.Clock.After w.probeClaude
			ctx.Err ctx.Done now.Sub`,
		"probeClaude": `
			w.EarlyRecovery w.EarlyRecovery.Enabled w.load w.Clock w.Clock.Now w.Fence w.probeMemory
			w.probeMemory.identity w.probeMemory.requestedAt w.probeMemory.lastHealthy w.Transport w.Transport.profile
			w.countClaudeProbe w.mu w.mu.Lock w.mu.Unlock w.reportClaudeRecovery w.considerEarlyRefresh
			w.dispatchClaudeRefresh w.publishClaudeReady w.recordClaudeRecovery
			snapshot.Stage snapshot.Access snapshot.Access.Generation snapshot.Access.AccessToken secret.UID
			state.Incident state.Incident.Held state.Incident.Generation state.IncarnationHigh state.IncarnationLow
			state.lastProbe state.lastProbe(gen).ObservedAt current.UID ready.Stage ready.Access recovery.IncarnationHigh
			recovery.IncarnationLow recovery.lastProbe previous.ObservedAt recovery.observe observation.coalesce
			context.WithoutCancel`,
		"considerEarlyRefresh": `
			state.Incident state.floor state.addAttempt state.LastAttempt state.CooldownUntil
			w.EarlyRecovery w.EarlyRecovery.MinInterval w.EarlyRecovery.MaxAttemptsPerDay
			w.recoveryPublished w.Clock w.Clock.Now w.Clock.Now().Before w.retryAfter now.Add
			i.Generation i.Resolved i.Held i.page p.Generation p.Class p.ObservedAt p.FirstUsable p.First401`,
		"claudeProbeAccessUsable":   `access.ExpiresAt access.ExpiresAt.Sub access.Validate`,
		"claudeScheduledGeneration": `secret.Data p.Transitions last.Generation last.Operation`,
		"classifyClaudeProbe":       `errors.As profile.AccountUUID profile.OrganizationUUID access.AccountUUID access.OrganizationUUID`,
		"loadClaudeRecovery": `
			secret.Data s.Incident s.Incident.Generation s.Incident.Replacement s.Probes s.Probes[len(s.Probes)-1].Generation
			s.EarlyTransitions s.EarlyTransitions[len(s.EarlyTransitions)-1].Generation s.useIncarnation`,
		"recoveryPublished": `w.published i.Replacement i.Published`,
		"load":              `context.WithTimeout w.Journal w.Journal.load`,
		"refresh": `
			w.load w.note w.EarlyRecovery w.EarlyRecovery.Enabled w.due w.earlyPublicationPending
			w.Clock w.Clock.Now w.Clock.Now().Before w.dispatchClaudeRefresh prev.Stage prev.Access
			prev.Access.Generation prev.Access.ExpiresAt`,
		"dispatchClaudeRefresh": `
			w.Publisher w.Publisher.generation w.note w.Fence w.Clock w.Clock.Now w.Clock.Now().Add
			w.Outcomes w.Outcomes.WithLabelValues w.Outcomes.WithLabelValues(outcome,trigger).Inc
			w.Journal w.Journal.saveIntent w.Journal.saveTransition w.load w.rollbackUnsent
			w.commitDispatch w.Transport w.Transport.refresh w.Log w.Log.Info w.retryAfter
			w.needsLoginAfter w.needsLogin w.EarlyRecovery w.EarlyRecovery.Enabled w.observeClaudeRecovery w.observe`,
	}
	list, decision := allowed[fn.Name.Name]
	if !decision || !strings.HasPrefix(file, "claude_") {
		return
	}
	selectors := strings.Fields(list)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		selector, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		var source bytes.Buffer
		if err := printer.Fprint(&source, files, selector); err != nil {
			t.Fatal(err)
		}
		path := strings.Join(strings.Fields(source.String()), "")
		if fn.Name.Name == "dispatchClaudeRefresh" && !strings.HasPrefix(path, "w.") {
			return true
		}
		if !slices.Contains(selectors, path) {
			t.Errorf("%s uses an unlisted input or effect %s", fn.Name.Name, source.String())
		}
		return true
	})
}
