package summonoutcome

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// stringConsts parses one Go file and returns the string value of every
// constant whose name starts with prefix. It reads the source, so a constant
// added later is walked without editing this test.
func stringConsts(t *testing.T, path, prefix string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, spec := range g.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, prefix) || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				out = append(out, v)
			}
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatalf("no %s constants in %s", prefix, path)
	}
	return out
}

// closeOf is the close the operator writes for an unreported reason
// (internal/controller/summon_outcome.go, closeSummon): a person's stop and
// escalation-bound close failed; every other reason closes by the lane's
// onUnreported, which the CRD fixes to escalated for remediation and failed
// for every other lane.
func closeOf(lane v1alpha1.Lane, reason string) v1alpha1.OutcomeState {
	switch {
	case reason == ReasonStopped, reason == ReasonEscalationBound:
		return v1alpha1.OutcomeFailed
	case lane == v1alpha1.LaneRemediation:
		return v1alpha1.OutcomeEscalated
	default:
		return v1alpha1.OutcomeFailed
	}
}

// reportClose is the close a report writes (internal/apiserver/outcome.go):
// closed.reason is the verb; in lane remediation failed is stored escalated.
func reportClose(lane v1alpha1.Lane, verb string) v1alpha1.OutcomeState {
	switch verb {
	case "done":
		return v1alpha1.OutcomeDone
	case "escalate":
		return v1alpha1.OutcomeEscalated
	}
	if lane == v1alpha1.LaneRemediation {
		return v1alpha1.OutcomeEscalated
	}
	return v1alpha1.OutcomeFailed
}

// The table as DESIGN-064 6.8 writes it, with the 779 correction's row for
// stopped and ruling Q4's row for not-charged (story 782, coordinator rulings
// on item 742 comment 7457). A new reason with no entry here fails the test.
var wantUnreported = map[string]map[v1alpha1.Lane]TerminalEvent{}

func init() {
	cue := func(e TerminalEvent) map[v1alpha1.Lane]TerminalEvent {
		return map[v1alpha1.Lane]TerminalEvent{v1alpha1.LaneRemediation: TerminalDigest, v1alpha1.LaneCuration: e, v1alpha1.LaneUpgrade: e, v1alpha1.LaneEscalation: e}
	}
	for _, r := range []string{ReasonAgentExited, ReasonExitUnknown, ReasonTurnCap, ReasonStopped} {
		wantUnreported[r] = cue(TerminalDigest)
	}
	for _, r := range []string{ReasonSessionLost, ReasonHeartbeatLost, ReasonAgentInterrupted, ReasonPodTerminated} {
		wantUnreported[r] = cue(TerminalLost)
	}
	for _, r := range []string{ReasonTimedOut, ReasonDeadline, ReasonStale, ReasonEscalationBound, ReasonAgentFailed, ReasonNeverStarted, ReasonNoUsableModel, ReasonSuspendedUnreported, ReasonNotCharged} {
		wantUnreported[r] = cue(TerminalFailed)
	}
	for _, r := range []string{ReasonHoldExpired, ReasonQuotaHoldExpired} {
		wantUnreported[r] = cue(TerminalFailed)
		wantUnreported[r][v1alpha1.LaneCuration] = TerminalDigest
	}
}

// unreachable are the lane and reason cells no operator writes and no row of
// 6.8 names, each with the code that rules it out. They take the fallback
// (op782-q-1). Any other cell without a row fails the walk, so a new reason
// in reasons.go fails this test until it gets a row.
var unreachable = map[string]string{
	string(v1alpha1.LaneRemediation) + "/" + ReasonEscalationBound: "the escalation bound is a lane escalation watchdog row (internal/controller/summon_watchdog.go)",
}

