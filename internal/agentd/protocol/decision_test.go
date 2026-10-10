package protocol

import (
	"strings"
	"testing"
)

func TestDecisionInputBoundsAndPlatformIdentityClaims(t *testing.T) {
	for _, raw := range []string{
		`{"question":"synthetic?","sessionUID":"forged"}`,
		`{"question":"synthetic?","threadID":"forged"}`,
		`{"question":"synthetic?","path":"/tmp/forged"}`,
		`{"question":"synthetic?","writerGeneration":1}`,
		`{"question":"synthetic?"} {"question":"second"}`,
		`{"question":"` + strings.Repeat("x", (2<<10)+1) + `"}`,
		`{"question":"synthetic?","context":"` + strings.Repeat("x", (8<<10)+1) + `"}`,
		`{"question":"synthetic\u001b[201~"}`,
		strings.Repeat(" ", MaxDecisionInputBytes+1),
	} {
		if _, err := ReadDecisionQuestion(strings.NewReader(raw)); err == nil {
			t.Fatal("decision input accepted an identity claim, control, extra document or excessive text")
		}
	}
	q, err := ReadDecisionQuestion(strings.NewReader(`{"question":"synthetic?","options":["A","B"],"context":"exact\ncontext"}`))
	if err != nil || len(q.Options) != 2 || q.Context != "exact\ncontext" {
		t.Fatal("bounded question changed its context", err)
	}
}

func TestDecisionAnswerPreservesTextAndRefusesIdentityExtras(t *testing.T) {
	const id = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	valid := `{"id":"` + id + `","text":"A\nsecond line"}`
	a, err := ReadDecisionAnswer(strings.NewReader(valid))
	if err != nil || a.Text != "A\nsecond line" {
		t.Fatal("answer lost its exact recorded text", err)
	}
	for _, raw := range []string{
		`{"id":"` + id + `","text":"A","sessionUID":"forged"}`,
		`{"id":"` + strings.Repeat("x", 36) + `","text":"A"}`,
		`{"id":"` + id + `","text":"\u001b[201~"}`,
		`{"id":"` + id + `","text":"` + strings.Repeat("x", MaxDecisionAnswerBytes+1) + `"}`,
	} {
		if _, err := ReadDecisionAnswer(strings.NewReader(raw)); err == nil {
			t.Fatal("answer accepted invalid bounded input")
		}
	}
}

func TestDecisionFiniteQuestionBounds(t *testing.T) {
	q := DecisionQuestion{Question: strings.Repeat("q", 2<<10), Context: strings.Repeat("c", 8<<10), Options: []string{strings.Repeat("o", 256)}}
	if q.Validate() != nil {
		t.Fatal("exact bounded question refused")
	}
	q.Options[0] += "x"
	if q.Validate() == nil {
		t.Fatal("option over256 bytes accepted")
	}
	q.Options = []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	if q.Validate() != nil {
		t.Fatal("eight bounded options refused")
	}
	q.Options = append(q.Options, "i")
	if q.Validate() == nil {
		t.Fatal("ninth option accepted")
	}
}
