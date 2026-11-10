package apiserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/watch"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

// parentHeldWaitLimit is how many waits one session may hold on this Server.
// Each API replica has its own Server. Two replicas can hold sixteen.
const parentHeldWaitLimit = 8

// parentWaitWatchRefused is the only sentence a refused or unstarted watch
// returns. It carries no note text, and the handler does not poll.
const parentWaitWatchRefused = "the parent wait cannot watch for a reply"

type parentWaitSlots struct {
	mu   sync.Mutex
	held map[coord.SessionIdentity]int
}

func (p *parentWaitSlots) acquire(party coord.SessionIdentity) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held[party] >= parentHeldWaitLimit {
		return false
	}
	if p.held == nil {
		p.held = make(map[coord.SessionIdentity]int)
	}
	p.held[party]++
	return true
}

func (p *parentWaitSlots) release(party coord.SessionIdentity) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held[party]--
	if p.held[party] == 0 {
		delete(p.held, party)
	}
}

func (s *Server) waitParentNote(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.parentNotesAvailable(); err != nil {
		return 0, nil, err
	}
	sess, err := s.inboxRecipientCaller(ctx, c)
	if err != nil {
		return 0, nil, err
	}
	party := sessionIdentity(sess)
	note, err := s.parentNoteFor(ctx, r.PathValue("message"), party, false)
	if err != nil {
		return 0, nil, err
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 1 || len(q["seconds"]) != 1 {
		return 0, nil, badRequest("wait requires seconds between 1 and 900")
	}
	seconds, err := strconv.Atoi(q.Get("seconds"))
	if err != nil || seconds < 1 || seconds > apiv1.ParentNoteMaxWaitSeconds {
		return 0, nil, badRequest("wait requires seconds between 1 and 900")
	}
	replies, version, err := s.Inbox.ParentReplies(ctx, note)
	if err != nil {
		return 0, nil, inboxUnknown()
	}
	var reply *coord.InboxMessage
	if len(replies) != 0 {
		reply = &replies[0]
	} else {
		if !s.parentWaits.acquire(party) {
			return 0, nil, newError(http.StatusTooManyRequests, apiv1.CodeLimitExceeded, "at most 8 parent waits may be held for one session")
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
		reply, err = s.awaitParentReply(waitCtx, note, version)
		if err != nil {
			// The slot is released by the defer above. A refused watch, a nil
			// watcher, or a stream that dies is one attempt: no second watch
			// and no list loop.
			return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, parentWaitWatchRefused)
		}
	}
	// A held token can outlive its pod. Re-prove the original identity before
	// returning any private answer, using the request context, not the timer.
	if err := s.parentNoteReadFence(ctx, c, party); err != nil {
		return 0, nil, err
	}
	result := apiv1.ParentNoteWaitResult{State: apiv1.ParentNoteWaiting}
	if reply != nil {
		result.State = apiv1.ParentNoteAnswered
		result.Reply = &apiv1.InboxMessage{InboxMetadata: withInboxCorrelation(inboxMetadata(reply), reply), Text: reply.Spec.Text}
	}
	return http.StatusOK, result, nil
}

func (s *Server) awaitParentReply(ctx context.Context, note *coord.InboxMessage, version string) (*coord.InboxMessage, error) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, nil
		}
		return nil, err
	}
	stream, err := s.Inbox.WatchParentReplies(ctx, note, version)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, nil
		}
		return nil, err
	}
	defer stream.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, nil
			}
			return nil, ctx.Err()
		case event, open := <-stream.ResultChan():
			if !open {
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return nil, nil
				}
				return nil, inbox.ErrUnavailable
			}
			if event.Type == watch.Bookmark {
				continue
			}
			reply, ok := event.Object.(*coord.InboxMessage)
			if (event.Type != watch.Added && event.Type != watch.Modified) || !ok || !s.Inbox.ValidParentReply(note, reply) {
				return nil, inbox.ErrUnavailable
			}
			return reply, nil
		}
	}
}
