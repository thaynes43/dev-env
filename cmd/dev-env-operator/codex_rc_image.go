package main

import (
	"errors"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thaynes43/dev-env/internal/controller"
)

func parseCodexRCAgent(image, session string) (*controller.CodexRCAgent, error) {
	if image == "" && session == "" {
		return nil, nil
	}
	name, uid, ok := strings.Cut(session, "/")
	if !remotePilotAgentImage.MatchString(image) || !ok || len(validation.IsDNS1123Label(name)) != 0 ||
		!regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`).MatchString(uid) {
		return nil, errors.New("codex RC image adoption requires --codex-rc-agent-image=ghcr.io/thaynes43/dev-env:2.X.Y@sha256:<64 lowercase hex> and --codex-rc-agent-session=<session-name>/<SessionUID UUID>")
	}
	return &controller.CodexRCAgent{Image: image, Name: name, UID: uid}, nil
}
