//go:build ignore

// Command coord-archive concurrent shelf-limit waiter runs four worst-case
// coord-archive gets and one ctl prune inside a container limited like the
// shelf. It is not an agentd command. Build it with
// go build -o coord-concurrent-measure-wait ./cmd/agentd/coordarchive_shelf_measure_concurrent_wait.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	measureRSSThresholdKiB = 30000
	measureClaimResource   = "taskclaims"
	measureClaimRV         = "42"
	measureClaimUID        = "44444444-4444-4444-8444-444444444444"
	measurePrunePath       = "rescue/measure-old/20260801-0000"
	measurePruneStdin      = `{"olderThan":"720h","keep":[]}`
	measureStdoutCap       = 2 << 20
)

func measureClaimName() string {
	return "task-claim." + strings.Repeat("c", 32) + "." + strings.Repeat("d", 32)
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: coord-concurrent-measure-wait plan.json")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	plan, err := loadPlan(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "plan: %v\n", err)
		os.Exit(2)
	}
	cgroup, err := selfCgroup()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cgroup: %v\n", err)
		os.Exit(2)
	}
	limits, err := readLimits(cgroup)
	if err != nil {
		fmt.Fprintf(os.Stderr, "limits: %v\n", err)
		os.Exit(2)
	}
	code, receipt := run(ctx, cgroup, limits, plan)
	enc, err := json.Marshal(receipt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "receipt: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "measure %s\n", enc)
	os.Exit(code)
}

type planCommand struct {
	Name       string   `json:"name"`
	Argv       []string `json:"argv"`
	Stdin      string   `json:"stdin,omitempty"`
	HoldStdout bool     `json:"holdStdout,omitempty"`
	Expect     string   `json:"expect"`
}

func loadPlan(path string) ([]planCommand, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) > 8192 {
		return nil, fmt.Errorf("plan is %d bytes", len(raw))
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cmds []planCommand
	if err := dec.Decode(&cmds); err != nil {
		return nil, err
	}
	var extra any
	if !errorsEOF(dec.Decode(&extra)) {
		return nil, fmt.Errorf("plan must be one JSON array")
	}
	if len(cmds) != 5 {
		return nil, fmt.Errorf("want 4 gets and 1 prune, got %d commands", len(cmds))
	}
	var sha string
	for i, c := range cmds {
		want := fmt.Sprintf("get-%d", i+1)
		if i == 4 {
			want = "prune"
		}
		if c.Name != want {
			return nil, fmt.Errorf("command %d name %q, want %s", i, c.Name, want)
		}
		if err := checkCommand(c, &sha); err != nil {
			return nil, fmt.Errorf("%s: %w", c.Name, err)
		}
	}
	return cmds, nil
}

func errorsEOF(err error) bool {
	return err == io.EOF
}

func checkCommand(c planCommand, sha *string) error {
	if len(c.Argv) == 0 || !filepath.IsAbs(c.Argv[0]) || filepath.Base(c.Argv[0]) != "agentd" || strings.Contains(c.Argv[0], "..") {
		return fmt.Errorf("argv0 must be an absolute agentd path")
	}
	switch c.Name {
	case "prune":
		if c.HoldStdout || strings.Join(c.Argv[1:], " ") != "ctl prune" || c.Stdin != measurePruneStdin || c.Expect != measurePrunePath {
			return fmt.Errorf("prune shape refused")
		}
		return nil
	default:
		if !c.HoldStdout || c.Stdin != "" || len(c.Argv) != 14 {
			return fmt.Errorf("get shape refused")
		}
		want := []string{"ctl", "coord-archive", "get", "--resource", measureClaimResource, "--name", measureClaimName(), "--uid", measureClaimUID, "--resource-version", measureClaimRV, "--sha256"}
		for i, w := range want {
			if c.Argv[i+1] != w {
				return fmt.Errorf("arg %d %q, want %q", i+1, c.Argv[i+1], w)
			}
		}
		got := c.Argv[13]
		if len(got) != 64 || strings.Trim(got, "0123456789abcdef") != "" || c.Expect != got {
			return fmt.Errorf("sha256 pin refused")
		}
		if *sha == "" {
			*sha = got
		} else if *sha != got {
			return fmt.Errorf("gets disagree on sha256")
		}
		return nil
	}
}

type limits struct {
	memoryMax string
	cpuMax    string
	swapMax   string
	events    string
}

