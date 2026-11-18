package agentrun

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/coordinatorwait"
)

func (a *app) coordinatorWait(ctx context.Context, args []string) error {
	cmd := newCommand("coordinator wait", outputJSON)
	var o coordinatorwait.Options
	var budget, after, mark, ack string
	var resume, reconciled bool
	cmd.fs.StringVar(&o.ThreadID, "thread", "", "")
	cmd.fs.StringVar(&o.WaitID, "wait-id", "", "")
	cmd.fs.StringVar(&o.Directory, "directory", "", "")
	cmd.fs.StringVar(&budget, "budget-end", "", "")
	cmd.fs.DurationVar(&o.Timer, "timer", time.Hour, "")
	cmd.fs.DurationVar(&o.ScanEvery, "scan-every", 10*time.Second, "")
	cmd.fs.IntVar(&o.MaxEntries, "max-entries", 256, "")
	cmd.fs.IntVar(&o.MaxPending, "max-pending", 64, "")
	cmd.fs.StringVar(&after, "after", "", "")
	cmd.fs.StringVar(&mark, "watermark", "", "")
	cmd.fs.StringVar(&ack, "ack", "", "")
	cmd.fs.BoolVar(&resume, "resume", false, "")
	cmd.fs.BoolVar(&reconciled, "reconciled", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usageError("coordinator wait takes no positional arguments")
	}
	o.BudgetEnd, err = time.Parse(time.RFC3339, budget)
	if err != nil {
		return usageError("coordinator wait requires --budget-end as an RFC3339 time")
	}
	o.BudgetEnd = o.BudgetEnd.UTC()
	o.SessionUID, err = a.coordinatorWaitUID()
	if err != nil {
		return err
	}
	if err = o.Validate(); err != nil {
		return usageError("coordinator wait: %s", err)
	}
	if after != "" && !apiv1.ValidResourceVersion(after) {
		return usageError("coordinator wait --after must be a resource version cursor")
	}
	store, err := coordinatorwait.OpenStore(a.env.Getenv("HOME"), o.SessionUID, o.WaitID)
	if err != nil {
		return fail(ExitFailed, "coordinator wait: %s", err)
	}
	defer store.Close()
	r, err := store.Load()
	if err != nil {
		return fail(ExitFailed, "coordinator wait: %s", err)
	}
	if r != nil {
		replaying := r.Outstanding != nil && ack == ""
		if !replaying && ((after != "" && after != r.Cursor) || (mark != "" && mark != r.Watermark)) {
			return usageError("coordinator wait requires the saved cursor and watermark")
		}
		if err = r.Bind(o, ack, resume, reconciled); err != nil {
			return fail(ExitUnconfirmed, "coordinator wait: %s", err)
		}
	} else if ack != "" || resume || reconciled || mark != "" {
		return usageError("coordinator wait cannot acknowledge or resume an absent record")
	}
	waitClock := a.env.WaitClock
	if waitClock == nil {
		waitClock = coordinatorwait.RealClock{}
	}
	fresh := r == nil
	if fresh && o.BudgetEnd.After(waitClock.Now().Add(24*time.Hour)) {
		return usageError("coordinator wait budget end must be at most 24 hours after the wait starts")
	}
	if fresh {
		r = coordinatorwait.New(o, waitClock.Now(), after, nil)
	}
	directory, directoryErr := coordinatorwait.OpenDirectory(o.Directory)
	if directoryErr == nil {
		defer func() { _ = directory.Close() }()
		directoryErr = r.BindDirectory(directory)
	}
	// A new binding always requires the real directory. Existing local terminal
	// results and durable replay survive its disappearance or replacement.
	if fresh && directoryErr != nil {
		return fail(ExitFailed, "coordinator wait: %s", directoryErr)
	}
	if r.Outstanding != nil {
		result := *r.Outstanding
		result.DirectoryUnchecked = directoryErr != nil
		return a.printJSON(&result)
	}
	stop, err := store.StopRequested()
	if err != nil {
		return fail(ExitFailed, "coordinator wait: %s", err)
	}
	// A stopped caller or budget already ended returns locally, without a read.
	if stop || ctx.Err() != nil || !waitClock.Now().Before(o.BudgetEnd) {
		result := r.Take(waitClock.Now(), stop || ctx.Err() != nil)
		if err = store.Save(r); err != nil {
			return fail(ExitFailed, "coordinator wait: %s", err)
		}
		result.DirectoryUnchecked = directoryErr != nil
		return a.printJSON(result)
	}
	if directoryErr != nil {
		return fail(ExitFailed, "coordinator wait: %s", directoryErr)
	}
	setupCtx, finishSetup := coordinatorWaitSetupContext(ctx, waitClock, o.BudgetEnd)
	defer finishSetup()
	c, err := a.connect(setupCtx, cmd.c.conn)
	if err != nil {
		if ctx.Err() != nil || !waitClock.Now().Before(o.BudgetEnd) {
			return a.coordinatorWaitTerminal(store, r, waitClock.Now(), ctx.Err() != nil)
		}
		return parentNoteError(0, err, "wait-any")
	}
	notes := a.coordinatorWaitNotes(c)
	if fresh || r.Cursor == "" {
		snapshot, err := directory.Scan(o.MaxEntries)
		if err != nil {
			return fail(ExitFailed, "coordinator wait: %s", err)
		}
		// Exactly one cursor capture, not a list loop. The API's no-after behavior
		// deliberately omits preexisting notes, just like parent wait-any --for 0.
		initial, err := notes(setupCtx, after, 0)
		if err != nil {
			if ctx.Err() != nil || !waitClock.Now().Before(o.BudgetEnd) {
				return a.coordinatorWaitTerminal(store, r, waitClock.Now(), ctx.Err() != nil)
			}
			return err
		}
		if fresh {
			started := r.Started
			r = coordinatorwait.New(o, started, initial.Cursor, snapshot.Files)
			if err = r.BindDirectory(directory); err != nil {
				return fail(ExitFailed, "coordinator wait: %s", err)
			}
		} else {
			// A stop before the first confirmed cursor has no safe source baseline.
			// Capture one now, then require source reconciliation from that position.
			r.Scan(snapshot.Files, snapshot.Overflow)
			r.Note("", initial.Cursor, true)
		}
		if snapshot.Overflow {
			r.Scan(snapshot.Files, true)
		}
		r.Refuse(snapshot.Refused)
	}
	if err = store.Save(r); err != nil {
		return fail(ExitFailed, "coordinator wait: %s", err)
	}
	runtime := coordinatorwait.Runtime{Clock: waitClock, Notes: notes, Stop: store.StopRequested, Save: store.Save,
		Scan: func() (coordinatorwait.Snapshot, error) { return directory.Scan(o.MaxEntries) },
	}
	result, err := runtime.Run(ctx, r)
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			return err
		}
		return fail(ExitFailed, "coordinator wait: %s", err)
	}
	return a.printJSON(result)
}

