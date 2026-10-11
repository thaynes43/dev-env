package agentd

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"syscall"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// LoadSession reads the session agentd runs for from its environment: the
// file named by AGENTD_SESSION_FILE when that is set, else the JSON document in
// AGENTD_SESSION (D-40).
func LoadSession(getenv func(string) string) (protocol.Session, error) {
	if path := getenv(protocol.SessionFileEnv); path != "" {
		if getenv("AGENTD_ENABLE_RETAINED_COMMON_RULES") == "true" && path != protocol.SessionInputPath {
			return protocol.Session{}, errors.New("retained session requires its fixed private input projection")
		}
		bound := protocol.MaxSessionBytes
		if getenv("AGENTD_ENABLE_RETAINED_COMMON_RULES") == "true" {
			bound = protocol.MaxSessionFileBytes
		}
		data, err := readSessionFile(path, bound)
		if err != nil {
			return protocol.Session{}, err
		}
		return protocol.ParseSessionFile(data)
	}
	doc := getenv(protocol.SessionEnv)
	if doc == "" {
		return protocol.Session{}, errors.New(protocol.SessionEnv + " is not set: the operator passes the session there")
	}
	return protocol.ParseSession([]byte(doc))
}

// Kubernetes DownwardAPI uses projected symlinks. Open their resolved target
// nonblocking, prove regular type/size before allocating, and reread at boot.
func readSessionFile(path string, bound int) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("session input is unavailable")
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > int64(bound) {
		return nil, errors.New("session input type or size is unconfirmed")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(bound)+1))
	if err != nil || len(data) > bound {
		return nil, errors.New("session input exceeds its bound or is unreadable")
	}
	return data, nil
}

// LogSteps writes one log line per boot step.
func LogSteps(log *slog.Logger, steps []Step) {
	for _, st := range steps {
		attrs := []any{"step", st.Name, "state", st.State}
		for i, n := range st.Notes {
			attrs = append(attrs, fmt.Sprintf("note%d", i+1), n)
		}
		switch st.State {
		case StepFail:
			log.Error("boot step", attrs...)
		case StepWarn:
			log.Warn("boot step", attrs...)
		default:
			log.Info("boot step", attrs...)
		}
	}
}