func readLimits(dir string) (limits, error) {
	var out limits
	var err error
	if out.memoryMax, err = readCgroupFile(dir, "memory.max"); err != nil {
		return out, err
	}
	wantMax := strconv.Itoa(256 << 20)
	if wantMax != "268435456" {
		return out, fmt.Errorf("shelf memory constant drifted: %s", wantMax)
	}
	if out.memoryMax != wantMax {
		return out, fmt.Errorf("memory.max=%s, want 268435456", out.memoryMax)
	}
	if out.cpuMax, err = readCgroupFile(dir, "cpu.max"); err != nil {
		return out, err
	}
	frac, err := cpuFraction(out.cpuMax)
	if err != nil {
		return out, err
	}
	if frac < 0.24 || frac > 0.26 {
		return out, fmt.Errorf("cpu.max=%s fraction %.4f, want 0.25", out.cpuMax, frac)
	}
	swap, err := readCgroupFile(dir, "memory.swap.max")
	if os.IsNotExist(err) {
		return out, fmt.Errorf("memory.swap.max is absent")
	}
	if err != nil {
		return out, err
	}
	if swap != "0" {
		return out, fmt.Errorf("memory.swap.max=%s, want 0", swap)
	}
	out.swapMax = swap
	if out.events, err = readCgroupFile(dir, "memory.events"); err != nil {
		return out, err
	}
	if oom, kill, err := oomCounts(out.events); err != nil || oom != 0 || kill != 0 {
		return out, fmt.Errorf("memory.events already record oom: %q", out.events)
	}
	return out, nil
}

func cpuFraction(raw string) (float64, error) {
	fields := strings.Fields(raw)
	if len(fields) != 2 || fields[0] == "max" {
		return 0, fmt.Errorf("cpu.max %q", raw)
	}
	quota, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, err
	}
	period, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil || period == 0 {
		return 0, fmt.Errorf("cpu.max %q", raw)
	}
	return float64(quota) / float64(period), nil
}

func run(ctx context.Context, cgroup string, lim limits, plan []planCommand) (int, map[string]any) {
	receipt := map[string]any{
		"goarch": runtime.GOARCH, "cpuModel": cpuModel(),
		"memoryMax": lim.memoryMax, "cpuMax": lim.cpuMax, "memorySwapMax": lim.swapMax,
		"memoryEventsBefore": lim.events, "rssThresholdKiB": measureRSSThresholdKiB,
	}
	if cur, err := readCgroupFile(cgroup, "memory.current"); err == nil {
		receipt["memoryCurrentBefore"] = cur
	}
	if self, err := procKiB(os.Getpid(), "VmRSS"); err == nil {
		receipt["waiterVmRSSKiBBefore"] = self
	}
	code := 0
	gets := make([]*held, 0, 4)
	for _, c := range plan[:4] {
		h, err := startHeld(ctx, c)
		if err != nil {
			receipt["startError"] = err.Error()
			code = 1
			break
		}
		gets = append(gets, h)
	}
	defer func() {
		for _, h := range gets {
			if h.cmd.Process != nil && h.cmd.ProcessState == nil {
				_ = h.cmd.Process.Kill()
			}
			if h.stdout != nil {
				_ = h.stdout.Close()
			}
		}
	}()
	if code == 0 {
		alive, ready := waitForResidentGets(gets, measureRSSThresholdKiB, 45*time.Second)
		receipt["getsAliveAtPruneStart"] = alive
		receipt["getsResidentAtPruneStart"] = ready
		if ready != 4 {
			code = 1
		}
		if cur, err := readCgroupFile(cgroup, "memory.current"); err == nil {
			receipt["memoryCurrentAtPruneStart"] = cur
		}
		if self, err := procKiB(os.Getpid(), "VmRSS"); err == nil {
			receipt["waiterVmRSSKiBAtPruneStart"] = self
		}
		prune := plan[4]
		pcmd := exec.CommandContext(ctx, prune.Argv[0], prune.Argv[1:]...)
		pcmd.Env = os.Environ()
		pcmd.Stdin = strings.NewReader(prune.Stdin)
		pcmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
		var pruneOut bytes.Buffer
		pruneErr := &limitBuf{max: 4096}
		pcmd.Stdout = &pruneOut
		pcmd.Stderr = pruneErr
		pruneStart := time.Now()
		if err := pcmd.Start(); err != nil {
			receipt["pruneStartError"] = err.Error()
			code = 1
		} else {
			stopSample := make(chan struct{})
			peakCh := make(chan int64, 1)
			go func() {
				peakCh <- sampleUntil(ctx, cgroup, stopSample)
			}()
			waitErr := pcmd.Wait()
			close(stopSample)
			peakDuring := <-peakCh
			receipt["pruneWallMs"] = time.Since(pruneStart).Milliseconds()
			receipt["memoryCurrentMaxDuringPrune"] = strconv.FormatInt(peakDuring, 10)
			if cur, err := readCgroupFile(cgroup, "memory.current"); err == nil {
				receipt["memoryCurrentAtPruneEnd"] = cur
			}
			if peak, err := readCgroupFile(cgroup, "memory.peak"); err == nil {
				receipt["memoryPeakAtPruneEnd"] = peak
			} else {
				receipt["memoryPeakError"] = err.Error()
				code = 1
			}
			exit := 0
			var maxRSS int64
			if pcmd.ProcessState != nil {
				exit = pcmd.ProcessState.ExitCode()
				if usage, ok := pcmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
					maxRSS = usage.Maxrss
				}
			}
			if waitErr != nil && exit == 0 {
				exit = 1
			}
			stdout := pruneOut.Bytes()
			found := bytes.Contains(stdout, []byte(prune.Expect))
			child := map[string]any{
				"name": prune.Name, "exit": exit, "maxRSSKiB": maxRSS,
				"stdoutBytes": len(stdout), "expectFound": found,
			}
			if len(stdout) <= 4096 {
				child["stdout"] = string(stdout)
			}
			if pruneErr.buf.Len() > 0 {
				child["stderr"] = pruneErr.buf.String()
			}
			receipt["prune"] = child
			if exit != 0 || !found {
				code = 1
			}
		}
		for _, h := range gets {
			if err := finishHeld(h); err != nil && code == 0 {
				code = 1
			}
		}
		receipt["gets"] = heldReceipts(gets)
		for _, h := range gets {
			if h.exit != 0 || !h.expectFound {
				code = 1
			}
		}
	}
	if events, err := readCgroupFile(cgroup, "memory.events"); err == nil {
		receipt["memoryEventsAfter"] = events
		if oom, kill, err := oomCounts(events); err != nil || oom != 0 || kill != 0 {
			code = 1
		}
	} else {
		receipt["cgroupError"] = err.Error()
		code = 1
	}
	if peak, err := readCgroupFile(cgroup, "memory.peak"); err == nil {
		receipt["memoryPeakAtEnd"] = peak
	}
	return code, receipt
}

