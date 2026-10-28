package keeper

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/reminders"
)

// The keeper hosts the reminder scheduler (DESIGN-032, OpenProject #206)
// because it already holds the dev-env Pushover credential, the egress to
// Pushover and a Lease. It is off by default (--enable-reminders), needs no new
// credential, and delivers through the same PushoverSender the login notices
// use. Calendar targets have no real adapter yet, so the scheduler records
// "calendar: unconfigured" for them.

// RemindersOptions configures the scheduler.
type RemindersOptions struct {
	// Enabled starts the scheduler; it also needs leader election and the
	// Pushover credential files.
	Enabled bool
	// Namespace holds the Reminder objects; the keeper's namespace when empty.
	Namespace string
	// Every is the polling period; 30 seconds when zero.
	Every time.Duration
	// MissGrace is how late a recurring occurrence may still fire.
	MissGrace time.Duration
}

// Pushover's limits are 250 characters for a title and 1024 for a message. They
// are applied here as bytes, which is never over the limit for any reading of
// "character". The API refuses a body over 1024 bytes, so a clamp is a defence for
// an object made by other means, not the normal path.
const (
	pushoverTitleMax   = 250
	pushoverMessageMax = 1024
	// clampMarker shows that text was cut.
	clampMarker = "…"
)

// clampUTF8 returns s unchanged when it fits in max bytes. Otherwise it cuts s on
// a rune boundary and ends it with a marker, so the result is valid UTF-8 and
// within max bytes. A Pushover 4xx on a split character would fail the delivery.
func clampUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max - len(clampMarker)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + clampMarker
}

// ReminderSender adapts the PushoverSender to the scheduler's Sender, and tells
// the scheduler how far a failed request got:
//
//   - missing credentials, 429 and 5xx: nothing was accepted, so a bounded retry
//     is safe (ErrNotSent);
//   - another non-2xx: the provider refused (ErrRejected);
//   - anything else, a transport error or a timeout: the request may have been
//     accepted, so the error is left bare and the delivery becomes ambiguous.
type ReminderSender struct {
	Pushover *PushoverSender
}

// Send implements reminders.Sender. Priority 0 (normal): a reminder is not a page.
func (s ReminderSender) Send(ctx context.Context, m reminders.Message) error {
	title, body := clampUTF8(m.Title, pushoverTitleMax), clampUTF8(m.Body, pushoverMessageMax)
	err := s.Pushover.Send(ctx, Notice{Title: title, Message: body, Priority: 0})
	if err == nil {
		return nil
	}
	if errors.Is(err, errPushoverCredentials) {
		return fmt.Errorf("%w: pushover credentials are not available", reminders.ErrNotSent)
	}
	var se *SendError
	if errors.As(err, &se) {
		if se.Status == 429 || se.Status >= 500 {
			return fmt.Errorf("%w: pushover answered %d", reminders.ErrNotSent, se.Status)
		}
		return fmt.Errorf("%w: pushover answered %d", reminders.ErrRejected, se.Status)
	}
	return err
}