// UID comes from this Session's own bounded document, not a latest-thread lookup,
// title, cwd, caller-supplied UID flag or remote host inspection.
func (a *app) coordinatorWaitUID() (string, error) {
	var s protocol.Session
	var err error
	if path := a.env.Getenv(protocol.SessionFileEnv); path != "" {
		f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if e != nil {
			return "", usageError("coordinator wait cannot read its session document")
		}
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() || st.Size() > protocol.MaxSessionFileBytes {
			_ = f.Close()
			return "", usageError("coordinator wait requires a bounded regular session document")
		}
		data, e := io.ReadAll(io.LimitReader(f, protocol.MaxSessionFileBytes+1))
		_ = f.Close()
		if e != nil {
			return "", usageError("coordinator wait cannot read its session document")
		}
		s, err = protocol.ParseSessionFile(data)
	} else {
		s, err = protocol.ParseSession([]byte(a.env.Getenv(protocol.SessionEnv)))
	}
	if err != nil || s.SessionUID == "" {
		return "", usageError("coordinator wait requires its own valid AGENTD_SESSION or AGENTD_SESSION_FILE with Session UID")
	}
	return s.SessionUID, nil
}

func (a *app) coordinatorWaitNotes(c *conn) coordinatorwait.Notes {
	return func(ctx context.Context, cursor string, duration time.Duration) (coordinatorwait.Note, error) {
		query := url.Values{"seconds": {strconv.FormatInt(int64(duration/time.Second), 10)}}
		if cursor != "" {
			query.Set("after", cursor)
		}
		var got apiv1.ParentNoteWaitAnyResult
		status, err := a.callWith(ctx, parentNoteClient(c, duration), http.MethodGet, apiv1.ParentNotesWaitAnyPath, query, nil, &got, false)
		if err != nil {
			return coordinatorwait.Note{}, parentNoteError(status, err, "wait-any")
		}
		if status != http.StatusOK || !validParentWaitAny(got) {
			return coordinatorwait.Note{}, fail(ExitUnconfirmed, "coordinator wait returned unconfirmed parent metadata; reconcile sources before rearming")
		}
		return coordinatorwait.Note{Name: got.Name, Cursor: got.Cursor, Resync: got.State == apiv1.ParentNoteResync}, nil
	}
}

// Setup also observes the injected budget clock: a slow cursor capture must not
// outlive the operating budget or turn into an automatic retry.
func coordinatorWaitSetupContext(ctx context.Context, c coordinatorwait.Clock, end time.Time) (context.Context, func()) {
	child, cancel := context.WithCancel(ctx)
	timer := c.NewTimer(max(0, end.Sub(c.Now())))
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-child.Done():
		case <-timer.C():
			cancel()
		}
	}()
	return child, func() { cancel(); timer.Stop(); <-done }
}
func (a *app) coordinatorWaitTerminal(s *coordinatorwait.Store, r *coordinatorwait.Record, now time.Time, stop bool) error {
	result := r.Take(now, stop)
	if err := s.Save(r); err != nil {
		return fail(ExitFailed, "coordinator wait: %s", err)
	}
	return a.printJSON(result)
}