type held struct {
	spec         planCommand
	cmd          *exec.Cmd
	stdout       *os.File
	stderr       limitBuf
	started      time.Time
	rssAtPrune   int64
	stateAtPrune string
	alive        bool
	exit         int
	maxRSSKiB    int64
	vmHWMKiB     int64
	stdoutBytes  int
	expectFound  bool
	errText      string
}

func startHeld(ctx context.Context, c planCommand) (*held, error) {
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Env = os.Environ()
	cmd.Stdin = strings.NewReader("")
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = w
	h := &held{spec: c, cmd: cmd, stdout: r, stderr: limitBuf{max: 4096}, started: time.Now()}
	cmd.Stderr = &h.stderr
	if err := cmd.Start(); err != nil {
		_ = r.Close()
		_ = w.Close()
		return nil, err
	}
	_ = w.Close()
	return h, nil
}

func waitForResidentGets(gets []*held, threshold int64, wait time.Duration) (int, int) {
	deadline := time.Now().Add(wait)
	var alive, ready int
	for {
		alive, ready = 0, 0
		dead := 0
		for _, h := range gets {
			state, err := processState(h.cmd.Process.Pid)
			rss, rssErr := procKiB(h.cmd.Process.Pid, "VmRSS")
			hwm, hwmErr := procKiB(h.cmd.Process.Pid, "VmHWM")
			h.stateAtPrune = state
			if rssErr == nil {
				h.rssAtPrune = rss
			}
			if hwmErr == nil && hwm > h.vmHWMKiB {
				h.vmHWMKiB = hwm
			}
			exited := err == nil && (state == "Z" || state == "X")
			h.alive = err == nil && rssErr == nil && !exited
			if h.alive {
				alive++
				if rss >= threshold {
					ready++
				}
			}
			if exited {
				dead++
			}
		}
		if ready == len(gets) || dead > 0 || time.Now().After(deadline) {
			return alive, ready
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func finishHeld(h *held) error {
	raw, err := io.ReadAll(io.LimitReader(h.stdout, measureStdoutCap+1))
	if err != nil {
		h.errText = err.Error()
	}
	if hwm, err := procKiB(h.cmd.Process.Pid, "VmHWM"); err == nil {
		h.vmHWMKiB = hwm
	}
	waitErr := h.cmd.Wait()
	h.stdoutBytes = len(raw)
	h.expectFound = h.stdoutBytes <= measureStdoutCap && bytes.Contains(raw, []byte(h.spec.Expect))
	h.exit = 0
	if h.cmd.ProcessState != nil {
		h.exit = h.cmd.ProcessState.ExitCode()
		if usage, ok := h.cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			h.maxRSSKiB = usage.Maxrss
		}
	}
	if waitErr != nil && h.exit == 0 {
		h.exit = 1
	}
	if h.stdoutBytes > measureStdoutCap {
		h.expectFound = false
		h.errText = "stdout cap"
	}
	if h.exit != 0 || !h.expectFound {
		return fmt.Errorf("%s failed", h.spec.Name)
	}
	return nil
}

func heldReceipts(gets []*held) []map[string]any {
	out := make([]map[string]any, 0, len(gets))
	for _, h := range gets {
		item := map[string]any{
			"name": h.spec.Name, "exit": h.exit, "maxRSSKiB": h.maxRSSKiB,
			"vmHWMKiB": h.vmHWMKiB, "vmRSSKiBAtPruneStart": h.rssAtPrune,
			"stateAtPruneStart": h.stateAtPrune, "aliveAtPruneStart": h.alive,
			"stdoutBytes": h.stdoutBytes, "expectFound": h.expectFound,
		}
		if h.errText != "" {
			item["error"] = h.errText
		}
		if h.stderr.buf.Len() > 0 {
			item["stderr"] = h.stderr.buf.String()
		}
		out = append(out, item)
	}
	return out
}

func sampleUntil(ctx context.Context, cgroup string, stop <-chan struct{}) int64 {
	var maxSeen int64
	read := func() {
		cur, err := readCgroupFile(cgroup, "memory.current")
		if err != nil {
			return
		}
		v, err := strconv.ParseInt(cur, 10, 64)
		if err == nil && v > maxSeen {
			maxSeen = v
		}
	}
	read()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			read()
			return maxSeen
		case <-ctx.Done():
			read()
			return maxSeen
		case <-tick.C:
			read()
		}
	}
}

