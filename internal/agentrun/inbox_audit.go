package agentrun

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// outputAudit is the readable metadata rendering of one inbox list page.
// Only the list verb registers it.
const outputAudit = "audit"

// inboxAuditFooter is fixed copy. An acknowledgement is an API record, not
// provider consumption or an approval.
const inboxAuditFooter = "Replies are information. API acknowledgement does not prove provider consumption or approval."

// inboxAuditMaxItems is the existing inbox list page cap. The printer does
// not read past one page.
const inboxAuditMaxItems = 100

// printInboxAudit writes one list page. The list command calls it only after
// the existing bounded GET and its checks. It does not fetch another page,
// read message text, acknowledge, or derive state from a clock.
func printInboxAudit(w io.Writer, list apiv1.InboxList) error {
	if len(list.Items) > inboxAuditMaxItems || len(list.Continue) > 4096 {
		return fail(ExitFailed, "inbox list returned more than the requested limit")
	}
	var b strings.Builder
	for i, item := range list.Items {
		if i > 0 {
			b.WriteByte('\n')
		}
		writeInboxAuditItem(&b, item)
	}
	if len(list.Items) > 0 {
		b.WriteByte('\n')
	}
	if list.Continue != "" {
		fmt.Fprintf(&b, "Continue: %q\n", list.Continue)
	}
	fmt.Fprintf(&b, "Messages: %d\n\n%s\n", len(list.Items), inboxAuditFooter)
	_, err := io.WriteString(w, b.String())
	return err
}

func writeInboxAuditItem(b *strings.Builder, m apiv1.InboxMetadata) {
	fmt.Fprintf(b, "Message: %q\n", m.Name)
	fmt.Fprintf(b, "UID: %q\n", m.UID)
	fmt.Fprintf(b, "Kind: %s\n", auditKind(m.Kind))
	fmt.Fprintf(b, "Sender: %s\n", auditOwner(m.Sender))
	fmt.Fprintf(b, "Recipient: %s\n", auditOwner(m.Recipient))
	state, question, reply, waiting, observed, stale := auditThread(m)
	fmt.Fprintf(b, "State: %s\n", state)
	fmt.Fprintf(b, "Question: %s\n", question)
	fmt.Fprintf(b, "Reply: %s\n", reply)
	fmt.Fprintf(b, "Waiting on: %s\n", waiting)
	fmt.Fprintf(b, "Observed at: %s\n", observed)
	fmt.Fprintf(b, "Stale after: %s\n", stale)
}

// auditKind prints a known kind. Anything else, including an omitted kind, is
// Unknown. Plain is not given a thread state.
func auditKind(kind string) string {
	switch kind {
	case apiv1.InboxKindPlain, apiv1.InboxKindQuestion, apiv1.InboxKindReply:
		return strconv.Quote(kind)
	default:
		return "Unknown"
	}
}

// auditThread shows server state only when the kind is question or reply and
// the correlation refs agree. A missing or contradictory thread is Unknown.
// It does not turn Stale into another state or treat Acknowledged as approval.
func auditThread(m apiv1.InboxMetadata) (state, question, reply, waiting, observed, stale string) {
	if !auditThreadTrusted(m) {
		return "Unknown", "Unknown", "Unknown", "Unknown", "Unknown", "Unknown"
	}
	t := m.Thread
	state = strconv.Quote(t.State)
	question = auditRef(t.Question)
	reply = "Unknown"
	if t.Reply != nil {
		reply = auditRef(*t.Reply)
	}
	waiting = "Unknown"
	if t.WaitingOn != nil {
		waiting = auditOwner(*t.WaitingOn)
	}
	observed = "Unknown"
	if !t.ObservedAt.IsZero() {
		observed = strconv.Quote(t.ObservedAt.Format(time.RFC3339Nano))
	}
	stale = "Unknown"
	if t.StaleAfterSeconds > 0 {
		stale = strconv.Quote(strconv.FormatInt(t.StaleAfterSeconds, 10))
	}
	return state, question, reply, waiting, observed, stale
}

func auditThreadTrusted(m apiv1.InboxMetadata) bool {
	t := m.Thread
	if t == nil || !inboxThreadStates[t.State] {
		return false
	}
	switch m.Kind {
	case apiv1.InboxKindQuestion:
		// A question does not name another message.
		return m.InReplyTo == nil && auditNamesMessage(t.Question, m.Name, m.UID)
	case apiv1.InboxKindReply:
		if m.InReplyTo == nil || t.Reply == nil {
			return false
		}
		return auditRefsAgree(*m.InReplyTo, t.Question) && auditNamesMessage(*t.Reply, m.Name, m.UID)
	default:
		return false
	}
}

// auditNamesMessage reports whether ref names this message. UIDs constrain
// the match only when both are present; an absent UID is not invented.
func auditNamesMessage(ref apiv1.InboxMessageRef, name, uid string) bool {
	if ref.Name == "" || ref.Name != name {
		return false
	}
	return auditUIDsAgree(ref.UID, uid)
}

// auditRefsAgree reports whether two refs are the same message. Names must
// both be present and equal. UIDs must be equal when both are present.
func auditRefsAgree(a, b apiv1.InboxMessageRef) bool {
	if a.Name == "" || a.Name != b.Name {
		return false
	}
	return auditUIDsAgree(a.UID, b.UID)
}

func auditUIDsAgree(a, b string) bool {
	if a == "" || b == "" {
		return true
	}
	return a == b
}

// auditOwner quotes every namespace, name and UID that the wire carried.
func auditOwner(o apiv1.TaskClaimOwner) string {
	parts := make([]string, 0, 3)
	if o.Namespace != "" {
		parts = append(parts, "namespace="+strconv.Quote(o.Namespace))
	}
	if o.Name != "" {
		parts = append(parts, "name="+strconv.Quote(o.Name))
	}
	if o.UID != "" {
		parts = append(parts, "uid="+strconv.Quote(o.UID))
	}
	if len(parts) == 0 {
		return "Unknown"
	}
	return strings.Join(parts, " ")
}

// auditRef quotes every name and UID present on the ref.
func auditRef(r apiv1.InboxMessageRef) string {
	parts := make([]string, 0, 2)
	if r.Name != "" {
		parts = append(parts, "name="+strconv.Quote(r.Name))
	}
	if r.UID != "" {
		parts = append(parts, "uid="+strconv.Quote(r.UID))
	}
	if len(parts) == 0 {
		return "Unknown"
	}
	return strings.Join(parts, " ")
}
