package coordretention

import (
	"context"
	"fmt"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	devv1 "github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/inbox"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Decision is eligibility only. Released never authorizes a mutation.
type Decision string

const (
	Released Decision = "Released"
	Kept     Decision = "Kept"
	Unknown  Decision = "Unknown"
)

// ArchiveEvidence is the narrow bulk-lookup seam for InboxReadAck evaluation.
// An absent map entry means the later archive lookup has not supplied proof.
type ArchiveEvidence struct{ Known, Holds bool }
type EvalConfig struct {
	Reader               client.Reader
	OperatorNamespace    string
	ReconciledNamespaces map[string]bool
	Archive              map[MessageRef]ArchiveEvidence
}
type Result struct {
	Summary         Summary
	Decision        Decision
	RequiredMessage *MessageRef
}
type sessionAnswer uint8

const (
	sessionUnknown sessionAnswer = iota
	sessionAbsent
	sessionPresent
)

type memoSession struct {
	answer sessionAnswer
	uid    string
}

// Evaluate evaluates a pass with a fresh session memo. It performs only Get reads.
func Evaluate(ctx context.Context, c EvalConfig, summaries []Summary) []Result {
	memo := map[string]memoSession{}
	return evaluate(ctx, c, summaries, memo)
}
func evaluate(ctx context.Context, c EvalConfig, summaries []Summary, memo map[string]memoSession) []Result {
	out := make([]Result, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, Result{Summary: s, Decision: decide(ctx, c, s, memo)})
	}
	for i := range out {
		if out[i].Decision == Unknown && out[i].Summary.Kind == InboxReadAck && out[i].Summary.Valid {
			m := out[i].Summary.Message
			if _, ok := c.Archive[m]; !ok {
				out[i].RequiredMessage = &m
			}
		}
	}
	return out
}
func session(ctx context.Context, c EvalConfig, r SessionRef, memo map[string]memoSession) sessionAnswer {
	if c.Reader == nil || !c.ReconciledNamespaces[r.Namespace] || !inbox.ValidSession(coord.SessionIdentity{Namespace: r.Namespace, Name: r.Name, UID: r.UID}) {
		return sessionUnknown
	}
	k := r.Namespace + "\x00" + r.Name
	if a, ok := memo[k]; ok {
		if a.answer == sessionPresent && a.uid != r.UID {
			return sessionAbsent
		}
		return a.answer
	}
	var got devv1.AgentSession
	err := c.Reader.Get(ctx, client.ObjectKey{Namespace: r.Namespace, Name: r.Name}, &got)
	if apierrors.IsNotFound(err) {
		memo[k] = memoSession{answer: sessionAbsent}
		return sessionAbsent
	}
	if err != nil {
		memo[k] = memoSession{answer: sessionUnknown}
		return sessionUnknown
	}
	memo[k] = memoSession{answer: sessionPresent, uid: string(got.UID)}
	if string(got.UID) != r.UID {
		return sessionAbsent
	}
	return sessionPresent
}
func absentFor(ctx context.Context, c EvalConfig, r SessionRef, memo map[string]memoSession) Decision {
	a := session(ctx, c, r, memo)
	if a == sessionUnknown {
		return Unknown
	}
	if a == sessionPresent {
		return Kept
	}
	return Released
}
func decide(ctx context.Context, c EvalConfig, s Summary, memo map[string]memoSession) Decision {
	if c.Reader == nil || !s.Valid || s.Namespace != c.OperatorNamespace {
		return Unknown
	}
	switch s.Kind {
	case TaskClaim:
		return absentFor(ctx, c, s.Owner, memo)
	case PublishedContext:
		return absentFor(ctx, c, s.Source, memo)
	case InboxMessage:
		if d := absentFor(ctx, c, s.Sender, memo); d != Released {
			return d
		}
		var a coord.InboxReadAck
		err := c.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: inbox.AckName(s.UID, s.Recipient.UID)}, &a)
		if err == nil {
			if !inbox.ValidAck(a.Spec) || a.Spec.Project != s.Project || a.Spec.MessageName != s.Name || a.Spec.MessageUID != s.UID || a.Spec.Recipient != (coord.SessionIdentity{Namespace: s.Recipient.Namespace, Name: s.Recipient.Name, UID: s.Recipient.UID}) {
				return Unknown
			}
			return Released
		}
		if !apierrors.IsNotFound(err) {
			return Unknown
		}
		return absentFor(ctx, c, s.Recipient, memo)
	case InboxReadAck:
		var m coord.InboxMessage
		err := c.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Message.Name}, &m)
		if err == nil && string(m.UID) == s.Message.UID {
			return Kept
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return Unknown
		}
		e, ok := c.Archive[s.Message]
		if !ok {
			return Unknown
		}
		if !e.Known {
			return Unknown
		}
		if !e.Holds {
			return Kept
		}
		return Released
	default:
		return Unknown
	}
}

func (s Summary) String() string { return fmt.Sprintf("%s/%s/%s", s.Kind, s.Project, s.Name) }
