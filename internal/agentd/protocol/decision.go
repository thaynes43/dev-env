package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var decisionID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func ValidDecisionID(id string) bool { return decisionID.MatchString(id) }

const (
	MaxDecisionInputBytes  = 16 << 10
	MaxDecisionRecordBytes = 32 << 10
	MaxDecisionAnswerBytes = 4 << 10
)

// DecisionQuestion is the only input the task may supply. Platform identity,
// record paths, native threads and writer generations are never caller fields.
type DecisionQuestion struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	Context  string   `json:"context,omitempty"`
}

type DecisionAnswer struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// DecisionRecord is private task state, read by the exact direct parent. Its
// identity is assigned by agentd from the platform's saved current launch.
type DecisionRecord struct {
	Version          int              `json:"version"`
	ID               string           `json:"id"`
	Session          string           `json:"session"`
	SessionUID       string           `json:"sessionUID"`
	PodUID           string           `json:"podUID"`
	ThreadID         string           `json:"threadID"`
	WriterGeneration uint64           `json:"writerGeneration"`
	Question         DecisionQuestion `json:"question"`
	State            string           `json:"state"`
	CreatedAt        time.Time        `json:"createdAt"`
	AnsweredAt       *time.Time       `json:"answeredAt,omitempty"`
	Answer           string           `json:"answer,omitempty"`
}

type DecisionResult struct {
	Decision *DecisionRecord `json:"decision,omitempty"`
}

// DecisionOutcome uses the existing Outcome escalated state for discovery.
// Questions, choices, answers and private context stay in the private record.
type DecisionOutcome struct {
	ID               string    `json:"id"`
	SessionUID       string    `json:"sessionUID"`
	PodUID           string    `json:"podUID"`
	WriterGeneration uint64    `json:"writerGeneration"`
	ThreadID         string    `json:"threadID,omitempty"`
	State            string    `json:"state,omitempty"`
	AnswerDigest     string    `json:"answerDigest,omitempty"`
	At               time.Time `json:"at"`
}

func decisionTextValid(text string, bound int, nonempty bool) bool {
	if !utf8.ValidString(text) || len(text) > bound || (nonempty && strings.TrimSpace(text) == "") {
		return false
	}
	for _, r := range text {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
	}
	return true
}

func (q DecisionQuestion) Validate() error {
	if !decisionTextValid(q.Question, 2<<10, true) || !decisionTextValid(q.Context, 8<<10, false) || len(q.Options) > 8 {
		return errors.New("decision question exceeds its text or option bounds")
	}
	for _, option := range q.Options {
		if !decisionTextValid(option, 256, true) {
			return errors.New("decision option is empty or exceeds its text bound")
		}
	}
	return nil
}

func (a DecisionAnswer) Validate() error {
	if !decisionTextValid(a.Text, MaxDecisionAnswerBytes, true) || !ValidDecisionID(a.ID) {
		return errors.New("decision answer has invalid identity or text")
	}
	return nil
}

func (r DecisionRecord) Validate() error {
	if r.Version != 1 || !ValidDecisionID(r.ID) || !ValidDecisionID(r.ThreadID) || r.Session == "" || r.SessionUID == "" || r.PodUID == "" ||
		r.WriterGeneration == 0 || r.WriterGeneration > math.MaxInt64 || r.CreatedAt.IsZero() || r.Question.Validate() != nil {
		return errors.New("decision record lacks exact bounded platform identity")
	}
	switch r.State {
	case "Open":
		if r.Answer != "" || r.AnsweredAt != nil {
			return errors.New("open decision has contradictory answer state")
		}
	case "Answered", "ResumeStarting", "Dispatching", "Delivered", "Uncertain":
		if (DecisionAnswer{ID: r.ID, Text: r.Answer}).Validate() != nil || r.AnsweredAt == nil || r.AnsweredAt.IsZero() || r.AnsweredAt.Before(r.CreatedAt) {
			return errors.New("answered decision lacks its durable answer")
		}
	default:
		return errors.New("decision record has unknown state")
	}
	return nil
}

func ReadDecisionQuestion(r io.Reader) (DecisionQuestion, error) {
	var q DecisionQuestion
	if err := readDecisionJSON(r, MaxDecisionInputBytes, &q); err != nil {
		return q, err
	}
	return q, q.Validate()
}

func ReadDecisionAnswer(r io.Reader) (DecisionAnswer, error) {
	var a DecisionAnswer
	if err := readDecisionJSON(r, MaxDecisionRecordBytes, &a); err != nil {
		return a, err
	}
	return a, a.Validate()
}

func readDecisionJSON(r io.Reader, bound int64, target any) error {
	b, err := io.ReadAll(io.LimitReader(r, bound+1))
	if err != nil || int64(len(b)) > bound {
		return errors.New("decision input is unreadable or exceeds its bound")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("decision input is invalid or claims unsupported fields")
	}
	return nil
}

// DecisionAuthority is operator-owned metadata. It carries no question or answer
// text. Confirmed proves a configured direct parent recorded this exact answer;
// it is not an attestation that a phone owner supplied it.
type DecisionAuthority struct {
	Version          int    `json:"version"`
	Session          string `json:"session"`
	SessionUID       string `json:"sessionUID"`
	PodUID           string `json:"podUID"`
	DecisionID       string `json:"decisionID"`
	ThreadID         string `json:"threadID"`
	WriterGeneration int64  `json:"writerGeneration"`
	Digest           string `json:"digest"`
	Phase            string `json:"phase"`
}

type DecisionAuthorityResult struct {
	Authority *DecisionAuthority `json:"authority,omitempty"`
}

func DecisionAuthorityPath(session string) string {
	return "/v1/sessions/" + session + "/decision-authority"
}

// AnswerAuthority uses a versioned domain and JSON's unambiguous field/length
// encoding. Options order, context, creation time, all identity and answer text
// are covered; state and local delivery timestamps are deliberately excluded.
func AnswerAuthority(r DecisionRecord, answer string) DecisionAuthority {
	payload := struct {
		Domain                                    string
		Version                                   int
		ID, Session, SessionUID, PodUID, ThreadID string
		WriterGeneration                          uint64
		Question                                  DecisionQuestion
		CreatedAt                                 time.Time
		Answer                                    string
	}{"dev-env/child-decision-answer/v1", r.Version, r.ID, r.Session, r.SessionUID, r.PodUID, r.ThreadID, r.WriterGeneration, r.Question, r.CreatedAt.UTC(), answer}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return DecisionAuthority{1, r.Session, r.SessionUID, r.PodUID, r.ID, r.ThreadID, int64(r.WriterGeneration), hex.EncodeToString(sum[:]), "Confirmed"}
}

func (a DecisionAuthority) Confirms(r DecisionRecord) bool {
	return r.Validate() == nil && r.State != "Open" && a == AnswerAuthority(r, r.Answer)
}
