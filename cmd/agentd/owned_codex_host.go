package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const (
	ownedOperatorURL = "https://dev-env-operator.dev-env-system.svc.cluster.local:8443"
	ownedTokenFile   = "/var/run/secrets/dev-env/token"
	ownedCAFile      = "/opt/dev-env/api-ca/ca.crt"
	ownedAccessFile  = "/opt/dev-env/codex-access/access.json"
)

// Explicit pod configuration selects this route. It cannot grant authority:
// the HTTPS operator must return and admit the exact assigned binding. No flag,
// local permit file, receipt removal, fresh deadline or nil Gate enables it.
func ownedCodexHost(ctx context.Context, args []string, stderr io.Writer, getenv func(string) string) int {
	if getenv("AGENTD_OWNED_CODEX_HOST_ENABLED") != "true" {
		_, _ = fmt.Fprintln(stderr, hostexecutor.ErrDisabled)
		return exitFailure
	}
	refused := func() int {
		_, _ = fmt.Fprintln(stderr, "owned executor authority or readiness refused")
		return exitFailure
	}
	if len(args) != 0 || getenv(protocol.SessionEnv) != "" || getenv(protocol.SessionFileEnv) != "" ||
		getenv("AGENTD_API_URL") != ownedOperatorURL || getenv("AGENTD_API_TOKEN_FILE") != ownedTokenFile ||
		getenv("AGENTD_API_CA_FILE") != ownedCAFile || getenv("AGENTD_CODEX_ACCESS_FILE") != ownedAccessFile {
		return refused()
	}
	s, err := agentd.LoadSettings(getenv)
	if err != nil || s.CodexHome != filepath.Join(s.Home, ".codex") {
		return refused()
	}
	epoch, err := strconv.ParseUint(getenv("AGENTD_OWNED_TASK_EPOCH"), 10, 64)
	if err != nil {
		return refused()
	}
	deadline, err := time.Parse(time.RFC3339Nano, getenv("AGENTD_OWNED_TASK_DEADLINE"))
	if err != nil {
		return refused()
	}
	b := hostexecutor.Binding{TaskUID: getenv("AGENTD_OWNED_TASK_UID"), Epoch: epoch, Deadline: deadline, HostID: getenv("AGENTD_OWNED_HOST_ID"), PodUID: getenv("DEV_ENV_POD_UID")}
	gate, err := newOwnedBudgetGate(ownedOperatorURL, ownedTokenFile, ownedCAFile, s)
	if err != nil {
		return refused()
	}
	bindingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	authoritative, err := gate.binding(bindingCtx, b)
	cancel()
	if err != nil {
		return refused()
	}
	helper, err := os.Executable()
	if err != nil {
		return refused()
	}
	c := hostexecutor.Config{Binding: authoritative, HelperBinary: helper, NativeBinary: "/usr/local/bin/codex", Home: s.Home, SocketPath: filepath.Join(s.CodexHome, "owned-control.sock"), ReceiptPath: filepath.Join(s.StateDir, "owned-host.json"), StopTimeout: 3 * time.Second, ReadyVersion: hostexecutor.PinnedNativeVersion}
	if _, err = hostexecutor.Run(ctx, c, gate); err != nil {
		return refused()
	}
	return exitOK
}

func ownedCodexHostHelper(stderr io.Writer) int {
	if err := hostexecutor.Helper(); err != nil {
		_, _ = fmt.Fprintln(stderr, "owned executor helper failed")
		return exitFailure
	}
	return exitOK
}
