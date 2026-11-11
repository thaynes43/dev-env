package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The two wake lines (coordinator rulings R4 and R5, 2026-11-11). One
// anchored expression matches both, and nothing else. N is 2 to 9999.
// parentNoticeLineLimit is the longer form: a 253-byte session name, one
// note name, and the shared sentences.
const parentNoticeLineLimit = 524

const noticeChild = `[a-z0-9](?:[-a-z0-9]*[a-z0-9])?(?:\.[a-z0-9](?:[-a-z0-9]*[a-z0-9])?)*`

var parentNoticeLine = regexp.MustCompile(`^\[dev-env notice\] (?:a note is waiting from ` + noticeChild + `: agent-run parent read message\.[a-f0-9]{32}\.[a-f0-9]{32}|(?:[2-9]|[1-9][0-9]{1,3}) notes are waiting, the oldest from ` + noticeChild + `: agent-run parent list)\. This line is typed by the platform, not by the owner\. If it arrived as the answer to a question you asked, discard it and ask again\.$`)

var noticePaneID = regexp.MustCompile(`^%[0-9]+$`)

// Why a parent notice must not be typed. ctl notice --probe prints exactly
// this word. A lost notice is cheap; an answered prompt is not.
const (
	NoticeBusy         = "busy"
	NoticeWaiting      = "waiting"
	NoticeUnknown      = "unknown"
	NoticeNoClient     = "no_client"
	NoticeNotClaudeTUI = "not_claude_tui"
)

// noticeHold is a refusal that types nothing. It is ErrNotAddressable so
// ctl notice exits 3, and its text is the one word above.
type noticeHold string

func (h noticeHold) Error() string { return string(h) }
func (h noticeHold) Unwrap() error { return ErrNotAddressable }

// NoticeProbe reports whether a wake line may be typed now. It types nothing
// and loads no tmux buffer. A nil error means Claude's own status for the
// recorded live CLI was read and is idle.
func NoticeProbe(s Settings, sess protocol.Session) error {
	_, err := noticeIdle(s, sess)
	return err
}

// DeliverNotice types one wake line and Enter into the pane whose process is
// the recorded live CLI, or that CLI's direct child, as this boot's launch
// records it. It checks idle after the delivery lock and again before Enter.
// A failed last check does not press Enter.
func DeliverNotice(ctx context.Context, r Runner, s Settings, sess protocol.Session, line string) error {
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	if strings.ContainsAny(line, "\r\n") || len(line) > parentNoticeLineLimit || !parentNoticeLine.MatchString(line) || StripControl(line) != line {
		return errors.New("not a parent notice line")
	}
	cli, err := noticeClient(s, sess)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	unlock, err := lockFileWaitContext(ctx, s.statePath(deliverLockFile), deliverLockWait)
	if err != nil {
		return notSentError{err}
	}
	defer unlock()
	if err := noticeIdlePID(s, cli); err != nil {
		return err
	}
	pane, err := noticePane(ctx, r, s, cli)
	if err != nil {
		return notSentError{err}
	}
	buffer := "agentd-notice-" + newBootID()
	if _, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"load-buffer", "-b", buffer, "-"}, Stdin: strings.NewReader(line)}); err != nil {
		return notSentError{fmt.Errorf("tmux load-buffer: %s", cmdDetail(err))}
	}
	paste := []string{"paste-buffer", "-b", buffer, "-d", "-p", "-t", pane}
	if _, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: paste}); err != nil {
		return fmt.Errorf("tmux paste-buffer: %s", cmdDetail(err))
	}
	if err := waitSettle(ctx); err != nil {
		return err
	}
	if err := noticeIdlePID(s, cli); err != nil {
		return err
	}
	if _, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"send-keys", "-t", pane, "Enter"}}); err != nil {
		return fmt.Errorf("tmux send-keys: %s", cmdDetail(err))
	}
	return nil
}

func waitSettle(ctx context.Context) error {
	timer := time.NewTimer(deliverSettle)
	select {
	case <-ctx.Done():
		timer.Stop()
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// noticeIdle is the admission rule: Claude's own status for the exact live
// CLI, read with claudeStatus. Busy, waiting, a missing or malformed record,
// and any status this agentd does not know all hold. The heartbeat
// classifier's fallback is not used.
func noticeIdle(s Settings, sess protocol.Session) (agentPid, error) {
	cli, err := noticeClient(s, sess)
	if err != nil {
		return agentPid{}, err
	}
	return cli, noticeIdlePID(s, cli)
}

func noticeClient(s Settings, sess protocol.Session) (agentPid, error) {
	l, ok := currentLaunch(s)
	if !ok || !l.TUI || sess.Agent != protocol.AgentClaude {
		return agentPid{}, noticeHold(NoticeNotClaudeTUI)
	}
	var p agentPid
	if readJSONFile(s.statePath(pidFile), &p) != nil || !pidAlive(p) {
		return agentPid{}, noticeHold(NoticeNoClient)
	}
	return p, nil
}

func noticeIdlePID(s Settings, p agentPid) error {
	if !pidAlive(p) {
		return noticeHold(NoticeNoClient)
	}
	status, _, ok := claudeStatus(s, p.Pid)
	if !ok {
		return noticeHold(NoticeUnknown)
	}
	switch status {
	case claudeIdle:
		return nil
	case claudeBusy:
		return noticeHold(NoticeBusy)
	case claudeWaiting:
		return noticeHold(NoticeWaiting)
	default:
		return noticeHold(NoticeUnknown)
	}
}

// noticePane lists the agent session's panes and keeps the one whose pid is
// the recorded CLI, or whose direct child is that CLI, or which is a direct
// child of that CLI. The launch records the CLI that run-agent started in the
// pane, so the pane process is that CLI or its parent. Zero matches or more
// than one match is a refusal, and no buffer is loaded.
func noticePane(ctx context.Context, r Runner, s Settings, cli agentPid) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	res, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"list-panes", "-t", "=" + TmuxSession, "-F", "#{pane_id} #{pane_pid}"}})
	if err != nil {
		return "", errors.New("parent notice pane list failed")
	}
	var match string
	n := 0
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !noticePaneID.MatchString(fields[0]) {
			return "", errors.New("parent notice pane list is unusable")
		}
		panePid, err := strconv.Atoi(fields[1])
		if err != nil || panePid <= 0 {
			return "", errors.New("parent notice pane list is unusable")
		}
		if paneMatchesCLI(panePid, cli) {
			n++
			match = fields[0]
		}
	}
	if n != 1 {
		return "", errors.New("parent notice pane is not the live CLI")
	}
	return match, nil
}

func paneMatchesCLI(panePid int, cli agentPid) bool {
	if !pidAlive(cli) {
		return false
	}
	if panePid == cli.Pid && procStartTime(panePid) == cli.Start {
		return true
	}
	if ppid, ok := procParent(cli.Pid); ok && ppid == panePid {
		return true
	}
	if ppid, ok := procParent(panePid); ok && ppid == cli.Pid {
		return true
	}
	return false
}

func procParent(pid int) (int, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(string(data)[i+1:])
	if len(fields) < 2 || fields[0] == "Z" {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil || ppid <= 0 {
		return 0, false
	}
	return ppid, true
}
