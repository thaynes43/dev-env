package agentd

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// parentNoticeLine is the only text DeliverNotice will type. CHILD is a
// session name and NAME is an InboxMessage name. The line has no note text.
var parentNoticeLine = regexp.MustCompile(`^\[dev-env notice\] a note is waiting from ([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*): agent-run parent read (message\.[a-f0-9]{32}\.[a-f0-9]{32})$`)

// DeliverNotice types one wake line and Enter into a Claude TUI. It does not
// wrap the line in the agent-run msg envelope. A Codex TUI, a headless task,
// and any line that is not the fixed notice are refused before anything is
// typed.
func DeliverNotice(ctx context.Context, r Runner, s Settings, sess protocol.Session, line string) error {
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	if strings.ContainsAny(line, "\r\n") || len(line) > 400 || !parentNoticeLine.MatchString(line) || StripControl(line) != line {
		return errors.New("not a parent notice line")
	}
	l, ok := currentLaunch(s)
	if !ok {
		return fmt.Errorf("%w: the agent has not started in this pod", ErrNotAddressable)
	}
	var p agentPid
	if readJSONFile(s.statePath(pidFile), &p) != nil || !pidAlive(p) {
		return fmt.Errorf("%w: the agent is not running", ErrNotAddressable)
	}
	if !l.TUI || sess.Agent != protocol.AgentClaude {
		return fmt.Errorf("%w: a parent notice is only for a Claude TUI", ErrNotAddressable)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	unlock, err := lockFileWait(s.statePath(deliverLockFile), deliverLockWait)
	if err != nil {
		return notSentError{err}
	}
	defer unlock()
	buffer := "agentd-notice-" + newBootID()
	if _, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"load-buffer", "-b", buffer, "-"}, Stdin: strings.NewReader(line)}); err != nil {
		return notSentError{fmt.Errorf("tmux load-buffer: %s", cmdDetail(err))}
	}
	paste := []string{"paste-buffer", "-b", buffer, "-d", "-p", "-t", "=" + TmuxSession + ":"}
	if _, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: paste}); err != nil {
		return fmt.Errorf("tmux paste-buffer: %s", cmdDetail(err))
	}
	time.Sleep(deliverSettle)
	if _, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"send-keys", "-t", "=" + TmuxSession + ":", "Enter"}}); err != nil {
		return fmt.Errorf("tmux send-keys: %s", cmdDetail(err))
	}
	return nil
}