// Proof 2 of story 782: every lane and every reason gives exactly one row,
// and that row is the one 6.8 and its dated rows name.
func TestTerminalTableExhaustiveAndExclusive(t *testing.T) {
	lanes := stringConsts(t, "../../api/v1alpha1/agentsession_types.go", "Lane")
	reasons := stringConsts(t, "reasons.go", "Reason")
	if len(lanes) != 4 || len(reasons) != 19 {
		t.Logf("lanes %v reasons %v", lanes, reasons) // informational: the walk below covers whatever is there
	}
	for _, l := range lanes {
		lane := v1alpha1.Lane(l)
		for _, reason := range reasons {
			state := closeOf(lane, reason)
			rows := matchingRows(lane, state, Unreported, reason)
			if why, ok := unreachable[l+"/"+reason]; ok {
				if e, named := TerminalRow(lane, state, Unreported, reason); len(rows) != 0 || named || e != TerminalFailed {
					t.Errorf("unreachable cell %s/%s (%s) has a row or no fallback", lane, reason, why)
				}
				continue
			}
			if len(rows) != 1 {
				t.Errorf("lane %s, %s, unreported, %s: %d rows match, want exactly one", lane, state, reason, len(rows))
				continue
			}
			want, ok := wantUnreported[reason][lane]
			if !ok {
				t.Errorf("reason %s in lane %s has no expected row in 6.8; add one to the table and to this test", reason, lane)
				continue
			}
			if got := TerminalPage(lane, state, Unreported, reason); got != want {
				t.Errorf("lane %s, %s, unreported, %s: %s, want %s", lane, state, reason, got, want)
			}
		}
		for _, verb := range []string{"done", "failed", "escalate"} {
			state := reportClose(lane, verb)
			if rows := matchingRows(lane, state, Report, verb); len(rows) != 1 {
				t.Errorf("lane %s, %s, report %s: %d rows match, want exactly one", lane, state, verb, len(rows))
			}
			want := TerminalDigest
			if lane != v1alpha1.LaneRemediation && verb == "failed" {
				want = TerminalFailed
			}
			if lane != v1alpha1.LaneRemediation && verb == "escalate" {
				want = TerminalEscalated
			}
			if got := TerminalPage(lane, state, Report, verb); got != want {
				t.Errorf("lane %s, report %s: %s, want %s", lane, verb, got, want)
			}
		}
	}
	// Every row names something the walk reaches, so no row is dead text.
	for i, r := range terminalRows {
		hit := false
		for _, l := range lanes {
			for _, reason := range append(append([]string{}, reasons...), "done", "failed", "escalate") {
				for _, src := range []string{Report, Unreported} {
					for _, st := range []v1alpha1.OutcomeState{v1alpha1.OutcomeDone, v1alpha1.OutcomeFailed, v1alpha1.OutcomeEscalated} {
						if r.matches(v1alpha1.Lane(l), st, src, reason) {
							hit = true
						}
					}
				}
			}
		}
		if !hit {
			t.Errorf("row %d matches nothing", i)
		}
	}
}

// A close no row names, which no operator writes today, pages failed (story
// 782, op782-q-1): a keeper older than its operator still pages.
func TestTerminalTableUnnamedCloseFails(t *testing.T) {
	for _, tc := range []struct {
		lane          v1alpha1.Lane
		state         v1alpha1.OutcomeState
		source, reson string
	}{
		{v1alpha1.LaneRemediation, v1alpha1.OutcomeFailed, Unreported, ReasonEscalationBound},
		{v1alpha1.LaneRemediation, v1alpha1.OutcomeFailed, Unreported, ReasonAgentExited},
		{v1alpha1.LaneCuration, v1alpha1.OutcomeEscalated, Unreported, ReasonSessionLost},
		{v1alpha1.LaneUpgrade, v1alpha1.OutcomeDone, Unreported, ReasonAgentExited},
		{v1alpha1.LaneUpgrade, v1alpha1.OutcomeFailed, Unreported, "a-reason-from-a-newer-operator"},
		{v1alpha1.Lane("a-new-lane"), v1alpha1.OutcomeDone, Report, "done"},
		{v1alpha1.LaneUpgrade, v1alpha1.OutcomeRunning, Report, "working"},
		{v1alpha1.LaneUpgrade, v1alpha1.OutcomeFailed, "other", ReasonAgentExited},
	} {
		if rows := matchingRows(tc.lane, tc.state, tc.source, tc.reson); len(rows) != 0 {
			t.Errorf("%+v matched %d rows; the case is meant to be unnamed", tc, len(rows))
		}
		if got := TerminalPage(tc.lane, tc.state, tc.source, tc.reson); got != TerminalFailed {
			t.Errorf("%+v: %s, want failed", tc, got)
		}
	}
}

func TestClosedTerminalPage(t *testing.T) {
	at := metav1.Now()
	s := &v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{Caller: "c", Lane: v1alpha1.LaneUpgrade}}
	if _, ok := ClosedTerminalPage(s); ok {
		t.Fatal("an open session has a terminal page")
	}
	s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeFailed}
	if _, ok := ClosedTerminalPage(s); ok {
		t.Fatal("a terminal outcome without the close record has a terminal page")
	}
	s.Status.Summon = &v1alpha1.SummonStatus{Closed: &v1alpha1.SummonClosedStatus{At: at, Source: Unreported, Reason: ReasonPodTerminated}}
	if e, ok := ClosedTerminalPage(s); !ok || e != TerminalLost {
		t.Fatalf("got %s %v", e, ok)
	}
	s.Status.Outcome.State = v1alpha1.OutcomeRunning
	if _, ok := ClosedTerminalPage(s); ok {
		t.Fatal("a running outcome has a terminal page")
	}
	if LabelSummoned != "dev-env.haynesops.com/summoned" || LabelSummonedValue != "true" {
		t.Fatal("the summoned label changed; the keeper's selector and haynes-ops follow it")
	}
}
