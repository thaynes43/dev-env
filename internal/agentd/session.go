package agentd

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// LoadSession reads the session agentd runs for from its environment: the
// file named by AGENTD_SESSION_FILE when that is set, else the JSON document in
// AGENTD_SESSION (D-40).
func LoadSession(getenv func(string) string) (protocol.Session, error) {
	if path := getenv(protocol.SessionFileEnv); path != "" {
		f, err := os.Open(path)
		if err != nil {
			return protocol.Session{}, fmt.Errorf("%s: %w", protocol.SessionFileEnv, err)
		}
		defer func() { _ = f.Close() }()
		data, err := io.ReadAll(io.LimitReader(f, protocol.MaxSessionBytes+1))
		if err != nil {
			return protocol.Session{}, fmt.Errorf("%s: %w", protocol.SessionFileEnv, err)
		}
		return protocol.ParseSession(data)
	}
	doc := getenv(protocol.SessionEnv)
	if doc == "" {
		return protocol.Session{}, errors.New(protocol.SessionEnv + " is not set: the operator passes the session there")
	}
	return protocol.ParseSession([]byte(doc))
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
