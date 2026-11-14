package agentd

// Stop ownership for the summoned task's first launch (OP791 round 2).
// The launcher leads the group while probes run inside it. The task gets
// its own group; its start and PID publication share a lock with stop.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const (
	startStoppedFile   = "start-stopped.json"
	startSpawnLockFile = "start-spawn.lock"
	// One deadline for lock + grace + kill observation, inside the pod's
	// 60s termination grace. The production bounds are 5s + 30s + 5s.
	preflightStopMaxWait = 40 * time.Second
)

var preflightLockWait = 5 * time.Second

// Narrow seams for deterministic stop deadlines, signal/identity observations
// and failed ownership writes. Production uses the same kernel and durable I/O.
var preflightStopNow = time.Now
var preflightStopLock = lockFileWait
var preflightStopWait = waitNativeGroupAbsent
var preflightSignal = syscall.Kill
var writePreflightRecord = writeWorkspaceJSON
var readPreflightStat = os.ReadFile

// claimPreflight is independent of usage/capture identity. The exclusive
// checking record is never removed, even on an incomplete write or failure.
func claimPreflight(l Launch, stateDir string) error {
	if l.Preflight == nil {
		return nil
	}
	if err := leadPreflightGroup(); err != nil {
		return err
	}
	self := os.Getpid()
	rec := startPreflight{State: preflightChecking, Pid: self, Start: procStartTime(self), GroupOwned: true,
		At: runAgentNow().UTC(), ConversationID: l.ConversationID, BootID: l.BootID}
	if rec.Start == "" {
		return errors.New("launcher identity is unconfirmed")
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return projectSyncExclusiveFile(statePathIn(stateDir, startPreflightFile), append(data, '\n'))
}

func leadPreflightGroup() error {
	// tmux may already have made run-agent a session/group leader. Calling
	// setpgid in that case would fail with EPERM despite the desired group.
	if syscall.Getpgrp() != os.Getpid() {
		if err := syscall.Setpgid(0, 0); err != nil {
			return err
		}
	}
	if syscall.Getpgrp() != os.Getpid() {
		return errors.New("launcher does not lead its process group")
	}
	return nil
}

func preflightStopRefusal(l Launch, stateDir string) string {
	if l.Preflight == nil {
		return ""
	}
	if _, err := os.Lstat(filepath.Join(stateDir, startStoppedFile)); !os.IsNotExist(err) {
		return "the summoned launch was stopped or its stop state is uncertain"
	}
	return ""
}

func preflightSpawnLock(l Launch, stateDir string) (func(), error) {
	if l.Preflight == nil {
		return func() {}, nil
	}
	if stateDir == "" {
		return nil, errors.New("pre-flight state directory is missing")
	}
	return lockFileWait(filepath.Join(stateDir, startSpawnLockFile), preflightLockWait)
}

// A launcher can die after task Start but before its independent group is
// recorded. The durable intent keeps that unconfirmed interval visible; it
// does not invent a group identity or claim that an unknown task was stopped.
func recordPreflightTaskPending(l Launch, stateDir string, pending bool) error {
	if l.Preflight == nil {
		return nil
	}
	rec, ok := readPreflight(Settings{StateDir: stateDir}, l)
	if !ok {
		return errors.New("pre-flight ownership record is unconfirmed")
	}
	rec.TaskPending = pending
	return writePreflightRecord(statePathIn(stateDir, startPreflightFile), rec)
}

func pendingPreflightTask(s Settings, rec startPreflight) bool {
	if !rec.TaskPending {
		return false
	}
	var task agentPid
	if readJSONFile(s.statePath(pidFile), &task) != nil || task.Pid <= 0 || task.Start == "" {
		return true
	}
	return !preflightGroupProvenGone(task) && !waitNativeGroupAbsent(task.Pid, 0)
}

// A boot may have a new PID namespace; old numeric identities cannot prove
// ownership across that boundary. A missing boot record proves no change.
func preflightBootChanged(l Launch, bootID string) bool {
	return l.Preflight != nil && bootID != "" && l.BootID != bootID
}

func preflightFromAnotherBoot(s Settings, l Launch) bool {
	var boot bootRecord
	return readJSONFile(s.statePath(bootFile), &boot) == nil && preflightBootChanged(l, boot.BootID)
}

func preflightStopRemaining(deadline time.Time, bound time.Duration) time.Duration {
	return max(0, min(bound, deadline.Sub(preflightStopNow())))
}

// stopPreflightLaunch publishes the irreversible fence first, then acquires
// the same bounded lock as check/start/PID publication. A timeout is running,
// never proof of a stop. Nothing can spawn once that critical section drains.
func stopPreflightLaunch(s Settings, l Launch, grace time.Duration) *protocol.AgentStop {
	st := &protocol.AgentStop{}
	grace = max(0, min(grace, 30*time.Second))
	deadline := preflightStopNow().Add(min(preflightLockWait+grace+stopKillWait, preflightStopMaxWait))
	if preflightFromAnotherBoot(s, l) {
		return st
	}
	marker := startRefused{Reason: "stop requested", At: preflightStopNow().UTC(), ConversationID: l.ConversationID, BootID: l.BootID}
	if err := writeWorkspaceJSON(s.statePath(startStoppedFile), marker); err != nil {
		st.Running = true
		return st
	}
	unlock, err := preflightStopLock(s.statePath(startSpawnLockFile), preflightStopRemaining(deadline, preflightLockWait))
	if err != nil {
		st.Running = true
		return st
	}
	var task agentPid
	taskErr := readJSONFile(s.statePath(pidFile), &task)
	rec, found := readPreflight(s, l)
	unlock()
	// The task was published before stop took the lock, or the marker refused
	// it. Stop the task while its launcher can still reap it/write the result.
	if taskErr == nil {
		return stopPreflightGroup(task, grace, deadline)
	} else if os.IsNotExist(taskErr) && found && rec.State == preflightStarted {
		// The finished task removed its PID. Its launcher still owns result
		// retention and must never receive a second grace or group SIGKILL.
		return st
	} else if !os.IsNotExist(taskErr) || rec.TaskPending {
		st.Running = true
	}
	if found && rec.GroupOwned {
		mergePreflightStop(st, stopPreflightGroup(agentPid{Pid: rec.Pid, Start: rec.Start}, grace, deadline))
	} else if found && pidAlive(agentPid{Pid: rec.Pid, Start: rec.Start}) {
		// An older checking record did not prove group leadership. Do not infer
		// ownership of an unrelated group from it.
		st.WasRunning, st.Running = true, true
	} else if !found {
		if _, err := os.Lstat(s.statePath(startPreflightFile)); !os.IsNotExist(err) {
			st.Running = true
		}
	}
	return st
}

func mergePreflightStop(dst, src *protocol.AgentStop) {
	dst.WasRunning = dst.WasRunning || src.WasRunning
	dst.Killed = dst.Killed || src.Killed
	dst.Running = dst.Running || src.Running
}

func stopPreflightGroup(p agentPid, grace time.Duration, deadline time.Time) *protocol.AgentStop {
	st := &protocol.AgentStop{}
	if p.Pid <= 0 || p.Start == "" {
		st.Running = true
		return st
	}
	// Reuse proves absence of the recorded group even when an unrelated
	// process now leads a group with that number. Never signal its identity.
	if preflightGroupProvenGone(p) {
		return st
	}
	if preflightGroupIdentityUncertain(p) {
		st.Running = true
		return st
	}
	if !pidAlive(p) && preflightStopWait(p.Pid, 0) {
		return st
	}
	st.WasRunning = true
	if pidAlive(p) {
		_ = preflightSignal(p.Pid, syscall.SIGTERM)
	}
	// Reserve the kill observation inside this same deadline. Marker I/O,
	// the lock and process observations all consume its remaining time.
	termWait := min(grace, max(0, deadline.Sub(preflightStopNow())-stopKillWait))
	if !preflightStopWait(p.Pid, termWait) {
		if preflightGroupProvenGone(p) {
			return &protocol.AgentStop{}
		}
		if preflightGroupIdentityUncertain(p) {
			st.Running = true
			return st
		}
		st.Killed = true
		_ = preflightSignal(-p.Pid, syscall.SIGKILL)
		st.Running = !preflightStopWait(p.Pid, preflightStopRemaining(deadline, stopKillWait))
	}
	st.Running = st.Running || pidAlive(p)
	return st
}

// preflightGroupProvenGone requires a readable, different start time. POSIX
// "Process ID Reuse" reserves the PID while its former group has members:
// https://pubs.opengroup.org/onlinepubs/9799919799/basedefs/V1_chap04.html#tag_04_17
// See also the committee draft, Base Definitions 4.13:
// https://www.open-std.org/jtc1/sc22/open/n4217.pdf
// Linux __change_pid/free_pid retain the allocation until every PID-type list
// is empty; exit.c detaches zombie membership only through release_task:
// https://github.com/torvalds/linux/blob/v6.12/kernel/pid.c
// https://github.com/torvalds/linux/blob/v6.12/kernel/exit.c
// A missing leader alone proves nothing: its group can still have members.
func preflightGroupProvenGone(p agentPid) bool {
	if p.Pid <= 0 || p.Start == "" {
		return false
	}
	if _, err := strconv.ParseUint(p.Start, 10, 64); err != nil {
		return false
	}
	current, _, err := readPreflightProcess(p.Pid)
	return err == nil && current.Start != p.Start
}

func preflightGroupIdentityUncertain(p agentPid) bool {
	current, group, err := readPreflightProcess(p.Pid)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return false // surviving group members can outlive their leader.
	}
	return err != nil || current != p || group != p.Pid
}