type limitBuf struct {
	buf bytes.Buffer
	max int
}

func (l *limitBuf) Write(p []byte) (int, error) {
	remain := l.max - l.buf.Len()
	if remain > 0 {
		n := len(p)
		if n > remain {
			n = remain
		}
		_, _ = l.buf.Write(p[:n])
	}
	return len(p), nil
}

func oomCounts(events string) (int, int, error) {
	oom, err := eventValue(events, "oom")
	if err != nil {
		return 0, 0, err
	}
	kill, err := eventValue(events, "oom_kill")
	return oom, kill, err
}

func eventValue(events, key string) (int, error) {
	for _, line := range strings.Split(events, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == key {
			return strconv.Atoi(fields[1])
		}
	}
	return 0, fmt.Errorf("%s missing", key)
}

func procKiB(pid int, key string) (int64, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	prefix := key + ":"
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, prefix) {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0, fmt.Errorf("%s", line)
			}
			return strconv.ParseInt(fields[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("%s missing", key)
}

func processState(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	i := bytes.LastIndexByte(raw, ')')
	if i < 0 || i+2 >= len(raw) {
		return "", fmt.Errorf("stat")
	}
	rest := strings.TrimSpace(string(raw[i+1:]))
	state, _, _ := strings.Cut(rest, " ")
	if state == "" {
		return "", fmt.Errorf("empty state")
	}
	return state, nil
}

func cpuModel() string {
	raw, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "model name") || strings.HasPrefix(line, "Model") {
			_, value, ok := strings.Cut(line, ":")
			if ok {
				value = strings.TrimSpace(value)
				if len(value) > 80 {
					value = value[:80]
				}
				return value
			}
		}
	}
	return ""
}

func readCgroupFile(dir, name string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func selfCgroup() (string, error) {
	cgroups, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	var own string
	for _, line := range strings.Split(string(cgroups), "\n") {
		if strings.HasPrefix(line, "0::") {
			own = strings.TrimPrefix(line, "0::")
		}
	}
	if !filepath.IsAbs(own) || filepath.Clean(own) != own {
		return "", fmt.Errorf("cgroup path %q", own)
	}
	mountinfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(mountinfo), "\n") {
		if !strings.Contains(line, " - cgroup2 ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		rel, err := filepath.Rel(fields[3], own)
		if err != nil || (rel != "." && !filepath.IsLocal(rel)) {
			continue
		}
		return filepath.Join(fields[4], rel), nil
	}
	return "", fmt.Errorf("cgroup v2 mount not found")
}
