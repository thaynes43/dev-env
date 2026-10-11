package agentrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

var canaryName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// canary has no repository/create fallback. The operator's fixed record owns
// the task; this client submits only its ID, once, without waiting or polling.
func (a *app) canary(ctx context.Context, args []string) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		return a.help([]string{"canary"})
	}
	if len(args) == 0 || args[0] != "create" {
		return usageError("canary takes the create subcommand; agent-run help canary lists its flags")
	}
	cmd := newCommand("canary", outputJSON)
	cmd.c.output = outputJSON
	var id string
	var once, asJSON bool
	cmd.fs.StringVar(&id, "id", "", "")
	cmd.fs.BoolVar(&once, "once", false, "")
	cmd.fs.BoolVar(&asJSON, "json", false, "")
	idFlags := 0
	for _, arg := range args[1:] {
		if arg == "--id" || arg == "-id" || strings.HasPrefix(arg, "--id=") || strings.HasPrefix(arg, "-id=") {
			idFlags++
		}
	}
	pos, err := cmd.parse(a, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 0 || !once || idFlags != 1 || !canaryName.MatchString(id) {
		return usageError("canary create requires one valid --id and --once, with no task options or positional arguments")
	}
	if asJSON {
		cmd.c.output = outputJSON
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *c
	one.http = &hc
	// CreateSessionRequest has other non-omitempty fields. A narrow struct is
	// necessary to satisfy the server's exact one-field admission boundary.
	request := struct {
		CanaryID string `json:"canaryID"`
	}{CanaryID: id}
	var raw json.RawMessage
	status, err := a.callOnce(ctx, &one, http.MethodPost, apiv1.SessionsPath, request, &raw)
	if err != nil {
		return err
	}
	if (status != http.StatusCreated && status != http.StatusOK) || !utf8.Valid(raw) {
		return fail(ExitFailed, "canary original receipt is unconfirmed; do not repeat the POST")
	}
	var session apiv1.Session
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&session) != nil || d.Decode(new(any)) != io.EOF || !confirmedCanaryReceipt(id, session) {
		return fail(ExitFailed, "canary original receipt is unconfirmed; do not repeat the POST")
	}
	disposition := "ExistingOriginalSession"
	if status == http.StatusCreated {
		disposition = "CreatedOriginalSession"
	}
	// Preserve raw JSON presence/nulls. Neither a new nor an existing Session
	// receipt asserts that a provider invocation started or finished.
	return a.printJSON(struct {
		CanaryID    string          `json:"canaryID"`
		HTTPStatus  int             `json:"httpStatus"`
		Disposition string          `json:"disposition"`
		Session     json.RawMessage `json:"session"`
	}{id, status, disposition, raw})
}

func confirmedCanaryReceipt(id string, session apiv1.Session) bool {
	parent := strings.Split(session.Parent, "/")
	if len(parent) != 2 || !canaryName.MatchString(parent[0]) || !canaryName.MatchString(parent[1]) {
		return false
	}
	digest := sha256.Sum256([]byte(session.Parent + "\x00" + id))
	expectedName := "private-canary-" + hex.EncodeToString(digest[:])[:32]
	return session.Name == expectedName && session.UID != "" && len(session.UID) <= 128 && !strings.ContainsAny(session.UID, "\x00\r\n") &&
		session.Mode == "task" && (session.Agent == "claude" || session.Agent == "codex") && !session.CreatedAt.IsZero() &&
		canaryName.MatchString(session.Repo) && session.Base != "" && session.Model != "" && session.Size == "S" &&
		canaryName.MatchString(session.Profile) && session.Profile != "full" && session.IdempotencyKey != ""
}
