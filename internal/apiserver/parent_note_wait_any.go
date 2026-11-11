package apiserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

// parentWaitAnyWatchRefused is the only sentence a refused or unstarted watch
// returns. It carries no note text, and the handler does not poll.
const parentWaitAnyWatchRefused = "the parent wait cannot watch for a note"

func parseParentWaitAnyQuery(r *http.Request) (int, string, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, "", badRequest("wait requires seconds between 1 and 900")
	}
	for key, values := range q {
		if len(values) != 1 || (key != "seconds" && key != "after") {
			return 0, "", badRequest("wait requires seconds between 1 and 900")
		}
	}
	if len(q["seconds"]) != 1 {
		return 0, "", badRequest("wait requires seconds between 1 and 900")
	}
	seconds, err := strconv.Atoi(q.Get("seconds"))
	if err != nil || seconds < 1 || seconds > apiv1.ParentNoteMaxWaitSeconds {
		return 0, "", badRequest("wait requires seconds between 1 and 900")
	}
	after := ""
	if _, ok := q["after"]; ok {
		after = q.Get("after")
		if !apiv1.ValidResourceVersion(after) {
			return 0, "", newError(http.StatusUnprocessableEntity, apiv1.CodeInvalid, "after must be a resource version")
		}
	}
	return seconds, after, nil
}

func (s *Server) waitAnyParentNote(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.parentNotesAvailable(); err != nil {
		return 0, nil, err
	}
	sess, err := s.inboxRecipientCaller(ctx, c)
	if err != nil {
		return 0, nil, err
	}
	party := sessionIdentity(sess)
	seconds, after, err := parseParentWaitAnyQuery(r)
	if err != nil {
		return 0, nil, err
	}
	cursor := after
	if after == "" {
		// One list, taken when the request arrives. Existing notes stay out
		// of the answer. The cursor is the list's resource version, not a clock.
		cursor, err = s.Inbox.ParentNoteWatchCursor(ctx, party)
		if err != nil {
			return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, parentWaitAnyWatchRefused)
		}
	}
	if !s.parentWaits.acquire(party) {
		return 0, nil, newError(http.StatusTooManyRequests, apiv1.CodeLimitExceeded, parentHeldWaitLimitMessage)
	}
	defer s.parentWaits.release(party)
	duration := time.Duration(seconds) * time.Second
	if err := holdResponse(w, duration+30*time.Second); err != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "this API cannot hold the request for the wait")
	}
	timeout := s.parentWaitTimeout
	if timeout == nil {
		timeout = context.WithTimeout
	}
	waitCtx, cancel := timeout(ctx, duration)
	defer cancel()
	note, next, resync, err := s.awaitParentNotes(waitCtx, party, cursor)
	if err != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, parentWaitAnyWatchRefused)
	}
	if resync {
		// A 410 is not a refused watch. The fresh cursor is a new list.
		// The handler does not compare times and does not answer 503.
		next, err = s.Inbox.ParentNoteWatchCursor(ctx, party)
		if err != nil {
			return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, parentWaitAnyWatchRefused)
		}
		note = nil
	}
	if err := s.parentNoteReadFence(ctx, c, party); err != nil {
		return 0, nil, err
	}
	if note != nil {
		s.logParentNote("wait-any", note)
		return http.StatusOK, parentWaitAnyNote(note), nil
	}
	state := apiv1.ParentNoteWaiting
	if resync {
		state = apiv1.ParentNoteResync
	}
	return http.StatusOK, apiv1.ParentNoteWaitAnyResult{State: state, Cursor: next}, nil
}

// parentWaitAnyNote copies the fields a parent may see. The note text and key
// stay on the stored object.
func parentWaitAnyNote(note *coord.InboxMessage) apiv1.ParentNoteWaitAnyResult {
	return apiv1.ParentNoteWaitAnyResult{
		State:     apiv1.ParentNoteArrived,
		Name:      note.Name,
		Child:     note.Spec.Sender.Name,
		Kind:      note.Spec.Kind,
		CreatedAt: note.CreationTimestamp.UTC(),
		Cursor:    note.ResourceVersion,
	}
}

func (s *Server) awaitParentNotes(ctx context.Context, party coord.SessionIdentity, cursor string) (*coord.InboxMessage, string, bool, error) {
	next := cursor
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, next, false, nil
		}
		return nil, "", false, err
	}
	stream, err := s.Inbox.WatchParentNotes(ctx, party, cursor)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, next, false, nil
		}
		if cursorExpired(err) {
			return nil, "", true, nil
		}
		return nil, "", false, err
	}
	defer stream.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, next, false, nil
			}
			return nil, "", false, ctx.Err()
		case event, open := <-stream.ResultChan():
			if !open {
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return nil, next, false, nil
				}
				return nil, "", false, inbox.ErrUnavailable
			}
			switch event.Type {
			case watch.Bookmark:
				if rv := reportedVersion(event.Object); rv != "" {
					next = rv
				}
			case watch.Error:
				status, _ := event.Object.(*metav1.Status)
				if cursorStatusExpired(status) {
					return nil, "", true, nil
				}
				return nil, "", false, inbox.ErrUnavailable
			case watch.Added, watch.Modified, watch.Deleted:
				msg, ok := event.Object.(*coord.InboxMessage)
				if !ok || !parentNoteAddressedTo(msg, party) {
					continue
				}
				if rv := reportedVersion(msg); rv != "" {
					next = rv
				}
				// Only an Added event stores a note. A later change wakes nobody.
				if event.Type == watch.Added && parentNoteWakes(msg) {
					return msg, msg.ResourceVersion, false, nil
				}
			}
		}
	}
}

func parentNoteAddressedTo(m *coord.InboxMessage, party coord.SessionIdentity) bool {
	if !inbox.IsParentNote(m) {
		return false
	}
	got := m.Spec.Recipient
	return got.Namespace == party.Namespace && got.Name == party.Name && got.UID == party.UID
}

func parentNoteWakes(m *coord.InboxMessage) bool {
	if m.Name == "" || !apiv1.ValidResourceVersion(m.ResourceVersion) || m.CreationTimestamp.IsZero() || m.Spec.Sender.Name == "" {
		return false
	}
	return m.Spec.Kind == inbox.KindQuestion || m.Spec.Kind == inbox.KindResult || m.Spec.Kind == inbox.KindBlocker
}

func reportedVersion(obj runtime.Object) string {
	accessor, err := meta.Accessor(obj)
	if err != nil || accessor == nil {
		return ""
	}
	rv := accessor.GetResourceVersion()
	if !apiv1.ValidResourceVersion(rv) {
		return ""
	}
	return rv
}

func cursorExpired(err error) bool {
	return apierrors.IsGone(err) || apierrors.IsResourceExpired(err)
}

func cursorStatusExpired(status *metav1.Status) bool {
	return status != nil && (status.Code == http.StatusGone || status.Reason == metav1.StatusReasonExpired)
}
