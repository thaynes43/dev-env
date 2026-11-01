package apiv1

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Wire checks for question-audit metadata. They mirror the inbox and task-claim
// bounds for the allowlisted fields. They are not a second store, lifecycle or
// policy framework. Message text, keys and provider output are not accepted here
// because those fields are not on the type.

var (
	portalQuestionName    = regexp.MustCompile(`^message\.[a-f0-9]{32}\.[a-f0-9]{32}$`)
	portalQuestionUID     = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
	portalQuestionLabel   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	portalQuestionDNS     = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	portalQuestionProject = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	portalQuestionDigest  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	portalQuestionHead    = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)
	portalQuestionGitHub  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,37}[a-z0-9])?/[a-z0-9_.-]{1,100}$`)
)

// PortalQuestionCursorOK is the store continuation token the route accepts.
// The consumer does not send one.
func PortalQuestionCursorOK(s string) bool {
	if len(s) < 1 || len(s) > 4096 {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// PortalQuestionAckActorMatches reports whether actor is the recorded recipient
// session. The pod name and service-account namespace must be that session's,
// and the kind must be session. A claimed coordinator origin does not match.
func PortalQuestionAckActorMatches(actor TaskClaimActor, session TaskClaimOwner) bool {
	if !portalQuestionActorOK(actor) || !portalQuestionOwnerOK(session) {
		return false
	}
	ns, _, ok := strings.Cut(actor.ServiceAccount, "/")
	return ok && ns == session.Namespace && actor.PodName == session.Name
}

// PortalQuestionAuditItemOK reports whether item is one trusted question or
// reply. A substituted question reference, a reply that does not name itself,
// a plain message, or an acknowledgement that disagrees with the thread is
// not trusted.
func PortalQuestionAuditItemOK(item PortalQuestionAuditItem) bool {
	if !portalQuestionName.MatchString(item.Name) || !portalQuestionUID.MatchString(item.UID) || item.CreatedAt.IsZero() {
		return false
	}
	if !portalQuestionFixed(item.Origin, item.Approval, item.ProviderConsumption, item.ServedModel) {
		return false
	}
	if !portalQuestionOwnerOK(item.Sender) || !portalQuestionOwnerOK(item.Recipient) || !portalQuestionActorOK(item.Actor) || !portalQuestionProvenanceOK(item.Provenance) {
		return false
	}
	if item.Thread == nil || !portalQuestionState(item.Thread.State) || item.Thread.StaleAfterSeconds != PortalQuestionAuditStaleAfterSeconds || item.Thread.ObservedAt.IsZero() {
		return false
	}
	if item.Thread.Replies != 0 && (item.Thread.Replies < 2 || item.Thread.Replies > 4096) {
		return false
	}
	if item.Thread.WaitingOn != nil && !portalQuestionOwnerOK(*item.Thread.WaitingOn) {
		return false
	}
	if !portalQuestionRefOK(item.Thread.Question) {
		return false
	}
	if item.Thread.Reply != nil && !portalQuestionRefOK(*item.Thread.Reply) {
		return false
	}
	switch item.Kind {
	case InboxKindQuestion:
		if item.InReplyTo != nil || item.Thread.Question.Name != item.Name || item.Thread.Question.UID != item.UID {
			return false
		}
		if item.Thread.Reply != nil && item.Thread.Reply.Name == item.Name && item.Thread.Reply.UID == item.UID {
			return false
		}
	case InboxKindReply:
		if item.InReplyTo == nil || !portalQuestionRefOK(*item.InReplyTo) || item.Thread.Reply == nil {
			return false
		}
		if item.Thread.Reply.Name != item.Name || item.Thread.Reply.UID != item.UID {
			return false
		}
		if item.InReplyTo.Name != item.Thread.Question.Name || item.InReplyTo.UID != item.Thread.Question.UID {
			return false
		}
		if item.Thread.Question.Name == item.Name && item.Thread.Question.UID == item.UID {
			return false
		}
	default:
		return false
	}
	switch item.Acknowledgement.Status {
	case PortalQuestionAckUnavailable:
		if item.Thread.State == InboxStateAcknowledged || item.Acknowledgement.Actor != nil || item.Acknowledgement.CreatedAt != nil {
			return false
		}
	case PortalQuestionAckRecorded:
		if item.Thread.State != InboxStateAcknowledged || item.Acknowledgement.Actor == nil || item.Acknowledgement.CreatedAt == nil || item.Acknowledgement.CreatedAt.IsZero() {
			return false
		}
		if item.Acknowledgement.CreatedAt.Before(item.CreatedAt) {
			return false
		}
		subject := item.Recipient
		if item.Kind == InboxKindQuestion {
			subject = item.Sender
		}
		if !PortalQuestionAckActorMatches(*item.Acknowledgement.Actor, subject) {
			return false
		}
	default:
		return false
	}
	return true
}

func portalQuestionFixed(origin, approval, consumption, served string) bool {
	return origin == PortalQuestionOriginUnknown && approval == PortalQuestionApprovalNotRecorded && consumption == PortalQuestionConsumptionNotRecorded && served == PortalQuestionServedModelUnknown
}

func portalQuestionState(state string) bool {
	switch state {
	case InboxStateUnanswered, InboxStateAnswered, InboxStateAcknowledged, InboxStateStale, InboxStateRecipientEnded, InboxStateUnknown:
		return true
	default:
		return false
	}
}

func portalQuestionRefOK(ref InboxMessageRef) bool {
	return portalQuestionName.MatchString(ref.Name) && portalQuestionUID.MatchString(ref.UID)
}

func portalQuestionOwnerOK(o TaskClaimOwner) bool {
	return portalQuestionLabel.MatchString(o.Namespace) && portalQuestionDNS.MatchString(o.Name) && len(o.Name) <= 253 && portalQuestionUID.MatchString(o.UID)
}

func portalQuestionActorOK(a TaskClaimActor) bool {
	ns, name, ok := strings.Cut(a.ServiceAccount, "/")
	return a.Kind == "session" && ok && portalQuestionLabel.MatchString(ns) && portalQuestionDNS.MatchString(name) && len(name) <= 253 && portalQuestionDNS.MatchString(a.PodName) && len(a.PodName) <= 253 && portalQuestionUID.MatchString(a.PodUID)
}

func portalQuestionProvenanceOK(p TaskClaimProvenance) bool {
	if !portalQuestionDigest.MatchString(p.CatalogRevision) || !portalQuestionDigest.MatchString(p.RulesRevision) || !portalQuestionGitHubOK(p.GitHub) || !portalQuestionBranchOK(p.DefaultBranch) || p.InitialPinState != "Unknown" {
		return false
	}
	if (p.ReportedHead == "") != (p.ReportedAt == nil) {
		return false
	}
	if p.ReportedHead != "" && (!portalQuestionHead.MatchString(p.ReportedHead) || p.ReportedAt.IsZero()) {
		return false
	}
	return true
}

func portalQuestionGitHubOK(identity string) bool {
	if len(identity) > 140 || !portalQuestionGitHub.MatchString(identity) {
		return false
	}
	owner, _, _ := strings.Cut(identity, "/")
	return !strings.Contains(owner, "--") && !strings.HasSuffix(identity, "/.") && !strings.HasSuffix(identity, "/..") && !strings.HasSuffix(identity, ".git")
}

func portalQuestionBranchOK(branch string) bool {
	if branch == "" || !utf8.ValidString(branch) || len(branch) > 255 || branch == "HEAD" || branch == "@" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "refs/") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.ContainsAny(branch, " ~^:?*[\\") {
		return false
	}
	for _, ch := range branch {
		if ch < 0x20 || ch == 0x7f {
			return false
		}
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

// PortalQuestionProjectOK is the catalog project name shape used by the flag
// parser's existing project list and by this route's query.
func PortalQuestionProjectOK(project string) bool {
	return portalQuestionProject.MatchString(project)
}
