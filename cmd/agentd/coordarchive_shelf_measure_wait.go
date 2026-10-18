//go:build ignore

// Command coord-archive shelf-limit waiter runs one argv and prints that
// process's peak RSS. It is not an agentd command. Build it with
// go build -o coord-get-measure-wait ./cmd/agentd/coordarchive_shelf_measure_wait.go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] != "--" {
		fmt.Fprintln(os.Stderr, "usage: coord-get-measure-wait -- command args...")
		os.Exit(2)
	}
	args = args[1:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "missing command")
		os.Exit(2)
	}
	max, events, err := cgroupMemory()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cgroup: %v\n", err)
		os.Exit(2)
	}
	if max != strconv.Itoa(256<<20) {
		fmt.Fprintf(os.Stderr, "memory.max=%s, want 268435456\n", max)
		os.Exit(2)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = os.Environ()
	cmd.Stderr = os.Stderr
	outPath := os.Getenv("DEV_ENV_COORD_GET_OUT")
	var out *os.File
	if outPath == "" {
		cmd.Stdout = os.Stdout
	} else {
		f, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stdout: %v\n", err)
			os.Exit(2)
		}
		out = f
		cmd.Stdout = f
	}
	runErr := cmd.Run()
	if out != nil {
		_ = out.Close()
	}
	code := 0
	var maxRSS int64
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
		if usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			maxRSS = usage.Maxrss
		}
	}
	if runErr != nil && code == 0 {
		code = 1
	}
	afterMax, afterEvents, afterErr := cgroupMemory()
	receipt := map[string]any{
		"memoryMax": max, "memoryEventsBefore": events,
		"memoryMaxAfter": afterMax, "memoryEventsAfter": afterEvents,
		"maxRSSKiB": maxRSS, "exit": code,
	}
	if afterErr != nil {
		receipt["cgroupError"] = afterErr.Error()
	}
	if sha := os.Getenv("DEV_ENV_COORD_EXPECT_SHA"); sha != "" && outPath != "" {
		raw, err := os.ReadFile(outPath)
		receipt["stdoutBytes"] = len(raw)
		if err != nil {
			receipt["stdoutError"] = err.Error()
			code = 1
		} else if !bytesContains(raw, sha) {
			receipt["shaFound"] = false
			code = 1
		} else {
			receipt["shaFound"] = true
		}
	}
	enc, err := json.Marshal(receipt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "receipt: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "measure %s\n", enc)
	os.Exit(code)
}

func bytesContains(b []byte, s string) bool {
	return strings.Contains(string(b), s)
}

func cgroupMemory() (string, string, error) {
	cgroup, err := selfCgroup()
	if err != nil {
		return "", "", err
	}
	maxRaw, err := os.ReadFile(filepath.Join(cgroup, "memory.max"))
	if err != nil {
		return "", "", err
	}
	eventsRaw, err := os.ReadFile(filepath.Join(cgroup, "memory.events"))
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(string(maxRaw)), strings.TrimSpace(string(eventsRaw)), nil
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