// strictPreflightGroup includes zombies and treats unreadable or malformed
// /proc state as uncertainty. It is used only for the launcher exception to
// the existing bounded absence check; whole-group proof still uses ESRCH.
func strictPreflightGroup(pgid int) ([]agentPid, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var members []agentPid
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		member, group, err := readPreflightProcess(pid)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if group == pgid {
			members = append(members, member)
		}
	}
	return members, nil
}

// Include unreaped zombies: a fast task can exit before PID publication,
// while its process group still has live descendants. procStartTime keeps
// its existing live-only semantics for ordinary sessions.
func readPreflightProcess(pid int) (agentPid, int, error) {
	raw, err := readPreflightStat("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return agentPid{}, 0, err
	}
	i := strings.LastIndexByte(string(raw), ')')
	if i < 0 {
		return agentPid{}, 0, errors.New("unreadable process identity")
	}
	fields := strings.Fields(string(raw)[i+1:])
	if len(fields) < 20 {
		return agentPid{}, 0, errors.New("incomplete process identity")
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return agentPid{}, 0, err
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return agentPid{}, 0, err
	}
	return agentPid{Pid: pid, Start: fields[19]}, group, nil
}

func publishPreflightTaskPID(stateDir string, pid int) error {
	p, group, err := readPreflightProcess(pid)
	if err != nil {
		return err
	}
	if group != pid || p.Start == "" {
		return errors.New("task process group identity is unconfirmed")
	}
	return writeWorkspaceJSON(statePathIn(stateDir, pidFile), p)
}

func preflightGroupOnlyLauncher(pgid int, self agentPid) bool {
	if pgid <= 0 || self.Pid != pgid || !pidAlive(self) {
		return false
	}
	members, err := strictPreflightGroup(pgid)
	return err == nil && len(members) == 1 && members[0] == self
}

// The launcher must survive normal turn cleanup. Signal each other member
// individually, checking its group immediately before each signal. The later
// bounded observation, not these signal requests, permits the next start.
func killPreflightMembers(self agentPid) error {
	if self.Pid != os.Getpid() || !pidAlive(self) || syscall.Getpgrp() != self.Pid {
		return errors.New("pre-flight group ownership is unconfirmed")
	}
	members, err := strictPreflightGroup(self.Pid)
	if err != nil {
		return err
	}
	for _, member := range members {
		if member == self {
			continue
		}
		group, err := syscall.Getpgid(member.Pid)
		if errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil || group != self.Pid {
			return errors.New("pre-flight member group is unconfirmed")
		}
		if err := preflightSignal(member.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}
	return nil
}
