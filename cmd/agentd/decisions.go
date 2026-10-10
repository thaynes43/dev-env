package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func askDecision(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) != 0 {
		return exitUsage
	}
	s, err := agentd.LoadSettings(getenv)
	if err != nil {
		return exitFailure
	}
	question, err := protocol.ReadDecisionQuestion(stdin)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "agentd: invalid bounded decision question")
		return exitUsage
	}
	record, err := agentd.AskDecision(ctx, s, question, time.Now())
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "agentd: decision admission refused")
		return exitFailure
	}
	if json.NewEncoder(stdout).Encode(record) != nil {
		return exitFailure
	}
	return exitOK
}

func decisionCtl(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("ctl decision", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	podUID := fs.String("expected-pod-uid", "", "")
	sessionUID := fs.String("expected-session-uid", "", "")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *podUID == "" || *sessionUID == "" {
		return exitUsage
	}
	s, err := agentd.LoadSettings(getenv)
	if err != nil {
		return exitFailure
	}
	sess, err := agentd.LoadSession(getenv)
	if err != nil || s.PodUID != *podUID || sess.SessionUID != *sessionUID {
		_, _ = fmt.Fprintln(stderr, "agentd: target identity does not match")
		return exitFailure
	}
	var result protocol.DecisionResult
	if args[0] == "decision-read" {
		result, err = agentd.ReadDecision(ctx, s)
	} else {
		var answer protocol.DecisionAnswer
		answer, err = protocol.ReadDecisionAnswer(stdin)
		if err == nil {
			result, err = agentd.AnswerDecision(ctx, s, answer, time.Now())
		}
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "agentd: exact decision is unavailable or uncertain")
		return exitFailure
	}
	if json.NewEncoder(stdout).Encode(result) != nil {
		return exitFailure
	}
	return exitOK
}
