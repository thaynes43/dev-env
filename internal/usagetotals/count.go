package usagetotals

import (
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const (
	// AnnotationCounted is the leader's mark that this Session was added.
	// A non-empty value means the Session is already counted. The value records
	// the observation that was added, so a later observation is not added again.
	//
	//	observation/<64-hex usage observation id>
	//	generation/<metadata.generation>
	//
	// generation is used when the Session has no 64-hex observation id, which
	// is a Cursor task result with no usage observation.
	AnnotationCounted = "dev-env.haynesops.com/task-usage-counted"

	labelOther      = "other"
	stateReported   = "Reported"
	stateFinal      = "Final"
	kindUnknown     = "Unknown"
	kindEstimate    = "ProviderCLIEquivalentEstimate"
	tokenInput      = "input"
	tokenOutput     = "output"
	tokenCacheRead  = "cache_read"
	tokenCacheWrite = "cache_write"
)

// labelName is the bound for a model or repository label. Anything else is
// labelOther, so a long or odd value cannot open a new series.
var labelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

var observationID = regexp.MustCompile(`^[0-9a-f]{64}$`)

type tokenAdd struct {
	model, repository, kind, token string
	amount                         float64
}

type costAdd struct {
	model, repository, kind string
	usd                     float64
}

type contribution struct {
	mark    string
	agent   string
	model   string
	seconds float64
	tokens  []tokenAdd
	costs   []costAdd
}

func boundLabel(v string) string {
	if labelName.MatchString(v) {
		return v
	}
	return labelOther
}

func boundAgent(agent v1alpha1.AgentKind) string {
	switch agent {
	case v1alpha1.AgentClaude, v1alpha1.AgentCodex, v1alpha1.AgentCursor:
		return string(agent)
	default:
		return labelOther
	}
}

func boundKind(kind string) string {
	switch kind {
	case kindUnknown, kindEstimate:
		return kind
	default:
		return labelOther
	}
}

// contributionOf reports what one reconcile adds, and whether the Session is
// ready to count. Cursor is ready when its task result is recorded, and then
// only sessions and seconds are added. Any other agent is ready when its usage
// observation is final.
func contributionOf(s *v1alpha1.AgentSession) (contribution, bool) {
	obs := s.Status.UsageObservation
	if s.Spec.Agent == v1alpha1.AgentCursor {
		if s.Status.TaskResult == nil {
			return contribution{}, false
		}
	} else if obs == nil || obs.ResultState != stateFinal {
		return contribution{}, false
	}
	con := contribution{
		mark:    markFor(s),
		agent:   boundAgent(s.Spec.Agent),
		model:   boundLabel(requestedModel(s)),
		seconds: wallSeconds(s),
	}
	if s.Spec.Agent != v1alpha1.AgentCursor && obs != nil {
		con.tokens, con.costs = usageSamples(s, obs)
	}
	return con, true
}

func requestedModel(s *v1alpha1.AgentSession) string {
	if obs := s.Status.UsageObservation; obs != nil && obs.Source.RequestedModel != "" {
		return obs.Source.RequestedModel
	}
	return s.Spec.Model
}

func markFor(s *v1alpha1.AgentSession) string {
	if obs := s.Status.UsageObservation; obs != nil && observationID.MatchString(obs.ID) {
		return "observation/" + obs.ID
	}
	return "generation/" + strconv.FormatInt(s.Generation, 10)
}

func wallSeconds(s *v1alpha1.AgentSession) float64 {
	if s.Status.Agent == nil || s.Status.Agent.Task == nil {
		return 0
	}
	start := s.CreationTimestamp.Time
	fin := s.Status.Agent.Task.FinishedAt.Time
	if start.IsZero() || fin.IsZero() || !fin.After(start) {
		return 0
	}
	return float64(fin.Sub(start) / time.Second)
}

func usageSamples(s *v1alpha1.AgentSession, obs *v1alpha1.UsageObservationStatus) ([]tokenAdd, []costAdd) {
	repo := boundLabel(s.Spec.Repo)
	if len(obs.ServedModels) > 0 {
		var tokens []tokenAdd
		var costs []costAdd
		for i := range obs.ServedModels {
			m := &obs.ServedModels[i]
			model := boundLabel(m.Model)
			kind := boundKind(m.Cost.Kind)
			tokens = appendTokens(tokens, model, repo, kind, m.InputTokens, m.OutputTokens, m.CacheReadInputTokens, m.CacheCreationInputTokens)
			if usd, ok := reportedUSD(m.Cost); ok {
				costs = append(costs, costAdd{model: model, repository: repo, kind: kind, usd: usd})
			}
		}
		return tokens, costs
	}
	model := boundLabel(requestedModel(s))
	kind := boundKind(obs.Cost.Kind)
	tokens := appendTokens(nil, model, repo, kind, obs.InputTokens, obs.OutputTokens, obs.CacheReadInputTokens, obs.CacheCreationInputTokens)
	var costs []costAdd
	if usd, ok := reportedUSD(obs.Cost); ok {
		costs = append(costs, costAdd{model: model, repository: repo, kind: kind, usd: usd})
	}
	return tokens, costs
}

func appendTokens(dst []tokenAdd, model, repo, kind string, input, output, cacheRead, cacheWrite v1alpha1.TokenObservationStatus) []tokenAdd {
	dst = appendToken(dst, model, repo, kind, tokenInput, input)
	dst = appendToken(dst, model, repo, kind, tokenOutput, output)
	dst = appendToken(dst, model, repo, kind, tokenCacheRead, cacheRead)
	return appendToken(dst, model, repo, kind, tokenCacheWrite, cacheWrite)
}

func appendToken(dst []tokenAdd, model, repo, kind, token string, obs v1alpha1.TokenObservationStatus) []tokenAdd {
	amount, ok := reportedTokens(obs)
	if !ok {
		return dst
	}
	return append(dst, tokenAdd{model: model, repository: repo, kind: kind, token: token, amount: amount})
}

func reportedTokens(obs v1alpha1.TokenObservationStatus) (float64, bool) {
	if obs.State != stateReported || obs.Amount == nil || *obs.Amount <= 0 {
		return 0, false
	}
	return float64(*obs.Amount), true
}

func reportedUSD(obs v1alpha1.CostObservationStatus) (float64, bool) {
	if obs.State != stateReported {
		return 0, false
	}
	v, err := strconv.ParseFloat(obs.Amount, 64)
	if err != nil || v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}
