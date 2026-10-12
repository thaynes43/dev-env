package agentd

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// reasonError carries a closed-set reason code (protocol.RepositoryReasons)
// beside an error whose message is unchanged. The code is what leaves the
// pod; the message stays in the private report (DESIGN-006, reason codes).
type reasonError struct {
	reason string
	err    error
}

func (e *reasonError) Error() string { return e.err.Error() }
func (e *reasonError) Unwrap() error { return e.err }

// reasonErr is a new error with a reason code.
func reasonErr(reason, message string) error {
	return &reasonError{reason: reason, err: errors.New(message)}
}

// withReason gives err a reason code unless it already carries one.
func withReason(reason string, err error) error {
	var known *reasonError
	if err == nil || errors.As(err, &known) {
		return err
	}
	return &reasonError{reason: reason, err: err}
}

// reasonOf is err's reason code, or fallback when it has none. Preserved
// clone staging is always StagingPreserved: someone must inspect it.
func reasonOf(err error, fallback string) string {
	var known *reasonError
	if errors.As(err, &known) {
		return known.reason
	}
	var staged *projectStagingPreserved
	if errors.As(err, &staged) {
		return protocol.ReasonStagingPreserved
	}
	return fallback
}

// endedReason is the code for work that stopped because ctx ended: TimedOut
// when a budget's deadline passed (the step's own or the operation's), Stopped
// when agentd itself was stopped (SIGTERM cancels its context), and "" while
// ctx is live.
func endedReason(ctx context.Context) string {
	switch err := ctx.Err(); {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return protocol.ReasonTimedOut
	default:
		return protocol.ReasonStopped
	}
}

// endedFailure is the private report's text for an operation that ended.
func endedFailure(reason, operation string) string {
	if reason == protocol.ReasonStopped {
		return "agentd stopped during " + operation
	}
	return operation + " time budget ended"
}

// rootError is a project root refusal (DESIGN-007) with its reason code, the
// member repository that caused it and that repository's clean-checkout
// cause. It is errPrivateRoot to every caller that compares errors.
type rootError struct {
	reason, repository, cause string
}

func (e *rootError) Error() string        { return errPrivateRoot.Error() }
func (e *rootError) Is(target error) bool { return target == errPrivateRoot }

// anchorRefusal is an AnchorUnproven refusal of repository, with the
// clean-checkout cause when err carries one.
func anchorRefusal(repository string, err error) error {
	var known *rootError
	if errors.As(err, &known) {
		return err
	}
	return &rootError{reason: protocol.ReasonAnchorUnproven, repository: repository, cause: reasonOf(err, "")}
}

// summaryLimit bounds the boot step's first note, so that the status line
// "project-bootstrap: fail: <note>" fits the API's 256-byte problem line.
const summaryLimit = 224

// summary is the project-bootstrap step's first note: the outcome, the
// catalog reason and every item that is not Ready, as catalog names and codes
// only. It never holds a path, an object id or detail text.
func (r PrivateProjectReport) summary() string {
	head := "catalog bootstrap outcome " + r.State
	var items []string
	if r.Reason != "" {
		items = append(items, r.Reason)
	}
	for _, repo := range r.Repositories {
		if repo.State != protocol.BootstrapReady {
			items = append(items, repo.Repository.Name+"="+firstNonEmpty(repo.Reason, repo.State))
		}
	}
	for _, project := range r.Projects {
		if project.State == protocol.BootstrapReady {
			continue
		}
		item := "project " + project.Project + "=" + firstNonEmpty(project.Reason, project.State)
		if project.Repository != "" {
			item += "@" + project.Repository
		}
		if project.Cause != "" {
			item += "(" + project.Cause + ")"
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return head
	}
	out := head + ": "
	for i, item := range items {
		sep := ""
		if i > 0 {
			sep = "; "
		}
		rest := len(items) - i
		more := " +" + strconv.Itoa(rest) + " more"
		// Keep room to say how many did not fit, unless this is the last.
		room := summaryLimit - len(out) - len(sep) - len(item)
		if rest > 1 {
			room -= len(" +" + strconv.Itoa(rest-1) + " more")
		}
		if room < 0 {
			return strings.TrimSuffix(out, ": ") + more
		}
		out += sep + item
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// published is the report as protocol.ProjectBootstrap: names, states,
// steps and codes, bounded at the catalog's limits.
func (r PrivateProjectReport) published() *protocol.ProjectBootstrap {
	if r.State == "Disabled" {
		return nil
	}
	out := &protocol.ProjectBootstrap{State: r.State, Reason: r.Reason, CatalogRevision: r.CatalogRevision}
	for _, repo := range r.Repositories {
		if len(out.Repositories) == protocol.MaxBootstrapRepositories {
			break
		}
		out.Repositories = append(out.Repositories, protocol.ProjectBootstrapRepository{Name: repo.Repository.Name, State: repo.State, Step: repo.Step, Reason: repo.Reason})
	}
	for _, project := range r.Projects {
		if len(out.Projects) == protocol.MaxBootstrapProjects {
			break
		}
		out.Projects = append(out.Projects, protocol.ProjectBootstrapProject{Name: project.Project, State: project.State, Reason: project.Reason, Repository: project.Repository, Cause: project.Cause})
	}
	return out
}
